"""Offline, fixed-denominator summaries of one owned OSWorld queue run.

No Docker, benchmark checkout, gateway or model is needed. Unknown measurements
remain null. RAW is evidence, not the policy-adjusted final score.
"""

from __future__ import annotations

import argparse
from collections import Counter
import csv
import io
import json
import math
import os
from pathlib import Path

from hcu.osworld.batch_config import normalized_tasks, resolve_config, validate_config
from hcu.paths import REPO_ROOT, results_root


STATUSES = (
    "valid", "budget", "timeout", "truncated", "evaluator_error",
    "execution_error", "missing", "invalid",
)
PATH_KEYS = (
    "guest_image", "artifact_dir", "agent_binary", "mcp_binary",
    "instructions_file", "base_instructions_file", "token_log",
)
METRICS = ("input_tokens", "output_tokens", "total_tokens", "elapsed_seconds")


def read_object(path: Path) -> dict:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"{path}: expected a JSON object")
    return value


def path_id(value: object, label: str) -> str:
    if (
        not isinstance(value, str) or not value or value != value.strip()
        or value in {".", ".."} or "/" in value or "\\" in value
    ):
        raise ValueError(f"{label} must be an explicit single path component")
    return value


def selection(config: dict) -> list[dict]:
    validate_config(config)
    path_id(config.get("run_id"), "run_id")
    tasks = normalized_tasks(config)
    ids = [path_id(task["id"], "task id") for task in tasks]
    if len(ids) != len(set(ids)):
        raise ValueError("Duplicate selected task IDs")
    return tasks


def owned_config(config: dict) -> dict:
    """Match the queue's saved configuration without executing an env script."""
    config = resolve_config(config, os.environ)
    config["tasks"] = normalized_tasks(config)
    config.pop("client_password", None)
    for key in PATH_KEYS:
        if config.get(key):
            path = Path(config[key]).expanduser()
            config[key] = str(
                (path if path.is_absolute() else REPO_ROOT / path).resolve()
            )
    if config.get("environment_file"):
        config["environment_file"] = str(
            Path(config["environment_file"]).expanduser().resolve()
        )
    return config


def number(value: object, *, score: bool = False) -> float | int | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    if not math.isfinite(value) or value < 0 or (score and value > 1):
        return None
    return value


def token_metrics(root: Path, meta: dict, proxy: dict) -> tuple[dict, list[str]]:
    tokens = {"input_tokens": None, "output_tokens": None, "total_tokens": None}
    path = root / "task-agent-token-usage.jsonl"
    if path.is_symlink():
        raise ValueError(f"Token evidence must not alias another attempt: {path}")
    if not path.exists():
        return tokens, []
    issues = []
    totals = {"input_tokens": 0, "output_tokens": 0}
    accounted = 0
    seen_rows = 0
    try:
        for index, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                row = json.loads(line)
                if not isinstance(row, dict):
                    raise ValueError("expected an object")
            except ValueError as exc:
                issues.append(f"{path.name}:{index}: {exc}")
                continue
            if not meta.get("affinity_task"):
                issues.append("Missing metadata affinity_task for token evidence")
                continue
            if row.get("task") != meta["affinity_task"]:
                raise ValueError(f"{path}:{index}: conflicting token task affinity")
            seen_rows += 1
            # Match collect_task_proxy_usage: an admitted failure can still have
            # usage; only requests rejected before admission are free of it.
            if (
                row.get("budget_rejected") is True or row.get("budget_inflight")
                or (row.get("generation_retry_exhausted") and not row.get("generation_admitted"))
            ):
                continue
            if row.get("generation_request") is False and all(
                row.get(key) is None
                for key in ("input_tokens", "output_tokens", "total_tokens", "reasoning_tokens")
            ):
                continue
            values = {key: number(row.get(key)) for key in totals}
            if not all(isinstance(value, int) for value in values.values()):
                issues.append(f"{path.name}:{index}: incomplete token accounting")
                continue
            accounted += 1
            for key, value in values.items():
                totals[key] += value
    except OSError as exc:
        issues.append(str(exc))
    # Trace/proxy summaries initialize absent usage to zero. Only frozen request
    # rows with explicit counts establish known totals, including a genuine zero.
    if (
        not issues and seen_rows > 0 and proxy.get("status") == "completed"
        and proxy.get("unaccounted_requests") == 0
        and proxy.get("accounted_responses") == accounted
    ):
        for key, value in totals.items():
            if proxy.get(key) != value:
                raise ValueError(f"{path}: conflicting proxy {key}")
        tokens.update(totals)
        tokens["total_tokens"] = sum(totals.values())
    return tokens, issues


