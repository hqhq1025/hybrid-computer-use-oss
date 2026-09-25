using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json.Nodes;

namespace ComputerUse.Windows.Runtime;

internal static partial class NativeDesktop
{
    private const int SmXVirtualScreen = 76;
    private const int SmYVirtualScreen = 77;
    private const int SmCxVirtualScreen = 78;
    private const int SmCyVirtualScreen = 79;
    private const int GwlStyle = -16;
    private const int GwlExStyle = -20;
    private const uint GwOwner = 4;
    private const uint WmClose = 0x0010;
    private const int SwRestore = 9;
    private const int ClipboardPasteSettleMilliseconds = 500;
    private const int ClipboardRetryCount = 10;
    private const int ClipboardRetryMilliseconds = 20;
    private const int CaptureLayoutRetryCount = 3;
    private const int CaptureLayoutRetryMilliseconds = 25;
    private const uint RdwInvalidate = 0x0001;
    private const uint RdwErase = 0x0004;
    private const uint RdwAllChildren = 0x0080;
    private const uint RdwUpdateNow = 0x0100;
    private const uint RdwFrame = 0x0400;
    private const uint WindowRenderCommitFlags =
        RdwInvalidate |
        RdwErase |
        RdwAllChildren |
        RdwUpdateNow |
        RdwFrame;
    private const int PublishedLayoutHistoryLimit = 32;
    private const int WindowActivationDirectTimeoutMilliseconds = 250;
    private const int WindowActivationPollMilliseconds = 25;
    private const int WindowActivationTimeoutMilliseconds = 750;
    private static readonly object PublishedLayoutLock = new();
    private static readonly Dictionary<
        string,
        WindowsDesktopLayoutSnapshot> PublishedLayouts = new(
            StringComparer.Ordinal);
    private static readonly Queue<string> PublishedLayoutOrder = new();

    internal sealed record ScreenFrame(
        int OriginX,
        int OriginY,
        int Width,
        int Height,
        int NativeWidth,
        int NativeHeight,
        string FrameId,
        string PngBase64)
    {
        internal byte[] Pixels { get; init; } = Array.Empty<byte>();
        internal string SampledAt { get; } = DateTimeOffset.UtcNow.ToString("O");
    }

    internal sealed record WindowRecord(
        nint Handle,
        nint OwnerHandle,
        int ProcessId,
        string Title,
        string ClassName,
        Rectangle Bounds,
        bool Active,
        uint Style,
        uint ExtendedStyle,
        uint ThreadId);

    internal static nint RootWindow(nint handle) => GetAncestor(handle, 2);
    internal static nint OwnerWindow(nint handle) => GetWindow(handle, GwOwner);
    internal static nint DesktopWindow() => GetDesktopWindow();
    internal static nint WindowAtPoint(Point point) =>
        WindowFromPoint(new NativePoint { X = point.X, Y = point.Y });

    [StructLayout(LayoutKind.Sequential)]
    private struct NativePoint
    {
        internal int X;
        internal int Y;
    }

    [DllImport("user32.dll")]
    private static extern nint WindowFromPoint(NativePoint point);

    [DllImport("user32.dll")]
    private static extern nint GetDesktopWindow();

    [DllImport("user32.dll")]
    private static extern nint GetAncestor(nint handle, uint flags);

    internal static ScreenFrame Capture()
    {
        return Capture(DesktopBounds());
    }

    internal static ScreenFrame Capture(Rectangle requestedBounds)
    {
        if (!TryCapture(requestedBounds, out var frame) || frame is null)
        {
            throw new InvalidOperationException(
                "requested_capture_bounds_unavailable");
        }
        return frame;
    }

    internal static ScreenFrame CaptureForSettling()
    {
        if (!TryCapture(DesktopBounds(), out var frame, includePixels: true) || frame is null)
        {
            throw new InvalidOperationException("requested_capture_bounds_unavailable");
        }
        return frame;
    }

