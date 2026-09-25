using System.Diagnostics;
using System.Drawing;
using System.Text.Json.Nodes;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.Core.Definitions;

namespace ComputerUse.Windows.Runtime;

internal sealed partial class UiaProvider
{
    internal JsonObject ResolveAndAct(JsonObject request)
    {
        var intent = RequiredString(request, "intent");
        var query = RequiredString(request, "app");
        var process = ResolveProcess(request, query);
        var target = request["target"] as JsonObject;
        var elementRecord = request["element"] as JsonObject;
        var selectedWindow = ResolveWindow(
            process,
            target,
            OptionalString(request, "window_handle", string.Empty),
            "stale_element_ref");
        var requestWindowHandle = OptionalString(
            request,
            "window_handle",
            string.Empty);
        var currentWindowHandle = OptionalString(
            target,
            "windowHandle",
            requestWindowHandle);
        WindowsWindowExpectation.ValidateCurrent(
            request["expected_window"] as JsonObject,
            requestWindowHandle,
            currentWindowHandle,
            $"win32:pid:{process.Id}",
            currentWindowHandle,
            ProcessGeneration(process),
            "window");
        var root = _automation.FromHandle(selectedWindow.Handle);
        var element = ResolveElement(
            root,
            target,
            elementRecord,
            process,
            selectedWindow,
            () => _automation.FromHandle(selectedWindow.Handle));
        EnsureTargetLogicalWindow(element, process, selectedWindow);
        var ownerIdentity = CapturePostActionOwnerIdentity(selectedWindow);
        if (intent == "resolve_target")
        {
            if (element is null)
            {
                throw new ProviderFailureException(
                    "semantic_target_not_found",
                    "precondition",
                    "resolve_target requires an exact target.");
            }
            return new JsonObject
            {
                ["ok"] = true,
                ["resolved_element"] = ResolvedElementJson(
                    root,
                    element,
                    process,
                    selectedWindow),
            };
        }
        var input = request["input"] as JsonObject ?? new JsonObject();
        var route = "unknown";
        var geometrySampled = false;
        bool? pointOwnerVerified = null;
        bool? focusVerified = null;
        var readbackSampled = false;
        bool? readbackVerified = null;
        var inputEmitted = false;
        var mutationAttempted = false;
        var pointerPreparationAttempted = false;

        using var changed = new ManualResetEventSlim(false);
        using var eventHandler = new FailurePreservingDisposable(
            root.RegisterStructureChangedEvent(
                TreeScope.Subtree,
                (_, _, _) => changed.Set()),
            error => Console.Error.WriteLine(
                "[provider] AX structure event cleanup failed without " +
                "replacing the primary action result: " +
                $"{error.GetType().Name} HRESULT=0x{error.HResult:x8}: " +
                error.Message));

        try
        {
            switch (intent)
            {
                case "click":
                var clickMethod = OptionalString(input, "click_method", "auto");
                if (TryInvoke(
                        element,
                        (selectedRoute, _) =>
                        {
                            route = selectedRoute;
                            mutationAttempted = true;
                        }))
                {
                    inputEmitted = true;
                }
                else
                {
                    if (clickMethod == "accessibility")
                    {
                        throw new ProviderFailureException(
                            "capability_unsupported",
                            "precondition",
                            "The target exposes no semantically equivalent UIA pattern.");
                    }
                    if (element is null)
                    {
                        throw new ProviderFailureException(
                            "semantic_target_not_found", "precondition",
                            "Pointer fallback requires an exact target.");
                    }
                    var point = VerifiedPointerPoint(element, process, selectedWindow);
                    geometrySampled = true;
                    pointOwnerVerified = true;
                    route = "input.send_input.pointer";
                    mutationAttempted = true;
                    NativeDesktop.Click(
                        point.X,
                        point.Y,
                        OptionalString(input, "mouse_button", "left"),
                        OptionalInt(input, "click_count", 1));
                    focusVerified = selectedWindow.Active;
                    inputEmitted = true;
                }
                    break;
                case "set_value":
                if (element is null)
                {
                    throw new ProviderFailureException(
                        "semantic_target_not_found",
                        "precondition",
                        "set_value requires an exact target.");
                }
                var requestedValue = OptionalString(input, "value", string.Empty);
                var rangePattern =
                    element.Patterns.RangeValue.PatternOrDefault;
                var rangeWritable = rangePattern is not null &&
                    !rangePattern.IsReadOnly.Value;
                if (UiaSetValueRouting.PrefersRange(
                        CanonicalRole(element.ControlType),
                        rangeWritable))
                {
                    if (!UiaSetValueRouting.TryParseFiniteRangeValue(
                            requestedValue,
                            out var requestedRangeValue))
                    {
                        throw new ProviderFailureException(
                            "invalid_request",
                            "precondition",
                            "set_value requires a finite invariant number for a UIA range target.");
                    }
                    if (!UiaSetValueRouting.IsWithinRange(
                            requestedRangeValue,
                            rangePattern!.Minimum.Value,
                            rangePattern.Maximum.Value))
                    {
                        throw new ProviderFailureException(
                            "invalid_request",
                            "precondition",
                            "set_value is outside the UIA range target bounds.");
                    }
                    route = "uia.range_value.set";
                    mutationAttempted = true;
                    rangePattern.SetValue(requestedRangeValue);
                    inputEmitted = true;
                    readbackSampled = true;
                    readbackVerified = LiveValueReadback.VerifyRange(
                        requestedRangeValue,
                        () =>
                        {
                            var readbackRoot =
                                _automation.FromHandle(selectedWindow.Handle);
                            var readbackElement = ResolveSetValueReadbackElement(
                                readbackRoot,
                                target,
                                elementRecord,
                                process,
                                selectedWindow);
                            var readbackRange =
                                readbackElement.Patterns.RangeValue.PatternOrDefault;
                            if (readbackRange is null ||
                                readbackRange.IsReadOnly.Value)
                            {
                                return null;
                            }
                            return readbackRange.Value.Value;
                        });
                    if (readbackVerified != true)
                    {
                        throw new ProviderFailureException(
                            "readback_mismatch",
                            "readback",
                            "UIA RangeValue accepted the write but readback did not match.",
                            inputEventsEmitted: true);
                    }
                    break;
                }
                var valuePattern = element.Patterns.Value.PatternOrDefault;
                if (valuePattern is null || valuePattern.IsReadOnly.Value)
                {
                    throw new ProviderFailureException(
                        "capability_unsupported",
                        "precondition",
                        "The target does not expose a writable UIA Value pattern.");
                }
                var textPattern = element.Patterns.Text.PatternOrDefault;
                if (UiaSetValueRouting.PrefersDocumentInput(
                        element.ControlType == ControlType.Document,
                        !valuePattern.IsReadOnly.Value,
                        textPattern is not null))
                {
                    route = "input.send_input.document_replace";
                    EnsureExactFocus(element,
                        () => EnsureTargetLogicalWindow(element, process, selectedWindow));
                    focusVerified = true;
                    mutationAttempted = true;
                    NativeDesktop.PressKeys(new[] { "CTRL", "A" });
                    inputEmitted = true;
                    if (requestedValue.Length == 0)
                    {
                        NativeDesktop.PressKeys(new[] { "DELETE" });
                    }
                    else
                    {
                        NativeDesktop.TypeText(requestedValue);
                    }
                }
                else
                {
                    route = "uia.value.set";
                    mutationAttempted = true;
                    valuePattern.SetValue(requestedValue);
                    inputEmitted = true;
                }
                readbackSampled = true;
                readbackVerified = LiveValueReadback.VerifyTextValue(
                    requestedValue,
                    () =>
                    {
                        var readbackRoot =
                            _automation.FromHandle(selectedWindow.Handle);
                        var readbackElement = ResolveSetValueReadbackElement(
                            readbackRoot,
                            target,
                            elementRecord,
                            process,
                            selectedWindow);
                        var readbackPattern =
                            readbackElement.Patterns.Value.PatternOrDefault;
                        if (readbackPattern is null ||
                            readbackPattern.IsReadOnly.Value)
                        {
                            return null;
                        }
                        return readbackPattern.Value.Value;
                    },
                    () =>
                    {
                        var readbackRoot =
                            _automation.FromHandle(selectedWindow.Handle);
                        var readbackElement = ResolveSetValueReadbackElement(
                            readbackRoot,
                            target,
                            elementRecord,
                            process,
                            selectedWindow);
                        var readbackPattern =
                            readbackElement.Patterns.Text.PatternOrDefault;
                        if (readbackPattern is null)
                        {
                            return null;
                        }
                        return readbackPattern.DocumentRange.GetText(-1);
                    });
                if (readbackVerified != true)
                {
                    throw new ProviderFailureException(
                        "readback_mismatch",
                        "readback",
                        "UIA Value accepted the write but readback did not match.",
                        inputEventsEmitted: true);
                }
                    break;
                case "type":
                    route = "input.send_input.keyboard";
                    if (element is not null)
                    {
                        EnsureExactFocus(element,
                            () => EnsureTargetLogicalWindow(element, process, selectedWindow));
                    }
                    else
                    {
                        NativeDesktop.ActivateWindow(selectedWindow.Handle);
                    }
                    focusVerified = true;
                    mutationAttempted = true;
                    NativeDesktop.TypeText(
                        OptionalString(input, "text", string.Empty));
                    inputEmitted = true;
                    break;
                case "keypress":
                    route = "input.send_input.keyboard";
                    if (element is not null)
                    {
                        EnsureExactFocus(element,
                            () => EnsureTargetLogicalWindow(element, process, selectedWindow));
                        focusVerified = true;
                    }
                    else
                    {
                        NativeDesktop.ActivateWindow(selectedWindow.Handle);
                        focusVerified = true;
                    }
                    mutationAttempted = true;
                    NativeDesktop.PressKeys(
                        new[] { RequiredString(input, "key") });
                    inputEmitted = true;
                    break;
                case "scroll":
                    if (TryScroll(
                            element,
                            OptionalString(input, "direction", "down"),
                            () =>
                            {
                                route = "uia.scroll";
                                mutationAttempted = true;
                            }))
                    {
                        inputEmitted = true;
                    }
                    else
                    {
                        route = "input.send_input.wheel";
                        // Window-scoped scrolling owns a logical window, not an
                        // arbitrary element. Element-scoped scrolling requires
                        // the exact element at the point.
                        RequirePointerEligibility(element ?? root);
                        pointerPreparationAttempted = true;
                        NativeDesktop.ActivateWindow(selectedWindow.Handle);
                        var point = VerifiedPointerPoint(
                            element ?? root, process, selectedWindow,
                            exactElementRequired: element is not null);
                        geometrySampled = true;
                        pointOwnerVerified = true;
                        mutationAttempted = true;
                        NativeDesktop.Scroll(
                            point.X,
                            point.Y,
                            0,
                            (OptionalString(input, "direction", "down") == "up"
                                ? -1
                                : 1) *
                            Math.Max(1, OptionalDouble(input, "pages", 1)));
                        focusVerified = true;
                        inputEmitted = true;
                    }
                    break;
                default:
                    throw new ProviderFailureException(
                        "unsupported_intent",
                        "precondition",
                        $"Unsupported accessibility intent {intent}.");
            }

            _ = changed.Wait(TimeSpan.FromMilliseconds(120));
            var observation = request["observation"] as JsonObject;
            if (observation?["capture_snapshot"]?.GetValue<bool>() == false)
            {
                return new JsonObject
                {
                    ["ok"] = true,
                    ["observation_skipped"] = true,
                    ["mutation"] = MutationFacts(inputEmitted, false),
                    ["action_evidence"] = ActionEvidence(
                        route,
                        geometrySampled,
                        focusVerified,
                        readbackSampled,
                        readbackVerified,
                        inputEmitted, pointOwnerVerified),
                };
            }
            var followedExternalOwner = false;
            var snapshot = PostActionObservationRetry.Execute(
                () => PostActionObservationWindows(
                    process.Id,
                    ownerIdentity),
                window => window.Handle.ToInt64(),
                window => window.Active,
                selectedWindow.Handle.ToInt64(),
                (window, _) =>
                {
                    if (window.ProcessId == process.Id)
                    {
                        return BuildSnapshot(
                            process,
                            window,
                            observation,
                            commitPostActionScreenshot: true);
                    }
                    if (!MatchesPostActionOwner(ownerIdentity, window))
                    {
                        throw new ProviderFailureException(
                            "window_target_stale",
                            "post_action_observation",
                            "The captured native owner identity is no longer current.");
                    }
                    using var ownerProcess = Process.GetProcessById(
                        window.ProcessId);
                    followedExternalOwner = true;
                    return BuildSnapshot(
                        ownerProcess,
                        window,
                        observation,
                        commitPostActionScreenshot: true);
                },
                () => new JsonObject
                {
                    ["app"] = AppJson(process),
                    ["windowAbsent"] = true,
                    ["treeLines"] = new JsonArray(),
                    ["elements"] = new JsonArray(),
                },
                onFailure: (attempt, window, error) =>
                    Console.Error.WriteLine(
                        "[provider] post-action snapshot attempt " +
                        $"{attempt} failed for HWND " +
                        (window is null
                            ? "none"
                            : $"0x{window.Handle.ToInt64():x}") +
                        $": {error.GetType().Name} " +
                        $"HRESULT=0x{error.HResult:x8}"),
                windowOwnerHandle:
                    window => window.OwnerHandle.ToInt64(),
                originalOwnerHandle:
                    ownerIdentity?.Handle.ToInt64() ?? 0);
            var response = new JsonObject
            {
                ["ok"] = true,
                ["snapshot"] = snapshot,
                ["mutation"] = MutationFacts(inputEmitted, false),
                ["action_evidence"] = ActionEvidence(
                    route,
                    geometrySampled,
                    focusVerified,
                    readbackSampled,
                    readbackVerified,
                    inputEmitted, pointOwnerVerified),
            };
            if (followedExternalOwner)
            {
                response["external_desktop_transition"] = true;
                response["notes"] = new JsonArray(
                    "The acted-on transient window retired; the final AX " +
                    "observation follows its exact pre-action native owner.");
            }
            return response;
        }
        catch (Exception error) when (
            pointerPreparationAttempted &&
            PointerPreparationFailureAttribution.HasKnownZeroRequestedInput(
                error, mutationAttempted, inputEmitted))
        {
            throw PointerPreparationFailureAttribution.FromZeroInputError(
                error, ActionEvidence(
                    route, geometrySampled, focusVerified,
                    readbackSampled, readbackVerified, false, pointOwnerVerified));
        }
        catch (ProviderFailureException error) when (
            mutationAttempted || error.FocusPreparation.HasValue)
        {
            throw UiaMutationFailureAttribution.Preserve(
                error, inputEmitted, ActionEvidence(
                    route,
                    geometrySampled,
                    error.FocusPreparation.HasValue ? false : focusVerified,
                    readbackSampled,
                    readbackVerified,
                    inputEmitted, pointOwnerVerified));
        }
        catch (Exception error) when (mutationAttempted)
        {
            var failureStage = inputEmitted
                ? "post_action_observation"
                : "action_dispatch";
            Console.Error.WriteLine(
                $"[provider] AX {failureStage} failed via {route}: " +
                $"{error.GetType().Name} HRESULT=0x{error.HResult:x8}: " +
                error.Message);
            throw UiaMutationFailureAttribution.FromUnexpected(
                error,
                inputEmitted,
                ActionEvidence(
                    route,
                    geometrySampled,
                    focusVerified,
                    readbackSampled,
                    readbackVerified,
                    inputEmitted, pointOwnerVerified));
        }
    }

