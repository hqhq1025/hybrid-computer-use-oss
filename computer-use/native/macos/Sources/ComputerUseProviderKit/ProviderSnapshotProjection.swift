import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

extension AppSnapshot {
    func providerDictionary(includeScreenshot: Bool) throws -> [String: Any] {
        let generation = providerProcessGeneration ?? macProcessGeneration(pid: app.pid)
        var result: [String: Any] = [
            "app": [
                "name": app.name,
                "bundleIdentifier": app.bundleIdentifier ?? app.name,
                "pid": Int(app.pid),
                "providerBusName": "mac:\(app.pid)",
                "processGeneration": generation,
                "displayText": app.bundleIdentifier.map {
                    "\(app.name) — \($0)"
                } ?? app.name,
                "inputCapable": true,
            ],
            "sampledAt": ISO8601DateFormatter().string(from: Date()),
            "treeLines": treeLines,
            "elements": try providerElementDictionaries,
        ]
        if let windowTitle {
            result["windowTitle"] = windowTitle
        }
        if let windowBounds {
            result["windowBounds"] = frameDictionary(windowBounds)
        }
        if let handle = targetWindowHandle {
            result["windowHandle"] = handle
            result["windows"] = [[
                "title": windowTitle ?? "",
                "role": "window",
                "handle": handle,
                "selected": true,
                "active": NSRunningApplication(
                    processIdentifier: app.pid
                )?.isActive ?? false,
                "modal": false,
                "showing": true,
            ]]
        }
        if includeScreenshot, let screenshotPNGData {
            result["screenshotPngBase64"] = screenshotPNGData.base64EncodedString()
            result["screenshotMimeType"] = "image/png"
            result["screenshotEncoding"] = "png"
            if let dimensions = imageDimensions(screenshotPNGData) {
                result["width"] = dimensions.width
                result["height"] = dimensions.height
            }
        }
        if let focusedSummary {
            result["focusedSummary"] = focusedSummary
        }
        if let selectedText {
            result["selectedText"] = selectedText
        }
        return result
    }

    var providerElementDictionaries: [[String: Any]] {
        get throws {
        guard elements.values.allSatisfy({
            $0.isSyntheticText || $0.element == nil || $0.boundProviderObjectPath != nil
        }) else {
            throw ComputerUseError.stateUnavailable(
                "Unbound native object identity; snapshot rejected."
            )
        }
        return elements.keys.sorted().map { index in
            let record = elements[index]!
            let processGeneration = providerProcessGeneration ?? macProcessGeneration(pid: app.pid)
            let windowObjectPath = targetWindowHandle.map {
                "window:\($0)"
            } ?? ""
            var item: [String: Any] = [
                "index": index,
                "runtimeId": record.runtimeID,
                "runtimeAliases": [],
                "automationId": record.identifier ?? "",
                "name": record.name ?? "",
                "controlType": record.providerRole ?? "unknown",
                "localizedControlType": record.localizedRole
                    ?? "unknown",
                "actions": record.prettyActions,
                "setValue": record.setValueSupported ?? false,
                "providerBusName": "mac:\(app.pid)",
                "providerObjectPath": macProviderObjectPath(record),
                "processGeneration": processGeneration,
                "providerIdentityScope": record.boundProviderObjectPath == nil
                    ? "process" : "process_instance",
                "providerWindowBusName": "mac:\(app.pid)",
                "providerWindowObjectPath": windowObjectPath,
                "providerWindowProcessGeneration": processGeneration,
                "providerWindowRole": "window",
                "providerWindowName": windowTitle ?? "",
                "showing": true,
                "visible": true,
            ]
            if let value = record.value {
                item["value"] = value
            }
            if let description = record.description {
                item["description"] = description
            }
            if let placeholder = record.placeholder {
                item["placeholder"] = placeholder
            }
            if let states = record.states {
                item["states"] = states
            }
            if let frame = record.localFrame {
                item["frame"] = frameDictionary(frame)
            }
            return item
        }
        }
    }
}

func parsedTreeLinesByIndex(
    _ treeLines: [String]
) -> [Int: ParsedTreeLine] {
    var parsedByIndex: [Int: ParsedTreeLine] = [:]
    for line in treeLines {
        guard let parsed = parseTreeLine(line),
              parsedByIndex[parsed.index] == nil else {
            continue
        }
        parsedByIndex[parsed.index] = parsed
    }
    return parsedByIndex
}

func validateRequestedIdentity(
    _ operation: [String: Any],
    app: RunningAppDescriptor
) throws {
    guard let identity = operation["appIdentity"] as? [String: Any] else {
        return
    }
    let expectedBus = "mac:\(app.pid)"
    let expectedGeneration = macProcessGeneration(pid: app.pid)
    guard string(identity["providerBusName"]) == expectedBus,
          string(identity["processGeneration"]) == expectedGeneration else {
        throw ComputerUseError.staleAppRef(
            "stale_app_ref provider_process_changed."
        )
    }
}

func validateRequestedWindow(
    _ operation: [String: Any],
    snapshot: AppSnapshot
) throws {
    let requested = string(operation["targetWindowHandle"])
    guard !requested.isEmpty else {
        return
    }
    let current = snapshot.targetWindowHandle ?? ""
    guard requested == current else {
        throw ComputerUseError.staleWindowRef(
            "stale_window_ref provider_window_changed."
        )
    }
}

func windowInventorySnapshot(
    app: RunningAppDescriptor,
    windows: [AXUIElement]
) -> [String: Any] {
    let generation = macProcessGeneration(pid: app.pid)
    let active = app.runningApplication.isActive
    let records = windows.enumerated().map { index, window -> [String: Any] in
        let windowID = windowNumber(window) ?? CGWindowID(index + 1)
        return [
            "title": axTitle(window) ?? "",
            "role": "window",
            "handle": macWindowHandle(pid: app.pid, windowID: windowID),
            "providerBusName": "mac:\(app.pid)",
            "providerObjectPath": "window:\(windowID)",
            "providerProcessGeneration": generation,
            "selected": index == 0,
            "active": active && index == 0,
            "modal": axBool(window, attribute: kAXModalAttribute as String),
            "showing": true,
        ]
    }
    return [
        "app": [
            "name": app.name,
            "bundleIdentifier": app.bundleIdentifier ?? app.name,
            "pid": Int(app.pid),
            "providerBusName": "mac:\(app.pid)",
            "processGeneration": generation,
            "inputCapable": true,
        ],
        "windows": records,
        "treeLines": records.map {
            "window \"\(string($0["title"]))\""
        },
        "elements": [],
    ]
}

func windowNumber(_ window: AXUIElement) -> CGWindowID? {
    var identifier: CGWindowID = 0
    let result = AXUIElementGetWindow(window, &identifier)
    return result == .success ? identifier : nil
}

private func axBool(_ element: AXUIElement, attribute: String) -> Bool {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(
        element,
        attribute as CFString,
        &value
    ) == .success else {
        return false
    }
    return (value as? NSNumber)?.boolValue ?? false
}
