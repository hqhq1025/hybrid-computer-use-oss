using System.Diagnostics;
using System.Drawing;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json.Nodes;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;
using FlaUI.Core.Definitions;
using FlaUI.UIA3;

namespace ComputerUse.Windows.Runtime;

internal sealed partial class UiaProvider : IDisposable
{
    private const int MaxDesktopRootCandidates = 512;
    private const int MaxDesktopTransientCandidates = 2048;
    private const int MaxDesktopTransientDepth = 8;

    private const int DefaultNodeLimit = 5000;
    private const int DefaultDepthLimit = 96;
    private readonly UIA3Automation _automation =
        WindowsUiaAutomationFactory.Create();
    private readonly WindowLifecycleTracker _windowLifecycle = new();
    private readonly ObservedElementLeaseRegistry<AutomationElement>
        _observedElementLeases = new();
    private readonly string _sessionNonce = Convert.ToHexString(
        RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
    private sealed record WindowBinding(
        nint Handle,
        int ProcessId,
        string ProcessGeneration,
        long LifecycleGeneration);
    private sealed record NativeWindowProjection(
        IReadOnlyList<NativeDesktop.WindowRecord> NativeWindows,
        IReadOnlyList<OwnedNativeWindowCandidate> Candidates,
        long PreferredLogicalHandle)
    {
        private Dictionary<long, long>? _logicalHandles;

        internal nint LogicalHandle(nint handle)
        {
            _logicalHandles ??= Candidates.ToDictionary(
                candidate => candidate.Handle,
                candidate => OwnedNativeWindowProjection.ResolveLogicalHandle(
                    Candidates, candidate.Handle));
            return (nint)_logicalHandles.GetValueOrDefault(handle.ToInt64(), handle.ToInt64());
        }

        internal IReadOnlyList<NativeDesktop.WindowRecord> Members(
            nint logicalHandle)
        {
            return NativeWindows
                .Where(window => LogicalHandle(window.Handle) == logicalHandle)
                .OrderByDescending(window => window.Handle == logicalHandle)
                .ToArray();
        }

        internal IReadOnlyList<NativeDesktop.WindowRecord> LogicalWindows()
        {
            return NativeWindows
                .Where(window => LogicalHandle(window.Handle) == window.Handle)
                .Select(LogicalRecord)
                .ToArray();
        }

        internal NativeDesktop.WindowRecord LogicalRecord(
            NativeDesktop.WindowRecord selected)
        {
            var logicalHandle = LogicalHandle(selected.Handle);
            var logical = NativeWindows.FirstOrDefault(
                window => window.Handle == logicalHandle) ?? selected;
            var members = Members(logicalHandle);
            return logical with
            {
                Active = members.Any(window => window.Active),
                Bounds = members
                    .Select(window => window.Bounds)
                    .Aggregate(logical.Bounds, Rectangle.Union),
            };
        }
    }

    private readonly Dictionary<string, WindowBinding> _windowHandles = new(
        StringComparer.Ordinal);

    internal JsonObject ObserveApps()
    {
        // One window enumeration serves the whole listing, so every app is
        // sampled against the same foreground window.
        var desktopWindows = NativeDesktop.ListWindows();
        var groups = WindowsAppInventory.Group(
            desktopWindows.Select(window => window.ToTopLevel()));
        var nativeWindows = desktopWindows
            .GroupBy(window => window.ProcessId)
            .ToDictionary(group => group.Key, group => group.ToArray());
        var entries = new List<(string Name, int Pid, string Text, JsonObject App)>();
        foreach (var group in groups)
        {
            try
            {
                using var process = Process.GetProcessById(group.OwnerProcessId);
                var projection = ProjectNativeWindows(
                    process,
                    nativeWindows.GetValueOrDefault(group.OwnerProcessId) ??
                        Array.Empty<NativeDesktop.WindowRecord>());
                var members = group.Windows
                    .Select(window => (long)projection.LogicalHandle((nint)window.Handle))
                    .ToHashSet();
                var targets = group.Windows
                    .Where(WindowsAppInventory.IsInputTarget)
                    .Select(window => (long)projection.LogicalHandle((nint)window.Handle))
                    .ToHashSet();
                var windows = projection.LogicalWindows()
                    .Where(window => members.Contains(window.Handle.ToInt64()))
                    .ToArray();
                if (windows.Length == 0)
                {
                    continue;
                }
                var generation = ProcessGeneration(process);
                var windowInventory = new JsonArray();
                foreach (var window in windows)
                {
                    var handle = OpaqueWindowHandle(process, window.Handle);
                    windowInventory.Add(new JsonObject
                    {
                        ["title"] = window.Title,
                        ["role"] = "window",
                        ["handle"] = handle,
                        ["providerBusName"] = $"win32:pid:{process.Id}",
                        ["providerObjectPath"] = handle,
                        ["processGeneration"] = generation,
                        ["active"] = window.Active,
                        ["inputTarget"] = targets.Contains(window.Handle.ToInt64()),
                    });
                }
                var host = group.AppProcessId == group.OwnerProcessId
                    ? string.Empty
                    : $" in {process.ProcessName} pid={process.Id}";
                var app = AppJson(process, group.AppProcessId);
                var name = app["name"]!.GetValue<string>();
                var text =
                    $"{name} (pid={group.AppProcessId}{host}, windows={windows.Length})";
                app["displayText"] = text;
                app["inputCapable"] = targets.Count > 0;
                app["windows"] = windowInventory;
                entries.Add((name, group.AppProcessId, text, app));
            }
            catch
            {
                // A process can terminate between enumeration and inspection.
            }
        }
        var ordered = entries
            .OrderBy(entry => entry.Name, StringComparer.OrdinalIgnoreCase)
            .ThenBy(entry => entry.Pid)
            .ToArray();
        return new JsonObject
        {
            ["ok"] = true,
            ["text"] = string.Join(
                Environment.NewLine,
                ordered.Select(entry => entry.Text)),
            ["apps"] = new JsonArray(
                ordered.Select(entry => (JsonNode)entry.App).ToArray()),
        };
    }

    internal JsonObject Observe(JsonObject request)
    {
        var query = RequiredString(request, "app");
        var (process, scope) = ResolveApp(request, query);
        try
        {
            var selected = ResolveWindow(
                process,
                null,
                OptionalString(request, "window_handle", string.Empty),
                "stale_window_ref",
                scope);
            return new JsonObject
            {
                ["ok"] = true,
                ["snapshot"] = BuildSnapshot(
                    process,
                    selected,
                    request["observation"] as JsonObject),
            };
        }
        catch (ProviderFailureException error) when (
            error.Code == "stale_window_ref")
        {
            throw WindowsProviderFailures.StaleWindow(
                error.Message,
                StaleWindowRecoveryContext(process));
        }
    }

    internal JsonObject ListWindows(JsonObject request)
    {
        var query = RequiredString(request, "app");
        if (request["app_identity"] is null && !int.TryParse(query, out _))
        {
            return ListWindowsByName(query);
        }
        var process = ResolveProcess(request, query);
        var windows = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id)).LogicalWindows();
        return new JsonObject
        {
            ["ok"] = true,
            ["app_name"] = process.ProcessName,
            ["app_pid"] = process.Id,
            ["window_titles"] = new JsonArray(
                windows.Select(value => JsonValue.Create(value.Title)).ToArray()),
        };
    }

    internal JsonObject ActOnWindow(JsonObject request)
    {
        var process = ResolveProcess(request, RequiredString(request, "app"));
        var target = request["target"] as JsonObject;
        var requestWindowHandle = OptionalString(
            request,
            "window_handle",
            string.Empty);
        _ = ResolveWindow(
            process,
            target,
            requestWindowHandle);
        var opaqueWindowHandle = OptionalString(
            target,
            "windowHandle",
            requestWindowHandle);
        if (string.IsNullOrEmpty(opaqueWindowHandle) ||
            !_windowHandles.TryGetValue(
                opaqueWindowHandle,
                out var binding))
        {
            throw new ProviderFailureException(
                "window_target_stale",
                "precondition",
                "Window actions require one exact current window reference; no input was emitted.");
        }
        var action = RequiredString(request, "action");
        if (action != "activate" && action != "close")
        {
            throw new ProviderFailureException(
                "unsupported_window_action",
                "precondition",
                $"Unsupported window action {action}.");
        }
        var selectedRoute = "uia.window." + action;
        var commandAttempted = false;
        var commandEmitted = false;
        var app = AppJson(process);
        var appName = process.ProcessName;
        var appPid = process.Id;
        try
        {
            var currentElement = _automation.FromHandle(binding.Handle);
            var windowPattern = currentElement.Patterns.Window.PatternOrDefault ??
                throw new ProviderFailureException(
                    "capability_unsupported",
                    "precondition",
                    "The exact window does not expose the UIA Window pattern.");
            commandAttempted = true;
            if (action == "activate")
            {
                if (windowPattern.WindowVisualState.Value ==
                    WindowVisualState.Minimized)
                {
                    windowPattern.SetWindowVisualState(
                        WindowVisualState.Normal);
                }
                NativeDesktop.ActivateWindow(binding.Handle);
            }
            else if (action == "close")
            {
                windowPattern.Close();
            }
            commandEmitted = true;
            var remaining = action == "close"
                ? WaitForWindowToDisappear(appPid, binding.Handle)
                : ProjectNativeWindows(
                    process,
                    NativeDesktop.ListWindows(appPid)).LogicalWindows();
            if (action == "close")
            {
                _windowHandles.Remove(opaqueWindowHandle);
                if (remaining.Any(value => value.Handle == binding.Handle))
                {
                    throw new ProviderFailureException(
                        "window_close_not_observed",
                        "postcondition",
                        "The close request was emitted, but the exact window remained visible.");
                }
            }
            var result = new JsonObject
            {
            ["ok"] = true,
            ["app_name"] = appName,
            ["app_pid"] = appPid,
            ["window_titles"] = new JsonArray(
                remaining.Select(value => JsonValue.Create(value.Title)).ToArray()),
            ["mutation"] = MutationFacts(true, false),
            ["action_evidence"] = ActionEvidence(
                selectedRoute,
                false,
                null,
                false,
                null,
                true),
            };
            if (remaining.Count > 0)
            {
                var selected = remaining.FirstOrDefault(value => value.Active) ??
                    remaining[0];
                result["snapshot"] = BuildSnapshot(process, selected, null);
            }
            else
            {
                result["snapshot"] = BuildWindowlessSnapshot(
                    app,
                    opaqueWindowHandle);
            }
            return result;
        }
        catch (ProviderFailureException error) when (commandAttempted)
        {
            throw new ProviderFailureException(
                error.Code,
                error.Phase,
                error.Message,
                commandEmitted || error.InputEventsEmitted,
                error.MutationUnknown,
                actionEvidence: ActionEvidence(
                    selectedRoute,
                    false,
                    null,
                    false,
                    null,
                    commandEmitted || error.InputEventsEmitted));
        }
        catch (Exception error) when (commandAttempted)
        {
            throw new ProviderFailureException(
                "post_window_action_observation_failed",
                "postcondition",
                error.Message,
                commandEmitted,
                true,
                actionEvidence: ActionEvidence(
                    selectedRoute,
                    false,
                    null,
                    false,
                    null,
                    commandEmitted));
        }
    }

    // The process an app-level request runs against. A name query resolves
    // through the visible top-level windows, so an ApplicationFrameHost
    // window counts as the UWP app it hosts and explorer.exe's taskbar
    // never counts as an application window.
    private sealed record ResolvedApp(
        Process Process,
        IReadOnlyCollection<long>? Windows);

    private Process ResolveProcess(JsonObject request, string query) =>
        ResolveApp(request, query).Process;

    private ResolvedApp ResolveApp(JsonObject request, string query)
    {
        var identity = request["app_identity"] as JsonObject;
        if (identity is not null)
        {
            var providerBusName = OptionalString(
                identity,
                "providerBusName",
                string.Empty);
            const string prefix = "uia3:pid:";
            if (providerBusName.StartsWith(prefix, StringComparison.Ordinal) &&
                int.TryParse(
                    providerBusName[prefix.Length..],
                    out var identityPID))
            {
                Process process;
                try
                {
                    process = Process.GetProcessById(identityPID);
                }
                catch (Exception error)
                {
                    throw WindowsProviderFailures.StaleApp(
                        $"The bound application process no longer exists: {error.Message}");
                }
                var expectedGeneration = OptionalString(
                    identity,
                    "processGeneration",
                    string.Empty);
                if (string.IsNullOrEmpty(expectedGeneration) ||
                    !string.Equals(
                        ProcessGeneration(process),
                        expectedGeneration,
                        StringComparison.Ordinal))
                {
                    process.Dispose();
                    throw WindowsProviderFailures.StaleApp(
                        "The bound application process generation is stale.");
                }
                return new(process, null);
            }
            throw WindowsProviderFailures.StaleApp(
                "The bound Windows application identity is malformed.");
        }
        var names = new Dictionary<int, string?>();
        var groups = WindowsAppInventory.Group(
            NativeDesktop.ListWindows().Select(window => window.ToTopLevel()));
        if (int.TryParse(query, out var pid))
        {
            // The apps listing names a hosted UWP app by its own pid; its
            // windows belong to the host process.
            var hosted = groups.FirstOrDefault(group =>
                group.AppProcessId == pid && group.OwnerProcessId != pid);
            return hosted is null
                ? new(Process.GetProcessById(pid), null)
                : new(
                    Process.GetProcessById(hosted.OwnerProcessId),
                    hosted.Windows.Select(window => window.Handle).ToArray());
        }
        var matches = WindowsAppInventory.MatchName(
            groups,
            id => ProcessName(id, names),
            query);
        if (matches.Count == 0)
        {
            throw new ProviderFailureException(
                "app_not_found",
                "precondition",
                WindowsAppInventory.MatchesOnlyInfrastructure(
                    groups,
                    id => ProcessName(id, names),
                    query)
                    ? $"Application {query} shows only desktop shell windows " +
                        "(taskbar, desktop), which take no application input. " +
                        "List windows with AX {\"mode\":\"apps\"}."
                    : $"Application {query} was not found.");
        }
        if (matches.Count > 1)
        {
            // An exact window reference names one of the processes.
            var bound = _windowHandles.TryGetValue(
                    OptionalString(request, "window_handle", string.Empty),
                    out var binding)
                ? matches.Where(match => match.OwnerProcessId == binding.ProcessId).ToArray()
                : Array.Empty<AppWindowGroup>();
            if (bound.Length != 1)
            {
                throw AmbiguousApp(query, matches, names);
            }
            matches = bound;
        }
        var match = matches[0];
        try
        {
            return new(
                Process.GetProcessById(match.OwnerProcessId),
                match.Windows.Select(window => window.Handle).ToArray());
        }
        catch (ArgumentException)
        {
            throw new ProviderFailureException(
                "app_not_found",
                "precondition",
                $"Application {query} exited while it was resolved.");
        }
    }

    // Several application processes show input windows under one name.
    // Picking the foreground one would guess; the candidates let the core
    // list each process's windows so the caller can pass one wN.
    private static ProviderFailureException AmbiguousApp(
        string query,
        IReadOnlyList<AppWindowGroup> matches,
        Dictionary<int, string?> names)
    {
        var candidates = new JsonArray();
        var labels = new List<string>();
        foreach (var match in matches)
        {
            var name = ProcessName(match.AppProcessId, names) ?? query;
            labels.Add($"{name} (pid={match.AppProcessId}, windows={match.Windows.Count})");
            try
            {
                using var owner = Process.GetProcessById(match.OwnerProcessId);
                candidates.Add(new JsonObject
                {
                    ["name"] = name,
                    ["providerBusName"] = $"uia3:pid:{owner.Id}",
                    ["processGeneration"] = ProcessGeneration(owner),
                });
            }
            catch (ArgumentException)
            {
                // An exited candidate cannot be addressed; it is still named.
            }
        }
        return new ProviderFailureException(
            "ax_app_ambiguous",
            "precondition",
            $"appTargetAmbiguous(\"{query}\"): {matches.Count} application " +
            $"processes with input windows match this query: {string.Join(", ", labels)}. " +
            "No action was attempted. Pass the wN of one window as window_ref; " +
            "AX {\"mode\":\"apps\"} lists the windows.",
            factsComplete: true,
            subject: "app",
            candidates: candidates);
    }

    // A title wait by application name asks whether any application of that
    // name shows such a window, so it lists every matching process; no match
    // is an empty listing, not a failure.
    private static JsonObject ListWindowsByName(string query)
    {
        var names = new Dictionary<int, string?>();
        var matches = WindowsAppInventory.MatchName(
            WindowsAppInventory.Group(
                NativeDesktop.ListWindows().Select(window => window.ToTopLevel())),
            id => ProcessName(id, names),
            query);
        var appNames = matches
            .Select(match => ProcessName(match.AppProcessId, names) ?? string.Empty)
            .Distinct(StringComparer.Ordinal)
            .ToArray();
        return new JsonObject
        {
            ["ok"] = true,
            ["app_name"] = appNames.Length == 1 ? appNames[0] : string.Empty,
            ["app_pid"] = matches.Count == 1 ? matches[0].AppProcessId : 0,
            ["window_titles"] = new JsonArray(
                matches
                    .SelectMany(match => match.Windows)
                    .Select(window => window.Title)
                    .Where(title => title.Length > 0)
                    .Select(title => (JsonNode)JsonValue.Create(title)!)
                    .ToArray()),
        };
    }

    private static string? ProcessName(int processId, Dictionary<int, string?> cache)
    {
        if (!cache.TryGetValue(processId, out var name))
        {
            try
            {
                using var process = Process.GetProcessById(processId);
                name = process.ProcessName;
            }
            catch (Exception)
            {
                name = null;
            }
            cache[processId] = name;
        }
        return name;
    }

    private JsonObject StaleWindowRecoveryContext(Process process)
    {
        var windows = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id)).LogicalWindows();
        var recovery = new JsonObject
        {
            ["sourceAppStatus"] = "alive",
            ["sourceWindowStatus"] = "gone",
            ["inventoryComplete"] = true,
        };
        if (windows.Count == 0)
        {
            return recovery;
        }
        var current = windows.FirstOrDefault(value => value.Active) ?? windows[0];
        var handle = OpaqueWindowHandle(process, current.Handle);
        recovery["currentWindow"] = new JsonObject
        {
            ["windowHandle"] = handle,
            ["title"] = current.Title,
            ["role"] = "window",
            ["providerBusName"] = $"win32:pid:{process.Id}",
            ["providerObjectPath"] = handle,
            ["processGeneration"] = ProcessGeneration(process),
        };
        return recovery;
    }

    private NativeWindowProjection ProjectNativeWindows(
        Process process,
        IReadOnlyList<NativeDesktop.WindowRecord> windows)
    {
        var candidates = windows.Select(
            window => new OwnedNativeWindowCandidate(
                window.Handle.ToInt64(),
                window.OwnerHandle.ToInt64(),
                window.ProcessId,
                IsIndependentManagedWindow(process, window),
                window.Bounds,
                window.ThreadId)).ToArray();
        return new NativeWindowProjection(
            windows,
            candidates,
            0);
    }

    private bool IsIndependentManagedWindow(
        Process process,
        NativeDesktop.WindowRecord window)
    {
        try
        {
            var root = _automation.FromHandle(window.Handle);
            return RunWithoutUiaCache(() =>
                root.ControlType == ControlType.Window &&
                root.Patterns.Window.PatternOrDefault is not null);
        }
        catch (Exception error)
        {
            Console.Error.WriteLine(
                "[provider] owned HWND classification failed closed for " +
                $"pid={process.Id} hwnd=0x{window.Handle.ToInt64():x}: " +
                $"{error.GetType().Name} HRESULT=0x{error.HResult:x8}");
            return true;
        }
    }

    private IReadOnlyList<AutomationElement> LogicalAutomationRoots(
        Process process,
        NativeDesktop.WindowRecord selectedWindow)
    {
        var projection = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id));
        var logical = projection.LogicalRecord(selectedWindow);
        return LogicalAutomationRoots(process, projection, logical);
    }

    private IReadOnlyList<AutomationElement> LogicalAutomationRoots(
        Process process,
        NativeWindowProjection projection,
        NativeDesktop.WindowRecord logical)
    {
        var projectedMembers = projection.Members(logical.Handle);
        var projectedHandles = projectedMembers
            .Select(member => member.Handle.ToInt64())
            .ToArray();
        var elements = new List<AutomationElement>();
        var candidates = new List<ProjectedUiaRootCandidate>();

        foreach (var member in projection.Members(logical.Handle))
        {
            try
            {
                AddCandidate(
                    _automation.FromHandle(member.Handle),
                    member.ProcessId,
                    member.Handle.ToInt64());
            }
            catch when (member.Handle != logical.Handle)
            {
                // A transient member may retire during one bounded observation.
            }
        }

        var desktop = _automation.GetDesktop();
        AddDesktopRoots(
            _automation.TreeWalkerFactory.GetControlViewWalker());
        AddDesktopRoots(
            _automation.TreeWalkerFactory.GetRawViewWalker());
        AddDesktopTransientRoots(
            _automation.TreeWalkerFactory.GetControlViewWalker());
        AddDesktopTransientRoots(
            _automation.TreeWalkerFactory.GetRawViewWalker());

        var selected = ProjectedUiaRootSelection.SelectIndices(
            candidates,
            process.Id,
            projectedHandles,
            logical.Bounds);
        if (selected.Count == 0)
        {
            throw new InvalidOperationException(
                "No UIA root matched the selected logical window.");
        }
        return selected.Select(index => elements[index]).ToArray();

        void AddDesktopRoots(ITreeWalker walker)
        {
            foreach (var desktopRoot in ReadControlChildren(
                         desktop,
                         walker,
                         MaxDesktopRootCandidates))
            {
                var rootProcessId = Safe(
                    () => desktopRoot.Properties.ProcessId.ValueOrDefault,
                    0);
                var nativeWindowHandle = Safe(
                    () => desktopRoot.Properties.NativeWindowHandle.ValueOrDefault,
                    0);
                AddCandidate(
                    desktopRoot,
                    rootProcessId,
                    nativeWindowHandle);
            }
        }

        void AddDesktopTransientRoots(ITreeWalker walker)
        {
            var pending = new Stack<(AutomationElement Element, int Depth)>();
            var initial = ReadControlChildren(
                desktop,
                walker,
                MaxDesktopRootCandidates);
            for (var index = initial.Length - 1; index >= 0; index--)
            {
                pending.Push((initial[index], 1));
            }
            var visited = 0;
            while (pending.Count > 0 &&
                   visited < MaxDesktopTransientCandidates)
            {
                var current = pending.Pop();
                visited++;
                var rootProcessId = Safe(
                    () => current.Element.Properties.ProcessId.ValueOrDefault,
                    0);
                if (rootProcessId != 0 && rootProcessId != process.Id)
                {
                    continue;
                }
                var controlType = Safe(
                    () => current.Element.ControlType,
                    ControlType.Unknown);
                if (controlType == ControlType.Menu ||
                    controlType == ControlType.ToolTip)
                {
                    AddCandidate(
                        current.Element,
                        rootProcessId,
                        Safe(
                            () => current.Element.Properties
                                .NativeWindowHandle.ValueOrDefault,
                            0),
                        spatialOwnershipEligible: true);
                }
                if (current.Depth >= MaxDesktopTransientDepth)
                {
                    continue;
                }
                var remaining =
                    MaxDesktopTransientCandidates - visited;
                if (remaining <= 0)
                {
                    continue;
                }
                var children = ReadControlChildren(
                    current.Element,
                    walker,
                    remaining);
                for (var index = children.Length - 1; index >= 0; index--)
                {
                    pending.Push((children[index], current.Depth + 1));
                }
            }
        }

        void AddCandidate(
            AutomationElement element,
            int rootProcessId,
            long nativeWindowHandle,
            bool spatialOwnershipEligible = false)
        {
            elements.Add(element);
            candidates.Add(
                new ProjectedUiaRootCandidate(
                    rootProcessId,
                    nativeWindowHandle,
                    RuntimeObjectPath(element),
                    Safe(
                        () => element.BoundingRectangle,
                        Rectangle.Empty),
                    Safe(
                        () =>
                            element.ControlType == ControlType.Window &&
                            element.Patterns.Window.PatternOrDefault is not null,
                        true),
                    spatialOwnershipEligible));
        }
    }

    public void Dispose()
    {
        _windowLifecycle.Dispose();
        _automation.UnregisterAllEvents();
        _automation.Dispose();
    }
}