def read_task(root: Path, task: dict, config: dict, failure: str | None = None) -> dict:
    """Read one canonical attempt; never search for or select a better attempt."""
    task_id = task["id"]
    problems = []
    payloads = {}
    for filename in ("result.json", "meta.json", "raw_evaluator.json"):
        path = root / filename
        if path.is_symlink():
            raise ValueError(f"Evidence must not alias another attempt: {path}")
        try:
            payloads[filename] = read_object(path) if path.exists() else {}
        except (OSError, ValueError) as exc:
            problems.append(f"{filename}: {exc}")
            payloads[filename] = {}
    result, meta, evaluator = (
        payloads[name] for name in ("result.json", "meta.json", "raw_evaluator.json")
    )
    for filename in ("result.json", "meta.json"):
        if payloads[filename] and "task_id" not in payloads[filename]:
            problems.append(f"{filename}: missing task_id")
    if result and number(result.get("score"), score=True) is None:
        problems.append("result.json: missing or invalid score")
    if "truncated" in result and not isinstance(result["truncated"], bool):
        problems.append("result.json: truncated must be boolean")
    for filename, payload in payloads.items():
        expected = {
            "task_id": task_id, "run_id": config["run_id"],
            "benchmark_version": config.get("benchmark_version") or "v2",
            "agent": config.get("agent") or "codex",
        }
        expected.update({
            key: config[key] for key in ("model", "effort") if config.get(key)
        })
        expected.update({key: config[key] for key in PATH_KEYS if config.get(key)})
        if config.get("no_instructions") or config.get("instructions_file") == "":
            expected["instructions_file"] = None
        if config.get("channels"):
            expected["mcp_channels"] = config["channels"]
        domain = task.get("task_domain", config.get("task_domain"))
        if domain:
            expected["task_domain"] = domain
        for key, value in expected.items():
            if key in payload and payload[key] != value:
                raise ValueError(f"{root / filename}: conflicting {key}")

    scores = {}
    for filename, payload in payloads.items():
        if payload.get("score") is not None:
            score = number(payload["score"], score=True)
            if score is None:
                problems.append(f"{filename}: invalid score {payload['score']!r}")
            else:
                scores[filename] = score
    score_path = root / "result.txt"
    if score_path.is_symlink():
        raise ValueError(f"Evidence must not alias another attempt: {score_path}")
    if score_path.exists():
        try:
            value = score_path.read_text(encoding="utf-8").strip()
            score = number(float(value), score=True)
            if score is None:
                raise ValueError(f"invalid score {value!r}")
            scores["result.txt"] = score
        except (OSError, ValueError) as exc:
            problems.append(f"result.txt: {exc}")
    if len(set(scores.values())) > 1:
        raise ValueError(f"{root}: conflicting scores: {scores}")
    raw = next(iter(scores.values()), None)
    if not result and raw is not None:
        problems.append("Missing result.json")
    if not meta and (result or raw is not None):
        problems.append("Missing meta.json")

    trace = result.get("trace_summary", meta.get("trace_summary")) or {}
    if not isinstance(trace, dict):
        problems.append("Invalid trace_summary")
        trace = {}
    reasons = {
        payload.get("termination_reason") for payload in (result, meta)
        if isinstance(payload.get("termination_reason"), str)
    }
    classifications = {
        payload.get("failure_classification") for payload in (meta, trace)
        if isinstance(payload.get("failure_classification"), str)
    }
    budget = bool(
        reasons & {"max_model_calls", "model_call_budget_exhausted"}
        or "model_call_budget_exhausted" in classifications
        or result.get("budget_exhausted") is True
    )
    timeout = "timeout" in reasons or "timeout" in classifications
    truncated = (
        budget or timeout or result.get("truncated") is True
        or meta.get("status") == "truncated"
        or bool(result.get("truncated_phases"))
    )
    errors = []
    for filename, payload in payloads.items():
        for key in ("official_evaluator_errors", "error", "errors"):
            if payload.get(key):
                errors.append({"source": filename, "field": key, "value": payload[key]})
    if failure:
        errors.append({"source": "QUEUE_STATE.json", "field": "failures", "value": failure})
    if trace.get("agent_error"):
        errors.append({
            "source": "trace_summary", "field": "agent_error", "value": trace["agent_error"],
        })
    evaluator_error = (
        any(payload.get("raw_evaluator_status") == "error" for payload in (result, meta))
        or evaluator.get("status") == "error"
        or meta.get("status") == "completed_with_evaluator_error"
        or bool(result.get("official_evaluator_errors") or meta.get("official_evaluator_errors"))
        or bool(evaluator.get("errors"))
    )
    if result and (
        result.get("raw_evaluator_status") not in {"completed", "error"}
        or meta.get("status") not in {
            "completed", "truncated", "completed_with_evaluator_error",
        }
        or not score_path.is_file()
    ):
        problems.append("Result is not sealed with terminal metadata and evaluator status")
    flags = []
    if evaluator_error:
        flags.append("evaluator_error")
    if budget:
        flags.append("budget")
    if timeout:
        flags.append("timeout")
    if truncated:
        flags.append("truncated")
    if meta.get("status") == "error" or meta.get("error") or (failure and not result):
        flags.append("execution_error")
    if raw is None:
        flags.append("missing")
    if problems:
        flags.append("invalid")
    status = flags[0] if flags else "valid"
    # A known truncation is a policy failure even when its RAW evaluator passed.
    effective = 0.0 if truncated else raw if status == "valid" else None
    elapsed_values = [
        number(payload["duration_sec"]) for payload in (result, meta)
        if payload.get("duration_sec") is not None
    ]
    if len(set(value for value in elapsed_values if value is not None)) > 1:
        raise ValueError(f"{root}: conflicting duration_sec")
    elapsed = elapsed_values[0] if elapsed_values else None
    proxy = trace.get("proxy_usage") or {}
    if not isinstance(proxy, dict):
        proxy = {}
    tokens, measurement_errors = token_metrics(root, meta, proxy)
    return {
        "task_id": task_id, "stratum": task.get("stratum"),
        "result_dir": str(root), "status": status, "flags": flags or ["valid"],
        "raw_score": raw, "effective_score": effective,
        "termination_reasons": sorted(reasons),
        "failure_classifications": sorted(classifications),
        "errors": errors, "diagnostics": problems,
        "agent_returncode": result.get("agent_returncode", meta.get("agent_returncode")),
        "elapsed_seconds": elapsed, **tokens,
        "token_usage_status": "complete" if tokens["total_tokens"] is not None else "unknown_or_partial",
        "reported_proxy_usage": proxy or None,
        "measurement_errors": measurement_errors,
    }


