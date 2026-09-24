"""Strict Provider V2 process boundary for the Linux implementation."""

import json
import sys
import traceback
from typing import Any

from .ax_dispatch import perform_provider_request
from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    GUI_MAX_SETTLE_MS,
    OperationFailure,
    require_desktop_session,
)
from .response_builder import (
    operation_failure_response,
    unexpected_operation_failure_response,
)


PROTOCOL = "computer-use-provider-v2"
SCHEMA_VERSION = 2
_provider_initialized = False


class ProviderRequestError(ValueError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def _object(value: object, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} must be an object".format(path),
        )
    return value


def _strict_object(
    value: object,
    path: str,
    allowed: set[str],
    required: tuple[str, ...] | set[str] = (),
) -> dict[str, Any]:
    value = _object(value, path)
    unknown = sorted(set(value) - set(allowed))
    if unknown:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} contains unknown field(s): {}".format(
                path, ", ".join(unknown)
            ),
        )
    missing = [field for field in required if field not in value]
    if missing:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} is missing required field(s): {}".format(
                path, ", ".join(missing)
            ),
        )
    return value


OBSERVATION_FIELDS = {
    "text_limit",
    "max_tree_nodes",
    "max_tree_depth",
    "prune",
    "boxes",
    "include_screenshot",
    "screenshot_scope",
    "capture_snapshot",
}
APP_IDENTITY_FIELDS = {"providerBusName", "processGeneration"}
EXPECTED_WINDOW_FIELDS = {
    "windowHandle",
    "providerBusName",
    "providerObjectPath",
    "processGeneration",
    "role",
    "name",
}
FRAME_FIELDS = {"x", "y", "width", "height"}
TARGET_FIELDS = {
    "selector",
    "locatorKind",
    "allowProviderRebind",
    "windowHandle",
    "providerBusName",
    "providerObjectPath",
    "processGeneration",
    "providerWindowBusName",
    "providerWindowObjectPath",
    "providerWindowProcessGeneration",
    "providerWindowRole",
    "providerWindowName",
    "providerRoleAliases",
    "providerIdentityScope",
    "runtimeId",
    "runtimeAliases",
    "role",
    "name",
    "hasName",
    "predicates",
}
PREDICATE_FIELDS = {"kind", "value"}
ACTION_INPUT_CONTRACTS = {
    "resolve_target": (set(), set()),
    "click": (
        {"x", "y", "click_count", "mouse_button", "click_method"},
        set(),
    ),
    "scroll": (
        {"direction", "pages", "window_centre"},
        {"direction", "pages"},
    ),
    "type": ({"text"}, {"text"}),
    "keypress": ({"key"}, {"key"}),
    "set_value": ({"value"}, {"value"}),
}
ELEMENT_FIELDS = {
    "index",
    "runtimeId",
    "automationId",
    "providerBusName",
    "providerObjectPath",
    "processGeneration",
    "providerIdentityScope",
    "providerWindowBusName",
    "providerWindowObjectPath",
    "providerWindowProcessGeneration",
    "providerWindowRole",
    "providerWindowName",
    "showing",
    "visible",
    "nameHash",
    "name",
    "controlType",
    "localizedControlType",
    "className",
    "value",
    "nativeWindowHandle",
    "frame",
    "actions",
    "states",
    "description",
    "textAttributes",
    "placeholder",
    "relations",
    "relationReadComplete",
    "nameSource",
}
POINT_FIELDS = {"x", "y"}
DESKTOP_ACTION_CONTRACTS = {
    "screenshot": ({"type"}, {"type"}),
    "click": ({"type", "x", "y", "button", "keys"}, {"type", "x", "y"}),
    "double_click": ({"type", "x", "y", "keys"}, {"type", "x", "y"}),
    "move": ({"type", "x", "y", "keys"}, {"type", "x", "y"}),
    "drag": ({"type", "path", "keys"}, {"type", "path"}),
    "scroll": (
        {"type", "x", "y", "scroll_x", "scroll_y", "keys"},
        {"type", "x", "y"},
    ),
    "type": ({"type", "text"}, {"type", "text"}),
    "keypress": ({"type", "keys"}, {"type", "keys"}),
    "wait": ({"type"}, {"type"}),
}


