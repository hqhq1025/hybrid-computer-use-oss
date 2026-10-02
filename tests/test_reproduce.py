import json
from pathlib import Path
import subprocess
import sys

import pytest

from hcu.osworld.batch_config import resolve_config, runner_command
from hcu.osworld.reproduce import prepare, read_selection


ROOT = Path(__file__).resolve().parents[1]


def example():
    return json.loads((ROOT / "configs/osworld-v1-codex-queue.example.json").read_text())


def create(tmp_path, **kwargs):
    values = dict(
        config=example(), output=tmp_path / "experiment", run_id="test-current",
        protocol_id="official-v1", protocol_revision="current",
    )
    values.update(kwargs)
    return prepare(**values)


def test_prepare_snapshots_current_instructions_and_exact_selection(tmp_path):
    manifest = create(
        tmp_path, tasks=[{"id": "a"}, {"id": "b"}], excluded=[{"id": "b"}]
    )
    assert manifest["task_ids"] == ["a"]
    assert manifest["excluded_tasks"] == [{"id": "b"}]
    assert manifest["evidence_status"] == "not_run"
    assert manifest["paper_reference"] is None
    output = tmp_path / "experiment"
    assert (output / "instructions/profile.md").read_text() == (
        ROOT / "instructions/hybrid.md"
    ).read_text()
    config = json.loads((output / "config.json").read_text())
    assert config["volume_size"] == 64
    assert config["timeout_seconds"] == 1800
    resolved = resolve_config(config, {
        "HCU_EXPERIMENT_DIR": str(output), "HCU_RESULTS_DIR": str(tmp_path / "runs"),
        "HCU_OPENAI_GATEWAY": "http://example.invalid",
        "OSWORLD_V1_GUEST_IMAGE": "/images/v1.qcow2", "HCU_ARTIFACT_DIR": "/artifacts",
    })
    command = runner_command(resolved, resolved["tasks"][0], tmp_path / "results", resolved=True)
    assert str(output / "instructions/profile.md") in command
    assert "--max-model-calls" in command
    assert "test-current" in resolved["token_log"]


def test_prepared_output_cannot_be_overwritten(tmp_path):
    create(tmp_path)
    with pytest.raises(FileExistsError):
        create(tmp_path)


@pytest.mark.parametrize("kwargs", [
    {"run_id": "../other"},
    {"tasks": [{"id": "../other"}]},
    {"tasks": [{"id": "a"}, {"id": "a"}]},
    {"tasks": [{"id": "a"}], "excluded": [{"id": "b"}]},
    {"tasks": [{"id": "a"}], "excluded": [{"id": "a"}]},
    {"timeout_seconds": 0},
    {"protocol_revision": " "},
])
def test_invalid_plan_has_no_side_effects(tmp_path, kwargs):
    with pytest.raises(ValueError):
        create(tmp_path, **kwargs)
    assert not (tmp_path / "experiment").exists()


@pytest.mark.parametrize("null_profile", [False, True])
def test_default_profile_preserves_runner_semantics(tmp_path, null_profile):
    config = example()
    config.pop("instructions_file")
    if null_profile:
        config["instructions_file"] = None
    original = runner_command(config, config["tasks"][0], tmp_path / "results")
    assert "--instructions-file" not in original

    manifest = create(tmp_path, config=config)
    output = tmp_path / "experiment"
    prepared = resolve_config(json.loads((output / "config.json").read_text()), {
        "HCU_EXPERIMENT_DIR": str(output), "HCU_RESULTS_DIR": str(tmp_path / "runs"),
        "HCU_OPENAI_GATEWAY": "http://example.invalid",
        "OSWORLD_V1_GUEST_IMAGE": "/images/v1.qcow2", "HCU_ARTIFACT_DIR": "/artifacts",
    })
    command = runner_command(
        prepared, prepared["tasks"][0], tmp_path / "results", resolved=True,
    )
    profile = Path(command[command.index("--instructions-file") + 1])
    assert profile == output / manifest["instructions"]["profile"]
    assert profile.read_text() == (ROOT / "instructions/hybrid.md").read_text()


@pytest.mark.parametrize("overrides", [
    {"instructions_file": ""},
    {"no_instructions": True},
    {"no_instructions": True, "instructions_file": None},
    {"no_instructions": True, "instructions_file": "missing-profile.md"},
])
def test_disabled_profile_preserves_base(tmp_path, overrides):
    config = example()
    config.update(overrides)
    manifest = create(tmp_path, config=config)
    assert manifest["instructions"]["profile"] is None
    assert (tmp_path / "experiment/instructions/base.md").is_file()
    assert not (tmp_path / "experiment/instructions/profile.md").exists()
    prepared = json.loads((tmp_path / "experiment/config.json").read_text())
    assert prepared["instructions_file"] == ""
    command = runner_command(prepared, prepared["tasks"][0], tmp_path / "results")
    assert command[command.index("--instructions-file") + 1] == ""


def test_known_drive_tasks_must_be_explicitly_excluded(tmp_path):
    drive = read_selection(ROOT / "configs/osworld-v1-excluded-tasks.json")
    tasks = [{"id": "allowed"}, *drive]
    with pytest.raises(ValueError, match="Google Drive"):
        create(tmp_path, tasks=tasks)
    manifest = create(tmp_path, tasks=tasks, excluded=drive)
    assert manifest["task_ids"] == ["allowed"]
    assert len(manifest["excluded_tasks"]) == 8


def test_shipped_full_v1_selection_prepares_exact_non_drive_membership(tmp_path):
    tasks = read_selection(ROOT / "configs/osworld-v1-tasks.json")
    excluded = read_selection(ROOT / "configs/osworld-v1-excluded-tasks.json")
    manifest = create(tmp_path, tasks=tasks, excluded=excluded)
    expected = {task["id"] for task in tasks} - {task["id"] for task in excluded}
    assert manifest["task_count"] == 361
    assert set(manifest["task_ids"]) == expected
    config = json.loads((tmp_path / "experiment/config.json").read_text())
    assert all(task.get("task_domain") for task in config["tasks"])


def test_upstream_membership_and_cli(tmp_path):
    selection = tmp_path / "tasks.json"
    selection.write_text(json.dumps({"chrome": ["task-a", "task-b"]}))
    assert read_selection(selection) == [
        {"id": "task-a", "task_domain": "chrome"},
        {"id": "task-b", "task_domain": "chrome"},
    ]
    result = subprocess.run([
        sys.executable, "-m", "hcu.osworld.reproduce",
        "--config", str(ROOT / "configs/osworld-v1-codex-queue.example.json"),
        "--tasks", str(selection), "--run-id", "cli-test",
        "--protocol-id", "official-v1", "--protocol-revision", "current",
        "--output", str(tmp_path / "experiment"),
    ], cwd=ROOT, capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout)["task_count"] == 2
