"""Configuration boundaries; no model calls or guest execution."""

import argparse
import importlib.util
from pathlib import Path
import sys
from types import ModuleType

import pytest

from hcu.osworld.agents import codex_config
from hcu.osworld.guest import GUEST_CODEX_HOME


def config(**kwargs):
    tomllib = pytest.importorskip("tomllib")
    return tomllib.loads(codex_config(
        model="selected-model",
        effort="xhigh",
        base_url="http://gateway/t/task-example/v1",
        codex_home=GUEST_CODEX_HOME,
        base_instructions="/guest/base.md",
        mcp_command="/guest/computer-use",
        **kwargs,
    ))


@pytest.mark.parametrize("channels", ["", "a11y,keyboard", "gui,keyboard"])
@pytest.mark.parametrize("instructions", [None, "", "Selected interaction guide"])
def test_guest_opt_in_changes_only_execution_permissions(channels, instructions):
    kwargs = dict(channels=channels, developer_instructions=instructions)
    standalone = config(**kwargs)
    assert standalone == config(**kwargs, execution_context="standalone")
    assert standalone.pop("approval_policy") == "on-request"
    assert standalone.pop("sandbox_mode") == "workspace-write"

    guest = config(**kwargs, execution_context="isolated-guest")
    assert guest.pop("approval_policy") == "never"
    assert guest.pop("sandbox_mode") == "danger-full-access"
    assert guest == standalone
    assert guest["features"]["shell_tool"] is True
    assert guest["features"]["unified_exec"] is True
    assert guest["mcp_servers"]["computer_use"]["required"] is True


@pytest.mark.parametrize("context", ["", "guest", "isolated_guest", None, True])
def test_unknown_execution_context_is_rejected(context):
    with pytest.raises(ValueError, match="execution_context"):
        config(execution_context=context)


@pytest.mark.parametrize("version", ["v1", "v2"])
def test_runner_stages_unrestricted_guest_config(tmp_path, monkeypatch, version):
    tomllib = pytest.importorskip("tomllib")
    root = Path(__file__).resolve().parents[1]
    # Import the real staging path without loading the VM or task assets.
    dependencies = {
        "hcu.osworld.task_loader": {"load_task_config": None},
        "desktop_env.cache": {"run_scoped_cache_dir": None},
        "desktop_env.desktop_env": {"DesktopEnv": None},
    }
    for name, attributes in dependencies.items():
        dependency = ModuleType(name)
        vars(dependency).update(attributes)
        monkeypatch.setitem(sys.modules, name, dependency)
    spec = importlib.util.spec_from_file_location(
        "agents_config_runner_test", root / "hcu/osworld/run_task.py",
    )
    runner = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, runner)
    spec.loader.exec_module(runner)
    args = argparse.Namespace(
        agent="codex", model="selected-model", effort="xhigh",
        gateway="http://gateway:8600",
        benchmark_version=version, channels="",
    )
    run = runner.TaskRun(
        args=args, run_tag="config-test", result_dir=tmp_path,
        token_log=tmp_path / "tokens.jsonl", affinity_task="task-config-test",
        agent_binary=tmp_path / "codex", node_binary=None,
        mcp_binary=tmp_path / "computer-use",
        instructions_file=root / "instructions/hybrid.md",
        base_instructions_file=root / "instructions/base.md",
        task={"instruction": "Do the task."}, phases=[], instruction="Do the task.",
        current_date="2000-01-01", run_timezone="UTC",
        run_started_at_utc="2000-01-01T00:00:00+00:00",
        explicit_task_date=None, guest_image="",
    )
    runner.stage_local_files(run)
    staged = tomllib.loads(run.config_path.read_text())
    assert staged["approval_policy"] == "never"
    assert staged["sandbox_mode"] == "danger-full-access"
    assert "developer_instructions" not in staged
    assert run.instructions_copy.read_text() == run.instructions_file.read_text()
