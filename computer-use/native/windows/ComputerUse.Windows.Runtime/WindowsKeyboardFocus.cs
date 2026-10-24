using System.Text.Json.Nodes;

namespace ComputerUse.Windows.Runtime;

internal sealed record WindowsKeyboardFocusRead(bool? Value, string? Gap = null)
{
    internal static WindowsKeyboardFocusRead Read(Func<(bool Supported, bool Value)> read)
    {
        try
        {
            var (supported, value) = read();
            return supported ? new(value) : new(null, "keyboard_focus_unsupported");
        }
        catch { return new(null, "keyboard_focus_read_failed"); }
    }
}

internal sealed record CanonicalFocusMatch(int? Index, string? Gap = null);

internal static class WindowsKeyboardFocus
{
    internal static bool IsFocused(JsonObject record) =>
        (record["states"]?.GetValue<string>() ?? string.Empty)
            .Split(' ', StringSplitOptions.RemoveEmptyEntries).Contains("focused");

    // A native runtime identity narrows comparisons; it never substitutes
    // for an exact comparison with the object returned by FocusedElement.
    internal static CanonicalFocusMatch Match<T>(
        IReadOnlyList<T> nodes, string focusedIdentity, Func<T, string> identity,
        Func<T, bool> isFocusedObject, Func<bool> focusStillCurrent, Action checkTime)
    {
        int? match = null;
        for (var index = 0; index < nodes.Count; index++)
        {
            checkTime();
            var candidateIdentity = identity(nodes[index]);
            if (focusedIdentity.Length > 0 && candidateIdentity.Length > 0 &&
                focusedIdentity != candidateIdentity) { continue; }
            if (!isFocusedObject(nodes[index])) { continue; }
            if (match is not null) { return new(null, "focused_element_ambiguous"); }
            match = index;
        }
        if (match is not null)
        {
            checkTime();
            if (!focusStillCurrent()) { return new(null, "keyboard_focus_changed"); }
        }
        return new(match);
    }
}
