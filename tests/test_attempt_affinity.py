"""Attempt identity through the real runner preparation, without starting a guest."""

import importlib.util
from pathlib import Path
import sys
from types import ModuleType, SimpleNamespace
from unittest.mock import Mock

import pytest

from hcu.gateway.common import GenerationBudget, TASK_AFFINITY_PREFIX
from hcu.osworld.agents import task_gateway_base
from hcu.paths import OSWORLD_ROOT


@pytest.mark.parametrize("explicit_result_dirs", [False, True])
def test_same_second_attempts_have_independent_affinity(tmp_path, monkeypatch, explicit_result_dirs):
    if not (OSWORLD_ROOT / "desktop_env").is_dir():
        pytest.skip("OSWorld checkout needed to import run_task")
    desktop = ModuleType("desktop_env.desktop_env")
    desktop.DesktopEnv = Mock(side_effect=AssertionError("No guest may be constructed"))
    monkeypatch.setitem(sys.modules, desktop.__name__, desktop)
    # Isolate the import so the test double cannot remain in the real runner module.
    spec = importlib.util.spec_from_file_location(
        "_attempt_runner_test", Path(__file__).resolve().parents[1] / "hcu/osworld/run_task.py",
    )
    run_task = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, run_task)
    spec.loader.exec_module(run_task)

    artifact = tmp_path / "artifact"
    artifact.touch()
    token_log = tmp_path / "tokens.jsonl"
    token_log.touch()
    monkeypatch.setattr(run_task, "time", SimpleNamespace(strftime=lambda _: "20000101-120000"))
    monkeypatch.setattr(run_task, "DEFAULT_RESULTS_DIR", tmp_path / "results")
    monkeypatch.setattr(run_task, "load_task_config", lambda *a, **kw: {"instruction": "offline"})
    monkeypatch.setattr(run_task, "resolve_guest_image", lambda *a: "/unused/image.qcow2")
    runs = []
    for queue in ("queue-a", "queue-b"):
        argv = [
            "run_task", "--task-id", "same-task", "--benchmark-version", "v1",
            "--agent-binary", str(artifact), "--mcp-binary", str(artifact),
            "--base-instructions-file", str(artifact), "--instructions-file", "",
            "--token-log", str(token_log), "--max-model-calls", "1",
            "--gateway", "http://unused:8611",
        ]
        if explicit_result_dirs:
            argv += ["--result-dir", str(tmp_path / queue / "same-task")]
        monkeypatch.setattr(sys, "argv", argv)
        runs.append(run_task.prepare_run(run_task.parse_args()))

    first, second = runs
    assert first.result_dir != second.result_dir
    assert first.affinity_task != second.affinity_task
    assert first.affinity_task == TASK_AFFINITY_PREFIX + first.run_tag
    assert first.token_log == second.token_log
    endpoints = [
        task_gateway_base("http://unused", run.run_tag, affinity_prefix="task")
        for run in runs
    ]
    assert endpoints[0] != endpoints[1]
    budget = GenerationBudget(1)
    row = {"task": first.affinity_task}
    assert budget.admit(row, True) is None
    budget.finish(row, SimpleNamespace(record=lambda _: None), True)
    assert budget.admit({"task": first.affinity_task}, True) == "budget_rejected"
    assert budget.admit({"task": second.affinity_task}, True) is None
