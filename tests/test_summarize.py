import csv
import io
import json
import os
from pathlib import Path
import subprocess
import sys

import pytest

from hcu.osworld.summarize import (
    aggregate, owned_config, render_csv, render_markdown, summarize,
)
from hcu.osworld.usage import collect_task_proxy_usage
from hcu.osworld.batch_config import runner_command
from hcu.gateway.gemini_proxy import extract_usage


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value), encoding="utf-8")


@pytest.fixture
def run(tmp_path):
    config = {
        "run_id": "fixture-run", "benchmark_version": "v2", "agent": "codex",
        "model": "fixture-model", "effort": "high",
        "tasks": ["partial", "budget", "timeout", "truncated", "error", "missing"],
    }
    root = tmp_path / "runs" / "osworld-v2" / config["run_id"]
    write_json(root / "QUEUE_OWNER.json", {
        "schema_version": 1, "run_id": config["run_id"], "config": owned_config(config),
    })
    write_json(root / "QUEUE_STATE.json", {
        "run_id": config["run_id"], "failures": {}, "children": {},
    })
    return config, root


def seal(run, task_id, score=1.0, **overrides):
    config, root = run
    result = {
        "task_id": task_id, "agent": config["agent"], "model": config["model"],
        "effort": config["effort"], "score": score,
        "raw_evaluator_status": "completed", "truncated": False,
        "termination_reason": "completed", "duration_sec": 12.5,
    }
    result.update(overrides)
    meta = {
        **result, "status": "truncated" if result["truncated"] else "completed",
        "benchmark_version": config["benchmark_version"],
        "run_tag": "task-attempt-" + task_id,
        "affinity_task": "affinity-" + task_id,
    }
    task = root / "hybrid" / task_id
    write_json(task / "result.json", result)
    write_json(task / "meta.json", meta)
    write_json(task / "raw_evaluator.json", {
        "score": score, "status": result["raw_evaluator_status"],
        "errors": result.get("official_evaluator_errors", []),
    })
    (task / "result.txt").write_text(str(score))
    proxy = result.get("trace_summary", {}).get("proxy_usage", {})
    if proxy.get("status") == "completed" and proxy.get("accounted_responses"):
        token_rows = [{
            "task": meta["affinity_task"], "input_tokens": proxy["input_tokens"],
            "output_tokens": proxy["output_tokens"],
        }] + [
            {"task": meta["affinity_task"], "input_tokens": 0, "output_tokens": 0}
            for _ in range(proxy["accounted_responses"] - 1)
        ]
        if proxy.get("unaccounted_requests"):
            token_rows.append({"task": meta["affinity_task"], "http_status": 503})
        (task / "task-agent-token-usage.jsonl").write_text(
            "\n".join(json.dumps(row) for row in token_rows) + "\n"
        )
    return task


def test_fixed_denominator_preserves_raw_continuous_and_all_failure_flags(run):
    seal(run, "partial", 0.375)
    seal(run, "budget", termination_reason="max_model_calls", truncated=True)
    seal(run, "timeout", 0.8, termination_reason="timeout", truncated=True)
    seal(run, "truncated", 0.6, truncated=True)
    seal(run, "error", 0.9, raw_evaluator_status="error",
         official_evaluator_errors=[{"phase": 1, "error": "offline evaluator failed"}])
    report = summarize(*run)
    rows = {row["task_id"]: row for row in report["tasks"]}
    assert [row["task_id"] for row in report["tasks"]] == run[0]["tasks"]
    assert rows["partial"]["status"] == "valid"
    assert rows["partial"]["effective_score"] == 0.375
    for name in ("budget", "timeout", "truncated"):
        assert rows[name]["status"] == name
        assert rows[name]["effective_score"] == 0
    assert rows["timeout"]["raw_score"] == 0.8
    assert rows["error"]["effective_score"] is None
    assert rows["error"]["raw_score"] == 0.9
    assert rows["error"]["errors"]
    assert rows["missing"]["status"] == "missing"
    totals = report["aggregate"]
    assert totals["selected_tasks"] == 6
    assert totals["raw_observed_count"] == 5
    assert totals["effective_lower_bound"] == 0.375 / 6
    assert totals["final_score"] is None
    assert not totals["complete"]
    assert "NOT final" in render_markdown(report)
    csv_rows = list(csv.DictReader(io.StringIO(render_csv(report))))
    assert csv_rows[-1]["raw_score"] == ""
    assert "offline evaluator failed" in csv_rows[-2]["errors"]


