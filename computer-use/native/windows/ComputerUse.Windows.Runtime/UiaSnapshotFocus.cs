using System.Text.Json.Nodes;
using FlaUI.Core.AutomationElements;

namespace ComputerUse.Windows.Runtime;

internal sealed partial class UiaProvider
{
    private void BindCurrentFocus(
        IReadOnlyList<(CachedUiaNode Node, JsonObject Record, int[] Path, int Depth)> sampled,
        JsonArray annotations,
        Action<string> gap,
        Action checkTime,
        LogicalWindowBoundary<AutomationElement> boundary,
        long selectedWindowHandle)
    {
        // Cached focus flags were sampled at different times and can
        // contradict UIA's focused object. Publish only one exact, stable
        // object binding inside this sample's canonical window records.
        foreach (var sample in sampled)
        {
            var states = sample.Record["states"]?.GetValue<string>() ?? string.Empty;
            sample.Record["states"] = string.Join(' ', states.Split(' ', StringSplitOptions.RemoveEmptyEntries)
                .Where(state => state != "focused"));
        }
        try
        {
            checkTime();
            var focused = _automation.FocusedElement();
            if (focused is null) { return; }
            var match = WindowsKeyboardFocus.Match(sampled, RuntimeObjectPath(focused),
                sample => sample.Node.Facts is { RuntimeId.Length: > 0 } facts
                    ? RuntimeObjectPath(facts.RuntimeId) : string.Empty,
                sample => _automation.Compare(sample.Node.Element, focused),
                () =>
                {
                    var current = _automation.FocusedElement();
                    return current is not null && _automation.Compare(focused, current);
                }, checkTime);
            if (match.Gap is { } focusGap) { gap(focusGap); return; }
            if (match.Index is not { } index)
            {
                checkTime();
                if (boundary.Owner(focused) == selectedWindowHandle)
                {
                    gap("focused_element_not_sampled");
                }
                return;
            }
            var selected = sampled[index];
            if (selected.Node.Facts?.KeyboardFocus.Value == false)
            {
                annotations.Add(new JsonObject
                {
                    ["kind"] = "focus_evidence",
                    ["parentRuntimeId"] = PathJson(selected.Path),
                    ["reason"] = "focused_object_property_conflict",
                    ["detail"] = "Current focus matches this node; its earlier focus flag was false.",
                });
            }
            var states = selected.Record["states"]?.GetValue<string>() ?? string.Empty;
            selected.Record["states"] = (states + " focused").Trim();
        }
        catch (ObservationDeadlineExceededException) { gap("keyboard_focus_time_budget"); }
        catch { gap("keyboard_focus_read_failed"); }
    }
}
