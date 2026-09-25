"""Drive the task agent through the turns of one task phase.

Each turn launches the guest launcher as a transient systemd unit and polls
for its turn-scoped completion record. A watchdog detects a vanished agent
or a failed guest filesystem, the model-call budget can stop a turn early,
and ``ASK_USER`` questions are answered by the task's user simulator.
"""

from __future__ import annotations

from dataclasses import dataclass
import datetime
import json
from pathlib import Path
import shlex
import sys
import time
from typing import TYPE_CHECKING

import requests

from hcu.osworld.agents import thread_id_from_trace
from hcu.osworld.diagnostics import (
    collect_guest_infra_diagnostic,
    collect_workload_unit_diagnostic,
)
from hcu.osworld.guest import (
    GUEST_DONE,
    GUEST_FINAL,
    GUEST_LAUNCHER,
    GUEST_LAUNCHER_STATUS,
    GUEST_PROMPT,
    GUEST_STDERR,
    GUEST_TRACE,
    fetch_guest_file,
    request_execute,
    request_launch,
    save_guest_artifact,
)
from hcu.osworld.results import write_local
from hcu.osworld.prompts import (
    ASK_USER_SENTINEL,
    DEFAULT_USER_RESPONSE,
    task_prompt,
    tool_argument_resume_prompt,
    transient_resume_prompt,
    user_question,
    user_response_prompt,
)
from hcu.osworld.traces import concise_failure_message, summarize_agent_trace
from hcu.osworld.usage import count_model_requests, task_model_budget_rejected

if TYPE_CHECKING:
    from desktop_env.desktop_env import DesktopEnv


AGENT_WATCHDOG_GRACE_SEC = 120.0
AGENT_WATCHDOG_INTERVAL_SEC = 30.0
AGENT_WATCHDOG_ABSENT_THRESHOLD = 2
STREAM_RETRY_LIMIT = 1
STREAM_RETRY_BACKOFF_SEC = 30.0
TOOL_ARGUMENT_RETRY_LIMIT = 1


@dataclass(frozen=True)
class TurnSettings:
    """Run options that control how agent turns are launched and stopped."""

    agent: str
    max_model_calls: int
    poll_interval: float
    max_user_simulator_turns: int


class AgentProcessLostError(RuntimeError):
    pass


class GuestFilesystemError(RuntimeError):
    pass


