"""Guest filesystem layout and the HTTP control channel into the QEMU guest.

All task-agent and Judge files live at fixed guest paths. Commands run through
the OSWorld guest server's ``/setup/execute`` endpoint, long-running workloads
start as transient systemd user units through ``/setup/launch``, and files are
read back through ``/file``.
"""

from __future__ import annotations

from contextlib import contextmanager
import json
import os
from pathlib import Path
import signal
import sys
import time

import requests


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


GUEST_FILE_ATTEMPTS = 5
GUEST_FILE_INITIAL_BACKOFF_SEC = 1.0


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


def safe_tag(value: str) -> str:
    return "".join(ch if ch.isalnum() or ch in "._-" else "-" for ch in value)


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


def guest_json_file(base_url: str, path: str) -> dict | None:
    raw = fetch_guest_file(base_url, path)
    if not raw:
        return None
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        return None
    return payload if isinstance(payload, dict) else None


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
