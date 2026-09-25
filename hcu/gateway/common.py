"""Plumbing shared by the task-scoped accounting gateways.

Guest agents reach a gateway at ``http://<bridge>:<port>/t/<task-id>/...``.
The task ID routes the request, keys its accounting row and decides whether
the per-task request budget applies. Every request is appended to a JSONL
token log that the harness reads back after the run.
"""

from __future__ import annotations

from collections.abc import Iterator
import json
from pathlib import Path
import threading
import urllib.parse

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