def aggregate(rows: list[dict]) -> dict:
    denominator = len(rows)
    if not denominator:
        raise ValueError("Cannot summarize an empty task selection")
    effective = [row["effective_score"] for row in rows if row["effective_score"] is not None]
    raw = [row["raw_score"] for row in rows if row["raw_score"] is not None]
    known_sum = sum(effective)
    complete = len(effective) == denominator and not any(
        set(row["flags"]) & {"invalid", "missing", "evaluator_error", "execution_error"}
        for row in rows
    )
    measurements = {}
    for key in METRICS:
        values = [row[key] for row in rows if row[key] is not None]
        measurements[key] = {
            "total": sum(values) if len(values) == denominator else None,
            "known_sum": sum(values) if values else None,
            "known_tasks": len(values), "unknown_tasks": denominator - len(values),
        }
    return {
        "selected_tasks": denominator,
        "status_counts": {status: sum(row["status"] == status for row in rows) for status in STATUSES},
        "flag_counts": dict(Counter(flag for row in rows for flag in row["flags"])),
        "raw_observed_count": len(raw),
        "raw_observed_sum": sum(raw) if raw else None,
        "raw_observed_mean": sum(raw) / len(raw) if raw else None,
        "effective_known_count": len(effective),
        "effective_known_sum": known_sum,
        "effective_lower_bound": known_sum / denominator,
        "final_score": known_sum / denominator if complete else None,
        "complete": complete,
        "measurements": measurements,
    }


