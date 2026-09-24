#!/usr/bin/env python3
"""Run one OSWorld V1 or V2 task in the official Docker/QEMU runtime.

The selected task agent and Computer Use run inside the QEMU guest. The
outer Docker container remains the stock OSWorld V2 runtime and only hosts the
VM. The independent interactive outcome Judge continues to use Codex.
"""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import datetime
import hashlib
import json
import logging
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import sys
import time
import traceback
import urllib.parse
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

import requests


INTEGRATION_ROOT = Path(__file__).resolve().parents[1]
REPO_ROOT = Path(
    os.environ.get(
        "HYBRID_COMPUTER_USE_ROOT",
        Path(__file__).resolve().parents[3],
    )
).expanduser().resolve()
ROOT = Path(
    os.environ.get("OSWORLD_V2_ROOT", REPO_ROOT / "external" / "OSWorld-V2")
).expanduser().resolve()
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(INTEGRATION_ROOT))
os.chdir(ROOT)
WORKSPACE_ROOT = REPO_ROOT
sys.path.insert(0, str(WORKSPACE_ROOT))

DEFAULT_ARTIFACT_DIR = Path(
    os.environ.get("HCU_ARTIFACT_DIR", REPO_ROOT / "artifacts")
).expanduser()
DEFAULT_SKILL_FILE = (
    WORKSPACE_ROOT / "skills/hybrid/SKILL.md"
)
DEFAULT_BASE_INSTRUCTIONS_FILE = (
    WORKSPACE_ROOT / "skills/base/CODEX_BASE_INSTRUCTIONS.md"
)
MCP_PUBLIC_TOOLS = ("AX", "GUI")

from desktop_env.cache import run_scoped_cache_dir  # noqa: E402
from desktop_env.desktop_env import DesktopEnv  # noqa: E402
from hybrid.runtime_preflight import (  # noqa: E402
    validate_dedicated_docker_runtime,
    validate_official_asset_runtime,
)
from runner_overrides.control_retry import bounded_control_retry  # noqa: E402
from runner_overrides.judge_budget import completed_tool_events  # noqa: E402
from task_loader import load_task_config  # noqa: E402
from integrations.common.evaluator_channels import (  # noqa: E402
    run_official_evaluator,
)


COMPLETE_SENTINEL = "TASK_COMPLETE"
INFEASIBLE_SENTINEL = "TASK_INFEASIBLE"
ASK_USER_SENTINEL = "ASK_USER"
DEFAULT_USER_RESPONSE = (
    "I have no further information to provide. "
    "I trust you can figure it out based on the current observation and the "
    "instruction. Please proceed with the next action. DO NOT ask me any more "
    "questions for this task."
)
GUEST_CODEX = "/home/user/.local/bin/codex"
GUEST_CLAUDE_CODE = "/home/user/.local/bin/claude"
GUEST_MCP = "/home/user/.local/bin/computer-use"
GUEST_MCP_RUNTIME_ROOT = "/opt/osw-runtime/mcp"
GUEST_CODEX_HOME = "/opt/osw-runtime/codex-task"
GUEST_CLAUDE_CONFIG_DIR = "/opt/osw-runtime/claude-task"
GUEST_CLAUDE_MCP_ADAPTER = (
    f"{GUEST_CLAUDE_CONFIG_DIR}/mcp_schema_adapter.py"
)
GUEST_JUDGE_CODEX_HOME = "/opt/osw-runtime/codex-judge"
GUEST_CONFIG = f"{GUEST_CODEX_HOME}/config.toml"
GUEST_CLAUDE_MCP_CONFIG = f"{GUEST_CLAUDE_CONFIG_DIR}/mcp.json"
GUEST_JUDGE_CONFIG = f"{GUEST_JUDGE_CODEX_HOME}/config.toml"
GUEST_BASE_INSTRUCTIONS = (
    f"{GUEST_CODEX_HOME}/osw-base-instructions.md"
)
GUEST_JUDGE_BASE_INSTRUCTIONS = (
    f"{GUEST_JUDGE_CODEX_HOME}/osw-base-instructions.md"
)
GUEST_SKILL = "/home/user/AGENTS.md"
GUEST_PROMPT = "/tmp/osworld-v2-hybrid-prompt.txt"
GUEST_LAUNCHER = "/tmp/osworld-v2-hybrid-run.sh"
GUEST_TRACE = "/tmp/osworld-v2-hybrid-agent.jsonl"
GUEST_STDERR = "/tmp/osworld-v2-hybrid-agent.stderr"
GUEST_FINAL = "/tmp/osworld-v2-hybrid-final.txt"
GUEST_DONE = "/tmp/osworld-v2-hybrid-done.json"
GUEST_LAUNCHER_STATUS = "/tmp/osworld-v2-hybrid-launcher-status.json"
GUEST_JUDGE_ROOT = "/tmp/osw-live-judge-sealed"
GUEST_JUDGE_WORK = "/tmp/osw-live-judge-work"
GUEST_JUDGE_PROMPT = "/tmp/osw-live-judge-prompt.txt"
GUEST_JUDGE_SCHEMA = "/tmp/osw-live-judge-schema.json"
GUEST_JUDGE_RESULT = "/tmp/osw-live-judge-result.json"
GUEST_JUDGE_TRACE = "/tmp/osw-live-judge-agent.jsonl"
GUEST_JUDGE_STDERR = "/tmp/osw-live-judge.stderr"
GUEST_JUDGE_STATUS = "/tmp/osw-live-judge-status.json"
GUEST_JUDGE_PROBE = f"{GUEST_JUDGE_WORK}/judge-probe"
GUEST_JUDGE_PTY_STREAM = f"{GUEST_JUDGE_WORK}/pty_stream.py"
GUEST_PRIOR_JUDGE_EVIDENCE = (
    f"{GUEST_JUDGE_ROOT}/prior_judge_execution_evidence.jsonl"
)
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
LIVE_JUDGE_STATE_PROBE = r"""
import hashlib
import json
import os
from pathlib import Path

home = Path("/home/user")
roots = [
    home / "Desktop",
    home / "Documents",
    home / "Downloads",
    home / "Pictures",
    home / "Music",
    home / "Videos",
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
max_bytes = 100 * 1024 * 1024
files = {}


def record(path):
    try:
        relative = str(path.relative_to(home))
        stat = path.stat()
        if stat.st_size >= max_bytes:
            return
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
"""
AGENT_WATCHDOG_GRACE_SEC = 120.0
AGENT_WATCHDOG_INTERVAL_SEC = 30.0
AGENT_WATCHDOG_ABSENT_THRESHOLD = 2
GUEST_FILE_ATTEMPTS = 5
GUEST_FILE_INITIAL_BACKOFF_SEC = 1.0
STREAM_RETRY_LIMIT = 1
STREAM_RETRY_BACKOFF_SEC = 30.0
TOOL_ARGUMENT_RETRY_LIMIT = 1
FAILURE_SUMMARY_MAX_CHARS = 4096
JUDGE_SCREENSHOT_CAPTURE_TIMEOUT_SEC = 60.0
JUDGE_A11Y_CAPTURE_TIMEOUT_SEC = 60.0
JUDGE_MANIFEST_CAPTURE_ATTEMPTS = 3
JUDGE_MANIFEST_CAPTURE_BACKOFF_SEC = 0.5
JUDGE_STATUS_POLL_INTERVAL_SEC = 2.0
PRIOR_JUDGE_COMMAND_MAX_CHARS = 2000
PRIOR_JUDGE_OUTPUT_MAX_CHARS = 6000
JUDGE_RETRYABLE_FAILURES = {
    "judge_timeout",
    "judge_signal_15",
    "judge_nonzero_exit",
    "judge_invalid_output",
    "judge_control_failure",
    "judge_tool_budget_exhausted",
}
CLAUDE_BUILTIN_TOOLS = "Bash,Edit,Read"
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
JUDGE_RETRY_CONVERGENCE_PROMPT = """

Bounded recovery attempt:
- A prior independent Judge attempt ended before producing a valid verdict.
- Read `/tmp/osw-live-judge-sealed/prior_judge_execution_evidence.jsonl`
  first. It contains only completed tool events from that attempt, with
  reasoning removed. Reuse its successful reads and outputs instead of
  repeating them.
- Do not repeat broad discovery or inventory every object.
- Reuse sealed evidence and inspect only the smallest unresolved,
  outcome-determinative facts.
- Prefer one structured API/artifact read over many UI reads.
- Do not use GUI navigation, repeated accessibility trees, browser-profile
  archaeology, or broad filesystem searches unless the unresolved fact exists
  only there.
- For a source-derived artifact, inspect the delivered artifact and
  authoritative source first, then run one bounded reconciliation. Do not
  re-check send or attachment state before semantic correctness.
- If the task says source records may arrive later or requests notifications,
  refresh the source and enumerate unread/activity/new records before PASS.
  Sampling only conversations visible at setup is not complete evidence.
- If the instruction restricts the source set, do not perform external web
  research for expected content. Use only the permitted sources; external
  material cannot supplement, correct or contradict them.
- If a concrete contradiction is found, stop immediately and return FAIL with
  the exact expected and observed values.
- If the decisive source cannot be inspected by the midpoint of this retry
  budget, stop exploration and return FAIL with INSPECTION_BLOCKED.
- Stop inspection early enough to produce the required schema-valid verdict.
- A concrete contradiction is sufficient for FAIL; complete positive evidence
  is required for PASS.
""".strip()
CODEX_BUNDLED_SYSTEM_SKILLS = (
    "imagegen",
    "openai-docs",
    "plugin-creator",
    "review-agent",
    "skill-creator",
    "skill-installer",
)


class RequestTotalTimeoutError(TimeoutError):
    """Raised when a guest HTTP request exceeds its total wall-clock budget."""


@contextmanager
def total_wall_timeout(seconds: float):
    duration = max(0.001, float(seconds))
    previous_handler = signal.getsignal(signal.SIGALRM)
    previous_timer = signal.getitimer(signal.ITIMER_REAL)
    started = time.monotonic()

    def raise_timeout(_signum, _frame):
        raise RequestTotalTimeoutError(
            f"Guest HTTP request exceeded {duration:.3f}s total wall time"
        )

    signal.signal(signal.SIGALRM, raise_timeout)
    signal.setitimer(signal.ITIMER_REAL, duration)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)
        if previous_timer[0] > 0:
            elapsed = time.monotonic() - started
            remaining = max(0.001, previous_timer[0] - elapsed)
            signal.setitimer(
                signal.ITIMER_REAL,
                remaining,
                previous_timer[1],
            )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--task-id", required=True)
    parser.add_argument(
        "--benchmark-version",
        choices=("v1", "v2"),
        default="v2",
        help=(
            "Task/evaluator generation. Both use the official Docker/QEMU "
            "runtime; v1 loads the legacy JSON task suite."
        ),
    )
    parser.add_argument(
        "--task-domain",
        default="",
        help="Optional V1 task domain; UUID lookup is automatic when unique.",
    )
    parser.add_argument(
        "--agent",
        choices=("codex", "claude-code"),
        default="codex",
        help="Guest-local Task Agent. The independent Judge remains Codex.",
    )
    parser.add_argument(
        "--model",
        default="",
        help=(
            "Task Agent model. Defaults to gpt-5.5 for Codex and "
            "claude-opus-5 for Claude Code."
        ),
    )
    parser.add_argument("--effort", default="xhigh")
    parser.add_argument(
        "--gateway-base",
        default=os.environ.get(
            "HCU_OPENAI_GATEWAY",
            "http://172.17.0.1:8611",
        ),
        help="Container-bridge address reachable from the QEMU guest.",
    )
    parser.add_argument(
        "--artifact-dir",
        default=str(DEFAULT_ARTIFACT_DIR),
    )
    parser.add_argument(
        "--codex-binary",
        default="",
        help="Explicit Codex executable; otherwise use artifact-dir/codex.",
    )
    parser.add_argument(
        "--claude-code-binary",
        default="",
        help=(
            "Explicit Claude Code executable; otherwise use "
            "artifact-dir/claude-code."
        ),
    )
    parser.add_argument(
        "--claude-gateway-base",
        default=os.environ.get(
            "HCU_ANTHROPIC_GATEWAY",
            "http://172.17.0.1:8537",
        ),
        help=(
            "Anthropic-compatible gateway base reachable from the QEMU guest. "
            "Claude Code appends /v1/messages."
        ),
    )
    parser.add_argument(
        "--mcp-binary",
        default="",
        help="Explicit MCP executable; otherwise use artifact-dir/computer-use.",
    )
    parser.add_argument(
        "--guest-image",
        default="",
        help="Optional QCOW2 override for the evaluated guest.",
    )
    parser.add_argument(
        "--skill-file",
        default=str(DEFAULT_SKILL_FILE),
    )
    parser.add_argument(
        "--base-instructions-file",
        default=str(DEFAULT_BASE_INSTRUCTIONS_FILE),
        help="Compact model base instructions copied into the guest.",
    )
    parser.add_argument("--judge-model", default="")
    parser.add_argument("--judge-effort", default="xhigh")
    parser.add_argument("--judge-timeout", type=int, default=600)
    parser.add_argument("--judge-tool-limit", type=int, default=30)
    parser.add_argument("--judge-retry-tool-limit", type=int, default=15)
    parser.add_argument(
        "--judge-prompt-file",
        default=str(INTEGRATION_ROOT / "judge" / "prompt.md"),
    )
    parser.add_argument(
        "--judge-schema-file",
        default=str(INTEGRATION_ROOT / "judge" / "schema.json"),
    )
    parser.add_argument(
        "--result-dir",
        default="",
        help="Defaults to HCU_RESULTS_DIR/osworld-<version>/<run-tag>.",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=0,
        help=(
            "Optional per-phase wall-clock timeout in seconds; 0 disables "
            "the extra harness timeout and leaves termination to the model-call "
            "budget, matching the upstream OpenAI runner."
        ),
    )
    parser.add_argument(
        "--max-model-calls",
        type=int,
        default=0,
        help="Stop after this many task-scoped model requests; 0 disables the limit.",
    )
    parser.add_argument(
        "--retain-container",
        action="store_true",
        help=(
            "Stop but do not remove the task VM container after evaluation and "
            "Judge completion, preserving it for manual inspection."
        ),
    )
    parser.add_argument(
        "--token-log",
        default="",
        help="Host-side token proxy JSONL used for task-scoped request counting.",
    )
    parser.add_argument("--poll-interval", type=float, default=5.0)
    parser.add_argument(
        "--max-user-simulator-turns",
        type=int,
        default=0,
        help=(
            "Optional extra ASK_USER cap per phase; 0 relies on the normal "
            "per-phase model-call budget, matching the upstream step loop."
        ),
    )
    parser.add_argument("--client-password", default="osworld-public-evaluation")
    parser.add_argument(
        "--volume-size",
        type=int,
        default=40,
        help="Guest root disk size in GB; prevents Codex from exhausting the base image.",
    )
    parser.add_argument(
        "--ram-size",
        default="4G",
        help="Docker guest RAM size, for example 4G or 8G.",
    )
    parser.add_argument("--enable-vnc", action="store_true")
    parser.add_argument("--enable-recording", action="store_true")
    args = parser.parse_args()
    if not args.model:
        args.model = (
            "claude-opus-5"
            if args.agent == "claude-code"
            else "gpt-5.5"
        )
    return args


def safe_tag(value: str) -> str:
    return "".join(ch if ch.isalnum() or ch in "._-" else "-" for ch in value)


def task_value(task, key: str, default=None):
    if hasattr(task, "get"):
        return task.get(key, default)
    return getattr(task, key, default)


def judge_user_context(task, phase_details: list[dict]) -> dict | None:
    simulator = task_value(task, "user_simulator")
    exchanges = []
    for phase in phase_details:
        for exchange in phase.get("agent", {}).get("user_exchanges", []):
            exchanges.append({
                "phase_index": exchange.get("phase_index"),
                "phase_name": exchange.get("phase_name"),
                "turn_index": exchange.get("turn_index"),
                "question": exchange.get("question"),
                "answer": exchange.get("answer"),
            })

    context: dict[str, object] = {}
    if isinstance(simulator, dict):
        for key in ("knowledge", "response", "default_response"):
            if key in simulator:
                context[key] = simulator[key]
    if exchanges:
        context["actual_exchanges"] = exchanges
    if not context:
        return None
    return json.loads(json.dumps(context, ensure_ascii=False, default=str))


