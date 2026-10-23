namespace ComputerUse.Windows.Runtime;

internal sealed class CanonicalNodeIdentitySet<T>(Func<T, T, bool> sameElement)
    where T : class
{
    private readonly Dictionary<string, T> _identified = new(StringComparer.Ordinal);
    private readonly List<T> _unidentified = new();

    internal bool TryAdd(T element, string providerIdentity)
    {
        if (providerIdentity.Length > 0 && _identified.ContainsKey(providerIdentity))
        {
            return false;
        }
        foreach (var previous in _unidentified)
        {
            if (sameElement(element, previous)) { return false; }
        }
        if (providerIdentity.Length == 0)
        {
            foreach (var previous in _identified.Values)
            {
                if (sameElement(element, previous)) { return false; }
            }
            _unidentified.Add(element);
        }
        else
        {
            _identified.Add(providerIdentity, element);
        }
        return true;
    }
}
