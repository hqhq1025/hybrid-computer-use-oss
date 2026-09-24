"""Run the interactive outcome Judge inside the task guest.

The Judge is a fresh Codex session started after the official evaluator has
sealed the RAW result. It inspects the still-running guest, returns a
schema-validated verdict and is stopped at a hard completed-tool budget.
One bounded retry reuses the first attempt's completed tool events.
"""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import shlex
import shutil
import time
from typing import TYPE_CHECKING

from hcu.osworld.agents import codex_config, task_gateway_base
from hcu.osworld.guest import (
    GUEST_CODEX,
    GUEST_JUDGE_BASE_INSTRUCTIONS,
    GUEST_JUDGE_CODEX_HOME,
    GUEST_JUDGE_CONFIG,
    GUEST_JUDGE_PROBE,
    GUEST_JUDGE_PROMPT,
    GUEST_JUDGE_PTY_STREAM,
    GUEST_JUDGE_RESULT,
    GUEST_JUDGE_ROOT,
    GUEST_JUDGE_SCHEMA,
    GUEST_JUDGE_STATUS,
    GUEST_JUDGE_STDERR,
    GUEST_JUDGE_TRACE,
    GUEST_JUDGE_WORK,
    GUEST_MCP,
    GUEST_PRIOR_JUDGE_EVIDENCE,
    fetch_guest_file,
    guest_json_file,
    request_execute,
    safe_tag,
    save_guest_artifact_best_effort,
    transient_workload_command,
)
from hcu.osworld.judge.evidence import (
    build_live_judge_trace_evidence,
    build_prior_judge_trace_evidence,
    completed_tool_events,
    live_judge_mutation_audit,
    nonvolatile_judge_changes,
)
from hcu.osworld.judge.prompt import (
    JUDGE_RETRY_CONVERGENCE_PROMPT,
    build_judge_prompt,
)
from hcu.osworld.judge.state import capture_judge_state, compare_judge_states
from hcu.osworld.judge.verdict import (
    classify_judge_attempt,
    judge_retry_eligible,
    valid_judge_verdict,
)

if TYPE_CHECKING:
    from desktop_env.desktop_env import DesktopEnv

JUDGE_DIR = Path(__file__).resolve().parent
GUEST_FILES_DIR = JUDGE_DIR.parent / "guest_files"
DEFAULT_JUDGE_PROMPT_FILE = JUDGE_DIR / "prompt.md"
DEFAULT_JUDGE_SCHEMA_FILE = JUDGE_DIR / "schema.json"


LIVE_JUDGE_PROBE_SCRIPT = r"""#!/usr/bin/env bash
set -euo pipefail

seconds=10
if [[ ${1:-} =~ ^[0-9]+$ ]]; then
    seconds=$1
    shift
fi
if (( seconds < 1 )); then
    seconds=1
elif (( seconds > 30 )); then
    seconds=30
fi
if [[ ${1:-} == "--" ]]; then
    shift
fi
if (( $# == 0 )); then
    echo "usage: judge-probe [1-30] [--] command [args...]" >&2
    exit 2
fi

exec /usr/bin/timeout --signal=TERM --kill-after=2s "${seconds}s" "$@"
"""


JUDGE_STATUS_POLL_INTERVAL_SEC = 2.0


@dataclass(frozen=True)
class JudgeSettings:
    """Judge model, budget and prompt files for one run."""

    model: str
    effort: str
    gateway_base: str
    timeout: int
    tool_limit: int
    retry_tool_limit: int
    prompt_file: Path
    schema_file: Path


def archive_judge_attempt(judge_dir: Path, attempt: int) -> Path:
    destination = judge_dir / f"attempt-{attempt:02d}"
    destination.mkdir(parents=True, exist_ok=True)
    for path in judge_dir.iterdir():
        if path == destination or path.name.startswith("attempt-"):
            continue
        if path.is_file():
            shutil.copy2(path, destination / path.name)
    return destination