def summarize(config: dict, run_dir: Path) -> dict:
    config = owned_config(config)
    tasks = selection(config)
    run_dir = run_dir.expanduser().resolve()
    owner = read_object(run_dir / "QUEUE_OWNER.json")
    if owner.get("schema_version") != 1 or owner.get("run_id") != config["run_id"]:
        raise ValueError("Invalid or conflicting queue owner")
    if owner.get("config") != config:
        raise ValueError("Conflicting config and QUEUE_OWNER.json; use the resolved run config")
    state_path = run_dir / "QUEUE_STATE.json"
    state = read_object(state_path) if state_path.exists() else {}
    if state and state.get("run_id") != config["run_id"]:
        raise ValueError("Conflicting QUEUE_STATE run_id")
    ids = {task["id"] for task in tasks}
    for field in ("failures", "children", "retryable_interruptions"):
        if not isinstance(state.get(field, {}), dict) or set(state.get(field, {})) - ids:
            raise ValueError(f"Unknown tasks or invalid QUEUE_STATE.{field}")
    hybrid = run_dir / "hybrid"
    if hybrid.exists():
        unknown = {path.name for path in hybrid.iterdir() if path.is_dir()} - ids
        if unknown:
            raise ValueError(f"Unselected task directories: {sorted(unknown)}")
    for task_id in ids:
        if (run_dir / task_id).exists():
            raise ValueError(f"Conflicting batch/queue task directory: {task_id}")
    rows = []
    for task in tasks:
        root = hybrid / task["id"]
        if root.resolve() != root:
            raise ValueError(f"Task directory must not alias another attempt: {root}")
        rows.append(read_task(root, task, config, state.get("failures", {}).get(task["id"])))
    archived = []
    archive_root = run_dir / "interrupted_attempts"
    if archive_root.exists():
        for path in sorted(archive_root.glob("*/*/interruption.json")):
            interruption = read_object(path)
            task_id = interruption.get("task_id")
            if task_id not in ids:
                raise ValueError(f"Unselected archived task: {path}")
            root = path.parent / "task"
            if root.resolve() != root:
                raise ValueError(f"Archived directory must not alias another attempt: {root}")
            if (root / "result.txt").exists() or (root / "result.json").exists():
                raise ValueError(f"Multiple result attempts for {task_id}: {root}")
            archived.append({
                "task_id": task_id, "path": str(path.parent), "interruption": interruption,
                "attempt": read_task(root, {"id": task_id}, config),
            })
        recorded = {Path(item["path"]).resolve() for item in archived}
        for root in archive_root.glob("*/*"):
            if root.is_dir() and root.resolve() not in recorded:
                raise ValueError(f"Unrecorded archived attempt: {root}")
    return {
        "schema_version": 1, "run_id": config["run_id"],
        "benchmark_version": config.get("benchmark_version") or "v2",
        "run_dir": str(run_dir), "task_ids": [task["id"] for task in tasks],
        "policy": {
            "denominator": "all configured task IDs",
            "truncated_budget_timeout": "effective zero; RAW unchanged",
            "unknown_or_error": "final score withheld; lower bound treats unknown as zero",
            "attempts": "canonical hybrid task directory only; scored archives rejected",
            "tokens": "complete frozen proxy rows reconciled to proxy summary; total is input plus output",
            "measurements": "canonical attempts only; archived attempt evidence is reported separately",
        },
        "aggregate": aggregate(rows), "tasks": rows, "interrupted_attempts": archived,
    }


