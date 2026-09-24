"""Task-scoped model usage from the gateway token log.

Gateways append one JSON row per model request. Rows carry the task
affinity ID, so a run can count its own requests for the model-call budget
and reconcile token usage against the agent trace.
"""

from __future__ import annotations

import json
from pathlib import Path


def collect_task_proxy_usage(
    token_log: Path,
    affinity_task: str,
) -> tuple[dict, bytes]:
    summary = {
        "status": "missing",
        "request_attempts": 0,
        "model_requests": 0,
        "budget_rejected_requests": 0,
        "accounted_responses": 0,
        "unaccounted_requests": 0,
        "input_tokens": 0,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "total_tokens": 0,
        "errors": 0,
        "rate_limited_requests": 0,
        "endpoints": [],
        "endpoint_affinity_ok": True,
    }
    if not token_log.is_file():
        return summary, b""

    matching_lines = []
    endpoints = set()
    with token_log.open(
        "r",
        encoding="utf-8",
        errors="replace",
    ) as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if row.get("task") != affinity_task:
                continue
            matching_lines.append(
                json.dumps(row, ensure_ascii=False, separators=(",", ":"))
            )
            summary["request_attempts"] += 1
            if row.get("budget_rejected") is True:
                summary["budget_rejected_requests"] += 1
                continue
            summary["model_requests"] += 1
            if row.get("endpoint"):
                endpoints.add(str(row["endpoint"]))
            if (
                row.get("error")
                or row.get("accounting_error")
                or row.get("upstream_error")
                or int(row.get("http_status") or 0) >= 400
            ):
                summary["errors"] += 1
            if row.get("rate_limited") or int(
                row.get("http_status") or 0
            ) == 429:
                summary["rate_limited_requests"] += 1
            if row.get("input_tokens") is None:
                continue
            summary["accounted_responses"] += 1
            summary["input_tokens"] += int(
                row.get("input_tokens") or 0
            )
            summary["cached_input_tokens"] += int(
                row.get("cached_tokens") or 0
            )
            summary["output_tokens"] += int(
                row.get("output_tokens") or 0
            )
            summary["reasoning_tokens"] += int(
                row.get("reasoning_tokens") or 0
            )
            summary["total_tokens"] += int(
                row.get("total_tokens") or 0
            )

    summary["unaccounted_requests"] = (
        summary["model_requests"] - summary["accounted_responses"]
    )
    summary["endpoints"] = sorted(endpoints)
    summary["endpoint_affinity_ok"] = len(endpoints) <= 1
    summary["status"] = (
        "completed" if summary["request_attempts"] else "no_matching_rows"
    )
    frozen_rows = (
        ("\n".join(matching_lines) + "\n").encode("utf-8")
        if matching_lines
        else b""
    )
    return summary, frozen_rows


def reconcile_trace_and_proxy_usage(
    trace_summary: dict,
    proxy_usage: dict,
    agent: str = "codex",
) -> dict:
    trace_usage = {
        "input_tokens": int(trace_summary.get("input_tokens") or 0),
        "cached_input_tokens": int(
            trace_summary.get("cached_input_tokens") or 0
        ),
        "output_tokens": int(trace_summary.get("output_tokens") or 0),
        "reasoning_tokens": int(
            trace_summary.get("reasoning_tokens") or 0
        ),
    }
    trace_summary["trace_usage"] = trace_usage
    trace_summary["proxy_usage"] = proxy_usage
    if int(proxy_usage.get("accounted_responses") or 0) <= 0:
        trace_summary["token_usage_source"] = f"{agent}_trace"
        trace_summary["token_usage_reconciled"] = False
        return trace_summary

    proxy_totals = {
        "input_tokens": int(proxy_usage.get("input_tokens") or 0),
        "cached_input_tokens": int(
            proxy_usage.get("cached_input_tokens") or 0
        ),
        "output_tokens": int(proxy_usage.get("output_tokens") or 0),
        "reasoning_tokens": int(
            proxy_usage.get("reasoning_tokens") or 0
        ),
    }
    trace_summary.update(proxy_totals)
    trace_summary["token_usage_source"] = "proxy_token_log"
    trace_summary["token_usage_reconciled"] = (
        trace_usage != proxy_totals
    )
    return trace_summary


def normalize_budget_termination(
    trace_summary: dict,
    *,
    budget_exhausted: bool,
) -> dict:
    if not budget_exhausted:
        return trace_summary
    original = trace_summary.get("failure_classification")
    if original and original != "model_call_budget_exhausted":
        trace_summary["failure_classification_original"] = original
    trace_summary["failure_classification"] = (
        "model_call_budget_exhausted"
    )
    return trace_summary


def count_model_requests(token_log: Path, affinity_task: str) -> int:
    count = 0
    with token_log.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if (
                row.get("task") == affinity_task
                and not row.get("budget_rejected")
            ):
                count += 1
    return count


def task_model_budget_rejected(
    token_log: Path,
    affinity_task: str,
) -> bool:
    with token_log.open("r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if (
                row.get("task") == affinity_task
                and row.get("budget_rejected") is True
            ):
                return True
    return False
