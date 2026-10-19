using System.Drawing;

namespace ComputerUse.Windows.Runtime;

internal static class WindowsDesktopImageGeometry
{
    // The GUI screenshot is delivered at the size the model sees: GPT-5.4 at
    // detail high shrinks 1920x1080 to 1365x768.
    internal const int MaxDeliveredWidth = 1366;
    internal const int MaxDeliveredHeight = 768;

    internal static Size DeliveredSize(int nativeWidth, int nativeHeight)
    {
        if (nativeWidth <= 0 || nativeHeight <= 0)
        {
            throw new ArgumentOutOfRangeException(
                nameof(nativeWidth),
                "Native image dimensions must be positive.");
        }

        var scale = Math.Min(
            1d,
            Math.Min(
                (double)MaxDeliveredWidth / nativeWidth,
                (double)MaxDeliveredHeight / nativeHeight));
        return new Size(
            Math.Max(
                1,
                (int)Math.Round(
                    nativeWidth * scale,
                    MidpointRounding.AwayFromZero)),
            Math.Max(
                1,
                (int)Math.Round(
                    nativeHeight * scale,
                    MidpointRounding.AwayFromZero)));
    }

    internal static double ToNativeCoordinate(
        double deliveredCoordinate,
        int deliveredExtent,
        int nativeOrigin,
        int nativeExtent)
    {
        if (deliveredExtent <= 0 || nativeExtent <= 0)
        {
            throw new ArgumentOutOfRangeException(
                nameof(deliveredExtent),
                "Delivered and native extents must be positive.");
        }

        var scale = (double)nativeExtent / deliveredExtent;
        var nativeOffset = Math.Min(
            nativeExtent - 1d,
            Math.Max(0d, (deliveredCoordinate + 0.5d) * scale - 0.5d));
        return nativeOrigin + nativeOffset;
    }
}
