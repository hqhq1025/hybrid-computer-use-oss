#!/usr/bin/env python3
"""只记账、不改写的 /v1/responses 透传代理 —— 直连 Azure OpenAI，不经 azure-bridge。

两个存在理由：
  1. 官方 GPT54Agent 组装 model_usage 时只保留 input_tokens / output_tokens，
     reasoning_tokens 在 agent 内部就被丢掉了（gpt54_agent.py predict()）。
     不能改官方代码，所以把账记在链路上。
  2. 对照组不能被 azure-bridge 的 store=false 重放/亲和逻辑污染。这里只从
     bridge 抄认证与寻址（AAD token + /openai/v1 + api-version=preview），
     请求体一个字节都不改。

任务级 endpoint 亲和（硬约束）：
  官方 agent 用 previous_response_id 走服务端状态，而该状态只存在于发起那次
  调用的区域。所以一个任务从头到尾固定一个区域，任务之间才轮询分摊。
  worker 用 OPENAI_BASE_URL=http://127.0.0.1:<port>/t/<uuid>/v1，代理从路径取
  task id，首次见到时按轮询绑定区域，之后永远复用。

关于重试：代理**自己不重试、不切区域**。
  - 切区域会让 previous_response_id 失效，产生"看起来跑完了但中途换过区域"的
    脏轨迹 —— 宁可任务失败。
  - 重试是官方 agent 的职责（_create_response 里有 5 次重试），它重试时仍然打到
    同一个 /t/<uuid>/ 路径，因此天然落在同一区域，符合"在原区域退避重试"。
  - 429/5xx 原样回传，并在记账行里打 rate_limited / upstream_error 标记，
    限流是可见的，不会被代理偷偷藏起来。

用法：
    python token_proxy.py --ports 8560,8561 --out trace-official-x/_tokens.jsonl
"""

import argparse
import json
import os
import socketserver
import subprocess
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import urllib3

# 抄自 azure-bridge.py：默认使用 MSRA 共享 Azure OpenAI 的三个区域副本。
# 批量任务可通过 --upstreams 显式传入经过当轮预检的更大区域池。
DEFAULT_UPSTREAMS = [
    "msra-im-openai-eus2.openai.azure.com",
    "msra-im-openai-se.openai.azure.com",
    "msra-im-openai.openai.azure.com",
]
UPSTREAMS = list(DEFAULT_UPSTREAMS)
MODEL_UPSTREAMS = {}
UPSTREAM_PREFIX = "/openai"          # /v1/responses -> /openai/v1/responses
EXTRA_QUERY = "api-version=preview"
AAD_RESOURCE = "https://cognitiveservices.azure.com"
REQUEST_TIMEOUT = urllib3.Timeout(connect=15, read=1800)

_write_lock = threading.Lock()
_out_fh = None
OUTPUT_PATH = None
_stats = {
    "calls": 0,
    "accounted": 0,
    "errors": 0,
    "rate_limited": 0,
    "budget_rejected": 0,
}
MAX_REQUESTS_PER_TASK = 0
LIMITED_TASK_PREFIX = "osworld-v2-hybrid-"

_token_lock = threading.Lock()
_token = {"value": None, "expires_at": 0}

_affinity_lock = threading.Lock()
_affinity = {}      # (task uuid, model) -> host
_rr = {}            # upstream tuple -> next index

_request_count_lock = threading.Lock()
_admitted_requests = {}

_pools = {}
_pool_lock = threading.Lock()


def log(msg):
    sys.stderr.write(f"[token_proxy {time.strftime('%H:%M:%S')}] {msg}\n")
    sys.stderr.flush()


# --- AAD token：缓存 + 后台预刷新（抄 bridge 的 get_token 逻辑） ---
def _refresh_token():
    out = subprocess.run(
        ["az", "account", "get-access-token", "--resource", AAD_RESOURCE, "-o", "json"],
        capture_output=True, text=True, timeout=60, check=True)
    data = json.loads(out.stdout)
    with _token_lock:
        _token["value"] = data["accessToken"]
        _token["expires_at"] = int(data.get("expires_on", time.time() + 3000))
    log(f"AAD token 刷新完成，expires_at="
        f"{time.strftime('%H:%M:%S', time.localtime(_token['expires_at']))}")
    return _token["value"]


def get_token():
    with _token_lock:
        value, exp = _token["value"], _token["expires_at"]
    if value and exp - time.time() > 60:
        return value
    return _refresh_token()


def _bg_refresher():
    while True:
        time.sleep(60)
        try:
            with _token_lock:
                exp = _token["expires_at"]
            if exp - time.time() < 1800:
                _refresh_token()
        except Exception as exc:
            log(f"后台刷新 token 失败（下一轮重试）: {exc}")