    internal static bool PixelsIdentical(ScreenFrame first, ScreenFrame second)
    {
        return first.OriginX == second.OriginX &&
            first.OriginY == second.OriginY &&
            first.NativeWidth == second.NativeWidth &&
            first.NativeHeight == second.NativeHeight &&
            first.FrameId == second.FrameId &&
            first.Pixels.Length > 0 &&
            first.Pixels.AsSpan().SequenceEqual(second.Pixels);
    }

    internal static bool TryCapture(
        Rectangle requestedBounds,
        out ScreenFrame? frame,
        bool includePixels = false)
    {
        if (!WindowsCaptureBounds.TryResolve(
                DesktopBounds(),
                requestedBounds,
                out var bounds))
        {
            frame = null;
            return false;
        }
        for (var attempt = 1; attempt <= CaptureLayoutRetryCount; attempt++)
        {
            var layoutBefore = CurrentLayout();
            var candidate = CaptureFrame(bounds, layoutBefore.FrameId, includePixels);
            var layoutAfter = CurrentLayout();
            if (string.Equals(
                    layoutBefore.FrameId,
                    layoutAfter.FrameId,
                    StringComparison.Ordinal))
            {
                RememberPublishedLayout(layoutBefore);
                frame = candidate;
                return true;
            }
            if (attempt < CaptureLayoutRetryCount)
            {
                Thread.Sleep(CaptureLayoutRetryMilliseconds);
            }
        }
        throw new ProviderFailureException(
            "desktop_layout_unstable",
            "observation",
            "Desktop layout changed during every bounded screenshot capture attempt.");
    }

    private static ScreenFrame CaptureFrame(
        Rectangle bounds,
        string frameId,
        bool includePixels)
    {
        using var nativeBitmap = new Bitmap(
            bounds.Width,
            bounds.Height,
            PixelFormat.Format32bppArgb);
        using (var graphics = Graphics.FromImage(nativeBitmap))
        {
            graphics.CopyFromScreen(
                bounds.X,
                bounds.Y,
                0,
                0,
                bounds.Size);
        }
        return EncodeFrame(nativeBitmap, bounds, frameId, includePixels);
    }

    internal static ScreenFrame CaptureCommittedWindow(nint handle)
    {
        return CaptureCommitted(
            handle,
            () => CaptureStableWindowBounds(handle));
    }

    internal static ScreenFrame CaptureCommittedScreen(nint handle)
    {
        return CaptureCommitted(
            handle,
            () =>
            {
                EnsureCurrentWindow(handle);
                var frame = Capture();
                EnsureCurrentWindow(handle);
                return frame;
            });
    }

    private static ScreenFrame CaptureCommitted(
        nint handle,
        Func<ScreenFrame> capture)
    {
        if (handle == 0)
        {
            throw new ProviderFailureException(
                "window_render_capture_failed",
                "observation",
                "The selected native window handle is unavailable.");
        }

        try
        {
            return WindowRenderCommit.Capture(
                () =>
                    IsWindow(handle) &&
                    RedrawWindow(
                        handle,
                        0,
                        0,
                        WindowRenderCommitFlags),
                DwmFlush,
                capture);
        }
        catch (InvalidOperationException error)
        {
            throw new ProviderFailureException(
                "window_render_capture_failed",
                "observation",
                $"The selected native window did not reach a committed " +
                $"render state: {error.Message}");
        }
    }

    private static ScreenFrame CaptureStableWindowBounds(nint handle)
    {
        EnsureCurrentWindow(handle);
        _ = GetWindowRect(handle, out var beforeRect);
        var bounds = Rectangle.FromLTRB(
            beforeRect.Left,
            beforeRect.Top,
            beforeRect.Right,
            beforeRect.Bottom);
        if (bounds.Width <= 0 || bounds.Height <= 0)
        {
            throw new InvalidOperationException(
                "window_has_no_drawable_bounds");
        }
        if (!TryCapture(bounds, out var frame) || frame is null)
        {
            throw new InvalidOperationException(
                "window_outside_virtual_desktop");
        }
        if (!IsWindow(handle) ||
            !GetWindowRect(handle, out var afterRect))
        {
            throw new ProviderFailureException(
                "window_target_stale",
                "observation",
                "The selected native window no longer exists.");
        }
        if (beforeRect.Left != afterRect.Left ||
            beforeRect.Top != afterRect.Top ||
            beforeRect.Right != afterRect.Right ||
            beforeRect.Bottom != afterRect.Bottom)
        {
            throw new InvalidOperationException(
                "window_bounds_changed_during_capture");
        }
        return frame;
    }

