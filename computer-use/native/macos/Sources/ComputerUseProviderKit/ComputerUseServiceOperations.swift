import ApplicationServices
import Foundation
import ImageIO

extension ComputerUseService {
    public func scroll(app query: String, direction: String, elementIndex: String, pages: Double) throws -> ToolCallResult {
        let normalized = direction.lowercased()
        guard ["up", "down", "left", "right"].contains(normalized) else {
            throw ComputerUseError.message("Invalid scroll direction: \(direction)")
        }
        guard pages.isFinite, pages > 0 else {
            throw ComputerUseError.message("pages must be > 0")
        }

        let snapshot = try currentSnapshot(for: query)
        let record = try lookupElement(snapshot: snapshot, index: elementIndex)

        let route: String
        if let repeatCount = integralScrollPageCount(pages),
           let rawAction = record.rawActions.first(where: { $0.caseInsensitiveCompare("AXScroll\(normalized.capitalized)ByPage") == .orderedSame }),
           let element = record.element {
            route = "ax.action.scroll\(normalized)bypage"
            for index in 0..<repeatCount {
                let result = AXUIElementPerformAction(element, rawAction as CFString)
                guard result == .success else {
                    throw nativeAXActionAttemptError(
                        action: rawAction, result: result,
                        earlierInputEmitted: index > 0
                    )
                }
                Thread.sleep(forTimeInterval: 0.05)
            }
        } else if let point = try globalPoint(for: record, snapshot: snapshot) {
            route = "input.cg_event.scroll"
            try performScrollEvent(
                at: point,
                direction: normalized,
                pages: pages,
                targetDescription: "element_index=\(elementIndex)",
                snapshot: snapshot
            )
        } else {
            throw ComputerUseError.stateUnavailable("element \(elementIndex) has no scrollable frame")
        }

        return try observeAfterConfirmedInput(route: route) {
            snapshotResult(for: try refreshSnapshot(for: query), style: .actionResult)
        }
    }

    public func drag(app query: String, fromX: Double, fromY: Double, toX: Double, toY: Double) throws -> ToolCallResult {
        let snapshot = try currentSnapshot(for: query)
        let start = try windowPointToGlobalPoint(snapshot: snapshot, point: CGPoint(x: fromX, y: fromY))
        let end = try windowPointToGlobalPoint(snapshot: snapshot, point: CGPoint(x: toX, y: toY))
        try performDragEvent(
            from: start,
            to: end,
            targetDescription: "from=(\(Int(fromX)), \(Int(fromY))) to=(\(Int(toX)), \(Int(toY)))",
            snapshot: snapshot
        )
        return snapshotResult(for: try refreshSnapshot(for: query), style: .actionResult)
    }

    public struct TextInputDispatchResult: @unchecked Sendable {
        public let result: ToolCallResult
        public let route: String
        public let fallbackUsed: Bool
    }

    public struct SetValueDispatchResult: @unchecked Sendable {
        public let result: ToolCallResult
        public let route: String
    }

    public struct RelativeAdjustmentDispatchResult: Sendable {
        public let readbackSampled: Bool
        public let readbackVerified: Bool?
    }

    static func textInputRoute() -> (route: String, fallbackUsed: Bool) {
        ("input.pasteboard.global_command_v", false)
    }

    public func typeTextWithRoute(
        app query: String,
        text: String
    ) throws -> TextInputDispatchResult {
        let snapshot = try currentSnapshot(for: query)
        guard let focusedElement = try focusedEditableTextTarget(in: snapshot) else {
            throw ComputerUseError.stateUnavailable("type_text requires a focused editable text element. Click a text entry area first, or use set_value on a settable text element.")
        }
        let retentionBarrier = AXPasteboardRetentionBarrier(
            pid: snapshot.app.pid,
            target: focusedElement
        )

        try InputSimulation.typeTextGlobally(text) {
            retentionBarrier.waitForSafeRestore()
        }
        let selected = Self.textInputRoute()
        return try observeAfterConfirmedInput(route: selected.route) {
            TextInputDispatchResult(
                result: snapshotResult(
                    for: try refreshSnapshot(for: query),
                    style: .actionResult
                ),
                route: selected.route,
                fallbackUsed: selected.fallbackUsed
            )
        }
    }

    public func typeText(app query: String, text: String) throws -> ToolCallResult {
        try typeTextWithRoute(app: query, text: text).result
    }

    public func pressKey(app query: String, key: String) throws -> ToolCallResult {
        let snapshot = try currentSnapshot(for: query)
        try InputSimulation.pressKey(key, pid: snapshot.app.pid)
        return try observeAfterConfirmedInput(route: "input.cg_event.keyboard") {
            snapshotResult(for: try refreshSnapshot(for: query), style: .actionResult)
        }
    }

