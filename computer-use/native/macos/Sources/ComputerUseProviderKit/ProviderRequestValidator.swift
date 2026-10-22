import Foundation

public enum ProviderRequestValidator {
    public static let operations = [
        "observe_apps",
        "observe",
        "resolve_and_act",
        "ordered_input",
        "desktop_execute_batch",
        "window_list",
        "window_action",
    ]

    private static let payloadByOperation = [
        "observe_apps": "observe_apps",
        "observe": "observe",
        "resolve_and_act": "action",
        "ordered_input": "ordered_input",
        "desktop_execute_batch": "desktop",
        "window_list": "window_list",
        "window_action": "window_action",
    ]

    private static let intents = [
        "resolve_target",
        "click",
        "scroll",
        "type",
        "keypress",
        "set_value",
        "select",
        "increment",
        "decrement",
    ]

    public static func validate(_ request: [String: Any]) throws {
        try rejectUnknown(
            request,
            path: "request",
            allowed: Set([
                "protocol",
                "schema_version",
                "request_id",
                "operation",
                "observe_apps",
                "observe",
                "action",
                "ordered_input",
                "desktop",
                "window_list",
                "window_action",
            ])
        )
        guard string(request["protocol"]) == "computer-use-provider" else {
            throw invalid("request.protocol must equal computer-use-provider")
        }
        guard integer(request["schema_version"]) == 2 else {
            throw invalid("request.schema_version must equal 2")
        }
        try requireNonblankString(request["request_id"], path: "request.request_id")
        let operation = try requiredString(request["operation"], path: "request.operation")
        guard let payloadName = payloadByOperation[operation] else {
            throw invalid("unsupported provider operation \(operation)")
        }
        let active = payloadByOperation.values.filter { request[$0] != nil }
        guard active.count == 1, active.first == payloadName else {
            throw invalid("provider request must contain exactly its operation payload")
        }
        let payload = try object(request[payloadName], path: "request.\(payloadName)")
        guard integer(payload["protocol_version"]) == 2 else {
            throw invalid("request.\(payloadName).protocol_version must equal 2")
        }

        switch payloadName {
        case "observe_apps":
            try rejectUnknown(payload, path: "request.observe_apps", allowed: ["protocol_version"])
        case "observe":
            try validateObserve(payload, path: "request.observe")
        case "action":
            try validateAction(payload, path: "request.action")
        case "ordered_input":
            try validateOrderedInput(
                payload,
                path: "request.ordered_input"
            )
        case "desktop":
            try validateDesktop(payload, path: "request.desktop")
        case "window_list":
            try validateWindowList(payload, path: "request.window_list")
        case "window_action":
            try validateWindowAction(payload, path: "request.window_action")
        default:
            throw invalid("unsupported provider payload \(payloadName)")
        }
    }

