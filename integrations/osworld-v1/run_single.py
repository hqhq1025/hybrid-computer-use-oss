#!/usr/bin/env python3
"""osw-hybrid 端到端单任务：容器内 Codex + 本地 MCP，OSWorld evaluator 出分。

架构（Codex 跑在容器里，作为容器内的 coding agent）：

    宿主                          容器 (hybrid-computer-use:v0.14)
    ─────                         ────────────────────
    DesktopEnv ──HTTP:5000──────> OSWorld guest server   (setup / getter / evaluate)
    AAD 网关:8600 <──host.docker.internal── codex exec   (模型调用)
                                    │
                                    └─stdio─> computer-use mcp
                                                  │
                                                  └─AT-SPI─> LibreOffice / GIMP / Chrome

容器出不去公网（Azure NSG），所以模型调用必须经宿主网关；而 MCP 是 stdio 协议、
必须与桌面同机，所以 codex 和 MCP 都在容器内——这两条约束共同决定了上面的形状。

产出：
  - trace/<task>/codex.jsonl   Codex 的完整事件流（含 reasoning/thinking、每次工具调用）
  - trace/<task>/resource.jsonl 容器资源采样（算并发上限用）
  - trace/<task>/result.json   分数 + 汇总指标
"""

import argparse
import collections
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import shlex
import subprocess
import sys
import threading
import time
import urllib.parse
from datetime import datetime, timezone

REPO_ROOT = Path(__file__).resolve().parents[2]
OSWORLD_ROOT = Path(
    os.environ.get("OSWORLD_V1_ROOT", REPO_ROOT / "external" / "OSWorld")
).expanduser().resolve()
sys.path.insert(0, str(OSWORLD_ROOT))

import logging

from evaluator_audit import AUDIT_VERSION, VLC_TASK, audit_task
from evaluation_policy import assert_task_allowed
from tool_release import (
    LEGACY_AX_TELEMETRY_PATH,
    LEGACY_MCP_COMMAND,
    assert_tool_candidate_image,
    assert_tool_release_image,
    result_tool_candidate_metadata,
    result_tool_metadata,
    tool_candidate_record,
    tool_release_record,
)

logging.basicConfig(level=logging.INFO,
                    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
                    datefmt="%H:%M:%S")
for noisy in ("urllib3", "docker", "requests", "httpx"):
    logging.getLogger(noisy).setLevel(logging.WARNING)

ROOT = str(Path(__file__).resolve().parent)
DEFAULT_TASK = "libreoffice_calc/01b269ae-2111-4a07-81fd-3fcd711993b0.json"
CODEX_BASE_INSTRUCTIONS_PATH = os.path.join(
    str(REPO_ROOT),
    "skills",
    "base",
    "CODEX_BASE_INSTRUCTIONS.md",
)
GUEST_CODEX_HOME = "/opt/osw-runtime/codex-task"
GUEST_CLAUDE_HOME = "/opt/osw-runtime/claude-task"
GUEST_CLAUDE_CODE = "/home/user/.local/bin/claude"
GUEST_CLAUDE_MCP_ADAPTER = os.path.join(
    GUEST_CLAUDE_HOME, "mcp_schema_adapter.py"
)
GUEST_CLAUDE_MCP_CONFIG = os.path.join(GUEST_CLAUDE_HOME, "mcp.json")
GUEST_CLAUDE_BASE_INSTRUCTIONS = os.path.join(
    GUEST_CLAUDE_HOME, "osw-base-instructions.md"
)
GUEST_JUDGE_CODEX_HOME = "/opt/osw-runtime/codex-judge"
GUEST_JUDGE_PROBE = "/tmp/osw-live-judge-work/judge-probe"
LIVE_JUDGE_PROBE_SCRIPT = r"""#!/usr/bin/env bash
set -euo pipefail

seconds=10
if [[ ${1:-} =~ ^[0-9]+$ ]]; then
    seconds=$1
    shift
fi
if (( seconds < 1 )); then
    seconds=1
elif (( seconds > 30 )); then
    seconds=30
fi
if [[ ${1:-} == "--" ]]; then
    shift
fi
if (( $# == 0 )); then
    echo "usage: judge-probe [1-30] [--] command [args...]" >&2
    exit 2
fi

exec /usr/bin/timeout --signal=TERM --kill-after=2s "${seconds}s" "$@"
"""
GUEST_CODEX_BASE_INSTRUCTIONS = os.path.join(
    GUEST_CODEX_HOME, "osw-base-instructions.md"
)
GUEST_JUDGE_BASE_INSTRUCTIONS = os.path.join(
    GUEST_JUDGE_CODEX_HOME, "osw-base-instructions.md"
)
FORBIDDEN_SKILL_BASENAMES = {"agents.md", "claude.md"}
DEVELOPMENT_INSTRUCTION_MARKERS = (
    "development instructions",
    "this file is for engineering agents",
    "develop and maintain this repository",
)
PROHIBITED_AGENT_HELPER_PATTERN = re.compile(
    r"\bosw-[a-z0-9][a-z0-9-]*\b",
    re.IGNORECASE,
)
CODEX_BUNDLED_SYSTEM_SKILLS = (
    "imagegen",
    "openai-docs",
    "plugin-creator",
    "review-agent",
    "skill-creator",
    "skill-installer",
)
CLAUDE_BUILTIN_TOOLS = (
    "Bash,Edit,Glob,Grep,Read,WebFetch,Write"
)
CLAUDE_MCP_TOOLS = "mcp__computer_use__AX,mcp__computer_use__GUI"


@contextmanager
def acquire_judge_slot():
    slot_dir = str(os.environ.get("OSW_JUDGE_SLOT_DIR") or "").strip()
    concurrency = int(os.environ.get("OSW_JUDGE_CONCURRENCY") or 0)
    timeout = float(os.environ.get("OSW_JUDGE_QUEUE_TIMEOUT") or 0)
    if not slot_dir or concurrency <= 0:
        yield None
        return
    os.makedirs(slot_dir, exist_ok=True)
    deadline = time.monotonic() + timeout if timeout > 0 else None
    while True:
        for index in range(concurrency):
            path = os.path.join(slot_dir, f"slot-{index}.lock")
            handle = open(path, "a+")
            try:
                fcntl.flock(
                    handle.fileno(),
                    fcntl.LOCK_EX | fcntl.LOCK_NB,
                )
            except BlockingIOError:
                handle.close()
                continue
            try:
                yield index
            finally:
                fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
                handle.close()
            return
        if deadline is not None and time.monotonic() >= deadline:
            raise TimeoutError(
                f"Judge queue exceeded {timeout:.0f}s with "
                f"{concurrency} shared slot(s)"
            )
        time.sleep(0.5)
ARTIFACT_EXTENSIONS = (
    "xlsx",
    "xls",
    "xlsm",
    "csv",
    "tsv",
    "ods",
    "docx",
    "doc",
    "odt",
    "rtf",
    "pptx",
    "ppt",
    "odp",
    "pdf",
    "png",
    "jpg",
    "jpeg",
    "gif",
    "webp",
    "svg",
    "mp3",
    "mp4",
    "wav",
    "m4a",
    "ogg",
    "webm",
    "txt",
    "md",
    "html",
    "htm",
    "py",
    "js",
    "ts",
    "json",
    "xml",
    "bib",
    "desktop",
    "conf",
    "ini",
    "sqlite",
    "db",
    "xcu",
)
ARTIFACT_EXPLICIT_BASENAMES = {
    "Preferences",
    "Local State",
    "History",
    "Cookies",
    "prefs.js",
    "gimprc",
    "vlcrc",
}
LIVE_JUDGE_PROMPT_PATH = os.path.join(
    ROOT, "analysis", "live_vm_judge_prompt.md"
)
LIVE_JUDGE_SCHEMA_PATH = os.path.join(
    ROOT, "analysis", "live_outcome_judge.schema.json"
)
AGENT_START_MARKER = "OSW_AGENT_START "
AGENT_LAUNCHER = r'''
import json
import os
import sys

expected = os.environ.get("OSW_EXPECTED_WORKDIR", "")
try:
    actual = os.getcwd()
except OSError as exc:
    actual = ""
    error = f"{type(exc).__name__}: {exc}"
else:
    error = None

evidence = {
    "actual_workdir": actual,
    "error": error,
    "expected_workdir": expected,
    "pid": os.getpid(),
    "validated": bool(actual) and actual == expected,
}
print(
    "OSW_AGENT_START "
    + json.dumps(evidence, sort_keys=True, separators=(",", ":")),
    flush=True,
)
if not evidence["validated"]:
    raise SystemExit(125)

os.execv(
    "/usr/bin/bash",
    [
        "/usr/bin/bash",
        "--noprofile",
        "--norc",
        "-c",
        sys.argv[1],
    ],
)
'''
PROHIBITED_HELPER_PROBE = r'''
import json
import os
from pathlib import Path

found = set()
for directory in os.environ.get("PATH", "").split(os.pathsep):
    if not directory:
        continue
    path = Path(directory)
    try:
        entries = list(path.iterdir())
    except OSError:
        continue
    for entry in entries:
        if (
            entry.name.lower().startswith("osw-")
            and entry.is_file()
            and os.access(entry, os.X_OK)
        ):
            found.add(str(entry))
print(json.dumps(sorted(found)))
'''

# 容器内 codex 的配置。gateway 走 host.docker.internal，MCP 是本地 stdio。
CODEX_CONFIG = """\
model = "{model}"
model_provider = "osw_gateway"
model_reasoning_effort = "{effort}"
approval_policy = "never"
sandbox_mode = "danger-full-access"
web_search = "disabled"

# 轨迹要留住 thinking：默认只在 usage 里给 reasoning_output_tokens 计数，
# 不落正文。跑分后要复盘"模型当时在想什么"，没有正文等于没有轨迹。
model_reasoning_summary = "detailed"
show_raw_agent_reasoning = true
model_instructions_file = "{base_instructions_path}"

# shell 开关 —— 这是决定这套评测在测什么的分水岭，由 harness 按口径注入。
# 关闭时测的是纯桌面链路（a11y + GUI 通道）；开启时 agent 可以直接用命令行改文件，
# 实测 16 次 command_execution 对 4 次 MCP 调用、整条 GUI 没碰。
# 两种口径都有意义，但**必须分开报告**，混在一起的平均分没有任何含义。
# 用 feature 开关而不是往 prompt 里写禁令：instruction 必须保持 OSWorld 原样。
{shell_features}
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

[memories]
generate_memories = false
use_memories = false
disable_on_external_context = true

[tools.experimental_request_user_input]
enabled = false

[tools.update_plan]
enabled = false

{disabled_system_skills}

[model_providers.osw_gateway]
name = "Azure OpenAI via osw-hybrid gateway"
base_url = "http://host.docker.internal:{gw_port}/t/{task_id}/v1"
wire_api = "responses"

[mcp_servers.computer_use]
command = "{mcp_command}"
args = ["mcp"]
startup_timeout_sec = 60
tool_timeout_sec = 180
"""


def build_disabled_system_skills_config(codex_home=GUEST_CODEX_HOME):
    return "\n\n".join(
        (
            "[[skills.config]]\n"
            f'path = "{codex_home}/skills/.system/{name}/SKILL.md"\n'
            "enabled = false"
        )
        for name in CODEX_BUNDLED_SYSTEM_SKILLS
    )


def build_codex_config(
    model,
    effort,
    gw_port,
    shell_features,
    task_id,
    mcp_command=LEGACY_MCP_COMMAND,
):
    return CODEX_CONFIG.format(
        model=model,
        effort=effort,
        gw_port=gw_port,
        shell_features=shell_features,
        base_instructions_path=GUEST_CODEX_BASE_INSTRUCTIONS,
        disabled_system_skills=build_disabled_system_skills_config(
            GUEST_CODEX_HOME
        ),
        task_id=urllib.parse.quote(task_id, safe=""),
        mcp_command=mcp_command,
    )


def build_live_judge_prompt(
    instruction,
    final_response="",
    declared_infeasible=False,
):
    template = Path(LIVE_JUDGE_PROMPT_PATH).read_text(encoding="utf-8")
    return template.format(
        instruction=instruction,
        final_response=final_response or "(no final response recorded)",
        completion_declaration=(
            INFEASIBLE_SENTINEL
            if declared_infeasible
            else "No TASK_INFEASIBLE declaration recorded."
        ),
    )


def build_live_judge_config(
    model,
    effort,
    gw_port,
    task_id,
    mcp_command=LEGACY_MCP_COMMAND,
):
    return CODEX_CONFIG.format(
        model=model,
        effort=effort,
        gw_port=gw_port,
        shell_features="[features]\nshell_tool = true\nunified_exec = true",
        base_instructions_path=GUEST_JUDGE_BASE_INSTRUCTIONS,
        disabled_system_skills=build_disabled_system_skills_config(
            GUEST_JUDGE_CODEX_HOME
        ),
        task_id=urllib.parse.quote(f"judge-{task_id}", safe=""),
        mcp_command=mcp_command,
    )


def claude_mcp_config(mcp_command=LEGACY_MCP_COMMAND):
    return json.dumps(
        {
            "mcpServers": {
                "computer_use": {
                    "type": "stdio",
                    "command": "/usr/bin/python3",
                    "args": [
                        GUEST_CLAUDE_MCP_ADAPTER,
                        mcp_command,
                        "mcp",
                    ],
                }
            }
        },
        indent=2,
    ) + "\n"


def claude_gateway_affinity(task_id):
    batch_run_id = str(os.environ.get("OSW_BATCH_RUN_ID") or "standalone")
    return f"osw-hybrid-{batch_run_id}-task-{task_id}"


def claude_gateway_endpoint(gateway_base, task_id):
    affinity = urllib.parse.quote(claude_gateway_affinity(task_id), safe="")
    return gateway_base.rstrip("/") + "/t/" + affinity


def claude_mcp_tools(public_tools=None):
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


