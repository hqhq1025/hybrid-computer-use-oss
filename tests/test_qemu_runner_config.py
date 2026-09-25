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


# The cross-model comparison runs four harnesses on V1; the V2 main
# experiment runs Codex and Claude Code.
EXAMPLE_AGENTS = {
    "v1": ("codex", "claude-code", "gemini-cli", "grok-build"),
    "v2": ("codex", "claude-code"),
}
V1_PASSWORD = "password"
V1_VOLUME_SIZE = 64
V2_PASSWORD = "osworld-public-evaluation"
V2_VOLUME_SIZE = 100


def example_configs():
    """Every example queue, keyed by its benchmark version and agent."""
    found = {}
    for path in sorted((ROOT / "configs").glob("osworld-*-queue.example.json")):
        config = json.loads(path.read_text(encoding="utf-8"))
        found.setdefault(config["benchmark_version"], {})[config["agent"]] = config
    return found


def test_example_queues_cover_the_harnesses_and_bind_their_guests():
    configs = example_configs()
    assert set(configs) == set(EXAMPLE_AGENTS)
    for version, agents in EXAMPLE_AGENTS.items():
        assert tuple(sorted(configs[version])) == tuple(sorted(agents))
        for agent, config in configs[version].items():
            validate_config(config)
            assert config["benchmark_version"] == version
            assert config["agent"] == agent
            assert config["run_id"] == f"example-osworld-{version}-{agent}"
    for config in configs["v1"].values():
        assert config["client_password"] == V1_PASSWORD
        assert {task["volume_size"] for task in config["tasks"]} == {V1_VOLUME_SIZE}
    for config in configs["v2"].values():
        assert config["client_password"] == V2_PASSWORD
        assert {task["volume_size"] for task in config["tasks"]} == {V2_VOLUME_SIZE}


def test_example_queues_point_each_agent_at_its_own_gateway():
    from hcu.osworld.agents import AGENT_PROFILES

    for version, configs in example_configs().items():
        for agent, config in configs.items():
            gateway = AGENT_PROFILES[agent]["gateway"]
            key = {
                "openai": "gateway_base",
                "anthropic": "claude_gateway_base",
                "gemini": "gemini_gateway_base",
            }[gateway]
            assert config[key].startswith("${HCU_")
            assert config["token_log"].endswith(
                f"{gateway}-tokens.jsonl"
            )


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


STAGING_PROBE = """
import argparse, json, sys
from pathlib import Path
from hcu.osworld import run_task

root, out, agent = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
profile = run_task.AGENT_PROFILES[agent]
args = argparse.Namespace(
    agent=agent, model=profile["default_model"],
    effort=profile["default_effort"],
    gateway_base="http://gateway:8600",
    claude_gateway_base="http://gateway:8537",
    gemini_gateway_base="http://gateway:8571",
    benchmark_version="v1", channels="",
)
run = run_task.TaskRun(
    args=args, run_tag="RUN", result_dir=out,
    token_log=out / "tokens.jsonl", affinity_task="task-RUN",
    agent_binary=out / profile["artifact"],
    node_binary=out / "node" if agent == "gemini-cli" else None,
    mcp_binary=out / "computer-use",
    skill_file=root / "skills/hybrid/SKILL.md",
    base_instructions_file=root / "skills/base/CODEX_BASE_INSTRUCTIONS.md",
    task={"instruction": "Do the task."}, phases=[],
    instruction="Do the task.", current_date="2026-01-01",
    run_timezone="UTC", run_started_at_utc="2026-01-01T00:00:00+00:00",
    explicit_task_date=None, guest_image="",
)
out.mkdir(parents=True, exist_ok=True)
run_task.stage_local_files(run)
uploads, executables = run_task.runtime_uploads(run)
print(json.dumps({
    "endpoint": run.agent_endpoint,
    "uploads": {row["path"]: Path(row["local_path"]).name for row in uploads},
    "executables": executables,
    "system_prompt": run.base_instructions_copy.read_text(),
    "launcher": run.launcher_path.read_text(),
}))
"""


