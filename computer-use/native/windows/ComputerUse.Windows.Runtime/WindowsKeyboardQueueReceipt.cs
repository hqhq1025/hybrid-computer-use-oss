namespace ComputerUse.Windows.Runtime;

internal interface IKeyboardInputBarrier : IDisposable
{
    nuint Tag(int index);
    void WaitForConsumption();
}

internal readonly record struct WindowsKeyboardQueueReceipt(
    int Sequence, bool StateAvailable, uint ModifiersDown);

internal static class WindowsKeyboardQueueReceipts
{
    // A later filtered key-up does not establish that earlier keys were read.
    internal static bool AreComplete(IReadOnlyList<WindowsKeyboardQueueReceipt> receipts)
        => receipts.Count > 0 && receipts.All(receipt => receipt.Sequence > 0);

    internal static bool ModifiersReleased(IReadOnlyList<WindowsKeyboardQueueReceipt> receipts)
    {
        if (!AreComplete(receipts)) return false;
        var final = receipts.MaxBy(receipt => receipt.Sequence);
        return final.StateAvailable && final.ModifiersDown == 0;
    }
}
