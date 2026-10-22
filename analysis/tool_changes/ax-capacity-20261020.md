# AX action capacity candidate

Status: `merged`
Paths refer to the layout before the package split (a4006c5).
Created: 2026-10-20
Candidate: `ax-capacity-20261020`
Baseline: main `8fe0c1e`, package VERSION 1.0.0, evaluation image
`osw-hybrid:hcu-ax-main-20261020-a1`. The evaluator's v0.14 namespace is separate.
Source: `/datadrive2/repos/hcu-next-capacity-20261020`.
No image or evaluation run has been created for this candidate.

## Change and reason

The historical error-recovery branch reported cell-entry batches needing
27 to 40 steps. This candidate accepts 64 actions instead of 32. It also
rejects batches whose declared wait timeouts total more than 60000 ms.
An omitted wait timeout counts as 5000 ms. The wait budget does not bound
action execution time or total tool latency.

The decoder validates the entire batch before input. Existing text budgets,
window ownership, live ref validation, and strict selection readback remain.
The rule applies to any application and does not encode task recipes.

Files: `computer-use/tool_contract.go`, `ax_public_request.go`, capacity and
argument/schema tests, the contract fixture, and `docs/MCP.md`.

## Model-contract review

AX and GUI remain the only tools. The AX actions schema exposes maxItems=64
and describes the total wait budget. The new rejection explains how to split
the call. MCP.md documents the same limits. Server instructions, base policy,
and hybrid guide describe general batching without numerical limits, so they
need no change. The provider protocol is unchanged.

## Validation and disposition

Boundary tests cover 64 versus 65 inputs, 12 versus 13 default waits, and
60000 versus 60002 ms of explicit waits. A public-dispatch regression proves
an invalid late wait rejects the batch before an earlier keypress.
A public-call mock executes all 64 steps in the original window.
Go vet/tests, 593 provider tests, and 344 Python tests (29 skipped) pass.
Logs: `/tmp/hcu-next-capacity-{go,runtime,pytest}.log`.
No real desktop smoke or candidate model evaluation has run.

The two current-main rounds contain 340 AX act calls. Their largest batch
has 7 steps; counts for sizes 1 to 7 are 189, 111, 26, 9, 1, 3, and 1.
These parent-audited runs are
`hcu-paper-ax-main-panel20-20261020T0335Z-r1` and `-r2`.
The panel does not exercise the larger limit, so another pair of these rounds
cannot establish a capacity benefit. Keep this candidate for a targeted long
batch desktop check before adoption. No performance gain or main promotion
is claimed. Larger batches can delay feedback and expose more partial work.