    private static void EnsureCurrentWindow(nint handle)
    {
        if (!IsWindow(handle) ||
            !GetWindowRect(handle, out _))
        {
            throw new ProviderFailureException(
                "window_target_stale",
                "observation",
                "The selected native window no longer exists.");
        }
    }

    private static ScreenFrame EncodeFrame(
        Bitmap nativeBitmap,
        Rectangle bounds,
        string frameId,
        bool includePixels)
    {
        var deliveredSize = WindowsDesktopImageGeometry.DeliveredSize(
            bounds.Width,
            bounds.Height);
        using var stream = new MemoryStream();
        if (deliveredSize.Width == bounds.Width &&
            deliveredSize.Height == bounds.Height)
        {
            nativeBitmap.Save(stream, ImageFormat.Png);
        }
        else
        {
            using var deliveredBitmap = new Bitmap(
                deliveredSize.Width,
                deliveredSize.Height,
                PixelFormat.Format32bppArgb);
            using (var graphics = Graphics.FromImage(deliveredBitmap))
            {
                graphics.CompositingMode = CompositingMode.SourceCopy;
                graphics.CompositingQuality = CompositingQuality.HighQuality;
                graphics.InterpolationMode = InterpolationMode.HighQualityBicubic;
                graphics.PixelOffsetMode = PixelOffsetMode.HighQuality;
                graphics.SmoothingMode = SmoothingMode.HighQuality;
                graphics.DrawImage(
                    nativeBitmap,
                    new Rectangle(Point.Empty, deliveredSize),
                    new Rectangle(Point.Empty, bounds.Size),
                    GraphicsUnit.Pixel);
            }
            deliveredBitmap.Save(stream, ImageFormat.Png);
        }
        return new ScreenFrame(
            bounds.X,
            bounds.Y,
            deliveredSize.Width,
            deliveredSize.Height,
            bounds.Width,
            bounds.Height,
            frameId,
            Convert.ToBase64String(stream.ToArray()))
        {
            Pixels = includePixels ? PixelBytes(nativeBitmap) : Array.Empty<byte>(),
        };
    }

    private static byte[] PixelBytes(Bitmap bitmap)
    {
        var data = bitmap.LockBits(
            new Rectangle(Point.Empty, bitmap.Size),
            ImageLockMode.ReadOnly,
            PixelFormat.Format32bppArgb);
        try
        {
            var rowBytes = checked(bitmap.Width * 4);
            var pixels = new byte[checked(rowBytes * bitmap.Height)];
            for (var row = 0; row < bitmap.Height; row++)
            {
                Marshal.Copy(data.Scan0 + row * data.Stride, pixels, row * rowBytes, rowBytes);
            }
            return pixels;
        }
        finally
        {
            bitmap.UnlockBits(data);
        }
    }

    internal static string LayoutId()
    {
        return CurrentLayout().FrameId;
    }

