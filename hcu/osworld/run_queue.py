"""Run a bounded rolling queue of hybrid OSWorld V2 tasks.

The queue keeps a fixed number of task runs going, retries interrupted runs
and renders ``QUEUE_STATUS.md`` while it runs.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time


from hcu.paths import OSWORLD_ROOT as ROOT, REPO_ROOT, anchor_path_args
from hcu.osworld.batch_config import (
    normalized_tasks,
    runner_command,
    validate_config,
)
from hcu.osworld.preflight import DEDICATED_DOCKER_HOST
from desktop_env.providers.volume import (
    normalize_volume_size,
    validate_guest_image_capacity,
)

RUNNER_MODULE = "hcu.osworld.run_task"

DEFAULT_QUEUE = Path(
    os.environ.get(
        "HCU_V2_QUEUE",
        REPO_ROOT / "configs" / "osworld-v2-queue.example.json",
    )
)
REQUIRED_CONFIG_KEYS = (
    "model",
    "effort",
    "max_model_calls",
    "timeout_seconds",
    "max_active_tasks",
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--queue", type=Path, default=DEFAULT_QUEUE)
    parser.add_argument("--poll-interval", type=float, default=10.0)
    return parser.parse_args()


def atomic_write(path: Path, content: str) -> None:
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_text(content, encoding="utf-8")
    temp.replace(path)


def load_score(path: Path) -> float | None:
    if not path.is_file():
        return None
    try:
        return float(path.read_text(encoding="utf-8").strip())
    except (OSError, ValueError):
        return None


def load_result_payload(path: Path) -> dict | None:
    if not path.is_file():
        return None
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return payload if isinstance(payload, dict) else None




def validate_queue_guest_image_capacity(config: dict) -> dict | None:
    raw_image = str(config.get("guest_image") or "").strip()
    if not raw_image:
        return None
    tasks = config.get("tasks") or []
    requested = [
        normalize_volume_size(task.get("volume_size", 40))
        for task in tasks
    ]
    required = max((size for size in requested if size is not None), default=40)
    capacity = validate_guest_image_capacity(raw_image, required)
    return {
        "path": str(Path(raw_image).resolve()),
        "virtual_size_gib": capacity,
        "required_size_gib": required,
    }


class QueueRunner:
    def __init__(self, queue_path: Path, poll_interval: float) -> None:
        self.queue_path = queue_path.resolve()
        self.base = self.queue_path.parent
        self.config = json.loads(self.queue_path.read_text(encoding="utf-8"))
        validate_config(self.config)
        missing = [key for key in REQUIRED_CONFIG_KEYS if key not in self.config]
        if missing:
            raise ValueError(
                "Queue config is missing required keys: " + ", ".join(missing)
            )
        self.config["tasks"] = normalized_tasks(self.config)
        self.execution_root = ROOT
        self.guest_image_capacity = validate_queue_guest_image_capacity(
            self.config
        )
        configured_start = str(
            self.config.get("run_started_at_utc") or ""
        ).strip()
        self.run_started_at_utc = configured_start or time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime()
        )
        self.run_timezone = str(
            self.config.get("run_timezone") or "UTC"
        ).strip()
        self.poll_interval = poll_interval
        self.logs = self.base / "logs"
        self.hybrid = self.base / "hybrid"
        self.logs.mkdir(parents=True, exist_ok=True)
        self.hybrid.mkdir(parents=True, exist_ok=True)
        self.child_env = self.load_child_environment()
        self.child_env["PYTHONDONTWRITEBYTECODE"] = "1"
        token_log_value = str(self.config.get("token_log") or "").strip()
        if int(self.config.get("max_model_calls") or 0) > 0:
            if not token_log_value:
                raise RuntimeError(
                    "max_model_calls requires a queue-scoped token_log"
                )
            token_log = Path(token_log_value).resolve()
            token_log.parent.mkdir(parents=True, exist_ok=True)
            token_log.touch(exist_ok=True)
        self.children: dict[tuple[str, str], subprocess.Popen] = {}
        self.adopted_pids: dict[tuple[str, str], int] = {}
        self.failures: dict[tuple[str, str], str] = {}
        self.retryable_interruptions: dict[tuple[str, str], dict] = {}
        self.rate_limit_events: list[float] = []
        self.rate_limit_pause_until = 0.0
        self.stop_requested = False
        state_path = self.base / "QUEUE_STATE.json"
        if state_path.is_file():
            try:
                state = json.loads(state_path.read_text(encoding="utf-8"))
                for task_id, pid in (state.get("children") or {}).items():
                    self.adopted_pids[task_id] = int(pid)
                for task_id, error in (state.get("failures") or {}).items():
                    self.failures[task_id] = str(error)
                for task_id, interruption in (
                    state.get("retryable_interruptions") or {}
                ).items():
                    if isinstance(interruption, dict):
                        self.retryable_interruptions[task_id] = dict(interruption)
                if not configured_start and state.get("run_started_at_utc"):
                    self.run_started_at_utc = str(
                        state["run_started_at_utc"]
                    )
                if (
                    "run_timezone" not in self.config
                    and state.get("run_timezone")
                ):
                    self.run_timezone = str(state["run_timezone"])
                self.rate_limit_events = [
                    float(value)
                    for value in state.get("rate_limit_events", [])
                ]
                self.rate_limit_pause_until = float(
                    state.get("rate_limit_pause_until") or 0
                )
            except (OSError, ValueError, TypeError, json.JSONDecodeError):
                pass
        self.child_env["OSW_RUN_STARTED_AT_UTC"] = self.run_started_at_utc
        self.child_env["OSW_RUN_TIMEZONE"] = self.run_timezone
        for key, pid in list(self.adopted_pids.items()):
            if self.pid_alive(pid):
                self.failures.pop(key, None)
        self.adopt_live_attempts()
        self.recover_retryable_interruptions()
        for task in self.config["tasks"]:
            task_id = task["id"]
            if (
                self.root_path(task_id).exists()
                and not self.result_path(task_id).is_file()
                and task_id not in self.adopted_pids
                and task_id not in self.failures
            ):
                self.failures[task_id] = (
                    "attempt directory exists without a result or live process"
                )

    def load_child_environment(self) -> dict[str, str]:
        env = os.environ.copy()
        raw_env_file = self.config.get("environment_file")
        if raw_env_file:
            env_file = Path(raw_env_file).resolve()
            if not env_file.is_file():
                raise FileNotFoundError(
                    f"Configured environment file does not exist: {env_file}"
                )
            result = subprocess.run(
                [
                    "bash",
                    "-c",
                    'set -a; source "$1"; env -0',
                    "_",
                    str(env_file),
                ],
                check=True,
                capture_output=True,
            )
            env = {}
            for entry in result.stdout.split(b"\0"):
                if not entry or b"=" not in entry:
                    continue
                key, value = entry.split(b"=", 1)
                env[key.decode()] = value.decode()
        missing = [
            key
            for key in self.config.get("required_environment", [])
            if not env.get(key)
        ]
        if missing:
            raise RuntimeError(
                "Missing required queue environment: " + ", ".join(missing)
            )
        docker_host = env.get("DOCKER_HOST", "")
        if docker_host != DEDICATED_DOCKER_HOST:
            raise RuntimeError(
                "The queue requires the dedicated OSWorld Docker "
                f"daemon: expected {DEDICATED_DOCKER_HOST}, "
                f"got {docker_host or '<unset>'}"
            )
        if docker_host.startswith("unix://"):
            socket_path = Path(docker_host.removeprefix("unix://"))
            if not socket_path.exists():
                raise RuntimeError(
                    f"Configured Docker socket does not exist: {socket_path}"
                )
        return env

    def adopt_live_attempts(self) -> None:
        roots = {
            str(self.root_path(task["id"])): task["id"]
            for task in self.config["tasks"]
        }
        try:
            proc_paths = sorted(
                Path("/proc").iterdir(),
                key=lambda path: int(path.name) if path.name.isdigit() else -1,
            )
        except OSError:
            return
        for proc_path in proc_paths:
            if not proc_path.name.isdigit():
                continue
            try:
                argv = (proc_path / "cmdline").read_bytes().split(b"\0")
                args = [item.decode(errors="replace") for item in argv if item]
            except OSError:
                continue
            if not args or RUNNER_MODULE not in " ".join(args):
                continue
            command = " ".join(args)
            for root, task_id in roots.items():
                if root in command:
                    self.adopted_pids[task_id] = int(proc_path.name)
                    self.failures.pop(task_id, None)
                    break

    def result_path(self, task_id: str) -> Path:
        return self.hybrid / task_id / "result.txt"

    def root_path(self, task_id: str) -> Path:
        return self.hybrid / task_id

    def archive_retryable_interruption(
        self,
        task_id: str,
        interruption: dict,
    ) -> Path:
        """Move one interrupted attempt aside and record why it stopped."""
        timestamp = str(
            interruption.get("timestamp_utc")
            or time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        )
        timestamp_slug = "".join(
            character
            if character.isalnum() or character in {"-", "_"}
            else "-"
            for character in timestamp
        ).strip("-")
        archive_base = (
            self.base / "interrupted_attempts" / timestamp_slug / task_id
        )
        archive = archive_base
        suffix = 1
        while archive.exists():
            suffix += 1
            archive = archive_base.with_name(f"{task_id}-attempt-{suffix}")
        archive.mkdir(parents=True)

        root = self.root_path(task_id)
        if root.exists():
            root.replace(archive / "task")
        log_path = self.logs / f"{task_id}.log"
        if log_path.exists():
            log_path.replace(archive / "runner.log")
        atomic_write(
            archive / "interruption.json",
            json.dumps(
                {
                    "schema_version": 1,
                    "task_id": task_id,
                    **interruption,
                },
                indent=2,
            )
            + "\n",
        )
        return archive

    def recover_retryable_interruptions(self) -> None:
        for task_id, interruption in list(self.retryable_interruptions.items()):
            if self.result_path(task_id).is_file():
                self.retryable_interruptions.pop(task_id, None)
                continue
            pid = self.adopted_pids.get(task_id)
            if pid is not None and self.pid_alive(pid):
                self.failures.pop(task_id, None)
                continue
            self.adopted_pids.pop(task_id, None)
            self.archive_retryable_interruption(task_id, interruption)
            self.failures.pop(task_id, None)
            self.retryable_interruptions.pop(task_id, None)

    def task_status(self, task_id: str) -> str:
        if self.result_path(task_id).is_file():
            return "completed"
        if task_id in self.failures:
            return "failed"
        child = self.children.get(task_id)
        if child is not None:
            return "running" if child.poll() is None else "failed"
        pid = self.adopted_pids.get(task_id)
        if pid is not None:
            return "external-running" if self.pid_alive(pid) else "failed"
        return "pending"

    def pid_alive(pid: int) -> bool:
        try:
            os.kill(pid, 0)
            return True
        except (OSError, ProcessLookupError):
            return False

    def command(self, task: dict) -> list[str]:
        task_id = task["id"]
        return runner_command(self.config, task, self.hybrid / task_id)

    def launch(self, task: dict) -> None:
        task_id = task["id"]
        log_path = self.logs / f"{task_id}.log"
        log_handle = log_path.open("ab", buffering=0)
        env = self.child_env.copy()
        child = subprocess.Popen(
            self.command(task),
            cwd=REPO_ROOT,
            env=env,
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        log_handle.close()
        self.children[task_id] = child
        self.adopted_pids.pop(task_id, None)
        self.retryable_interruptions.pop(task_id, None)
        self.write_state()
        stagger = float(self.config.get("launch_stagger_seconds", 0))
        if stagger > 0:
            time.sleep(stagger)

    def reap(self) -> None:
        for task_id, child in list(self.children.items()):
            returncode = child.poll()
            if returncode is None:
                continue
            if not self.result_path(task_id).is_file():
                failure = f"process exited with code {returncode}"
                classification = self.failure_classification(task_id)
                if classification:
                    failure += f" ({classification})"
                self.failures[task_id] = failure
                if classification == "upstream_rate_limit":
                    self.record_rate_limit_event()
            del self.children[task_id]
        for task_id, pid in list(self.adopted_pids.items()):
            if self.result_path(task_id).is_file():
                del self.adopted_pids[task_id]
            elif not self.pid_alive(pid):
                if task_id in self.retryable_interruptions:
                    self.recover_retryable_interruptions()
                else:
                    self.failures[task_id] = (
                        f"adopted process {pid} exited without result"
                    )
                    del self.adopted_pids[task_id]

    def failure_classification(self, task_id: str) -> str | None:
        meta = self.root_path(task_id) / "meta.json"
        try:
            payload = json.loads(meta.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return None
        explicit = payload.get("failure_classification")
        if isinstance(explicit, str) and explicit:
            return explicit
        error = str(payload.get("error") or "").casefold()
        if "exceeded rate limit" in error or "upstream_rate_limit" in error:
            return "upstream_rate_limit"
        return None

    def record_rate_limit_event(self, now: float | None = None) -> None:
        timestamp = time.time() if now is None else float(now)
        window = float(
            self.config.get("rate_limit_circuit_window_seconds", 120)
        )
        threshold = int(
            self.config.get("rate_limit_circuit_threshold", 3)
        )
        pause = float(
            self.config.get("rate_limit_circuit_pause_seconds", 180)
        )
        self.rate_limit_events = [
            value
            for value in self.rate_limit_events
            if timestamp - value <= window
        ]
        self.rate_limit_events.append(timestamp)
        if len(self.rate_limit_events) >= threshold:
            self.rate_limit_pause_until = max(
                self.rate_limit_pause_until,
                timestamp + pause,
            )
        self.write_state()

    def rate_limit_pause_remaining(self, now: float | None = None) -> float:
        timestamp = time.time() if now is None else float(now)
        return max(0.0, self.rate_limit_pause_until - timestamp)

    def active_count(self) -> int:
        """Return the number of task runs this queue owns."""
        active = 0
        for task in self.config["tasks"]:
            if self.task_status(task["id"]) in {"running", "external-running"}:
                active += 1
        return active + self.external_active_count()

    def total_live_count(self) -> int:
        active = 0
        for task in self.config["tasks"]:
            if self.task_status(task["id"]) in {"running", "external-running"}:
                active += 1
        return active + self.external_active_count()

    def task_ready(self, task_id: str) -> bool:
        if self.result_path(task_id).is_file():
            return True
        meta = self.root_path(task_id) / "meta.json"
        payload = {}
        try:
            payload = json.loads(meta.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            pass
        return payload.get("status") in {
            "agent_running",
            "completed",
            "truncated",
        }

    def starting_count(self) -> int:
        starting = 0
        for task in self.config["tasks"]:
            task_id = task["id"]
            if self.task_status(task_id) in {
                "running",
                "external-running",
            } and not self.task_ready(task_id):
                starting += 1
        return starting

    def external_active_count(self) -> int:
        active = 0
        for raw_path in self.config.get("external_pid_files", []):
            path = Path(raw_path)
            try:
                pid = int(path.read_text(encoding="utf-8").strip())
            except (OSError, ValueError):
                continue
            if self.pid_alive(pid):
                active += 1
        return active

    def schedule(self) -> None:
        if not self.launch_gate_open():
            return
        capacity = self.config["max_active_tasks"] - self.active_count()
        max_total_live_tasks = self.config.get("max_total_live_tasks")
        if max_total_live_tasks is not None:
            capacity = min(
                capacity,
                int(max_total_live_tasks) - self.total_live_count(),
            )
        startup_capacity = (
            int(self.config.get("max_starting_tasks", 6))
            - self.starting_count()
        )
        launch_budget = min(capacity, startup_capacity)
        if launch_budget <= 0:
            return
        for task in self.config["tasks"]:
            if capacity <= 0 or launch_budget <= 0:
                return
            if not self.launch_gate_open():
                return
            if self.task_status(task["id"]) == "pending":
                self.launch(task)
                capacity -= 1
                launch_budget -= 1

    def launch_gate_open(self) -> bool:
        return self.rate_limit_pause_remaining() <= 0

    @staticmethod
    def display_stratum(value: object) -> str:
        """Render queue taxonomy without presenting stale diagnostics as state."""

        text = " ".join(str(value or "").split()).strip()
        if not text:
            return "unclassified"
        error_markers = (
            "blocked",
            "error",
            "failed",
            "failure",
            "exception",
            "traceback",
        )
        if any(marker in text.casefold() for marker in error_markers):
            return "legacy diagnostic cohort"
        return text.replace("|", r"\|")

    def render_status(self) -> None:
        rows = []
        completed = 0
        official_raw_scores = []
        total_tasks = len(self.config["tasks"])
        for task in self.config["tasks"]:
            cells = []
            status = self.task_status(task["id"])
            score = load_score(self.result_path(task["id"]))
            result_payload = load_result_payload(
                self.root_path(task["id"]) / "result.json"
            )
            if status == "completed":
                completed += 1
            if (
                result_payload is not None
                and result_payload.get("raw_evaluator_status") == "error"
            ):
                score_text = " ERR=evaluator"
            else:
                score_text = "" if score is None else f" RAW={score:.4f}"
                if score is not None:
                    official_raw_scores.append(score)
            cells.append(f"{status}{score_text}")
            rows.append(
                "| "
                + " | ".join(
                    [
                        task["id"],
                        self.display_stratum(task.get("stratum")),
                        *cells,
                    ]
                )
                + " |"
            )
        headers = ["Task", "Static stratum", "Hybrid"]
        lines = [
            "# Rolling OSWorld V2 Queue Status",
            "",
            f"Updated: {time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime())}",
            "",
            f"Completed tasks: {completed}/{total_tasks}",
            "Official RAW: "
            + (
                f"{sum(official_raw_scores) / len(official_raw_scores):.4f} "
                f"across {len(official_raw_scores)} completed scores"
                if official_raw_scores
                else "no completed scores"
            ),
            f"Active task runs: {self.active_count()}/"
            f"{self.config['max_active_tasks']}",
            "Total live runs: "
            f"{self.total_live_count()}/"
            f"{self.config.get('max_total_live_tasks', 'unbounded')}",
            f"Starting runs: {self.starting_count()}/"
            f"{self.config.get('max_starting_tasks', 6)}",
            f"External active processes: {self.external_active_count()}",
            f"Host load 1m: {os.getloadavg()[0]:.1f}",
            f"Available memory: {self.available_memory_gb():.1f} GiB",
            f"Rate-limit pause remaining: "
            f"{self.rate_limit_pause_remaining():.0f}s",
            f"Launch gate: {'open' if self.launch_gate_open() else 'waiting'}",
            "",
            "| " + " | ".join(headers) + " |",
            "| " + " | ".join(["---"] * len(headers)) + " |",
            *rows,
            "",
            "Logs are under `logs/`; immutable task artifacts are under `hybrid/`.",
        ]
        atomic_write(self.base / "QUEUE_STATUS.md", "\n".join(lines) + "\n")
        self.write_state()

    def write_state(self) -> None:
        state = {
            "updated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "run_started_at_utc": self.run_started_at_utc,
            "run_timezone": self.run_timezone,
            "rate_limit_events": self.rate_limit_events,
            "rate_limit_pause_until": self.rate_limit_pause_until,
            "children": {
                **{
                    task_id: pid
                    for task_id, pid in self.adopted_pids.items()
                    if self.pid_alive(pid)
                },
                **{
                    task_id: child.pid
                    for task_id, child in self.children.items()
                    if child.poll() is None
                },
            },
            "failures": dict(self.failures),
            "retryable_interruptions": dict(self.retryable_interruptions),
            "external_active_processes": self.external_active_count(),
            "guest_image_capacity": self.guest_image_capacity,
        }
        atomic_write(
            self.base / "QUEUE_STATE.json",
            json.dumps(state, indent=2) + "\n",
        )

    def is_finished(self) -> bool:
        terminal = {"completed", "failed"}
        return all(
            self.task_status(task["id"]) in terminal
            for task in self.config["tasks"]
        )

    def stop(self, _signum=None, _frame=None) -> None:
        self.stop_requested = True

    def run(self) -> int:
        signal.signal(signal.SIGINT, self.stop)
        signal.signal(signal.SIGTERM, self.stop)
        while not self.stop_requested:
            self.reap()
            self.schedule()
            self.render_status()
            if self.is_finished():
                return 0
            time.sleep(self.poll_interval)
        self.render_status()
        return 130


def main() -> int:
    args = parse_args()
    anchor_path_args(args, "queue")
    os.chdir(ROOT)
    runner = QueueRunner(args.queue, args.poll_interval)
    return runner.run()


if __name__ == "__main__":
    raise SystemExit(main())
