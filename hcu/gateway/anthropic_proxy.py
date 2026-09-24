"""Task-scoped, accounting passthrough to an Anthropic-compatible API.

Guest-local Claude Code sends ``/t/<task>/v1/messages`` requests; the gateway
forwards them with the operator's upstream credential, records usage in the
token log and enforces the per-task budget. The budget counts successful
responses that reached the agent, so a request that failed upstream or was
never delivered does not consume a model step.

Usage::

    OSW_ANTHROPIC_UPSTREAM_BASE=https://<host> \\
    OSW_ANTHROPIC_UPSTREAM_TOKEN=<secret> \\
    python -m hcu.gateway.anthropic_proxy --host <bridge-ip> --port 8537 \\
        --out runs/anthropic-tokens.jsonl
"""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import threading
import time
import urllib.parse

import urllib3

from hcu.gateway.common import (
    TokenLog,
    is_budgeted_task,
    new_stats,
    read_log_rows,
    relay_response,
    send_json,
    split_task_path,
)

REQUEST_TIMEOUT = urllib3.Timeout(connect=15, read=1800)
BUDGETED_PATH = "/v1/messages"
UNFORWARDED_REQUEST_HEADERS = frozenset({
    "host",
    "connection",
    "content-length",
    "authorization",
    "x-api-key",
    "accept-encoding",
})


class AnthropicGateway:
    """Upstream, budget and accounting state shared by all request threads."""

    def __init__(
        self,
        *,
        upstream_base: str,
        upstream_token: str,
        token_log: TokenLog,
        max_requests_per_task: int,
        pool: urllib3.PoolManager | None = None,
    ) -> None:
        self.upstream_base = upstream_base.rstrip("/")
        self.upstream_token = upstream_token
        self.token_log = token_log
        self.max_requests_per_task = max_requests_per_task
        self.pool = pool or urllib3.PoolManager(maxsize=128, block=False, retries=False)
        self.stats = new_stats()
        self._budget_lock = threading.Lock()
        self._completed: dict[str, int] = {}

    def restore(self) -> None:
        """Rebuild completed-response counts from the log."""
        completed: dict[str, int] = {}
        for row in read_log_rows(self.token_log.path):
            task = row.get("task")
            if (
                isinstance(task, str)
                and is_budgeted_task(task)
                and row.get("path") == BUDGETED_PATH
                and row.get("model_step_completed")
            ):
                completed[task] = completed.get(task, 0) + 1
        with self._budget_lock:
            self._completed.update(completed)

    def _budgeted(self, task: str | None, path: str) -> bool:
        return (
            self.max_requests_per_task > 0
            and is_budgeted_task(task)
            and path == BUDGETED_PATH
        )

    def admit(self, task: str | None, path: str) -> tuple[bool, int | None]:
        if not self._budgeted(task, path):
            return True, None
        with self._budget_lock:
            completed = self._completed.get(task, 0)
            return completed < self.max_requests_per_task, completed + 1

    def complete(self, task: str | None, path: str, delivered: bool) -> int | None:
        if not delivered or not self._budgeted(task, path):
            return None
        with self._budget_lock:
            completed = self._completed.get(task, 0) + 1
            self._completed[task] = completed
            return completed

    def healthz(self) -> dict:
        return {
            "ok": True,
            "max_completed_responses_per_task": self.max_requests_per_task,
            "step_unit": "successful_task_agent_response",
            **self.stats,
        }


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
            (usage.get("output_tokens_details") or {}).get("thinking_tokens")
            or 0
        ),
        "total_tokens": (
            input_tokens + cache_creation + cache_read + output_tokens
        ),
    }


def parse_sse_usage(raw: bytes) -> tuple[dict | None, str | None]:
    """Return the maximum streamed usage per field and the message ID."""
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