def monitor_judge_workload(
    *,
    base_url: str,
    unit_name: str,
    timeout_seconds: int,
    tool_limit: int,
    attempt_token: str,
) -> dict:
    if tool_limit < 1:
        raise ValueError("Judge tool limit must be positive")
    if not attempt_token:
        raise ValueError("Judge attempt token must be non-empty")
    started = time.monotonic()
    deadline = started + timeout_seconds + 20
    peak_completed_tools = 0
    tool_budget_exhausted = False
    finalization_threshold_reached = False
    finalization_at = max(1, tool_limit - min(4, tool_limit - 1))
    last_status = None

    def sample_completed_tools() -> int:
        trace_raw = fetch_guest_file(base_url, GUEST_JUDGE_TRACE)
        if not trace_raw:
            return 0
        return len(
            completed_tool_events(
                trace_raw.decode(
                    "utf-8",
                    errors="replace",
                ).splitlines()
            )
        )

    while time.monotonic() < deadline:
        status = guest_json_file(base_url, GUEST_JUDGE_STATUS)
        current_attempt = bool(
            status
            and status.get("attempt_token") == attempt_token
        )
        if current_attempt:
            last_status = status
            if status.get("state") == "exited":
                completed = sample_completed_tools()
                peak_completed_tools = max(
                    peak_completed_tools,
                    completed,
                )
                if completed >= finalization_at:
                    finalization_threshold_reached = True
                if completed >= tool_limit:
                    tool_budget_exhausted = True
                break
        else:
            time.sleep(JUDGE_STATUS_POLL_INTERVAL_SEC)
            continue
        completed = sample_completed_tools()
        peak_completed_tools = max(peak_completed_tools, completed)
        if completed >= finalization_at:
            finalization_threshold_reached = True
        if completed >= tool_limit:
            tool_budget_exhausted = True
            try:
                request_execute(
                    base_url,
                    [
                        "systemctl",
                        "--user",
                        "kill",
                        "--signal=TERM",
                        safe_tag(unit_name)[:200],
                    ],
                    timeout=30,
                )
            except Exception:
                pass
            break
        time.sleep(JUDGE_STATUS_POLL_INTERVAL_SEC)

    if tool_budget_exhausted:
        stop_deadline = time.monotonic() + 15
        while time.monotonic() < stop_deadline:
            status = guest_json_file(base_url, GUEST_JUDGE_STATUS)
            if (
                status
                and status.get("attempt_token") == attempt_token
            ):
                last_status = status
                if status.get("state") == "exited":
                    break
            time.sleep(1)
    return {
        "status": last_status,
        "completed_tools": peak_completed_tools,
        "tool_limit": tool_limit,
        "finalization_at": finalization_at,
        "finalization_threshold_reached": finalization_threshold_reached,
        "tool_budget_exhausted": tool_budget_exhausted,
        "monitor_duration_seconds": round(time.monotonic() - started, 3),
    }


def judge_prompt_text(
    settings: JudgeSettings,
    *,
    instruction: str,
    final_response: str,
    declared_infeasible: bool,
    current_date: str,
    timezone_name: str,
    user_context: dict | None,
    tool_limit: int,
    retry_context: str | None,
) -> str:
    prompt_text = build_judge_prompt(
        Path(settings.prompt_file).resolve(),
        instruction=instruction,
        final_response=final_response,
        declared_infeasible=declared_infeasible,
        current_date=current_date,
        timezone_name=timezone_name,
        user_context=user_context,
    )
    prompt_text = (
        prompt_text
        + "\n\nEnforced inspection budget:\n"
        + f"- hard completed-tool limit: {tool_limit}\n"
        + "- finish evidence collection at least four calls before the hard "
        "limit and emit the final JSON verdict.\n"
        + "- the runner will terminate the exact Judge workload when the "
        "hard limit is reached.\n"
    )
    if retry_context:
        prompt_text = f"{prompt_text}\n\n{retry_context.strip()}\n"
    return prompt_text


