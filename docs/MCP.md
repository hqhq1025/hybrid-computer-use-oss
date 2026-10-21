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

### Linux screenshot pipeline

The provider captures raw pixels and samples the desktop layout before encoding
an image. A screenshot-only GUI call uses one captured frame. A mutation batch
retains its pre-action frame for failure evidence and encodes its final frame
for delivery. Layout-race retries discard raw captures without compressing them.
The provider caches encoded images only within the current call, never across calls.

PNG uses lossless compression level 3. If its base64 size would exceed the image
budget, the provider tries JPEG qualities 85, 70, 55, and 40. Only the selected
bytes are base64-encoded. If no encoding fits, the existing smallest-image
fallback remains. Returned MIME and dimensions describe the actual image.
Lower PNG compression can switch near-budget images to JPEG sooner.

Coordinate actions still validate the captured layout and recheck the live
layout before each coordinate input. On Linux a layout change rejects a
coordinate only when the window under that point changed: another window is
now topmost there, or that window moved, resized or closed. Capture failure before dispatch prevents
input. After an action failure, the provider attempts a fresh capture. If final capture or
encoding fails, the response labels any pre-action fallback explicitly.
If neither image is available, the response reports unknown visual state.

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
```

`make build` runs gofmt and the Go tests inside the builder, injects the
version from `VERSION` and checks that `--version` reports it. A plain
`go build` reports `dev`.

Those release targets build Linux. [Platform support](PLATFORMS.md) covers
the macOS app bundle, Windows self-contained executable, and permissions.
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

`call` runs the same core as an MCP client in its own process and prints the
same model-facing result, with images written to files; it is useful for
diagnostics. Refs do not survive between `call` processes; the daemon
commands in [Code entry points](CODE.md) keep them.

```bash
computer-use call AX --args '{"mode":"apps"}'
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

To generate the Codex tool profile and selected interaction guide outside OSWorld:

```bash
python3 -m hcu.render_agent_config --agent codex \
  --model gpt-5.5 --effort xhigh \
  --gateway http://127.0.0.1:8611 \
  --mcp "$PWD/artifacts/computer-use" \
  --instructions-file "$PWD/instructions/hybrid.md" \
  --base-instructions "$PWD/instructions/base.md" \
  --codex-home /tmp/codex-home \
  --output /tmp/codex-home/config.toml

CODEX_HOME=/tmp/codex-home codex
```

The standalone configuration embeds the selected `--instructions-file` text in
`developer_instructions`; it does not write or overwrite a workspace
`AGENTS.md`. The OSWorld runner continues to inject its task-local `AGENTS.md`
inside the disposable guest. Set `--codex-home` to the home the client will
actually use so bundled-skill paths match. Local generation does not reproduce
guest isolation or grant permission to automate the desktop.

Standalone Codex configurations use `approval_policy = "on-request"` and
`sandbox_mode = "workspace-write"`. Shell commands run within the workspace
sandbox; the agent can request approval for elevated access. This is not an
approval prompt for every action. Review the working directory before starting
Codex, since workspace files can be changed without an escalation.
Use the interactive `codex` command for approval prompts. Non-interactive
`codex exec` can disable approval prompts even when this config requests them;
it retains the selected shell sandbox but cannot ask for elevated access.

The launch command above adds no permission overrides. Codex CLI options such
as `--dangerously-bypass-approvals-and-sandbox`, `--sandbox danger-full-access`
or `-c approval_policy=never` can override these defaults. Do not add them to
standalone launch scripts. `--ignore-user-config` skips the generated config;
`--profile` can layer different permissions over it.

Use interactive `codex` when you need approval prompts. The pinned CLI's
non-interactive `codex exec` selects `never` approvals while retaining
`workspace-write` sandboxing; actions requiring elevation cannot prompt for
approval. This does not grant unrestricted shell access.

The shell sandbox does not isolate the MCP server or the desktop it controls.
Use a dedicated desktop or disposable VM for untrusted tasks, keep sensitive
applications and credentials out of that session, and review any client
permission overrides before use. A separate `CODEX_HOME` isolates configuration,
not the operating system.

Python callers of `hcu.osworld.agents.codex_config()` get the same standalone
defaults. Only an externally isolated, disposable evaluation guest should use
`execution_context="isolated-guest"`; that explicitly selects
`approval_policy = "never"` and `sandbox_mode = "danger-full-access"` for
unattended execution. Guest paths, task IDs and channel selections do not
select this mode. The standalone generator always selects `standalone`.

