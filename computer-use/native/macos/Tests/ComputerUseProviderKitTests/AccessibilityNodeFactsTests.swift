@testable import ComputerUseProviderKit
import ApplicationServices
import Foundation
import Testing

@Test
func snapshotNodeReadsShareOneBatchedRequestPerElement() {
    var batches = 0
    var singles = 0
    var settableReads = 0
    let cache = AXNodeFactsCache(
        attributes: ["AXRole", "AXValue"],
        multiRead: { _, _ in
            batches += 1
            return [
                AXRead(status: .success, value: "AXButton" as CFString),
                AXRead(status: .attributeUnsupported, value: nil),
            ]
        },
        singleRead: { _, _ in
            singles += 1
            return AXRead(status: .noValue, value: nil)
        },
        settableRead: { _, _ in
            settableReads += 1
            return true
        }
    )
    // Two references to one element share the cached sample.
    let element = AXUIElementCreateApplication(4242)
    let sameElement = AXUIElementCreateApplication(4242)
    #expect(cache.attribute(element, "AXRole").value as? String == "AXButton")
    #expect(cache.attribute(sameElement, "AXValue").status == .attributeUnsupported)
    #expect(cache.isSettable(element, "AXValue") == false)
    #expect(cache.attribute(element, "AXTitle").status == .noValue)
    #expect(cache.attribute(element, "AXTitle").status == .noValue)
    #expect(cache.isSettable(element, "AXTitle"))
    #expect(cache.isSettable(element, "AXTitle"))
    #expect(batches == 1)
    #expect(singles == 1)
    #expect(settableReads == 1)
}

@Test
func failedBatchFallsBackToSingleReads() {
    var singles = 0
    let cache = AXNodeFactsCache(
        attributes: ["AXRole"],
        multiRead: { _, _ in nil },
        singleRead: { _, _ in
            singles += 1
            return AXRead(status: .success, value: "AXGroup" as CFString)
        }
    )
    let element = AXUIElementCreateApplication(4243)
    #expect(cache.attribute(element, "AXRole").value as? String == "AXGroup")
    #expect(cache.attribute(element, "AXRole").value as? String == "AXGroup")
    #expect(singles == 1)
}

@Test
func snapshotScopeInstallsOneCacheAndRemovesIt() {
    #expect(AXNodeFactsCache.current == nil)
    AXNodeFactsCache.withSnapshotScope {
        let outer = AXNodeFactsCache.current
        #expect(outer != nil)
        AXNodeFactsCache.withSnapshotScope {
            #expect(AXNodeFactsCache.current === outer)
        }
    }
    #expect(AXNodeFactsCache.current == nil)
}
