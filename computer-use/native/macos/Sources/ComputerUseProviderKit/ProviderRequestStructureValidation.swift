import Foundation

extension ProviderRequestValidator {
    static func validateAppIdentity(_ raw: Any?, path: String) throws {
        guard let identity = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(
            identity,
            path: path,
            allowed: ["providerBusName", "processGeneration"]
        )
        try requireNonblankString(
            identity["providerBusName"],
            path: "\(path).providerBusName"
        )
        try requireNonblankString(
            identity["processGeneration"],
            path: "\(path).processGeneration"
        )
    }

    static func validateObservation(_ raw: Any?, path: String) throws {
        guard let value = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(
            value,
            path: path,
            allowed: [
                "text_limit", "max_tree_nodes", "max_tree_depth",
                "prune", "boxes", "include_screenshot",
                "capture_snapshot",
            ]
        )
        if let count = integer(value["max_tree_nodes"]), count < 0 {
            throw invalid("\(path).max_tree_nodes must be non-negative")
        }
        if let depth = integer(value["max_tree_depth"]), depth < 0 {
            throw invalid("\(path).max_tree_depth must be non-negative")
        }
        if (value["include_screenshot"] as? Bool) == true {
            // AX images are full-desktop GUI frames that the core captures
            // with a desktop batch; this provider never captures a window image.
            throw invalid(
                "\(path).include_screenshot is not supported; request a desktop batch screenshot"
            )
        }
        if let capture = value["capture_snapshot"], !(capture is Bool) {
            throw invalid("\(path).capture_snapshot must be a boolean")
        }
        if let limit = value["text_limit"] {
            let valid = (limit as? String) == "max" ||
                ((integer(limit) ?? 0) > 0)
            guard valid else {
                throw invalid(
                    "\(path).text_limit must be a positive integer or max"
                )
            }
        }
    }

    static func validateExpectedWindow(
        _ raw: Any?,
        windowHandle: Any?,
        path: String
    ) throws {
        guard let value = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(
            value,
            path: path,
            allowed: [
                "windowHandle", "providerBusName", "providerObjectPath",
                "processGeneration", "role", "name",
            ]
        )
        for field in [
            "windowHandle", "providerBusName", "providerObjectPath",
            "processGeneration", "role",
        ] {
            try requireNonblankString(
                value[field],
                path: "\(path).\(field)"
            )
        }
        let privateHandle = try requiredString(
            windowHandle,
            path: "\(path).window_handle"
        )
        guard string(value["windowHandle"]) == privateHandle else {
            throw invalid(
                "\(path).windowHandle must match action.window_handle"
            )
        }
        if let name = value["name"], !(name is String) {
            throw invalid("\(path).name must be a string")
        }
    }

    static func validateTarget(_ raw: Any?, path: String) throws {
        guard let value = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(
            value,
            path: path,
            allowed: [
                "selector", "locatorKind", "allowProviderRebind", "editTarget",
                "windowHandle", "providerBusName", "providerObjectPath",
                "processGeneration", "providerWindowBusName",
                "providerWindowObjectPath",
                "providerWindowProcessGeneration", "providerWindowRole",
                "providerWindowName", "providerRoleAliases",
                "providerIdentityScope", "automationId",
                "runtimeId", "runtimeAliases",
                "role", "name", "hasName", "predicates",
            ]
        )
        try validateEditTarget(value["editTarget"], path: "\(path).editTarget")
        try validateIntegerArray(value["runtimeId"], path: "\(path).runtimeId")
        if let aliases = value["runtimeAliases"] as? [Any] {
            for (index, alias) in aliases.enumerated() {
                try validateIntegerArray(
                    alias,
                    path: "\(path).runtimeAliases[\(index)]"
                )
            }
        } else if value["runtimeAliases"] != nil {
            throw invalid("\(path).runtimeAliases must be an array")
        }
        if let aliases = value["providerRoleAliases"] as? [Any] {
            for (index, alias) in aliases.enumerated() {
                try requireNonblankString(
                    alias,
                    path: "\(path).providerRoleAliases[\(index)]"
                )
            }
        } else if value["providerRoleAliases"] != nil {
            throw invalid("\(path).providerRoleAliases must be an array")
        }
        if let predicates = value["predicates"] as? [Any] {
            for (index, rawPredicate) in predicates.enumerated() {
                let predicate = try object(
                    rawPredicate,
                    path: "\(path).predicates[\(index)]"
                )
                try rejectUnknown(
                    predicate,
                    path: "\(path).predicates[\(index)]",
                    allowed: ["kind", "value"]
                )
                try requireNonblankString(
                    predicate["kind"],
                    path: "\(path).predicates[\(index)].kind"
                )
            }
        } else if value["predicates"] != nil {
            throw invalid("\(path).predicates must be an array")
        }
    }

