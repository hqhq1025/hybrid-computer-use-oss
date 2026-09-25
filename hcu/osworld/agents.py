"""Configuration and launcher for the guest-local task agents.

Codex reads a generated ``config.toml``; Claude Code receives CLI flags, an
MCP configuration and a schema adapter; Gemini CLI reads a ``settings.json``
from an isolated home; Grok Build reads a ``config.toml`` from an isolated
``GROK_HOME``. All four run through one guest launcher script that writes a
turn-scoped completion record when the agent exits.
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
    GUEST_GEMINI_CLI,
    GUEST_GEMINI_EXPERIMENTS,
    GUEST_GEMINI_HOME,
    GUEST_GEMINI_NODE,
    GUEST_GEMINI_TRUSTED,
    GUEST_GROK,
    GUEST_GROK_HOME,
    GUEST_LAUNCHER_STATUS,
    GUEST_MCP,
    GUEST_PROMPT,
    GUEST_STDERR,
    GUEST_TRACE,
)


# Per-agent facts the runner needs: the file under artifacts/, the gateway
# that serves the agent ("openai" is the Responses API), the process name the
# watchdog looks for, the defaults for --model and --effort (Gemini CLI takes
# a thinking level), and whether a turn can resume the agent's session.
# Grok Build 1.0.40 drops --system-prompt-override on --resume and falls back
# to its default prompt, so it cannot continue a task under the same
# instructions.
AGENT_PROFILES = {
    "codex": {
        "artifact": "codex",
        "gateway": "openai",
        "process": "codex",
        "default_model": "gpt-5.5",
        "default_effort": "xhigh",
        "can_resume": True,
    },
    "claude-code": {
        "artifact": "claude-code",
        "gateway": "anthropic",
        "process": "claude",
        "default_model": "claude-opus-5",
        "default_effort": "xhigh",
        "can_resume": True,
    },
    "gemini-cli": {
        "artifact": "gemini-cli.tgz",
        "gateway": "gemini",
        "process": "gemini-node",
        "default_model": "gemini-3.8-flash",
        "default_effort": "high",
        "can_resume": True,
    },
    "grok-build": {
        "artifact": "grok",
        "gateway": "openai",
        "process": "grok",
        "default_model": "grok-4.7",
        "default_effort": "xhigh",
        "can_resume": False,
    },
}
AGENTS = tuple(AGENT_PROFILES)


CLAUDE_BUILTIN_TOOLS = "Bash,Edit,Read"


CODEX_BUNDLED_SYSTEM_SKILLS = (
    "imagegen",
    "openai-docs",
    "plugin-creator",
    "review-agent",
    "skill-creator",
    "skill-installer",
)


CODEX_PROVIDER_ID = "osworld_gateway"
CODEX_PROVIDER_NAME = "OSWorld task-scoped model gateway"


def task_gateway_base(
    gateway_base: str,
    affinity_id: str,
    *,
    affinity_prefix: str,
) -> str:
    """Gateway URL whose ``/t/<id>`` path carries the task affinity ID."""
    return (
        gateway_base.rstrip("/")
        + "/t/"
        + urllib.parse.quote(f"{affinity_prefix}-{affinity_id}", safe="")
    )


# Ablation configurations expose one desktop channel. The MCP server reads
# COMPUTER_USE_CHANNELS and lists only that channel's tool, so the agent never
# sees the other one. The values are the server's channel names.
CHANNEL_TOOLS = {
    "": ("AX", "GUI"),
    "a11y,keyboard": ("AX",),
    "gui,keyboard": ("GUI",),
}


def public_tools(channels: str) -> tuple[str, ...]:
    if channels not in CHANNEL_TOOLS:
        raise ValueError(
            "channels must be empty, a11y,keyboard or gui,keyboard, "
            f"got {channels!r}"
        )
    return CHANNEL_TOOLS[channels]


def codex_config(
    *,
    model: str,
    effort: str,
    base_url: str,
    codex_home: str,
    base_instructions: str,
    mcp_command: str,
    channels: str = "",
) -> str:
    """Codex ``config.toml`` with ambient features off and the MCP required."""
    public_tools(channels)
    mcp_env = (
        f'\n[mcp_servers.computer_use.env]\nCOMPUTER_USE_CHANNELS = {json.dumps(channels)}\n'
        if channels
        else ""
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
model_provider = "{CODEX_PROVIDER_ID}"
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

[model_providers.{CODEX_PROVIDER_ID}]
name = {json.dumps(CODEX_PROVIDER_NAME)}
base_url = {json.dumps(base_url)}
wire_api = "responses"

[mcp_servers.computer_use]
command = {json.dumps(mcp_command)}
args = ["mcp"]
required = true
startup_timeout_sec = 60
tool_timeout_sec = 180
{mcp_env}"""


