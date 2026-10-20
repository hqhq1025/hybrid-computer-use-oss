using System.Drawing;
using System.Text.Json;
using System.Text.Json.Nodes;
using ComputerUse.Windows.Runtime;

SelectionTests.Run();
if (args is ["--selection-contract"])
{
    return 0;
}
ExactFocusSettlementTests.Run();
FocusPreparationTests.Run();
LogicalWindowBoundaryTests.Run();
HandlelessSemanticWindowTests.Run();
if (OptionalRoleRegression.Run() != 0)
{
    throw new InvalidOperationException("Optional role ownership regression failed.");
}

if (args is ["--focus-preparation"])
{
    Console.WriteLine("Exact focus settlement and preparation tests passed.");
    return 0;
}

if (args.Length != 1)
{
    Console.Error.WriteLine(
        "usage: ComputerUse.Windows.ProviderV2.Tests <provider-v2-fixture-dir>");
    return 2;
}

var fixtureRoot = Path.GetFullPath(args[0]);
NativeManifestTests.Run(fixtureRoot);
var manifest = JsonNode.Parse(
    File.ReadAllText(Path.Combine(fixtureRoot, "manifest.json"))
) as JsonObject ?? throw new InvalidDataException("manifest must be an object");
var fixtures = manifest["fixtures"] as JsonArray ??
    throw new InvalidDataException("manifest fixtures must be an array");
var validCount = 0;
foreach (var node in fixtures)
{
    var fixture = node as JsonObject ??
        throw new InvalidDataException("fixture entry must be an object");
    if (fixture["direction"]?.GetValue<string>() != "request")
    {
        continue;
    }
    var file = fixture["file"]?.GetValue<string>() ??
        throw new InvalidDataException("fixture file is required");
    var request = JsonNode.Parse(
        File.ReadAllText(Path.Combine(fixtureRoot, file))
    ) as JsonObject ?? throw new InvalidDataException($"{file} must be an object");
    ProviderServer.ValidateRequest(request);
    if (request["action"]?["target"] is JsonObject target)
    {
        target["automationId"] = "save-button";
        ProviderServer.ValidateRequest(request);
        target["automationId"] = 42;
        try
        {
            ProviderServer.ValidateRequest(request);
            throw new InvalidDataException("Numeric target automationId was accepted.");
        }
        catch (ProviderFailureException)
        {
        }
    }
    validCount++;
}

var invalid = JsonNode.Parse(
    File.ReadAllText(Path.Combine(fixtureRoot, "invalid-requests.json"))
) as JsonArray ?? throw new InvalidDataException(
    "invalid-requests.json must be an array");
var rejectedCount = 0;
foreach (var node in invalid)
{
    var fixture = node as JsonObject ??
        throw new InvalidDataException("invalid fixture must be an object");
    var name = fixture["name"]?.GetValue<string>() ?? "unnamed";
    var request = fixture["request"] as JsonObject ??
        throw new InvalidDataException($"{name}: request must be an object");
    try
    {
        ProviderServer.ValidateRequest(request);
        throw new InvalidDataException(
            $"C# Provider V2 validator accepted invalid fixture: {name}");
    }
    catch (ProviderFailureException)
    {
        rejectedCount++;
    }
}

using (var automation = WindowsUiaAutomationFactory.Create())
{
    if (automation.TransactionTimeout != TimeSpan.FromSeconds(3))
    {
        throw new InvalidDataException(
            "Windows UIA transaction timeout must be exactly three seconds.");
    }
}

var ownerProjectionCandidates = new[]
{
    new OwnedNativeWindowCandidate(
        Handle: 0x10,
        OwnerHandle: 0,
        ProcessId: 41,
        IndependentWindow: true,
        Bounds: Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        Handle: 0x20,
        OwnerHandle: 0x10,
        ProcessId: 41,
        IndependentWindow: false,
        Bounds: Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        Handle: 0x21,
        OwnerHandle: 0x20,
        ProcessId: 41,
        IndependentWindow: false,
        Bounds: Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        Handle: 0x30,
        OwnerHandle: 0,
        ProcessId: 41,
        IndependentWindow: true,
        Bounds: Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        Handle: 0x31,
        OwnerHandle: 0x30,
        ProcessId: 41,
        IndependentWindow: false,
        Bounds: Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        Handle: 0x40,
        OwnerHandle: 0x10,
        ProcessId: 41,
        IndependentWindow: true,
        Bounds: Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        Handle: 0x50,
        OwnerHandle: 0x10,
        ProcessId: 42,
        IndependentWindow: false,
        Bounds: Rectangle.Empty),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ownerProjectionCandidates,
        0x20) != 0x10 ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        ownerProjectionCandidates,
        0x21) != 0x10 ||
    !OwnedNativeWindowProjection.Members(
            ownerProjectionCandidates,
            0x10)
        .SequenceEqual(new long[] { 0x10, 0x20, 0x21 }) ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        ownerProjectionCandidates,
        0x31) != 0x30 ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        ownerProjectionCandidates,
        0x40) != 0x40 ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        ownerProjectionCandidates,
        0x50) != 0x50)
{
    throw new InvalidDataException(
        "Owned native popup projection crossed an owner, process, or independent-window boundary.");
}
var cyclicOwnerCandidates = new[]
{
    new OwnedNativeWindowCandidate(
        0x60, 0x61, 51, false, Rectangle.Empty),
    new OwnedNativeWindowCandidate(
        0x61, 0x60, 51, false, Rectangle.Empty),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        cyclicOwnerCandidates,
        0x60) != 0x60)
{
    throw new InvalidDataException(
        "Owned native popup projection did not fail closed on an owner cycle.");
}

var ownerlessPopupCandidates = new[]
{
    new OwnedNativeWindowCandidate(
        0x70, 0, 61, true, new Rectangle(100, 100, 1000, 700)),
    new OwnedNativeWindowCandidate(
        0x71, 0, 61, false, new Rectangle(200, 200, 300, 400)),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ownerlessPopupCandidates,
        0x71) != 0x70 ||
    !OwnedNativeWindowProjection.Members(
            ownerlessPopupCandidates,
            0x70)
        .SequenceEqual(new long[] { 0x70, 0x71 }))
{
    throw new InvalidDataException(
        "A uniquely contained ownerless transient was not projected into its managed window.");
}

var ambiguousOwnerlessPopupCandidates = ownerlessPopupCandidates.Append(
    new OwnedNativeWindowCandidate(
        0x72, 0, 61, true, new Rectangle(150, 150, 600, 500))
).ToArray();
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousOwnerlessPopupCandidates,
        0x71) != 0x72)
{
    throw new InvalidDataException(
        "An ownerless transient did not select its nearest containing managed window.");
}
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousOwnerlessPopupCandidates,
        0x71,
        0x70) != 0x72 ||
    !OwnedNativeWindowProjection.Members(
            ambiguousOwnerlessPopupCandidates,
            0x70,
            0x70)
        .SequenceEqual(new long[] { 0x70 }))
{
    throw new InvalidDataException(
        "A requested window changed the uniquely projected transient owner.");
}
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousOwnerlessPopupCandidates,
        0x72,
        0x70) != 0x72)
{
    throw new InvalidDataException(
        "A bound logical window absorbed an independent managed window.");
}
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousOwnerlessPopupCandidates,
        0x71,
        0x71) != 0x72)
{
    throw new InvalidDataException(
        "A non-window preferred root displaced the nearest managed window.");
}

var tiedOwnerlessPopupCandidates = ownerlessPopupCandidates.Append(
    new OwnedNativeWindowCandidate(
        0x75, 0, 61, true, new Rectangle(100, 100, 1000, 700))
).ToArray();
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        tiedOwnerlessPopupCandidates,
        0x71) != 0x71)
{
    throw new InvalidDataException(
        "An ownerless transient crossed a tied spatial ownership boundary.");
}
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        tiedOwnerlessPopupCandidates,
        0x71,
        0x70) != 0x71)
{
    throw new InvalidDataException(
        "A requested window incorrectly resolved an ambiguous spatial tie.");
}

var uncontainedOwnerlessPopupCandidates = new[]
{
    ownerlessPopupCandidates[0],
    new OwnedNativeWindowCandidate(
        0x73, 0, 61, false, new Rectangle(1200, 200, 300, 400)),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        uncontainedOwnerlessPopupCandidates,
        0x73) != 0x73)
{
    throw new InvalidDataException(
        "An uncontained ownerless transient was projected into a managed window.");
}

var foreignOwnerlessPopupCandidates = new[]
{
    ownerlessPopupCandidates[0],
    new OwnedNativeWindowCandidate(
        0x74, 0, 62, false, new Rectangle(200, 200, 300, 400)),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        foreignOwnerlessPopupCandidates,
        0x74,
        0x70) != 0x74)
{
    throw new InvalidDataException(
        "A bound logical window crossed a process boundary.");
}

var sameThreadOverflowCandidates = new[]
{
    new OwnedNativeWindowCandidate(
        0x76,
        0,
        61,
        true,
        new Rectangle(100, 100, 1000, 700),
        ThreadId: 701),
    new OwnedNativeWindowCandidate(
        0x77,
        0,
        61,
        false,
        new Rectangle(300, 650, 320, 480),
        ThreadId: 701),
    new OwnedNativeWindowCandidate(
        0x78,
        0,
        61,
        true,
        new Rectangle(0, 0, 1920, 1080),
        ThreadId: 702),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        sameThreadOverflowCandidates,
        0x77) != 0x76 ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        sameThreadOverflowCandidates,
        0x77,
        0x76) != 0x76)
{
    throw new InvalidDataException(
        "A same-process ownerless transient escaped its unique GUI-thread window when its bounds overflowed that window.");
}

var ambiguousSameThreadCandidates = sameThreadOverflowCandidates.Append(
    new OwnedNativeWindowCandidate(
        0x79,
        0,
        61,
        true,
        new Rectangle(150, 150, 600, 500),
        ThreadId: 701)
).ToArray();
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousSameThreadCandidates,
        0x77) != 0x77 ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousSameThreadCandidates,
        0x77,
        0x76) != 0x77 ||
    OwnedNativeWindowProjection.ResolveLogicalHandle(
        ambiguousSameThreadCandidates,
        0x79,
        0x76) != 0x79)
{
    throw new InvalidDataException(
        "GUI-thread ownership crossed an ambiguous or independent-window boundary.");
}

var foreignSameThreadCandidates = new[]
{
    sameThreadOverflowCandidates[0],
    new OwnedNativeWindowCandidate(
        0x7a,
        0,
        62,
        false,
        new Rectangle(300, 650, 320, 480),
        ThreadId: 701),
};
if (OwnedNativeWindowProjection.ResolveLogicalHandle(
        foreignSameThreadCandidates,
        0x7a,
        0x76) != 0x7a)
{
    throw new InvalidDataException(
        "GUI-thread ownership crossed a process boundary.");
}

