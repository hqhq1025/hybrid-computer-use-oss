# Retry rejected macOS final capture samples within the settle budget

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `macos-final-capture-layout-retry-20261021`
Baseline: package version `1.0.0`, unreleased native candidate r11.
Baseline artifact: `hcu-macos-mab-20261022-r11`.
Baseline prepared image: `multiapp5-r11-rebuilt-20261022`.
Source: `/datadrive2/repos/hcu-macos-observation-fix-20261021`.
Candidate: `hcu-macos-mab-20261021-r12`; r11 remains unchanged.

## Problem and evidence

The final GUI observation failed when desktop layout changed during capture,
even though the caller requested time for the desktop to settle. Every capture
exception escaped the stillness sampler. The first capture could fail before
the sampler established its settle deadline.

The r11 comparison trace records eight such failures after input dispatch.
Each failure reports all requested input actions as dispatched. Each following
pure screenshot succeeds. All eight failed calls requested `settle_ms=1000`.
The trace is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/multiapp5-r11-20261022-a3/02-hcu/agent.jsonl`,
lines 36, 40, 44, 48, 52, 56, 70, and 74.
This evidence identifies a capture failure after input; it does not prove which
window moved or that the task outcome was correct.

## Mechanism and scope

`DesktopRuntime.swift` gives the existing before/after layout mismatch a
distinct internal error type. The layout comparison itself is unchanged.
`GUIStillness.swift` discards only samples rejected with that type. A discarded
sample resets consecutive stability evidence. Subsequent capture attempts use
the same settle deadline. No input action runs again.

The existing timing contract remains one initial capture attempt followed by
`settle_ms`. The sampler establishes that deadline after the first attempt,
whether the attempt succeeds or encounters a layout mismatch. Captures remain
100 ms apart. Four identical valid samples spanning 300 ms establish stillness.
The sampler still takes one final capture when sleep reaches the deadline.
A zero settle budget permits only one capture attempt.

An in-flight capture retains the existing bounded native timeout and can finish
after the settle deadline. A failed capture that reaches the deadline ends the
operation. Permission, provider reset, encoding, and other errors still fail
immediately. Matching error text alone never triggers another attempt.

The provider returns only an image with its own matching layout. It does not
return a previously valid image after final capture failure. Exhaustion retains
the original screenshot failure, input dispatch counts, and uncertainty flags.
Coordinate checks before input and full before/after capture layout checks
remain intact. The mechanism uses no application names or task identifiers.

## Contract review

The MCP schema, parameter descriptions, server instructions, developer API
documentation, and evaluated-agent instructions require no interface change.
The caller already requests a bounded final observation through `settle_ms`.
The correction makes that existing budget usable after a transient capture
mismatch. The public failure text remains unchanged after exhaustion.
Successful capture still does not prove an input effect or task completion.

The provider may spend more of the requested settle budget taking screenshots
instead of failing on its first mismatch. No measured latency, cost, token, or
benchmark improvement is claimed. There is no new dependency, retry option,
native timeout, public tool, or persisted state.

## Validation and disposition

Focused tests in `GUIStillnessTimingTests.swift` cover initial and later layout
changes, stable-sequence reset, repeated failure under one deadline, zero
budget, deadline overrun, final capture recovery, and non-retryable errors.
`GUIStillnessTests.swift` checks matching image/layout delivery and unchanged
input evidence after recovery or exhaustion. Existing timing, reset, pixel,
and GUI safety tests remain part of native validation.

Native production compilation and all 21 standalone sampling tests passed on
an isolated macOS clone, including the eight new regression tests. The adapter
links production sources; this is not a full SwiftPM test run because the
installed CLT lacks Swift Testing. Initial test type-inference failures were
fixed with explicit closure return types; the assertions were not changed.

After normal macOS permission grants for the new installation, native apps,
observe, screenshot, and a keyboard action with final screenshot passed.
A real GPT-5.5 xhigh Codex session successfully called AX apps and returned
READY. These are diagnostic checks, not a benchmark rerun or a measured
reduction in tool failures. Evidence is under
`/datadrive2/benchmarks/macagentbench-20261021/evidence/mab-20261021-observation-fix-native-a1/`,
in `test-retry2-result.json`, `native-canary-authorized/report.json`, and
`codex-canary-authorized/result.json`.

The combined candidate passed Go tests, vet, formatting, 609 provider tests,
and the full pytest suite in the existing virtual environment: 364 passed,
32 skipped. Logs are preserved in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/r12-local-checks/`.
No benchmark attempt uses this change. The completed r11 comparison retains
its frozen implementation and separate supplemental environment record.
This change remains an unreleased, validated candidate.
