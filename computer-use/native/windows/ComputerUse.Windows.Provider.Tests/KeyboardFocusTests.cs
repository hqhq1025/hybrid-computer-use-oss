using ComputerUse.Windows.Runtime;
using System.Text.Json.Nodes;

internal static class KeyboardFocusTests
{
    internal static void Run()
    {
        Check(WindowsKeyboardFocus.IsFocused(new JsonObject { ["states"] = "enabled focused selected" }),
            "Spreadsheet reuse reads the canonical focus state after exact object binding.");
        Check(!WindowsKeyboardFocus.IsFocused(new JsonObject { ["states"] = "enabled unfocused" }) &&
            !WindowsKeyboardFocus.IsFocused(new JsonObject()),
            "Missing canonical focus or an unrelated state cannot restore an earlier cached focus flag.");
        Check(WindowsKeyboardFocusRead.Read(() => (true, false)) == new WindowsKeyboardFocusRead(false),
            "A supported false property remains a known false.");
        Check(WindowsKeyboardFocusRead.Read(() => (true, true)) == new WindowsKeyboardFocusRead(true),
            "A supported true property remains a known true.");
        Check(WindowsKeyboardFocusRead.Read(() => (false, true)) ==
            new WindowsKeyboardFocusRead(null, "keyboard_focus_unsupported"),
            "An unsupported out-value is not a focus fact.");
        Check(WindowsKeyboardFocusRead.Read(() => throw new InvalidOperationException()) ==
            new WindowsKeyboardFocusRead(null, "keyboard_focus_read_failed"),
            "A failed property read differs from unsupported and false.");

        var comparisons = new List<string>();
        var rows = new[] { "other-window", "focused", "sibling" };
        CanonicalFocusMatch Match(string focus, Func<string, bool> equal) =>
            WindowsKeyboardFocus.Match(rows, focus, node => node,
                node => { comparisons.Add(node); return equal(node); }, () => true, () => { });
        var result = Match("focused", node => node == "focused");
        Check(result.Index == 1 && result.Gap is null && comparisons.SequenceEqual(new[] { "focused" }),
            "Stable identities narrow work but the focused object still requires an exact comparison.");
        comparisons.Clear();
        Check(Match("focused", _ => false).Index is null && comparisons.Count == 1,
            "A matching runtime identity alone does not prove focus.");
        comparisons.Clear();
        Check(Match("foreign-window", _ => throw new InvalidOperationException()).Index is null && comparisons.Count == 0,
            "Focus outside the sampled window does not select a foreground or sibling node.");

        var unidentified = new[] { new object(), new object() };
        result = WindowsKeyboardFocus.Match(unidentified, "", _ => "",
            node => ReferenceEquals(node, unidentified[1]), () => true, () => { });
        Check(result.Index == 1 && result.Gap is null, "An object without a runtime ID needs an exact comparison.");
        result = WindowsKeyboardFocus.Match(unidentified, "", _ => "", _ => true,
            () => throw new InvalidOperationException("Ambiguous focus must not run confirmation."), () => { });
        Check(result.Index is null && result.Gap == "focused_element_ambiguous", "Multiple canonical matches must not publish arbitrary focus.");
        foreach (var laterFocus in new object?[] { null, new object() })
        {
            result = WindowsKeyboardFocus.Match(unidentified, "", _ => "",
                node => ReferenceEquals(node, unidentified[1]),
                () => laterFocus is not null && ReferenceEquals(unidentified[1], laterFocus), () => { });
            Check(result.Index is null && result.Gap == "keyboard_focus_changed",
                "Missing or changed focus during confirmation does not bind the earlier object.");
        }
        var readFailure = new InvalidOperationException("focus read failed");
        try
        {
            WindowsKeyboardFocus.Match(unidentified, "", _ => "",
                node => ReferenceEquals(node, unidentified[1]), () => throw readFailure, () => { });
            throw new InvalidOperationException("Focus read failure became a successful match.");
        }
        catch (InvalidOperationException error) when (ReferenceEquals(error, readFailure)) { }
        var deadline = new InvalidOperationException("deadline");
        try
        {
            WindowsKeyboardFocus.Match(unidentified, "", _ => "", _ => true, () => true, () => throw deadline);
            throw new InvalidOperationException("Focus ignored its sampling deadline.");
        }
        catch (InvalidOperationException error) when (ReferenceEquals(error, deadline)) { }
        Console.WriteLine("Keyboard focus identity and property tests passed.");
    }

    private static void Check(bool condition, string message)
    {
        if (!condition) { throw new InvalidOperationException(message); }
    }
}
