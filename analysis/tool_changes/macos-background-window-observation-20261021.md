# macOS observations of explicitly selected background windows

Status: `merged`
Created: 2026-10-21
Change ID: `macos-background-window-observation-20261021`
Baseline package version: `1.0.0`, unchanged.
Baseline native deployment: `hcu-candidate-r2`.
Baseline image: not applicable; the provider is a native macOS executable.
Candidate source: `/datadrive2/repos/hcu-macos-mab-20261021`.
No release or image promotion accompanies this change.

## Root cause and evidence

A real two-window TextEdit check selected background window A while B remained
active. `AX find` for A returned `state_unavailable` even though A still existed.
The original trace is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r2b/mcp.json`.

The Go request and native bridge supplied the selected window handle.
`SnapshotBuilder.build` nevertheless chose the focused window first. It checked
the requested handle only when it needed a cached fallback because no focused
window existed. The outer identity check correctly rejected the snapshot of B.

Two adjacent evidence paths also assumed that the selected window was active.
Snapshot focus and selected text came from the application's current focus,
and the window metadata treated an active application as an active window.
The native capture chooser could also borrow a sibling's geometry when an
explicit native window ID was absent from its preferred candidates.

## Implementation and model contract

`AccessibilitySnapshot.swift` now selects an explicit handle from a complete
AX window inventory using the same canonical handle used to issue references.
It requires one match. Missing, ambiguous or unreadable inventories fail without
selecting a sibling or changing desktop focus. The outer liveness check still
decides whether absence is proven. Unbound discovery keeps its existing behavior.

Explicit window selection disables both activation-recovery paths. It includes
background and minimized windows in the inventory. Focus and selected text are
retained only when their containing window matches the selected window.

`AccessibilityWindowCapture.swift` requires an exact unique native ID when the
caller supplies one. It does not replace a missing target with a similarly
titled or foreground sibling. An unavailable capture can still leave AX-only
geometry from the selected root when the caller did not request an image.

`ProviderSnapshotProjection.swift` marks a selected window active only when
the application is active and the current focused window has that exact handle.

The public schema, tool descriptions, server instructions and interaction guides
already define explicit window ownership. They require no changes. This fix
implements that contract rather than changing ref meaning or adding app-specific
behavior. Error responses retain their existing no-input evidence.

The change does not activate a background window, emit input, or add retries.
Focus summaries no longer include objects outside the selected window, including
app-owned menus that have no containing window. The menu tree remains visible.
No latency, token, cost or task-success improvement is claimed.

## Verification and disposition

`BackgroundWindowSnapshotTests.swift` covers explicit background selection,
missing or duplicate handles, incomplete inventories, unbound discovery,
cross-window focus exclusion, and exact native capture identity. The capture
tests include a small target, a same-title sibling and duplicate native IDs.

A separate reviewer checked the selection and focus-projection changes without
finding a blocking issue. The complete candidate library compiled with Apple
Swift 6.3.2, and all standalone native assertions passed. The runner derives
from the standard Swift Testing file; Command Line Tools lacks the `Testing`
module, so this does not establish a successful SwiftPM test-suite run.

Evidence is under `/datadrive2/benchmarks/macagentbench-20261021/evidence/`:

- `hcu-background-window-build.log` records the native library build.
- `hcu-background-window-tests.log` records zero assertion failures.
- `background-window-main.swift` contains the generated assertion runner.

`git diff --check` passed, and the document checker found no hard-rule
violations. Root owns the real two-window and stale-reference canaries.
No candidate deployment success is claimed until those checks complete.

## Combined candidate validation

Candidate `hcu-macos-mab-20261021-r5` builds successfully on macOS 26.3
with Swift 6.3.2. The combined `make test` passes Go checks, 607 provider
tests and 360 Python tests; 28 environment-dependent Python tests skip.
Logs: `/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-build-r5.log`
and `hcu-combined-checks-r5.log` in the same directory.
The candidate manifest is `/datadrive2/benchmarks/macagentbench-20261021/hcu-candidate-r5.json`.
Real desktop validation remains separate from these build and test results.

## Final integration result

Candidate `hcu-macos-mab-20261021-r6` passed the complete Go/provider/Python
checks and all seven native desktop canaries on macOS 26.3 x86_64.
It combines the r6 Go wrapper with the unchanged, authorized r5 Swift provider.
The final close response reports `target_window_absent=true` and
`close_effect_verified=true`, with a lifecycle observation and no unknown-effect
flags. Background read/write, exact text, sibling preservation, stale refs,
cross-window preflight, whole-GUI-batch validation and stale pixels passed.

Evidence: `/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r6/report.json`
and `hcu-combined-checks-r6.log` in the evidence directory.
The manifest is `/datadrive2/benchmarks/macagentbench-20261021/hcu-candidate-r6.json`.
Injected failures remain separate from normal live input checks; no real
application timeout was forced. Standard SwiftPM tests remain unavailable
without the `Testing` module. Native assertion logs retain their narrower scope.
Disposition: retain the validated candidate; no release, benchmark score,
latency claim or promotion. Historical failed attempts remain unchanged.