def resolve_guest_image(task, args) -> str:
    task_image = str(task_value(task, "image", "") or "")
    if not args.guest_image:
        return task_image

    image = Path(args.guest_image).expanduser().resolve()
    if not image.is_file():
        raise FileNotFoundError(f"Guest image not found: {image}")
    return str(image)


def task_phases(task) -> list[dict]:
    getter = getattr(task, "get_phases", None)
    if not callable(getter):
        return []
    phases = getter() or []
    if not isinstance(phases, list):
        raise TypeError("get_phases() must return a list")
    return phases


def task_current_date(
    task,
    *,
    run_started_at_utc: str | None = None,
    timezone_name: str = "UTC",
) -> str:
    value = None
    if hasattr(task, "get") and callable(getattr(task, "get")):
        value = task.get("task_current_date")
    if value is None:
        value = getattr(task, "task_current_date", None)
    if value is not None:
        return str(value)

    raw_started = (
        str(run_started_at_utc or "").strip()
        or datetime.datetime.now(datetime.timezone.utc).isoformat()
    )
    parsed = datetime.datetime.fromisoformat(
        raw_started.replace("Z", "+00:00")
    )
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=datetime.timezone.utc)
    try:
        zone = ZoneInfo(timezone_name)
    except ZoneInfoNotFoundError as exc:
        raise ValueError(
            f"Unknown run timezone: {timezone_name}"
        ) from exc
    return parsed.astimezone(zone).date().isoformat()


def validate_guest_current_date(
    *,
    expected_date: str,
    command_output: str,
) -> str:
    observed_date = str(command_output or "").strip()
    try:
        datetime.date.fromisoformat(expected_date)
        datetime.date.fromisoformat(observed_date)
    except ValueError as exc:
        raise RuntimeError(
            "Guest current-date preflight returned an invalid ISO date: "
            f"expected={expected_date!r}, observed={observed_date!r}"
        ) from exc
    if observed_date != expected_date:
        raise RuntimeError(
            "Guest current date does not match the immutable run-bound date: "
            f"expected={expected_date}, observed={observed_date}. "
            "The runner must align the guest to OSW_RUN_TIMEZONE before task "
            "setup."
        )
    return observed_date


def bind_task_runtime_timezone(task, timezone_name: str) -> None:
    normalized = str(timezone_name or "").strip()
    if not normalized:
        raise ValueError("Run timezone must not be empty")
    if hasattr(task, "__setitem__"):
        task["runtime_timezone"] = normalized
        return
    setattr(task, "runtime_timezone", normalized)


def align_guest_run_clock(
    setup_controller,
    *,
    expected_date: str,
    timezone_name: str,
) -> None:
    datetime.date.fromisoformat(expected_date)
    normalized_timezone = str(timezone_name or "").strip()
    if not normalized_timezone:
        raise ValueError("Run timezone must not be empty")
    setup_controller.execute(
        [
            "bash",
            "-lc",
            (
                "set -e; "
                "timedatectl_retry() { "
                "for attempt in 1 2 3 4 5 6; do "
                "output=$(printf '%s\\n' '{CLIENT_PASSWORD}' | "
                "sudo -S timedatectl \"$@\" 2>&1) && return 0; "
                "rc=$?; "
                "case \"$output\" in "
                "*'Previous request is not finished'*) sleep 2 ;; "
                "*) printf '%s\\n' \"$output\" >&2; return \"$rc\" ;; "
                "esac; "
                "done; "
                "printf '%s\\n' \"$output\" >&2; return \"$rc\"; "
                "}; "
                "timedatectl_retry set-timezone "
                f"{shlex.quote(normalized_timezone)}; "
                "timedatectl_retry set-ntp false; "
                "timedatectl_retry set-time "
                f"{shlex.quote(expected_date + ' 12:00:00')}"
            ),
        ],
        timeout=60,
    )


def request_execute(
    base_url: str,
    command: list[str],
    *,
    timeout: int = 120,
    total_timeout: float | None = None,
) -> dict:
    wall_timeout = (
        float(total_timeout)
        if total_timeout is not None
        else float(timeout) + 20.0
    )
    with total_wall_timeout(wall_timeout):
        response = requests.post(
            base_url + "/setup/execute",
            json={"command": command, "shell": False, "timeout": timeout},
            timeout=timeout + 20,
        )
    response.raise_for_status()
    payload = response.json()
    if payload.get("status") != "success":
        raise RuntimeError(f"Guest command failed: {payload}")
    returncode = payload.get("returncode")
    if returncode not in {0, None}:
        raise RuntimeError(
            "Guest command returned nonzero status: "
            f"returncode={returncode} "
            f"stdout={payload.get('output', '')!r} "
            f"stderr={payload.get('error', '')!r}"
        )
    return payload


def runtime_bootstrap_command(client_password: str) -> list[str]:
    runtime_dirs = (
        GUEST_CODEX_HOME,
        GUEST_CLAUDE_CONFIG_DIR,
        GUEST_JUDGE_CODEX_HOME,
        GUEST_MCP_RUNTIME_ROOT,
    )
    quoted_dirs = " ".join(shlex.quote(path) for path in runtime_dirs)
    return [
        "bash",
        "-lc",
        f"printf '%s\\n' {shlex.quote(client_password)} "
        "| sudo -S sh -c "
        + shlex.quote(
            "rm -rf /home/user/.codex /home/user/.claude "
            "/home/user/.claude.json "
            "/home/user/AGENTS.md /home/user/CLAUDE.md"
        )
        + " "
        f"&& printf '%s\\n' {shlex.quote(client_password)} "
        f"| sudo -S install -d -o user -g user {quoted_dirs} "
        f"&& test -w {shlex.quote(GUEST_CODEX_HOME)} "
        f"&& test -w {shlex.quote(GUEST_CLAUDE_CONFIG_DIR)} "
        f"&& test -w {shlex.quote(GUEST_JUDGE_CODEX_HOME)}",
    ]


def validate_provider_mount_isolation(container, guest_image: str) -> list[dict]:
    container.reload()
    mounts = list(container.attrs.get("Mounts") or [])
    expected_source = os.path.realpath(guest_image)
    bind_mounts = [
        mount for mount in mounts if mount.get("Type") == "bind"
    ]
    if len(bind_mounts) != 1:
        raise RuntimeError(
            "OSWorld V2 runtime must have exactly one host bind mount: "
            + json.dumps(mounts, sort_keys=True)
        )
    guest_mount = bind_mounts[0]
    valid_guest_mount = (
        os.path.realpath(str(guest_mount.get("Source") or ""))
        == expected_source
        and guest_mount.get("Destination") == "/System.qcow2"
        and not bool(guest_mount.get("RW"))
    )
    if not valid_guest_mount:
        raise RuntimeError(
            "OSWorld V2 runtime exposes an unexpected host mount: "
            + json.dumps(guest_mount, sort_keys=True)
        )
    unexpected_mounts = [
        mount
        for mount in mounts
        if mount is not guest_mount
        and not (
            mount.get("Type") == "volume"
            and mount.get("Destination") == "/storage"
        )
    ]
    if unexpected_mounts:
        raise RuntimeError(
            "OSWorld V2 runtime exposes an unexpected additional mount: "
            + json.dumps(unexpected_mounts, sort_keys=True)
        )
    return mounts


def install_guest_runtime(
    setup_controller,
    *,
    client_password: str,
    upload_rows: list[dict[str, str]],
    executable_paths: tuple[str, ...] = (
        GUEST_CODEX,
        GUEST_MCP,
        GUEST_LAUNCHER,
    ),
) -> None:
    base_url = setup_controller.http_server
    request_execute(
        base_url,
        runtime_bootstrap_command(client_password),
    )
    setup_controller._upload_file_setup(upload_rows)
    request_execute(
        base_url,
        [
            "chmod",
            "0755",
            *executable_paths,
        ],
    )


def public_mcp_probe_script() -> str:
    return r'''
import json
import subprocess
import sys

process = subprocess.Popen(
    [sys.argv[1], "mcp"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    bufsize=1,
)
next_id = 1


def request(method, params):
    global next_id
    request_id = next_id
    next_id += 1
    process.stdin.write(json.dumps({
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method,
        "params": params,
    }) + "\n")
    process.stdin.flush()
    while True:
        line = process.stdout.readline()
        if not line:
            raise RuntimeError(
                "MCP exited during public-surface probe: "
                + process.stderr.read()
            )
        message = json.loads(line)
        if message.get("id") != request_id:
            continue
        if "error" in message:
            raise RuntimeError(str(message["error"]))
        return message.get("result") or {}


try:
    request(
        "initialize",
        {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {
                "name": "osworld-v2-harness-preflight",
                "version": "1",
            },
        },
    )
    tools = request("tools/list", {}).get("tools") or []
    names = [tool.get("name") for tool in tools]
    expected = json.loads(sys.argv[2])
    if names != expected:
        raise RuntimeError("unexpected public MCP tools: " + repr(names))
    print(json.dumps({"public_tools": names}, sort_keys=True))
finally:
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)
'''


def transient_workload_command(
    command: list[str],
    *,
    unit_name: str,
    wait: bool,
    preserve_background_children: bool = False,
) -> list[str]:
    wrapped = [
        "systemd-run",
        "--user",
        "--collect",
        "--quiet",
        f"--unit={safe_tag(unit_name)[:200]}",
        "--property=Type=exec",
        "--property=OOMPolicy=continue",
    ]
    if preserve_background_children:
        wrapped.append("--property=KillMode=process")
    if wait:
        wrapped.extend(["--wait", "--pipe"])
    return [*wrapped, "--", *command]


def request_launch(base_url: str, command: list[str]) -> str:
    unit_name = (
        f"osw-workload-{os.getpid()}-{time.time_ns()}"
    )
    wrapped = transient_workload_command(
        command,
        unit_name=unit_name,
        wait=False,
        preserve_background_children=True,
    )
    response = requests.post(
        base_url + "/setup/launch",
        json={"command": wrapped, "shell": False},
        timeout=30,
    )
    response.raise_for_status()
    return safe_tag(unit_name)[:200]


def fetch_guest_file_once(base_url: str, path: str) -> bytes | None:
    response = requests.post(
        base_url + "/file",
        data={"file_path": path},
        timeout=120,
    )
    if response.status_code == 404:
        return None
    response.raise_for_status()
    return response.content


def fetch_guest_file(
    base_url: str,
    path: str,
    *,
    attempts: int = GUEST_FILE_ATTEMPTS,
    initial_backoff_sec: float = GUEST_FILE_INITIAL_BACKOFF_SEC,
) -> bytes | None:
    if attempts < 1:
        raise ValueError("attempts must be at least 1")
    for attempt in range(1, attempts + 1):
        try:
            return fetch_guest_file_once(base_url, path)
        except requests.RequestException as exc:
            if attempt == attempts:
                raise
            delay = initial_backoff_sec * (2 ** (attempt - 1))
            print(
                "Transient guest-file read failure "
                f"({attempt}/{attempts}): {type(exc).__name__}: {exc}; "
                f"retrying in {delay:g}s",
                file=sys.stderr,
                flush=True,
            )
            time.sleep(delay)

    raise AssertionError("unreachable")