def _non_empty_string(value: object, path: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} must be a non-empty string".format(path),
        )
    return value


def _integer_list(value: object, path: str) -> None:
    if not isinstance(value, list) or any(
        not isinstance(item, int) or isinstance(item, bool)
        for item in value
    ):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} must be an array of integers".format(path),
        )


def _validate_app_identity(value, path):
    if value is not None:
        identity = _strict_object(
            value,
            path,
            APP_IDENTITY_FIELDS,
            ("providerBusName", "processGeneration"),
        )
        _non_empty_string(
            identity["providerBusName"],
            path + ".providerBusName",
        )
        _non_empty_string(
            identity["processGeneration"],
            path + ".processGeneration",
        )


def _validate_observation(value, path):
    if value is None:
        return
    observation = _strict_object(value, path, OBSERVATION_FIELDS)
    text_limit = observation.get("text_limit")
    if text_limit is not None:
        valid_limit = (
            text_limit == "max"
            or (
                isinstance(text_limit, int)
                and not isinstance(text_limit, bool)
                and text_limit > 0
            )
        )
        if not valid_limit:
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.text_limit must be a positive integer or 'max'".format(path),
            )
    for field in ("max_tree_nodes", "max_tree_depth"):
        field_value = observation.get(field)
        if field_value is not None and (
            not isinstance(field_value, int)
            or isinstance(field_value, bool)
            or field_value < 0
        ):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.{} must be a non-negative integer".format(path, field),
            )
    for field in ("prune", "boxes", "include_screenshot", "capture_snapshot"):
        field_value = observation.get(field)
        if field_value is not None and not isinstance(field_value, bool):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.{} must be a boolean".format(path, field),
            )
    screenshot_scope = observation.get("screenshot_scope")
    if screenshot_scope is not None and screenshot_scope not in {"screen", "window"}:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.screenshot_scope must be 'screen' or 'window'".format(path),
        )


def _number(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _validate_keys(value, path):
    if not isinstance(value, list) or not value:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} must be a non-empty array".format(path),
        )
    if any(not isinstance(key, str) or not key.strip() for key in value):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} entries must be non-empty strings".format(path),
        )


def _validate_desktop_action(action: object, index: int) -> None:
    path = "desktop.actions[{}]".format(index)
    if not isinstance(action, dict):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{} must be an object".format(path),
        )
    action_type = action.get("type")
    if not isinstance(action_type, str):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.type must be a string".format(path),
        )
    contract = DESKTOP_ACTION_CONTRACTS.get(action_type)
    if contract is None:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.type is unsupported: {!r}".format(path, action_type),
        )
    allowed, required = contract
    action = _strict_object(action, path, allowed, required)
    for field in ("x", "y", "scroll_x", "scroll_y"):
        if field in action and not _number(action[field]):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.{} must be a number".format(path, field),
            )
    if action_type == "click" and action.get("button", "left") not in {
        "left", "right", "wheel", "back", "forward",
    }:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.button is unsupported".format(path),
        )
    if "keys" in action:
        _validate_keys(action["keys"], path + ".keys")
    if action_type == "drag":
        points = action["path"]
        if not isinstance(points, list) or len(points) < 2:
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.path must contain at least two points".format(path),
            )
        for point_index, point in enumerate(points):
            point_path = "{}.path[{}]".format(path, point_index)
            point = _strict_object(point, point_path, POINT_FIELDS, POINT_FIELDS)
            if not _number(point["x"]) or not _number(point["y"]):
                raise ProviderRequestError(
                    "invalid_provider_request",
                    "{} coordinates must be numbers".format(point_path),
                )
    if action_type == "scroll":
        if not action.get("scroll_x", 0) and not action.get("scroll_y", 0):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{} requires a non-zero scroll_x or scroll_y".format(path),
            )
    if action_type == "type" and (
        not isinstance(action["text"], str) or not action["text"]
    ):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.text must be a non-empty string".format(path),
        )


