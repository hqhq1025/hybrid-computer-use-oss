# Native Platforms

The public MCP surface is always `AX` and `GUI`. The binary selects its
backend at compile time; an unsupported OS fails explicitly. No platform
selection parameter is exposed to the model.

| OS | Native implementation | Runtime requirements |
| --- | --- | --- |
| Linux | Embedded Python, AT-SPI2, X11 | Signed-in X11 session and the packages in [Computer Use MCP](MCP.md#desktop-runtime); the provider switches accessibility on, and applications started before it may need a restart ([details](MCP.md#accessibility-on-linux)) |
| macOS | Swift, AXUIElement, CoreGraphics, ScreenCaptureKit | macOS 14+, graphical login, Accessibility and Screen Recording consent |
| Windows | C#/.NET 8, FlaUI UIA3 5.0, Win32 | Unlocked interactive session, compatible target integrity level |

The macOS and Windows integration is experimental. The OSWorld harness uses
Linux guests; native providers are for controlling a local desktop.

| OS | Checked in CI | Not checked in CI |
| --- | --- | --- |
| Linux | Go tests, race tests of both native transports, provider tests with PyGObject | Desktop input; OSWorld guest runs cover it |
| macOS | Swift unit tests, `GOOS=darwin go vet`, package build, MCP smoke test | Permissions and desktop input |
| Windows | .NET protocol and policy tests, `GOOS=windows go vet`, package build, MCP smoke test | Session, integrity level and desktop input |

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

Every provider scales GUI screenshots to fit within 1366x768 and maps
coordinates back to the native desktop per axis. Each provider checks the
layout before coordinate input. macOS ordered keyboard input accepts up to 64
steps, matching the Go core.

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

Permission diagnostics use the current provider process's non-prompting AX and
CoreGraphics checks. An old TCC database grant cannot override a runtime denial.
After replacing a signed bundle, grant access to the current provider and restart
it before checking permissions again.

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
From the repository root (`make build-windows` runs the same script):

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

Text input requires a Unicode focused window. Standard Edit and RichEdit
controls receive each text segment through `EM_REPLACESEL`, which replaces
the selection or inserts at the caret using the control's formatting rules.
A segment ends at CR or LF.
Other windows receive character messages. The provider uses native control
classes to select the route, without application-specific rules.
It rejects missing focus, ANSI-only windows and read-only standard text
controls before input. NUL characters and unpaired UTF-16 surrogates anywhere
in a GUI batch reject the entire batch before input.
Standard single-line controls reject text containing CR or LF before its first
segment. Ordinary text remains supported in those controls.
Tabs are literal characters; use `keypress TAB` to move focus. CR, LF and
CRLF each produce one logical newline. Standard Edit controls receive CRLF
through `EM_REPLACESEL`; supported RichEdit 2+ controls receive CR through
the same route. Other Unicode windows receive one carriage-return character
message. Literal text does not synthesize Enter keydown or keyup. The clipboard
is not used.
Other control characters retain the application's message semantics; a
successful send does not guarantee that they become stored characters.

The provider checks the focused window before each message and stops if it
changes. Each message has a one-second timeout; one text action has a
15-second budget. A timeout can occur after the application received a
message. Inspect the application before retrying, because the provider does
not replay uncertain input. A timed-out segment has an unknown result for
the whole segment; message counts do not identify a completed character prefix.
Successful delivery does not verify field
contents or prove that the application has finished drawing.
After any send attempt, the provider posts one wake message to the original
receiver so its message loop can resume. This also applies after partial
input; the wake does not replay text or acknowledge its completion.

GUI scroll deltas send one wheel notch per 100 units, and at least one notch
for any non-zero delta, as on Linux. A single-character key must be an ASCII
letter, digit or US-layout punctuation key. Other characters, such as `!` or
`é`, are rejected before any input in the batch; enter them with `type`.

An observation reads the window tree for at most 15 seconds. A larger tree,
such as a LibreOffice Calc window, returns the part read so far with partial
coverage instead of an error. Controls in the unread part are not
addressable; use `find`, a subtree root, or GUI for them.

LibreOffice draws its controls with its own toolkit, VCL, and loads the
Windows accessibility bridge only when its process asks for it. Some
releases, including 24.8, need `SAL_FORCE_IACCESSIBLE2=1` in the environment
of the LibreOffice process; without it a window exposes only its title bar.
LibreOffice 25.2 exposes its controls without the variable. The provider
cannot change the environment of a running application, so
`AX {"mode":"apps"}` and observe name such a window and the restart that fixes
it. To enable the bridge:

1. Set the variable for your account: `setx SAL_FORCE_IACCESSIBLE2 1`.
2. Quit every LibreOffice window, including the Quickstarter in the
   notification area, because a running instance keeps its environment.
3. Start LibreOffice again from the Start menu or from a new shell.

In LibreOffice Calc, a sheet lists its cells over the whole sheet row by
row (24.8) or not at all (25.2), so the provider reads the cells in view by
hit test, one per cell. Cells that hold a value and the cell under the
cursor get refs; the other cells in view are summarized as `/empty-cells`.
After a click or value on a cell, or text, an edit key or a cursor key in
the sheet, the provider reads only the window root, the formula bar, the
sheet, and the cells that hold a value or the cursor. The result notes that
the other controls and the empty cells are as last observed; `observe`
reads them all again.

An uncertain focus or pointer preparation failure is different from a stale
target. The requested input may be known not to have been emitted while a
queued focus request can still change the desktop later. Honor an explicit
stop response: do not replay, automatically observe, or continue the batch.
A fresh screenshot or restarting the MCP process does not prove that a pending
operating-system request has settled. Stop desktop automation and report the
uncertainty for operator recovery.

## Start a session

Run `computer-use doctor` as the signed-in desktop user, then configure the
client as described in [Computer Use MCP](MCP.md). Start with an AX application
observation or a GUI screenshot. Use only refs from the current session and
coordinates from the latest GUI screenshot.

AX readback describes provider-visible state. Verify saved files through the
application or filesystem when persistence is required.
