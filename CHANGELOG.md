# Changelog

## Unreleased

- AX `act` takes a wait step `{"type":"wait","settle":true}` that needs no
  predicted state: after an input step it waits until the UI changes (windows,
  focus, or the window's tree) and stops changing, names what
  changed, and fails with "no change" only when nothing changed. The SDK and
  CLI `--wait` accept it.
- The OSWorld runner and `hcu.render_agent_config` take `--code-mode`
  (queue config `"code_mode": true`) for Codex. It keeps the computer-use MCP
  server registered for code to call, turns on Codex code mode with
  `omit_tools_from = ["deferred"]` on the server, and appends
  `instructions/hybrid-code.md` to the profile.
  `configs/osworld-v2-codex-code-mode-queue.example.json` shows it.
- `instructions/hybrid-code.md` covers code that calls the MCP tools inside
  a code tool, the CLI and the SDK. It drops the help and schema reads at the
  start, shows one batch with its check, and opens an image in the same code
  that took it.
- The Python SDK adds `click_zoom(zoom_record, u, v)`, which clicks the
  frame pixel shown at a point of a zoom image.
- The CLI `act --json` and `gui --json` read their body from standard input
  with `-` and from a file with `@FILE`, in addition to an inline value.
- Linux: AX output shows what is on screen. Options of a closed combo box
  are no longer listed (Qt kept them visible, also for combos on hidden
  pages); a click that opens a menu or popup returns its items under
  `popup:`; spreadsheet tables state how many empty cells were omitted;
  hidden toolkit windows leave the window listings; windows of applications
  without an accessibility tree are listed with a wN for activate, close and
  window waits; a cell that cannot confirm keyboard focus names the routes
  that work.
- macOS: when the screenshot after GUI input catches a desktop layout
  change, the provider discards that sample and captures again within the
  same `settle_ms` budget. Input is not replayed and the layout checks are
  unchanged; if the budget runs out, the call fails as before.
- Linux: GTK, LibreOffice and Qt applications are easier to address. Editable
  text fields and document editors appear as `textbox` with their value and a
  ref; unnamed list rows take their visible label as name; Qt top-level
  widgets and unnamed frames get a window ref and their window-manager
  title; keys such as `COMMA` and `PLUS` resolve; non-ASCII text typed by
  keyboard synthesis arrives intact (at most 500 non-ASCII characters per
  call); an element that refuses keyboard focus reports why.
- Bring the CLI, Python SDK, code mode and PTC adapter up to the MCP
  contract. The CLI adds `gui zoom X0 Y0 X1 Y1`, `gui double_click|move X Y`
  and `select rN [TEXT] [--to rN]`. The SDK adds `zoom(region)`,
  `hcu.zoom_point(record, u, v)` and `select()`. Zoom counts as an
  observation everywhere: it gets no `request_id`, retries like a read, and
  is allowed by an in-process MCP server while a daemon runs. Records of AX
  `observe:"full"`/`"screenshot"` now carry `screen`, the GUI frame, like a
  GUI screenshot. `CodeDesktop.emit_image` delivers a zoom image labelled
  `zoom_pixels` with its mapping. MCP output is unchanged. Two tests read the
  AX modes, observe values, act steps, wait kinds and GUI actions from the
  published schemas. They fail when the CLI or the SDK neither supports a new
  item nor lists it as reachable only through generic JSON calls.
- GUI `zoom` returns an enlarged region of the current full-desktop frame
  for small text and dense icons, with the same contract on Linux, macOS and
  Windows. A zoom is look-only: it is bound to the frame it enlarges, fails
  as stale when a window over the region changed, and never becomes the
  frame that later coordinates refer to.
- AX `observe` and `act` with `full` or `screenshot` return the same
  full-desktop image as GUI, and that image becomes the current GUI frame,
  so a GUI coordinate action can follow an AX read without another
  screenshot.
- The gateways finish accounting for a response before they admit the
  agent's next request of the same task, so a fast follow-up request is no
  longer rejected as in flight.
- The Python SDK serializes concurrent calls on one socket, closes the
  socket after a rejected hello, and binds input retries to the daemon
  instance that received the first attempt: after a daemon restart, an
  uncertain input is reported as unknown instead of being sent again.
  Attached MCP calls keep element refs unique across daemon restarts.
- The PTC adapter keeps each tool event's outcome for the conversation and
  returns it for a repeated event instead of sending the input again. Direct
  screenshot results include images when the operator supplies an image
  reader.
- The Python SDK accepts named `window_ref` and `app` targets and the
  published find field names, and handles cancellation without sending input
  after the cancelled call. `CodeDesktop` publishes errors to the host before
  raising them. `sdk/python/examples/open_gcua_worker.py` shows the OpenGCUA
  integration.
- The CLI rejects unknown options and trailing JSON content before it
  contacts the daemon.
- The GUI schema declares `actions` and `settle_ms` at its root, so clients
  that close the root object (OpenCode) accept legal calls. Gemini CLI uses
  the MCP server key `computer-use`; trace summaries read both the old and
  the new key.
- macOS permission checks use only the running process's grants. A Screen
  Recording denial is reported as `permission_denied` with recovery advice.
- macOS AX writes and window close that fail with an uncertain native error
  report `effect_unknown`. Explicitly selected background windows are
  observed without activating them, internal reads keep the window the
  request selected, and a verified native close is reported as such.
- The hybrid guide uses `open -a APP` to launch applications on macOS.
- Cross-window AX batches fail as `invalid_argument` with a hint to split
  them by window. An ordered-input request that never reached the provider
  reports that no input was sent.
- macOS application discovery reads the workspace on the main thread, so
  a long-running provider sees newly launched applications, and it drops
  processes that have exited. When one application's windows cannot be
  read, `AX apps` lists the others and marks the inventory incomplete; title
  resolution and window waits still require a complete inventory. After a
  close whose final snapshot fails, the provider rechecks the window briefly
  before it reports the close as unknown.
- Drop history-only version markers from names: `AXSnapshotV1` is
  `AXSnapshot`, `ProviderV2*` is `Provider*`, `provider_wire_v2.go` is
  `provider_wire.go`, `wire_v2.py` is `wire.py`, the Windows test project is
  `ComputerUse.Windows.Provider.Tests` and the shared fixtures are in
  `testdata/provider-wire/`. The private Go-to-provider wire value is
  `computer-use-provider` in all four implementations; `schema_version` and
  `protocol_version` stay 2. MCP output is byte-identical.
  `scripts/maint/drop_version_names.sh` reproduces the rename. Add
  `scripts/maint/check_layout.sh` to CI and a file layout section to
  `AGENTS.md`.
- Rewrite the README as a short overview with a feature table, a platform
  matrix and client, CLI and SDK quick starts. Update the docs for the
  package split, the 1366x768 screenshot size, the current error table, CLI
  commands and environment variables, and replace duplicated setup text with
  links.
- Split the `computer-use` Go module from one `package main` into layered
  packages: `internal/contract`, `internal/core`, `internal/provider`,
  `internal/transport` and `cmd/computer-use`; the embedded Linux provider
  stays at the module root. Build scripts build `./cmd/computer-use` and keep
  `-X main.version`. MCP `initialize`, `tools/list`, `help` and `schema`
  output is byte-identical; `scripts/maint/split_packages.sh` reproduces
  the split.
- Route edits of composite Linux drop-downs to their verified editable
  partner, while keeping clicks on the list toggle. Reject missing,
  ambiguous, or replaced editing targets before input.
- Send the UTF-8 byte length to AT-SPI text insertion, preserving Chinese,
  emoji and combining characters while retaining character-based offsets.
- Honor explicit AX tree/full evidence after final waits and ordered input
  sequences; full includes the final screenshot. Keep compact diff as the
  default and preserve failed-batch prefixes.
- Return executable recovery hints for coordinate ownership failures,
  anonymous popups, and returns to an existing window. Align the operating
  guide with input and completed-prefix evidence instead of blind retries.
- Probe enabled AX/GUI calls through the selected client transport before
  launching a task, and record skipped window reads when no window exists.
  The evaluation canary also validates AX on a known desktop window.
- Correct the provider's internal screenshot-only description: it captures
  one frame without comparing before/after pixels.
- Accept up to 64 AX actions and reject calls whose wait timeouts total more
  than 60 seconds. Accept unambiguous `type.value` and `set_value.text`
  aliases; conflicting fields still reject the whole batch before input.
- Read only the window root between eligible untargeted AX inputs, while
  retaining full reads before ref actions, waits, and the final input.
- Encourage batching known AX, GUI, and shell actions until a decision needs
  fresh evidence. Preserve focus, ref, layout, failure, and final-evidence
  boundaries. The combined guidance requires a fresh model evaluation.
- Include a bounded text excerpt in AX find matches when the rendered fields
  omit the value. Removed diff nodes omit addressability markers, and
  consecutive absent refs use inclusive ranges. Explicit full reads still
  return the complete working tree immediately.
- Deliver new AX refs even when the provider tree is unchanged. Match find
  roles and states to the rendered tree, explain content-scope expansion,
  and return bounded current candidates on failed finds and waits.
- Expose visible drop-down controls through AX and add `select` for text,
  items, and cell ranges. Verify the exact selection and report uncertain
  partial effects when readback disagrees with the requested range.
- Fit GUI screenshots into 1366 x 768 and map input coordinates back to the
  native desktop using the exact delivered width and height. This aligns
  screenshot pixels with the image GPT-5.4 sees at high detail.
- Update base and hybrid instructions to stop after sufficient evidence,
  bound application discovery, and save/close/reopen documents when a needed
  property requires direct file editing. These instruction changes need a
  new model evaluation.
- On Linux, an AX click on a Qt control whose only accessibility action is
  `Toggle` (checkable buttons, radio buttons, item-view cells) uses a real
  pointer click when the window is in front, the geometry is stable, and a
  hit test reaches the element. Qt's `Toggle` changes the checked state
  without the `clicked` signal, so VLC's preferences categories, its
  Simple/All switch and its preferences tree turned checked while the page
  stayed the same.
- On Linux, items of an open GTK2 (GAIL) menu count as showing. GAIL never
  reports SHOWING for them, so after an AX click opened GIMP's Edit menu,
  `Preferences` was `addressable=false reason=not_currently_showing`. The
  provider reads the menu shell's selection to tell an open menu from a
  closed one.
- The act `effect` line says `(only state flags changed in this window)` when
  nothing else changed, and counts the nodes that changed when the element
  itself did not, instead of `no visible change`.
- AX act results start with an effect line and a windows line, and return a
  diff when it is shorter than the tree. On OSWorld tasks this reduced the
  median model input per solved task at the same step count; scores stayed
  within one task.
- `until` is replaced by ordered `wait` steps inside an act batch. The
  computer-use daemon serves the same core to the CLI and to the `hcu_sdk`
  Python module, which includes a PTC adapter.
- GUI results start with the same JSON evidence line and public error codes
  as AX. On Linux, a GUI click is rejected as stale only when the window
  under its point changed after the screenshot.
- On Linux, an unchanged window's AX tree is reused across calls by evidence
  (no UI event, same fingerprint, visible content re-read) instead of a
  10-second age limit, within a 64 MB cache, so repeated reads of an
  unchanged window are faster.
- Internal identifiers use `ax` instead of `a11y`; wire names and MCP output
  are unchanged. Long functions are split into named, tested steps.
- Model guidance says to start GUI applications from the shell with
  `setsid -f`, because some agent shells kill the process group of each
  command when it returns.
- Remove project fork identity from lineage metadata, use neutral native
  application identifiers, and check submission addresses without embedding
  operator endpoints. Preserve third-party attribution.
- Export complete reviewed experiment directories, including instruction
  snapshots and CSV summaries, and reject broken references or task/run
  conflicts. Include explicitly unexecuted V1/V2 preparation records.
- Isolate same-second task attempts, recheck budgets at completion, and retain
  available turn evidence on budget-validation failures. Accept Gemini JSON-array
  streams as well as SSE.
- Include V1 evaluator setup dependencies, preserve checkout-relative asset
  bindings, fix V1 canary evaluation, and bind asset roots during queue recovery.
- Require sealed truncation evidence for final summaries, keep instruction
  defaults and opt-outs consistent, and reject undiscoverable Grok config paths.
- Unify gateway and runner budgets around complete successful generation
  delivery, preserving final-response tool execution. Failed generations have
  an independent bounded retry circuit and no-progress timeout.
- Separate V1/V2 setup assets, validate selected dependencies, and remove
  implicit setup downloads. Add explicit local-asset preparation and manifests.
- Add current-source experiment preparation with explicit task membership,
  exclusions, protocol IDs, pinned client versions and copied instructions.
  Preparation never launches a task or claims a measured result.
- Add fixed-denominator queue summaries in JSON, CSV and Markdown, preserving
  RAW scores, truncation policy, missing/error states and unknown measurements.
- Add allowlisted current-worktree submission export and actual extracted-source
  Linux build/MCP verification, separate from HEAD-only release archives.
- Replace always-injected skill files with condensed plain-text instructions
  under `instructions/`, separating common safety and outcome policy from the
  selected interaction guide. Runner protocol markers stay in the runner.
- Rename operating-profile selection to `--instructions-file` /
  `instructions_file` and the batch/queue opt-out to `no_instructions`.
  Standalone generation also uses `--instructions-file` and keeps
  `--base-instructions`. Old skill-selection names have no compatibility aliases.
  An empty instructions file argument omits only the profile, not base policy.
- These instruction edits change model context and require new evaluation
  attempts; no performance improvement is established by this migration.
- Add experimental native macOS (Swift Accessibility/CoreGraphics) and Windows
  (C# UIA3/Win32) backends behind the existing `AX` and `GUI` tools.
- Include native build scripts, unit/protocol tests, bounded sidecar lifecycle,
  stable native identity, and per-step ordered keyboard evidence on macOS.
- Keep provider pre-dispatch rejection separate from transport loss and
  uncertain input. Document platform permissions and native validation limits.

## 1.0.0

First open-source release of Hybrid Computer Use.

### Capabilities

- The `computer-use` MCP server exposes `AX` for application-scoped AT-SPI
  observation, lookup, assertions and batched actions, and `GUI` for
  full-desktop screenshots and batched pointer and keyboard input.
- The hybrid agent combines these desktop tools with shell commands, file
  editing, application APIs and, for Codex, its browser runtime over CDP.
  AX-only, GUI-only and no-skill configurations support channel ablations.
- The Python `hcu` package runs Codex, Claude Code, Gemini CLI and Grok Build
  in isolated OSWorld V1 or V2 Docker/QEMU guests. It provides setup canaries,
  single-task runs and a bounded queue with restart recovery. One JSON
  config describes a run; unknown keys fail validation. Each task writes
  one authoritative `result.json`.
- Gateways wait out upstream rate limits before the agent sees them and may
  move a stateless request to another deployment; rate-limited tasks are
  retried as infrastructure failures, never scored as agent failures.
- Setup assets come from local paths or a URL-keyed download cache;
  `--offline-assets` forbids downloads.
- Task-scoped OpenAI Responses, Anthropic Messages and Gemini gateways keep
  model credentials on the host and record token usage. The official
  evaluator supplies scores; result files also report evaluator errors,
  termination reasons and truncation.

### Dependencies

- The MCP runtime needs a Linux X11 desktop session, Python 3, PyGObject,
  AT-SPI, GTK and GdkPixbuf typelibs, `xdotool`, `wmctrl` and `x11-utils`.
  Building needs Docker or a local Go 1.22+ toolchain.
- OSWorld runs need Linux x86-64, KVM, Docker, `qemu-img`, Python 3.10+,
  the pinned OSWorld-V2 checkout with this repository's overlay, a compatible
  guest image, local task assets and a container-reachable model gateway.
  OSWorld-V2 task definitions and some assets require Hugging Face access.
- Agent CLIs and Gemini CLI's Node runtime are downloaded separately with
  `make agents`; they are not included in the source distribution.
  Guest images and task websites also require separate setup.
- `VERSION` supplies build and package version metadata. Original code is MIT
  licensed; files adapted from OSWorld-V2 retain Apache-2.0 licensing.

### Limits

- The desktop provider supports Linux AT-SPI/X11, not native macOS, Windows
  or Wayland desktops. Accessibility assertions prove only the requested
  provider-visible state; they do not prove file persistence, pixel content
  or task completion.
- Grok Build 1.0.40 cannot safely resume with its replacement system prompt.
  Tasks requiring a second turn fail explicitly. Claude Code uses a schema
  adapter because its pinned client omits root-level `oneOf` tools.
- Request budgets differ by gateway: OpenAI counts admitted requests;
  Anthropic and Gemini count successful responses delivered to the agent.
  The single-task runner defaults to no model-call or wall-clock limit, so
  operators must set budgets for real runs.
- Queue concurrency limits do not enforce host memory, disk or I/O pressure
  thresholds. Operators must provision capacity and monitor the host.
  Gateways spend model quota for reachable clients and require network access
  controls.
- Official evaluator scores are not independent instruction-level judgments.
  Inspect traces, evaluator errors and truncation before interpreting them.

See [Running OSWorld](docs/OSWORLD.md) for setup and operating requirements,
and [Agent profiles](docs/AGENT_PROFILES.md) for harness-specific behavior.
