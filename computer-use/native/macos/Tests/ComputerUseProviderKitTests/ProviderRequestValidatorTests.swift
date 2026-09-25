@testable import ComputerUseProviderKit
import Foundation
import Testing

private func envelope(
    operation: String,
    payloadName: String,
    payload: [String: Any]
) -> [String: Any] {
    [
        "protocol": "computer-use-provider-v2",
        "schema_version": 2,
        "request_id": "validation-test",
        "operation": operation,
        payloadName: payload,
    ]
}

@Test
func allSevenProviderOperationsAreAccepted() throws {
    let identity: [String: Any] = [
        "providerBusName": "mac:42",
        "processGeneration": "123456",
    ]
    let requests: [[String: Any]] = [
        envelope(
            operation: "observe_apps",
            payloadName: "observe_apps",
            payload: ["protocol_version": 2]
        ),
        envelope(
            operation: "observe",
            payloadName: "observe",
            payload: [
                "protocol_version": 2,
                "app": "TextEdit",
                "app_identity": identity,
            ]
        ),
        envelope(
            operation: "resolve_and_act",
            payloadName: "action",
            payload: [
                "protocol_version": 2,
                "intent": "resolve_target",
                "app": "TextEdit",
                "target": [
                    "selector": "role=button",
                    "locatorKind": "exact",
                    "providerRoleAliases": ["AXButton"],
                    "providerIdentityScope": "process",
                    "runtimeId": [4],
                    "runtimeAliases": [[3]],
                    "hasName": false,
                ] as [String: Any],
            ]
        ),
        envelope(
            operation: "ordered_input",
            payloadName: "ordered_input",
            payload: orderedInputValidationPayload()
        ),
        envelope(
            operation: "desktop_execute_batch",
            payloadName: "desktop",
            payload: [
                "protocol_version": 2,
                "actions": [["type": "screenshot"]],
                "settle_ms": 0,
            ]
        ),
        envelope(
            operation: "window_list",
            payloadName: "window_list",
            payload: [
                "protocol_version": 2,
                "app": "TextEdit",
                "app_identity": identity,
            ]
        ),
        envelope(
            operation: "window_action",
            payloadName: "window_action",
            payload: [
                "protocol_version": 2,
                "action": "activate",
                "app": "TextEdit",
                "target": [
                    "selector": "role=window",
                    "windowHandle": "w2:mac:42:123456:7",
                    "hasName": false,
                ] as [String: Any],
            ]
        ),
    ]
    for request in requests {
        try ProviderRequestValidator.validate(request)
    }
}

private func orderedInputValidationPayload() -> [String: Any] {
    let handle = "w2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    return [
        "protocol_version": 2,
        "contract_version": 1,
        "app": "TextEdit",
        "app_identity": [
            "providerBusName": "mac:42",
            "processGeneration": "123456",
        ],
        "window_handle": handle,
        "expected_window": [
            "windowHandle": handle,
            "providerBusName": "mac:42",
            "providerObjectPath": "window:7",
            "processGeneration": "123456",
            "role": "window",
            "name": "Document",
        ],
        "steps": [
            ["action_index": 3, "key": "DOWN"],
            ["action_index": 4, "key": "ENTER"],
        ],
        "final_observation": ["capture_snapshot": true],
    ]
}

@Test
func orderedInputRequiresContiguousStepsAndCanonicalBoundary() throws {
    try ProviderRequestValidator.validate(
        envelope(
            operation: "ordered_input",
            payloadName: "ordered_input",
            payload: orderedInputValidationPayload()
        )
    )

    var noncontiguous = orderedInputValidationPayload()
    noncontiguous["steps"] = [
        ["action_index": 3, "key": "DOWN"],
        ["action_index": 5, "key": "ENTER"],
    ]
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "ordered_input",
                payloadName: "ordered_input",
                payload: noncontiguous
            )
        )
    }

    var noBoundary = orderedInputValidationPayload()
    noBoundary["final_observation"] = ["capture_snapshot": false]
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "ordered_input",
                payloadName: "ordered_input",
                payload: noBoundary
            )
        )
    }
}

@Test
func orderedInputValidatesEveryKeyBeforeExecution() throws {
    var punctuation = orderedInputValidationPayload()
    punctuation["steps"] = [
        ["action_index": 3, "key": "SUPER+,"],
    ]
    try ProviderRequestValidator.validate(
        envelope(
            operation: "ordered_input",
            payloadName: "ordered_input",
            payload: punctuation
        )
    )

    var invalid = orderedInputValidationPayload()
    invalid["steps"] = [
        ["action_index": 3, "key": "SUPER+NOT_A_REAL_KEY"],
    ]
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "ordered_input",
                payloadName: "ordered_input",
                payload: invalid
            )
        )
    }
}