def upstreams_for_model(model):
    return MODEL_UPSTREAMS.get(model) or UPSTREAMS


def host_for_task(task, model=None):
    """任务级亲和：首次见到该 task 时按轮询绑定区域，此后永不更换。"""
    if not task:
        task = "_notask"
    model = str(model or "")
    upstreams = upstreams_for_model(model)
    if not upstreams:
        raise RuntimeError(f"model {model or '<unknown>'} has no upstreams")
    affinity_key = (task, model)
    pool_key = tuple(upstreams)
    with _affinity_lock:
        host = _affinity.get(affinity_key)
        if host is None:
            index = _rr.get(pool_key, 0)
            host = upstreams[index % len(upstreams)]
            _rr[pool_key] = index + 1
            _affinity[affinity_key] = host
            log(
                f"task {task[:8]} model={model or '<unknown>'} -> {host}"
                "（本任务此后固定该区域）"
            )
        return host


def get_pool(host):
    with _pool_lock:
        pool = _pools.get(host)
        if pool is None:
            pool = urllib3.HTTPSConnectionPool(host, port=443, maxsize=64,
                                               block=False, retries=False)
            _pools[host] = pool
        return pool


def record(row):
    with _write_lock:
        _out_fh.write(json.dumps(row, ensure_ascii=False) + "\n")
        _out_fh.flush()


def restore_affinity(path, allowed_hosts):
    restored = {}
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            for line in handle:
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                task = row.get("task")
                model = str(row.get("model") or "")
                endpoint = row.get("endpoint")
                if task and endpoint in allowed_hosts:
                    restored[(str(task), model)] = str(endpoint)
    except FileNotFoundError:
        return 0

    with _affinity_lock:
        _affinity.update(restored)
    return len(restored)


def restore_admitted_requests(path):
    restored = {}
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            for line in handle:
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                task = row.get("task")
                if (
                    not task
                    or not str(task).startswith(LIMITED_TASK_PREFIX)
                    or row.get("path") != "/v1/responses"
                    or row.get("budget_rejected")
                ):
                    continue
                restored[str(task)] = restored.get(str(task), 0) + 1
    except FileNotFoundError:
        return 0

    with _request_count_lock:
        _admitted_requests.update(restored)
    return len(restored)


def admit_model_request(task, path):
    if (
        MAX_REQUESTS_PER_TASK <= 0
        or not task
        or not str(task).startswith(LIMITED_TASK_PREFIX)
        or path != "/v1/responses"
    ):
        return True, None
    task = str(task)
    with _request_count_lock:
        admitted = _admitted_requests.get(task, 0)
        if admitted >= MAX_REQUESTS_PER_TASK:
            return False, admitted + 1
        admitted += 1
        _admitted_requests[task] = admitted
        return True, admitted


def _dig(d, *path):
    cur = d
    for key in path:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(key)
        if cur is None:
            return None
    return cur


def extract_usage(payload):
    usage = payload.get("usage") or {}
    return {
        "input_tokens": usage.get("input_tokens"),
        "cached_tokens": _dig(usage, "input_tokens_details", "cached_tokens"),
        "output_tokens": usage.get("output_tokens"),
        "reasoning_tokens": _dig(usage, "output_tokens_details", "reasoning_tokens"),
        "total_tokens": usage.get("total_tokens"),
    }


