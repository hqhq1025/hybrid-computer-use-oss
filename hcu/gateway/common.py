"""Plumbing shared by the task-scoped accounting gateways.

Guest agents reach a gateway at ``http://<bridge>:<port>/t/<task-id>/...``.
The task ID routes the request, keys its accounting row and decides whether
the per-task request budget applies. Every request is appended to a JSONL
token log that the harness reads back after the run.
"""

from __future__ import annotations

from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import threading
import time
import urllib.parse

import urllib3

# Task agents of the OSWorld runner use this prefix; other traffic is not
# budgeted. The prefix reaches the guest as part of the gateway URL, so it
# names nothing about the benchmark.
TASK_AFFINITY_PREFIX = "task-"
HOP_BY_HOP_RESPONSE_HEADERS = frozenset({
    "transfer-encoding",
    "content-length",
    "connection",
    "content-encoding",
})


def split_task_path(path: str) -> tuple[str | None, str]:
    """Split ``/t/<task>/<rest>`` into the task ID and the upstream path."""
    if not path.startswith("/t/"):
        return None, path
    task, separator, tail = path[3:].partition("/")
    task = urllib.parse.unquote(task)
    if not separator:
        return task, "/"
    return task, "/" + tail


def is_budgeted_task(task: str | None) -> bool:
    return bool(task) and task.startswith(TASK_AFFINITY_PREFIX)


def read_log_rows(path: Path) -> Iterator[dict]:
    """Yield the JSON rows of an existing token log, skipping damaged lines."""
    try:
        handle = path.open(encoding="utf-8", errors="replace")
    except FileNotFoundError:
        return
    with handle:
        for line in handle:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(row, dict):
                yield row


class TokenLog:
    """Thread-safe, append-only JSONL log with one row per request."""

    def __init__(self, path: Path) -> None:
        self.path = path.resolve()
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._handle = self.path.open("a", encoding="utf-8", buffering=1)

    def record(self, row: dict) -> None:
        with self._lock:
            self._handle.write(json.dumps(row, ensure_ascii=False) + "\n")
            self._handle.flush()

    def close(self) -> None:
        self._handle.close()


def new_stats() -> dict[str, int]:
    return {
        "calls": 0,
        "accounted": 0,
        "errors": 0,
        "rate_limited": 0,
        "budget_rejected": 0,
    }


def send_json(handler, status: int, payload: dict) -> None:
    body = json.dumps(payload).encode()
    handler.send_response(status)
    handler.send_header("content-type", "application/json")
    handler.send_header("content-length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


def relay_response(handler, status: int, headers, body: bytes) -> bool:
    """Relay an upstream response; return False if the client disconnected.

    The harness stops a task by killing its agent, so a client may disappear
    while the upstream request is in flight. The response is still accounted,
    but the expected broken pipe must not surface as a server traceback.
    """
    try:
        handler.send_response(status)
        for key, value in headers.items():
            if key.casefold() in HOP_BY_HOP_RESPONSE_HEADERS:
                continue
            handler.send_header(key, value)
        handler.send_header("content-length", str(len(body)))
        handler.end_headers()
        handler.wfile.write(body)
    except (BrokenPipeError, ConnectionResetError):
        return False
    return True


REQUEST_TIMEOUT = urllib3.Timeout(connect=15, read=1800)


class PassthroughGateway:
    """A task-scoped gateway to one upstream base URL with a fixed credential.

    The budget counts successful responses that reached the agent, so a
    request that failed upstream or was never delivered does not consume a
    model step. Subclasses describe one wire API: which requests are
    budgeted, how the credential is sent and where usage is reported.
    """

    name = "passthrough"
    unforwarded_request_headers: frozenset[str] = frozenset({
        "host", "connection", "content-length", "authorization", "accept-encoding",
    })

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

    def is_budgeted_path(self, path: str) -> bool:
        raise NotImplementedError

    def auth_headers(self) -> dict[str, str]:
        raise NotImplementedError

    def request_model(self, path: str, body: bytes) -> str | None:
        raise NotImplementedError

    def json_usage(self, payload: dict) -> tuple[dict | None, str | None]:
        """Usage row fields and response ID of a non-streamed response."""
        raise NotImplementedError

    def sse_usage(self, raw: bytes) -> tuple[dict | None, str | None]:
        raise NotImplementedError

    def error_body(self, status: int, message: str) -> dict:
        raise NotImplementedError

    def restore(self) -> None:
        """Rebuild completed-response counts from the log."""
        completed: dict[str, int] = {}
        for row in read_log_rows(self.token_log.path):
            task = row.get("task")
            if (
                isinstance(task, str)
                and is_budgeted_task(task)
                and self.is_budgeted_path(str(row.get("path") or ""))
                and row.get("model_step_completed")
            ):
                completed[task] = completed.get(task, 0) + 1
        with self._budget_lock:
            self._completed.update(completed)

    def _budgeted(self, task: str | None, path: str) -> bool:
        return (
            self.max_requests_per_task > 0
            and is_budgeted_task(task)
            and self.is_budgeted_path(path)
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


def make_passthrough_handler(
    gateway: PassthroughGateway,
) -> type[BaseHTTPRequestHandler]:
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
            send_json(self, status, gateway.error_body(status, f"{gateway.name}: {message}"))

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
                "model": gateway.request_model(parsed.path, body),
            }
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
                if key.casefold() not in gateway.unforwarded_request_headers
            }
            headers.update(gateway.auth_headers())
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
                    usage, response_id = gateway.sse_usage(response_body)
                    row["response_id"] = response_id
                    if usage:
                        row.update(usage)
                elif response_body[:1] == b"{":
                    usage, response_id = gateway.json_usage(json.loads(response_body))
                    row["response_id"] = response_id
                    if usage:
                        row.update(usage)
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


def serve_passthrough(gateway: PassthroughGateway, host: str, port: int) -> int:
    """Restore the budget from the log and serve until interrupted."""
    gateway.restore()
    server = ThreadingHTTPServer((host, port), make_passthrough_handler(gateway))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 0
    finally:
        server.server_close()
        gateway.token_log.close()
    return 0


def add_passthrough_arguments(parser, *, port: int, upstream_env: str, token_env: str,
                              upstream_default: str = "") -> None:
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help=(
            "Listen address. Guests reach the gateway through the Docker "
            "bridge, so pass the bridge gateway IP for OSWorld runs."
        ),
    )
    parser.add_argument("--port", type=int, default=port)
    parser.add_argument("--out", required=True, help="Token log JSONL path.")
    parser.add_argument(
        "--upstream-base",
        default=os.environ.get(upstream_env, upstream_default),
    )
    parser.add_argument(
        "--upstream-token",
        default=os.environ.get(token_env, ""),
    )
    parser.add_argument("--max-requests-per-task", type=int, default=0)


def check_passthrough_arguments(parser, args, *, upstream_env: str, token_env: str) -> None:
    if not args.upstream_base:
        parser.error(f"set {upstream_env} or pass --upstream-base")
    if not args.upstream_token:
        parser.error(f"set {token_env} or pass --upstream-token")
    if args.max_requests_per_task < 0:
        parser.error("--max-requests-per-task must be at least 0")
    parsed = urllib.parse.urlsplit(args.upstream_base)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        parser.error("--upstream-base must be an absolute HTTP(S) URL")
