# AX text-field tolerance candidate

Status: `merged`
Paths refer to the layout before the package split (a4006c5).
Created: 2026-10-20
Candidate: `ax-tolerance-20261020`
Baseline: main `8fe0c1e`, package VERSION 1.0.0, evaluation image
`osw-hybrid:hcu-ax-main-20261020-a1`. The evaluator's v0.14 namespace is separate.
Source: `/datadrive2/repos/hcu-next-tolerance-20261020`.
No image or evaluation run has been created for this candidate.

## Change and reason

Historical error-recovery tests cite four calls that exchanged the text and
value field names. The decoder now accepts `type.value` as `type.text`, and
non-empty `set_value.text` as `set_value.value`. The action determines the
meaning, so no destination or input value is guessed. Both fields together
remain invalid, even if their values match. Empty set_value.text remains
invalid; callers can clear an element with canonical `value:""`.

The normalizer copies the argument map and leaves recorded caller input
unchanged. Existing type, ref, text-size, whole-batch, and selection checks
run after normalization. This applies to all applications.

Files: `computer-use/ax_text_alias.go`, `ax_public_request.go`, alias tests,
and `docs/MCP.md`. The provider protocol and execution paths are unchanged.

## Model-contract review

The schema already exposes both fields for different action types and stays
unchanged. MCP.md documents accepted aliases. Server instructions, parameter
descriptions, base policy, and hybrid guide keep teaching canonical names;
aliases are recovery tolerance and add no new operation or usage burden.
Public tools remain AX and GUI. No input is sent for conflicting aliases.

App-based act routing and ignored apps scoping fields are excluded. Original
`ax_act_app.go` lines 26 to 35 accept any numeric app value when a ref exists,
then return before checking inventory. That implementation can ignore a
contradictory PID. Silently dropping apps scope also broadens a scoped read.
Both need separate contract decisions and validation.

## Validation and disposition

Tests cover literal Unicode preservation, wrong types, both names, empty
values, missing refs, select field rejection, text limits, unchanged caller
maps, and late invalid steps rejecting before earlier input. A public-call
test verifies literal text reaches the expected provider window.
Go vet/tests, 593 provider tests, and 344 Python tests (29 skipped) pass.
Logs: `/tmp/hcu-next-tolerance-{go,runtime,pytest}.log`.
No real desktop smoke or candidate model evaluation has run.

The parent audit of current-main rounds `hcu-paper-ax-main-panel20-20261020T0335Z-r1`
and `-r2` found zero type.value or set_value.text uses among 633 AX calls.
The same panel cannot measure an alias benefit when aliases are unused.
Retain the candidate with deterministic evidence; do not claim lower cost
or error rates. Lack of panel usage does not establish that tolerance has
no general product value. No main promotion is requested by this record.