def stage_judge_inputs(
    *,
    env: DesktopEnv,
    base_url: str,
    judge_dir: Path,
    trace_path: Path,
    settings: JudgeSettings,
    run_tag: str,
    prompt_text: str,
    base_instructions_file: Path,
    final_response: str,
    prior_judge_evidence_path: Path | None,
) -> dict:
    """Write the Judge inputs locally, upload them and describe the seal."""
    config_path = judge_dir / "config.toml"
    prompt_path = judge_dir / "prompt.txt"
    schema_copy = judge_dir / "schema.json"
    base_copy = judge_dir / "codex-base-instructions.md"
    probe_copy = judge_dir / "judge-probe"
    final_response_path = judge_dir / "task_agent_final_response.txt"
    execution_evidence_path = judge_dir / "task_agent_execution_evidence.jsonl"
    execution_event_count = build_live_judge_trace_evidence(
        trace_path,
        execution_evidence_path,
    )
    config_path.write_text(
        codex_config(
            model=settings.model,
            effort=settings.effort,
            base_url=task_gateway_base(
                settings.gateway_base,
                run_tag,
                affinity_prefix="osworld-v2-judge",
            ) + "/v1",
            codex_home=GUEST_JUDGE_CODEX_HOME,
            base_instructions=GUEST_JUDGE_BASE_INSTRUCTIONS,
            mcp_command=GUEST_MCP,
        ),
        encoding="utf-8",
    )
    prompt_path.write_text(prompt_text, encoding="utf-8")
    schema_copy.write_bytes(Path(settings.schema_file).resolve().read_bytes())
    base_copy.write_bytes(base_instructions_file.read_bytes())
    probe_copy.write_text(LIVE_JUDGE_PROBE_SCRIPT, encoding="utf-8")
    final_response_path.write_text(final_response, encoding="utf-8")

    request_execute(
        base_url,
        [
            "bash",
            "-lc",
            f"rm -rf {shlex.quote(GUEST_JUDGE_WORK)} "
            "&& install -d "
            f"{shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
            f"{shlex.quote(GUEST_JUDGE_ROOT)} "
            f"{shlex.quote(GUEST_JUDGE_ROOT + '/artifacts')} "
            f"{shlex.quote(GUEST_JUDGE_WORK)} "
            f"&& test -w {shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
            f"&& test -w {shlex.quote(GUEST_JUDGE_ROOT)} "
            f"&& test -w {shlex.quote(GUEST_JUDGE_WORK)}",
        ],
    )
    upload_rows = [
        {"local_path": str(config_path), "path": GUEST_JUDGE_CONFIG},
        {"local_path": str(base_copy), "path": GUEST_JUDGE_BASE_INSTRUCTIONS},
        {"local_path": str(prompt_path), "path": GUEST_JUDGE_PROMPT},
        {"local_path": str(schema_copy), "path": GUEST_JUDGE_SCHEMA},
        {
            "local_path": str(GUEST_FILES_DIR / "pty_stream.py"),
            "path": GUEST_JUDGE_PTY_STREAM,
        },
        {
            "local_path": str(final_response_path),
            "path": f"{GUEST_JUDGE_ROOT}/task_agent_final_response.txt",
        },
        {
            "local_path": str(execution_evidence_path),
            "path": f"{GUEST_JUDGE_ROOT}/task_agent_execution_evidence.jsonl",
        },
        {"local_path": str(probe_copy), "path": GUEST_JUDGE_PROBE},
        {
            "local_path": str(judge_dir / "pre_judge.a11y.txt"),
            "path": f"{GUEST_JUDGE_ROOT}/final_state.a11y.txt",
        },
        {
            "local_path": str(judge_dir / "pre_judge.json"),
            "path": f"{GUEST_JUDGE_ROOT}/final_state.json",
        },
    ]
    if (judge_dir / "pre_judge.png").is_file():
        upload_rows.append({
            "local_path": str(judge_dir / "pre_judge.png"),
            "path": f"{GUEST_JUDGE_ROOT}/final_state.png",
        })
    prior_judge_event_count = 0
    if prior_judge_evidence_path is not None:
        prior_judge_evidence_path = Path(prior_judge_evidence_path)
        if not prior_judge_evidence_path.is_file():
            raise FileNotFoundError(prior_judge_evidence_path)
        prior_judge_event_count = sum(
            1
            for line in prior_judge_evidence_path.read_text(
                errors="replace",
            ).splitlines()
            if line.strip()
        )
        upload_rows.append({
            "local_path": str(prior_judge_evidence_path),
            "path": GUEST_PRIOR_JUDGE_EVIDENCE,
        })
    env.setup_controller._upload_file_setup(upload_rows)
    request_execute(
        base_url,
        [
            "bash",
            "-lc",
            f"chmod 755 {shlex.quote(GUEST_JUDGE_PROBE)} "
            f"{shlex.quote(GUEST_JUDGE_PTY_STREAM)} "
            f"&& test -x {shlex.quote(GUEST_JUDGE_PROBE)} "
            f"&& test -x {shlex.quote(GUEST_JUDGE_PTY_STREAM)} "
            "&& test -r "
            f"{shlex.quote(GUEST_JUDGE_ROOT + '/task_agent_execution_evidence.jsonl')}",
        ],
    )
    return {
        "container_path": GUEST_JUDGE_ROOT,
        "execution_evidence_file": (
            f"{GUEST_JUDGE_ROOT}/task_agent_execution_evidence.jsonl"
        ),
        "execution_event_count": execution_event_count,
        "prior_judge_execution_evidence_file": (
            GUEST_PRIOR_JUDGE_EVIDENCE
            if prior_judge_evidence_path is not None
            else None
        ),
        "prior_judge_execution_event_count": prior_judge_event_count,
        "uploaded_files": [Path(row["path"]).name for row in upload_rows],
    }


