# Initialize the workspace application inventory before provider workers

Status: `rejected`
Created: 2026-10-21 (UTC)
Change ID: `macos-main-runloop-workspace-refresh-20261022`
Baseline: native candidate r9, package version `1.0.0`.
Candidate: r10.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; this is a native macOS candidate.

## Problem and evidence

The r9 long-lived provider omitted newly launched Contacts and Safari even
though a separate process could see their new live PIDs. Its observe requests
returned `app_unavailable`; a fresh provider subsequently observed both apps.
Evidence is under the benchmark workspace
`/datadrive2/benchmarks/macagentbench-20261021/evidence/` in
`hcu-native-process-r9-live-a1/` and `hcu-native-process-r9-fresh-a2/`.
The independent Calculator lifecycle diagnostic also reproduced stale discovery
in `hcu-native-lifecycle-r9-control-a1/`.

Apple documents that `NSWorkspace.runningApplications` changes only while the
main run loop runs in a common mode. Reads from any thread are safe and atomic:
<https://developer.apple.com/documentation/appkit/nsworkspace/runningapplications>.
This requirement does not prove that the previous provider lacked a main loop.
Swift's Darwin concurrency runtime already uses `CFRunLoopRun`, and the
coordinator captured the idle r9 main thread inside that run loop. The
provider's blocking transport read was on a worker thread. Evidence is in
`hcu-r9-idle-sample-a1/sample.stdout`. Therefore this change does not add a new
run loop or move the transport to another executor.

## Candidate and boundaries

`ComputerUseProvider/main.swift` imports AppKit and reads
`NSWorkspace.shared.runningApplications` in an explicit `MainActor.run` block
at startup, before constructing the server and before provider workers can
make the first inventory read. This initializes
both the workspace singleton and the application inventory on the main thread.
The existing async entrypoint, transport, serial request loop, process-exit
behavior, and launch completion handling remain intact.

This is a small experiment against the observed first-access ordering. Apple
does not document main-thread initialization as a requirement, so the precise
framework failure mechanism remains a hypothesis until the controlled native
lifecycle checks establish the candidate's behavior. No app names, PID cache,
extra polling, periodic run-loop pumping, or new configuration enter production.
The r9 process-existence filter and the existing process-generation and window
identity checks remain in force. No performance benefit is claimed.

## Contract review and validation

The public MCP schemas, descriptions, server instructions, developer API docs,
and evaluated-agent guides do not change. The candidate attempts to restore
current running-application discovery within the existing contract. It does not
introduce an application-launch guarantee, change launch policy, or expand
permission or input capabilities. This record explains the native startup
mechanism and its uncertainty.

The diagnostic `test_entrypoint.py` builds the actual entrypoint against the small
`Tests/EntrypointFixture/ProviderFixture.swift` fixture. The fixture verifies
main-loop event delivery during synchronous worker waits, repeated requests,
EOF exit, and startup-error reporting. Its `apps` command supports a persistent
process discovery diagnostic. It emits no desktop input and needs no AX
permission. This is an entrypoint integration test, not the full provider or
the SwiftPM suite. Linux Python syntax checks passed.
The coordinator preserved the diagnostic script, fixture, and follow-up
bootstrap variants outside the product repository in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-entrypoint-diagnostic-sources-r11/`.

Native production compilation and the entrypoint integration checks passed
with Swift 6.3.2, Go 1.26.1 on darwin/amd64, and SDK 26.5. The combined build
and test command took 60.16 seconds. Evidence is in
`hcu-native-build-r10-a1/result.json` and `stdout.log` under the evidence root.
The installed bundle at `/Users/pipiwu/mab-tools/hcu-candidate-r10/` passed
signature verification and reported package version `1.0.0`; see
`rebuilt-tools-20261022/a2/r10-install.json`.

The combined source passed `make test`: Go formatting, vet and tests,
609 Linux provider tests, and repository pytest with 363 passed and 32 skipped.
The command used the existing benchmark virtual environment with the system
PyGObject path (`PYTHONPATH=/usr/lib/python3/dist-packages`). Evidence is in
`hcu-r10-make-test-a2.log`. The first attempt passed Go checks but could not
import `gi` from that virtual environment; `hcu-r10-make-test.log` preserves
the dependency failure. No dependency or environment configuration was changed.

The controlled same-process application lifecycle check failed four of five
checks, matching r9. Auto-launch observation, visibility after auto-launch,
visibility after external relaunch, and observation after external relaunch
failed. Only the absence check after quit passed. Evidence is in
`hcu-native-lifecycle-r10-a1/report.json`; cleanup completed.
Main-thread inventory initialization alone did not fix discovery. This
experiment is rejected, and its entrypoint checks do not establish application
list freshness. Candidate r11 restores the original entrypoint and separately
tests main-thread reads under `macos-main-thread-app-discovery-20261022`.
No combined desktop safety pass, benchmark
score, promotion, or formal release is claimed for r10.
