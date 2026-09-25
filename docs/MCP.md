# Computer Use MCP

`computer-use` is a stdio MCP server that controls an already-running
desktop. This page covers building it, running it, configuring Codex and
Claude Code, the public contract and troubleshooting. For the internal design
see [Architecture](ARCHITECTURE.md); for the desktop session and the browser
channel see [AX, GUI and CDP](AX_AND_CDP.md).

| | |
| --- | --- |
| Executable | `computer-use` |
| Transport | stdio, newline-delimited JSON-RPC |
| MCP protocol versions | `2025-03-26` and `2025-06-18` |
| Public tools | `AX` and `GUI` |
| Version | the repository `VERSION` file, reported by `--version` and `serverInfo.version` |

One `computer-use mcp` process serves one desktop session. It starts one
persistent platform provider and keeps AX snapshots, element references and the
GUI layout lease in memory, so never share a process between benchmark tasks.

## Source layout

```text
computer-use/
  main.go                  CLI entry point
  mcp_transport.go         JSON-RPC framing and MCP method handling
  service.go               tool dispatch, session state and error context
  tool_contract.go         server instructions and the AX and GUI schemas
  tool_arguments.go        argument parsing shared by the tools
  tool_result.go           result type, evidence text and file output
  app_snapshot.go          snapshot model returned by the provider
  ax_*.go                  AX requests, batches, refs, projections, evidence
  gui_service.go           GUI batches and layout-lease checks
  runtime_backend*.go      platform backend boundary
  provider_*.go            typed private provider protocol
  linux_*.go               Linux backend: provider process and host runtime
  native/linux/provider/   AT-SPI, X11, screenshot and input provider (Python)
  native/macos/            Accessibility and CoreGraphics provider (Swift)
  native/windows/          UIA3 and Win32 provider (C#)
  *_sidecar_transport.go   native worker lifecycle and bounded transport
  runtime.py               provider entry point
```

## Build

```bash
make build        # release binary in artifacts/computer-use, built in Docker
make build-local  # the same with a local Go 1.22+ toolchain
make smoke        # initialize, list tools, require exactly AX and GUI
make test         # gofmt, go vet, Go tests, provider tests, Python tests
```

`make build` runs gofmt and the Go tests inside the builder, injects the
version from `VERSION` and checks that `--version` reports it. A plain
`go build` reports `dev`.

Those release targets build Linux. [Platform support](PLATFORMS.md) covers
the macOS app bundle, Windows self-contained executable, and native tests.
When redistributing a build, include its `LICENSE`, `THIRD_PARTY_NOTICES.md`
and `licenses/` directory alongside the executable. The Linux output directory
can also contain downloaded agent CLIs; those are separate dependencies, not
part of the Computer Use package.

## Desktop runtime

Run the server as the signed-in desktop user. On Linux, bind its environment:

```text
DISPLAY
XAUTHORITY
XDG_RUNTIME_DIR
DBUS_SESSION_BUS_ADDRESS
```

The provider needs Python 3 with PyGObject and the AT-SPI, GTK and GdkPixbuf
typelibs, plus `xdotool`, `wmctrl` and `x11-utils`. On Debian or Ubuntu:

```bash
sudo apt-get install python3 python3-gi gir1.2-atspi-2.0 gir1.2-gtk-3.0 \
  gir1.2-gdkpixbuf-2.0 xdotool wmctrl x11-utils
```

OSWorld guests provide `/home/user/a11y-env.sh`, which the runner sources
before it starts the agent.

The server speaks MCP on stdout. A wrapper must never print to stdout; send
diagnostics to stderr.

## Command line

```text
computer-use mcp               start the stdio MCP server
computer-use call AX|GUI       call one tool, or a JSON array of calls with --calls
computer-use doctor            print platform runtime requirements
computer-use version           print the version
computer-use help [command]
```

`call` uses the same schemas and the same stateful service as an MCP client,
which makes it useful for diagnostics:

```bash
computer-use call AX --args '{"mode":"observe","observation_target":"apps"}'
computer-use call GUI --args '{"actions":[{"type":"screenshot"}]}'
```

## Codex

The runner writes a task-local `config.toml`; its MCP section is:

```toml
[mcp_servers.computer_use]
command = "/path/to/computer-use"
args = ["mcp"]
required = true
startup_timeout_sec = 60
tool_timeout_sec = 180
```

To generate the Codex tool profile and selected skill outside OSWorld:

```bash
python3 -m hcu.render_agent_config --agent codex \
  --model gpt-5.5 --effort xhigh \
  --gateway http://127.0.0.1:8611 \
  --mcp "$PWD/artifacts/computer-use" \
  --codex-home /tmp/codex-home \
  --output /tmp/codex-home/config.toml

CODEX_HOME=/tmp/codex-home codex
```

The standalone configuration embeds the selected `--skill` text in
`developer_instructions`; it does not write or overwrite a workspace
`AGENTS.md`. The OSWorld runner continues to inject its task-local `AGENTS.md`
inside the disposable guest. Set `--codex-home` to the home the client will
actually use so bundled-skill paths match. Local generation does not reproduce
guest isolation or grant permission to automate the desktop.

The profile turns off Codex's built-in `computer_use` feature, so desktop
input comes only from this server, the shell or the browser runtime. See
[Agent profiles](AGENT_PROFILES.md) for every switch.

