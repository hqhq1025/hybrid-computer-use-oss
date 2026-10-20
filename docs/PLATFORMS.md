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

## AX selection and editable controls

The shared AX interface accepts `select`; native capabilities determine which
ranges can be selected and verified.

| Provider | Implemented selection | Readback |
| --- | --- | --- |
| macOS | All text or one unique text match within one element with a writable AX selection range | Exact UTF-16 range, selected text, and unchanged source text |
| Windows | All text or one unique match in a UIA TextPattern control; one SelectionItem | Complete selection, text range endpoints and text, or the selected item |

Both providers reject cross-element ranges before input. Duplicate or missing
text also fails before selection. After a selection write, failed readback
reports that input may have changed the selection; do not retry blindly.

Windows selection readback proves the UIA selection state. It does not prove
that an application received a selection-change callback or updated its own
cached selection. Verify the resulting application state before relying on
that callback. A native WinForms ListBox test exposed this distinction.

The Linux composite-control `editTarget` identity has no native mapping on
these providers. They validate its wire format and reject its use before input.
Use a current ref for the native editable field. Ordinary native field editing
continues through each provider's existing text and value interfaces.

GUI image scaling and coordinate mapping remain in the existing platform
capture and input paths. These paths check layout before coordinate input.
macOS ordered keyboard input accepts up to 64 steps, matching the Go core.

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

App discovery includes window titles and identities for AX window waits.
Incomplete enumeration or failed title reads return an error, so missing
evidence cannot prove that a window closed. Windows without a unique native
handle remain visible by title but do not receive actionable window refs.

Closing the last document can remove the window before final capture. If the
provider cannot verify the result, it reports `effect_unknown` and stops the
batch. This does not establish that the close failed; inspect the current
desktop before deciding what to do next.

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