def _validate_target(value, path):
    if value is None:
        return
    target = _strict_object(value, path, TARGET_FIELDS, ("selector", "hasName"))
    _non_empty_string(target["selector"], path + ".selector")
    if not isinstance(target["hasName"], bool):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.hasName must be a boolean".format(path),
        )
    if "allowProviderRebind" in target and not isinstance(
        target["allowProviderRebind"], bool
    ):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.allowProviderRebind must be a boolean".format(path),
        )
    for field in (
        "locatorKind",
        "windowHandle",
        "providerBusName",
        "providerObjectPath",
        "processGeneration",
        "providerWindowBusName",
        "providerWindowObjectPath",
        "providerWindowProcessGeneration",
        "providerWindowRole",
        "providerWindowName",
        "providerIdentityScope",
        "role",
        "name",
    ):
        if field in target and not isinstance(target[field], str):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.{} must be a string".format(path, field),
            )
    if "providerRoleAliases" in target:
        aliases = target["providerRoleAliases"]
        if not isinstance(aliases, list) or any(
            not isinstance(alias, str) for alias in aliases
        ):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.providerRoleAliases must be an array of strings".format(path),
            )
    if "runtimeId" in target:
        _integer_list(target["runtimeId"], path + ".runtimeId")
    if "runtimeAliases" in target:
        aliases = target["runtimeAliases"]
        if not isinstance(aliases, list):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.runtimeAliases must be an array".format(path),
            )
        for index, alias in enumerate(aliases):
            _integer_list(alias, "{}.runtimeAliases[{}]".format(path, index))
    predicates = target.get("predicates")
    if predicates is not None and not isinstance(predicates, list):
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.predicates must be an array".format(path),
        )
    for index, predicate in enumerate(target.get("predicates") or []):
        predicate = _strict_object(
            predicate,
            "{}.predicates[{}]".format(path, index),
            PREDICATE_FIELDS,
            ("kind", "value"),
        )
        _non_empty_string(
            predicate["kind"],
            "{}.predicates[{}].kind".format(path, index),
        )
        if not isinstance(predicate["value"], str):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.predicates[{}].value must be a string".format(path, index),
            )


def _validate_expected_window(value, path, window_handle):
    if value is None:
        return
    expected = _strict_object(
        value,
        path,
        EXPECTED_WINDOW_FIELDS,
        (
            "windowHandle",
            "providerBusName",
            "processGeneration",
            "role",
        ),
    )
    for field in EXPECTED_WINDOW_FIELDS:
        if field in expected and not isinstance(expected[field], str):
            raise ProviderRequestError(
                "invalid_provider_request",
                "{}.{} must be a string".format(path, field),
            )
    for field in (
        "windowHandle",
        "providerBusName",
        "processGeneration",
        "role",
    ):
        _non_empty_string(expected[field], "{}.{}".format(path, field))
    if (
        expected["role"].strip().lower()
        in {"alert", "alert dialog", "dialog", "file chooser", "color chooser"}
    ):
        _non_empty_string(
            expected.get("providerObjectPath"),
            path + ".providerObjectPath",
        )
    if expected["windowHandle"] != window_handle:
        raise ProviderRequestError(
            "invalid_provider_request",
            "{}.windowHandle must match action.window_handle".format(path),
        )


def _validate_element(value, path):
    if value is None:
        return
    element = _strict_object(value, path, ELEMENT_FIELDS, ("index",))
    if element.get("frame") is not None:
        _strict_object(
            element["frame"], path + ".frame", FRAME_FIELDS, FRAME_FIELDS
        )


