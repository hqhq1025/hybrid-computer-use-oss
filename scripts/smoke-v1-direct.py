#!/usr/bin/env python3
"""Exercise the direct-container desktop and the public MCP surface."""

from __future__ import annotations

import argparse
import base64
import json
import subprocess
import time
import urllib.request
import uuid
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def run(*args: str, input_text: str | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        args,
        input=input_text,
        text=True,
        capture_output=True,
        check=True,
    )


def docker_output(*args: str) -> str:
    return run("docker", *args).stdout.strip()


def wait_for_screenshot(container: str, timeout: float) -> tuple[str, bytes]:
    deadline = time.monotonic() + timeout
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        ip = docker_output(
            "inspect",
            "-f",
            "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
            container,
        )
        try:
            with urllib.request.urlopen(
                f"http://{ip}:5000/screenshot",
                timeout=3,
            ) as response:
                return ip, response.read()
        except Exception as exc:
            last_error = exc
            time.sleep(1)
    raise TimeoutError(f"guest screenshot did not become ready: {last_error}")


def mcp_probe(container: str) -> dict:
    script = r'''
import base64
import json
import subprocess

process = subprocess.Popen(
    ["/usr/local/bin/computer-use", "mcp"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    bufsize=1,
)


def call(request_id, method, params):
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
            raise RuntimeError(process.stderr.read())
        response = json.loads(line)
        if response.get("id") == request_id:
            if "error" in response:
                raise RuntimeError(response["error"])
            return response["result"]


call(1, "initialize", {
    "protocolVersion": "2025-06-18",
    "capabilities": {},
    "clientInfo": {"name": "hcu-v1-direct-smoke", "version": "1"},
})
tools = call(2, "tools/list", {}).get("tools", [])
ax = call(3, "tools/call", {
    "name": "AX",
    "arguments": {"mode": "observe", "observation_target": "apps"},
})
gui = call(4, "tools/call", {
    "name": "GUI",
    "arguments": {"actions": [{"type": "screenshot"}]},
})

gui_text = next(
    item["text"] for item in gui.get("content", [])
    if item.get("type") == "text"
)
gui_image = next(
    item["data"] for item in gui.get("content", [])
    if item.get("type") == "image"
)
ax_text = next(
    item["text"] for item in ax.get("content", [])
    if item.get("type") == "text"
)
print(json.dumps({
    "public_tools": [tool["name"] for tool in tools],
    "ax_ok": not ax.get("isError", False),
    "ax_app_lines": len(ax_text.splitlines()),
    "gui_ok": not gui.get("isError", False),
    "gui": json.loads(gui_text),
    "gui_image_bytes": len(base64.b64decode(gui_image)),
}, sort_keys=True))
process.terminate()
process.wait(timeout=5)
'''
    completed = run(
        "docker",
        "exec",
        "-i",
        container,
        "python3",
        "-",
        input_text=script,
    )
    return json.loads(completed.stdout)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="hybrid-computer-use:v0.14-source")
    parser.add_argument("--network", default="osw-net")
    parser.add_argument("--timeout", type=float, default=180)
    parser.add_argument("--keep", action="store_true")
    args = parser.parse_args()

    suffix = uuid.uuid4().hex[:8]
    container = f"hcu-v1-direct-smoke-{suffix}"
    gateway = json.loads(
        docker_output("network", "inspect", args.network)
    )[0]["IPAM"]["Config"][0]["Gateway"]
    try:
        docker_output(
            "run",
            "-d",
            "--name",
            container,
            "--network",
            args.network,
            "--shm-size",
            "2g",
            "--add-host",
            f"host.docker.internal:{gateway}",
            args.image,
            "sleep",
            "infinity",
        )
        ip, screenshot = wait_for_screenshot(container, args.timeout)
        version = docker_output(
            "exec",
            container,
            "/usr/local/bin/computer-use",
            "--version",
        )
        codex_version = docker_output(
            "exec", container, "/usr/bin/codex", "--version"
        )
        claude_version = docker_output(
            "exec", container, "/usr/bin/claude", "--version"
        )
        agent_lock = json.loads(
            (ROOT / "configs/agent-artifacts.lock.json").read_text(
                encoding="utf-8"
            )
        )
        expected_codex = (
            f"codex-cli {agent_lock['agents']['codex']['version']}"
        )
        expected_claude = (
            f"{agent_lock['agents']['claude_code']['version']} (Claude Code)"
        )
        if codex_version != expected_codex:
            raise RuntimeError(
                f"unexpected Codex version: {codex_version!r}"
            )
        if claude_version != expected_claude:
            raise RuntimeError(
                f"unexpected Claude Code version: {claude_version!r}"
            )
        probe = mcp_probe(container)
        expected_screen = {
            "coordinate_space": "screen_image_px",
            "height": 1080,
            "screenshot_returned": True,
            "width": 1920,
        }
        if probe["public_tools"] != ["AX", "GUI"]:
            raise RuntimeError(f"unexpected MCP tools: {probe['public_tools']}")
        if probe["gui"].get("screen") != expected_screen:
            raise RuntimeError(f"unexpected GUI screen: {probe['gui']}")
        report = {
            "container": container,
            "image": args.image,
            "ip": ip,
            "guest_screenshot_bytes": len(screenshot),
            "mcp_version": version,
            "codex_version": codex_version,
            "claude_code_version": claude_version,
            **probe,
        }
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0
    finally:
        if not args.keep:
            subprocess.run(
                ["docker", "rm", "-f", container],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                check=False,
            )


if __name__ == "__main__":
    raise SystemExit(main())