class AgentWatchdog:
    def __init__(
        self,
        result_dir: Path,
        *,
        workload_unit: str,
        process_name: str = "codex",
        grace_sec: float = AGENT_WATCHDOG_GRACE_SEC,
        interval_sec: float = AGENT_WATCHDOG_INTERVAL_SEC,
        absent_threshold: int = AGENT_WATCHDOG_ABSENT_THRESHOLD,
    ):
        self.result_dir = result_dir
        self.workload_unit = workload_unit
        self.process_name = process_name
        self.grace_sec = grace_sec
        self.interval_sec = interval_sec
        self.absent_threshold = absent_threshold
        self.started_at = time.monotonic()
        self.last_check_at: float | None = None
        self.consecutive_absences = 0

    def guest_state(self, base_url: str) -> str:
        result = request_execute(
            base_url,
            [
                "bash",
                "-lc",
                "options=$(findmnt -no OPTIONS / 2>/dev/null || true); "
                "available_kb=$(df -Pk / 2>/dev/null "
                "| awk 'NR==2 {print $4}'); "
                "probe=/tmp/.osworld-watchdog-write-$$; "
                "if case \",$options,\" in *,ro,*) true;; *) false;; esac; "
                "then echo filesystem_failure; "
                "elif [ \"${available_kb:-0}\" -le 0 ]; "
                "then echo disk_full; "
                "elif ! (umask 077; printf ok >\"$probe\") 2>/dev/null; "
                "then echo filesystem_failure; "
                "elif rm -f \"$probe\" 2>/dev/null "
                f"&& pgrep -x {shlex.quote(self.process_name)} >/dev/null; then "
                f"echo {shlex.quote(self.process_name)}; "
                f"elif pgrep -f '^/bin/bash {GUEST_LAUNCHER}' "
                ">/dev/null; then "
                "echo launcher; "
                "else echo absent; fi",
            ],
            timeout=30,
        )
        return str(result.get("output", "")).strip()

    def preserve(self, base_url: str, state: str, label: str) -> None:
        preserved = {}
        artifact_map = {
            GUEST_TRACE: f"{label}-agent.jsonl",
            GUEST_STDERR: f"{label}-agent.stderr",
            GUEST_FINAL: f"{label}-final.txt",
            GUEST_LAUNCHER_STATUS: f"{label}-launcher-status.json",
        }
        for guest_path, local_name in artifact_map.items():
            local_path = self.result_dir / local_name
            try:
                data = fetch_guest_file(
                    base_url,
                    guest_path,
                    attempts=3,
                    initial_backoff_sec=1.0,
                )
            except requests.RequestException:
                data = None
            if data is not None:
                local_path.write_bytes(data)
                preserved[local_name] = len(data)

        screenshot_path = self.result_dir / f"{label}-agent-process-lost.png"
        try:
            response = requests.get(f"{base_url}/screenshot", timeout=30)
            response.raise_for_status()
            screenshot_path.write_bytes(response.content)
            preserved[screenshot_path.name] = len(response.content)
        except requests.RequestException:
            pass

        infra = collect_guest_infra_diagnostic(base_url)
        infra_text_path = self.result_dir / f"{label}-guest-infra.txt"
        if infra["output"]:
            infra_text_path.write_text(
                infra["output"],
                encoding="utf-8",
            )
            preserved[infra_text_path.name] = len(
                infra["output"].encode("utf-8")
            )

        workload = collect_workload_unit_diagnostic(
            base_url,
            self.workload_unit,
        )
        workload_text_path = (
            self.result_dir / f"{label}-workload-unit.txt"
        )
        if workload["output"]:
            workload_text_path.write_text(
                workload["output"],
                encoding="utf-8",
            )
            preserved[workload_text_path.name] = len(
                workload["output"].encode("utf-8")
            )

        diagnostic = {
            "status": "agent_process_lost",
            "guest_process_state": state,
            "workload_unit": self.workload_unit,
            "workload_properties": workload["properties"],
            "workload_classifications": workload["classifications"],
            "workload_transport_error": workload["transport_error"],
            "consecutive_absences": self.consecutive_absences,
            "preserved_artifacts": preserved,
            "infra_classifications": infra["classifications"],
            "infra_transport_error": infra["transport_error"],
            "detected_at": time.strftime(
                "%Y-%m-%dT%H:%M:%SZ",
                time.gmtime(),
            ),
        }
        (self.result_dir / f"{label}-agent-process-lost.json").write_text(
            json.dumps(diagnostic, indent=2),
            encoding="utf-8",
        )

    def check(self, base_url: str, label: str) -> None:
        now = time.monotonic()
        if now - self.started_at < self.grace_sec:
            return
        if (
            self.last_check_at is not None
            and now - self.last_check_at < self.interval_sec
        ):
            return
        self.last_check_at = now
        try:
            state = self.guest_state(base_url)
        except Exception as exc:
            print(
                "Agent watchdog process check failed: "
                f"{type(exc).__name__}: {exc}",
                file=sys.stderr,
                flush=True,
            )
            return
        if state in {self.process_name, "launcher"}:
            self.consecutive_absences = 0
            return
        if state in {"filesystem_failure", "disk_full"}:
            self.preserve(base_url, state, label)
            if state == "disk_full":
                raise GuestFilesystemError(
                    "Guest root filesystem exhausted all available space "
                    "during the agent run"
                )
            raise GuestFilesystemError(
                "Guest root filesystem became read-only during the agent run"
            )
        if state != "absent":
            self.consecutive_absences = 0
            return
        self.consecutive_absences += 1
        if self.consecutive_absences < self.absent_threshold:
            return
        self.preserve(base_url, state, label)
        raise AgentProcessLostError(
            f"{self.process_name} and the guest launcher disappeared before producing "
            f"{GUEST_DONE}"
        )


def append_artifact(path: Path, data: bytes | None) -> None:
    if not data:
        return
    with path.open("ab") as handle:
        handle.write(data)
        if not data.endswith(b"\n"):
            handle.write(b"\n")


def upload_prompt(env: DesktopEnv, local_path: Path, content: str) -> None:
    write_local(local_path, content)
    env.setup_controller._upload_file_setup([
        {"local_path": str(local_path), "path": GUEST_PROMPT},
    ])


