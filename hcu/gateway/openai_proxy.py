"""Task-scoped, accounting passthrough to an OpenAI Responses API.

The upstream is either Azure OpenAI, authenticated with an Azure CLI (AAD)
token, or any OpenAI-compatible ``/v1/responses`` endpoint authenticated with
a bearer token (``--upstream-kind openai``). Codex and Grok Build both speak
this API. Request bodies are forwarded byte for byte. The gateway exists for
two reasons:

* Accounting. Some agents drop reasoning-token counts before reporting usage,
  so usage is recorded on the wire, one JSONL row per request.
* Deployments. An upstream may be written ``HOST@DEPLOYMENT``; requests
  routed to it have their ``model`` replaced by that deployment name. Azure
  rate limits are per deployment, so several deployments of the same model on
  one resource add capacity without changing the model.
* Resource pinning. With ``--pin-resources`` the gateway forwards to a local
  relay (such as an Azure bridge) and pins each task to one Azure resource by
  sending ``X-HCU-Resource``. Encrypted reasoning that the agent replays can be
  decrypted only by the resource that produced it; the relay may still move a
  rate-limited request between deployments of that resource.
* Task affinity. Agents that continue with ``previous_response_id`` rely on
  server-side state that exists only in the region that served the previous
  call. A task is therefore pinned to one upstream for its whole run; tasks
  are spread across upstreams round-robin.

The gateway never retries and never switches upstreams: switching would
invalidate ``previous_response_id`` mid-trajectory. Retries belong to the
agent, whose retries reuse the same ``/t/<task>/`` path and therefore the same
upstream. Rate limits and upstream errors are relayed unchanged and flagged
in the accounting row.

Usage::

    python -m hcu.gateway.openai_proxy --host <bridge-ip> --ports 8611 \\
        --out runs/openai-tokens.jsonl --upstreams <resource>.openai.azure.com

    OSW_OPENAI_UPSTREAM_TOKEN=<secret> \\
    python -m hcu.gateway.openai_proxy --host <bridge-ip> --ports 8611 \\
        --out runs/openai-tokens.jsonl --upstream-kind openai \\
        --upstreams api.x.ai
"""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socketserver
import subprocess
import sys
import threading
import time
import urllib.parse

import urllib3

from hcu.gateway.common import (
    GenerationBudget,
    MAX_CONSECUTIVE_GENERATION_FAILURES,
    TokenLog,
    new_stats,
    read_log_rows,
    relay_response,
    response_payloads,
    send_json,
    split_task_path,
)

# Azure serves the Responses API under /openai with an api-version query;
# OpenAI-compatible upstreams take the path unchanged.
UPSTREAM_KINDS = {
    "azure": {"prefix": "/openai", "query": "api-version=preview"},
    "openai": {"prefix": "", "query": ""},
}
AAD_RESOURCE = "https://cognitiveservices.azure.com"
# A silent upstream is abandoned after 10 minutes. Normal responses finish well
# within that (p99 78 s on the V1 panel); a relay may hold a rate-limited
# request for up to about 5 minutes before the first byte.
REQUEST_TIMEOUT = urllib3.Timeout(connect=15, read=600)
BUDGETED_PATH = "/v1/responses"
UNFORWARDED_REQUEST_HEADERS = frozenset({
    "host",
    "connection",
    "content-length",
    "authorization",
    "accept-encoding",
    "api-key",
})


def log(message: str) -> None:
    sys.stderr.write(f"[openai_proxy {time.strftime('%H:%M:%S')}] {message}\n")
    sys.stderr.flush()


