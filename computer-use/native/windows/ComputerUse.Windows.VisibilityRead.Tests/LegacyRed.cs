using System.Drawing;
using System.Runtime.InteropServices;
using ComputerUse.Windows.Runtime;

internal static class LegacyRed
{
    internal static int Run()
    {
        var failures = 0;
        foreach (var focused in new[] { false, true })
        {
            var calls = 0;
            bool? observed = UiaProvider.LegacyEffectiveVisibility(
                () =>
                {
                    calls++;
                    throw new COMException("Synthetic client getter failure",
                        unchecked((int)0x80004005));
                },
                focused,
                new Rectangle(120, 140, 400, 240),
                new Rectangle(100, 100, 800, 600));
            var passed = observed is null && calls == 1;
            Console.WriteLine(
                $"{(passed ? "PASS" : "FAIL")} old-getter focused={focused} " +
                $"expected=null actual={observed} calls={calls}");
            if (!passed) { failures++; }
        }
        Console.WriteLine(
            $"legacy unknown-preservation assertions: failed={failures}/2; " +
            "linked actual Safe helper, synthetic getter exception, no UIA/VM");
        return failures == 0 ? 0 : 1;
    }
}
