"""Configuration and launcher for the guest-local task agents.

Codex reads a generated ``config.toml``; Claude Code receives CLI flags, an
MCP configuration and a schema adapter. Both run through one guest launcher
script that writes a turn-scoped completion record when the agent exits.
"""

from __future__ import annotations

import json
import shlex
import urllib.parse

from hcu.osworld.guest import (
    GUEST_BASE_INSTRUCTIONS,
    GUEST_CLAUDE_CODE,
    GUEST_CLAUDE_CONFIG_DIR,
    GUEST_CLAUDE_MCP_ADAPTER,
    GUEST_CLAUDE_MCP_CONFIG,
    GUEST_CODEX,
    GUEST_CODEX_HOME,
    GUEST_DONE,
    GUEST_FINAL,
    GUEST_LAUNCHER_STATUS,
    GUEST_MCP,
    GUEST_PROMPT,
    GUEST_STDERR,
    GUEST_TRACE,
)


CLAUDE_BUILTIN_TOOLS = "Bash,Edit,Read"


CODEX_BUNDLED_SYSTEM_SKILLS = (
    "imagegen",
    "openai-docs",
    "plugin-creator",
    "review-agent",
    "skill-creator",
    "skill-installer",
)


def codex_config(
    *,
    model: str,
    effort: str,
    gateway_base: str,
    affinity_id: str,
    codex_home: str = GUEST_CODEX_HOME,
    base_instructions: str = GUEST_BASE_INSTRUCTIONS,
    affinity_prefix: str = "osworld-v2-hybrid",
) -> str:
    endpoint = (
        gateway_base.rstrip("/")
        + "/t/"
        + urllib.parse.quote(f"{affinity_prefix}-{affinity_id}", safe="")
        + "/v1"
    )
    disabled_system_skills = "\n\n".join(
        (
            "[[skills.config]]\n"
            f'path = "{codex_home}/skills/.system/{name}/SKILL.md"\n'
            "enabled = false"
        )
        for name in CODEX_BUNDLED_SYSTEM_SKILLS
    )
    return f"""\
model = {json.dumps(model)}
model_provider = "osworld_v2_gateway"
model_reasoning_effort = {json.dumps(effort)}
approval_policy = "never"
sandbox_mode = "danger-full-access"
web_search = "disabled"
model_reasoning_summary = "detailed"
show_raw_agent_reasoning = true
model_instructions_file = {json.dumps(base_instructions)}
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

{disabled_system_skills}

[model_providers.osworld_v2_gateway]
name = "OSWorld V2 task-scoped Azure gateway"
base_url = {json.dumps(endpoint)}
wire_api = "responses"

[mcp_servers.computer_use]
command = {json.dumps(GUEST_MCP)}
args = ["mcp"]
required = true
startup_timeout_sec = 60
tool_timeout_sec = 180
"""


def claude_mcp_config() -> str:
    return json.dumps(
        {
            "mcpServers": {
                "computer_use": {
                    "type": "stdio",
                    "command": "/usr/bin/python3",
                    "args": [
                        GUEST_CLAUDE_MCP_ADAPTER,
                        GUEST_MCP,
                        "mcp",
                    ],
                }
            }
        },
        indent=2,
    ) + "\n"


def claude_gateway_endpoint(
    gateway_base: str,
    affinity_id: str,
    *,
    affinity_prefix: str = "osworld-v2-hybrid",
) -> str:
    return (
        gateway_base.rstrip("/")
        + "/t/"
        + urllib.parse.quote(f"{affinity_prefix}-{affinity_id}", safe="")
    )


def claude_mcp_tools(public_tools=None) -> str:
    names = public_tools or ("AX", "GUI")
    qualified = []
    for name in names:
        name = str(name or "").strip()
        if not name or "," in name:
            raise ValueError(f"invalid MCP tool name: {name!r}")
        if name.startswith("mcp__computer_use__"):
            qualified.append(name)
        else:
            qualified.append(f"mcp__computer_use__{name}")
    if not qualified:
        raise ValueError("Claude Code requires at least one MCP tool")
    return ",".join(qualified)


