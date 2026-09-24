"""Run a small, bounded OSWorld V1 or V2 QEMU batch."""

from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed
import json
import os
from pathlib import Path
import subprocess

from hcu.osworld.batch_config import (
    normalized_tasks,
    runner_command,
    validate_config,
)
from hcu.paths import REPO_ROOT, results_root


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--config",
        type=Path,
        default=REPO_ROOT / "configs/osworld-v2-queue.example.json",
    )
    parser.add_argument("--concurrency", type=int, default=1)
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Validate the config and print commands without launching tasks.",
    )
    return parser.parse_args()


def run_task(
    config: dict,
    task: dict,
    output_root: Path,
    log_root: Path,
) -> tuple[str, int]:
    task_id = task["id"]
    log_path = log_root / f"{task_id}.log"
    with log_path.open("w", encoding="utf-8") as log:
        completed = subprocess.run(
            runner_command(config, task, output_root / task["id"]),
            cwd=REPO_ROOT,
            env=os.environ.copy(),
            stdout=log,
            stderr=subprocess.STDOUT,
            check=False,
        )
    return task_id, completed.returncode


def main() -> int:
    args = parse_args()
    if args.concurrency < 1:
        raise SystemExit("--concurrency must be at least 1")
    config_path = args.config.expanduser().resolve()
    config = json.loads(config_path.read_text(encoding="utf-8"))
    validate_config(config)
    tasks = normalized_tasks(config)
    run_id = str(config.get("run_id") or config_path.stem)
    benchmark_version = str(config.get("benchmark_version") or "v2")
    if benchmark_version not in {"v1", "v2"}:
        raise ValueError("config.benchmark_version must be v1 or v2")
    output_root = results_root() / f"osworld-{benchmark_version}" / run_id
    log_root = output_root / "logs"
    if args.dry_run:
        for task in tasks:
            print(json.dumps(runner_command(config, task, output_root / task["id"])))
        return 0
    log_root.mkdir(parents=True, exist_ok=True)

    failures = []
    with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = {
            pool.submit(run_task, config, task, output_root, log_root): task["id"]
            for task in tasks
        }
        for future in as_completed(futures):
            task_id, returncode = future.result()
            status = "PASS" if returncode == 0 else f"ERR({returncode})"
            print(f"{task_id}: {status}", flush=True)
            if returncode != 0:
                failures.append(task_id)

    summary = {
        "run_id": run_id,
        "task_count": len(tasks),
        "failed_task_ids": sorted(failures),
        "result_root": str(output_root),
    }
    (output_root / "batch-summary.json").write_text(
        json.dumps(summary, indent=2) + "\n",
        encoding="utf-8",
    )
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
