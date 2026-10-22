# Useful feature integration

Status: `merged`
Created: 2026-10-20
Change ID: `useful-features-20261020`
Baseline: main `8fe0c1e`, package VERSION 1.0.0.
Baseline image: `osw-hybrid:hcu-ax-main-20261020-a1`.
Source: `/datadrive2/repos/hcu-useful-main-20261020`.
Executable: `artifacts/computer-use` in that checkout.
This integration does not assign a new product release or replace an image.

## Selection

The user authorized merging useful features after reviewing their evidence.
The user also requested guidance that encourages AX, GUI, and CLI batching.

| Change | Reason for inclusion | Evidence limit |
| --- | --- | --- |
| 64 AX actions and a 60-second sum of wait timeouts | Existing candidate traces attempted 43 and 36 actions, rejected by the old 32-action limit | No independent model panel for the larger limit |
| Text field aliases | The action unambiguously identifies literal text versus a value; conflicting fields still fail before input | No aliases occurred in the new-main panel |
| Intermediate window-root reads | Avoids full reads between eligible untargeted inputs without removing the final full read | An eight-node GTK fixture saved about 12 ms per eight-action batch |
| Find text excerpts and compact removed-node diffs | Expose otherwise hidden match text and remove redundant deleted-node metadata | Old trim panel results measure a larger bundle |
| Batching guidance | Explain when known actions and shell commands can share a call | Changed model guidance requires fresh evaluation |

The existing `ax-capacity-20261020`, `ax-tolerance-20261020`, and
`batch-20261020` records preserve candidate evidence. This integration's final
disposition supersedes their earlier decisions to defer a main merge.

Actobs and suppression of an explicitly requested full tree remain excluded.
Actobs did not reduce follow-up observations in the two-round panel.
Full suppression changes when the caller receives requested information.
Recent-document refs, popup continuation, and GUI zoom have reproducible
identity or coordinate counterexamples. They remain excluded.
Budget reminders and app/cross-window routing need separate validation.

## Model-contract review

Public tools remain AX and GUI. The AX schema changes its action limit to 64
and documents the sum of wait timeouts. Alias handling preserves canonical
parameter names, caller arguments, text limits, and conflicting-field errors.
Find and diff text changes are documented in MCP.md and the extracted change
record. Explicit full requests still return their full observation immediately.

`instructions/base.md` adds a general rule: batch known actions until a
decision requires new evidence. It also requires checking exit status before
dependent shell commands and limits concurrency to independent commands.
`instructions/hybrid.md` clarifies AX focus/ref validity and GUI batching from
one screenshot, with observation boundaries after window/layout transitions.
AX-only and GUI-only guides already state those channel-specific boundaries;
both inherit the new base rule. Server instructions need no separate change.
No task recipes, benchmark knowledge, or application-specific conditions are
added. CLI guidance adds no command wrapper or tool.

## Safety, latency, and compatibility

Whole-batch preflight, original window ownership, live ref resolution,
partial-input evidence, and final observation remain required.
The wait limit bounds declared waits, not total call duration. Larger batches
can delay feedback, so guidance explicitly treats the capacity as a limit,
not a target. Shell commands do not acquire AX-style preflight or rollback;
they must check dependency failures themselves.

Batching can reduce round trips but can also increase work done before a
failure. No combined token, latency, cost, or success-rate gain is claimed.
Previous panels used the old instructions and separate candidates, and do
not measure this integration. No combined model evaluation has run.

## Validation and disposition

Go formatting, vet, full tests, Darwin/Windows cross-vet, and race-enabled
sidecar transport tests pass. All 593 Linux provider tests pass.
Python runner checks pass: 344 passed, 29 skipped, using the established
isolated Python environment and this checkout's `osworld-overlay` on
PYTHONPATH. The skipped integration tests require dependencies not installed
in that environment. The host runtime tests use system Python for GI.
Pointing OSWORLD_V2_ROOT at the overlay alone is invalid: the overlay is not
a complete upstream checkout. An initial check with that setting failed two
upstream-dependent tests; the supported isolated configuration passes.
Logs: `/tmp/hcu-useful-main-{test,runtime,transport,pytest-final}.log`.

Two combined regressions cover a disappearing ref after root-only reads and
partial input at step 40 of a 64-step batch. They preserve the completed
prefix, stop later input, and recover a full observation of the partial state.

Operator-only GTK smoke passes on the combined binary. A 64-action call
completed in 7.081 seconds with 62 root reads and two full reads; text and
cursor returned to their starting state. Calls with 65 actions, excessive
wait budgets, or conflicting aliases emitted no input. Both aliases produced
the expected text. A two-action GUI batch returned one final screenshot.
Role-only find returned a clipped text excerpt and a copyable ref.
These checks used fresh network-none containers, now removed, and no model.
Evidence: `/datadrive2/hcu-eval/useful-main-smoke-20261020/summary.json`.

The user subsequently authorized two-round comparisons of latest main,
historical 1.0, and v0.14. The plan and exact baseline identities are in
`/datadrive2/hcu-eval/useful-main-eval-20261020/plan.md`.
Those evaluations have not started. Old model results are not combined gains.
No formal release is assigned by merging source to main.

Disposition: selected for main under the user's explicit authorization.
The source merge adopts the verified functions; model performance remains
under testing. Evaluation image: `osw-hybrid:useful-main-20261020-a1`.
Build inputs: `/datadrive2/hcu-eval/paper-harness/build/useful-main-20261020-a1`.
