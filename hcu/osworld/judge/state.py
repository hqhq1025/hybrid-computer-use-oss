"""Capture and compare the guest state before and after the Judge.

The Judge must not change the delivered state. Screenshots, accessibility
trees and a file manifest of the user's home directory are captured before
and after the Judge session and compared by digest.
"""

from __future__ import annotations

from collections.abc import Callable
import hashlib
import json
from pathlib import Path
import time
from typing import TYPE_CHECKING, Any, TypeVar

import requests

from hcu.osworld.guest import request_execute, total_wall_timeout

if TYPE_CHECKING:
    from desktop_env.desktop_env import DesktopEnv

T = TypeVar("T")


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


JUDGE_SCREENSHOT_CAPTURE_TIMEOUT_SEC = 60.0
JUDGE_A11Y_CAPTURE_TIMEOUT_SEC = 60.0
JUDGE_MANIFEST_CAPTURE_ATTEMPTS = 3
JUDGE_MANIFEST_CAPTURE_BACKOFF_SEC = 0.5


def bounded_control_retry(
    operation: Callable[[], T],
    *,
    attempts: int = 3,
    initial_backoff_seconds: float = 0.5,
    retryable: Callable[[Exception], bool] | None = None,
    sleep: Callable[[float], Any] = time.sleep,
) -> tuple[T, dict[str, Any]]:
    if attempts < 1:
        raise ValueError("attempts must be positive")
    if initial_backoff_seconds < 0:
        raise ValueError("initial_backoff_seconds must be non-negative")
    should_retry = retryable or (lambda _exc: True)
    failures = []
    for attempt in range(1, attempts + 1):
        try:
            return operation(), {
                "attempts": attempt,
                "retry_assisted": attempt > 1,
                "failures": failures,
            }
        except Exception as exc:
            failure = f"{type(exc).__name__}: {exc}"
            failures.append(failure)
            if attempt >= attempts or not should_retry(exc):
                exc.retry_evidence = {
                    "attempts": attempt,
                    "retry_assisted": attempt > 1,
                    "failures": list(failures),
                }
                raise
            delay = initial_backoff_seconds * (2 ** (attempt - 1))
            if delay:
                sleep(delay)
    raise AssertionError("unreachable")


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