def launcher_script(
    *,
    agent: str = "codex",
    model: str = "",
    effort: str = "xhigh",
    claude_gateway_base: str = "",
    public_mcp_tools=None,
) -> str:
    if agent == "claude-code":
        mcp_tools = claude_mcp_tools(public_mcp_tools)
        allowed_tools = f"{CLAUDE_BUILTIN_TOOLS},{mcp_tools}"
        common = (
            f"{shlex.quote(GUEST_CLAUDE_CODE)} -p "
            "--bare "
            f"--model {shlex.quote(model)} "
            f"--effort {shlex.quote(effort)} "
            "--permission-mode bypassPermissions "
            f"--mcp-config {shlex.quote(GUEST_CLAUDE_MCP_CONFIG)} "
            "--strict-mcp-config "
            "--setting-sources '' "
            f"--system-prompt-file {shlex.quote(GUEST_BASE_INSTRUCTIONS)} "
            f"--tools {shlex.quote(CLAUDE_BUILTIN_TOOLS)} "
            f"--allowedTools {shlex.quote(allowed_tools)} "
            "--disable-slash-commands "
            "--no-chrome "
            "--prompt-suggestions false "
            "--output-format stream-json --verbose"
        )
        start_command = (
            f"{common} < {shlex.quote(GUEST_PROMPT)} "
            f"> {shlex.quote(GUEST_TRACE)} "
            f"2> {shlex.quote(GUEST_STDERR)}"
        )
        resume_command = (
            f"{common} --resume \"$session_id\" "
            f"< {shlex.quote(GUEST_PROMPT)} "
            f"> {shlex.quote(GUEST_TRACE)} "
            f"2> {shlex.quote(GUEST_STDERR)}"
        )
        finalize_command = f"""\
python3 - {shlex.quote(GUEST_TRACE)} {shlex.quote(GUEST_FINAL)} <<'PY'
import json
import sys

last = ""
with open(sys.argv[1], encoding="utf-8", errors="replace") as handle:
    for line in handle:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "result" and isinstance(event.get("result"), str):
            last = event["result"]
        elif event.get("type") == "assistant":
            for block in (event.get("message") or {{}}).get("content", []) or []:
                if block.get("type") == "text" and str(block.get("text") or "").strip():
                    last = str(block["text"])
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write(last)
PY
"""
        agent_environment = f"""\
export CLAUDE_CONFIG_DIR={shlex.quote(GUEST_CLAUDE_CONFIG_DIR)}
export ANTHROPIC_BASE_URL={shlex.quote(claude_gateway_base)}
export ANTHROPIC_AUTH_TOKEN=osw-hybrid-task-proxy
export CLAUDE_CODE_SKIP_AUTH_LOGIN=true
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
export CLAUDE_CODE_DISABLE_AUTO_MEMORY=1
export CLAUDE_CODE_ATTRIBUTION_HEADER=0
export DISABLE_TELEMETRY=1
export DISABLE_ERROR_REPORTING=1
export DISABLE_AUTOUPDATER=1
export DISABLE_NON_ESSENTIAL_MODEL_CALLS=1
export CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY=1
export DISABLE_COST_WARNINGS=1
"""
    elif agent == "codex":
        start_command = (
            f"{shlex.quote(GUEST_CODEX)} exec "
            "--strict-config --json --color never --skip-git-repo-check "
            f"--output-last-message {shlex.quote(GUEST_FINAL)} "
            f"- < {shlex.quote(GUEST_PROMPT)} "
            f"> {shlex.quote(GUEST_TRACE)} "
            f"2> {shlex.quote(GUEST_STDERR)}"
        )
        resume_command = (
            f"{shlex.quote(GUEST_CODEX)} exec resume "
            "--strict-config --json --skip-git-repo-check "
            f"--output-last-message {shlex.quote(GUEST_FINAL)} "
            f"\"$session_id\" - < {shlex.quote(GUEST_PROMPT)} "
            f"> {shlex.quote(GUEST_TRACE)} "
            f"2> {shlex.quote(GUEST_STDERR)}"
        )
        finalize_command = ""
        agent_environment = f"""\
export CODEX_HOME={shlex.quote(GUEST_CODEX_HOME)}
export CODEX_SQLITE_HOME={shlex.quote(GUEST_CODEX_HOME)}
"""
    else:
        raise ValueError(f"Unsupported task agent: {agent}")
    return f"""\
#!/bin/bash
set +e
export HOME=/home/user
{agent_environment}
export PATH=/home/user/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
cd /home/user
rm -f {shlex.quote(GUEST_TRACE)} {shlex.quote(GUEST_STDERR)} \
  {shlex.quote(GUEST_FINAL)} {shlex.quote(GUEST_DONE)} \
  {shlex.quote(GUEST_LAUNCHER_STATUS)}
mode="${{1:-start}}"
session_id="${{2:-}}"
turn_token="${{3:-unknown}}"
write_status() {{
  python3 - "$1" "$turn_token" {shlex.quote(GUEST_LAUNCHER_STATUS)} <<'PY'
import json
import os
import sys
import time
path = sys.argv[3]
tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as handle:
    json.dump({{
        "state": sys.argv[1],
        "turn_token": sys.argv[2],
        "pid": os.getppid(),
        "timestamp": time.time(),
    }}, handle)
    handle.flush()
    os.fsync(handle.fileno())
os.replace(tmp, path)
PY
}}
write_done() {{
  python3 - "$1" "$turn_token" {shlex.quote(GUEST_DONE)} <<'PY'
import json
import os
import sys
path = sys.argv[3]
tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as handle:
    json.dump({{
        "returncode": int(sys.argv[1]),
        "turn_token": sys.argv[2],
    }}, handle)
    handle.flush()
    os.fsync(handle.fileno())
os.replace(tmp, path)
PY
}}
launcher_exit() {{
  rc=$?
  trap - EXIT
  write_status "exited:$rc" || true
  write_done "$rc" || true
  exit "$rc"
}}
trap launcher_exit EXIT
write_status "started"
if [ "$mode" = "resume" ]; then
  {resume_command}
else
  {start_command}
fi
rc=$?
{finalize_command}
exit "$rc"
"""


def thread_id_from_trace(raw: bytes | None, agent: str = "codex") -> str:
    if not raw:
        return ""
    for line in raw.decode("utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if agent == "codex" and event.get("type") == "thread.started":
            return str(event.get("thread_id") or "")
        if agent == "claude-code" and event.get("session_id"):
            return str(event["session_id"])
    return ""
