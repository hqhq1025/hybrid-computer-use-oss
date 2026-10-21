# Wait for corroborated window closure after a failed final snapshot

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `macos-close-lifecycle-settle-20261022`
Baseline: native candidate r7, package version `1.0.0`.
Candidate: r8, build attempt `native-build-r8-a1`.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; the candidate uses a native macOS provider.

## Problem and evidence

The r7 safety canary passed six checks but failed its final close check.
The close emitted input against the confirmed temporary TextEdit window.
Its final target snapshot failed with `AXError.cannotComplete` (`-25204`).
The subsequent independent liveness check read AX successfully and found the
target absent. WindowServer still listed the exact target handle. The native
provider therefore correctly returned unknown; a later read of the sibling
window did not prove the close.

The evidence root is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r7-a1/`.
`report.json` retains the failed canary. `mcp.json` contains close request 24,
targeting `w13`. `native-window-log.json` contains native request
`darwin-1792616668272364000-21` with:

- `native_comparable=true`, after native identity registration;
- `ax_complete=true`, `ax_state=absent`, no unknown AX identities;
- `cg_state=present`, one matching WindowServer handle;
- the resulting liveness `unknown`.

The close path sampled corroborated liveness only once. AX and WindowServer
can remove a closing window at different times. The observed disagreement
motivates a bounded resample, but does not by itself prove a transient delay.
The r8 native canary must show whether both inventories eventually agree.
The r7 failure remains valid evidence and is not reclassified as a pass.

## Implementation and contract review

`ComputerUseRuntimeWindows.swift` adds `validateClosingWindow`. Only the
existing recovery after a failed close snapshot uses it. The helper resamples
the existing `validateBoundWindowLiveness` within a 0.6-second monotonic deadline.
It uses `ProcessInfo.systemUptime`, counts read time against the budget, and
uses the existing AX wake mechanism between reads. Without a wake observer,
the caller uses the existing thread-delay mechanism.

Each read validates the same process identity before and after the existing
AX/WindowServer check. A typed stale identity still carries the absence proof.
The helper immediately propagates any error; it never retries permissions,
provider reset, or process-identity errors. A normal return at the deadline
leaves closure unproven. The existing caller returns `effect_unknown`.

The deadline prevents new waits or reads after expiry. It cannot interrupt
an already-running native read, which can finish after the deadline. This is
not a strict 0.6-second total-call limit. Successful immediate absence adds no
wait. Previously unresolved closure can add verification latency; no latency
or token gain is claimed.

The change preserves native provenance and the AX/WindowServer agreement
requirement. AX absent with WindowServer present is never sufficient.
It does not weaken global partial-inventory handling, reinterpret a sibling
snapshot, use destruction notifications as substitute evidence, alter AX
timeouts, or move work between threads. No application-specific branch or
dependency is added.

Server instructions, tool schemas and parameter descriptions, developer API
documentation, and agent interaction guides already describe verified closure
and uncertain action effects. Their observable evidence contract does not
change; they need no text or schema updates. This operator change record
documents the additional bounded verification delay. No public option is added.

## Validation and disposition

`ClosingWindowSettleTests.swift` uses injected clocks and read sequences.
Five test functions cover delayed WindowServer disappearance, persistent
conflict or unknown state, missing native provenance, a target that reappears,
immediate process/provider error propagation, and time spent inside reads.
The tests assert exact target binding and two process checks per liveness sample.
They do not sleep or emit desktop input.

Focused Go close regressions passed with the race detector. The preceding r7
full Go tests, 609 Linux provider tests, and repository pytest remain applicable
to unchanged Go/Python code. The implementation delta is native Swift only.
Independent code review found no blocker.

The build host lacks Swift Testing. An independent harness in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-close-r8/`
generates assertions from the five test functions and links unchanged production
sources. Python generation and script checks passed on Linux. Native production
build and all five standalone test functions passed. The combined build/test
command exited zero in 132.35 seconds. Logs and the result are in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-build-r8-a1/`.
The standalone assertions are not the SwiftPM full suite.

The r8 bundle was installed separately at
`/Users/pipiwu/mab-tools/hcu-candidate-r8/`. Signature verification and the
wrapper version check succeeded, as recorded in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/rebuilt-tools-20261022/a2/r8-install.json`.
The r8 desktop permission setup remained incomplete, so r8 did not run a desktop
safety canary. The coordinator selected the combined r9 candidate for the next
seven-check canary instead of repeating r8 permission setup. That canary
validated this close change together with
`macos-discard-exited-workspace-processes-20261022`.

The combined r9 canary passed all seven checks. The final close returned
`close_effect_verified=true` and `target_window_absent=true`, and the follow-up
inventory retained the sibling window. The same run also passed background
lookup and text editing, cross-window batch rejection before input, stale-ref
rejection after same-title replacement, GUI batch prevalidation, and stale-pixel
rejection after a window move. Evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r9-a1/report.json`.
This verifies the combined candidate in that desktop scenario. The r7 failure
remains recorded, and no isolated r8 desktop pass or universal close guarantee
is claimed.

The later combined r11 candidate also passed the same seven safety checks,
including exact target absence and sibling preservation. Its evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-safety-r11-a1/report.json`.

This candidate is unreleased. It does not authorize or claim a benchmark score,
promotion, or formal release.
