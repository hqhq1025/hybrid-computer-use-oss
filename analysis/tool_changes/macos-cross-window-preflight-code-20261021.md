# Classify cross-window AX batches as invalid arguments

Status: `testing`
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `macos-cross-window-preflight-code-20261021`
Baseline: native candidate r4, package version `1.0.0`; diagnostic attempt r4b.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; the candidate uses a native macOS provider.

## Problem and evidence

The native safety canary combined refs from two TextEdit windows in one AX
batch. The core rejected the batch before input, but returned `runtime_error`
and a hint to list apps. The caller needed to split the batch by window.
Evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r4b/mcp.json`.

`deriveAXBatchContext` returned an untyped error for an ownership conflict.
`axBatchPreflightError` recognized ref lifecycle errors but did not classify
the ownership conflict as an invalid argument. Public rendering therefore
used its default runtime error code.

## Change and contract review

`computer-use/ax_batch.go` returns the existing typed `axArgError` for
cross-window batch ownership conflicts. The preflight error renderer retains
the type and exposes `invalid_argument`, the conflicting field's path, and
a hint to split the actions by window. A conflicting explicit `window_ref`
gets a hint to omit it or use refs belonging to that window.

The window-binding checks still run before any provider call. Ref lifecycle
errors retain their existing classification. The fix changes neither
which batches are accepted nor which inputs are emitted.

Public schemas, parameter descriptions, error-code names, server instructions,
and evaluated-agent instructions need no updates. They already require one
window per batch and define `invalid_argument` as a caller correction.
The visible error text changes only for a verified ownership conflict.
The implementation adds no application-specific or benchmark-specific branch.
The Go wrapper can carry this fix without rebuilding the Swift provider.

## Validation and disposition

Three public-call regressions reproduced the baseline classification error:
two element refs, an element ref plus a window action, and an explicit
window binding that conflicts with its element ref. The fixed tests check
the code, field path, recovery hint, zero completed actions, requested count,
no input, and zero provider calls. A separate unknown-ref regression checks
that a ref never issued in the session still reports `stale_ref`.

Focused cross-window, lifecycle, and argument tests passed with the Go race
detector. The coordinator runs final combined checks and the native r5
canary. This change creates no formal release or evaluation score.

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
