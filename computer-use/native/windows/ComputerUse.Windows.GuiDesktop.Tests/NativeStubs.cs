using System.Drawing;
using System.Text.Json.Nodes;

namespace ComputerUse.Windows.Runtime;

// Replaces only the SendInput boundary, the Win32 layout read and GDI capture.
// The production input methods, key mapping, coordinate transform, batch loop
// and screenshot retry loop run as is.
internal static partial class NativeDesktop
{
    internal sealed record InputEvent(uint Type, int X, int Y, uint Flags, uint Data, ushort Key, ushort Scan = 0);
    internal static List<InputEvent[]> Calls { get; } = new();
    internal static Queue<uint> SendCounts { get; } = new();
    internal static Action? AfterSend { get; set; }
    internal static Rectangle Bounds { get; set; } = new(0, 0, 1920, 1080);
    // Scripted layouts are read first; once they run out, the layout is the
    // current Bounds and never changes.
    internal static Queue<Layout> Layouts { get; } = new();
    internal static List<ScreenFrame> Captures { get; } = new();
    internal static List<Layout> Published { get; } = new();

    internal static void Reset(params Layout[] layouts)
    {
        Calls.Clear();
        SendCounts.Clear();
        InputIdleWaits = 0;
        AfterSend = null;
        Bounds = new(0, 0, 1920, 1080);
        Layouts.Clear();
        foreach (var layout in layouts) Layouts.Enqueue(layout);
        Captures.Clear();
        Published.Clear();
    }

    private static uint SendInput(uint count, Input[] inputs, int size)
    {
        Calls.Add(inputs.Select(value => value.Type == InputMouse
            ? new InputEvent(value.Type, value.Union.Mouse.Dx, value.Union.Mouse.Dy,
                value.Union.Mouse.Flags, value.Union.Mouse.MouseData, 0)
            : new InputEvent(value.Type, 0, 0, value.Union.Keyboard.Flags, 0,
                value.Union.Keyboard.VirtualKey, value.Union.Keyboard.Scan)).ToArray());
        AfterSend?.Invoke();
        return SendCounts.Count > 0 ? SendCounts.Dequeue() : count;
    }

    internal sealed record ScreenFrame(Rectangle Bounds, string FrameId, bool IncludesPixels)
    {
        internal int OriginX => Bounds.X;
        internal int OriginY => Bounds.Y;
        internal int Width => Bounds.Width;
        internal int Height => Bounds.Height;
        internal int NativeWidth => Bounds.Width;
        internal int NativeHeight => Bounds.Height;
    }

    internal sealed record Layout(int DesktopX, int DesktopY, int DesktopWidth, int DesktopHeight, string FrameId);

    internal static Rectangle DesktopBounds() => Bounds;

    internal static Layout CurrentLayout() => Layouts.Count > 0
        ? Layouts.Dequeue()
        : new(Bounds.X, Bounds.Y, Bounds.Width, Bounds.Height, "fixture-layout");

    private static ScreenFrame CaptureFrame(Rectangle bounds, string frameId, bool includePixels)
    {
        var frame = new ScreenFrame(bounds, frameId, includePixels);
        Captures.Add(frame);
        return frame;
    }

    private static void RememberPublishedLayout(Layout layout) => Published.Add(layout);
    internal static bool PixelsIdentical(ScreenFrame a, ScreenFrame b) => a == b;
    internal static void LogStaleLayout(string expected, Layout current) { }
    internal static JsonObject SnapshotJson(ScreenFrame frame) => new();
    internal static int InputIdleWaits { get; set; }
    internal static void WaitForForegroundInputIdle(int timeoutMilliseconds) => InputIdleWaits++;
    internal static JsonObject Zoom(JsonObject action, string frameId) =>
        throw new NotSupportedException();
}

internal static class UiaProvider
{
    internal static string RequiredString(JsonObject node, string field) =>
        node[field]!.GetValue<string>();
}
