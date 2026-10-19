"""Programmatic tool calling (PTC) adapter for the Anthropic API.

Two pieces, both thin:

1. tool_definitions(schema) turns the daemon's published schemas into the
   client tools hcu_ax and hcu_gui. Nothing is generated ahead of time: the
   schema is read from the running daemon (Desktop.schema()), so the PTC
   definitions cannot drift from MCP. Local $ref definitions are inlined,
   because client tool input schemas must be self-contained.

2. forward(tool_use, desktop) is the harness side: it takes one tool_use
   block (from code execution, with a caller field, or direct), sends the
   call to the guest daemon and returns the records JSON string that
   becomes the tool_result content.

allowed_callers is not a security boundary (Anthropic documents this):
code that runs in the container can call any tool its caller list allows,
and all safety checks (argument validation, ref resolution, window binding,
preflight before input) stay in the computer-use core. hcu_ax can be
called from code; hcu_gui is kept direct by default so screenshots reach
the model as images, unless the harness opts in.
"""

import copy
import json

CODE_EXECUTION_CALLER = "code_execution_20260120"

# Call options accepted next to the tool arguments in a tool_use input.
_OPTIONS = {
    "request_id": {"type": "string", "maxLength": 128,
                   "description": "Idempotency key for act: a retry with the same id returns the first result."},
    "since": {"type": "string", "description": "observe: snapshot_id of an earlier observe; returns a diff."},
}

_READ_MODE = "read"


def _inline_refs(schema):
    """Replace local {"$ref": "#/$defs/x"} with the definition."""
    definitions = schema.get("$defs", {})

    def walk(node):
        if isinstance(node, dict):
            ref = node.get("$ref")
            if isinstance(ref, str) and ref.startswith("#/$defs/"):
                return walk(copy.deepcopy(definitions[ref[len("#/$defs/"):]]))
            return {key: walk(value) for key, value in node.items() if key != "$defs"}
        if isinstance(node, list):
            return [walk(item) for item in node]
        return node

    return walk(schema)


def _input_schema(tool, records):
    schema = _inline_refs(tool["input_schema"])
    properties = schema.setdefault("properties", {})
    if tool["name"] == "AX":
        # read is a records-only mode; it has no MCP form.
        mode = dict(properties["mode"])
        mode["enum"] = list(mode["enum"]) + [_READ_MODE]
        mode["description"] = mode.get("description", "") + " read returns the live value and states of refs."
        properties["mode"] = mode
        properties["refs"] = {
            "type": "array", "minItems": 1, "items": {"type": "string", "pattern": "^r[1-9][0-9]*$"},
            "description": "read: element refs rN.",
        }
    for name, option in _OPTIONS.items():
        properties[name] = option
    if "oneOf" in schema:
        # GUI: one object schema per branch; the options are valid on both.
        for branch in schema["oneOf"]:
            branch.setdefault("properties", {}).update(_OPTIONS)
    return schema


_DESCRIPTIONS = {
    "AX": "Desktop accessibility (HCU AX). Same arguments as the AX MCP tool, plus mode read, "
          "request_id and since. Returns a JSON string: {ok, code, message, hint, op, window, "
          "elements|matches|read|diff, page{returned,total,next_cursor}, input_emitted, "
          "completed, requested, timing_ms}. Never truncated; page with next_cursor.",
    "GUI": "Full-desktop screenshot and pixel input (HCU GUI). Same arguments as the GUI MCP tool, "
           "plus request_id. Returns a JSON string with screen{width,height,image} where image is "
           "a file path in the guest.",
}


def tool_definitions(schema, gui_from_code=False):
    """Client tool definitions for the Messages API, from Desktop.schema()."""
    tools = []
    for tool in schema["tools"]:
        name = "hcu_" + tool["name"].lower()
        definition = {
            "name": name,
            "description": _DESCRIPTIONS[tool["name"]],
            "input_schema": _input_schema(tool, schema.get("records", {})),
        }
        if tool["name"] == "AX" or gui_from_code:
            definition["allowed_callers"] = [CODE_EXECUTION_CALLER]
        tools.append(definition)
    return tools


def forward(tool_use, desktop):
    """Harness side: run one tool_use block against the guest daemon.

    tool_use is the block from the API response ({"type":"tool_use","id",
    "name","input","caller"?}). Returns the tool_result block to send back.
    Errors are results too: ok:false with code and hint, so code in the
    container can branch on them.
    """
    name = tool_use["name"]
    if name not in ("hcu_ax", "hcu_gui"):
        raise ValueError("not an hcu tool: %r" % name)
    arguments = dict(tool_use.get("input") or {})
    request_id = arguments.pop("request_id", None)
    since = arguments.pop("since", None)
    params = {"tool": "AX" if name == "hcu_ax" else "GUI", "args": arguments, "format": "records"}
    if request_id:
        params["request_id"] = request_id
    if since:
        params["since"] = since
    record = desktop.transport("call", params)
    return {
        "type": "tool_result",
        "tool_use_id": tool_use["id"],
        "content": json.dumps(record, ensure_ascii=False),
        "is_error": False,
    }
