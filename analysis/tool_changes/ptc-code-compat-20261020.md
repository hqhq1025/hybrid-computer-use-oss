# PTC forwarding and image delivery

Status: `testing`
Created: 2026-10-20
Change ID: `ptc-code-compat-20261020`
Baseline: main source `b34e5a2`, package version 1.0.0.
Baseline Linux image: `osw-hybrid:ax-bugfix-main-20261020-a1`.
Source: `/datadrive2/repos/hcu-ptc-compat-20261020`.
No candidate image, formal release, or model evaluation was created.

## Problem and evidence

The PTC forwarder used `desktop.transport` directly. Raw tool events without
request IDs bypassed SDK replay protection. Repeating a tool_use event sent
another desktop input. The SDK wrapper path did generate IDs, so these entry
points had different guarantees.

The forwarder also returned screenshot paths as JSON while the documentation
said direct GUI calls delivered images. A guest path is not accessible in a
cloud code-execution container. The earlier audit reproduced both issues:
`/datadrive2/hcu-eval/ptc-audit-20261020/`.

## Change and scope

`sdk/python/hcu_sdk/ptc.py` adds a conversation-scoped Forwarder. It retains
each tool event and its outcome, serializes concurrent resends, and refuses
changed event arguments. The first execution uses Desktop.call with an
explicit or generated request ID. SDK daemon-instance and replay-only guards
apply during that call. A repeated event returns retained records without
calling the daemon, including after daemon replacement or cache expiry.

The ledger has a configurable event limit, default 1,000. It refuses new
events at capacity and does not evict outcomes. Interrupted attempts remain
blocked from re-execution. State is in memory only; restarting the harness
does not authorize replay of a pending event. Explicit caller request IDs
retain the daemon's cache semantics across distinct API events.

Direct screenshot results include image blocks when an operator supplies an
image reader. No image path is opened implicitly. The local POSIX reader
restricts access to the selected output directory, rejects symlinks and
nonregular files, bounds reads, and checks PNG/JPEG signatures. Remote
deployments provide their own bounded guest-file reader. Image delivery
failure preserves the core record, marks a delivery error, and never reruns
input. Code callers retain JSON-string results and need direct observation
to show screenshots to the model.

`sdk/python/examples/ptc_messages.py` is a small continuation example. It
preserves assistant content and container identity, answers multiple tool
events, limits rounds, and stops on expired or missing required containers.
At the round limit, an exception retains the completed tool results and
request for resumption with the same Forwarder. It does not automatically
retry API errors or recover a restarted harness.

## Model-contract review

AX/GUI core, public input schemas, SDK transport, and daemon remain unchanged.
PTC definitions still derive from the live schema and expose only hcu_ax and
hcu_gui. Their descriptions now distinguish records paths from image content
and bound request-ID guarantees. `docs/CODE.md` documents lifecycle, images,
errors, and the separate Codex route. No MCP instruction or evaluated-agent
skill changes are needed: this adapter is not injected into existing runs.

The fix is general transport behavior, not a benchmark workflow. It adds no
application recipes or task-specific conditions. It retains complete core
error and partial-input evidence. `ok:true` still does not prove a user task
or visible effect succeeded.

## Validation, cost, and disposition

Tests use a real Go daemon with a fake desktop and controlled Python
transports. New tests cover concurrent duplicate events, capacity/conflicts,
request IDs, guarded retry failures, typed SDK errors, image bytes and failed
image delivery after partial input, path escape/symlinks/FIFO/format/size, and
Messages container continuation. They do not call Anthropic or validate a
live cloud model. The new controlled tests include interrupted events,
caller conflicts, and resumption after the continuation limit. The real-daemon
SDK suite also tests generated-ID replay. Final logs are under
`/datadrive2/hcu-eval/ptc-compat-20261020/`.

All 18 new adapter/example tests passed. Full `make test` passed: Go format,
vet and tests, 607 provider tests, and 360 harness tests with 28 skips. The
harness tests used the existing isolated OSWorld dependency overlay at
`/tmp/hcu-native-test-osworld` and the osw-hybrid virtual environment. Python
compilation and `git diff --check` passed. Independent review covered the
event ledger, image boundaries, interrupted input, continuation resumption,
and optional caller/SDK datetime serialization. No review blockers remain.

No model cost or latency improvement is claimed. The ledger retains records
until the conversation ends; large trees increase its memory use. Repeated
images can be reread without executing the desktop call. Direct image blocks
add actual image tokens; this repairs missing evidence rather than promising
token savings. Cross-process durable event replay remains unsupported.

Disposition: ready for source integration after CI. This change does not
assign a product release or change the frozen CLI/SDK evaluation image.
