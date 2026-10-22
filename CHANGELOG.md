# Changelog

## Unreleased

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
  screenshot pixels with the GPT-5.4 high-detail image used in validation.
- Update base and hybrid instructions to stop after sufficient evidence,
  bound application discovery, and save/close/reopen documents when a needed
  property requires direct file editing. The combined changes have not yet
  received a new model evaluation; separate candidate results are not a
  combined performance measurement.
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
  diff when it is shorter than the tree. On the paper OSWorld 1.0 harness
  (20-task panel, GPT-5.4, two rounds), the median input per solved task is about 0.6 of
  v0.14 with the same steps; scores are within one task of v0.14.
- `until` is replaced by ordered `wait` steps inside an act batch. The
  computer-use daemon serves the same core to the CLI and to the `hcu_sdk`
  Python module, which includes a PTC adapter.
- GUI results start with the same JSON evidence line and public error codes
  as AX. On Linux, a GUI click is rejected as stale only when the window
  under its point changed after the screenshot.
- On Linux, an unchanged window's AX tree is reused across calls by evidence
  (no UI event, same fingerprint, visible content re-read) instead of a
  10-second age limit, within a 64 MB cache. A Calc observe drops from about
  550 ms to about 195 ms on a real desktop.
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
