# Preserve verified native close evidence

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `macos-close-lifecycle-evidence-20261021`
Baseline: native candidate r5, package version `1.0.0`.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; the candidate uses a native macOS provider.

## Problem and evidence

The r5 native safety canary closed one TextEdit window and kept its sibling.
The native provider independently verified that the exact target no longer
existed. Its successful response included `windowAbsent` and
`closedWindowHandle`. Go decoded these fields but rendered the response as
an empty accessibility snapshot. Batch recovery then read the closed window
again. That read failed and replaced the verified close with
`effect_unknown` and `mutation_unknown`.

The contradictory public result reported success, one completed action,
emitted input, and an unknown effect. Evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r5/mcp.json`,
request ID 20. The subsequent apps listing confirmed the target was absent.

## Change and contract review

`computer-use/ax_action_result.go` recognizes the successful native close
response before generic snapshot rendering. The response must report absence
and identify the exact window bound by the request. If both request and
semantic target carry a window handle, those handles must agree. Failed
native actions cannot enter this path.

The core uses the existing close lifecycle observation and attaches actual
action-emission evidence. `computer-use/ax_batch_observation.go` accepts that
proof without another read of the removed window. Existing recovery rules
still require complete lifecycle evidence when no native proof exists.

For `observe=diff`, the final observation reports the target window absent.
For `observe=none`, the core retains the close proof without a final tree
or screenshot. For `observe=screenshot`, the core reports that the closed
window has no available screenshot and retains the observation requirement.
The missing screenshot does not invalidate verified close evidence.
`computer-use/ax_evidence.go` preserves explicit `screenshot_returned=false`
in public responses. Actual action failures and partial-input evidence retain
their existing handling.

The server instructions, schemas, parameter descriptions, and evaluated-agent
skill need no changes. The existing contract distinguishes window lifecycle
evidence from screenshot evidence. This record documents the implementation
correction; public error names and tool arguments do not change.

The fix applies to any provider that proves an exact window absent after
close. It adds no application or benchmark conditions. It removes one
unnecessary provider observation on this path; latency and token gains have
not been measured. It adds no dependencies or external calls.

## Validation and disposition

`computer-use/ax_native_close_evidence_test.go` reproduced the r5 failure
before the fix. The public-call tests cover diff, none, and screenshot output,
actual input evidence, batch progress, named closed-window reporting, and
zero redundant observations. Negative tests cover a different window handle,
a missing handle, a window without absence evidence, and a failed native
close. They cannot acquire verified-close status.

Focused native close, existing close lifecycle, and AX evidence tests passed
with the Go race detector. The coordinator owns combined checks and the r6
native canary. This change is an unreleased candidate and creates no formal
benchmark score. Native r6 validation remains pending in this record.

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
