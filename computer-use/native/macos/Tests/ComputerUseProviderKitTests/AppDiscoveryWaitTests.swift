@testable import ComputerUseProviderKit
import Foundation
import Testing

@Test
func appLaunchWaitReturnsImmediatelyForCompletedLaunch() {
    let semaphore = DispatchSemaphore(value: 0)
    semaphore.signal()

    #expect(
        AppDiscovery.waitForSignal(
            semaphore,
            timeout: 1,
            isMainThread: false
        )
    )
}

@Test
func appLaunchWaitFailsClosedWhenCompletionDoesNotArrive() {
    let semaphore = DispatchSemaphore(value: 0)

    #expect(
        !AppDiscovery.waitForSignal(
            semaphore,
            timeout: 0,
            isMainThread: false
        )
    )
}

@Test
func appLaunchWaitPumpsMainRunLoopUntilCompletion() {
    let semaphore = DispatchSemaphore(value: 0)
    var pumpCount = 0

    let completed = AppDiscovery.waitForSignal(
        semaphore,
        timeout: 1,
        isMainThread: true,
        pumpRunLoop: {
            pumpCount += 1
            semaphore.signal()
        }
    )

    #expect(completed)
    #expect(pumpCount == 1)
}

@Test
func appCatalogHidesComputerUseProvider() {
    #expect(
        !AppDiscovery.isDiscoverableBundleIdentifier(
            "com.microsoft.computer-use.provider.v2"
        )
    )
    #expect(AppDiscovery.isDiscoverableBundleIdentifier("com.apple.Notes"))
}

@Test
func runningAppEnumerationHidesComputerUseProvider() {
    #expect(
        !AppDiscovery.isDiscoverableRunningApplication(
            isTerminated: false,
            bundleIdentifier: "com.microsoft.computer-use.provider.v2"
        )
    )
    #expect(
        AppDiscovery.isDiscoverableRunningApplication(
            isTerminated: false,
            bundleIdentifier: "com.apple.Notes"
        )
    )
    #expect(
        !AppDiscovery.isDiscoverableRunningApplication(
            isTerminated: true,
            bundleIdentifier: "com.apple.Notes"
        )
    )
}
