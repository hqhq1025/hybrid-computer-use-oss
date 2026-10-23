using System.Diagnostics;
using System.Drawing;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json.Nodes;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.Core.Definitions;

namespace ComputerUse.Windows.Runtime;

internal sealed partial class UiaProvider
{
    private static JsonObject BuildWindowlessSnapshot(
        JsonObject app,
        string closedWindowHandle)
    {
        return new JsonObject
        {
            ["app"] = app.DeepClone(),
            ["windowAbsent"] = true,
            ["closedWindowHandle"] = closedWindowHandle,
            ["windows"] = new JsonArray(),
            ["snapshotId"] = Guid.NewGuid().ToString("N"),
            ["sampledAt"] = DateTimeOffset.UtcNow.ToString("O"),
            ["treeLines"] = new JsonArray(
                "No top-level AX windows remain for this application after close."),
            ["elements"] = new JsonArray(),
        };
    }

    private JsonObject BuildSnapshot(
        Process process,
        NativeDesktop.WindowRecord selectedWindow,
        JsonObject? observation)
    {
        // One clock covers ownership, traversal and the single ownership
        // retry, so a retry cannot restart the sampling deadline.
        var elapsed = Stopwatch.StartNew();
        try
        {
            return WindowOwnershipObservation.Read(() => BuildSnapshotSample(
                process, selectedWindow, observation, () => elapsed.Elapsed));
        }
        catch (WindowOwnershipUnavailableException error)
        {
            throw new ProviderFailureException(
                "window_ownership_unavailable", "observation",
                $"A complete logical-window sample could not be acquired: {error.Message}");
        }
    }

