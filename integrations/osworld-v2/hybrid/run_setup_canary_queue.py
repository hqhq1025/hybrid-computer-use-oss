#!/usr/bin/env python3
from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time


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

from hybrid.run_setup_canary import sha256_file  # noqa: E402
from hybrid.runtime_preflight import (  # noqa: E402
    validate_dedicated_docker_runtime,
    validate_official_asset_runtime,
)
from desktop_env.providers.volume import (  # noqa: E402
    validate_guest_image_capacity,
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--result-root", type=Path, required=True)
    parser.add_argument("--guest-image", type=Path, required=True)
    parser.add_argument("--expected-guest-image-sha256", required=True)
    parser.add_argument("--concurrency", type=int, default=20)
    parser.add_argument("--task-timeout", type=int, default=1200)
    parser.add_argument(
        "--task-profile",
        type=Path,
        help=(
            "Queue JSON whose per-task volume_size values must be used by "
            "the setup canary."
        ),
    )
    parser.add_argument(
        "--retain-containers",
        action="store_true",
        help="Stop but do not remove every canary container after setup.",
    )
    parser.add_argument("--tasks", nargs="*")
    return parser.parse_args()


def task_ids() -> list[str]:
    return sorted(
        path.stem.removeprefix("task_")
        for path in (ROOT / "evaluation_examples/task_class").glob(
            "task_[0-9][0-9][0-9].py"
        )
    )


def task_set_sha256(ids: list[str]) -> str:
    digest = hashlib.sha256()
    task_root = ROOT / "evaluation_examples/task_class"
    for task_id in ids:
        path = task_root / f"task_{task_id}.py"
        digest.update(task_id.encode())
        digest.update(b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def atomic_json(path: Path, value: object) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    temporary.replace(path)


def classify_report(report: dict | None, returncode: int) -> str:
    if returncode != 0 or not report or report.get("status") != "passed":
        return "failed"
    attempts = report.get("setup_attempts") or []
    failed_attempts = [
        attempt for attempt in attempts if attempt.get("status") == "failed"
    ]
    return "passed_after_retry" if failed_attempts else "passed_clean"


def load_task_resources(
    task_profile: Path | None,
    selected: list[str],
) -> tuple[dict[str, int], dict[str, str], str | None]:
    if task_profile is None:
        return {}, {}, None
    profile = task_profile.resolve()
    payload = json.loads(profile.read_text(encoding="utf-8"))
    volumes = {
        str(task["id"]): int(task["volume_size"])
        for task in payload.get("tasks", [])
        if task.get("id") is not None
        and task.get("volume_size") is not None
    }
    ram_sizes = {
        str(task["id"]): str(task["ram_size"])
        for task in payload.get("tasks", [])
        if task.get("id") is not None
        and task.get("ram_size") is not None
    }
    missing = sorted(set(selected) - set(volumes))
    if missing:
        raise ValueError(
            "Task profile is missing volume_size for: "
            + ", ".join(missing)
        )
    return volumes, ram_sizes, sha256_file(profile)


def load_task_volumes(
    task_profile: Path | None,
    selected: list[str],
) -> tuple[dict[str, int], str | None]:
    """Compatibility wrapper for callers that only consume disk sizing."""
    volumes, _ram_sizes, digest = load_task_resources(
        task_profile,
        selected,
    )
    return volumes, digest


def main() -> int:
    args = parse_args()
    if args.concurrency <= 0:
        raise ValueError("--concurrency must be positive")
    if args.task_timeout <= 0:
        raise ValueError("--task-timeout must be positive")

    docker_runtime = validate_dedicated_docker_runtime()
    asset_runtime = validate_official_asset_runtime()
    image = args.guest_image.resolve()
    actual_image_sha256 = sha256_file(image)
    if actual_image_sha256 != args.expected_guest_image_sha256.lower():
        raise ValueError(
            "Guest image SHA-256 mismatch: "
            f"expected {args.expected_guest_image_sha256}, "
            f"got {actual_image_sha256}"
        )

    selected = args.tasks or task_ids()
    unknown = sorted(set(selected) - set(task_ids()))
    if unknown:
        raise ValueError("Unknown task IDs: " + ", ".join(unknown))
    task_volumes, task_ram_sizes, task_profile_sha256 = load_task_resources(
        args.task_profile,
        selected,
    )
    required_volume_size = max(
        (task_volumes.get(task_id, 40) for task_id in selected),
        default=40,
    )
    image_virtual_size_gib = validate_guest_image_capacity(
        image,
        required_volume_size,
    )

    result_root = args.result_root.resolve()
    result_root.mkdir(parents=True, exist_ok=False)
    logs = result_root / "logs"
    tasks_root = result_root / "tasks"
    logs.mkdir()
    tasks_root.mkdir()

    manifest = {
        "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "task_ids": selected,
        "task_count": len(selected),
        "task_set_sha256": task_set_sha256(selected),
        "guest_image": str(image),
        "guest_image_sha256": actual_image_sha256,
        "guest_image_virtual_size_gib": image_virtual_size_gib,
        "required_volume_size_gib": required_volume_size,
        "asset_runtime": asset_runtime,
        "docker_runtime": docker_runtime,
        "concurrency": args.concurrency,
        "task_timeout_seconds": args.task_timeout,
        "task_profile": (
            str(args.task_profile.resolve()) if args.task_profile else None
        ),
        "task_profile_sha256": task_profile_sha256,
        "task_volume_sizes": {
            task_id: task_volumes.get(task_id, 40)
            for task_id in selected
        },
        "task_ram_sizes": {
            task_id: task_ram_sizes.get(task_id, "4G")
            for task_id in selected
        },
        "retain_containers": args.retain_containers,
    }
    atomic_json(result_root / "manifest.json", manifest)

    statuses = {
        task_id: {"status": "pending", "returncode": None}
        for task_id in selected
    }
    status_lock = threading.Lock()
    atomic_json(result_root / "status.json", statuses)

    def run_task(task_id: str) -> tuple[str, dict]:
        with status_lock:
            statuses[task_id] = {"status": "running", "returncode": None}
            atomic_json(result_root / "status.json", statuses)
        task_result = tasks_root / task_id
        command = [
            sys.executable,
            str(INTEGRATION_ROOT / "hybrid/run_setup_canary.py"),
            "--task-id",
            task_id,
            "--guest-image",
            str(image),
            "--expected-guest-image-sha256",
            actual_image_sha256,
            "--precomputed-guest-image-sha256",
            actual_image_sha256,
            "--result-dir",
            str(task_result),
            "--volume-size",
            str(task_volumes.get(task_id, 40)),
            "--ram-size",
            task_ram_sizes.get(task_id, "4G"),
        ]
        if args.retain_containers:
            command.append("--retain-container")
        started = time.monotonic()
        log_path = logs / f"{task_id}.log"
        with log_path.open("wb") as log:
            process = subprocess.Popen(
                command,
                cwd=ROOT,
                env=os.environ.copy(),
                stdout=log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
            try:
                returncode = process.wait(timeout=args.task_timeout)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                returncode = 124
        report_path = task_result / "report.json"
        report = None
        if report_path.is_file():
            try:
                report = json.loads(report_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError):
                pass
        result = {
            "status": classify_report(report, returncode),
            "returncode": returncode,
            "duration_sec": round(time.monotonic() - started, 1),
            "report": report,
            "log": str(log_path),
        }
        return task_id, result

    with concurrent.futures.ThreadPoolExecutor(
        max_workers=args.concurrency
    ) as pool:
        futures = [pool.submit(run_task, task_id) for task_id in selected]
        for future in concurrent.futures.as_completed(futures):
            task_id, result = future.result()
            with status_lock:
                statuses[task_id] = result
                atomic_json(result_root / "status.json", statuses)

    counts = {}
    for result in statuses.values():
        status = result["status"]
        counts[status] = counts.get(status, 0) + 1
    summary = {
        "finished_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "counts": counts,
        "all_clean": counts.get("passed_clean", 0) == len(selected),
    }
    atomic_json(result_root / "summary.json", summary)
    print(json.dumps(summary, ensure_ascii=False))
    return 0 if not counts.get("failed") else 1


if __name__ == "__main__":
    raise SystemExit(main())
