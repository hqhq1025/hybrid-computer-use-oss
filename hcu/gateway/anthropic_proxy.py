#!/usr/bin/env python3
"""Task-scoped, accounting Anthropic API proxy for guest-local Claude Code."""

from __future__ import annotations

import argparse
import json
import os
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import urllib3


REQUEST_TIMEOUT = urllib3.Timeout(connect=15, read=1800)
LIMITED_TASK_PREFIXES = ("osw-hybrid-", "osworld-v2-hybrid-")
_write_lock = threading.Lock()
_request_lock = threading.Lock()
_out_fh = None
_completed_responses: dict[str, int] = {}
_pool = urllib3.PoolManager(maxsize=128, block=False, retries=False)
UPSTREAM_BASE = ""
UPSTREAM_TOKEN = ""
MAX_REQUESTS_PER_TASK = 0
_stats = {
    "calls": 0,
    "accounted": 0,
    "errors": 0,
    "rate_limited": 0,
    "budget_rejected": 0,
}


def record(row: dict) -> None:
    with _write_lock:
        _out_fh.write(json.dumps(row, ensure_ascii=False) + "\n")
        _out_fh.flush()


def split_task(path: str) -> tuple[str | None, str]:
    task = None
    if path.startswith("/t/"):
        task, separator, tail = path[3:].partition("/")
        if not separator:
            return urllib.parse.unquote(task), "/"
        path = "/" + tail
        task = urllib.parse.unquote(task)
    return task, path


def admit_model_request(task: str | None, path: str) -> tuple[bool, int | None]:
    if (
        MAX_REQUESTS_PER_TASK <= 0
        or not task
        or not task.startswith(LIMITED_TASK_PREFIXES)
        or path != "/v1/messages"
    ):
        return True, None
    with _request_lock:
        completed = _completed_responses.get(task, 0)
        if completed >= MAX_REQUESTS_PER_TASK:
            return False, completed + 1
        return True, completed + 1


def complete_model_request(
    task: str | None,
    path: str,
    delivered: bool,
) -> int | None:
    if (
        MAX_REQUESTS_PER_TASK <= 0
        or not task
        or not task.startswith(LIMITED_TASK_PREFIXES)
        or path != "/v1/messages"
        or not delivered
    ):
        return None
    with _request_lock:
        completed = _completed_responses.get(task, 0) + 1
        _completed_responses[task] = completed
        return completed


def restore_completed_responses(path: str) -> int:
    restored: dict[str, int] = {}
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            for line in handle:
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                task = row.get("task")
                if (
                    not isinstance(task, str)
                    or not task.startswith(LIMITED_TASK_PREFIXES)
                    or row.get("path") != "/v1/messages"
                    or not row.get("model_step_completed")
                ):
                    continue
                restored[task] = restored.get(task, 0) + 1
    except FileNotFoundError:
        return 0
    with _request_lock:
        _completed_responses.update(restored)
    return len(restored)


def extract_usage(payload: dict) -> dict:
    usage = payload.get("usage") or {}
    input_tokens = int(usage.get("input_tokens") or 0)
    cache_read = int(usage.get("cache_read_input_tokens") or 0)
    cache_creation = int(usage.get("cache_creation_input_tokens") or 0)
    output_tokens = int(usage.get("output_tokens") or 0)
    return {
        "input_tokens": input_tokens,
        "cached_tokens": cache_read,
        "cache_creation_input_tokens": cache_creation,
        "output_tokens": output_tokens,
        "reasoning_tokens": int(
            (usage.get("output_tokens_details") or {}).get(
                "thinking_tokens"
            )
            or 0
        ),
        "total_tokens": (
            input_tokens + cache_creation + cache_read + output_tokens
        ),
    }


