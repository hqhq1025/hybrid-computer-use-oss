# AX, GUI, and CDP

Last verified against commit `a4006c5`.

## AX and GUI startup

`computer-use` must run in the same signed-in desktop session as the
applications. [Computer Use MCP](MCP.md#desktop-runtime) lists the Linux
session variables and packages, and [Platform support](PLATFORMS.md) covers
macOS permissions and Windows session requirements.

On Linux the provider switches session accessibility on itself, so
applications started by the user need no flags such as
`--force-renderer-accessibility`. Applications that were already running
may need a restart; `AX apps` names them.
[Accessibility on Linux](MCP.md#accessibility-on-linux) lists what each
toolkit needs. OSWorld guests also source `/home/user/a11y-env.sh` before
starting the agent. When session variables are missing, the server tries to
discover them from `/run/user/<uid>` and desktop processes, but explicit
session binding is more reliable. [Troubleshooting](MCP.md#troubleshooting)
lists diagnostic commands.

## CDP startup

CDP is independent from AX and GUI:

- AX sees accessibility semantics exposed by Chrome.
- GUI sees desktop pixels and sends desktop input.
- CDP controls the already-running browser through its debugging endpoint.

Do not launch a second Chrome when the endpoint is temporarily unavailable.
The V2 overlay retries `connect_over_cdp` three times with bounded exponential
backoff and probes `/json/version` for diagnostics. If all attempts fail, stop
with the accumulated error.

Typical guest checks:

```bash
curl -fsS http://127.0.0.1:9222/json/version
curl -fsS http://127.0.0.1:9222/json/list
ss -ltnp | grep 9222
```

The endpoint may be forwarded by the OSWorld provider. Always use the endpoint
reported by the active environment rather than assuming a host port.

## Channel selection

- Use CDP for DOM-aware browser navigation and downloads when Codex exposes it.
- Use AX for browser chrome, dialogs, menus, named controls, and semantic state.
- Use GUI for canvas content, spatial operations, pixels, and AX gaps.
- Use shell/file APIs for durable state when the instruction does not require a
  visible application workflow.

After GUI input, read AX again before reusing refs. GUI coordinates are
pixels of the latest GUI screenshot; do not reuse AX bounds or
window-relative coordinates as GUI coordinates.
