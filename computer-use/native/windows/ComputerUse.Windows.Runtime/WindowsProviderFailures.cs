using System.Text.Json.Nodes;

namespace ComputerUse.Windows.Runtime;

internal static class WindowsProviderFailures
{
    internal static ProviderFailureException StaleApp(string message)
    {
        return new ProviderFailureException(
            "stale_app_ref",
            "precondition",
            message,
            factsComplete: true,
            subject: "app");
    }

    internal static ProviderFailureException StaleWindow(
        string message,
        JsonObject? recoveryContext = null)
    {
        return new ProviderFailureException(
            "stale_window_ref",
            "precondition",
            message,
            factsComplete: true,
            subject: "window",
            recoveryContext: recoveryContext);
    }
}
