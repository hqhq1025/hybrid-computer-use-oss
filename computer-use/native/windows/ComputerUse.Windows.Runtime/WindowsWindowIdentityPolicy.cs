namespace ComputerUse.Windows.Runtime;

internal static class WindowsWindowIdentityPolicy
{
    internal static bool Matches(
        nint expectedHandle,
        int expectedProcessId,
        string expectedProcessGeneration,
        long expectedLifecycleGeneration,
        nint currentHandle,
        int currentProcessId,
        string currentProcessGeneration,
        long currentLifecycleGeneration)
    {
        return expectedHandle != 0 &&
            expectedHandle == currentHandle &&
            expectedProcessId == currentProcessId &&
            string.Equals(
                expectedProcessGeneration,
                currentProcessGeneration,
                StringComparison.Ordinal) &&
            expectedLifecycleGeneration == currentLifecycleGeneration;
    }
}
