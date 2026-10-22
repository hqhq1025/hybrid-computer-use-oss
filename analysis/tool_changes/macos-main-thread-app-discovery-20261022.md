# Read each application inventory on the main thread

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `macos-main-thread-app-discovery-20261022`
Baseline: native candidate r9, package version `1.0.0`.
Candidate: r11; excludes the rejected r10 startup change.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; this change affects native macOS discovery.

## Problem and controlled evidence

The long-lived r9 provider returned an application inventory that omitted newly
launched processes. A fresh provider observed them. Filtering known exited
PIDs removed ghosts but could not discover missing new PIDs. The same-process
Calculator lifecycle test failed four of five checks in r9 and r10. A one-time
main-thread initialization in r10 did not fix later discovery.

Apple documents that `NSWorkspace.runningApplications` is safe to read from
any thread, while its value changes when the main run loop runs in common
modes. The actual r9 main thread already ran `CFRunLoopRun`; its transport
read occupied a worker. Thread safety alone did not establish fresh snapshots
for the observed long-lived worker reads.

The coordinator compiled the provider entrypoint against a small, permission-
free fixture. Each fixture process remained alive across Calculator launch,
quit, relaunch with a new PID, and quit. Independent `pgrep` checks supplied
the expected PIDs. Evidence is under
`/datadrive2/benchmarks/macagentbench-20261021/evidence/`.

| Read or bootstrap | Observed lifecycle result | Evidence directory |
| --- | --- | --- |
| Worker reads with r10 startup initialization | New process omitted | `hcu-r10-fixture-lifecycle-a1` |
| Initialize `NSApplication`, retain async entrypoint | Both launches omitted | `hcu-entrypoint-variant-b-lifecycle-a1` |
| Run `NSApplication` main loop | First launch appeared; exit and relaunch stayed stale | `hcu-entrypoint-variant-c-lifecycle-a1` |
| Worker reads with an autorelease pool | Both launches omitted | `hcu-entrypoint-pool-read-lifecycle-a1` |
| Each read on main, with r10 entrypoint | All five lifecycle observations passed | `hcu-entrypoint-main-read-lifecycle-a1` |
| Each read on main, with original r9 entrypoint | All five lifecycle observations passed | `hcu-entrypoint-original-main-mainread-lifecycle-a1` |

The original-entrypoint comparison isolates the read context from startup
initialization. These observations support reading each inventory on the main
thread. They do not establish undocumented internals of Apple's cache or claim
that every macOS version behaves this way.
The diagnostic entrypoint, fixture, build script, and bootstrap variants are
preserved in `evidence/hcu-entrypoint-diagnostic-sources-r11/`, outside the
product repository. They are not evaluated-agent tooling or runtime dependencies.

## Change and boundaries

`AppDiscovery.runningApps` dispatches synchronously to the main queue only when
the caller is not already on the main thread. The main-thread path reads,
filters, sorts, and materializes the current workspace inventory in one call.
The injected read closure supports deterministic thread tests; production reads
the existing `NSWorkspace.shared.runningApplications` property.

The main-thread guard avoids dispatching synchronously to the current queue.
There is no cache, extra polling, application-specific condition, or new event
loop. The r9 entrypoint is restored; the rejected r10 startup code is excluded.
Existing internal-provider and ESRCH filters, application matching and launch
policy, process-generation validation, and exact window identity remain intact.
AX tree traversal and desktop input do not move onto the main queue.

A background inventory read now waits for main-queue scheduling. Existing
provider timeout and reset handling still applies; this change does not add a
new timeout or promise a latency improvement. A blocked main queue can still
make discovery unavailable. No benchmark result or cost reduction is claimed.

## Contract review and validation

Public MCP schemas and parameter descriptions, server instructions, developer
API docs, and evaluated-agent guides retain their existing discovery contract.
This change restores fresh discovery without a new caller option or recovery
instruction. No model-visible surface needs revision. This record explains
the native mechanism, measured scope, and remaining uncertainty.

`AppDiscoveryThreadTests.swift` checks direct main-thread calls and repeated
worker calls. Every injected read must execute on the main thread, and the
second worker call must read again. The tests use no sleeps or desktop
input. The standalone harness in `evidence/hcu-native-discovery-r11/` translates
these two original test functions and compiles the 44 production kit sources.
Linux generation and Python syntax checks pass. Native production compilation
and both standalone thread tests passed with Swift 6.3.2, Go 1.26.1 on
darwin/amd64, and SDK 26.5. The combined build/test command took 116.01 seconds;
see `evidence/hcu-native-build-r11-a1/result.json` and `stdout.log`.
The installed Command Line Tools lack Swift Testing, so this harness is
distinct from the full SwiftPM suite.

The coordinator installed the candidate at
`/Users/pipiwu/mab-tools/hcu-candidate-r11/`. Signature verification passed,
and the wrapper reported package version `1.0.0`; see
`evidence/rebuilt-tools-20261022/a2/r11-install.json`.

The earlier complete `make test` in `hcu-r10-make-test-a2.log` covers unchanged
Go and Python code. Two fresh-process provision checks passed with the expected
`AX` and `GUI` tools and decoded 1365 by 768 PNG screenshots; see
`evidence/r11-hcu-preflight-a1/provision-readonly/report.json` and
`evidence/r11-hcu-preflight-a2/provision-readonly/report.json`.

The packaged r11 provider passed all five same-process lifecycle checks on
macOS 26.3: automatic launch and observation, visibility of that new process,
absence after quit, visibility after external relaunch, and observation after
relaunch. The coordinator used the same script that failed four checks for r9
and r10. Evidence is in `evidence/hcu-native-lifecycle-r11-a1/report.json`;
cleanup completed. This verifies that repeated main-thread discovery fixes the
tested persistent-provider lifecycle failure on this system.

The combined candidate also passed all seven desktop safety checks in
`evidence/hcu-native-safety-r11-a1/report.json`: background lookup and editing,
cross-window rejection before input, stale refs after same-title replacement,
GUI batch validation, stale pixels after a window move, and exact target close
with the sibling preserved. Close returned `close_effect_verified=true` and
`target_window_absent=true`.

The final inventory still reported incomplete AX reads for Safari helper
processes. r7's typed partial-inventory evidence preserved those limitations;
the safety result does not claim complete AX coverage of every process.
r11 remains unreleased and does not provide a benchmark score or formal
product promotion.
