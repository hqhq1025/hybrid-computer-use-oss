using System.Drawing;
using System.Text.Json.Nodes;
using ComputerUse.Windows.Runtime;

var cases = 0;
void Check(bool condition, string message)
{
    if (!condition) throw new Exception(message);
}
T Fails<T>(Action action) where T : Exception
{
    try { action(); }
    catch (T error) { return error; }
    throw new Exception($"Expected {typeof(T).Name}");
}

// A character without its own virtual key fails before any input.
foreach (var key in new[] { "!", "?", "_", "中", "é", "ı", "😀" })
{
    NativeDesktop.Reset();
    Fails<InvalidOperationException>(() => NativeDesktop.PressKeys(new[] { "CTRL", key }));
    Check(NativeDesktop.Calls.Count == 0, $"Invalid key {key} emitted input");
    cases++;
}
foreach (var (key, expected) in new[] { ("a", 0x41), ("9", 0x39), (";", 0xBA), ("-", 0xBD), ("PERIOD", 0xBE) })
{
    NativeDesktop.Reset();
    NativeDesktop.PressKeys(new[] { key });
    Check(NativeDesktop.Calls[0][0].Key == expected, $"Wrong virtual key for {key}");
    Check(NativeDesktop.Calls[0][0].Flags == 0 && NativeDesktop.Calls[1][0].Flags == 2,
        $"Ordinary key {key} got the extended flag");
    cases++;
}
// Navigation, Delete and Windows keys carry the extended flag down and up.
foreach (var key in new[] { "DELETE", "LEFT", "UP", "RIGHT", "DOWN", "HOME", "END", "PAGEUP", "PAGEDOWN", "WIN" })
{
    NativeDesktop.Reset();
    NativeDesktop.PressKeys(new[] { "CTRL", key });
    Check(NativeDesktop.Calls[0][1].Flags == 1 && NativeDesktop.Calls[1][0].Flags == 3,
        $"Extended key {key} lost its extended flag on key down or key up");
    cases++;
}

// A click whose button-down was sent but whose button-up was not releases
// the same button and keeps the input evidence.
foreach (var (button, releaseFlag, data) in new[] { ("left", 4U, 0U), ("right", 16U, 0U), ("wheel", 64U, 0U), ("back", 256U, 1U), ("forward", 256U, 2U) })
{
    NativeDesktop.Reset();
    NativeDesktop.SendCounts.Enqueue(1); // pointer move
    NativeDesktop.SendCounts.Enqueue(1); // only button-down
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.Click(5, 5, button, 1));
    Check(failure.InputEventsEmitted && failure.MutationUnknown, "Partial click lost input evidence");
    Check(NativeDesktop.Calls.Count == 3 && NativeDesktop.Calls[2][0].Flags == releaseFlag &&
        NativeDesktop.Calls[2][0].Data == data, $"Partial {button} click did not release the same button");
    cases++;
}
{
    NativeDesktop.Reset();
    foreach (var sent in new uint[] { 1, 1, 0 }) NativeDesktop.SendCounts.Enqueue(sent);
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.Click(5, 5, "left", 1));
    Check(failure.Code == "send_input_partial" && failure.Message.Contains("release also failed"),
        "A failed release replaced the original partial-click failure");
    cases++;
}
{
    NativeDesktop.Reset();
    NativeDesktop.SendCounts.Enqueue(1);
    NativeDesktop.SendCounts.Enqueue(0);
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.Click(5, 5, "left", 1));
    Check(failure.InputEventsEmitted && NativeDesktop.Calls.Count == 2,
        "No button event was sent, so no release may follow the pointer move");
    cases++;
}
// A later drag point that fails after the button went down keeps the input
// evidence and still releases the button.
{
    NativeDesktop.Reset();
    NativeDesktop.AfterSend = () =>
    {
        if (NativeDesktop.Calls.Count == 2) NativeDesktop.Bounds = new(0, 0, 1, 1);
    };
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.Drag(new[] { new PointF(5, 5), new PointF(8, 8) }));
    Check(failure.InputEventsEmitted && failure.MutationUnknown, "A drag failure after button-down lost prior input");
    Check(NativeDesktop.Calls[^1][0].Flags == 4, "A drag failure after button-down left the button down");
    cases++;
}

