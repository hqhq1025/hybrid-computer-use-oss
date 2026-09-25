import json
import os
from pathlib import Path
import subprocess

import pytest

from hcu.osworld.batch_config import runner_command, validate_config


ROOT = Path(__file__).resolve().parents[1]
OSWORLD_ROOT = Path(
    os.environ.get("OSWORLD_V2_ROOT", ROOT / "external" / "OSWorld-V2")
)


def require_osworld_checkout() -> Path:
    python = OSWORLD_ROOT / ".venv/bin/python"
    if not (OSWORLD_ROOT / "desktop_env").is_dir() or not python.is_file():
        pytest.skip(f"OSWorld-V2 checkout with .venv not found at {OSWORLD_ROOT}")
    return python


def test_v1_batch_selects_qemu_runner_and_domain(tmp_path):
    command = runner_command(
        {
            "benchmark_version": "v1",
            "agent": "codex",
            "model": "gpt-5.4",
            "effort": "xhigh",
            "client_password": "password",
        },
        {
            "id": "task-uuid",
            "task_domain": "libreoffice_calc",
        },
        tmp_path,
    )
    assert command[1:3] == ["-m", "hcu.osworld.run_task"]
    assert command[command.index("--benchmark-version") + 1] == "v1"
    assert command[command.index("--task-domain") + 1] == "libreoffice_calc"
    assert command[command.index("--client-password") + 1] == "password"


def test_v2_batch_defaults_to_v2(tmp_path):
    command = runner_command({}, {"id": "001"}, tmp_path)
    assert command[command.index("--benchmark-version") + 1] == "v2"
    assert "--task-domain" not in command


def test_batch_expands_environment_backed_paths(tmp_path, monkeypatch):
    monkeypatch.setenv("TEST_GUEST_IMAGE", "/images/v2.qcow2")
    command = runner_command(
        {
            "guest_image": "${TEST_GUEST_IMAGE}",
            "client_password": "osworld-public-evaluation",
            "max_model_calls": 200,
        },
        {"id": "001"},
        tmp_path,
    )
    assert command[command.index("--guest-image") + 1] == "/images/v2.qcow2"
    assert (
        command[command.index("--client-password") + 1]
        == "osworld-public-evaluation"
    )
    assert command[command.index("--max-model-calls") + 1] == "200"


def test_example_queues_bind_guest_password_and_capacity():
    v1 = json.loads(
        (ROOT / "configs/osworld-v1-queue.example.json").read_text(
            encoding="utf-8"
        )
    )
    v2 = json.loads(
        (ROOT / "configs/osworld-v2-queue.example.json").read_text(
            encoding="utf-8"
        )
    )
    assert v1["client_password"] == "password"
    assert {task["volume_size"] for task in v1["tasks"]} == {64}
    assert v2["client_password"] == "osworld-public-evaluation"
    assert {task["volume_size"] for task in v2["tasks"]} == {100}
    validate_config(v1)
    validate_config(v2)


def test_config_rejects_unknown_keys():
    with pytest.raises(ValueError, match="hybrid_skill_file"):
        validate_config({"tasks": ["001"], "hybrid_skill_file": "/s.md"})
    with pytest.raises(ValueError, match="flavor"):
        validate_config({"tasks": [{"id": "001", "flavor": "x"}]})


def test_task_level_resources_override_config_defaults(tmp_path):
    command = runner_command(
        {"volume_size": 64, "ram_size": "8G", "task_domain": "chrome"},
        {"id": "a", "volume_size": 100, "task_domain": "gimp"},
        tmp_path / "a",
    )
    assert command[command.index("--volume-size") + 1] == "100"
    assert command[command.index("--ram-size") + 1] == "8G"
    assert command[command.index("--task-domain") + 1] == "gimp"
    assert command[command.index("--result-dir") + 1] == str(tmp_path / "a")


@pytest.mark.parametrize(
    ("module", "expected"),
    [
        ("hcu.osworld.run_task", ["--benchmark-version {v1,v2}"]),
        (
            "hcu.osworld.setup_canary",
            ["--benchmark-version {v1,v2}", "--task-domain TASK_DOMAIN"],
        ),
    ],
)
def test_entrypoint_help_imports_with_osworld_checkout(module, expected):
    python = require_osworld_checkout()
    result = subprocess.run(
        [str(python), "-m", module, "--help"],
        cwd=ROOT,
        env={**os.environ, "OSWORLD_V2_ROOT": str(OSWORLD_ROOT)},
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, result.stderr
    for text in expected:
        assert text in result.stdout


GATEWAY_AFFINITY_PROBE = """
import argparse, json, sys
from pathlib import Path
from hcu.osworld import run_task

root, out = Path(sys.argv[1]), Path(sys.argv[2])
args = argparse.Namespace(
    agent="codex", model="gpt-5.5", effort="xhigh",
    gateway_base="http://gateway:8600",
    claude_gateway_base="http://gateway:8537",
    benchmark_version="v1",
)
run = run_task.TaskRun(
    args=args, run_tag="RUN", result_dir=out,
    token_log=out / "tokens.jsonl", affinity_task="task-RUN",
    codex_binary=out / "codex", claude_code_binary=out / "claude",
    mcp_binary=out / "computer-use",
    skill_file=root / "skills/hybrid/SKILL.md",
    base_instructions_file=root / "skills/base/CODEX_BASE_INSTRUCTIONS.md",
    task={"instruction": "Do the task."}, phases=[],
    instruction="Do the task.", current_date="2026-01-01",
    run_timezone="UTC", run_started_at_utc="2026-01-01T00:00:00+00:00",
    explicit_task_date=None, guest_image="",
)
run_task.stage_local_files(run)
print(json.dumps({"claude_endpoint": run.claude_endpoint}))
"""


def test_gateway_affinity_uses_the_neutral_task_prefix(tmp_path):
    python = require_osworld_checkout()
    result = subprocess.run(
        [str(python), "-c", GATEWAY_AFFINITY_PROBE, str(ROOT), str(tmp_path)],
        cwd=ROOT,
        env={**os.environ, "OSWORLD_V2_ROOT": str(OSWORLD_ROOT)},
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, result.stderr
    config = (tmp_path / "guest-config.toml").read_text(encoding="utf-8")
    assert 'base_url = "http://gateway:8600/t/task-RUN/v1"' in config
    assert json.loads(result.stdout)["claude_endpoint"] == (
        "http://gateway:8537/t/task-RUN"
    )