def claude_mcp_config(
    *,
    adapter: str = GUEST_CLAUDE_MCP_ADAPTER,
    mcp_command: str = GUEST_MCP,
    channels: str = "",
) -> str:
    """Claude Code MCP configuration that runs the MCP behind the adapter."""
    public_tools(channels)
    server: dict = {
        "type": "stdio",
        "command": "/usr/bin/python3",
        "args": [adapter, mcp_command, "mcp"],
    }
    if channels:
        server["env"] = {"COMPUTER_USE_CHANNELS": channels}
    return json.dumps({"mcpServers": {"computer_use": server}}, indent=2) + "\n"


def claude_system_prompt(base_instructions: str, skill: str) -> str:
    """Claude Code gets the skill in its replacement system prompt.

    A ``CLAUDE.md`` file would also load ambient configuration, so the skill
    is appended to the base instructions instead.
    """
    return base_instructions + "\n\n# Benchmark computer-use skill\n\n" + skill


def grok_system_prompt(base_instructions: str, skill: str) -> str:
    """Grok Build ignores its rules files under a system-prompt override, so
    the skill follows the base instructions in the override itself."""
    return base_instructions + "\n\n" + skill


# Gemini CLI tools that talk to the user or the web, or add planning and
# memory layers outside the benchmark contract.
GEMINI_EXCLUDED_TOOLS = (
    "google_web_search", "web_fetch", "save_memory", "write_todos", "ask_user",
    "enter_plan_mode", "exit_plan_mode", "invoke_agent", "activate_skill",
    "update_topic",
)
GEMINI_THINKING_LEVELS = ("minimal", "low", "medium", "high")
# Gemini CLI waits 60 s for response headers by default. The gateway relays a
# streamed response only after it completes, so long generations need more.
GEMINI_REQUEST_TIMEOUT_SEC = 600
GEMINI_EXPERIMENT_REQUEST_TIMEOUT = 45773134


def gemini_experiments() -> str:
    """Local experiment flags for ``GEMINI_EXP``; only the request timeout."""
    return json.dumps({
        "flags": [{
            "flagId": GEMINI_EXPERIMENT_REQUEST_TIMEOUT,
            "intValue": str(GEMINI_REQUEST_TIMEOUT_SEC),
        }],
        "experimentIds": [],
    }, indent=2) + "\n"


def gemini_settings(
    *,
    model: str,
    effort: str,
    mcp_command: str = GUEST_MCP,
    channels: str = "",
) -> str:
    """Gemini CLI ``settings.json`` with ambient features off and the MCP allowed.

    ``dynamicModelConfiguration`` is required: without it CLI 0.60.0 rewrites
    the selected model. Only the global ``GEMINI.md`` next to this file is
    loaded as context, and it carries the skill; directory discovery is off.
    ``effort`` is a Gemini thinking level, or ``native`` for the model default.
    """
    public_tools(channels)
    if effort != "native" and effort not in GEMINI_THINKING_LEVELS:
        raise ValueError(
            "Gemini CLI effort must be native or a thinking level ("
            + ", ".join(GEMINI_THINKING_LEVELS) + f"), got {effort!r}"
        )
    server: dict = {
        "command": mcp_command,
        "args": ["mcp"],
        "timeout": 180000,
        "trust": False,
    }
    if channels:
        server["env"] = {"COMPUTER_USE_CHANNELS": channels}
    settings = {
        # The gateway enforces the model-call budget; the CLI has no turn cap.
        "model": {"name": model, "maxSessionTurns": -1},
        "experimental": {
            "dynamicModelConfiguration": True,
            "enableAgents": False,
            "autoMemory": False,
            "taskTracker": False,
        },
        "general": {
            "enableAutoUpdate": False,
            "enableAutoUpdateNotification": False,
            "topicUpdateNarration": False,
            "plan": {"enabled": False, "modelRouting": False},
        },
        "skills": {"enabled": False},
        "hooksConfig": {"enabled": False},
        "admin": {"extensions": {"enabled": False}, "skills": {"enabled": False}},
        "tools": {"exclude": list(GEMINI_EXCLUDED_TOOLS)},
        "mcpServers": {"computer_use": server},
        "mcp": {"allowed": ["computer_use"]},
        "security": {
            "auth": {"selectedType": "gemini-api-key", "enforcedType": "gemini-api-key"},
            "environmentVariableRedaction": {"allowed": ["COMPUTER_USE_CHANNELS"]},
        },
        "context": {
            "fileName": "GEMINI.md",
            "discoveryMaxDirs": 0,
            "memoryBoundaryMarkers": [],
            "loadMemoryFromIncludeDirectories": False,
            "includeDirectoryTree": False,
        },
        "advanced": {"ignoreLocalEnv": True, "autoConfigureMemory": False},
        "privacy": {"usageStatisticsEnabled": False},
        "telemetry": {"enabled": False},
        # Compression and loop detection otherwise fall back to older models.
        "modelConfigs": {"customAliases": {
            alias: {"modelConfig": {"model": model}}
            for alias in ("chat-compression-default", "loop-detection",
                          "loop-detection-double-check")
        }},
    }
    if effort != "native":
        settings["modelConfigs"]["customOverrides"] = [{
            "match": {"model": model},
            "modelConfig": {"generateContentConfig": {
                "thinkingConfig": {"thinkingLevel": effort.upper()},
            }},
        }]
    return json.dumps(settings, indent=2) + "\n"