def _validate_action_input(payload):
    intent = payload["intent"]
    contract = ACTION_INPUT_CONTRACTS.get(intent)
    if contract is None:
        raise ProviderRequestError(
            "invalid_provider_request",
            "action.intent is unsupported: {!r}".format(intent),
        )
    input_value = payload.get("input")
    allowed, required = contract
    input_object: dict[str, Any] = {}
    if intent == "resolve_target":
        if input_value is not None:
            raise ProviderRequestError(
                "invalid_provider_request",
                "action.input is not allowed for resolve_target",
            )
    else:
        input_object = _strict_object(
            input_value,
            "action.input",
            allowed,
            required,
        )
    has_target = payload.get("target") is not None or payload.get("element") is not None
    if intent in {"resolve_target", "click", "set_value"} and not has_target:
        raise ProviderRequestError(
            "invalid_provider_request",
            "action.{} requires an exact target".format(intent),
        )
    if intent in {"scroll", "type", "keypress"}:
        if not has_target and not payload.get("window_handle"):
            raise ProviderRequestError(
                "invalid_provider_request",
                "untargeted action.{} requires window_handle".format(intent),
            )
    if intent == "scroll":
        if input_object["direction"] not in {"up", "down", "left", "right"}:
            raise ProviderRequestError(
                "invalid_provider_request",
                "action.input.direction must be up, down, left, or right",
            )
        if not _number(input_object["pages"]) or input_object["pages"] <= 0:
            raise ProviderRequestError(
                "invalid_provider_request",
                "action.input.pages must be greater than zero",
            )
    if intent == "type" and not isinstance(input_object["text"], str):
        raise ProviderRequestError(
            "invalid_provider_request",
            "action.input.text must be a string",
        )
    if intent == "keypress":
        if (
            not isinstance(input_object["key"], str)
            or not input_object["key"].strip()
        ):
            raise ProviderRequestError(
                "invalid_provider_request",
                "action.input.key must be a non-empty string",
            )
    if intent == "set_value" and not isinstance(input_object["value"], str):
        raise ProviderRequestError(
            "invalid_provider_request",
            "action.input.value must be a string",
        )


def _validate_payload(operation, payload):
    if operation == "observe_apps":
        _strict_object(
            payload,
            "observe_apps",
            {"protocol_version"},
            ("protocol_version",),
        )
        return
    if operation == "observe":
        payload = _strict_object(
            payload,
            "observe",
            {
                "protocol_version",
                "app",
                "app_identity",
                "window_handle",
                "observation",
            },
            ("protocol_version", "app"),
        )
        _non_empty_string(payload["app"], "observe.app")
        _validate_app_identity(payload.get("app_identity"), "observe.app_identity")
        _validate_observation(payload.get("observation"), "observe.observation")
        return
    if operation == "resolve_and_act":
        payload = _strict_object(
            payload,
            "action",
            {
                "protocol_version",
                "intent",
                "app",
                "app_identity",
                "window_handle",
                "expected_window",
                "element",
                "target",
                "input",
                "window_bounds",
                "observation",
            },
            ("protocol_version", "intent", "app"),
        )
        _non_empty_string(payload["app"], "action.app")
        _validate_app_identity(payload.get("app_identity"), "action.app_identity")
        _validate_expected_window(
            payload.get("expected_window"),
            "action.expected_window",
            payload.get("window_handle"),
        )
        _validate_element(payload.get("element"), "action.element")
        _validate_target(payload.get("target"), "action.target")
        _validate_action_input(payload)
        if payload.get("window_bounds") is not None:
            _strict_object(
                payload["window_bounds"],
                "action.window_bounds",
                FRAME_FIELDS,
                FRAME_FIELDS,
            )
        _validate_observation(payload.get("observation"), "action.observation")
        return
    if operation == "desktop_execute_batch":
        payload = _strict_object(
            payload,
            "desktop",
            {
                "protocol_version",
                "actions",
                "settle_ms",
                "frame_id",
                "lease_state",
            },
            ("protocol_version", "actions", "settle_ms"),
        )
        if not isinstance(payload["actions"], list):
            raise ProviderRequestError(
                "invalid_provider_request",
                "desktop.actions must be an array",
            )
        if not payload["actions"]:
            raise ProviderRequestError(
                "invalid_provider_request",
                "desktop.actions must not be empty",
            )
        settle_ms = payload["settle_ms"]
        if (
            not isinstance(settle_ms, int)
            or isinstance(settle_ms, bool)
            or settle_ms < 0
            or settle_ms > GUI_MAX_SETTLE_MS
        ):
            raise ProviderRequestError(
                "invalid_provider_request",
                "desktop.settle_ms must be an integer between 0 and {}".format(
                    GUI_MAX_SETTLE_MS
                ),
            )
        for index, action in enumerate(payload["actions"]):
            _validate_desktop_action(action, index)
        return
    if operation == "window_list":
        payload = _strict_object(
            payload,
            "window_list",
            {
                "protocol_version",
                "app",
                "app_identity",
                "window_handle",
            },
            ("protocol_version", "app"),
        )
        _non_empty_string(payload["app"], "window_list.app")
        _validate_app_identity(
            payload.get("app_identity"), "window_list.app_identity"
        )
        return
    if operation == "window_action":
        payload = _strict_object(
            payload,
            "window_action",
            {
                "protocol_version",
                "action",
                "app",
                "app_identity",
                "window_handle",
                "target",
                "observation",
            },
            ("protocol_version", "action", "app"),
        )
        _non_empty_string(payload["app"], "window_action.app")
        _validate_app_identity(
            payload.get("app_identity"), "window_action.app_identity"
        )
        if payload["action"] not in {"activate", "close"}:
            raise ProviderRequestError(
                "invalid_provider_request",
                "window_action.action must be 'activate' or 'close'",
            )
        if payload.get("target") is None:
            raise ProviderRequestError(
                "invalid_provider_request",
                "window_action requires an exact target",
            )
        _validate_target(payload.get("target"), "window_action.target")
        _validate_observation(
            payload.get("observation"), "window_action.observation"
        )
        return
    raise ProviderRequestError(
        "invalid_provider_request",
        "unsupported provider operation {!r}".format(operation),
    )