var projectedRootCandidates = new[]
{
    new ProjectedUiaRootCandidate(71, 0x80, "runtime:owner"),
    // ControlView exposes the tooltip but can omit the interactive popup.
    new ProjectedUiaRootCandidate(71, 0x81, "runtime:tooltip"),
    // RawView repeats the tooltip and contributes the otherwise absent menu.
    new ProjectedUiaRootCandidate(71, 0x81, "runtime:tooltip"),
    new ProjectedUiaRootCandidate(71, 0x81, "runtime:menu"),
    new ProjectedUiaRootCandidate(71, 0x81, "runtime:tooltip"),
    new ProjectedUiaRootCandidate(72, 0x81, "runtime:foreign-process"),
    new ProjectedUiaRootCandidate(71, 0x82, "runtime:foreign-hwnd"),
    new ProjectedUiaRootCandidate(71, 0x81, string.Empty),
    new ProjectedUiaRootCandidate(71, 0x81, string.Empty),
};
var selectedProjectedRoots = ProjectedUiaRootSelection.SelectIndices(
    projectedRootCandidates,
    processId: 71,
    projectedHandles: new long[] { 0x80, 0x81 });
if (!selectedProjectedRoots.SequenceEqual(new[] { 0, 1, 3, 7, 8 }))
{
    throw new InvalidDataException(
        "Projected UIA root selection lost a RawView-only sibling root, retained a cross-view duplicate runtime identity, or crossed a process/HWND boundary.");
}

var independentDialogRoots = ProjectedUiaRootSelection.SelectIndices(
    projectedRootCandidates,
    processId: 71,
    projectedHandles: new long[] { 0x82 });
if (!independentDialogRoots.SequenceEqual(new[] { 6 }))
{
    throw new InvalidDataException(
        "Projected UIA root selection absorbed or lost an independent dialog HWND.");
}

var handlelessTransientRoots = new[]
{
    new ProjectedUiaRootCandidate(
        71,
        0x80,
        "runtime:owner",
        new Rectangle(100, 100, 1000, 700),
        true),
    new ProjectedUiaRootCandidate(
        71,
        0,
        "runtime:contained-menu",
        new Rectangle(300, 300, 320, 480),
        SpatialOwnershipEligible: true),
    new ProjectedUiaRootCandidate(
        0,
        0,
        "runtime:process-neutral-contained-menu",
        new Rectangle(325, 325, 300, 450),
        SpatialOwnershipEligible: true),
    new ProjectedUiaRootCandidate(
        71,
        0x99,
        "runtime:unlisted-contained-menu",
        new Rectangle(350, 350, 300, 400),
        SpatialOwnershipEligible: true),
    new ProjectedUiaRootCandidate(
        71,
        0,
        "runtime:contained-pane",
        new Rectangle(250, 250, 500, 300)),
    new ProjectedUiaRootCandidate(
        0,
        0,
        "runtime:process-neutral-contained-pane",
        new Rectangle(275, 275, 450, 250)),
    new ProjectedUiaRootCandidate(
        71,
        0,
        "runtime:independent-dialog",
        new Rectangle(400, 400, 250, 200),
        true),
    new ProjectedUiaRootCandidate(
        71,
        0,
        "runtime:outside-menu",
        new Rectangle(1200, 300, 320, 480)),
    new ProjectedUiaRootCandidate(
        72,
        0,
        "runtime:foreign-menu",
        new Rectangle(300, 300, 320, 480),
        SpatialOwnershipEligible: true),
};
var selectedHandlelessTransientRoots =
    ProjectedUiaRootSelection.SelectIndices(
        handlelessTransientRoots,
        processId: 71,
        projectedHandles: new long[] { 0x80 },
        logicalBounds: new Rectangle(100, 100, 1000, 700));
if (!selectedHandlelessTransientRoots.SequenceEqual(new[] { 0, 1, 2, 3 }))
{
    throw new InvalidDataException(
        "Projected UIA root selection lost an eligible same-process or process-neutral spatial transient, or absorbed an ordinary pane across an independent-window, foreign-process, or geometry boundary.");
}

var readbackDelayCount = 0;
var readbackSampleCount = 0;
var liveReadbackVerified = LiveValueReadback.Verify(
    "Tamiflu",
    () =>
    {
        readbackSampleCount++;
        return "Tamiflu";
    },
    delay =>
    {
        if (delay != LiveValueReadback.SettleDelay)
        {
            throw new InvalidDataException(
                "set_value live readback used the wrong bounded barrier.");
        }
        readbackDelayCount++;
    });
if (!liveReadbackVerified ||
    readbackDelayCount != 1 ||
    readbackSampleCount != 1)
{
    throw new InvalidDataException(
        "set_value did not verify one fresh exact value after its barrier.");
}
if (LiveValueReadback.Verify(
        "expected",
        () => "different",
        _ => { }) ||
    LiveValueReadback.Verify(
        "expected",
        () => throw new InvalidOperationException("stale"),
        _ => { }))
{
    throw new InvalidDataException(
        "set_value live readback accepted a mismatch or unreadable target.");
}
var multilineValueReads = 0;
var multilineTextReads = 0;
if (!LiveValueReadback.VerifyTextValue(
        "first\r\nsecond",
        () =>
        {
            multilineValueReads++;
            return "first second";
        },
        () =>
        {
            multilineTextReads++;
            return "first\nsecond";
        },
        _ => { }) ||
    multilineValueReads != 0 ||
    multilineTextReads != 1 ||
    LiveValueReadback.VerifyTextValue(
        "first\r\nsecond",
        () => "first second",
        () => "first second",
        _ => { }) ||
    LiveValueReadback.VerifyTextValue(
        "first\r\nsecond",
        () => "first second",
        () => null,
        _ => { }) ||
    LiveValueReadback.CanonicalTextValue(
        "first second",
        "first\r\nsecond") != "first\r\nsecond" ||
    LiveValueReadback.CanonicalTextValue(
        "single line",
        "single line") != "single line")
{
    throw new InvalidDataException(
        "multiline UIA set_value did not require exact TextPattern document readback.");
}

if (!UiaSetValueRouting.PrefersRange("slider", true) ||
    !UiaSetValueRouting.PrefersRange("spinbutton", true) ||
    !UiaSetValueRouting.PrefersRange("scrollbar", true) ||
    UiaSetValueRouting.PrefersRange("combobox", true) ||
    UiaSetValueRouting.PrefersRange("slider", false))
{
    throw new InvalidDataException(
        "UIA set_value range routing changed its role or capability boundary.");
}
if (!UiaSetValueRouting.PrefersDocumentInput(
        documentControl: true,
        writableValueAvailable: true,
        textPatternAvailable: true) ||
    UiaSetValueRouting.PrefersDocumentInput(
        documentControl: false,
        writableValueAvailable: true,
        textPatternAvailable: true) ||
    UiaSetValueRouting.PrefersDocumentInput(
        documentControl: true,
        writableValueAvailable: false,
        textPatternAvailable: true) ||
    UiaSetValueRouting.PrefersDocumentInput(
        documentControl: true,
        writableValueAvailable: true,
        textPatternAvailable: false))
{
    throw new InvalidDataException(
        "UIA document set_value input routing crossed its control or pattern boundary.");
}
if (!UiaSetValueRouting.TryParseFiniteRangeValue("24", out var parsedRange) ||
    parsedRange != 24 ||
    UiaSetValueRouting.TryParseFiniteRangeValue("24px", out _) ||
    UiaSetValueRouting.TryParseFiniteRangeValue("NaN", out _) ||
    UiaSetValueRouting.TryParseFiniteRangeValue("Infinity", out _) ||
    !UiaSetValueRouting.IsWithinRange(24, 9, 72) ||
    UiaSetValueRouting.IsWithinRange(73, 9, 72))
{
    throw new InvalidDataException(
        "UIA set_value range validation accepted a non-finite, malformed, or out-of-bounds value.");
}
var rangeReadbackSamples = 0;
if (!LiveValueReadback.VerifyRange(
        24,
        () =>
        {
            rangeReadbackSamples++;
            return 24;
        },
        _ => { }) ||
    rangeReadbackSamples != 1 ||
    LiveValueReadback.VerifyRange(24, () => 23, _ => { }) ||
    LiveValueReadback.VerifyRange(24, () => null, _ => { }))
{
    throw new InvalidDataException(
        "UIA RangeValue set_value did not require one fresh exact readback.");
}

if (UiaClickRouting.Select("radio", true, true, true) is not null ||
    UiaClickRouting.Select("radio", false, true, true) is not null ||
    UiaClickRouting.Select("radio", true, true, false) !=
        UiaClickRoute.SelectionItem ||
    UiaClickRouting.Select("radio", true, false, true) !=
        UiaClickRoute.Toggle ||
    UiaClickRouting.Select("listitem", true, true, true) !=
        UiaClickRoute.SelectionItem ||
    UiaClickRouting.Select("treeitem", true, true, true) !=
        UiaClickRoute.Toggle ||
    UiaClickRouting.Select("treeitem", false, true, true) !=
        UiaClickRoute.Toggle ||
    UiaClickRouting.Select("treeitem", true, true, false) !=
        UiaClickRoute.Invoke ||
    UiaClickRouting.Select("treeitem", false, true, false) !=
        UiaClickRoute.SelectionItem ||
    UiaClickRouting.Select("checkbox", true, true, true) !=
        UiaClickRoute.Toggle ||
    UiaClickRouting.Select("switch", true, true, true) !=
        UiaClickRoute.Toggle ||
    UiaClickRouting.Select("button", true, true, true) !=
        UiaClickRoute.Invoke ||
    UiaClickRouting.Select("radio", true, false, false) !=
        UiaClickRoute.Invoke ||
    UiaClickRouting.Select("checkbox", true, false, false) !=
        UiaClickRoute.Invoke ||
    UiaClickRouting.Select("generic", false, true, false) !=
        UiaClickRoute.SelectionItem ||
    UiaClickRouting.Select("generic", false, false, true) !=
        UiaClickRoute.Toggle ||
    UiaClickRouting.Select("generic", false, false, false) is not null)
{
    throw new InvalidDataException(
        "UIA click routing lost its role-specific pattern boundary.");
}

var semanticResolutionSamples = 0;
var semanticResolutionDelays = new List<TimeSpan>();
var semanticResolutionTarget = new object();
var recoveredSemanticTarget = SemanticResolutionRetry.Resolve(
    enabled: true,
    attempt =>
    {
        semanticResolutionSamples++;
        return attempt == SemanticResolutionRetry.MaxAttempts - 1
            ? semanticResolutionTarget
            : null;
    },
    semanticResolutionDelays.Add);
if (!ReferenceEquals(recoveredSemanticTarget, semanticResolutionTarget) ||
    semanticResolutionSamples != SemanticResolutionRetry.MaxAttempts ||
    semanticResolutionDelays.Count !=
        SemanticResolutionRetry.MaxAttempts - 1 ||
    semanticResolutionDelays.Any(
        delay => delay != SemanticResolutionRetry.RetryDelay))
{
    throw new InvalidDataException(
        "Transient semantic resolution did not use its exact bounded retry policy.");
}

semanticResolutionSamples = 0;
semanticResolutionDelays.Clear();
var missingSemanticTarget = SemanticResolutionRetry.Resolve<object>(
    enabled: true,
    _ =>
    {
        semanticResolutionSamples++;
        return null;
    },
    semanticResolutionDelays.Add);
if (missingSemanticTarget is not null ||
    semanticResolutionSamples != SemanticResolutionRetry.MaxAttempts ||
    semanticResolutionDelays.Count !=
        SemanticResolutionRetry.MaxAttempts - 1)
{
    throw new InvalidDataException(
        "Missing semantic target escaped the bounded retry limit.");
}

