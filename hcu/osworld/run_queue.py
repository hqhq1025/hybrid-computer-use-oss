"""Run a bounded rolling queue of hybrid and/or original OSWorld V2 tasks."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request


from hcu.paths import OSWORLD_ROOT as ROOT, REPO_ROOT, anchor_path_args
from hcu.osworld.host_pressure import (
    block_device_names_for_paths,
    sample_host_kernel_pressure,
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
OFFICIAL_OPENAI_MAX_STEPS = 200
OFFICIAL_OPENAI_MAX_TOKENS = 8192
OFFICIAL_OPENAI_TASK_TIMEOUT_SECONDS = 0
# Hash-bound admission options that were removed; configs using them fail
# fast instead of silently running without the requested check.
REMOVED_CONFIG_KEYS = frozenset({
    "require_implementation_binding",
    "implementation_binding",
    "expected_implementation_binding_sha256",
    "expected_implementation_binding_id",
    "precomputed_guest_image_sha256",
    "expected_guest_image_sha256",
    "require_benchmark_input_binding",
    "benchmark_release_manifest",
    "expected_benchmark_release_manifest_sha256",
    "expected_task_tree_sha256",
    "expected_task_file_count",
    "expected_asset_tree_sha256",
    "expected_asset_file_count",
    "require_frozen_execution_snapshot",
    "execution_snapshot_source_root",
    "execution_snapshot_entrypoint_root",
    "execution_snapshot_paths",
    "expected_execution_snapshot_sha256",
    "expected_execution_snapshot_file_count",
    "expected_token_proxy_sha256",
    "expected_release_manifest_sha256",
    "expected_skill_sha256",
    "expected_base_instructions_sha256",
    "tool_version",
    "release_manifest",
    "audited_evaluator_overrides",
})


def validate_official_openai_runner_alignment(config: dict) -> None:
    if not config.get("require_official_openai_runner_alignment", False):
        return
    expected = {
        "max_steps": OFFICIAL_OPENAI_MAX_STEPS,
        "max_model_calls": OFFICIAL_OPENAI_MAX_STEPS,
        "max_tokens": OFFICIAL_OPENAI_MAX_TOKENS,
        "timeout_seconds": OFFICIAL_OPENAI_TASK_TIMEOUT_SECONDS,
    }
    mismatches = {
        key: {"expected": value, "actual": config.get(key)}
        for key, value in expected.items()
        if config.get(key) != value
    }
    if mismatches:
        raise RuntimeError(
            "Queue does not match the official OpenAI runner contract: "
            + json.dumps(mismatches, sort_keys=True)
        )


def validate_token_proxy_binding(config: dict) -> dict | None:
    if not config.get("require_token_proxy_binding", False):
        return None
    gateway_base = str(config.get("gateway_base") or "").rstrip("/")
    token_log_value = str(config.get("token_log") or "").strip()
    expected_limit = int(config.get("max_model_calls") or 0)
    if not gateway_base or not token_log_value:
        raise RuntimeError(
            "Token proxy binding requires gateway_base and token_log"
        )
    try:
        with urllib.request.urlopen(
            gateway_base + "/healthz",
            timeout=10,
        ) as response:
            payload = json.load(response)
    except (
        OSError,
        urllib.error.URLError,
        json.JSONDecodeError,
    ) as exc:
        raise RuntimeError(
            f"Cannot validate token proxy binding at {gateway_base}: {exc}"
        ) from exc
    if not isinstance(payload, dict) or payload.get("ok") is not True:
        raise RuntimeError("Token proxy health response is not healthy")
    mismatches = {}
    actual_out = str(payload.get("out") or "").strip()
    if not actual_out or Path(actual_out).resolve() != Path(
        token_log_value
    ).resolve():
        mismatches["token_log"] = {
            "expected": str(Path(token_log_value).resolve()),
            "actual": actual_out or None,
        }
    actual_limit = payload.get("max_requests_per_task")
    if actual_limit != expected_limit:
        mismatches["max_requests_per_task"] = {
            "expected": expected_limit,
            "actual": actual_limit,
        }
    if mismatches:
        raise RuntimeError(
            "Token proxy binding mismatch: "
            + json.dumps(mismatches, sort_keys=True)
        )
    return payload


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


def judge_score_status(result_payload: dict | None) -> tuple[float | None, str]:
    if result_payload is None:
        return None, "missing"
    live_judge = result_payload.get("live_judge")
    if not isinstance(live_judge, dict):
        return None, "missing"
    if not live_judge.get("judge_valid", False):
        return None, "error"
    if not live_judge.get("answer_score_eligible", False):
        return None, "excluded"
    verdict = live_judge.get("strict_instruction_verdict")
    if verdict == "PASS":
        return 1.0, "scored"
    if verdict == "FAIL":
        return 0.0, "scored"
    return None, "error"


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
        removed = sorted(set(self.config) & set(REMOVED_CONFIG_KEYS))
        if removed:
            raise ValueError(
                "Queue config uses removed hash-binding keys: "
                + ", ".join(removed)
            )
        self.execution_root = ROOT
        self.guest_image_capacity = validate_queue_guest_image_capacity(
            self.config
        )
        approved_kernels = [
            str(value).strip()
            for value in self.config.get(
                "approved_host_kernel_releases",
                [],
            )
            if str(value).strip()
        ]
        if self.config.get("require_approved_host_kernel", False):
            current_kernel = platform.release()
            if current_kernel not in approved_kernels:
                raise RuntimeError(
                    "Host kernel is not approved for this formal queue: "
                    f"current={current_kernel!r}, "
                    f"approved={approved_kernels!r}"
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
        validate_official_openai_runner_alignment(self.config)
        self.sides = tuple(self.config.get("sides") or ("hybrid", "original"))
        invalid_sides = sorted(set(self.sides) - {"hybrid", "original"})
        if invalid_sides:
            raise ValueError(
                "Unsupported queue sides: " + ", ".join(invalid_sides)
            )
        if not self.sides or len(set(self.sides)) != len(self.sides):
            raise ValueError("Queue sides must be non-empty and unique")
        self.poll_interval = poll_interval
        self.logs = self.base / "logs"
        self.hybrid = self.base / "hybrid"
        self.original = self.base / "original"
        self.logs.mkdir(parents=True, exist_ok=True)
        self.hybrid.mkdir(parents=True, exist_ok=True)
        self.original.mkdir(parents=True, exist_ok=True)
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
        self.token_proxy_binding = validate_token_proxy_binding(self.config)
        self.children: dict[tuple[str, str], subprocess.Popen] = {}
        self.adopted_pids: dict[tuple[str, str], int] = {}
        self.failures: dict[tuple[str, str], str] = {}
        self.retryable_interruptions: dict[tuple[str, str], dict] = {}
        self.rate_limit_events: list[float] = []
        self.rate_limit_pause_until = 0.0
        self._host_pressure_cache = None
        self._host_pressure_cache_at = 0.0
        monitored_paths = [self.base]
        docker_root = self.config.get("docker_root_path")
        if docker_root:
            monitored_paths.append(Path(str(docker_root)))
        self.monitored_block_devices = block_device_names_for_paths(
            monitored_paths
        )
        if not self.monitored_block_devices:
            raise RuntimeError(
                "Cannot resolve block devices for host-pressure monitoring: "
                + ", ".join(str(path) for path in monitored_paths)
            )
        self._critical_pressure_signature: tuple[tuple[int, str], ...] = ()
        self._critical_pressure_first_seen_at: float | None = None
        self._critical_pressure_samples = 0
        self.stop_requested = False
        self.stop_reason = None
        state_path = self.base / "QUEUE_STATE.json"
        if state_path.is_file():
            try:
                state = json.loads(state_path.read_text(encoding="utf-8"))
                for key, pid in (state.get("children") or {}).items():
                    task_id, side = key.split(":", 1)
                    self.adopted_pids[(task_id, side)] = int(pid)
                for key, error in (state.get("failures") or {}).items():
                    task_id, side = key.split(":", 1)
                    self.failures[(task_id, side)] = str(error)
                for key, interruption in (
                    state.get("retryable_interruptions") or {}
                ).items():
                    task_id, side = key.split(":", 1)
                    if isinstance(interruption, dict):
                        self.retryable_interruptions[(task_id, side)] = dict(
                            interruption
                        )
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
        cleanup_environment = {
            "OSWORLD_CONTAINER_CLEANUP_LOCK": self.config.get(
                "container_cleanup_lock"
            ),
            "OSWORLD_CONTAINER_CLEANUP_LOCK_TIMEOUT_SECONDS": self.config.get(
                "container_cleanup_lock_timeout_seconds"
            ),
            "OSWORLD_CLEANUP_MAX_D_STATE": self.config.get(
                "max_host_d_state"
            ),
            "OSWORLD_CLEANUP_MAX_INODE_SWITCH_WBS": self.config.get(
                "max_inode_switch_wbs_workers"
            ),
            "OSWORLD_CLEANUP_RUNC_DELETE_AGE_SECONDS": self.config.get(
                "runc_delete_age_seconds"
            ),
            "OSWORLD_CLEANUP_MONITORED_BLOCK_DEVICES": ",".join(
                sorted(self.monitored_block_devices)
            ),
        }
        for key, value in cleanup_environment.items():
            if value is not None:
                self.child_env[key] = str(value)
        for key, pid in list(self.adopted_pids.items()):
            if self.pid_alive(pid):
                self.failures.pop(key, None)
        self.adopt_live_attempts()
        self.recover_retryable_interruptions()
        for task in self.config["tasks"]:
            for side in self.sides:
                key = (task["id"], side)
                if (
                    self.root_path(*key).exists()
                    and not self.result_path(*key).is_file()
                    and key not in self.adopted_pids
                    and key not in self.failures
                ):
                    self.failures[key] = (
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
                "Paired formal queues require the dedicated OSWorld Docker "
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
            str(self.root_path(task["id"], side)): (task["id"], side)
            for task in self.config["tasks"]
            for side in self.sides
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
            if not args or not any(
                marker in argument
                for argument in args
                for marker in (
                    RUNNER_MODULE,
                    "scripts/python/run_multienv_gpt_response_api.py",
                )
            ):
                continue
            command = " ".join(args)
            for root, key in roots.items():
                if root in command:
                    self.adopted_pids[key] = int(proc_path.name)
                    self.failures.pop(key, None)
                    break

    def result_path(self, task_id: str, side: str) -> Path:
        if side == "hybrid":
            return self.hybrid / task_id / "result.txt"
        return (
            self.original
            / task_id
            / "pyautogui"
            / "screenshot"
            / self.config["model"]
            / "tasks"
            / task_id
            / "result.txt"
        )

    def root_path(self, task_id: str, side: str) -> Path:
        return (self.hybrid if side == "hybrid" else self.original) / task_id

    def archive_retryable_interruption(
        self,
        key: tuple[str, str],
        interruption: dict,
    ) -> Path:
        task_id, side = key
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
            self.base
            / "interrupted_attempts"
            / timestamp_slug
            / side
            / task_id
        )
        archive = archive_base
        suffix = 1
        while archive.exists():
            suffix += 1
            archive = archive_base.with_name(f"{task_id}-attempt-{suffix}")
        archive.mkdir(parents=True)

        root = self.root_path(task_id, side)
        if root.exists():
            root.replace(archive / "task")
        log_path = self.logs / f"{side}-{task_id}.log"
        if log_path.exists():
            log_path.replace(archive / "runner.log")
        atomic_write(
            archive / "interruption.json",
            json.dumps(
                {
                    "schema_version": 1,
                    "task_id": task_id,
                    "side": side,
                    **interruption,
                },
                indent=2,
            )
            + "\n",
        )
        return archive

    def recover_retryable_interruptions(self) -> None:
        for key, interruption in list(self.retryable_interruptions.items()):
            if self.result_path(*key).is_file():
                self.retryable_interruptions.pop(key, None)
                continue
            pid = self.adopted_pids.get(key)
            if pid is not None and self.pid_alive(pid):
                self.failures.pop(key, None)
                continue
            self.adopted_pids.pop(key, None)
            self.archive_retryable_interruption(key, interruption)
            self.failures.pop(key, None)
            self.retryable_interruptions.pop(key, None)

    def side_status(self, task_id: str, side: str) -> str:
        key = (task_id, side)
        if self.result_path(task_id, side).is_file():
            return "completed"
        if key in self.failures:
            return "failed"
        child = self.children.get(key)
        if child is not None:
            return "running" if child.poll() is None else "failed"
        pid = self.adopted_pids.get(key)
        if pid is not None:
            return "external-running" if self.pid_alive(pid) else "failed"
        return "pending"

    @staticmethod
    def pid_alive(pid: int) -> bool:
        try:
            os.kill(pid, 0)
            return True
        except (OSError, ProcessLookupError):
            return False

    def command(self, task: dict, side: str) -> list[str]:
        task_id = task["id"]
        volume_size = str(task.get("volume_size", 40))
        ram_size = str(task.get("ram_size", "4G"))
        model = self.config["model"]
        effort = self.config["effort"]
        if side == "hybrid":
            agent = str(self.config.get("agent") or "codex")
            command = [
                sys.executable,
                "-m",
                RUNNER_MODULE,
                "--task-id",
                task_id,
                "--agent",
                agent,
                "--model",
                model,
                "--effort",
                effort,
                "--volume-size",
                volume_size,
                "--ram-size",
                ram_size,
                "--max-model-calls",
                str(self.config["max_model_calls"]),
                "--timeout",
                str(self.config["timeout_seconds"]),
                "--result-dir",
                str(self.hybrid / task_id),
            ]
            optional_args = (
                ("gateway_base", "--gateway-base"),
                ("claude_gateway_base", "--claude-gateway-base"),
                ("guest_image", "--guest-image"),
                ("hybrid_codex_binary", "--codex-binary"),
                (
                    "hybrid_claude_code_binary",
                    "--claude-code-binary",
                ),
                ("hybrid_mcp_binary", "--mcp-binary"),
                ("hybrid_skill_file", "--skill-file"),
                (
                    "hybrid_base_instructions_file",
                    "--base-instructions-file",
                ),
                ("judge_model", "--judge-model"),
                ("judge_effort", "--judge-effort"),
                ("judge_timeout_seconds", "--judge-timeout"),
                ("judge_tool_limit", "--judge-tool-limit"),
                ("judge_retry_tool_limit", "--judge-retry-tool-limit"),
                ("judge_prompt_file", "--judge-prompt-file"),
                ("judge_schema_file", "--judge-schema-file"),
                ("token_log", "--token-log"),
            )
            for key, flag in optional_args:
                value = self.config.get(key)
                if value:
                    command.extend([flag, str(value)])
            if self.config.get("retain_containers", False):
                command.append("--retain-container")
            return command
        return [
            sys.executable,
            str(
                self.execution_root
                / "scripts/python/run_multienv_gpt_response_api.py"
            ),
            "--provider_name",
            "docker",
            "--headless",
            "--action_space",
            "pyautogui",
            "--observation_type",
            "screenshot",
            "--model",
            model,
            "--reasoning_effort",
            effort,
            "--max_steps",
            str(self.config["max_steps"]),
            "--max_tokens",
            str(
                self.config.get(
                    "max_tokens",
                    OFFICIAL_OPENAI_MAX_TOKENS,
                )
            ),
            "--specific_task_id",
            task_id,
            "--eval_version",
            "v2",
            "--test_all_meta_path",
            "evaluation_examples/test_v2.json",
            "--test_config_base_dir",
            "evaluation_examples",
            "--result_dir",
            str(self.original / task_id),
            "--num_envs",
            "1",
            "--client_password",
            "osworld-public-evaluation",
            "--volume_size",
            volume_size,
        ]

    def original_base_url(self, task_id: str) -> str:
        run_id = str(self.config.get("run_id") or "unversioned")
        affinity = "".join(
            char if char.isalnum() or char in "._-" else "-"
            for char in f"osworld-v2-original-{run_id}-{task_id}"
        )
        gateway = os.environ.get(
            "HCU_OPENAI_GATEWAY",
            "http://172.17.0.1:8611",
        ).rstrip("/")
        return f"{gateway}/t/{affinity}/v1"

    def evaluator_base_url(self, task_id: str) -> str:
        run_id = str(self.config.get("run_id") or "unversioned")
        affinity = "".join(
            char if char.isalnum() or char in "._-" else "-"
            for char in f"osworld-v2-evaluator-{run_id}-{task_id}"
        )
        gateway = os.environ.get(
            "HCU_EVALUATOR_GATEWAY",
            "http://172.17.0.1:8612",
        ).rstrip("/")
        return f"{gateway}/t/{affinity}/v1"

    def launch(self, task: dict, side: str) -> None:
        task_id = task["id"]
        key = (task_id, side)
        log_path = self.logs / f"{side}-{task_id}.log"
        log_handle = log_path.open("ab", buffering=0)
        env = self.child_env.copy()
        if side == "original":
            env["OPENAI_API_KEY"] = "dummy"
            env["OPENAI_BASE_URL"] = self.original_base_url(task_id)
        else:
            evaluator_base = self.evaluator_base_url(task_id)
            env["OSWORLD_EVAL_MODEL_BASE_URL"] = evaluator_base
            env["OPENAI_BASE_URL"] = evaluator_base
        child = subprocess.Popen(
            self.command(task, side),
            # The hybrid runner is a module of this repository; the upstream
            # runner resolves its task paths relative to the OSWorld checkout.
            cwd=REPO_ROOT if side == "hybrid" else self.execution_root,
            env=env,
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        log_handle.close()
        self.children[key] = child
        self.adopted_pids.pop(key, None)
        self.retryable_interruptions.pop(key, None)
        self.write_state()
        stagger = float(self.config.get("launch_stagger_seconds", 0))
        if stagger > 0:
            time.sleep(stagger)

    def reap(self) -> None:
        for key, child in list(self.children.items()):
            returncode = child.poll()
            if returncode is None:
                continue
            task_id, side = key
            if not self.result_path(task_id, side).is_file():
                failure = f"process exited with code {returncode}"
                classification = self.failure_classification(task_id, side)
                if classification:
                    failure += f" ({classification})"
                self.failures[key] = failure
                if classification == "upstream_rate_limit":
                    self.record_rate_limit_event()
            del self.children[key]
        for key, pid in list(self.adopted_pids.items()):
            if self.result_path(*key).is_file():
                del self.adopted_pids[key]
            elif not self.pid_alive(pid):
                if key in self.retryable_interruptions:
                    self.recover_retryable_interruptions()
                else:
                    self.failures[key] = (
                        f"adopted process {pid} exited without result"
                    )
                    del self.adopted_pids[key]

    def failure_classification(self, task_id: str, side: str) -> str | None:
        if side != "hybrid":
            return None
        meta = self.root_path(task_id, side) / "meta.json"
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
        """Return bounded Task-side load; Judges use a separate count."""
        active = 0
        for task in self.config["tasks"]:
            for side in self.sides:
                task_id = task["id"]
                if (
                    self.side_status(task_id, side) in {
                        "running",
                        "external-running",
                    }
                    and not self.is_judge_phase(task_id, side)
                ):
                    active += 1
        return active + self.external_active_count()

    def judge_active_count(self) -> int:
        active = 0
        for task in self.config["tasks"]:
            for side in self.sides:
                task_id = task["id"]
                if (
                    self.side_status(task_id, side) in {
                        "running",
                        "external-running",
                    }
                    and self.is_judge_phase(task_id, side)
                ):
                    active += 1
        return active

    def hybrid_phase(self, task_id: str) -> str | None:
        meta = self.root_path(task_id, "hybrid") / "meta.json"
        try:
            payload = json.loads(meta.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return None
        status = payload.get("status")
        return status if isinstance(status, str) and status else None

    def is_judge_phase(self, task_id: str, side: str) -> bool:
        if side != "hybrid":
            return False
        return self.hybrid_phase(task_id) == "judge_running"

    def total_live_count(self) -> int:
        active = 0
        for task in self.config["tasks"]:
            for side in self.sides:
                if self.side_status(task["id"], side) in {
                    "running",
                    "external-running",
                }:
                    active += 1
        return active + self.external_active_count()

    def side_ready(self, task_id: str, side: str) -> bool:
        if self.result_path(task_id, side).is_file():
            return True
        if side == "hybrid":
            meta = self.root_path(task_id, side) / "meta.json"
            payload = {}
            try:
                payload = json.loads(meta.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError):
                pass
            return payload.get("status") in {
                "agent_running",
                "judge_running",
                "completed",
                "truncated",
            }
        log_path = self.logs / f"original-{task_id}.log"
        try:
            text = log_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            return False
        return (
            "Sending GPT-5.4 request" in text
            or "Sending GPT-5.5 request" in text
            or "Step 1:" in text
        )

    def starting_count(self) -> int:
        starting = 0
        for task in self.config["tasks"]:
            task_id = task["id"]
            for side in self.sides:
                if self.side_status(task_id, side) in {
                    "running",
                    "external-running",
                } and not self.side_ready(task_id, side):
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
        capacity = self.config["max_active_sides"] - self.active_count()
        max_total_live_sides = self.config.get("max_total_live_sides")
        if max_total_live_sides is not None:
            capacity = min(
                capacity,
                int(max_total_live_sides) - self.total_live_count(),
            )
        startup_capacity = (
            int(self.config.get("max_starting_sides", 6))
            - self.starting_count()
        )
        launch_budget = min(capacity, startup_capacity)
        if launch_budget <= 0:
            return

        if set(self.sides) == {"hybrid", "original"}:
            # Complete existing pairs before starting new paired work.
            for task in self.config["tasks"]:
                statuses = {
                    side: self.side_status(task["id"], side)
                    for side in self.sides
                }
                for side, other in (
                    ("hybrid", "original"),
                    ("original", "hybrid"),
                ):
                    if (
                        launch_budget > 0
                        and statuses[side] == "pending"
                        and statuses[other] != "pending"
                        and self.launch_gate_open()
                    ):
                        self.launch(task, side)
                        statuses[side] = "running"
                        capacity -= 1
                        launch_budget -= 1

            # Start new tasks only when both sides fit.
            for task in self.config["tasks"]:
                if capacity < 2 or launch_budget < 2:
                    break
                if not self.launch_gate_open():
                    break
                statuses = [
                    self.side_status(task["id"], side)
                    for side in self.sides
                ]
                if statuses == ["pending", "pending"]:
                    for side in self.sides:
                        self.launch(task, side)
                    capacity -= 2
                    launch_budget -= 2
            return

        for task in self.config["tasks"]:
            for side in self.sides:
                if capacity <= 0 or launch_budget <= 0:
                    return
                if not self.launch_gate_open():
                    return
                if self.side_status(task["id"], side) == "pending":
                    self.launch(task, side)
                    capacity -= 1
                    launch_budget -= 1

    def launch_gate_open(self) -> bool:
        if self.rate_limit_pause_remaining() > 0:
            return False
        if (
            self.config.get("host_pressure_guard_enabled", False)
            and not self.host_pressure()["healthy"]
        ):
            return False
        max_load = self.config.get("max_load_1m")
        if max_load is not None:
            try:
                if os.getloadavg()[0] > float(max_load):
                    return False
            except (AttributeError, OSError, ValueError):
                return False
        min_memory = self.config.get("min_available_memory_gb")
        if min_memory is not None:
            available = self.available_memory_gb()
            if available is None or available < float(min_memory):
                return False
        min_free_disk = self.config.get("min_free_disk_gb")
        min_free_inodes = self.config.get("min_free_inodes")
        if min_free_disk is not None or min_free_inodes is not None:
            capacity = self.filesystem_capacity(self.base)
            if capacity is None:
                return False
            free_gb, free_inodes = capacity
            if (
                min_free_disk is not None
                and free_gb < float(min_free_disk)
            ):
                return False
            if (
                min_free_inodes is not None
                and free_inodes < int(min_free_inodes)
            ):
                return False
        docker_root = self.config.get("docker_root_path")
        min_docker_free = self.config.get("min_docker_free_gb")
        if docker_root and min_docker_free is not None:
            capacity = self.filesystem_capacity(Path(docker_root))
            if capacity is None or capacity[0] < float(min_docker_free):
                return False
        return True

    def host_pressure(self) -> dict:
        now = time.monotonic()
        cache_seconds = float(
            self.config.get("host_pressure_cache_seconds", 5)
        )
        if (
            self._host_pressure_cache is not None
            and now - self._host_pressure_cache_at < cache_seconds
        ):
            return self._host_pressure_cache
        self._host_pressure_cache = sample_host_kernel_pressure(
            max_d_state=int(
                self.config.get("max_host_d_state", 128)
            ),
            max_inode_switch_wbs=int(
                self.config.get("max_inode_switch_wbs_workers", 32)
            ),
            runc_delete_age_seconds=float(
                self.config.get("runc_delete_age_seconds", 30)
            ),
            max_io_full_avg10=(
                float(self.config["max_io_full_avg10"])
                if self.config.get("max_io_full_avg10") is not None
                else None
            ),
            monitored_block_devices=self.monitored_block_devices,
        )
        self._host_pressure_cache_at = now
        return self._host_pressure_cache

    def host_pressure_requires_abort(self, pressure: dict) -> bool:
        if not self.config.get("host_pressure_guard_enabled", False):
            self.reset_critical_pressure_confirmation()
            return False
        if not self.config.get("host_pressure_abort_enabled", True):
            self.reset_critical_pressure_confirmation()
            return False
        if pressure.get("aged_runc_delete"):
            self.reset_critical_pressure_confirmation()
            return True
        try:
            if int(pressure.get("d_state") or 0) > int(
                self.config.get("max_host_d_state", 128)
            ):
                self.reset_critical_pressure_confirmation()
                return True
        except (TypeError, ValueError):
            self.reset_critical_pressure_confirmation()
            return True
        critical = pressure.get("critical_d_state") or []
        if not critical:
            self.reset_critical_pressure_confirmation()
            return False
        has_rcu_critical = any(
            str(record.get("comm") or "").startswith("rcu_exp_gp")
            for record in critical
        )
        has_xfs_critical = any(
            str(record.get("comm") or "").startswith("xfsaild/")
            for record in critical
        )
        min_xfs_d_state = max(
            1,
            int(
                self.config.get(
                    "critical_host_pressure_abort_min_d_state",
                    16,
                )
            ),
        )
        d_state = int(pressure.get("d_state") or 0)
        pressure["abort_eligibility"] = {
            "has_rcu_critical": has_rcu_critical,
            "has_xfs_critical": has_xfs_critical,
            "d_state": d_state,
            "min_xfs_d_state": min_xfs_d_state,
        }
        if has_xfs_critical and not has_rcu_critical and d_state < min_xfs_d_state:
            self.reset_critical_pressure_confirmation()
            return False
        signature = tuple(sorted(
            (
                int(record.get("pid") or 0),
                str(record.get("comm") or ""),
            )
            for record in critical
        ))
        now = time.monotonic()
        if signature != self._critical_pressure_signature:
            self._critical_pressure_signature = signature
            self._critical_pressure_first_seen_at = now
            self._critical_pressure_samples = 1
        else:
            self._critical_pressure_samples += 1
        first_seen = self._critical_pressure_first_seen_at or now
        elapsed = max(0.0, now - first_seen)
        required_samples = max(
            1,
            int(
                self.config.get(
                    "critical_host_pressure_abort_confirmation_samples",
                    3,
                )
            ),
        )
        required_seconds = max(
            0.0,
            float(
                self.config.get(
                    "critical_host_pressure_abort_confirmation_seconds",
                    15,
                )
            ),
        )
        pressure["abort_confirmation"] = {
            "signature": [
                {"pid": pid, "comm": comm}
                for pid, comm in signature
            ],
            "samples": self._critical_pressure_samples,
            "required_samples": required_samples,
            "elapsed_seconds": round(elapsed, 3),
            "required_seconds": required_seconds,
        }
        return (
            self._critical_pressure_samples >= required_samples
            and elapsed >= required_seconds
        )

    def reset_critical_pressure_confirmation(self) -> None:
        self._critical_pressure_signature = ()
        self._critical_pressure_first_seen_at = None
        self._critical_pressure_samples = 0

    def abort_owned_children_for_host_pressure(
        self,
        pressure: dict,
    ) -> None:
        if self.stop_reason is not None:
            return
        self.stop_reason = "critical_host_kernel_pressure"
        terminated = []
        timestamp_utc = time.strftime(
            "%Y-%m-%dT%H:%M:%SZ",
            time.gmtime(),
        )
        for key, child in list(self.children.items()):
            if child.poll() is not None:
                continue
            child.terminate()
            record = {
                "task_id": key[0],
                "side": key[1],
                "pid": child.pid,
                "source": "owned_child",
            }
            terminated.append(record)
            self.retryable_interruptions[key] = {
                **record,
                "timestamp_utc": timestamp_utc,
                "reason": self.stop_reason,
            }
        for key, pid in list(self.adopted_pids.items()):
            if not self.pid_alive(pid):
                continue
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                continue
            record = {
                "task_id": key[0],
                "side": key[1],
                "pid": pid,
                "source": "adopted_child",
            }
            terminated.append(record)
            self.retryable_interruptions[key] = {
                **record,
                "timestamp_utc": timestamp_utc,
                "reason": self.stop_reason,
            }
        payload = {
            "schema_version": 1,
            "timestamp_utc": timestamp_utc,
            "reason": self.stop_reason,
            "pressure": pressure,
            "terminated": terminated,
            "container_cleanup_attempted": False,
        }
        atomic_write(
            self.base / "HOST_PRESSURE_ABORT.json",
            json.dumps(payload, indent=2) + "\n",
        )
        self.stop_requested = True

    @staticmethod
    def available_memory_gb() -> float | None:
        try:
            with open("/proc/meminfo", encoding="utf-8") as handle:
                for line in handle:
                    if line.startswith("MemAvailable:"):
                        kib = int(line.split()[1])
                        return kib / 1024 / 1024
        except (OSError, ValueError, IndexError):
            return None
        return None

    @staticmethod
    def filesystem_capacity(path: Path) -> tuple[float, int] | None:
        try:
            stats = os.statvfs(path)
        except OSError:
            return None
        free_gb = stats.f_bavail * stats.f_frsize / 1024**3
        return free_gb, stats.f_favail

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
        host_pressure = self.host_pressure()
        rows = []
        complete_sides = 0
        official_raw_scores = []
        judge_scores = []
        judge_error_count = 0
        judge_excluded_count = 0
        total_sides = len(self.config["tasks"]) * len(self.sides)
        for task in self.config["tasks"]:
            cells = []
            for side in self.sides:
                status = self.side_status(task["id"], side)
                score = load_score(self.result_path(task["id"], side))
                result_payload = (
                    load_result_payload(
                        self.root_path(task["id"], side) / "result.json"
                    )
                    if side == "hybrid"
                    else None
                )
                if status == "completed":
                    complete_sides += 1
                if (
                    result_payload is not None
                    and result_payload.get("raw_evaluator_status") == "error"
                ):
                    score_text = " ERR=evaluator"
                else:
                    score_text = (
                        "" if score is None else f" RAW={score:.4f}"
                    )
                    if score is not None:
                        official_raw_scores.append(score)
                judge_text = ""
                if side == "hybrid" and self.config.get("judge_model"):
                    judge_score, judge_status = judge_score_status(
                        result_payload
                    )
                    if judge_status == "scored":
                        judge_scores.append(judge_score)
                        judge_text = f" JUDGE={judge_score:.0f}"
                    elif status == "completed":
                        if judge_status == "excluded":
                            judge_excluded_count += 1
                            judge_text = " JUDGE=EXCLUDED"
                        else:
                            judge_error_count += 1
                            judge_text = " JUDGE=ERR"
                cells.append(f"{status}{score_text}{judge_text}")
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
        side_headers = [
            "Hybrid" if side == "hybrid" else "Original GUI"
            for side in self.sides
        ]
        headers = ["Task", "Static stratum", *side_headers]
        lines = [
            "# Rolling OSWorld V2 Queue Status",
            "",
            f"Updated: {time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime())}",
            "",
            f"Completed sides: {complete_sides}/{total_sides}",
            "Official RAW: "
            + (
                f"{sum(official_raw_scores) / len(official_raw_scores):.4f} "
                f"across {len(official_raw_scores)} completed scores"
                if official_raw_scores
                else "no completed scores"
            ),
            "Agent-as-Judge: "
            + (
                f"{sum(judge_scores):.0f}/{len(judge_scores)} PASS "
                f"({sum(judge_scores) / len(judge_scores):.4f})"
                if judge_scores
                else "no eligible verdicts"
            )
            + (
                f"; invalid={judge_error_count}, "
                f"excluded={judge_excluded_count}"
                if judge_error_count or judge_excluded_count
                else ""
            ),
            f"Active Task sides: {self.active_count()}/"
            f"{self.config['max_active_sides']}",
            f"Active Judges: {self.judge_active_count()}",
            "Total live sides: "
            f"{self.total_live_count()}/"
            f"{self.config.get('max_total_live_sides', 'unbounded')}",
            f"Starting sides: {self.starting_count()}/"
            f"{self.config.get('max_starting_sides', 6)}",
            f"External active processes: {self.external_active_count()}",
            f"Host load 1m: {os.getloadavg()[0]:.1f}",
            f"Available memory: {self.available_memory_gb():.1f} GiB",
            f"Rate-limit pause remaining: "
            f"{self.rate_limit_pause_remaining():.0f}s",
            "Host kernel pressure: "
            + (
                "healthy"
                if host_pressure["healthy"]
                else "blocked: " + ", ".join(host_pressure["reasons"])
            ),
            f"Launch gate: {'open' if self.launch_gate_open() else 'waiting'}",
            "",
            "| " + " | ".join(headers) + " |",
            "| " + " | ".join(["---"] * len(headers)) + " |",
            *rows,
            "",
            "Logs are under `logs/`; immutable task artifacts are under `hybrid/` "
            "and `original/`.",
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
                    f"{task_id}:{side}": pid
                    for (task_id, side), pid in self.adopted_pids.items()
                    if self.pid_alive(pid)
                },
                **{
                    f"{task_id}:{side}": child.pid
                    for (task_id, side), child in self.children.items()
                    if child.poll() is None
                },
            },
            "failures": {
                f"{task_id}:{side}": error
                for (task_id, side), error in self.failures.items()
            },
            "retryable_interruptions": {
                f"{task_id}:{side}": interruption
                for (task_id, side), interruption
                in self.retryable_interruptions.items()
            },
            "external_active_processes": self.external_active_count(),
            "host_pressure": self.host_pressure(),
            "host_kernel_release": platform.release(),
            "stop_reason": self.stop_reason,
            "guest_image_capacity": self.guest_image_capacity,
            "token_proxy_binding": self.token_proxy_binding,
        }
        atomic_write(
            self.base / "QUEUE_STATE.json",
            json.dumps(state, indent=2) + "\n",
        )

    def is_finished(self) -> bool:
        terminal = {"completed", "failed"}
        return all(
            self.side_status(task["id"], side) in terminal
            for task in self.config["tasks"]
            for side in self.sides
        )

    def stop(self, _signum=None, _frame=None) -> None:
        self.stop_requested = True

    def run(self) -> int:
        signal.signal(signal.SIGINT, self.stop)
        signal.signal(signal.SIGTERM, self.stop)
        while not self.stop_requested:
            self.reap()
            pressure = self.host_pressure()
            if self.host_pressure_requires_abort(pressure):
                self.abort_owned_children_for_host_pressure(pressure)
                break
            self.schedule()
            self.render_status()
            if self.is_finished():
                return 0
            time.sleep(self.poll_interval)
        self.render_status()
        return 75 if self.stop_reason else 130


def main() -> int:
    args = parse_args()
    anchor_path_args(args, "queue")
    os.chdir(ROOT)
    runner = QueueRunner(args.queue, args.poll_interval)
    return runner.run()


if __name__ == "__main__":
    raise SystemExit(main())
