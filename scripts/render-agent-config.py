#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DISABLED_CODEX_SKILLS = (
    "imagegen",
    "openai-docs",
    "plugin-creator",
    "review-agent",
    "skill-creator",
    "skill-installer",
)
CLAUDE_BUILTINS = "Bash,Edit,Read"
CLAUDE_MCP_TOOLS = "mcp__computer_use__AX,mcp__computer_use__GUI"


def codex_config(args: argparse.Namespace) -> str:
    disabled = "\n\n".join(
        "[[skills.config]]\n"
        f'path = "{args.codex_home}/skills/.system/{name}/SKILL.md"\n'
        "enabled = false"
        for name in DISABLED_CODEX_SKILLS
    )
    return f'''model = {json.dumps(args.model)}
model_provider = "osworld_gateway"
model_reasoning_effort = {json.dumps(args.effort)}
approval_policy = "never"
sandbox_mode = "danger-full-access"
web_search = "disabled"
model_reasoning_summary = "detailed"
show_raw_agent_reasoning = true
model_instructions_file = {json.dumps(str(args.base_instructions))}
check_for_update_on_startup = false

[analytics]
enabled = false

[history]
persistence = "none"

[otel]
exporter = "none"
metrics_exporter = "none"
trace_exporter = "none"
log_user_prompt = false

[features]
shell_tool = true
unified_exec = true
view_image = true
plugins = false
plugin_sharing = false
remote_plugin = false
skill_search = false
skill_mcp_dependency_install = false
memories = false
goals = false
apps = false
hooks = false
multi_agent = false
browser_use = true
browser_use_external = true
browser_use_full_cdp_access = true
in_app_browser = true
workspace_dependencies = false
image_generation = false
computer_use = false
auth_elicitation = false
code_mode_host = false
fast_mode = false
in_app_chat = false
in_app_dictation = false
in_app_local_automation = false
in_app_updates = false
mcp_2026_07_28 = false
recommended_plugins = false
remote_compaction_v2 = false
tool_call_mcp_elicitation = false
tool_suggest = false
unbounded_connection_retries = false

[memories]
generate_memories = false
use_memories = false
disable_on_external_context = true

[tools.experimental_request_user_input]
enabled = false

[tools.update_plan]
enabled = false

{disabled}

[model_providers.osworld_gateway]
name = "task-scoped model gateway"
base_url = {json.dumps(args.gateway.rstrip("/") + "/t/" + args.task_id + "/v1")}
wire_api = "responses"

[mcp_servers.computer_use]
command = {json.dumps(str(args.mcp))}
args = ["mcp"]
required = true
startup_timeout_sec = 60
tool_timeout_sec = 180
'''


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent", choices=("codex", "claude-code"), required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--effort", default="xhigh")
    parser.add_argument("--task-id", default="manual")
    parser.add_argument("--gateway", required=True)
    parser.add_argument("--mcp", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument(
        "--base-instructions",
        type=Path,
        default=ROOT / "skills/base/CODEX_BASE_INSTRUCTIONS.md",
    )
    parser.add_argument(
        "--skill",
        type=Path,
        default=ROOT / "skills/hybrid/SKILL.md",
    )
    parser.add_argument("--codex-home", default="/opt/osw-runtime/codex-task")
    args = parser.parse_args()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    if args.agent == "codex":
        args.output.write_text(codex_config(args), encoding="utf-8")
    else:
        adapter = ROOT / "integrations/common/claude_mcp_schema_adapter.py"
        payload = {
            "mcpServers": {
                "computer_use": {
                    "type": "stdio",
                    "command": "/usr/bin/python3",
                    "args": [str(adapter), str(args.mcp), "mcp"],
                }
            }
        }
        args.output.write_text(
            json.dumps(payload, indent=2) + "\n",
            encoding="utf-8",
        )
        prompt = args.output.with_suffix(".system-prompt.md")
        prompt.write_text(
            args.base_instructions.read_text(encoding="utf-8")
            + "\n\n"
            + args.skill.read_text(encoding="utf-8"),
            encoding="utf-8",
        )
        print(
            "Claude allowed tools: "
            f"{CLAUDE_BUILTINS},{CLAUDE_MCP_TOOLS}"
        )
        print(f"Claude replacement system prompt: {prompt}")
    print(args.output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