## Claude Code

Claude Code 2.1.251 drops tools whose input schema is a root-level `oneOf`,
so it reaches the server through `hcu/osworld/guest_files/claude_mcp_schema_adapter.py`.
The adapter flattens only the schemas it shows Claude; the server still
validates the original schemas before any side effect.

```json
{
  "mcpServers": {
    "computer_use": {
      "type": "stdio",
      "command": "/absolute/path/to/python",
      "args": ["/path/to/claude_mcp_schema_adapter.py", "/path/to/computer-use", "mcp"]
    }
  }
}
```

`python3 -m hcu.render_agent_config --agent claude-code ...` writes this file
and the replacement system prompt, selecting the Python interpreter running
the generator. On Windows, run the generator with the local Python installation;
the generated executable and adapter paths are native paths. Keep that Python
installation available when launching Claude. The Linux guest runner retains
its guest-specific Python path. The runner allows the tools `Bash`,
`Edit`, `Read`, `mcp__computer_use__AX` and `mcp__computer_use__GUI`.

`python3 -m hcu.render_agent_config` also renders the Gemini CLI and Grok
Build profiles (`--agent gemini-cli`, `--agent grok-build`), writing the
settings or config file plus the files each harness reads instead: a global
`GEMINI.md` and an experiments file for Gemini CLI, a system prompt for Grok
Build. `--channels a11y,keyboard` or `gui,keyboard` renders the one-channel
ablation for any agent.

## Gemini CLI and Grok Build

Both register the same server directly, without an adapter. Gemini CLI reads
`mcpServers` from its `settings.json` and is served by
`hcu.gateway.gemini_proxy`; Grok Build reads `[mcp_servers.computer_use]` from
its `config.toml` and is served by `hcu.gateway.openai_proxy` on the Responses
API. [Agent profiles](AGENT_PROFILES.md) describes the rest of each profile,
including the tool lists each harness disables.

## Public contract

`AX` takes a `mode`: `observe`, `find`, `act` or `assert_ax`. The server
owns an identity graph:

```text
application aN -> top-level window wN -> element rN
```

References describe current state; they are not durable selectors. Every
mutation resolves its reference against the live accessibility tree again and
rejects stale or cross-window targets before any input.

`GUI` takes an ordered `actions` array: screenshot, pointer, drag, scroll,
keyboard, text and wait actions. A screenshot establishes a private layout
lease for the full desktop; coordinate actions fail before input when the
live layout no longer matches the screenshot they refer to.

AX evidence covers only what the accessibility tree shows, and GUI evidence
only the returned pixels and reported input delivery. Neither proves file
contents, persistence or network state; agents should verify those through
the shell or the application.

### Contract tests

The model sees the tool schemas, descriptions, server instructions and error
texts, so they are treated as an interface. `contract_golden_test.go`
records `initialize`, `tools/list`, the argument-validation errors and the CLI
help in `testdata/contract/public_contract.json`, and `go test` fails when any
of them changes. After an intentional change, regenerate the file and review
the diff:

```bash
cd computer-use && go test -run TestPublicContractMatchesGolden -update-contract
```

## Environment variables

| Variable | Effect |
| --- | --- |
| `COMPUTER_USE_CHANNELS` | comma-separated channels to expose (`a11y`, `gui`, `keyboard`); all by default. Disabled tools disappear from `tools/list`. |
| `COMPUTER_USE_STRUCTURED_CONTENT` | `1` adds `structuredContent` to tool results |
| `COMPUTER_USE_ERROR_CONTEXT` | `off` stops capturing fresh UI state after a failed call |
| `COMPUTER_USE_OUTPUT_MODE` | `file` writes large outputs to files and returns their paths |
| `COMPUTER_USE_OUTPUT_DIR`, `COMPUTER_USE_OUTPUT_THRESHOLD` | directory and minimum size (4000 characters) for file output |
| `COMPUTER_USE_MAX_INLINE_CHARS` | inline text budget per result, 24000 characters by default |
| `COMPUTER_USE_TRACE_FILE` | append one JSONL line per AX action |
| `COMPUTER_USE_AX_TELEMETRY_LOG` | private AX telemetry log, `/tmp/computer-use-ax-telemetry.jsonl` by default |
| `COMPUTER_USE_PYTHON_BINARY` | interpreter for the provider, `python3` by default |
| `COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS` | provider request timeout, 30 seconds by default |

The contract test clears the variables that change model-visible output, so
it always checks the defaults.

## Troubleshooting

AX sees no applications:

```bash
source /home/user/a11y-env.sh
computer-use doctor
python3 -c 'import gi; gi.require_version("Atspi", "2.0"); from gi.repository import Atspi'
printf '%s\n' "$DISPLAY" "$XAUTHORITY" "$XDG_RUNTIME_DIR" "$DBUS_SESSION_BUS_ADDRESS"
```

GUI observation or input fails:

```bash
xdotool getactivewindow
wmctrl -lpG
```

A client cannot initialize the server:

- check that the configured binary exists and runs `computer-use --version`;
- make sure no wrapper writes to stdout;
- run the server as the desktop user, not in a detached root session;
- run `make smoke` outside the desktop to separate protocol problems from
  desktop problems.

When AX reports a stale reference or GUI a stale layout, take a fresh AX
observation or GUI screenshot instead of repeating the rejected call.
