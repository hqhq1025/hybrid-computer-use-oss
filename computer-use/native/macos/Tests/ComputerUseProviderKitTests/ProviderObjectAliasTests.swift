import ApplicationServices
import Foundation
@testable import ComputerUseProviderKit
import Testing

private func aliasRecord(
    _ index: Int,
    _ element: AXUIElement?,
    synthetic: Bool = false
) -> ElementRecord {
    ElementRecord(
        index: index,
        identifier: nil,
        element: element,
        localFrame: nil,
        rawActions: [],
        prettyActions: [],
        isSyntheticText: synthetic
    )
}

@Test
func providerObjectAliasesAcceptEqualAXObjects() throws {
    let first = AXUIElementCreateApplication(getpid())
    let second = AXUIElementCreateApplication(getpid())
    #expect(CFEqual(first, second))
    try validateMacProviderObjectAliases(
        [aliasRecord(1, first), aliasRecord(2, second)],
        objectHash: { _ in 7 }
    )
}

@Test
func providerObjectAliasesRejectForcedHashCollision() {
    let first = AXUIElementCreateSystemWide()
    let second = AXUIElementCreateApplication(getpid())
    #expect(!CFEqual(first, second))
    do {
        try validateMacProviderObjectAliases(
            [aliasRecord(1, first), aliasRecord(2, second)],
            objectHash: { _ in 7 }
        )
        Issue.record("Distinct objects sharing a hash were accepted")
    } catch {
        let failure = runtimeFailureResult(error)
        #expect(failure["failureCode"] as? String == "state_unavailable")
        #expect(failure["input_events_emitted"] as? Bool == false)
        #expect(failure["partial_input_possible"] as? Bool == false)
        #expect((failure["error"] as? String)?.contains("provider_identity_conflict") == true)
    }
}

@Test
func providerObjectAliasesCompareOnlyRepeatedRealObjectHashes() throws {
    let first = AXUIElementCreateSystemWide()
    let second = AXUIElementCreateApplication(getpid())
    var comparisons = 0
    try validateMacProviderObjectAliases(
        [aliasRecord(1, first), aliasRecord(2, first),
         aliasRecord(3, second), aliasRecord(4, second, synthetic: true),
         aliasRecord(5, nil)],
        objectHash: { CFEqual($0, first) ? 7 : 8 },
        objectsEqual: {
            comparisons += 1
            return CFEqual($0, $1)
        }
    )
    #expect(comparisons == 1)
}

@Test
func providerObjectAliasDiagnosticExcludesContentAndKeepsNativeIdentity() throws {
    let element = AXUIElementCreateApplication(getpid())
    let left = ElementRecord(
        index: 1, runtimeID: [0, 2], identifier: "secret-id",
        element: element, localFrame: nil, rawActions: [], prettyActions: [],
        name: "private title", description: "private description",
        placeholder: "private placeholder", value: "private value"
    )
    let facts = macProviderAliasConflictFacts(left, aliasRecord(2, element))
    #expect(facts["equal_forward"] as? Bool == true)
    #expect(facts["equal_reverse"] as? Bool == true)
    #expect(facts["same_wrapper"] as? Bool == true)
    let first = try #require(facts["left"] as? [String: Any])
    #expect(first["pid"] as? Int == Int(getpid()))
    #expect(first["pid_status"] as? Int == 0)
    #expect(first["runtime_path"] as? [Int] == [0, 2])
    let data = try JSONSerialization.data(withJSONObject: facts)
    let text = String(decoding: data, as: UTF8.self)
    #expect(!text.contains("private"))
    #expect(!text.contains("secret-id"))
}
