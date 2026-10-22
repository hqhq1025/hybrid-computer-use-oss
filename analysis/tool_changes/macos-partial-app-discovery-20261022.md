# Preserve application discovery when one window inventory fails

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `macos-partial-app-discovery-20261022`
Baseline: native candidate r6, package version `1.0.0`, using the r5 Swift provider.
Candidate: r7; successful native build attempt `hcu-native-build-r7-a2`.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline VM image: repaired official macOS 26.3 / 25D125 derivative at
`/datadrive2/benchmarks/macagentbench-20261021/images/repaired-26.3-20261022-r2/`.

## Problem and evidence

On the rebuilt MacAgentBench guest, two HCU `AX mode=apps` calls failed
because Calendar's application-level `AXWindows` read returned
`AXError.cannotComplete` (`-25204`). Five preceding application reads succeeded.
One failed application's read discarded the complete discovery response.

The evidence root is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/rebuilt-tools-20261022/a2/`.
The first two transcripts are `hcu-initial/provision-readonly/mcp.json` and
`hcu-after-calendar-activation/provision-readonly/mcp.json`.
`native-window-log-selected.json` retains the status, nil value, and failed cast.
The calls emitted no input. This was an unavailable read, not a complete empty
window list.

CUA subsequently read a Calendar window's AX tree. Its evidence is in
`cua-calendar-diagnostic/mcp.json`; HCU still failed in
`hcu-after-cua-read/mcp.json`. This does not identify why HCU's application-level
attribute read fails. Single-window reads and application window enumeration
use different queries. This candidate does not change AX timeouts, activate
applications, or claim to repair Calendar's underlying attribute behavior.

## Implementation and safety

The private provider response gains `inventory_issues`, containing `app` and
`reason`. An omitted application means the provider could not enumerate all
applications. Empty issues preserve the existing complete-list behavior.

- `ComputerUseRuntimeBridge.swift` collects each application's window records.
  It catches only local `stateUnavailable` errors and preserves other apps.
  The response identifies the incomplete application instead of claiming its
  windows are absent. Global Accessibility permission is checked before and
  after enumeration; denied or revoked permission still fails the call.
- `ComputerUseRuntimeWindows.swift` retains the AX status alongside the existing
  complete flag and diagnostics. `apiDisabled` remains a permission failure.
- `ProviderV2Server.swift` and `provider_types_v2.go` carry the typed issues.
- `ax_window_inventory.go` allows partial inventory only for `listApps`.
  Title resolution, implicit focus, and ambiguous-app recovery require
  complete inventory. Issued refs still use the existing target validation.
- `ax_wait.go` requires complete inventory for window waits. A partial read
  cannot pass a `gone` wait or become a definite false predicate on timeout.
- `ax_evidence.go` preserves `inventory_complete=false` and `inventory_issues`
  in public text and records evidence.
- Linux `app_resolver.py`, `ax_dispatch.py`, and `wire_v2.py` carry existing
  incomplete-enumeration facts through the same field. Existing notes remain.
  The Linux collection algorithm does not change. This also prevents its
  former notes-only partial lists from satisfying a Go absence predicate.

This change does not weaken native close liveness checks, create refs for
missing windows, or mark missing windows stale. It adds no application-specific
branch, new dependency, retry loop, or background input. The per-app AX timeout
and the outer read timeout stay unchanged. Partial output may contain more
useful windows and error text than the former single error. No token, latency,
or cost improvement has been measured.

## Model contract review

The AX description and mode description now say discovery returns confirmed
windows and reports incomplete inventories. The reviewed contract golden is
updated for that intended text change. Arguments and tool names do not change.
The server instructions already describe window discovery without promising
completeness and need no change.

`docs/MCP.md` documents partial output and strict selection/wait behavior.
`docs/ARCHITECTURE.md` documents the private field and its consumers.
`instructions/hybrid.md` and `instructions/ax-only.md` explain that returned
refs remain usable while missing windows remain unknown. `hybrid-code.md`
inherits the hybrid guide; the GUI-only guide has no AX discovery surface.
No guide adds an application recipe or benchmark-derived procedure.

## Validation and disposition

Go regressions cover public partial apps output, records parity, confirmed
window close, denied title/focus inference with zero input, unknown window
waits, complete-inventory recovery, stable refs, and global provider failure.
Focused Go race tests, including existing close/window tests and the provider
fixture corpus, passed.

Linux regressions cover typed per-app and whole-bus incompleteness, retained
notes, complete inventories, and wire projection. The full Go tests and all
609 Linux provider tests passed during `make test`. The first repository pytest
attempt used a Python without the declared `docker` dependency: 337 passed,
32 skipped, 2 failed, and 24 setup errors. All reported failures were
`ModuleNotFoundError: No module named 'docker'`. Its log is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-r7-inventory-make-test.log`.

The coordinator's existing environment at
`/datadrive2/benchmarks/macagentbench-20261021/.venv/bin/python` then passed the
repository pytest suite: 362 passed and 33 skipped in 47.82 seconds. The log is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-r7-pytest-venv.log`.
All `make test` components therefore passed with
their dependencies available; the initial system-Python failure remains recorded.

Native tests in `AppWindowInventoryTests.swift` cover partial discovery,
complete empty lists, permission denial and revocation, non-local errors,
and V2 issue projection. Native build and all six standalone test functions
passed on macOS 26.3 using Apple Swift 6.3.2 and SDK 26.5. The standalone
program mechanically translates the test assertions and links the unchanged
44 production kit sources. It is not the complete SwiftPM suite: the installed
CLT lacks `Testing`, confirmed by a fresh import check. Build, signature,
test and module-availability evidence is under
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-native-build-r7-a2/`.
The earlier a1 build was terminated after the original CLT's duplicate
SwiftBridging module definitions were identified. Apple softwareupdate then
installed a complete CLT 26.5 package with the original directory preserved.

The r7 artifact has been installed at
`/Users/pipiwu/mab-tools/hcu-candidate-r7/`. Native GUI permission approval and
two fresh-process provision checks passed, including decoded 1365 by 768 desktop
screenshots. The second check returned Calendar's confirmed window while
explicitly reporting Contacts and Safari inventories as incomplete. This
validates partial discovery, not universal application AX availability.
Evidence is in `evidence/rebuilt-tools-20261022/a2/r7-final/` and
`r7-freshprocess/` under the benchmark workspace.

The real input safety canary `hcu-native-safety-r7-a1` passed its first six
checks but failed the final close check. After close, AX showed the exact target
absent while WindowServer still listed it. The provider returned unknown;
reading the sibling window did not verify the close. The failed canary and
native evidence remain in `evidence/hcu-native-safety-r7-a1/`. The separate
`macos-close-lifecycle-settle-20261022` change addresses bounded resampling in
r8. This r7 canary remains failed. The combined r9 candidate subsequently
passed the same seven safety checks, including exact target absence and sibling
preservation, in `evidence/hcu-native-safety-r9-a1/report.json`. That later
result validates the combined changes and does not rewrite the r7 result.
Independent code review reported no blocker.
No benchmark rollout, promotion, or release is claimed.

The Windows provider has an existing catch-all around per-process discovery
in `UiaProvider.ObserveApps`. It does not emit typed incompleteness for those
exceptions. This candidate does not change or validate Windows discovery.
