# Code Entry Points: Daemon, CLI, SDK, PTC

One core implements the contract. MCP, the daemon socket, the command line,
the Python SDK and the programmatic tool calling (PTC) adapter are transports
over the same `callTool`. None of them validates arguments, resolves refs or
decides what an error means; they only build arguments and print or return
the core's result.

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

| Role | Files |
| --- | --- |
| Core: contract | `tool_contract.go` (schemas, descriptions), `ax_public_request.go` (decoder, public codes), `schema_validator.go`, `tool_argument_rules.go`, `contract_describe.go` (contract version, error table, `describeContract`) |
| Core: state | `service.go` (`desktopState`, `sessionState`), `ax_ref_registry.go`, `ax_find_cursor.go`, `ax_snapshot_store.go`, `ax_failure_recovery.go` |
| Core: operations | `ax_*.go`, `gui_service.go`, `app_snapshot.go` |
| Core: rendering | `ax_renderer.go`, `ax_evidence.go`, `ax_response_budget.go`, `tool_result.go` (`finalizeForModel`), `records.go` (records) |
| Provider boundary | `platform_contract.go`, `runtime_backend*.go`, `provider_*.go`, `linux_*.go`, `darwin_*.go`, `windows_*.go`, `native/` |
| Adapters | `mcp_transport.go`, `mcp_attach.go` (MCP); `daemon.go`, `daemon_client.go`, `daemon_spawn_*.go`, `peer_cred_*.go` (socket); `cli.go`, `main.go` (command line); `sdk/python/` (SDK, PTC) |

All Go code is still in `package main`; the table is the boundary that adapter
code keeps. Moving the core into its own package is a mechanical follow-up
and was not done to avoid conflicts with the branches that edit it.

## Single source

The core is the only implementation of the contract. There are no generated
schema, type or error files:

- `computer-use schema` and the daemon's `schema` method return the exact
  published tool schemas and the error table (`describeContract`).
- The SDK creates its exception classes from code names; a test checks that
  every code in `schema` has one.
- The PTC adapter builds `hcu_ax` and `hcu_gui` from `schema` at runtime.
- The CLI shorthand table (`cliShorthands` in `cli.go`) is the only
  hand-maintained mapping; `TestCLIShorthandsProduceSchemaValidCalls` checks
  every entry against the decoder and the published schema.

Drift tests: SDK helpers produce calls the core accepts
(`sdk/python/tests/test_sdk.py`), MCP and `call` return byte-identical text
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

`{"mode":"read","refs":["r1",...]}` is a records-only AX mode: one live
resolve per ref, no tree walk. It is not in the MCP schema and MCP rejects it.

Records never reach a model through MCP: the MCP server always returns text,
so its contract golden is unchanged.

## Daemon

`computer-use serve [--socket PATH] [--idle-timeout 30m]` serves one desktop.
The CLI and SDK start it on first use.

| Property | Behaviour |
| --- | --- |
| Socket | `$XDG_RUNTIME_DIR/hcu/desktop-<display>.sock` (or `HCU_SOCKET`); directory 0700, socket 0600; `SO_PEERCRED` uid check on Linux; refuses a socket path owned by another user; replaces a stale socket, never a live one |
| Start | first call forks `computer-use serve`; a `flock` beside the socket makes concurrent first calls start one daemon |
| Methods | `hello` (version, contract version, display check), `call`, `schema`, `status`, `stop`, `mcp` |
| Sessions | each connection has its own session unless `hello` names one (`HCU_SESSION`, `--session`); same name, same session |
| Input lock | one call runs at a time across all sessions, so input never interleaves |
| `request_id` | a call with an id runs once; a repeat (also on a new connection) returns the first result with `replayed: true`; kept 10 minutes |
| Environment | `hello` with another display is refused; a call after the daemon's own session environment changed is refused with `runtime_error` |
| Restart | ref counters are saved beside the socket, so a restarted daemon never issues an old number; old refs fail as expired |
| Idle | exits after the idle timeout with no connection |

## Command line

```text
computer-use apps | observe | wait | find | read | click | type | key | set |
             scroll | activate | close | act | gui     shorthand calls
computer-use batch [FILE|-] [--keep-going]              JSON array of {"tool","args"}
computer-use schema                                     contract as JSON
computer-use daemon start|stop|status
computer-use mcp --attach SOCKET                        MCP through the daemon
computer-use help cli                                   full usage and exit codes
```

Default output is the MCP text, byte for byte; `--json` (for `act` and `gui`,
`--records`) prints the record. Exit status comes from the error table: 0
success, 2 `invalid_argument`, 3 `until_failed`/`until_unknown`, 4
`stale_ref`/`gui_frame_stale`, 5 `window_changed`/`effect_unknown`, 6
`app_unavailable`/`ambiguous_window`/`window_not_found`/`no_focused_window`,
10 `runtime_error`, 64 usage. No exit status mixes codes that sent no input
with codes that may have.

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

`sdk/python/hcu_sdk` (standard library only, Python 3.8+). Import it as
`import hcu_sdk as hcu`; the repository's harness package already owns the
name `hcu`.

- `Desktop.call(tool, args)` is the whole API; helpers (`apps`, `observe`,
  `find`, `find_all`, `read`, `act`, `click`, `type`, `key`, `set_value`,
  `scroll`, `activate`, `close_window`, `screenshot`, `gui`, `wait`) only
  build argument dicts.
- Records are dicts with attribute access. `ok:false` raises `HcuError`; the
  subclass is named after the code (`hcu.StaleRef`).
- `act` and GUI input get a fresh `request_id`; after a dropped connection
  the SDK reconnects and resends with the same id.
- `AsyncDesktop` has the same methods as coroutines, over the socket or any
  async transport (`hcu.ptc_transport(hcu_ax, hcu_gui)` in a PTC container).
- Examples: `sdk/python/examples/` (read a sheet, check a menu item, save as).

## Programmatic tool calling

`hcu_sdk.ptc` serves Anthropic programmatic tool calling:

- `tool_definitions(d.schema())` returns `hcu_ax` and `hcu_gui` client tool
  definitions built from the live schema, with local `$ref` inlined and the
  records options added. `hcu_ax` sets `allowed_callers:
  ["code_execution_20260120"]`; `hcu_gui` stays direct, so screenshots reach
  the model as images, unless `gui_from_code=True`.
- `forward(tool_use, desktop)` is the harness side: it sends one `tool_use`
  block (with or without `caller`) to the guest daemon and returns the
  `tool_result` whose content is the records JSON string.

`allowed_callers` is not a security boundary. Code in the container can call
every tool its caller list allows; argument validation, ref resolution,
window binding and preflight before input stay in the core.

## Codex code mode

No change is needed. Codex renders each MCP input schema to a TypeScript
declaration with a 16,000-byte limit (`code-mode-protocol`,
`DEFAULT_INPUT_SCHEMA_MAX_BYTES`). Rendered with that code, the AX schema is
3,157 bytes and the GUI schema 1,701 bytes. In code mode the tools stay text:

```js
const out = await tools.mcp__computer_use__AX({mode: "find", window_ref: "w1", query: {role: "button"}, limit: 50});
const evidence = JSON.parse(out.content[0].text.split("\n")[0]);   // {"ok":true,...}
const refs = [...out.content[0].text.matchAll(/ref="(r\d+)"/g)].map(m => m[1]);  // find units print ref="rN"
text(JSON.stringify({ok: evidence.ok, refs}));
```

Records are not offered through MCP, so code mode parses the evidence line
and the `ref="rN"` units; a records option for MCP is an open decision.
