#!/usr/bin/env python3
"""Boot an OSWorld QEMU guest and verify workstation dependencies plus MCP."""

from __future__ import annotations

import argparse
import io
import json
import os
from pathlib import Path
import sys
import time

import requests
from PIL import Image


ROOT = Path(__file__).resolve().parents[1]


def screenshot_is_nonblack(data: bytes | None) -> bool:
    if not data:
        return False
    image = Image.open(io.BytesIO(data)).convert("RGB").resize((160, 90))
    visible = sum(
        1 for red, green, blue in image.getdata() if max(red, green, blue) > 16
    )
    return visible / (160 * 90) >= 0.01


def execute(base_url: str, command: list[str], timeout: int = 300) -> dict:
    response = requests.post(
        base_url + "/setup/execute",
        json={"command": command, "shell": False, "timeout": timeout},
        timeout=timeout + 20,
    )
    response.raise_for_status()
    payload = response.json()
    if payload.get("status") != "success" or payload.get("returncode") not in {
        0,
        None,
    }:
        raise RuntimeError(
            "guest command failed: "
            f"command={command!r} returncode={payload.get('returncode')} "
            f"stdout={payload.get('output', '')!r} "
            f"stderr={payload.get('error', '')!r}"
        )
    return payload


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--label", required=True)
    parser.add_argument("--image", type=Path, required=True)
    parser.add_argument("--volume-size", type=int, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument(
        "--osworld-root",
        type=Path,
        default=Path(
            os.environ.get("OSWORLD_V2_ROOT", ROOT / "external" / "OSWorld-V2")
        ),
    )
    parser.add_argument(
        "--mcp-binary",
        type=Path,
        default=ROOT / "artifacts/computer-use",
    )
    parser.add_argument(
        "--dependency-smoke",
        type=Path,
        default=(
            ROOT
            / "guest-images/workstation-deps-v014-beta1/profiles/v2-full/verify-guest.sh"
        ),
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    image = args.image.resolve()
    mcp_binary = args.mcp_binary.resolve()
    output_dir = args.output_dir.resolve()
    dependency_smoke = args.dependency_smoke.resolve()
    output_dir.mkdir(parents=True, exist_ok=False)

    if not image.is_file():
        raise FileNotFoundError(f"guest image not found: {image}")

    osworld_root = args.osworld_root.resolve()
    sys.path.insert(0, str(osworld_root))
    os.chdir(osworld_root)
    from desktop_env.desktop_env import DesktopEnv

    mcp_smoke = (
        ROOT
        / "guest-images/workstation-deps-v014-beta1/guest-mcp-smoke.py"
    )
    summary = {
        "label": args.label,
        "image": str(image),
        "volume_size_gib": args.volume_size,
        "mcp_binary": str(mcp_binary),
        "status": "ERROR",
    }
    env = None
    started = time.monotonic()
    try:
        env = DesktopEnv(
            provider_name="docker",
            path_to_vm=str(image),
            action_space="pyautogui",
            cache_dir=str(output_dir / "desktop-env-cache"),
            headless=True,
            require_a11y_tree=False,
            os_type="Ubuntu",
            client_password="osworld-public-evaluation",
            volume_size=args.volume_size,
            force_disable_vnc=True,
            force_disable_recording=True,
        )
        summary["container_name"] = env.provider.container.name
        env.reset(task_config=None)
        env.setup_controller.assert_root_writable()

        desktop_screenshot = None
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            candidate = env.controller.get_screenshot()
            if screenshot_is_nonblack(candidate):
                desktop_screenshot = candidate
                break
            time.sleep(2)
        if desktop_screenshot is None:
            raise AssertionError("desktop remained blank for 90 seconds")
        screenshot_path = output_dir / "desktop.png"
        screenshot_path.write_bytes(desktop_screenshot)
        summary["desktop_screenshot"] = {
            "path": str(screenshot_path),
            "bytes": len(desktop_screenshot),
        }

        env.setup_controller._upload_file_setup(
            [
                {
                    "local_path": str(dependency_smoke),
                    "path": "/tmp/osw-verify-guest.sh",
                },
                {
                    "local_path": str(mcp_binary),
                    "path": "/home/user/.local/bin/computer-use",
                },
                {
                    "local_path": str(mcp_smoke),
                    "path": "/tmp/osw-guest-mcp-smoke.py",
                },
            ]
        )
        execute(
            env.setup_controller.http_server,
            [
                "chmod",
                "0755",
                "/tmp/osw-verify-guest.sh",
                "/tmp/osw-guest-mcp-smoke.py",
                "/home/user/.local/bin/computer-use",
            ],
            timeout=30,
        )
        dependency_result = execute(
            env.setup_controller.http_server,
            ["bash", "-lc", "/tmp/osw-verify-guest.sh"],
            timeout=600,
        )
        mcp_result = execute(
            env.setup_controller.http_server,
            [
                "bash",
                "-lc",
                (
                    "export DISPLAY=:0 XAUTHORITY=/home/user/.Xauthority; "
                    "python3 /tmp/osw-guest-mcp-smoke.py"
                ),
            ],
            timeout=300,
        )
        dependency_output = str(dependency_result.get("output", "")).strip()
        mcp_output = str(mcp_result.get("output", "")).strip()
        summary["dependency_output"] = dependency_output
        summary["mcp"] = json.loads(mcp_output.splitlines()[-1])
        summary["status"] = "PASS"
        return 0
    except Exception as exc:
        summary["error"] = f"{type(exc).__name__}: {exc}"
        raise
    finally:
        summary["duration_seconds"] = round(time.monotonic() - started, 1)
        if env is not None:
            try:
                env.close()
                summary["cleanup"] = "PASS"
            except Exception as exc:
                summary["cleanup"] = f"ERROR: {type(exc).__name__}: {exc}"
        (output_dir / "report.json").write_text(
            json.dumps(summary, indent=2, sort_keys=True),
            encoding="utf-8",
        )
        print(json.dumps(summary, indent=2, sort_keys=True))


if __name__ == "__main__":
    raise SystemExit(main())