// A bad key in a later action rejects the whole batch before capture or input.
foreach (var later in new[] { """{"type":"keypress","keys":["CTRL","!"]}""", """{"type":"click","x":5,"y":5,"keys":["BAD_KEY"]}""" })
{
    NativeDesktop.Reset();
    var request = JsonNode.Parse("{\"actions\":[{\"type\":\"type\",\"text\":\"do not type\"}," + later + "]}")!.AsObject();
    var failure = Fails<ProviderFailureException>(() => DesktopProvider.ExecuteBatch(request));
    Check(failure.Phase == "precondition" && !failure.InputEventsEmitted &&
        NativeDesktop.Calls.Count == 0 && NativeDesktop.Captures.Count == 0,
        "A bad later key did not reject the batch before capture and input");
    cases++;
}

foreach (var sent in new uint[] { 0, 1 })
{
    NativeDesktop.Reset();
    NativeDesktop.SendCounts.Enqueue(sent);
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.PressKeys(new[] { "CTRL", "DELETE" }));
    Check(failure.InputEventsEmitted == (sent > 0), "Key-down emission evidence is wrong");
    Check(NativeDesktop.Calls.Count == (sent == 0 ? 1 : 3), "Key cleanup count is wrong");
    if (sent > 0)
        Check(NativeDesktop.Calls[1][0].Key == 0x2E && NativeDesktop.Calls[1][0].Flags == 3 &&
            NativeDesktop.Calls[2][0].Key == 0x11 && NativeDesktop.Calls[2][0].Flags == 2,
            "A partial key-down did not release the chord in reverse with the extended flag");
    cases++;
}
{
    NativeDesktop.Reset();
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.WithHeldKeys(new[] { "WIN" },
        () => throw new InvalidOperationException("pointer became invalid")));
    Check(failure.InputEventsEmitted && failure.MutationUnknown && NativeDesktop.Calls[^1][0].Flags == 3,
        "A held Windows key was not released after the action failed");
    cases++;
}

// GUI scroll takes Computer deltas: 100 per wheel notch, at least one notch.
foreach (var (delta, notches) in new[] { (500d, 5), (-500d, -5), (0.001d, 1), (-0.001d, -1), (2000d, 20), (-2000d, -20) })
{
    foreach (var horizontal in new[] { false, true })
    {
        NativeDesktop.Reset();
        var scroll = new JsonObject
        {
            ["type"] = "scroll", ["x"] = 5d, ["y"] = 5d,
            [horizontal ? "scroll_x" : "scroll_y"] = delta,
        };
        _ = DesktopProvider.ExecuteBatch(new JsonObject { ["actions"] = new JsonArray(scroll) });
        var wheel = NativeDesktop.Calls[1].Single();
        Check(wheel.Flags == (horizontal ? 0x1000U : 0x800U) &&
            unchecked((int)wheel.Data) == notches * 120 * (horizontal ? 1 : -1),
            $"Scroll delta {delta} sent the wrong wheel amount or direction");
        cases++;
    }
}
foreach (var invalid in new[] { 2000.01, -2000.01, double.NaN, double.PositiveInfinity })
{
    NativeDesktop.Reset();
    var scroll = new JsonObject { ["type"] = "scroll", ["x"] = 5d, ["y"] = 5d, ["scroll_y"] = invalid };
    var request = new JsonObject { ["actions"] = new JsonArray(
        new JsonObject { ["type"] = "type", ["text"] = "must not happen" }, scroll) };
    var failure = Fails<ProviderFailureException>(() => DesktopProvider.ExecuteBatch(request));
    Check(failure.Phase == "precondition" && !failure.InputEventsEmitted &&
        NativeDesktop.Calls.Count == 0 && NativeDesktop.Captures.Count == 0,
        "An invalid scroll delta did not reject the batch before input");
    cases++;
}
// AX page scrolling still passes wheel notches straight through.
{
    NativeDesktop.Reset();
    NativeDesktop.Scroll(5, 5, 0, 3);
    Check(unchecked((int)NativeDesktop.Calls[1][0].Data) == -360,
        "Native wheel notches changed with the GUI delta conversion");
    cases++;
}

// Screenshot capture across desktop layout changes.
var small = new NativeDesktop.Layout(0, 0, 1920, 1080, "small");
var large = new NativeDesktop.Layout(-1920, -1080, 3840, 2160, "large");

