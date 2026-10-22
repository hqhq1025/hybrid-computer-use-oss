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
        JsonObject? observation,
        bool commitPostActionScreenshot = false)
    {
        try
        {
            return WindowOwnershipObservation.Read(() => BuildSnapshotSample(
                process, selectedWindow, observation, commitPostActionScreenshot));
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
        bool commitPostActionScreenshot)
    {
        var projection = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id));
        selectedWindow = projection.LogicalRecord(selectedWindow);
        var roots = LogicalAutomationRoots(
            process,
            projection,
            selectedWindow);
        var maxNodes = OptionalInt(observation, "max_tree_nodes", DefaultNodeLimit);
        var maxDepth = OptionalInt(observation, "max_tree_depth", DefaultDepthLimit);
        var visits = new OwnershipProbeBudget(maxNodes, TimeSpan.FromSeconds(15));
        var ownershipBudget = new OwnershipProbeBudget(
            checked(maxNodes * 4 + 256), TimeSpan.FromSeconds(15));
        var boundary = CreateLogicalBoundary(process, projection, ownershipBudget);
        var annotations = new JsonArray();
        var records = new JsonArray();
        var lines = new JsonArray();
        var index = 0;
        var selectedHandle = OpaqueWindowHandle(process, selectedWindow.Handle);
        var seenProviderObjectPaths = new HashSet<string>(StringComparer.Ordinal);
        var observedElementLeases =
            new Dictionary<string, ObservedElementLease<AutomationElement>>(
                StringComparer.Ordinal);
        var controlWalker =
            _automation.TreeWalkerFactory.GetControlViewWalker();

        using (CacheRequest.ForceNoCache())
        {
            LogicalWindowTraversal.Visit(
                roots, visits, maxDepth,
                element => boundary.Owner(element) == selectedWindow.Handle.ToInt64(),
                Emit,
                element => ReadChild(() => controlWalker.GetFirstChild(element)),
                element => ReadChild(() => controlWalker.GetNextSibling(element)),
                Gap);
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
        if (observation?["include_screenshot"]?.GetValue<bool>() == true)
        {
            var screenshotScope = OptionalString(
                observation,
                "screenshot_scope",
                "window");
            NativeDesktop.ScreenFrame? frame;
            if (screenshotScope == "screen")
            {
                frame = commitPostActionScreenshot
                    ? NativeDesktop.CaptureCommittedScreen(
                        selectedWindow.Handle)
                    : NativeDesktop.Capture();
            }
            else
            {
                NativeDesktop.ScreenFrame CaptureWindow()
                {
                    if (!NativeDesktop.TryCapture(
                            selectedWindow.Bounds,
                            out var captured) ||
                        captured is null)
                    {
                        throw new InvalidOperationException(
                            "window_outside_virtual_desktop");
                    }
                    return captured;
                }

                try
                {
                    frame = commitPostActionScreenshot
                        ? NativeDesktop.CaptureCommittedWindow(
                            selectedWindow.Handle)
                        : CaptureWindow();
                }
                catch (InvalidOperationException error) when (
                    error.Message == "window_outside_virtual_desktop")
                {
                    snapshot["screenshotUnavailableReason"] =
                        "window_outside_virtual_desktop";
                    frame = null;
                }
            }
            if (frame is not null)
            {
                snapshot["windowBounds"] = RectangleJson(
                    new Rectangle(
                        frame.OriginX,
                        frame.OriginY,
                        frame.NativeWidth,
                        frame.NativeHeight));
                snapshot["screenshotPngBase64"] = frame.PngBase64;
                snapshot["screenshotMimeType"] = "image/png";
                snapshot["screenshotEncoding"] = "png";
                snapshot["coordinateSpace"] = screenshotScope == "screen"
                    ? "screen_image_px"
                    : "window_image_px";
                snapshot["frameId"] = frame.FrameId;
                snapshot["width"] = frame.Width;
                snapshot["height"] = frame.Height;
            }
        }
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
            if (providerObjectPath.StartsWith(
                    "runtime:",
                    StringComparison.Ordinal) &&
                !seenProviderObjectPaths.Add(providerObjectPath))
            {
                return false;
            }
            records.Add(record);
            if (providerObjectPath.StartsWith(
                    "runtime:",
                    StringComparison.Ordinal))
            {
                observedElementLeases[providerObjectPath] =
                    new ObservedElementLease<AutomationElement>(
                        element,
                        record);
            }
            lines.Add(
                new string(' ', depth * 2) +
                $"{index} {record["controlType"]?.GetValue<string>() ?? "unknown"} " +
                $"{record["name"]?.GetValue<string>() ?? string.Empty}");
            index++;
            return true;
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
