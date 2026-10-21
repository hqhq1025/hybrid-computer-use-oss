# macOS screen-capture permission errors

Status: `testing`
Created: 2026-10-21
Change ID: `macos-capture-permission-errors-20261021`
Baseline package version: `1.0.0`, unchanged.
Baseline runtime: `hcu-candidate-r1` in the isolated macOS test VM.
Baseline image: not applicable; the provider is a native macOS executable.
Candidate source: `/datadrive2/repos/hcu-macos-mab-20261021`.
This change does not assign a release or a Docker image.

## Problem and evidence

The first real GUI screenshot failed because ScreenCaptureKit denied capture.
The native error had domain `com.apple.ScreenCaptureKit.SCStreamErrorDomain`
and code `-3801`. The public result instead reported `runtime_error` and
suggested another screenshot. Repeating the screenshot cannot grant permission.
The request emitted no input.

The original public response is preserved at
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-initial-gui.json`.
The VM's Apple SDK `SCError.h` declares `SCStreamErrorUserDeclined = -3801`
and defines the condition as the user choosing not to authorize capture.

## Implementation

`DesktopRuntime.swift` normalizes that exact NSError domain and typed
`SCStreamError.Code.userDeclined` code to `ComputerUseError.permissionDenied`.
The message asks the user to enable Screen Recording for the current
Computer Use Provider app and restart the provider before another screenshot.
Error descriptions can be localized; classification does not inspect words.

`ComputerUseRuntimeGUI.swift` applies this normalization at the final-observation
error boundary and returns `failureCode=permission_denied`. It preserves the
input count, input-emission flag, partial-input possibility and unknown-mutation
flag. A failure after earlier input still reports that input.

`ComputerUseRuntimeBridge.swift` applies the same normalization at its runtime
error boundary. Wrong domains, other native codes and messages that merely
mention TCC retain their existing error classification and evidence.

The companion Go change preserves permission-denied errors in public GUI
results and supplies permission recovery guidance. That change has separate
ownership and verification. The Swift fix alone cannot correct the public
Go mapping or its default screenshot hint.

The implementation applies to all ScreenCaptureKit consumers without
application-specific or benchmark-specific conditions. Missing entitlements
remain a distinct native failure; granting Screen Recording does not fix them.

## Model-contract review

AX and GUI schemas, action parameters and server instructions remain unchanged.
The runtime error message now identifies an actionable permission problem.
The public Go error table and recovery guidance require a coordinated update
because the previous GUI contract prescribed a screenshot hint for every error.
The evaluated-agent recovery text and platform documentation are reviewed by
the Go and integration owners with that public mapping change.

No automatic permission request, retry, input action or provider reset is added.
The message asks for permission recovery; it does not claim that permission
was granted, that input succeeded or that a screenshot exists.
There is no measured token, latency, cost or task-success improvement.

## Tests and disposition

`CapturePermissionErrorTests.swift` covers the native denial at both runtime
boundaries, GUI failures before and after input, and localized error text.
Negative cases cover a wrong domain, a different ScreenCaptureKit code,
missing entitlements and a matching error message from another domain.

The regression driver first ran against the existing r1 native library and
reproduced the classification and recovery-message failures. The driver derives
its assertions from the standard Swift Testing file. It uses ordinary assertions
because the installed Command Line Tools lacks the `Testing` module.
The r1 library failed nine assertions. The isolated candidate library compiled
with Apple Swift 6.3.2, and the same driver then passed every assertion.
The standard Swift Testing file remains unchanged. These checks did not
modify the installed provider app or emit GUI input.

Evidence is under
`/datadrive2/benchmarks/macagentbench-20261021/evidence/`:

- `hcu-capture-permission-before.log` records the failing baseline assertions.
- `hcu-capture-permission-build.log` records the native candidate compilation.
- `hcu-capture-permission-after.log` records the passing candidate assertions.
- `capture-permissions-main.swift` contains the generated assertion driver.

`git diff --check` passed. The document checker found no hard-rule violations.

The integrated r2 bundle was built and installed in the macOS 26.3 VM.
Before granting access to its new signed bundle, an actual GUI screenshot
returned `permission_denied` with `input_emitted:false` and instructions to
restore access. The r1 call in the same situation returned `runtime_error`
and a repeated screenshot hint. Evidence is `hcu-r2-initial-gui.json` and
`hcu-initial-gui.json` in the directory above. This checks a real operating-system
denial through the Swift provider and public Go response together.