semanticResolutionSamples = 0;
semanticResolutionDelays.Clear();
var nonRebindableTarget = SemanticResolutionRetry.Resolve<object>(
    enabled: false,
    _ =>
    {
        semanticResolutionSamples++;
        return null;
    },
    semanticResolutionDelays.Add);
if (nonRebindableTarget is not null ||
    semanticResolutionSamples != 1 ||
    semanticResolutionDelays.Count != 0)
{
    throw new InvalidDataException(
        "Non-rebindable target consumed a semantic retry barrier.");
}

var semanticResolutionContexts = new List<object>();
var initialSemanticResolutionContext = new object();
var liveSemanticResolutionTarget = new object();
var liveRootSemanticTarget =
    SemanticResolutionRetry.ResolveWithContext<object, object>(
        enabled: true,
        attempt =>
            attempt == 0
                ? initialSemanticResolutionContext
                : new object(),
        (attempt, context) =>
        {
            semanticResolutionContexts.Add(context);
            return attempt == SemanticResolutionRetry.MaxAttempts - 1
                ? liveSemanticResolutionTarget
                : null;
        },
        _ => { });
if (!ReferenceEquals(liveRootSemanticTarget, liveSemanticResolutionTarget) ||
    semanticResolutionContexts.Count !=
        SemanticResolutionRetry.MaxAttempts ||
    !ReferenceEquals(
        semanticResolutionContexts[0],
        initialSemanticResolutionContext) ||
    semanticResolutionContexts
        .Skip(1)
        .Any(context => ReferenceEquals(
            context,
            initialSemanticResolutionContext)) ||
    semanticResolutionContexts.Distinct().Count() !=
        SemanticResolutionRetry.MaxAttempts)
{
    throw new InvalidDataException(
        "Semantic retries did not reacquire a fresh provider context.");
}

var resolutionScopeActive = false;
var resolutionScopeDisposed = false;
var readableCandidate = 0;
var visitedCandidates = new List<int>();
ResolutionCandidateScope.Visit<CallbackDisposable, int>(
    () =>
    {
        resolutionScopeActive = true;
        return new CallbackDisposable(
            () =>
            {
                resolutionScopeActive = false;
                resolutionScopeDisposed = true;
            });
    },
    visit =>
    {
        foreach (var candidate in new[] { 1, 2, 3 })
        {
            readableCandidate = candidate;
            if (!visit(candidate))
            {
                break;
            }
            readableCandidate = 0;
        }
    },
    candidate =>
    {
        if (!resolutionScopeActive ||
            resolutionScopeDisposed ||
            readableCandidate != candidate)
        {
            throw new InvalidDataException(
                "Resolution candidate properties were not consumed while " +
                "that candidate remained current in the active cache scope.");
        }
        visitedCandidates.Add(candidate);
        return true;
    });
if (!visitedCandidates.SequenceEqual(new[] { 1, 2, 3 }) ||
    resolutionScopeActive ||
    !resolutionScopeDisposed)
{
    throw new InvalidDataException(
        "Resolution candidates were not streamed through their cache scope.");
}

if (!AuthorizedCanonicalResolutionPolicy
        .UseCanonicalBeforeProviderIdentity(
            semanticRebindAuthorized: true,
            providerObjectPath: "runtime:42.100.7") ||
    AuthorizedCanonicalResolutionPolicy
        .UseCanonicalBeforeProviderIdentity(
            semanticRebindAuthorized: false,
            providerObjectPath: "runtime:42.100.7") ||
    AuthorizedCanonicalResolutionPolicy
        .UseCanonicalBeforeProviderIdentity(
            semanticRebindAuthorized: true,
            providerObjectPath: "window:opaque"))
{
    throw new InvalidDataException(
        "Canonical-first routing escaped the explicitly authorized runtime path.");
}

var observedLeaseRegistry = new ObservedElementLeaseRegistry<object>();
var firstObservedElement = new object();
observedLeaseRegistry.Replace(
    processId: 41,
    processGeneration: "generation-a",
    windowHandle: 7001,
    new Dictionary<string, ObservedElementLease<object>>
    {
        ["runtime:42.100.7"] = new(
            firstObservedElement,
            new JsonObject
            {
                ["name"] = "Origin",
                ["controlType"] = "combobox",
                ["providerObjectPath"] = "runtime:42.100.7",
            }),
    });
if (!observedLeaseRegistry.TryGet(
        41,
        "generation-a",
        7001,
        "runtime:42.100.7",
        out var currentObservedLease,
        out var currentLeaseDiagnostic) ||
    currentObservedLease is null ||
    !ReferenceEquals(currentObservedLease.Element, firstObservedElement) ||
    currentLeaseDiagnostic["entry_count"]?.GetValue<int>() != 1 ||
    currentLeaseDiagnostic["process_id_matches"]?.GetValue<bool>() != true ||
    currentLeaseDiagnostic["process_generation_matches"]?.GetValue<bool>() != true ||
    currentLeaseDiagnostic["window_handle_matches"]?.GetValue<bool>() != true ||
    currentLeaseDiagnostic["provider_object_path_present"]?.GetValue<bool>() != true ||
    observedLeaseRegistry.TryGet(
        41,
        "generation-b",
        7001,
        "runtime:42.100.7",
        out _) ||
    observedLeaseRegistry.TryGet(
        41,
        "generation-a",
        7002,
        "runtime:42.100.7",
        out _))
{
    throw new InvalidDataException(
        "Observed element leases escaped their exact process/window generation.");
}
if (observedLeaseRegistry.TryGet(
        41,
        "generation-b",
        7001,
        "runtime:42.100.7",
        out _,
        out var generationMissDiagnostic) ||
    generationMissDiagnostic["process_generation_matches"]?.GetValue<bool>() != false ||
    generationMissDiagnostic["provider_object_path_present"]?.GetValue<bool>() != true)
{
    throw new InvalidDataException(
        "Observed element lease diagnostics did not isolate a generation mismatch.");
}
if (observedLeaseRegistry.TryGet(
        41,
        "generation-a",
        7001,
        "runtime:42.100.8",
        out _,
        out var pathMissDiagnostic) ||
    pathMissDiagnostic["process_generation_matches"]?.GetValue<bool>() != true ||
    pathMissDiagnostic["provider_object_path_present"]?.GetValue<bool>() != false)
{
    throw new InvalidDataException(
        "Observed element lease diagnostics did not isolate a provider-path miss.");
}
observedLeaseRegistry.Replace(
    processId: 41,
    processGeneration: "generation-a",
    windowHandle: 7001,
    new Dictionary<string, ObservedElementLease<object>>());
if (observedLeaseRegistry.TryGet(
        41,
        "generation-a",
        7001,
        "runtime:42.100.7",
        out _))
{
    throw new InvalidDataException(
        "A replaced snapshot retained an expired observed element lease.");
}

var canonicalCandidate = JsonNode.Parse(
    """
    {
      "runtimeId":[0,2,4],
      "providerObjectPath":"runtime:42.100.7",
      "name":"Origin",
      "controlType":"combobox",
      "description":"Departure airport",
      "states":"enabled focused",
      "actions":["setValue","invoke"]
    }
    """)!.AsObject();
var canonicalTarget = JsonNode.Parse(
    """
    {
      "name":"Origin",
      "hasName":true,
      "role":"combobox",
      "providerRoleAliases":["combobox","combo box"],
      "predicates":[
        {"kind":"state","value":"focused"},
        {"kind":"desc","value":"Departure airport"},
        {"kind":"action","value":"invoke"}
      ]
    }
    """)!.AsObject();
if (!CanonicalResolutionRecord.NameMatches(
        canonicalCandidate,
        canonicalTarget,
        null) ||
    !CanonicalResolutionRecord.RoleMatches(
        canonicalCandidate,
        canonicalTarget,
        null) ||
    !CanonicalResolutionRecord.PredicatesMatch(
        canonicalCandidate,
        canonicalTarget,
        null) ||
    !CanonicalResolutionRecord.TryTreePath(
        canonicalCandidate,
        out var canonicalPath) ||
    !canonicalPath.SequenceEqual(new[] { 0, 2, 4 }) ||
    !CanonicalResolutionRecord.ProviderIdentityMatches(
        canonicalCandidate,
        "runtime:42.100.7"))
{
    throw new InvalidDataException(
        "Canonical snapshot records did not preserve exact target identity.");
}
var wrongCanonicalTarget =
    (JsonObject)canonicalTarget.DeepClone();
wrongCanonicalTarget["name"] = "Destination";
if (CanonicalResolutionRecord.NameMatches(
        canonicalCandidate,
        wrongCanonicalTarget,
        null) ||
    CanonicalResolutionRecord.ProviderIdentityMatches(
        canonicalCandidate,
        "runtime:42.100.8"))
{
    throw new InvalidDataException(
        "Canonical snapshot resolution accepted a changed identity.");
}
var rawNameCandidate =
    (JsonObject)canonicalCandidate.DeepClone();
rawNameCandidate["name"] = "Where  from?";
var providerNameBoundTarget =
    (JsonObject)canonicalTarget.DeepClone();
providerNameBoundTarget["name"] = "Where from?";
providerNameBoundTarget["providerObservedName"] = "Where  from?";
providerNameBoundTarget["hasProviderObservedName"] = true;
if (!CanonicalResolutionRecord.NameMatches(
        rawNameCandidate,
        providerNameBoundTarget,
        null))
{
    throw new InvalidDataException(
        "A provider-bound raw name did not match its canonical ref identity.");
}
providerNameBoundTarget["providerObservedName"] = "Where  to?";
if (CanonicalResolutionRecord.NameMatches(
        rawNameCandidate,
        providerNameBoundTarget,
        null))
{
    throw new InvalidDataException(
        "A provider-bound target accepted a different raw provider name.");
}
providerNameBoundTarget.Remove("providerObservedName");
providerNameBoundTarget["hasProviderObservedName"] = false;
if (CanonicalResolutionRecord.NameMatches(
        rawNameCandidate,
        providerNameBoundTarget,
        null))
{
    throw new InvalidDataException(
        "An unbound selector silently received provider-name normalization.");
}
var invalidCanonicalPath =
    (JsonObject)canonicalCandidate.DeepClone();
invalidCanonicalPath["runtimeId"] = new JsonArray(1, -1);
if (CanonicalResolutionRecord.TryTreePath(
        invalidCanonicalPath,
        out _))
{
    throw new InvalidDataException(
        "Canonical snapshot resolution accepted an invalid tree path.");
}