def wait_for_agent_turn(
    *,
    base_url: str,
    result_dir: Path,
    label: str,
    settings: TurnSettings,
    token_log: Path,
    affinity_task: str,
    model_request_baseline: int,
    expected_turn_token: str,
    workload_unit: str,
    deadline: float,
) -> dict:
    agent = settings.agent
    watchdog = AgentWatchdog(
        result_dir,
        workload_unit=workload_unit,
        process_name="claude" if agent == "claude-code" else "codex",
    )
    done_payload = None
    budget_exhausted = False
    while time.monotonic() < deadline:
        done_raw = fetch_guest_file(base_url, GUEST_DONE)
        if done_raw:
            candidate = json.loads(done_raw)
            if candidate.get("turn_token") == expected_turn_token:
                done_payload = candidate
                if (
                    settings.max_model_calls > 0
                    and candidate.get("returncode") not in {0, None}
                    and task_model_budget_rejected(
                        token_log,
                        affinity_task,
                    )
                ):
                    budget_exhausted = True
                break
        watchdog.check(base_url, label)
        if settings.max_model_calls > 0:
            model_requests = (
                count_model_requests(token_log, affinity_task)
                - model_request_baseline
            )
            if model_requests >= settings.max_model_calls:
                budget_exhausted = True
                break
        time.sleep(settings.poll_interval)

    truncated = done_payload is None or budget_exhausted
    if truncated:
        request_execute(
            base_url,
            [
                "bash",
                "-lc",
                "pids=$(pgrep -x "
                + ("claude" if agent == "claude-code" else "codex")
                + " || true); "
                "[ -z \"$pids\" ] || kill -TERM $pids",
            ],
            timeout=30,
        )
        time.sleep(3)
        done_raw = fetch_guest_file(base_url, GUEST_DONE)
        if done_raw:
            candidate = json.loads(done_raw)
            if candidate.get("turn_token") == expected_turn_token:
                done_payload = candidate

    trace_raw = save_guest_artifact(
        base_url,
        GUEST_TRACE,
        result_dir / f"{label}-agent.jsonl",
    )
    stderr_raw = save_guest_artifact(
        base_url,
        GUEST_STDERR,
        result_dir / f"{label}-agent.stderr",
    )
    final_raw = save_guest_artifact(
        base_url,
        GUEST_FINAL,
        result_dir / f"{label}-final.txt",
    )
    append_artifact(result_dir / "agent.jsonl", trace_raw)
    append_artifact(result_dir / "agent.stderr", stderr_raw)
    final_message = (
        final_raw.decode("utf-8", errors="replace").strip()
        if final_raw else ""
    )
    return {
        "done_payload": done_payload,
        "truncated": truncated,
        "budget_exhausted": budget_exhausted,
        "trace_raw": trace_raw,
        "stderr_raw": stderr_raw,
        "final_message": final_message,
        "thread_id": thread_id_from_trace(trace_raw, agent),
        "workload_unit": workload_unit,
    }