    internal static WindowsDesktopLayoutSnapshot CurrentLayout()
    {
        var desktopBounds = DesktopBounds();
        var foreground = GetForegroundWindow();
        var windows = new List<WindowsDesktopLayoutWindow>();
        var zOrder = 0;
        foreach (var window in ListWindows())
        {
            if (!WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
                    window.ClassName,
                    window.OwnerHandle,
                    window.Style,
                    window.ExtendedStyle) ||
                !desktopBounds.IntersectsWith(window.Bounds))
            {
                continue;
            }
            windows.Add(new WindowsDesktopLayoutWindow(
                zOrder,
                window.Handle.ToInt64(),
                window.OwnerHandle.ToInt64(),
                window.ProcessId,
                window.ClassName,
                window.Bounds.X,
                window.Bounds.Y,
                window.Bounds.Width,
                window.Bounds.Height,
                window.Style,
                window.ExtendedStyle));
            zOrder++;
        }
        return WindowsDesktopLayoutDiagnostics.Create(
            desktopBounds.X,
            desktopBounds.Y,
            desktopBounds.Width,
            desktopBounds.Height,
            foreground.ToInt64(),
            windows);
    }

    internal static void LogStaleLayout(
        string requestedFrameId,
        WindowsDesktopLayoutSnapshot current)
    {
        WindowsDesktopLayoutSnapshot? requested;
        lock (PublishedLayoutLock)
        {
            PublishedLayouts.TryGetValue(requestedFrameId, out requested);
        }
        Console.Error.WriteLine(
            WindowsDesktopLayoutDiagnostics.Difference(
                requestedFrameId,
                requested,
                current).ToJsonString());
    }

    private static void RememberPublishedLayout(
        WindowsDesktopLayoutSnapshot layout)
    {
        lock (PublishedLayoutLock)
        {
            if (PublishedLayouts.ContainsKey(layout.FrameId))
            {
                return;
            }
            PublishedLayouts[layout.FrameId] = layout;
            PublishedLayoutOrder.Enqueue(layout.FrameId);
            while (PublishedLayoutOrder.Count > PublishedLayoutHistoryLimit)
            {
                var expired = PublishedLayoutOrder.Dequeue();
                PublishedLayouts.Remove(expired);
            }
        }
    }

    internal static IReadOnlyList<WindowRecord> ListWindows(int? processId = null)
    {
        var windows = new List<WindowRecord>();
        var foreground = GetForegroundWindow();
        EnumWindows(
            (handle, parameter) =>
            {
                if (!IsWindowVisible(handle) || !GetWindowRect(handle, out var rect))
                {
                    return true;
                }
                _ = parameter;
                var threadId = GetWindowThreadProcessId(handle, out var pid);
                if (pid == 0 || (processId.HasValue && pid != processId.Value))
                {
                    return true;
                }
                var bounds = Rectangle.FromLTRB(
                    rect.Left,
                    rect.Top,
                    rect.Right,
                    rect.Bottom);
                if (bounds.Width <= 0 || bounds.Height <= 0)
                {
                    return true;
                }
                var length = GetWindowTextLength(handle);
                var title = new StringBuilder(Math.Max(1, length + 1));
                _ = GetWindowText(handle, title, title.Capacity);
                var className = new StringBuilder(256);
                _ = GetClassName(handle, className, className.Capacity);
                windows.Add(new WindowRecord(
                    handle,
                    GetWindow(handle, GwOwner),
                    unchecked((int)pid),
                    title.ToString(),
                    className.ToString(),
                    bounds,
                    handle == foreground,
                    unchecked((uint)GetWindowLongPtr(handle, GwlStyle).ToInt64()),
                    unchecked((uint)GetWindowLongPtr(
                        handle,
                        GwlExStyle).ToInt64()),
                    threadId));
                return true;
            },
            0);
        return windows;
    }

    internal static bool TryGetWindowProcessId(
        nint handle,
        out int processId)
    {
        processId = 0;
        if (handle == 0 || !IsWindow(handle))
        {
            return false;
        }
        _ = GetWindowThreadProcessId(handle, out var nativeProcessId);
        if (nativeProcessId == 0 || nativeProcessId > int.MaxValue)
        {
            return false;
        }
        processId = (int)nativeProcessId;
        return true;
    }

    internal static void ActivateWindow(nint handle)
    {
        if (handle == 0 || !IsWindow(handle))
        {
            throw new ProviderFailureException(
                "window_target_stale",
                "precondition",
                "The exact native window no longer exists.");
        }

        if (WindowsForegroundActivationPolicy.PrepareAndCheckForeground(
                handle,
                () => IsIconic(handle),
                () => { _ = ShowWindow(handle, SwRestore); },
                GetForegroundWindow))
        {
            return;
        }

        var foregroundBefore = GetForegroundWindow();
        var directAccepted = SetForegroundWindow(handle);
        if (WaitForForeground(
                handle,
                WindowActivationDirectTimeoutMilliseconds))
        {
            return;
        }

        var currentThread = GetCurrentThreadId();
        var foregroundThread = GetWindowThreadProcessId(
            foregroundBefore,
            out var foregroundProcess);
        var targetThread = GetWindowThreadProcessId(
            handle,
            out var targetProcess);
        var attachedForeground = false;
        var attachedTarget = false;
        var bringAccepted = false;
        var retryAccepted = false;
        try
        {
            if (WindowsForegroundActivationPolicy.ShouldAttach(
                    currentThread,
                    foregroundThread))
            {
                attachedForeground = AttachThreadInput(
                    currentThread,
                    foregroundThread,
                    true);
            }
            if (WindowsForegroundActivationPolicy.ShouldAttach(
                    currentThread,
                    targetThread))
            {
                attachedTarget = AttachThreadInput(
                    currentThread,
                    targetThread,
                    true);
            }

            bringAccepted = BringWindowToTop(handle);
            retryAccepted = SetForegroundWindow(handle);
            if (WaitForForeground(
                    handle,
                    WindowActivationTimeoutMilliseconds))
            {
                return;
            }
        }
        finally
        {
            if (attachedTarget)
            {
                _ = AttachThreadInput(currentThread, targetThread, false);
            }
            if (attachedForeground)
            {
                _ = AttachThreadInput(currentThread, foregroundThread, false);
            }
        }

        var foregroundAfter = GetForegroundWindow();
        var afterThread = GetWindowThreadProcessId(
            foregroundAfter,
            out var afterProcess);
        Console.Error.WriteLine(
            "window_activation_failed " +
            $"target=0x{handle.ToInt64():x} target_pid={targetProcess} " +
            $"target_tid={targetThread} foreground_before=" +
            $"0x{foregroundBefore.ToInt64():x} foreground_before_pid=" +
            $"{foregroundProcess} foreground_before_tid={foregroundThread} " +
            $"foreground_after=0x{foregroundAfter.ToInt64():x} " +
            $"foreground_after_pid={afterProcess} " +
            $"foreground_after_tid={afterThread} current_tid={currentThread} " +
            $"direct_accepted={directAccepted} " +
            $"attached_foreground={attachedForeground} " +
            $"attached_target={attachedTarget} " +
            $"bring_accepted={bringAccepted} retry_accepted={retryAccepted}");
        throw new ProviderFailureException(
            "window_activation_failed",
            "emission",
            "The exact native window did not become the foreground window; no input was emitted.");
    }

    private static bool WaitForForeground(nint handle, int timeoutMilliseconds)
    {
        var deadline = Environment.TickCount64 + timeoutMilliseconds;
        do
        {
            if (WindowsForegroundActivationPolicy.IsVerified(
                    handle,
                    GetForegroundWindow()))
            {
                return true;
            }
            Thread.Sleep(WindowActivationPollMilliseconds);
        }
        while (Environment.TickCount64 < deadline);
        return WindowsForegroundActivationPolicy.IsVerified(
            handle,
            GetForegroundWindow());
    }

    internal static void CloseWindow(nint handle)
    {
        if (!PostMessage(handle, WmClose, 0, 0))
        {
            throw new ProviderFailureException(
                "window_close_failed",
                "emission",
                "The native close command could not be posted.");
        }
    }

    internal static void TypeText(string text)
    {
        if (text.Length == 0)
        {
            return;
        }

        ClipboardSnapshot? previousClipboard = null;
        var inputEmitted = false;
        Exception? operationError = null;
        try
        {
            previousClipboard = CaptureClipboard();
            ClipboardRetry(
                () => System.Windows.Forms.Clipboard.SetText(
                    text,
                    System.Windows.Forms.TextDataFormat.UnicodeText));
            PressKeys(new[] { "CTRL", "V" });
            inputEmitted = true;
            Thread.Sleep(ClipboardPasteSettleMilliseconds);
        }
        catch (ProviderFailureException error)
        {
            inputEmitted = error.InputEventsEmitted;
            operationError = error;
        }
        catch (Exception error)
        {
            operationError = error;
        }

        Exception? restoreError = null;
        if (previousClipboard is not null)
        {
            try
            {
                previousClipboard.Restore();
            }
            catch (Exception error)
            {
                restoreError = error;
            }
        }

        previousClipboard?.Dispose();
        if (restoreError is not null)
        {
            throw new ProviderFailureException(
                "clipboard_restore_failed",
                "postcondition",
                $"Text input completed but the prior clipboard could not be " +
                $"restored: {restoreError.Message}",
                inputEmitted,
                true);
        }
        if (operationError is ProviderFailureException providerFailure)
        {
            throw providerFailure;
        }
        if (operationError is not null)
        {
            throw new ProviderFailureException(
                "clipboard_type_failed",
                inputEmitted ? "postcondition" : "emission",
                $"Clipboard-backed text input failed: {operationError.Message}",
                inputEmitted,
                inputEmitted);
        }
    }

    private static ClipboardSnapshot CaptureClipboard()
    {
        var source = ClipboardRetry(
            System.Windows.Forms.Clipboard.GetDataObject);
        if (source is null)
        {
            return new ClipboardSnapshot(null, Array.Empty<IDisposable>());
        }

        var data = new System.Windows.Forms.DataObject();
        var owned = new List<IDisposable>();
        foreach (var format in source.GetFormats(false).Distinct())
        {
            var value = ClipboardRetry(
                () => source.GetData(format, false));
            if (value is null)
            {
                continue;
            }
            data.SetData(
                format,
                false,
                CloneClipboardValue(format, value, owned));
        }
        return new ClipboardSnapshot(data, owned);
    }

    private static object CloneClipboardValue(
        string format,
        object value,
        List<IDisposable> owned)
    {
        switch (value)
        {
            case string:
            case bool:
            case byte:
            case sbyte:
            case short:
            case ushort:
            case int:
            case uint:
            case long:
            case ulong:
            case float:
            case double:
            case decimal:
            case char:
            case DateTime:
            case Guid:
                return value;
            case byte[] bytes:
                return bytes.ToArray();
            case string[] strings:
                return strings.ToArray();
            case System.Collections.Specialized.StringCollection strings:
                var copiedStrings =
                    new System.Collections.Specialized.StringCollection();
                copiedStrings.AddRange(strings.Cast<string>().ToArray());
                return copiedStrings;
            case Stream stream:
                var copiedStream = new MemoryStream();
                var previousPosition = stream.CanSeek ? stream.Position : 0;
                if (stream.CanSeek)
                {
                    stream.Position = 0;
                }
                stream.CopyTo(copiedStream);
                if (stream.CanSeek)
                {
                    stream.Position = previousPosition;
                }
                copiedStream.Position = 0;
                owned.Add(copiedStream);
                return copiedStream;
            case Image image:
                var copiedImage = (Image)image.Clone();
                owned.Add(copiedImage);
                return copiedImage;
            case ICloneable cloneable:
                return cloneable.Clone() ??
                    throw new InvalidOperationException(
                        $"Clipboard format {format} returned a null clone.");
            default:
                throw new InvalidOperationException(
                    $"Clipboard format {format} uses unsupported data type " +
                    $"{value.GetType().FullName}; text input was not emitted.");
        }
    }

    private static T ClipboardRetry<T>(Func<T> operation)
    {
        for (var attempt = 1; ; attempt++)
        {
            try
            {
                return operation();
            }
            catch (ExternalException) when (attempt < ClipboardRetryCount)
            {
                Thread.Sleep(ClipboardRetryMilliseconds);
            }
        }
    }

    private static void ClipboardRetry(Action operation)
    {
        ClipboardRetry(
            () =>
            {
                operation();
                return true;
            });
    }

    private sealed class ClipboardSnapshot : IDisposable
    {
        private readonly System.Windows.Forms.DataObject? _data;
        private readonly IReadOnlyList<IDisposable> _owned;

        internal ClipboardSnapshot(
            System.Windows.Forms.DataObject? data,
            IReadOnlyList<IDisposable> owned)
        {
            _data = data;
            _owned = owned;
        }

        internal void Restore()
        {
            if (_data is null)
            {
                ClipboardRetry(System.Windows.Forms.Clipboard.Clear);
                return;
            }
            ClipboardRetry(
                () => System.Windows.Forms.Clipboard.SetDataObject(
                    _data,
                    true));
        }

        public void Dispose()
        {
            foreach (var value in _owned)
            {
                value.Dispose();
            }
        }
    }

    internal static JsonObject SnapshotJson(ScreenFrame frame)
    {
        return new JsonObject
        {
            ["app"] = new JsonObject { ["name"] = string.Empty, ["pid"] = 0 },
            ["sampledAt"] = frame.SampledAt,
            ["screenshotPngBase64"] = frame.PngBase64,
            ["screenshotMimeType"] = "image/png",
            ["screenshotEncoding"] = "png",
            ["coordinateSpace"] = "screen_image_px",
            ["frameId"] = frame.FrameId,
            ["width"] = frame.Width,
            ["height"] = frame.Height,
            ["desktopLayout"] = new JsonObject
            {
                ["origin_x"] = frame.OriginX,
                ["origin_y"] = frame.OriginY,
            },
        };
    }

    internal static Rectangle DesktopBounds()
    {
        return new Rectangle(
            GetSystemMetrics(SmXVirtualScreen),
            GetSystemMetrics(SmYVirtualScreen),
            GetSystemMetrics(SmCxVirtualScreen),
            GetSystemMetrics(SmCyVirtualScreen));
    }

    private delegate bool EnumWindowsProc(nint handle, nint parameter);

    [StructLayout(LayoutKind.Sequential)]
    private struct Rect
    {
        internal int Left;
        internal int Top;
        internal int Right;
        internal int Bottom;
    }

    [DllImport("user32.dll")]
    private static extern int GetSystemMetrics(int index);

    [DllImport("user32.dll")]
    private static extern bool EnumWindows(EnumWindowsProc callback, nint parameter);

    [DllImport("user32.dll")]
    private static extern bool IsWindowVisible(nint handle);

    [DllImport("user32.dll")]
    private static extern bool IsWindow(nint handle);

    [DllImport("user32.dll")]
    private static extern bool GetWindowRect(nint handle, out Rect rect);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(nint handle, out uint processId);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetWindowText(nint handle, StringBuilder text, int count);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(
        nint handle,
        StringBuilder className,
        int count);

    [DllImport("user32.dll")]
    private static extern int GetWindowTextLength(nint handle);

    [DllImport("user32.dll")]
    private static extern nint GetForegroundWindow();

    [DllImport("user32.dll")]
    private static extern nint GetWindow(nint handle, uint command);

    [DllImport("user32.dll", EntryPoint = "GetWindowLongPtrW")]
    private static extern nint GetWindowLongPtr(nint handle, int index);

    [DllImport("user32.dll")]
    private static extern bool ShowWindow(nint handle, int command);

    [DllImport("user32.dll")]
    private static extern bool IsIconic(nint handle);

    [DllImport("user32.dll")]
    private static extern bool SetForegroundWindow(nint handle);

    [DllImport("user32.dll")]
    private static extern bool BringWindowToTop(nint handle);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool AttachThreadInput(
        uint attachThread,
        uint attachToThread,
        bool attach);

    [DllImport("kernel32.dll")]
    private static extern uint GetCurrentThreadId();

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool PostMessage(
        nint handle,
        uint message,
        nint wParam,
        nint lParam);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool RedrawWindow(
        nint handle,
        nint updateRectangle,
        nint updateRegion,
        uint flags);

    [DllImport("dwmapi.dll")]
    private static extern int DwmFlush();

}