var validSchemaCases = new[]
{
    (
        "target allowProviderRebind",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"valid-rebind",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"click",
            "app":"Editor",
            "app_identity":{
              "providerBusName":"uia3:pid:42",
              "processGeneration":"123"
            },
            "window_handle":"w2:fixture",
            "target":{
              "selector":"button \"Save\"",
              "allowProviderRebind":true,
              "providerObservedName":"Save  now",
              "hasProviderObservedName":true,
              "windowHandle":"w2:fixture",
              "hasName":false
            },
            "input":{"click_count":1}
          }
        }
        """
    ),
    (
        "empty app identity fields remain schema-valid",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"valid-empty-identity",
          "operation":"observe",
          "observe":{
            "protocol_version":2,
            "app":"Editor",
            "app_identity":{
              "providerBusName":"",
              "processGeneration":""
            },
            "window_handle":""
          }
        }
        """
    ),
    (
        "action expected window identity",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"valid-expected-window",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"keypress",
            "app":"Editor",
            "window_handle":"w2:fixture",
            "expected_window":{
              "windowHandle":"w2:fixture",
              "providerBusName":"win32:pid:42",
              "providerObjectPath":"w2:fixture",
              "processGeneration":"123",
              "role":"window",
              "name":"Editor"
            },
            "input":{"key":"CTRL+,"}
          }
        }
        """
    ),
    (
        "screenshot may appear inside a desktop batch",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"valid-screenshot-batch",
          "operation":"desktop_execute_batch",
          "desktop":{
            "protocol_version":2,
            "actions":[
              {"type":"screenshot"},
              {"type":"move","x":10,"y":20}
            ],
            "settle_ms":0,
            "frame_id":"layout-1"
          }
        }
        """
    ),
};
foreach (var (name, json) in validSchemaCases)
{
    var request = JsonNode.Parse(json) as JsonObject ??
        throw new InvalidDataException($"{name}: request must be an object");
    ProviderServer.ValidateRequest(request);
    validCount++;
}

var semanticRebindTarget = JsonNode.Parse(
    """
    {
      "selector":"listitem \"TestImage1.jpg\"",
      "locatorKind":"semantic",
      "allowProviderRebind":true,
      "providerObjectPath":"runtime:1.2.3"
    }
    """) as JsonObject ??
    throw new InvalidDataException("semantic rebind target must be an object");
if (!ProviderTargetResolutionPolicy.AllowsSemanticProviderRebind(
        semanticRebindTarget))
{
    throw new InvalidDataException(
        "Fresh semantic refs must permit an explicitly authorized provider rebind.");
}
foreach (var json in new[]
{
    """
    {
      "selector":"listitem \"TestImage1.jpg\"",
      "locatorKind":"provider_bound",
      "allowProviderRebind":true,
      "providerObjectPath":"runtime:1.2.3"
    }
    """,
    """
    {
      "selector":"listitem \"TestImage1.jpg\"",
      "locatorKind":"semantic",
      "allowProviderRebind":false,
      "providerObjectPath":"runtime:1.2.3"
    }
    """,
    """
    {
      "locatorKind":"semantic",
      "allowProviderRebind":true,
      "providerObjectPath":"runtime:1.2.3"
    }
    """,
})
{
    var target = JsonNode.Parse(json) as JsonObject ??
        throw new InvalidDataException("rebind rejection target must be an object");
    if (ProviderTargetResolutionPolicy.AllowsSemanticProviderRebind(target))
    {
        throw new InvalidDataException(
            "Provider rebind escaped its explicit semantic-selector boundary.");
    }
}

