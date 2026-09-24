#!/usr/bin/env python3
"""Run a small, bounded OSWorld V1 or V2 QEMU batch."""

from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed
import json
import os
from pathlib import Path
import subprocess
import sys


REPO_ROOT = Path(__file__).resolve().parents[2]
RUNNER = REPO_ROOT / "integrations/osworld-v2/hybrid/run_hybrid.py"


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


def normalized_tasks(config: dict) -> list[dict]:
    tasks = []
    for entry in config.get("tasks") or []:
        task = {"id": entry} if isinstance(entry, str) else dict(entry)
        task_id = str(task.get("id") or "").strip()
        if not task_id:
            raise ValueError("every task must have a non-empty id")
        task["id"] = task_id
        tasks.append(task)
    if not tasks:
        raise ValueError("config.tasks must not be empty")
    return tasks


def append_option(command: list[str], config: dict, key: str, flag: str) -> None:
    value = config.get(key)
    if value not in (None, ""):
        command.extend((flag, os.path.expandvars(str(value))))


def command_for(config: dict, task: dict, output_root: Path) -> list[str]:
    command = [
        sys.executable,
        str(RUNNER),
        "--task-id",
        task["id"],
        "--benchmark-version",
        str(config.get("benchmark_version") or "v2"),
        "--agent",
        str(config.get("agent") or "codex"),
        "--model",
        str(config.get("model") or ""),
        "--effort",
        str(config.get("effort") or "xhigh"),
        "--volume-size",
        str(task.get("volume_size", config.get("volume_size", 40))),
        "--ram-size",
        str(task.get("ram_size", config.get("ram_size", "4G"))),
        "--max-model-calls",
        str(config.get("max_model_calls", 200)),
        "--timeout",
        str(config.get("timeout_seconds", 0)),
        "--result-dir",
        str(output_root / task["id"]),
    ]
    for key, flag in (
        ("gateway_base", "--gateway-base"),
        ("claude_gateway_base", "--claude-gateway-base"),
        ("guest_image", "--guest-image"),
        ("artifact_dir", "--artifact-dir"),
        ("codex_binary", "--codex-binary"),
        ("claude_code_binary", "--claude-code-binary"),
        ("mcp_binary", "--mcp-binary"),
        ("skill_file", "--skill-file"),
        ("base_instructions_file", "--base-instructions-file"),
        ("client_password", "--client-password"),
        ("judge_model", "--judge-model"),
        ("judge_effort", "--judge-effort"),
        ("judge_timeout", "--judge-timeout"),
        ("judge_tool_limit", "--judge-tool-limit"),
        ("judge_retry_tool_limit", "--judge-retry-tool-limit"),
        ("token_log", "--token-log"),
    ):
        append_option(command, config, key, flag)
    task_domain = task.get("task_domain", config.get("task_domain"))
    if task_domain:
        command.extend(("--task-domain", str(task_domain)))
    if config.get("retain_containers"):
        command.append("--retain-container")
    if config.get("enable_vnc"):
        command.append("--enable-vnc")
    if config.get("enable_recording"):
        command.append("--enable-recording")
    return command


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
            command_for(config, task, output_root),
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
    tasks = normalized_tasks(config)
    run_id = str(config.get("run_id") or config_path.stem)
    results_root = Path(
        os.environ.get("HCU_RESULTS_DIR", REPO_ROOT / "runs")
    ).expanduser().resolve()
    benchmark_version = str(config.get("benchmark_version") or "v2")
    if benchmark_version not in {"v1", "v2"}:
        raise ValueError("config.benchmark_version must be v1 or v2")
    output_root = results_root / f"osworld-{benchmark_version}" / run_id
    log_root = output_root / "logs"
    if args.dry_run:
        for task in tasks:
            print(json.dumps(command_for(config, task, output_root)))
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
