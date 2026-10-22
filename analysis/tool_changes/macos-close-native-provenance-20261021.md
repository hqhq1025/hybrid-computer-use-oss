# Preserve native identity before closing an apps-only window

Status: `merged`
Created: 2026-10-21
Change ID: `macos-close-native-provenance-20261021`
Baseline package version: `1.0.0`, unchanged.
Baseline native candidate: `hcu-macos-mab-20261021-r4`.
Baseline image: not applicable; the provider is a native macOS executable.
Baseline manifest: `/datadrive2/benchmarks/macagentbench-20261021/hcu-candidate-r4.json`.
Candidate source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Integrated candidate: `hcu-macos-mab-20261021-r5`.
Guest executable: `/Users/pipiwu/mab-tools/hcu-candidate-r5/computer-use`.

## Problem and evidence

An AX apps observation can issue a window ref before any full snapshot of that
window. Closing that ref successfully removed the requested window in the r4c
desktop check. The provider returned `effect_unknown` because it lacked the
native identity provenance required to prove absence while a sibling survived.

The service previously recorded native identity only from a full snapshot.
The close path resolved the exact live window, but did not record that identity
before creating its liveness inspector. The inspector captured an unregistered
identity and retained that state after close.

Evidence is under `/datadrive2/benchmarks/macagentbench-20261021/evidence/`:

- `hcu-native-safety-r4c/mcp.json`, call 20, records close of apps-only ref `w7`.
- `hcu-native-safety-r4c/report.json` records the failing close check.
- `hcu-close-liveness-r4c.log` records the native cause. Both AX and WindowServer
  report absence with zero matching windows. `native_comparable` is false,
  `native_empty` is false, and the result is `unknown`.

## Implementation

`ComputerUseRuntimeWindows.swift` records the selected window's verified native
identity before input and before creating the liveness inspector.
`BoundWindowLiveness.swift` adds a shared registration overload accepting the
process ID, native window ID and canonical handle. Snapshot registration uses
the same overload. Registration requires a positive native ID whose canonical
handle exactly matches the selected handle. Missing IDs and fallback handles
remain unregistered.

The liveness inspector accepts an injectable inventory function for focused
tests. Its production function and captured provenance semantics are unchanged.
`ClosedWindowProvenanceTests.swift` covers registration and close completion.

Native provenance alone does not prove closure. The existing independent AX
and WindowServer absence checks still decide whether closure is verified.
Conflicting or unavailable evidence retains `effect_unknown` and emitted-input
evidence. The fix adds no retry, window activation or sibling substitution.

## Generality and model-contract review

Any application can expose a window through apps discovery before a full
snapshot. The change uses native identity and inventory evidence without
application names, benchmark tasks or evaluator knowledge.

AX/GUI schemas, parameter descriptions, server instructions and benchmark
skills remain unchanged. They already permit close through a valid window ref
and require honest evidence after input. No new model action or recovery rule
is needed. This change record documents the implementation requirement for
developers; no additional operator action is introduced.

The fix retains strict absence proof, compatibility and input evidence.
It adds a registration in the existing provider-local identity set and no
additional screenshot or inventory query. No measured latency, token, cost or
task-success improvement is claimed.

## Verification and disposition

The full native library builds with Apple Swift 6.3.2. A standalone native
assertion runner derived from `ClosedWindowProvenanceTests.swift` exits zero
and reports `Closed window provenance failures: 0`. The VM's Command Line
Tools lacks the `Testing` module, so this is not a SwiftPM test-suite pass.

The assertions reject missing, zero, mismatched and fallback native identity.
They exercise 18 combinations of registration, AX state and WindowServer
state with a surviving native window. Only registered identity plus absence
in both inventories produces `windowAbsent`. All other combinations retain
`effect_unknown`; every close result retains `input_events_emitted=true`.

Focused evidence in the same evidence directory:

- `hcu-closed-window-provenance-build.log` records the library build.
- `hcu-closed-window-provenance-tests.log` records zero assertion failures.
- `closed-window-provenance-main.swift` preserves the assertion runner.

Independent source review found no blocking issue. Source is frozen for the
r5 build. The parent agent owns the actual r5 desktop close check and combined
validation. Those results remain pending; the r4c failure is not relabeled.
No formal benchmark run, release promotion or product score is claimed.

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