    static func validateElement(_ raw: Any?, path: String) throws {
        guard let value = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(
            value,
            path: path,
            allowed: [
                "index", "runtimeId", "runtimeAliases", "automationId",
                "providerBusName", "providerObjectPath", "processGeneration",
                "providerIdentityScope", "providerWindowBusName",
                "providerWindowObjectPath",
                "providerWindowProcessGeneration", "providerWindowRole",
                "providerWindowName", "showing", "visible", "nameHash",
                "name", "controlType", "localizedControlType", "className",
                "value", "nativeWindowHandle", "frame", "actions", "states",
                "description", "textAttributes", "placeholder", "relations",
                "relationReadComplete", "nameSource", "editTarget",
            ]
        )
        try validateEditTarget(value["editTarget"], path: "\(path).editTarget")
        try validateIntegerArray(value["runtimeId"], path: "\(path).runtimeId")
        if let aliases = value["runtimeAliases"] as? [Any] {
            for (index, alias) in aliases.enumerated() {
                try validateIntegerArray(
                    alias,
                    path: "\(path).runtimeAliases[\(index)]"
                )
            }
        } else if value["runtimeAliases"] != nil {
            throw invalid("\(path).runtimeAliases must be an array")
        }
        try rejectUnknownIfPresent(
            value["frame"],
            path: "\(path).frame",
            allowed: ["x", "y", "width", "height"]
        )
    }

    private static func validateEditTarget(_ raw: Any?, path: String) throws {
        guard let value = try optionalObject(raw, path: path) else { return }
        let fields: Set<String> = ["providerBusName", "providerObjectPath", "processGeneration"]
        try rejectUnknown(value, path: path, allowed: fields)
        for field in fields.sorted() {
            try requireNonblankString(value[field], path: "\(path).\(field)")
        }
    }

    private static func validateIntegerArray(_ raw: Any?, path: String) throws {
        guard let raw else {
            return
        }
        guard let values = raw as? [Any],
              values.allSatisfy({ integer($0) != nil }) else {
            throw invalid("\(path) must be an integer array")
        }
    }

    static func validateKeys(
        _ raw: Any?,
        path: String,
        optional: Bool
    ) throws {
        if raw == nil, optional {
            return
        }
        guard let values = raw as? [Any], !values.isEmpty else {
            throw invalid("\(path) must be a non-empty string array")
        }
        for (index, value) in values.enumerated() {
            try requireNonblankString(value, path: "\(path)[\(index)]")
        }
    }

    static func rejectUnknownIfPresent(
        _ raw: Any?,
        path: String,
        allowed: Set<String>
    ) throws {
        guard let value = try optionalObject(raw, path: path) else {
            return
        }
        try rejectUnknown(value, path: path, allowed: allowed)
    }

    static func rejectUnknown(
        _ value: [String: Any],
        path: String,
        allowed: Set<String>
    ) throws {
        let unknown = value.keys.filter { !allowed.contains($0) }.sorted()
        if !unknown.isEmpty {
            throw invalid(
                "\(path) contains unknown fields: \(unknown.joined(separator: ", "))"
            )
        }
    }

    static func object(
        _ raw: Any?,
        path: String
    ) throws -> [String: Any] {
        guard let value = raw as? [String: Any] else {
            throw invalid("\(path) must be an object")
        }
        return value
    }

    static func optionalObject(
        _ raw: Any?,
        path: String
    ) throws -> [String: Any]? {
        guard let raw else {
            return nil
        }
        return try object(raw, path: path)
    }

    static func requiredString(
        _ raw: Any?,
        path: String
    ) throws -> String {
        guard let value = raw as? String else {
            throw invalid("\(path) must be a string")
        }
        return value
    }

    static func requireNonblankString(
        _ raw: Any?,
        path: String
    ) throws {
        guard let value = raw as? String,
              !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        else {
            throw invalid("\(path) must be a non-empty string")
        }
    }

    static func string(_ raw: Any?) -> String? {
        raw as? String
    }

    static func integer(_ raw: Any?) -> Int? {
        if let value = raw as? Int {
            return value
        }
        if let value = raw as? NSNumber,
           CFGetTypeID(value) != CFBooleanGetTypeID()
        {
            return value.intValue
        }
        return nil
    }

    static func number(_ raw: Any?) -> Double? {
        if let value = raw as? Double {
            return value
        }
        if let value = raw as? Int {
            return Double(value)
        }
        if let value = raw as? NSNumber,
           CFGetTypeID(value) != CFBooleanGetTypeID()
        {
            return value.doubleValue
        }
        return nil
    }

    static func boolean(_ raw: Any?) -> Bool? {
        guard let value = raw as? NSNumber,
              CFGetTypeID(value) == CFBooleanGetTypeID()
        else {
            return nil
        }
        return value.boolValue
    }

    static func invalid(_ message: String) -> ComputerUseError {
        .invalidArguments(message)
    }
}