def write_local(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")


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


def task_prompt(
    instruction: str,
    *,
    current_date: str | None = None,
    can_ask_user: bool = True,
) -> str:
    date_context = (
        f"Current date for this task: {current_date}\n\n"
        if current_date
        else ""
    )
    ask_contract = f"""
If information that only the user can supply is genuinely required, stop this
turn and reply in exactly this format:
{ASK_USER_SENTINEL}
<one concise question>
Do not use this for information that can be inspected from the environment.
"""
    if can_ask_user:
        ask_contract += f"""
A task-provided user-response channel is configured. Before replying
{INFEASIBLE_SENTINEL} because user-provided information, credentials, or input
files are missing, you must ask one concise question through {ASK_USER_SENTINEL}
and consider the returned answer. Do not ask about a blocker that the
environment itself can resolve.

If the user's instruction explicitly invites questions and a category, range,
eligibility, status, or assignment rule remains unresolved after inspection,
you must use {ASK_USER_SENTINEL} before any affected send, submit, publish, or
overwrite action. Do not choose a conventional interpretation silently.
Do not infer eligibility for one result from a source statement that discusses
only a different metric or consequence.
Bundle every unresolved rule that can change the submitted result into the
same concise question, then recompute the complete result after the answer.
"""
    return f"""\
{date_context}
{instruction}

Provide any textual answer requested by the user normally. When the task is
complete, put {COMPLETE_SENTINEL} alone on the final non-empty line of your
response. If no textual answer is requested, {COMPLETE_SENTINEL} may be the
entire response.
If the task cannot be completed because of missing credentials, unavailable
required capabilities, contradictory requirements, or another demonstrated
hard blocker, briefly explain the blocker and put {INFEASIBLE_SENTINEL} alone
on the final non-empty line.
{ask_contract}
"""


def split_completion_message(text: str) -> tuple[str, str | None]:
    """Separate a final-line harness status marker from user-facing text."""
    stripped = str(text or "").strip()
    if not stripped:
        return "", None
    lines = stripped.splitlines()
    sentinel = lines[-1].strip()
    if sentinel not in {COMPLETE_SENTINEL, INFEASIBLE_SENTINEL}:
        return stripped, None
    return "\n".join(lines[:-1]).strip(), sentinel


def judge_instruction_text(
    instruction: str,
    phases: list[dict],
) -> str:
    if not phases:
        return instruction
    sections = []
    for index, phase in enumerate(phases, start=1):
        name = str(phase.get("name") or f"Phase {index}")
        phase_instruction = str(phase.get("instruction") or instruction)
        sections.append(f"Phase {index} - {name}\n{phase_instruction}")
    return "\n\n".join(sections)


def phase_delivery_status(
    phases: list[dict],
    phase_details: list[dict],
) -> dict:
    expected = len(phases) if phases else 1
    delivered = len(phase_details)
    delivered_phase_numbers = [
        int(detail.get("phase_index", index))
        for index, detail in enumerate(phase_details, start=1)
    ]
    missing_phase_numbers = [
        index
        for index in range(1, expected + 1)
        if index not in delivered_phase_numbers
    ]
    return {
        "expected_phase_count": expected,
        "delivered_phase_count": delivered,
        "delivered_phase_numbers": delivered_phase_numbers,
        "missing_phase_numbers": missing_phase_numbers,
        "all_phases_delivered": (
            delivered == expected
            and not missing_phase_numbers
        ),
    }


def judge_answer_score_eligibility(
    *,
    truncated: bool,
    delivery: dict,
    final_message: str,
    done_payload: dict | None,
    official_evaluator_errors: list[dict],
) -> tuple[bool, str | None]:
    if official_evaluator_errors:
        return (
            False,
            "Official evaluator did not complete; Judge verdict is diagnostic "
            "until evaluator recovery.",
        )
    if truncated:
        return False, "Task Agent run was truncated."
    if not delivery["all_phases_delivered"]:
        return (
            False,
            "Not all declared task phases were delivered: "
            f"missing={delivery['missing_phase_numbers']}.",
        )
    _body, sentinel = split_completion_message(final_message)
    if sentinel is None:
        return False, "Task Agent produced no valid completion declaration."
    if (
        done_payload is not None
        and done_payload.get("returncode") not in {0, None}
    ):
        return False, "Task Agent completion process returned nonzero status."
    return True, None


def build_judge_prompt(
    template_path: Path,
    *,
    instruction: str,
    final_response: str,
    declared_infeasible: bool,
    current_date: str,
    timezone_name: str,
    user_context: dict | None = None,
) -> str:
    template = template_path.read_text(encoding="utf-8")
    rendered = template.format(
        instruction=instruction,
        final_response=final_response or "(no final response recorded)",
        completion_declaration=(
            INFEASIBLE_SENTINEL
            if declared_infeasible
            else "No TASK_INFEASIBLE declaration recorded."
        ),
    )
    prompt = (
        "Authoritative run-bound date context:\n"
        f"- task current date: {current_date}\n"
        f"- task timezone: {timezone_name}\n"
        "Use this immutable date for relative terms such as today, tomorrow, "
        "and weekdays. The live guest clock is diagnostic only.\n\n"
        + rendered
    )
    if user_context is not None:
        prompt += (
            "\n\n## Task-Provided User Context\n\n"
            "The harness supplied the following sanitized user-response "
            "context. Treat its knowledge and fixed responses as "
            "authoritative user facts or clarification rules for judging the "
            "outcome. They do not prove that the Task Agent asked for or "
            "received those facts; only entries under `actual_exchanges` "
            "prove an exchange occurred. Do not infer any omitted task "
            "configuration, evaluator rule, or gold answer from this block.\n\n"
            "```json\n"
            + json.dumps(user_context, ensure_ascii=False, indent=2)
            + "\n```\n"
        )
    return prompt


def valid_judge_verdict(verdict: object) -> bool:
    if not isinstance(verdict, dict):
        return False
    if verdict.get("judge_type") != "interactive_outcome":
        return False
    if verdict.get("instruction_verdict") not in {"PASS", "FAIL"}:
        return False
    requirements = verdict.get("requirements")
    if not isinstance(requirements, list) or not requirements:
        return False
    for requirement in requirements:
        if not isinstance(requirement, dict):
            return False
        if requirement.get("status") not in {"MET", "NOT_MET"}:
            return False
        coverage_mode = requirement.get("coverage_mode")
        if coverage_mode not in {
            "exact_single",
            "exhaustive_set",
            "representative_sample",
            "not_applicable",
        }:
            return False
        if coverage_mode == "exhaustive_set":
            expected_count = requirement.get("expected_count")
            inspected_count = requirement.get("inspected_count")
            if (
                not isinstance(expected_count, int)
                or isinstance(expected_count, bool)
                or expected_count < 0
                or not isinstance(inspected_count, int)
                or isinstance(inspected_count, bool)
                or inspected_count < 0
            ):
                return False

    verdict_name = verdict["instruction_verdict"]
    failure_reasons = verdict.get("failure_reasons")
    if verdict_name == "PASS":
        if any(row["status"] != "MET" for row in requirements):
            return False
        if failure_reasons != []:
            return False
        for requirement in requirements:
            coverage_mode = requirement["coverage_mode"]
            if coverage_mode == "representative_sample":
                return False
            if (
                coverage_mode == "exhaustive_set"
                and requirement["expected_count"]
                != requirement["inspected_count"]
            ):
                return False
    else:
        if not any(row["status"] == "NOT_MET" for row in requirements):
            return False
        if not isinstance(failure_reasons, list) or not failure_reasons:
            return False
    return True


def classify_judge_attempt(
    *,
    returncode: int | None,
    status: dict | None,
    execution_error: str | None,
    verdict: object,
    tool_budget_exhausted: bool = False,
) -> str | None:
    if tool_budget_exhausted:
        return "judge_tool_budget_exhausted"
    signal_number = (
        status.get("signal")
        if isinstance(status, dict)
        else None
    )
    if signal_number:
        return f"judge_signal_{signal_number}"
    if returncode == 124:
        return "judge_timeout"
    if returncode not in {0, None}:
        return "judge_nonzero_exit"
    if execution_error:
        return "judge_control_failure"
    if not valid_judge_verdict(verdict):
        return "judge_invalid_output"
    return None


def judge_retry_eligible(metadata: object) -> bool:
    if not isinstance(metadata, dict):
        return False
    return metadata.get("failure_classification") in JUDGE_RETRYABLE_FAILURES


def archive_judge_attempt(judge_dir: Path, attempt: int) -> Path:
    destination = judge_dir / f"attempt-{attempt:02d}"
    destination.mkdir(parents=True, exist_ok=True)
    for path in judge_dir.iterdir():
        if path == destination or path.name.startswith("attempt-"):
            continue
        if path.is_file():
            shutil.copy2(path, destination / path.name)
    return destination


def capture_judge_state(
    env: DesktopEnv,
    base_url: str,
    judge_dir: Path,
    label: str,
) -> dict:
    screenshot_path = judge_dir / f"{label}.png"
    screenshot = None
    screenshot_error = None
    try:
        with total_wall_timeout(JUDGE_SCREENSHOT_CAPTURE_TIMEOUT_SEC):
            screenshot = env.controller.get_screenshot()
    except Exception as exc:
        screenshot_error = f"{type(exc).__name__}: {exc}"
    if screenshot:
        screenshot_path.write_bytes(screenshot)

    tree_path = judge_dir / f"{label}.a11y.txt"
    tree = ""
    accessibility_error = None
    try:
        with total_wall_timeout(JUDGE_A11Y_CAPTURE_TIMEOUT_SEC):
            tree = env.controller.get_accessibility_tree() or ""
    except Exception as exc:
        accessibility_error = f"{type(exc).__name__}: {exc}"
    tree_path.write_text(tree, encoding="utf-8")

    manifest_path = judge_dir / f"{label}.files.json"
    manifest = None
    manifest_error = None
    manifest_retry = {
        "attempts": 0,
        "retry_assisted": False,
        "failures": [],
    }
    try:
        manifest_result, manifest_retry = bounded_control_retry(
            lambda: request_execute(
                base_url,
                [
                    "/usr/bin/python3",
                    "-c",
                    LIVE_JUDGE_STATE_PROBE,
                ],
                timeout=180,
            ),
            attempts=JUDGE_MANIFEST_CAPTURE_ATTEMPTS,
            initial_backoff_seconds=JUDGE_MANIFEST_CAPTURE_BACKOFF_SEC,
            retryable=lambda exc: isinstance(
                exc,
                (
                    ConnectionError,
                    TimeoutError,
                    requests.ConnectionError,
                    requests.Timeout,
                ),
            ),
        )
        manifest = json.loads(str(manifest_result.get("output", "")))
        if not isinstance(manifest, dict) or not isinstance(
            manifest.get("files"),
            dict,
        ):
            raise ValueError("invalid Judge file manifest")
        manifest_path.write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    except Exception as exc:
        manifest_error = f"{type(exc).__name__}: {exc}"
        manifest_retry = getattr(
            exc,
            "retry_evidence",
            {
                "attempts": 1,
                "retry_assisted": False,
                "failures": [manifest_error],
            },
        )

    metadata = {
        "label": label,
        "screenshot_file": screenshot_path.name if screenshot else None,
        "screenshot_sha256": (
            hashlib.sha256(screenshot).hexdigest() if screenshot else None
        ),
        "screenshot_error": screenshot_error,
        "accessibility_file": tree_path.name,
        "accessibility_sha256": hashlib.sha256(
            tree.encode("utf-8")
        ).hexdigest(),
        "accessibility_error": accessibility_error,
        "file_manifest": (
            manifest_path.name if manifest_path.is_file() else None
        ),
        "file_manifest_sha256": (
            manifest.get("manifest_sha256")
            if isinstance(manifest, dict)
            else None
        ),
        "file_count": (
            manifest.get("file_count")
            if isinstance(manifest, dict)
            else None
        ),
        "file_manifest_error": manifest_error,
        "file_manifest_retry": manifest_retry,
    }
    (judge_dir / f"{label}.json").write_text(
        json.dumps(metadata, indent=2),
        encoding="utf-8",
    )
    return metadata


def compare_judge_states(
    before: dict,
    after: dict,
    judge_dir: Path,
) -> dict:
    def files_for(state: dict) -> dict:
        name = state.get("file_manifest")
        if not name:
            return {}
        payload = json.loads(
            (judge_dir / str(name)).read_text(encoding="utf-8")
        )
        files = payload.get("files")
        return files if isinstance(files, dict) else {}

    before_files = files_for(before)
    after_files = files_for(after)
    return {
        "available": bool(
            before.get("file_manifest_sha256")
            and after.get("file_manifest_sha256")
        ),
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


def live_judge_mutation_audit(events_text: str) -> dict:
    mutating_mcp_tools = {
        "click", "double_click", "right_click", "drag", "gui", "hotkey",
        "key", "press_key", "scroll", "set_value", "type_text",
    }
    command_patterns = (
        r"(^|[;&|]\s*)(rm|mv|cp|touch|mkdir|rmdir|chmod|chown|ln|"
        r"truncate|dd|install)\b",
        r"(^|[^<])>>?",
        r"\bsed\s+-i\b",
        r"\btee\b",
        r"\b(os\.(remove|unlink|rename|replace|mkdir|makedirs)|"
        r"shutil\.(copy|copy2|move|rmtree)|"
        r"\.(write_text|write_bytes|write)\s*\()",
        r"\b(open|zipfile\.ZipFile)\s*\([^)]*,\s*['\"](?:w|a|x)",
    )
    mutating_mcp_calls = []
    suspicious_commands = []
    for line in (events_text or "").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item") or {}
        item_type = item.get("type")
        if item_type == "command_execution":
            command = str(item.get("command") or "")
            if any(
                re.search(pattern, command, flags=re.IGNORECASE)
                for pattern in command_patterns
            ):
                suspicious_commands.append(command[:1000])
            continue
        if item_type != "mcp_tool_call":
            continue
        tool = str(item.get("tool") or "").rsplit(".", 1)[-1].lower()
        arguments = item.get("arguments")
        if isinstance(arguments, str):
            try:
                arguments = json.loads(arguments)
            except json.JSONDecodeError:
                arguments = {}
        if tool == "gui":
            actions = (
                arguments.get("actions", [])
                if isinstance(arguments, dict)
                else []
            )
            mutating_mcp_calls.extend(
                f"gui:{str(action.get('type') or '').lower()}"
                for action in actions
                if isinstance(action, dict)
                and str(action.get("type") or "").lower()
                not in {"", "screenshot", "wait"}
            )
        elif tool in {"ax", "a11y"}:
            if (
                isinstance(arguments, dict)
                and str(arguments.get("mode") or "").lower() == "act"
            ):
                mutating_mcp_calls.extend(
                    f"ax:{str(action.get('type') or '').lower()}"
                    for action in arguments.get("actions") or []
                    if isinstance(action, dict)
                )
        elif tool in mutating_mcp_tools:
            mutating_mcp_calls.append(tool)
    return {
        "mutating_mcp_calls": mutating_mcp_calls,
        "mutation_capable_mcp_used": bool(mutating_mcp_calls),
        "suspicious_commands": suspicious_commands,
        "mutation_capable_command_used": bool(suspicious_commands),
    }


def nonvolatile_judge_changes(integrity: dict) -> list[str]:
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


def build_live_judge_trace_evidence(
    trace_path: Path,
    output_path: Path,
) -> int:
    """Write completed execution events without reasoning or image payloads."""
    retained = 0
    with output_path.open("w", encoding="utf-8") as output:
        for line in trace_path.read_text(errors="replace").splitlines():
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
            output.write(json.dumps(sanitized, ensure_ascii=False) + "\n")
            retained += 1
    return retained


def _bounded_evidence_text(value: object, limit: int) -> str:
    text = str(value or "")
    if len(text) <= limit:
        return text
    half = max(1, (limit - 80) // 2)
    omitted = len(text) - (half * 2)
    return (
        text[:half]
        + f"\n...[{omitted} chars omitted from prior Judge evidence]...\n"
        + text[-half:]
    )


def build_prior_judge_trace_evidence(
    trace_path: Path,
    output_path: Path,
) -> int:
    """Write a bounded, reasoning-free ledger of completed Judge tools."""
    retained = 0
    with output_path.open("w", encoding="utf-8") as output:
        for line in trace_path.read_text(errors="replace").splitlines():
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

            compact_item = {
                key: item[key]
                for key in ("id", "type", "tool", "exit_code", "status")
                if key in item
            }
            if "command" in item:
                compact_item["command"] = _bounded_evidence_text(
                    item["command"],
                    PRIOR_JUDGE_COMMAND_MAX_CHARS,
                )
            if "arguments" in item:
                arguments = item["arguments"]
                if not isinstance(arguments, str):
                    arguments = json.dumps(
                        arguments,
                        ensure_ascii=False,
                        sort_keys=True,
                    )
                compact_item["arguments"] = _bounded_evidence_text(
                    arguments,
                    PRIOR_JUDGE_COMMAND_MAX_CHARS,
                )
            for key in ("aggregated_output", "output", "text"):
                if key in item:
                    compact_item[f"{key}_excerpt"] = (
                        _bounded_evidence_text(
                            item[key],
                            PRIOR_JUDGE_OUTPUT_MAX_CHARS,
                        )
                    )
            if "result" in item:
                compact_item["result_excerpt"] = _bounded_evidence_text(
                    json.dumps(
                        item["result"],
                        ensure_ascii=False,
                        sort_keys=True,
                    ),
                    PRIOR_JUDGE_OUTPUT_MAX_CHARS,
                )
            output.write(json.dumps(
                {"type": "item.completed", "item": compact_item},
                ensure_ascii=False,
            ) + "\n")
            retained += 1
    return retained


def _guest_json_file(base_url: str, path: str) -> dict | None:
    raw = fetch_guest_file(base_url, path)
    if not raw:
        return None
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        return None
    return payload if isinstance(payload, dict) else None


def monitor_judge_workload(
    *,
    base_url: str,
    unit_name: str,
    timeout_seconds: int,
    tool_limit: int,
    attempt_token: str,
) -> dict:
    if tool_limit < 1:
        raise ValueError("Judge tool limit must be positive")
    if not attempt_token:
        raise ValueError("Judge attempt token must be non-empty")
    started = time.monotonic()
    deadline = started + timeout_seconds + 20
    peak_completed_tools = 0
    tool_budget_exhausted = False
    finalization_threshold_reached = False
    finalization_at = max(1, tool_limit - min(4, tool_limit - 1))
    last_status = None

    def sample_completed_tools() -> int:
        trace_raw = fetch_guest_file(base_url, GUEST_JUDGE_TRACE)
        if not trace_raw:
            return 0
        return len(
            completed_tool_events(
                trace_raw.decode(
                    "utf-8",
                    errors="replace",
                ).splitlines()
            )
        )

    while time.monotonic() < deadline:
        status = _guest_json_file(base_url, GUEST_JUDGE_STATUS)
        current_attempt = bool(
            status
            and status.get("attempt_token") == attempt_token
        )
        if current_attempt:
            last_status = status
            if status.get("state") == "exited":
                completed = sample_completed_tools()
                peak_completed_tools = max(
                    peak_completed_tools,
                    completed,
                )
                if completed >= finalization_at:
                    finalization_threshold_reached = True
                if completed >= tool_limit:
                    tool_budget_exhausted = True
                break
        else:
            time.sleep(JUDGE_STATUS_POLL_INTERVAL_SEC)
            continue
        completed = sample_completed_tools()
        peak_completed_tools = max(peak_completed_tools, completed)
        if completed >= finalization_at:
            finalization_threshold_reached = True
        if completed >= tool_limit:
            tool_budget_exhausted = True
            try:
                request_execute(
                    base_url,
                    [
                        "systemctl",
                        "--user",
                        "kill",
                        "--signal=TERM",
                        safe_tag(unit_name)[:200],
                    ],
                    timeout=30,
                )
            except Exception:
                pass
            break
        time.sleep(JUDGE_STATUS_POLL_INTERVAL_SEC)

    if tool_budget_exhausted:
        stop_deadline = time.monotonic() + 15
        while time.monotonic() < stop_deadline:
            status = _guest_json_file(base_url, GUEST_JUDGE_STATUS)
            if (
                status
                and status.get("attempt_token") == attempt_token
            ):
                last_status = status
                if status.get("state") == "exited":
                    break
            time.sleep(1)
    return {
        "status": last_status,
        "completed_tools": peak_completed_tools,
        "tool_limit": tool_limit,
        "finalization_at": finalization_at,
        "finalization_threshold_reached": finalization_threshold_reached,
        "tool_budget_exhausted": tool_budget_exhausted,
        "monitor_duration_seconds": round(time.monotonic() - started, 3),
    }


def run_live_judge(
    *,
    env: DesktopEnv,
    base_url: str,
    result_dir: Path,
    args: argparse.Namespace,
    run_tag: str,
    instruction: str,
    final_response: str,
    declared_infeasible: bool,
    base_instructions_file: Path,
    current_date: str,
    timezone_name: str,
    user_context: dict | None = None,
    retry_context: str | None = None,
    tool_limit: int | None = None,
    raw_evaluator_completed_before_judge: bool = True,
    sealed_before: dict | None = None,
    prior_judge_evidence_path: Path | None = None,
) -> dict:
    judge_dir = result_dir / "judge"
    judge_dir.mkdir(parents=True, exist_ok=True)
    before = sealed_before or capture_judge_state(
        env, base_url, judge_dir, "pre_judge"
    )

    prompt_template = Path(args.judge_prompt_file).resolve()
    schema_file = Path(args.judge_schema_file).resolve()
    for required in (prompt_template, schema_file):
        if not required.is_file():
            raise FileNotFoundError(required)

    config_path = judge_dir / "config.toml"
    prompt_path = judge_dir / "prompt.txt"
    schema_copy = judge_dir / "schema.json"
    base_copy = judge_dir / "codex-base-instructions.md"
    probe_copy = judge_dir / "judge-probe"
    final_response_path = judge_dir / "task_agent_final_response.txt"
    trace_path = result_dir / "agent.jsonl"
    if not trace_path.is_file():
        raise FileNotFoundError(
            "Task Agent execution trace is required before Judge staging: "
            f"{trace_path}"
        )
    execution_evidence_path = (
        judge_dir / "task_agent_execution_evidence.jsonl"
    )
    execution_event_count = build_live_judge_trace_evidence(
        trace_path,
        execution_evidence_path,
    )
    config_path.write_text(
        codex_config(
            model=args.judge_model,
            effort=args.judge_effort,
            gateway_base=args.gateway_base,
            affinity_id=run_tag,
            codex_home=GUEST_JUDGE_CODEX_HOME,
            base_instructions=GUEST_JUDGE_BASE_INSTRUCTIONS,
            affinity_prefix="osworld-v2-judge",
        ),
        encoding="utf-8",
    )
    prompt_text = build_judge_prompt(
            prompt_template,
            instruction=instruction,
            final_response=final_response,
            declared_infeasible=declared_infeasible,
            current_date=current_date,
            timezone_name=timezone_name,
            user_context=user_context,
        )
    effective_tool_limit = max(
        1,
        int(
            tool_limit
            if tool_limit is not None
            else args.judge_tool_limit
        ),
    )
    prompt_text = (
        prompt_text
        + "\n\nEnforced inspection budget:\n"
        + f"- hard completed-tool limit: {effective_tool_limit}\n"
        + "- finish evidence collection at least four calls before the hard "
        "limit and emit the final JSON verdict.\n"
        + "- the runner will terminate the exact Judge workload when the "
        "hard limit is reached.\n"
    )
    if retry_context:
        prompt_text = f"{prompt_text}\n\n{retry_context.strip()}\n"
    prompt_path.write_text(prompt_text, encoding="utf-8")
    schema_copy.write_bytes(schema_file.read_bytes())
    base_copy.write_bytes(base_instructions_file.read_bytes())
    probe_copy.write_text(LIVE_JUDGE_PROBE_SCRIPT, encoding="utf-8")
    final_response_path.write_text(final_response, encoding="utf-8")

    request_execute(
        base_url,
        [
            "bash",
            "-lc",
            f"rm -rf {shlex.quote(GUEST_JUDGE_WORK)} "
            "&& install -d "
            f"{shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
            f"{shlex.quote(GUEST_JUDGE_ROOT)} "
            f"{shlex.quote(GUEST_JUDGE_ROOT + '/artifacts')} "
            f"{shlex.quote(GUEST_JUDGE_WORK)} "
            f"&& test -w {shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
            f"&& test -w {shlex.quote(GUEST_JUDGE_ROOT)} "
            f"&& test -w {shlex.quote(GUEST_JUDGE_WORK)}",
        ],
    )
    upload_rows = [
        {"local_path": str(config_path), "path": GUEST_JUDGE_CONFIG},
        {
            "local_path": str(base_copy),
            "path": GUEST_JUDGE_BASE_INSTRUCTIONS,
        },
        {"local_path": str(prompt_path), "path": GUEST_JUDGE_PROMPT},
        {"local_path": str(schema_copy), "path": GUEST_JUDGE_SCHEMA},
        {
            "local_path": str(
                INTEGRATION_ROOT / "runner_overrides" / "pty_stream.py"
            ),
            "path": GUEST_JUDGE_PTY_STREAM,
        },
        {
            "local_path": str(final_response_path),
            "path": f"{GUEST_JUDGE_ROOT}/task_agent_final_response.txt",
        },
        {
            "local_path": str(execution_evidence_path),
            "path": (
                f"{GUEST_JUDGE_ROOT}/"
                "task_agent_execution_evidence.jsonl"
            ),
        },
        {
            "local_path": str(probe_copy),
            "path": GUEST_JUDGE_PROBE,
        },
        {
            "local_path": str(judge_dir / "pre_judge.a11y.txt"),
            "path": f"{GUEST_JUDGE_ROOT}/final_state.a11y.txt",
        },
        {
            "local_path": str(judge_dir / "pre_judge.json"),
            "path": f"{GUEST_JUDGE_ROOT}/final_state.json",
        },
    ]
    if (judge_dir / "pre_judge.png").is_file():
        upload_rows.append({
            "local_path": str(judge_dir / "pre_judge.png"),
            "path": f"{GUEST_JUDGE_ROOT}/final_state.png",
        })
    prior_judge_event_count = 0
    if prior_judge_evidence_path is not None:
        prior_judge_evidence_path = Path(prior_judge_evidence_path)
        if not prior_judge_evidence_path.is_file():
            raise FileNotFoundError(prior_judge_evidence_path)
        prior_judge_event_count = sum(
            1
            for line in prior_judge_evidence_path.read_text(
                errors="replace",
            ).splitlines()
            if line.strip()
        )
        upload_rows.append({
            "local_path": str(prior_judge_evidence_path),
            "path": GUEST_PRIOR_JUDGE_EVIDENCE,
        })
    env.setup_controller._upload_file_setup(upload_rows)
    request_execute(
        base_url,
        [
            "bash",
            "-lc",
            f"chmod 755 {shlex.quote(GUEST_JUDGE_PROBE)} "
            f"{shlex.quote(GUEST_JUDGE_PTY_STREAM)} "
            f"&& test -x {shlex.quote(GUEST_JUDGE_PROBE)} "
            f"&& test -x {shlex.quote(GUEST_JUDGE_PTY_STREAM)} "
            "&& test -r "
            f"{shlex.quote(GUEST_JUDGE_ROOT + '/task_agent_execution_evidence.jsonl')}",
        ],
    )
    sealed_evidence = {
        "container_path": GUEST_JUDGE_ROOT,
        "execution_evidence_file": (
            f"{GUEST_JUDGE_ROOT}/task_agent_execution_evidence.jsonl"
        ),
        "execution_event_count": execution_event_count,
        "prior_judge_execution_evidence_file": (
            GUEST_PRIOR_JUDGE_EVIDENCE
            if prior_judge_evidence_path is not None
            else None
        ),
        "prior_judge_execution_event_count": prior_judge_event_count,
        "uploaded_files": [
            Path(row["path"]).name
            for row in upload_rows
        ],
    }

    timeout = max(1, int(args.judge_timeout))
    image_arg = (
        f"-i {shlex.quote(GUEST_JUDGE_ROOT + '/final_state.png')} "
        if (judge_dir / "pre_judge.png").is_file()
        else ""
    )
    started = time.monotonic()
    attempt_token = hashlib.sha256(
        f"{run_tag}:{time.time_ns()}".encode()
    ).hexdigest()[:24]
    command = (
        "set +e; "
        "export HOME=/home/user "
        f"CODEX_HOME={shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
        f"CODEX_SQLITE_HOME={shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
        "PYTHONDONTWRITEBYTECODE=1 "
        "GIT_OPTIONAL_LOCKS=0; "
        f"cd {shlex.quote(GUEST_JUDGE_WORK)}; "
        f"rm -f {shlex.quote(GUEST_JUDGE_RESULT)} "
        f"{shlex.quote(GUEST_JUDGE_TRACE)} "
        f"{shlex.quote(GUEST_JUDGE_STDERR)} "
        f"{shlex.quote(GUEST_JUDGE_STATUS)}; "
        f"attempt_token={shlex.quote(attempt_token)}; "
        "started=$(date +%s); "
        "printf '{\"state\":\"running\",\"attempt_token\":\"%s\","
        "\"started_at_epoch\":%s}\\n' "
        "\"$attempt_token\" \"$started\" "
        f"> {shlex.quote(GUEST_JUDGE_STATUS)}; "
        f"{shlex.quote(GUEST_JUDGE_PTY_STREAM)} "
        f"--stdin {shlex.quote(GUEST_JUDGE_PROMPT)} "
        f"--stderr {shlex.quote(GUEST_JUDGE_STDERR)} -- "
        f"timeout --signal=TERM --kill-after=10s {timeout}s "
        f"{shlex.quote(GUEST_CODEX)} exec "
        "--strict-config --dangerously-bypass-approvals-and-sandbox "
        "--ephemeral --ignore-rules --skip-git-repo-check "
        f"{image_arg}"
        "--json "
        f"--output-schema {shlex.quote(GUEST_JUDGE_SCHEMA)} "
        f"--output-last-message {shlex.quote(GUEST_JUDGE_RESULT)} "
        f"- > {shlex.quote(GUEST_JUDGE_TRACE)}; "
        "rc=$?; signal=0; "
        "if [ \"$rc\" -ge 128 ]; then signal=$((rc-128)); fi; "
        "finished=$(date +%s); "
        "printf '{\"state\":\"exited\",\"attempt_token\":\"%s\","
        "\"started_at_epoch\":%s,\"finished_at_epoch\":%s,"
        "\"returncode\":%s,\"signal\":%s}\\n' "
        "\"$attempt_token\" \"$started\" \"$finished\" \"$rc\" \"$signal\" "
        f"> {shlex.quote(GUEST_JUDGE_STATUS)}; "
        "echo $rc"
    )
    execution_error = None
    monitor = {
        "status": None,
        "completed_tools": 0,
        "tool_limit": effective_tool_limit,
        "finalization_at": max(1, effective_tool_limit - 4),
        "finalization_threshold_reached": False,
        "tool_budget_exhausted": False,
        "monitor_duration_seconds": 0.0,
    }
    verdict_raw = None
    status_raw = None
    artifact_fetch_errors = {}
    judge_unit_name = f"osw-judge-{run_tag}"
    try:
        request_execute(
            base_url,
            transient_workload_command(
                ["bash", "-lc", command],
                unit_name=judge_unit_name,
                wait=False,
            ),
            timeout=30,
        )
        monitor = monitor_judge_workload(
            base_url=base_url,
            unit_name=judge_unit_name,
            timeout_seconds=timeout,
            tool_limit=effective_tool_limit,
            attempt_token=attempt_token,
        )
    except Exception as exc:
        execution_error = f"{type(exc).__name__}: {exc}"
    finally:
        verdict_raw, fetch_error = save_guest_artifact_best_effort(
            base_url,
            GUEST_JUDGE_RESULT,
            judge_dir / "verdict.json",
        )
        if fetch_error:
            artifact_fetch_errors["verdict.json"] = fetch_error
        _, fetch_error = save_guest_artifact_best_effort(
            base_url,
            GUEST_JUDGE_TRACE,
            judge_dir / "agent.jsonl",
        )
        if fetch_error:
            artifact_fetch_errors["agent.jsonl"] = fetch_error
        _, fetch_error = save_guest_artifact_best_effort(
            base_url,
            GUEST_JUDGE_STDERR,
            judge_dir / "stderr.log",
        )
        if fetch_error:
            artifact_fetch_errors["stderr.log"] = fetch_error
        status_raw, fetch_error = save_guest_artifact_best_effort(
            base_url,
            GUEST_JUDGE_STATUS,
            judge_dir / "status.json",
        )
        if fetch_error:
            artifact_fetch_errors["status.json"] = fetch_error
    returncode = None
    status = None
    if status_raw:
        try:
            status = json.loads(status_raw)
        except json.JSONDecodeError:
            status = None
    if (
        isinstance(status, dict)
        and isinstance(status.get("returncode"), int)
    ):
        returncode = status["returncode"]
    elif (
        isinstance(monitor.get("status"), dict)
        and isinstance(monitor["status"].get("returncode"), int)
    ):
        returncode = monitor["status"]["returncode"]
    verdict = None
    if verdict_raw:
        try:
            verdict = json.loads(verdict_raw)
        except json.JSONDecodeError:
            verdict = None

    after = capture_judge_state(
        env,
        base_url,
        judge_dir,
        "post_judge",
    )
    integrity = compare_judge_states(before, after, judge_dir)
    judge_trace_path = judge_dir / "agent.jsonl"
    mutation_audit = live_judge_mutation_audit(
        judge_trace_path.read_text(
            encoding="utf-8",
            errors="replace",
        )
        if judge_trace_path.is_file()
        else ""
    )
    nonvolatile_changes = nonvolatile_judge_changes(integrity)
    state_capture_errors = [
        error
        for error in (
            before.get("file_manifest_error"),
            after.get("file_manifest_error"),
        )
        if error
    ]
    control_error = execution_error
    if state_capture_errors:
        control_error = "; ".join(state_capture_errors)
    failure_classification = classify_judge_attempt(
        returncode=returncode,
        status=status,
        execution_error=control_error,
        verdict=verdict,
        tool_budget_exhausted=bool(
            monitor.get("tool_budget_exhausted")
        ),
    )
    metadata = {
        "judge_mode": "interactive_outcome",
        "model": args.judge_model,
        "effort": args.judge_effort,
        "duration_seconds": round(time.monotonic() - started, 1),
        "returncode": returncode,
        "status": status,
        "execution_error": execution_error,
        "state_capture_errors": state_capture_errors,
        "monitor": monitor,
        "attempt_token": attempt_token,
        "artifact_fetch_errors": artifact_fetch_errors,
        "failure_classification": failure_classification,
        "sealed_evidence": sealed_evidence,
        "verdict": verdict,
        "strict_instruction_verdict": (
            verdict.get("instruction_verdict")
            if valid_judge_verdict(verdict)
            else None
        ),
        "judge_valid": (
            returncode == 0
            and execution_error is None
            and not state_capture_errors
            and valid_judge_verdict(verdict)
        ),
        "integrity": {**integrity, "before": before, "after": after},
        "mutation_audit": mutation_audit,
        "nonvolatile_file_changes": nonvolatile_changes,
        "stdout": "judge/agent.jsonl",
        "stderr": "judge/stderr.log",
        "status_file": "judge/status.json",
        "raw_evaluator_attempted_before_judge": True,
        "raw_evaluator_completed_before_judge": (
            raw_evaluator_completed_before_judge
        ),
    }
    (judge_dir / "metadata.json").write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return metadata


def run_live_judge_with_retry(**kwargs) -> dict:
    result_dir = Path(kwargs["result_dir"])
    judge_dir = result_dir / "judge"
    first = run_live_judge(**kwargs)
    first_dir = archive_judge_attempt(judge_dir, 1)
    attempts = [
        {
            "attempt": 1,
            "artifact_dir": first_dir.name,
            "judge_valid": first.get("judge_valid"),
            "failure_classification": first.get("failure_classification"),
            "duration_seconds": first.get("duration_seconds"),
        }
    ]
    selected = first
    selected_attempt = 1

    if judge_retry_eligible(first):
        retry_kwargs = dict(kwargs)
        initial_seal = (first.get("integrity") or {}).get("before")
        if (
            isinstance(initial_seal, dict)
            and initial_seal.get("file_manifest_sha256")
            and not initial_seal.get("file_manifest_error")
        ):
            retry_kwargs["sealed_before"] = initial_seal
        prior_judge_evidence_path = (
            judge_dir / "prior_judge_execution_evidence.jsonl"
        )
        prior_judge_trace_path = first_dir / "agent.jsonl"
        prior_judge_event_count = 0
        if prior_judge_trace_path.is_file():
            prior_judge_event_count = build_prior_judge_trace_evidence(
                prior_judge_trace_path,
                prior_judge_evidence_path,
            )
            retry_kwargs["prior_judge_evidence_path"] = (
                prior_judge_evidence_path
            )
        retry_kwargs["run_tag"] = f"{kwargs['run_tag']}-judge-retry-02"
        retry_kwargs["retry_context"] = (
            JUDGE_RETRY_CONVERGENCE_PROMPT
            + "\n- Prior-attempt evidence contains "
            f"{prior_judge_event_count} completed tool events."
        )
        retry_kwargs["tool_limit"] = max(
            1,
            int(kwargs["args"].judge_retry_tool_limit),
        )
        second = run_live_judge(**retry_kwargs)
        second_dir = archive_judge_attempt(judge_dir, 2)
        attempts.append(
            {
                "attempt": 2,
                "artifact_dir": second_dir.name,
                "judge_valid": second.get("judge_valid"),
                "failure_classification": second.get(
                    "failure_classification"
                ),
                "duration_seconds": second.get("duration_seconds"),
            }
        )
        selected = second
        selected_attempt = 2

    aggregate = dict(selected)
    aggregate["attempts"] = attempts
    aggregate["selected_attempt"] = selected_attempt
    aggregate["retry_assisted"] = selected_attempt > 1
    (judge_dir / "metadata.json").write_text(
        json.dumps(aggregate, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return aggregate


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


def user_question(final_message: str) -> str | None:
    body, sentinel = split_completion_message(final_message)
    if sentinel is not None:
        return None
    lines = body.splitlines()
    if lines and lines[0].strip() == ASK_USER_SENTINEL:
        question = "\n".join(lines[1:]).strip()
        if not question:
            raise ValueError("ASK_USER response did not include a question")
        return question
    return body


def user_response_prompt(answer: str) -> str:
    return f"""\
User response: {answer}

Treat this answer as resolving only the facts or choices it states. Before any
affected external or irreversible action, re-check the source evidence and the
original instruction for other unresolved eligibility, status, range,
assignment, recipient, or publication rules. If another material rule remains
unresolved and the user invited questions, use ASK_USER again. Do not treat one
answer as blanket approval for assumptions that were not asked or answered.
"""


def summarize_trace(raw: bytes | None) -> dict:
    summary = {
        "events": 0,
        "mcp_calls": 0,
        "shell_calls": 0,
        "tool_breakdown": {},
        "input_tokens": 0,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "last_agent_message": "",
        "last_substantive_message": "",
        "completion_sentinel": None,
        "agent_error": None,
        "agent_error_original_chars": 0,
        "failure_classification": None,
    }
    if not raw:
        return summary

    for line in raw.decode("utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        summary["events"] += 1
        if event.get("type") == "turn.failed":
            failure = event.get("error") or {}
            if isinstance(failure, dict):
                message = str(
                    failure.get("message")
                    or failure.get("code")
                    or failure
                )
            else:
                message = str(failure)
            message = message or "turn failed"
            summary["agent_error_original_chars"] = len(message)
            summary["failure_classification"] = (
                classify_codex_turn_failure(message)
            )
            summary["agent_error"] = concise_failure_message(message)
        if event.get("type") == "turn.completed":
            usage = event.get("usage") or {}
            summary["input_tokens"] += int(usage.get("input_tokens") or 0)
            summary["cached_input_tokens"] += int(
                usage.get("cached_input_tokens") or 0
            )
            summary["output_tokens"] += int(usage.get("output_tokens") or 0)
            summary["reasoning_tokens"] += int(
                usage.get("reasoning_output_tokens") or 0
            )
        if event.get("type") != "item.completed":
            continue
        item = event.get("item") or {}
        item_type = item.get("type")
        if item_type == "agent_message":
            message = str(item.get("text") or "")
            summary["last_agent_message"] = message
            body, sentinel = split_completion_message(message)
            if body:
                summary["last_substantive_message"] = body
            if sentinel is not None:
                summary["completion_sentinel"] = sentinel
        elif item_type == "mcp_tool_call":
            summary["mcp_calls"] += 1
            name = str(item.get("tool") or item.get("name") or "unknown")
            summary["tool_breakdown"][name] = (
                summary["tool_breakdown"].get(name, 0) + 1
            )
        elif item_type == "command_execution":
            summary["shell_calls"] += 1
    return summary


def summarize_claude_trace(raw: bytes | None) -> dict:
    summary = {
        "events": 0,
        "mcp_calls": 0,
        "shell_calls": 0,
        "tool_breakdown": {},
        "input_tokens": 0,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "last_agent_message": "",
        "last_substantive_message": "",
        "completion_sentinel": None,
        "agent_error": None,
        "agent_error_original_chars": 0,
        "failure_classification": None,
    }
    if not raw:
        return summary

    shell_tools = {"Bash", "BashOutput", "KillShell"}
    for line in raw.decode("utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        summary["events"] += 1
        if event.get("type") == "assistant":
            for block in (event.get("message") or {}).get("content", []) or []:
                block_type = block.get("type")
                if block_type == "thinking":
                    summary["reasoning_tokens"] += int(
                        (block.get("usage") or {}).get("thinking_tokens") or 0
                    )
                elif block_type == "text":
                    message = str(block.get("text") or "")
                    if not message.strip():
                        continue
                    summary["last_agent_message"] = message
                    body, sentinel = split_completion_message(message)
                    if body:
                        summary["last_substantive_message"] = body
                    if sentinel is not None:
                        summary["completion_sentinel"] = sentinel
                elif block_type == "tool_use":
                    name = str(block.get("name") or "unknown")
                    if name in shell_tools:
                        summary["shell_calls"] += 1
                    elif name.startswith("mcp__"):
                        summary["mcp_calls"] += 1
                        short_name = name.rsplit("__", 1)[-1]
                        summary["tool_breakdown"][short_name] = (
                            summary["tool_breakdown"].get(short_name, 0) + 1
                        )
        elif event.get("type") == "result":
            usage = event.get("usage") or {}
            summary["input_tokens"] = int(usage.get("input_tokens") or 0)
            summary["cached_input_tokens"] = int(
                usage.get("cache_read_input_tokens") or 0
            )
            summary["output_tokens"] = int(usage.get("output_tokens") or 0)
            summary["reasoning_tokens"] = max(
                summary["reasoning_tokens"],
                int(
                    (usage.get("output_tokens_details") or {}).get(
                        "thinking_tokens"
                    )
                    or 0
                ),
            )
            message = str(event.get("result") or "")
            if message.strip():
                summary["last_agent_message"] = message
                body, sentinel = split_completion_message(message)
                if body:
                    summary["last_substantive_message"] = body
                if sentinel is not None:
                    summary["completion_sentinel"] = sentinel
            if event.get("is_error"):
                error = message or str(event.get("error") or "Claude Code failed")
                summary["agent_error_original_chars"] = len(error)
                summary["failure_classification"] = (
                    classify_codex_turn_failure(error)
                )
                summary["agent_error"] = concise_failure_message(error)
    return summary


def summarize_agent_trace(raw: bytes | None, agent: str) -> dict:
    if agent == "claude-code":
        return summarize_claude_trace(raw)
    return summarize_trace(raw)


def collect_task_proxy_usage(
    token_log: Path,
    affinity_task: str,
) -> tuple[dict, bytes]:
    summary = {
        "status": "missing",
        "request_attempts": 0,
        "model_requests": 0,
        "budget_rejected_requests": 0,
        "accounted_responses": 0,
        "unaccounted_requests": 0,
        "input_tokens": 0,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "total_tokens": 0,
        "errors": 0,
        "rate_limited_requests": 0,
        "endpoints": [],
        "endpoint_affinity_ok": True,
    }
    if not token_log.is_file():
        return summary, b""

    matching_lines = []
    endpoints = set()
    with token_log.open(
        "r",
        encoding="utf-8",
        errors="replace",
    ) as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if row.get("task") != affinity_task:
                continue
            matching_lines.append(
                json.dumps(row, ensure_ascii=False, separators=(",", ":"))
            )
            summary["request_attempts"] += 1
            if row.get("budget_rejected") is True:
                summary["budget_rejected_requests"] += 1
                continue
            summary["model_requests"] += 1
            if row.get("endpoint"):
                endpoints.add(str(row["endpoint"]))
            if (
                row.get("error")
                or row.get("accounting_error")
                or row.get("upstream_error")
                or int(row.get("http_status") or 0) >= 400
            ):
                summary["errors"] += 1
            if row.get("rate_limited") or int(
                row.get("http_status") or 0
            ) == 429:
                summary["rate_limited_requests"] += 1
            if row.get("input_tokens") is None:
                continue
            summary["accounted_responses"] += 1
            summary["input_tokens"] += int(
                row.get("input_tokens") or 0
            )
            summary["cached_input_tokens"] += int(
                row.get("cached_tokens") or 0
            )
            summary["output_tokens"] += int(
                row.get("output_tokens") or 0
            )
            summary["reasoning_tokens"] += int(
                row.get("reasoning_tokens") or 0
            )
            summary["total_tokens"] += int(
                row.get("total_tokens") or 0
            )

    summary["unaccounted_requests"] = (
        summary["model_requests"] - summary["accounted_responses"]
    )
    summary["endpoints"] = sorted(endpoints)
    summary["endpoint_affinity_ok"] = len(endpoints) <= 1
    summary["status"] = (
        "completed" if summary["request_attempts"] else "no_matching_rows"
    )
    frozen_rows = (
        ("\n".join(matching_lines) + "\n").encode("utf-8")
        if matching_lines
        else b""
    )
    return summary, frozen_rows


def reconcile_trace_and_proxy_usage(
    trace_summary: dict,
    proxy_usage: dict,
    agent: str = "codex",
) -> dict:
    trace_usage = {
        "input_tokens": int(trace_summary.get("input_tokens") or 0),
        "cached_input_tokens": int(
            trace_summary.get("cached_input_tokens") or 0
        ),
        "output_tokens": int(trace_summary.get("output_tokens") or 0),
        "reasoning_tokens": int(
            trace_summary.get("reasoning_tokens") or 0
        ),
    }
    trace_summary["trace_usage"] = trace_usage
    trace_summary["proxy_usage"] = proxy_usage
    if int(proxy_usage.get("accounted_responses") or 0) <= 0:
        trace_summary["token_usage_source"] = f"{agent}_trace"
        trace_summary["token_usage_reconciled"] = False
        return trace_summary

    proxy_totals = {
        "input_tokens": int(proxy_usage.get("input_tokens") or 0),
        "cached_input_tokens": int(
            proxy_usage.get("cached_input_tokens") or 0
        ),
        "output_tokens": int(proxy_usage.get("output_tokens") or 0),
        "reasoning_tokens": int(
            proxy_usage.get("reasoning_tokens") or 0
        ),
    }
    trace_summary.update(proxy_totals)
    trace_summary["token_usage_source"] = "proxy_token_log"
    trace_summary["token_usage_reconciled"] = (
        trace_usage != proxy_totals
    )
    return trace_summary


def normalize_budget_termination(
    trace_summary: dict,
    *,
    budget_exhausted: bool,
) -> dict:
    if not budget_exhausted:
        return trace_summary
    original = trace_summary.get("failure_classification")
    if original and original != "model_call_budget_exhausted":
        trace_summary["failure_classification_original"] = original
    trace_summary["failure_classification"] = (
        "model_call_budget_exhausted"
    )
    return trace_summary


def classify_codex_turn_failure(message: str) -> str:
    normalized = (message or "").casefold()
    if "exceeded rate limit" in normalized or "rate_limit" in normalized:
        return "upstream_rate_limit"
    if (
        "tool_argument_too_large" in normalized
        or "argument list too long" in normalized
        or "os error 7" in normalized
    ):
        return "tool_argument_too_large"
    if "no space left on device" in normalized or "os error 28" in normalized:
        return "disk_full"
    if "out of memory" in normalized or "oom-kill" in normalized:
        return "guest_oom"
    return "unclassified_turn_failure"


def concise_failure_message(message: str) -> str:
    message = message or ""
    classification = classify_codex_turn_failure(message)
    if classification == "tool_argument_too_large":
        size_match = re.search(
            r"largest argv element is (\d+) bytes and total argv size is "
            r"(\d+) bytes",
            message,
            flags=re.IGNORECASE,
        )
        sizes = (
            f" largest_argv_bytes={size_match.group(1)}"
            f" total_argv_bytes={size_match.group(2)}"
            if size_match
            else ""
        )
        return (
            "tool_argument_too_large: command was rejected before execution; "
            "use an existing file or stream data through stdin/write_stdin."
            f"{sizes} original_chars={len(message)}"
        )
    if len(message) <= FAILURE_SUMMARY_MAX_CHARS:
        return message
    prefix = message[:FAILURE_SUMMARY_MAX_CHARS]
    return (
        f"{prefix}\n[error summary truncated: original_chars={len(message)}]"
    )


def transient_resume_prompt(failure_message: str) -> str:
    return (
        "The previous model response stream ended before a completion "
        "declaration because of a transient upstream transport failure. "
        "Continue in this same task session from the current durable VM state. "
        "Do not repeat actions that are already complete. Inspect the current "
        "state before any mutation, then finish the original instruction.\n\n"
        f"Recorded transport failure: {failure_message}"
    )


def tool_argument_resume_prompt() -> str:
    return (
        "The previous shell tool call was rejected before process creation "
        "because its argv payload exceeded the operating-system limit. The "
        "rejected command had no side effects. Continue the original task in "
        "this same session without repeating or embedding that payload. "
        "Process an existing file, or start a small stdin-reading command with "
        "tty=true and stream data through write_stdin. Inspect current state "
        "before the next mutation."
    )


def count_model_requests(token_log: Path, affinity_task: str) -> int:
    count = 0
    with token_log.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if (
                row.get("task") == affinity_task
                and not row.get("budget_rejected")
            ):
                count += 1
    return count


def task_model_budget_rejected(
    token_log: Path,
    affinity_task: str,
) -> bool:
    with token_log.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if (
                row.get("task") == affinity_task
                and row.get("budget_rejected") is True
            ):
                return True
    return False


def save_guest_artifact(base_url: str, guest_path: str, local_path: Path) -> bytes | None:
    data = fetch_guest_file(base_url, guest_path)
    if data is not None:
        local_path.write_bytes(data)
    return data


def save_guest_artifact_best_effort(
    base_url: str,
    guest_path: str,
    local_path: Path,
) -> tuple[bytes | None, str | None]:
    try:
        return save_guest_artifact(
            base_url,
            guest_path,
            local_path,
        ), None
    except Exception as exc:
        error = f"{type(exc).__name__}: {exc}"
        local_path.with_suffix(local_path.suffix + ".fetch-error").write_text(
            error + "\n",
            encoding="utf-8",
        )
        return None, error


class AgentProcessLostError(RuntimeError):
    pass


class GuestFilesystemError(RuntimeError):
    pass


def collect_guest_infra_diagnostic(base_url: str) -> dict:
    command = r"""
set +e
echo '=== root_mount ==='
findmnt -no SOURCE,FSTYPE,OPTIONS /
echo '=== root_write_probe ==='
probe=/tmp/.osworld-infra-write-probe-$$
if printf ok >"$probe" 2>/tmp/osworld-write-probe.err; then
  echo writable
  rm -f "$probe"
else
  echo not_writable
  cat /tmp/osworld-write-probe.err
fi
echo '=== memory ==='
cat /proc/meminfo
echo '=== disk ==='
df -hT
echo '=== processes ==='
ps -eo pid,ppid,state,etimes,rss,cmd --sort=-rss | head -n 100
echo '=== kernel_dmesg ==='
dmesg --ctime 2>&1 | tail -n 300
echo '=== kernel_journal ==='
journalctl -k --no-pager -n 300 2>&1
exit 0
"""
    try:
        result = request_execute(
            base_url,
            ["bash", "-lc", command],
            timeout=60,
        )
        output = str(result.get("output", ""))
        transport_error = None
    except Exception as exc:
        output = ""
        transport_error = f"{type(exc).__name__}: {exc}"

    lowered = output.lower()
    classifications = []
    mount_line = ""
    if "=== root_mount ===" in output:
        mount_line = output.split("=== root_mount ===", 1)[1].splitlines()[1:2]
        mount_line = mount_line[0] if mount_line else ""
    mount_options = (
        mount_line.rsplit(None, 1)[-1].split(",")
        if mount_line.strip()
        else []
    )
    root_is_read_only = "ro" in {
        option.strip().casefold() for option in mount_options
    }
    disk_full = (
        "no space left on device" in lowered
        or any(
            line.rstrip().endswith("100% /")
            for line in output.splitlines()
        )
    )
    if (
        root_is_read_only
        or "ext4-fs error" in lowered
        or "aborting journal" in lowered
        or "remounting filesystem read-only" in lowered
    ):
        classifications.append("guest_root_filesystem_failure")
    elif disk_full:
        classifications.append("guest_disk_full")
    elif "not_writable" in lowered:
        classifications.append("guest_write_failure")
    if (
        "out of memory" in lowered
        or "oom-kill" in lowered
        or "killed process" in lowered
    ):
        classifications.append("guest_oom_kill")
    if transport_error:
        classifications.append("guest_control_transport_failure")
    if not classifications:
        classifications.append("unclassified_guest_process_loss")

    return {
        "classifications": classifications,
        "transport_error": transport_error,
        "output": output,
        "collected_at": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ",
            time.gmtime(),
        ),
    }


def parse_systemd_show(output: str) -> dict[str, str]:
    properties = {}
    in_show = False
    for line in output.splitlines():
        if line == "=== systemctl_show ===":
            in_show = True
            continue
        if line.startswith("=== ") and line.endswith(" ==="):
            in_show = False
            continue
        if not in_show or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key and key.replace("_", "").isalnum():
            properties[key] = value
    return properties


def collect_workload_unit_diagnostic(
    base_url: str,
    unit_name: str,
) -> dict:
    normalized = safe_tag(unit_name)[:200]
    if not normalized or normalized != unit_name:
        raise ValueError(f"Invalid workload unit name: {unit_name!r}")
    command = r"""
set +e
unit=$1
echo '=== workload_unit ==='
printf '%s\n' "$unit"
echo '=== systemctl_show ==='
systemctl --user show "$unit" --no-pager --all 2>&1
show_rc=$?
echo '=== systemctl_show_result ==='
printf 'systemctl_show_rc=%s\n' "$show_rc"
echo '=== unit_journal ==='
journalctl --user --unit "$unit" --no-pager -n 300 -o short-precise 2>&1
journal_rc=$?
echo '=== unit_journal_result ==='
printf 'unit_journal_rc=%s\n' "$journal_rc"
exit 0
"""
    try:
        result = request_execute(
            base_url,
            ["bash", "-lc", command, "bash", normalized],
            timeout=60,
        )
        output = str(result.get("output", ""))
        transport_error = None
    except Exception as exc:
        output = ""
        transport_error = f"{type(exc).__name__}: {exc}"

    properties = parse_systemd_show(output)
    result_value = properties.get("Result", "").casefold()
    active_state = properties.get("ActiveState", "").casefold()
    exec_status = properties.get("ExecMainStatus", "")
    oom_killed = properties.get("OOMKilled", "").casefold() == "yes"
    classifications = []
    if oom_killed or result_value == "oom-kill":
        classifications.append("guest_workload_oom")
    elif (
        active_state == "failed"
        or result_value not in {"", "success"}
        or exec_status not in {"", "0"}
    ):
        classifications.append("guest_workload_unit_failure")
    if transport_error:
        classifications.append("guest_workload_diagnostic_transport_failure")
    if not properties and not transport_error:
        classifications.append("guest_workload_unit_unavailable")

    return {
        "unit_name": normalized,
        "properties": properties,
        "classifications": classifications,
        "transport_error": transport_error,
        "output": output,
        "collected_at": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ",
            time.gmtime(),
        ),
    }


class AgentWatchdog:
    def __init__(
        self,
        result_dir: Path,
        *,
        workload_unit: str,
        process_name: str = "codex",
        grace_sec: float = AGENT_WATCHDOG_GRACE_SEC,
        interval_sec: float = AGENT_WATCHDOG_INTERVAL_SEC,
        absent_threshold: int = AGENT_WATCHDOG_ABSENT_THRESHOLD,
    ):
        self.result_dir = result_dir
        self.workload_unit = workload_unit
        self.process_name = process_name
        self.grace_sec = grace_sec
        self.interval_sec = interval_sec
        self.absent_threshold = absent_threshold
        self.started_at = time.monotonic()
        self.last_check_at: float | None = None
        self.consecutive_absences = 0

    def guest_state(self, base_url: str) -> str:
        result = request_execute(
            base_url,
            [
                "bash",
                "-lc",
                "options=$(findmnt -no OPTIONS / 2>/dev/null || true); "
                "available_kb=$(df -Pk / 2>/dev/null "
                "| awk 'NR==2 {print $4}'); "
                "probe=/tmp/.osworld-watchdog-write-$$; "
                "if case \",$options,\" in *,ro,*) true;; *) false;; esac; "
                "then echo filesystem_failure; "
                "elif [ \"${available_kb:-0}\" -le 0 ]; "
                "then echo disk_full; "
                "elif ! (umask 077; printf ok >\"$probe\") 2>/dev/null; "
                "then echo filesystem_failure; "
                "elif rm -f \"$probe\" 2>/dev/null "
                f"&& pgrep -x {shlex.quote(self.process_name)} >/dev/null; then "
                f"echo {shlex.quote(self.process_name)}; "
                "elif pgrep -f '^/bin/bash /tmp/osworld-v2-hybrid-run.sh' "
                ">/dev/null; then "
                "echo launcher; "
                "else echo absent; fi",
            ],
            timeout=30,
        )
        return str(result.get("output", "")).strip()

    def preserve(self, base_url: str, state: str, label: str) -> None:
        preserved = {}
        artifact_map = {
            GUEST_TRACE: f"{label}-agent.jsonl",
            GUEST_STDERR: f"{label}-agent.stderr",
            GUEST_FINAL: f"{label}-final.txt",
            GUEST_LAUNCHER_STATUS: f"{label}-launcher-status.json",
        }
        for guest_path, local_name in artifact_map.items():
            local_path = self.result_dir / local_name
            try:
                data = fetch_guest_file(
                    base_url,
                    guest_path,
                    attempts=3,
                    initial_backoff_sec=1.0,
                )
            except requests.RequestException:
                data = None
            if data is not None:
                local_path.write_bytes(data)
                preserved[local_name] = len(data)

        screenshot_path = self.result_dir / f"{label}-agent-process-lost.png"
        try:
            response = requests.get(f"{base_url}/screenshot", timeout=30)
            response.raise_for_status()
            screenshot_path.write_bytes(response.content)
            preserved[screenshot_path.name] = len(response.content)
        except requests.RequestException:
            pass

        infra = collect_guest_infra_diagnostic(base_url)
        infra_text_path = self.result_dir / f"{label}-guest-infra.txt"
        if infra["output"]:
            infra_text_path.write_text(
                infra["output"],
                encoding="utf-8",
            )
            preserved[infra_text_path.name] = len(
                infra["output"].encode("utf-8")
            )

        workload = collect_workload_unit_diagnostic(
            base_url,
            self.workload_unit,
        )
        workload_text_path = (
            self.result_dir / f"{label}-workload-unit.txt"
        )
        if workload["output"]:
            workload_text_path.write_text(
                workload["output"],
                encoding="utf-8",
            )
            preserved[workload_text_path.name] = len(
                workload["output"].encode("utf-8")
            )

        diagnostic = {
            "status": "agent_process_lost",
            "guest_process_state": state,
            "workload_unit": self.workload_unit,
            "workload_properties": workload["properties"],
            "workload_classifications": workload["classifications"],
            "workload_transport_error": workload["transport_error"],
            "consecutive_absences": self.consecutive_absences,
            "preserved_artifacts": preserved,
            "infra_classifications": infra["classifications"],
            "infra_transport_error": infra["transport_error"],
            "detected_at": time.strftime(
                "%Y-%m-%dT%H:%M:%SZ",
                time.gmtime(),
            ),
        }
        (self.result_dir / f"{label}-agent-process-lost.json").write_text(
            json.dumps(diagnostic, indent=2),
            encoding="utf-8",
        )

    def check(self, base_url: str, label: str) -> None:
        now = time.monotonic()
        if now - self.started_at < self.grace_sec:
            return
        if (
            self.last_check_at is not None
            and now - self.last_check_at < self.interval_sec
        ):
            return
        self.last_check_at = now
        try:
            state = self.guest_state(base_url)
        except Exception as exc:
            print(
                "Agent watchdog process check failed: "
                f"{type(exc).__name__}: {exc}",
                file=sys.stderr,
                flush=True,
            )
            return
        if state in {self.process_name, "launcher"}:
            self.consecutive_absences = 0
            return
        if state in {"filesystem_failure", "disk_full"}:
            self.preserve(base_url, state, label)
            if state == "disk_full":
                raise GuestFilesystemError(
                    "Guest root filesystem exhausted all available space "
                    "during the agent run"
                )
            raise GuestFilesystemError(
                "Guest root filesystem became read-only during the agent run"
            )
        if state != "absent":
            self.consecutive_absences = 0
            return
        self.consecutive_absences += 1
        if self.consecutive_absences < self.absent_threshold:
            return
        self.preserve(base_url, state, label)
        raise AgentProcessLostError(
            f"{self.process_name} and the guest launcher disappeared before producing "
            f"{GUEST_DONE}"
        )


def append_artifact(path: Path, data: bytes | None) -> None:
    if not data:
        return
    with path.open("ab") as handle:
        handle.write(data)
        if not data.endswith(b"\n"):
            handle.write(b"\n")


def upload_prompt(env: DesktopEnv, local_path: Path, content: str) -> None:
    write_local(local_path, content)
    env.setup_controller._upload_file_setup([
        {"local_path": str(local_path), "path": GUEST_PROMPT},
    ])


def wait_for_agent_turn(
    *,
    base_url: str,
    result_dir: Path,
    label: str,
    args: argparse.Namespace,
    token_log: Path,
    affinity_task: str,
    model_request_baseline: int,
    expected_turn_token: str,
    workload_unit: str,
    deadline: float,
) -> dict:
    agent = getattr(args, "agent", "codex")
    watchdog = AgentWatchdog(
        result_dir,
        workload_unit=workload_unit,
        process_name="claude" if agent == "claude-code" else "codex",
    )
    done_payload = None
    budget_exhausted = False
    while time.monotonic() < deadline:
        done_raw = fetch_guest_file(base_url, GUEST_DONE)
        if done_raw:
            candidate = json.loads(done_raw)
            if candidate.get("turn_token") == expected_turn_token:
                done_payload = candidate
                if (
                    args.max_model_calls > 0
                    and candidate.get("returncode") not in {0, None}
                    and task_model_budget_rejected(
                        token_log,
                        affinity_task,
                    )
                ):
                    budget_exhausted = True
                break
        watchdog.check(base_url, label)
        if args.max_model_calls > 0:
            model_requests = (
                count_model_requests(token_log, affinity_task)
                - model_request_baseline
            )
            if model_requests >= args.max_model_calls:
                budget_exhausted = True
                break
        time.sleep(args.poll_interval)

    truncated = done_payload is None or budget_exhausted
    if truncated:
        request_execute(
            base_url,
            [
                "bash",
                "-lc",
                "pids=$(pgrep -x "
                + ("claude" if agent == "claude-code" else "codex")
                + " || true); "
                "[ -z \"$pids\" ] || kill -TERM $pids",
            ],
            timeout=30,
        )
        time.sleep(3)
        done_raw = fetch_guest_file(base_url, GUEST_DONE)
        if done_raw:
            candidate = json.loads(done_raw)
            if candidate.get("turn_token") == expected_turn_token:
                done_payload = candidate

    trace_raw = save_guest_artifact(
        base_url,
        GUEST_TRACE,
        result_dir / f"{label}-agent.jsonl",
    )
    stderr_raw = save_guest_artifact(
        base_url,
        GUEST_STDERR,
        result_dir / f"{label}-agent.stderr",
    )
    final_raw = save_guest_artifact(
        base_url,
        GUEST_FINAL,
        result_dir / f"{label}-final.txt",
    )
    append_artifact(result_dir / "agent.jsonl", trace_raw)
    append_artifact(result_dir / "agent.stderr", stderr_raw)
    final_message = (
        final_raw.decode("utf-8", errors="replace").strip()
        if final_raw else ""
    )
    return {
        "done_payload": done_payload,
        "truncated": truncated,
        "budget_exhausted": budget_exhausted,
        "trace_raw": trace_raw,
        "stderr_raw": stderr_raw,
        "final_message": final_message,
        "thread_id": thread_id_from_trace(trace_raw, agent),
        "workload_unit": workload_unit,
    }


def run_agent_phase(
    *,
    env: DesktopEnv,
    base_url: str,
    result_dir: Path,
    prompt_path: Path,
    phase_index: int,
    phase_name: str,
    instruction: str,
    current_date: str | None,
    args: argparse.Namespace,
    token_log: Path,
    affinity_task: str,
    model_request_baseline: int,
    deadline: float,
    multi_phase: bool,
) -> dict:
    agent = getattr(args, "agent", "codex")
    can_ask_user = env.user_simulator is not None
    upload_prompt(
        env,
        prompt_path,
        task_prompt(
            instruction,
            current_date=current_date,
            can_ask_user=can_ask_user,
        ),
    )
    session_id = ""
    user_exchanges = []
    turns = []
    turn_index = 0
    transport_retries = 0
    tool_argument_retries = 0
    while True:
        turn_index += 1
        label = f"phase-{phase_index:02d}-turn-{turn_index:02d}"
        request_execute(
            base_url,
            [
                "rm",
                "-f",
                GUEST_DONE,
                GUEST_TRACE,
                GUEST_STDERR,
                GUEST_FINAL,
            ],
            timeout=30,
        )
        command = [GUEST_LAUNCHER, "start", "", label]
        if session_id:
            command = [GUEST_LAUNCHER, "resume", session_id, label]
        workload_unit = request_launch(base_url, command)
        write_local(
            result_dir / f"{label}-workload-unit.json",
            json.dumps(
                {
                    "unit_name": workload_unit,
                    "turn_token": label,
                    "requested_at": time.strftime(
                        "%Y-%m-%dT%H:%M:%SZ",
                        time.gmtime(),
                    ),
                },
                indent=2,
            ),
        )
        turn = wait_for_agent_turn(
            base_url=base_url,
            result_dir=result_dir,
            label=label,
            args=args,
            token_log=token_log,
            affinity_task=affinity_task,
            model_request_baseline=model_request_baseline,
            expected_turn_token=label,
            workload_unit=workload_unit,
            deadline=deadline,
        )
        trace_summary = summarize_agent_trace(turn["trace_raw"], agent)
        observed_thread_id = turn["thread_id"] or session_id
        turns.append({
            "turn_index": turn_index,
            "label": label,
            "thread_id": observed_thread_id,
            "workload_unit": workload_unit,
            "agent_returncode": (
                turn["done_payload"].get("returncode")
                if turn["done_payload"] else None
            ),
            "truncated": turn["truncated"],
            "budget_exhausted": turn["budget_exhausted"],
            "final_message": turn["final_message"],
            "agent_error": trace_summary["agent_error"],
            "failure_classification": (
                trace_summary["failure_classification"]
            ),
            "retry_assisted": (
                transport_retries > 0 or tool_argument_retries > 0
            ),
        })
        if turn["truncated"]:
            return {
                "phase_index": phase_index,
                "phase_name": phase_name,
                "instruction": instruction,
                "turns": turns,
                "user_exchanges": user_exchanges,
                "final_message": turn["final_message"],
                "done_payload": turn["done_payload"],
                "truncated": True,
                "budget_exhausted": turn["budget_exhausted"],
            }
        returncode = (
            turn["done_payload"].get("returncode")
            if turn["done_payload"] else None
        )
        if returncode not in {0, None}:
            failure_classification = trace_summary[
                "failure_classification"
            ]
            if (
                failure_classification == "upstream_rate_limit"
                and observed_thread_id
                and transport_retries < STREAM_RETRY_LIMIT
                and time.monotonic() + STREAM_RETRY_BACKOFF_SEC < deadline
            ):
                transport_retries += 1
                session_id = observed_thread_id
                upload_prompt(
                    env,
                    prompt_path,
                    transient_resume_prompt(
                        trace_summary["agent_error"]
                        or "upstream rate limit"
                    ),
                )
                print(
                    f"{agent} stream rate limit; resuming the same session after "
                    f"{STREAM_RETRY_BACKOFF_SEC:.0f}s "
                    f"({transport_retries}/{STREAM_RETRY_LIMIT})",
                    file=sys.stderr,
                    flush=True,
                )
                time.sleep(STREAM_RETRY_BACKOFF_SEC)
                continue
            if (
                failure_classification == "tool_argument_too_large"
                and observed_thread_id
                and tool_argument_retries < TOOL_ARGUMENT_RETRY_LIMIT
                and time.monotonic() < deadline
            ):
                tool_argument_retries += 1
                session_id = observed_thread_id
                upload_prompt(
                    env,
                    prompt_path,
                    tool_argument_resume_prompt(),
                )
                print(
                    f"{agent} shell argv exceeded the process limit; resuming "
                    "the same thread with binary-safe transfer guidance "
                    f"({tool_argument_retries}/{TOOL_ARGUMENT_RETRY_LIMIT})",
                    file=sys.stderr,
                    flush=True,
                )
                continue
            stderr_text = (
                turn["stderr_raw"].decode(
                    "utf-8",
                    errors="replace",
                ).strip()
                if turn["stderr_raw"] else ""
            )
            failure_message = concise_failure_message(
                trace_summary["agent_error"]
                or stderr_text
                or "no stderr"
            )
            raise RuntimeError(
                f"{agent} turn {turn_index} exited with return code "
                f"{returncode}: {failure_message}"
            )
        if not session_id:
            session_id = turn["thread_id"]
        question = user_question(turn["final_message"])
        if question is None:
            return {
                "phase_index": phase_index,
                "phase_name": phase_name,
                "instruction": instruction,
                "turns": turns,
                "user_exchanges": user_exchanges,
                "final_message": turn["final_message"],
                "done_payload": turn["done_payload"],
                "truncated": False,
                "budget_exhausted": False,
            }
        if not session_id:
            raise RuntimeError(
                f"ASK_USER requires a persisted {agent} session ID, "
                "but none was recorded"
            )
        if (
            args.max_user_simulator_turns > 0
            and len(user_exchanges) >= args.max_user_simulator_turns
        ):
            raise RuntimeError(
                f"Phase {phase_index} exceeded --max-user-simulator-turns="
                f"{args.max_user_simulator_turns}"
            )
        answer = DEFAULT_USER_RESPONSE
        if env.user_simulator is not None:
            answer = env.user_simulator.respond(question)
        exchange = {
            "phase_index": phase_index,
            "phase_name": phase_name,
            "turn_index": turn_index,
            "question": question,
            "answer": answer,
            "timestamp": datetime.datetime.now().strftime(
                "%Y%m%d@%H%M%S%f"
            ),
        }
        user_exchanges.append(exchange)
        with (result_dir / "user-simulator.jsonl").open(
            "a",
            encoding="utf-8",
        ) as handle:
            handle.write(json.dumps(exchange, ensure_ascii=False) + "\n")
        trajectory_row = {
            "step_num": turn_index,
            "action_timestamp": exchange["timestamp"],
            "action": ASK_USER_SENTINEL,
            "question": question,
            "user_answer": answer,
            "screenshot_file": None,
        }
        if multi_phase:
            trajectory_row.update({
                "phase_index": phase_index,
                "phase_name": phase_name,
            })
        with (result_dir / "traj.jsonl").open(
            "a",
            encoding="utf-8",
        ) as handle:
            handle.write(json.dumps(
                trajectory_row,
                ensure_ascii=False,
            ))
            handle.write("\n")
        upload_prompt(env, prompt_path, user_response_prompt(answer))


def main() -> int:
    args = parse_args()
    docker_runtime = validate_dedicated_docker_runtime()
    asset_runtime = validate_official_asset_runtime()
    run_tag = (
        f"{time.strftime('%Y%m%d-%H%M%S')}-task-{safe_tag(args.task_id)}-"
        f"{safe_tag(args.agent)}-{safe_tag(args.model)}-{safe_tag(args.effort)}"
    )
    results_root = Path(
        os.environ.get("HCU_RESULTS_DIR", REPO_ROOT / "runs")
    ).expanduser().resolve()
    benchmark_dir = f"osworld-{args.benchmark_version}"
    result_dir = Path(
        args.result_dir or results_root / benchmark_dir / run_tag
    ).resolve()
    result_dir.mkdir(parents=True, exist_ok=False)
    token_log_value = args.token_log or str(
        results_root
        / benchmark_dir
        / (
            "claude-tokens.jsonl"
            if args.agent == "claude-code"
            else "tokens.jsonl"
        )
    )
    token_log = Path(token_log_value).resolve()
    if args.max_model_calls > 0 and not token_log.is_file():
        raise FileNotFoundError(
            f"--max-model-calls requires the proxy token log: {token_log}"
        )
    affinity_task = f"osworld-{args.benchmark_version}-hybrid-{run_tag}"

    artifact_dir = Path(args.artifact_dir).resolve()
    codex_binary = Path(
        args.codex_binary or artifact_dir / "codex"
    ).resolve()
    claude_code_binary = Path(
        args.claude_code_binary or artifact_dir / "claude-code"
    ).resolve()
    mcp_binary = Path(
        args.mcp_binary or artifact_dir / "computer-use"
    ).resolve()
    skill_file = Path(args.skill_file).resolve()
    base_instructions_file = Path(
        args.base_instructions_file
    ).resolve()
    claude_mcp_adapter = (
        REPO_ROOT
        / "integrations/common/claude_mcp_schema_adapter.py"
    ).resolve()
    required_files = [mcp_binary, skill_file, base_instructions_file]
    if args.agent == "codex" or args.judge_model:
        required_files.append(codex_binary)
    if args.agent == "claude-code":
        required_files.append(claude_code_binary)
        required_files.append(claude_mcp_adapter)
    for required in required_files:
        if not required.is_file():
            raise FileNotFoundError(required)
    if args.judge_model:
        for required in (
            Path(args.judge_prompt_file).resolve(),
            Path(args.judge_schema_file).resolve(),
        ):
            if not required.is_file():
                raise FileNotFoundError(required)
    mcp_public_tools = list(MCP_PUBLIC_TOOLS)

    task = load_task_config(
        None,
        task_id=args.task_id,
        base_dir=str(ROOT / "evaluation_examples"),
        domain=args.task_domain or None,
        eval_version=args.benchmark_version,
    )
    phases = task_phases(task)
    run_started_at_utc = (
        str(os.environ.get("OSW_RUN_STARTED_AT_UTC") or "").strip()
        or datetime.datetime.now(datetime.timezone.utc).isoformat()
    )
    run_timezone = str(
        os.environ.get("OSW_RUN_TIMEZONE") or "UTC"
    ).strip()
    explicit_task_date = (
        task.get("task_current_date")
        if hasattr(task, "get") and callable(getattr(task, "get"))
        else None
    )
    if explicit_task_date is None:
        explicit_task_date = getattr(task, "task_current_date", None)
    current_date = task_current_date(
        task,
        run_started_at_utc=run_started_at_utc,
        timezone_name=run_timezone,
    )
    bind_task_runtime_timezone(task, run_timezone)
    instruction = str(task_value(task, "instruction", ""))
    guest_image = resolve_guest_image(task, args)
    if not instruction:
        raise ValueError(f"Task {args.task_id} has no instruction")

    config_path = result_dir / (
        "guest-config.toml"
        if args.agent == "codex"
        else "guest-mcp.json"
    )
    prompt_path = result_dir / "prompt.txt"
    launcher_path = result_dir / "guest-launcher.sh"
    skill_copy = result_dir / "AGENTS.md"
    base_instructions_copy = result_dir / "agent-base-instructions.md"
    if args.agent == "codex":
        write_local(
            config_path,
            codex_config(
                model=args.model,
                effort=args.effort,
                gateway_base=args.gateway_base,
                affinity_id=run_tag,
                affinity_prefix=(
                    f"osworld-{args.benchmark_version}-hybrid"
                ),
            ),
        )
    else:
        write_local(config_path, claude_mcp_config())
    write_local(
        prompt_path,
        task_prompt(
            instruction,
            current_date=current_date,
            can_ask_user=task_value(task, "user_simulator") is not None,
        ),
    )
    skill_copy.write_bytes(skill_file.read_bytes())
    base_instructions = base_instructions_file.read_text(encoding="utf-8")
    if args.agent == "claude-code":
        base_instructions += (
            "\n\n# Benchmark computer-use skill\n\n"
            + skill_file.read_text(encoding="utf-8")
        )
    write_local(base_instructions_copy, base_instructions)
    claude_endpoint = claude_gateway_endpoint(
        args.claude_gateway_base,
        run_tag,
        affinity_prefix=f"osworld-{args.benchmark_version}-hybrid",
    )
    write_local(
        launcher_path,
        launcher_script(
            agent=args.agent,
            model=args.model,
            effort=args.effort,
            claude_gateway_base=claude_endpoint,
            public_mcp_tools=mcp_public_tools,
        ),
    )

    meta = {
        "run_tag": run_tag,
        "task_id": args.task_id,
        "benchmark_version": args.benchmark_version,
        "task_domain": args.task_domain or None,
        "docker_runtime": docker_runtime,
        "asset_runtime": asset_runtime,
        "instruction": instruction,
        "task_current_date": current_date,
        "task_current_date_source": (
            "task_config" if explicit_task_date is not None else "run_start"
        ),
        "task_current_date_timezone": run_timezone,
        "run_started_at_utc": run_started_at_utc,
        "task_protocol": "multi_phase" if phases else "single_phase",
        "phase_count": len(phases) if phases else 1,
        "model": args.model,
        "effort": args.effort,
        "agent": args.agent,
        "gateway_base": args.gateway_base,
        "claude_gateway_base": (
            args.claude_gateway_base
            if args.agent == "claude-code"
            else None
        ),
        "claude_gateway_endpoint": (
            claude_endpoint
            if args.agent == "claude-code"
            else None
        ),
        "affinity_task": affinity_task,
        "token_log": str(token_log),
        "provider": "docker",
        "guest_image": guest_image,
        "codex_binary": (
            str(codex_binary)
            if args.agent == "codex" or args.judge_model
            else None
        ),
        "claude_code_binary": (
            str(claude_code_binary)
            if args.agent == "claude-code"
            else None
        ),
        "claude_mcp_adapter": (
            str(claude_mcp_adapter)
            if args.agent == "claude-code"
            else None
        ),
        "mcp_binary": str(mcp_binary),
        "mcp_public_tools": mcp_public_tools,
        "skill_file": str(skill_file),
        "base_instructions_file": str(base_instructions_file),
        "start_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "judge_model": args.judge_model or None,
        "judge_effort": args.judge_effort if args.judge_model else None,
        "judge_timeout_seconds": (
            args.judge_timeout if args.judge_model else None
        ),
        "judge_tool_limit": (
            args.judge_tool_limit if args.judge_model else None
        ),
        "judge_retry_tool_limit": (
            args.judge_retry_tool_limit if args.judge_model else None
        ),
        "judge_prompt_file": (
            str(Path(args.judge_prompt_file).resolve())
            if args.judge_model else None
        ),
        "judge_schema_file": (
            str(Path(args.judge_schema_file).resolve())
            if args.judge_model else None
        ),
        "status": "initializing",
        "score": None,
        "error": None,
    }
    write_local(result_dir / "meta.json", json.dumps(meta, indent=2))

    env = None
    recording_started = False
    start = time.monotonic()
    try:
        task_platform = str(task_value(task, "platform", "linux")).lower()
        os_type = "Windows" if task_platform.startswith("win") else "Ubuntu"
        if os_type != "Ubuntu":
            raise ValueError("The hybrid MCP prototype currently supports Ubuntu tasks only")

        task_volume_size = task_value(task, "volume_size")
        volume_size = args.volume_size
        if task_volume_size is not None:
            volume_size = max(volume_size, int(task_volume_size))
        os.environ["OSWORLD_DOCKER_RAM_SIZE"] = args.ram_size

        runtime_upload_rows = [
            {
                "local_path": str(base_instructions_copy),
                "path": GUEST_BASE_INSTRUCTIONS,
            },
            {"local_path": str(prompt_path), "path": GUEST_PROMPT},
            {"local_path": str(launcher_path), "path": GUEST_LAUNCHER},
        ]
        runtime_upload_rows.append(
            {"local_path": str(mcp_binary), "path": GUEST_MCP}
        )
        executable_paths = [GUEST_MCP, GUEST_LAUNCHER]
        if args.agent == "codex":
            runtime_upload_rows.extend([
                {"local_path": str(codex_binary), "path": GUEST_CODEX},
                {"local_path": str(config_path), "path": GUEST_CONFIG},
                {"local_path": str(skill_copy), "path": GUEST_SKILL},
            ])
            executable_paths.append(GUEST_CODEX)
        else:
            runtime_upload_rows.extend([
                {
                    "local_path": str(claude_code_binary),
                    "path": GUEST_CLAUDE_CODE,
                },
                {
                    "local_path": str(config_path),
                    "path": GUEST_CLAUDE_MCP_CONFIG,
                },
                {
                    "local_path": str(claude_mcp_adapter),
                    "path": GUEST_CLAUDE_MCP_ADAPTER,
                },
            ])
            executable_paths.append(GUEST_CLAUDE_CODE)
        if args.judge_model and args.agent != "codex":
            runtime_upload_rows.append({
                "local_path": str(codex_binary),
                "path": GUEST_CODEX,
            })
            executable_paths.append(GUEST_CODEX)

        def bootstrap_runtime(setup_controller) -> None:
            install_guest_runtime(
                setup_controller,
                client_password=args.client_password,
                upload_rows=runtime_upload_rows,
                executable_paths=tuple(executable_paths),
            )

        env = DesktopEnv(
            provider_name="docker",
            path_to_vm=guest_image,
            action_space="pyautogui",
            cache_dir=run_scoped_cache_dir(result_dir, "desktop-env"),
            headless=True,
            require_a11y_tree=False,
            os_type=os_type,
            client_password=args.client_password,
            volume_size=volume_size,
            force_disable_vnc=not args.enable_vnc,
            force_disable_recording=not args.enable_recording,
            before_task_setup=bootstrap_runtime,
        )
        meta["provider_mounts"] = validate_provider_mount_isolation(
            env.provider.container,
            guest_image,
        )
        meta["container_name"] = env.provider.container.name
        meta["vm_startup_attempts"] = getattr(
            env.provider,
            "startup_attempts",
            [],
        )
        meta["vm_runtime_environment"] = dict(
            getattr(env.provider, "environment", {})
        )
        meta["volume_size_gb"] = volume_size
        meta["ram_size"] = args.ram_size
        meta["status"] = "setup"
        write_local(result_dir / "meta.json", json.dumps(meta, indent=2))

        align_guest_run_clock(
            env.setup_controller,
            expected_date=current_date,
            timezone_name=run_timezone,
        )
        env.reset(task_config=task)
        reset_finished_at = time.monotonic()
        meta["setup_attempts"] = list(
            getattr(env, "setup_attempts", [])
        )
        meta["container_name"] = env.provider.container.name
        meta["vm_startup_attempts"] = getattr(
            env.provider,
            "startup_attempts",
            [],
        )
        base_url = env.setup_controller.http_server
        guest_date_probe = request_execute(
            base_url,
            ["date", "+%Y-%m-%d"],
            timeout=20,
        )
        meta["guest_current_date_after_setup"] = validate_guest_current_date(
            expected_date=current_date,
            command_output=guest_date_probe.get("output", ""),
        )
        env.setup_controller.assert_root_writable()
        meta["guest_root_health_after_setup"] = "writable"
        workload_preflight = request_execute(
            base_url,
            transient_workload_command(
                ["/usr/bin/true"],
                unit_name=f"osw-preflight-{run_tag}",
                wait=True,
            ),
            timeout=30,
        )
        meta["workload_cgroup_preflight"] = {
            "status": "passed",
            "output": str(
                workload_preflight.get("output") or ""
            ).strip(),
        }

        active_gateway_base = (
            args.claude_gateway_base
            if args.agent == "claude-code"
            else args.gateway_base
        )
        health = request_execute(
            base_url,
            [
                "curl",
                "-fsS",
                "--max-time",
                "10",
                active_gateway_base + "/healthz",
            ],
            timeout=20,
        )
        meta["gateway_health"] = health.get("output", "").strip()

        version_commands = [f"{shlex.quote(GUEST_MCP)} --version"]
        if args.agent == "codex" or args.judge_model:
            version_commands.insert(
                0,
                f"{shlex.quote(GUEST_CODEX)} --version",
            )
        if args.agent == "claude-code":
            version_commands.insert(
                0,
                f"{shlex.quote(GUEST_CLAUDE_CODE)} --version",
            )
        versions = request_execute(
            base_url,
            [
                "bash",
                "-lc",
                " && ".join(version_commands),
            ],
        )
        meta["guest_versions"] = versions.get("output", "").strip().splitlines()

        mcp_probe = request_execute(
            base_url,
            [
                "/usr/bin/python3",
                "-c",
                public_mcp_probe_script(),
                GUEST_MCP,
                json.dumps(mcp_public_tools),
            ],
            timeout=180,
        )
        meta["mcp_probe"] = mcp_probe.get("output", "").strip()
        initial_wait_remaining = max(
            0.0,
            60.0 - (time.monotonic() - reset_finished_at),
        )
        if initial_wait_remaining:
            time.sleep(initial_wait_remaining)
        env.controller.start_recording()
        recording_started = True
        meta["status"] = "agent_running"
        write_local(result_dir / "meta.json", json.dumps(meta, indent=2))

        phase_definitions = phases or [{
            "name": "Task",
            "instruction": instruction,
            "evaluate": lambda current_env: current_env.evaluate(),
        }]
        phase_results = []
        phase_details = []
        total_score = 0.0
        official_evaluator_errors = []
        truncated = False
        budget_exhausted = False
        truncated_phases = []
        done_payload = None
        final_message = ""
        evaluator_payload = None
        use_proxy = bool(
            task_value(task, "proxy", False)
            and getattr(env, "enable_proxy", False)
        )

        for phase_index, phase in enumerate(phase_definitions, start=1):
            phase_name = str(phase.get("name") or f"Phase {phase_index}")
            phase_instruction = str(phase.get("instruction") or instruction)
            if phase_index > 1:
                if hasattr(env, "_step_no"):
                    env._step_no = 0
                if hasattr(env, "action_history"):
                    env.action_history.clear()
                if hasattr(env, "_traj_no"):
                    env._traj_no += 1
                setup = phase.get("setup")
                if not callable(setup):
                    raise ValueError(
                        f"Phase {phase_index} has no callable setup"
                    )
                setup(env.setup_controller, use_proxy=use_proxy)
                env.is_environment_used = True
                env.setup_controller.assert_root_writable()
                pause = float(phase.get("pause_after_setup_seconds", 5))
                if pause > 0:
                    time.sleep(pause)

            env.instruction = phase_instruction
            meta["status"] = "agent_running"
            meta["active_phase"] = phase_index
            meta["active_phase_name"] = phase_name
            write_local(
                result_dir / "meta.json",
                json.dumps(meta, ensure_ascii=False, indent=2),
            )
            phase_request_baseline = (
                count_model_requests(token_log, affinity_task)
                if args.max_model_calls > 0
                else 0
            )
            phase_deadline = (
                float("inf")
                if args.timeout == 0
                else time.monotonic() + args.timeout
            )
            agent_phase = run_agent_phase(
                env=env,
                base_url=base_url,
                result_dir=result_dir,
                prompt_path=prompt_path,
                phase_index=phase_index,
                phase_name=phase_name,
                instruction=phase_instruction,
                current_date=current_date,
                args=args,
                token_log=token_log,
                affinity_task=affinity_task,
                model_request_baseline=phase_request_baseline,
                deadline=phase_deadline,
                multi_phase=bool(phases),
            )
            final_message = agent_phase["final_message"]
            done_payload = agent_phase["done_payload"]
            phase_truncated = bool(agent_phase["truncated"])
            phase_budget_exhausted = bool(
                agent_phase["budget_exhausted"]
            )
            if phase_truncated:
                truncated = True
                budget_exhausted = (
                    budget_exhausted or phase_budget_exhausted
                )
                truncated_phases.append(phase_index)
            final_response, completion_sentinel = split_completion_message(
                final_message
            )
            if completion_sentinel == INFEASIBLE_SENTINEL:
                env.action_history.append("FAIL")
            elif completion_sentinel == COMPLETE_SENTINEL:
                env.action_history.append("DONE")

            before_eval = env.controller.get_screenshot()
            if before_eval is not None:
                (result_dir / (
                    f"phase-{phase_index:02d}-before-eval.png"
                )).write_bytes(before_eval)
                (result_dir / "final-before-eval.png").write_bytes(
                    before_eval
                )

            official_channel = run_official_evaluator(
                lambda: phase["evaluate"](env)
            )
            official_completed = (
                official_channel["status"] == "completed"
            )
            phase_evaluator_payload = (
                official_channel["result"]
                if official_completed
                else {
                    "status": "error",
                    "score": None,
                    "error": official_channel["error"],
                    "traceback": official_channel["traceback"],
                }
            )
            phase_score = (
                official_channel["result"]["score"]
                if official_completed
                else 0.0
            )
            if not official_completed:
                official_evaluator_errors.append({
                    "phase_index": phase_index,
                    "phase_name": phase_name,
                    "error": official_channel["error"],
                    "traceback": official_channel["traceback"],
                })
            evaluator_payload = phase_evaluator_payload
            total_score += phase_score

            after_eval = env.controller.get_screenshot()
            if after_eval is not None:
                (result_dir / (
                    f"phase-{phase_index:02d}-after-eval.png"
                )).write_bytes(after_eval)
                (result_dir / "final-after-eval.png").write_bytes(
                    after_eval
                )

            phase_result = {
                "phase_index": phase_index,
                "phase_name": phase_name,
                "instruction": phase_instruction,
                "score": phase_score,
                "evaluator_status": official_channel["status"],
            }
            phase_detail = {
                **phase_result,
                "evaluator": phase_evaluator_payload,
                "agent": agent_phase,
            }
            phase_results.append(phase_result)
            phase_details.append(phase_detail)
            gate_min_score = phase.get("gate_min_score")
            if not official_completed:
                phase_detail["gate_stop"] = "official_evaluator_error"
            elif (
                gate_min_score is not None
                and phase_score < float(gate_min_score)
            ):
                phase_detail["gate_stop"] = "below_gate_min_score"
            if (
                official_completed
                and phase.get("gate")
                and phase_score <= 0.0
            ):
                phase_detail["gate_stop"] = "zero_score_gate"
            if phases:
                write_local(
                    result_dir / "phase_results.json",
                    json.dumps(
                        phase_results,
                        ensure_ascii=False,
                        indent=2,
                    ),
                )
            write_local(
                result_dir / "phase_details.json",
                json.dumps(
                    phase_details,
                    ensure_ascii=False,
                    indent=2,
                ),
            )
            if phase_detail.get("gate_stop"):
                break

        score = round(max(0.0, min(1.0, total_score)), 4)
        raw_evaluator_status = (
            "error" if official_evaluator_errors else "completed"
        )
        if phases:
            result_payload = {
                "score": score,
                "phase_results": phase_results,
                "phase_details": phase_details,
            }
        else:
            result_payload = dict(evaluator_payload or {"score": score})
            result_payload["score"] = score
            result_payload["phase_details"] = phase_details
        result_payload.update({
            "raw_evaluator_status": raw_evaluator_status,
            "official_evaluator_errors": official_evaluator_errors,
        })
        write_local(result_dir / "final.txt", final_message + "\n")
        write_local(
            result_dir / "raw_evaluator.json",
            json.dumps(
                {
                    "score": score,
                    "status": raw_evaluator_status,
                    "errors": official_evaluator_errors,
                    "phase_results": phase_results,
                    "phase_details": phase_details,
                },
                ensure_ascii=False,
                indent=2,
            ),
        )

        delivery = phase_delivery_status(phases, phase_details)
        result_payload["phase_delivery"] = delivery
        live_judge = None
        if args.judge_model:
            meta["status"] = "judge_running"
            write_local(
                result_dir / "meta.json",
                json.dumps(meta, ensure_ascii=False, indent=2),
            )
            try:
                live_judge = run_live_judge_with_retry(
                    env=env,
                    base_url=base_url,
                    result_dir=result_dir,
                    args=args,
                    run_tag=run_tag,
                    instruction=judge_instruction_text(
                        instruction,
                        (
                            phases[:delivery["delivered_phase_count"]]
                            if phases
                            else phases
                        ),
                    ),
                    final_response=final_response,
                    declared_infeasible=(
                        completion_sentinel == INFEASIBLE_SENTINEL
                    ),
                    base_instructions_file=base_instructions_file,
                    current_date=current_date,
                    timezone_name=run_timezone,
                    user_context=judge_user_context(task, phase_details),
                    raw_evaluator_completed_before_judge=(
                        not official_evaluator_errors
                    ),
                )
            except Exception as judge_exc:
                live_judge = {
                    "judge_mode": "interactive_outcome",
                    "model": args.judge_model,
                    "effort": args.judge_effort,
                    "judge_valid": False,
                    "error": (
                        f"{type(judge_exc).__name__}: {judge_exc}"
                    ),
                    "traceback": traceback.format_exc(),
                    "raw_evaluator_attempted_before_judge": True,
                    "raw_evaluator_completed_before_judge": (
                        not official_evaluator_errors
                    ),
                }
                judge_dir = result_dir / "judge"
                judge_dir.mkdir(parents=True, exist_ok=True)
                (judge_dir / "metadata.json").write_text(
                    json.dumps(
                        live_judge,
                        ensure_ascii=False,
                        indent=2,
                    ),
                    encoding="utf-8",
                )
            live_judge["phase_delivery"] = delivery
            (
                live_judge["answer_score_eligible"],
                live_judge["answer_score_exclusion_reason"],
            ) = judge_answer_score_eligibility(
                truncated=truncated,
                delivery=delivery,
                final_message=final_message,
                done_payload=done_payload,
                official_evaluator_errors=official_evaluator_errors,
            )
            result_payload["live_judge"] = live_judge

        aggregate_trace = (
            (result_dir / "agent.jsonl").read_bytes()
            if (result_dir / "agent.jsonl").is_file()
            else None
        )
        summary = summarize_agent_trace(aggregate_trace, args.agent)
        proxy_usage, frozen_proxy_rows = collect_task_proxy_usage(
            token_log,
            affinity_task,
        )
        if frozen_proxy_rows:
            proxy_evidence_path = (
                result_dir / "task-agent-token-usage.jsonl"
            )
            proxy_evidence_path.write_bytes(frozen_proxy_rows)
            proxy_usage["evidence_file"] = proxy_evidence_path.name
        summary = reconcile_trace_and_proxy_usage(
            summary,
            proxy_usage,
            args.agent,
        )
        summary = normalize_budget_termination(
            summary,
            budget_exhausted=budget_exhausted,
        )

        result_payload.update({
            "task_id": args.task_id,
            "agent": args.agent,
            "model": args.model,
            "effort": args.effort,
            "agent_returncode": (
                done_payload.get("returncode") if done_payload else None
            ),
            "truncated": truncated,
            "truncated_phases": truncated_phases,
            "termination_reason": (
                "max_model_calls"
                if budget_exhausted
                else "timeout"
                if truncated
                else "completed"
            ),
            "final_message": final_message,
            "trace_summary": summary,
            "duration_sec": round(time.monotonic() - start, 1),
        })
        write_local(
            result_dir / "result.json",
            json.dumps(result_payload, ensure_ascii=False, indent=2),
        )
        write_local(result_dir / "result.txt", f"{score}\n")

        meta.update({
            "status": (
                "truncated"
                if truncated
                else "completed_with_evaluator_error"
                if official_evaluator_errors
                else "completed"
            ),
            "score": score,
            "raw_evaluator_status": raw_evaluator_status,
            "official_evaluator_errors": official_evaluator_errors,
            "end_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "duration_sec": result_payload["duration_sec"],
            "agent_returncode": result_payload["agent_returncode"],
            "termination_reason": result_payload["termination_reason"],
            "final_message": final_message,
            "trace_summary": summary,
            "live_judge": live_judge,
        })
        write_local(result_dir / "meta.json", json.dumps(meta, ensure_ascii=False, indent=2))
        print(json.dumps({"result_dir": str(result_dir), **result_payload}, ensure_ascii=False))
        return 0
    except Exception as exc:
        raw_error_text = f"{type(exc).__name__}: {exc}"
        failure_classification = classify_codex_turn_failure(raw_error_text)
        error_text = concise_failure_message(raw_error_text)
        infra_diagnostic = None
        if env is not None:
            infra_diagnostic = collect_guest_infra_diagnostic(
                env.setup_controller.http_server
            )
            infra_diagnostic["primary_failure_classification"] = (
                failure_classification
            )
            infra_diagnostic["causal_to_primary_failure"] = (
                False
                if failure_classification == "tool_argument_too_large"
                else None
            )
            infra_output = str(infra_diagnostic.pop("output", ""))
            if infra_output:
                (result_dir / "fatal-guest-infra.txt").write_text(
                    infra_output,
                    encoding="utf-8",
                )
            (result_dir / "fatal-guest-infra.json").write_text(
                json.dumps(
                    infra_diagnostic,
                    ensure_ascii=False,
                    indent=2,
                ),
                encoding="utf-8",
            )
        meta.update({
            "status": "error",
            "error": error_text,
            "failure_classification": failure_classification,
            "traceback": concise_failure_message(traceback.format_exc()),
            "infra_diagnostic": infra_diagnostic,
            "end_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "duration_sec": round(time.monotonic() - start, 1),
        })
        write_local(result_dir / "meta.json", json.dumps(meta, ensure_ascii=False, indent=2))
        (result_dir / "error.txt").write_text(meta["traceback"], encoding="utf-8")
        print(meta["traceback"], file=sys.stderr)
        return 1
    finally:
        if env is not None:
            if recording_started:
                try:
                    env.controller.end_recording(
                        str(result_dir / "recording.mp4")
                    )
                except Exception:
                    logging.exception("Failed to stop task recording")
            if args.retain_container:
                container = getattr(
                    getattr(env, "provider", None),
                    "container",
                    None,
                )
                retain_error = None
                if container is not None:
                    try:
                        container.stop(timeout=30)
                        retain_status = "retained_stopped"
                    except Exception as exc:
                        retain_status = "retain_stop_failed"
                        retain_error = f"{type(exc).__name__}: {exc}"
                        logging.exception(
                            "Failed to stop retained task container"
                        )
                else:
                    retain_status = "retain_container_missing"
                cleanup_status = {
                    "status": retain_status,
                    "reason": "requested",
                    "container_name": getattr(container, "name", None),
                    "error": retain_error,
                }
            else:
                try:
                    env.close()
                except Exception:
                    logging.exception("Failed to close task environment")
                cleanup_status = getattr(
                    getattr(env, "provider", None),
                    "last_cleanup_status",
                    None,
                )
            if cleanup_status is not None:
                write_local(
                    result_dir / "cleanup.json",
                    json.dumps(
                        cleanup_status,
                        ensure_ascii=False,
                        indent=2,
                    ),
                )
                try:
                    current_meta = json.loads(
                        (result_dir / "meta.json").read_text(
                            encoding="utf-8"
                        )
                    )
                except (OSError, json.JSONDecodeError):
                    current_meta = meta
                current_meta["container_cleanup"] = cleanup_status
                write_local(
                    result_dir / "meta.json",
                    json.dumps(
                        current_meta,
                        ensure_ascii=False,
                        indent=2,
                    ),
                )


if __name__ == "__main__":
    raise SystemExit(main())