def test_complete_run_final_uses_selected_denominator_not_success_count(run):
    for task_id in run[0]["tasks"]:
        seal(run, task_id, 0.25)
    seal(run, "budget", 1, truncated=True, termination_reason="max_model_calls")
    report = summarize(*run)
    assert report["aggregate"]["complete"]
    assert report["aggregate"]["final_score"] == 1.25 / 6
    assert report["aggregate"]["raw_observed_mean"] == 2.25 / 6


@pytest.mark.parametrize("reason", ["max_model_calls", "timeout", "completed"])
@pytest.mark.parametrize("unfinished", ["result.txt", "metadata", "evaluator"])
def test_unsealed_truncation_never_makes_a_final_score(run, reason, unfinished):
    for task_id in run[0]["tasks"]:
        seal(run, task_id)
    task = seal(run, "budget", truncated=True, termination_reason=reason)
    if unfinished == "result.txt":
        (task / "result.txt").unlink()
    else:
        name = "meta.json" if unfinished == "metadata" else "result.json"
        payload = json.loads((task / name).read_text())
        payload.update(
            {"status": "agent_running", "score": None}
            if unfinished == "metadata" else {"raw_evaluator_status": "running"}
        )
        write_json(task / name, payload)
    report = summarize(*run)
    row = report["tasks"][1]
    assert row["raw_score"] == 1
    assert row["effective_score"] == 0
    assert "invalid" in row["flags"]
    assert not report["aggregate"]["complete"]
    assert report["aggregate"]["final_score"] is None
    assert report["aggregate"]["effective_lower_bound"] == 5 / 6


@pytest.mark.parametrize("selection", [
    {"no_instructions": True, "instructions_file": "instructions/hybrid.md"},
    {"no_instructions": True, "instructions_file": ""},
    {"instructions_file": ""},
])
def test_disabled_instructions_match_runner_and_reject_injected_profile(run, selection):
    config, root = run
    config.update(selection)
    resolved = owned_config(config)
    write_json(root / "QUEUE_OWNER.json", {
        "schema_version": 1, "run_id": config["run_id"], "config": resolved,
    })
    for task_id in config["tasks"]:
        task_root = seal(run, task_id)
        meta = json.loads((task_root / "meta.json").read_text())
        meta["instructions_file"] = None
        write_json(task_root / "meta.json", meta)
    command = runner_command(resolved, {"id": "partial"}, root / "hybrid/partial", resolved=True)
    assert command[command.index("--instructions-file") + 1] == ""
    assert summarize(*run)["aggregate"]["complete"]
    meta_path = root / "hybrid/partial/meta.json"
    meta = json.loads(meta_path.read_text())
    meta["instructions_file"] = "/unexpected/profile.md"
    write_json(meta_path, meta)
    with pytest.raises(ValueError, match="conflicting instructions_file"):
        summarize(*run)


