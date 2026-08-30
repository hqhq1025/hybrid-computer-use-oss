#!/usr/bin/env python3
"""osw-hybrid 并行跑测：一个任务 = 一个容器 + 一个 codex 进程，互不混用。

隔离边界（每个任务独占，不共享任何状态）：
  - 独立容器：独立 Xvfb / dbus / AT-SPI / 应用进程，端口由 provider 加锁分配
  - 独立 codex 进程：MCP 是容器内本地 stdio，快照状态在 MCP 进程内，天然不跨任务
  - 独立 trace 目录：codex.jsonl / resource.jsonl / result.json / artifacts

跑完即销毁容器，下一个任务重新创建——等价于官方 docker provider 的 revert 语义。

失败任务同样写 result.json（带 error 字段）。这一点是刻意的：OSWorld 官方
show_result.py 只统计有 result.txt 的目录，崩溃任务会从分子分母里一起消失、
把成功率抬高。这里让失败留痕，报告时分母才是真的。
"""

import argparse
import concurrent.futures as cf
import glob
import hashlib
import json
import math
import os
import queue
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid

from evaluator_audit import summarize_provisional_audited
from evaluation_policy import (
    GOOGLE_DRIVE_TASK_IDS,
    assert_formal_full_task_set,
    policy_exclusion,
    task_set_record,
)
from tool_release import (
    assert_tool_candidate_image,
    assert_tool_release_image,
    current_tool_version,
    result_tool_candidate_metadata,
    result_tool_metadata,
    tool_candidate_record,
    tool_release_record,
)

ROOT = os.path.dirname(os.path.abspath(__file__))
DEFAULT_CLAUDE_PROXY_TOKEN_LOG = (
    os.environ.get(
        "HCU_CLAUDE_TOKEN_LOG",
        os.path.join(ROOT, "runs", "claude-tokens.jsonl"),
    )
)


def resolve_proxy_token_log(agent, configured, environ=None):
    if configured or agent not in {"claude", "claude-code"}:
        return configured
    env = os.environ if environ is None else environ
    return env.get(
        "OSW_CLAUDE_PROXY_TOKEN_LOG",
        DEFAULT_CLAUDE_PROXY_TOKEN_LOG,
    )


# 记录哪些槽位已经历过冷启动，避免复用槽位时重复延迟
_slot_warmed = {}
_stop_requested = threading.Event()
_active_processes = {}
_active_processes_lock = threading.Lock()
FULL_SCORE_ABS_TOLERANCE = 1e-9


def classify_score(score):
    if isinstance(score, bool) or not isinstance(score, (int, float)):
        return "ERR"
    if not math.isfinite(score):
        return "ERR"
    if not 0.0 <= score <= 1.0:
        return "ERR"
    if math.isclose(
        score,
        1.0,
        rel_tol=0.0,
        abs_tol=FULL_SCORE_ABS_TOLERANCE,
    ):
        return "PASS"
    if score == 0.0:
        return "FAIL"
    if 0.0 < score < 1.0:
        return "PARTIAL"
    return "ERR"


def classify_result(result):
    if result.get("runner_timed_out"):
        return "ERR"
    if result.get("truncated"):
        return "ERR"
    returncode = result.get("runner_returncode")
    if returncode is not None and returncode != 0:
        return "ERR"
    return classify_score(result.get("score"))


def summarize_results(results):
    summary = {"PASS": 0, "FAIL": 0, "PARTIAL": 0, "ERR": 0}
    for result in results:
        summary[classify_result(result)] += 1
    return summary


def summarize_diagnostics(results):
    return {
        "truncated": sum(bool(result.get("truncated")) for result in results),
        "agent_timed_out": sum(
            bool(result.get("agent_timed_out") or result.get("timed_out"))
            for result in results
        ),
        "timed_out": sum(
            bool(result.get("agent_timed_out") or result.get("timed_out"))
            for result in results
        ),
        "runner_timed_out": sum(
            bool(result.get("runner_timed_out")) for result in results
        ),
        "setup_failure": sum(
            bool(
                result.get("setup_failure")
                or result.get("setup_failed")
                or result.get("status") in {"setup_failure", "setup_failed"}
            )
            for result in results
        ),
        "with_error": sum(bool(result.get("error")) for result in results),
    }


