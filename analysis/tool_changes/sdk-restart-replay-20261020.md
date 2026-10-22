# Bind SDK retries to their daemon instance

Status: `merged`
Created: 2026-10-20
Change ID: `sdk-restart-replay-20261020`
Baseline: main `629e777`, package version 1.0.0.
Baseline Linux image: `osw-hybrid:ax-bugfix-main-20261020-a1`.
Source: `/datadrive2/repos/hcu-sdk-restart-eval-20261020`.
Candidate image and evaluation attempts will use separate recorded identities.

## Problem and mechanism

The SDK retries input after a dropped reply using the same request ID. A
daemon restart loses the in-memory replay cache, so the new daemon could send
the same input again. A PID cannot identify the daemon instance reliably.

Each daemon now generates a random instance ID and returns it in hello. The
SDK binds the first input call to that instance. Reconnection, comparison,
and sending stay under one transport lock. A changed instance stops the
retry before sending input; the original effect remains unknown. The daemon
also rejects a mismatched instance binding before desktop execution.

Input replay requires a nonblank string request ID. Legacy daemons without
an instance ID and custom transports without an explicit replay guarantee
do not automatically retry uncertain input. Reads can still retry. The CLI
does not automatically reconnect and resend, so its send path needs no fix.

Replay entries also expire or leave the bounded cache. An automatic retry
must only recover an existing entry, including an in-flight result, rather
than create a new execution after eviction. The final implementation and
tests cover this boundary together with daemon replacement.

## Model-contract and compatibility review

`instance_id` belongs to hello; retry bindings belong to the socket call
envelope, outside AX/GUI arguments. Public tools, schemas, server instructions,
and evaluated-agent guides remain unchanged. `docs/CODE.md` and SDK docstrings
describe caller-visible retry behavior. No benchmark-specific mechanism.

Normal calls through existing custom transports remain supported. A custom
transport may mark a dropped call safe to retry only when its own mechanism
prevents repeated input. The new unknown-effect exception keeps
`input_emitted=None`; it does not claim that the original input did not run.
This change adds no durable replay log or crash recovery guarantee.

## Validation and disposition

Real Unix-socket proxy tests drop a completed reply and replace the daemon
at the same socket in the same PID. They count provider inputs and forwarded
mutations independently of SDK exceptions. Sync and async paths cover same
instance, replacement, reads, legacy hello, and absent/invalid request IDs.
Twenty sync/async scenarios passed through actual Go daemons and the proxy.
Same-instance reply loss produced one input and a replayed result. Replacement
produced one input in the old daemon, zero in the new daemon, and zero forwarded
retry mutations. Expired and evicted results stopped without more input.
When the first call never reached the daemon, the retry also stopped rather
than creating a new execution. Tests cover invalid instance bindings and
preserve in-flight entries during pruning. Focused race checks passed.

The cache check and lookup share the request lock. Automatic retries carry
`replay_only` outside AX/GUI arguments. A missing entry returns an unknown
effect error, including when a failed first send never reached the server.
This conservatism trades some automatic recovery for no new retry execution.
Full `make test` passed: Go format/vet/tests, 607 provider tests, and 362
Python tests with 26 skips using the existing isolated OSWorld overlay.
The complete Go transport race suite passed. Independent review covered the
final cache guard and found no blocker. Logs are under
`/datadrive2/hcu-eval/sdk-restart-replay-20261020/`.

The code-mode comparison candidate is `sdk-cli-code-mode-20261020-a1`.
Both CLI and SDK arms use the same frozen source and image. Their operator
records live under `/datadrive2/hcu-eval/sdk-cli-panel20-20261020/` and require
actual code-mode and image-delivery canaries before model task evaluation.

The user also requested two 20-task CLI/SDK evaluation arms and code-mode
compatibility analysis. Those attempts use separate manifests and do not
change the meaning of the transport regression tests.
