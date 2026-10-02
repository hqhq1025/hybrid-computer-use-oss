"""Score validation and sealing without an OSWorld checkout, model, or VM."""

from decimal import Decimal
from fractions import Fraction
import importlib.util
import json
from pathlib import Path
import sys
import time
from types import ModuleType, SimpleNamespace
from unittest.mock import Mock

import pytest

from hcu.osworld.evaluation import run_official_evaluator
from hcu.osworld.summarize import aggregate, read_task


@pytest.mark.parametrize("value,expected", [
    (0, 0.0), (False, 0.0), (True, 1.0), (0.25, 0.25),
    (Decimal("0.125"), 0.125), (Fraction(1, 4), 0.25), ("0.5", 0.5),
])
@pytest.mark.parametrize("wrapped", [False, True])
def test_valid_scalar_scores(value, expected, wrapped):
    result = {"score": value, "detail": "retained"} if wrapped else value
    channel = run_official_evaluator(lambda: result)
    assert channel["status"] == "completed"
    assert channel["result"]["score"] == expected
    assert type(channel["result"]["score"]) is float
    if wrapped:
        assert channel["result"]["detail"] == "retained"
        assert result["score"] is value


BAD_SCORES = [
    None, float("nan"), float("inf"), -float("inf"),
    "NaN", "Infinity", "-Infinity", "not a number",
    Decimal("NaN"), Decimal("Infinity"), [], {},
]


@pytest.mark.parametrize("value", BAD_SCORES)
@pytest.mark.parametrize("wrapped", [False, True])
def test_invalid_scores_preserve_evidence_not_a_score(value, wrapped):
    result = {"score": value, "detail": "getter evidence"} if wrapped else value
    channel = run_official_evaluator(lambda: result)
    assert channel["status"] == "error"
    assert channel["result"] is None
    assert channel["result_repr"] == repr(result)
    assert channel["error"]
    assert channel["error"] in channel["traceback"]
    json.dumps(channel, allow_nan=False)


def test_missing_score_does_not_default_to_zero():
    channel = run_official_evaluator(lambda: {"detail": "getter returned no score"})
    assert channel["status"] == "error"
    assert channel["result"] is None
    assert channel["error"] == "KeyError: 'score'"
    assert "getter returned no score" in channel["result_repr"]


def test_numpy_scalar_compatibility():
    numpy = pytest.importorskip("numpy")
    for value in (numpy.bool_(False), numpy.bool_(True), numpy.int64(0), numpy.float32(0.25)):
        channel = run_official_evaluator(lambda: value)
        assert channel["status"] == "completed"
        assert channel["result"]["score"] == float(value)
    for value in (numpy.float32("nan"), numpy.float64("inf")):
        assert run_official_evaluator(lambda: value)["status"] == "error"


def test_evaluator_exception_has_no_returned_result():
    evaluate = Mock(side_effect=RuntimeError("getter failed"))
    channel = run_official_evaluator(evaluate)
    evaluate.assert_called_once_with()
    assert channel["status"] == "error"
    assert channel["result_repr"] is None
    assert "RuntimeError: getter failed" in channel["traceback"]


@pytest.fixture
def runner(monkeypatch):
    # Isolate imports and forbid runtime entry points; only phase accounting
    # and result sealing run against local test doubles.
    for name, attribute in [
        ("desktop_env.cache", "run_scoped_cache_dir"),
        ("desktop_env.desktop_env", "DesktopEnv"),
        ("hcu.osworld.task_loader", "load_task_config"),
    ]:
        module = ModuleType(name)
        setattr(module, attribute, Mock(side_effect=AssertionError("No VM or task loading")))
        monkeypatch.setitem(sys.modules, name, module)
    spec = importlib.util.spec_from_file_location(
        "_evaluation_runner_test",
        Path(__file__).resolve().parents[1] / "hcu/osworld/run_task.py",
    )
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, module)
    spec.loader.exec_module(module)
    monkeypatch.setattr(module, "run_agent_phase", Mock(return_value={
        "final_message": "TASK_COMPLETE",
        "done_payload": {"returncode": 0},
        "truncated": False,
        "budget_exhausted": False,
    }))
    monkeypatch.setattr(module, "save_phase_screenshot", Mock())
    monkeypatch.setattr(module, "summarize_usage", lambda *_: {})
    return module


def make_run(runner, tmp_path, phases=()):
    run = SimpleNamespace(
        args=SimpleNamespace(
            task_id="score-test", agent="codex", model="offline", effort="high",
            max_model_calls=1, poll_interval=0, max_user_simulator_turns=0,
            timeout=0,
        ),
        result_dir=tmp_path, phases=list(phases), meta={"task_id": "score-test"},
        instruction="Test instruction", current_date="2000-01-01",
        task={}, prompt_path=tmp_path / "prompt.txt",
        token_log=tmp_path / "tokens.jsonl", affinity_task="score-test",
        started=time.monotonic(),
    )
    run.write_meta = lambda **kwargs: runner.TaskRun.write_meta(run, **kwargs)
    return run


