# SDK transport and daemon reference persistence

Status: `merged`
Created: 2026-10-20
Change ID: `sdk-daemon-lifecycle-20261020`
Baseline: remote main `5d3e423`; package version 1.0.0, unchanged.
Baseline Linux image: `osw-hybrid:ax-bugfix-main-20261020-a1`.
Source: `/datadrive2/repos/hcu-sdk-daemon-fixes-20261020`.
No new image, model evaluation, or formal product release.

## Problem and scope

The user selected three defects from the SDK/CLI audit for parallel repair:

- Concurrent SDK calls share a socket and receive buffer without a lock.
  Controlled socket scheduling reproduced swapped responses.
- A rejected hello leaves the SDK socket open. A second call skips the
  handshake. A real Go daemon with a fake desktop reproduced this behavior.
- Attached MCP calls omit the ref-counter save used by ordinary daemon calls.
  After a normal restart, the daemon issues the same old element refs.

Baseline reports and reproducible probes are under
`/datadrive2/hcu-eval/native-current-20261020/sdk-audit/` and `cli-audit/`.

The repair covers the SDK socket lifecycle and both daemon call entry points.
It does not repair unrelated CLI argument parsing, PTC image forwarding,
packaging, or retry deduplication across daemon generations.

## Mechanism and model-contract review

The SDK serializes connection establishment, hello, request transmission,
response reception, and close on one connection. Responses must match the
request ID. Failed handshakes and broken responses clear connection state.
The daemon applies shared locked checks and ref-counter persistence to its
ordinary and attached MCP entry points.

Counter writes use a temporary file and replacement. Write failure withholds
the new refs, preserves available input facts, and blocks later calls on that
daemon. Invalid existing counter files prevent startup. These checks keep a
normal restart from treating unreadable state as an empty ref registry.

The public AX/GUI tools, parameters, server instructions, and evaluated-agent
instructions remain unchanged. These defects belong to local transport and
daemon state. `docs/CODE.md` documents the connection and restart guarantees;
SDK comments describe caller-visible behavior. No benchmark recipes apply.

## Safety and compatibility

One SDK connection executes one exchange at a time; callers can still use
AsyncDesktop. The daemon already serializes desktop calls, so this does not
remove supported simultaneous desktop input. A failed handshake cannot leave
a reusable connection. A malformed or mismatched response must not trigger an
automatic mutation retry.

Same-daemon request-ID replay remains supported. Deduplication state is
process-local; this change does not make retries safe across daemon restart.
Ref persistence checks target normal restart, not crash-safe allocation.
No latency, token, cost, or benchmark success-rate gain is claimed.

## Validation and disposition

Nine Python transport checks passed through the ordinary Go test entry point.
They cover cold and connected eight-way async calls, EOF, bad frames and IDs,
hello protocol/session rejection, event-loop progress during schema calls,
repeated display rejection, and same-daemon lost-reply deduplication.

The baseline SDK failed all seven wire test methods in the recorded negative
control. After a rejected display handshake it sent one provider input; the
fixed version sent zero. Same-daemon lost-reply replay sent one input before
and after the repair. Concurrent tests use socketpair and a start barrier;
the baseline failure is observed, not guaranteed under every thread schedule.

Daemon tests passed all four ordinary/attached-MCP restart combinations.
All three counters advanced; old element refs failed before input. Tests also
passed for environment drift, invalid saved records, withheld refs on write
failure, and blocked subsequent calls. When a click precedes a save failure,
MCP, records, and text responses retain `input_emitted=true` and one completed
action without disclosing new refs.

Combined `make test` passed Go format/vet/tests, 607 provider tests, and
362 Python tests with 26 skips. The Python check used the existing isolated
compatible OSWorld overlay. All Go transport race tests passed. Independent
code review found no blocker. Final remote CI and merge state belong to the PR.

Evidence: `/datadrive2/hcu-eval/sdk-daemon-lifecycle-20261020/`, including
`make-test.log`, `transport-race.log`, and the SDK negative control in
`sdk-transport/`. Tests use real local sockets and Go daemons with fake
desktops. They do not operate the user's desktop or invoke a model.
