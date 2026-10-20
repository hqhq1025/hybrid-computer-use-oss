@testable import ComputerUseProviderKit
import AppKit
import ApplicationServices
import CoreGraphics
import Testing

@Test func unnumberedWindowsKeepTitlesWithoutInventingHandles() throws {
    let windowIDs: [CGWindowID?] = [nil, 0]
    for windowID in windowIDs {
        let record = macWindowInventoryRecord(pid: getpid(), generation: "test", windowID: windowID, title: "Unsaved", focusedHandle: nil, modal: false)
        let window = try #require(appListingWindows([record]).first)
        #expect(window["title"] as? String == "Unsaved")
        #expect(window["handle"] as? String == "")
        #expect(window["providerObjectPath"] as? String == "")
        #expect(window["inputTarget"] as? Bool == true)
    }
}

@Test func appWindowIdentityMatchesObservationAndFocusIsNotArrayOrder() {
    let handle = macWindowHandle(pid: getpid(), windowID: 77)
    let unfocused = macWindowInventoryRecord(pid: getpid(), generation: "test", windowID: 66, title: "First", focusedHandle: handle, modal: false)
    let focused = macWindowInventoryRecord(pid: getpid(), generation: "test", windowID: 77, title: "Second", focusedHandle: handle, modal: false)
    let windows = appListingWindows([unfocused, focused])
    #expect(windows[0]["active"] as? Bool == false)
    #expect(windows[1]["active"] as? Bool == true)
    #expect(windows[1]["handle"] as? String == handle)
    #expect(windows[1]["providerObjectPath"] as? String == "window:\(handle)")
    #expect(windows[1]["processGeneration"] as? String == "test")
}

@Test func aliasedWindowsDoNotAcquireIndependentExecutableRefs() {
    let record = macWindowInventoryRecord(pid: getpid(), generation: "test", windowID: 77, title: "Shared", focusedHandle: nil, modal: false)
    let windows = appListingWindows([record, record])
    #expect(windows.count == 2)
    #expect(windows.allSatisfy { $0["handle"] as? String == "" })
    #expect(windows.allSatisfy { $0["inputTarget"] as? Bool == true })
}

@Test func incompleteWindowReadFailsInsteadOfProvingAbsence() throws {
    let app = RunningAppDescriptor(name: "Test", bundleIdentifier: "test.inventory", pid: getpid(), runningApplication: NSRunningApplication.current)
    let empty = try macApplicationListing([app], inputCapable: { _ in true }, windows: { _ in [] })
    #expect((empty[0]["windows"] as? [[String: Any]])?.isEmpty == true)
    do {
        _ = try macApplicationListing([app], inputCapable: { _ in true }, windows: { _ in
            throw ComputerUseError.stateUnavailable("inventory incomplete")
        })
        Issue.record("Incomplete enumeration must not become an empty successful listing")
    } catch {
        let result = runtimeFailureResult(error)
        #expect(result["ok"] as? Bool == false)
        #expect(result["input_events_emitted"] as? Bool == false)
    }
}

@Test func failedTitleReadsCannotBecomeSuccessfulEmptyTitles() throws {
    #expect(try completeWindowTitle(status: .success, value: "Document" as CFString) == "Document")
    #expect(try completeWindowTitle(status: .success, value: nil) == "")
    #expect(try completeWindowTitle(status: .attributeUnsupported, value: nil) == "")
    #expect(try completeWindowTitle(status: .noValue, value: nil) == "")
    for status in [AXError.cannotComplete, .apiDisabled, .invalidUIElement] {
        #expect(throws: (any Error).self) { try completeWindowTitle(status: status, value: nil) }
    }
    #expect(throws: (any Error).self) { try completeWindowTitle(status: .success, value: kCFBooleanTrue) }
}
