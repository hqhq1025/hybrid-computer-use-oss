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
        "run_id": "status-test",
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
    monkeypatch.setattr(queue_module, "results_root", lambda: tmp_path / "runs")
    monkeypatch.setattr(runner_type, "owns_process", lambda self, task, pid: True)
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
    text = (runner.base / "QUEUE_STATUS.md").read_text()
    command = runner.command(config["tasks"][0])
    generation = command[command.index("--benchmark-version") + 1]
    assert text.startswith(f"# Rolling OSWorld {generation.upper()} Queue Status\n")
    assert generation == (version or "v2")
    assert "| scored | failed \\| recovery | completed RAW=0.5000 |" in text
    assert "| evaluator-error | normal | completed ERR=evaluator |" in text
    assert "| active | ERROR analysis | external-running |" in text
    assert "Observed RAW mean (not final): 0.2500 across 2 scores" in text
    assert "Final effective score: unavailable" in text
    assert "Completed tasks: 2/3" in text
    assert "Active task runs: 1/1" in text
    state = json.loads((runner.base / "QUEUE_STATE.json").read_text())
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


@pytest.fixture
def make_queue(queue_module, monkeypatch, tmp_path):
    original_loader = queue_module.QueueRunner.load_child_environment
    original_adopter = queue_module.QueueRunner.adopt_live_attempts
    monkeypatch.setattr(queue_module, "results_root", lambda: tmp_path / "runs")
    monkeypatch.setattr(queue_module.QueueRunner, "load_child_environment", lambda self: {})
    monkeypatch.setattr(queue_module.QueueRunner, "adopt_live_attempts", lambda self: None)

    def make(name="queue.json", **overrides):
        config = {
            "run_id": "first-run", "model": "test-model", "effort": "high",
            "max_model_calls": 0, "timeout_seconds": 1, "max_active_tasks": 1,
            "tasks": ["001"],
        }
        config.update(overrides)
        path = tmp_path / name
        path.write_text(json.dumps(config))
        return queue_module.QueueRunner(path, 1)
    make.original_loader = original_loader
    make.original_adopter = original_adopter
    return make


def test_runs_in_same_config_directory_do_not_share_results_or_state(make_queue):
    first = make_queue("a.json")
    first.root_path("001").mkdir()
    first.result_path("001").write_text("1")
    first.failures["001"] = "old failure"
    first.render_status()
    second = make_queue("b.json", run_id="second-run", model="different-model")
    assert second.task_status("001") == "pending"
    assert second.failures == {}
    assert first.base != second.base
    assert first.logs != second.logs
    assert second.base.name == "second-run"
    assert second.base.parent.name == "osworld-v2"
    assert not (second.base / "QUEUE_STATE.json").exists()
    resumed = make_queue("moved-config.json")
    assert resumed.task_status("001") == "completed"
    assert resumed.base == first.base


@pytest.mark.parametrize("changes", [
    {"model": "other"}, {"agent": "claude-code"}, {"tasks": ["002"]},
    {"instructions_file": ""}, {"max_model_calls": 1, "token_log": "/tmp/log"},
])
def test_same_run_id_rejects_conflicting_configuration(make_queue, changes):
    first = make_queue()
    first.write_state()
    state = (first.base / "QUEUE_STATE.json").read_text()
    with pytest.raises(ValueError, match="Conflicting configuration"):
        make_queue("another.json", **changes)
    assert (first.base / "QUEUE_STATE.json").read_text() == state


@pytest.mark.parametrize("version,key", [
    ("v1", "OSWORLD_V1_ASSET_ROOT"), ("v2", "OSWORLD_FILE_BASE_URL"),
])
def test_asset_root_is_bound_on_queue_resume(
    make_queue, queue_module, monkeypatch, tmp_path, version, key,
):
    environment = {key: str(tmp_path / "assets-a")}
    monkeypatch.setattr(
        queue_module.QueueRunner, "load_child_environment", lambda self: environment.copy(),
    )
    runner = make_queue(benchmark_version=version)
    owner = json.loads((runner.base / "QUEUE_OWNER.json").read_text())
    assert owner["environment"][key] == environment[key]
    assert make_queue(benchmark_version=version).base == runner.base
    environment[key] = str(tmp_path / "assets-b")
    with pytest.raises(ValueError, match="Conflicting configuration"):
        make_queue(benchmark_version=version)


@pytest.mark.parametrize("run_id", [None, "", "..", "../escape", "a/b", "a\\b"])
def test_queue_requires_safe_explicit_run_id(make_queue, run_id):
    with pytest.raises(ValueError, match="run_id"):
        make_queue(run_id=run_id)


def test_unowned_existing_run_is_not_admitted(make_queue):
    runner = make_queue()
    (runner.base / "QUEUE_OWNER.json").unlink()
    with pytest.raises(ValueError, match="no valid queue owner"):
        make_queue()