@Test(arguments: [
    "resolve_target", "click", "scroll", "type", "keypress", "set_value",
    "increment", "decrement",
])
func allAccessibilityIntentsAreAccepted(_ intent: String) throws {
    var payload: [String: Any] = [
        "protocol_version": 2,
        "intent": intent,
        "app": "TextEdit",
        "window_handle": "w2:mac:42:123456:7",
    ]
    if [
        "resolve_target", "click", "set_value", "increment", "decrement",
    ].contains(intent) {
        payload["target"] = [
            "selector": "role=text",
            "hasName": false,
        ]
    }
    switch intent {
    case "click":
        payload["input"] = ["click_count": 1]
    case "scroll":
        payload["input"] = ["direction": "down", "pages": 1]
    case "type":
        payload["input"] = ["text": "Ada"]
    case "keypress":
        payload["input"] = ["key": "CMD+A"]
    case "set_value":
        payload["input"] = ["value": ""]
    default:
        break
    }
    try ProviderRequestValidator.validate(
        envelope(
            operation: "resolve_and_act",
            payloadName: "action",
            payload: payload
        )
    )
}

@Test
func accessibilityKeypressValidatesTheKeyBeforeExecution() throws {
    let identity: [String: Any] = [
        "providerBusName": "mac:42",
        "processGeneration": "123456",
    ]
    let payload: [String: Any] = [
        "protocol_version": 2,
        "intent": "keypress",
        "app": "TextEdit",
        "app_identity": identity,
        "window_handle": "w2:mac:42:123456:7",
        "expected_window": [
            "windowHandle": "w2:mac:42:123456:7",
            "providerBusName": "mac:42",
            "providerObjectPath": "window:7",
            "processGeneration": "123456",
            "role": "window",
            "name": "Document",
        ] as [String: Any],
        "input": ["key": "SUPER+NOT_A_REAL_KEY"],
    ]

    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "resolve_and_act",
                payloadName: "action",
                payload: payload
            )
        )
    }
}

@Test
func relativeAdjustmentRequiresTargetAndRejectsInput() {
    var missingTarget: [String: Any] = [
        "protocol_version": 2,
        "intent": "increment",
        "app": "TextEdit",
    ]
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "resolve_and_act",
                payloadName: "action",
                payload: missingTarget
            )
        )
    }

    missingTarget["target"] = [
        "selector": "role=slider",
        "hasName": false,
    ]
    missingTarget["input"] = [:] as [String: Any]
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "resolve_and_act",
                payloadName: "action",
                payload: missingTarget
            )
        )
    }
}

@Test
func unsupportedOperationIsRejected() {
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "unsupported_operation",
                payloadName: "unsupported_operation",
                payload: ["protocol_version": 2]
            )
        )
    }
}

@Test
func unsupportedAccessibilityIntentIsRejected() {
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "resolve_and_act",
                payloadName: "action",
                payload: [
                    "protocol_version": 2,
                    "intent": "unsupported_intent",
                    "app": "TextEdit",
                    "target": [
                        "selector": "role=button",
                        "hasName": false,
                    ],
                ]
            )
        )
    }
}

@Test
func unknownFieldsAreRejectedTogether() {
    var request = envelope(
        operation: "observe_apps",
        payloadName: "observe_apps",
        payload: ["protocol_version": 2]
    )
    request["typo_two"] = true
    request["typo_one"] = true
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(request)
    }
}

@Test
func screenshotCanBeCombinedWithMutation() throws {
    try ProviderRequestValidator.validate(
        envelope(
            operation: "desktop_execute_batch",
            payloadName: "desktop",
            payload: [
                "protocol_version": 2,
                "actions": [
                    ["type": "screenshot"],
                    ["type": "click", "x": 10, "y": 20],
                ],
                "settle_ms": 0,
            ]
        )
    )
}

