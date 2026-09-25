namespace ComputerUse.Windows.Runtime;

internal static class WindowRenderCommit
{
    private const int CaptureCount = 3;
    private static readonly TimeSpan CommitDelay =
        TimeSpan.FromMilliseconds(100);

    internal static T Capture<T>(
        Func<bool> redraw,
        Func<int> flushComposition,
        Func<T> capture,
        Action<TimeSpan>? delay = null)
    {
        ArgumentNullException.ThrowIfNull(redraw);
        ArgumentNullException.ThrowIfNull(flushComposition);
        ArgumentNullException.ThrowIfNull(capture);
        delay ??= Thread.Sleep;

        T? current = default;
        for (var attempt = 0; attempt < CaptureCount; attempt++)
        {
            if (!redraw())
            {
                throw new InvalidOperationException("window_redraw_failed");
            }
            if (flushComposition() != 0)
            {
                throw new InvalidOperationException(
                    "desktop_composition_flush_failed");
            }
            delay(CommitDelay);
            current = capture();
        }
        return current!;
    }
}