def stage(tmp_path, agent):
    python = require_osworld_checkout()
    result = subprocess.run(
        [str(python), "-c", STAGING_PROBE, str(ROOT), str(tmp_path), agent],
        cwd=ROOT,
        env={**os.environ, "OSWORLD_V2_ROOT": str(OSWORLD_ROOT)},
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert result.returncode == 0, result.stderr
    return json.loads(result.stdout)


def test_each_agent_is_bound_to_its_gateway_with_the_task_prefix(tmp_path):
    expected = {
        "codex": "http://gateway:8600/t/task-RUN",
        "claude-code": "http://gateway:8537/t/task-RUN",
        "gemini-cli": "http://gateway:8571/t/task-RUN",
        "grok-build": "http://gateway:8600/t/task-RUN",
    }
    staged = {agent: stage(tmp_path / agent, agent) for agent in expected}
    for agent, endpoint in expected.items():
        assert staged[agent]["endpoint"] == endpoint
    for agent in ("codex", "grok-build"):
        config = (tmp_path / agent / "guest-config.toml").read_text(encoding="utf-8")
        assert 'base_url = "http://gateway:8600/t/task-RUN/v1"' in config
    assert (
        "ANTHROPIC_BASE_URL=http://gateway:8537/t/task-RUN"
        in staged["claude-code"]["launcher"]
    )
    assert (
        "GOOGLE_GEMINI_BASE_URL=http://gateway:8571/t/task-RUN"
        in staged["gemini-cli"]["launcher"]
    )


def test_each_agent_receives_the_skill_through_its_own_channel(tmp_path):
    skill_head = (ROOT / "skills/hybrid/SKILL.md").read_text().splitlines()[0]
    base_head = (
        ROOT / "skills/base/CODEX_BASE_INSTRUCTIONS.md"
    ).read_text().splitlines()[0]
    staged = {agent: stage(tmp_path / agent, agent) for agent in (
        "codex", "claude-code", "gemini-cli", "grok-build"
    )}
    # Codex and Gemini CLI read the skill from a context file.
    assert staged["codex"]["uploads"]["/home/user/AGENTS.md"] == "AGENTS.md"
    assert staged["gemini-cli"]["uploads"][
        "/opt/agent-runtime/gemini/.gemini/GEMINI.md"
    ] == "AGENTS.md"
    for agent in ("codex", "gemini-cli"):
        assert skill_head not in staged[agent]["system_prompt"]
    # Claude Code and Grok Build get it after the base instructions.
    for agent in ("claude-code", "grok-build"):
        prompt = staged[agent]["system_prompt"]
        assert prompt.startswith(base_head)
        assert skill_head in prompt
        assert "/home/user/AGENTS.md" not in staged[agent]["uploads"]


def test_gemini_ships_its_own_node_and_unpacks_the_cli_in_the_guest(tmp_path):
    staged = stage(tmp_path, "gemini-cli")
    uploads = staged["uploads"]
    assert uploads["/tmp/agent-task/gemini-cli.tgz"] == "gemini-cli.tgz"
    assert uploads["/home/user/.local/bin/gemini-node"] == "node"
    assert "/tmp/agent-task/gemini-cli.tgz" not in staged["executables"]
    assert "/home/user/.local/bin/gemini-node" in staged["executables"]
    experiments = json.loads(
        (tmp_path / "guest-experiments.json").read_text(encoding="utf-8")
    )
    assert experiments["flags"][0]["intValue"] == "600"
    settings = json.loads((tmp_path / "guest-settings.json").read_text())
    assert settings["modelConfigs"]["customOverrides"][0]["modelConfig"] == {
        "generateContentConfig": {"thinkingConfig": {"thinkingLevel": "HIGH"}}
    }


def test_channel_ablation_exposes_one_tool_to_each_agent():
    from hcu.osworld.agents import claude_mcp_config, codex_config, public_tools

    assert public_tools("") == ("AX", "GUI")
    assert public_tools("a11y,keyboard") == ("AX",)
    assert public_tools("gui,keyboard") == ("GUI",)
    with pytest.raises(ValueError):
        public_tools("a11y")

    codex = codex_config(
        model="m", effort="xhigh", base_url="http://g/v1", codex_home="/h",
        base_instructions="/b.md", mcp_command="/computer-use",
        channels="a11y,keyboard",
    )
    assert "[mcp_servers.computer_use.env]" in codex
    assert 'COMPUTER_USE_CHANNELS = "a11y,keyboard"' in codex
    assert "computer_use.env" not in codex_config(
        model="m", effort="xhigh", base_url="http://g/v1", codex_home="/h",
        base_instructions="/b.md", mcp_command="/computer-use",
    )

    claude = json.loads(claude_mcp_config(channels="gui,keyboard"))
    assert claude["mcpServers"]["computer_use"]["env"] == {
        "COMPUTER_USE_CHANNELS": "gui,keyboard"
    }
    assert "env" not in json.loads(claude_mcp_config())["mcpServers"]["computer_use"]


def test_batch_forwards_channels_and_an_empty_skill(tmp_path):
    command = runner_command(
        {"channels": "gui,keyboard", "no_skill": True}, {"id": "001"}, tmp_path
    )
    assert command[command.index("--channels") + 1] == "gui,keyboard"
    assert command[command.index("--skill-file") + 1] == ""


def test_gemini_and_grok_expose_only_the_requested_channel():
    from hcu.osworld.agents import gemini_settings, grok_config

    settings = json.loads(gemini_settings(
        model="gemini-3.8-flash", effort="native", channels="a11y,keyboard"
    ))
    assert settings["mcpServers"]["computer_use"]["env"] == {
        "COMPUTER_USE_CHANNELS": "a11y,keyboard"
    }
    assert "customOverrides" not in settings["modelConfigs"]
    assert "google_web_search" in settings["tools"]["exclude"]
    with pytest.raises(ValueError):
        gemini_settings(model="gemini-3.8-flash", effort="xhigh")

    grok = grok_config(
        model="grok-4.7", base_url="http://g/t/task-x/v1", effort="xhigh",
        channels="gui,keyboard",
    )
    assert 'COMPUTER_USE_CHANNELS = "gui,keyboard"' in grok
    assert "computer_use.env" not in grok_config(
        model="grok-4.7", base_url="http://g/t/task-x/v1", effort="xhigh"
    )


def test_batch_leaves_model_and_effort_defaults_to_the_agent(tmp_path):
    command = runner_command({"agent": "gemini-cli"}, {"id": "001"}, tmp_path)
    assert command[command.index("--effort") + 1] == ""
    assert command[command.index("--model") + 1] == ""