// A monitor added during the first attempt: the retry captures the new
// desktop rectangle and publishes the layout it captured.
foreach (var pixels in new[] { false, true })
{
    NativeDesktop.Reset(small, large, large, large);
    var frame = pixels ? NativeDesktop.CaptureForSettling() : NativeDesktop.Capture();
    Check(frame.Bounds == new Rectangle(-1920, -1080, 3840, 2160) && frame.FrameId == "large" &&
        frame.IncludesPixels == pixels, "A full-desktop retry kept the bounds of the old layout");
    Check(NativeDesktop.Captures.Count == 2 && NativeDesktop.Published.Single() == large,
        "An unstable attempt was published");
    cases++;
}
// A window crop is clipped again against the desktop of each attempt.
{
    var crop = new Rectangle(-100, -100, 300, 300);
    NativeDesktop.Reset(small, large, large, large);
    var frame = NativeDesktop.Capture(crop);
    Check(NativeDesktop.Captures[0].Bounds == new Rectangle(0, 0, 200, 200) && frame.Bounds == crop,
        "A window crop was not clipped against the new desktop");
    cases++;
}
// A monitor removed during the first attempt.
{
    NativeDesktop.Reset(large, small, small, small);
    var frame = NativeDesktop.Capture();
    Check(frame.Bounds == new Rectangle(0, 0, 1920, 1080), "A removed monitor stayed in the capture bounds");
    cases++;
}
// A crop entirely off the desktop captures and publishes nothing.
{
    NativeDesktop.Reset(small);
    Check(!NativeDesktop.TryCapture(new Rectangle(-100, -100, 50, 50), out var frame) && frame is null,
        "An off-desktop crop succeeded");
    Check(NativeDesktop.Captures.Count == 0 && NativeDesktop.Published.Count == 0,
        "An off-desktop crop captured or published a frame");
    cases++;
}
// A layout that keeps changing fails after the bounded number of attempts.
{
    NativeDesktop.Reset(small, large, small, large, small, large);
    try
    {
        NativeDesktop.Capture();
        throw new Exception("A continuously changing desktop produced a frame");
    }
    catch (ProviderFailureException error)
    {
        Check(error.Code == "desktop_layout_unstable" && NativeDesktop.Captures.Count == 3 &&
            NativeDesktop.Published.Count == 0, "Capture retries were unbounded or published an unstable frame");
    }
    cases++;
}

