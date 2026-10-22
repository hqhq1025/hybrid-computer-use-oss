# Code Entry Points: Daemon, CLI, SDK, PTC

One core implements the contract. MCP, the daemon socket, the command line,
the Python SDK and the programmatic tool calling (PTC) adapter are transports
over the same `callTool`. Adapters construct requests and reject ambiguous
client arguments. The core validates desktop requests, resolves refs, and
defines operation errors. Adapters print or return the core's result.

```text
 MCP stdio      CLI          hcu_sdk (sync/async)    PTC forwarder
    |            |                    |                    |
    | in-process | Unix socket, line JSON-RPC (one daemon per desktop)
    v            v                    v                    v
 +--------------------------------------------------------------+
 | core: schema + decoder + callTool + text/records rendering    |
 | desktop state: provider, snapshots, aN/wN/rN, request_id,     |
 |                input lock                                     |
 | session state: delivered trees, diff baselines, find cursors, |
 |                pending observations, GUI frame                |
 +--------------------------------------------------------------+
```

The picture shows the entry points; the core sits below all of them.

## Core boundary

Go paths are relative to `computer-use/`.

| Role | Files |
| --- | --- |
| Contract | `internal/contract/`: `tool_contract.go` (schemas, descriptions), `published_values.go` (public enums, codes, limits), `schema_validator.go`, `tool_argument_rules.go`, `contract_describe.go` (contract version, error table, `DescribeContract`) |
| Core: decoding | `internal/core/ax_public_request.go` (decoder, public codes), `internal/core/argument_errors.go` |
| Core: state | `internal/core/service.go` (`DesktopState`, `sessionState`), `ax_ref_registry.go`, `ax_find_cursor.go`, `ax_snapshot_store.go`, `ax_failure_recovery.go`, `request_replay.go` |
| Core: operations | `internal/core/ax_*.go`, `gui_service.go`, `app_snapshot.go` |
| Core: rendering | `internal/core/ax_renderer.go`, `ax_evidence.go`, `ax_response_budget.go`, `tool_result.go` (`FinalizeForModel`), `records.go` (records) |
| Provider port | `internal/core/platform_contract.go`, `platform_default.go`, `provider_*.go` |
| Provider backends | `internal/provider/`: `runtime_backend_<os>.go`, `linux_*.go`, `darwin_*.go`, `windows_*.go`; `native/` |
| Adapters | `internal/transport/`: `mcp_transport.go`, `mcp_attach.go` (MCP); `daemon.go`, `daemon_client.go`, `daemon_spawn_*.go`, `peer_cred_*.go` (socket); `cli.go`, `cli_main.go` (command line); `cmd/computer-use/main.go`; `sdk/python/` (SDK, PTC) |

