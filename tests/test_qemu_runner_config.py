import importlib.util
import json
import os
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[1]


def load_run_batch():
    path = ROOT / "integrations/osworld-v2/run_batch.py"
    spec = importlib.util.spec_from_file_location("run_batch", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_v1_batch_selects_qemu_runner_and_domain(tmp_path):
    module = load_run_batch()
    command = module.command_for(
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
    assert "--benchmark-version" in command
    assert command[command.index("--benchmark-version") + 1] == "v1"
    assert command[command.index("--task-domain") + 1] == "libreoffice_calc"
    assert command[command.index("--client-password") + 1] == "password"


def test_v2_batch_defaults_to_v2(tmp_path):
    module = load_run_batch()
    command = module.command_for({}, {"id": "001"}, tmp_path)
    assert command[command.index("--benchmark-version") + 1] == "v2"
    assert "--task-domain" not in command


def test_batch_expands_environment_backed_paths(tmp_path, monkeypatch):
    module = load_run_batch()
    monkeypatch.setenv("TEST_GUEST_IMAGE", "/images/v2.qcow2")
    command = module.command_for(
        {
            "guest_image": "${TEST_GUEST_IMAGE}",
            "client_password": "osworld-public-evaluation",
            "judge_timeout": 600,
            "judge_tool_limit": 30,
            "judge_retry_tool_limit": 15,
        },
        {"id": "001"},
        tmp_path,
    )
    assert command[command.index("--guest-image") + 1] == "/images/v2.qcow2"
    assert (
        command[command.index("--client-password") + 1]
        == "osworld-public-evaluation"
    )
    assert command[command.index("--judge-timeout") + 1] == "600"
    assert command[command.index("--judge-tool-limit") + 1] == "30"
    assert command[command.index("--judge-retry-tool-limit") + 1] == "15"


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


def test_qemu_runner_help_imports_with_pinned_osworld_checkout():
    osworld_root = Path(
        os.environ.get(
            "OSWORLD_V2_ROOT",
            "/datadrive2/repos/osw-hybrid/OSWorld-V2",
        )
    )
    python = osworld_root / ".venv/bin/python"
    if not python.is_file():
        return
    result = subprocess.run(
        [
            str(python),
            str(ROOT / "integrations/osworld-v2/hybrid/run_hybrid.py"),
            "--help",
        ],
        env={**os.environ, "OSWORLD_V2_ROOT": str(osworld_root)},
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0, result.stderr
    assert "--benchmark-version {v1,v2}" in result.stdout


def test_setup_canary_help_supports_v1_task_resolution():
    osworld_root = Path(
        os.environ.get(
            "OSWORLD_V2_ROOT",
            "/datadrive2/repos/osw-hybrid/OSWorld-V2",
        )
    )
    python = osworld_root / ".venv/bin/python"
    if not python.is_file():
        return
    result = subprocess.run(
        [
            str(python),
            str(
                ROOT
                / "integrations/osworld-v2/hybrid/run_setup_canary.py"
            ),
            "--help",
        ],
        env={**os.environ, "OSWORLD_V2_ROOT": str(osworld_root)},
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0, result.stderr
    assert "--benchmark-version {v1,v2}" in result.stdout
    assert "--task-domain TASK_DOMAIN" in result.stdout


def test_qemu_runner_uses_selected_benchmark_in_gateway_affinity():
    source = (
        ROOT / "integrations/osworld-v2/hybrid/run_hybrid.py"
    ).read_text(encoding="utf-8")
    assert source.count(
        'f"osworld-{args.benchmark_version}-hybrid"'
    ) >= 2