def judge_command(*, attempt_token: str, timeout: int, with_image: bool) -> str:
    """Shell command that runs one Judge attempt and records its exit state."""
    image_arg = (
        f"-i {shlex.quote(GUEST_JUDGE_ROOT + '/final_state.png')} "
        if with_image
        else ""
    )
    return (
        "set +e; "
        "export HOME=/home/user "
        f"CODEX_HOME={shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
        f"CODEX_SQLITE_HOME={shlex.quote(GUEST_JUDGE_CODEX_HOME)} "
        "PYTHONDONTWRITEBYTECODE=1 "
        "GIT_OPTIONAL_LOCKS=0; "
        f"cd {shlex.quote(GUEST_JUDGE_WORK)}; "
        f"rm -f {shlex.quote(GUEST_JUDGE_RESULT)} "
        f"{shlex.quote(GUEST_JUDGE_TRACE)} "
        f"{shlex.quote(GUEST_JUDGE_STDERR)} "
        f"{shlex.quote(GUEST_JUDGE_STATUS)}; "
        f"attempt_token={shlex.quote(attempt_token)}; "
        "started=$(date +%s); "
        "printf '{\"state\":\"running\",\"attempt_token\":\"%s\","
        "\"started_at_epoch\":%s}\\n' "
        "\"$attempt_token\" \"$started\" "
        f"> {shlex.quote(GUEST_JUDGE_STATUS)}; "
        f"{shlex.quote(GUEST_JUDGE_PTY_STREAM)} "
        f"--stdin {shlex.quote(GUEST_JUDGE_PROMPT)} "
        f"--stderr {shlex.quote(GUEST_JUDGE_STDERR)} -- "
        f"timeout --signal=TERM --kill-after=10s {timeout}s "
        f"{shlex.quote(GUEST_CODEX)} exec "
        "--strict-config --dangerously-bypass-approvals-and-sandbox "
        "--ephemeral --ignore-rules --skip-git-repo-check "
        f"{image_arg}"
        "--json "
        f"--output-schema {shlex.quote(GUEST_JUDGE_SCHEMA)} "
        f"--output-last-message {shlex.quote(GUEST_JUDGE_RESULT)} "
        f"- > {shlex.quote(GUEST_JUDGE_TRACE)}; "
        "rc=$?; signal=0; "
        "if [ \"$rc\" -ge 128 ]; then signal=$((rc-128)); fi; "
        "finished=$(date +%s); "
        "printf '{\"state\":\"exited\",\"attempt_token\":\"%s\","
        "\"started_at_epoch\":%s,\"finished_at_epoch\":%s,"
        "\"returncode\":%s,\"signal\":%s}\\n' "
        "\"$attempt_token\" \"$started\" \"$finished\" \"$rc\" \"$signal\" "
        f"> {shlex.quote(GUEST_JUDGE_STATUS)}; "
        "echo $rc"
    )


def launch_judge(
    *,
    base_url: str,
    judge_dir: Path,
    run_tag: str,
    command: str,
    timeout: int,
    tool_limit: int,
    attempt_token: str,
) -> dict:
    """Run the Judge workload, then fetch its artifacts even if it failed."""
    execution_error = None
    monitor = {
        "status": None,
        "completed_tools": 0,
        "tool_limit": tool_limit,
        "finalization_at": max(1, tool_limit - 4),
        "finalization_threshold_reached": False,
        "tool_budget_exhausted": False,
        "monitor_duration_seconds": 0.0,
    }
    fetched: dict[str, bytes | None] = {}
    artifact_fetch_errors = {}
    judge_unit_name = f"osw-judge-{run_tag}"
    try:
        request_execute(
            base_url,
            transient_workload_command(
                ["bash", "-lc", command],
                unit_name=judge_unit_name,
                wait=False,
            ),
            timeout=30,
        )
        monitor = monitor_judge_workload(
            base_url=base_url,
            unit_name=judge_unit_name,
            timeout_seconds=timeout,
            tool_limit=tool_limit,
            attempt_token=attempt_token,
        )
    except Exception as exc:
        execution_error = f"{type(exc).__name__}: {exc}"
    finally:
        for guest_path, local_name in (
            (GUEST_JUDGE_RESULT, "verdict.json"),
            (GUEST_JUDGE_TRACE, "agent.jsonl"),
            (GUEST_JUDGE_STDERR, "stderr.log"),
            (GUEST_JUDGE_STATUS, "status.json"),
        ):
            fetched[local_name], fetch_error = save_guest_artifact_best_effort(
                base_url,
                guest_path,
                judge_dir / local_name,
            )
            if fetch_error:
                artifact_fetch_errors[local_name] = fetch_error
    return {
        "execution_error": execution_error,
        "monitor": monitor,
        "verdict_raw": fetched["verdict.json"],
        "status_raw": fetched["status.json"],
        "artifact_fetch_errors": artifact_fetch_errors,
    }