    private PostActionOwnerIdentity? CapturePostActionOwnerIdentity(
        NativeDesktop.WindowRecord selectedWindow)
    {
        return PostActionOwnerTransitionPolicy.Capture(
            selectedWindow.OwnerHandle,
            handle =>
                NativeDesktop.TryGetWindowProcessId(handle, out var processId)
                    ? processId
                    : null,
            processId =>
            {
                using var process = Process.GetProcessById(processId);
                return ProcessGeneration(process);
            },
            handle => _windowLifecycle.Generation(handle));
    }

    private IReadOnlyList<NativeDesktop.WindowRecord>
        PostActionObservationWindows(
            int sourceProcessId,
            PostActionOwnerIdentity? ownerIdentity)
    {
        return NativeDesktop.ListWindows()
            .Where(
                window =>
                    window.ProcessId == sourceProcessId ||
                    MatchesPostActionOwner(ownerIdentity, window))
            .ToArray();
    }

    private bool MatchesPostActionOwner(
        PostActionOwnerIdentity? expected,
        NativeDesktop.WindowRecord current)
    {
        if (expected is null ||
            current.Handle != expected.Handle ||
            current.ProcessId != expected.ProcessId)
        {
            return false;
        }
        try
        {
            using var process = Process.GetProcessById(current.ProcessId);
            return PostActionOwnerTransitionPolicy.Matches(
                expected,
                current.Handle,
                current.ProcessId,
                ProcessGeneration(process),
                _windowLifecycle.Generation(current.Handle));
        }
        catch
        {
            return false;
        }
    }

