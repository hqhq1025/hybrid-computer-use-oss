# AX, GUI, And CDP

## AX Startup

`computer-use` must run in the same signed-in Linux desktop session as the
applications. The environment must expose the correct:

```text
DISPLAY
XAUTHORITY
XDG_RUNTIME_DIR
DBUS_SESSION_BUS_ADDRESS
```

OSWorld guests provide `/home/user/a11y-env.sh`; runners source it before
starting the agent. The MCP can discover some session values from
`/run/user/<uid>` and desktop processes, but explicit guest session binding is
more reliable.

Required Linux packages:

```text
python3
python3-gi
gir1.2-atspi-2.0
gir1.2-gtk-3.0
gir1.2-gdkpixbuf-2.0
xdotool
wmctrl
x11-utils
```

Quick diagnostics:

```bash
computer-use doctor
computer-use --version
python3 -c 'import gi; gi.require_version("Atspi", "2.0"); from gi.repository import Atspi'
xdotool getactivewindow
wmctrl -lpG
```

## CDP Startup

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

## Channel Selection

- Use CDP for DOM-aware browser navigation and downloads when Codex exposes it.
- Use AX for browser chrome, dialogs, menus, named controls, and semantic state.
- Use GUI for canvas content, spatial operations, pixels, and AX gaps.
- Use shell/file APIs for durable state when the instruction does not require a
  visible application workflow.

After GUI mutation, refresh AX before reusing semantic refs. Never reuse AX
window-relative coordinates as GUI full-desktop coordinates.
