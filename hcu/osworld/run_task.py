"""Run one OSWorld V1 or V2 task with the hybrid agent in the official QEMU runtime.

The task agent (Codex or Claude Code) and the computer-use MCP run inside
the QEMU guest; the outer Docker container is the stock OSWorld runtime that
only hosts the VM. After the official evaluator seals the RAW result, the
optional interactive Judge inspects the still-running guest.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass, field
import datetime
import json
import logging
import os
from pathlib import Path
import shlex
import sys
import time
import traceback
from typing import Any

from hcu.paths import (
    DEFAULT_ARTIFACT_DIR,
    DEFAULT_BASE_INSTRUCTIONS_FILE,
    DEFAULT_SKILL_FILE,
    OSWORLD_ROOT,
    anchor_path_args,
    results_root,
)
from hcu.osworld.agent_loop import TurnSettings, run_agent_phase
from hcu.osworld.agents import (
    claude_mcp_config,
    claude_system_prompt,
    codex_config,
    launcher_script,
    task_gateway_base,
)
from hcu.osworld.diagnostics import collect_guest_infra_diagnostic
from hcu.osworld.evaluation import run_official_evaluator
from hcu.osworld.guest import (
    GUEST_BASE_INSTRUCTIONS,
    GUEST_CLAUDE_CODE,
    GUEST_CLAUDE_MCP_ADAPTER,
    GUEST_CLAUDE_MCP_CONFIG,
    GUEST_CODEX,
    GUEST_CODEX_HOME,
    GUEST_CONFIG,
    GUEST_LAUNCHER,
    GUEST_MCP,
    GUEST_PROMPT,
    GUEST_SKILL,
    request_execute,
    safe_tag,
    transient_workload_command,
)
from hcu.osworld.judge.prompt import judge_instruction_text, judge_user_context
from hcu.osworld.judge.runner import (
    DEFAULT_JUDGE_PROMPT_FILE,
    DEFAULT_JUDGE_SCHEMA_FILE,
    JudgeSettings,
    run_live_judge_with_retry,
)
from hcu.osworld.judge.verdict import judge_answer_score_eligibility
from hcu.osworld.preflight import (
    validate_dedicated_docker_runtime,
    validate_official_asset_runtime,
)
from hcu.osworld.prompts import (
    COMPLETE_SENTINEL,
    INFEASIBLE_SENTINEL,
    split_completion_message,
    task_prompt,
)
from hcu.osworld.results import write_local
from hcu.osworld.runtime import (
    align_guest_run_clock,
    install_guest_runtime,
    public_mcp_probe_script,
    validate_guest_current_date,
    validate_provider_mount_isolation,
)
from hcu.osworld.task import (
    bind_task_runtime_timezone,
    phase_delivery_status,
    resolve_guest_image,
    task_current_date,
    task_phases,
    task_value,
)
from hcu.osworld.task_loader import load_task_config
from hcu.osworld.traces import (
    classify_turn_failure,
    concise_failure_message,
    summarize_agent_trace,
)
from hcu.osworld.usage import (
    collect_task_proxy_usage,
    count_model_requests,
    normalize_budget_termination,
    reconcile_trace_and_proxy_usage,
)
from desktop_env.cache import run_scoped_cache_dir
from desktop_env.desktop_env import DesktopEnv

MCP_PUBLIC_TOOLS = ("AX", "GUI")
CLAUDE_MCP_ADAPTER = (
    Path(__file__).resolve().parent
    / "guest_files"
    / "claude_mcp_schema_adapter.py"
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--task-id", required=True)
    parser.add_argument(
        "--benchmark-version",
        choices=("v1", "v2"),
        default="v2",
        help=(
            "Task/evaluator generation. Both use the official Docker/QEMU "
            "runtime; v1 loads the legacy JSON task suite."
        ),
    )
    parser.add_argument(
        "--task-domain",
        default="",
        help="Optional V1 task domain; UUID lookup is automatic when unique.",
    )
    parser.add_argument(
        "--agent",
        choices=("codex", "claude-code"),
        default="codex",
        help="Guest-local Task Agent. The independent Judge remains Codex.",
    )
    parser.add_argument(
        "--model",
        default="",
        help=(
            "Task Agent model. Defaults to gpt-5.5 for Codex and "
            "claude-opus-5 for Claude Code."
        ),
    )
    parser.add_argument("--effort", default="xhigh")
    parser.add_argument(
        "--gateway-base",
        default=os.environ.get(
            "HCU_OPENAI_GATEWAY",
            "http://172.17.0.1:8611",
        ),
        help="Container-bridge address reachable from the QEMU guest.",
    )
    parser.add_argument(
        "--artifact-dir",
        default=str(DEFAULT_ARTIFACT_DIR),
    )
    parser.add_argument(
        "--codex-binary",
        default="",
        help="Explicit Codex executable; otherwise use artifact-dir/codex.",
    )
    parser.add_argument(
        "--claude-code-binary",
        default="",
        help=(
            "Explicit Claude Code executable; otherwise use "
            "artifact-dir/claude-code."
        ),
    )
    parser.add_argument(
        "--claude-gateway-base",
        default=os.environ.get(
            "HCU_ANTHROPIC_GATEWAY",
            "http://172.17.0.1:8537",
        ),
        help=(
            "Anthropic-compatible gateway base reachable from the QEMU guest. "
            "Claude Code appends /v1/messages."
        ),
    )
    parser.add_argument(
        "--mcp-binary",
        default="",
        help="Explicit MCP executable; otherwise use artifact-dir/computer-use.",
    )
    parser.add_argument(
        "--guest-image",
        default="",
        help="Optional QCOW2 override for the evaluated guest.",
    )
    parser.add_argument(
        "--skill-file",
        default=str(DEFAULT_SKILL_FILE),
    )
    parser.add_argument(
        "--base-instructions-file",
        default=str(DEFAULT_BASE_INSTRUCTIONS_FILE),
        help="Compact model base instructions copied into the guest.",
    )
    parser.add_argument("--judge-model", default="")
    parser.add_argument("--judge-effort", default="xhigh")
    parser.add_argument("--judge-timeout", type=int, default=600)
    parser.add_argument("--judge-tool-limit", type=int, default=30)
    parser.add_argument("--judge-retry-tool-limit", type=int, default=15)
    parser.add_argument(
        "--judge-prompt-file",
        default=str(DEFAULT_JUDGE_PROMPT_FILE),
    )
    parser.add_argument(
        "--judge-schema-file",
        default=str(DEFAULT_JUDGE_SCHEMA_FILE),
    )
    parser.add_argument(
        "--result-dir",
        default="",
        help="Defaults to HCU_RESULTS_DIR/osworld-<version>/<run-tag>.",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=0,
        help=(
            "Optional per-phase wall-clock timeout in seconds; 0 disables "
            "the extra harness timeout and leaves termination to the model-call "
            "budget, matching the upstream OpenAI runner."
        ),
    )
    parser.add_argument(
        "--max-model-calls",
        type=int,
        default=0,
        help="Stop after this many task-scoped model requests; 0 disables the limit.",
    )
    parser.add_argument(
        "--retain-container",
        action="store_true",
        help=(
            "Stop but do not remove the task VM container after evaluation and "
            "Judge completion, preserving it for manual inspection."
        ),
    )
    parser.add_argument(
        "--token-log",
        default="",
        help="Host-side token proxy JSONL used for task-scoped request counting.",
    )
    parser.add_argument("--poll-interval", type=float, default=5.0)
    parser.add_argument(
        "--max-user-simulator-turns",
        type=int,
        default=0,
        help=(
            "Optional extra ASK_USER cap per phase; 0 relies on the normal "
            "per-phase model-call budget, matching the upstream step loop."
        ),
    )
    parser.add_argument("--client-password", default="osworld-public-evaluation")
    parser.add_argument(
        "--volume-size",
        type=int,
        default=40,
        help="Guest root disk size in GB; prevents Codex from exhausting the base image.",
    )
    parser.add_argument(
        "--ram-size",
        default="4G",
        help="Docker guest RAM size, for example 4G or 8G.",
    )
    parser.add_argument("--enable-vnc", action="store_true")
    parser.add_argument("--enable-recording", action="store_true")
    args = parser.parse_args()
    if not args.model:
        args.model = (
            "claude-opus-5"
            if args.agent == "claude-code"
            else "gpt-5.5"
        )
    return args


@dataclass
class TaskRun:
    """Inputs resolved before the guest starts, plus the evolving metadata."""

    args: argparse.Namespace
    run_tag: str
    result_dir: Path
    token_log: Path
    affinity_task: str
    codex_binary: Path
    claude_code_binary: Path
    mcp_binary: Path
    skill_file: Path
    base_instructions_file: Path
    task: Any
    phases: list[dict]
    instruction: str
    current_date: str
    run_timezone: str
    run_started_at_utc: str
    explicit_task_date: Any
    guest_image: str
    claude_endpoint: str = ""
    meta: dict = field(default_factory=dict)
    started: float = 0.0
    recording_started: bool = False

    @property
    def config_path(self) -> Path:
        return self.result_dir / (
            "guest-config.toml"
            if self.args.agent == "codex"
            else "guest-mcp.json"
        )

    @property
    def prompt_path(self) -> Path:
        return self.result_dir / "prompt.txt"

    @property
    def launcher_path(self) -> Path:
        return self.result_dir / "guest-launcher.sh"

    @property
    def skill_copy(self) -> Path:
        return self.result_dir / "AGENTS.md"

    @property
    def base_instructions_copy(self) -> Path:
        return self.result_dir / "agent-base-instructions.md"

    @property
    def uses_codex(self) -> bool:
        return self.args.agent == "codex" or bool(self.args.judge_model)

    def write_meta(self, **json_options) -> None:
        write_local(
            self.result_dir / "meta.json",
            json.dumps(self.meta, **json_options, indent=2),
        )


@dataclass
class PhaseOutcome:
    """Aggregated agent and evaluator results across the task phases."""

    phase_results: list[dict] = field(default_factory=list)
    phase_details: list[dict] = field(default_factory=list)
    total_score: float = 0.0
    official_evaluator_errors: list[dict] = field(default_factory=list)
    truncated: bool = False
    budget_exhausted: bool = False
    truncated_phases: list[int] = field(default_factory=list)
    done_payload: dict | None = None
    final_message: str = ""
    evaluator_payload: dict | None = None
    final_response: str = ""
    completion_sentinel: str | None = None


def prepare_run(args: argparse.Namespace) -> TaskRun:
    """Resolve result paths, artifacts and the task before any guest work."""
    run_tag = (
        f"{time.strftime('%Y%m%d-%H%M%S')}-task-{safe_tag(args.task_id)}-"
        f"{safe_tag(args.agent)}-{safe_tag(args.model)}-{safe_tag(args.effort)}"
    )
    results = results_root()
    benchmark_dir = f"osworld-{args.benchmark_version}"
    result_dir = Path(
        args.result_dir or results / benchmark_dir / run_tag
    ).resolve()
    result_dir.mkdir(parents=True, exist_ok=False)
    token_log_value = args.token_log or str(
        results
        / benchmark_dir
        / (
            "claude-tokens.jsonl"
            if args.agent == "claude-code"
            else "tokens.jsonl"
        )
    )
    token_log = Path(token_log_value).resolve()
    if args.max_model_calls > 0 and not token_log.is_file():
        raise FileNotFoundError(
            f"--max-model-calls requires the proxy token log: {token_log}"
        )
    affinity_task = f"osworld-{args.benchmark_version}-hybrid-{run_tag}"

    artifact_dir = Path(args.artifact_dir).resolve()
    codex_binary = Path(
        args.codex_binary or artifact_dir / "codex"
    ).resolve()
    claude_code_binary = Path(
        args.claude_code_binary or artifact_dir / "claude-code"
    ).resolve()
    mcp_binary = Path(
        args.mcp_binary or artifact_dir / "computer-use"
    ).resolve()
    skill_file = Path(args.skill_file).resolve()
    base_instructions_file = Path(
        args.base_instructions_file
    ).resolve()
    required_files = [mcp_binary, skill_file, base_instructions_file]
    if args.agent == "codex" or args.judge_model:
        required_files.append(codex_binary)
    if args.agent == "claude-code":
        required_files.append(claude_code_binary)
        required_files.append(CLAUDE_MCP_ADAPTER)
    for required in required_files:
        if not required.is_file():
            raise FileNotFoundError(required)
    if args.judge_model:
        for required in (
            Path(args.judge_prompt_file).resolve(),
            Path(args.judge_schema_file).resolve(),
        ):
            if not required.is_file():
                raise FileNotFoundError(required)

    task = load_task_config(
        None,
        task_id=args.task_id,
        base_dir=str(OSWORLD_ROOT / "evaluation_examples"),
        domain=args.task_domain or None,
        eval_version=args.benchmark_version,
    )
    phases = task_phases(task)
    run_started_at_utc = (
        str(os.environ.get("OSW_RUN_STARTED_AT_UTC") or "").strip()
        or datetime.datetime.now(datetime.timezone.utc).isoformat()
    )
    run_timezone = str(
        os.environ.get("OSW_RUN_TIMEZONE") or "UTC"
    ).strip()
    explicit_task_date = (
        task.get("task_current_date")
        if hasattr(task, "get") and callable(getattr(task, "get"))
        else None
    )
    if explicit_task_date is None:
        explicit_task_date = getattr(task, "task_current_date", None)
    current_date = task_current_date(
        task,
        run_started_at_utc=run_started_at_utc,
        timezone_name=run_timezone,
    )
    bind_task_runtime_timezone(task, run_timezone)
    instruction = str(task_value(task, "instruction", ""))
    guest_image = resolve_guest_image(task, args)
    if not instruction:
        raise ValueError(f"Task {args.task_id} has no instruction")
    return TaskRun(
        args=args,
        run_tag=run_tag,
        result_dir=result_dir,
        token_log=token_log,
        affinity_task=affinity_task,
        codex_binary=codex_binary,
        claude_code_binary=claude_code_binary,
        mcp_binary=mcp_binary,
        skill_file=skill_file,
        base_instructions_file=base_instructions_file,
        task=task,
        phases=phases,
        instruction=instruction,
        current_date=current_date,
        run_timezone=run_timezone,
        run_started_at_utc=run_started_at_utc,
        explicit_task_date=explicit_task_date,
        guest_image=guest_image,
    )


def stage_local_files(run: TaskRun) -> None:
    """Write the agent configuration, prompt, skill and launcher locally."""
    args = run.args
    affinity_prefix = f"osworld-{args.benchmark_version}-hybrid"
    if args.agent == "codex":
        write_local(
            run.config_path,
            codex_config(
                model=args.model,
                effort=args.effort,
                base_url=task_gateway_base(
                    args.gateway_base,
                    run.run_tag,
                    affinity_prefix=affinity_prefix,
                ) + "/v1",
                codex_home=GUEST_CODEX_HOME,
                base_instructions=GUEST_BASE_INSTRUCTIONS,
                mcp_command=GUEST_MCP,
            ),
        )
    else:
        write_local(run.config_path, claude_mcp_config())
    write_local(
        run.prompt_path,
        task_prompt(
            run.instruction,
            current_date=run.current_date,
            can_ask_user=task_value(run.task, "user_simulator") is not None,
        ),
    )
    run.skill_copy.write_bytes(run.skill_file.read_bytes())
    base_instructions = run.base_instructions_file.read_text(encoding="utf-8")
    if args.agent == "claude-code":
        base_instructions = claude_system_prompt(
            base_instructions,
            run.skill_file.read_text(encoding="utf-8"),
        )
    write_local(run.base_instructions_copy, base_instructions)
    run.claude_endpoint = task_gateway_base(
        args.claude_gateway_base,
        run.run_tag,
        affinity_prefix=affinity_prefix,
    )
    write_local(
        run.launcher_path,
        launcher_script(
            agent=args.agent,
            model=args.model,
            effort=args.effort,
            claude_gateway_base=run.claude_endpoint,
            public_mcp_tools=list(MCP_PUBLIC_TOOLS),
        ),
    )


def initial_meta(
    run: TaskRun,
    docker_runtime: dict,
    asset_runtime: dict,
) -> dict:
    args = run.args
    return {
        "run_tag": run.run_tag,
        "task_id": args.task_id,
        "benchmark_version": args.benchmark_version,
        "task_domain": args.task_domain or None,
        "docker_runtime": docker_runtime,
        "asset_runtime": asset_runtime,
        "instruction": run.instruction,
        "task_current_date": run.current_date,
        "task_current_date_source": (
            "task_config"
            if run.explicit_task_date is not None
            else "run_start"
        ),
        "task_current_date_timezone": run.run_timezone,
        "run_started_at_utc": run.run_started_at_utc,
        "task_protocol": "multi_phase" if run.phases else "single_phase",
        "phase_count": len(run.phases) if run.phases else 1,
        "model": args.model,
        "effort": args.effort,
        "agent": args.agent,
        "gateway_base": args.gateway_base,
        "claude_gateway_base": (
            args.claude_gateway_base
            if args.agent == "claude-code"
            else None
        ),
        "claude_gateway_endpoint": (
            run.claude_endpoint
            if args.agent == "claude-code"
            else None
        ),
        "affinity_task": run.affinity_task,
        "token_log": str(run.token_log),
        "provider": "docker",
        "guest_image": run.guest_image,
        "codex_binary": str(run.codex_binary) if run.uses_codex else None,
        "claude_code_binary": (
            str(run.claude_code_binary)
            if args.agent == "claude-code"
            else None
        ),
        "claude_mcp_adapter": (
            str(CLAUDE_MCP_ADAPTER)
            if args.agent == "claude-code"
            else None
        ),
        "mcp_binary": str(run.mcp_binary),
        "mcp_public_tools": list(MCP_PUBLIC_TOOLS),
        "skill_file": str(run.skill_file),
        "base_instructions_file": str(run.base_instructions_file),
        "start_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "judge_model": args.judge_model or None,
        "judge_effort": args.judge_effort if args.judge_model else None,
        "judge_timeout_seconds": (
            args.judge_timeout if args.judge_model else None
        ),
        "judge_tool_limit": (
            args.judge_tool_limit if args.judge_model else None
        ),
        "judge_retry_tool_limit": (
            args.judge_retry_tool_limit if args.judge_model else None
        ),
        "judge_prompt_file": (
            str(Path(args.judge_prompt_file).resolve())
            if args.judge_model else None
        ),
        "judge_schema_file": (
            str(Path(args.judge_schema_file).resolve())
            if args.judge_model else None
        ),
        "status": "initializing",
        "score": None,
        "error": None,
    }


def runtime_uploads(run: TaskRun) -> tuple[list[dict], list[str]]:
    """Return the guest runtime upload rows and the paths to mark executable."""
    args = run.args
    upload_rows = [
        {
            "local_path": str(run.base_instructions_copy),
            "path": GUEST_BASE_INSTRUCTIONS,
        },
        {"local_path": str(run.prompt_path), "path": GUEST_PROMPT},
        {"local_path": str(run.launcher_path), "path": GUEST_LAUNCHER},
        {"local_path": str(run.mcp_binary), "path": GUEST_MCP},
    ]
    executable_paths = [GUEST_MCP, GUEST_LAUNCHER]
    if args.agent == "codex":
        upload_rows.extend([
            {"local_path": str(run.codex_binary), "path": GUEST_CODEX},
            {"local_path": str(run.config_path), "path": GUEST_CONFIG},
            {"local_path": str(run.skill_copy), "path": GUEST_SKILL},
        ])
        executable_paths.append(GUEST_CODEX)
    else:
        upload_rows.extend([
            {
                "local_path": str(run.claude_code_binary),
                "path": GUEST_CLAUDE_CODE,
            },
            {
                "local_path": str(run.config_path),
                "path": GUEST_CLAUDE_MCP_CONFIG,
            },
            {
                "local_path": str(CLAUDE_MCP_ADAPTER),
                "path": GUEST_CLAUDE_MCP_ADAPTER,
            },
        ])
        executable_paths.append(GUEST_CLAUDE_CODE)
    if args.judge_model and args.agent != "codex":
        upload_rows.append({
            "local_path": str(run.codex_binary),
            "path": GUEST_CODEX,
        })
        executable_paths.append(GUEST_CODEX)
    return upload_rows, executable_paths


def start_guest(run: TaskRun) -> DesktopEnv:
    """Create the QEMU guest; the agent runtime is installed before setup."""
    args = run.args
    meta = run.meta
    task_platform = str(task_value(run.task, "platform", "linux")).lower()
    os_type = "Windows" if task_platform.startswith("win") else "Ubuntu"
    if os_type != "Ubuntu":
        raise ValueError(
            "The hybrid MCP prototype currently supports Ubuntu tasks only"
        )

    task_volume_size = task_value(run.task, "volume_size")
    volume_size = args.volume_size
    if task_volume_size is not None:
        volume_size = max(volume_size, int(task_volume_size))
    os.environ["OSWORLD_DOCKER_RAM_SIZE"] = args.ram_size
    upload_rows, executable_paths = runtime_uploads(run)

    def bootstrap_runtime(setup_controller) -> None:
        install_guest_runtime(
            setup_controller,
            client_password=args.client_password,
            upload_rows=upload_rows,
            executable_paths=tuple(executable_paths),
        )

    env = DesktopEnv(
        provider_name="docker",
        path_to_vm=run.guest_image,
        action_space="pyautogui",
        cache_dir=run_scoped_cache_dir(run.result_dir, "desktop-env"),
        headless=True,
        require_a11y_tree=False,
        os_type=os_type,
        client_password=args.client_password,
        volume_size=volume_size,
        force_disable_vnc=not args.enable_vnc,
        force_disable_recording=not args.enable_recording,
        before_task_setup=bootstrap_runtime,
    )
    meta["provider_mounts"] = validate_provider_mount_isolation(
        env.provider.container,
        run.guest_image,
    )
    meta["container_name"] = env.provider.container.name
    meta["vm_startup_attempts"] = getattr(
        env.provider,
        "startup_attempts",
        [],
    )
    meta["vm_runtime_environment"] = dict(
        getattr(env.provider, "environment", {})
    )
    meta["volume_size_gb"] = volume_size
    meta["ram_size"] = args.ram_size
    meta["status"] = "setup"
    run.write_meta()
    return env


def prepare_guest(run: TaskRun, env: DesktopEnv) -> str:
    """Run task setup and the guest preflight checks; return the guest URL."""
    args = run.args
    meta = run.meta
    align_guest_run_clock(
        env.setup_controller,
        expected_date=run.current_date,
        timezone_name=run.run_timezone,
    )
    env.reset(task_config=run.task)
    reset_finished_at = time.monotonic()
    meta["setup_attempts"] = list(
        getattr(env, "setup_attempts", [])
    )
    meta["container_name"] = env.provider.container.name
    meta["vm_startup_attempts"] = getattr(
        env.provider,
        "startup_attempts",
        [],
    )
    base_url = env.setup_controller.http_server
    guest_date_probe = request_execute(
        base_url,
        ["date", "+%Y-%m-%d"],
        timeout=20,
    )
    meta["guest_current_date_after_setup"] = validate_guest_current_date(
        expected_date=run.current_date,
        command_output=guest_date_probe.get("output", ""),
    )
    env.setup_controller.assert_root_writable()
    meta["guest_root_health_after_setup"] = "writable"
    workload_preflight = request_execute(
        base_url,
        transient_workload_command(
            ["/usr/bin/true"],
            unit_name=f"osw-preflight-{run.run_tag}",
            wait=True,
        ),
        timeout=30,
    )
    meta["workload_cgroup_preflight"] = {
        "status": "passed",
        "output": str(
            workload_preflight.get("output") or ""
        ).strip(),
    }

    active_gateway_base = (
        args.claude_gateway_base
        if args.agent == "claude-code"
        else args.gateway_base
    )
    health = request_execute(
        base_url,
        [
            "curl",
            "-fsS",
            "--max-time",
            "10",
            active_gateway_base + "/healthz",
        ],
        timeout=20,
    )
    meta["gateway_health"] = health.get("output", "").strip()

    version_commands = [f"{shlex.quote(GUEST_MCP)} --version"]
    if run.uses_codex:
        version_commands.insert(
            0,
            f"{shlex.quote(GUEST_CODEX)} --version",
        )
    if args.agent == "claude-code":
        version_commands.insert(
            0,
            f"{shlex.quote(GUEST_CLAUDE_CODE)} --version",
        )
    versions = request_execute(
        base_url,
        [
            "bash",
            "-lc",
            " && ".join(version_commands),
        ],
    )
    meta["guest_versions"] = versions.get("output", "").strip().splitlines()

    mcp_probe = request_execute(
        base_url,
        [
            "/usr/bin/python3",
            "-c",
            public_mcp_probe_script(),
            GUEST_MCP,
            json.dumps(list(MCP_PUBLIC_TOOLS)),
        ],
        timeout=180,
    )
    meta["mcp_probe"] = mcp_probe.get("output", "").strip()
    initial_wait_remaining = max(
        0.0,
        60.0 - (time.monotonic() - reset_finished_at),
    )
    if initial_wait_remaining:
        time.sleep(initial_wait_remaining)
    env.controller.start_recording()
    run.recording_started = True
    meta["status"] = "agent_running"
    run.write_meta()
    return base_url


def run_phases(run: TaskRun, env: DesktopEnv, base_url: str) -> PhaseOutcome:
    """Run the agent and the official evaluator for every task phase."""
    args = run.args
    meta = run.meta
    phase_definitions = run.phases or [{
        "name": "Task",
        "instruction": run.instruction,
        "evaluate": lambda current_env: current_env.evaluate(),
    }]
    outcome = PhaseOutcome()
    turn_settings = TurnSettings(
        agent=args.agent,
        max_model_calls=args.max_model_calls,
        poll_interval=args.poll_interval,
        max_user_simulator_turns=args.max_user_simulator_turns,
    )
    use_proxy = bool(
        task_value(run.task, "proxy", False)
        and getattr(env, "enable_proxy", False)
    )

    for phase_index, phase in enumerate(phase_definitions, start=1):
        phase_name = str(phase.get("name") or f"Phase {phase_index}")
        phase_instruction = str(phase.get("instruction") or run.instruction)
        if phase_index > 1:
            if hasattr(env, "_step_no"):
                env._step_no = 0
            if hasattr(env, "action_history"):
                env.action_history.clear()
            if hasattr(env, "_traj_no"):
                env._traj_no += 1
            setup = phase.get("setup")
            if not callable(setup):
                raise ValueError(
                    f"Phase {phase_index} has no callable setup"
                )
            setup(env.setup_controller, use_proxy=use_proxy)
            env.is_environment_used = True
            env.setup_controller.assert_root_writable()
            pause = float(phase.get("pause_after_setup_seconds", 5))
            if pause > 0:
                time.sleep(pause)

        env.instruction = phase_instruction
        meta["status"] = "agent_running"
        meta["active_phase"] = phase_index
        meta["active_phase_name"] = phase_name
        run.write_meta(ensure_ascii=False)
        phase_request_baseline = (
            count_model_requests(run.token_log, run.affinity_task)
            if args.max_model_calls > 0
            else 0
        )
        phase_deadline = (
            float("inf")
            if args.timeout == 0
            else time.monotonic() + args.timeout
        )
        agent_phase = run_agent_phase(
            env=env,
            base_url=base_url,
            result_dir=run.result_dir,
            prompt_path=run.prompt_path,
            phase_index=phase_index,
            phase_name=phase_name,
            instruction=phase_instruction,
            current_date=run.current_date,
            settings=turn_settings,
            token_log=run.token_log,
            affinity_task=run.affinity_task,
            model_request_baseline=phase_request_baseline,
            deadline=phase_deadline,
            multi_phase=bool(run.phases),
        )
        outcome.final_message = agent_phase["final_message"]
        outcome.done_payload = agent_phase["done_payload"]
        if agent_phase["truncated"]:
            outcome.truncated = True
            outcome.budget_exhausted = (
                outcome.budget_exhausted
                or bool(agent_phase["budget_exhausted"])
            )
            outcome.truncated_phases.append(phase_index)
        (
            outcome.final_response,
            outcome.completion_sentinel,
        ) = split_completion_message(outcome.final_message)
        if outcome.completion_sentinel == INFEASIBLE_SENTINEL:
            env.action_history.append("FAIL")
        elif outcome.completion_sentinel == COMPLETE_SENTINEL:
            env.action_history.append("DONE")

        save_phase_screenshot(env, run.result_dir, phase_index, "before-eval")
        official_channel = run_official_evaluator(
            lambda: phase["evaluate"](env)
        )
        official_completed = official_channel["status"] == "completed"
        phase_evaluator_payload = (
            official_channel["result"]
            if official_completed
            else {
                "status": "error",
                "score": None,
                "error": official_channel["error"],
                "traceback": official_channel["traceback"],
            }
        )
        phase_score = (
            official_channel["result"]["score"]
            if official_completed
            else 0.0
        )
        if not official_completed:
            outcome.official_evaluator_errors.append({
                "phase_index": phase_index,
                "phase_name": phase_name,
                "error": official_channel["error"],
                "traceback": official_channel["traceback"],
            })
        outcome.evaluator_payload = phase_evaluator_payload
        outcome.total_score += phase_score
        save_phase_screenshot(env, run.result_dir, phase_index, "after-eval")

        phase_result = {
            "phase_index": phase_index,
            "phase_name": phase_name,
            "instruction": phase_instruction,
            "score": phase_score,
            "evaluator_status": official_channel["status"],
        }
        phase_detail = {
            **phase_result,
            "evaluator": phase_evaluator_payload,
            "agent": agent_phase,
        }
        outcome.phase_results.append(phase_result)
        outcome.phase_details.append(phase_detail)
        gate_min_score = phase.get("gate_min_score")
        if not official_completed:
            phase_detail["gate_stop"] = "official_evaluator_error"
        elif (
            gate_min_score is not None
            and phase_score < float(gate_min_score)
        ):
            phase_detail["gate_stop"] = "below_gate_min_score"
        if (
            official_completed
            and phase.get("gate")
            and phase_score <= 0.0
        ):
            phase_detail["gate_stop"] = "zero_score_gate"
        if run.phases:
            write_local(
                run.result_dir / "phase_results.json",
                json.dumps(
                    outcome.phase_results,
                    ensure_ascii=False,
                    indent=2,
                ),
            )
        write_local(
            run.result_dir / "phase_details.json",
            json.dumps(
                outcome.phase_details,
                ensure_ascii=False,
                indent=2,
            ),
        )
        if phase_detail.get("gate_stop"):
            break
    return outcome


def save_phase_screenshot(
    env: DesktopEnv,
    result_dir: Path,
    phase_index: int,
    label: str,
) -> None:
    screenshot = env.controller.get_screenshot()
    if screenshot is not None:
        (result_dir / f"phase-{phase_index:02d}-{label}.png").write_bytes(
            screenshot
        )
        (result_dir / f"final-{label}.png").write_bytes(screenshot)


def run_judge(
    run: TaskRun,
    env: DesktopEnv,
    base_url: str,
    outcome: PhaseOutcome,
    delivery: dict,
) -> dict:
    """Run the interactive Judge and decide whether its verdict may score."""
    args = run.args
    run.meta["status"] = "judge_running"
    run.write_meta(ensure_ascii=False)
    settings = JudgeSettings(
        model=args.judge_model,
        effort=args.judge_effort,
        gateway_base=args.gateway_base,
        timeout=args.judge_timeout,
        tool_limit=args.judge_tool_limit,
        retry_tool_limit=args.judge_retry_tool_limit,
        prompt_file=Path(args.judge_prompt_file),
        schema_file=Path(args.judge_schema_file),
    )
    try:
        live_judge = run_live_judge_with_retry(
            env=env,
            base_url=base_url,
            result_dir=run.result_dir,
            settings=settings,
            run_tag=run.run_tag,
            instruction=judge_instruction_text(
                run.instruction,
                (
                    run.phases[:delivery["delivered_phase_count"]]
                    if run.phases
                    else run.phases
                ),
            ),
            final_response=outcome.final_response,
            declared_infeasible=(
                outcome.completion_sentinel == INFEASIBLE_SENTINEL
            ),
            base_instructions_file=run.base_instructions_file,
            current_date=run.current_date,
            timezone_name=run.run_timezone,
            user_context=judge_user_context(run.task, outcome.phase_details),
            raw_evaluator_completed_before_judge=(
                not outcome.official_evaluator_errors
            ),
        )
    except Exception as judge_exc:
        live_judge = {
            "judge_mode": "interactive_outcome",
            "model": args.judge_model,
            "effort": args.judge_effort,
            "judge_valid": False,
            "error": f"{type(judge_exc).__name__}: {judge_exc}",
            "traceback": traceback.format_exc(),
            "raw_evaluator_attempted_before_judge": True,
            "raw_evaluator_completed_before_judge": (
                not outcome.official_evaluator_errors
            ),
        }
        judge_dir = run.result_dir / "judge"
        judge_dir.mkdir(parents=True, exist_ok=True)
        (judge_dir / "metadata.json").write_text(
            json.dumps(
                live_judge,
                ensure_ascii=False,
                indent=2,
            ),
            encoding="utf-8",
        )
    live_judge["phase_delivery"] = delivery
    (
        live_judge["answer_score_eligible"],
        live_judge["answer_score_exclusion_reason"],
    ) = judge_answer_score_eligibility(
        truncated=outcome.truncated,
        delivery=delivery,
        final_message=outcome.final_message,
        done_payload=outcome.done_payload,
        official_evaluator_errors=outcome.official_evaluator_errors,
    )
    return live_judge


def summarize_usage(run: TaskRun, outcome: PhaseOutcome) -> dict:
    """Summarize the aggregate agent trace and reconcile it with the proxy."""
    aggregate_path = run.result_dir / "agent.jsonl"
    aggregate_trace = (
        aggregate_path.read_bytes() if aggregate_path.is_file() else None
    )
    summary = summarize_agent_trace(aggregate_trace, run.args.agent)
    proxy_usage, frozen_proxy_rows = collect_task_proxy_usage(
        run.token_log,
        run.affinity_task,
    )
    if frozen_proxy_rows:
        proxy_evidence_path = run.result_dir / "task-agent-token-usage.jsonl"
        proxy_evidence_path.write_bytes(frozen_proxy_rows)
        proxy_usage["evidence_file"] = proxy_evidence_path.name
    summary = reconcile_trace_and_proxy_usage(
        summary,
        proxy_usage,
        run.args.agent,
    )
    return normalize_budget_termination(
        summary,
        budget_exhausted=outcome.budget_exhausted,
    )


def finish_run(
    run: TaskRun,
    env: DesktopEnv,
    base_url: str,
    outcome: PhaseOutcome,
) -> int:
    """Write the RAW evaluator result, run the Judge and seal the result."""
    args = run.args
    score = round(max(0.0, min(1.0, outcome.total_score)), 4)
    raw_evaluator_status = (
        "error" if outcome.official_evaluator_errors else "completed"
    )
    if run.phases:
        result_payload = {
            "score": score,
            "phase_results": outcome.phase_results,
            "phase_details": outcome.phase_details,
        }
    else:
        result_payload = dict(outcome.evaluator_payload or {"score": score})
        result_payload["score"] = score
        result_payload["phase_details"] = outcome.phase_details
    result_payload.update({
        "raw_evaluator_status": raw_evaluator_status,
        "official_evaluator_errors": outcome.official_evaluator_errors,
    })
    write_local(run.result_dir / "final.txt", outcome.final_message + "\n")
    write_local(
        run.result_dir / "raw_evaluator.json",
        json.dumps(
            {
                "score": score,
                "status": raw_evaluator_status,
                "errors": outcome.official_evaluator_errors,
                "phase_results": outcome.phase_results,
                "phase_details": outcome.phase_details,
            },
            ensure_ascii=False,
            indent=2,
        ),
    )

    delivery = phase_delivery_status(run.phases, outcome.phase_details)
    result_payload["phase_delivery"] = delivery
    live_judge = None
    if args.judge_model:
        live_judge = run_judge(run, env, base_url, outcome, delivery)
        result_payload["live_judge"] = live_judge

    summary = summarize_usage(run, outcome)
    done_payload = outcome.done_payload
    result_payload.update({
        "task_id": args.task_id,
        "agent": args.agent,
        "model": args.model,
        "effort": args.effort,
        "agent_returncode": (
            done_payload.get("returncode") if done_payload else None
        ),
        "truncated": outcome.truncated,
        "truncated_phases": outcome.truncated_phases,
        "termination_reason": (
            "max_model_calls"
            if outcome.budget_exhausted
            else "timeout"
            if outcome.truncated
            else "completed"
        ),
        "final_message": outcome.final_message,
        "trace_summary": summary,
        "duration_sec": round(time.monotonic() - run.started, 1),
    })
    write_local(
        run.result_dir / "result.json",
        json.dumps(result_payload, ensure_ascii=False, indent=2),
    )
    write_local(run.result_dir / "result.txt", f"{score}\n")

    run.meta.update({
        "status": (
            "truncated"
            if outcome.truncated
            else "completed_with_evaluator_error"
            if outcome.official_evaluator_errors
            else "completed"
        ),
        "score": score,
        "raw_evaluator_status": raw_evaluator_status,
        "official_evaluator_errors": outcome.official_evaluator_errors,
        "end_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "duration_sec": result_payload["duration_sec"],
        "agent_returncode": result_payload["agent_returncode"],
        "termination_reason": result_payload["termination_reason"],
        "final_message": outcome.final_message,
        "trace_summary": summary,
        "live_judge": live_judge,
    })
    run.write_meta(ensure_ascii=False)
    print(json.dumps(
        {"result_dir": str(run.result_dir), **result_payload},
        ensure_ascii=False,
    ))
    return 0


def record_failure(run: TaskRun, env: DesktopEnv | None, exc: Exception) -> int:
    """Record a fatal run error together with guest diagnostics."""
    raw_error_text = f"{type(exc).__name__}: {exc}"
    failure_classification = classify_turn_failure(raw_error_text)
    error_text = concise_failure_message(raw_error_text)
    infra_diagnostic = None
    if env is not None:
        infra_diagnostic = collect_guest_infra_diagnostic(
            env.setup_controller.http_server
        )
        infra_diagnostic["primary_failure_classification"] = (
            failure_classification
        )
        infra_diagnostic["causal_to_primary_failure"] = (
            False
            if failure_classification == "tool_argument_too_large"
            else None
        )
        infra_output = str(infra_diagnostic.pop("output", ""))
        if infra_output:
            (run.result_dir / "fatal-guest-infra.txt").write_text(
                infra_output,
                encoding="utf-8",
            )
        (run.result_dir / "fatal-guest-infra.json").write_text(
            json.dumps(
                infra_diagnostic,
                ensure_ascii=False,
                indent=2,
            ),
            encoding="utf-8",
        )
    run.meta.update({
        "status": "error",
        "error": error_text,
        "failure_classification": failure_classification,
        "traceback": concise_failure_message(traceback.format_exc()),
        "infra_diagnostic": infra_diagnostic,
        "end_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "duration_sec": round(time.monotonic() - run.started, 1),
    })
    run.write_meta(ensure_ascii=False)
    (run.result_dir / "error.txt").write_text(
        run.meta["traceback"],
        encoding="utf-8",
    )
    print(run.meta["traceback"], file=sys.stderr)
    return 1


def close_guest(run: TaskRun, env: DesktopEnv) -> None:
    """Stop recording, then remove or retain the task container."""
    if run.recording_started:
        try:
            env.controller.end_recording(
                str(run.result_dir / "recording.mp4")
            )
        except Exception:
            logging.exception("Failed to stop task recording")
    if run.args.retain_container:
        container = getattr(
            getattr(env, "provider", None),
            "container",
            None,
        )
        retain_error = None
        if container is not None:
            try:
                container.stop(timeout=30)
                retain_status = "retained_stopped"
            except Exception as exc:
                retain_status = "retain_stop_failed"
                retain_error = f"{type(exc).__name__}: {exc}"
                logging.exception(
                    "Failed to stop retained task container"
                )
        else:
            retain_status = "retain_container_missing"
        cleanup_status = {
            "status": retain_status,
            "reason": "requested",
            "container_name": getattr(container, "name", None),
            "error": retain_error,
        }
    else:
        try:
            env.close()
        except Exception:
            logging.exception("Failed to close task environment")
        cleanup_status = getattr(
            getattr(env, "provider", None),
            "last_cleanup_status",
            None,
        )
    if cleanup_status is None:
        return
    write_local(
        run.result_dir / "cleanup.json",
        json.dumps(
            cleanup_status,
            ensure_ascii=False,
            indent=2,
        ),
    )
    try:
        current_meta = json.loads(
            (run.result_dir / "meta.json").read_text(
                encoding="utf-8"
            )
        )
    except (OSError, json.JSONDecodeError):
        current_meta = run.meta
    current_meta["container_cleanup"] = cleanup_status
    write_local(
        run.result_dir / "meta.json",
        json.dumps(
            current_meta,
            ensure_ascii=False,
            indent=2,
        ),
    )


def main() -> int:
    args = parse_args()
    anchor_path_args(
        args,
        "artifact_dir",
        "codex_binary",
        "claude_code_binary",
        "mcp_binary",
        "guest_image",
        "skill_file",
        "base_instructions_file",
        "judge_prompt_file",
        "judge_schema_file",
        "result_dir",
        "token_log",
    )
    os.chdir(OSWORLD_ROOT)
    docker_runtime = validate_dedicated_docker_runtime()
    asset_runtime = validate_official_asset_runtime()
    run = prepare_run(args)
    stage_local_files(run)
    run.meta = initial_meta(run, docker_runtime, asset_runtime)
    run.write_meta()

    env = None
    run.started = time.monotonic()
    try:
        env = start_guest(run)
        base_url = prepare_guest(run, env)
        outcome = run_phases(run, env, base_url)
        return finish_run(run, env, base_url, outcome)
    except Exception as exc:
        return record_failure(run, env, exc)
    finally:
        if env is not None:
            close_guest(run, env)


if __name__ == "__main__":
    raise SystemExit(main())