The guide is always injected plain text, not a discoverable or activated
skill. `--instructions-file ""` omits the guide but retains base policy;
`--base-instructions` selects that policy. Product instruction files contain no runner protocol
markers. See [Instructions](INSTRUCTIONS.md) for the shared policy and profiles.

The profile turns off Codex's built-in `computer_use` feature, so desktop
input comes only from this server, the shell or the browser runtime. See
[Agent profiles](AGENT_PROFILES.md) for every switch.

## Claude Code

Claude Code connects through `hcu/osworld/guest_files/claude_mcp_schema_adapter.py`.
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

`python3 -m hcu.render_agent_config --agent claude-code ...` writes this file,
the replacement system prompt and a `.launch.py` startup script. It selects the Python interpreter running
the generator. On Windows, run the generator with the local Python installation;
the generated executable and adapter paths are native paths. Keep that Python
installation available when launching Claude. The Linux guest runner retains
its guest-specific Python path. The runner allows the tools `Bash`,
`Edit`, `Read`, `mcp__computer_use__AX` and `mcp__computer_use__GUI`.

For a standalone session:

```bash
python3 -m hcu.render_agent_config --agent claude-code \
  --model claude-opus-5 --effort xhigh \
  --gateway http://127.0.0.1:8537 \
  --task-id manual \
  --mcp "$PWD/artifacts/computer-use" \
  --output /tmp/hcu-claude/mcp.json

python3 /tmp/hcu-claude/mcp.launch.py
```

The script requires `claude` on `PATH` and forwards additional CLI arguments.
It supplies the selected model and effort, the task-scoped gateway URL,
placeholder authentication, MCP configuration and replacement prompt. Loading
the MCP JSON alone does not apply these startup settings. Local sessions use
manual tool approvals, not the disposable guest's permission bypass; generation
does not reproduce guest isolation.

`python3 -m hcu.render_agent_config` also renders the Gemini CLI and Grok
Build profiles (`--agent gemini-cli`, `--agent grok-build`), writing the
settings or config file plus the files each harness reads instead: a global
`GEMINI.md` and an experiments file for Gemini CLI, a system prompt for Grok
Build. `--channels a11y,keyboard` or `gui,keyboard` renders the one-channel
ablation for any agent.

## Gemini CLI and Grok Build

Both register the same server directly, without an adapter. Gemini CLI reads
the `computer-use` entry under `mcpServers` in its `settings.json` and is served by
`hcu.gateway.gemini_proxy`; Grok Build reads `[mcp_servers.computer_use]` from
its `config.toml` and is served by `hcu.gateway.openai_proxy` on the Responses
API. [Agent profiles](AGENT_PROFILES.md) describes the rest of each profile,
including the tool lists each harness disables.

For Gemini, `--output` must be `<home>/.gemini/settings.json`, for example
`/tmp/hcu-gemini/.gemini/settings.json`. Other layouts are rejected before
writing configuration. The generator prints the required environment, including
`GEMINI_CLI_HOME=<home>` and absolute paths for the base prompt and experiments
file. Apply those environment variables when starting `gemini`; its global
`GEMINI.md` is written beside `settings.json`. An arbitrary JSON filename is not
a discoverable Gemini configuration.

The Gemini server key avoids underscores because its policy parser uses an
underscore to separate server and tool names. Other client keys are unchanged.
See [Client compatibility](CLIENT_COMPATIBILITY.md) for schema conversion,
error images, and session checks when adding another client.

## Public contract

`AX` takes a `mode`: `apps`, `observe`, `find` or `act`. The server owns an
identity graph:

```text
application aN -> top-level window wN -> element rN
```

- `apps` lists confirmed top-level windows as `wN` with title, application and
  whether it has focus. Output names applications but shows no `aN`; `app`
  still accepts an `aN`, a name, an app id or a PID.
  If an application does not answer, the listing retains confirmed windows
  and reports `inventory_complete=false` with `inventory_issues` in text and
  records output. Each issue names the application when known and gives the
  read failure. Missing windows remain unknown. Returned refs remain usable;
  title-based selection, implicit focus, and window waits require a complete
  listing. A missing window cannot prove that a close succeeded.
- `observe` returns a window tree or a diff with `rN` refs; `find` returns
  matching `rN`, and a truncated result prints the call for the next page.
- An explicit `act observe:"tree"` returns the complete final window tree;
  `full` also returns its screenshot. These requests have the same meaning
  after a mutation, an ordered input sequence, or a final wait. Evidence is
  captured after the wait finishes. The default remains compact `diff`.