GROK_DISALLOWED_TOOLS = (
    "todo_write,enter_plan_mode,exit_plan_mode,skill,ask_user_question,image_edit,"
    "workflow,scheduler_create,scheduler_delete,scheduler_list,monitor,send_feedback"
)


def grok_config(
    *,
    model: str,
    base_url: str,
    effort: str,
    mcp_command: str = GUEST_MCP,
    channels: str = "",
) -> str:
    """Grok Build ``config.toml`` bound to the task gateway with ambient features off.

    The model is registered under the key ``gateway`` so the CLI never
    contacts x.ai directly; the gateway URL already carries the task path.
    """
    public_tools(channels)
    q = json.dumps
    mcp_env = (
        f"\n[mcp_servers.computer_use.env]\nCOMPUTER_USE_CHANNELS = {q(channels)}\n"
        if channels
        else ""
    )
    off = lambda *keys: "\n".join(f"{key} = false" for key in keys)  # noqa: E731
    return f"""\
[cli]
auto_update = false
use_leader = false

[models]
default = "gateway"
allowed_models = ["gateway", {q(model)}]
session_summary = "gateway"
image_description = "gateway"
web_search = "gateway"
prompt_suggestion = "gateway"
max_retries = 0
subagent_rate_limit_max_attempts = 0

[model.gateway]
model = {q(model)}
name = {q(model)}
base_url = {q(base_url)}
api_backend = "responses"
env_key = "GROK_GATEWAY_TOKEN"
reasoning_summary = "none"
context_window = 500000
max_retries = 0
inference_idle_timeout_secs = 600
subagent_rate_limit_max_attempts = 0
supports_backend_search = false

[[model.gateway.reasoning_efforts]]
id = {q(effort)}
name = {q(effort.title())}
value = {q(effort)}

[endpoints]
models_base_url = {q(base_url)}
models_list_url = {q(base_url + "/models")}
xai_api_base_url = {q(base_url)}
cli_chat_proxy_base_url = {q(base_url)}

[mcp_servers.computer_use]
command = {q(mcp_command)}
args = ["mcp"]
enabled = true
startup_timeout_sec = 60
tool_timeout_sec = 180
{mcp_env}
[features]
{off("remote_fetch", "managed_config", "telemetry", "title_refresh", "turn_summary",
     "session_recap", "session_search", "feedback", "backend_tools", "image_gen",
     "video_gen", "voice_mode", "non_git_warning", "ask_user_question")}

[telemetry]
otel_enabled = false
trace_upload = false

[relay]
enabled = false

[memory]
enabled = false

[memory_v2]
enabled = false

[session]
load_envrc = false

[subagents]
enabled = false

[workflows]
enabled = false

[ui]
prompt_suggestions = false

[compat.claude]
{off("agents", "hooks", "mcps", "rules", "skills", "sessions")}

[compat.cursor]
{off("agents", "hooks", "mcps", "rules", "skills", "sessions")}

[compat.codex]
{off("hooks", "skills", "sessions")}
"""


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


# Shared by the agents whose stream does not write the final answer itself:
# take the last assistant text from the JSONL trace.
FINAL_MESSAGE_SCRIPT = """\
python3 - {trace} {final} <<'PY'
import json
import sys

last = ""
with open(sys.argv[1], encoding="utf-8", errors="replace") as handle:
    for line in handle:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
{extract}
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write(last)
PY
"""


def final_message_command(trace: str, final: str, extract: str) -> str:
    return FINAL_MESSAGE_SCRIPT.format(
        trace=shlex.quote(trace), final=shlex.quote(final), extract=extract
    )