def build_claude_agent_command(
    model,
    effort,
    enable_shell,
    public_mcp_tools=None,
):
    builtin_tools = CLAUDE_BUILTIN_TOOLS if enable_shell else ""
    mcp_tools = claude_mcp_tools(public_mcp_tools)
    allowed_tools = (
        f"{builtin_tools},{mcp_tools}"
        if builtin_tools
        else mcp_tools
    )
    return (
        "source /home/user/a11y-env.sh && "
        f"exec {GUEST_CLAUDE_CODE} -p "
        f"--model {shlex.quote(model)} "
        f"--effort {shlex.quote(effort)} "
        "--permission-mode bypassPermissions "
        f"--mcp-config {GUEST_CLAUDE_MCP_CONFIG} "
        "--strict-mcp-config "
        "--setting-sources '' "
        f"--system-prompt-file {GUEST_CLAUDE_BASE_INSTRUCTIONS} "
        f"--tools {shlex.quote(builtin_tools)} "
        f"--allowedTools {shlex.quote(allowed_tools)} "
        "--disable-slash-commands "
        "--no-chrome "
        "--prompt-suggestions false "
        "--no-session-persistence "
        "--output-format stream-json --verbose"
    )


def assert_container_mount_isolation(container_name):
    completed = subprocess.run(
        [
            "docker",
            "inspect",
            container_name,
            "--format",
            "{{json .Mounts}}",
        ],
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    )
    mounts = json.loads(completed.stdout or "[]")
    if mounts:
        raise RuntimeError(
            "evaluated task container has host or shared mounts: "
            + json.dumps(mounts, sort_keys=True)
        )
    return mounts


def clear_guest_agent_context(container_name):
    subprocess.run(
        [
            "docker",
            "exec",
            "--user",
            "root",
            container_name,
            "bash",
            "--noprofile",
            "--norc",
            "-c",
            (
                "rm -rf /home/user/.codex /home/user/.claude "
                "/home/user/.claude.json "
                "/home/user/AGENTS.md /home/user/CLAUDE.md"
            ),
        ],
        check=True,
        capture_output=True,
    )


def bind_live_judge_run_validity(
    live_judge,
    *,
    truncated,
    completion_boundary_detected,
    agent_returncode,
):
    eligible = bool(
        not truncated
        and completion_boundary_detected
        and agent_returncode in (None, 0)
    )
    live_judge["answer_score_eligible"] = eligible
    if eligible:
        live_judge["answer_score_exclusion_reason"] = None
    elif truncated:
        live_judge["answer_score_exclusion_reason"] = (
            "Task Agent run was truncated before a valid completion answer."
        )
    elif not completion_boundary_detected:
        live_judge["answer_score_exclusion_reason"] = (
            "Task Agent produced no valid completion boundary."
        )
    else:
        live_judge["answer_score_exclusion_reason"] = (
            f"Task Agent exited abnormally with return code {agent_returncode}."
        )
    return live_judge


def stop_evaluated_agent_processes(
    container_name,
    agent,
    mcp_process_name=LEGACY_MCP_COMMAND,
    timeout=15,
):
    mcp_process_name = os.path.basename(mcp_process_name)
    process_names = (
        ["codex", mcp_process_name]
        if agent == "codex"
        else ["claude", mcp_process_name]
    )
    terminal_states = ["Z", "X", "x"]

    def current():
        completed = subprocess.run(
            [
                "docker", "exec", container_name,
                "/usr/bin/python3", "-c",
                "import json,os\n"
                f"names={process_names!r}\n"
                f"terminal_states={terminal_states!r}\n"
                "found=[]\n"
                "for item in os.listdir('/proc'):\n"
                " if not item.isdigit(): continue\n"
                " try:\n"
                "  name=open(f'/proc/{item}/comm').read().strip()\n"
                "  stat=open(f'/proc/{item}/stat').read()\n"
                "  end=stat.rfind(')')\n"
                "  state=stat[end+2:end+3] if end >= 0 else ''\n"
                " except OSError: continue\n"
                " if name not in names or state in terminal_states: continue\n"
                " found.append({'pid':int(item),'name':name,'state':state})\n"
                "print(json.dumps(sorted(found,key=lambda x:x['pid'])))",
            ],
            capture_output=True,
            text=True,
            timeout=10,
            check=True,
        )
        return json.loads(completed.stdout)

    before = current()
    if not before:
        return {
            "initial_processes": [],
            "term_sent": False,
            "kill_sent": False,
            "remaining_processes": [],
        }

    pids = [str(item["pid"]) for item in before]
    subprocess.run(
        [
            "docker", "exec", "--user", "root", container_name,
            "/bin/kill", "-TERM", *pids,
        ],
        capture_output=True,
        timeout=10,
    )
    deadline = time.time() + timeout
    remaining = current()
    while remaining and time.time() < deadline:
        time.sleep(0.5)
        remaining = current()

    kill_sent = False
    if remaining:
        kill_sent = True
        subprocess.run(
            [
                "docker", "exec", "--user", "root", container_name,
                "/bin/kill", "-KILL",
                *[str(item["pid"]) for item in remaining],
            ],
            capture_output=True,
            timeout=10,
        )
        time.sleep(0.5)
        remaining = current()
    if remaining:
        raise RuntimeError(
            "evaluated agent processes remain before Judge: "
            + json.dumps(remaining, sort_keys=True)
        )
    return {
        "initial_processes": before,
        "term_sent": True,
        "kill_sent": kill_sent,
        "remaining_processes": [],
    }


LIVE_JUDGE_STATE_PROBE = r'''
import hashlib
import json
import os
from pathlib import Path

home = Path("/home/user")
roots = [
    home / "Desktop",
    home / "Documents",
    home / "Downloads",
    home / "project",
    home / "Projects",
    home / ".config",
    home / ".thunderbird",
    home / ".mozilla",
]
single_files = [
    home / ".chrome-osworld" / "Default" / "Preferences",
    home / ".chrome-osworld" / "Local State",
]
excluded_parts = {
    ".cache", ".codex", ".codex-judge", "__pycache__", "Cache",
    "Code Cache", "GPUCache", "DawnCache", "Crashpad", "ShaderCache",
    "Service Worker",
}

files = {}
def record(path):
    try:
        relative = str(path.relative_to(home))
        stat = path.stat()
        digest = hashlib.sha256()
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
        files[relative] = {
            "bytes": stat.st_size,
            "sha256": digest.hexdigest(),
        }
    except (OSError, ValueError):
        pass

for root in roots:
    if not root.exists():
        continue
    for directory, dirnames, filenames in os.walk(root):
        dirnames[:] = [
            name for name in dirnames
            if name not in excluded_parts
        ]
        base = Path(directory)
        if any(part in excluded_parts for part in base.parts):
            continue
        for name in filenames:
            record(base / name)
for path in single_files:
    if path.is_file():
        record(path)

payload = json.dumps(files, sort_keys=True, separators=(",", ":"))
print(json.dumps({
    "files": files,
    "file_count": len(files),
    "manifest_sha256": hashlib.sha256(payload.encode()).hexdigest(),
}, sort_keys=True))
'''


