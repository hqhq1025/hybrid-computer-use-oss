# AX correctness and selection integration

Status: `merged`
Created: 2026-10-20

Baseline: main development snapshot after gui-scale-generic-instructions-20261020;
package VERSION 1.0.0, evaluator baseline namespace v0.14. No formal release
promotion is implied. Evaluation base image: osw-hybrid:hcu-oss-1.0-9de13e7.
Source: /datadrive2/repos/hybrid-computer-use-oss-ax-main.
Executable: artifacts/computer-use; evaluation image will use a fresh explicit tag.

## Scope and mechanism

The user requested five selected changes and two concurrent 20-task rounds:

1. Reissued refs require a new tree even when provider state is unchanged.
   Explicit full reads still return the tree immediately.
2. Find and query waits match rendered roles/states; missing content roots
   produce an explained whole-window read; failed finds/waits show bounded
   current candidates. A failed final window inventory cannot prove absence.
3. Visible drop-down toggles expose the hidden sibling combo's semantics.
4. GTK2 menu visibility uses the menu shell selection. Qt Toggle-only clicks
   use pointer delivery only after front-window, geometry, and hit-test checks.
   Action effects distinguish target flags from changes elsewhere in the window.
5. AX select supports text, item, and cell selection with exact readback.

Excluded: transient-item ref rebinding, parameter aliases, 64-step cap,
cross-window mutation extension, full-read suppression, zoom, query clicks,
and screenshot opt-out. These exclusions preserve the requested subset.

## Integration corrections

The original select implementation could return success for shifted or
oversized spans and checked cell count without checking selected cell identity.
Integration now rejects mismatched spans, extra selected paragraphs, and wrong
cells with effect_unknown. It validates same-table endpoints before input and
preserves possible partial input after API, caret, or clear-selection attempts.
Fifteen new deterministic boundary tests cover these conditions.

The original window-timeout change could present old inventory after a later
read failed. It now reports observation_incomplete with no stale candidates.

## Model-contract review

Public tools remain AX and GUI. AX adds select and to_ref, with schema fixture
and provider protocol updated together. Server instructions need no separate
lifecycle change. GUI and its coordinate mapping remain the current baseline.
MCP.md, base-preserving hybrid instructions, AX-only delivery guidance, and
CHANGELOG describe selection, scope, suggestions, and actual input routes.
All guidance is general-purpose, without evaluator checks or task recipes.

## Evidence and validation

The earlier candidate panels are separate evidence, not measurements of this
subset: axerr scored 18/20 with one truncation; axcap 20/20; axdeliv 19/20;
trim 19/20. Their runs are under /datadrive2/results with prefixes
hcu-paper-full-{axerr,axcap,axdeliv,trim}-panel20-r1-. Exact selected IDs and
configuration remain in those run manifests.

Focused Find/Wait/Scope/Projection Go tests passed. Focused selection tests
passed (26 including 15 new boundary cases). Combined validation passed:
Go formatting/vet/tests, darwin and windows Go vet, both sidecar test tags,
593 Linux provider tests, 344 Python tests (29 skipped), Linux build, and
MCP smoke exposing exactly AX and GUI. Python uses the isolated declared
dependencies and this checkout's osworld-overlay as documented in the prior
integration record. Logs on cpu001: /tmp/hcu-ax-main-{go-final,runtime,pytest,transports}.log.

Exact run names and image selection are recorded in fresh run manifests under
/datadrive2/results/hcu-paper-ax-main-panel20-20261020*, with supervisor and
comparison evidence in the sibling comparison directory.

## Evaluation and disposition

The user authorized merging this subset, then running two rounds concurrently,
20 tasks each (40 total maximum). Use fresh run roots, distinct proxy ports,
100 successful model responses per task, GPT-5.4 xhigh, official/original
evaluation, no Judge, and the existing 5400-second comparison timeout.
The exact task IDs are copied into each run manifest; no content hashes gate
admission. New records use candidate IDs, tags, version, and evidence paths.

The old wrapper deletes all containers sharing an image after one round ends;
it cannot safely run these rounds concurrently. The new run supervisor under
/datadrive2/hcu-eval/paper-harness/ax-main-20261020 uses only runner-owned
task cleanup and Popen-owned proxy processes. Model retries remain bounded by
the existing gateway. Concurrent load may alter rate-limit waits and wall time.

Remaining limits: native macOS/Windows selection is not newly implemented;
Linux desktop success and benchmark success require live validation. The
combined run measures this selected subset, not the original complete branches.
Status remains testing; no product release number is assigned.
