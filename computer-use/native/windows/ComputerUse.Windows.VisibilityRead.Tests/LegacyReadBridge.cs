using System.Drawing;

namespace ComputerUse.Windows.Runtime;

internal sealed partial class UiaProvider
{
    // Same composition as the old ElementJson; Safe itself is linked, not copied.
    internal static bool LegacyEffectiveVisibility(
        Func<bool> getter, bool focused, Rectangle bounds, Rectangle windowBounds)
    {
        return WindowsUiaVisibility.IsEffectivelyVisible(
            Safe(getter, true), focused, bounds, windowBounds);
    }
}

// Only satisfies the unused RequiredString branch in the linked helper file.
internal sealed class ProviderFailureException(
    string code, string phase, string message) : Exception($"{code}/{phase}: {message}");