def _failure(response):
    code = response.get("failureCode")
    underlying = response.get("underlyingFailureCode")
    phase = response.get("failurePhase")
    subject = response.get("failureSubject")
    replacement = response.get("replacementWindow")
    facts_complete = response.get("failureFactsComplete")
    if not any((code, underlying, phase, subject, replacement, facts_complete)):
        return None
    result = {
        "code": code or "",
        "underlying_code": underlying or "",
        "phase": phase or "",
        "facts_complete": bool(facts_complete),
        "subject": subject or "",
    }
    if replacement is not None:
        result["replacement_window"] = replacement
    return result


def _mutation(response):
    result = {
        "input_events_emitted": bool(
            response.get("input_events_emitted")
        ),
        "partial_input_possible": bool(
            response.get("partial_input_possible")
        ),
        "desktop_state_mutated": bool(
            response.get("desktop_state_mutated")
        ),
        "mutation_unknown": bool(response.get("mutation_unknown")),
    }
    return result if any(result.values()) else None


def _common_result(response):
    result: dict[str, object] = {
        "ok": bool(response.get("ok")),
    }
    for source, destination in (
        ("text", "text"),
        ("error", "error"),
        ("notes", "notes"),
        ("snapshot", "snapshot"),
    ):
        if response.get(source) not in (None, "", []):
            result[destination] = response[source]
    failure = _failure(response)
    if failure is not None:
        result["failure"] = failure
    mutation = _mutation(response)
    if mutation is not None:
        result["mutation"] = mutation
    return result


def _accessibility_result(response):
    result = _common_result(response)
    mappings = (
        ("actionEvidence", "action_evidence"),
        ("recoveryContext", "recovery_context"),
        ("apps", "apps"),
        ("resolvedElement", "resolved_element"),
        ("candidates", "candidates"),
        ("candidateCount", "candidate_count"),
        ("candidatesTruncated", "candidates_truncated"),
        ("guiLeaseState", "lease_state"),
        ("externalDesktopTransition", "external_desktop_transition"),
        ("observationSkipped", "observation_skipped"),
    )
    for source, destination in mappings:
        value = response.get(source)
        if value not in (None, "", [], False, 0):
            result[destination] = value
    return result


def _desktop_result(response):
    result = _common_result(response)
    gui_result = response.get("guiResult")
    if isinstance(gui_result, dict) and gui_result:
        result["gui_result"] = gui_result
    if response.get("validation_errors"):
        result["validation_errors"] = response["validation_errors"]
    if response.get("guiLeaseState"):
        result["lease_state"] = response["guiLeaseState"]
    return result