def parse_json_bytes(raw: bytes | None):
    if not raw:
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return None


def run_live_judge(
    *,
    env: DesktopEnv,
    base_url: str,
    result_dir: Path,
    settings: JudgeSettings,
    run_tag: str,
    instruction: str,
    final_response: str,
    declared_infeasible: bool,
    base_instructions_file: Path,
    current_date: str,
    timezone_name: str,
    user_context: dict | None = None,
    retry_context: str | None = None,
    tool_limit: int | None = None,
    raw_evaluator_completed_before_judge: bool = True,
    sealed_before: dict | None = None,
    prior_judge_evidence_path: Path | None = None,
) -> dict:
    """Run one Judge attempt and write its metadata to judge/metadata.json."""
    judge_dir = result_dir / "judge"
    judge_dir.mkdir(parents=True, exist_ok=True)
    before = sealed_before or capture_judge_state(
        env, base_url, judge_dir, "pre_judge"
    )
    for required in (
        Path(settings.prompt_file).resolve(),
        Path(settings.schema_file).resolve(),
    ):
        if not required.is_file():
            raise FileNotFoundError(required)
    trace_path = result_dir / "agent.jsonl"
    if not trace_path.is_file():
        raise FileNotFoundError(
            "Task Agent execution trace is required before Judge staging: "
            f"{trace_path}"
        )
    effective_tool_limit = max(
        1,
        int(tool_limit if tool_limit is not None else settings.tool_limit),
    )
    sealed_evidence = stage_judge_inputs(
        env=env,
        base_url=base_url,
        judge_dir=judge_dir,
        trace_path=trace_path,
        settings=settings,
        run_tag=run_tag,
        prompt_text=judge_prompt_text(
            settings,
            instruction=instruction,
            final_response=final_response,
            declared_infeasible=declared_infeasible,
            current_date=current_date,
            timezone_name=timezone_name,
            user_context=user_context,
            tool_limit=effective_tool_limit,
            retry_context=retry_context,
        ),
        base_instructions_file=base_instructions_file,
        final_response=final_response,
        prior_judge_evidence_path=prior_judge_evidence_path,
    )

    timeout = max(1, int(settings.timeout))
    started = time.monotonic()
    attempt_token = hashlib.sha256(
        f"{run_tag}:{time.time_ns()}".encode()
    ).hexdigest()[:24]
    launched = launch_judge(
        base_url=base_url,
        judge_dir=judge_dir,
        run_tag=run_tag,
        command=judge_command(
            attempt_token=attempt_token,
            timeout=timeout,
            with_image=(judge_dir / "pre_judge.png").is_file(),
        ),
        timeout=timeout,
        tool_limit=effective_tool_limit,
        attempt_token=attempt_token,
    )
    monitor = launched["monitor"]
    execution_error = launched["execution_error"]
    status = parse_json_bytes(launched["status_raw"])
    returncode = None
    if isinstance(status, dict) and isinstance(status.get("returncode"), int):
        returncode = status["returncode"]
    elif (
        isinstance(monitor.get("status"), dict)
        and isinstance(monitor["status"].get("returncode"), int)
    ):
        returncode = monitor["status"]["returncode"]
    verdict = parse_json_bytes(launched["verdict_raw"])

    after = capture_judge_state(env, base_url, judge_dir, "post_judge")
    integrity = compare_judge_states(before, after, judge_dir)
    judge_trace_path = judge_dir / "agent.jsonl"
    mutation_audit = live_judge_mutation_audit(
        judge_trace_path.read_text(encoding="utf-8", errors="replace")
        if judge_trace_path.is_file()
        else ""
    )
    state_capture_errors = [
        error
        for error in (
            before.get("file_manifest_error"),
            after.get("file_manifest_error"),
        )
        if error
    ]
    failure_classification = classify_judge_attempt(
        returncode=returncode,
        status=status,
        execution_error=(
            "; ".join(state_capture_errors)
            if state_capture_errors
            else execution_error
        ),
        verdict=verdict,
        tool_budget_exhausted=bool(monitor.get("tool_budget_exhausted")),
    )
    metadata = {
        "judge_mode": "interactive_outcome",
        "model": settings.model,
        "effort": settings.effort,
        "duration_seconds": round(time.monotonic() - started, 1),
        "returncode": returncode,
        "status": status,
        "execution_error": execution_error,
        "state_capture_errors": state_capture_errors,
        "monitor": monitor,
        "attempt_token": attempt_token,
        "artifact_fetch_errors": launched["artifact_fetch_errors"],
        "failure_classification": failure_classification,
        "sealed_evidence": sealed_evidence,
        "verdict": verdict,
        "strict_instruction_verdict": (
            verdict.get("instruction_verdict")
            if valid_judge_verdict(verdict)
            else None
        ),
        "judge_valid": (
            returncode == 0
            and execution_error is None
            and not state_capture_errors
            and valid_judge_verdict(verdict)
        ),
        "integrity": {**integrity, "before": before, "after": after},
        "mutation_audit": mutation_audit,
        "nonvolatile_file_changes": nonvolatile_judge_changes(integrity),
        "stdout": "judge/agent.jsonl",
        "stderr": "judge/stderr.log",
        "status_file": "judge/status.json",
        "raw_evaluator_attempted_before_judge": True,
        "raw_evaluator_completed_before_judge": (
            raw_evaluator_completed_before_judge
        ),
    }
    (judge_dir / "metadata.json").write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return metadata


