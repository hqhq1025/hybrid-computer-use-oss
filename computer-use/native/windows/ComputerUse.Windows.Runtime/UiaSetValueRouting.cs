using System.Globalization;

namespace ComputerUse.Windows.Runtime;

internal static class UiaSetValueRouting
{
    internal static bool PrefersDocumentInput(
        bool documentControl,
        bool writableValueAvailable,
        bool textPatternAvailable)
    {
        return documentControl &&
            writableValueAvailable &&
            textPatternAvailable;
    }

    internal static bool PrefersRange(
        string canonicalRole,
        bool writableRangeAvailable)
    {
        return writableRangeAvailable &&
            canonicalRole is "slider" or "spinbutton" or "scrollbar";
    }

    internal static bool TryParseFiniteRangeValue(
        string requested,
        out double value)
    {
        return double.TryParse(
                requested,
                NumberStyles.Float,
                CultureInfo.InvariantCulture,
                out value) &&
            double.IsFinite(value);
    }

    internal static bool IsWithinRange(
        double requested,
        double minimum,
        double maximum)
    {
        return double.IsFinite(requested) &&
            double.IsFinite(minimum) &&
            double.IsFinite(maximum) &&
            minimum <= maximum &&
            requested >= minimum &&
            requested <= maximum;
    }
}