def test_env_image_preflight_and_child_share_values(
    make_queue, queue_module, monkeypatch, tmp_path
):
    env = {
        "IMAGE": str(tmp_path / "image.qcow2"),
        "TOKENS": str(tmp_path / "tokens.jsonl"),
        "MODEL": "expanded-model",
    }
    monkeypatch.setenv("IMAGE", "/wrong-parent-image")
    monkeypatch.setattr(queue_module.QueueRunner, "load_child_environment", lambda self: env.copy())
    calls = []
    monkeypatch.setattr(
        queue_module, "validate_guest_image_capacity",
        lambda path, size: calls.append((path, size)) or 100,
    )
    runner = make_queue(
        guest_image="${IMAGE}", token_log="$TOKENS", model="$MODEL",
        max_model_calls=10, volume_size=64,
    )
    assert calls == [(env["IMAGE"], 64)]
    command = runner.command(runner.config["tasks"][0])
    assert command[command.index("--guest-image") + 1] == env["IMAGE"]
    assert command[command.index("--token-log") + 1] == env["TOKENS"]
    assert command[command.index("--model") + 1] == env["MODEL"]
    assert Path(env["TOKENS"]).is_file()
    assert runner.guest_image_capacity["path"] == env["IMAGE"]
    # Parent-environment changes after admission cannot change child arguments.
    monkeypatch.setenv("IMAGE", "/another-parent-image")
    assert runner.command(runner.config["tasks"][0]) == command
    env["IMAGE"] = str(tmp_path / "replacement.qcow2")
    with pytest.raises(ValueError, match="Conflicting configuration"):
        make_queue(
            guest_image="${IMAGE}", token_log="$TOKENS", model="$MODEL",
            max_model_calls=10, volume_size=64,
        )


def test_missing_env_is_rejected_before_preflight(make_queue, queue_module, monkeypatch):
    monkeypatch.setattr(
        queue_module, "validate_guest_image_capacity",
        lambda *args: pytest.fail("preflight must not receive unresolved variables"),
    )
    with pytest.raises(ValueError, match="Missing configuration environment variable"):
        make_queue(guest_image="${MISSING_IMAGE}")


def test_saved_pid_cannot_adopt_an_unrelated_live_process(
    make_queue, queue_module, monkeypatch
):
    runner = make_queue()
    runner.root_path("001").mkdir()
    runner.write_state()
    path = runner.base / "QUEUE_STATE.json"
    state = json.loads(path.read_text())
    state["children"] = {"001": os.getpid()}
    path.write_text(json.dumps(state))
    resumed = make_queue()
    assert resumed.adopted_pids == {}
    assert resumed.task_status("001") == "failed"
    expected = resumed.command(resumed.config["tasks"][0])
    monkeypatch.setattr(queue_module.QueueRunner, "process_command", staticmethod(lambda pid: expected))
    resumed = make_queue()
    assert resumed.adopted_pids == {"001": os.getpid()}
    assert resumed.task_status("001") == "external-running"
    monkeypatch.setattr(
        queue_module.QueueRunner, "process_command",
        staticmethod(lambda pid: expected + ["--result-dir", "/different-run"]),
    )
    assert resumed.task_status("001") == "failed"


def test_state_identity_is_checked_before_recovery(make_queue):
    runner = make_queue()
    runner.write_state()
    path = runner.base / "QUEUE_STATE.json"
    state = json.loads(path.read_text())
    state["run_id"] = "another-run"
    path.write_text(json.dumps(state))
    with pytest.raises(ValueError, match="state run_id"):
        make_queue()


def test_retry_archive_stays_in_own_run(make_queue):
    first = make_queue()
    first.root_path("001").mkdir()
    (first.logs / "001.log").write_text("interrupted")
    first.retryable_interruptions["001"] = {"timestamp_utc": "2000-01-01"}
    first.write_state()
    other = make_queue("other.json", run_id="other")
    assert not (other.base / "interrupted_attempts").exists()
    resumed = make_queue()
    assert resumed.task_status("001") == "pending"
    archive = resumed.base / "interrupted_attempts" / "2000-01-01" / "001"
    assert (archive / "runner.log").read_text() == "interrupted"
    assert (archive / "task").is_dir()


@pytest.mark.parametrize("tasks", [["../escape"], ["/absolute"], ["a\\b"], ["001", "001"]])
def test_queue_rejects_unsafe_or_duplicate_tasks(make_queue, tasks):
    with pytest.raises(ValueError, match="task"):
        make_queue(tasks=tasks)