// Each keypress returns only after its own queue-consumption boundary.
{
    NativeDesktop.Reset();
    DesktopProvider.ExecuteBatch(JsonNode.Parse(
        """{"actions":[{"type":"keypress","keys":["A"]},{"type":"keypress","keys":["B"]}]}""")!.AsObject());
    Check(NativeDesktop.BarrierWaits == 2, "A keypress returned without its queue boundary");
    cases++;
}
// Delayed queue consumption holds back the next synchronous text action.
{
    NativeDesktop.Reset();
    using var entered = new ManualResetEventSlim();
    using var released = new ManualResetEventSlim();
    NativeDesktop.OnBarrierWait = () =>
    {
        entered.Set();
        if (!released.Wait(3000)) throw new Exception("Test release was not signalled");
    };
    var task = Task.Run(() => DesktopProvider.ExecuteBatch(JsonNode.Parse(
        """{"actions":[{"type":"keypress","keys":["CTRL","HOME"]},{"type":"type","text":"HCU"}]}""")!.AsObject()));
    try
    {
        Check(entered.Wait(3000), "Keyboard wait was not entered");
        Check(NativeDesktop.TextMessages.Count == 0, "Text overtook queued keys");
        var events = NativeDesktop.Calls.SelectMany(call => call).ToArray();
        Check(events.Length == 4 && events.Select(input => input.Tag).Distinct().Count() == 4,
            "The chord did not tag every real event independently");
    }
    finally { released.Set(); }
    Check(task.Wait(3000) && NativeDesktop.TextMessages.Count == 3,
        "Text did not follow the completed queue wait");
    cases++;
}
// Missing receipt or receiver-thread changes stop later input without replay.
{
    NativeDesktop.Reset();
    NativeDesktop.OnBarrierWait = () => throw new ProviderFailureException(
        "desktop_input_failed", "emission", "receiver thread changed", true, true,
        actionEvidence: new JsonObject { ["consumed_events"] = 3 });
    var failure = Fails<ProviderFailureException>(() => DesktopProvider.ExecuteBatch(JsonNode.Parse(
        """{"actions":[{"type":"keypress","keys":["CTRL","HOME"]},{"type":"type","text":"HCU"}]}""")!.AsObject()));
    Check(failure.InputEventsEmitted && failure.MutationUnknown && NativeDesktop.Calls.Count == 2 &&
        NativeDesktop.TextMessages.Count == 0 && failure.ActionEvidence?["consumed_events"]?.GetValue<int>() == 3,
        "Uncertain queue consumption replayed input or lost its partial evidence");
    cases++;
}
// A modified mouse effect stays unknown when its release reached another thread.
{
    NativeDesktop.Reset();
    NativeDesktop.OnBarrierWait = () => throw new ProviderFailureException(
        "desktop_input_failed", "emission", "receiver thread changed", true, true,
        actionEvidence: new JsonObject { ["consumed_events"] = 1 });
    var mouseEmitted = false;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.WithHeldKeys(
        new[] { "CTRL" }, () => { NativeDesktop.MovePointer(5, 5); mouseEmitted = true; }));
    Check(mouseEmitted && failure.MutationUnknown && NativeDesktop.Calls.Count == 3 &&
        failure.ActionEvidence?["consumed_events"]?.GetValue<int>() == 1,
        "Modified mouse input lost uncertainty or repeated modifier release");
    cases++;
}
// Filtered removal of the final key-up does not replace missing earlier events.
{
    var receipts = new[]
    {
        new WindowsKeyboardQueueReceipt(0, false, 0),
        new WindowsKeyboardQueueReceipt(2, true, 0),
    };
    Check(!WindowsKeyboardQueueReceipts.AreComplete(receipts), "A final key-up hid an earlier pending key");
    receipts[0] = new(3, true, 3);
    Check(WindowsKeyboardQueueReceipts.AreComplete(receipts) &&
        !WindowsKeyboardQueueReceipts.ModifiersReleased(receipts), "A late Ctrl-down was treated as released");
    receipts[0] = new(3, false, 0);
    Check(!WindowsKeyboardQueueReceipts.ModifiersReleased(receipts), "Unavailable key state was treated as released");
    receipts[0] = new(3, true, 0);
    Check(WindowsKeyboardQueueReceipts.ModifiersReleased(receipts), "Complete released input was rejected");
    cases++;
}
// Text retains UTF-16 ordering and literal tabs; CRLF is one line break.
{
    NativeDesktop.Reset();
    NativeDesktop.TypeText("a\u00e9\r\n\t\U0001F600");
    var chars = NativeDesktop.TextMessages.Where(value => value.Message == 0x102)
        .Select(value => (char)value.Value).ToArray();
    Check(new string(chars) == "a\u00e9\r\t\U0001F600" &&
        NativeDesktop.TextMessages.Count == 8 && NativeDesktop.Calls.Count == 0,
        "Text reordered characters, lost a literal tab, or duplicated CRLF");
    cases++;
}
// An unknown delivery stops immediately, retaining both the prefix and unknown unit.
{
    NativeDesktop.Reset();
    NativeDesktop.OnTextMessage = count => count != 2;
    var generation = NativeDesktop.InputGeneration;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("ABC"));
    Check(NativeDesktop.TextMessages.Count == 2 && failure.InputEventsEmitted &&
        failure.MutationUnknown && failure.ActionEvidence!["unknown_message_index"]!.GetValue<int>() == 1 &&
        NativeDesktop.InputGeneration == generation + 2,
        "Unknown text delivery was replayed, lost evidence, or retained cached state");
    cases++;
}
// Even the first timed-out send may take effect later.
{
    NativeDesktop.Reset();
    NativeDesktop.OnTextMessage = _ => false;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(!failure.InputEventsEmitted && failure.MutationUnknown && NativeDesktop.TextMessages.Count == 1,
        "First unknown delivery was represented as safe to retry");
    cases++;
}
// A focus change stops the suffix without following the newly focused control.
{
    NativeDesktop.Reset();
    NativeDesktop.OnTextMessage = _ => { NativeDesktop.Focus = null; return true; };
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(NativeDesktop.TextMessages.Count == 1 && failure.InputEventsEmitted &&
        failure.ActionEvidence!["source_utf16_index"]!.GetValue<int>() == 1,
        "Changed focus received a text suffix");
    cases++;
}
// Validate the whole string before any emission, including a bad suffix.
{
    NativeDesktop.Reset();
    var generation = NativeDesktop.InputGeneration;
    _ = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("valid\uD800"));
    Check(NativeDesktop.TextMessages.Count == 0 && NativeDesktop.InputGeneration == generation,
        "Malformed UTF-16 was rejected only after emitting its prefix");
    _ = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("x", 99));
    Check(NativeDesktop.TextMessages.Count == 0, "AX expected root was ignored");
    cases++;
}
// Resolve an invalid Enter scan before sending even a valid text prefix.
{
    NativeDesktop.Reset();
    NativeDesktop.Focus = null;
    var generation = NativeDesktop.InputGeneration;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(NativeDesktop.TextMessages.Count == 0 && !failure.InputEventsEmitted &&
        !failure.MutationUnknown && NativeDesktop.InputGeneration == generation,
        "Unavailable Unicode focus attempted input or invalidated the cache");
    cases++;
}
// Resolve an invalid Enter scan before sending even a valid text prefix.
{
    NativeDesktop.Reset();
    NativeDesktop.EnterScan = 0;
    _ = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("prefix\nsuffix"));
    Check(NativeDesktop.TextMessages.Count == 0, "Invalid Enter scan was rejected after text emission");
    cases++;
}
// A deadline expires before the next message; no delayed retry or cleanup input.
{
    NativeDesktop.Reset();
    NativeDesktop.OnTextMessage = _ => { NativeDesktop.TextTicks = 15000; return true; };
    _ = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(NativeDesktop.TextMessages.Count == 1, "Text continued after its deadline");
    cases++;
}
// Failure halfway through Enter or a surrogate pair must not send the suffix.
foreach (var text in new[] { "\r\nrest", "\U0001F600rest" })
{
    NativeDesktop.Reset();
    NativeDesktop.OnTextMessage = count => count != 2;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText(text));
    Check(NativeDesktop.TextMessages.Count == 2 && failure.MutationUnknown,
        "A partial text sequence triggered extra input");
    cases++;
}
// The public maximum length is supported without truncation.
{
    NativeDesktop.Reset();
    NativeDesktop.TypeText(new string('x', 10000));
    Check(NativeDesktop.TextMessages.Count == 10000 && NativeDesktop.TextWakes.Count == 1,
        "Long text was truncated or failed to wake the receiver once");
    cases++;
}