    private AutomationElement ResolveSetValueReadbackElement(
        AutomationElement root,
        JsonObject? target,
        JsonObject? record,
        Process process,
        NativeDesktop.WindowRecord selectedWindow)
    {
        var resolved = ResolveSetValueReadbackElementCore(
            root, target, record, process, selectedWindow);
        try
        {
            EnsureTargetLogicalWindow(resolved, process, selectedWindow);
        }
        catch (ProviderFailureException)
        {
            throw new ProviderFailureException(
                "stale_element_ref", "readback",
                "The written target's window ownership could not be verified during readback.",
                inputEventsEmitted: true);
        }
        return resolved;
    }

    private AutomationElement ResolveSetValueReadbackElementCore(
        AutomationElement root,
        JsonObject? target,
        JsonObject? record,
        Process process,
        NativeDesktop.WindowRecord selectedWindow)
    {
        var providerObjectPath = OptionalString(
            target,
            "providerObjectPath",
            OptionalString(record, "providerObjectPath", string.Empty));
        if (!providerObjectPath.StartsWith(
                "runtime:",
                StringComparison.Ordinal))
        {
            return ResolveElement(
                    root,
                    target,
                    record,
                    process,
                    selectedWindow) ??
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "readback",
                    "The set_value target disappeared before live readback.",
                    inputEventsEmitted: true);
        }

        return ResolveProviderObjectPath(
            LogicalAutomationRoots(process, selectedWindow),
            providerObjectPath,
            resolved =>
            {
                if (!RoleMatchesTarget(resolved, target, record))
                {
                    throw new ProviderFailureException(
                        "stale_element_ref",
                        "readback",
                        "The set_value target role changed before live readback.",
                        inputEventsEmitted: true);
                }
            });
    }
}
