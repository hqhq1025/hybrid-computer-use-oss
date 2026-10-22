# Exclude confirmed exited processes from application discovery

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `macos-discard-exited-workspace-processes-20261022`
Baseline: native candidate r8, package version `1.0.0`.
Candidate: r9.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; this change affects native macOS discovery.

## Problem and evidence

`NSWorkspace.runningApplications` returned Safari PID 812 and Contacts PID 852
with `isTerminated=false` and `isFinishedLaunching=false`. The coordinator
independently confirmed those processes were absent with `ps`.
Workspace evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/rebuilt-tools-20261022/a2/workspace-process-diagnostic.json`.

`AppDiscovery.runningApps` trusted the workspace's termination flag.
`AppDiscovery.resolve` could therefore return an exited application before it
reached the existing `launchIfPossible` path. Window discovery could also attempt
AX reads against a PID that no longer existed. Repeating the same workspace
query did not itself prove that its process records were current.

## Change and boundaries

`AppDiscovery.swift` adds one check at the common `runningApps` filter.
For a positive PID, `kill(pid, 0)` checks process existence without sending a
signal. Only a failed call with `errno == ESRCH` excludes the process.
Success retains the process even if errno still contains ESRCH. EPERM and all
other errors remain unknown and retain the workspace record. Nonpositive PIDs
are not probed because POSIX interprets them as process groups or wider scopes.

The helper accepts an injected probe result for deterministic tests. It does
not cache liveness, pump the run loop, reject unfinished launches, terminate
processes, change launch policy, or claim that every retained process is alive.
The existing terminated/internal-provider filters remain in force.
Subsequent identity validation still handles processes that exit or change
after discovery. This check cannot prevent PID reuse and does not replace
process-generation validation.

This is a general correction for stale workspace records, with no application
names or benchmark procedures in runtime conditions. It adds one signal-zero
probe per otherwise eligible workspace record. No performance gain is claimed.

## Contract review and validation

Server instructions, tool schemas and descriptions, developer API docs, and
agent guides already describe discovery of running applications. Excluding a
process that the OS confirms absent restores that contract; none needs a new
argument or guidance. Existing application launch and error behavior remain
unchanged after discovery. This record documents the native mechanism.

`AppProcessLivenessTests.swift` covers eight result/error combinations,
nonpositive PIDs without probing, and repeated live/missing/unknown/live results
without caching. The native standalone runner in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-process-r9/`
generates these three test functions and links unchanged production sources.
It requires no Swift Testing module, desktop permission, signals, or input.

Linux generation and Python script checks pass. Native build r9-a1 passed with
Swift 6.3.2, Go 1.26.1 on darwin/amd64, and SDK 26.5. The production provider
bundle and MCP wrapper built successfully. All three generated standalone
process-liveness test functions passed. The combined build and test command
took 102.05 seconds; this is not an isolated test-duration measurement.
Evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-build-r9-a1/`.
The full SwiftPM suite was not run because the installed Command Line Tools
do not provide the Swift `Testing` module. The standalone runner compiled the
44 production source files listed by the production build script.

The uncached full Go suite passed on the combined r9 source with
`go test -count=1 ./...` in `computer-use`; its log is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-r9-final-go-tests.log`.
These Linux tests do not compile the native
Swift implementation. The earlier Python test results remain applicable to
this native-only delta.

The coordinator installed r9 separately at
`/Users/pipiwu/mab-tools/hcu-candidate-r9/`. Signature verification and the
wrapper version check passed; the wrapper reported `1.0.0`. Evidence is in
`/datadrive2/benchmarks/macagentbench-20261021/evidence/rebuilt-tools-20261022/a2/r9-install.json`.
Two fresh-process HCU provision checks passed with decoded 1365 by 768 PNG
screenshots and the expected `AX` and `GUI` tools. Evidence is in
`evidence/r9-hcu-preflight-a1/provision-readonly/report.json` and
`evidence/r9-hcu-preflight-a2/provision-readonly/report.json` under the benchmark
workspace. The combined seven-check desktop safety canary also passed; see
`evidence/hcu-native-safety-r9-a1/report.json`. It verified background lookup
and editing, cross-window rejection, stale semantic and pixel targeting, GUI
batch prevalidation, and exact target close while retaining the sibling.
This canary covers the r8 close change together with r9; r8 itself did not run
a desktop safety canary.

The separate live discovery/launch diagnostic failed its two observe checks;
see `evidence/hcu-native-process-r9-live-a1/report.json` and `mcp.json`.
The old Safari PID 812 and Contacts PID 852 were absent, and HCU did not list
them. Each observe call returned `app_unavailable` after about five seconds.
A separate fresh process then saw live Contacts PID 3937 and Safari PID 3943,
both finished launching with successful signal-zero probes. These facts
confirm ghost exclusion and new application processes, but do not establish
successful HCU observation of the launched applications. The same HCU process
still omitted them from its final inventory. r9 left this discovery issue
unresolved; its live diagnostic remains failed.
The separate r11 `macos-main-thread-app-discovery-20261022` change subsequently
passed all five same-process lifecycle checks, including automatic launch and
external relaunch, in `evidence/hcu-native-lifecycle-r11-a1/report.json`.
The r9 failed diagnostic remains unchanged.
Independent code review found no blocker. This candidate does not change r7/r8
artifacts, claim a benchmark score, or create a formal release.
