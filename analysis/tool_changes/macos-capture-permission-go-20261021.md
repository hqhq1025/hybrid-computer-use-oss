# GUI permission denial recovery

Status: `merged`
Paths refer to the layout before the package split (a4006c5).
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `macos-capture-permission-go-20261021`
Baseline: package version `1.0.0`, `origin/main` selected for this worktree.
Source: `/datadrive2/repos/hcu-macos-mab-20261021`.
Baseline image: not applicable; this change targets the native macOS deployment.
This work creates no product release or evaluation result.

## Problem and evidence

The MacAgentBench guest denied ScreenCaptureKit access with error `-3801`.
The GUI response reported `runtime_error` and suggested another screenshot.
Repeating that call cannot grant the missing system permission.
The captured response is at
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-initial-gui.json`.

The Swift provider now identifies the typed capture-permission error as
`permission_denied`. This Go change preserves that classification and gives
the caller instructions that resolve the missing permission.

## Change and contract review

`computer-use/gui_service.go` maps provider `permission_denied` to the same
public code when no input may have occurred. If input was emitted or remains
uncertain, the public code remains `effect_unknown`. Both cases instruct
the caller to ask the user for system permission, restart the provider,
and take a fresh screenshot before retrying input.

The recovery hint applies both when the provider returns an image and when
capture fails without an image. The Go layer does not retry the provider.
Existing input, completed-action, and partial-input evidence is preserved.

`computer-use/contract_describe.go` publishes the new GUI error code with
CLI exit status 6 and `input_emitted: no`. The CLI schema generates this
error table at runtime. Regenerating the contract golden produced no diff
because that snapshot does not include the schema command's error table.
AX/GUI input schemas, tool descriptions, and server instructions are unchanged.
The existing description already defines errors as a code and a hint.

The coordinating agent updates `docs/MCP.md`, `instructions/hybrid.md`, and
`instructions/gui-only.md` to describe the permission recovery. These
instruction edits require a new evaluation attempt. No task recipe,
application exception, or benchmark-specific runtime branch is added.

## Validation and disposition

The new no-image regressions failed against baseline Go behavior. Without
input, baseline returned `runtime_error`; after input, its hint still
suggested an immediate screenshot. The fixed tests check both cases through
the model-visible call path and verify exactly one provider request.
They also check input emission, completed/requested counts, and partial-input
warnings. Existing GUI tests cover errors with returned screenshots.

Focused GUI, public error, CLI exit-code, and contract tests passed under the
Go race detector. The combined `make test` passed Go checks, 607 provider
tests and 360 Python tests, with 28 environment-dependent skips.

The integrated native r2 bundle also returned the corrected `permission_denied`
response after a real macOS screen-capture denial. It retained
`input_emitted:false` and gave permission-recovery instructions. The trace is
`/datadrive2/benchmarks/macagentbench-20261021/evidence/hcu-r2-initial-gui.json`.
No score, token, or latency gain is claimed.