    public func setValueWithRoute(
        app query: String,
        elementIndex: String,
        value: String
    ) throws -> SetValueDispatchResult {
        let snapshot = try currentSnapshot(for: query)
        let record = try lookupElement(snapshot: snapshot, index: elementIndex)

        guard let element = record.element else {
            throw ComputerUseError.stateUnavailable("element \(elementIndex) has no backing accessibility object")
        }

        guard try isSettableForSetValue(element: element, attribute: kAXValueAttribute) else {
            throw ComputerUseError.message(nonSettableSetValueErrorMessage)
        }

        try performNativeAXMutation(
            operation: "AXUIElementSetAttributeValue(AXValue)",
            route: "ax.attribute.value.set"
        ) {
            AXUIElementSetAttributeValue(
                element,
                kAXValueAttribute as CFString,
                value as CFString
            )
        }

        Thread.sleep(forTimeInterval: 0.1)
        let observedValue = attributeValue(
            of: element,
            attribute: kAXValueAttribute
        )
        guard exactStringReadbackMatches(
            requested: value,
            observed: observedValue
        ) else {
            let observedDescription = (observedValue as? String)
                ?? "<missing or non-string>"
            throw InputDeliveryError(
                message: "AX value write returned success, but exact readback did not match: requested \(String(reflecting: value)), observed \(String(reflecting: observedDescription))",
                inputEventsEmitted: true,
                failureCode: "readback_mismatch",
                partialInputPossible: false,
                readbackSampled: true,
                readbackVerified: false,
                actionRoute: "ax.attribute.value.set"
            )
        }
        return try observeAfterConfirmedInput(
            route: "ax.attribute.value.set",
            readbackSampled: true,
            readbackVerified: true
        ) {
            SetValueDispatchResult(
                result: snapshotResult(
                    for: try refreshSnapshot(for: query),
                    style: .actionResult
                ),
                route: "ax.attribute.value.set"
            )
        }
    }

    public func setValue(
        app query: String,
        elementIndex: String,
        value: String
    ) throws -> ToolCallResult {
        try setValueWithRoute(
            app: query,
            elementIndex: elementIndex,
            value: value
        ).result
    }

    public func performRelativeAdjustment(
        app query: String,
        elementIndex: String,
        direction: String
    ) throws -> RelativeAdjustmentDispatchResult {
        try performRelativeAdjustment(
            app: query, elementIndex: elementIndex, direction: direction, diagnostic: nil
        )
    }

    func performRelativeAdjustment(
        app query: String,
        elementIndex: String,
        direction: String,
        diagnostic: RelativeReadbackDiagnostic?
    ) throws -> RelativeAdjustmentDispatchResult {
        guard let action = Self.nativeRelativeAdjustmentAction(
            for: direction
        ) else {
            throw ComputerUseError.invalidArguments(
                "unsupported relative adjustment \(direction)"
            )
        }

        let snapshot = try currentSnapshot(for: query)
        let record = try lookupElement(
            snapshot: snapshot,
            index: elementIndex
        )
        guard let element = record.element else {
            throw ComputerUseError.stateUnavailable(
                "element \(elementIndex) has no backing accessibility object"
            )
        }
        guard record.rawActions.contains(where: {
            $0.caseInsensitiveCompare(action) == .orderedSame
        }) else {
            throw ComputerUseError.stateUnavailable(
                "element \(elementIndex) does not expose \(direction); no input was emitted"
            )
        }
        diagnostic?.bind(snapshot: snapshot, target: record)
        return try dispatchRelativeAdjustment(
            action: action, direction: direction, diagnostic: diagnostic,
            readValue: { attributeValue(of: element, attribute: kAXValueAttribute) },
            performAction: { AXUIElementPerformAction(element, action as CFString) }
        )
    }

    static func nativeRelativeAdjustmentAction(
        for direction: String
    ) -> String? {
        switch direction {
        case "increment":
            return kAXIncrementAction as String
        case "decrement":
            return kAXDecrementAction as String
        default:
            return nil
        }
    }

    func currentSnapshot(for query: String) throws -> AppSnapshot {
        if let snapshot = snapshotsByApp[query.lowercased()] {
            return snapshot
        }

        return try refreshSnapshot(for: query)
    }

    func cachedSnapshot(for query: String) -> AppSnapshot? {
        if let snapshot = snapshotsByApp[query.lowercased()] {
            return snapshot
        }
        guard let app = try? resolveRuntimeApplication(query: query, identity: activeAppIdentity) else {
            return nil
        }
        return snapshotsByApp[app.name.lowercased()]
            ?? app.bundleIdentifier.flatMap {
                snapshotsByApp[$0.lowercased()]
            }
    }