- The decoder accepts `type.value` as `type.text` and non-empty
  `set_value.text` as `set_value.value`. Supplying both fields is rejected
  before input. The published schema continues to advertise the canonical
  field names. Ref, window, and selection validation remain unchanged.
- `act` accepts up to 64 ordered steps. The sum of all wait timeouts must
  not exceed 60 seconds; an omitted timeout counts as 5 seconds. This bounds
  waiting only, not total call duration. Argument errors reject the whole
  batch before input. `act` runs ordered steps: actions
  `{type, ref|title, text|keys|value|direction|pages|to_ref}` and `wait` steps. The
  `rN` or `wN` refs name the window, so an `act` call with refs needs no
  `window_ref`; `window_ref` only sends ref-less `keypress` and `type` to a
  window other than the focused one. `activate` and `close` accept a window
  title substring, resolved to one `wN` before any input. `keypress` and
  `type` without `ref` go to the window that had focus when the call started.
  Every step uses the same rule: the first step that fails stops the batch,
  and the result says how many ran.
- A `wait` step sends no input. Its forms are `window` (a top-level window
  whose title contains the text, read from the whole desktop, so another
  process or a relaunched application counts), `query` (an element in any
  visible window of the batch's application, including dialogs and popups,
  matched as `find` matches), `ref` with `value`, `state` or `text` (one
  element), and `text` alone (text anywhere in the batch window). `gone:true`
  waits for absence; an exited application has no window. `timeout_ms`
  defaults to 5000. On timeout the step fails with `wait_timeout`, the
  message says what was seen or why the state could not be read, and the
  current state follows; a read that is incomplete keeps polling until the
  timeout. After a `window` wait, keys and text without `ref` go to that
  window. An `act` with only `wait` steps waits without input.
- A `select` step selects without typing or clicking. `{"ref":"rA"}`
  selects the whole element, or the item in a list. `"text"` selects that
  exact substring of `rA`; it must occur once. `"to_ref":"rB"` selects from
  the start of `rA` through the end of `rB`, for two cells of one table or
  for two sibling text elements such as Writer paragraphs. The provider reads
  the selection back and reports it as a note, for example
  `[select] selected 4 cells A2:B3`; it fails with `effect_unknown` when it
  cannot read back what it asked for. On Linux, text across paragraphs is
  selected with the caret at the start of `rA` and `ctrl+shift+Down`, because
  AT-SPI selects inside one object only. A shifted or oversized selection,
  or a cell set that differs from the requested range, fails with
  `effect_unknown`; the result reports possible partial input.

Observe and find with `scope:"content"` use the whole window when a dialog
or a window has no unique content area. The result states why the scope
expanded. Find and query waits accept the roles and states shown in the tree.
A zero-result find lists at most five nearby candidates; these suggestions
do not change the query or perform input. Wait timeouts put current window
refs and nearby elements before diagnostic detail. A failed latest window
read remains an incomplete observation and never proves that a window closed.

Find matches include a `text` excerpt when their role's value is absent from
the rendered fields, as with paragraphs and cells. The excerpt contains at
most 80 Unicode code points, plus an ellipsis when clipped. Clipped excerpts
also carry `text_truncated=true`. Text already displayed in a name, property,
or matched-text field is not repeated. Excerpts are display-only and do not
change canonical text, selectors, refs, or the response budget.

Removed diff lines omit refs and addressability markers because those lines
describe prior state. Their absent refs appear separately in `absent_refs`.
Three or more consecutive absent refs use an inclusive range: `r40..r43`
means `r40`, `r41`, `r42`, and `r43`. A gap or a ref still present in the
current snapshot breaks the range. Absence from this snapshot does not by
itself prove that the object was destroyed.

An unchanged-tree response requires the delivered refs to match as well as
the provider state. Reissued refs cause a fresh tree delivery. An explicit
`observe:"full"` continues to return the tree on the first request.
- The removed `until` field, and its older spellings `postcondition` and
  `assertion`, are rejected before any input. The hint is the same call
  rewritten with `wait` steps.

Every `act` result that sent input starts, after the evidence line, with a
report built from the reads the call makes anyway:

```text
effect: r58 checkbox "Interface" checked=false→true
windows: opened w5 "Insert Sheet" (dialog, focused); closed w3 "Print"; focus w2
```