def run_live_judge_with_retry(**kwargs) -> dict:
    result_dir = Path(kwargs["result_dir"])
    judge_dir = result_dir / "judge"
    first = run_live_judge(**kwargs)
    first_dir = archive_judge_attempt(judge_dir, 1)
    attempts = [
        {
            "attempt": 1,
            "artifact_dir": first_dir.name,
            "judge_valid": first.get("judge_valid"),
            "failure_classification": first.get("failure_classification"),
            "duration_seconds": first.get("duration_seconds"),
        }
    ]
    selected = first
    selected_attempt = 1

    if judge_retry_eligible(first):
        retry_kwargs = dict(kwargs)
        initial_seal = (first.get("integrity") or {}).get("before")
        if (
            isinstance(initial_seal, dict)
            and initial_seal.get("file_manifest_sha256")
            and not initial_seal.get("file_manifest_error")
        ):
            retry_kwargs["sealed_before"] = initial_seal
        prior_judge_evidence_path = (
            judge_dir / "prior_judge_execution_evidence.jsonl"
        )
        prior_judge_trace_path = first_dir / "agent.jsonl"
        prior_judge_event_count = 0
        if prior_judge_trace_path.is_file():
            prior_judge_event_count = build_prior_judge_trace_evidence(
                prior_judge_trace_path,
                prior_judge_evidence_path,
            )
            retry_kwargs["prior_judge_evidence_path"] = (
                prior_judge_evidence_path
            )
        retry_kwargs["run_tag"] = f"{kwargs['run_tag']}-judge-retry-02"
        retry_kwargs["retry_context"] = (
            JUDGE_RETRY_CONVERGENCE_PROMPT
            + "\n- Prior-attempt evidence contains "
            f"{prior_judge_event_count} completed tool events."
        )
        retry_kwargs["tool_limit"] = max(
            1,
            int(kwargs["settings"].retry_tool_limit),
        )
        second = run_live_judge(**retry_kwargs)
        second_dir = archive_judge_attempt(judge_dir, 2)
        attempts.append(
            {
                "attempt": 2,
                "artifact_dir": second_dir.name,
                "judge_valid": second.get("judge_valid"),
                "failure_classification": second.get(
                    "failure_classification"
                ),
                "duration_seconds": second.get("duration_seconds"),
            }
        )
        selected = second
        selected_attempt = 2

    aggregate = dict(selected)
    aggregate["attempts"] = attempts
    aggregate["selected_attempt"] = selected_attempt
    aggregate["retry_assisted"] = selected_attempt > 1
    (judge_dir / "metadata.json").write_text(
        json.dumps(aggregate, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )
    return aggregate