def test_unknown_measurements_never_become_zero_and_partial_proxy_is_not_total(run):
    proxy = {
        "status": "completed", "accounted_responses": 2, "unaccounted_requests": 0,
        "input_tokens": 10, "output_tokens": 4, "total_tokens": 14,
    }
    seal(run, "partial", trace_summary={"proxy_usage": proxy})
    seal(run, "budget", duration_sec=None, trace_summary={
        "events": 100, "input_tokens": 0, "output_tokens": 0,
        "proxy_usage": {**proxy, "unaccounted_requests": 1},
    })
    seal(run, "timeout", trace_summary={
        "proxy_usage": {**proxy, "input_tokens": 0, "output_tokens": 0},
    })
    report = summarize(*run)
    row = report["tasks"][1]
    assert row["total_tokens"] is None
    assert row["elapsed_seconds"] is None
    assert row["reported_proxy_usage"]["input_tokens"] == 10
    tokens = report["aggregate"]["measurements"]["total_tokens"]
    assert tokens == {"total": None, "known_sum": 14, "known_tasks": 2, "unknown_tasks": 4}
    assert report["tasks"][2]["total_tokens"] == 0
    empty = aggregate([report["tasks"][-1]])
    assert empty["measurements"]["elapsed_seconds"]["known_sum"] is None


def test_default_zero_proxy_output_without_explicit_evidence_is_unknown(run):
    task = seal(run, "partial", trace_summary={"proxy_usage": {
        "status": "completed", "accounted_responses": 1, "unaccounted_requests": 0,
        "input_tokens": 10, "output_tokens": 0,
    }})
    (task / "task-agent-token-usage.jsonl").write_text(json.dumps({
        "task": "affinity-partial", "input_tokens": 10,
    }) + "\n")
    row = summarize(*run)["tasks"][0]
    assert row["total_tokens"] is None
    assert row["output_tokens"] is None
    assert row["measurement_errors"]
    (task / "task-agent-token-usage.jsonl").unlink()
    assert summarize(*run)["tasks"][0]["total_tokens"] is None


def test_token_rows_from_other_attempt_are_rejected(run):
    task = seal(run, "partial")
    (task / "task-agent-token-usage.jsonl").write_text(json.dumps({
        "task": "other-affinity", "input_tokens": 10, "output_tokens": 0,
    }) + "\n")
    with pytest.raises(ValueError, match="conflicting token task affinity"):
        summarize(*run)


def collect_into_task(run, tmp_path, rows):
    """Use actual runner collection output, not synthesized proxy summaries."""
    log = tmp_path / "gateway.jsonl"
    log.write_text("\n".join(json.dumps(row) for row in rows) + "\n")
    proxy, evidence = collect_task_proxy_usage(log, "affinity-partial")
    task = seal(run, "partial")
    for name in ("result.json", "meta.json"):
        payload = json.loads((task / name).read_text())
        payload["trace_summary"] = {"proxy_usage": proxy}
        write_json(task / name, payload)
    (task / "task-agent-token-usage.jsonl").write_bytes(evidence)
    return proxy, summarize(*run)["tasks"][0]


def test_real_collect_excludes_unadmitted_and_non_generation_requests(run, tmp_path):
    usage = extract_usage({"usageMetadata": {
        "promptTokenCount": 20, "toolUsePromptTokenCount": 3,
        "cachedContentTokenCount": 5, "candidatesTokenCount": 7,
        "thoughtsTokenCount": 11, "totalTokenCount": 41,
    }})
    task = {"task": "affinity-partial"}
    proxy, row = collect_into_task(run, tmp_path, [
        {**task, "generation_request": True, "generation_admitted": True,
         "model_step_completed": True, **usage},
        {**task, "budget_rejected": True},
        {**task, "budget_inflight": True},
        {**task, "generation_retry_exhausted": True, "generation_admitted": False},
        {**task, "generation_request": False, "method": "POST",
         "path": "/v1beta/models/gemini:countTokens", "http_status": 200},
        {**task, "generation_request": False, "method": "POST",
         "path": "/v1/messages/count_tokens", "http_status": 200},
        {**task, "generation_request": False, "method": "GET",
         "path": "/v1beta/models/gemini", "http_status": 200},
        {"task": "other", "input_tokens": 999, "output_tokens": 999},
    ])
    assert proxy["request_attempts"] == 7
    assert proxy["gateway_rejected_requests"] == 3
    assert proxy["non_generation_requests"] == 3
    assert proxy["model_requests"] == proxy["accounted_responses"] == 1
    assert proxy["unaccounted_requests"] == 0
    assert row["measurement_errors"] == []
    assert row["input_tokens"] == 23
    assert row["output_tokens"] == 18
    assert row["total_tokens"] == proxy["total_tokens"] == 41
    assert row["token_usage_status"] == "complete"