def render_csv(report: dict) -> str:
    fields = (
        "task_id", "stratum", "status", "flags", "raw_score", "effective_score",
        *METRICS, "token_usage_status", "termination_reasons",
        "failure_classifications", "agent_returncode", "errors", "diagnostics",
        "measurement_errors", "result_dir",
    )
    output = io.StringIO(newline="")
    writer = csv.DictWriter(output, fieldnames=fields, extrasaction="ignore")
    writer.writeheader()
    for row in report["tasks"]:
        writer.writerow({
            key: json.dumps(value, ensure_ascii=False) if isinstance(value, (list, dict)) else value
            for key, value in row.items()
        })
    return output.getvalue()


def render_markdown(report: dict) -> str:
    def cell(value):
        if value is None:
            return "unknown"
        return " ".join(str(value).split()).replace("|", r"\|")

    totals = report["aggregate"]
    lines = [
        f"# OSWorld summary: {cell(report['run_id'])}", "",
        f"Selected denominator: {totals['selected_tasks']}",
        f"Complete: {str(totals['complete']).lower()}",
        f"Final effective score: {cell(totals['final_score'])}",
        f"Effective lower bound (unknowns counted as zero): {totals['effective_lower_bound']}",
        f"Observed RAW mean (NOT final): {cell(totals['raw_observed_mean'])} "
        f"over {totals['raw_observed_count']} scores", "",
        "| Task | Status / flags | RAW | Effective | Tokens | Elapsed (s) | Errors / diagnostics |",
        "| --- | --- | --- | --- | --- | --- | --- |",
    ]
    for row in report["tasks"]:
        lines.append("| " + " | ".join(cell(value) for value in (
            row["task_id"], ", ".join(row["flags"]), row["raw_score"], row["effective_score"],
            row["total_tokens"], row["elapsed_seconds"],
            json.dumps(row["errors"] + row["diagnostics"] + row["measurement_errors"], ensure_ascii=False),
        )) + " |")
    lines.extend(["", "| Measurement | Total | Known sum | Known tasks | Unknown tasks |",
                  "| --- | --- | --- | --- | --- |"])
    for key, value in totals["measurements"].items():
        lines.append("| " + " | ".join(cell(item) for item in (
            key, value["total"], value["known_sum"], value["known_tasks"], value["unknown_tasks"],
        )) + " |")
    lines.extend(["", f"Archived interruptions (not scored): {len(report['interrupted_attempts'])}"])
    for attempt in report["interrupted_attempts"]:
        lines.append(f"- {cell(attempt['task_id'])}: {cell(attempt['path'])}; "
                     f"{cell(json.dumps(attempt['interruption'], ensure_ascii=False))}")
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--run-dir", type=Path, help="Queue run directory, not its hybrid subdirectory")
    parser.add_argument("--output-dir", type=Path, help="Default: <run-dir>/summary")
    args = parser.parse_args(argv)
    try:
        config = read_object(args.config)
        selection(config)
        identity = resolve_config({
            "run_id": config["run_id"],
            "benchmark_version": config.get("benchmark_version") or "v2",
        }, os.environ)
        run_dir = args.run_dir or (
            results_root() / f"osworld-{identity['benchmark_version']}" / identity["run_id"]
        )
        report = summarize(config, run_dir)
        output = (args.output_dir or run_dir / "summary").expanduser().resolve()
        output.mkdir(parents=True, exist_ok=True)
        for name, text in (
            ("summary.json", json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False) + "\n"),
            ("tasks.csv", render_csv(report)),
            ("summary.md", render_markdown(report)),
        ):
            (output / name).write_text(text, encoding="utf-8")
    except (OSError, ValueError) as exc:
        parser.exit(2, f"Summary rejected: {exc}\n")
    print(json.dumps({"output_dir": str(output), **report["aggregate"]}, allow_nan=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
