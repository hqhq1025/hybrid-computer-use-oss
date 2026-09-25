using FlaUI.UIA3;

namespace ComputerUse.Windows.Runtime;

internal static class WindowsUiaAutomationFactory
{
    internal static readonly TimeSpan TransactionTimeout =
        TimeSpan.FromSeconds(3);

    internal static UIA3Automation Create()
    {
        var automation = new UIA3Automation
        {
            TransactionTimeout = TransactionTimeout,
        };
        return automation;
    }
}