def _window_result(response):
    result = _common_result(response)
    mappings = (
        ("appName", "app_name"),
        ("appPid", "app_pid"),
        ("windowTitles", "window_titles"),
        ("actionEvidence", "action_evidence"),
        ("recoveryContext", "recovery_context"),
        ("guiLeaseState", "lease_state"),
        ("externalDesktopTransition", "external_desktop_transition"),
    )
    for source, destination in mappings:
        value = response.get(source)
        if value not in (None, "", [], False, 0):
            result[destination] = value
    return result


def provider_response(request, response, provider_reset_required=False):
    operation = request["operation"]
    envelope = {
        "protocol": PROTOCOL,
        "schema_version": SCHEMA_VERSION,
        "request_id": request["request_id"],
        "operation": operation,
    }
    if provider_reset_required:
        envelope["provider_reset_required"] = True
    if operation in {"observe_apps", "observe", "resolve_and_act"}:
        envelope["accessibility"] = _accessibility_result(response)
    elif operation == "desktop_execute_batch":
        envelope["desktop"] = _desktop_result(response)
    elif operation in {"window_list", "window_action"}:
        envelope["window"] = _window_result(response)
    else:
        raise ValueError("unsupported provider operation {!r}".format(operation))
    return envelope


def validate_request(request):
    request = _strict_object(
        request,
        "provider request",
        {
            "protocol",
            "schema_version",
            "request_id",
            "operation",
            "observe_apps",
            "observe",
            "action",
            "desktop",
            "window_list",
            "window_action",
        },
        ("protocol", "schema_version", "request_id", "operation"),
    )
    if request.get("protocol") != PROTOCOL:
        raise ProviderRequestError(
            "provider_protocol_mismatch",
            "provider protocol must be {!r}".format(PROTOCOL),
        )
    if request.get("schema_version") != SCHEMA_VERSION:
        raise ProviderRequestError(
            "provider_protocol_mismatch",
            "provider schema_version must be {}".format(SCHEMA_VERSION)
        )
    if not request.get("request_id"):
        raise ProviderRequestError(
            "invalid_provider_request",
            "provider request_id is required",
        )
    operation = request.get("operation")
    if not isinstance(operation, str):
        raise ProviderRequestError(
            "invalid_provider_request",
            "provider operation must be a string",
        )
    payload_key = {
        "observe_apps": "observe_apps",
        "observe": "observe",
        "resolve_and_act": "action",
        "desktop_execute_batch": "desktop",
        "window_list": "window_list",
        "window_action": "window_action",
    }.get(operation)
    if payload_key is None:
        raise ProviderRequestError(
            "invalid_provider_request",
            "unsupported provider operation {!r}".format(operation),
        )
    payloads = (
        "observe_apps",
        "observe",
        "action",
        "desktop",
        "window_list",
        "window_action",
    )
    present = [key for key in payloads if request.get(key) is not None]
    if present != [payload_key]:
        raise ProviderRequestError(
            "invalid_provider_request",
            "provider operation {!r} requires only payload {!r}".format(
                operation, payload_key
            )
        )
    payload = request[payload_key]
    _validate_payload(operation, payload)
    if payload.get("protocol_version") != SCHEMA_VERSION:
        raise ProviderRequestError(
            "provider_protocol_mismatch",
            "provider payload protocol_version must be {}".format(
                SCHEMA_VERSION
            )
        )


def provider_boundary_failure(
    request, code, message, phase, provider_reset_required=False
):
    operation = (
        request.get("operation")
        if isinstance(request, dict)
        else "observe_apps"
    )
    if operation not in {
        "observe_apps",
        "observe",
        "resolve_and_act",
        "desktop_execute_batch",
        "window_list",
        "window_action",
    }:
        operation = "observe_apps"
    response = {
        "ok": False,
        "error": message,
        "failureCode": code,
        "failurePhase": phase,
        "failureFactsComplete": True,
        "input_events_emitted": False,
        "partial_input_possible": False,
        "mutation_unknown": False,
    }
    request_id = (
        request.get("request_id")
        if isinstance(request, dict)
        else None
    ) or "startup"
    envelope_request = {
        "operation": operation,
        "request_id": request_id,
    }
    return provider_response(
        envelope_request,
        response,
        provider_reset_required=provider_reset_required,
    )