def task_ids_sha256(values):
    payload = "\n".join(sorted(set(values))).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def validate_completed_results(results, expected_task_ids, require_judge=False):
    expected = [str(task_id) for task_id in expected_task_ids]
    actual = [str(result.get("task_id") or "") for result in results]
    expected_set = set(expected)
    actual_set = set(actual)
    duplicate_task_ids = sorted(
        task_id for task_id in actual_set if actual.count(task_id) > 1
    )
    setup_failures = []
    invalid_judges = []
    truncated = []
    for result, task_id in zip(results, actual):
        setup_failure = bool(
            result.get("setup_failure")
            or result.get("setup_failed")
            or result.get("status") in {"setup_failure", "setup_failed"}
        )
        if setup_failure:
            setup_failures.append(task_id)
        if require_judge and not setup_failure:
            judge = result.get("live_judge")
            if (
                not isinstance(judge, dict)
                or judge.get("judge_valid") is not True
            ):
                invalid_judges.append(task_id)
        if bool(result.get("truncated")):
            truncated.append(task_id)

    missing_task_ids = sorted(expected_set - actual_set)
    unexpected_task_ids = sorted(actual_set - expected_set)
    failures = []
    if len(results) != len(expected) or missing_task_ids or unexpected_task_ids:
        failures.append("task_set_mismatch")
    if duplicate_task_ids:
        failures.append("duplicate_task_ids")
    if setup_failures:
        failures.append("setup_failures")
    if invalid_judges:
        failures.append("invalid_judges")
    if truncated:
        failures.append("truncated")
    return {
        "valid": not failures,
        "failure_reasons": failures,
        "result_count": len(results),
        "expected_task_count": len(expected),
        "actual_task_ids_sha256": task_ids_sha256(actual),
        "expected_task_ids_sha256": task_ids_sha256(expected),
        "missing_task_ids": missing_task_ids,
        "unexpected_task_ids": unexpected_task_ids,
        "duplicate_task_ids": duplicate_task_ids,
        "setup_failure_count": len(setup_failures),
        "setup_failure_task_ids": sorted(setup_failures),
        "invalid_judge_count": len(invalid_judges),
        "invalid_judge_task_ids": sorted(invalid_judges),
        "truncated_count": len(truncated),
        "truncated_task_ids": sorted(truncated),
    }


def trace_root_for_output(out_path):
    name = os.path.basename(out_path)
    stem = name[:-6] if name.endswith(".jsonl") else os.path.splitext(name)[0]
    stem = stem or "run"
    return os.path.join(ROOT, f"trace-{stem}")


def run_id_for_output(out_path):
    name = os.path.basename(out_path)
    stem = name[:-6] if name.endswith(".jsonl") else os.path.splitext(name)[0]
    return stem or f"run-{time.strftime('%Y%m%d-%H%M%S')}"


def run_identity_metadata(args):
    metadata = {
        "batch_run_id": getattr(args, "run_id", None),
    }
    metadata.update(
        result_tool_metadata(getattr(args, "tool_release", None))
    )
    metadata.update(
        result_tool_candidate_metadata(
            getattr(args, "tool_candidate", None)
        )
    )
    return metadata


def validate_test_contract(environ=None):
    environ = os.environ if environ is None else environ
    missing = []
    if environ.get("OSW_ENABLE_SHELL") != "1":
        missing.append("OSW_ENABLE_SHELL=1")
    if not str(environ.get("OSW_SKILL_FILE") or "").strip():
        missing.append("OSW_SKILL_FILE=<benchmark skill>")
    if missing:
        raise ValueError(
            "formal OSW hybrid tests require Bash and skill injection; missing "
            + ", ".join(missing)
        )


def validate_tool_release_runtime(version, image):
    try:
        image_id = subprocess.check_output(
            ["docker", "image", "inspect", image, "--format", "{{.Id}}"],
            text=True,
            timeout=30,
        ).strip()
    except (
        OSError,
        subprocess.CalledProcessError,
        subprocess.TimeoutExpired,
    ) as exc:
        raise RuntimeError(
            f"cannot resolve runtime image {image!r}: {exc}"
        ) from exc
    if not image_id:
        raise RuntimeError(f"runtime image {image!r} resolved to an empty ID")
    try:
        assert_tool_release_image(version, image, image_id)
    except ValueError as exc:
        raise RuntimeError(str(exc)) from exc
    return image_id


def validate_tool_candidate_runtime(manifest, image):
    try:
        image_id = subprocess.check_output(
            ["docker", "image", "inspect", image, "--format", "{{.Id}}"],
            text=True,
            timeout=30,
        ).strip()
    except (
        OSError,
        subprocess.CalledProcessError,
        subprocess.TimeoutExpired,
    ) as exc:
        raise RuntimeError(
            f"cannot resolve runtime image {image!r}: {exc}"
        ) from exc
    if not image_id:
        raise RuntimeError(f"runtime image {image!r} resolved to an empty ID")
    try:
        assert_tool_candidate_image(manifest, image, image_id)
    except ValueError as exc:
        raise RuntimeError(str(exc)) from exc
    return image_id