foreach (var (app, expectedCode, expectedSubject, expectRecovery) in new[]
{
    ("StaleApp", "stale_app_ref", "app", false),
    ("StaleWindow", "stale_window_ref", "window", true),
})
{
    var request = JsonNode.Parse(
        $$"""
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"{{app}}",
          "operation":"observe",
          "observe":{
            "protocol_version":2,
            "app":"{{app}}",
            "window_handle":"w2:fixture"
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException("stale identity request must be an object");
    using var server = new ProviderServer();
    var response = server.Dispatch(request);
    var accessibility = response["accessibility"] as JsonObject ??
        throw new InvalidDataException("stale identity payload is missing");
    var failure = accessibility["failure"] as JsonObject ??
        throw new InvalidDataException("stale identity failure is missing");
    if (failure["code"]?.GetValue<string>() != expectedCode ||
        failure["phase"]?.GetValue<string>() != "precondition" ||
        failure["facts_complete"]?.GetValue<bool>() != true ||
        failure["subject"]?.GetValue<string>() != expectedSubject)
    {
        throw new InvalidDataException(
            $"Canonical stale identity facts were not preserved for {app}.");
    }
    var recovery = accessibility["recovery_context"] as JsonObject;
    if (expectRecovery)
    {
        if (recovery?["sourceAppStatus"]?.GetValue<string>() != "alive" ||
            recovery["sourceWindowStatus"]?.GetValue<string>() != "gone" ||
            recovery["inventoryComplete"]?.GetValue<bool>() != true)
        {
            throw new InvalidDataException(
                "Stale window recovery inventory is incomplete.");
        }
    }
    else if (recovery is not null)
    {
        throw new InvalidDataException(
            "Stale app failure unexpectedly included window recovery context.");
    }
}

var seenResolutionPaths = new HashSet<string>(StringComparer.Ordinal);
var acceptedResolutionPaths = new List<string>();
foreach (var providerObjectPath in new[]
{
    "runtime:root",
    "runtime:duplicate",
    "runtime:duplicate",
    "runtime:duplicate",
    "runtime:target",
})
{
    if (ProviderResolutionCandidatePolicy.ShouldInclude(
            providerObjectPath,
            seenResolutionPaths))
    {
        acceptedResolutionPaths.Add(providerObjectPath);
    }
}
if (!acceptedResolutionPaths.SequenceEqual(new[]
    {
        "runtime:root",
        "runtime:duplicate",
        "runtime:target",
    }))
{
    throw new InvalidDataException(
        "Action resolution did not apply the snapshot runtime-identity deduplication rule.");
}

if (!WindowsForegroundActivationPolicy.IsVerified((nint)42, (nint)42) ||
    WindowsForegroundActivationPolicy.IsVerified((nint)42, (nint)43) ||
    WindowsForegroundActivationPolicy.IsVerified(0, 0))
{
    throw new InvalidDataException(
        "Foreground activation must require the exact non-zero target HWND.");
}
if (WindowsForegroundActivationPolicy.ShouldAttach(7, 7) ||
    WindowsForegroundActivationPolicy.ShouldAttach(0, 7) ||
    !WindowsForegroundActivationPolicy.ShouldAttach(7, 8))
{
    throw new InvalidDataException(
        "Thread-input attachment policy crossed its bounded identity rules.");
}
if (WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "tooltips_class32", 0, 0x94000000, 0x00000088) ||
    WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "TOOLTIPS_CLASS32", 0, 0, 0) ||
    WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "SALTMPSUBFRAME", (nint)0x10, 0x94000000, 0x00000088) ||
    !WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "SALTMPSUBFRAME", (nint)0x10, 0x94000000, 0x00000080) ||
    !WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "SALTMPSUBFRAME", 0, 0x94000000, 0x00000088) ||
    WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "SysShadow", 0, 0x94000000, 0x000800a8) ||
    !WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "SysShadow", 0, 0x94000000, 0x000800a0) ||
    !WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "#32768", 0, 0x94000000, 0x00000088) ||
    !WindowsDesktopLayoutIdentityPolicy.IncludesWindow(
        "Chrome_WidgetWin_1", 0, 0x16CF0000, 0x00000100))
{
    throw new InvalidDataException(
        "Desktop layout identity must ignore transient help windows while retaining interactive menus and unrelated windows.");
}

var diagnosticLayoutBefore = WindowsDesktopLayoutDiagnostics.Create(
    0,
    0,
    1920,
    1080,
    0x10,
    new[]
    {
        new WindowsDesktopLayoutWindow(
            0, 0x10, 0, 101, "MainWindow", 0, 0, 1920, 1040,
            0x16CF0000, 0x00000100),
        new WindowsDesktopLayoutWindow(
            1, 0x20, 0x10, 202, "PopupWindow", 200, 300, 400, 500,
            0x84000000, 0x00000080),
    });
var diagnosticLayoutAfter = WindowsDesktopLayoutDiagnostics.Create(
    0,
    0,
    1920,
    1080,
    0x30,
    new[]
    {
        new WindowsDesktopLayoutWindow(
            0, 0x30, 0x10, 303, "#32768", 300, 400, 200, 300,
            0x84000000, 0x08000088),
        new WindowsDesktopLayoutWindow(
            1, 0x10, 0, 101, "MainWindow", 0, 0, 1920, 1040,
            0x16CF0000, 0x00000100),
        new WindowsDesktopLayoutWindow(
            2, 0x20, 0x10, 202, "PopupWindow", 210, 300, 400, 500,
            0x84000000, 0x00000080),
    });
var diagnosticDifference = WindowsDesktopLayoutDiagnostics.Difference(
    diagnosticLayoutBefore.FrameId,
    diagnosticLayoutBefore,
    diagnosticLayoutAfter);
if (diagnosticLayoutBefore.FrameId == diagnosticLayoutAfter.FrameId ||
    diagnosticDifference["requested_layout_known"]?.GetValue<bool>() != true ||
    diagnosticDifference["foreground_changed"]?.GetValue<bool>() != true ||
    (diagnosticDifference["added"] as JsonArray)?.Count != 1 ||
    (diagnosticDifference["removed"] as JsonArray)?.Count != 0 ||
    (diagnosticDifference["changed"] as JsonArray)?.Count != 2 ||
    diagnosticDifference.ToJsonString().Contains(
        "\"owner_hwnd\":\"0x10\"",
        StringComparison.Ordinal) != true ||
    diagnosticDifference.ToJsonString().Contains(
        "\"style\":\"0x84000000\"",
        StringComparison.Ordinal) != true ||
    diagnosticDifference.ToJsonString().Contains(
        "\"extended_style\":\"0x08000088\"",
        StringComparison.Ordinal) != true ||
    diagnosticDifference.ToJsonString().Contains(
        "window title",
        StringComparison.OrdinalIgnoreCase))
{
    throw new InvalidDataException(
        "Desktop stale diagnostics did not expose a bounded private layout diff.");
}
var unknownDiagnosticDifference =
    WindowsDesktopLayoutDiagnostics.Difference(
        "expired-frame",
        null,
        diagnosticLayoutAfter);
if (unknownDiagnosticDifference["requested_layout_known"]?.GetValue<bool>() !=
        false ||
    unknownDiagnosticDifference["requested"] is not null)
{
    throw new InvalidDataException(
        "Expired desktop frame diagnostics must remain explicit and bounded.");
}

if (!WindowsWindowIdentityPolicy.Matches(
        (nint)42,
        100,
        "process-generation",
        7,
        (nint)42,
        100,
        "process-generation",
        7) ||
    WindowsWindowIdentityPolicy.Matches(
        (nint)42,
        100,
        "process-generation",
        7,
        (nint)43,
        100,
        "process-generation",
        7) ||
    WindowsWindowIdentityPolicy.Matches(
        (nint)42,
        100,
        "process-generation",
        7,
        (nint)42,
        100,
        "process-generation",
        8))
{
    throw new InvalidDataException(
        "Stable window identity did not preserve HWND and lifecycle boundaries.");
}

var desktopBounds = new System.Drawing.Rectangle(0, 0, 1280, 800);
if (!WindowsCaptureBounds.TryResolve(
        desktopBounds,
        new System.Drawing.Rectangle(1200, 700, 200, 200),
        out var clippedCaptureBounds) ||
    clippedCaptureBounds != new System.Drawing.Rectangle(1200, 700, 80, 100) ||
    WindowsCaptureBounds.TryResolve(
        desktopBounds,
        new System.Drawing.Rectangle(-32000, -32000, 160, 28),
        out _))
{
    throw new InvalidDataException(
        "Capture bounds did not distinguish clipped and unavailable windows.");
}

var nativeSize = WindowsDesktopImageGeometry.DeliveredSize(1280, 800);
var fullHdSize = WindowsDesktopImageGeometry.DeliveredSize(1920, 1080);
var fourKSize = WindowsDesktopImageGeometry.DeliveredSize(3840, 2160);
var ultrawideSize = WindowsDesktopImageGeometry.DeliveredSize(3440, 1440);
var portraitSize = WindowsDesktopImageGeometry.DeliveredSize(1440, 2560);
if (nativeSize != new System.Drawing.Size(1229, 768) ||
    fullHdSize != new System.Drawing.Size(1365, 768) ||
    fourKSize != new System.Drawing.Size(1365, 768) ||
    ultrawideSize != new System.Drawing.Size(1366, 572) ||
    portraitSize != new System.Drawing.Size(432, 768))
{
    throw new InvalidDataException(
        "Windows desktop delivery did not preserve aspect ratio within 1366x768.");
}

var mappedOrigin = WindowsDesktopImageGeometry.ToNativeCoordinate(
    0,
    1920,
    -1920,
    3840);
var mappedCenter = WindowsDesktopImageGeometry.ToNativeCoordinate(
    959.5,
    1920,
    -1920,
    3840);
var mappedLast = WindowsDesktopImageGeometry.ToNativeCoordinate(
    1919,
    1920,
    -1920,
    3840);
if (mappedOrigin != -1919.5 ||
    mappedCenter != -0.5 ||
    mappedLast != 1918.5)
{
    throw new InvalidDataException(
        "Delivered-image coordinates did not map through native pixel centers.");
}

var validIntents = new Dictionary<string, string>
{
    ["resolve_target"] = """
        "target":{"selector":"button \"Save\"","hasName":false}
        """,
    ["click"] = """
        "target":{"selector":"button \"Save\"","hasName":false},
        "input":{"click_count":1}
        """,
    ["scroll"] = """
        "window_handle":"w2:fixture",
        "input":{"direction":"down","pages":1}
        """,
    ["type"] = """
        "window_handle":"w2:fixture",
        "input":{"text":"hello"}
        """,
    ["keypress"] = """
        "window_handle":"w2:fixture",
        "input":{"key":"ENTER"}
        """,
    ["set_value"] = """
        "target":{"selector":"entry \"Name\"","hasName":false},
        "input":{"value":"Ada"}
        """,
};
foreach (var (intent, body) in validIntents)
{
    var request = JsonNode.Parse(
        $$"""
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"valid-intent-{{intent}}",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"{{intent}}",
            "app":"Editor",
            {{body}}
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException($"{intent}: request must be an object");
    ProviderServer.ValidateRequest(request);
    validCount++;
}

var axSettingsChord = WindowsKeyMapping.ResolveChord(
    new[] { "CTRL+," });
if (!axSettingsChord.SequenceEqual(new ushort[] { 0x11, 0xBC }))
{
    throw new InvalidDataException(
        "AX chord encoding did not resolve CTRL+COMMA.");
}

var toggleStates = new Dictionary<string, string>
{
    ["On"] = "checked",
    ["Off"] = "unchecked",
    ["Indeterminate"] = "mixed",
};
foreach (var (state, expected) in toggleStates)
{
    var actual = WindowsToggleState.Canonicalize(state);
    if (actual != expected)
    {
        throw new InvalidDataException(
            $"Toggle state {state} serialized as {actual}, expected {expected}.");
    }
}

var windowBounds = new System.Drawing.Rectangle(100, 100, 800, 600);
var zeroPointerDispatch = new ProviderFailureException(
    "input_dispatch_failed", "emission", "SendInput delivered zero events.");
var evidenceOnlyDispatch = UiaMutationFailureAttribution.Preserve(
    new ProviderFailureException("dispatch_failed", "emission", "input in evidence",
        actionEvidence: new JsonObject { ["input_emitted"] = true }),
    false, new JsonObject { ["input_emitted"] = false });
if (!evidenceOnlyDispatch.InputEventsEmitted ||
    evidenceOnlyDispatch.ActionEvidence!["input_emitted"]!.GetValue<bool>() != true)
{
    throw new InvalidDataException("Action failure rebuild erased emitted input evidence.");
}
if (!PointerPreparationFailureAttribution.HasKnownZeroRequestedInput(
        zeroPointerDispatch, true, false) ||
    !PointerPreparationFailureAttribution.HasKnownZeroRequestedInput(
        new InvalidOperationException("hit failed"), false, false))
{
    throw new InvalidDataException("Known zero pointer delivery lost activation uncertainty.");
}
foreach (var ambiguousPointerFailure in new Exception[]
{
    new InvalidOperationException("dispatch completion unknown"),
    new ProviderFailureException("partial", "emission", "partial", inputEventsEmitted: true),
    new ProviderFailureException("unknown", "emission", "unknown", mutationUnknown: true),
    new ProviderFailureException("evidence", "emission", "evidence",
        actionEvidence: new JsonObject { ["input_emitted"] = true }),
})
{
    if (PointerPreparationFailureAttribution.HasKnownZeroRequestedInput(
            ambiguousPointerFailure, true, false))
    {
        throw new InvalidDataException("Uncertain pointer delivery was changed into zero delivery.");
    }
}
var zeroPointerPreparation = PointerPreparationFailureAttribution.FromZeroInputError(
    zeroPointerDispatch, new JsonObject { ["input_emitted"] = false });
if (zeroPointerPreparation.InputEventsEmitted ||
    !zeroPointerPreparation.MutationUnknown ||
    zeroPointerPreparation.FactsComplete != false ||
    zeroPointerPreparation.PointerPreparation !=
        PointerPreparationState.ActivationAttemptedOutcomeUnknown ||
    zeroPointerPreparation.Code != "pointer_preparation_failed")
{
    throw new InvalidDataException("Zero pointer failure did not preserve preparation facts.");
}
foreach (var unknownRead in new[]
{
    WindowsVisibilityRead.Read(() => (false, false)),
    WindowsVisibilityRead.Read(() => (false, true)),
    WindowsVisibilityRead.Read(() => throw new InvalidOperationException("client read failure")),
})
{
    if (unknownRead.IsOffscreen is not null ||
        unknownRead.EffectiveVisibility(true, windowBounds, windowBounds) is not null)
    {
        throw new InvalidDataException("Unknown visibility became definite via focus or default value.");
    }
    var inputCount = 0;
    try
    {
        _ = WindowsPointerPreflight.Verify(
            () => unknownRead.IsOffscreen,
            () => true, () => windowBounds, () => { }, _ => true, _ => true);
        inputCount++;
    }
    catch (PointerPreflightException error) when (error.Code == "visibility_unknown") { }
    if (inputCount != 0) { throw new InvalidDataException("Unknown visibility authorized pointer input."); }
}
if (!WindowsUiaVisibility.IsEffectivelyVisible(
        false,
        false,
        System.Drawing.Rectangle.Empty,
        windowBounds))
{
    throw new InvalidDataException(
        "A UIA element reported on-screen was marked invisible.");
}
if (!WindowsUiaVisibility.IsEffectivelyVisible(
        true,
        true,
        new System.Drawing.Rectangle(120, 140, 400, 240),
        windowBounds))
{
    throw new InvalidDataException(
        "A focused, bounded element intersecting its window was marked invisible.");
}
if (WindowsUiaVisibility.IsEffectivelyVisible(
        true,
        false,
        new System.Drawing.Rectangle(120, 140, 400, 240),
        windowBounds))
{
    throw new InvalidDataException(
        "An unfocused UIA-offscreen element was marked visible.");
}
if (WindowsUiaVisibility.IsEffectivelyVisible(
        true,
        true,
        new System.Drawing.Rectangle(1000, 1000, 400, 240),
        windowBounds))
{
    throw new InvalidDataException(
        "A focused element outside its owning window was marked visible.");
}
if (WindowsUiaVisibility.IsEffectivelyVisible(
        true,
        true,
        System.Drawing.Rectangle.Empty,
        windowBounds))
{
    throw new InvalidDataException(
        "A focused element without geometry was marked visible.");
}

var guiSettingsChord = WindowsKeyMapping.ResolveChord(
    new[] { "CTRL", "," });
if (!guiSettingsChord.SequenceEqual(axSettingsChord))
{
    throw new InvalidDataException(
        "AX and GUI chord forms resolved differently.");
}
if (!WindowsKeyMapping.ResolveChord(
        new[] { "CTRL", "SHIFT", "PERIOD" })
    .SequenceEqual(new ushort[] { 0x11, 0x10, 0xBE }))
{
    throw new InvalidDataException(
        "Named OEM punctuation did not resolve.");
}
try
{
    _ = WindowsKeyMapping.ResolveChord(new[] { "CTRL+NOT_A_REAL_KEY" });
    throw new InvalidDataException("Unsupported key was accepted.");
}
catch (InvalidOperationException error)
    when (error.Message == "unsupported_key:NOT_A_REAL_KEY")
{
}

var currentWindowExpectation = JsonNode.Parse(
    """
    {
      "windowHandle":"w2:fixture",
      "providerBusName":"win32:pid:42",
      "providerObjectPath":"w2:fixture",
      "processGeneration":"123",
      "role":"window",
      "name":"Old title"
    }
    """) as JsonObject ??
    throw new InvalidDataException("window expectation must be an object");
WindowsWindowExpectation.ValidateCurrent(
    currentWindowExpectation,
    "w2:fixture",
    "w2:fixture",
    "win32:pid:42",
    "w2:fixture",
    "123",
    "window");
try
{
    WindowsWindowExpectation.ValidateCurrent(
        currentWindowExpectation,
        "w2:fixture",
        "w2:fixture",
        "win32:pid:42",
        "w2:fixture",
        "replacement-generation",
        "window");
    throw new InvalidDataException(
        "Stale expected window identity was accepted.");
}
catch (ProviderFailureException error)
    when (error.Code == "stale_window_ref" &&
          error.Phase == "precondition" &&
          !error.InputEventsEmitted &&
          !error.MutationUnknown)
{
}

var captureSnapshotRequest = JsonNode.Parse(
    """
    {
      "protocol":"computer-use-provider-v2",
      "schema_version":2,
      "request_id":"valid-capture-snapshot",
      "operation":"resolve_and_act",
      "action":{
        "protocol_version":2,
        "intent":"click",
        "app":"Editor",
        "target":{"selector":"button \"Save\"","hasName":false},
        "input":{"click_count":1},
        "observation":{"capture_snapshot":false}
      }
    }
    """) as JsonObject ??
    throw new InvalidDataException("capture snapshot request must be an object");
ProviderServer.ValidateRequest(captureSnapshotRequest);
validCount++;

var invalidSchemaCases = new[]
{
    (
        "target rejects unknown field",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-target-field",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"click",
            "app":"Editor",
            "target":{
              "selector":"button \"Save\"",
              "allowProviderRebind":true,
              "compatibilityTarget":"hidden",
              "hasName":false
            },
            "input":{"click_count":1}
          }
        }
        """
    ),
    (
        "target rejects string allowProviderRebind",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-rebind-type",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"click",
            "app":"Editor",
            "target":{
              "selector":"button \"Save\"",
              "allowProviderRebind":"true",
              "hasName":false
            },
            "input":{"click_count":1}
          }
        }
        """
    ),
    (
        "expected window rejects missing process generation",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-expected-window",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"keypress",
            "app":"Editor",
            "window_handle":"w2:fixture",
            "expected_window":{
              "windowHandle":"w2:fixture",
              "providerBusName":"win32:pid:42",
              "role":"window"
            },
            "input":{"key":"CTRL+,"}
          }
        }
        """
    ),
    (
        "observe rejects numeric window_handle",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-window-handle",
          "operation":"observe",
          "observe":{
            "protocol_version":2,
            "app":"Editor",
            "window_handle":42
          }
        }
        """
    ),
    (
        "desktop rejects numeric frame_id",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-frame-id",
          "operation":"desktop_execute_batch",
          "desktop":{
            "protocol_version":2,
            "actions":[{"type":"screenshot"}],
            "settle_ms":0,
            "frame_id":42
          }
        }
        """
    ),
    (
        "desktop rejects hidden action field",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-hidden-action",
          "operation":"desktop_execute_batch",
          "desktop":{
            "protocol_version":2,
            "actions":[{"type":"wait","seconds":1}],
            "settle_ms":0
          }
        }
        """
    ),
    (
        "seventh AX intent remains rejected",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-extra-intent",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"invoke",
            "app":"Editor",
            "target":{"selector":"button \"Save\"","hasName":false}
          }
        }
        """
    ),
    (
        "seventh operation remains rejected",
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"invalid-extra-operation",
          "operation":"shutdown",
          "observe_apps":{"protocol_version":2}
        }
        """
    ),
};
foreach (var (name, json) in invalidSchemaCases)
{
    var request = JsonNode.Parse(json) as JsonObject ??
        throw new InvalidDataException($"{name}: request must be an object");
    try
    {
        ProviderServer.ValidateRequest(request);
        throw new InvalidDataException(
            $"C# Provider V2 validator accepted invalid fixture: {name}");
    }
    catch (ProviderFailureException)
    {
        rejectedCount++;
    }
}