@pytest.mark.parametrize("with_usage", [False, True])
def test_real_collect_keeps_admitted_retry_exhaustion(run, tmp_path, with_usage):
    task = {"task": "affinity-partial", "generation_request": True, "generation_admitted": True}
    failed = {**task, "generation_retry_exhausted": True, "http_status": 503}
    if with_usage:
        failed.update(input_tokens=4, output_tokens=2, total_tokens=6)
    proxy, row = collect_into_task(run, tmp_path, [
        {**task, "model_step_completed": True,
         "input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
        failed,
    ])
    assert proxy["gateway_rejected_requests"] == 0
    assert proxy["model_requests"] == 2
    assert proxy["errors"] == 1
    if with_usage:
        assert row["total_tokens"] == proxy["total_tokens"] == 11
    else:
        assert proxy["unaccounted_requests"] == 1
        assert row["total_tokens"] is None
        assert row["measurement_errors"]


def test_real_collect_preserves_non_generation_usage(run, tmp_path):
    proxy, row = collect_into_task(run, tmp_path, [{
        "task": "affinity-partial", "generation_request": False, "method": "POST",
        "path": "/v1/responses/compact", "http_status": 200,
        "input_tokens": 8, "output_tokens": 2, "total_tokens": 10,
    }])
    assert proxy["non_generation_requests"] == 1
    assert proxy["model_requests"] == 0
    assert row["total_tokens"] == 10


@pytest.mark.parametrize("auxiliary", [True, False])
def test_real_collect_only_non_generations_or_rejections_is_known_zero(run, tmp_path, auxiliary):
    entry = (
        {"generation_request": False, "path": "/v1beta/models/gemini:countTokens"}
        if auxiliary else {"generation_retry_exhausted": True}
    )
    proxy, row = collect_into_task(run, tmp_path, [{"task": "affinity-partial", **entry}])
    assert proxy["model_requests"] == proxy["accounted_responses"] == 0
    assert row["total_tokens"] == 0
    assert row["measurement_errors"] == []


def test_real_collect_unclassified_usage_cannot_prove_complete_coverage(run, tmp_path):
    proxy, row = collect_into_task(run, tmp_path, [{
        "task": "affinity-partial", "input_tokens": 8, "output_tokens": 2, "total_tokens": 10,
    }])
    assert proxy["status"] == "incomplete"
    assert proxy["unclassified_requests"] == 1
    assert row["total_tokens"] is None


@pytest.mark.parametrize("changes", [
    {"run_id": "other"}, {"model": "other"}, {"tasks": ["partial"]},
    {"max_model_calls": 2}, {"instructions_file": ""},
])
def test_reject_conflicting_config(run, changes):
    with pytest.raises(ValueError, match="conflicting|Conflicting"):
        summarize({**run[0], **changes}, run[1])


@pytest.mark.parametrize("field,value", [
    ("run_id", "other"), ("task_id", "other"), ("benchmark_version", "v1"),
    ("model", "other"), ("agent", "claude-code"), ("effort", "low"),
])
def test_reject_mixed_task_identity(run, field, value):
    root = seal(run, "partial")
    meta = json.loads((root / "meta.json").read_text())
    write_json(root / "meta.json", {**meta, field: value})
    with pytest.raises(ValueError, match=f"conflicting {field}"):
        summarize(*run)


def test_conflicting_duplicate_score_is_rejected_not_best_selected(run):
    task = seal(run, "partial", 0.5)
    (task / "result.txt").write_text("1.0")
    with pytest.raises(ValueError, match="conflicting scores"):
        summarize(*run)


def test_other_task_or_duplicate_layout_is_rejected(run):
    extra = run[1] / "hybrid" / "not-selected"
    extra.mkdir(parents=True)
    with pytest.raises(ValueError, match="Unselected task"):
        summarize(*run)
    extra.rmdir()
    (run[1] / "partial").mkdir()
    with pytest.raises(ValueError, match="batch/queue"):
        summarize(*run)


def test_unknown_queue_failure_and_wrong_run_are_rejected(run):
    state = run[1] / "QUEUE_STATE.json"
    write_json(state, {"run_id": "other"})
    with pytest.raises(ValueError, match="QUEUE_STATE run_id"):
        summarize(*run)
    write_json(state, {"run_id": run[0]["run_id"], "failures": {"other": "error"}})
    with pytest.raises(ValueError, match="Unknown tasks"):
        summarize(*run)


def test_duplicate_and_unsafe_config_tasks_rejected(run):
    for tasks in (["partial", "partial"], ["../outside"]):
        with pytest.raises(ValueError, match="Duplicate|path component"):
            summarize({**run[0], "tasks": tasks}, run[1])


@pytest.mark.parametrize("bad", ["NaN", "inf", "-0.1", "1.01", "junk"])
def test_bad_score_is_visible_invalid_not_silently_dropped(run, bad):
    task = seal(run, "partial", 0.4)
    (task / "result.txt").write_text(bad)
    row = summarize(*run)["tasks"][0]
    assert row["status"] == "invalid"
    assert row["raw_score"] == 0.4
    assert row["effective_score"] is None
    assert row["diagnostics"]


def test_meta_only_failure_and_malformed_result_are_visible(run):
    write_json(run[1] / "hybrid" / "partial" / "meta.json", {
        "task_id": "partial", "status": "error", "error": "guest crashed",
        "failure_classification": "guest_failure", "duration_sec": 13,
    })
    seal(run, "error")
    (run[1] / "hybrid" / "error" / "result.json").write_text("{invalid")
    report = summarize(*run)
    failed = report["tasks"][0]
    assert failed["status"] == "execution_error"
    assert failed["failure_classifications"] == ["guest_failure"]
    assert failed["errors"][0]["value"] == "guest crashed"
    assert failed["elapsed_seconds"] == 13
    assert "invalid" in report["tasks"][4]["flags"]
    assert not report["aggregate"]["complete"]


def test_budget_and_evaluator_error_both_preserved(run):
    seal(run, "budget", truncated=True, termination_reason="max_model_calls",
         raw_evaluator_status="error", official_evaluator_errors=["failure"])
    row = summarize(*run)["tasks"][1]
    assert row["flags"] == ["evaluator_error", "budget", "truncated"]
    assert row["raw_score"] == 1
    assert row["effective_score"] == 0


def test_retry_archives_reported_but_never_scored_or_best_selected(run):
    archive = run[1] / "interrupted_attempts" / "time" / "partial"
    write_json(archive / "interruption.json", {
        "task_id": "partial", "reason": "coordinator interrupted",
    })
    write_json(archive / "task" / "meta.json", {
        "task_id": "partial", "status": "error", "error": "connection lost",
    })
    seal(run, "partial", 0.2)
    report = summarize(*run)
    assert report["tasks"][0]["effective_score"] == 0.2
    assert report["interrupted_attempts"][0]["attempt"]["errors"][0]["value"] == "connection lost"
    write_json(archive / "task" / "result.json", {"task_id": "partial", "score": 1})
    with pytest.raises(ValueError, match="Multiple result attempts"):
        summarize(*run)


def test_unrecorded_retry_is_not_ignored(run):
    (run[1] / "interrupted_attempts" / "time" / "partial").mkdir(parents=True)
    with pytest.raises(ValueError, match="Unrecorded"):
        summarize(*run)


def test_task_directory_cannot_link_to_other_run(run, tmp_path):
    destination = tmp_path / "other-run"
    destination.mkdir()
    (run[1] / "hybrid").mkdir()
    (run[1] / "hybrid" / "partial").symlink_to(destination, target_is_directory=True)
    with pytest.raises(ValueError, match="alias"):
        summarize(*run)


def test_evidence_file_cannot_link_to_other_run(run, tmp_path):
    task = seal(run, "partial")
    external = tmp_path / "other-result.txt"
    external.write_text("1")
    (task / "result.txt").unlink()
    (task / "result.txt").symlink_to(external)
    with pytest.raises(ValueError, match="alias"):
        summarize(*run)


def test_missing_identity_does_not_count_as_valid(run):
    task = seal(run, "partial")
    result = json.loads((task / "result.json").read_text())
    del result["task_id"]
    write_json(task / "result.json", result)
    row = summarize(*run)["tasks"][0]
    assert row["status"] == "invalid"
    assert row["effective_score"] is None


def test_config_environment_binding_matches_queue_without_sourcing_files(run, monkeypatch):
    config = dict(run[0], guest_image="${SUMMARY_TEST_IMAGE}")
    monkeypatch.setenv("SUMMARY_TEST_IMAGE", "/tmp/fixture-image.qcow2")
    write_json(run[1] / "QUEUE_OWNER.json", {
        "schema_version": 1, "run_id": config["run_id"], "config": owned_config(config),
    })
    summarize(config, run[1])
    monkeypatch.setenv("SUMMARY_TEST_IMAGE", "/tmp/changed-image.qcow2")
    with pytest.raises(ValueError, match="Conflicting config"):
        summarize(config, run[1])


def test_missing_owner_is_not_inferred_from_result_paths(run):
    (run[1] / "QUEUE_OWNER.json").unlink()
    with pytest.raises(FileNotFoundError):
        summarize(*run)


@pytest.mark.parametrize("explicit", [False, True])
def test_real_module_entrypoint_fixture_without_osworld_or_model(run, tmp_path, explicit):
    seal(run, "partial", 0.5)
    config_path = tmp_path / "config.json"
    write_json(config_path, run[0])
    command = [sys.executable, "-m", "hcu.osworld.summarize", "--config", str(config_path)]
    output = run[1] / "summary"
    if explicit:
        output = tmp_path / "exported-summary"
        command += ["--run-dir", str(run[1]), "--output-dir", str(output)]
    result = subprocess.run(
        command, cwd=Path(__file__).resolve().parents[1], capture_output=True, text=True,
        env={**os.environ, "HCU_RESULTS_DIR": str(tmp_path / "runs")},
    )
    assert result.returncode == 0, result.stderr
    report = json.loads((output / "summary.json").read_text())
    assert report["aggregate"]["selected_tasks"] == 6
    assert report["aggregate"]["effective_lower_bound"] == 0.5 / 6
    assert report["aggregate"]["final_score"] is None
    assert len(list(csv.DictReader(io.StringIO((output / "tasks.csv").read_text())))) == 6
    assert "unknown" in (output / "summary.md").read_text()
    assert json.loads(result.stdout)["output_dir"] == str(output)


def test_cli_conflict_exits_nonzero_and_does_not_write_summary(run, tmp_path):
    config_path = tmp_path / "config.json"
    write_json(config_path, {**run[0], "model": "other"})
    result = subprocess.run(
        [sys.executable, "-m", "hcu.osworld.summarize", "--config", str(config_path),
         "--run-dir", str(run[1])],
        cwd=Path(__file__).resolve().parents[1], capture_output=True, text=True,
    )
    assert result.returncode == 2
    assert "Conflicting config" in result.stderr
    assert not (run[1] / "summary").exists()
