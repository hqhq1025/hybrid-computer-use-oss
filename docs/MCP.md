# Computer Use MCP

This document is the operational entry point for the released Computer Use
`v0.14` MCP. For internal design details, see
[Architecture](ARCHITECTURE.md). For desktop-session and browser-channel
details, see [AX, GUI, And CDP](AX_AND_CDP.md).

## Released Identity

- Product release: `v0.14`
- Executable: `computer-use`
- MCP transport: stdio
- Public tools: `AX` and `GUI`
- Component version:
  `0.70.0-beta.70+osw-v013-gui-native-1080p`
- Promoted Linux x86-64 binary SHA-256:
  `11adcefd856200eda37ec127b586d95105f1d9b52c64e6ee8ff5571c1f5bacf0`

The exact release binding is in `releases/v0.14/release.json`. The component
version contains the historical candidate identifier and is intentionally not
rewritten after promotion.

## Source Layout

```text
computer-use/
  main.go                     MCP service and session state
  mcp_transport.go            stdio JSON-RPC transport and local call CLI
  tool_contract.go            public AX and GUI JSON Schemas
  ax_*.go                     AX snapshots, refs, actions and evidence
  gui_service.go              GUI batching and stale-layout checks
  runtime_backend*.go         private runtime boundary
  provider_wire_v2.go         typed Go/Python provider protocol
  native/linux/provider_v1/   persistent AT-SPI/X11 Python provider
```

One `computer-use mcp` process owns one desktop session. It starts one
persistent Python provider and keeps AX snapshots, semantic refs and the GUI
layout lease in process memory. Do not share one MCP process across benchmark
tasks.

## Build And Verify

Build the frozen release artifact:

```bash
make build
sha256sum artifacts/releases/v0.14/computer-use
make verify
make smoke
```

`make verify` checks the release-bound source, prompt and skill hashes.
`make smoke` starts the built stdio server, initializes MCP, lists tools and
requires the public surface to be exactly `AX` and `GUI`.

For an unbound local development build:

```bash
make build-local
```

Do not use a local development build in a formal run unless it has a recorded
tool change ID, exact hashes and a new run ID.

## Desktop Runtime

Run the MCP as the signed-in desktop user with the desktop session environment:

```bash
source /home/user/a11y-env.sh
export DISPLAY="${DISPLAY:-:0}"
computer-use doctor
computer-use mcp
```

The important environment values are:

```text
DISPLAY
XAUTHORITY
XDG_RUNTIME_DIR
DBUS_SESSION_BUS_ADDRESS
```

The guest also needs AT-SPI, GTK/GdkPixbuf, X11 input and window-management
packages. The V1 and V2 derived qcow2 profiles install these dependencies.

The server communicates over stdin/stdout. Never write diagnostics to stdout
from a wrapper because that corrupts the MCP stream; send logs to stderr.

## Local Calls

The executable includes a local call client for diagnostics:

```bash
computer-use call AX \
  --args '{"mode":"observe","observation_target":"apps"}'

computer-use call GUI \
  --args '{"actions":[{"type":"screenshot"}]}'
```

Multiple ordered diagnostic calls can be supplied with `--calls`. This CLI
uses the same public schemas and stateful service as MCP clients.

## Codex Configuration

The generated task-local Codex configuration contains:

```toml
[mcp_servers.computer_use]
command = "/opt/osw-runtime/computer-use"
args = ["mcp"]
required = true
startup_timeout_sec = 60
tool_timeout_sec = 180
```

Generate the complete isolated profile with:

```bash
python3 scripts/render-agent-config.py \
  --agent codex \
  --model gpt-5.5 \
  --effort xhigh \
  --task-id manual \
  --gateway http://172.17.0.1:18080 \
  --mcp /opt/osw-runtime/computer-use \
  --output /tmp/osw-codex/config.toml
```

The benchmark profile disables Codex's built-in `computer_use` feature so the
only computer-control MCP is this released server. Browser/CDP and shell remain
separate hybrid channels.

## Claude Code Configuration

Claude Code `2.1.251` requires the included schema adapter:

```json
{
  "mcpServers": {
    "computer_use": {
      "type": "stdio",
      "command": "/usr/bin/python3",
      "args": [
        "/opt/osw-runtime/claude_mcp_schema_adapter.py",
        "/opt/osw-runtime/computer-use",
        "mcp"
      ]
    }
  }
}
```

Generate the complete MCP file and replacement system prompt with:

```bash
python3 scripts/render-agent-config.py \
  --agent claude-code \
  --model claude-opus-5 \
  --task-id manual \
  --gateway http://172.17.0.1:18081 \
  --mcp /opt/osw-runtime/computer-use \
  --output /tmp/osw-claude/mcp.json
```

The allowed Claude tools are `Bash`, `Edit`, `Read`,
`mcp__computer_use__AX` and `mcp__computer_use__GUI`.

## Public Contract

`AX` is a mode-discriminated tool with `observe`, `find`, `act` and
`assert_ax`. Its server-managed identity graph is:

```text
application aN -> top-level window wN -> element rN
```

Refs are current-state capabilities, not durable selectors. Mutation actions
re-resolve refs against the live canonical tree and reject stale or
cross-window targets before input.

`GUI` accepts an ordered `actions` array. It supports screenshot, pointer,
drag, scroll, keyboard, text and fixed one-second wait actions. A screenshot
establishes a private full-desktop layout lease. Coordinate actions fail closed
before stale input if the live layout no longer matches the screenshot.

AX evidence proves only accessibility-visible state. GUI evidence proves only
the returned desktop pixels and reported input delivery. Use shell, file APIs
or application-native inspection when durable contents or persistence must be
verified.

## OSWorld Lifecycle

For every task, the unified runner:

1. creates a fresh official Docker/QEMU environment and qcow2 overlay;
2. copies the frozen MCP binary and selected agent into the guest;
3. removes inherited Codex and Claude state;
4. writes a task-local MCP/client configuration;
5. sources the active desktop accessibility environment;
6. starts one agent session, which owns one MCP child;
7. preserves tool calls, screenshots, results and cleanup evidence;
8. terminates the task environment without reusing MCP state.

V1 and V2 use the same MCP binary and lifecycle. Their task data, evaluator,
guest password, derived qcow2 profile and volume size differ.

## Troubleshooting

If the server starts but AX cannot see applications:

```bash
source /home/user/a11y-env.sh
computer-use doctor
python3 -c 'import gi; gi.require_version("Atspi", "2.0"); from gi.repository import Atspi'
printf '%s\n' "$DISPLAY" "$XAUTHORITY" "$XDG_RUNTIME_DIR" \
  "$DBUS_SESSION_BUS_ADDRESS"
```

If GUI observation or input fails:

```bash
xdotool getactivewindow
wmctrl -lpG
```

If an MCP client cannot initialize:

- confirm the configured binary exists and is executable;
- run `computer-use --version`;
- ensure wrappers keep stdout reserved for MCP JSON-RPC;
- verify the process runs as the desktop user, not in a detached root session;
- run `make smoke` outside the guest to separate protocol failure from desktop
  provider failure.

If AX reports a stale ref or GUI reports a stale layout, obtain a fresh AX
observation or GUI screenshot. Do not retry the rejected mutation unchanged.

## Change Control

Changes to schemas, descriptions, provider behavior, screenshots, input
delivery, runtime dependencies or model-visible recovery text are product
changes. Record them under `analysis/tool_changes/`, build a new immutable
artifact identity and use a new run ID. Do not overwrite the `v0.14` release
manifest or silently mix MCP builds in one evaluation round.
