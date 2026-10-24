using System.Text.Json.Nodes;
using ComputerUse.Windows.Runtime;

internal static class TextControlTests
{
    internal static void Run()
    {
        var cases = 0;
        foreach (var name in new[] { "Edit", "richedit20w", "RICHEDIT50W", "RichEditD2D", "rIcHeDiTd2DpT" })
        {
            NativeDesktop.Reset();
            NativeDesktop.TextControlClass = name;
            var generation = NativeDesktop.InputGeneration;
            NativeDesktop.TypeText("中文\t😀e\u0301", 1);
            Check(NativeDesktop.TextSelections.Single().Text == "中文\t😀e\u0301" &&
                NativeDesktop.TextSelections[0].Window == 2 && NativeDesktop.TextMessages.Count == 0 &&
                NativeDesktop.Calls.Count == 0 && NativeDesktop.TextWakes.Count == 1 &&
                NativeDesktop.InputGeneration == generation + 1,
                $"Standard control {name} must insert the full Unicode segment once at the pinned focus.");
            cases++;
        }
        foreach (var name in new[] { "RichEdit", "RichEdit20A", "RichEditD2DPT-custom", "Chrome_RenderWidgetHostHWND", "Unknown" })
        {
            NativeDesktop.Reset();
            NativeDesktop.TextControlClass = name;
            NativeDesktop.TypeText("AB");
            Check(NativeDesktop.TextSelections.Count == 0 && NativeDesktop.TextMessages.Count == 2,
                $"Unrecognized class {name} must retain character messages.");
            cases++;
        }

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        NativeDesktop.TypeText("中😀\te\u0301\r\n\n日本\r한국\n");
        Check(NativeDesktop.TextEmissionOrder.SequenceEqual(new[] {
            "text:中😀\te\u0301", "message:100:D", "message:102:D", "message:101:D",
            "message:100:D", "message:102:D", "message:101:D", "text:日本",
            "message:100:D", "message:102:D", "message:101:D", "text:한국",
            "message:100:D", "message:102:D", "message:101:D" }),
            "Mixed Unicode, literal tabs, CRLF, empty lines and trailing newline must preserve order.");
        Check(NativeDesktop.TextWakes.Count == 1 && NativeDesktop.Calls.Count == 0,
            "Segmented input must wake once without queued keyboard input.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        NativeDesktop.OnTextSelection = _ => false;
        var failure = Fails(() => NativeDesktop.TypeText("中😀\tline\r\nlater"));
        Check(!failure.InputEventsEmitted && failure.MutationUnknown &&
            Evidence(failure, "attempted_messages") == 1 && Evidence(failure, "returned_messages") == 0 &&
            Evidence(failure, "unknown_message_index") == 0 && Evidence(failure, "source_utf16_index") == 0 &&
            Evidence(failure, "source_utf16_count") == "中😀\tline".Length &&
            NativeDesktop.TextSelections.Count == 1 && NativeDesktop.TextMessages.Count == 0 &&
            NativeDesktop.TextWakes.Count == 1,
            "First unknown replacement must cover its full source segment, stop the suffix and never replay.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        NativeDesktop.OnTextSelection = count => count != 2;
        failure = Fails(() => DesktopProvider.ExecuteBatch(new JsonObject { ["actions"] = new JsonArray(
            new JsonObject { ["type"] = "type", ["text"] = "A\r\n中😀\tB\nnever" },
            new JsonObject { ["type"] = "click", ["x"] = 5d, ["y"] = 5d }) }));
        Check(failure.InputEventsEmitted && failure.MutationUnknown &&
            Evidence(failure, "attempted_messages") == 5 && Evidence(failure, "returned_messages") == 4 &&
            Evidence(failure, "unknown_message_index") == 4 && Evidence(failure, "source_utf16_index") == 3 &&
            Evidence(failure, "source_utf16_count") == "中😀\tB".Length &&
            NativeDesktop.TextSelections.Count == 2 && NativeDesktop.TextMessages.Count == 3 &&
            NativeDesktop.Calls.Count == 0,
            "GUI must retain message counts and the whole unknown segment, without emitting the later click.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        NativeDesktop.TextControlReadOnly = true;
        var before = NativeDesktop.InputGeneration;
        failure = Fails(() => NativeDesktop.TypeText("blocked"));
        Check(failure.Phase == "preparation" && !failure.InputEventsEmitted && !failure.MutationUnknown &&
            NativeDesktop.TextSelections.Count == 0 && NativeDesktop.TextMessages.Count == 0 &&
            NativeDesktop.TextWakes.Count == 0 && NativeDesktop.InputGeneration == before,
            "A read-only standard control must fail without input, wake or cache invalidation.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        NativeDesktop.OnTextSelection = _ => { NativeDesktop.TextControlReadOnly = true; return true; };
        failure = Fails(() => NativeDesktop.TypeText("first\nsecond"));
        Check(failure.InputEventsEmitted && NativeDesktop.TextSelections.Count == 1 &&
            NativeDesktop.TextMessages.Count == 0 && NativeDesktop.TextWakes.Count == 1,
            "A control becoming read-only must stop before the next message and preserve prior input.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        NativeDesktop.OnTextSelection = _ =>
        {
            NativeDesktop.OnReadTextControlReadOnly = () => throw new ProviderFailureException(
                "desktop_input_failed", "preparation", "Control style unavailable.");
            return true;
        };
        failure = Fails(() => NativeDesktop.TypeText("first\nsecond"));
        Check(failure.Phase == "emission" && failure.InputEventsEmitted && failure.MutationUnknown &&
            Evidence(failure, "returned_messages") == 1 && NativeDesktop.TextSelections.Count == 1 &&
            NativeDesktop.TextMessages.Count == 0 && NativeDesktop.TextWakes.Count == 1,
            "A later style-read failure must preserve the emitted prefix and stop further input.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        var original = NativeDesktop.Focus!.Value;
        NativeDesktop.OnTextSelection = _ => { NativeDesktop.Focus = original with { Focus = 99 }; return true; };
        failure = Fails(() => NativeDesktop.TypeText("first\nsecond"));
        Check(failure.InputEventsEmitted && NativeDesktop.TextSelections.Count == 1 &&
            NativeDesktop.TextMessages.Count == 0 && NativeDesktop.TextWakes.SequenceEqual(new[] { original }),
            "Selection insertion must not follow new focus and must wake only its original receiver.");
        cases++;

        NativeDesktop.Reset();
        NativeDesktop.TextControlClass = "Edit";
        before = NativeDesktop.InputGeneration;
        failure = Fails(() => NativeDesktop.TypeText("wrong root", 99));
        Check(!failure.InputEventsEmitted && NativeDesktop.TextSelections.Count == 0 &&
            NativeDesktop.TextWakes.Count == 0 && NativeDesktop.InputGeneration == before,
            "AX root mismatch must reject standard-control input before insertion.");
        cases++;

        foreach (var name in new[] { "Edit", "Unknown" })
        {
            NativeDesktop.Reset();
            NativeDesktop.TextControlClass = name;
            before = NativeDesktop.InputGeneration;
            failure = Fails(() => DesktopProvider.ExecuteBatch(new JsonObject { ["actions"] = new JsonArray(
                new JsonObject { ["type"] = "click", ["x"] = 5d, ["y"] = 5d },
                new JsonObject { ["type"] = "type", ["text"] = "valid\0suffix" }) }));
            Check(failure.Code == "invalid_argument" && !failure.InputEventsEmitted &&
                NativeDesktop.Calls.Count == 0 && NativeDesktop.TextSelections.Count == 0 &&
                NativeDesktop.TextMessages.Count == 0 && NativeDesktop.Captures.Count == 0 &&
                NativeDesktop.TextWakes.Count == 0 && NativeDesktop.InputGeneration == before,
                "NUL in later text must reject the whole GUI batch before any side effect.");
            cases++;
        }
        Console.WriteLine($"Standard text control input tests passed ({cases} cases).");
    }

    private static int Evidence(ProviderFailureException failure, string key) =>
        failure.ActionEvidence![key]!.GetValue<int>();
    private static ProviderFailureException Fails(Action action)
    {
        try { action(); }
        catch (ProviderFailureException failure) { return failure; }
        throw new InvalidOperationException("Expected a provider failure.");
    }
    private static void Check(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException(message);
    }
}
