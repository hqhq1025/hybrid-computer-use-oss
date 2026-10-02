"""Prepare an explicit current-source experiment without starting a task."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re

from hcu.osworld.batch_config import normalized_tasks, validate_config
from hcu.paths import REPO_ROOT


def read_selection(path: Path) -> list[dict]:
    """Accept runner task lists or upstream domain-to-task-ID lists."""
    value = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(value, dict):
        entries = []
        for domain, task_ids in value.items():
            if not isinstance(task_ids, list):
                raise ValueError("task selection must map domains to task-ID lists")
            entries.extend({"id": task_id, "task_domain": domain} for task_id in task_ids)
    elif isinstance(value, list):
        entries = value
    else:
        raise ValueError("task selection must be a list or domain-to-task-ID object")
    return checked_tasks(entries)


def checked_tasks(entries: list) -> list[dict]:
    tasks = normalized_tasks({"tasks": entries})
    seen = set()
    for task in tasks:
        task_id = task["id"]
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", task_id):
            raise ValueError(f"unsafe task ID: {task_id!r}")
        if task_id in seen:
            raise ValueError(f"duplicate task ID: {task_id}")
        seen.add(task_id)
    return tasks


def instruction_text(value: str, root: Path) -> str:
    value = value.replace("${HYBRID_COMPUTER_USE_ROOT}", str(root))
    value = os.path.expandvars(value)
    if "$" in value:
        raise ValueError("unresolved variable in instruction path")
    path = Path(value).expanduser()
    if not path.is_absolute():
        path = root / path
    return path.read_text(encoding="utf-8")


def prepare(
    config: dict,
    *,
    output: Path,
    run_id: str,
    protocol_id: str,
    protocol_revision: str,
    tasks: list[dict] | None = None,
    excluded: list[dict] | None = None,
    paper_reference: str | None = None,
    timeout_seconds: int = 1800,
    root: Path = REPO_ROOT,
) -> dict:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", run_id):
        raise ValueError("run_id must be a safe nonempty directory name")
    if not protocol_id.strip() or not protocol_revision.strip():
        raise ValueError("protocol ID and revision must be explicit")
    validate_config(config)
    selected = checked_tasks(tasks if tasks is not None else config["tasks"])
    excluded = checked_tasks(excluded) if excluded else []
    selected_ids = {task["id"] for task in selected}
    excluded_ids = {task["id"] for task in excluded}
    if excluded_ids - selected_ids:
        raise ValueError("exclusions must belong to the supplied task selection")
    selected = [task for task in selected if task["id"] not in excluded_ids]
    if not selected:
        raise ValueError("exclusions removed every task")
    if config.get("benchmark_version") == "v1":
        unsupported = set(json.loads(
            (root / "configs/osworld-v1-excluded-tasks.json").read_text()
        ))
        if unsupported.intersection(task["id"] for task in selected):
            raise ValueError(
                "Google Drive tasks are outside scope; explicitly exclude them "
                "with configs/osworld-v1-excluded-tasks.json"
            )
    if timeout_seconds <= 0:
        raise ValueError("a positive wall-clock timeout is required")
    budget = config.get("max_model_calls")
    if isinstance(budget, bool) or not isinstance(budget, int) or budget <= 0:
        raise ValueError("config.max_model_calls must be a positive integer")

    config = dict(config)
    # Preserve resource defaults when replacing a one-task example's membership.
    original_tasks = normalized_tasks(config)
    for key in ("volume_size", "ram_size"):
        values = {task.get(key, config.get(key)) for task in original_tasks}
        if key not in config and len(values) == 1 and None not in values:
            config[key] = values.pop()
    benchmark = str(config.get("benchmark_version") or "v2")
    result_root = f"${{HCU_RESULTS_DIR}}/osworld-{benchmark}/{run_id}"
    config.update(
        run_id=run_id,
        tasks=selected,
        timeout_seconds=timeout_seconds,
        token_log=f"${{HCU_RESULTS_DIR}}/tokens/osworld-{benchmark}/{run_id}.jsonl",
    )
    base = instruction_text(
        str(config.get("base_instructions_file") or root / "instructions/base.md"), root
    )
    profile_path = config.get("instructions_file")
    if profile_path is None:
        profile_path = root / "instructions/hybrid.md"
    profile = (
        instruction_text(str(profile_path), root)
        if profile_path and not config.get("no_instructions")
        else None
    )
    config["base_instructions_file"] = "${HCU_EXPERIMENT_DIR}/instructions/base.md"
    config["instructions_file"] = (
        "${HCU_EXPERIMENT_DIR}/instructions/profile.md" if profile is not None else ""
    )
    validate_config(config)
    lock = json.loads((root / "configs/agent-artifacts.lock.json").read_text())
    manifest = {
        "schema_version": 1,
        "record_kind": "prepared_experiment",
        "created_at_utc": datetime.now(timezone.utc).isoformat(),
        "run_id": run_id,
        "source_version": (root / "VERSION").read_text().strip(),
        "protocol": {"id": protocol_id, "revision": protocol_revision},
        "paper_reference": paper_reference,
        "benchmark_version": benchmark,
        "evaluator": "upstream_official",
        "task_count": len(selected),
        "task_ids": [task["id"] for task in selected],
        "excluded_tasks": excluded,
        "configuration": "config.json",
        "instructions": {
            "base": "instructions/base.md",
            "profile": "instructions/profile.md" if profile is not None else None,
        },
        "agent_versions": {name: row["version"] for name, row in lock["agents"].items()},
        "runtime_versions": {name: row["version"] for name, row in lock["runtimes"].items()},
        "expected_queue_result_root": result_root,
        "evidence_status": "not_run",
    }
    # Validate and read every input before creating the exclusive destination.
    output.mkdir(parents=True, exist_ok=False)
    (output / "instructions").mkdir()
    (output / "instructions/base.md").write_text(base, encoding="utf-8")
    if profile is not None:
        (output / "instructions/profile.md").write_text(profile, encoding="utf-8")
    (output / "config.json").write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
    (output / "experiment.json").write_text(
        json.dumps(manifest, indent=2) + "\n", encoding="utf-8"
    )
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--tasks", type=Path, help="JSON task list or upstream test_all.json")
    parser.add_argument("--exclude", type=Path, help="same format as --tasks")
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--protocol-id", required=True)
    parser.add_argument("--protocol-revision", required=True)
    parser.add_argument("--paper-reference", help="actual table/figure/row, when assigned")
    parser.add_argument("--timeout-seconds", type=int, default=1800)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        manifest = prepare(
            json.loads(args.config.read_text(encoding="utf-8")),
            output=args.output,
            run_id=args.run_id,
            protocol_id=args.protocol_id,
            protocol_revision=args.protocol_revision,
            tasks=read_selection(args.tasks) if args.tasks else None,
            excluded=read_selection(args.exclude) if args.exclude else None,
            paper_reference=args.paper_reference,
            timeout_seconds=args.timeout_seconds,
        )
    except (OSError, ValueError, TypeError, KeyError) as exc:
        parser.exit(2, f"Cannot prepare experiment: {exc}\n")
    print(json.dumps(manifest, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