class AzureToken:
    """AAD access token from the Azure CLI, refreshed before it expires."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._value: str | None = None
        self._expires_at = 0

    def refresh(self) -> str:
        completed = subprocess.run(
            [
                "az", "account", "get-access-token",
                "--resource", AAD_RESOURCE, "-o", "json",
            ],
            capture_output=True,
            text=True,
            timeout=60,
            check=True,
        )
        data = json.loads(completed.stdout)
        with self._lock:
            self._value = data["accessToken"]
            self._expires_at = int(data.get("expires_on", time.time() + 3000))
        expires = time.strftime("%H:%M:%S", time.localtime(self._expires_at))
        log(f"AAD token refreshed, expires at {expires}")
        return self._value

    def get(self) -> str:
        with self._lock:
            value, expires_at = self._value, self._expires_at
        if value and expires_at - time.time() > 60:
            return value
        return self.refresh()

    def refresh_in_background(self) -> None:
        def loop() -> None:
            while True:
                time.sleep(60)
                try:
                    with self._lock:
                        expires_at = self._expires_at
                    if expires_at - time.time() < 1800:
                        self.refresh()
                except Exception as exc:
                    log(f"background token refresh failed, will retry: {exc}")

        threading.Thread(target=loop, daemon=True).start()


class StaticToken:
    """A fixed bearer token for OpenAI-compatible upstreams."""

    def __init__(self, value: str) -> None:
        self._value = value

    def refresh(self) -> str:
        return self._value

    def refresh_in_background(self) -> None:
        return

    def get(self) -> str:
        return self._value


class OpenAIGateway:
    """Routing, budget and accounting state shared by all request threads."""

    def __init__(
        self,
        *,
        upstreams: list[str],
        model_upstreams: dict[str, list[str]],
        token_log: TokenLog,
        max_requests_per_task: int,
        token: AzureToken | StaticToken,
        upstream_kind: str = "azure",
        upstream_scheme: str = "https",
    ) -> None:
        if upstream_kind not in UPSTREAM_KINDS:
            raise ValueError(f"unknown upstream kind {upstream_kind!r}")
        self.upstreams = upstreams
        self.model_upstreams = model_upstreams
        self.token_log = token_log
        self.max_requests_per_task = max_requests_per_task
        self.token = token
        self.upstream_kind = upstream_kind
        self.upstream_scheme = upstream_scheme
        self.stats = new_stats()
        self._affinity_lock = threading.Lock()
        self._affinity: dict[tuple[str, str], str] = {}
        self._next_upstream: dict[tuple[str, ...], int] = {}
        self.pin_resources: list[str] = []
        self._task_resource: dict[str, str] = {}
        self.budget = GenerationBudget(max_requests_per_task)
        self._pool_lock = threading.Lock()
        self._pools: dict[str, urllib3.HTTPSConnectionPool] = {}

    @property
    def all_upstreams(self) -> set[str]:
        return set(self.upstreams) | {
            host for hosts in self.model_upstreams.values() for host in hosts
        }

    def restore(self) -> None:
        """Rebuild affinity and successful-delivery counts from the log."""
        allowed = self.all_upstreams
        affinity = {}
        for row in read_log_rows(self.token_log.path):
            task = row.get("task")
            endpoint = row.get("endpoint")
            if task and endpoint in allowed:
                affinity[(str(task), str(row.get("model") or ""))] = str(endpoint)
        with self._affinity_lock:
            self._affinity.update(affinity)
        self.budget.restore(
            row for row in read_log_rows(self.token_log.path)
            if row.get("path") == BUDGETED_PATH
        )
        if affinity:
            log(f"restored {len(affinity)} task/model upstream affinities")

    def host_for_task(self, task: str | None, model: str | None) -> str:
        """Pin a task to one upstream on first sight; never switch later."""
        task = task or "_notask"
        model = str(model or "")
        upstreams = self.model_upstreams.get(model) or self.upstreams
        if not upstreams:
            raise RuntimeError(f"model {model or '<unknown>'} has no upstreams")
        key = (task, model)
        pool_key = tuple(upstreams)
        with self._affinity_lock:
            host = self._affinity.get(key)
            if host is None:
                index = self._next_upstream.get(pool_key, 0)
                host = upstreams[index % len(upstreams)]
                self._next_upstream[pool_key] = index + 1
                self._affinity[key] = host
                log(
                    f"task {task[:8]} model={model or '<unknown>'} -> {host} "
                    "(pinned for the rest of the task)"
                )
            return host

    def resource_for_task(self, task: str | None) -> str | None:
        """Pin a task to one Azure resource on first sight, round-robin."""
        if not self.pin_resources:
            return None
        task = task or "_notask"
        with self._affinity_lock:
            resource = self._task_resource.get(task)
            if resource is None:
                resource = self.pin_resources[len(self._task_resource) % len(self.pin_resources)]
                self._task_resource[task] = resource
                log(f"task {task[:8]} -> resource {resource} (pinned)")
            return resource

    def pool(self, host: str) -> urllib3.HTTPSConnectionPool:
        with self._pool_lock:
            pool = self._pools.get(host)
            if pool is None:
                name, port = split_host(host)
                pool_class = (
                    urllib3.HTTPConnectionPool
                    if self.upstream_scheme == "http"
                    else urllib3.HTTPSConnectionPool
                )
                pool = pool_class(
                    name, port=port, maxsize=64, block=False, retries=False
                )
                self._pools[host] = pool
            return pool

    def healthz(self) -> dict:
        return {
            "ok": True,
            "pid": os.getpid(),
            "tasks": len(self._affinity),
            "out": str(self.token_log.path),
            "max_requests_per_task": self.max_requests_per_task,
            "max_completed_responses_per_task": self.max_requests_per_task,
            "step_unit": "successful_task_agent_response",
            "max_consecutive_generation_failures": MAX_CONSECUTIVE_GENERATION_FAILURES,
            **self.stats,
        }


def extract_usage(payload: dict) -> dict:
    usage = payload.get("usage") or {}
    return {
        "input_tokens": usage.get("input_tokens"),
        "cached_tokens": (usage.get("input_tokens_details") or {}).get(
            "cached_tokens"
        ),
        "output_tokens": usage.get("output_tokens"),
        "reasoning_tokens": (usage.get("output_tokens_details") or {}).get(
            "reasoning_tokens"
        ),
        "total_tokens": usage.get("total_tokens"),
    }


def parse_sse_for_usage(raw: bytes) -> dict | None:
    """Return the last streamed response object that carries usage."""
    found = None
    for line in raw.split(b"\n"):
        if not line.startswith(b"data:"):
            continue
        chunk = line[5:].strip()
        if not chunk or chunk == b"[DONE]":
            continue
        try:
            event = json.loads(chunk)
        except Exception:
            continue
        response = event.get("response") if isinstance(event, dict) else None
        if isinstance(response, dict) and response.get("usage"):
            found = response
    return found


def generation_succeeded(body: bytes, headers) -> bool:
    events = response_payloads(body, headers)
    if "text/event-stream" not in headers.get("content-type", ""):
        return bool(events) and events[-1].get("status") in {"completed", "incomplete"}
    terminal = False
    for event in events:
        if event.get("type") in {"error", "response.failed", "response.cancelled"}:
            return False
        if event.get("type") in {"response.completed", "response.incomplete"}:
            terminal = True
    return terminal


def make_handler(gateway: OpenAIGateway) -> type[BaseHTTPRequestHandler]:
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

        def do_DELETE(self) -> None:
            self.proxy("DELETE")

        def fail(self, status: int, message: str, row: dict) -> None:
            row["http_status"] = status
            row["error"] = message
            gateway.stats["errors"] += 1
            gateway.budget.finish(row, gateway.token_log)
            log(f"ERROR {message}")
            try:
                send_json(self, status, {
                    "error": {"message": f"token_proxy: {message}"},
                })
            except Exception:
                pass

        def reject_budget(self, row: dict, reason: str) -> None:
            row["http_status"] = 429
            row["error"] = reason
            if reason == "budget_rejected":
                gateway.stats["budget_rejected"] += 1
            gateway.budget.finish(row, gateway.token_log)
            try:
                send_json(self, 429, {
                    "error": {
                        "message": f"token_proxy: {reason}",
                        "type": reason,
                    }
                })
            except Exception:
                pass

        def proxy(self, method: str) -> None:
            task, local_path = split_task_path(self.path)
            parsed = urllib.parse.urlparse(local_path)
            kind = UPSTREAM_KINDS[gateway.upstream_kind]
            query = "&".join(part for part in (parsed.query, kind["query"]) if part)
            upstream_path = kind["prefix"] + parsed.path + (f"?{query}" if query else "")
            length = int(self.headers.get("content-length") or 0)
            body = self.rfile.read(length) if length else b""

            row = {
                "ts": time.time(),
                "task": task,
                "endpoint": None,
                "path": parsed.path,
                "method": method,
                "input_items": None,
                "previous_response_id": None,
                "model": None,
            }
            if body[:1] == b"{":
                try:
                    request = json.loads(body)
                    items = request.get("input")
                    row["input_items"] = len(items) if isinstance(items, list) else 1
                    row["previous_response_id"] = request.get("previous_response_id")
                    row["model"] = request.get("model")
                except Exception:
                    pass
            rejection = gateway.budget.admit(
                row, method == "POST" and parsed.path == BUDGETED_PATH
            )
            if rejection:
                self.reject_budget(row, rejection)
                return
            try:
                route = gateway.host_for_task(task, row["model"])
            except Exception as exc:
                self.fail(502, f"upstream routing failed: {exc}", row)
                return
            row["endpoint"] = route
            host, _, deployment = route.partition("@")
            if deployment and row["model"] is not None:
                request["model"] = deployment
                body = json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode()
            try:
                token = gateway.token.get()
            except Exception as exc:
                self.fail(502, f"AAD token acquisition failed: {exc}", row)
                return

            # Only authentication and Host are replaced; the body is untouched.
            headers = {
                key: value
                for key, value in self.headers.items()
                if key.lower() not in UNFORWARDED_REQUEST_HEADERS
            }
            headers["Authorization"] = f"Bearer {token}"
            headers["Host"] = host
            resource = gateway.resource_for_task(task)
            if resource:
                headers["X-HCU-Resource"] = resource
                row["resource"] = resource
            headers["content-length"] = str(len(body))
            headers["accept-encoding"] = "identity"

            started = time.time()
            gateway.stats["calls"] += 1
            try:
                response = gateway.pool(host).urlopen(
                    method,
                    upstream_path,
                    body=body,
                    headers=headers,
                    preload_content=True,
                    retries=False,
                    timeout=REQUEST_TIMEOUT,
                )
            except Exception as exc:
                row["latency_sec"] = round(time.time() - started, 3)
                self.fail(
                    502,
                    f"upstream {host} connection failed: {type(exc).__name__}: {exc}",
                    row,
                )
                return

            response_body = response.data
            status = response.status
            delivered = relay_response(self, status, response.headers, response_body)
            if not delivered:
                row["client_disconnected"] = True
            row["http_status"] = status
            row["latency_sec"] = round(time.time() - started, 3)
            task_label = (task or "")[:8]
            if status == 429:
                gateway.stats["rate_limited"] += 1
                row["rate_limited"] = True
                log(f"429 from {host} task={task_label}; relayed without retry")
            elif status >= 500:
                gateway.stats["errors"] += 1
                row["upstream_error"] = True
                log(f"{status} from {host} task={task_label}: {response_body[:200]!r}")
            elif status >= 400:
                gateway.stats["errors"] += 1
                row["upstream_error"] = True
                log(f"{status} from {host} task={task_label}: {response_body[:300]!r}")

            try:
                payload = None
                if "text/event-stream" in response.headers.get("content-type", ""):
                    payload = parse_sse_for_usage(response_body)
                elif response_body[:1] == b"{":
                    payload = json.loads(response_body)
                if isinstance(payload, dict):
                    row["response_id"] = payload.get("id")
                    row["response_status"] = payload.get("status")
                    row.update(extract_usage(payload))
                    if row.get("input_tokens") is not None:
                        gateway.stats["accounted"] += 1
            except Exception as exc:
                row["accounting_error"] = f"{type(exc).__name__}: {exc}"
                gateway.stats["errors"] += 1
                log(f"ERROR usage parsing failed task={task}: {exc}")
            succeeded = False
            if delivered and 200 <= status < 300 and row["generation_request"]:
                try:
                    succeeded = generation_succeeded(response_body, response.headers)
                except (ValueError, TypeError, KeyError, AttributeError):
                    pass
                if not succeeded:
                    row["generation_error"] = "missing successful generation terminal"
            gateway.budget.finish(row, gateway.token_log, succeeded)

    return Handler


def parse_hosts(value: str) -> list[str]:
    """Split a comma-separated upstream list.

    A host may carry a port for a local relay (``127.0.0.1:8537``); a scheme
    and a path stay rejected, because the scheme is chosen by
    ``--upstream-scheme`` and the path comes from the client.
    """
    return [host.strip() for host in value.split(",") if host.strip()]


def split_host(host: str) -> tuple[str, int]:
    host = host.partition("@")[0]
    name, separator, port = host.rpartition(":")
    if not separator or not port.isdigit():
        return host, 443
    return name, int(port)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--ports",
        default="8611",
        help="Comma-separated local ports to listen on.",
    )
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help=(
            "Listen address. Guests reach the gateway through the Docker "
            "bridge, so pass the bridge gateway IP for OSWorld runs."
        ),
    )
    parser.add_argument("--out", required=True, help="Token log JSONL path.")
    parser.add_argument(
        "--upstreams",
        required=True,
        help=(
            "Comma-separated upstream hosts such as "
            "<resource>.openai.azure.com or api.x.ai, without scheme or path."
        ),
    )
    parser.add_argument(
        "--upstream-kind",
        choices=sorted(UPSTREAM_KINDS),
        default="azure",
        help=(
            "azure: Azure OpenAI with an Azure CLI token. openai: any "
            "OpenAI-compatible /v1/responses host with a bearer token from "
            "--upstream-token or OSW_OPENAI_UPSTREAM_TOKEN."
        ),
    )
    parser.add_argument(
        "--upstream-token",
        default=os.environ.get("OSW_OPENAI_UPSTREAM_TOKEN", ""),
    )
    parser.add_argument(
        "--upstream-scheme",
        choices=("https", "http"),
        default="https",
        help="Scheme for upstream requests; http serves a local relay.",
    )
    parser.add_argument(
        "--model-upstreams",
        action="append",
        default=[],
        metavar="MODEL=HOST[,HOST...]",
        help="Per-model upstream pool; may be repeated.",
    )
    parser.add_argument(
        "--max-requests-per-task",
        type=int,
        default=0,
        help=(
            "Successful delivered generation responses per task agent; 0 "
            "disables the response cap. Consecutive failures remain bounded."
        ),
    )
    parser.add_argument(
        "--pin-resources",
        default="",
        help=(
            "Comma-separated Azure resource hosts. Each task is pinned to one, "
            "sent to the relay as X-HCU-Resource."
        ),
    )
    args = parser.parse_args()
    if args.max_requests_per_task < 0:
        parser.error("--max-requests-per-task must be at least 0")
    upstreams = parse_hosts(args.upstreams)
    if not upstreams:
        parser.error("--upstreams needs at least one host")
    model_upstreams = {}
    for spec in args.model_upstreams:
        model, separator, hosts_text = spec.partition("=")
        hosts = parse_hosts(hosts_text)
        if not separator or not model.strip() or not hosts:
            parser.error("--model-upstreams must be MODEL=HOST[,HOST...]")
        model_upstreams[model.strip()] = hosts
    invalid = [
        host
        for host in upstreams + [h for hosts in model_upstreams.values() for h in hosts]
        if "://" in host or "/" in host or any(ch.isspace() for ch in host)
        or host.count("@") > 1 or host.endswith("@")
    ]
    if invalid:
        parser.error(
            "upstreams must be bare hosts, optionally with a port and "
            "@DEPLOYMENT, without scheme, path or whitespace: "
            + ", ".join(invalid)
        )

    if args.upstream_kind == "openai" and not args.upstream_token:
        parser.error("--upstream-kind openai needs --upstream-token or OSW_OPENAI_UPSTREAM_TOKEN")
    token = (
        AzureToken() if args.upstream_kind == "azure" else StaticToken(args.upstream_token)
    )
    token_log = TokenLog(Path(args.out))
    gateway = OpenAIGateway(
        upstreams=upstreams,
        model_upstreams=model_upstreams,
        token_log=token_log,
        max_requests_per_task=args.max_requests_per_task,
        token=token,
        upstream_kind=args.upstream_kind,
        upstream_scheme=args.upstream_scheme,
    )
    gateway.pin_resources = parse_hosts(args.pin_resources)
    gateway.restore()
    try:
        gateway.token.refresh()
    except Exception as exc:
        log(f"FATAL initial token acquisition failed: {exc}")
        return 2
    gateway.token.refresh_in_background()

    socketserver.TCPServer.allow_reuse_address = True
    handler = make_handler(gateway)
    for port in args.ports.split(","):
        try:
            server = ThreadingHTTPServer((args.host, int(port)), handler)
        except OSError as exc:
            log(f"FATAL cannot listen on {args.host}:{port}: {exc}")
            return 2
        threading.Thread(target=server.serve_forever, daemon=True).start()
        log(
            f"listening on {args.host}:{port} -> {upstreams}; "
            f"model overrides {model_upstreams}"
        )
    log(f"accounting to {token_log.path}")
    try:
        while True:
            time.sleep(60)
            log(f"stats {gateway.stats} tasks={len(gateway._affinity)}")
    except KeyboardInterrupt:
        log(f"exit stats={gateway.stats}")
        return 0


if __name__ == "__main__":
    sys.exit(main())