def validate_mcp_server_runtime(image, runtime_record):
    command = str(runtime_record.get("tool_mcp_command") or "").strip()
    if not command:
        raise RuntimeError("tool manifest resolved an empty MCP command")
    requests = (
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {
                    "name": "osw-hybrid-runner-preflight",
                    "version": "1",
                },
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "tools/list",
            "params": {},
        },
    )
    payload = "".join(json.dumps(item) + "\n" for item in requests)
    try:
        completed = subprocess.run(
            [
                "docker",
                "run",
                "--rm",
                "-i",
                "--entrypoint",
                command,
                image,
                "mcp",
            ],
            input=payload,
            text=True,
            capture_output=True,
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise RuntimeError(
            f"MCP startup preflight failed for {command!r}: {exc}"
        ) from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "").strip()
        raise RuntimeError(
            f"MCP startup preflight failed for {command!r} "
            f"(exit {completed.returncode}): {detail[:1000]}"
        )
    responses = {}
    try:
        for line in completed.stdout.splitlines():
            if not line.strip():
                continue
            response = json.loads(line)
            if isinstance(response, dict) and response.get("id") in {1, 2}:
                responses[response["id"]] = response
    except json.JSONDecodeError as exc:
        raise RuntimeError(
            f"MCP startup preflight returned invalid JSON: {exc}"
        ) from exc
    initialize = responses.get(1, {})
    tools_list = responses.get(2, {})
    if initialize.get("error") or not isinstance(
        initialize.get("result", {}).get("serverInfo"), dict
    ):
        raise RuntimeError(
            "MCP startup preflight did not return a valid initialize result"
        )
    tools = tools_list.get("result", {}).get("tools")
    if not isinstance(tools, list):
        raise RuntimeError(
            "MCP startup preflight did not return a valid tools/list result"
        )
    tool_names = [tool.get("name") for tool in tools if isinstance(tool, dict)]
    if tool_names != ["AX", "GUI"]:
        raise RuntimeError(
            "MCP startup preflight expected tools ['AX', 'GUI'], got "
            f"{tool_names!r}"
        )
    return {
        "command": command,
        "server_name": initialize["result"]["serverInfo"].get("name"),
        "tools": tool_names,
    }


def _docker_network_gateway(network_name=None):
    network_name = (
        network_name
        or os.environ.get("OSW_HYBRID_NETWORK")
        or "osw-net"
    )
    try:
        payload = subprocess.check_output(
            ["docker", "network", "inspect", network_name],
            text=True,
            timeout=10,
        )
        networks = json.loads(payload)
        return networks[0]["IPAM"]["Config"][0]["Gateway"]
    except (
        OSError,
        subprocess.SubprocessError,
        json.JSONDecodeError,
        KeyError,
        IndexError,
        TypeError,
    ):
        return ""


def validate_gateway(port, host=""):
    candidates = [
        host,
        os.environ.get("OSW_GATEWAY_HOST", ""),
        "127.0.0.1",
        _docker_network_gateway(),
    ]
    errors = []
    seen = set()
    for candidate in candidates:
        candidate = str(candidate or "").strip()
        if not candidate or candidate in seen:
            continue
        seen.add(candidate)
        url = f"http://{candidate}:{port}/v1/models"
        try:
            with urllib.request.urlopen(url, timeout=10) as response:
                if response.status != 200:
                    errors.append(f"{url}: HTTP {response.status}")
                    continue
                response.read(4096)
                return url
        except (OSError, urllib.error.URLError) as exc:
            errors.append(f"{url}: {type(exc).__name__}: {exc}")
    raise RuntimeError(
        f"gateway preflight failed for port {port}; "
        + ("; ".join(errors) if errors else "no gateway host candidates")
    )


def _archive_result_path(res_path, run_id, label):
    directory = os.path.dirname(res_path)
    return os.path.join(directory, f"result.{label}-{run_id}.json")


def _isolate_existing_result(res_path, run_id, label="pre-run"):
    if not os.path.exists(res_path):
        return None
    archived_path = _archive_result_path(res_path, run_id, label)
    os.replace(res_path, archived_path)
    return archived_path


def _atomic_write_json(path, payload):
    directory = os.path.dirname(path)
    os.makedirs(directory, exist_ok=True)
    fd, tmp_path = tempfile.mkstemp(
        prefix=".result.", suffix=".tmp", dir=directory
    )
    try:
        with os.fdopen(fd, "w") as fh:
            json.dump(payload, fh, ensure_ascii=False, indent=2)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp_path, path)
    except Exception:
        try:
            os.unlink(tmp_path)
        except FileNotFoundError:
            pass
        raise


