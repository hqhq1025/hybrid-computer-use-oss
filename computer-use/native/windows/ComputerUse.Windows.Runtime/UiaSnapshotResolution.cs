using System.Diagnostics;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json.Nodes;
using FlaUI.Core;
using FlaUI.Core.AutomationElements;

namespace ComputerUse.Windows.Runtime;

internal sealed partial class UiaProvider
{
    private AutomationElement? ResolveElement(
        AutomationElement root,
        JsonObject? target,
        JsonObject? record,
        Process process,
        NativeDesktop.WindowRecord selectedWindow,
        Func<AutomationElement>? refreshRoot = null)
    {
        if (target is null && record is null)
        {
            return null;
        }
        var requestedProviderObjectPath = OptionalString(
            target,
            "providerObjectPath",
            OptionalString(record, "providerObjectPath", string.Empty));
        var allowSemanticProviderRebind =
            ProviderTargetResolutionPolicy.AllowsSemanticProviderRebind(
                target);
        if (requestedProviderObjectPath.StartsWith(
                SpreadsheetCellScope + ":",
                StringComparison.Ordinal))
        {
            return ResolveSpreadsheetCell(
                process, selectedWindow, requestedProviderObjectPath, target, record);
        }
        if (requestedProviderObjectPath.StartsWith(
                "runtime:",
                StringComparison.Ordinal))
        {
            var logicalWindow = ProjectNativeWindows(
                process,
                NativeDesktop.ListWindows(process.Id))
                .LogicalRecord(selectedWindow);
            var leaseFound = _observedElementLeases.TryGet(
                process.Id,
                ProcessGeneration(process),
                logicalWindow.Handle.ToInt64(),
                requestedProviderObjectPath,
                out var observedLease,
                out var leaseDiagnostic);
            var nameMatches = leaseFound &&
                observedLease is not null &&
                CanonicalResolutionRecord.NameMatches(
                observedLease.CanonicalRecord,
                target,
                record);
            var roleMatches = leaseFound &&
                observedLease is not null &&
                CanonicalResolutionRecord.RoleMatches(
                observedLease.CanonicalRecord,
                target,
                record);
            var predicatesMatch = leaseFound &&
                observedLease is not null &&
                CanonicalResolutionRecord.PredicatesMatch(
                observedLease.CanonicalRecord,
                target,
                record);
            if (leaseFound &&
                observedLease is not null &&
                nameMatches &&
                roleMatches &&
                predicatesMatch)
            {
                EnsureObservedElementLeaseUsable(
                    observedLease.Element,
                    target,
                    record);
                return observedLease.Element;
            }
            leaseDiagnostic["name_matches"] = nameMatches;
            leaseDiagnostic["role_matches"] = roleMatches;
            leaseDiagnostic["predicates_match"] = predicatesMatch;
            leaseDiagnostic["result"] = leaseFound
                ? "canonical_mismatch"
                : "lookup_miss";
            Console.Error.WriteLine(leaseDiagnostic.ToJsonString());
        }
        if (AuthorizedCanonicalResolutionPolicy
            .UseCanonicalBeforeProviderIdentity(
                allowSemanticProviderRebind,
                requestedProviderObjectPath))
        {
            return ResolveCanonicalSemanticElement(
                root,
                target,
                record,
                process,
                selectedWindow,
                refreshRoot,
                requestedProviderObjectPath,
                allowSemanticProviderRebind);
        }
        if (requestedProviderObjectPath.StartsWith(
                "runtime:",
                StringComparison.Ordinal))
        {
            try
            {
                return ResolveProviderObjectPath(
                    LogicalAutomationRoots(process, selectedWindow),
                    requestedProviderObjectPath,
                    resolved =>
                    {
                        EnsureTargetStillMatches(resolved, target, record);
                    });
            }
            catch (ProviderFailureException error) when (
                error.Code == "stale_element_ref" &&
                allowSemanticProviderRebind)
            {
                // The wrapper authorizes this only after a fresh, complete,
                // same-window semantic observation. Continue below and prove
                // that the selector has exactly one live match before input.
            }
        }
        else if (!string.IsNullOrEmpty(requestedProviderObjectPath))
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The provider-native target identity is unsupported or stale; no input was emitted.");
        }
        var identityScope = OptionalString(
            target,
            "providerIdentityScope",
            OptionalString(record, "providerIdentityScope", string.Empty));
        if (identityScope == "snapshot_only")
        {
            return ResolveSnapshotPath(
                LogicalAutomationRoots(process, selectedWindow),
                target,
                record);
        }
        return ResolveCanonicalSemanticElement(
            root,
            target,
            record,
            process,
            selectedWindow,
            refreshRoot,
            requestedProviderObjectPath,
            allowSemanticProviderRebind);
    }

    private AutomationElement ResolveCanonicalSemanticElement(
        AutomationElement root,
        JsonObject? target,
        JsonObject? record,
        Process process,
        NativeDesktop.WindowRecord selectedWindow,
        Func<AutomationElement>? refreshRoot,
        string requestedProviderObjectPath,
        bool allowSemanticProviderRebind)
    {
        var requestedName = OptionalString(
            target,
            "name",
            OptionalString(record, "name", string.Empty));
        var hasName = target?["hasName"]?.GetValue<bool>() ??
            !string.IsNullOrEmpty(requestedName);
        var readableCandidates = 0;
        var nameMatches = 0;
        var roleMatches = 0;
        var readFailures = 0;
        var resolutionAttempts = 0;
        var diagnosticRoot = root;
        var semanticCandidateCount = 0;
        var resolvedSemanticTarget =
            SemanticResolutionRetry.ResolveWithContext<
                AutomationElement?,
                AutomationElement>(
            allowSemanticProviderRebind,
            attempt =>
                attempt == 0 || refreshRoot is null
                    ? root
                    : TryRefreshResolutionRoot(refreshRoot),
            (attempt, currentRoot) =>
            {
                resolutionAttempts++;
                if (currentRoot is null)
                {
                    semanticCandidateCount = 0;
                    readableCandidates = 0;
                    nameMatches = 0;
                    roleMatches = 0;
                    readFailures = 0;
                    return null;
                }
                diagnosticRoot = currentRoot;
                readableCandidates = 0;
                nameMatches = 0;
                roleMatches = 0;
                readFailures = 0;
                var snapshot = BuildSnapshot(
                    process,
                    selectedWindow,
                    null);
                if (snapshot["annotations"] is JsonArray gaps && gaps.Count != 0)
                {
                    throw new ProviderFailureException(
                        "window_ownership_unavailable", "precondition",
                        "An incomplete canonical sample cannot prove a unique semantic target; no input was emitted.");
                }
                var candidates = snapshot["elements"] as JsonArray ??
                    new JsonArray();
                semanticCandidateCount = candidates.Count;
                readableCandidates = candidates.Count;
                var matches = new List<JsonObject>();
                foreach (var candidate in candidates.OfType<JsonObject>())
                {
                    if (!CanonicalResolutionRecord.NameMatches(
                            candidate,
                            target,
                            record))
                    {
                        continue;
                    }
                    nameMatches++;
                    if (!CanonicalResolutionRecord.RoleMatches(
                            candidate,
                            target,
                            record))
                    {
                        continue;
                    }
                    roleMatches++;
                    if (!CanonicalResolutionRecord.PredicatesMatch(
                            candidate,
                            target,
                            record))
                    {
                        continue;
                    }
                    matches.Add(candidate);
                    if (matches.Count > 1)
                    {
                        break;
                    }
                }
                if (matches.Count > 1)
                {
                    LogSemanticResolutionDiagnostic(
                        currentRoot,
                        requestedProviderObjectPath,
                        requestedName,
                        hasName,
                        semanticCandidateCount,
                        readableCandidates,
                        nameMatches,
                        roleMatches,
                        readFailures,
                        matches.Count,
                        resolutionAttempts,
                        target);
                    throw new ProviderFailureException(
                        "semantic_target_ambiguous",
                        "precondition",
                        $"The exact target matched {matches.Count} elements; no input was emitted.");
                }
                if (matches.Count != 1)
                {
                    return null;
                }
                var liveRoots = LogicalAutomationRoots(
                    process,
                    selectedWindow);
                var currentProviderObjectPath = OptionalString(
                    matches[0],
                    "providerObjectPath",
                    string.Empty);
                if (currentProviderObjectPath.StartsWith(
                        "runtime:",
                        StringComparison.Ordinal))
                {
                    return ResolveProviderObjectPath(
                        liveRoots,
                        currentProviderObjectPath);
                }
                return ResolveCanonicalSnapshotRecord(liveRoots, matches[0]);
            });
        if (resolvedSemanticTarget is null)
        {
            LogSemanticResolutionDiagnostic(
                diagnosticRoot,
                requestedProviderObjectPath,
                requestedName,
                hasName,
                semanticCandidateCount,
                readableCandidates,
                nameMatches,
                roleMatches,
                readFailures,
                0,
                resolutionAttempts,
                target);
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The exact target is not present; no input was emitted.");
        }
        return resolvedSemanticTarget;
    }

    private static void EnsureObservedElementLeaseUsable(
        AutomationElement element,
        JsonObject? target,
        JsonObject? record)
    {
        if (!RoleMatchesTarget(element, target, record) ||
            !TargetPredicatesMatch(element, target, record))
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The observed provider element is no longer usable; no input was emitted.");
        }
    }

    private AutomationElement ResolveCanonicalSnapshotRecord(
        IReadOnlyList<AutomationElement> roots,
        JsonObject candidate)
    {
        if (!CanonicalResolutionRecord.TryTreePath(
                candidate,
                out var path))
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The canonical target has no valid tree path; no input was emitted.");
        }
        if (path.Length == 0 ||
            path[0] < 0 ||
            path[0] >= roots.Count)
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The canonical target root is no longer present; no input was emitted.");
        }
        var current = roots[path[0]];
        var controlWalker =
            _automation.TreeWalkerFactory.GetControlViewWalker();
        for (var depth = 1; depth < path.Length; depth++)
        {
            var childIndex = path[depth];
            if (childIndex >= DefaultNodeLimit)
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The canonical target tree path exceeds the bounded child budget; no input was emitted.");
            }
            AutomationElement[] children;
            try
            {
                using (FlaUI.Core.CacheRequest.ForceNoCache())
                {
                    children = ReadControlChildren(
                        current,
                        controlWalker,
                        childIndex + 1);
                }
            }
            catch
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The canonical target tree path is no longer readable; no input was emitted.");
            }
            if (childIndex >= children.Length)
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The canonical target tree path no longer exists; no input was emitted.");
            }
            current = children[childIndex];
        }
        string liveProviderObjectPath;
        using (FlaUI.Core.CacheRequest.ForceNoCache())
        {
            liveProviderObjectPath = RuntimeObjectPath(current);
        }
        if (!CanonicalResolutionRecord.ProviderIdentityMatches(
                candidate,
                liveProviderObjectPath))
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The canonical target identity changed before input; no input was emitted.");
        }
        return current;
    }

    private static AutomationElement? TryRefreshResolutionRoot(
        Func<AutomationElement> refreshRoot)
    {
        try
        {
            return refreshRoot();
        }
        catch
        {
            // A provider root can disappear during a bounded read-only sample.
            // Treat it as absence and let the remaining samples decide.
            return null;
        }
    }

    private static void LogSemanticResolutionDiagnostic(
        AutomationElement root,
        string requestedProviderObjectPath,
        string requestedName,
        bool hasName,
        int candidateCount,
        int readableCandidates,
        int nameMatches,
        int roleMatches,
        int readFailures,
        int finalMatches,
        int resolutionAttempts,
        JsonObject? target)
    {
        try
        {
            var diagnostic = new JsonObject
            {
                ["event"] = "uia_semantic_resolution_diagnostic",
                ["requested_provider_path_present"] =
                    !string.IsNullOrEmpty(requestedProviderObjectPath),
                ["requested_name"] = requestedName,
                ["has_name"] = hasName,
                ["requested_role"] = OptionalString(
                    target,
                    "role",
                    string.Empty),
                ["provider_role_aliases"] =
                    target?["providerRoleAliases"]?.DeepClone(),
                ["candidate_count"] = candidateCount,
                ["readable_candidates"] = readableCandidates,
                ["name_matches"] = nameMatches,
                ["role_matches"] = roleMatches,
                ["read_failures"] = readFailures,
                ["final_matches"] = finalMatches,
                ["resolution_attempts"] = resolutionAttempts,
                ["root_name"] = Safe(
                    () => root.Name,
                    string.Empty),
                ["root_role"] = Safe(
                    () => CanonicalRole(root.ControlType),
                    string.Empty),
                ["root_runtime_path"] = RuntimeObjectPath(root),
            };
            Console.Error.WriteLine(diagnostic.ToJsonString());
        }
        catch
        {
            // Diagnostics must never change target-resolution behavior.
        }
    }

    private int VisitResolutionCandidates(
        IReadOnlyList<AutomationElement> roots,
        Func<AutomationElement, bool> visit)
    {
        var seenProviderObjectPaths = new HashSet<string>(
            StringComparer.Ordinal);
        var candidateCount = 0;
        var controlWalker =
            _automation.TreeWalkerFactory.GetControlViewWalker();
        ResolutionCandidateScope.Visit<IDisposable, AutomationElement>(
            CacheRequest.ForceNoCache,
            scopedVisit =>
            {
                foreach (var root in roots)
                {
                    if (!Walk(root, 0, scopedVisit))
                    {
                        break;
                    }
                }
            },
            element =>
            {
                candidateCount++;
                return visit(element);
            });
        return candidateCount;

        bool Walk(
            AutomationElement element,
            int depth,
            Func<AutomationElement, bool> scopedVisit)
        {
            if (candidateCount >= DefaultNodeLimit ||
                depth > DefaultDepthLimit)
            {
                return candidateCount < DefaultNodeLimit;
            }
            var providerObjectPath = RuntimeObjectPath(element);
            if (!ProviderResolutionCandidatePolicy.ShouldInclude(
                    providerObjectPath,
                    seenProviderObjectPaths))
            {
                return true;
            }
            if (!scopedVisit(element))
            {
                return false;
            }
            AutomationElement[] children;
            try
            {
                children = ReadControlChildren(
                    element,
                    controlWalker,
                    DefaultNodeLimit - candidateCount);
            }
            catch
            {
                return true;
            }
            foreach (var child in children)
            {
                if (!Walk(child, depth + 1, scopedVisit))
                {
                    return false;
                }
            }
            return true;
        }
    }

    // A spreadsheet cell is found again by a hit test at its rectangle in
    // the latest observation of the window. If the sheet scrolled or a
    // window covers the cell, the hit returns another element and the
    // reference is stale.
    private AutomationElement ResolveSpreadsheetCell(
        Process process,
        NativeDesktop.WindowRecord selectedWindow,
        string providerObjectPath,
        JsonObject? target,
        JsonObject? record)
    {
        var logicalWindow = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id))
            .LogicalRecord(selectedWindow);
        if (!_observedElementLeases.TryGet(
                process.Id,
                ProcessGeneration(process),
                logicalWindow.Handle.ToInt64(),
                providerObjectPath,
                out var lease) ||
            lease?.CanonicalRecord["frame"] is not JsonObject frame ||
            !SpreadsheetGrid.TryParseA1(
                lease.CanonicalRecord["name"]?.GetValue<string>(), out var row, out var column))
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The cell is not in the latest observation of this window; no input was emitted.");
        }
        var center = new System.Drawing.Point(
            Safe(() => frame["x"]!.GetValue<int>() + frame["width"]!.GetValue<int>() / 2, int.MinValue),
            Safe(() => frame["y"]!.GetValue<int>() + frame["height"]!.GetValue<int>() / 2, int.MinValue));
        AutomationElement? hit = null;
        if (center.X != int.MinValue && center.Y != int.MinValue &&
            SpreadsheetGrid.PointShowsWindow(center, logicalWindow.Handle))
        {
            using (FlaUI.Core.CacheRequest.ForceNoCache())
            {
                hit = Safe<AutomationElement?>(() => _automation.FromPoint(center), null);
            }
        }
        if (hit is null ||
            !SpreadsheetGrid.TryParseA1(Safe(() => hit.Name, string.Empty), out var hitRow, out var hitColumn) ||
            hitRow != row ||
            hitColumn != column)
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The cell is no longer at its observed position; no input was emitted.");
        }
        EnsureTargetStillMatches(hit, target, record);
        return hit;
    }

    private AutomationElement ResolveSnapshotPath(
        IReadOnlyList<AutomationElement> roots,
        JsonObject? target,
        JsonObject? record)
    {
        var source = target?["runtimeId"] ?? record?["runtimeId"];
        if (source is not JsonArray path || path.Count == 0)
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The snapshot-only target has no tree path; no input was emitted.");
        }
        var indices = new List<int>(path.Count);
        foreach (var node in path)
        {
            if (node is not JsonValue value ||
                !value.TryGetValue<int>(out var index) ||
                index < 0)
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The snapshot-only target tree path is invalid; no input was emitted.");
            }
            indices.Add(index);
        }
        if (indices[0] < 0 || indices[0] >= roots.Count)
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The snapshot-only target tree path is not rooted; no input was emitted.");
        }
        var current = roots[indices[0]];
        var controlWalker =
            _automation.TreeWalkerFactory.GetControlViewWalker();
        for (var depth = 1; depth < indices.Count; depth++)
        {
            var childIndex = indices[depth];
            if (childIndex >= DefaultNodeLimit)
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The snapshot-only target tree path exceeds the bounded child budget; no input was emitted.");
            }
            AutomationElement[] children;
            try
            {
                children = ReadControlChildren(
                    current,
                    controlWalker,
                    childIndex + 1);
            }
            catch
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The snapshot-only target tree path is no longer readable; no input was emitted.");
            }
            if (childIndex >= children.Length)
            {
                throw new ProviderFailureException(
                    "stale_element_ref",
                    "precondition",
                    "The snapshot-only target tree path no longer exists; no input was emitted.");
            }
            current = children[childIndex];
        }
        EnsureTargetStillMatches(current, target, record);
        return current;
    }

    private JsonObject ResolvedElementJson(
        AutomationElement root,
        AutomationElement resolved,
        Process process,
        NativeDesktop.WindowRecord window)
    {
        var requestedObjectPath = RuntimeObjectPath(resolved);
        if (string.IsNullOrEmpty(requestedObjectPath))
        {
            throw new ProviderFailureException(
                "semantic_target_not_found",
                "observation",
                "The resolved target has no provider-native identity.");
        }
        var snapshot = BuildSnapshot(process, window, null);
        var elements = snapshot["elements"] as JsonArray ?? new JsonArray();
        var matches = elements
            .OfType<JsonObject>()
            .Where(record => string.Equals(
                record["providerObjectPath"]?.GetValue<string>(),
                requestedObjectPath,
                StringComparison.Ordinal))
            .ToArray();
        if (matches.Length == 0)
        {
            throw new ProviderFailureException(
                "semantic_target_not_found",
                "observation",
                "The resolved target disappeared before it could be sampled.");
        }
        if (matches.Length != 1)
        {
            throw new ProviderFailureException(
                "semantic_target_ambiguous",
                "observation",
                "The resolved provider-native target identity is not unique.");
        }
        return (JsonObject)matches[0].DeepClone();
    }

    private AutomationElement ResolveProviderObjectPath(
        IReadOnlyList<AutomationElement> roots,
        string requestedProviderObjectPath,
        Action<AutomationElement>? validate = null)
    {
        var matches = new List<AutomationElement>();
        _ = VisitResolutionCandidates(
            roots,
            element =>
            {
                var objectPath = RuntimeObjectPath(element);
                if (!string.IsNullOrEmpty(objectPath) &&
                    string.Equals(
                        objectPath,
                        requestedProviderObjectPath,
                        StringComparison.Ordinal))
                {
                    validate?.Invoke(element);
                    matches.Add(element);
                }
                return matches.Count < 2;
            });
        if (matches.Count == 0)
        {
            throw new ProviderFailureException(
                "stale_element_ref",
                "precondition",
                "The provider-native target identity is stale; no input was emitted.");
        }
        if (matches.Count != 1)
        {
            throw new ProviderFailureException(
                "semantic_target_ambiguous",
                "precondition",
                "The provider-native target identity is not unique; no input was emitted.");
        }
        return matches[0];
    }

    private static string RuntimeObjectPath(AutomationElement element)
    {
        var runtimeId = Safe(
            () => element.Properties.RuntimeId.ValueOrDefault ?? Array.Empty<int>(),
            Array.Empty<int>());
        return runtimeId.Length == 0 ? string.Empty : RuntimeObjectPath(runtimeId);
    }

    private static string RuntimeObjectPath(IReadOnlyList<int> runtimeId)
    {
        return "runtime:" + string.Join('.', runtimeId);
    }

    private string MissingWindowMessage(string opaqueWindowHandle) =>
        WindowsWindowIdentityPolicy.MissingWindowMessage(BoundWindowLifecycle(
            _windowHandles.TryGetValue(opaqueWindowHandle, out var binding) ? binding : null));

    private NativeDesktop.WindowRecord ResolveWindow(
        Process process,
        JsonObject? target,
        string requestWindowHandle,
        string staleFailureCode = "window_target_stale",
        IReadOnlyCollection<long>? scope = null)
    {
        var opaque = OptionalString(target, "windowHandle", requestWindowHandle);
        var projection = ProjectNativeWindows(
            process,
            NativeDesktop.ListWindows(process.Id));
        var windows = projection.LogicalWindows();
        if (windows.Count == 0)
        {
            if (!string.IsNullOrEmpty(requestWindowHandle) ||
                !string.IsNullOrEmpty(OptionalString(
                    target,
                    "windowHandle",
                    string.Empty)))
            {
                throw WindowsProviderFailures.StaleWindow(MissingWindowMessage(opaque));
            }
            throw new ProviderFailureException(
                "window_not_found",
                "precondition",
                "The application has no visible top-level window.");
        }
        if (!string.IsNullOrEmpty(opaque))
        {
            if (!_windowHandles.TryGetValue(opaque, out var binding))
            {
                throw staleFailureCode == "stale_window_ref"
                    ? WindowsProviderFailures.StaleWindow(
                        "The exact window reference is stale; no input was emitted.")
                    : new ProviderFailureException(
                        staleFailureCode,
                        "precondition",
                        "The exact window reference is stale; no input was emitted.");
            }
            if (!WindowsWindowIdentityPolicy.Matches(
                    binding.Handle,
                    binding.ProcessId,
                    binding.ProcessGeneration,
                    binding.LifecycleGeneration,
                    currentHandle: binding.Handle,
                    currentProcessId: process.Id,
                    currentProcessGeneration: ProcessGeneration(process),
                    currentLifecycleGeneration:
                        _windowLifecycle.Generation(binding.Handle)))
            {
                _windowHandles.Remove(opaque);
                throw staleFailureCode == "stale_window_ref"
                    ? WindowsProviderFailures.StaleWindow(
                        "The exact window process identity is stale; no input was emitted.")
                    : new ProviderFailureException(
                        staleFailureCode,
                        "precondition",
                        "The exact window process identity is stale; no input was emitted.");
            }
            var exact = windows.FirstOrDefault(
                value => value.Handle == binding.Handle);
            if (exact is null)
            {
                var message = WindowsWindowIdentityPolicy.MissingWindowMessage(
                    BoundWindowLifecycle(binding));
                throw staleFailureCode == "stale_window_ref"
                    ? WindowsProviderFailures.StaleWindow(message)
                    : new ProviderFailureException(staleFailureCode, "precondition", message);
            }
            return exact;
        }
        var nativeWindows = projection.NativeWindows
            .Select(window => window.ToTopLevel())
            .ToArray();
        var preferred = WindowsAppInventory.DefaultWindow(
            nativeWindows,
            windows.Select(window => window.Handle.ToInt64()).ToArray(),
            handle => projection.LogicalHandle((nint)handle).ToInt64(),
            scope);
        if (preferred is long handle)
        {
            return windows.First(window => window.Handle.ToInt64() == handle);
        }
        if (scope is not null)
        {
            throw new ProviderFailureException(
                "window_not_found",
                "precondition",
                "The application's windows closed while it was resolved.");
        }
        // An identity-bound application that shows no input window (only
        // tool or shell windows) keeps its previous default.
        var activeNative = projection.NativeWindows.FirstOrDefault(
            value => value.Active);
        return activeNative is not null
            ? projection.LogicalRecord(activeNative)
            : windows[0];
    }

    private string OpaqueWindowHandle(Process process, nint handle)
    {
        var processGeneration = ProcessGeneration(process);
        var lifecycleGeneration = _windowLifecycle.Generation(handle);
        foreach (var existing in _windowHandles)
        {
            var binding = existing.Value;
            if (WindowsWindowIdentityPolicy.Matches(
                    binding.Handle,
                    binding.ProcessId,
                    binding.ProcessGeneration,
                    binding.LifecycleGeneration,
                    handle,
                    process.Id,
                    processGeneration,
                    lifecycleGeneration))
            {
                return existing.Key;
            }
        }
        var material =
            $"{_sessionNonce}:{process.Id}:{processGeneration}:{handle}:" +
            $"{lifecycleGeneration}:{Guid.NewGuid():N}";
        var opaque = "w2:" + Convert.ToHexString(
            SHA256.HashData(Encoding.UTF8.GetBytes(material)))
            .ToLowerInvariant()[..32];
        _windowHandles[opaque] = new WindowBinding(
            handle,
            process.Id,
            processGeneration,
            lifecycleGeneration);
        return opaque;
    }
}