var observationAttempts = 0;
var observationDelays = new List<TimeSpan>();
var observedFailureAttempts = new List<int>();
var recoveredObservation = BoundedObservationRetry.Execute(
    () =>
    {
        observationAttempts++;
        if (observationAttempts == 1)
        {
            throw new InvalidOperationException("transient snapshot failure");
        }
        return "current-state";
    },
    observationDelays.Add,
    (attempt, _) => observedFailureAttempts.Add(attempt));
if (recoveredObservation != "current-state" ||
    observationAttempts != 2 ||
    observationDelays.Count != 1 ||
    observationDelays[0] != TimeSpan.FromMilliseconds(75) ||
    observedFailureAttempts.Count != 1 ||
    observedFailureAttempts[0] != 1)
{
    throw new InvalidDataException(
        "Bounded post-action observation did not recover exactly once.");
}

observationAttempts = 0;
observationDelays.Clear();
observedFailureAttempts.Clear();
try
{
    _ = BoundedObservationRetry.Execute<string>(
        () =>
        {
            observationAttempts++;
            throw new InvalidOperationException(
                "persistent snapshot failure");
        },
        observationDelays.Add,
        (attempt, _) => observedFailureAttempts.Add(attempt));
    throw new InvalidDataException(
        "Exhausted post-action observation retry unexpectedly succeeded.");
}
catch (InvalidOperationException error)
    when (error.Message == "persistent snapshot failure")
{
}
if (observationAttempts != 3 ||
    observationDelays.Count != 2 ||
    observationDelays[0] != TimeSpan.FromMilliseconds(75) ||
    observationDelays[1] != TimeSpan.FromMilliseconds(150) ||
    !observedFailureAttempts.SequenceEqual(new[] { 1, 2, 3 }))
{
    throw new InvalidDataException(
        "Bounded post-action observation exceeded or shortened its retry budget.");
}

var windowEnumerations = 0;
var selectedWindowHandles = new List<long>();
observationDelays.Clear();
observedFailureAttempts.Clear();
var transitionedObservation = PostActionObservationRetry.Execute(
    () =>
    {
        windowEnumerations++;
        return windowEnumerations == 1
            ? new[]
            {
                new TestWindow(101, true),
                new TestWindow(202, false),
            }
            : new[]
            {
                new TestWindow(202, true),
            };
    },
    window => window.Handle,
    window => window.Active,
    101,
    (window, _) =>
    {
        selectedWindowHandles.Add(window.Handle);
        if (window.Handle == 101)
        {
            throw new InvalidOperationException(
                "source window closed during snapshot");
        }
        return $"window:{window.Handle}";
    },
    () => "window-absent",
    observationDelays.Add,
    (attempt, _, _) => observedFailureAttempts.Add(attempt));
if (transitionedObservation != "window:202" ||
    windowEnumerations != 2 ||
    !selectedWindowHandles.SequenceEqual(new long[] { 101, 202 }) ||
    observationDelays.Count != 1 ||
    observationDelays[0] != TimeSpan.FromMilliseconds(75) ||
    !observedFailureAttempts.SequenceEqual(new[] { 1 }))
{
    throw new InvalidDataException(
        "Post-action observation did not re-enumerate and follow the current window.");
}

selectedWindowHandles.Clear();
windowEnumerations = 0;
observationDelays.Clear();
var activeReplacementObservation = PostActionObservationRetry.Execute(
    () =>
    {
        windowEnumerations++;
        return windowEnumerations < 3
            ? new[]
            {
                new TestWindow(101, false, 202),
                new TestWindow(202, true),
            }
            : new[]
        {
            new TestWindow(202, true),
        };
    },
    window => window.Handle,
    window => window.Active,
    101,
    (window, originalOwnedWindowRetired) =>
    {
        selectedWindowHandles.Add(window.Handle);
        if (!originalOwnedWindowRetired)
        {
            throw new InvalidDataException(
                "Retired owned-window transition was not surfaced to the snapshot.");
        }
        return $"window:{window.Handle}";
    },
    () => "window-absent",
    observationDelays.Add,
    windowOwnerHandle: window => window.OwnerHandle);
if (activeReplacementObservation != "window:202" ||
    windowEnumerations != 3 ||
    !selectedWindowHandles.SequenceEqual(new long[] { 202 }) ||
    !observationDelays.SequenceEqual(
        new[]
        {
            TimeSpan.FromMilliseconds(100),
            TimeSpan.FromMilliseconds(100),
        }))
{
    throw new InvalidDataException(
        "Post-action observation did not wait for an owned window to retire " +
        "and mark the final snapshot for visual stabilization.");
}

selectedWindowHandles.Clear();
windowEnumerations = 0;
observationDelays.Clear();
var delayedOwnedTransitionObservation = PostActionObservationRetry.Execute(
    () =>
    {
        windowEnumerations++;
        return windowEnumerations switch
        {
            1 or 2 => new[]
            {
                new TestWindow(101, true, 202),
                new TestWindow(202, false),
            },
            3 => new[]
            {
                new TestWindow(101, false, 202),
                new TestWindow(202, true),
            },
            _ => new[]
            {
                new TestWindow(202, true),
            },
        };
    },
    window => window.Handle,
    window => window.Active,
    101,
    (window, originalOwnedWindowRetired) =>
    {
        selectedWindowHandles.Add(window.Handle);
        if (!originalOwnedWindowRetired)
        {
            throw new InvalidDataException(
                "Delayed retired owned-window transition was not surfaced.");
        }
        return $"window:{window.Handle}";
    },
    () => "window-absent",
    observationDelays.Add,
    windowOwnerHandle: window => window.OwnerHandle);
if (delayedOwnedTransitionObservation != "window:202" ||
    windowEnumerations != 4 ||
    !selectedWindowHandles.SequenceEqual(new long[] { 202 }) ||
    observationDelays.Count != 3 ||
    observationDelays.Any(
        value => value != TimeSpan.FromMilliseconds(100)))
{
    throw new InvalidDataException(
        "Post-action observation did not detect a delayed owned-window " +
        "transition before waiting for retirement.");
}

selectedWindowHandles.Clear();
windowEnumerations = 0;
observationDelays.Clear();
var independentWindowObservation = PostActionObservationRetry.Execute(
    () =>
    {
        windowEnumerations++;
        return new[]
        {
            new TestWindow(101, false),
            new TestWindow(202, true),
        };
    },
    window => window.Handle,
    window => window.Active,
    101,
    (window, _) =>
    {
        selectedWindowHandles.Add(window.Handle);
        return $"window:{window.Handle}";
    },
    () => "window-absent",
    observationDelays.Add,
    windowOwnerHandle: window => window.OwnerHandle);
if (independentWindowObservation != "window:202" ||
    windowEnumerations != 1 ||
    !selectedWindowHandles.SequenceEqual(new long[] { 202 }) ||
    observationDelays.Count != 0)
{
    throw new InvalidDataException(
        "Post-action observation delayed an unrelated same-process window switch.");
}

selectedWindowHandles.Clear();
windowEnumerations = 0;
observationDelays.Clear();
var boundedOwnedWindowObservation = PostActionObservationRetry.Execute(
    () =>
    {
        windowEnumerations++;
        return new[]
        {
            new TestWindow(101, false, 202),
            new TestWindow(202, true),
        };
    },
    window => window.Handle,
    window => window.Active,
    101,
    (window, originalOwnedWindowRetired) =>
    {
        selectedWindowHandles.Add(window.Handle);
        if (originalOwnedWindowRetired)
        {
            throw new InvalidDataException(
                "Still-present owned window was reported as retired.");
        }
        return $"window:{window.Handle}";
    },
    () => "window-absent",
    observationDelays.Add,
    windowOwnerHandle: window => window.OwnerHandle);
if (boundedOwnedWindowObservation != "window:202" ||
    windowEnumerations != 21 ||
    !selectedWindowHandles.SequenceEqual(new long[] { 202 }) ||
    observationDelays.Count != 20 ||
    observationDelays.Any(
        value => value != TimeSpan.FromMilliseconds(100)))
{
    throw new InvalidDataException(
        "Owned-window retirement polling was not bounded to two seconds.");
}

selectedWindowHandles.Clear();
windowEnumerations = 0;
observationDelays.Clear();
var crossProcessOwnerObservation = PostActionObservationRetry.Execute(
    () =>
    {
        windowEnumerations++;
        return new[]
        {
            new TestWindow(202, true, ProcessId: 22),
        };
    },
    window => window.Handle,
    window => window.Active,
    101,
    (window, originalOwnedWindowRetired) =>
    {
        selectedWindowHandles.Add(window.Handle);
        if (!originalOwnedWindowRetired)
        {
            throw new InvalidDataException(
                "An already-retired cross-process owned window was not marked retired.");
        }
        return $"window:{window.Handle}";
    },
    () => "window-absent",
    observationDelays.Add,
    windowOwnerHandle: window => window.OwnerHandle,
    originalOwnerHandle: 202);
if (crossProcessOwnerObservation != "window:202" ||
    windowEnumerations != 1 ||
    !selectedWindowHandles.SequenceEqual(new long[] { 202 }) ||
    observationDelays.Count != 0)
{
    throw new InvalidDataException(
        "Post-action observation did not follow an exact owner after the " +
        "cross-process source window had already retired.");
}

