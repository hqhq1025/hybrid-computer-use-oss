"""Run one OSWorld V1 or V2 task with the hybrid agent in the official QEMU runtime.

The task agent (Codex or Claude Code) and the computer-use MCP run inside
the QEMU guest; the outer Docker container is the stock OSWorld runtime that
only hosts the VM. The official evaluator scores the guest afterwards.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass, field
import datetime
import json
import logging
import math
import os
from pathlib import Path
import shlex
import sys
import time
import traceback
from typing import Any
import uuid

from hcu.paths import (
    DEFAULT_ARTIFACT_DIR,
    DEFAULT_ASSET_CACHE,
    DEFAULT_BASE_INSTRUCTIONS_FILE,
    DEFAULT_INSTRUCTIONS_FILE,
    DEFAULT_RESULTS_DIR,
    OSWORLD_ROOT,
    anchor_path_args,
)
from hcu.osworld.agent_loop import TurnSettings, run_agent_phase
from hcu.osworld.agents import (
    AGENT_PROFILES,
    AGENTS,
    claude_mcp_config,
    claude_system_prompt,
    codex_config,
    gemini_experiments,
    gemini_settings,
    grok_config,
    grok_system_prompt,
    public_tools,
    launcher_script,
    task_gateway_base,
)
from hcu.osworld.diagnostics import collect_guest_infra_diagnostic
from hcu.gateway.common import TASK_AFFINITY_PREFIX
from hcu.osworld.evaluation import run_official_evaluator
from hcu.osworld.guest import (
    GUEST_BASE_INSTRUCTIONS,
    GUEST_CLAUDE_MCP_ADAPTER,
    GUEST_CLAUDE_MCP_CONFIG,
    GUEST_CODEX_HOME,
    GUEST_CLAUDE_CODE,
    GUEST_CODEX,
    GUEST_CONFIG,
    GUEST_GEMINI_ARCHIVE,
    GUEST_GEMINI_CLI,
    GUEST_GEMINI_CONTEXT,
    GUEST_GEMINI_EXPERIMENTS,
    GUEST_GEMINI_NODE,
    GUEST_GEMINI_PACKAGE,
    GUEST_GEMINI_SETTINGS,
    GUEST_GEMINI_TRUSTED,
    GUEST_GROK,
    GUEST_GROK_CONFIG,
    GUEST_LAUNCHER,
    GUEST_MCP,
    GUEST_PROMPT,
    GUEST_INSTRUCTIONS,
    request_execute,
    safe_tag,
    transient_workload_command,
)
from hcu.osworld.preflight import docker_runtime
from hcu.osworld.prompts import (
    COMPLETE_SENTINEL,
    INFEASIBLE_SENTINEL,
    split_completion_message,
    task_prompt,
)
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
    check_gateway_budget,
    collect_task_proxy_usage,
    normalize_budget_termination,
    reconcile_trace_and_proxy_usage,
)
from desktop_env.file_source import configure as configure_assets
from desktop_env.cache import run_scoped_cache_dir
from desktop_env.desktop_env import DesktopEnv


def write_text(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")


# Where each agent's artifact lands in the guest.
GUEST_AGENT_BINARIES = {
    "codex": GUEST_CODEX,
    "claude-code": GUEST_CLAUDE_CODE,
    "gemini-cli": GUEST_GEMINI_ARCHIVE,
    "grok-build": GUEST_GROK,
}

GATEWAY_PORTS = {"openai": 8611, "anthropic": 8537, "gemini": 8571}

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
        choices=AGENTS,
        default="codex",
        help="Guest-local Task Agent.",
    )
    parser.add_argument(
        "--model",
        default="",
        help=(
            "Task Agent model. Defaults: gpt-5.5 (Codex), claude-opus-5 "
            "(Claude Code), gemini-3.8-flash (Gemini CLI), grok-4.7 (Grok Build)."
        ),
    )
    parser.add_argument(
        "--effort",
        default="",
        help=(
            "Reasoning effort; for Gemini CLI a thinking level or native. "
            "Defaults to xhigh, and to high for Gemini CLI."
        ),
    )
    parser.add_argument(
        "--gateway",
        default="",
        help=(
            "Model gateway URL reachable from the QEMU guest. Default: the "
            "Docker bridge gateway at the agent's port (OpenAI 8611, "
            "Anthropic 8537, Gemini 8571)."
        ),
    )
    parser.add_argument(
        "--artifact-dir",
        default=str(DEFAULT_ARTIFACT_DIR),
    )
    parser.add_argument(
        "--agent-binary",
        default="",
        help=(
            "Explicit agent executable, or the Gemini CLI npm tarball; "
            "otherwise the agent's file under --artifact-dir."
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
        "--instructions-file",
        default=str(DEFAULT_INSTRUCTIONS_FILE),
        help="Profile instructions given to the agent; empty disables the profile.",
    )
    parser.add_argument(
        "--channels",
        default="",
        choices=("", "a11y,keyboard", "gui,keyboard"),
        help=(
            "Expose only the AX channel (a11y,keyboard) or only the GUI "
            "channel (gui,keyboard); empty exposes both."
        ),
    )
    parser.add_argument(
        "--base-instructions-file",
        default=str(DEFAULT_BASE_INSTRUCTIONS_FILE),
        help="Compact model base instructions copied into the guest.",
    )
    parser.add_argument(
        "--result-dir",
        default="",
        help="Default: runs/single/<run-tag> in this repository.",
    )
    parser.add_argument(
        "--asset-cache",
        default=str(DEFAULT_ASSET_CACHE),
        help="Setup asset download cache, keyed by source URL.",
    )
    parser.add_argument(
        "--offline-assets",
        action="store_true",
        help="Fail setup instead of downloading an asset missing from the cache.",
    )
    parser.add_argument(
        "--timezone", default="UTC",
        help="Guest timezone; also decides the task's current date.",
    )
    parser.add_argument(
        "--run-started-at", default="",
        help="UTC start of the whole run (ISO 8601); fixes the task date across a panel.",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=0,
        help=(
            "Optional per-phase wall-clock timeout in seconds; 0 disables "
            "this deadline, not the 1800-second generation no-progress limit."
        ),
    )
    parser.add_argument(
        "--max-model-calls",
        type=int,
        default=0,
        help=(
            "Allow this many successful task-scoped generation responses; "
            "the last response may finish its tools. 0 disables the count limit."
        ),
    )
    parser.add_argument(
        "--retain-container",
        action="store_true",
        help=(
            "Stop but do not remove the task VM container after evaluation, "
            "preserving it for manual inspection."
        ),
    )
    parser.add_argument(
        "--token-log",
        default="",
        help="Host-side gateway JSONL with successful-generation and usage evidence.",
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
        help="Required guest root-volume size class in GiB, within existing image capacity.",
    )
    parser.add_argument(
        "--ram-size",
        default="4G",
        help="Docker guest RAM size, for example 4G or 8G.",
    )
    parser.add_argument("--enable-vnc", action="store_true")
    parser.add_argument("--enable-recording", action="store_true")
    args = parser.parse_args()
    profile = AGENT_PROFILES[args.agent]
    args.model = args.model or profile["default_model"]
    args.effort = args.effort or profile["default_effort"]
    return args


@dataclass
class TaskRun:
    """Inputs resolved before the guest starts, plus the evolving metadata."""

    args: argparse.Namespace
    run_tag: str
    result_dir: Path
    token_log: Path
    affinity_task: str
    agent_binary: Path
    node_binary: Path | None
    mcp_binary: Path
    instructions_file: Path | None
    base_instructions_file: Path
    task: Any
    phases: list[dict]
    instruction: str
    current_date: str
    run_timezone: str
    run_started_at_utc: str
    explicit_task_date: Any
    guest_image: str
    agent_endpoint: str = ""
    meta: dict = field(default_factory=dict)
    started: float = 0.0
    recording_started: bool = False

    @property
    def profile(self) -> dict:
        return AGENT_PROFILES[self.args.agent]

    @property
    def config_path(self) -> Path:
        return self.result_dir / {
            "codex": "guest-config.toml",
            "claude-code": "guest-mcp.json",
            "gemini-cli": "guest-settings.json",
            "grok-build": "guest-config.toml",
        }[self.args.agent]

    @property
    def gemini_trusted_path(self) -> Path:
        return self.result_dir / "guest-trusted-folders.json"

    @property
    def gemini_experiments_path(self) -> Path:
        return self.result_dir / "guest-experiments.json"

    @property
    def prompt_path(self) -> Path:
        return self.result_dir / "prompt.txt"

    @property
    def launcher_path(self) -> Path:
        return self.result_dir / "guest-launcher.sh"

    @property
    def instructions_copy(self) -> Path:
        return self.result_dir / "operating-instructions.md"

    @property
    def mcp_public_tools(self) -> list[str]:
        return list(public_tools(self.args.channels))

    @property
    def base_instructions_copy(self) -> Path:
        return self.result_dir / "agent-base-instructions.md"

    def write_meta(self, **json_options) -> None:
        write_text(
            self.result_dir / "meta.json",
            json.dumps(self.meta, **json_options, indent=2),
        )


@dataclass
class PhaseOutcome:
    """Aggregated agent and evaluator results across the task phases."""

    phase_results: list[dict] = field(default_factory=list)
    phase_details: list[dict] = field(default_factory=list)
    total_score: float | None = None
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
        f"{safe_tag(args.agent)}-{safe_tag(args.model)}-{safe_tag(args.effort)}-"
        f"{uuid.uuid4()}"
    )
    result_dir = Path(
        args.result_dir or DEFAULT_RESULTS_DIR / "single" / run_tag
    ).resolve()
    result_dir.mkdir(parents=True, exist_ok=False)
    token_log = Path(
        args.token_log or result_dir.parent / "gateway-tokens.jsonl"
    ).resolve()
    if args.max_model_calls > 0 and not token_log.is_file():
        raise FileNotFoundError(
            f"--max-model-calls requires the gateway token log: {token_log}"
        )
    affinity_task = f"{TASK_AFFINITY_PREFIX}{run_tag}"

    artifact_dir = Path(args.artifact_dir).resolve()
    profile = AGENT_PROFILES[args.agent]
    agent_binary = Path(
        args.agent_binary or artifact_dir / profile["artifact"]
    ).resolve()
    node_binary = (
        (artifact_dir / "node").resolve() if args.agent == "gemini-cli" else None
    )
    mcp_binary = Path(
        args.mcp_binary or artifact_dir / "computer-use"
    ).resolve()
    instructions_file = (
        Path(args.instructions_file).resolve() if args.instructions_file else None
    )
    base_instructions_file = Path(
        args.base_instructions_file
    ).resolve()
    required_files = [agent_binary, mcp_binary, base_instructions_file]
    if instructions_file is not None:
        required_files.append(instructions_file)
    if args.agent == "claude-code":
        required_files.append(CLAUDE_MCP_ADAPTER)
    if node_binary is not None:
        required_files.append(node_binary)
    for required in required_files:
        if not required.is_file():
            raise FileNotFoundError(required)
    task = load_task_config(
        None,
        task_id=args.task_id,
        base_dir=str(OSWORLD_ROOT / "evaluation_examples"),
        domain=args.task_domain or None,
        eval_version=args.benchmark_version,
        prefer_class=args.benchmark_version == "v2",
    )
    phases = task_phases(task)
    run_started_at_utc = (
        args.run_started_at
        or datetime.datetime.now(datetime.timezone.utc).isoformat()
    )
    run_timezone = args.timezone
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
        agent_binary=agent_binary,
        node_binary=node_binary,
        mcp_binary=mcp_binary,
        instructions_file=instructions_file,
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
    """Write the agent configuration, instructions and launcher locally."""
    args = run.args
    run.agent_endpoint = task_gateway_base(
        args.gateway,
        run.run_tag,
        affinity_prefix=TASK_AFFINITY_PREFIX.rstrip("-"),
    )
    if args.agent == "codex":
        config = codex_config(
            model=args.model,
            effort=args.effort,
            base_url=run.agent_endpoint + "/v1",
            codex_home=GUEST_CODEX_HOME,
            base_instructions=GUEST_BASE_INSTRUCTIONS,
            mcp_command=GUEST_MCP,
            channels=args.channels,
            execution_context="isolated-guest",
        )
    elif args.agent == "claude-code":
        config = claude_mcp_config(channels=args.channels)
    elif args.agent == "gemini-cli":
        config = gemini_settings(
            model=args.model, effort=args.effort, channels=args.channels
        )
    else:
        config = grok_config(
            model=args.model,
            base_url=run.agent_endpoint + "/v1",
            effort=args.effort,
            channels=args.channels,
        )
    write_text(run.config_path, config)
    write_text(
        run.prompt_path,
        task_prompt(
            run.instruction,
            current_date=run.current_date,
            can_ask_user=task_value(run.task, "user_simulator") is not None,
        ),
    )
    # Codex reads the profile from a task-local AGENTS.md and Gemini CLI from
    # its global GEMINI.md, both uploaded from instructions_copy; Claude Code and
    # Grok Build get it inside their replacement system prompts.
    if run.instructions_file is not None:
        run.instructions_copy.write_bytes(run.instructions_file.read_bytes())
    base_instructions = run.base_instructions_file.read_text(encoding="utf-8")
    if run.instructions_file is not None:
        instructions = run.instructions_file.read_text(encoding="utf-8")
        if args.agent == "claude-code":
            base_instructions = claude_system_prompt(base_instructions, instructions)
        elif args.agent == "grok-build":
            base_instructions = grok_system_prompt(base_instructions, instructions)
    write_text(run.base_instructions_copy, base_instructions)
    if args.agent == "gemini-cli":
        write_text(
            run.gemini_trusted_path,
            json.dumps({"/home/user": "TRUST_FOLDER"}) + "\n",
        )
        write_text(run.gemini_experiments_path, gemini_experiments())
    write_text(
        run.launcher_path,
        launcher_script(
            agent=args.agent,
            model=args.model,
            effort=args.effort,
            gateway_endpoint=run.agent_endpoint,
            public_mcp_tools=run.mcp_public_tools,
        ),
    )


def initial_meta(run: TaskRun, docker_runtime: dict) -> dict:
    args = run.args
    return {
        "run_tag": run.run_tag,
        "task_id": args.task_id,
        "benchmark_version": args.benchmark_version,
        "task_domain": args.task_domain or None,
        "docker_runtime": docker_runtime,
        "asset_cache": args.asset_cache,
        "offline_assets": args.offline_assets,
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
        "gateway": run.profile["gateway"],
        "gateway_endpoint": run.agent_endpoint,
        "affinity_task": run.affinity_task,
        "token_log": str(run.token_log),
        "provider": "docker",
        "guest_image": run.guest_image,
        "agent_binary": str(run.agent_binary),
        "claude_mcp_adapter": (
            str(CLAUDE_MCP_ADAPTER)
            if args.agent == "claude-code"
            else None
        ),
        "mcp_binary": str(run.mcp_binary),
        "mcp_channels": args.channels or None,
        "mcp_public_tools": run.mcp_public_tools,
        "instructions_file": str(run.instructions_file) if run.instructions_file else None,
        "base_instructions_file": str(run.base_instructions_file),
        "start_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "status": "initializing",
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
    guest_binary = GUEST_AGENT_BINARIES[args.agent]
    upload_rows.append({"local_path": str(run.agent_binary), "path": guest_binary})
    if args.agent != "gemini-cli":
        executable_paths.append(guest_binary)
    if args.agent == "codex":
        upload_rows.append({"local_path": str(run.config_path), "path": GUEST_CONFIG})
        if run.instructions_file is not None:
            upload_rows.append(
                {"local_path": str(run.instructions_copy), "path": GUEST_INSTRUCTIONS}
            )
    elif args.agent == "claude-code":
        upload_rows.extend([
            {"local_path": str(run.config_path), "path": GUEST_CLAUDE_MCP_CONFIG},
            {"local_path": str(CLAUDE_MCP_ADAPTER), "path": GUEST_CLAUDE_MCP_ADAPTER},
        ])
    elif args.agent == "gemini-cli":
        upload_rows.extend([
            {"local_path": str(run.node_binary), "path": GUEST_GEMINI_NODE},
            {"local_path": str(run.config_path), "path": GUEST_GEMINI_SETTINGS},
            {"local_path": str(run.gemini_trusted_path), "path": GUEST_GEMINI_TRUSTED},
            {
                "local_path": str(run.gemini_experiments_path),
                "path": GUEST_GEMINI_EXPERIMENTS,
            },
        ])
        if run.instructions_file is not None:
            upload_rows.append(
                {"local_path": str(run.instructions_copy), "path": GUEST_GEMINI_CONTEXT}
            )
        executable_paths.append(GUEST_GEMINI_NODE)
    else:
        upload_rows.append({"local_path": str(run.config_path), "path": GUEST_GROK_CONFIG})
    return upload_rows, executable_paths


def start_guest(run: TaskRun) -> DesktopEnv:
    """Create the QEMU guest; the agent runtime is installed before setup."""
    args = run.args
    meta = run.meta
    task_platform = str(task_value(run.task, "platform", "linux")).lower()
    os_type = "Windows" if task_platform.startswith("win") else "Ubuntu"
    if os_type != "Ubuntu":
        raise ValueError(
            "The OSWorld runner supports Ubuntu tasks only"
        )

    task_volume_size = task_value(run.task, "volume_size")
    volume_size = args.volume_size
    if task_volume_size is not None:
        volume_size = max(volume_size, int(task_volume_size))
    os.environ["OSWORLD_DOCKER_RAM_SIZE"] = args.ram_size
    upload_rows, executable_paths = runtime_uploads(run)
    unpack = (
        {GUEST_GEMINI_ARCHIVE: GUEST_GEMINI_PACKAGE}
        if args.agent == "gemini-cli"
        else {}
    )

    def bootstrap_runtime(setup_controller) -> None:
        install_guest_runtime(
            setup_controller,
            client_password=args.client_password,
            upload_rows=upload_rows,
            executable_paths=tuple(executable_paths),
            unpack=unpack,
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
            unit_name=f"agent-preflight-{run.run_tag}",
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

    gateway_base = run.agent_endpoint.rsplit("/t/", 1)[0]
    health = request_execute(
        base_url,
        [
            "curl",
            "-fsS",
            "--max-time",
            "10",
            gateway_base + "/healthz",
        ],
        timeout=20,
    )
    meta["gateway_health"] = health.get("output", "").strip()
    check_gateway_budget(meta["gateway_health"], args.max_model_calls)

    agent_version = (
        f"{shlex.quote(GUEST_GEMINI_NODE)} {shlex.quote(GUEST_GEMINI_CLI)}"
        if args.agent == "gemini-cli"
        else shlex.quote(GUEST_AGENT_BINARIES[args.agent])
    )
    version_commands = [
        f"{agent_version} --version",
        f"{shlex.quote(GUEST_MCP)} --version",
    ]
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
            json.dumps(run.mcp_public_tools),
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
                "result_repr": official_channel["result_repr"],
            }
        )
        phase_score = (
            official_channel["result"]["score"]
            if official_completed
            else None
        )
        if not official_completed:
            outcome.official_evaluator_errors.append({
                "phase_index": phase_index,
                "phase_name": phase_name,
                "error": official_channel["error"],
                "traceback": official_channel["traceback"],
                "result_repr": official_channel["result_repr"],
            })
        outcome.evaluator_payload = phase_evaluator_payload
        if phase_score is not None:
            outcome.total_score = (
                0.0 if outcome.total_score is None else outcome.total_score
            ) + phase_score
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


def write_result(run: TaskRun, status: str, **fields) -> dict:
    """Write ``result.json``, the one authoritative record of a task run.

    Every run that gets this far writes exactly one: ``completed`` or
    ``evaluator_error`` after evaluation, ``error`` when the run failed.
    """
    args = run.args
    result = {
        "task_id": args.task_id,
        "benchmark_version": args.benchmark_version,
        "agent": args.agent,
        "model": args.model,
        "effort": args.effort,
        "status": status,
        "score": None,
        **fields,
        "duration_sec": round(time.monotonic() - run.started, 1),
    }
    (run.result_dir / "result.json").write_text(
        json.dumps(result, ensure_ascii=False, indent=2, allow_nan=False),
        encoding="utf-8",
    )
    run.meta.update({
        "status": status,
        "end_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    })
    run.write_meta(ensure_ascii=False)
    return result


def finish_run(
    run: TaskRun,
    env: DesktopEnv,
    base_url: str,
    outcome: PhaseOutcome,
) -> int:
    """Clamp the summed phase scores and write the result."""
    score = None
    errors = outcome.official_evaluator_errors
    if not errors:
        total = outcome.total_score
        if total is None or not math.isfinite(total):
            # Finite phase scores can still overflow when summed.
            errors.append({
                "stage": "aggregation",
                "error": "aggregate score is not a finite number",
                "result_repr": repr(total),
            })
        else:
            score = round(max(0.0, min(1.0, total)), 4)
    done_payload = outcome.done_payload
    result = write_result(
        run,
        "evaluator_error" if errors else "completed",
        score=score,
        termination_reason=(
            "max_model_calls" if outcome.budget_exhausted
            else "timeout" if outcome.truncated
            else "completed"
        ),
        truncated=outcome.truncated,
        truncated_phases=outcome.truncated_phases,
        evaluator_errors=errors,
        final_message=outcome.final_message,
        agent_returncode=done_payload.get("returncode") if done_payload else None,
        phase_delivery=phase_delivery_status(run.phases, outcome.phase_details),
        phases=outcome.phase_details,
        usage=summarize_usage(run, outcome),
    )
    print(json.dumps(
        {"result_dir": str(run.result_dir), "status": result["status"], "score": score},
    ))
    return 0


def record_failure(run: TaskRun, env: DesktopEnv | None, exc: Exception) -> int:
    """Record a fatal run error together with guest diagnostics."""
    raw_error_text = f"{type(exc).__name__}: {exc}"
    failure_classification = classify_turn_failure(raw_error_text)
    if env is not None:
        infra_diagnostic = collect_guest_infra_diagnostic(
            env.setup_controller.http_server
        )
        infra_output = str(infra_diagnostic.pop("output", ""))
        if infra_output:
            (run.result_dir / "fatal-guest-infra.txt").write_text(
                infra_output,
                encoding="utf-8",
            )
        (run.result_dir / "fatal-guest-infra.json").write_text(
            json.dumps(infra_diagnostic, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    trace = concise_failure_message(traceback.format_exc())
    write_result(
        run,
        "error",
        failure_classification=failure_classification,
        error=concise_failure_message(raw_error_text),
        traceback=trace,
    )
    print(trace, file=sys.stderr)
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
    if cleanup_status is not None:
        run.meta["container_cleanup"] = cleanup_status
        run.write_meta(ensure_ascii=False)


def main() -> int:
    args = parse_args()
    anchor_path_args(
        args,
        "artifact_dir",
        "agent_binary",
        "mcp_binary",
        "guest_image",
        "instructions_file",
        "base_instructions_file",
        "result_dir",
        "token_log",
        "asset_cache",
    )
    os.chdir(OSWORLD_ROOT)
    configure_assets(args.asset_cache, offline=args.offline_assets)
    runtime = docker_runtime()
    if not args.gateway:
        bridge = (runtime["bridge_gateways"] or ["172.17.0.1"])[0]
        port = GATEWAY_PORTS[AGENT_PROFILES[args.agent]["gateway"]]
        args.gateway = f"http://{bridge}:{port}"
    run = prepare_run(args)
    stage_local_files(run)
    run.meta = initial_meta(run, runtime)
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
