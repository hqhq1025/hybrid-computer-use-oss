# Native Platforms

The public MCP surface is always `AX` and `GUI`. The binary selects its
backend at compile time; an unsupported OS fails explicitly. No platform
selection parameter is exposed to the model.

| OS | Native implementation | Runtime requirements |
| --- | --- | --- |
| Linux | Embedded Python, AT-SPI2, X11 | Signed-in X11 session and the packages in MCP.md |
| macOS | Swift, AXUIElement, CoreGraphics, ScreenCaptureKit | macOS 14+, graphical login, Accessibility and Screen Recording consent |
| Windows | C#/.NET 8, FlaUI UIA3 5.0, Win32 | Unlocked interactive session, compatible target integrity level |

The macOS and Windows integration is experimental. The OSWorld harness uses
Linux guests; native providers are for controlling a local desktop.

## macOS

Install Go 1.22+, Swift 6.2+ and the macOS SDK, then from the repository root:

```bash
make build-macos
artifacts/macos/computer-use doctor
python3 scripts/smoke-mcp.py artifacts/macos/computer-use
```

Keep `ComputerUseProvider.app` beside `computer-use`. The build script uses
the repository `VERSION` and signs the provider bundle for local execution.
Local signing is not notarization or a distribution signing identity. Replacing
or moving the bundle may require renewing its permissions in System Settings.
Grant Accessibility and Screen Recording to the native provider. Launch from
the signed-in user's session, not a headless service account.
The provider's bundle identifier is `org.computer-use.provider.v2`. Upgrading
from a bundle with a different identifier changes its macOS permission identity;
grant permissions to the new bundle rather than assuming an old grant applies.

The provider uses a private local socket and retains native object identity
within its process generation. A reset invalidates that continuity. Do not
reuse refs from an earlier MCP session or retry an uncertain mutation blindly.

Some window and input operations use Apple's private SkyLight SPI from the
installed macOS system framework. No Apple framework is bundled. These
interfaces are not a public compatibility contract and can change between
macOS releases.

## Windows

Install Go 1.22+, the .NET 8 SDK and PowerShell 7+. Windows PowerShell 5.1
is not supported; installing the .NET SDK does not upgrade PowerShell.
From the repository root:

```powershell
pwsh -NoProfile -File ./computer-use/native/windows/build.ps1 -OutputDirectory "$PWD/artifacts/windows"
./artifacts/windows/computer-use.exe doctor
python scripts/smoke-mcp.py artifacts/windows/computer-use.exe
```

Keep the self-contained `computer-use-windows-provider.exe` beside
`computer-use.exe`; a separately installed .NET runtime is not required for
that package. The provider runs under the MCP user's account and integrity
level. It does not bypass UAC, secure desktops, or session isolation. UIA
visibility reported by the application can differ from visible pixels;
reobserve instead of forcing a stale or offscreen semantic target.

An uncertain focus or pointer preparation failure is different from a stale
target. The requested input may be known not to have been emitted while a
queued focus request can still change the desktop later. Honor an explicit
stop response: do not replay, automatically observe, or continue the batch.
A fresh screenshot or restarting the MCP process does not prove that a pending
operating-system request has settled. Stop desktop automation and report the
uncertainty for operator recovery.

## Start A Session

Run `computer-use doctor` as the signed-in desktop user, then configure the
client as described in [Computer Use MCP](MCP.md). Start with an AX application
observation or a GUI screenshot. Use only refs from the current session and
coordinates from the latest GUI screenshot.

AX readback describes provider-visible state. Verify saved files through the
application or filesystem when persistence is required.
