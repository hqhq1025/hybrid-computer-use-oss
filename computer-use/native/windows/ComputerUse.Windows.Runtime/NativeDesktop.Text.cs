using System.Runtime.InteropServices;

namespace ComputerUse.Windows.Runtime;

internal static partial class NativeDesktop
{
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
                inputEmitted ? "post_action_observation" : "emission",
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
                inputEmitted ? "post_action_observation" : "emission",
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
}
