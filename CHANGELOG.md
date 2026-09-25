# Changelog

## Unreleased

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
  single-task runs, bounded batches and rolling queues with restart recovery,
  launch staggering and a rate-limit circuit breaker.
- Batch and queue runners share one configuration schema. Each configuration
  selects one benchmark generation, and unknown configuration keys fail
  validation. `HCU_QUEUE` selects the default rolling-queue configuration.
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
