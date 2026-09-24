"""Configuration shared by the batch and queue coordinators.

One JSON file describes a set of tasks and the options every task run uses.
``run_batch`` and ``run_queue`` accept the same keys, so a configuration can
move between the simple batch runner and the rolling queue unchanged.
Unknown keys are rejected instead of silently ignored.
"""

from __future__ import annotations

import os
from pathlib import Path
import sys

# Config key -> run_task flag for options that take a value. String values
# may reference environment variables such as ${HCU_ARTIFACT_DIR}.
RUNNER_VALUE_OPTIONS = (
    ("gateway_base", "--gateway-base"),
    ("claude_gateway_base", "--claude-gateway-base"),
    ("guest_image", "--guest-image"),
    ("artifact_dir", "--artifact-dir"),
    ("codex_binary", "--codex-binary"),
    ("claude_code_binary", "--claude-code-binary"),
    ("mcp_binary", "--mcp-binary"),
    ("skill_file", "--skill-file"),
    ("base_instructions_file", "--base-instructions-file"),
    ("client_password", "--client-password"),
    ("judge_model", "--judge-model"),
    ("judge_effort", "--judge-effort"),
    ("judge_timeout", "--judge-timeout"),
    ("judge_tool_limit", "--judge-tool-limit"),
    ("judge_retry_tool_limit", "--judge-retry-tool-limit"),
    ("judge_prompt_file", "--judge-prompt-file"),
    ("judge_schema_file", "--judge-schema-file"),
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
# Scheduling, paired-run and host-safety options read only by run_queue.
QUEUE_KEYS = frozenset({
    "sides",
    "max_active_sides",
    "max_total_live_sides",
    "max_starting_sides",
    "launch_stagger_seconds",
    "max_steps",
    "max_tokens",
    "require_official_openai_runner_alignment",
    "require_token_proxy_binding",
    "require_approved_host_kernel",
    "approved_host_kernel_releases",
    "run_started_at_utc",
    "run_timezone",
    "environment_file",
    "required_environment",
    "docker_root_path",
    "container_cleanup_lock",
    "container_cleanup_lock_timeout_seconds",
    "max_host_d_state",
    "max_inode_switch_wbs_workers",
    "runc_delete_age_seconds",
    "host_pressure_guard_enabled",
    "host_pressure_abort_enabled",
    "host_pressure_cache_seconds",
    "max_io_full_avg10",
    "critical_host_pressure_abort_min_d_state",
    "critical_host_pressure_abort_confirmation_samples",
    "critical_host_pressure_abort_confirmation_seconds",
    "max_load_1m",
    "min_available_memory_gb",
    "min_free_disk_gb",
    "min_free_inodes",
    "min_docker_free_gb",
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


def append_option(command: list[str], config: dict, key: str, flag: str) -> None:
    value = config.get(key)
    if value not in (None, ""):
        command.extend((flag, os.path.expandvars(str(value))))


def runner_command(config: dict, task: dict, result_dir: Path) -> list[str]:
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
        str(config.get("effort") or "xhigh"),
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
        append_option(command, config, key, flag)
    task_domain = task.get("task_domain", config.get("task_domain"))
    if task_domain:
        command.extend(("--task-domain", str(task_domain)))
    for key, flag in RUNNER_SWITCHES:
        if config.get(key):
            command.append(flag)
    return command
