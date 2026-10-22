# macOS AX mutation failure evidence

Status: `merged`
Created: 2026-10-21
Change ID: `macos-ax-mutation-errors-20261021`
Baseline package version: `1.0.0`, unchanged.
Baseline native deployment: `hcu-candidate-r2`.
Baseline image: not applicable; the provider is a native macOS executable.
Candidate source: `/datadrive2/repos/hcu-macos-mab-20261021`.
No release or image promotion accompanies this change.

## Root cause

`setValueWithRoute` and window close threw ordinary errors when an AX mutation
returned a failure. `runtimeFailureResult` then reported no input. This loses
evidence when the target acted but its reply timed out.

The installed Apple SDK documents that `AXUIElementPerformAction` can return
`cannotComplete` because a modal callback has not returned. The documentation
explicitly says this does not necessarily mean the action failed.
`AXUIElementSetAttributeValue` also uses `cannotComplete` for messaging failure.
The existing selection route already preserved possible input in this case.

The existing action-error helper had the opposite problem for definite
rejections: it marked unsupported actions and invalid arguments as emitted.
Changing that helper also requires preserving earlier successful input when a
later action in a repeated click or scroll fails.

## Implementation and contract

`ComputerUseServiceOperations.swift` adds a shared mutation result check and
uses it for AXValue writes. Window close uses the same check in
`ComputerUseRuntimeWindows.swift`. The existing action-error helper delegates
to the shared error classification.

Illegal arguments, invalid AX objects, unsupported attributes or actions,
unimplemented AX operations and disabled AX APIs remain no-input rejections.
Messaging failures, generic failures and unrecognized mutation errors return
`effect_unknown`, with possible input and advice to observe before retrying.
Native success still proceeds through the existing readback and observation.

Repeated native scroll and click actions now pass whether an earlier action
succeeded. A later rejection cannot erase that earlier input. The click change
is in `ComputerUseServiceAccessibilityActions.swift`.

No native failure triggers an automatic retry or fallback. The change uses
native error codes and applies across applications. It does not add task IDs,
application recipes or benchmark conditions.

AX/GUI schemas, server instructions and parameter descriptions remain unchanged.
The existing public `effect_unknown` behavior and interaction guides already
require observation before retrying uncertain input. Runtime error messages now
conform to that contract. No instruction change or extra model context is needed.
No latency, token, cost or task-success improvement is claimed.

## Verification

`NativeAXMutationEvidenceTests.swift` injects a mutation that changes state and
then returns `cannotComplete`. Both value-write and window-close routes preserve
possible input and execute exactly one attempt. Other cases cover definite
rejections, earlier successful input, unknown native failures and native success.
The existing click-error regression now uses a messaging failure rather than
an unsupported-action rejection for its post-emission assertions.

The complete native library compiled with Apple Swift 6.3.2. A standalone
runner derived from the standard Swift Testing source passed all assertions.
The installed Command Line Tools lacks `Testing`, so this is native assertion
execution, not a successful SwiftPM test-suite run.

Evidence is under `/datadrive2/benchmarks/macagentbench-20261021/evidence/`:

- `hcu-ax-mutation-build.log` records the native library build.
- `hcu-ax-mutation-evidence.log` records zero assertion failures.
- `ax-mutation-evidence-main.swift` contains the assertion runner.

`git diff --check` passed. Tests used injected callbacks and did not send GUI
input or alter the installed provider. No real application timeout has been
forced. The fix remains a candidate pending integration checks and deployment.

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
