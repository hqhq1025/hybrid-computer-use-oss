#!/usr/bin/env python3
"""Verifier-only smoke test for the released Computer Use MCP binary."""

from __future__ import annotations

import base64
import json
import subprocess
from io import BytesIO

from PIL import Image


BINARY = "/home/user/.local/bin/computer-use"


def request(process: subprocess.Popen[str], request_id: int, method: str, params: dict) -> dict:
    assert process.stdin is not None
    assert process.stdout is not None
    process.stdin.write(
        json.dumps(
            {
                "jsonrpc": "2.0",
                "id": request_id,
                "method": method,
                "params": params,
            }
        )
        + "\n"
    )
    process.stdin.flush()
    while True:
        line = process.stdout.readline()
        if not line:
            stderr = process.stderr.read() if process.stderr is not None else ""
            raise RuntimeError(f"MCP exited while waiting for {method}: {stderr}")
        response = json.loads(line)
        if response.get("id") == request_id:
            if "error" in response:
                raise RuntimeError(response["error"])
            return response.get("result") or {}


def text_content(result: dict) -> str:
    return "".join(
        item.get("text", "")
        for item in result.get("content", [])
        if item.get("type") == "text"
    )


def main() -> int:
    process = subprocess.Popen(
        [BINARY, "mcp"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )
    try:
        request(
            process,
            1,
            "initialize",
            {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "hcu-qemu-guest-smoke", "version": "1"},
            },
        )
        tools = request(process, 2, "tools/list", {}).get("tools", [])
        tool_names = [tool["name"] for tool in tools]
        if tool_names != ["AX", "GUI"]:
            raise AssertionError(f"unexpected public tools: {tool_names}")

        ax = request(
            process,
            3,
            "tools/call",
            {
                "name": "AX",
                "arguments": {
                    "mode": "observe",
                    "observation_target": "apps",
                },
            },
        )
        if ax.get("isError") or not text_content(ax).strip():
            raise AssertionError(f"AX observation failed: {text_content(ax)}")

        gui = request(
            process,
            4,
            "tools/call",
            {
                "name": "GUI",
                "arguments": {"actions": [{"type": "screenshot"}]},
            },
        )
        if gui.get("isError"):
            raise AssertionError(f"GUI screenshot failed: {text_content(gui)}")
        images = [
            item for item in gui.get("content", []) if item.get("type") == "image"
        ]
        if len(images) != 1:
            raise AssertionError(f"GUI returned {len(images)} images")
        screenshot = base64.b64decode(images[0]["data"], validate=True)
        image = Image.open(BytesIO(screenshot)).convert("RGB")
        if image.size != (1920, 1080):
            raise AssertionError(f"unexpected GUI screenshot size: {image.size}")
        if not any(high > 8 for _low, high in image.getextrema()):
            raise AssertionError("GUI screenshot is black")

        print(
            json.dumps(
                {
                    "status": "PASS",
                    "tools": tool_names,
                    "ax_text_bytes": len(text_content(ax).encode()),
                    "gui_screenshot_bytes": len(screenshot),
                    "gui_screenshot_size": list(image.size),
                },
                sort_keys=True,
            )
        )
        return 0
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
