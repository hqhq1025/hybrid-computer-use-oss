using System.Text.Json.Nodes;
using ComputerUse.Windows.Runtime;

internal static class GuiTextIntegrationTests
{
    internal static void Run()
    {
        NativeDesktop.Reset();
        var malformed = Batch(
            new JsonObject { ["type"] = "click", ["x"] = 5d, ["y"] = 5d },
            new JsonObject { ["type"] = "type", ["text"] = "later\ud800" });
        var failure = Fails(() => DesktopProvider.ExecuteBatch(malformed));
        Check(failure.Code == "invalid_argument" && !failure.InputEventsEmitted &&
            NativeDesktop.Calls.Count == 0 && NativeDesktop.TextMessages.Count == 0 &&
            NativeDesktop.Captures.Count == 0,
            "Malformed later text must reject before the preceding click or any capture.");

        NativeDesktop.Reset();
        NativeDesktop.OnTextMessage = _ => false;
        failure = Fails(() => DesktopProvider.ExecuteBatch(Batch(Text("ab"))));
        Check(!failure.InputEventsEmitted && failure.MutationUnknown &&
            failure.DispatchedActions == 0 &&
            failure.ActionEvidence?["attempted_messages"]?.GetValue<int>() == 1 &&
            failure.ActionEvidence?["unknown_message_index"]?.GetValue<int>() == 0 &&
            NativeDesktop.TextMessages.Count == 1,
            "The first unknown message must retain uncertainty and its attempted index without claiming delivery.");

        NativeDesktop.Reset();
        NativeDesktop.OnTextMessage = count => count < 2;
        failure = Fails(() => DesktopProvider.ExecuteBatch(Batch(
            new JsonObject { ["type"] = "keypress", ["keys"] = new JsonArray("LEFT") },
            Text("abc"), new JsonObject { ["type"] = "click", ["x"] = 5d, ["y"] = 5d })));
        Check(failure.InputEventsEmitted && failure.MutationUnknown &&
            failure.DispatchedActions == 1 && failure.DispatchedInputActions == 1 &&
            failure.ActionEvidence?["returned_messages"]?.GetValue<int>() == 1 &&
            failure.ActionEvidence?["unknown_message_index"]?.GetValue<int>() == 1 &&
            failure.ActionEvidence?["route"]?.GetValue<string>() == "input.window_messages" &&
            NativeDesktop.TextMessages.Count == 2 && NativeDesktop.Calls.Count == 2,
            "GUI wrapping must retain the completed prefix, native failure evidence, and stop before the later click.");

        NativeDesktop.Reset();
        var result = DesktopProvider.ExecuteBatch(Batch(Text("")));
        Check(result["mutation"]?["input_events_emitted"]?.GetValue<bool>() == false &&
            result["gui_result"]?["input_actions_dispatched"]?.GetValue<int>() == 0 &&
            NativeDesktop.TextMessages.Count == 0 && NativeDesktop.InputIdleWaits == 0,
            "Empty text must not claim emission or consume a keyboard processing barrier.");
        Console.WriteLine("GUI text validation and failure integration tests passed (4 cases).");
    }

    private static JsonObject Text(string value) => new() { ["type"] = "type", ["text"] = value };
    private static JsonObject Batch(params JsonNode[] actions) => new() { ["actions"] = new JsonArray(actions) };
    private static ProviderFailureException Fails(Action action)
    {
        try { action(); }
        catch (ProviderFailureException error) { return error; }
        throw new InvalidOperationException("Expected a provider failure.");
    }
    private static void Check(bool condition, string message)
    {
        if (!condition) { throw new InvalidOperationException(message); }
    }
}
