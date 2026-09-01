"""Strict JSONL Provider V2 boundary shared by macOS and Windows."""

from __future__ import annotations

import json
import sys
import traceback
from typing import Any


PROTOCOL = "osw-provider-v2"
SCHEMA_VERSION = 2
OPERATIONS = {
    "observe_apps": "observe_apps",
    "observe": "observe",
    "resolve_and_act": "action",
    "desktop_execute_batch": "desktop",
    "window_list": "window_list",
    "window_action": "window_action",
}


class NativeProviderError(RuntimeError):
    def __init__(
        self,
        message: str,
        *,
        code: str = "native_provider_failed",
        phase: str = "native_api",
        emitted: bool = False,
        facts_complete: bool = True,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.phase = phase
        self.emitted = emitted
        self.facts_complete = facts_complete


def _backend():
    if sys.platform == "darwin":
        from native.macos.provider_v1.backend import MacOSBackend

        return MacOSBackend()
    if sys.platform == "win32":
        from native.windows.provider_v1.backend import WindowsBackend

        return WindowsBackend()
    raise NativeProviderError(
        "portable provider started on an unsupported platform",
        code="unsupported_platform",
        phase="initialize",
    )


def _validate_request(request: Any) -> tuple[str, dict[str, Any]]:
    if not isinstance(request, dict):
        raise NativeProviderError("provider request must be an object", code="invalid_provider_request")
    if request.get("protocol") != PROTOCOL or request.get("schema_version") != SCHEMA_VERSION:
        raise NativeProviderError("provider protocol or schema version mismatch", code="invalid_provider_request")
    request_id = request.get("request_id")
    operation = request.get("operation")
    if not isinstance(request_id, str) or not request_id:
        raise NativeProviderError("provider request_id is required", code="invalid_provider_request")
    payload_name = OPERATIONS.get(operation)
    if payload_name is None:
        raise NativeProviderError("unsupported provider operation", code="invalid_provider_request")
    active = [name for name in OPERATIONS.values() if request.get(name) is not None]
    if active != [payload_name]:
        raise NativeProviderError("provider operation must select exactly one payload", code="invalid_provider_request")
    payload = request[payload_name]
    if not isinstance(payload, dict) or payload.get("protocol_version") != SCHEMA_VERSION:
        raise NativeProviderError("provider payload protocol version mismatch", code="invalid_provider_request")
    return operation, payload


def _failure_result(error: BaseException, *, mutable: bool) -> dict[str, Any]:
    if isinstance(error, NativeProviderError):
        emitted = bool(error.emitted)
        code = error.code
        phase = error.phase
        facts_complete = error.facts_complete
        message = str(error)
    else:
        emitted = False
        code = "native_provider_failed"
        phase = "native_api"
        facts_complete = False
        message = "native provider failed before input"
    if emitted:
        underlying = code
        code = "effect_unknown"
        phase = "post_action_observation"
        facts_complete = False
        message = "input was emitted; the final desktop effect is unknown"
    result: dict[str, Any] = {
        "ok": False,
        "error": message,
        "failure": {
            "code": code,
            "phase": phase,
            "facts_complete": bool(facts_complete),
        },
    }
    if emitted:
        result["failure"]["underlying_code"] = underlying
    if mutable or emitted:
        result["mutation"] = {
            "input_events_emitted": emitted,
            "partial_input_possible": emitted,
            "mutation_unknown": emitted,
        }
    return result


def _envelope(request: dict[str, Any], payload: dict[str, Any]) -> dict[str, Any]:
    operation = request["operation"]
    response: dict[str, Any] = {
        "protocol": PROTOCOL,
        "schema_version": SCHEMA_VERSION,
        "request_id": request["request_id"],
        "operation": operation,
    }
    if operation in {"observe_apps", "observe", "resolve_and_act"}:
        response["accessibility"] = payload
    elif operation == "desktop_execute_batch":
        response["desktop"] = payload
    else:
        response["window"] = payload
    return response


def _dispatch(backend: Any, operation: str, payload: dict[str, Any]) -> dict[str, Any]:
    if operation == "observe_apps":
        return {"ok": True, "apps": backend.observe_apps()}
    if operation == "observe":
        return backend.observe(payload)
    if operation == "resolve_and_act":
        return backend.resolve_and_act(payload)
    if operation == "desktop_execute_batch":
        return backend.desktop_execute_batch(payload)
    if operation == "window_list":
        return backend.window_list(payload)
    if operation == "window_action":
        return backend.window_action(payload)
    raise NativeProviderError("unsupported provider operation", code="invalid_provider_request")


def serve() -> None:
    backend = _backend()
    for raw in sys.stdin:
        raw = raw.strip()
        if not raw:
            continue
        request: dict[str, Any] = {}
        try:
            decoded = json.loads(raw)
            if isinstance(decoded, dict):
                request = decoded
            operation, payload = _validate_request(decoded)
            result = _dispatch(backend, operation, payload)
        except BaseException as error:  # provider must preserve JSONL framing
            operation = request.get("operation", "observe_apps")
            mutable = operation in {"resolve_and_act", "desktop_execute_batch", "window_action"}
            result = _failure_result(error, mutable=mutable)
            print(
                json.dumps(
                    {
                        "kind": "native_provider_diagnostic",
                        "request_id": request.get("request_id", "unknown"),
                        "operation": operation,
                        "stage": "dispatch",
                        "diagnostic": traceback.format_exc(limit=8),
                    },
                    separators=(",", ":"),
                ),
                file=sys.stderr,
                flush=True,
            )
        if not request.get("request_id"):
            # A malformed envelope cannot be correlated by the Go caller; the
            # worker is intentionally terminated so the transport resets.
            return
        print(json.dumps(_envelope(request, result), separators=(",", ":")), flush=True)


def main() -> None:
    if "--stdio" not in sys.argv:
        raise SystemExit("portable provider requires --stdio")
    serve()
