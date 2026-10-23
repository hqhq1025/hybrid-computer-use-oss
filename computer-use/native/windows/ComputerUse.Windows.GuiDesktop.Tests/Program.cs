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
    Check(failure.Phase == "precondition" && !failure.InputEventsEmitted && NativeDesktop.Typed == 0 &&
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
    Check(failure.Phase == "precondition" && !failure.InputEventsEmitted && NativeDesktop.Typed == 0 &&
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

Console.WriteLine($"PASS {cases} Windows GUI input and capture cases");
