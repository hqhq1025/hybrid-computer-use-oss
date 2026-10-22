import importlib.util
import json
from pathlib import Path

# The adapter is uploaded into the guest as a standalone script.
ADAPTER_PATH = (
    Path(__file__).resolve().parents[1]
    / "hcu/osworld/guest_files/claude_mcp_schema_adapter.py"
)
SPEC = importlib.util.spec_from_file_location("claude_adapter", ADAPTER_PATH)
assert SPEC is not None and SPEC.loader is not None
adapter = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(adapter)
adapt_input_schema = adapter.adapt_input_schema


def test_flattens_root_oneof_but_keeps_mode_values():
    schema = {
        "oneOf": [
            {
                "type": "object",
                "properties": {
                    "mode": {"const": "observe"},
                    "app": {"type": "string"},
                },
                "required": ["mode"],
            },
            {
                "type": "object",
                "properties": {
                    "mode": {"const": "act"},
                    "actions": {"type": "array"},
                },
                "required": ["mode"],
            },
        ]
    }
    adapted = adapt_input_schema(schema)
    assert "oneOf" not in adapted
    assert adapted["properties"]["mode"]["enum"] == ["observe", "act"]
    assert adapted["required"] == ["mode"]


def test_shared_root_constraints_survive_adapter_without_self_references():
    contract = json.loads(
        (ADAPTER_PATH.parents[3] / "computer-use/testdata/contract/public_contract.json").read_text()
    )
    # Use the real tools/list contract so additions to AX fields cannot be
    # silently replaced with unconstrained objects by the adapter.
    tools = contract["tools_list"]["result"]["tools"]
    ax = next(tool["inputSchema"] for tool in tools if tool["name"] == "AX")
    adapted = adapt_input_schema(ax)
    assert "oneOf" not in adapted
    assert adapted["required"] == ["mode"]
    for field in ("app", "window_ref", "actions", "query", "scope", "observe", "limit", "cursor", "root"):
        assert adapted["properties"][field] == ax["properties"][field]
    serialized = json.dumps(adapted)
    assert "#/properties/" not in serialized
    assert adapted["$defs"] == ax["$defs"]
    assert adapted["properties"]["actions"]["items"]["properties"]["type"]["enum"]