    private static func validateOrderedInput(
        _ payload: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            payload,
            path: path,
            allowed: [
                "protocol_version", "contract_version", "app",
                "app_identity", "window_handle", "expected_window", "steps",
                "final_observation", "diagnostics",
            ]
        )
        guard integer(payload["contract_version"]) == 1 else {
            throw invalid("\(path).contract_version must equal 1")
        }
        try requireNonblankString(payload["app"], path: "\(path).app")
        let identity = try object(
            payload["app_identity"],
            path: "\(path).app_identity"
        )
        try validateAppIdentity(identity, path: "\(path).app_identity")
        let windowHandle = try requiredString(
            payload["window_handle"],
            path: "\(path).window_handle"
        )
        try validateExpectedWindow(
            payload["expected_window"],
            windowHandle: windowHandle,
            path: "\(path).expected_window"
        )
        let expectedWindow = try object(
            payload["expected_window"],
            path: "\(path).expected_window"
        )
        guard string(identity["providerBusName"]) ==
                string(expectedWindow["providerBusName"]),
              string(identity["processGeneration"]) ==
                string(expectedWindow["processGeneration"])
        else {
            throw invalid(
                "\(path).app_identity must match expected_window ownership"
            )
        }
        guard let steps = payload["steps"] as? [Any],
              !steps.isEmpty, steps.count <= 64 else {
            throw invalid("\(path).steps must contain between 1 and 64 actions")
        }
        var priorIndex: Int?
        for (position, rawStep) in steps.enumerated() {
            let step = try object(
                rawStep,
                path: "\(path).steps[\(position)]"
            )
            try rejectUnknown(
                step,
                path: "\(path).steps[\(position)]",
                allowed: ["action_index", "key"]
            )
            guard let actionIndex = integer(step["action_index"]),
                  actionIndex >= 0 else {
                throw invalid(
                    "\(path).steps[\(position)].action_index must be non-negative"
                )
            }
            if let priorIndex, actionIndex != priorIndex + 1 {
                throw invalid(
                    "\(path).steps action_index values must be contiguous"
                )
            }
            priorIndex = actionIndex
            let key = try requiredString(
                step["key"],
                path: "\(path).steps[\(position)].key"
            )
            guard !key.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw invalid(
                    "\(path).steps[\(position)].key must be a non-empty string"
                )
            }
            _ = try KeyPressParser.parse(key)
        }
        let observation = try object(
            payload["final_observation"],
            path: "\(path).final_observation"
        )
        try validateObservation(
            observation,
            path: "\(path).final_observation"
        )
        guard observation["capture_snapshot"] as? Bool == true else {
            throw invalid(
                "\(path).final_observation.capture_snapshot must equal true"
            )
        }
        try validateDiagnostics(
            payload["diagnostics"],
            path: "\(path).diagnostics"
        )
    }

    private static func validateObserve(
        _ payload: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            payload,
            path: path,
            allowed: [
                "protocol_version", "app", "app_identity",
                "window_handle", "observation",
            ]
        )
        try requireNonblankString(payload["app"], path: "\(path).app")
        try validateAppIdentity(payload["app_identity"], path: "\(path).app_identity")
        try validateObservation(payload["observation"], path: "\(path).observation")
    }

    private static func validateAction(
        _ payload: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            payload,
            path: path,
            allowed: [
                "protocol_version", "intent", "app", "app_identity",
                "window_handle", "expected_window", "element", "target", "input",
                "window_bounds", "observation", "diagnostics",
            ]
        )
        let intent = try requiredString(payload["intent"], path: "\(path).intent")
        guard intents.contains(intent) else {
            throw invalid("unsupported provider accessibility intent \(intent)")
        }
        try requireNonblankString(payload["app"], path: "\(path).app")
        try validateAppIdentity(payload["app_identity"], path: "\(path).app_identity")
        try validateExpectedWindow(
            payload["expected_window"],
            windowHandle: payload["window_handle"],
            path: "\(path).expected_window"
        )
        try validateElement(payload["element"], path: "\(path).element")
        try validateTarget(payload["target"], path: "\(path).target")
        try rejectUnknownIfPresent(
            payload["window_bounds"],
            path: "\(path).window_bounds",
            allowed: ["x", "y", "width", "height"]
        )
        try validateObservation(payload["observation"], path: "\(path).observation")
        try validateDiagnostics(payload["diagnostics"], path: "\(path).diagnostics")

        let hasTarget = payload["target"] != nil || payload["element"] != nil
        let input = try optionalObject(payload["input"], path: "\(path).input")
        switch intent {
        case "resolve_target":
            guard hasTarget else {
                throw invalid("\(path).resolve_target requires an exact target")
            }
            guard input == nil else {
                throw invalid("\(path).resolve_target does not accept input")
            }
        case "click":
            guard hasTarget else {
                throw invalid("\(path).click requires an exact target")
            }
            try validateInput(
                input,
                path: "\(path).input",
                allowed: ["x", "y", "click_count", "mouse_button", "click_method"]
            )
        case "scroll":
            try validateInput(
                input,
                path: "\(path).input",
                allowed: ["direction", "pages", "window_centre"]
            )
            try requireNonblankString(input?["direction"], path: "\(path).input.direction")
            guard number(input?["pages"]) ?? 0 > 0 else {
                throw invalid("\(path).input.pages must be greater than zero")
            }
            try requireTargetOrWindow(payload, hasTarget: hasTarget, path: path)
        case "type":
            try validateInput(input, path: "\(path).input", allowed: ["text"])
            guard let text = input?["text"] as? String, !text.isEmpty else {
                throw invalid("\(path).input.text is required")
            }
            try requireTargetOrWindow(payload, hasTarget: hasTarget, path: path)
        case "keypress":
            try validateInput(input, path: "\(path).input", allowed: ["key"])
            let key = try requiredString(
                input?["key"],
                path: "\(path).input.key"
            )
            guard !key.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw invalid("\(path).input.key must be a non-empty string")
            }
            _ = try KeyPressParser.parse(key)
            try requireTargetOrWindow(payload, hasTarget: hasTarget, path: path)
        case "set_value":
            guard hasTarget else {
                throw invalid("\(path).set_value requires an exact target")
            }
            try validateInput(input, path: "\(path).input", allowed: ["value"])
            guard input?["value"] is String else {
                throw invalid("\(path).input.value is required")
            }
        case "select":
            guard hasTarget else {
                throw invalid("\(path).select requires an exact target")
            }
            try validateInput(input, path: "\(path).input", allowed: ["text", "to_target"])
            if let text = input?["text"], !(text is String) {
                throw invalid("\(path).input.text must be a string")
            }
            if input?["to_target"] != nil {
                try validateTarget(input?["to_target"], path: "\(path).input.to_target")
                if let text = input?["text"] as? String, !text.isEmpty {
                    throw invalid("\(path).select cannot combine text and to_target")
                }
            }
        case "increment", "decrement":
            guard hasTarget else {
                throw invalid("\(path).\(intent) requires an exact target")
            }
            guard input == nil else {
                throw invalid("\(path).\(intent) does not accept input")
            }
        default:
            break
        }
    }

    private static func validateDiagnostics(
        _ raw: Any?,
        path: String
    ) throws {
        guard let diagnostics = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(
            diagnostics,
            path: path,
            allowed: ["phase_timing", "relative_readback"]
        )
        for key in ["phase_timing", "relative_readback"] {
            if diagnostics[key] != nil, boolean(diagnostics[key]) == nil {
                throw invalid("\(path).\(key) must be a boolean")
            }
        }
    }

    private static func requireTargetOrWindow(
        _ payload: [String: Any],
        hasTarget: Bool,
        path: String
    ) throws {
        if !hasTarget {
            try requireNonblankString(
                payload["window_handle"],
                path: "\(path).window_handle"
            )
        }
    }

    private static func validateInput(
        _ input: [String: Any]?,
        path: String,
        allowed: Set<String>
    ) throws {
        guard let input else {
            throw invalid("\(path) is required")
        }
        try rejectUnknown(input, path: path, allowed: allowed)
    }

}