def make_handler(gateway: AnthropicGateway) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_args) -> None:
            return

        def do_GET(self) -> None:
            if self.path == "/healthz":
                send_json(self, 200, gateway.healthz())
                return
            self.proxy("GET")

        def do_POST(self) -> None:
            self.proxy("POST")

        def fail(self, status: int, message: str, row: dict) -> None:
            row["http_status"] = status
            row["error"] = message
            gateway.stats["errors"] += 1
            gateway.token_log.record(row)
            send_json(self, status, {
                "type": "error",
                "error": {
                    "type": "api_error",
                    "message": f"anthropic_proxy: {message}",
                },
            })

        def proxy(self, method: str) -> None:
            task, local_path = split_task_path(self.path)
            parsed = urllib.parse.urlsplit(local_path)
            length = int(self.headers.get("content-length") or 0)
            body = self.rfile.read(length) if length else b""
            row = {
                "ts": time.time(),
                "task": task,
                "endpoint": gateway.upstream_base,
                "path": parsed.path,
                "model": None,
            }
            if body[:1] == b"{":
                try:
                    row["model"] = json.loads(body).get("model")
                except json.JSONDecodeError:
                    pass
            admitted, ordinal = gateway.admit(task, parsed.path)
            if ordinal is not None:
                row["request_ordinal"] = ordinal
                row["request_limit"] = gateway.max_requests_per_task
            if not admitted:
                row["budget_rejected"] = True
                gateway.stats["budget_rejected"] += 1
                self.fail(
                    429,
                    "task model-call budget exhausted before upstream admission",
                    row,
                )
                return

            upstream_url = (
                gateway.upstream_base
                + parsed.path
                + (f"?{parsed.query}" if parsed.query else "")
            )
            headers = {
                key: value
                for key, value in self.headers.items()
                if key.casefold() not in UNFORWARDED_REQUEST_HEADERS
            }
            headers["authorization"] = f"Bearer {gateway.upstream_token}"
            headers["x-api-key"] = gateway.upstream_token
            headers["accept-encoding"] = "identity"
            headers["content-length"] = str(len(body))

            started = time.monotonic()
            gateway.stats["calls"] += 1
            try:
                response = gateway.pool.request(
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
                gateway.stats["rate_limited"] += 1
            elif response.status >= 400:
                row["upstream_error"] = True
                gateway.stats["errors"] += 1

            try:
                if "text/event-stream" in response.headers.get("content-type", ""):
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
                    gateway.stats["accounted"] += 1
            except Exception as exc:
                row["accounting_error"] = f"{type(exc).__name__}: {exc}"
                gateway.stats["errors"] += 1
            delivered = relay_response(
                self, response.status, response.headers, response_body
            )
            if not delivered:
                row["client_disconnected"] = True
            if 200 <= response.status < 300:
                completed = gateway.complete(task, parsed.path, delivered)
                if completed is not None:
                    row["model_step_completed"] = True
                    row["request_ordinal"] = completed
            gateway.token_log.record(row)

    return Handler


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help=(
            "Listen address. Guests reach the gateway through the Docker "
            "bridge, so pass the bridge gateway IP for OSWorld runs."
        ),
    )
    parser.add_argument("--port", type=int, default=8537)
    parser.add_argument("--out", required=True, help="Token log JSONL path.")
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
        parser.error("set OSW_ANTHROPIC_UPSTREAM_BASE or pass --upstream-base")
    if not args.upstream_token:
        parser.error("set OSW_ANTHROPIC_UPSTREAM_TOKEN or pass --upstream-token")
    if args.max_requests_per_task < 0:
        parser.error("--max-requests-per-task must be at least 0")
    parsed = urllib.parse.urlsplit(args.upstream_base)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        parser.error("--upstream-base must be an absolute HTTP(S) URL")

    token_log = TokenLog(Path(args.out))
    gateway = AnthropicGateway(
        upstream_base=args.upstream_base,
        upstream_token=args.upstream_token,
        token_log=token_log,
        max_requests_per_task=args.max_requests_per_task,
    )
    gateway.restore()
    server = ThreadingHTTPServer((args.host, args.port), make_handler(gateway))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    finally:
        server.server_close()
        token_log.close()


if __name__ == "__main__":
    raise SystemExit(main())
