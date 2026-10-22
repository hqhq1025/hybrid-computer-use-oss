# Python code runtime interoperability

Status: `merged`
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `code-runtime-adapter-20261021`
Baseline: main `125189a`, package version 1.0.0.
Baseline diagnostic image: `osw-hybrid:sdk-cli-code-mode-20261020-a1`.
Source: `/datadrive2/repos/hcu-code-runtime-adapter-20261021`.
No new formal release or model evaluation is assigned to this change.

## Evidence

The completed Astra CLI and SDK comparison used 20 identical tasks per arm.
Both arms scored 20/20 in the official evaluator, without task truncation.
The SDK arm exposed 31 Python interface errors: 18 calls using
`observe(window_ref=...)`, two calls without positional `target`, and 11
find calls using published query names. The model read the wire schema and
then applied those names to the Python helpers. All errors had a later
successful HCU result; that does not prove each original operation recovered.

The report and exact request evidence are at:
`/datadrive2/hcu-eval/sdk-cli-panel20-20261020/trajectory-audit/summary.md`.
Those runs precede this change. The 31 errors are not a measured saving of
31 model responses. Source images were not exported in the full panel;
image delivery was verified from requests, not by comparing source bytes.

OpenGCUA's persistent Python worker can cancel outstanding asyncio tasks
between cells. Controlled probes found that a cancelled HCU executor thread
could still emit input after the next cell began. A second probe used the
real SocketTransport with a socketpair: a cancelled call waiting for its
transport lock still sent after the earlier call completed. No native
desktop or model was used in these cancellation probes. Evidence is at:
`/datadrive2/hcu-eval/code-runtime-adapter-20261021/`.

The upstream CodeExecutor also printed HcuError as a string without the
complete partial-input record. Catching that exception in model code hid it
entirely from the host's automatic error events. Integration probes use the
upstream runtime from OpenGCUA `ae34bba`; its bootstrap is a private hook.

## Scope and contract

The Python SDK accepts named `window_ref` or `app` for observe/find, alongside
the existing positional target. It rejects conflicting target forms and
duplicate shorthand/canonical filters before transport. Published find
field names pass through unchanged. Unknown fields reach core validation.
Cursor calls no longer silently discard accompanying filters or targets;
zero limits reach the core instead of becoming the default limit.

AsyncDesktop queues calls before submitting transport work. A cancelled
queued call is not submitted. Once work starts, cancellation waits for the
transport to settle, then raises CallCancelled with the available record and
input facts. The synchronous path holds the executor Future directly, so an
upstream all-tasks cancellation cannot discard the thread through an internal
asyncio Task. Shared SocketTransport calls check cancellation after acquiring
the socket lock. Retries retain unknown input from an earlier failed attempt.

CodeDesktop uses synchronous host callbacks for explicit text, record, and
image output. It automatically publishes complete errors before raising;
caught errors therefore remain visible. A failed sink is attached as
output_error without replacing the original exception. Image delivery reads
an existing result through an explicit bounded reader and never repeats
input. The OpenGCUA example reuses upstream CodeExecutor private hooks, checks
Python 3.11+, preserves HCU pixel coordinates, and requires a fresh executor
and session instead of reusing unsupported upstream task/context resets.

These changes preserve daemon-instance/replay-only restrictions. No code cell
is an atomic transaction. Local transport completion does not prove that a
disconnected remote daemon has stopped input. A custom async transport must
settle its own background work before returning or raising. The adapter does
not provide a security sandbox or crash recovery.

MCP schemas, AX/GUI behavior, daemon protocol, CLI, and provider code are
unchanged. The new adapters belong to the SDK and host integration, not a
new public desktop tool. The developer guide documents helper signatures,
explicit image delivery, and the SDK's existing observe-none default.
Existing benchmark instructions and frozen images are unchanged; they do
not load the new adapter. Any later model test needs its own run and image.

This is a general interface and lifecycle correction, with no task-specific
application rules. No cost, latency, or score improvement is claimed.

## Validation and disposition

The named discovery helpers passed a real-daemon/fake-desktop test, including
canonical find names, alias equivalence, conflicts, core error propagation,
and async usage. Nine cancellation regressions and 13 output adapter tests
passed. Independent review used the same all-tasks probe on the baseline and
candidate to confirm the old late input and the corrected ordering. A second
negative comparison confirmed that sink failure no longer replaces cancelled
or effect-unknown exceptions.

The existing socket concurrency test previously required eight executor
threads inside one AsyncDesktop. That contradicts the new per-instance queue.
It now uses eight AsyncDesktop instances sharing one SocketTransport, retains
the eight-thread barrier, and verifies the same response ownership property.
The full transport suite passed after this fixture correction.

The real upstream CodeExecutor and product bootstrap passed four controlled
cells, including persistent variables, image events, caught partial errors,
and cancelled input evidence. The real Linux Docker adapter passed six checks:
canonical fields, a two-step Unicode AX batch, independent GTK readback,
silent success, byte-identical explicit image output, and caught stale-ref
evidence. The first combined upstream/desktop attempt stopped before any cell
because the old image uses Python 3.10. A separate read-only CPython 3.11.13
and Pillow 11.3.0 overlay then passed the full real upstream/desktop path.
Three cells verified persistent state, real AX editing with independent GTK
readback, a 1365 by 768 image event, and caught error evidence. All cells
returned zero without timeout or reset; both diagnostic containers were
removed. Evidence is in `desktop-smoke/summary.md` under the change evidence
directory above.

Final `make test` passed: Go format/vet/tests, 607 provider tests, and 360
harness tests with 28 skips. The harness used the existing OSWorld dependency
overlay at `/tmp/hcu-native-test-osworld` and the osw-hybrid virtual environment
site packages with system Python. The first full attempt exposed the barrier
fixture mismatch described above; it was corrected without weakening wire
concurrency. Focused socket and cancellation tests then passed before the
complete rerun. Document checks report no hard issues; diff checks pass.

Disposition: ready for source integration after CI. No frozen evaluation
image, formal product release, or historical run result changes. Improvements
in model efficiency remain unmeasured after this patch.