def capture_live_judge_state(env, container_name, judge_dir, label):
    state = {
        "captured_at_utc": datetime.now(timezone.utc).isoformat(),
        "label": label,
    }
    try:
        screenshot = env.controller.get_screenshot()
        if screenshot:
            screenshot_path = os.path.join(judge_dir, f"{label}.png")
            with open(screenshot_path, "wb") as output:
                output.write(screenshot)
            state["screenshot"] = os.path.basename(screenshot_path)
            state["screenshot_sha256"] = hashlib.sha256(
                screenshot
            ).hexdigest()
    except Exception as exc:
        state["screenshot_error"] = f"{type(exc).__name__}: {exc}"
    try:
        tree = env.controller.get_accessibility_tree() or ""
        tree_path = os.path.join(judge_dir, f"{label}.a11y.txt")
        Path(tree_path).write_text(tree, encoding="utf-8")
        state["accessibility_tree"] = os.path.basename(tree_path)
        state["accessibility_sha256"] = hashlib.sha256(
            tree.encode("utf-8", errors="replace")
        ).hexdigest()
    except Exception as exc:
        state["accessibility_error"] = f"{type(exc).__name__}: {exc}"
    try:
        windows = subprocess.run(
            [
                "docker", "exec", "--user", "user",
                "-e", "DISPLAY=:0",
                container_name,
                "bash", "--noprofile", "--norc", "-c",
                "wmctrl -l -p -x 2>&1",
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        state["windows"] = windows.stdout
        state["windows_returncode"] = windows.returncode
    except Exception as exc:
        state["windows_error"] = f"{type(exc).__name__}: {exc}"
    try:
        manifest = subprocess.run(
            [
                "docker", "exec", "--user", "user",
                container_name,
                "/usr/bin/python3", "-c", LIVE_JUDGE_STATE_PROBE,
            ],
            capture_output=True,
            text=True,
            timeout=180,
            check=True,
        )
        state["file_manifest"] = json.loads(manifest.stdout)
    except Exception as exc:
        state["file_manifest_error"] = f"{type(exc).__name__}: {exc}"
    Path(judge_dir, f"{label}.json").write_text(
        json.dumps(state, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return state


def compare_live_judge_states(before, after):
    before_files = (before.get("file_manifest") or {}).get("files", {})
    after_files = (after.get("file_manifest") or {}).get("files", {})
    return {
        "file_state_changed": before_files != after_files,
        "added_files": sorted(set(after_files) - set(before_files)),
        "removed_files": sorted(set(before_files) - set(after_files)),
        "changed_files": sorted(
            path
            for path in set(before_files) & set(after_files)
            if before_files[path] != after_files[path]
        ),
        "screenshot_changed": (
            before.get("screenshot_sha256")
            != after.get("screenshot_sha256")
        ),
        "accessibility_changed": (
            before.get("accessibility_sha256")
            != after.get("accessibility_sha256")
        ),
    }


def strict_live_judge_verdict(verdict):
    if not isinstance(verdict, dict):
        return None
    statuses = {
        requirement.get("status")
        for requirement in verdict.get("requirements", [])
        if isinstance(requirement, dict)
    }
    if statuses & {"NOT_MET", "PROVEN_FALSE"}:
        return "FAIL"
    declared = verdict.get("instruction_verdict")
    if declared == "FAIL":
        return "FAIL"
    if declared == "PASS" and statuses and statuses <= {
        "MET", "PROVEN_TRUE"
    }:
        return "PASS"
    if statuses and statuses <= {"MET", "PROVEN_TRUE"}:
        return "PASS"
    return None


def valid_live_outcome_verdict(verdict):
    if not isinstance(verdict, dict):
        return False
    requirements = verdict.get("requirements")
    if not isinstance(requirements, list) or not requirements:
        return False
    statuses = {
        requirement.get("status")
        for requirement in requirements
        if isinstance(requirement, dict)
    }
    declared = verdict.get("instruction_verdict")
    answer_mode = verdict.get("answer_mode")
    infeasibility = verdict.get("infeasibility_assessment")
    failure_reasons = verdict.get("failure_reasons")
    if (
        answer_mode not in {"OUTCOME", "INFEASIBLE"}
        or not isinstance(infeasibility, dict)
        or not isinstance(failure_reasons, list)
    ):
        return False
    if declared == "PASS":
        if statuses != {"MET"} or failure_reasons:
            return False
        if answer_mode == "INFEASIBLE":
            return (
                infeasibility.get("claimed_by_agent") is True
                and infeasibility.get("genuinely_infeasible") is True
            )
        return True
    if declared == "FAIL":
        return "NOT_MET" in statuses and bool(failure_reasons)
    return False


def live_judge_mutation_audit(events_text):
    mutating_mcp_tools = {
        "click", "double_click", "right_click", "drag", "gui", "hotkey",
        "key", "press_key", "scroll", "set_value", "type_text",
    }
    observed = []
    suspicious_commands = []
    command_patterns = (
        r"(^|[;&|]\s*)(rm|mv|cp|touch|mkdir|rmdir|chmod|chown|ln|truncate|dd|install)\b",
        r"(^|[^<])>>?",
        r"\bsed\s+-i\b",
        r"\btee\b",
        r"\b(os\.(remove|unlink|rename|replace|mkdir|makedirs)|"
        r"shutil\.(copy|copy2|move|rmtree)|"
        r"\.(write_text|write_bytes|write)\s*\()",
        r"\b(open|zipfile\.ZipFile)\s*\([^)]*,\s*['\"](?:w|a|x)",
    )
    for line in (events_text or "").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item") or {}
        if item.get("type") != "mcp_tool_call":
            if item.get("type") == "command_execution":
                command = str(item.get("command") or "")
                if any(
                    re.search(pattern, command, flags=re.IGNORECASE)
                    for pattern in command_patterns
                ):
                    suspicious_commands.append(command[:1000])
            continue
        tool = _tool_short_name(item.get("tool")).lower()
        if tool == "gui":
            arguments = item.get("arguments")
            if isinstance(arguments, str):
                try:
                    arguments = json.loads(arguments)
                except json.JSONDecodeError:
                    arguments = {}
            actions = (
                arguments.get("actions", [])
                if isinstance(arguments, dict)
                else []
            )
            mutating_actions = [
                str(action.get("type") or "").lower()
                for action in actions
                if isinstance(action, dict)
                and str(action.get("type") or "").lower()
                not in {"", "screenshot", "wait"}
            ]
            observed.extend(f"gui:{action}" for action in mutating_actions)
        elif tool in {"ax", "a11y"}:
            arguments = item.get("arguments")
            if isinstance(arguments, str):
                try:
                    arguments = json.loads(arguments)
                except json.JSONDecodeError:
                    arguments = {}
            if (
                isinstance(arguments, dict)
                and str(arguments.get("mode") or "").lower() == "act"
            ):
                actions = arguments.get("actions") or []
                observed.extend(
                    f"ax:{str(action.get('type') or '').lower()}"
                    for action in actions
                    if isinstance(action, dict)
                )
        elif tool in mutating_mcp_tools:
            observed.append(tool)
    return {
        "mutating_mcp_calls": observed,
        "mutation_capable_mcp_used": bool(observed),
        "suspicious_commands": suspicious_commands,
        "mutation_capable_command_used": bool(suspicious_commands),
    }


def nonvolatile_live_judge_changes(integrity):
    volatile_prefixes = (
        ".chrome-osworld/",
        ".config/google-chrome/",
    )
    return [
        path
        for path in (
            integrity.get("added_files", [])
            + integrity.get("removed_files", [])
            + integrity.get("changed_files", [])
        )
        if not path.startswith(volatile_prefixes)
    ]


LIVE_JUDGE_TRACE_EVIDENCE_ITEM_TYPES = {
    "command_execution",
    "file_change",
    "local_shell_call",
    "mcp_tool_call",
    "web_search",
}

LIVE_JUDGE_FORBIDDEN_TRACE_MARKERS = (
    "/evaluation_examples/",
    "/evaluator",
    "evaluation_examples",
    "evaluator.py",
    "gold/reference",
    "gold_artifact",
    "task_config",
)


def build_live_judge_trace_evidence(trace_path, output_path):
    """Write read-only execution evidence without Agent reasoning or claims."""
    source = Path(trace_path)
    destination = Path(output_path)
    retained = 0
    with destination.open("w", encoding="utf-8") as output:
        for line in source.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get("type") != "item.completed":
                continue
            item = event.get("item") or {}
            if item.get("type") not in LIVE_JUDGE_TRACE_EVIDENCE_ITEM_TYPES:
                continue
            serialized = json.dumps(item, ensure_ascii=False).casefold()
            if any(
                marker in serialized
                for marker in LIVE_JUDGE_FORBIDDEN_TRACE_MARKERS
            ):
                continue

            sanitized = json.loads(json.dumps(event))
            result = (sanitized.get("item") or {}).get("result")
            if isinstance(result, dict) and isinstance(
                result.get("content"), list
            ):
                content = []
                for entry in result["content"]:
                    if not isinstance(entry, dict):
                        continue
                    if entry.get("type") == "text":
                        content.append(entry)
                    elif entry.get("type") == "image":
                        content.append({
                            "type": "image",
                            "mimeType": (
                                entry.get("mimeType")
                                or entry.get("mime_type")
                            ),
                            "omitted_from_judge_evidence": True,
                        })
                result["content"] = content
            output.write(
                json.dumps(sanitized, ensure_ascii=False) + "\n"
            )
            retained += 1
    return retained


def stage_live_judge_evidence(
    container_name,
    judge_dir,
    artifacts_dir,
    final_response,
    trace_dir=None,
):
    destination = "/tmp/osw-live-judge-sealed"
    subprocess.run(
        [
            "docker", "exec", "--user", "root", container_name,
            "bash", "--noprofile", "--norc", "-c",
            f"rm -rf {destination} && mkdir -p "
            f"{destination}/artifacts",
        ],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
    )
    copied = []
    evidence_files = (
        ("pre_judge.png", "final_state.png"),
        ("pre_judge.a11y.txt", "final_state.a11y.txt"),
        ("pre_judge.json", "final_state.json"),
    )
    for source_name, destination_name in evidence_files:
        source = Path(judge_dir, source_name)
        if not source.is_file():
            continue
        completed = subprocess.run(
            [
                "docker", "cp", str(source),
                f"{container_name}:{destination}/{destination_name}",
            ],
            capture_output=True,
            text=True,
            timeout=120,
        )
        if completed.returncode == 0:
            copied.append(destination_name)
    if artifacts_dir and Path(artifacts_dir).is_dir():
        completed = subprocess.run(
            [
                "docker", "cp", f"{artifacts_dir}/.",
                f"{container_name}:{destination}/artifacts",
            ],
            capture_output=True,
            text=True,
            timeout=300,
        )
        if completed.returncode == 0:
            copied.append("artifacts/")
    if trace_dir:
        trace_path = Path(trace_dir, "agent.jsonl")
        if trace_path.is_file():
            evidence_path = Path(
                judge_dir, "task_agent_execution_evidence.jsonl"
            )
            build_live_judge_trace_evidence(trace_path, evidence_path)
            completed = subprocess.run(
                [
                    "docker", "cp", str(evidence_path),
                    (
                        f"{container_name}:{destination}/"
                        "task_agent_execution_evidence.jsonl"
                    ),
                ],
                capture_output=True,
                text=True,
                timeout=120,
            )
            if completed.returncode == 0:
                copied.append("task_agent_execution_evidence.jsonl")
        inventory_path = Path(
            trace_dir, "pre_agent_artifact_inventory.json"
        )
        if inventory_path.is_file():
            completed = subprocess.run(
                [
                    "docker", "cp", str(inventory_path),
                    (
                        f"{container_name}:{destination}/"
                        "pre_agent_artifact_inventory.json"
                    ),
                ],
                capture_output=True,
                text=True,
                timeout=120,
            )
            if completed.returncode == 0:
                copied.append("pre_agent_artifact_inventory.json")
    response_path = Path(judge_dir, "task_agent_final_response.txt")
    response_path.write_text(final_response or "", encoding="utf-8")
    completed = subprocess.run(
        [
            "docker", "cp", str(response_path),
            f"{container_name}:{destination}/task_agent_final_response.txt",
        ],
        capture_output=True,
        text=True,
        timeout=120,
    )
    if completed.returncode == 0:
        copied.append("task_agent_final_response.txt")
    subprocess.run(
        [
            "docker", "exec", "--user", "root", container_name,
            "chmod", "-R", "a+rX", destination,
        ],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
    )
    return {
        "container_path": destination,
        "copied": copied,
        "final_state_image": (
            f"{destination}/final_state.png"
            if "final_state.png" in copied
            else None
        ),
    }


def run_live_vm_judge(
    env,
    container_name,
    trace_dir,
    instruction,
    task_id,
    model,
    effort,
    gw_port,
    timeout,
    sealed_before=None,
    artifacts_dir=None,
    final_response="",
    raw_evaluator_completed=True,
    raw_evaluator_error=None,
    declared_infeasible=False,
    mcp_command=LEGACY_MCP_COMMAND,
):
    judge_dir = os.path.join(trace_dir, "judge")
    os.makedirs(judge_dir, exist_ok=True)
    before = sealed_before or capture_live_judge_state(
        env, container_name, judge_dir, "pre_judge"
    )
    staged_evidence = stage_live_judge_evidence(
        container_name,
        judge_dir,
        artifacts_dir,
        final_response,
        trace_dir=trace_dir,
    )
    config = build_live_judge_config(
        model, effort, gw_port, task_id, mcp_command=mcp_command
    )
    base_instructions = Path(
        CODEX_BASE_INSTRUCTIONS_PATH
    ).read_text(encoding="utf-8")
    subprocess.run(
        [
            "docker", "exec", "--user", "root", container_name,
            "bash", "--noprofile", "--norc", "-c",
            f"mkdir -p {GUEST_JUDGE_CODEX_HOME} "
            "&& chown -R user:user /opt/osw-runtime",
        ],
        check=True,
        capture_output=True,
        timeout=30,
    )
    subprocess.run(
        [
            "docker", "exec", "-i", "--user", "user",
            container_name,
            "bash", "--noprofile", "--norc", "-c",
            f"mkdir -p {GUEST_JUDGE_CODEX_HOME} "
            "/tmp/osw-live-judge-work "
            f"&& cat > {GUEST_JUDGE_CODEX_HOME}/config.toml",
        ],
        input=config,
        text=True,
        check=True,
        capture_output=True,
        timeout=30,
    )
    subprocess.run(
        [
            "docker", "exec", "-i", "--user", "user",
            container_name,
            "bash", "--noprofile", "--norc", "-c",
            f"cat > {GUEST_JUDGE_BASE_INSTRUCTIONS}",
        ],
        input=base_instructions,
        text=True,
        check=True,
        capture_output=True,
        timeout=30,
    )
    subprocess.run(
        [
            "docker", "exec", "-i", "--user", "user",
            container_name,
            "bash", "--noprofile", "--norc", "-c",
            f"cat > {GUEST_JUDGE_PROBE} && chmod 755 {GUEST_JUDGE_PROBE}",
        ],
        input=LIVE_JUDGE_PROBE_SCRIPT,
        text=True,
        check=True,
        capture_output=True,
        timeout=30,
    )
    schema = Path(LIVE_JUDGE_SCHEMA_PATH).read_text(encoding="utf-8")
    subprocess.run(
        [
            "docker", "exec", "-i", "--user", "user",
            container_name,
            "bash", "--noprofile", "--norc", "-c",
            "cat > /tmp/osw-live-judge-schema.json "
            "&& rm -f /tmp/osw-live-judge-result.json",
        ],
        input=schema,
        text=True,
        check=True,
        capture_output=True,
        timeout=30,
    )
    prompt = build_live_judge_prompt(
        instruction,
        final_response,
        declared_infeasible=declared_infeasible,
    )
    image_arg = (
        f"-i {staged_evidence['final_state_image']} "
        if staged_evidence["final_state_image"]
        else ""
    )
    inner = (
        "source /home/user/a11y-env.sh && "
        "export HOME=/home/user "
        f"CODEX_HOME={GUEST_JUDGE_CODEX_HOME} "
        f"CODEX_SQLITE_HOME={GUEST_JUDGE_CODEX_HOME} "
        "PYTHONDONTWRITEBYTECODE=1 && "
        f"exec /usr/bin/timeout --signal=TERM --kill-after=10s {int(timeout)}s "
        "/usr/bin/codex exec "
        f"-m {model} "
        f"-c model_reasoning_effort='\"{effort}\"' "
        "--dangerously-bypass-approvals-and-sandbox "
        "--ephemeral --ignore-rules --skip-git-repo-check "
        f"{image_arg}"
        "--json "
        "--output-schema /tmp/osw-live-judge-schema.json "
        "-o /tmp/osw-live-judge-result.json -"
    )
    command = [
        "docker", "exec", "-i", "--user", "user",
        "-w", "/tmp/osw-live-judge-work",
        container_name,
        "bash", "--noprofile", "--norc", "-c", inner,
    ]
    started = time.time()
    completed = subprocess.run(
        command,
        input=prompt,
        text=True,
        capture_output=True,
        timeout=timeout + 30,
    )
    Path(judge_dir, "agent.jsonl").write_text(
        completed.stdout or "", encoding="utf-8"
    )
    Path(judge_dir, "stderr.log").write_text(
        completed.stderr or "", encoding="utf-8"
    )
    result_copy = subprocess.run(
        [
            "docker", "cp",
            f"{container_name}:/tmp/osw-live-judge-result.json",
            os.path.join(judge_dir, "verdict.json"),
        ],
        capture_output=True,
        text=True,
        timeout=30,
    )
    verdict = None
    verdict_path = Path(judge_dir, "verdict.json")
    if result_copy.returncode == 0 and verdict_path.is_file():
        try:
            verdict = json.loads(verdict_path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            verdict = None
    after = capture_live_judge_state(
        env, container_name, judge_dir, "post_judge"
    )
    integrity = compare_live_judge_states(before, after)
    mutation_audit = live_judge_mutation_audit(completed.stdout)
    nonvolatile_changes = nonvolatile_live_judge_changes(integrity)
    metadata = {
        "judge_mode": "interactive_outcome",
        "model": model,
        "effort": effort,
        "duration_seconds": round(time.time() - started, 1),
        "returncode": completed.returncode,
        "verdict": verdict,
        "strict_instruction_verdict": strict_live_judge_verdict(verdict),
        "integrity": integrity,
        "mutation_audit": mutation_audit,
        "nonvolatile_file_changes": nonvolatile_changes,
        "sealed_evidence": staged_evidence,
        "raw_evaluator_attempted_before_judge": True,
        "raw_evaluator_completed_before_judge": raw_evaluator_completed,
        "raw_evaluator_error": raw_evaluator_error,
        "judge_valid": (
            completed.returncode == 0
            and valid_live_outcome_verdict(verdict)
        ),
        "stdout": "judge/agent.jsonl",
        "stderr": "judge/stderr.log",
    }
    Path(judge_dir, "metadata.json").write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return metadata


def evaluate_official_raw(env):
    try:
        return env.evaluate(), None
    except Exception as exc:
        return None, f"{type(exc).__name__}: {exc}"


def build_guarded_agent_command(
    container_name,
    exec_workdir,
    expected_workdir,
    inner_command,
    extra_env=None,
):
    command = [
        "docker",
        "exec",
        "-i",
        "-w",
        exec_workdir,
        "-e",
        f"OSW_EXPECTED_WORKDIR={expected_workdir}",
    ]
    for name, value in (extra_env or {}).items():
        command.extend(["-e", f"{name}={value}"])
    command.extend(
        [
            container_name,
            "/usr/bin/python3",
            "-c",
            AGENT_LAUNCHER,
            inner_command,
        ]
    )
    return command


def normalize_agent_workdir(value):
    value = str(value or "").strip()
    if (
        not (value == "/home/user" or value.startswith("/home/user/"))
        or "\x00" in value
        or "\n" in value
        or "\r" in value
    ):
        return "/home/user", "fallback"
    path = Path(value)
    if ".." in path.parts:
        return "/home/user", "fallback"
    return value.rstrip("/") or "/home/user", "active_terminal"


TERMINAL_CWD_PROBE = r'''
import json
import os
import re
import subprocess

def emit(**value):
    print(json.dumps(value, sort_keys=True, separators=(",", ":")))

try:
    active = subprocess.run(
        ["/usr/bin/xdotool", "getactivewindow"],
        capture_output=True, text=True, timeout=2,
        env={
            "DISPLAY": os.environ.get("DISPLAY", ":0"),
            "XAUTHORITY": os.environ.get(
                "XAUTHORITY", "/home/user/.Xauthority"
            ),
        },
    )
    if active.returncode != 0 or not active.stdout.strip():
        emit(status="no_active_terminal", reason="no_active_window")
        raise SystemExit
    window_id = active.stdout.strip()
    prop = subprocess.run(
        ["/usr/bin/xprop", "-id", window_id, "_NET_WM_PID"],
        capture_output=True, text=True, timeout=2,
        env={
            "DISPLAY": os.environ.get("DISPLAY", ":0"),
            "XAUTHORITY": os.environ.get(
                "XAUTHORITY", "/home/user/.Xauthority"
            ),
        },
    )
    match = re.search(r"=\s*(\d+)\s*$", prop.stdout)
    if not match:
        emit(
            status="no_active_terminal",
            reason="active_window_has_no_pid",
            window_id=window_id,
        )
        raise SystemExit
    terminal_pid = int(match.group(1))
    try:
        terminal_exe = os.path.realpath(f"/proc/{terminal_pid}/exe")
    except FileNotFoundError:
        emit(
            status="no_active_terminal",
            reason="active_window_process_gone",
            window_id=window_id,
            terminal_pid=terminal_pid,
        )
        raise SystemExit
    terminal_name = os.path.basename(terminal_exe)
    allowed_terminals = {
        "xfce4-terminal", "gnome-terminal-server", "xterm", "konsole",
        "lxterminal", "mate-terminal", "tilix", "kitty", "alacritty",
    }
    if (
        terminal_name not in allowed_terminals
        or not terminal_exe.startswith(("/usr/bin/", "/usr/lib/", "/usr/libexec/"))
    ):
        emit(
            status="no_active_terminal",
            reason="active_window_is_not_terminal",
            window_id=window_id,
            terminal_pid=terminal_pid,
            terminal_exe=terminal_exe,
        )
        raise SystemExit

    processes = {}
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        pid = int(name)
        try:
            stat = open(f"/proc/{pid}/stat", encoding="utf-8").read()
            close = stat.rfind(")")
            fields = stat[close + 1:].split()
            processes[pid] = {
                "ppid": int(fields[1]),
                "start_ticks": int(fields[19]),
                "state": fields[0],
                "exe": os.path.realpath(f"/proc/{pid}/exe"),
            }
        except Exception:
            continue

    def descendant_of_terminal(pid):
        seen = set()
        current = pid
        while current in processes and current not in seen:
            if current == terminal_pid:
                return True
            seen.add(current)
            current = processes[current]["ppid"]
        return False

    shell_names = {
        "bash", "zsh", "fish", "sh", "dash", "ksh", "tcsh", "csh",
    }
    candidates = []
    for pid, process in processes.items():
        if process["state"] in {"Z", "X", "x"}:
            continue
        if not descendant_of_terminal(pid):
            continue
        if (
            os.path.basename(process["exe"]) not in shell_names
            or not process["exe"].startswith(("/bin/", "/usr/bin/"))
        ):
            continue
        try:
            tty = os.path.realpath(f"/proc/{pid}/fd/0")
            cwd = os.path.realpath(f"/proc/{pid}/cwd")
        except OSError:
            continue
        if not tty.startswith("/dev/pts/"):
            continue
        candidates.append({
            "pid": pid,
            "start_ticks": process["start_ticks"],
            "tty": tty,
            "cwd": cwd,
            "exe": process["exe"],
        })

    if len(candidates) != 1:
        emit(
            status="terminal_unresolved",
            reason="expected_exactly_one_terminal_shell",
            window_id=window_id,
            terminal_pid=terminal_pid,
            terminal_exe=terminal_exe,
            candidate_count=len(candidates),
            candidates=candidates,
        )
        raise SystemExit
    shell = candidates[0]
    if (
        not os.path.isdir(shell["cwd"])
        or not (
            shell["cwd"] == "/home/user"
            or shell["cwd"].startswith("/home/user/")
        )
    ):
        emit(
            status="terminal_unresolved",
            reason="terminal_shell_cwd_outside_home_or_missing",
            window_id=window_id,
            terminal_pid=terminal_pid,
            terminal_exe=terminal_exe,
            candidate_count=1,
            candidates=candidates,
        )
        raise SystemExit
    emit(
        status="active_terminal",
        window_id=window_id,
        terminal_pid=terminal_pid,
        terminal_exe=terminal_exe,
        shell_pid=shell["pid"],
        shell_start_ticks=shell["start_ticks"],
        shell_exe=shell["exe"],
        tty=shell["tty"],
        cwd=shell["cwd"],
        exec_workdir=f"/proc/{shell['pid']}/cwd",
        candidate_count=1,
    )
except SystemExit:
    pass
except Exception as exc:
    emit(
        status="terminal_unresolved",
        reason="probe_exception",
        error=f"{type(exc).__name__}: {exc}",
    )
'''


TERMINAL_CWD_VALIDATE = r'''
import json
import os
import sys

def emit(**value):
    print(json.dumps(value, sort_keys=True, separators=(",", ":")))

try:
    pid = int(sys.argv[1])
    expected_start = int(sys.argv[2])
    expected_cwd = sys.argv[3]
    expected_tty = sys.argv[4]
    stat = open(f"/proc/{pid}/stat", encoding="utf-8").read()
    close = stat.rfind(")")
    fields = stat[close + 1:].split()
    start_ticks = int(fields[19])
    state = fields[0]
    cwd = os.path.realpath(f"/proc/{pid}/cwd")
    tty = os.path.realpath(f"/proc/{pid}/fd/0")
    valid = (
        state not in {"Z", "X", "x"}
        and start_ticks == expected_start
        and cwd == expected_cwd
        and tty == expected_tty
        and os.path.isdir(cwd)
        and (cwd == "/home/user" or cwd.startswith("/home/user/"))
    )
    emit(
        valid=valid,
        shell_pid=pid,
        shell_start_ticks=start_ticks,
        cwd=cwd,
        tty=tty,
        exec_workdir=f"/proc/{pid}/cwd" if valid else None,
    )
except Exception as exc:
    emit(valid=False, error=f"{type(exc).__name__}: {exc}")
'''


def parse_terminal_cwd_probe(stdout):
    lines = stdout.splitlines()
    if len(lines) != 1:
        return {
            "status": "terminal_unresolved",
            "reason": "probe_output_not_single_json_line",
        }
    try:
        value = json.loads(lines[0])
    except (json.JSONDecodeError, TypeError):
        return {
            "status": "terminal_unresolved",
            "reason": "probe_output_invalid_json",
        }
    if not isinstance(value, dict):
        return {
            "status": "terminal_unresolved",
            "reason": "probe_output_not_object",
        }
    if value.get("status") == "no_active_terminal":
        return value
    if value.get("status") != "active_terminal":
        return {
            **value,
            "status": "terminal_unresolved",
        }
    cwd, source = normalize_agent_workdir(value.get("cwd"))
    if source != "active_terminal":
        return {
            **value,
            "status": "terminal_unresolved",
            "reason": "probe_cwd_rejected",
        }
    if (
        not isinstance(value.get("shell_pid"), int)
        or not isinstance(value.get("shell_start_ticks"), int)
        or not str(value.get("tty") or "").startswith("/dev/pts/")
        or value.get("exec_workdir")
        != f"/proc/{value.get('shell_pid')}/cwd"
    ):
        return {
            **value,
            "status": "terminal_unresolved",
            "reason": "probe_identity_incomplete",
        }
    return {**value, "cwd": cwd}


def detect_active_terminal_cwd(container_name):
    """Return a clean, single-record terminal/shell identity probe."""
    for attempt in range(3):
        try:
            completed = subprocess.run(
                [
                    "docker", "exec", "--user", "user",
                    "-e", "DISPLAY=:0",
                    "-e", "XAUTHORITY=/home/user/.Xauthority",
                    container_name,
                    "/usr/bin/python3", "-c", TERMINAL_CWD_PROBE,
                ],
                capture_output=True,
                text=True,
                timeout=10,
            )
        except Exception:
            return {
                "status": "terminal_unresolved",
                "reason": "probe_process_failed",
            }
        if completed.returncode != 0:
            return {
                "status": "terminal_unresolved",
                "reason": "probe_nonzero_exit",
                "stderr": str(completed.stderr or "")[-500:],
            }
        result = parse_terminal_cwd_probe(completed.stdout)
        transient_proc_race = (
            result.get("reason") == "probe_exception"
            and isinstance(result.get("error"), str)
            and "FileNotFoundError" in result["error"]
            and "/proc/" in result["error"]
        )
        if not transient_proc_race:
            return result
        if attempt < 2:
            time.sleep(0.1)
    return {
        "status": "no_active_terminal",
        "reason": "active_window_process_churn",
    }


def validate_active_terminal_workdir(container_name, probe):
    if probe.get("status") != "active_terminal":
        return probe
    try:
        completed = subprocess.run(
            [
                "docker", "exec", "--user", "user",
                container_name,
                "/usr/bin/python3", "-c", TERMINAL_CWD_VALIDATE,
                str(probe["shell_pid"]),
                str(probe["shell_start_ticks"]),
                probe["cwd"],
                probe["tty"],
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except Exception:
        return {
            **probe,
            "status": "terminal_unresolved",
            "reason": "workdir_validation_process_failed",
        }
    lines = completed.stdout.splitlines()
    try:
        validation = (
            json.loads(lines[0])
            if completed.returncode == 0 and len(lines) == 1
            else {"valid": False}
        )
    except (json.JSONDecodeError, TypeError):
        validation = {"valid": False}
    if (
        completed.returncode != 0
        or not validation.get("valid")
        or validation.get("shell_pid") != probe["shell_pid"]
        or validation.get("shell_start_ticks") != probe["shell_start_ticks"]
        or validation.get("cwd") != probe["cwd"]
        or validation.get("tty") != probe["tty"]
        or validation.get("exec_workdir") != probe["exec_workdir"]
    ):
        return {
            **probe,
            "status": "terminal_unresolved",
            "reason": "workdir_identity_changed_before_agent_start",
        }
    return {**probe, "validated": True}

# prompt 就是 OSWorld 的原始 instruction，不加任何策略指导。
# 任何"教它怎么省 token / 怎么用工具"的话都会污染 baseline：分数不再反映
# 模型 + MCP 的真实能力，也无法与其他 agent 的公开结果对比。
# 唯一的附加物是结束约定，harness 需要一个明确的终止信号。
#
# 第二个终止信号 TASK_INFEASIBLE 是补齐官方 harness 本来就有的通道，不是策略指导：
# OSWorld 有 27 道题的正确答案就是"做不到"，其判据（desktop_env.py:469）只认
# action_history 末尾的 "FAIL" 动作。我们的 agent 全程走 MCP、从不调 env.step()，
# action_history 恒为空，所以这 27 道题此前恒判 0 分——哪怕它把理由说得完全正确。
# 官方 GPT54Agent 的 OPERATOR_PROMPT 里有对应的一句（output exactly "[INFEASIBLE]"），
# lib_run_single.py 收到后调 env.step("FAIL")。措辞照抄官方，避免我们自己引入偏差。
#
# 代价是真实的：普通题一旦注入 FAIL 会被直接判 0（desktop_env.py:476），
# 所以这个出口被滥用会掉分。这正是加完之后要单独测量的东西。
PROMPT = """\
{instruction}

Provide any textual answer requested by the user normally. When the task is \
complete, put TASK_COMPLETE alone on the final non-empty line of your response. \
If no textual answer is requested, TASK_COMPLETE may be the entire response.
If the task is infeasible because of missing apps, permissions, contradictory \
requirements, or other hard blockers, briefly explain the blocker and put \
TASK_INFEASIBLE alone on the final non-empty line.
"""

# 严格哨兵：只认整词。官方的检测把任何含 "impossible"/"cannot be done" 的句子
# 都判成 infeasible，那会把"这个子步骤做不到但我换了条路"误判成放弃整道题。
INFEASIBLE_SENTINEL = "TASK_INFEASIBLE"
COMPLETE_SENTINEL = "TASK_COMPLETE"
COMPLETION_GRACE_SECONDS = 5.0
GUI_ACTION_ALIASES = {
    "double": "double_click",
    "right": "right_click",
}
GUI_ACTION_TYPES = {
    "screenshot", "click", "double_click", "right_click", "move", "drag",
    "scroll", "type", "keypress", "wait",
}


def split_completion_message(text):
    """Split a final-line harness status marker from user-facing answer text."""
    stripped = str(text or "").strip()
    if not stripped:
        return "", None
    lines = stripped.splitlines()
    sentinel = lines[-1].strip()
    if sentinel not in {COMPLETE_SENTINEL, INFEASIBLE_SENTINEL}:
        return stripped, None
    body = "\n".join(lines[:-1]).strip()
    return body, sentinel


def split_null_paths(payload):
    if not isinstance(payload, (bytes, bytearray)):
        raise TypeError("NUL-delimited path payload must be bytes")
    return [
        item.decode("utf-8", errors="surrogateescape")
        for item in payload.split(b"\0")
        if item
    ]


def artifact_find_command():
    predicates = " -o ".join(
        f"-name '*.{extension}'" for extension in ARTIFACT_EXTENSIONS
    )
    return (
        "find /home/user "
        "\\( -path '/home/user/.cache' "
        "-o -path '/home/user/.codex' "
        "-o -path '/home/user/.codex-judge' "
        "-o -path '/home/user/.npm' "
        "-o -path '*/node_modules' "
        "-o -path '*/.venv' "
        "-o -path '*/venv' "
        "-o -path '*/Cache' "
        "-o -path '*/Code Cache' "
        "-o -path '*/GPUCache' "
        "-o -path '*/Service Worker' \\) -prune "
        f"-o -type f \\( {predicates} \\) -print0 2>/dev/null"
    )


ARTIFACT_INVENTORY_PROBE = r'''
import hashlib
import json
import os
from pathlib import Path
import sys

home = Path("/home/user")
extensions = set(json.loads(sys.argv[1]))
explicit_names = set(json.loads(sys.argv[2]))
excluded_names = {
    ".cache", ".codex", ".codex-judge", ".npm", ".venv", "venv",
    "node_modules", "Cache", "Code Cache", "GPUCache", "Service Worker",
}
files = {}
for directory, dirnames, filenames in os.walk(home):
    dirnames[:] = [
        name for name in dirnames
        if name not in excluded_names
    ]
    base = Path(directory)
    if any(part in excluded_names for part in base.parts):
        continue
    for name in filenames:
        path = base / name
        extension = path.suffix.lower().lstrip(".")
        if extension not in extensions and name not in explicit_names:
            continue
        try:
            relative = path.relative_to(home).as_posix()
            stat = path.stat()
            digest = hashlib.sha256()
            with path.open("rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
            files[relative] = {
                "bytes": stat.st_size,
                "sha256": digest.hexdigest(),
            }
        except (OSError, ValueError):
            pass
payload = json.dumps(files, sort_keys=True, separators=(",", ":"))
print(json.dumps({
    "files": files,
    "file_count": len(files),
    "manifest_sha256": hashlib.sha256(payload.encode()).hexdigest(),
}, sort_keys=True))
'''


def capture_artifact_inventory(container_name):
    completed = subprocess.run(
        [
            "docker",
            "exec",
            "--user",
            "user",
            container_name,
            "/usr/bin/python3",
            "-c",
            ARTIFACT_INVENTORY_PROBE,
            json.dumps(ARTIFACT_EXTENSIONS),
            json.dumps(sorted(ARTIFACT_EXPLICIT_BASENAMES)),
        ],
        capture_output=True,
        text=True,
        timeout=300,
        check=True,
    )
    inventory = json.loads(completed.stdout)
    if not isinstance(inventory.get("files"), dict):
        raise RuntimeError("artifact inventory did not return a file map")
    return inventory


def export_task_artifacts(
    container_name,
    artifacts_dir,
    before_inventory=None,
):
    artifacts = Path(artifacts_dir)
    files_root = artifacts / "files"
    files_root.mkdir(parents=True, exist_ok=True)
    after_inventory = capture_artifact_inventory(container_name)
    before_files = (
        (before_inventory or {}).get("files") or {}
    )
    after_files = after_inventory["files"]
    changed_relative_paths = sorted(
        relative
        for relative, metadata in after_files.items()
        if before_files.get(relative) != metadata
    )
    removed_relative_paths = sorted(
        set(before_files) - set(after_files)
    )
    sources = [
        f"/home/user/{relative}"
        for relative in changed_relative_paths
    ]
    entries = []
    basename_counts = collections.Counter(
        Path(source).name for source in sources
    )
    for source in sources:
        try:
            relative = Path(source).relative_to("/home/user")
        except ValueError:
            continue
        if not relative.parts or ".." in relative.parts:
            continue
        destination = files_root / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        completed = subprocess.run(
            [
                "docker",
                "cp",
                f"{container_name}:{source}",
                str(destination),
            ],
            capture_output=True,
            text=True,
            timeout=300,
        )
        entry = {
            "source_path": source,
            "stored_path": destination.relative_to(artifacts).as_posix(),
            "copy_returncode": completed.returncode,
        }
        if completed.returncode != 0 or not destination.is_file():
            entry["error"] = (
                completed.stderr or completed.stdout or "copy failed"
            )[-1000:]
            entries.append(entry)
            continue
        digest = hashlib.sha256()
        with destination.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
        entry.update(
            {
                "bytes": destination.stat().st_size,
                "sha256": digest.hexdigest(),
            }
        )
        entries.append(entry)
        if basename_counts[destination.name] == 1:
            compatibility_path = artifacts / destination.name
            if not compatibility_path.exists():
                try:
                    os.link(destination, compatibility_path)
                except OSError:
                    pass
    manifest = {
        "root": "/home/user",
        "path_preserving_root": "files",
        "before_manifest_sha256": (
            (before_inventory or {}).get("manifest_sha256")
        ),
        "after_manifest_sha256": after_inventory.get("manifest_sha256"),
        "candidate_file_count_before": len(before_files),
        "candidate_file_count_after": len(after_files),
        "changed_or_added_paths": changed_relative_paths,
        "removed_paths": removed_relative_paths,
        "file_count": len(entries),
        "copied_count": sum(
            entry.get("copy_returncode") == 0 for entry in entries
        ),
        "failed_count": sum(
            entry.get("copy_returncode") != 0 for entry in entries
        ),
        "files": entries,
    }
    (artifacts / "artifact_manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return manifest


def export_ax_telemetry(
    container_name,
    trace_dir,
    telemetry_path=LEGACY_AX_TELEMETRY_PATH,
):
    destination = Path(trace_dir, "ax_telemetry.jsonl")
    completed = subprocess.run(
        [
            "docker",
            "cp",
            f"{container_name}:{telemetry_path}",
            str(destination),
        ],
        capture_output=True,
        text=True,
        timeout=60,
    )
    if completed.returncode != 0:
        missing = "Could not find the file" in (
            (completed.stderr or "") + (completed.stdout or "")
        )
        if missing:
            return {
                "present": False,
                "path": None,
                "bytes": 0,
            }
        raise RuntimeError(
            completed.stderr or completed.stdout or "telemetry copy failed"
        )
    if not destination.is_file():
        raise RuntimeError("telemetry copy returned success without a file")
    return {
        "present": True,
        "path": destination.name,
        "bytes": destination.stat().st_size,
    }


def capture_pre_agent_state(env, container_name, task_id, trace_dir):
    if task_id != VLC_TASK:
        return None

    state = {
        "task_id": task_id,
        "captured_at_utc": datetime.now(timezone.utc).isoformat(),
    }
    try:
        screenshot = env.controller.get_screenshot()
        if screenshot:
            screenshot_path = os.path.join(
                trace_dir, "pre_agent_screenshot.png"
            )
            with open(screenshot_path, "wb") as output:
                output.write(screenshot)
            state["screenshot"] = os.path.basename(screenshot_path)
    except Exception as exc:
        state["screenshot_error"] = f"{type(exc).__name__}: {exc}"

    try:
        accessibility_tree = env.controller.get_accessibility_tree() or ""
        state["accessibility_tree"] = accessibility_tree
        state["privacy_dialog_present"] = (
            "Privacy and Network Access Policy" in accessibility_tree
        )
    except Exception as exc:
        state["accessibility_error"] = f"{type(exc).__name__}: {exc}"
        state["privacy_dialog_present"] = None

    try:
        windows = subprocess.run(
            [
                "docker",
                "exec",
                container_name,
                "bash",
                "-lc",
                "DISPLAY=:0 wmctrl -l 2>&1",
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        state["windows"] = windows.stdout
        state["windows_returncode"] = windows.returncode
        if "Privacy and Network Access Policy" in windows.stdout:
            state["privacy_dialog_present"] = True
    except Exception as exc:
        state["windows_error"] = f"{type(exc).__name__}: {exc}"

    with open(os.path.join(trace_dir, "pre_agent_state.json"), "w") as output:
        json.dump(state, output, ensure_ascii=False, indent=2)
    return state


def _tool_short_name(name):
    name = str(name or "")
    if name.startswith("mcp__"):
        name = name.split("__")[-1]
    return name.rsplit("/", 1)[-1].rsplit(".", 1)[-1]


def gui_action_types(arguments):
    """Return valid canonical action types from one unified gui invocation."""
    if isinstance(arguments, str):
        try:
            arguments = json.loads(arguments)
        except (TypeError, ValueError):
            return []
    if not isinstance(arguments, dict) or not isinstance(arguments.get("actions"), list):
        return []

    action_types = []
    for action in arguments["actions"]:
        if not isinstance(action, dict):
            continue
        action_type = str(action.get("type") or "").strip().lower()
        action_type = GUI_ACTION_ALIASES.get(action_type, action_type)
        if action_type in GUI_ACTION_TYPES:
            action_types.append(action_type)
    return action_types


def prepare_skill_injection(skill_file=None):
    """Validate and load an explicitly selected benchmark-only skill.

    Repository development instructions must never enter the evaluated agent's
    context. Resolve symlinks before checking names so a harmless-looking link
    cannot point at AGENTS.md or CLAUDE.md.
    """
    configured_path = (os.environ.get("OSW_SKILL_FILE", "")
                       if skill_file is None else skill_file)
    if not configured_path:
        return None, {"enabled": False, "path": None, "hash": None}

    requested = Path(configured_path).expanduser()
    try:
        resolved = requested.resolve(strict=True)
    except (OSError, RuntimeError) as exc:
        raise ValueError(
            f"OSW_SKILL_FILE does not resolve to an existing file: {requested}"
        ) from exc

    requested_name = requested.name.casefold()
    resolved_name = resolved.name.casefold()
    if (requested_name in FORBIDDEN_SKILL_BASENAMES
            or resolved_name in FORBIDDEN_SKILL_BASENAMES):
        raise ValueError(
            "OSW_SKILL_FILE must not use or resolve to AGENTS.md or CLAUDE.md"
        )

    project_agents = (Path(ROOT) / "AGENTS.md").resolve()
    if resolved == project_agents:
        raise ValueError("OSW_SKILL_FILE must not reference the project AGENTS.md")
    if not resolved.is_file():
        raise ValueError(f"OSW_SKILL_FILE is not a regular file: {resolved}")

    raw = resolved.read_bytes()
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ValueError("OSW_SKILL_FILE must be valid UTF-8 text") from exc

    normalized = " ".join(text.casefold().split())
    marker = next(
        (item for item in DEVELOPMENT_INSTRUCTION_MARKERS if item in normalized),
        None,
    )
    if marker:
        raise ValueError(
            "OSW_SKILL_FILE declares repository development instructions "
            f"(matched {marker!r})"
        )
    helper = PROHIBITED_AGENT_HELPER_PATTERN.search(text)
    if helper:
        raise ValueError(
            "OSW_SKILL_FILE advertises a repository-authored convenience "
            f"helper ({helper.group(0)!r}); evaluated skills may name only "
            "standard upstream tools and libraries"
        )

    digest = hashlib.sha256(raw).hexdigest()
    return text, {
        "enabled": True,
        "path": str(resolved),
        "hash": f"sha256:{digest}",
    }


def inspect_prohibited_agent_helpers(container_name):
    completed = subprocess.run(
        [
            "docker",
            "exec",
            "--user",
            "user",
            container_name,
            "/usr/bin/python3",
            "-c",
            PROHIBITED_HELPER_PROBE,
        ],
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    )
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            "agent helper policy probe returned invalid JSON"
        ) from exc
    if not isinstance(payload, list) or any(
        not isinstance(item, str) for item in payload
    ):
        raise RuntimeError("agent helper policy probe returned invalid data")
    return payload


def sample_resources(container_name, out_path, stop_event, interval=3.0):
    """采样容器资源。跑并发前需要真实的单容器占用曲线，估算值不可靠。"""
    import docker as docker_sdk
    client = docker_sdk.from_env()
    try:
        ctr = client.containers.get(container_name)
    except Exception:
        return
    with open(out_path, "w") as fh:
        while not stop_event.is_set():
            try:
                s = ctr.stats(stream=False)
                cpu = s["cpu_stats"]; pre = s["precpu_stats"]
                cpu_delta = cpu["cpu_usage"]["total_usage"] - pre["cpu_usage"].get("total_usage", 0)
                sys_delta = cpu["system_cpu_usage"] - pre.get("system_cpu_usage", 0)
                ncpu = cpu.get("online_cpus") or 1
                pct = (cpu_delta / sys_delta) * ncpu * 100 if sys_delta > 0 else 0.0
                mem = s["memory_stats"].get("usage", 0)
                cache = s["memory_stats"].get("stats", {}).get("file", 0)
                fh.write(json.dumps({
                    "t": round(time.time(), 2),
                    "cpu_pct": round(pct, 2),
                    "mem_mb": round((mem - cache) / 1048576, 1),
                    "mem_total_mb": round(mem / 1048576, 1),
                }) + "\n")
                fh.flush()
            except Exception:
                pass
            stop_event.wait(interval)


def summarize_trace(jsonl_path):
    """从 Codex 事件流里抽出可比较的指标。turn 数和 token 是跑分报告的核心列。"""
    n_events = n_tool = n_reason = n_msg = 0
    n_shell = 0
    tools = {}
    tok_in = tok_out = tok_reason = 0
    cached_in = 0
    compactions = 0
    turn_completed_usage_detected = False
    gui_actions = collections.Counter()
    agent_error = None
    last_msg = ""
    last_substantive_msg = ""
    saw_infeasible = False
    with open(jsonl_path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except Exception:
                continue
            n_events += 1
            if ev.get("type") == "turn.failed":
                failure = ev.get("error") or {}
                agent_error = failure.get("message") or str(failure) or "turn failed"
            item = ev.get("item") or {}
            itype = item.get("type", "")
            # item.started 与 item.completed 携带同一个 item，只认 completed，
            # 否则所有计数翻倍（比例不变，故不易察觉）
            if ev.get("type") == "item.started":
                continue

            if itype == "reasoning":
                n_reason += 1
            elif itype == "agent_message":
                n_msg += 1
                t = item.get("text", "")
                if t.strip():
                    body, sentinel = split_completion_message(t)
                    if sentinel == INFEASIBLE_SENTINEL:
                        saw_infeasible = True
                    last_msg = t.strip()[:400]
                    if body:
                        last_substantive_msg = body[:4000]
            elif itype == "error":
                # context 压缩会实质降低准确性，必须计数并在报告里体现
                if "compaction" in item.get("message", "").lower():
                    compactions += 1
            elif itype == "mcp_tool_call":
                name = item.get("tool")
                if name:
                    tools[name] = tools.get(name, 0) + 1
                    n_tool += 1
                    if _tool_short_name(name).lower() == "gui":
                        gui_actions.update(gui_action_types(item.get("arguments")))
            elif itype in ("command_execution", "local_shell_call"):
                # shell 本应被 features.shell_tool=false 关死。这里仍然计数：
                # 一旦哪次配置没生效，分数会静默变成"shell 能力"而不是 GUI 能力，
                # 不留计数就永远发现不了。
                n_shell += 1

            # turn.completed 带全量 usage
            u = ev.get("usage") or {}
            if isinstance(u, dict) and u:
                if ev.get("type") == "turn.completed":
                    turn_completed_usage_detected = True
                tok_in = max(tok_in, u.get("input_tokens", 0) or 0)
                tok_out = max(tok_out, u.get("output_tokens", 0) or 0)
                tok_reason = max(tok_reason, u.get("reasoning_output_tokens", 0) or 0)
                cached_in = max(cached_in, u.get("cached_input_tokens", 0) or 0)

    return {"events": n_events, "tool_calls": n_tool, "reasoning_items": n_reason,
            "agent_messages": n_msg, "compaction_warnings": compactions,
            "shell_calls": n_shell,
            "tool_breakdown": tools, "input_tokens": tok_in,
            "cached_input_tokens": cached_in,
            "output_tokens": tok_out, "reasoning_tokens": tok_reason,
            "gui_actions": sum(gui_actions.values()),
            "gui_action_breakdown": dict(gui_actions),
            "last_message": last_msg,
            "last_substantive_message": last_substantive_msg,
            "saw_infeasible": saw_infeasible,
            "agent_error": agent_error,
            "turn_completed_usage_detected": turn_completed_usage_detected,
            "token_usage_source": "codex_trace"}


def collect_task_proxy_usage(token_log_path, task_id):
    """Aggregate proxy usage for one exact Task Agent affinity key."""
    usage = {
        "status": "missing",
        "request_attempts": 0,
        "accounted_responses": 0,
        "input_tokens": 0,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "total_tokens": 0,
    }
    if not token_log_path:
        return usage
    path = Path(token_log_path)
    if not path.is_file():
        return usage

    usage["status"] = "no_matching_rows"
    with path.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            affinity_task = row.get("task")
            if (
                not isinstance(affinity_task, str)
                or affinity_task.startswith("judge-")
                or affinity_task != task_id
            ):
                continue
            usage["request_attempts"] += 1
            if row.get("input_tokens") is None:
                continue
            usage["accounted_responses"] += 1
            usage["input_tokens"] += int(row.get("input_tokens") or 0)
            usage["cached_input_tokens"] += int(
                row.get("cached_tokens") or 0
            )
            usage["output_tokens"] += int(row.get("output_tokens") or 0)
            usage["reasoning_tokens"] += int(
                row.get("reasoning_tokens") or 0
            )
            usage["total_tokens"] += int(row.get("total_tokens") or 0)
    if usage["request_attempts"]:
        usage["status"] = "completed"
    return usage


def recover_truncated_token_usage(
    summary,
    token_log_path,
    task_id,
    *,
    truncated,
):
    """Recover usage only when a truncated trace lacks turn.completed usage."""
    if not truncated or summary.get("turn_completed_usage_detected"):
        return summary

    trace_usage = {
        key: summary.get(key)
        for key in (
            "input_tokens",
            "cached_input_tokens",
            "output_tokens",
            "reasoning_tokens",
        )
    }
    proxy_usage = collect_task_proxy_usage(token_log_path, task_id)
    summary["trace_usage"] = trace_usage
    summary["proxy_usage"] = proxy_usage
    summary["proxy_token_log"] = (
        os.path.abspath(token_log_path) if token_log_path else None
    )
    if proxy_usage["accounted_responses"]:
        for key in trace_usage:
            summary[key] = proxy_usage[key]
        summary["token_usage_source"] = "proxy_token_log"
        summary["token_usage_recovered"] = True
    else:
        summary["token_usage_source"] = "unavailable"
        summary["token_usage_recovered"] = False
    return summary

def codex_completion_sentinel(line):
    """Return the exact completed Codex sentinel carried by one JSONL event."""
    try:
        event = json.loads(line)
    except (TypeError, ValueError):
        return None
    if event.get("type") != "item.completed":
        return None
    item = event.get("item") or {}
    if item.get("type") != "agent_message":
        return None
    _, sentinel = split_completion_message(item.get("text"))
    return sentinel


def claude_completion_sentinel(line):
    """Return a completion sentinel carried by one Claude stream event."""
    try:
        event = json.loads(line)
    except (TypeError, ValueError):
        return None
    texts = []
    if event.get("type") == "result" and isinstance(event.get("result"), str):
        texts.append(event["result"])
    elif event.get("type") == "assistant":
        for block in (event.get("message") or {}).get("content", []) or []:
            if block.get("type") == "text":
                texts.append(str(block.get("text") or ""))
    for text in reversed(texts):
        _, sentinel = split_completion_message(text)
        if sentinel is not None:
            return sentinel
    return None


def terminate_completed_codex(container_name, proc):
    """End a Codex process that kept an exec session alive after its sentinel."""
    if proc.poll() is not None:
        return False
    try:
        subprocess.run(
            [
                "docker",
                "exec",
                container_name,
                "bash",
                "-lc",
                "pids=$(pgrep -x codex || true); "
                "[ -z \"$pids\" ] || kill -TERM $pids",
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
    except Exception:
        pass
    try:
        proc.terminate()
    except Exception:
        pass
    return True


def summarize_claude_trace(jsonl_path):
    """解析 claude code 的 stream-json。

    与 codex 的事件流是两套结构：工具调用在 assistant 消息的 content[] 里以
    type=tool_use 出现，工具名带 `mcp__<server>__<tool>` 前缀；结果在下一条
    user 消息的 tool_result 里。thinking 以 type=thinking 的 content 块出现。
    """
    n_events = n_tool = n_reason = n_msg = n_shell = 0
    tools = {}
    tok_in = tok_out = tok_reason = cached_in = 0
    last_msg = ""
    last_substantive_msg = ""
    saw_infeasible = False
    gui_actions = collections.Counter()
    agent_error = None
    SHELL_TOOLS = {"Bash", "BashOutput", "KillShell"}

    with open(jsonl_path, errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except Exception:
                continue
            n_events += 1
            et = ev.get("type")

            if et == "assistant":
                for blk in (ev.get("message") or {}).get("content", []) or []:
                    bt = blk.get("type")
                    if bt == "thinking":
                        n_reason += 1
                    elif bt == "text" and blk.get("text", "").strip():
                        n_msg += 1
                        body, sentinel = split_completion_message(blk["text"])
                        if sentinel == INFEASIBLE_SENTINEL:
                            saw_infeasible = True
                        last_msg = blk["text"].strip()[:400]
                        if body:
                            last_substantive_msg = body[:4000]
                    elif bt == "tool_use":
                        name = blk.get("name", "")
                        if name in SHELL_TOOLS:
                            n_shell += 1
                        elif name.startswith("mcp__"):
                            # mcp__open_computer_use__click -> click
                            short = name.split("__")[-1]
                            tools[short] = tools.get(short, 0) + 1
                            n_tool += 1
                            if _tool_short_name(name).lower() == "gui":
                                gui_actions.update(gui_action_types(blk.get("input")))

            elif et == "result":
                u = ev.get("usage") or {}
                tok_in = u.get("input_tokens", 0) or 0
                tok_out = u.get("output_tokens", 0) or 0
                cached_in = u.get("cache_read_input_tokens", 0) or 0
                if isinstance(ev.get("result"), str) and ev["result"].strip():
                    body, sentinel = split_completion_message(ev["result"])
                    if sentinel == INFEASIBLE_SENTINEL:
                        saw_infeasible = True
                    last_msg = ev["result"].strip()[:400]
                    if body:
                        last_substantive_msg = body[:4000]
                if ev.get("is_error"):
                    agent_error = str(ev.get("result") or ev.get("error") or "agent failed")

    return {"events": n_events, "tool_calls": n_tool, "reasoning_items": n_reason,
            "agent_messages": n_msg, "compaction_warnings": 0, "shell_calls": n_shell,
            "tool_breakdown": tools, "input_tokens": tok_in,
            "cached_input_tokens": cached_in, "output_tokens": tok_out,
            "reasoning_tokens": tok_reason,
            "gui_actions": sum(gui_actions.values()),
            "gui_action_breakdown": dict(gui_actions),
            "last_message": last_msg,
            "last_substantive_message": last_substantive_msg,
            "saw_infeasible": saw_infeasible,
            "agent_error": agent_error}


def score_agent_run(
    evaluator_score,
    summary,
    agent_returncode=0,
    timed_out=False,
    completion_boundary=False,
    task_is_infeasible=False,
):
    """Keep evaluator evidence without scoring an infrastructure-failed agent run."""
    agent_error = summary.get("agent_error")
    if agent_error:
        return None, f"agent failed before completion: {agent_error}"
    if timed_out:
        return None, "agent run timed out before a normal completion boundary"
    if (
        summary.get("saw_infeasible")
        and not task_is_infeasible
        and (summary.get("tool_calls") or 0) == 0
        and (summary.get("shell_calls") or 0) == 0
    ):
        return (
            None,
            "agent declared infeasible without any observable tool or shell check",
        )
    if agent_returncode not in (None, 0) and not completion_boundary:
        return None, f"agent process exited with code {agent_returncode}"
    return evaluator_score, None


def build_failure_result(
    task_id,
    instruction,
    model,
    effort,
    agent,
    skill_injection,
    phase,
    exc,
    tool_release=None,
    tool_candidate=None,
):
    setup_failure = phase in {"container_init", "setup", "agent_prepare"}
    evaluation_failure = phase == "evaluate"
    status = (
        "setup_failure"
        if setup_failure
        else "evaluation_failure"
        if evaluation_failure
        else "error"
    )
    result = {
        "task_id": task_id,
        "runner_run_id": os.environ.get("OSW_HYBRID_RUN_ID"),
        "batch_run_id": os.environ.get("OSW_BATCH_RUN_ID"),
        "instruction": instruction,
        "model": model,
        "effort": effort,
        "score": None,
        "evaluator_score": None,
        "error": f"{type(exc).__name__}: {exc}",
        "status": status,
        "setup_failure": setup_failure,
        "evaluation_failure": evaluation_failure,
        "truncated": False,
        "agent": agent,
        "bash_enabled": os.environ.get("OSW_ENABLE_SHELL", "0") == "1",
        "skill_injection": skill_injection,
        "failure_phase": phase,
    }
    result.update(result_tool_metadata(tool_release))
    result.update(result_tool_candidate_metadata(tool_candidate))
    return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--task", default=DEFAULT_TASK)
    ap.add_argument("--image", default="osw-hybrid:v0.14")
    ap.add_argument(
        "--tool-version",
        default=os.environ.get("OSW_TOOL_VERSION", ""),
        help="产品工具版本；留空表示不受发布清单约束的诊断运行",
    )
    ap.add_argument(
        "--tool-candidate-manifest",
        default=os.environ.get("OSW_TOOL_CANDIDATE_MANIFEST", ""),
        help="未发布工具候选清单；与 baseline --tool-version 一起严格校验",
    )
    ap.add_argument("--model", default="gpt-5.5")
    ap.add_argument("--effort", default="xhigh")
    ap.add_argument("--gw-port", type=int, default=8600)
    ap.add_argument("--timeout", type=int, default=1800)
    ap.add_argument(
        "--proxy-token-log",
        default=os.environ.get("OSW_PROXY_TOKEN_LOG", ""),
        help=(
            "wrapper-owned token proxy JSONL; used only to recover exact-task "
            "usage when a truncated Codex trace lacks turn.completed"
        ),
    )
    ap.add_argument(
        "--agent",
        default="codex",
        choices=["codex", "claude", "claude-code"],
                    help="容器内跑哪个 coding agent")
    ap.add_argument(
        "--claude-code-binary",
        default=(
            str(REPO_ROOT / "artifacts" / "claude-code")
        ),
    )
    ap.add_argument(
        "--claude-mcp-adapter",
        default=str(
            REPO_ROOT
            / "integrations/common/claude_mcp_schema_adapter.py"
        ),
    )
    ap.add_argument(
        "--claude-gateway-base",
        default=os.environ.get(
            "OSW_ANTHROPIC_BASE_URL",
            "http://172.17.0.1:8537",
        ),
        help=(
            "Anthropic-compatible coproxy base reachable from the OSWorld "
            "container bridge."
        ),
    )
    ap.add_argument(
        "--judge-model",
        default="",
        help=(
            "封存 Task Agent 最终状态并完成 RAW evaluator 后，"
            "在同一容器启动独立 interactive outcome Judge；留空禁用"
        ),
    )
    ap.add_argument("--judge-effort", default="xhigh")
    ap.add_argument("--judge-timeout", type=int, default=420)
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()
    tool_release = None
    tool_candidate = None
    if args.tool_version:
        try:
            tool_release = tool_release_record(args.tool_version)
            if args.tool_candidate_manifest:
                tool_candidate = tool_candidate_record(
                    args.tool_candidate_manifest
                )
                if (
                    tool_candidate["baseline_tool_version"]
                    != args.tool_version
                ):
                    raise ValueError(
                        "tool candidate baseline mismatch: "
                        f"{tool_candidate['baseline_tool_version']} != "
                        f"{args.tool_version}"
                    )
                assert_tool_candidate_image(
                    args.tool_candidate_manifest,
                    args.image,
                )
            else:
                assert_tool_release_image(args.tool_version, args.image)
        except ValueError as exc:
            print(str(exc), file=sys.stderr)
            return 2
        os.environ["OSW_TOOL_VERSION"] = args.tool_version
        if args.tool_candidate_manifest:
            os.environ["OSW_TOOL_CANDIDATE_MANIFEST"] = (
                args.tool_candidate_manifest
            )
    runtime_record = tool_candidate or tool_release or {}
    mcp_command = runtime_record.get(
        "tool_mcp_command",
        LEGACY_MCP_COMMAND,
    )
    ax_telemetry_path = runtime_record.get(
        "tool_ax_telemetry_path",
        LEGACY_AX_TELEMETRY_PATH,
    )

    task_path = os.path.join(
        str(OSWORLD_ROOT),
        "evaluation_examples/examples",
        args.task,
    )
    task_config = json.load(open(task_path))
    task_id = os.path.splitext(os.path.basename(args.task))[0]
    try:
        assert_task_allowed(task_id, task_config)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    instruction = task_config.get("instruction", "")

    # trace 根目录可由环境变量隔离：开/关 bash 两轮要并行跑，共用一个目录会互相覆盖
    trace_root = os.environ.get("OSW_TRACE_ROOT") or os.path.join(ROOT, "trace")
    trace_dir = os.path.join(trace_root, task_id)
    os.makedirs(trace_dir, exist_ok=True)
    codex_jsonl = os.path.join(trace_dir, "agent.jsonl")
    res_jsonl = os.path.join(trace_dir, "resource.jsonl")

    # Validate before creating a container. Only an explicitly selected,
    # benchmark-specific skill may be injected; repository AGENTS instructions
    # are rejected even through symlinks.
    skill_text, skill_injection = prepare_skill_injection()

    print("=" * 74)
    print(f"任务 {task_id}")
    print(f"指令 {instruction[:160]}")
    print(f"模型 {args.model} (effort={args.effort})   轨迹 {trace_dir}")
    print("=" * 74)

    os.environ["OSW_HYBRID_IMAGE"] = args.image
    env = None
    termination_requested = {"hit": False}

    def handle_termination(_signum, _frame):
        termination_requested["hit"] = True
        if env is not None:
            raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, handle_termination)
    cname = ""
    stop_event = threading.Event()
    sampler = None
    artifact_inventory_before = None
    phase = "container_init"
    try:
        from desktop_env.desktop_env import DesktopEnv

        requested_image_id = subprocess.run(
            [
                "docker", "image", "inspect", args.image,
                "--format", "{{.Id}}",
            ],
            capture_output=True,
            text=True,
            timeout=30,
            check=True,
        ).stdout.strip()
        if args.tool_version:
            if tool_candidate:
                assert_tool_candidate_image(
                    args.tool_candidate_manifest,
                    args.image,
                    requested_image_id,
                )
            else:
                assert_tool_release_image(
                    args.tool_version,
                    args.image,
                    requested_image_id,
                )
        env = DesktopEnv(provider_name="hybrid", action_space="pyautogui", headless=True,
                         require_a11y_tree=False, os_type="Ubuntu",
                         cache_dir=os.path.join(ROOT, "cache"))
        cname = env.provider.get_container_name()
        if termination_requested["hit"]:
            raise KeyboardInterrupt
        print(f"\n[1] 容器 {cname} 就绪")
        actual_image_id = subprocess.run(
            [
                "docker", "inspect", cname,
                "--format", "{{.Image}}",
            ],
            capture_output=True,
            text=True,
            timeout=30,
            check=True,
        ).stdout.strip()
        if actual_image_id != requested_image_id:
            raise RuntimeError(
                "container image mismatch: "
                f"requested {requested_image_id}, actual {actual_image_id}"
            )
        assert_container_mount_isolation(cname)
        clear_guest_agent_context(cname)
        prohibited_helpers = inspect_prohibited_agent_helpers(cname)
        if prohibited_helpers:
            raise RuntimeError(
                "evaluated image exposes prohibited repository-authored "
                "convenience helpers on the agent PATH: "
                + ", ".join(prohibited_helpers)
            )

        phase = "setup"
        env.reset(task_config=task_config)
        print("[2] setup 完成，任务初始状态已就位")
        pre_agent_state = capture_pre_agent_state(
            env, cname, task_id, trace_dir
        )
        if pre_agent_state:
            print(
                "[2b] 已保存 pre-agent setup 证据 "
                f"privacy_dialog={pre_agent_state.get('privacy_dialog_present')}"
            )
        phase = "agent_prepare"
        # 把 codex 配置写进容器
            # 由环境变量决定这一轮的 bash 口径，默认关闭
        enable_shell = os.environ.get("OSW_ENABLE_SHELL", "0") == "1"
        if args.agent == "codex":
            shell_features = ("[features]\nshell_tool = true\nunified_exec = true"
                              if enable_shell else
                              "[features]\nshell_tool = false\nunified_exec = false")
            cfg = build_codex_config(
                args.model,
                args.effort,
                args.gw_port,
                shell_features,
                task_id,
                mcp_command=mcp_command,
            )
            subprocess.run(
                [
                    "docker", "exec", "--user", "root", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"mkdir -p {GUEST_CODEX_HOME} "
                    "&& chown -R user:user /opt/osw-runtime",
                ],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                [
                    "docker", "exec", "-i", "--user", "user", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"mkdir -p {GUEST_CODEX_HOME} "
                    f"&& cat > {GUEST_CODEX_HOME}/config.toml",
                ],
                input=cfg,
                text=True,
                check=True,
                capture_output=True,
            )
            subprocess.run(
                [
                    "docker", "exec", "-i", "--user", "user", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"cat > {GUEST_CODEX_BASE_INSTRUCTIONS}",
                ],
                input=Path(CODEX_BASE_INSTRUCTIONS_PATH).read_text(
                    encoding="utf-8"
                ),
                text=True,
                check=True,
                capture_output=True,
            )
            print("[3] 容器内 codex 配置已写入")
        else:
            claude_binary = Path(args.claude_code_binary).resolve()
            claude_adapter = Path(args.claude_mcp_adapter).resolve()
            for required in (claude_binary, claude_adapter):
                if not required.is_file():
                    raise FileNotFoundError(
                        f"required Claude Code artifact missing: {required}"
                    )
            subprocess.run(
                [
                    "docker", "exec", "--user", "root", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"mkdir -p {GUEST_CLAUDE_HOME} /home/user/.local/bin "
                    f"&& chown -R user:user {GUEST_CLAUDE_HOME} "
                    "/home/user/.local",
                ],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["docker", "cp", str(claude_binary), f"{cname}:{GUEST_CLAUDE_CODE}"],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                [
                    "docker", "cp", str(claude_adapter),
                    f"{cname}:{GUEST_CLAUDE_MCP_ADAPTER}",
                ],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                [
                    "docker", "exec", "--user", "root", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"chown user:user {GUEST_CLAUDE_CODE} "
                    f"{GUEST_CLAUDE_MCP_ADAPTER} "
                    f"&& chmod 0755 {GUEST_CLAUDE_CODE} "
                    f"{GUEST_CLAUDE_MCP_ADAPTER}",
                ],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                [
                    "docker", "exec", "-i", "--user", "user", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"cat > {GUEST_CLAUDE_MCP_CONFIG}",
                ],
                input=claude_mcp_config(mcp_command),
                text=True,
                check=True,
                capture_output=True,
            )
            claude_instructions = Path(
                CODEX_BASE_INSTRUCTIONS_PATH
            ).read_text(encoding="utf-8")
            if skill_injection["enabled"]:
                claude_instructions += "\n\n" + skill_text
            subprocess.run(
                [
                    "docker", "exec", "-i", "--user", "user", cname,
                    "bash", "--noprofile", "--norc", "-c",
                    f"cat > {GUEST_CLAUDE_BASE_INSTRUCTIONS}",
                ],
                input=claude_instructions,
                text=True,
                check=True,
                capture_output=True,
            )
            print(
                "[3] Claude Code 固定产物与 MCP schema adapter 已安装 "
                f"(model={args.model}, effort={args.effort})"
            )

        # Codex 从 AGENTS.md 读取经过校验的 benchmark skill。Claude Code
        # 只通过替换式 system prompt 文件读取，不在任务目录留下可被自动
        # 发现或重复加载的 CLAUDE.md。
        # 刻意不走 user prompt——instruction 必须保持 OSWorld 原样，任何附加
        # 内容混进去都会让分数失去可比性。这里只在运行时写容器工作目录，
        # 不会把宿主文件复制进镜像。
        if skill_injection["enabled"] and args.agent == "codex":
            subprocess.run(["docker", "exec", "-i", cname, "bash", "-lc",
                            "cat > /home/user/AGENTS.md"],
                           input=skill_text, text=True, check=True, capture_output=True)
            print(f"[3b] 已注入测试 skill {skill_injection['path']} "
                  f"({len(skill_text)} 字符, {skill_injection['hash']})")
        elif skill_injection["enabled"]:
            print(
                "[3b] benchmark skill 已合并进 Claude Code 替换式 system prompt"
            )
        else:
            print("[3b] 未启用 benchmark skill 注入")

        artifact_inventory_before = capture_artifact_inventory(cname)
        Path(trace_dir, "pre_agent_artifact_inventory.json").write_text(
            json.dumps(
                artifact_inventory_before,
                ensure_ascii=False,
                indent=2,
            ),
            encoding="utf-8",
        )
        print(
            "[3c] 已封存 Task Agent 启动前产物基线 "
            f"files={artifact_inventory_before['file_count']}"
        )

        workdir_probe = detect_active_terminal_cwd(cname)
        if workdir_probe.get("status") == "terminal_unresolved":
            raise RuntimeError(
                "active terminal cwd is not uniquely provable: "
                + str(workdir_probe.get("reason") or "unknown")
                + " "
                + json.dumps(workdir_probe, ensure_ascii=False, sort_keys=True)
            )
        if workdir_probe.get("status") == "active_terminal":
            workdir_probe = validate_active_terminal_workdir(
                cname, workdir_probe
            )
            if workdir_probe.get("status") != "active_terminal":
                raise RuntimeError(
                    "active terminal cwd changed before agent start: "
                    + str(workdir_probe.get("reason") or "unknown")
                    + " "
                    + json.dumps(
                        workdir_probe, ensure_ascii=False, sort_keys=True
                    )
                )
            agent_workdir = workdir_probe["cwd"]
            agent_exec_workdir = workdir_probe["exec_workdir"]
            agent_workdir_source = "active_terminal"
        else:
            agent_workdir = "/home/user"
            agent_exec_workdir = "/home/user"
            agent_workdir_source = "fallback"
        print(
            f"[3d] agent workdir={agent_workdir} "
            f"exec_workdir={agent_exec_workdir} "
            f"source={agent_workdir_source}"
        )

        sampler = threading.Thread(target=sample_resources,
                                   args=(cname, res_jsonl, stop_event), daemon=True)
        sampler.start()

        prompt = PROMPT.format(instruction=instruction)
        if args.agent == "codex":
            # --json 输出完整事件流（含 reasoning），是轨迹的全部来源
            cmd = build_guarded_agent_command(
                cname,
                agent_exec_workdir,
                agent_workdir,
                "source /home/user/a11y-env.sh && "
                "exec /usr/bin/codex exec --ephemeral "
                "--json --skip-git-repo-check -",
                extra_env={
                    "CODEX_HOME": GUEST_CODEX_HOME,
                    "CODEX_SQLITE_HOME": GUEST_CODEX_HOME,
                },
            )
        else:
            inner = build_claude_agent_command(
                args.model,
                args.effort,
                enable_shell,
                runtime_record.get("tool_public_tools"),
            )
            cmd = build_guarded_agent_command(
                cname,
                agent_exec_workdir,
                agent_workdir,
                inner,
                extra_env={
                    "CLAUDE_CONFIG_DIR": GUEST_CLAUDE_HOME,
                    "ANTHROPIC_BASE_URL": claude_gateway_endpoint(
                        args.claude_gateway_base,
                        task_id,
                    ),
                    "ANTHROPIC_AUTH_TOKEN": "osw-hybrid-task-proxy",
                    "CLAUDE_CODE_SKIP_AUTH_LOGIN": "true",
                    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
                    "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
                    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0",
                    "DISABLE_NON_ESSENTIAL_MODEL_CALLS": "1",
                    "CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY": "1",
                    "DISABLE_COST_WARNINGS": "1",
                },
            )
        print(f"[4] 启动容器内 Codex（超时 {args.timeout}s）...\n")

        t0 = time.time()
        agent_start_evidence = None
        with open(codex_jsonl, "w") as trace_fh:
            proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, text=True, bufsize=1)
            proc.stdin.write(prompt)
            proc.stdin.close()

            # 真正的超时必须能打断 stdout 读取循环本身。之前把 proc.wait(timeout=)
            # 放在读循环之后，而读循环一直读到 EOF——agent 不结束就永远到不了 wait，
            # 超时形同虚设（实测跑了 2350s，设的是 1800s）。用定时器直接 kill。
            timed_out = {"hit": False}
            completion = {
                "detected": False,
                "sentinel": None,
                "termination_requested": False,
            }
            completion_timer = {"value": None}

            def _kill_on_timeout():
                timed_out["hit"] = True
                try:
                    proc.kill()
                except Exception:
                    pass

            killer = threading.Timer(args.timeout, _kill_on_timeout)
            killer.start()
            n = 0
            try:
                for line in proc.stdout:
                    if line.startswith(AGENT_START_MARKER):
                        try:
                            agent_start_evidence = json.loads(
                                line[len(AGENT_START_MARKER):]
                            )
                        except json.JSONDecodeError as exc:
                            agent_start_evidence = {
                                "actual_workdir": None,
                                "error": (
                                    "invalid agent-start evidence: "
                                    f"{type(exc).__name__}: {exc}"
                                ),
                                "expected_workdir": agent_workdir,
                                "validated": False,
                            }
                        if agent_start_evidence.get("validated"):
                            phase = "agent"
                        continue
                    trace_fh.write(line)
                    trace_fh.flush()
                    n += 1
                    if (
                        args.agent == "codex"
                        and not completion["detected"]
                    ):
                        sentinel = codex_completion_sentinel(line)
                        if sentinel is not None:
                            completion["detected"] = True
                            completion["sentinel"] = sentinel
                            killer.cancel()

                            def _stop_after_completion():
                                completion["termination_requested"] = (
                                    terminate_completed_codex(cname, proc)
                                )

                            completion_timer["value"] = threading.Timer(
                                COMPLETION_GRACE_SECONDS,
                                _stop_after_completion,
                            )
                            completion_timer["value"].start()
                    elif (
                        args.agent != "codex"
                        and not completion["detected"]
                    ):
                        sentinel = claude_completion_sentinel(line)
                        if sentinel is not None:
                            completion["detected"] = True
                            completion["sentinel"] = sentinel
                            killer.cancel()
                    if n % 20 == 0:
                        print(f"    ...{n} 条事件, {time.time()-t0:.0f}s")
                proc.wait()
            finally:
                killer.cancel()
                if completion_timer["value"] is not None:
                    completion_timer["value"].cancel()
            agent_returncode = proc.returncode
            if (
                not isinstance(agent_start_evidence, dict)
                or not agent_start_evidence.get("validated")
                or agent_start_evidence.get("actual_workdir")
                != agent_workdir
            ):
                phase = "agent_prepare"
                raise RuntimeError(
                    "agent runtime cwd validation failed: "
                    + json.dumps(
                        agent_start_evidence,
                        ensure_ascii=False,
                        sort_keys=True,
                    )
                )
            if timed_out["hit"]:
                print(f"    Codex 超过 {args.timeout}s 被终止")
            err = proc.stderr.read()[-1500:]
        agent_secs = time.time() - t0
        print(f"\n[5] Codex 结束，耗时 {agent_secs:.0f}s，事件 {n} 条")
        if err.strip():
            print(f"    stderr 尾部: {err.strip()[:600]}")

        stop_event.set()
        time.sleep(0.5)
        agent_process_boundary = stop_evaluated_agent_processes(
            cname, args.agent, mcp_process_name=mcp_command
        )
        print(
            "[5b] Task Agent 进程边界已封存 "
            f"initial={len(agent_process_boundary['initial_processes'])} "
            f"forced={agent_process_boundary['kill_sent']}"
        )
        try:
            ax_telemetry = export_ax_telemetry(
                cname,
                trace_dir,
                telemetry_path=ax_telemetry_path,
            )
            if ax_telemetry["present"]:
                print(
                    "[5c] AX operator telemetry 已封存 "
                    f"bytes={ax_telemetry['bytes']}"
                )
        except Exception as e:
            ax_telemetry = {
                "present": False,
                "path": None,
                "bytes": 0,
                "error": f"{type(e).__name__}: {e}",
            }
            print(f"[5c] AX operator telemetry 封存失败: {ax_telemetry['error']}")

        # evaluate 之前先把 VM 里的产物拷出来。0 分有两种截然不同的原因：
        # agent 真没做对，或者做对了但存错了地方/格式。没有产物就无法区分，
        # 而这两种在跑分报告里的含义完全不同。
        artifacts = os.path.join(trace_dir, "artifacts")
        os.makedirs(artifacts, exist_ok=True)
        try:
            listing = subprocess.run(
                ["docker", "exec", cname, "bash", "-lc",
                 "ls -la /home/user/ 2>/dev/null | head -40"],
                capture_output=True, text=True, timeout=60).stdout
            with open(os.path.join(artifacts, "home_listing.txt"), "w") as fh:
                fh.write(listing)
            artifact_manifest = export_task_artifacts(
                cname,
                artifacts,
                before_inventory=artifact_inventory_before,
            )
            print(
                "[6] 已按原路径导出 "
                f"{artifact_manifest['copied_count']}/"
                f"{artifact_manifest['file_count']} 个产物到 {artifacts}"
            )
        except Exception as e:
            print(f"[6] 产物导出失败: {e}")

        # summarize 提到 evaluate 之前：判分需要知道 agent 是否宣布了不可行。
        # summarize 只读轨迹文件，不碰环境，提前调用没有副作用。
        summary = (summarize_trace(codex_jsonl) if args.agent == "codex"
                   else summarize_claude_trace(codex_jsonl))
        proxy_task_id = (
            task_id
            if args.agent == "codex"
            else claude_gateway_affinity(task_id)
        )
        summary = recover_truncated_token_usage(
            summary,
            args.proxy_token_log,
            proxy_task_id,
            truncated=timed_out["hit"],
        )

        live_judge = None
        sealed_judge_state = None
        if args.judge_model:
            phase = "judge_seal"
            judge_dir = os.path.join(trace_dir, "judge")
            os.makedirs(judge_dir, exist_ok=True)
            print(
                "[6b] 封存 Task Agent 最终状态，"
                "interactive outcome Judge 将在 RAW evaluator 后运行..."
            )
            try:
                sealed_judge_state = capture_live_judge_state(
                    env, cname, judge_dir, "pre_judge"
                )
                print(
                    "[6b] 最终状态已封存 "
                    f"files={((sealed_judge_state.get('file_manifest') or {}).get('file_count'))}"
                )
            except Exception as exc:
                sealed_judge_state = {
                    "error": f"{type(exc).__name__}: {exc}",
                }
                print(
                    "[6b] 最终状态封存失败: "
                    f"{sealed_judge_state['error']}"
                )

        # 把"宣布不可行"翻译成官方判据认识的 FAIL 动作。
        # 直接 append 而不是 env.step("FAIL")：step 会真的去操作环境（截图、执行动作），
        # 此刻 agent 已经退出，再动环境只会污染产物。判据只读 action_history[-1]。
        declared_infeasible = bool(summary.get("saw_infeasible"))
        if declared_infeasible:
            env.action_history.append("FAIL")
            print(f"[7] agent 宣布 {INFEASIBLE_SENTINEL}，已注入 FAIL 动作")

        task_is_infeasible = (
            "infeasible"
            in str((task_config.get("evaluator") or {}).get("func", ""))
        )
        phase = "evaluate"
        evaluator_score, evaluator_error = evaluate_official_raw(env)
        if evaluator_error:
            score = None
            agent_run_error = f"official evaluator failed: {evaluator_error}"
            print(f"\n[7] evaluate() ERROR: {evaluator_error}")
        else:
            score, agent_run_error = score_agent_run(
                evaluator_score,
                summary,
                agent_returncode=agent_returncode,
                timed_out=timed_out["hit"],
                completion_boundary=completion["detected"],
                task_is_infeasible=task_is_infeasible,
            )
            print(f"\n[7] evaluate() = {evaluator_score}")
        if agent_run_error:
            print(f"[7b] 本轮记为 ERR：{agent_run_error}")
        evaluated_at = datetime.now(timezone.utc)
        audited = None
        if evaluator_error:
            audited = {
                "version": AUDIT_VERSION,
                "task_id": task_id,
                "classification": "AUDIT_ERROR",
                "official_score": None,
                "audited_score": None,
                "score_lower": None,
                "score_upper": None,
                "include_in_capability_denominator": False,
                "reason": (
                    "The official evaluator did not complete: "
                    + evaluator_error
                ),
                "evidence": {},
                "bash_equivalence": {
                    "status": "needs_review",
                    "reason": "The evaluator did not produce a score.",
                },
            }
            with open(os.path.join(trace_dir, "audit.json"), "w") as fh:
                json.dump(audited, fh, ensure_ascii=False, indent=2)
        else:
            try:
                audited = audit_task(
                    task_id,
                    evaluator_score,
                    trace_dir,
                    as_of=evaluated_at.date(),
                    container_name=cname,
                )
                if audited:
                    with open(os.path.join(trace_dir, "audit.json"), "w") as fh:
                        json.dump(audited, fh, ensure_ascii=False, indent=2)
                    print(
                        "[7c] audited evaluator = "
                        f"{audited['classification']} "
                        f"[{audited['score_lower']}, {audited['score_upper']}]"
                    )
            except Exception as exc:
                audited = {
                    "version": AUDIT_VERSION,
                    "task_id": task_id,
                    "classification": "AUDIT_ERROR",
                    "official_score": evaluator_score,
                    "audited_score": None,
                    "score_lower": None,
                    "score_upper": None,
                    "include_in_capability_denominator": False,
                    "reason": f"{type(exc).__name__}: {exc}",
                    "evidence": {},
                    "bash_equivalence": {
                        "status": "needs_review",
                        "reason": "The audit did not complete.",
                    },
                }
                with open(os.path.join(trace_dir, "audit.json"), "w") as fh:
                    json.dump(audited, fh, ensure_ascii=False, indent=2)
                print(
                    f"[7c] evaluator audit failed: "
                    f"{type(exc).__name__}: {exc}"
                )
        if args.judge_model:
            phase = "judge"
            try:
                with acquire_judge_slot() as judge_slot:
                    print(
                        f"[7d] 启动 interactive outcome Judge "
                        f"{args.judge_model}/{args.judge_effort} "
                        f"slot={judge_slot}..."
                    )
                    live_judge = run_live_vm_judge(
                        env,
                        cname,
                        trace_dir,
                        instruction,
                        task_id,
                        args.judge_model,
                        args.judge_effort,
                        args.gw_port,
                        args.judge_timeout,
                        sealed_before=sealed_judge_state,
                        artifacts_dir=artifacts,
                        final_response=(
                            summary.get("last_substantive_message")
                            or summary.get("last_message")
                            or ""
                        ),
                        raw_evaluator_completed=evaluator_error is None,
                        raw_evaluator_error=evaluator_error,
                        declared_infeasible=declared_infeasible,
                        mcp_command=mcp_command,
                    )
                bind_live_judge_run_validity(
                    live_judge,
                    truncated=timed_out["hit"],
                    completion_boundary_detected=completion["detected"],
                    agent_returncode=agent_returncode,
                )
                verdict = (live_judge.get("verdict") or {}).get(
                    "instruction_verdict"
                )
                print(
                    f"[7d] Judge={verdict} "
                    f"valid={live_judge['judge_valid']} "
                    f"inspection_changed_state="
                    f"{live_judge['integrity']['file_state_changed']}"
                )
            except Exception as exc:
                live_judge = {
                    "judge_mode": "interactive_outcome",
                    "model": args.judge_model,
                    "effort": args.judge_effort,
                    "error": f"{type(exc).__name__}: {exc}",
                    "verdict": None,
                    "judge_valid": False,
                }
                print(f"[7d] Judge 失败: {live_judge['error']}")
        summary["declared_infeasible"] = declared_infeasible
        summary["task_is_infeasible"] = task_is_infeasible
        # 被超时 kill 的 run 常拿不到 turn.completed。优先使用 wrapper proxy
        # 对精确 task_id 的记账；若仍无可用 usage，则保留“未知”而不是伪造为零。
        if timed_out["hit"]:
            for k in ("input_tokens", "output_tokens", "reasoning_tokens",
                      "cached_input_tokens"):
                if not summary.get(k):
                    summary[k] = None
        result = {"task_id": task_id,
                  "runner_run_id": os.environ.get("OSW_HYBRID_RUN_ID"),
                  "batch_run_id": os.environ.get("OSW_BATCH_RUN_ID"),
                  "instruction": instruction, "model": args.model,
                  "effort": args.effort, "score": score,
                  "evaluator_score": evaluator_score,
                  "error": agent_run_error,
                  "status": (
                      "evaluation_failure"
                      if evaluator_error
                      else "completed"
                  ),
                  "setup_failure": False,
                  "evaluation_failure": bool(evaluator_error),
                  "agent_returncode": agent_returncode,
                  "completion_boundary_detected": completion["detected"],
                  "completion_boundary_sentinel": completion["sentinel"],
                  "completion_boundary_termination_requested":
                      completion["termination_requested"],
                  "agent_timed_out": timed_out["hit"],
                  "timed_out": timed_out["hit"],
                  "truncated": timed_out["hit"],
                  "agent": args.agent,
                  "bash_enabled": os.environ.get("OSW_ENABLE_SHELL", "0") == "1",
                  "skill_injection": skill_injection,
                  "codex_base_instructions": {
                      "path": CODEX_BASE_INSTRUCTIONS_PATH,
                      "sha256": "sha256:" + hashlib.sha256(
                          Path(CODEX_BASE_INSTRUCTIONS_PATH).read_bytes()
                      ).hexdigest(),
                  },
                  "agent_workdir": agent_workdir,
                  "agent_workdir_source": agent_workdir_source,
                  "agent_workdir_probe": workdir_probe,
                  "agent_start_evidence": agent_start_evidence,
                  "agent_process_boundary": agent_process_boundary,
                  "ax_telemetry": ax_telemetry,
                  "requested_image": args.image,
                  "requested_image_id": requested_image_id,
                  "actual_image_id": actual_image_id,
                  "harness_sha256": "sha256:" + hashlib.sha256(
                      Path(__file__).read_bytes()
                  ).hexdigest(),
                  "evaluated_at_utc": evaluated_at.isoformat(),
                  "agent_seconds": round(agent_secs, 1), **summary}
        result.update(result_tool_metadata(tool_release))
        result.update(result_tool_candidate_metadata(tool_candidate))
        if live_judge is not None:
            result["live_judge"] = live_judge
        if pre_agent_state:
            result["pre_agent_state"] = {
                "captured_at_utc": pre_agent_state.get("captured_at_utc"),
                "privacy_dialog_present": pre_agent_state.get(
                    "privacy_dialog_present"
                ),
                "screenshot": pre_agent_state.get("screenshot"),
            }
        if audited:
            result["audited"] = audited
        with open(os.path.join(trace_dir, "result.json"), "w") as fh:
            json.dump(result, fh, ensure_ascii=False, indent=2)

        print("\n" + "=" * 74)
        print(f"分数 {score}   工具调用 {summary['tool_calls']}   "
              f"reasoning 事件 {summary['reasoning_items']}")
        print(f"token: in={summary['input_tokens']} out={summary['output_tokens']} "
              f"reasoning={summary['reasoning_tokens']}")
        print(f"工具分布: {summary['tool_breakdown']}")
        print(f"末条消息: {summary['last_message'][:200]}")
        print("=" * 74)
        return 0 if score is not None else 1

    except Exception as e:
        print(f"\n失败: {type(e).__name__}: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        result_path = os.path.join(trace_dir, "result.json")
        if not os.path.exists(result_path):
            failure = build_failure_result(
                task_id,
                instruction,
                args.model,
                args.effort,
                args.agent,
                skill_injection,
                phase,
                e,
                tool_release,
                tool_candidate,
            )
            with open(result_path, "w") as fh:
                json.dump(failure, fh, ensure_ascii=False, indent=2)
        return 1
    finally:
        stop_event.set()
        if env is not None and not args.keep:
            try:
                env.close()
            except Exception:
                pass


if __name__ == "__main__":
    sys.exit(main())