def assert_sealed(run, expected_score, expected_status):
    payloads = {
        name: json.loads((run.result_dir / name).read_text())
        for name in ("result.json", "raw_evaluator.json", "meta.json")
    }
    for name, payload in payloads.items():
        assert payload["score"] == expected_score, name
        status_key = "status" if name == "raw_evaluator.json" else "raw_evaluator_status"
        assert payload[status_key] == expected_status, name
        json.dumps(payload, allow_nan=False)
    text = (run.result_dir / "result.txt").read_text()
    assert json.loads(text) == expected_score
    if expected_status == "error":
        assert payloads["meta.json"]["status"] == "completed_with_evaluator_error"
        with pytest.raises(ValueError):
            float(text)
    row = read_task(run.result_dir, {"id": run.args.task_id}, {"run_id": "score-test"})
    assert row["status"] == ("evaluator_error" if expected_status == "error" else "valid")
    assert row["raw_score"] == expected_score
    assert row["effective_score"] == expected_score
    totals = aggregate([row])
    assert totals["raw_observed_count"] == int(expected_status == "completed")
    assert totals["effective_known_count"] == int(expected_status == "completed")
    assert totals["final_score"] == expected_score
    return payloads


@pytest.mark.parametrize("value", [{}, None, float("nan"), float("inf"), -float("inf")])
def test_phase_error_is_null_at_every_consumption_boundary(runner, tmp_path, value):
    run = make_run(runner, tmp_path)
    env = SimpleNamespace(evaluate=Mock(return_value=value), action_history=[])
    outcome = runner.run_phases(run, env, "unused")
    assert outcome.total_score is None
    assert outcome.phase_results[0]["score"] is None
    assert outcome.phase_details[0]["gate_stop"] == "official_evaluator_error"
    assert outcome.official_evaluator_errors[0]["result_repr"] == repr(value)
    assert outcome.evaluator_payload["result_repr"] == repr(value)
    env.evaluate.assert_called_once_with()
    assert runner.finish_run(run, env, "unused", outcome) == 0
    payloads = assert_sealed(run, None, "error")
    assert payloads["result.json"]["result_repr"] == repr(value)
    assert json.loads((tmp_path / "phase_details.json").read_text())[0]["score"] is None


@pytest.mark.parametrize("value,expected", [(False, 0.0), (True, 1.0), (0.34567, 0.3457)])
def test_valid_score_survives_phase_and_sealing(runner, tmp_path, value, expected):
    run = make_run(runner, tmp_path)
    env = SimpleNamespace(evaluate=lambda: value, action_history=[])
    outcome = runner.run_phases(run, env, "unused")
    assert outcome.total_score == float(value)
    assert not outcome.official_evaluator_errors
    runner.finish_run(run, env, "unused", outcome)
    assert_sealed(run, expected, "completed")


@pytest.mark.parametrize("total", [None, float("nan"), float("inf"), -float("inf")])
def test_final_boundary_rejects_invalid_aggregate(runner, tmp_path, total):
    run = make_run(runner, tmp_path)
    outcome = runner.PhaseOutcome(total_score=total)
    runner.finish_run(run, None, "unused", outcome)
    payloads = assert_sealed(run, None, "error")
    error = payloads["raw_evaluator.json"]["errors"][0]
    assert error["stage"] == "aggregation"
    assert error["result_repr"] == repr(total)


@pytest.mark.parametrize("values,expected,error", [
    ([0.4, {"detail": "missing"}, 0.6], None, True),
    ([1e308, 1e308], None, True),
    ([0.25, 0.5], 0.75, False),
    ([0.75, 0.75], 1.0, False),
    ([-0.25], 0.0, False),
])
def test_multiphase_accounting_and_aggregate_validation(
    runner, tmp_path, values, expected, error,
):
    phases = [{
        "name": f"Phase {index}",
        "evaluate": Mock(return_value=value),
        "setup": Mock(),
        "pause_after_setup_seconds": 0,
    } for index, value in enumerate(values)]
    run = make_run(runner, tmp_path, phases)
    env = SimpleNamespace(
        action_history=[], setup_controller=SimpleNamespace(assert_root_writable=Mock()),
    )
    outcome = runner.run_phases(run, env, "unused")
    if len(values) == 3:
        assert outcome.total_score == 0.4
        assert outcome.phase_results[1]["score"] is None
        phases[2]["setup"].assert_not_called()
        phases[2]["evaluate"].assert_not_called()
        assert json.loads((tmp_path / "phase_results.json").read_text())[1]["score"] is None
    runner.finish_run(run, env, "unused", outcome)
    assert_sealed(run, expected, "error" if error else "completed")
