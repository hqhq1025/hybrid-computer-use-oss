# macOS current-process permission checks

Status: `merged`
Created: 2026-10-21
Change ID: `macos-runtime-permissions-20261021`
Baseline package version: `1.0.0`, unchanged.
Baseline runtime: the macOS native provider in the main-branch checkout.
Baseline image: not applicable; the provider is a native macOS executable.
Candidate source: `/datadrive2/repos/hcu-macos-mab-20261021`.
No new release or Docker image is assigned.

## Problem and evidence

`PermissionDiagnostics.current()` combined each runtime result with an
authorization row from the system TCC database. A stored grant therefore
overrode a denial from the current process. The query checked the service,
client identifier and client type, but did not verify the executable's signing
requirement. An earlier grant could survive a replacement executable.

The GUI input gate consumes `eventPostingGranted`. AX snapshots consume
`accessibilityTrusted`. An incorrect grant allowed these paths past their
permission checks and hid the cause of a later operation failure.

The macOS 26.3 VM has SDK 26.2. Its installed Apple headers were inspected
without requesting permissions or emitting input:

- `AXUIElement.h` defines `AXIsProcessTrusted` as the trust status of the
  current process.
- `CGEvent.h` defines `CGPreflightPostEventAccess` as a check of the current
  process's existing event-synthesis access.
- `CGWindow.h` defines `CGPreflightScreenCaptureAccess` as a check of the
  current process's existing screen-capture access.

These preflight APIs remain in use. Permission checks do not prompt the user.
The SDK comments distinguish preflight checks from the corresponding request
APIs, which may prompt. The existing explicit accessibility-prompt helper is
unchanged.

## Changes

`computer-use/native/macos/Sources/ComputerUseProviderKit/Permissions.swift`
now uses the three runtime results directly. It removes the private SQLite
reader and the stored-grant override. The public permission-client helper
remains available for source compatibility, but it does not authorize input.

`computer-use/native/macos/Tests/ComputerUseProviderKitTests/PermissionDiagnosticsTests.swift`
tests all eight combinations of runtime grants. It checks every diagnostic
field and missing-permission entry, and verifies that every probe runs once.
A second test checks grant, denial and grant across successive samples.

The correction applies to all applications and all deployment paths. It uses
no benchmark task IDs, application recipes or expected answers.

## Model-contract review

The public MCP schema, tool names, parameter descriptions and server
instructions do not change. The existing permission-denied behavior now
receives current-process evidence. AX and GUI no longer bypass their permission
gates because a database row records an earlier grant.

The base policy and evaluated-agent interaction guides require no change.
The agent still needs a provider that has the required operating-system
permissions. Platform documentation should explain current-process checks and
permission renewal after changes to the provider's signing identity.

## Safety, compatibility and cost

The provider fails before input when the existing runtime gate denies access.
No permission requests, TCC writes, fallback authorization or retries are
added. The change preserves each permission independently and retains fresh
checks on every diagnostic call.

Deployments that relied on a stale database row may now report a missing
permission. Such a report requires granting access to the current provider
and checking it again after restart. A true preflight result still does not
prove that an input reached its target or that a screenshot captured the
requested content; the existing operation evidence remains necessary.

Removing database reads reduces work in the permission path. No measured
latency, token, cost or task-success improvement is claimed.

## Verification and disposition

SDK API semantics were checked on the macOS 26.3 test VM with SDK 26.2.
Both macOS packaging tests passed on cpu001. `git diff --check` passed, and
the document checker reported no hard-rule violations.
The macOS VM then compiled the production module with Apple Swift 6.3.2.
The focused SwiftPM run stopped because Command Line Tools does not include
the `Testing` module. No SwiftPM test cases executed. The full compiler log is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-permissions-tests.log`.

A standalone native runner compiled the unchanged production `Permissions.swift`
with the assertions from `PermissionDiagnosticsTests.swift`. The generated
runner removes test-framework annotations, converts `#expect` to `precondition`,
and invokes the eight permission combinations and the resampling test.
All assertions passed on the macOS VM with Apple Swift 6.3.2. The log is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-permissions-standalone.log`;
the generated source is beside it as `permissions-standalone-main.swift`.
The standard Swift Testing source remains unchanged.

No live permission revocation or code-signing replacement experiment has run
yet. The native assertion runner uses injected runtime results and does not
establish operating-system authorization. The SDK and native compilation
checks establish API availability, not successful GUI input or screen capture.

No model evaluation or formal benchmark score is produced by this change.
The candidate remains under test; native test and deployment evidence must
be recorded before promotion.