def parse_sse_usage(raw: bytes) -> tuple[dict | None, str | None]:
    totals = None
    response_id = None
    for line in raw.splitlines():
        if not line.startswith(b"data:"):
            continue
        chunk = line[5:].strip()
        if not chunk or chunk == b"[DONE]":
            continue
        try:
            event = json.loads(chunk)
        except json.JSONDecodeError:
            continue
        message = event.get("message")
        if isinstance(message, dict):
            response_id = str(message.get("id") or response_id or "") or None
            if message.get("usage"):
                totals = extract_usage(message)
        if event.get("usage"):
            current = extract_usage(event)
            if totals is None:
                totals = current
            else:
                for key, value in current.items():
                    totals[key] = max(int(totals.get(key) or 0), int(value or 0))
    return totals, response_id


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args) -> None:
        return

    def do_GET(self) -> None:
        if self.path == "/healthz":
            body = json.dumps({
                "ok": True,
                "max_completed_responses_per_task": MAX_REQUESTS_PER_TASK,
                "step_unit": "successful_task_agent_response",
                **_stats,
            }).encode()
            self.send_response(200)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.proxy("GET")

    def do_POST(self) -> None:
        self.proxy("POST")

    def fail(self, status: int, message: str, row: dict) -> None:
        row["http_status"] = status
        row["error"] = message
        _stats["errors"] += 1
        record(row)
        body = json.dumps({
            "type": "error",
            "error": {
                "type": "api_error",
                "message": f"anthropic_proxy: {message}",
            },
        }).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def reject_budget(self, row: dict) -> None:
        row["budget_rejected"] = True
        _stats["budget_rejected"] += 1
        self.fail(
            429,
            "task model-call budget exhausted before upstream admission",
            row,
        )

    def proxy(self, method: str) -> None:
        task, local_path = split_task(self.path)
        parsed = urllib.parse.urlsplit(local_path)
        body_length = int(self.headers.get("content-length") or 0)
        body = self.rfile.read(body_length) if body_length else b""
        row = {
            "ts": time.time(),
            "task": task,
            "endpoint": UPSTREAM_BASE,
            "path": parsed.path,
            "model": None,
        }
        if body[:1] == b"{":
            try:
                row["model"] = json.loads(body).get("model")
            except json.JSONDecodeError:
                pass
        admitted, ordinal = admit_model_request(task, parsed.path)
        if ordinal is not None:
            row["request_ordinal"] = ordinal
            row["request_limit"] = MAX_REQUESTS_PER_TASK
        if not admitted:
            self.reject_budget(row)
            return

        upstream_url = (
            UPSTREAM_BASE.rstrip("/")
            + parsed.path
            + (f"?{parsed.query}" if parsed.query else "")
        )
        headers = {}
        for key, value in self.headers.items():
            if key.casefold() in {
                "host",
                "connection",
                "content-length",
                "authorization",
                "x-api-key",
                "accept-encoding",
            }:
                continue
            headers[key] = value
        headers["authorization"] = f"Bearer {UPSTREAM_TOKEN}"
        headers["x-api-key"] = UPSTREAM_TOKEN
        headers["accept-encoding"] = "identity"
        headers["content-length"] = str(len(body))

        started = time.monotonic()
        _stats["calls"] += 1
        try:
            response = _pool.request(
                method,
                upstream_url,
                body=body,
                headers=headers,
                preload_content=True,
                timeout=REQUEST_TIMEOUT,
            )
        except Exception as exc:
            self.fail(
                502,
                f"upstream request failed: {type(exc).__name__}: {exc}",
                row,
            )
            return

        response_body = response.data
        row["http_status"] = response.status
        row["latency_sec"] = round(time.monotonic() - started, 3)
        if response.status == 429:
            row["rate_limited"] = True
            _stats["rate_limited"] += 1
        elif response.status >= 400:
            row["upstream_error"] = True
            _stats["errors"] += 1

        content_type = response.headers.get("content-type", "")
        try:
            payload = None
            if "text/event-stream" in content_type:
                usage, response_id = parse_sse_usage(response_body)
                row["response_id"] = response_id
                if usage:
                    row.update(usage)
            elif response_body[:1] == b"{":
                payload = json.loads(response_body)
                row["response_id"] = payload.get("id")
                if payload.get("usage"):
                    row.update(extract_usage(payload))
            if row.get("input_tokens") is not None:
                _stats["accounted"] += 1
        except Exception as exc:
            row["accounting_error"] = f"{type(exc).__name__}: {exc}"
            _stats["errors"] += 1
        delivered = True
        try:
            self.send_response(response.status)
            for key, value in response.headers.items():
                if key.casefold() in {
                    "transfer-encoding",
                    "content-length",
                    "connection",
                    "content-encoding",
                }:
                    continue
                self.send_header(key, value)
            self.send_header("content-length", str(len(response_body)))
            self.end_headers()
            self.wfile.write(response_body)
        except (BrokenPipeError, ConnectionResetError):
            row["client_disconnected"] = True
            delivered = False
        if 200 <= response.status < 300:
            completed = complete_model_request(
                task,
                parsed.path,
                delivered,
            )
            if completed is not None:
                row["model_step_completed"] = True
                row["request_ordinal"] = completed
        record(row)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8613)
    parser.add_argument("--out", required=True)
    parser.add_argument(
        "--upstream-base",
        default=os.environ.get("OSW_ANTHROPIC_UPSTREAM_BASE", ""),
    )
    parser.add_argument(
        "--upstream-token",
        default=os.environ.get("OSW_ANTHROPIC_UPSTREAM_TOKEN", ""),
    )
    parser.add_argument("--max-requests-per-task", type=int, default=0)
    args = parser.parse_args()
    if not args.upstream_base:
        parser.error(
            "set OSW_ANTHROPIC_UPSTREAM_BASE or pass --upstream-base"
        )
    if not args.upstream_token:
        parser.error(
            "set OSW_ANTHROPIC_UPSTREAM_TOKEN or pass --upstream-token"
        )
    if args.max_requests_per_task < 0:
        parser.error("--max-requests-per-task must be at least 0")

    parsed = urllib.parse.urlsplit(args.upstream_base)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        parser.error("--upstream-base must be an absolute HTTP(S) URL")

    global _out_fh, UPSTREAM_BASE, UPSTREAM_TOKEN, MAX_REQUESTS_PER_TASK
    UPSTREAM_BASE = args.upstream_base.rstrip("/")
    UPSTREAM_TOKEN = args.upstream_token
    MAX_REQUESTS_PER_TASK = args.max_requests_per_task
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    restore_completed_responses(args.out)
    _out_fh = open(args.out, "a", encoding="utf-8", buffering=1)
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    finally:
        server.server_close()
        _out_fh.close()


if __name__ == "__main__":
    raise SystemExit(main())