var ownerIdentity = new PostActionOwnerIdentity(
    (nint)202,
    22,
    "pid:22:start:100",
    7);
if (!PostActionOwnerTransitionPolicy.Matches(
        ownerIdentity,
        (nint)202,
        22,
        "pid:22:start:100",
        7))
{
    throw new InvalidDataException(
        "The exact cross-process owner identity was rejected.");
}
var capturedOwnerHandle = (nint)0;
var capturedOwner = PostActionOwnerTransitionPolicy.Capture(
    (nint)202,
    handle =>
    {
        capturedOwnerHandle = handle;
        return 22;
    },
    processId =>
        processId == 22 ? "pid:22:start:100" : null,
    handle => handle == (nint)202 ? 7 : -1);
if (capturedOwner is null ||
    capturedOwnerHandle != (nint)202 ||
    capturedOwner.Handle != (nint)202 ||
    capturedOwner.ProcessId != 22 ||
    capturedOwner.ProcessGeneration != "pid:22:start:100" ||
    capturedOwner.LifecycleGeneration != 7)
{
    throw new InvalidDataException(
        "Exact owner capture incorrectly required visible-window inventory.");
}
if (PostActionOwnerTransitionPolicy.Capture(
        (nint)202,
        _ => null,
        _ => "pid:22:start:100",
        _ => 7) is not null)
{
    throw new InvalidDataException(
        "Owner capture accepted an unresolved native process identity.");
}
foreach (var mismatch in new[]
{
    ((nint)203, 22, "pid:22:start:100", 7L),
    ((nint)202, 23, "pid:22:start:100", 7L),
    ((nint)202, 22, "pid:22:start:101", 7L),
    ((nint)202, 22, "pid:22:start:100", 8L),
})
{
    if (PostActionOwnerTransitionPolicy.Matches(
        ownerIdentity,
        mismatch.Item1,
        mismatch.Item2,
        mismatch.Item3,
        mismatch.Item4))
    {
        throw new InvalidDataException(
            "A changed cross-process owner identity was accepted.");
    }
}

var redrawCalls = 0;
var compositionFlushCalls = 0;
var committedCaptureCalls = 0;
var renderCommitDelays = new List<TimeSpan>();
var committedWindowObservation = WindowRenderCommit.Capture(
    () =>
    {
        redrawCalls++;
        return true;
    },
    () =>
    {
        compositionFlushCalls++;
        return 0;
    },
    () => $"committed-frame-{++committedCaptureCalls}",
    renderCommitDelays.Add);
if (committedWindowObservation != "committed-frame-3" ||
    redrawCalls != 3 ||
    compositionFlushCalls != 3 ||
    committedCaptureCalls != 3 ||
    renderCommitDelays.Count != 3 ||
    renderCommitDelays.Any(
        value => value != TimeSpan.FromMilliseconds(100)))
{
    throw new InvalidDataException(
        "Target-window capture did not require bounded redraw and DWM commits.");
}

try
{
    _ = WindowRenderCommit.Capture(
        () => false,
        () => 0,
        () => "unexpected",
        _ => { });
    throw new InvalidDataException(
        "Target-window capture accepted a failed redraw.");
}
catch (InvalidOperationException error) when (
    error.Message == "window_redraw_failed")
{
}

try
{
    _ = WindowRenderCommit.Capture(
        () => true,
        () => unchecked((int)0x80263001),
        () => "unexpected",
        _ => { });
    throw new InvalidDataException(
        "Target-window capture accepted a failed DWM composition flush.");
}
catch (InvalidOperationException error) when (
    error.Message == "desktop_composition_flush_failed")
{
}

var desktopCaptureAttempts = 0;
observationDelays.Clear();
observedFailureAttempts.Clear();
var recoveredDesktopObservation = DesktopObservationRetry.Execute(
    () =>
    {
        desktopCaptureAttempts++;
        if (desktopCaptureAttempts == 1)
        {
            throw new InvalidOperationException(
                "transient desktop capture failure");
        }
        return "current-desktop";
    },
    observationDelays.Add,
    (attempt, _) => observedFailureAttempts.Add(attempt));
if (recoveredDesktopObservation != "current-desktop" ||
    desktopCaptureAttempts != 2 ||
    observationDelays.Count != 1 ||
    observationDelays[0] != TimeSpan.FromMilliseconds(75) ||
    !observedFailureAttempts.SequenceEqual(new[] { 1 }))
{
    throw new InvalidDataException(
        "Desktop post-action observation did not recover without replay.");
}

desktopCaptureAttempts = 0;
observationDelays.Clear();
observedFailureAttempts.Clear();
try
{
    _ = DesktopObservationRetry.Execute<string>(
        () =>
        {
            desktopCaptureAttempts++;
            throw new InvalidOperationException(
                "persistent desktop capture failure");
        },
        observationDelays.Add,
        (attempt, _) => observedFailureAttempts.Add(attempt));
    throw new InvalidDataException(
        "Exhausted desktop post-action observation unexpectedly succeeded.");
}
catch (InvalidOperationException error)
    when (error.Message == "persistent desktop capture failure")
{
}
if (desktopCaptureAttempts != 3 ||
    observationDelays.Count != 2 ||
    observationDelays[0] != TimeSpan.FromMilliseconds(75) ||
    observationDelays[1] != TimeSpan.FromMilliseconds(150) ||
    !observedFailureAttempts.SequenceEqual(new[] { 1, 2, 3 }))
{
    throw new InvalidDataException(
        "Desktop post-action observation exceeded or shortened its retry budget.");
}