    private JsonObject BuildSnapshotSample(
        Process process,
        NativeDesktop.WindowRecord selectedWindow,
        JsonObject? observation,
        Func<TimeSpan> elapsed)
    {
        var annotations = new JsonArray();
        var projection = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id));
        selectedWindow = projection.LogicalRecord(selectedWindow);
        var roots = LogicalAutomationRoots(
            process,
            projection,
            selectedWindow,
            discoverTransients: false,
            annotation: Gap,
            elapsed: elapsed);
        var initialRootIdentities = roots.Select(RuntimeObjectPath).ToArray();
        var maxNodes = OptionalInt(observation, "max_tree_nodes", DefaultNodeLimit);
        var maxDepth = OptionalInt(observation, "max_tree_depth", DefaultDepthLimit);
        var visits = new OwnershipProbeBudget(
            maxNodes, SnapshotSampleTimeout, elapsed);
        var ownershipBudget = new OwnershipProbeBudget(
            checked(maxNodes * 4 + 256), SnapshotSampleTimeout, elapsed);
        var boundary = CreateLogicalBoundary(process, projection, ownershipBudget);
        var records = new JsonArray();
        var lines = new JsonArray();
        var index = 0;
        var selectedHandle = OpaqueWindowHandle(process, selectedWindow.Handle);
        var sampled = new List<(AutomationElement Element, JsonObject Record, int[] Path, int Depth)>();
        var identities = new CanonicalNodeIdentitySet<AutomationElement>((left, right) =>
        {
            visits.CheckTime();
            return _automation.Compare(left, right);
        });
        var observedElementLeases =
            new Dictionary<string, ObservedElementLease<AutomationElement>>(
                StringComparer.Ordinal);
        var controlWalker =
            _automation.TreeWalkerFactory.GetControlViewWalker();

        using (CacheRequest.ForceNoCache())
        {
            try
            {
                Visit(roots);
                // Known window roots provide the main canonical tree. Searching
                // the desktop again for raw-only popups must not delay it.
                if (visits.Remaining > 0 && elapsed() < SnapshotSampleTimeout)
                {
                    var additionalRoots = LogicalAutomationRoots(
                        process, projection, selectedWindow,
                        transientsOnly: true,
                        annotation: Gap, elapsed: elapsed,
                        precedingRuntimeIdentities: initialRootIdentities);
                    // Keep the first root table frozen. New roots append to it;
                    // duplicate records skip emission, not their index slots.
                    Visit(additionalRoots, roots.Count);
                }
                else
                {
                    Gap("transient_discovery_incomplete");
                }
            }
            catch (ObservationDeadlineExceededException)
            {
                // Keep the sampled canonical records. An exhausted read budget
                // is not evidence that the window was replaced and cannot retry.
                Gap("time_budget");
                Gap("transient_discovery_incomplete");
            }
        }

        // Traversal alternates parent branches, so records arrive breadth
        // first. Publish them in tree-path order with dense indices.
        var pathOrder = Comparer<int[]>.Create((left, right) =>
        {
            for (var offset = 0; offset < Math.Min(left.Length, right.Length); offset++)
            {
                var order = left[offset].CompareTo(right[offset]);
                if (order != 0) { return order; }
            }
            return left.Length.CompareTo(right.Length);
        });
        index = 0;
        foreach (var sample in sampled.OrderBy(sample => sample.Path, pathOrder))
        {
            sample.Record["index"] = index;
            records.Add(sample.Record);
            lines.Add(new string(' ', sample.Depth * 2) +
                $"{index} {sample.Record["controlType"]?.GetValue<string>() ?? "unknown"} " +
                $"{sample.Record["name"]?.GetValue<string>() ?? string.Empty}");
            var providerPath = sample.Record["providerObjectPath"]?.GetValue<string>() ?? string.Empty;
            if (providerPath.StartsWith("runtime:", StringComparison.Ordinal))
            {
                observedElementLeases[providerPath] =
                    new ObservedElementLease<AutomationElement>(sample.Element, sample.Record);
            }
            index++;
        }
        _observedElementLeases.Replace(
            process.Id,
            ProcessGeneration(process),
            selectedWindow.Handle.ToInt64(),
            observedElementLeases);

        var windows = projection.LogicalWindows();
        var windowInventory = new JsonArray();
        foreach (var window in windows)
        {
            var handle = OpaqueWindowHandle(process, window.Handle);
            windowInventory.Add(
                new JsonObject
                {
                    ["title"] = window.Title,
                    ["role"] = "window",
                    ["handle"] = handle,
                    ["providerBusName"] = $"win32:pid:{process.Id}",
                    ["providerObjectPath"] = handle,
                    ["providerProcessGeneration"] = ProcessGeneration(process),
                    ["selected"] = window.Handle == selectedWindow.Handle,
                    ["active"] = window.Active,
                    ["showing"] = true,
                });
        }
        var snapshot = new JsonObject
        {
            ["app"] = AppJson(process, selectedWindow.HostedProcessId),
            ["windowTitle"] = selectedWindow.Title,
            ["windowHandle"] = selectedHandle,
            ["windows"] = windowInventory,
            ["snapshotId"] = Guid.NewGuid().ToString("N"),
            ["sampledAt"] = DateTimeOffset.UtcNow.ToString("O"),
            ["windowBounds"] = RectangleJson(selectedWindow.Bounds),
            ["treeLines"] = lines,
            ["elements"] = records,
            ["annotations"] = annotations,
        };
        return snapshot;

        bool Emit(AutomationElement element, int[] treePath, int depth)
        {
            JsonObject record;
            try
            {
                record = ElementJson(
                    element,
                    index,
                    process,
                    selectedWindow,
                    selectedHandle,
                    treePath);
            }
            catch
            {
                Gap("node_read_failed");
                return false;
            }
            var providerObjectPath =
                record["providerObjectPath"]?.GetValue<string>() ?? string.Empty;
            try
            {
                var propertyKey = string.Join('\u001f',
                    record["controlType"]?.GetValue<string>() ?? string.Empty,
                    record["name"]?.GetValue<string>() ?? string.Empty,
                    record["className"]?.GetValue<string>() ?? string.Empty,
                    record["frame"]?.ToJsonString() ?? string.Empty);
                if (!identities.TryAdd(element, providerObjectPath, propertyKey, treePath[0])) { return false; }
            }
            catch (ObservationDeadlineExceededException) { throw; }
            catch
            {
                Gap("node_identity_unavailable");
                return false;
            }
            sampled.Add((element, record, treePath, depth));
            index++;
            return true;
        }

        void Visit(IReadOnlyList<AutomationElement> selectedRoots, int rootIndexOffset = 0)
        {
            LogicalWindowTraversal.Visit(
                selectedRoots, visits, maxDepth,
                element => boundary.Owner(element) == selectedWindow.Handle.ToInt64(),
                Emit,
                element => ReadChild(() => controlWalker.GetFirstChild(element)),
                element => ReadChild(() => controlWalker.GetNextSibling(element)),
                Gap,
                rootIndexOffset);
        }

        AutomationElement? ReadChild(Func<AutomationElement?> read)
        {
            try
            {
                return read();
            }
            catch
            {
                Gap("children_read_failed");
                return null;
            }
        }

        void Gap(string reason)
        {
            if (annotations.OfType<JsonObject>().Any(item =>
                item["reason"]?.GetValue<string>() == reason)) { return; }
            annotations.Add(new JsonObject
            {
                ["kind"] = "omitted",
                ["count"] = 1,
                ["reason"] = reason,
                ["detail"] = "Logical-window canonical acquisition is incomplete.",
            });
        }
    }

    private JsonObject ElementJson(
        AutomationElement element,
        int index,
        Process process,
        NativeDesktop.WindowRecord window,
        string windowHandle,
        IReadOnlyList<int> treePath)
    {
        var role = UiaSnapshotNodeReads.RoleOrUnknown(
            () => CanonicalRole(element.ControlType));
        var name = index == 0
            ? window.Title
            : Safe(() => element.Name, string.Empty);
        var rectangle = Safe(() => element.BoundingRectangle, Rectangle.Empty);
        var hasKeyboardFocus = Safe(
            () => element.Properties.HasKeyboardFocus.ValueOrDefault,
            false);
        var visibility = ReadVisibility(element);
        var isEffectivelyVisible = visibility.EffectiveVisibility(
            hasKeyboardFocus,
            rectangle,
            window.Bounds);
        JsonObject? visibilityEvidence = null;
        if (visibility.IsOffscreen is null)
        {
            visibilityEvidence = new JsonObject
            {
                ["state"] = "unknown",
                ["reason"] = visibility.Reason,
            };
            if (visibility.ExceptionType is not null)
            {
                visibilityEvidence["exceptionType"] = visibility.ExceptionType;
                visibilityEvidence["hresult"] = visibility.HResult;
            }
        }
        else if (visibility.IsOffscreen == true && isEffectivelyVisible == true)
        {
            visibilityEvidence = new JsonObject
            {
                ["state"] = "offscreen",
                ["reason"] = "focus_geometry_override",
            };
        }
        var toggle = UiaSnapshotNodeReads.OptionalPattern(
            () => element.Patterns.Toggle.PatternOrDefault);
        var selectionItem = UiaSnapshotNodeReads.OptionalPattern(
            () => element.Patterns.SelectionItem.PatternOrDefault);
        var valuePattern = UiaSnapshotNodeReads.OptionalPattern(
            () => element.Patterns.Value.PatternOrDefault);
        var textPattern = UiaSnapshotNodeReads.OptionalPattern(
            () => element.Patterns.Text.PatternOrDefault);
        var valueWritable = valuePattern is not null &&
            !Safe(() => valuePattern.IsReadOnly.Value, true);
        var rangeValuePattern = UiaSnapshotNodeReads.OptionalPattern(
            () => element.Patterns.RangeValue.PatternOrDefault);
        var rangeValueWritable = rangeValuePattern is not null &&
            !Safe(() => rangeValuePattern.IsReadOnly.Value, true);
        var actions = new JsonArray();
        AddAction(
            actions,
            UiaSnapshotNodeReads.OptionalPattern(
                () => element.Patterns.Invoke.PatternOrDefault) is not null,
            "invoke");
        AddAction(actions, toggle is not null, "toggle");
        AddAction(
            actions,
            selectionItem is not null || textPattern is not null &&
                Safe(() => textPattern.SupportedTextSelection !=
                    FlaUI.Core.Definitions.SupportedTextSelection.None, false),
            "select");
        AddAction(
            actions,
            UiaSnapshotNodeReads.OptionalPattern(
                () => element.Patterns.ExpandCollapse.PatternOrDefault) is not null,
            "expand");
        AddAction(
            actions,
            UiaSnapshotNodeReads.OptionalPattern(
                () => element.Patterns.ScrollItem.PatternOrDefault) is not null,
            "scrollIntoView");
        AddAction(
            actions,
            valueWritable ||
                UiaSetValueRouting.PrefersRange(role, rangeValueWritable),
            "setValue");
        var states = new List<string>();
        if (Safe(() => element.IsEnabled, false))
        {
            states.Add("enabled");
        }
        if (hasKeyboardFocus)
        {
            states.Add("focused");
        }
        if (toggle is not null)
        {
            var toggleState = ProviderValueReads.OptionalString(
                () => WindowsToggleState.Canonicalize(
                    toggle.ToggleState.ValueOrDefault.ToString()));
            if (!string.IsNullOrEmpty(toggleState))
            {
                states.Add(toggleState);
            }
        }
        if (selectionItem is not null)
        {
            states.Add(Safe(
                () => selectionItem.IsSelected.ValueOrDefault,
                false)
                ? "selected"
                : "unselected");
        }
        var value = string.Empty;
        if (valuePattern is not null)
        {
            var valuePatternValue =
                Safe(() => valuePattern.Value.Value, string.Empty);
            var documentText = textPattern is null
                ? null
                : ProviderValueReads.OptionalString(
                    () => textPattern.DocumentRange.GetText(-1));
            value = LiveValueReadback.CanonicalTextValue(
                valuePatternValue,
                documentText);
        }
        else if (rangeValuePattern is not null)
        {
            value = Safe(
                () => rangeValuePattern.Value.Value.ToString(
                    "R",
                    System.Globalization.CultureInfo.InvariantCulture),
                string.Empty);
        }
        var nativeRuntimeId = Safe(
            () => element.Properties.RuntimeId.ValueOrDefault ?? Array.Empty<int>(),
            Array.Empty<int>());
        var providerBusName = $"uia3:pid:{process.Id}";
        var providerObjectPath = nativeRuntimeId.Length == 0
            ? string.Empty
            : RuntimeObjectPath(nativeRuntimeId);
        var record = new JsonObject
        {
            ["index"] = index,
            ["runtimeId"] = new JsonArray(
                treePath.Select(value => JsonValue.Create(value)).ToArray()),
            ["automationId"] = Safe(() => element.AutomationId, string.Empty),
            ["providerBusName"] = providerBusName,
            ["providerObjectPath"] = providerObjectPath,
            ["processGeneration"] = ProcessGeneration(process),
            ["providerIdentityScope"] = nativeRuntimeId.Length == 0
                ? "snapshot_only"
                : "self",
            ["providerWindowBusName"] = $"win32:pid:{process.Id}",
            ["providerWindowObjectPath"] = windowHandle,
            ["providerWindowProcessGeneration"] = ProcessGeneration(process),
            ["providerWindowRole"] = "window",
            ["providerWindowName"] = window.Title,
            ["showing"] = isEffectivelyVisible,
            ["visible"] = isEffectivelyVisible,
            ["name"] = name,
            ["controlType"] = role,
            ["localizedControlType"] = Safe(
                () => element.Properties.LocalizedControlType.ValueOrDefault,
                string.Empty),
            ["className"] = Safe(() => element.ClassName, string.Empty),
            ["value"] = value,
            ["frame"] = RectangleJson(rectangle),
            ["actions"] = actions,
            ["states"] = string.Join(' ', states),
            ["description"] = Safe(() => element.HelpText, string.Empty),
        };
        if (visibilityEvidence is not null)
        {
            record["visibilityEvidence"] = visibilityEvidence;
        }
        return record;
    }

}