def run_agent_phase(
    *,
    env: DesktopEnv,
    base_url: str,
    result_dir: Path,
    prompt_path: Path,
    phase_index: int,
    phase_name: str,
    instruction: str,
    current_date: str | None,
    settings: TurnSettings,
    token_log: Path,
    affinity_task: str,
    model_request_baseline: int,
    deadline: float,
    multi_phase: bool,
) -> dict:
    agent = settings.agent
    can_ask_user = env.user_simulator is not None
    upload_prompt(
        env,
        prompt_path,
        task_prompt(
            instruction,
            current_date=current_date,
            can_ask_user=can_ask_user,
        ),
    )
    session_id = ""
    user_exchanges = []
    turns = []

    def phase_result(turn: dict) -> dict:
        return {
            "phase_index": phase_index,
            "phase_name": phase_name,
            "instruction": instruction,
            "turns": turns,
            "user_exchanges": user_exchanges,
            "final_message": turn["final_message"],
            "done_payload": turn["done_payload"],
            "truncated": turn["truncated"],
            "budget_exhausted": turn["budget_exhausted"],
        }

    turn_index = 0
    transport_retries = 0
    tool_argument_retries = 0
    while True:
        turn_index += 1
        label = f"phase-{phase_index:02d}-turn-{turn_index:02d}"
        request_execute(
            base_url,
            [
                "rm",
                "-f",
                GUEST_DONE,
                GUEST_TRACE,
                GUEST_STDERR,
                GUEST_FINAL,
            ],
            timeout=30,
        )
        command = [GUEST_LAUNCHER, "start", "", label]
        if session_id:
            command = [GUEST_LAUNCHER, "resume", session_id, label]
        workload_unit = request_launch(base_url, command)
        write_local(
            result_dir / f"{label}-workload-unit.json",
            json.dumps(
                {
                    "unit_name": workload_unit,
                    "turn_token": label,
                    "requested_at": time.strftime(
                        "%Y-%m-%dT%H:%M:%SZ",
                        time.gmtime(),
                    ),
                },
                indent=2,
            ),
        )
        turn = wait_for_agent_turn(
            base_url=base_url,
            result_dir=result_dir,
            label=label,
            settings=settings,
            token_log=token_log,
            affinity_task=affinity_task,
            model_request_baseline=model_request_baseline,
            expected_turn_token=label,
            workload_unit=workload_unit,
            deadline=deadline,
        )
        trace_summary = summarize_agent_trace(turn["trace_raw"], agent)
        observed_thread_id = turn["thread_id"] or session_id
        turns.append({
            "turn_index": turn_index,
            "label": label,
            "thread_id": observed_thread_id,
            "workload_unit": workload_unit,
            "agent_returncode": (
                turn["done_payload"].get("returncode")
                if turn["done_payload"] else None
            ),
            "truncated": turn["truncated"],
            "budget_exhausted": turn["budget_exhausted"],
            "final_message": turn["final_message"],
            "agent_error": trace_summary["agent_error"],
            "failure_classification": (
                trace_summary["failure_classification"]
            ),
            "retry_assisted": (
                transport_retries > 0 or tool_argument_retries > 0
            ),
        })
        if turn["truncated"]:
            return phase_result(turn)
        returncode = (
            turn["done_payload"].get("returncode")
            if turn["done_payload"] else None
        )
        if returncode not in {0, None}:
            failure_classification = trace_summary[
                "failure_classification"
            ]
            if (
                failure_classification == "upstream_rate_limit"
                and observed_thread_id
                and transport_retries < STREAM_RETRY_LIMIT
                and time.monotonic() + STREAM_RETRY_BACKOFF_SEC < deadline
            ):
                transport_retries += 1
                session_id = observed_thread_id
                upload_prompt(
                    env,
                    prompt_path,
                    transient_resume_prompt(
                        trace_summary["agent_error"]
                        or "upstream rate limit"
                    ),
                )
                print(
                    f"{agent} stream rate limit; resuming the same session after "
                    f"{STREAM_RETRY_BACKOFF_SEC:.0f}s "
                    f"({transport_retries}/{STREAM_RETRY_LIMIT})",
                    file=sys.stderr,
                    flush=True,
                )
                time.sleep(STREAM_RETRY_BACKOFF_SEC)
                continue
            if (
                failure_classification == "tool_argument_too_large"
                and observed_thread_id
                and tool_argument_retries < TOOL_ARGUMENT_RETRY_LIMIT
                and time.monotonic() < deadline
            ):
                tool_argument_retries += 1
                session_id = observed_thread_id
                upload_prompt(
                    env,
                    prompt_path,
                    tool_argument_resume_prompt(),
                )
                print(
                    f"{agent} shell argv exceeded the process limit; resuming "
                    "the same thread with binary-safe transfer guidance "
                    f"({tool_argument_retries}/{TOOL_ARGUMENT_RETRY_LIMIT})",
                    file=sys.stderr,
                    flush=True,
                )
                continue
            stderr_text = (
                turn["stderr_raw"].decode(
                    "utf-8",
                    errors="replace",
                ).strip()
                if turn["stderr_raw"] else ""
            )
            failure_message = concise_failure_message(
                trace_summary["agent_error"]
                or stderr_text
                or "no stderr"
            )
            raise RuntimeError(
                f"{agent} turn {turn_index} exited with return code "
                f"{returncode}: {failure_message}"
            )
        if not session_id:
            session_id = turn["thread_id"]
        question = user_question(turn["final_message"])
        if question is None:
            return phase_result(turn)
        if not session_id:
            raise RuntimeError(
                f"ASK_USER requires a persisted {agent} session ID, "
                "but none was recorded"
            )
        if (
            settings.max_user_simulator_turns > 0
            and len(user_exchanges) >= settings.max_user_simulator_turns
        ):
            raise RuntimeError(
                f"Phase {phase_index} exceeded --max-user-simulator-turns="
                f"{settings.max_user_simulator_turns}"
            )
        answer = DEFAULT_USER_RESPONSE
        if env.user_simulator is not None:
            answer = env.user_simulator.respond(question)
        exchange = {
            "phase_index": phase_index,
            "phase_name": phase_name,
            "turn_index": turn_index,
            "question": question,
            "answer": answer,
            "timestamp": datetime.datetime.now().strftime(
                "%Y%m%d@%H%M%S%f"
            ),
        }
        user_exchanges.append(exchange)
        with (result_dir / "user-simulator.jsonl").open(
            "a",
            encoding="utf-8",
        ) as handle:
            handle.write(json.dumps(exchange, ensure_ascii=False) + "\n")
        trajectory_row = {
            "step_num": turn_index,
            "action_timestamp": exchange["timestamp"],
            "action": ASK_USER_SENTINEL,
            "question": question,
            "user_answer": answer,
            "screenshot_file": None,
        }
        if multi_phase:
            trajectory_row.update({
                "phase_index": phase_index,
                "phase_name": phase_name,
            })
        with (result_dir / "traj.jsonl").open(
            "a",
            encoding="utf-8",
        ) as handle:
            handle.write(json.dumps(
                trajectory_row,
                ensure_ascii=False,
            ))
            handle.write("\n")
        upload_prompt(env, prompt_path, user_response_prompt(answer))
