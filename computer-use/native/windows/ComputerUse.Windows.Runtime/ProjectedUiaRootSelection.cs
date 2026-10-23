using System.Drawing;

namespace ComputerUse.Windows.Runtime;

internal sealed record ProjectedUiaRootCandidate(
    int ProcessId,
    long NativeWindowHandle,
    string RuntimeIdentity,
    Rectangle Bounds = default,
    bool IndependentWindow = false,
    bool SpatialOwnershipEligible = false);

internal static class ProjectedUiaRootSelection
{
    internal static IReadOnlyList<int> SelectIndices(
        IReadOnlyList<ProjectedUiaRootCandidate> candidates,
        int processId,
        IReadOnlyCollection<long> projectedHandles,
        Rectangle logicalBounds = default,
        IReadOnlyCollection<string>? precedingRuntimeIdentities = null)
    {
        var allowedHandles = projectedHandles.ToHashSet();
        var seenRuntimeIdentities = new HashSet<string>(
            StringComparer.Ordinal);
        if (precedingRuntimeIdentities is not null)
        {
            foreach (var identity in precedingRuntimeIdentities)
            {
                if (identity.Length > 0) { seenRuntimeIdentities.Add(identity); }
            }
        }
        var selected = new List<int>();
        for (var index = 0; index < candidates.Count; index++)
        {
            var candidate = candidates[index];
            var processOwned =
                candidate.ProcessId == processId ||
                (candidate.ProcessId == 0 &&
                 candidate.SpatialOwnershipEligible);
            if (!processOwned)
            {
                continue;
            }
            var projectedHandle =
                allowedHandles.Contains(candidate.NativeWindowHandle);
            var spatiallyOwnedTransient =
                candidate.SpatialOwnershipEligible &&
                !candidate.IndependentWindow &&
                Contains(logicalBounds, candidate.Bounds);
            if (!projectedHandle && !spatiallyOwnedTransient)
            {
                continue;
            }
            if (!string.IsNullOrEmpty(candidate.RuntimeIdentity) &&
                !seenRuntimeIdentities.Add(candidate.RuntimeIdentity))
            {
                continue;
            }
            selected.Add(index);
        }
        return selected;
    }

    private static bool Contains(Rectangle owner, Rectangle transient)
    {
        return owner.Width > 0 &&
            owner.Height > 0 &&
            transient.Width > 0 &&
            transient.Height > 0 &&
            owner.Contains(transient);
    }
}
