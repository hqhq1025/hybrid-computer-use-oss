"""Configuration shared by the batch and queue coordinators.

One JSON file describes a set of tasks and the options every task run uses.
``run_batch`` and ``run_queue`` accept the same keys, so a configuration can
move between the simple batch runner and the rolling queue unchanged.
Unknown keys are rejected instead of silently ignored.
"""

from __future__ import annotations

from collections.abc import Mapping
import os
from pathlib import Path
import re
import sys

# Config key -> run_task flag for options that take a value. String values
# may reference environment variables such as ${HCU_ARTIFACT_DIR}.
RUNNER_VALUE_OPTIONS = (
    ("gateway_base", "--gateway-base"),
    ("claude_gateway_base", "--claude-gateway-base"),
    ("gemini_gateway_base", "--gemini-gateway-base"),
    ("guest_image", "--guest-image"),
    ("artifact_dir", "--artifact-dir"),
    ("agent_binary", "--agent-binary"),
    ("mcp_binary", "--mcp-binary"),
    ("instructions_file", "--instructions-file"),
    ("channels", "--channels"),
    ("base_instructions_file", "--base-instructions-file"),
    ("client_password", "--client-password"),
    ("token_log", "--token-log"),
)
RUNNER_SWITCHES = (
    ("retain_containers", "--retain-container"),
    ("enable_vnc", "--enable-vnc"),
    ("enable_recording", "--enable-recording"),
)
RUNNER_KEYS = frozenset(
    {key for key, _flag in RUNNER_VALUE_OPTIONS}
    | {key for key, _flag in RUNNER_SWITCHES}
    | {
        "no_instructions",
        "benchmark_version",
        "agent",
        "model",
        "effort",
        "volume_size",
        "ram_size",
        "max_model_calls",
        "timeout_seconds",
        "task_domain",
    }
)
# Scheduling options read only by run_queue.
QUEUE_KEYS = frozenset({
    "max_active_tasks",
    "max_total_live_tasks",
    "max_starting_tasks",
    "launch_stagger_seconds",
    "max_steps",
    "run_started_at_utc",
    "run_timezone",
    "environment_file",
    "required_environment",
    "rate_limit_circuit_window_seconds",
    "rate_limit_circuit_threshold",
    "rate_limit_circuit_pause_seconds",
    "external_pid_files",
})
CONFIG_KEYS = RUNNER_KEYS | QUEUE_KEYS | {"run_id", "tasks"}
TASK_KEYS = frozenset({"id", "volume_size", "ram_size", "task_domain", "stratum"})


def validate_config(config: dict) -> None:
    unknown = sorted(set(config) - CONFIG_KEYS)
    if unknown:
        raise ValueError("Unknown config keys: " + ", ".join(unknown))
    benchmark_version = str(config.get("benchmark_version") or "v2")
    if benchmark_version not in {"v1", "v2"}:
        raise ValueError("config.benchmark_version must be v1 or v2")
    for task in normalized_tasks(config):
        unknown = sorted(set(task) - TASK_KEYS)
        if unknown:
            raise ValueError(
                f"Unknown keys for task {task['id']}: " + ", ".join(unknown)
            )


def normalized_tasks(config: dict) -> list[dict]:
    tasks = []
    for entry in config.get("tasks") or []:
        task = {"id": entry} if isinstance(entry, str) else dict(entry)
        task_id = str(task.get("id") or "").strip()
        if not task_id:
            raise ValueError("every task must have a non-empty id")
        task["id"] = task_id
        tasks.append(task)
    if not tasks:
        raise ValueError("config.tasks must not be empty")
    return tasks


def resolve_config(config: dict, environment: Mapping[str, str]) -> dict:
    """Expand config variables once, against the environment the child receives."""
    variable = re.compile(r"\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))")

    def expand(value):
        if isinstance(value, str):
            def replace(match):
                name = match.group(1) or match.group(2)
                if name not in environment:
                    raise ValueError(f"Missing configuration environment variable: {name}")
                return environment[name]
            return variable.sub(replace, value)
        if isinstance(value, list):
            return [expand(item) for item in value]
        if isinstance(value, dict):
            return {key: expand(item) for key, item in value.items()}
        return value

    return expand(config)


def append_option(
    command: list[str], config: dict, key: str, flag: str, *, resolved: bool = False
) -> None:
    value = config.get(key)
    if value not in (None, ""):
        command.extend((flag, str(value) if resolved else os.path.expandvars(str(value))))


def runner_command(
    config: dict, task: dict, result_dir: Path, *, resolved: bool = False
) -> list[str]:
    """Build the run_task command line for one task of a configuration."""
    command = [
        sys.executable,
        "-m",
        "hcu.osworld.run_task",
        "--task-id",
        task["id"],
        "--benchmark-version",
        str(config.get("benchmark_version") or "v2"),
        "--agent",
        str(config.get("agent") or "codex"),
        "--model",
        str(config.get("model") or ""),
        "--effort",
        str(config.get("effort") or ""),
        "--volume-size",
        str(task.get("volume_size", config.get("volume_size", 40))),
        "--ram-size",
        str(task.get("ram_size", config.get("ram_size", "4G"))),
        "--max-model-calls",
        str(config.get("max_model_calls", 200)),
        "--timeout",
        str(config.get("timeout_seconds", 0)),
        "--result-dir",
        str(result_dir),
    ]
    for key, flag in RUNNER_VALUE_OPTIONS:
        if key == "instructions_file":
            # Preserve explicit empty profiles as well as the boolean switch.
            if config.get("no_instructions"):
                command.extend((flag, ""))
            elif config.get(key) is not None:
                value = str(config[key])
                command.extend((flag, value if resolved else os.path.expandvars(value)))
            continue
        append_option(command, config, key, flag, resolved=resolved)
    task_domain = task.get("task_domain", config.get("task_domain"))
    if task_domain:
        command.extend(("--task-domain", str(task_domain)))
    for key, flag in RUNNER_SWITCHES:
        if config.get(key):
            command.append(flag)
    return command