def _write_operator_exception(request, stage):
    diagnostic = {
        "kind": "linux_provider_diagnostic",
        "request_id": (
            request.get("request_id")
            if isinstance(request, dict)
            else "startup"
        ) or "startup",
        "operation": (
            request.get("operation")
            if isinstance(request, dict)
            else "unknown"
        ) or "unknown",
        "stage": stage,
        "diagnostic": traceback.format_exc(),
    }
    print(
        json.dumps(diagnostic, separators=(",", ":")),
        file=sys.stderr,
        flush=True,
    )


def _initialize_provider(request):
    global _provider_initialized
    if _provider_initialized:
        return
    try:
        require_desktop_session()
        Atspi.init()
    except Exception:
        _write_operator_exception(request, "startup")
        raise
    _provider_initialized = True


def execute(request):
    validate_request(request)
    operation = request["operation"]
    payload_key = {
        "observe_apps": "observe_apps",
        "observe": "observe",
        "resolve_and_act": "action",
        "desktop_execute_batch": "desktop",
        "window_list": "window_list",
        "window_action": "window_action",
    }[operation]
    try:
        response = perform_provider_request(operation, request[payload_key])
    except OperationFailure as exc:
        response = operation_failure_response(
            exc,
            recovery_operation=(
                request[payload_key]
                if operation in {"observe", "resolve_and_act", "window_action"}
                else None
            ),
        )
        return provider_response(
            request,
            response,
            provider_reset_required=exc.provider_reset_required,
        )
    except Exception as exc:
        _write_operator_exception(request, "dispatch")
        response = unexpected_operation_failure_response(
            exc,
            recovery_operation=(
                request[payload_key]
                if operation in {"resolve_and_act", "window_action"}
                else None
            ),
        )
        return provider_response(
            request,
            response,
            provider_reset_required=True,
        )
    return provider_response(request, response)


def _execute_serialized_request(raw):
    request = None
    try:
        request = json.loads(raw)
        validate_request(request)
    except ProviderRequestError as exc:
        _write_operator_exception(request, "precondition")
        return provider_boundary_failure(
            request,
            exc.code,
            str(exc),
            "precondition",
        )
    except (json.JSONDecodeError, TypeError, ValueError):
        _write_operator_exception(request, "precondition")
        return provider_boundary_failure(
            request,
            "invalid_provider_request",
            "invalid_provider_request",
            "precondition",
        )
    try:
        _initialize_provider(request)
    except Exception:
        return provider_boundary_failure(
            request,
            "runtime_startup_failed",
            "provider_startup_failed",
            "startup",
            provider_reset_required=True,
        )
    try:
        return execute(request)
    except Exception:
        _write_operator_exception(request, "dispatch")
        return provider_boundary_failure(
            request,
            "input_failed",
            "provider_dispatch_failed",
            "dispatch",
            provider_reset_required=True,
        )


def serve_stdio(stdin=None, stdout=None):
    stdin = stdin or sys.stdin
    stdout = stdout or sys.stdout
    for raw in stdin:
        if not raw.strip():
            continue
        response = _execute_serialized_request(raw)
        stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
        stdout.flush()


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "--stdio":
        serve_stdio()
        return
    if len(sys.argv) != 2:
        try:
            raise RuntimeError(
                "runtime.py requires a Provider V2 request path or --stdio"
            )
        except RuntimeError:
            request = None
            _write_operator_exception(request, "startup")
        response = provider_boundary_failure(
            request,
            "runtime_startup_failed",
            "provider_startup_failed",
            "startup",
        )
    else:
        try:
            with open(sys.argv[1], "r", encoding="utf-8") as file:
                raw = file.read()
        except OSError:
            request = None
            _write_operator_exception(request, "startup")
            response = provider_boundary_failure(
                request,
                "runtime_startup_failed",
                "provider_startup_failed",
                "startup",
            )
        else:
            response = _execute_serialized_request(raw)
    print(json.dumps(response, separators=(",", ":")))


if __name__ == "__main__":
    main()
