@testable import ComputerUseProviderKit
import Foundation
import Testing

@Test
func boundedSynchronousBridgeReturnsCompletedValue() throws {
    let value = try BlockingSynchronousBridge.run(timeout: 1) {
        "ready"
    }
    #expect(value == "ready")
}

@Test
func boundedSynchronousBridgeRequestsResetAfterTimeout() {
    let started = Date()
    do {
        _ = try BlockingSynchronousBridge.run(timeout: 0.05) {
            Thread.sleep(forTimeInterval: 0.5)
            return "late"
        }
        Issue.record("blocked operation unexpectedly completed")
    } catch {
        #expect(requiresProviderReset(error))
        #expect(Date().timeIntervalSince(started) < 0.3)
    }
}

@Test
func onlyReadOnlyProviderToolsUseTheInnerRuntimeBound() {
    for tool in [
        "list_apps",
        "get_app_state",
        "list_app_windows",
        "resolve_target",
    ] {
        #expect(providerReadOnlyToolNeedsRuntimeBound(tool))
    }
    for tool in ["click", "scroll", "type", "keypress", "set_value", "gui"] {
        #expect(!providerReadOnlyToolNeedsRuntimeBound(tool))
    }
}
