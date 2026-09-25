import importlib.util
import json
import os
from pathlib import Path
import sys

import pytest

from hcu.osworld.batch_config import validate_config


ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def queue_module(monkeypatch):
    # Exercise the shipped volume helper without an installed OSWorld checkout.
    volume_spec = importlib.util.spec_from_file_location(
        "desktop_env.providers.volume",
        ROOT / "osworld-overlay/desktop_env/providers/volume.py",
    )
    volume = importlib.util.module_from_spec(volume_spec)
    volume_spec.loader.exec_module(volume)
    monkeypatch.setitem(sys.modules, volume_spec.name, volume)
    spec = importlib.util.spec_from_file_location(
        "queue_under_test", ROOT / "hcu/osworld/run_queue.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.mark.parametrize(
    ("current", "old", "expected"),
    [
        (None, None, None),
        (None, "/unused-old.json", None),
        ("", "/unused-old.json", None),
        ("/selected.json", None, "/selected.json"),
        ("/selected.json", "/unused-old.json", "/selected.json"),
    ],
)
def test_default_queue_uses_only_current_variable(
    queue_module, monkeypatch, current, old, expected
):
    for name, value in (("HCU_QUEUE", current), ("HCU_V2_QUEUE", old)):
        if value is None:
            monkeypatch.delenv(name, raising=False)
        else:
            monkeypatch.setenv(name, value)
    fallback = queue_module.REPO_ROOT / "configs/osworld-v2-codex-queue.example.json"
    assert queue_module.default_queue_path() == (Path(expected) if expected else fallback)


def test_explicit_queue_overrides_default(queue_module, monkeypatch):
    monkeypatch.setattr(queue_module, "DEFAULT_QUEUE", Path("/default.json"))
    monkeypatch.setattr(sys, "argv", ["run_queue", "--queue", "/explicit.json"])
    assert queue_module.parse_args().queue == Path("/explicit.json")


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        (None, "unclassified"),
        (" \n\t", "unclassified"),
        (0, "0"),
        ("blocked tasks", "blocked tasks"),
        ("ERROR recovery", "ERROR recovery"),
        ("failed-login", "failed-login"),
        ("failure analysis", "failure analysis"),
        ("Exception handling", "Exception handling"),
        ("traceback review", "traceback review"),
        ("  error | failed\n recovery\t", r"error \| failed recovery"),
    ],
)
def test_stratum_preserves_labels_and_escapes_table_cells(queue_module, value, expected):
    assert queue_module.QueueRunner.display_stratum(value) == expected


@pytest.mark.parametrize("version", ["v1", "v2", None])
def test_status_generation_matches_commands_and_keeps_results_separate(
    queue_module, monkeypatch, tmp_path, version
):
    config = {
        "model": "test-model",
        "effort": "high",
        "max_model_calls": 0,
        "timeout_seconds": 1,
        "max_active_tasks": 1,
        "tasks": [
            {"id": "scored", "stratum": "failed | recovery"},
            {"id": "evaluator-error", "stratum": "normal"},
            {"id": "active", "stratum": "ERROR analysis"},
        ],
    }
    if version is not None:
        config["benchmark_version"] = version
    path = tmp_path / "queue.json"
    path.write_text(json.dumps(config))
    runner_type = queue_module.QueueRunner
    monkeypatch.setattr(runner_type, "load_child_environment", lambda self: {})
    monkeypatch.setattr(runner_type, "adopt_live_attempts", lambda self: None)
    runner = runner_type(path, 1)
    for task_id, score, status in (
        ("scored", 0.5, "completed"),
        ("evaluator-error", 0, "error"),
    ):
        runner.root_path(task_id).mkdir()
        runner.result_path(task_id).write_text(str(score))
        (runner.root_path(task_id) / "result.json").write_text(
            json.dumps({"raw_evaluator_status": status})
        )
    runner.adopted_pids["active"] = os.getpid()
    runner.render_status()
    text = (tmp_path / "QUEUE_STATUS.md").read_text()
    command = runner.command(config["tasks"][0])
    generation = command[command.index("--benchmark-version") + 1]
    assert text.startswith(f"# Rolling OSWorld {generation.upper()} Queue Status\n")
    assert generation == (version or "v2")
    assert "| scored | failed \\| recovery | completed RAW=0.5000 |" in text
    assert "| evaluator-error | normal | completed ERR=evaluator |" in text
    assert "| active | ERROR analysis | external-running |" in text
    assert "Official RAW: 0.5000 across 1 completed scores" in text
    assert "Completed tasks: 2/3" in text
    assert "Active task runs: 1/1" in text
    state = json.loads((tmp_path / "QUEUE_STATE.json").read_text())
    assert state["children"] == {"active": os.getpid()}


def test_process_check_handles_exited_process(queue_module, monkeypatch):
    def gone(pid, sig):
        assert (pid, sig) == (123, 0)
        raise ProcessLookupError(pid)

    monkeypatch.setattr(queue_module.os, "kill", gone)
    runner = object.__new__(queue_module.QueueRunner)
    assert not runner.pid_alive(123)


@pytest.mark.parametrize("version", ["v3", "V1", "mixed", ["v1", "v2"]])
def test_config_rejects_unknown_or_mixed_generation(version):
    with pytest.raises(ValueError, match="benchmark_version must be v1 or v2"):
        validate_config({"benchmark_version": version, "tasks": ["001"]})


def test_config_rejects_task_level_generation_override():
    with pytest.raises(ValueError, match="Unknown keys for task 001: benchmark_version"):
        validate_config({
            "benchmark_version": "v1",
            "tasks": [{"id": "001", "benchmark_version": "v2"}],
        })