    @discardableResult
    func refreshSnapshot(
        for query: String,
        textLimit: SnapshotTextLimit = .defaults,
        treeLimits: AccessibilityTreeLimits = .defaults,
        recoveryPolicy: SnapshotRecoveryPolicy = .allowActivation,
        fallbackWindow: AXUIElement? = nil,
        expectedWindowHandle: String? = nil
    ) throws -> AppSnapshot {
        let windowHandle = try snapshotWindowHandle(requested: expectedWindowHandle)
        let app = try resolveRuntimeApplication(query: query, identity: activeAppIdentity)
        let snapshot = try SnapshotBuilder.build(
            for: app,
            textLimit: textLimit,
            treeLimits: treeLimits,
            recoveryPolicy: recoveryPolicy,
            fallbackWindow: fallbackWindow,
            expectedWindowHandle: windowHandle
        )
        if let identity = activeAppIdentity {
            _ = try resolveRuntimeApplication(query: query, identity: identity)
            try validateRequestedIdentity(["appIdentity": identity], app: snapshot.app)
        }

        let keys = Set([
            query.lowercased(),
            app.name.lowercased(),
            (app.bundleIdentifier ?? "").lowercased(),
        ].filter { !$0.isEmpty })

        for key in keys {
            snapshotsByApp[key] = snapshot
        }
        rememberNativeWindowIdentity(snapshot)

        return snapshot
    }


    func snapshotResult(for snapshot: AppSnapshot, style: SnapshotTextStyle) -> ToolCallResult {
        ToolCallResult(content: [.text(snapshot.renderedText(style: style))])
    }
}

func dispatchRelativeAdjustment(
    action: String,
    direction: String,
    diagnostic: RelativeReadbackDiagnostic?,
    readValue: () -> CFTypeRef?,
    performAction: () -> AXError,
    now: () -> TimeInterval = { ProcessInfo.processInfo.systemUptime },
    sleep: (TimeInterval) -> Void = { Thread.sleep(forTimeInterval: $0) }
) throws -> ComputerUseService.RelativeAdjustmentDispatchResult {
    diagnostic?.willReadBefore()
    let before = relativeAdjustmentScalarReadback(readValue())
    diagnostic?.didReadBefore(before)
    diagnostic?.willPerformAction()
    let result = performAction()
    diagnostic?.didPerformAction(result)
    guard result == .success else {
        throw nativeAXActionAttemptError(action: action, result: result)
    }

    guard let before else {
        diagnostic?.finish("before_unavailable", sampled: false, verified: nil)
        return ComputerUseService.RelativeAdjustmentDispatchResult(
            readbackSampled: false, readbackVerified: nil
        )
    }

    let deadline = now() + 0.12
    var after: RelativeAdjustmentScalarReadback?
    repeat {
        diagnostic?.willReadAfter()
        after = relativeAdjustmentScalarReadback(readValue())
        diagnostic?.didReadAfter(after)
        if relativeAdjustmentReadbackOutcome(before: before, after: after) == .changed {
            diagnostic?.finish("changed", sampled: true, verified: true)
            return ComputerUseService.RelativeAdjustmentDispatchResult(
                readbackSampled: true, readbackVerified: true
            )
        }
        if now() < deadline {
            sleep(0.02)
        }
    } while now() < deadline

    guard after != nil else {
        diagnostic?.finish("after_unavailable", sampled: false, verified: nil)
        return ComputerUseService.RelativeAdjustmentDispatchResult(
            readbackSampled: false, readbackVerified: nil
        )
    }

    diagnostic?.finish("unchanged", sampled: true, verified: false)
    throw InputDeliveryError(
        message: "AX \(direction) returned success, but scalar AX value readback remained unchanged at \(before.description)",
        inputEventsEmitted: true,
        failureCode: "readback_unchanged",
        partialInputPossible: false,
        readbackSampled: true,
        readbackVerified: false,
        actionRoute: "ax.action.\(direction)"
    )
}

func nativeAXActionAttemptError(
    action: String,
    result: AXError,
    earlierInputEmitted: Bool = false
) -> InputDeliveryError {
    let routeName = action
        .replacingOccurrences(of: "AX", with: "")
        .lowercased()
    return nativeAXMutationAttemptError(
        operation: "AXUIElementPerformAction(\(action))",
        route: "ax.action.\(routeName)",
        result: result,
        earlierInputEmitted: earlierInputEmitted
    )
}

func performNativeAXMutation(
    operation: String,
    route: String,
    action: () -> AXError
) throws {
    let result = action()
    guard result == .success else {
        throw nativeAXMutationAttemptError(
            operation: operation, route: route, result: result
        )
    }
}

func nativeAXMutationAttemptError(
    operation: String,
    route: String,
    result: AXError,
    earlierInputEmitted: Bool = false
) -> InputDeliveryError {
    let rejected: Bool
    switch result {
    case .illegalArgument, .invalidUIElement, .attributeUnsupported,
         .actionUnsupported, .notImplemented, .apiDisabled:
        rejected = true
    default:
        // AX cannotComplete includes callbacks that acted but did not reply.
        // Generic or unknown native errors do not prove rejection either.
        rejected = false
    }
    let possibleInput = earlierInputEmitted || !rejected
    let explanation = possibleInput
        ? "The request may have taken effect or earlier input was emitted. Observe the current state before retrying."
        : "The native request was rejected; no input was emitted."
    return InputDeliveryError(
        message: "\(operation) returned \(result.rawValue). \(explanation)",
        inputEventsEmitted: possibleInput,
        failureCode: possibleInput ? "effect_unknown" : nil,
        partialInputPossible: possibleInput,
        actionRoute: route
    )
}
