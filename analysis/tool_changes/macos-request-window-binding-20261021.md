# Preserve the requested window during internal AX observations

Status: `merged`
Created: 2026-10-21
Change ID: `macos-request-window-binding-20261021`
Baseline package version: `1.0.0`, unchanged.
Baseline native candidate: `hcu-candidate-r3`.
Baseline image: not applicable; the provider is a native macOS executable.
Candidate source: `/datadrive2/repos/hcu-macos-mab-20261021`.
The integrated executable needs a new candidate identity, `hcu-candidate-r4`.

## Root cause

The background-window fix honors an explicit handle in `SnapshotBuilder.build`.
Several input operations still call `refreshSnapshot` without that argument
after input. For a request targeting background window A, those calls can read
active sibling B and overwrite the service cache before the final observation.

The affected paths include service-level scroll, drag, type, keypress, value
write and click, plus runtime final observations and window-action capture.
The request already binds application identity across these internal calls,
but it did not retain the requested window in that same scope.

The native ordered-input final observation already passes its exact window
handle directly to `SnapshotBuilder`. That path needs no additional change.
Unbound application discovery must continue to choose its normal initial window.

## Implementation

`ComputerUseService.swift` adds the window handle to the existing synchronous
request identity scope. It restores both application and window bindings after
normal return or an error. A nested request without a window does not inherit
the outer request's window; it restores that outer binding on exit.

`ComputerUseRuntimeBridge.swift` supplies the handle using the same resolver as
window actions. `ComputerUseRuntimeWindows.swift` makes that existing resolver
available within the module. The resolver accepts the top-level target handle
and the existing semantic-target window handle.

`ComputerUseServiceOperations.swift` makes every internal `refreshSnapshot`
inherit the active request window when the caller omits a handle. An explicit
conflicting handle fails before application discovery or capture. This fixes
the shared path instead of adding separate fallback rules to each input action.

After close, a missing requested window still fails capture. The existing
post-input handler preserves emitted-input evidence and checks exact-window
liveness. Only a separate absence proof produces `windowAbsent`; a sibling
snapshot cannot substitute for that proof.

## Model-contract review and implications

AX/GUI schemas, action parameters, server instructions and interaction guides
remain unchanged. They already bind semantic actions to one window. The change
keeps internal observations within that ownership contract and preserves the
existing `effect_unknown` behavior when observation fails after input.

No new model context, retry, activation or fallback is added. Request bindings
remain process-local and temporary. The provider already serializes requests;
this change does not introduce a shared cross-request identity cache.
No latency, token, cost or task-success improvement is claimed.

## Verification and disposition

`RequestWindowBindingTests.swift` covers successful and failing nested scopes,
unbound nested requests, explicit conflicts before discovery, and close followed
by disappearance while a sibling survives. The close tests distinguish proven
absence from unconfirmed absence and retain emitted-input evidence in both cases.

A separate static review found no blocking issue. The full native library
compiled with Apple Swift 6.3.2, and the standalone native assertion runner
passed every assertion. The runner derives from the standard Swift Testing
file. Command Line Tools lacks `Testing`; this is not a SwiftPM suite pass.

Evidence is under `/datadrive2/benchmarks/macagentbench-20261021/evidence/`:

- `hcu-request-window-binding-build.log` records the native library build.
- `hcu-request-window-binding-tests.log` records zero assertion failures.
- `request-window-binding-main.swift` contains the assertion runner.

`git diff --check` passed, and the document checker found no hard-rule
violations. Root owns the fresh runtime check: write background A, retain B
as active, then verify A's content and its observation. A later observation
must not be presented as evidence from the action's own final snapshot.
No r3 result is relabeled as an r4 result.

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