using (var server = new ProviderServer())
{
    var staleDesktop = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"stale-desktop",
          "operation":"desktop_execute_batch",
          "desktop":{
            "protocol_version":2,
            "actions":[{"type":"click","x":10,"y":20}],
            "settle_ms":0,
            "frame_id":"stale-fixture"
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException(
            "stale desktop request must be an object");
    var staleDesktopResponse = server.Dispatch(staleDesktop);
    var staleDesktopPayload = staleDesktopResponse["desktop"] as JsonObject ??
        throw new InvalidDataException("stale desktop payload is missing");
    var staleDesktopFailure =
        staleDesktopPayload["failure"] as JsonObject ??
        throw new InvalidDataException("stale desktop failure is missing");
    var staleDesktopMutation =
        staleDesktopPayload["mutation"] as JsonObject ??
        throw new InvalidDataException("stale desktop mutation is missing");
    var staleDesktopSnapshot =
        staleDesktopPayload["snapshot"] as JsonObject ??
        throw new InvalidDataException(
            "stale desktop recovery snapshot is missing");
    var staleDesktopResult =
        staleDesktopPayload["gui_result"] as JsonObject ??
        throw new InvalidDataException("stale desktop result is missing");
    if (staleDesktopFailure["code"]?.GetValue<string>() != "stale_layout" ||
        staleDesktopFailure["phase"]?.GetValue<string>() != "precondition" ||
        staleDesktopFailure["facts_complete"]?.GetValue<bool>() != true ||
        staleDesktopMutation["input_events_emitted"]?.GetValue<bool>() !=
            false ||
        staleDesktopMutation["partial_input_possible"]?.GetValue<bool>() !=
            false ||
        staleDesktopMutation["mutation_unknown"]?.GetValue<bool>() != false ||
        staleDesktopSnapshot["frameId"]?.GetValue<string>() !=
            "fresh-fixture" ||
        staleDesktopSnapshot["width"]?.GetValue<int>() != 1920 ||
        staleDesktopSnapshot["height"]?.GetValue<int>() != 1080 ||
        staleDesktopResult["quiescent"]?.GetValue<bool>() != true)
    {
        throw new InvalidDataException(
            "Stale desktop failure did not return a fresh zero-input recovery snapshot.");
    }

    var staleAfterInput = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"stale-after-input",
          "operation":"desktop_execute_batch",
          "desktop":{
            "protocol_version":2,
            "actions":[{"type":"keypress","keys":["ESC"]},{"type":"click","x":10,"y":20}],
            "settle_ms":0,
            "frame_id":"stale-after-input-fixture"
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException(
            "stale-after-input request must be an object");
    var staleAfterInputResponse = server.Dispatch(staleAfterInput);
    var staleAfterInputPayload =
        staleAfterInputResponse["desktop"] as JsonObject ??
        throw new InvalidDataException(
            "stale-after-input payload is missing");
    var staleAfterInputFailure =
        staleAfterInputPayload["failure"] as JsonObject ??
        throw new InvalidDataException(
            "stale-after-input failure is missing");
    var staleAfterInputSnapshot =
        staleAfterInputPayload["snapshot"] as JsonObject ??
        throw new InvalidDataException(
            "stale-after-input recovery snapshot is missing");
    if (staleAfterInputFailure["code"]?.GetValue<string>() !=
            "effect_unknown" ||
        staleAfterInputFailure["underlying_code"]?.GetValue<string>() !=
            "stale_layout" ||
        staleAfterInputSnapshot["frameId"]?.GetValue<string>() !=
            "fresh-fixture")
    {
        throw new InvalidDataException(
            "Mid-batch stale layout did not preserve uncertainty with a fresh snapshot.");
    }

    var internalFailure = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"internal-reset",
          "operation":"observe_apps",
          "observe_apps":{"protocol_version":2}
        }
        """) as JsonObject ??
        throw new InvalidDataException("internal failure request must be an object");
    var response = server.Dispatch(internalFailure);
    if (response["provider_reset_required"]?.GetValue<bool>() != true)
    {
        throw new InvalidDataException(
            "Unexpected provider exceptions must require provider reset.");
    }

    var evidenceFailure = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"evidence-failure",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"set_value",
            "app":"EvidenceFailure",
            "target":{
              "selector":"entry \"Name\"",
              "hasName":false
            },
            "input":{"value":"Ada"}
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException("evidence failure request must be an object");
    var evidenceResponse = server.Dispatch(evidenceFailure);
    var accessibility = evidenceResponse["accessibility"] as JsonObject ??
        throw new InvalidDataException("evidence failure payload is missing");
    var failure = accessibility["failure"] as JsonObject ??
        throw new InvalidDataException("evidence failure object is missing");
    var mutation = accessibility["mutation"] as JsonObject ??
        throw new InvalidDataException("evidence mutation object is missing");
    var evidence = accessibility["action_evidence"] as JsonObject ??
        throw new InvalidDataException("action evidence is missing");
    if (failure["code"]?.GetValue<string>() != "effect_unknown" ||
        failure["underlying_code"]?.GetValue<string>() != "native_write_failed" ||
        mutation["input_events_emitted"]?.GetValue<bool>() != true ||
        mutation["mutation_unknown"]?.GetValue<bool>() != true ||
        evidence["input_emitted"]?.GetValue<bool>() != true)
    {
        throw new InvalidDataException(
            "Post-emission failure evidence is not monotonic.");
    }
    if (evidenceResponse["provider_reset_required"] is not null)
    {
        throw new InvalidDataException(
            "Typed operation failures must not force a provider reset.");
    }

    var activationFailure = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"activation-failure",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"keypress",
            "app":"ActivationFailure",
            "window_handle":"w2:fixture",
            "input":{"key":"ALT+ENTER"}
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException(
            "activation failure request must be an object");
    var activationResponse = server.Dispatch(activationFailure);
    var activationAccessibility =
        activationResponse["accessibility"] as JsonObject ??
        throw new InvalidDataException(
            "activation failure payload is missing");
    var activationFailureFacts =
        activationAccessibility["failure"] as JsonObject ??
        throw new InvalidDataException(
            "activation failure facts are missing");
    var activationMutation =
        activationAccessibility["mutation"] as JsonObject ??
        throw new InvalidDataException(
            "activation mutation facts are missing");
    if (activationFailureFacts["code"]?.GetValue<string>() !=
            "window_activation_failed" ||
        activationFailureFacts["phase"]?.GetValue<string>() != "emission" ||
        activationFailureFacts["facts_complete"]?.GetValue<bool>() != true ||
        activationMutation["input_events_emitted"]?.GetValue<bool>() != false ||
        activationMutation["partial_input_possible"]?.GetValue<bool>() !=
            false ||
        activationMutation["mutation_unknown"]?.GetValue<bool>() != false)
    {
        throw new InvalidDataException(
            "Pre-input activation failure was incorrectly reported as effect_unknown.");
    }

    var dispatchOutcomeUnknown = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"dispatch-outcome-unknown",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"click",
            "app":"DispatchOutcomeUnknown",
            "target":{
              "selector":"button \"Run\"",
              "hasName":false
            },
            "input":{"click_count":1},
            "observation":{"capture_snapshot":false}
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException(
            "dispatch outcome request must be an object");
    var dispatchResponse = server.Dispatch(dispatchOutcomeUnknown);
    var dispatchAccessibility =
        dispatchResponse["accessibility"] as JsonObject ??
        throw new InvalidDataException(
            "dispatch outcome payload is missing");
    var dispatchFailure =
        dispatchAccessibility["failure"] as JsonObject ??
        throw new InvalidDataException(
            "dispatch outcome failure facts are missing");
    var dispatchMutation =
        dispatchAccessibility["mutation"] as JsonObject ??
        throw new InvalidDataException(
            "dispatch outcome mutation facts are missing");
    var dispatchEvidence =
        dispatchAccessibility["action_evidence"] as JsonObject ??
        throw new InvalidDataException(
            "dispatch outcome action evidence is missing");
    if (dispatchFailure["code"]?.GetValue<string>() != "effect_unknown" ||
        dispatchFailure["underlying_code"]?.GetValue<string>() !=
            "action_dispatch_outcome_unknown" ||
        dispatchFailure["phase"]?.GetValue<string>() != "emission" ||
        dispatchFailure["facts_complete"]?.GetValue<bool>() != false ||
        dispatchMutation["input_events_emitted"]?.GetValue<bool>() != false ||
        dispatchMutation["partial_input_possible"]?.GetValue<bool>() != true ||
        dispatchMutation["mutation_unknown"]?.GetValue<bool>() != true ||
        dispatchEvidence["input_emitted"]?.GetValue<bool>() != false ||
        !dispatchAccessibility["error"]!.GetValue<string>().Contains(
            "do not repeat the same request",
            StringComparison.Ordinal))
    {
        throw new InvalidDataException(
            "Ambiguous UIA dispatch failure was mislabeled as an observation failure.");
    }

    Exception? cleanupDiagnostic = null;
    Exception? preservedFailure = null;
    try
    {
        using var cleanup = new FailurePreservingDisposable(
            new ThrowingDisposable(),
            error => cleanupDiagnostic = error);
        throw UiaMutationFailureAttribution.FromUnexpected(
            new TimeoutException("Synthetic UIA transaction timeout."),
            inputEmitted: false,
            actionEvidence: new JsonObject
            {
                ["route"] = "uia.pattern.invoke",
                ["geometry_sampled"] = false,
                ["readback_sampled"] = false,
                ["input_emitted"] = false,
            });
    }
    catch (Exception error)
    {
        preservedFailure = error;
    }
    if (preservedFailure is not ProviderFailureException {
            Code: "action_dispatch_outcome_unknown",
            MutationUnknown: true
        } ||
        cleanupDiagnostic is not TimeoutException)
    {
        throw new InvalidDataException(
            "Cleanup failure replaced the primary unknown mutation result.");
    }

    var disabledEvidence = new JsonObject
    {
        ["route"] = "uia.pattern.invoke",
        ["geometry_sampled"] = false,
        ["readback_sampled"] = false,
        ["input_emitted"] = false,
    };
    var disabledFailure = UiaMutationFailureAttribution.FromUnexpected(
        new FlaUI.Core.Exceptions.ElementNotEnabledException(),
        inputEmitted: false,
        actionEvidence: disabledEvidence);
    if (disabledFailure is not {
            Code: "target_not_enabled",
            Phase: "precondition",
            InputEventsEmitted: false,
            MutationUnknown: false,
            FactsComplete: true
        } ||
        !ReferenceEquals(disabledFailure.ActionEvidence, disabledEvidence))
    {
        throw new InvalidDataException(
            "A disabled UIA target was not attributed as a complete zero-input precondition failure.");
    }

    var disabledAfterInputFailure =
        UiaMutationFailureAttribution.FromUnexpected(
            new FlaUI.Core.Exceptions.ElementNotEnabledException(),
            inputEmitted: true,
            actionEvidence: disabledEvidence);
    if (disabledAfterInputFailure is not {
            Code: "post_action_observation_failed",
            Phase: "postcondition",
            InputEventsEmitted: true,
            MutationUnknown: true
        })
    {
        throw new InvalidDataException(
            "A disabled-target exception after input incorrectly erased mutation uncertainty.");
    }

    var staleElement = JsonNode.Parse(
        """
        {
          "protocol":"computer-use-provider-v2",
          "schema_version":2,
          "request_id":"stale-element",
          "operation":"resolve_and_act",
          "action":{
            "protocol_version":2,
            "intent":"click",
            "app":"StaleElement",
            "target":{
              "selector":"button \"Save\"",
              "providerObjectPath":"runtime:1.2.3",
              "hasName":false
            },
            "input":{"click_count":1}
          }
        }
        """) as JsonObject ??
        throw new InvalidDataException("stale element request must be an object");
    var staleResponse = server.Dispatch(staleElement);
    var staleAccessibility = staleResponse["accessibility"] as JsonObject ??
        throw new InvalidDataException("stale element payload is missing");
    var staleFailure = staleAccessibility["failure"] as JsonObject ??
        throw new InvalidDataException("stale element failure is missing");
    var staleMutation = staleAccessibility["mutation"] as JsonObject ??
        throw new InvalidDataException("stale element mutation is missing");
    if (staleFailure["code"]?.GetValue<string>() != "stale_element_ref" ||
        staleFailure["phase"]?.GetValue<string>() != "precondition" ||
        staleFailure["facts_complete"]?.GetValue<bool>() != true ||
        staleMutation["input_events_emitted"]?.GetValue<bool>() != false ||
        staleMutation["partial_input_possible"]?.GetValue<bool>() != false ||
        staleMutation["mutation_unknown"]?.GetValue<bool>() != false)
    {
        throw new InvalidDataException(
            "Stale element failure does not satisfy beta59 no-input facts.");
    }
}

var siblingThree = new TestSiblingNode("three", null);
var siblingTwo = new TestSiblingNode("two", siblingThree);
var siblingOne = new TestSiblingNode("one", siblingTwo);
var boundedSiblings = BoundedSiblingTraversal.Collect(
    () => siblingOne,
    value => value.Next,
    2);
if (boundedSiblings.Count != 2 ||
    boundedSiblings[0] != siblingOne ||
    boundedSiblings[1] != siblingTwo)
{
    throw new InvalidDataException(
        "Sibling traversal did not stop at the remaining node budget.");
}
var zeroBudgetRead = false;
var zeroBudgetSiblings = BoundedSiblingTraversal.Collect<TestSiblingNode>(
    () =>
    {
        zeroBudgetRead = true;
        return siblingOne;
    },
    value => value.Next,
    0);
if (zeroBudgetRead || zeroBudgetSiblings.Count != 0)
{
    throw new InvalidDataException(
        "Zero-budget sibling traversal touched the provider tree.");
}
var allSiblings = BoundedSiblingTraversal.Collect(
    () => siblingOne,
    value => value.Next,
    8);
if (allSiblings.Count != 3 || allSiblings[2] != siblingThree)
{
    throw new InvalidDataException(
        "Sibling traversal did not preserve complete provider order.");
}
using (new FlaUI.Core.CacheRequest().Activate())
{
    if (!FlaUI.Core.CacheRequest.IsCachingActive)
    {
        throw new InvalidDataException(
            "Focused traversal test did not establish an ambient UIA cache.");
    }
    var cacheActiveDuringTraversal = UiaProvider.RunWithoutUiaCache(
        () => FlaUI.Core.CacheRequest.IsCachingActive);
    if (cacheActiveDuringTraversal)
    {
        throw new InvalidDataException(
            "ControlView traversal inherited the ambient UIA cache.");
    }
    if (!FlaUI.Core.CacheRequest.IsCachingActive)
    {
        throw new InvalidDataException(
            "ControlView traversal did not restore the ambient UIA cache.");
    }
}
var unsupportedRole = UiaSnapshotNodeReads.RoleOrUnknown(
    () => throw new NotSupportedException("Synthetic unsupported ControlType."));
if (unsupportedRole != "unknown")
{
    throw new InvalidDataException(
        "Unsupported snapshot ControlType did not preserve the node.");
}
var supportedRole = UiaSnapshotNodeReads.RoleOrUnknown(() => "button");
if (supportedRole != "button")
{
    throw new InvalidDataException(
        "Supported snapshot ControlType was not preserved.");
}
var unavailablePattern = UiaSnapshotNodeReads.OptionalPattern<object>(
    () => throw new NotSupportedException("Synthetic unsupported pattern."));
if (unavailablePattern is not null)
{
    throw new InvalidDataException(
        "Unsupported snapshot pattern was reported as actionable.");
}

foreach (var minimized in new[] { false, true })
foreach (var initiallyForeground in new[] { false, true })
{
    var activationEvents = new List<string>();
    var foreground = initiallyForeground ? (nint)42 : (nint)7;
    var verified = WindowsForegroundActivationPolicy.PrepareAndCheckForeground(
        42,
        () => { activationEvents.Add("minimized"); return minimized; },
        () => { activationEvents.Add("restore"); foreground = 42; },
        () => { activationEvents.Add("foreground"); return foreground; });
    var expected = minimized
        ? new[] { "minimized", "restore", "foreground" }
        : new[] { "minimized", "foreground" };
    if (!activationEvents.SequenceEqual(expected) ||
        verified != (minimized || initiallyForeground))
    {
        throw new InvalidDataException("Activation changed non-minimized layout or skipped foreground verification.");
    }
}

Console.WriteLine(JsonSerializer.Serialize(new
{
    passed = true,
    valid_requests = validCount,
    invalid_requests_rejected = rejectedCount,
}));
return 0;

internal sealed record TestWindow(
    long Handle,
    bool Active,
    long OwnerHandle = 0,
    int ProcessId = 11);

internal sealed record TestSiblingNode(
    string Name,
    TestSiblingNode? Next);

internal sealed class ThrowingDisposable : IDisposable
{
    public void Dispose()
    {
        throw new TimeoutException("Synthetic cleanup timeout.");
    }
}
