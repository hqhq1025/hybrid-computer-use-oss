# Preserve unsent ordered-input evidence

Status: `testing`
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `macos-ordered-unsent-evidence-20261021`
Baseline: native candidate r2, package version `1.0.0`.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; native macOS provider deployment.

## Problem and evidence

The Darwin transport reports `requestSent=false` when a socket write fails
before sending any bytes. Ordinary AX and GUI paths retain that evidence.
The ordered-keyboard path instead set `partial_input_possible` and
`mutation_unknown` to true for every transport error.
This discarded a known no-input result and gave callers an uncertain outcome.

The new ordered-input regression failed against candidate r2 for a typed
zero-byte write failure. The baseline reported an uncertain, partially
executed sequence. A separate closed `net.Pipe` test verifies that the
Darwin transport produces the typed unsent error from a real failed write.

## Change and model contract

`computer-use/ax_batch_ordered_input.go` retains the no-input evidence only
when a typed transport error states that the request was not sent.
It preserves that fact when constructing the whole-batch summary and marks
action 0 as unattempted. Partial writes, response loss, timeouts, and
unclassified errors remain uncertain. A failed segment does not erase
input emitted by an earlier action. No request is replayed.

The existing public fields and error codes remain unchanged. The no-input
error message now states that no bytes reached the provider. AX/GUI schemas,
parameter descriptions, server instructions, and evaluated-agent instructions
need no update because the change corrects evidence within their existing
contracts. Developer documentation already distinguishes known no-input
failures from uncertain input. No application-specific behavior is added.

## Validation and disposition

Tests cover a zero-byte failed write, partial write, lost response, timeout,
unclassified error, and an unsent keyboard segment after a successful click.
They check public counts and input evidence and verify that the backend
receives no repeated request. The Darwin transport test closes the peer
before writing and needs no timing sleep.

The focused Darwin and ordered-input suites passed with the Go race detector:
`go test -race -tags darwin_sidecar_test -run 'TestDarwin|TestOrdered|TestAX.*Ordered|TestUnsentOrdered' .`.
The coordinating agent runs the combined `make test` and native r3 checks.
This change creates no evaluation attempt, image, or formal release.
No latency, token, or score improvement is claimed.

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
