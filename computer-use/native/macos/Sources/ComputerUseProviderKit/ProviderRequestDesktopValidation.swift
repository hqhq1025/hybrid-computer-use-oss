import Foundation

extension ProviderRequestValidator {
    static func validateDesktop(
        _ payload: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            payload,
            path: path,
            allowed: [
                "protocol_version", "actions", "settle_ms",
                "frame_id",
            ]
        )
        guard let actions = payload["actions"] as? [Any], !actions.isEmpty else {
            throw invalid("\(path).actions must be a non-empty array")
        }
        if let settle = integer(payload["settle_ms"]), settle < 0 {
            throw invalid("\(path).settle_ms must be non-negative")
        }
        for (index, rawAction) in actions.enumerated() {
            try validateDesktopAction(
                try object(rawAction, path: "\(path).actions[\(index)]"),
                path: "\(path).actions[\(index)]"
            )
        }
    }

    private static func validateDesktopAction(
        _ action: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            action,
            path: path,
            allowed: [
                "type", "x", "y", "button", "text", "keys",
                "path", "scroll_x", "scroll_y", "region",
            ]
        )
        let type = try requiredString(action["type"], path: "\(path).type")
        let present = Set(action.keys).subtracting(["type"])
        func requireCoordinates() throws {
            guard number(action["x"]) != nil, number(action["y"]) != nil else {
                throw invalid("\(path).\(type) requires numeric x and y")
            }
        }
        switch type {
        case "zoom":
            guard present == ["region"],
                  let region = action["region"] as? [Any], region.count == 4,
                  region.allSatisfy({ number($0) != nil })
            else {
                throw invalid("\(path).zoom requires region with four numbers only")
            }
        case "screenshot", "wait":
            guard present.isEmpty else {
                throw invalid("\(path).\(type) does not accept action fields")
            }
        case "click":
            try requireCoordinates()
            let allowed = Set(["x", "y", "button", "keys"])
            guard present.isSubset(of: allowed) else {
                throw invalid("\(path).click contains fields for another action")
            }
            if let button = action["button"] as? String,
               !["left", "right", "wheel", "back", "forward"].contains(button)
            {
                throw invalid("\(path).click button \(button) is unsupported")
            }
            try validateKeys(action["keys"], path: "\(path).keys", optional: true)
        case "double_click", "move":
            try requireCoordinates()
            let allowed = Set(["x", "y", "keys"])
            guard present.isSubset(of: allowed) else {
                throw invalid("\(path).\(type) contains fields for another action")
            }
            try validateKeys(action["keys"], path: "\(path).keys", optional: true)
        case "drag":
            guard let points = action["path"] as? [Any], points.count >= 2 else {
                throw invalid("\(path).drag requires at least two path points")
            }
            guard present.isSubset(of: ["path", "keys"]) else {
                throw invalid("\(path).drag contains fields for another action")
            }
            for (index, point) in points.enumerated() {
                let value = try object(point, path: "\(path).path[\(index)]")
                try rejectUnknown(
                    value,
                    path: "\(path).path[\(index)]",
                    allowed: ["x", "y"]
                )
                guard number(value["x"]) != nil, number(value["y"]) != nil else {
                    throw invalid("\(path).path[\(index)] requires numeric x and y")
                }
            }
            try validateKeys(action["keys"], path: "\(path).keys", optional: true)
        case "scroll":
            try requireCoordinates()
            guard present.isSubset(of: ["x", "y", "scroll_x", "scroll_y", "keys"]) else {
                throw invalid("\(path).scroll contains fields for another action")
            }
            let x = number(action["scroll_x"]) ?? 0
            let y = number(action["scroll_y"]) ?? 0
            guard x != 0 || y != 0 else {
                throw invalid("\(path).scroll requires a non-zero delta")
            }
            try validateKeys(action["keys"], path: "\(path).keys", optional: true)
        case "type":
            guard let text = action["text"] as? String, !text.isEmpty,
                  present == ["text"] else {
                throw invalid("\(path).type requires text only")
            }
        case "keypress":
            guard present == ["keys"] else {
                throw invalid("\(path).keypress requires keys only")
            }
            try validateKeys(action["keys"], path: "\(path).keys", optional: false)
        default:
            throw invalid("unsupported GUI action type \(type)")
        }
    }

    static func validateWindowList(
        _ payload: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            payload,
            path: path,
            allowed: [
                "protocol_version", "app", "app_identity", "window_handle",
            ]
        )
        try requireNonblankString(payload["app"], path: "\(path).app")
        try validateAppIdentity(payload["app_identity"], path: "\(path).app_identity")
    }

    static func validateWindowAction(
        _ payload: [String: Any],
        path: String
    ) throws {
        try rejectUnknown(
            payload,
            path: path,
            allowed: [
                "protocol_version", "action", "app", "app_identity",
                "window_handle", "target", "observation",
            ]
        )
        let action = try requiredString(payload["action"], path: "\(path).action")
        guard ["activate", "close"].contains(action) else {
            throw invalid("unsupported provider window action \(action)")
        }
        try requireNonblankString(payload["app"], path: "\(path).app")
        try validateAppIdentity(payload["app_identity"], path: "\(path).app_identity")
        guard payload["target"] != nil else {
            throw invalid("\(path).target is required")
        }
        try validateTarget(payload["target"], path: "\(path).target")
        try validateObservation(payload["observation"], path: "\(path).observation")
    }
}