@Test
func desktopMouseActionsAcceptAdvertisedHeldModifierKeys() throws {
    let actions: [[String: Any]] = [
        ["type": "click", "x": 10, "y": 20, "keys": ["SUPER"]],
        ["type": "double_click", "x": 10, "y": 20, "keys": ["SHIFT"]],
        ["type": "move", "x": 10, "y": 20, "keys": ["ALT"]],
        [
            "type": "drag",
            "path": [["x": 10, "y": 20], ["x": 30, "y": 40]],
            "keys": ["CONTROL"],
        ],
        [
            "type": "scroll",
            "x": 10,
            "y": 20,
            "scroll_y": 1,
            "keys": ["SUPER"],
        ],
    ]

    try ProviderRequestValidator.validate(
        envelope(
            operation: "desktop_execute_batch",
            payloadName: "desktop",
            payload: [
                "protocol_version": 2,
                "actions": actions,
                "settle_ms": 0,
            ]
        )
    )
}

@Test
func accessibilityObservationCanSkipPostActionSnapshot() throws {
    try ProviderRequestValidator.validate(
        envelope(
            operation: "resolve_and_act",
            payloadName: "action",
            payload: [
                "protocol_version": 2,
                "intent": "click",
                "app": "TextEdit",
                "target": [
                    "selector": "button \"Save\"",
                    "hasName": false,
                ],
                "input": ["click_count": 1],
                "observation": ["capture_snapshot": false],
            ]
        )
    )
}

@Test
func accessibilityActionAcceptsStrictPrivateDiagnostics() throws {
    for enabled in [true, false] {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "resolve_and_act",
                payloadName: "action",
                payload: [
                    "protocol_version": 2,
                    "intent": "keypress",
                    "app": "TextEdit",
                    "window_handle": "w2:mac:42:123456:7",
                    "input": ["key": "ENTER"],
                    "diagnostics": ["phase_timing": enabled, "relative_readback": enabled],
                ]
            )
        )
    }
}

@Test
func accessibilityActionRejectsInvalidPrivateDiagnostics() {
    for diagnostics: [String: Any] in [
        ["phase_timing": "true"],
        ["phase_timing": 1],
        ["relative_readback": "true"],
        ["relative_readback": 1],
        ["relative_readback": NSNull()],
        ["phase_timing": true, "unknown": false],
    ] {
        #expect(throws: ComputerUseError.self) {
            try ProviderRequestValidator.validate(
                envelope(
                    operation: "resolve_and_act",
                    payloadName: "action",
                    payload: [
                        "protocol_version": 2,
                        "intent": "keypress",
                        "app": "TextEdit",
                        "window_handle": "w2:mac:42:123456:7",
                        "input": ["key": "ENTER"],
                        "diagnostics": diagnostics,
                    ]
                )
            )
        }
    }
}

@Test
func diagnosticsAreRejectedOutsideAccessibilityActions() {
    for (operation, payloadName, payload) in [
        (
            "observe",
            "observe",
            ["protocol_version": 2, "app": "TextEdit"]
        ),
        (
            "desktop_execute_batch",
            "desktop",
            [
                "protocol_version": 2,
                "actions": [["type": "screenshot"]],
                "settle_ms": 0,
            ]
        ),
        (
            "window_list",
            "window_list",
            ["protocol_version": 2, "app": "TextEdit"]
        ),
    ] as [(String, String, [String: Any])] {
        var invalidPayload = payload
        invalidPayload["diagnostics"] = ["phase_timing": true]
        #expect(throws: ComputerUseError.self) {
            try ProviderRequestValidator.validate(
                envelope(
                    operation: operation,
                    payloadName: payloadName,
                    payload: invalidPayload
                )
            )
        }
    }
}

@Test
func accessibilityActionAcceptsBoundExpectedWindow() throws {
    let handle = "w2:mac:42:123456:7"
    let payload: [String: Any] = [
        "protocol_version": 2,
        "intent": "keypress",
        "app": "Finder",
        "window_handle": handle,
        "expected_window": [
            "windowHandle": handle,
            "providerBusName": "mac:42",
            "providerObjectPath": "window:7",
            "processGeneration": "123456",
            "role": "window",
            "name": "Shared",
        ],
        "target": [
            "selector": "textbox",
            "hasName": false,
        ],
        "input": ["key": "ENTER"],
    ]
    try ProviderRequestValidator.validate(
        envelope(
            operation: "resolve_and_act",
            payloadName: "action",
            payload: payload
        )
    )

    var mismatched = payload
    mismatched["expected_window"] = [
        "windowHandle": "w2:mac:42:123456:8",
        "providerBusName": "mac:42",
        "providerObjectPath": "window:8",
        "processGeneration": "123456",
        "role": "window",
    ]
    #expect(throws: ComputerUseError.self) {
        try ProviderRequestValidator.validate(
            envelope(
                operation: "resolve_and_act",
                payloadName: "action",
                payload: mismatched
            )
        )
    }
}
