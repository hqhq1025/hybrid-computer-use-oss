#!/usr/bin/env python3
"""Offline conversion of historical runtime calls to Provider V2 envelopes.

This utility is intentionally outside the runtime package. Production Linux,
Windows, and macOS backends must never import it.
"""

import argparse
import json
import sys


PROTOCOL = "osw-provider-v2"
SCHEMA_VERSION = 2


def _observation(operation):
    fields = {
        "text_limit": operation.get("text_limit"),
        "max_tree_nodes": operation.get("max_tree_nodes"),
        "max_tree_depth": operation.get("max_tree_depth"),
        "prune": operation.get("prune"),
        "boxes": operation.get("boxes"),
        "include_screenshot": operation.get("includeScreenshot"),
        "screenshot_scope": operation.get("screenshotScope"),
    }
    return {key: value for key, value in fields.items() if value is not None}


def _target(operation):
    target = operation.get("semanticTarget")
    if not isinstance(target, dict):
        return None
    result = {
        "selector": target.get("selector", ""),
        "hasName": bool(target.get("hasName")),
    }
    aliases = {
        "locatorKind": "locatorKind",
        "windowHandle": "windowHandle",
        "providerBusName": "providerBusName",
        "providerObjectPath": "providerObjectPath",
        "processGeneration": "processGeneration",
        "providerWindowBusName": "providerWindowBusName",
        "providerWindowObjectPath": "providerWindowObjectPath",
        "providerWindowProcessGeneration": "providerWindowProcessGeneration",
        "providerWindowRole": "providerWindowRole",
        "providerWindowName": "providerWindowName",
        "providerRoleAliases": "providerRoleAliases",
        "providerIdentityScope": "providerIdentityScope",
        "runtimeId": "runtimeId",
        "runtimeAliases": "runtimeAliases",
        "role": "role",
        "name": "name",
        "predicates": "predicates",
    }
    for source, destination in aliases.items():
        value = target.get(source)
        if value not in (None, "", []):
            result[destination] = value
    return result


def convert_operation(operation, request_id):
    tool = operation.get("tool")
    envelope = {
        "protocol": PROTOCOL,
        "schema_version": SCHEMA_VERSION,
        "request_id": request_id,
    }
    if tool == "list_apps":
        envelope.update({
            "operation": "observe_apps",
            "observe_apps": {"protocol_version": SCHEMA_VERSION},
        })
        return envelope
    if tool == "get_app_state":
        envelope.update({
            "operation": "observe",
            "observe": {
                "protocol_version": SCHEMA_VERSION,
                "app": operation.get("app", ""),
                "window_handle": operation.get("targetWindowHandle", ""),
                "observation": _observation(operation),
            },
        })
        return envelope
    if tool == "gui":
        envelope.update({
            "operation": "desktop_execute_batch",
            "desktop": {
                "protocol_version": SCHEMA_VERSION,
                "actions": operation.get("actions") or [],
                "settle_ms": operation.get("settle_ms", 800),
                "frame_id": operation.get("frame_id", ""),
                "lease_state": operation.get("guiLeaseState", ""),
            },
        })
        return envelope
    if tool == "list_app_windows":
        envelope.update({
            "operation": "window_list",
            "window_list": {
                "protocol_version": SCHEMA_VERSION,
                "app": operation.get("app", ""),
                "window_handle": operation.get("targetWindowHandle", ""),
            },
        })
        return envelope
    if tool in {"activate", "close"}:
        envelope.update({
            "operation": "window_action",
            "window_action": {
                "protocol_version": SCHEMA_VERSION,
                "action": tool,
                "app": operation.get("app", ""),
                "window_handle": operation.get("targetWindowHandle", ""),
                "target": _target(operation),
                "observation": _observation(operation),
            },
        })
        return envelope

    action_input = {
        "x": operation.get("x"),
        "y": operation.get("y"),
        "click_count": operation.get("click_count"),
        "mouse_button": operation.get("mouse_button"),
        "click_method": operation.get("click_method"),
        "action": operation.get("action"),
        "direction": operation.get("direction"),
        "pages": operation.get("pages"),
        "text": operation.get("text"),
        "key": operation.get("key"),
        "value": operation.get("value"),
        "window_centre": operation.get("windowCentre"),
    }
    envelope.update({
        "operation": "resolve_and_act",
        "action": {
            "protocol_version": SCHEMA_VERSION,
            "intent": tool,
            "app": operation.get("app", ""),
            "window_handle": operation.get("targetWindowHandle", ""),
            "element": operation.get("element"),
            "target": _target(operation),
            "input": {
                key: value for key, value in action_input.items()
                if value not in (None, "")
            },
            "window_bounds": operation.get("windowBounds"),
            "observation": _observation(operation),
        },
    })
    return envelope


def convert_record(record, sequence):
    if isinstance(record, dict) and "tool" in record:
        return convert_operation(record, "legacy-{}".format(sequence))
    if isinstance(record, dict) and isinstance(record.get("request"), dict):
        converted = dict(record)
        converted["request"] = convert_operation(
            record["request"],
            str(record.get("request_id") or "legacy-{}".format(sequence)),
        )
        return converted
    raise ValueError("record does not contain a historical runtime request")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("input", nargs="?", help="JSONL input; stdin by default")
    args = parser.parse_args()
    stream = open(args.input, encoding="utf-8") if args.input else sys.stdin
    try:
        for sequence, line in enumerate(stream, 1):
            if not line.strip():
                continue
            converted = convert_record(json.loads(line), sequence)
            print(json.dumps(converted, separators=(",", ":"), ensure_ascii=False))
    finally:
        if args.input:
            stream.close()


if __name__ == "__main__":
    main()