// A focus change still wakes the original receiver, without input at the new focus.
{
    NativeDesktop.Reset();
    var original = NativeDesktop.Focus!.Value;
    NativeDesktop.OnTextMessage = _ => { NativeDesktop.Focus = original with { Focus = 99 }; return true; };
    _ = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(NativeDesktop.TextMessages.Count == 1 && NativeDesktop.TextWakes.SequenceEqual(new[] { original }),
        "Partial text wake followed changed focus or repeated input");
    cases++;
}
// Wake failure cannot erase the unknown message or replay the text.
{
    NativeDesktop.Reset();
    NativeDesktop.TextWakeFails = true;
    NativeDesktop.OnTextMessage = _ => false;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(NativeDesktop.TextMessages.Count == 1 && NativeDesktop.TextWakes.Count == 1 &&
        failure.ActionEvidence!["unknown_message_index"]!.GetValue<int>() == 0 &&
        !failure.InputEventsEmitted && failure.MutationUnknown,
        "Wake failure replaced the primary unknown-delivery evidence");
    cases++;
}
// Successful character sends followed by a failed wake remain unsafe to repeat.
{
    NativeDesktop.Reset();
    NativeDesktop.TextWakeThrows = true;
    NativeDesktop.OnTextMessage = _ => false;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(failure.ActionEvidence!["unknown_message_index"]!.GetValue<int>() == 0 &&
        failure.MutationUnknown && NativeDesktop.TextWakes.Count == 1,
        "Throwing wake boundary masked the original unknown input");
    cases++;
}
// Successful character sends followed by a failed wake remain unsafe to repeat.
{
    NativeDesktop.Reset();
    NativeDesktop.TextWakeFails = true;
    var failure = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("AB"));
    Check(NativeDesktop.TextMessages.Count == 2 && NativeDesktop.TextWakes.Count == 1 &&
        failure.InputEventsEmitted && failure.MutationUnknown &&
        failure.ActionEvidence!["sequence_phase"]!.GetValue<string>() == "wake",
        "Failed wake lost already-sent text or reported success");
    cases++;
}

// Empty or invalid text never posts a message to an application.
{
    NativeDesktop.Reset();
    NativeDesktop.TypeText("");
    _ = Fails<ProviderFailureException>(() => NativeDesktop.TypeText("\uD800"));
    Check(NativeDesktop.TextWakes.Count == 0, "Zero-input rejection posted a wake");
    cases++;
}

GuiTextIntegrationTests.Run();
Console.WriteLine($"PASS {cases} Windows GUI input and capture cases");
