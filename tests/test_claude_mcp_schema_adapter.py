from integrations.common.claude_mcp_schema_adapter import adapt_input_schema


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