def test_environment_file_is_loaded_before_expansion_and_bound_on_resume(
    make_queue, queue_module, monkeypatch, tmp_path
):
    monkeypatch.setattr(
        queue_module.QueueRunner, "load_child_environment", make_queue.original_loader
    )
    monkeypatch.setattr(queue_module, "DEDICATED_DOCKER_HOST", "tcp://offline-test")
    env_file = tmp_path / "queue.env"
    monkeypatch.setenv("QUEUE_ENV", str(env_file))
    env_file.write_text(
        'export DOCKER_HOST=tcp://offline-test\n'
        f'export QUEUE_IMAGE="{tmp_path}/test.qcow2"\n'
        f'export HCU_RESULTS_DIR="{tmp_path}/env-results"\n'
        'export HCU_OPENAI_GATEWAY=http://first.example/v1\n'
        'export PRIVATE_API_KEY=do-not-persist\n'
    )
    seen = []
    monkeypatch.setattr(
        queue_module, "validate_guest_image_capacity",
        lambda path, size: seen.append(path) or 100,
    )
    runner = make_queue(environment_file="${QUEUE_ENV}", guest_image="$QUEUE_IMAGE")
    assert seen == [str(tmp_path / "test.qcow2")]
    assert runner.base == tmp_path / "env-results" / "osworld-v2" / "first-run"
    owner_text = (runner.base / "QUEUE_OWNER.json").read_text()
    owner = json.loads(owner_text)
    assert owner["environment"]["HCU_OPENAI_GATEWAY"] == "http://first.example/v1"
    assert "PRIVATE_API_KEY" not in owner_text
    assert "do-not-persist" not in owner_text
    env_file.write_text(env_file.read_text().replace("first.example", "second.example"))
    with pytest.raises(ValueError, match="Conflicting configuration"):
        make_queue(environment_file="${QUEUE_ENV}", guest_image="$QUEUE_IMAGE")


@pytest.mark.parametrize("key", [
    "OSWORLD_FILE_BASE_URL", "HCU_ARTIFACT_DIR", "HCU_OPENAI_GATEWAY",
    "HCU_ANTHROPIC_GATEWAY", "HCU_GEMINI_GATEWAY",
])
def test_effective_environment_changes_reject_reuse(
    make_queue, queue_module, monkeypatch, key
):
    env = {key: "/first"}
    monkeypatch.setattr(queue_module.QueueRunner, "load_child_environment", lambda self: env.copy())
    make_queue()
    env[key] = "/changed"
    with pytest.raises(ValueError, match="Conflicting configuration"):
        make_queue()


def test_adoption_requires_exact_command_not_substring(
    make_queue, queue_module, monkeypatch
):
    runner = make_queue()
    expected = runner.command(runner.config["tasks"][0])
    monkeypatch.setattr(Path, "iterdir", lambda self: iter([Path("/proc/123"), Path("/proc/456")]))
    monkeypatch.setattr(
        queue_module.QueueRunner, "process_command",
        staticmethod(lambda pid: expected if pid == 123 else expected + ["extra"]),
    )
    make_queue.original_adopter(runner)
    assert runner.adopted_pids == {"001": 123}


def test_queue_rejects_existing_batch_output(make_queue, tmp_path):
    root = tmp_path / "runs" / "osworld-v2" / "first-run"
    (root / "001").mkdir(parents=True)
    (root / "001" / "result.txt").write_text("1")
    with pytest.raises(ValueError, match="no valid queue owner"):
        make_queue()
    assert (root / "001" / "result.txt").read_text() == "1"


def test_resolved_values_are_not_expanded_twice(
    make_queue, queue_module, monkeypatch, tmp_path
):
    literal_path = str(tmp_path / "$LITERAL" / "tokens.jsonl")
    env = {"TOKENS": literal_path}
    monkeypatch.setenv("LITERAL", "wrong")
    monkeypatch.setattr(queue_module.QueueRunner, "load_child_environment", lambda self: env.copy())
    runner = make_queue(token_log="$TOKENS", instructions_file="")
    command = runner.command(runner.config["tasks"][0])
    assert command[command.index("--token-log") + 1] == literal_path
    assert command[command.index("--instructions-file") + 1] == ""


def test_queue_owner_does_not_persist_guest_password(make_queue):
    runner = make_queue(client_password="private-guest-password")
    owner = (runner.base / "QUEUE_OWNER.json").read_text()
    assert "private-guest-password" not in owner
    assert "client_password" not in owner
    command = runner.command(runner.config["tasks"][0])
    assert command[command.index("--client-password") + 1] == "private-guest-password"


def test_status_uses_fixed_denominator_and_zeroes_truncation(make_queue):
    runner = make_queue(tasks=["partial", "budget", "missing"])
    for task_id, score, reason in (
        ("partial", 0.5, "completed"), ("budget", 1.0, "max_model_calls"),
    ):
        root = runner.root_path(task_id)
        root.mkdir()
        runner.result_path(task_id).write_text(str(score))
        payload = {
            "task_id": task_id, "score": score, "termination_reason": reason,
            "truncated": reason != "completed", "raw_evaluator_status": "completed",
        }
        (root / "result.json").write_text(json.dumps(payload))
        (root / "meta.json").write_text(json.dumps({
            **payload, "status": "completed" if reason == "completed" else "truncated",
        }))
    runner.render_status()
    text = (runner.base / "QUEUE_STATUS.md").read_text()
    assert "Observed RAW mean (not final): 0.7500 across 2 scores" in text
    assert "Effective lower bound: 0.1667 over all 3 selected tasks" in text
    assert "Final effective score: unavailable" in text
    assert "completed RAW=1.0000 [budget]" in text
    # Bad evidence must be displayed without stopping the scheduler.
    runner.result_path("partial").write_text("0.9")
    runner.render_status()
    text = (runner.base / "QUEUE_STATUS.md").read_text()
    assert "Aggregation unavailable: conflicting evidence" in text
