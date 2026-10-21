import AppKit
import Foundation
import Testing
@testable import ComputerUseProviderKit

@Test @MainActor
func applicationDiscoveryReadsOnMainWithoutRedispatch() {
    var calls = 0
    let apps = AppDiscovery.runningApps {
        #expect(Thread.isMainThread)
        calls += 1
        return []
    }
    #expect(calls == 1)
    #expect(apps.isEmpty)
}

@Test
func applicationDiscoveryMovesEachWorkerReadToMain() async {
    let results = await Task.detached {
        #expect(!Thread.isMainThread)
        var reads = 0
        let read: () -> [NSRunningApplication] = {
            #expect(Thread.isMainThread)
            reads += 1
            return []
        }
        let first = AppDiscovery.runningApps(readWorkspace: read)
        let second = AppDiscovery.runningApps(readWorkspace: read)
        return (first.count, second.count, reads)
    }.value
    #expect(results.0 == 0)
    #expect(results.1 == 0)
    #expect(results.2 == 2)
}