Each role sits in its own Go package; [Architecture](ARCHITECTURE.md#go-packages)
lists the packages, their import direction and which files change together.

## Single source

The core is the only implementation of the contract. There are no generated
schema, type or error files:

- `computer-use schema` and the daemon's `schema` method return the exact
  published tool schemas and the error table (`DescribeContract`).
- The SDK creates its exception classes from code names; a test checks that
  every code in `schema` has one.
- The PTC adapter builds `hcu_ax` and `hcu_gui` from `schema` at runtime.
- The CLI shorthand table (`cliShorthands` in `internal/transport/cli.go`) is the only
  hand-maintained mapping; `TestCLIShorthandsProduceSchemaValidCalls` checks
  every entry against the decoder and the published schema.

Drift tests: SDK helpers produce calls the core accepts
(`sdk/python/tests/test_sdk.py`). The CLI shorthands
(`TestCLIReachesEveryPublishedItem`) and the SDK helpers
(`test_helpers_reach_every_published_item`) each reach every AX mode, observe
value, act step type, wait step kind and GUI action type read from the
published schemas. The few items not covered are listed by name as reachable
only through `--json` or the generic `act`/`gui`/`call`. MCP and `call` return byte-identical text
(`TestCallOutputMatchesMCPToolResult`), the CLI prints the MCP text
(`TestCLITextMatchesMCPOverTheDaemon`), attached MCP matches in-process MCP
(`TestAttachedMCPMatchesInProcessAndSharesRefs`), and every public code is in
the error table (`TestEveryPublicErrorCodeIsPublished`).

## Records

`format: "records"` renders the same call as one JSON object, for success and
error alike:

```json
{"ok":true,"op":"ax.find","window":{"ref":"w1","title":"..."},
 "matches":[{"ref":"r41","role":"button","name":"Sort Ascending","states":{"pressed":"false"},"bounds":[412,88,24,24],"depth":0,"parent":"r30"}],
 "page":{"returned":20,"total":63,"offset":0,"next_cursor":"f3"},
 "input_emitted":false,"timing_ms":{"provider":612,"core":9,"total":621}}
```

Errors carry `code`, `path`, `message` and `hint`, the same values as the MCP
evidence line. Records differ from text in four ways, all aimed at code:

- `observe` returns every canonical element and is never cut by the model
  text budget; `find` pages by `limit` and `next_cursor`.
- `act` defaults to `observe: "none"`; ask for `"diff"` explicitly.
- `since: "<snapshot_id>"` on `observe` returns added, removed and changed
  elements against an earlier records snapshot of the same session.
- Images are files; the record holds their paths.
- `screen{width,height,coordinate_space,image}` names the current GUI frame:
  GUI `x`/`y` are pixels of that image. GUI screenshots and input batches
  set it, and so do AX `observe:"full"` and `observe:"screenshot"`, because
  their image is the same full-desktop frame. A GUI input call may follow
  either without a new screenshot.
- A GUI `zoom` record has `images[0]` (the zoom image) and
  `evidence.zoom{frame,region,width,height,scale_x,scale_y}`, and no
  `screen`: the zoom image is not a frame. Zoom pixel `(u, v)` is frame pixel
  `x = region[0] + u/scale_x`, `y = region[1] + v/scale_y`.

`{"mode":"read","refs":["r1",...]}` is a records-only AX mode: one live
resolve per ref, no tree walk. It is not in the MCP schema and MCP rejects it.

Records never reach a model through MCP: the MCP server always returns text,
so its contract golden is unchanged.

## Daemon

`computer-use serve [--socket PATH] [--idle-timeout 30m]` serves one desktop.
The CLI and SDK start it on first use.

| Property | Behaviour |
| --- | --- |
| Socket | `$XDG_RUNTIME_DIR/hcu/desktop-<display>.sock`, or `hcu-<uid>/` in the system temporary directory without `XDG_RUNTIME_DIR`; `HCU_SOCKET` overrides it. `<display>` comes from `WAYLAND_DISPLAY` or `DISPLAY`. Directory 0700, socket 0600; `SO_PEERCRED` uid check on Linux; refuses a socket path owned by another user; replaces a stale socket, never a live one |
| Start | on Linux and macOS, the first call forks `computer-use serve`; a `flock` beside the socket makes concurrent first calls start one daemon. Other platforms run `computer-use serve` explicitly |
| Methods | `hello` (version, contract version, display check), `call`, `schema`, `status`, `stop`, `mcp` |
| Sessions | each connection has its own session unless `hello` names one (`HCU_SESSION`, `--session`); same name, same session |
| Input lock | one call runs at a time across all sessions, so input never interleaves. A `wait` step polls inside its call and holds the lock for at most its `timeout_ms` (default 5000, at most 60000); the lock is not released between polls. The CLI `wait` command and SDK `wait()` send `observe:"none"`, so a wait adds no tree read after it, also when it times out. An act of only `wait` steps sends no input, so an in-process MCP server allows it while a daemon runs |
| `request_id` | within one running daemon, a call with an id runs once; a repeat (also on a new connection) returns the first result with `replayed: true`; kept 10 minutes. Restart clears this replay cache |
| Environment | `hello` with another display is refused; a call after the daemon's own session environment changed is refused with `runtime_error` |
| Restart | ordinary calls and attached MCP calls save ref counters beside the socket before returning refs. A normal restart reads those counters and does not reissue old numbers; old refs fail as expired. This is not a crash-safe allocation guarantee |
| Idle | exits after the idle timeout with no connection |

If ref counters cannot be saved, the daemon withholds the new refs and refuses
later calls. The error retains available input and partial-completion facts;
the preceding action may already have run. Invalid existing counter files
prevent startup. Preserve the counter file when restarting the daemon.

## Command line

```text
computer-use apps | observe | wait | find | read | click | type | key | set |
             select | scroll | activate | close | act | gui   shorthand calls
computer-use batch [FILE|-] [--keep-going] [--text]     JSON array of {"tool","args"}
computer-use schema                                     contract as JSON
computer-use serve [--socket PATH] [--idle-timeout 30m] daemon in the foreground
computer-use daemon start|stop|status
computer-use mcp --attach SOCKET                        MCP through the daemon
computer-use help cli                                   full usage and exit codes
```

Input shorthands also take `--window wN`, `--as none|diff|tree` and
`--wait STEP_JSON`, one wait step that runs after the action, for example
`--wait '{"window":"Save"}'`. `select rN [TEXT] [--to rN]` selects an
element, a substring of it, or a range. `gui` has `screenshot`,
`zoom X0 Y0 X1 Y1`, `click|double_click|move X Y`, `type TEXT` and
`key CHORD`. GUI `drag`, `scroll` and `wait` take `gui --json`, and an AX
batch takes `act --json`. `--json -` reads the body from standard input and
`--json @FILE` from a file, so code that builds a command need not quote
JSON for the shell. `observe --as full|screenshot` prints an
`image: full desktop WxH, the current GUI frame` line before the image path.
After that line, `gui click` may use the image's pixels. `gui zoom` prints the
mapping line and the zoom image path. Common options are `--session NAME`
(`HCU_SESSION`), `--socket PATH` (`HCU_SOCKET`), `--request-id ID` and
`--since SNAPSHOT_ID`.

Default output is the MCP text, byte for byte; `--json` (for `act` and `gui`,
`--records`) prints the record. Exit status comes from the error table: 0
success, 2 `invalid_argument`, 3 `wait_timeout`, 4
`stale_ref`/`gui_frame_stale`, 5 `window_changed`/`effect_unknown`, 6
`app_unavailable`/`ambiguous_window`/`window_not_found`/`no_focused_window`/
`permission_denied`, 10 `runtime_error`, 64 usage. No exit status mixes codes that sent no input
with codes that may have.

Unknown options on shorthand, batch, schema, and daemon commands fail with
exit 64 before connecting to the daemon. JSON arguments must contain exactly one value; trailing whitespace
is allowed, but a second value or trailing garbage is rejected before any
action. This also applies to `--wait` steps, `call --args`/`--args-file`,
`call --calls`/`--calls-file`, and `batch`. One-shot commands keep their existing error exit
status. `find --text VALUE` takes a search value; `batch --text` selects text
output and does not take a value.

`computer-use call` keeps its one-shot behaviour (own process, no daemon) and
prints the same model-facing result as MCP, with images as file paths.

## MCP with a daemon

- `computer-use mcp` without `--attach` runs in process as before. If a
  daemon serves the same desktop, it refuses input (`act`, GUI input) with a
  message that names `--attach`, and still answers reads. Without a daemon
  nothing changes, so OSWorld runs are unaffected.
- `computer-use mcp --attach SOCKET` forwards every MCP request to the daemon
  session `mcp`. Its bytes match the in-process server; refs and the input
  lock are shared with the CLI and SDK; its diff baseline is its own.

## Python SDK

`sdk/python/hcu_sdk` (standard library only, Python 3.8+); put `sdk/python` on
`PYTHONPATH`. The SDK starts the daemon with `computer-use` from `PATH`, or
from `HCU_BIN`. Import it as
`import hcu_sdk as hcu`; the repository's harness package already owns the
name `hcu`.

- `Desktop.call(tool, args)` is the whole API; helpers (`apps`, `observe`,
  `find`, `find_all`, `read`, `act`, `click`, `type`, `key`, `set_value`,
  `select`, `scroll`, `activate`, `close_window`, `screenshot`, `zoom`,
  `click_zoom`, `gui`, `wait`) only build argument dicts. GUI input actions go through
  `gui(actions)`, with `x`/`y` in pixels of the latest `screen` image.
- `zoom(region)` returns the zoom record. `hcu.zoom_point(record, u, v)`
  converts a zoom image pixel to the frame pixel that GUI `x`/`y` take, using
  the record's `evidence.zoom`. Code does not parse the mapping text:

  ```python
  frame = d.screenshot()                        # or d.observe(w, observe="full")
  z = d.zoom([600, 60, 680, 100])               # frame pixels
  x, y = hcu.zoom_point(z, 212, 96)             # a point seen in the zoom image
  d.gui([{"type": "click", "x": x, "y": y}])
  d.click_zoom(z, 212, 96)                      # the same click in one call
  ```

  `click_zoom` sends an ordinary GUI click at the converted frame pixel, so
  the core checks it against the current frame like any other click.

- `screenshot()` and `zoom()` only look: they get no `request_id`, and a
  dropped connection retries them like any other read.
- Records are dicts with attribute access. `ok:false` raises `HcuError`; the
  subclass is named after the code (`hcu.StaleRef`).
- `observe` and `find` accept a positional `target`, or one named `window_ref`
  or `app`. Do not combine these target forms. `find` accepts the published
  query names, including `name_equals`, `name_contains`, `text_equals`, and
  `text_contains`. The shorthand names `name`, `name_has`, `text`, and
  `text_has` remain accepted. Supplying both names for one field is an error.
  Unknown query fields reach the core's normal argument error, not a Python
  `KeyError`. A cursor cannot be combined with a target, scope, or query.
- `act` and GUI input get a fresh `request_id`; after a dropped connection
  the SDK binds any automatic retry to the original daemon instance. A new
  daemon stops the retry before desktop input and raises
  `TransportEffectUnknown`; `input_emitted=None` means the original effect is
  unknown. The socket protocol carries this binding outside AX/GUI arguments.
- Automatic input retries set `replay_only`: the daemon may return a cached
  result or wait for the original in-flight call, but cannot start a new call.
  A missing or expired replay entry raises `TransportEffectUnknown`. This can
  stop recovery even when the first request never reached the daemon; the SDK
  cannot distinguish that case from a completed call whose result was lost.
- `AsyncDesktop` has the same methods as coroutines, over the socket or any
  async transport (`hcu.ptc_transport(hcu_ax, hcu_gui)` in a PTC container).
- One `AsyncDesktop` queues transport calls before submitting work. Cancelling
  a queued call prevents it from being submitted. A running call settles its
  transport before cancellation returns, including after repeated cancellation.
  `CallCancelled` subclasses `asyncio.CancelledError` and retains `request_id`,
  `record`, `transport_error`, and `input_emitted` (`None` when unknown).
  Cancellation does not undo input. Inspect the record before continuing;
  do not rerun the cancelled code cell automatically.
- A synchronous transport runs in a worker thread. Cancelling its asyncio
  caller cannot stop that thread, so cancellation may wait for its configured
  transport timeout. A lost connection can leave remote effects unknown even
  after the local thread finishes. Custom async transports must settle their
  own work before returning or raising; detached background work is outside
  this guarantee. Share one `AsyncDesktop` per active Python event loop.
- A shared `SocketTransport` serializes the complete request and response,
  including connection setup and hello. Responses must match their request
  IDs. A failed hello, EOF, or invalid response clears the connection state.
  Invalid responses raise a protocol error without an automatic retry.
- Legacy daemons without an instance ID do not support automatic input replay.
  Custom transports remain callable, but a dropped mutation is not retried
  unless the transport raises `TransportClosed(..., retry_safe=True)`. That
  flag requires the transport to guarantee that identical params cannot
  repeat input, including after restart. Reads still retry on a dropped link.
- CLI calls do not reconnect and resend automatically. Manually running a new
  CLI command with the same request ID after daemon restart is a new call,
  not a durable replay guarantee.
- Examples: `sdk/python/examples/` (read a sheet, check a menu item, save as).

The live schema describes the wire request. Helpers flatten find query fields:

```python
tree = d.observe(window_ref=window_ref)
matches = d.find(window_ref=window_ref, role="button", name_contains="Save")
```

SDK actions default to `observe="none"`. Request `observe="diff"` when the
next decision needs the resulting window state. A screenshot record contains
a file path; the host must deliver the image bytes to the model. When the
path is already known, code can read and display it in the same code cell.
Neither a Python loop nor an entire code cell is an atomic desktop batch.
Use `act(actions=[...])` or `gui(actions=[...])` for known ordered actions.

## Python code executors and OpenGCUA

`hcu_sdk.code.CodeDesktop` extends `AsyncDesktop` for an existing Python
executor. It does not execute model code or add desktop tools. Configure a
synchronous `output(event)` sink and, for images, a synchronous
`image_reader(path)` returning `(media_type, bytes)`. No image file is read
implicitly. `hcu_sdk.ptc.local_image_reader(output_dir)` provides a bounded
local reader; remote desktops need their own explicit reader.

Successful calls return records without automatically displaying them.
`await d.emit(value)` displays selected text or JSON. `await d.emit_image(record)`
reads the existing `screen.image` and sends PNG/JPEG bytes to the sink; it
does not repeat the desktop call. The sink must send actual image content to
the model and preserve the delivered pixel coordinates. A GUI frame image
(GUI screenshot, AX `full`/`screenshot`) has `coordinate_space:
"image_pixels"`. A zoom image has `coordinate_space: "zoom_pixels"` and its
`zoom` mapping; GUI `x`/`y` never take zoom pixels directly.

```python
frame = await d.screenshot()
await d.emit({"screen": frame.screen})
await d.emit_image(frame)
```

HCU errors and cancelled calls automatically publish complete error events
before raising, even if model code catches the exception. Events retain the
core record, partial input, and unknown effects. If the sink fails, the
original exception remains authoritative and its `output_error` attribute
describes the failed delivery. Image retrieval failures raise `OutputError`
with the original record. Neither failure retries desktop input.

`sdk/python/examples/open_gcua_worker.py` injects this adapter into OpenGCUA's
existing `CodeExecutor`. The example names the OpenGCUA revision it was
tested against and uses private bootstrap hooks, not a supported plugin
interface. The host and worker require Python 3.11+ and Pillow. Configure
these variables:

| Variable | Value |
| --- | --- |
| `OPEN_GCUA_RUNTIME_DIR` | `<OpenGCUA>/packages/harness/runtime` |
| `PYTHONPATH` | Include `<HCU>/sdk/python` |
| `HCU_SOCKET` | Existing desktop daemon socket |
| `HCU_SESSION` | Unique session for the task |
| `HCU_OUTPUT_ROOT` | Daemon screenshot directory visible to the worker |

Set `CodeExecutor(runtime_path=...)` to the example file and configure its
normal work and image directories. Model code receives `d`, `emit`,
`emit_image`, `hcu`, and `asyncio`. The host binds each presentation's
`screenshot_index` to its file and delivers the image without resizing.
Use HCU instructions with screenshot-pixel coordinates. The original Mac
prompt describes `device`/`ui` and normalized coordinates and is incompatible.

Create one executor per isolated task. The example rejects the upstream
task/context reset operations; start a new executor and session instead.
Persistent variables do not keep stale HCU refs valid. This executes trusted
Python and is not a security sandbox. Tests cover the real upstream worker
with controlled transport and with a real Docker desktop. The desktop test
used a read-only CPython 3.11.13 and Pillow 11.3.0 overlay because the existing
image has Python 3.10. Three cells verified persistent state, AX input and
independent GTK readback, image events, and caught error evidence. No model
evaluation of this adapter has been run.

## Programmatic tool calling

`hcu_sdk.ptc` serves Anthropic programmatic tool calling:

- `tool_definitions(d.schema())` returns `hcu_ax` and `hcu_gui` client tool
  definitions built from the live schema, with local `$ref` inlined and the
  records options added. `hcu_ax` sets `allowed_callers:
  ["code_execution_20260120"]`; `hcu_gui` stays direct unless
  `gui_from_code=True`. Code callers receive a records JSON string.
- Keep one `Forwarder(desktop, image_reader=...)` for the API conversation.
  It uses `Desktop.call`, including daemon-instance and cache-only retry
  checks. Input gets a request ID when none was supplied. Repeated
  `tool_use.id` values return the retained outcome without another desktop
  call. Reusing an event ID with different arguments fails before input.
- Events remain in memory for the forwarder's lifetime. The default limit
  is 1,000 events; reaching it refuses new events rather than evicting old
  ones. A new API conversation needs a new forwarder. This state is not
  durable: do not replay pending input with a new forwarder after a harness
  restart. Distinct event IDs are new calls, even with identical arguments.
- `forward(tool_use, desktop)` retains the same ledger on the supplied
  Desktop as a convenience. Use the explicit Forwarder for image delivery.
- Direct calls with screenshots return a text block containing records and
  image blocks containing the screenshot bytes. Configure an explicit
  `image_reader(path)` returning `(media_type, bytes)`. The adapter accepts
  PNG/JPEG images of at most 5 MiB each; it never reads guest paths implicitly.
  `local_image_reader(output_dir)` reads only regular PNG/JPEG files beneath
  that configured POSIX directory and rejects symlinks. For a remote guest,
  provide a bounded reader over your existing guest connection. A path on the
  guest is not a file in the cloud code-execution container.
- Image retrieval failure returns `delivery_error` and outer `is_error:true`
  while retaining the original input and partial-completion facts. Retrying
  the same event can retrieve its image again but never repeats the input.
  Code callers receive JSON only; use a direct GUI observation to show an
  image to the model. Allowing GUI from code does not itself display images.
- Core errors remain records JSON so code can inspect `ok`, `code`, and input
  evidence. An uncertain transport failure after attempted input returns
  `effect_unknown` with `input_emitted:null`; it does not assert no input.
- `sdk/python/examples/ptc_messages.py` shows a bounded Messages continuation
  loop. It preserves assistant blocks, returns every tool result, and reuses
  the response container ID. At its round limit, `ContinuationLimit.request`
  retains the completed tool results for resumption with the same Forwarder.
  Configure the API client without automatic
  retries for unknown API outcomes. The example has fake-client tests, not
  live Anthropic validation. It is separate from Codex code mode below.

`allowed_callers` is not a security boundary. Code in the container can call
every tool its caller list allows; argument validation, ref resolution,
window binding and preflight before input stay in the core.

## Codex code mode

In Codex code mode the model writes JavaScript for one `exec` tool, and that
code calls nested tools. Registering the MCP server is enough: code calls
`tools.mcp__computer_use__AX(args)` and `tools.mcp__computer_use__GUI(args)`
and gets the MCP result, `{content, isError}`. The first text item is the
JSON evidence line; images are `ImageContent` blocks, and `image(block)` shows
one to the model from the same `exec`, without another model turn.
`instructions/hybrid-code.md` is the guide for this mode.

- Codex renders each MCP input schema as a TypeScript declaration. The
  renderer does not resolve `$ref` in every version and shows a composition
  next to properties as untyped branches, so the published schemas keep both
  out of the fields code writes: the AX schema writes the `query` object out
  at each use, and the core enforces the scroll delta rule.
- Codex can defer MCP tools in code mode. A deferred tool is still callable as
  `tools.mcp__computer_use__AX` and listed in `ALL_TOOLS`, but its declaration
  is left out of the `exec` description. `omit_tools_from = ["deferred"]` in
  the server's `[mcp_servers.computer_use]` entry lists AX and GUI again.
- Use one entry point per task. An in-process MCP server refuses input while
  a CLI or SDK daemon serves the same desktop; `computer-use mcp --attach`
  shares one daemon when a task needs both.
- Without MCP, code runs the CLI or Python through `tools.exec_command`. The
  record holds the image path; `tools.view_image({path})` returns its data
  URL, and `image(url)` shows it from the same `exec`. Printing the image
  as a data URL instead does not survive the nested output limit: a
  full-desktop PNG is about 100,000 characters, and a smaller
  `max_output_tokens` on `exec_command` cuts the middle out of it.

In code mode the tools stay text:

```js
const out = await tools.mcp__computer_use__AX({mode: "find", window_ref: "w1", query: {role: "button"}, limit: 50});
const evidence = JSON.parse(out.content[0].text.split("\n")[0]);   // {"ok":true,...}
const refs = [...out.content[0].text.matchAll(/ref="(r\d+)"/g)].map(m => m[1]);  // find units print ref="rN"
text(JSON.stringify({ok: evidence.ok, refs}));
```

Records are not offered through MCP, so code mode parses the evidence line
and the `ref="rN"` units; a records option for MCP is an open decision. A GUI
zoom's evidence line carries its mapping, so code mode reads it the same way:

```js
const z = await tools.mcp__computer_use__GUI({actions: [{type: "zoom", region: [600, 60, 680, 100]}]});
const {zoom} = JSON.parse(z.content[0].text.split("\n")[0]);   // {region, scale_x, scale_y, ...}
const x = zoom.region[0] + 212 / zoom.scale_x, y = zoom.region[1] + 96 / zoom.scale_y;
```
