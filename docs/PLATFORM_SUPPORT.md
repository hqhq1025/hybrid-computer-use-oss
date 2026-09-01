# Platform Support

The MCP surface is platform-invariant. Linux, macOS, and Windows advertise the
same two public tools and the same JSON Schemas:

- `AX` for application/window inventory, semantic observation, find, assert,
  and batched actions;
- `GUI` for full-desktop screenshot observation and batched pixel input.

Operating-system selection happens only below `PlatformBackend` through the
private Provider V2 wire contract.

## Capability Matrix

| Provider operation | Linux | macOS | Windows |
| --- | --- | --- | --- |
| Application inventory | AT-SPI | LaunchServices inventory | Win32 process inventory |
| Top-level windows | AT-SPI + X11 | Accessibility windows | UI Automation windows |
| Semantic tree | AT-SPI | macOS Accessibility | UI Automation Raw View |
| Semantic click | AT-SPI action + guarded coordinate fallback | `AXPress` + CoreGraphics fallback | Invoke/Select/Toggle + Win32 fallback |
| Type / keypress | AT-SPI + XTest | Accessibility focus + CoreGraphics | UIA focus + `SendInput` |
| Scroll | AT-SPI + XTest | CoreGraphics | Win32 wheel input |
| Set value | AT-SPI EditableText/Value | `AXValue` | UIA ValuePattern |
| Activate / close | X11 + AT-SPI | `AXRaise` / `AXClose` | UIA WindowPattern |
| Full-desktop screenshot | GDK/GdkPixbuf | `screencapture` | GDI `CopyFromScreen` |
| HiDPI/image coordinate mapping | yes | yes | yes |
| Stale layout rejection | yes | yes | yes |
| Post-input unknown-effect evidence | yes | yes | yes |

The portable providers keep native process generations, window handles and
element identities private. Go issues the model-visible `aN`, `wN`, and `rN`
refs under the same stale-reference policy on every OS.

## Runtime Requirements

macOS requires Accessibility, Automation of System Events, and Screen
Recording grants for the process identity launching the provider. Application
inventory remains available before those grants; denied semantic access is a
typed precondition failure with no inferred input.

Windows must run in the signed-in interactive session. UI Automation and input
delivery cannot cross to a higher-integrity target unless the driver runs at a
matching level. The provider uses the Win32 virtual-screen rectangle for
negative monitor origins and scales images to the public 1920×1080 delivery
bound before translating image coordinates back to native pixels.

Linux runtime dependencies and session discovery remain unchanged.

## Validation Levels

- Linux retains the existing Go, Python provider, container, QEMU, and OSWorld
  validation paths.
- macOS is compiled natively and has live host smoke coverage for Provider V2
  application inventory, permission denial, full-desktop PNG capture, HiDPI
  scaling, and MCP response rendering. Semantic action smoke requires granting
  TCC access to the test process.
- Windows is protocol-tested and cross-compiled from macOS. Final release
  qualification must also run the same MCP smoke and real UIA/input scenarios
  in an interactive Windows VM; cross-compilation alone is not delivery proof.

This distinction is intentional: build portability, native permission access,
pixel correctness, and accepted application input are separate evidence.
