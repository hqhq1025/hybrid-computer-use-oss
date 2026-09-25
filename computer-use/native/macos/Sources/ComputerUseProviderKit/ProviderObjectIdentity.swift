import ApplicationServices
import Foundation

// Hashes select buckets only. Retained objects and equality establish identity.
// No eviction: losing a live identity must never masquerade as object absence.
final class ProviderObjectIdentityRegistry<Object>: @unchecked Sendable {
    private struct Bucket: Hashable {
        let scope: String
        let hash: UInt
    }

    private struct Entry {
        let object: Object
        let token: String
    }

    private let lock = NSLock()
    private let epoch = UUID().uuidString
    private let capacity: Int
    private let hash: (Object) -> UInt
    private let equal: (Object, Object) -> Bool
    private var buckets: [Bucket: [Entry]] = [:]
    private var count = 0

    init(
        capacity: Int = 65_536,
        hash: @escaping (Object) -> UInt,
        equal: @escaping (Object, Object) -> Bool
    ) {
        self.capacity = capacity
        self.hash = hash
        self.equal = equal
    }

    func bind(_ objects: [Object], scope: String) throws -> [String] {
        lock.lock()
        defer { lock.unlock() }
        var pending: [Bucket: [Entry]] = [:]
        var added = 0
        var comparisons = 0
        var tokens: [String] = []
        for object in objects {
            let key = Bucket(scope: scope, hash: hash(object))
            var match: String?
            for entries in [buckets[key] ?? [], pending[key] ?? []] {
                for entry in entries {
                    comparisons += 1
                    guard comparisons <= 131_072 else {
                        throw ComputerUseError.providerResetRequired(
                            "Native object identity comparison budget exhausted; restart the provider before another request."
                        )
                    }
                    if equal(entry.object, object) {
                        match = entry.token
                        break
                    }
                }
                if match != nil { break }
            }
            if let match {
                tokens.append(match)
                continue
            }
            guard count + added < capacity else {
                throw ComputerUseError.providerResetRequired(
                    "Native object identity capacity exhausted; restart the provider before another request."
                )
            }
            added += 1
            let token = "ax:\(epoch):\(count + added)"
            pending[key, default: []].append(Entry(object: object, token: token))
            tokens.append(token)
        }
        // Failed batches publish neither partial identities nor consumed IDs.
        for (key, entries) in pending {
            buckets[key, default: []].append(contentsOf: entries)
        }
        count += added
        return tokens
    }

    func matchesRetained(_ object: Object, token: String, scope: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        let key = Bucket(scope: scope, hash: hash(object))
        guard let entry = buckets[key]?.first(where: { $0.token == token }) else {
            return false
        }
        return equal(entry.object, object) && equal(object, entry.object)
    }
}

private let macProviderObjectRegistry = ProviderObjectIdentityRegistry<AXUIElement>(
    hash: { CFHash($0) },
    equal: { CFEqual($0, $1) }
)

func matchesRetainedMacProviderObject(
    _ object: AXUIElement, token: String, scope: String,
    registry: ProviderObjectIdentityRegistry<AXUIElement>? = nil
) -> Bool {
    (registry ?? macProviderObjectRegistry).matchesRetained(
        object, token: token, scope: scope
    )
}

func bindMacProviderObjectIdentities(
    _ records: [ElementRecord],
    scope: String,
    registry: ProviderObjectIdentityRegistry<AXUIElement>? = nil
) throws {
    let real = records.filter { !$0.isSyntheticText && $0.element != nil }
        .sorted { $0.index < $1.index }
    guard real.allSatisfy({ $0.boundProviderObjectPath == nil }) else {
        throw ComputerUseError.stateUnavailable(
            "Native object identities were already bound; snapshot rejected."
        )
    }
    let tokens = try (registry ?? macProviderObjectRegistry).bind(
        real.compactMap(\.element), scope: scope
    )
    for (record, token) in zip(real, tokens) {
        record.bindProviderObjectPath(token)
    }
}