def finalize_run_metadata(
    path,
    run_metadata,
    results,
    expected_task_ids,
    raw_summary,
    diagnostics,
    *,
    require_judge=False,
    completed_at_utc=None,
):
    completion_validation = validate_completed_results(
        results,
        expected_task_ids,
        require_judge=require_judge,
    )
    run_metadata.update(
        {
            "status": (
                "completed"
                if completion_validation["valid"]
                else "completed_with_validation_failure"
            ),
            "completed_at_utc": completed_at_utc or time.strftime(
                "%Y-%m-%dT%H:%M:%SZ", time.gmtime()
            ),
            "result_count": len(results),
            "raw_evaluator": raw_summary,
            "diagnostics": diagnostics,
            "completion_validation": completion_validation,
        }
    )
    _atomic_write_json(path, run_metadata)
    return completion_validation


def load_tasks(domains, limit, task_ids):
    repo_root = os.path.abspath(os.path.join(ROOT, "..", ".."))
    osworld_root = os.path.abspath(
        os.path.expanduser(
            os.environ.get(
                "OSWORLD_V1_ROOT",
                os.path.join(repo_root, "external", "OSWorld"),
            )
        )
    )
    ex = os.path.join(osworld_root, "evaluation_examples/examples")
    out = []
    excluded = []
    for path in sorted(glob.glob(os.path.join(ex, "*/*.json"))):
        domain = os.path.basename(os.path.dirname(path))
        tid = os.path.splitext(os.path.basename(path))[0]
        if domains and domain not in domains:
            continue
        if task_ids and tid not in task_ids:
            continue
        with open(path, encoding="utf-8") as source:
            task_config = json.load(source)
        exclusion = policy_exclusion(tid, task_config)
        if exclusion:
            excluded.append((domain, tid, exclusion))
            continue
        out.append((domain, tid, f"{domain}/{tid}.json"))
    if limit:
        out = out[:limit]
    return out, excluded


def load_task_ids(tasks, tasks_file):
    if tasks and tasks_file:
        raise ValueError("use either --tasks or --tasks-file, not both")
    if not tasks_file:
        return [item.strip() for item in tasks.split(",") if item.strip()]

    task_ids = []
    with open(tasks_file, encoding="utf-8") as fh:
        for line_number, line in enumerate(fh, 1):
            line = line.strip()
            if not line:
                continue
            try:
                payload = json.loads(line)
            except json.JSONDecodeError:
                payload = None
            if isinstance(payload, dict):
                task_id = str(payload.get("task_id") or "").strip()
                if not task_id:
                    raise ValueError(
                        f"{tasks_file}:{line_number} has no task_id"
                    )
                task_ids.append(task_id)
            else:
                task_ids.extend(
                    item.strip() for item in line.split(",") if item.strip()
                )
    return list(dict.fromkeys(task_ids))


def _terminate_process_group(proc):
    if proc is None or proc.poll() is not None:
        return
    try:
        os.killpg(proc.pid, signal.SIGTERM)
    except ProcessLookupError:
        return


