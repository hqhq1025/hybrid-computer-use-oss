namespace ComputerUse.Windows.Runtime;

internal static class LiveValueReadback
{
    private const double RangeTolerance = 1e-9;

    internal static readonly TimeSpan SettleDelay =
        TimeSpan.FromMilliseconds(120);

    internal static bool Verify(
        string expected,
        Func<string?> readFreshValue,
        Action<TimeSpan>? delay = null)
    {
        delay ??= Thread.Sleep;
        delay(SettleDelay);
        try
        {
            return string.Equals(
                readFreshValue(),
                expected,
                StringComparison.Ordinal);
        }
        catch
        {
            return false;
        }
    }

    internal static bool VerifyTextValue(
        string expected,
        Func<string?> readFreshValue,
        Func<string?> readFreshDocumentText,
        Action<TimeSpan>? delay = null)
    {
        delay ??= Thread.Sleep;
        delay(SettleDelay);
        try
        {
            if (!HasLineBreak(expected))
            {
                return string.Equals(
                    readFreshValue(),
                    expected,
                    StringComparison.Ordinal);
            }
            var documentText = readFreshDocumentText();
            return documentText is not null &&
                string.Equals(
                    NormalizeLineEndings(documentText),
                    NormalizeLineEndings(expected),
                    StringComparison.Ordinal);
        }
        catch
        {
            return false;
        }
    }

    internal static string CanonicalTextValue(
        string valuePatternValue,
        string? documentText)
    {
        return documentText is not null && HasLineBreak(documentText)
            ? documentText
            : valuePatternValue;
    }

    private static bool HasLineBreak(string value)
    {
        return value.Contains('\r') || value.Contains('\n');
    }

    private static string NormalizeLineEndings(string value)
    {
        return value.Replace("\r\n", "\n", StringComparison.Ordinal)
            .Replace('\r', '\n');
    }

    internal static bool VerifyRange(
        double expected,
        Func<double?> readFreshValue,
        Action<TimeSpan>? delay = null)
    {
        delay ??= Thread.Sleep;
        delay(SettleDelay);
        try
        {
            var actual = readFreshValue();
            return actual is not null &&
                double.IsFinite(actual.Value) &&
                Math.Abs(actual.Value - expected) <= RangeTolerance;
        }
        catch
        {
            return false;
        }
    }
}