def parse_sse_for_usage(raw):
    """流式：取最后一个带 usage 的 response 事件。官方 agent 目前非流式，
    但兼容住，避免将来一改就静默丢账。"""
    found = None
    for line in raw.split(b"\n"):
        if not line.startswith(b"data:"):
            continue
        chunk = line[5:].strip()
        if not chunk or chunk == b"[DONE]":
            continue
        try:
            evt = json.loads(chunk)
        except Exception:
            continue
        resp = evt.get("response") if isinstance(evt, dict) else None
        if isinstance(resp, dict) and resp.get("usage"):
            found = resp
    return found


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def _split_task(self):
        path = self.path
        task = None
        if path.startswith("/t/"):
            task, _, tail = path[3:].partition("/")
            path = "/" + tail
        return task, path

    def do_GET(self):
        if self.path == "/healthz":
            body = json.dumps(
                {
                    "ok": True,
                    "pid": os.getpid(),
                    "tasks": len(_affinity),
                    "out": OUTPUT_PATH,
                    "max_requests_per_task": MAX_REQUESTS_PER_TASK,
                    **_stats,
                }
            ).encode()
            self.send_response(200)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self._proxy("GET")

    def do_POST(self):
        self._proxy("POST")

    def do_DELETE(self):
        self._proxy("DELETE")

    def _fail(self, status, message, row):
        row["error"] = message
        _stats["errors"] += 1
        record(row)
        log(f"ERROR {message}")
        body = json.dumps({"error": {"message": f"token_proxy: {message}"}}).encode()
        try:
            self.send_response(status)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except Exception:
            pass

    def _reject_budget(self, row):
        row["http_status"] = 429
        row["budget_rejected"] = True
        row["error"] = (
            "task model-call budget exhausted before upstream admission"
        )
        _stats["budget_rejected"] += 1
        record(row)
        body = json.dumps({
            "error": {
                "message": "token_proxy: task model-call budget exhausted",
                "type": "model_call_budget_exhausted",
            }
        }).encode()
        try:
            self.send_response(429)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except Exception:
            pass

    def _proxy(self, method):
        task, local_path = self._split_task()
        parsed = urllib.parse.urlparse(local_path)
        query = (parsed.query + "&" if parsed.query else "") + EXTRA_QUERY
        full_path = f"{UPSTREAM_PREFIX}{parsed.path}?{query}"

        length = int(self.headers.get("content-length") or 0)
        body = self.rfile.read(length) if length else b""

        row = {"ts": time.time(), "task": task, "endpoint": None,
               "path": parsed.path, "input_items": None,
               "previous_response_id": None, "model": None}
        if body[:1] == b"{":
            try:
                req = json.loads(body)
                inp = req.get("input")
                row["input_items"] = len(inp) if isinstance(inp, list) else 1
                row["previous_response_id"] = req.get("previous_response_id")
                row["model"] = req.get("model")
            except Exception:
                pass
        admitted, ordinal = admit_model_request(task, parsed.path)
        if ordinal is not None:
            row["request_ordinal"] = ordinal
            row["request_limit"] = MAX_REQUESTS_PER_TASK
        if not admitted:
            return self._reject_budget(row)
        try:
            host = host_for_task(task, row["model"])
        except Exception as exc:
            return self._fail(502, f"上游路由失败: {exc}", row)
        row["endpoint"] = host

        try:
            token = get_token()
        except Exception as exc:
            return self._fail(502, f"AAD token 获取失败: {exc}", row)

        # 请求头：只替换认证与 Host，其余原样透传；body 一个字节都不改
        headers = {}
        for key, value in self.headers.items():
            if key.lower() in ("host", "connection", "content-length",
                               "authorization", "accept-encoding", "api-key"):
                continue
            headers[key] = value
        headers["Authorization"] = f"Bearer {token}"
        headers["Host"] = host
        headers["content-length"] = str(len(body))
        headers["accept-encoding"] = "identity"

        t0 = time.time()
        _stats["calls"] += 1
        try:
            resp = get_pool(host).urlopen(method, full_path, body=body, headers=headers,
                                          preload_content=True, retries=False,
                                          timeout=REQUEST_TIMEOUT)
        except Exception as exc:
            row["latency_sec"] = round(time.time() - t0, 3)
            return self._fail(502, f"上游 {host} 连接失败: {type(exc).__name__}: {exc}", row)

        resp_body = resp.data
        ctype = resp.headers.get("content-type", "")
        status = resp.status

        # 原样回传。任务被 harness 中止时客户端会先断开；上游响应仍要记账，
        # 但不能让 socketserver 把预期的 BrokenPipe 打成整页 traceback。
        try:
            self.send_response(status)
            for key, value in resp.headers.items():
                if key.lower() in ("transfer-encoding", "content-length", "connection",
                                   "content-encoding"):
                    continue
                self.send_header(key, value)
            self.send_header("content-length", str(len(resp_body)))
            self.end_headers()
            self.wfile.write(resp_body)
        except (BrokenPipeError, ConnectionResetError):
            row["client_disconnected"] = True

        row["http_status"] = status
        row["latency_sec"] = round(time.time() - t0, 3)
        if status == 429:
            _stats["rate_limited"] += 1
            row["rate_limited"] = True
            log(f"429 from {host} task={(task or '')[:8]}（不切区域、不代理重试，"
                f"由官方 agent 自己退避重试）")
        elif status >= 500:
            _stats["errors"] += 1
            row["upstream_error"] = True
            log(f"{status} from {host} task={(task or '')[:8]}: {resp_body[:200]!r}")
        elif status >= 400:
            _stats["errors"] += 1
            row["upstream_error"] = True
            log(f"{status} from {host} task={(task or '')[:8]}: {resp_body[:300]!r}")

        try:
            payload = None
            if "text/event-stream" in ctype:
                payload = parse_sse_for_usage(resp_body)
            elif resp_body[:1] == b"{":
                payload = json.loads(resp_body)
            if isinstance(payload, dict):
                row["response_id"] = payload.get("id")
                row["response_status"] = payload.get("status")
                row.update(extract_usage(payload))
                if row.get("input_tokens") is not None:
                    _stats["accounted"] += 1
        except Exception as exc:
            row["accounting_error"] = f"{type(exc).__name__}: {exc}"
            _stats["errors"] += 1
            log(f"ERROR 记账解析失败 task={task}: {exc}")
        record(row)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ports", default="8560,8561", help="本地监听端口，逗号分隔")
    ap.add_argument(
        "--host",
        default="127.0.0.1",
        help="监听地址。容器评测应显式传 Docker bridge gateway，例如 172.20.0.1",
    )
    ap.add_argument("--out", required=True)
    ap.add_argument(
        "--upstreams",
        default=",".join(DEFAULT_UPSTREAMS),
        help=(
            "Azure OpenAI upstream host，逗号分隔；只传 host，"
            "不要包含 scheme 或路径"
        ),
    )
    ap.add_argument(
        "--model-upstreams",
        action="append",
        default=[],
        metavar="MODEL=HOST[,HOST...]",
        help=(
            "按模型覆盖 upstream 池，可重复传入；例如 "
            "gpt-5.6-sol=msra-im-openai-se.openai.azure.com"
        ),
    )
    ap.add_argument(
        "--max-requests-per-task",
        type=int,
        default=0,
        help=(
            "Hard upstream-admission cap for each osworld-v2-hybrid task; "
            "0 disables the cap. Judge and non-task affinity paths are exempt."
        ),
    )
    args = ap.parse_args()
    if args.max_requests_per_task < 0:
        ap.error("--max-requests-per-task must be at least 0")

    upstreams = [host.strip() for host in args.upstreams.split(",") if host.strip()]
    if not upstreams:
        ap.error("--upstreams 至少需要一个 host")

    model_upstreams = {}
    for spec in args.model_upstreams:
        model, separator, hosts_text = spec.partition("=")
        model = model.strip()
        hosts = [
            host.strip()
            for host in hosts_text.split(",")
            if host.strip()
        ]
        if not separator or not model or not hosts:
            ap.error(
                "--model-upstreams 必须是 MODEL=HOST[,HOST...]"
            )
        model_upstreams[model] = hosts

    all_hosts = upstreams + [
        host
        for hosts in model_upstreams.values()
        for host in hosts
    ]
    invalid = [
        host
        for host in all_hosts
        if "://" in host or "/" in host or any(ch.isspace() for ch in host)
    ]
    if invalid:
        ap.error(
            "upstream 只能包含 host，不得包含 scheme、路径或空白: "
            + ", ".join(invalid)
        )

    global _out_fh, OUTPUT_PATH, UPSTREAMS, MODEL_UPSTREAMS
    global MAX_REQUESTS_PER_TASK
    UPSTREAMS = upstreams
    MODEL_UPSTREAMS = model_upstreams
    MAX_REQUESTS_PER_TASK = args.max_requests_per_task
    with _affinity_lock:
        _affinity.clear()
        _rr.clear()
    with _request_count_lock:
        _admitted_requests.clear()
    OUTPUT_PATH = os.path.abspath(args.out)
    os.makedirs(os.path.dirname(OUTPUT_PATH), exist_ok=True)
    restored = restore_affinity(OUTPUT_PATH, set(all_hosts))
    if restored:
        log(f"从既有记账恢复 {restored} 个 task/model 区域亲和")
    restored_request_counts = restore_admitted_requests(OUTPUT_PATH)
    if restored_request_counts:
        log(f"从既有记账恢复 {restored_request_counts} 个 task 请求计数")
    _out_fh = open(OUTPUT_PATH, "a", buffering=1)

    try:
        _refresh_token()
    except Exception as exc:
        log(f"FATAL 首次获取 AAD token 失败: {exc}")
        return 2
    threading.Thread(target=_bg_refresher, daemon=True).start()

    socketserver.TCPServer.allow_reuse_address = True
    for port_s in args.ports.split(","):
        port = int(port_s)
        try:
            srv = ThreadingHTTPServer((args.host, port), Handler)
        except OSError as exc:
            log(f"FATAL 无法监听 {args.host}:{port}: {exc}")
            return 2
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        log(
            f"listen {args.host}:{port} -> 默认 {UPSTREAMS}; "
            f"模型覆盖 {MODEL_UPSTREAMS}"
        )

    log(f"记账写入 {OUTPUT_PATH}")
    try:
        while True:
            time.sleep(60)
            log(f"stats {_stats} tasks={len(_affinity)}")
    except KeyboardInterrupt:
        log(f"退出 stats={_stats}")
        return 0


if __name__ == "__main__":
    sys.exit(main())