def _run_runner_process(cmd, env, timeout):
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        start_new_session=True,
    )
    with _active_processes_lock:
        _active_processes[proc.pid] = proc
    try:
        stdout, stderr = proc.communicate(timeout=timeout)
        return subprocess.CompletedProcess(
            cmd,
            proc.returncode,
            stdout,
            stderr,
        )
    except subprocess.TimeoutExpired:
        _terminate_process_group(proc)
        try:
            stdout, stderr = proc.communicate(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            stdout, stderr = proc.communicate()
        raise subprocess.TimeoutExpired(
            cmd,
            timeout,
            output=stdout,
            stderr=stderr,
        )
    finally:
        with _active_processes_lock:
            _active_processes.pop(proc.pid, None)


def request_stop(futures=()):
    _stop_requested.set()
    for future in futures:
        future.cancel()
    with _active_processes_lock:
        active = list(_active_processes.values())
    for proc in active:
        _terminate_process_group(proc)


def _handle_termination_signal(_signum, _frame):
    raise KeyboardInterrupt


def outer_runner_timeout(args):
    judge_timeout = (
        int(getattr(args, "judge_timeout", 0) or 0)
        if getattr(args, "judge_model", "")
        else 0
    )
    judge_queue_timeout = (
        int(getattr(args, "judge_queue_timeout", 0) or 0)
        if getattr(args, "judge_model", "")
        else 0
    )
    return int(args.timeout) + judge_timeout + judge_queue_timeout + 900


def run_one(rel_path, args, idx, total, lock, slots):
    if _stop_requested.is_set():
        raise cf.CancelledError()
    domain, fname = rel_path.split("/", 1)
    tid = fname[:-5]
    trace_root = (
        getattr(args, "trace_root", "")
        or os.environ.get("OSW_TRACE_ROOT")
        or os.path.join(ROOT, "trace")
    )
    res_path = os.path.join(trace_root, tid, "result.json")
    os.makedirs(os.path.dirname(res_path), exist_ok=True)
    run_id = uuid.uuid4().hex
    archived_result = _isolate_existing_result(res_path, run_id)

    # 取一个并发槽位：槽位号唯一决定容器端口，跑完归还给下一个任务复用。
    # 这取代了原先的全局端口锁——那个锁在 100 并发下会超时，把任务判成 ERR。
    while True:
        if _stop_requested.is_set():
            raise cf.CancelledError()
        try:
            slot = slots.get(timeout=0.5)
            break
        except queue.Empty:
            continue
    env = dict(
        os.environ,
        OSW_TRACE_ROOT=trace_root,
        OSW_HYBRID_SLOT=str(slot),
        OSW_HYBRID_RUN_ID=run_id,
        OSW_BATCH_RUN_ID=str(getattr(args, "run_id", "") or ""),
        OSW_TOOL_VERSION=str(getattr(args, "tool_version", "") or ""),
        OSW_TOOL_CANDIDATE_ID=str(
            (getattr(args, "tool_candidate", None) or {}).get(
                "tool_candidate_id", ""
            )
        ),
        OSW_JUDGE_SLOT_DIR=os.path.join(trace_root, "_judge_slots"),
        OSW_JUDGE_CONCURRENCY=str(
            int(getattr(args, "judge_concurrency", 0) or 0)
        ),
        OSW_JUDGE_QUEUE_TIMEOUT=str(
            int(getattr(args, "judge_queue_timeout", 0) or 0)
        ),
    )
    # 错开冷启动：100 个容器同时起 Xvfb+XFCE+flask+Chrome 会把宿主 load 打到 70+，
    # 各容器的就绪检查随之集体超时。按槽位号阶梯延迟，把冷启动摊开到几分钟内。
    # 只在该槽位的第一个任务生效，后续任务复用槽位时无需再等。
    if not _slot_warmed.get(slot):
        if _stop_requested.wait(slot * args.stagger):
            slots.put(slot)
            raise cf.CancelledError()
        _slot_warmed[slot] = True
    cmd = [os.path.join(ROOT, ".venv/bin/python"), os.path.join(ROOT, "run_single.py"),
           "--task", rel_path, "--image", args.image, "--model", args.model,
           "--effort", args.effort, "--gw-port", str(args.gw_port),
           "--timeout", str(args.timeout), "--agent", args.agent]
    if args.agent in {"claude", "claude-code"}:
        cmd.extend(["--claude-gateway-base", args.claude_gateway_base])
    if getattr(args, "tool_version", ""):
        cmd.extend(["--tool-version", args.tool_version])
    if getattr(args, "tool_candidate_manifest", ""):
        cmd.extend([
            "--tool-candidate-manifest",
            args.tool_candidate_manifest,
        ])
    if getattr(args, "proxy_token_log", ""):
        cmd.extend(["--proxy-token-log", args.proxy_token_log])
    judge_model = getattr(args, "judge_model", "")
    if judge_model:
        cmd.extend([
            "--judge-model", judge_model,
            "--judge-effort", getattr(args, "judge_effort", "xhigh"),
            "--judge-timeout", str(
                getattr(args, "judge_timeout", 420)
            ),
        ])
    t0 = time.time()
    returncode = None
    runner_timed_out = False
    runner_error = None
    try:
        # 给外层留出余量：内层已有自己的超时，这里只兜底进程卡死
        proc = _run_runner_process(
            cmd,
            env,
            outer_runner_timeout(args),
        )
        returncode = proc.returncode
        tail = (proc.stdout or "")[-800:] + (proc.stderr or "")[-800:]
    except subprocess.TimeoutExpired as exc:
        runner_timed_out = True
        tail = "outer timeout"
        if exc.stdout:
            tail += "\n" + str(exc.stdout)[-400:]
        if exc.stderr:
            tail += "\n" + str(exc.stderr)[-400:]
    except OSError as exc:
        runner_error = f"{type(exc).__name__}: {exc}"
        tail = runner_error
    finally:
        slots.put(slot)
    dt = time.time() - t0

    result = None
    result_mtime_ns = None
    if os.path.exists(res_path):
        try:
            result_mtime_ns = os.stat(res_path).st_mtime_ns
            with open(res_path) as fh:
                loaded = json.load(fh)
            if not isinstance(loaded, dict):
                raise ValueError("result.json root must be an object")
            result_run_id = loaded.get("runner_run_id")
            if result_run_id and result_run_id != run_id:
                _isolate_existing_result(res_path, run_id, label="late-stale")
            else:
                result = loaded
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            result = {
                "task_id": tid,
                "domain": domain,
                "score": None,
                "error": f"invalid result.json: {type(exc).__name__}: {exc}",
            }

    if result is None:
        error = "no result.json produced"
        if runner_timed_out:
            error = "run_single exceeded the outer timeout without producing result.json"
        elif runner_error:
            error = f"failed to start run_single: {runner_error}"
        elif returncode:
            error = (
                f"run_single exited with code {returncode} "
                "without producing result.json"
            )
        result = {
            "task_id": tid,
            "domain": domain,
            "score": None,
            "error": error,
            "tail": tail[-600:],
            "wall_seconds": round(dt, 1),
            "runner_run_id": run_id,
            "runner_returncode": returncode,
            "runner_timed_out": runner_timed_out,
            "runner_error": runner_error,
            "agent_timed_out": False,
            "timed_out": False,
            "truncated": runner_timed_out,
            **run_identity_metadata(args),
        }
        _atomic_write_json(res_path, result)
        result_mtime_ns = os.stat(res_path).st_mtime_ns

    result = dict(result)
    result.setdefault("task_id", tid)
    result.setdefault("domain", domain)
    result["wall_seconds"] = round(dt, 1)
    result["runner_run_id"] = run_id
    result["runner_returncode"] = returncode
    result["runner_timed_out"] = runner_timed_out
    result["runner_error"] = runner_error
    result.setdefault(
        "agent_timed_out",
        bool(result.get("timed_out")),
    )
    result.setdefault("timed_out", bool(result.get("agent_timed_out")))
    result["truncated"] = bool(
        result.get("truncated") or runner_timed_out
    )
    result["result_mtime_ns"] = result_mtime_ns
    result.update(run_identity_metadata(args))
    if archived_result:
        result["previous_result_path"] = archived_result
    if returncode not in (None, 0) and not result.get("error"):
        result["error"] = f"run_single exited with code {returncode}"
    # Persist the final runner-owned classification metadata as well. Keeping
    # these fields only in memory lets a failed new attempt leave a stale PASS
    # on disk, which the next analysis run would count as success.
    _atomic_write_json(res_path, result)

    with lock:
        mark = classify_result(result)
        print(f"[{idx}/{total}] {mark:<7} {domain}/{tid[:8]}  "
              f"score={result.get('score')}  {dt/60:.1f}min",
              flush=True)
    return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--concurrency", type=int, default=32)
    ap.add_argument("--domains", default="", help="逗号分隔，如 libreoffice_calc,gimp")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--tasks", default="", help="逗号分隔的 task id，优先于 domains")
    ap.add_argument(
        "--tasks-file",
        default="",
        help="从 JSONL 的 task_id 或纯文本 UUID 列表读取任务；不能与 --tasks 同用",
    )
    ap.add_argument(
        "--tool-version",
        default=current_tool_version(),
        help="产品工具版本；与本次实验的 --run-id 独立",
    )
    ap.add_argument(
        "--run-id",
        default="",
        help="本次实验的唯一标识；默认取 --out 文件名，不表示工具版本",
    )
    ap.add_argument(
        "--tool-candidate-manifest",
        default="",
        help=(
            "未发布工具候选清单；严格绑定 baseline release、change IDs、"
            "镜像 ID 和组件版本，不会修改正式 tool version"
        ),
    )
    ap.add_argument(
        "--image",
        default="",
        help="运行镜像；默认由 --tool-version 发布清单决定",
    )
    ap.add_argument("--model", default="gpt-5.5")
    ap.add_argument("--effort", default="xhigh")
    ap.add_argument("--gw-port", type=int, default=8600)
    ap.add_argument("--timeout", type=int, default=1800)
    ap.add_argument(
        "--proxy-token-log",
        default=os.environ.get("OSW_PROXY_TOKEN_LOG", ""),
        help=(
            "wrapper-owned token proxy JSONL forwarded to each run_single "
            "process for truncated-agent usage recovery"
        ),
    )
    ap.add_argument("--out", default="")
    ap.add_argument(
        "--trace-root",
        default="",
        help="轨迹根目录；默认根据 --out 文件名派生为 trace-<stem>",
    )
    ap.add_argument(
        "--agent",
        default="codex",
        choices=["codex", "claude", "claude-code"],
    )
    ap.add_argument(
        "--claude-gateway-base",
        default=os.environ.get(
            "OSW_ANTHROPIC_BASE_URL",
            "http://172.17.0.1:8537",
        ),
        help=(
            "Anthropic-compatible coproxy base reachable from the OSWorld "
            "container bridge."
        ),
    )
    ap.add_argument("--judge-model", default="")
    ap.add_argument("--judge-effort", default="xhigh")
    ap.add_argument("--judge-timeout", type=int, default=420)
    ap.add_argument(
        "--judge-concurrency",
        type=int,
        default=0,
        help="共享 interactive Judge 并发上限；0 表示不额外限流",
    )
    ap.add_argument(
        "--judge-queue-timeout",
        type=int,
        default=0,
        help="等待共享 Judge 槽位的最长秒数；0 表示不限时",
    )
    ap.add_argument("--stagger", type=float, default=2.0,
                    help="每个槽位冷启动的阶梯延迟秒数，避免容器同时冷启动打爆宿主")
    args = ap.parse_args()
    args.proxy_token_log = resolve_proxy_token_log(
        args.agent,
        args.proxy_token_log,
    )
    _stop_requested.clear()
    try:
        args.tool_candidate = None
        if args.tool_candidate_manifest:
            args.tool_candidate = tool_candidate_record(
                args.tool_candidate_manifest
            )
            if args.tool_version != args.tool_candidate["baseline_tool_version"]:
                raise ValueError(
                    "tool candidate baseline mismatch: --tool-version "
                    f"{args.tool_version!r}, candidate requires "
                    f"{args.tool_candidate['baseline_tool_version']!r}"
                )
        args.tool_release = tool_release_record(args.tool_version)
        if not args.image:
            args.image = (
                args.tool_candidate["tool_image"]
                if args.tool_candidate
                else args.tool_release["tool_image"]
            )
        if args.tool_candidate:
            assert_tool_candidate_image(
                args.tool_candidate_manifest,
                args.image,
            )
        else:
            assert_tool_release_image(args.tool_version, args.image)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    out_path = args.out or os.path.join(
        ROOT, "logs", f"run-{time.strftime('%m%d-%H%M')}.jsonl"
    )
    args.run_id = str(args.run_id or run_id_for_output(out_path)).strip()
    if not args.run_id:
        print("run id must not be empty", file=sys.stderr)
        return 2

    domains = [d.strip() for d in args.domains.split(",") if d.strip()]
    try:
        task_ids = load_task_ids(args.tasks, args.tasks_file)
    except (OSError, ValueError) as exc:
        print(str(exc), file=sys.stderr)
        return 2
    requested_excluded = sorted(
        set(task_ids) & GOOGLE_DRIVE_TASK_IDS
    )
    if requested_excluded:
        print(
            "explicit task selection contains permanently excluded "
            "Google Drive tasks: " + ", ".join(requested_excluded),
            file=sys.stderr,
        )
        return 2
    try:
        validate_test_contract()
        if args.tool_candidate:
            resolved_image_id = validate_tool_candidate_runtime(
                args.tool_candidate_manifest,
                args.image,
            )
        else:
            resolved_image_id = validate_tool_release_runtime(
                args.tool_version,
                args.image,
            )
        mcp_preflight = validate_mcp_server_runtime(
            args.image,
            args.tool_candidate or args.tool_release,
        )
        gateway_url = validate_gateway(args.gw_port)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    except RuntimeError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    try:
        tasks, excluded = load_tasks(domains, args.limit, task_ids)
    except (OSError, ValueError) as exc:
        print(str(exc), file=sys.stderr)
        return 2
    if not tasks:
        print("没有匹配的任务", file=sys.stderr)
        return 1
    if not domains and not task_ids and not args.limit:
        try:
            assert_formal_full_task_set(tid for _, tid, _ in tasks)
        except ValueError as exc:
            print(str(exc), file=sys.stderr)
            return 2

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    args.trace_root = (
        args.trace_root
        or os.environ.get("OSW_TRACE_ROOT")
        or trace_root_for_output(out_path)
    )
    os.makedirs(args.trace_root, exist_ok=True)
    selected_task_policy = task_set_record(
        (tid for _, tid, _ in tasks),
        (tid for _, tid, _ in excluded),
    )
    _atomic_write_json(
        os.path.join(args.trace_root, "task_set_policy.json"),
        selected_task_policy,
    )
    run_metadata_path = os.path.join(args.trace_root, "run_metadata.json")
    run_metadata = {
        "schema_version": 1,
        "run_id": args.run_id,
        "status": "running",
        "started_at_utc": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime()
        ),
        "tool_release": args.tool_release,
        "tool_candidate": args.tool_candidate,
        "run_configuration": {
            "image": args.image,
            "image_id": resolved_image_id,
            "model": args.model,
            "effort": args.effort,
            "agent": args.agent,
            "judge_model": args.judge_model,
            "judge_effort": args.judge_effort,
            "concurrency": args.concurrency,
            "task_timeout_seconds": args.timeout,
            "judge_timeout_seconds": args.judge_timeout,
            "judge_concurrency": args.judge_concurrency,
            "judge_queue_timeout_seconds": args.judge_queue_timeout,
            "stagger_seconds": args.stagger,
            "gateway_port": args.gw_port,
            "proxy_token_log": (
                os.path.abspath(args.proxy_token_log)
                if args.proxy_token_log
                else None
            ),
            "mcp_preflight": mcp_preflight,
            "output": os.path.abspath(out_path),
            "trace_root": os.path.abspath(args.trace_root),
            "task_set_policy": selected_task_policy,
        },
    }
    _atomic_write_json(run_metadata_path, run_metadata)

    print("=" * 76)
    print(
        f"Run {args.run_id}   Tool {args.tool_version}   "
        f"任务 {len(tasks)} 个   并发 {args.concurrency}"
    )
    print(
        f"镜像 {args.image}   组件 "
        f"{(args.tool_candidate or args.tool_release)['tool_component_version']}"
    )
    print(f"镜像身份 {resolved_image_id}")
    if excluded:
        print(
            f"策略排除 {len(excluded)} 个 Google Drive 任务 "
            "(GOOGLE_DRIVE_EXCLUDED)"
        )
    print(f"模型 {args.model} effort={args.effort}   单任务超时 {args.timeout}s")
    print(f"Gateway preflight: {gateway_url}")
    print(
        "MCP preflight: "
        f"{mcp_preflight['command']} "
        f"server={mcp_preflight['server_name']} "
        f"tools={','.join(mcp_preflight['tools'])}"
    )
    print(f"结果 {out_path}")
    print(f"轨迹 {args.trace_root}")
    print("=" * 76, flush=True)

    lock = threading.Lock()
    slots = queue.Queue()
    for i in range(args.concurrency):
        slots.put(i)
    results = []
    t0 = time.time()
    ex = cf.ThreadPoolExecutor(max_workers=args.concurrency)
    futs = {
        ex.submit(run_one, rel, args, i, len(tasks), lock, slots): rel
        for i, (_, _, rel) in enumerate(tasks, 1)
    }
    try:
        with open(out_path, "w") as fh:
            for fut in cf.as_completed(futs):
                try:
                    r = fut.result()
                except cf.CancelledError:
                    continue
                except Exception as e:
                    r = {
                        "task_id": futs[fut],
                        "score": None,
                        "error": str(e),
                        **run_identity_metadata(args),
                    }
                results.append(r)
                fh.write(json.dumps(r, ensure_ascii=False) + "\n")
                fh.flush()
    except KeyboardInterrupt:
        print(
            "\nInterrupted: stopping active run_single process groups and "
            "cancelling queued tasks.",
            file=sys.stderr,
            flush=True,
        )
        request_stop(futs)
        ex.shutdown(wait=True, cancel_futures=True)
        run_metadata.update(
            {
                "status": "interrupted",
                "completed_at_utc": time.strftime(
                    "%Y-%m-%dT%H:%M:%SZ", time.gmtime()
                ),
                "result_count": len(results),
            }
        )
        _atomic_write_json(run_metadata_path, run_metadata)
        return 130
    else:
        ex.shutdown(wait=True)

    wall = time.time() - t0
    summary = summarize_results(results)
    diagnostics = summarize_diagnostics(results)
    audited = summarize_provisional_audited(results)
    completion_validation = finalize_run_metadata(
        run_metadata_path,
        run_metadata,
        results,
        [tid for _, tid, _ in tasks],
        summary,
        diagnostics,
        require_judge=bool(args.judge_model),
    )
    passed = summary["PASS"]

    print("\n" + "=" * 76)
    print(f"完成 {len(results)}/{len(tasks)}   墙钟 {wall/60:.1f} 分钟")
    print(
        f"RAW evaluator: PASS {summary['PASS']}   FAIL {summary['FAIL']}   "
        f"PARTIAL {summary['PARTIAL']}   ERR {summary['ERR']}"
    )
    # 分母必须是全部任务，不是「成功产出结果的任务」——后者会系统性高估
    print(
        f"RAW evaluator strict PASS {passed}/{len(tasks)} = "
        f"{passed/len(tasks)*100:.1f}%  "
        "(仅官方原始评分，不是能力分数)"
    )
    if summary["ERR"]:
        print(f"注意：{summary['ERR']} 个 ERR 任务的 score 为 null 或无效")
    print(
        f"诊断：error={diagnostics['with_error']}  "
        f"truncated={diagnostics['truncated']}  "
        f"agent_timed_out={diagnostics['agent_timed_out']}  "
        f"runner_timed_out={diagnostics['runner_timed_out']}  "
        f"setup_failure={diagnostics['setup_failure']}"
    )
    if audited["reviewed"]:
        print(
            "PROVISIONAL audit 覆盖 "
            f"{audited['reviewed']}/{audited['total']}；"
            f"provisional strict PASS "
            f"{audited['strict_pass_lower']}-{audited['strict_pass_upper']}"
            f"/{audited['capability_denominator']}；"
            f"分类 {audited['classifications']}"
        )
    print(
        "真实能力分数必须由 agent 逐题复核，并通过 "
        "evaluator_audit.py --require-final-audit "
        "--require-all-task-review；否则禁止引用为结论。"
    )
    print("=" * 76)
    if not completion_validation["valid"]:
        print(
            "Completion validation failed: "
            + ", ".join(completion_validation["failure_reasons"]),
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, _handle_termination_signal)
    sys.exit(main())
