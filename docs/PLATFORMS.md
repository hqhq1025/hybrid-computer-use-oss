# Native Platforms

The public MCP surface is always `AX` and `GUI`. The binary selects its
backend at compile time; an unsupported OS fails explicitly. No platform
selection parameter is exposed to the model.

| OS | Native implementation | Runtime requirements |
| --- | --- | --- |
| Linux | Embedded Python, AT-SPI2, X11 | Signed-in X11 session and the packages in MCP.md |
| macOS | Swift, AXUIElement, CoreGraphics, ScreenCaptureKit | macOS 14+, graphical login, Accessibility and Screen Recording consent |
| Windows | C#/.NET 8, FlaUI UIA3 5.0, Win32 | Unlocked interactive session, compatible target integrity level |

The macOS and Windows integration is experimental. Compilation, protocol
tests, and MCP initialization do not prove native input delivery or permission
readiness. No native desktop benchmark score is implied. The OSWorld harness
still runs its Linux guest environment; native providers do not add macOS or
Windows benchmark orchestration.

## macOS

Install Go 1.22+, Swift 6.2+ and the macOS SDK, then from the repository root:

```bash
make build-macos
swift test --package-path computer-use/native/macos
artifacts/macos/computer-use doctor
python3 scripts/smoke-mcp.py artifacts/macos/computer-use
```

Keep `ComputerUseProvider.app` beside `computer-use`. The build script uses
the repository `VERSION` and signs the provider bundle for local execution.
Local signing is not notarization or a distribution signing identity. Replacing
or moving the bundle may require renewing its permissions in System Settings.
Grant Accessibility and Screen Recording to the native provider. Launch from
the signed-in user's session, not a headless service account.

The provider uses a private local socket and retains native object identity
within its process generation. A reset invalidates that continuity. Do not
reuse refs from an earlier MCP session or retry an uncertain mutation blindly.

## Windows

Install Go 1.22+ and the .NET 8 SDK. In PowerShell from the repository root:

```powershell
./computer-use/native/windows/build.ps1 -OutputDirectory "$PWD/artifacts/windows"
dotnet run --project computer-use/native/windows/ComputerUse.Windows.ProviderV2.Tests -- computer-use/testdata/provider-v2
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

## Verification

`make test` runs shared Go, Linux provider and harness tests.
`make test-transports` runs the macOS and Windows transport suites using
subprocess fixtures. Native CI builds the Swift app and C# executable and
runs their unit/protocol tests without claiming a usable graphical session.

Before deployment, verify in a disposable signed-in desktop session:

1. Public `AX` app enumeration and one independent `GUI` screenshot.
2. A semantic action and readback in a disposable document.
3. Window closure/replacement: old refs reject without input.
4. An ordered batch with an induced failure: later actions are not emitted.
5. Screenshot-to-pointer mapping at the actual display scale and monitor layout.
6. Permission denial, worker reset, and uncertain post-input capture failure.
7. A multi-point drag that changes direction, preserving every requested point.
8. Visual settling on a static view, a changing view that stabilizes, and a
   continuously changing view that reaches the time budget. Check that
   `settle_ms=0` captures without claiming a stillness comparison.

macOS relative-action readback continuity and Windows offscreen menu reporting
remain limitations under investigation. AX readback proves provider-visible
state, not durable persistence or whole-task completion.