There is one `effect` line per action with an element `ref`. It compares the
element in the read before that action with the next read after it and names
the role, the name, and the state or value fields that changed. When both
reads are of the same window and nothing else in it changed, the line ends
with `(only state flags changed in this window)`. When the element itself did
not change but other nodes did, the line counts them, for example
`itself unchanged; 25 nodes appeared under it, 1 node changed elsewhere`.
Nodes that appear or disappear under the element count as its effect; changes
elsewhere do not. The line says `no visible change` when nothing in the window
changed, `gone` when a complete read no longer has the element, and
`unknown (reason)` when no read after the action covers it. The
`windows` line compares the application's top-level windows before the first
action with those after the last; new windows get a `wN` at once. When the
report shows every targeted element changed, or a `wait` after the last action
held, the evidence line does not also say `effect_unknown`.

References describe current state; they are not durable selectors. Every
mutation resolves its reference against the live accessibility tree again and
rejects stale or cross-window targets before any input.

On Linux, a visible composite drop-down can represent both a list toggle and
an editable field. A click addresses the toggle; text editing addresses a
unique editable partner in that same control and verifies its value there.
The editing capability and partner identity must still be valid before input.
A list-only control does not become editable merely because its displayed
role is `combobox`. Missing, ambiguous, or replaced editing targets do not
receive synthesized keyboard input through the toggle.

Every AX result starts with one JSON line: `ok`, and on failure `code`,
`path`, `message` and `hint` (a corrected call), then `input_emitted`,
`completed` and `requested`. The codes are `invalid_argument`, `stale_ref`,
`window_changed`, `wait_timeout`, `effect_unknown`, `app_unavailable`,
`runtime_error`, `ambiguous_window`, `window_not_found` and
`no_focused_window`.

Recovery follows the concrete failure, `input_emitted`, `completed`, and
`retry_same_request`. A coordinate ownership failure requests a new GUI
screenshot; refreshing an AX ref does not establish that the point belongs
to its window. A window-change hint uses an available current window ref or
requests a GUI screenshot for an anonymous popup. It does not invent a
window title or instruct the caller to repeat already completed steps.

`GUI` takes an ordered `actions` array: screenshot, pointer, drag, scroll,
keyboard, text and wait actions. A screenshot establishes a private layout
lease for the full desktop; coordinate actions fail before input when the
window under one of their points no longer matches the screenshot they refer
to.

Every GUI result starts with the same JSON line as AX, followed by `screen`
(the returned image's size and coordinate space) and `actions` (pixel-change
and settle facts); the screenshot follows as an image. GUI errors use
`invalid_argument`, `window_changed`, `effect_unknown` and `runtime_error`
like AX. `gui_frame_stale` means the window under a screenshot coordinate
changed, with nothing sent; its hint requests a new screenshot.
`permission_denied` means the operating system denied access. Its hint gives
permission-recovery instructions instead of another call. Restore the current
provider's permissions and restart it before observing again. When capture
fails after input, the result retains its partial or uncertain input evidence;
restoring permission does not authorize replaying the actions.

AX evidence covers only what the accessibility tree shows, and GUI evidence
only the returned pixels and reported input delivery. Neither proves file
contents, persistence or network state; agents should verify those through
the shell or the application.

## Environment variables

| Variable | Effect |
| --- | --- |
| `COMPUTER_USE_CHANNELS` | comma-separated channels to expose (`a11y`, `gui`, `keyboard`); all by default. Disabled tools disappear from `tools/list`. |
| `COMPUTER_USE_STRUCTURED_CONTENT` | `1` adds `structuredContent` to tool results. Default off: evidence stays in text because some clients replace sibling text with the structured object. Enable only with a tested consumer. |
| `COMPUTER_USE_ERROR_CONTEXT` | `off` stops capturing fresh UI state after a failed call |
| `COMPUTER_USE_OUTPUT_MODE` | `file` writes large outputs to files and returns their paths |
| `COMPUTER_USE_OUTPUT_DIR`, `COMPUTER_USE_OUTPUT_THRESHOLD` | directory and minimum size (4000 characters) for file output |
| `COMPUTER_USE_MAX_INLINE_CHARS` | inline text budget per result, 24000 characters by default |
| `COMPUTER_USE_TRACE_FILE` | append one JSONL line per AX action |
| `COMPUTER_USE_PYTHON_BINARY` | interpreter for the provider, `python3` by default |
| `COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS` | provider request timeout, 30 seconds by default |

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

When AX reports `stale_ref` or GUI `gui_frame_stale`, take a fresh AX
observation or GUI screenshot instead of repeating the rejected call.
