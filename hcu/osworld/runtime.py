"""Install the task-agent runtime into the guest and verify the guest.

The runtime (agent binary, MCP binary, launcher, configuration and prompts)
is uploaded before task setup so that setup cannot observe a missing agent.
The guest clock is aligned to the run-bound task date before setup as well.
"""

from __future__ import annotations

import datetime
import json
import os
import shlex

from hcu.osworld.guest import (
    GUEST_CLAUDE_CONFIG_DIR,
    GUEST_CODEX,
    GUEST_CODEX_HOME,
    GUEST_LAUNCHER,
    GUEST_MCP,
    GUEST_MCP_RUNTIME_ROOT,
    request_execute,
)


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


def runtime_bootstrap_command(client_password: str) -> list[str]:
    runtime_dirs = (
        GUEST_CODEX_HOME,
        GUEST_CLAUDE_CONFIG_DIR,
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
        f"&& test -w {shlex.quote(GUEST_CLAUDE_CONFIG_DIR)}",
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