def launcher_script(
    *,
    agent: str = "codex",
    model: str = "",
    effort: str = "xhigh",
    gateway_endpoint: str = "",
    public_mcp_tools=None,
) -> str:
    """Guest launcher for one agent turn.

    ``gateway_endpoint`` is the task-scoped gateway URL. Codex and Grok Build
    read it from their configuration files; Claude Code and Gemini CLI get it
    through the environment.
    """
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
        finalize_command = final_message_command(GUEST_TRACE, GUEST_FINAL, """\
        if event.get("type") == "result" and isinstance(event.get("result"), str):
            last = event["result"]
        elif event.get("type") == "assistant":
            for block in (event.get("message") or {}).get("content", []) or []:
                if block.get("type") == "text" and str(block.get("text") or "").strip():
                    last = str(block["text"])""")
        agent_environment = f"""\
export CLAUDE_CONFIG_DIR={shlex.quote(GUEST_CLAUDE_CONFIG_DIR)}
export ANTHROPIC_BASE_URL={shlex.quote(gateway_endpoint)}
export ANTHROPIC_AUTH_TOKEN=hcu-task-gateway
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
    elif agent == "gemini-cli":
        # Assistant text streams as deltas; a turn ends with one "result" row.
        common = (
            f"{shlex.quote(GUEST_GEMINI_NODE)} {shlex.quote(GUEST_GEMINI_CLI)} "
            f"--model {shlex.quote(model)} "
            "--include-directories /home/user "
            "--approval-mode yolo "
            "--output-format stream-json"
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
        finalize_command = final_message_command(GUEST_TRACE, GUEST_FINAL, """\
        if event.get("type") == "message" and event.get("role") == "assistant":
            chunk = str(event.get("content") or "")
            last = last + chunk if event.get("delta") else chunk""")
        agent_environment = f"""\
export GEMINI_CLI_HOME={shlex.quote(GUEST_GEMINI_HOME)}
export GEMINI_CLI_SYSTEM_SETTINGS_PATH={shlex.quote(GUEST_GEMINI_HOME + "/absent-system-settings.json")}
export GEMINI_CLI_SYSTEM_DEFAULTS_PATH={shlex.quote(GUEST_GEMINI_HOME + "/absent-system-defaults.json")}
export GEMINI_CLI_TRUSTED_FOLDERS_PATH={shlex.quote(GUEST_GEMINI_TRUSTED)}
export GEMINI_SYSTEM_MD={shlex.quote(GUEST_BASE_INSTRUCTIONS)}
export GEMINI_EXP={shlex.quote(GUEST_GEMINI_EXPERIMENTS)}
export GEMINI_CLI_NO_RELAUNCH=1
export GOOGLE_GENAI_API_VERSION=v1beta
export GOOGLE_GEMINI_BASE_URL={shlex.quote(gateway_endpoint)}
export GEMINI_API_KEY=hcu-task-gateway
"""
    elif agent == "grok-build":
        common = (
            f"{shlex.quote(GUEST_GROK)} --cwd /home/user "
            "--model gateway "
            f"--reasoning-effort {shlex.quote(effort)} "
            f"--system-prompt-override=\"$(cat {shlex.quote(GUEST_BASE_INSTRUCTIONS)})\" "
            "--output-format streaming-json --always-approve "
            "--no-plan --no-subagents --disable-web-search --verbatim "
            f"--disallowed-tools {shlex.quote(GROK_DISALLOWED_TOOLS)}"
        )
        start_command = (
            f"{common} --prompt-file {shlex.quote(GUEST_PROMPT)} "
            f"> {shlex.quote(GUEST_TRACE)} "
            f"2> {shlex.quote(GUEST_STDERR)}"
        )
        resume_command = (
            "echo 'Grok Build cannot resume under the system prompt override' "
            f"> {shlex.quote(GUEST_STDERR)}; false"
        )
        finalize_command = final_message_command(GUEST_TRACE, GUEST_FINAL, """\
        if event.get("type") == "text" and isinstance(event.get("data"), str):
            last += event["data"]
        elif event.get("type") == "tool_call":
            last = \"\"""")
        agent_environment = f"""\
export GROK_HOME={shlex.quote(GUEST_GROK_HOME)}
export GROK_GATEWAY_TOKEN=hcu-task-gateway
export GROK_DISABLE_AUTOUPDATER=1
export GROK_MEMORY=0
export GROK_LOG_FILE={shlex.quote(GUEST_GROK_HOME + "/grok.log")}
export RUST_LOG=warn
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
        if agent == "gemini-cli" and event.get("type") == "init":
            return str(event.get("session_id") or "")
        if agent == "grok-build" and event.get("type") == "end":
            return str(event.get("sessionId") or "")
    return ""
