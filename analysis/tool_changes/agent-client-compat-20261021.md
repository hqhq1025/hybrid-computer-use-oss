# Agent client compatibility

Status: `testing`
Created: 2026-10-21 (Asia/Shanghai)
Change ID: `agent-client-compat-20261021`
Baseline: main `42488fc`, package version 1.0.0.
Baseline diagnostic image: `osw-hybrid:sdk-cli-code-mode-20261020-a1`.
Source: `/datadrive2/repos/hcu-agent-compat-20261021`.
No new image, model evaluation, or formal release is created.

## Problems and mechanisms

OpenCode v1.19.3 copies a tool's schema and closes its root object with
`additionalProperties:false`. HCU GUI properties were declared only inside
`oneOf` branches, so a legal screenshot or click became invalid in that client.
The GUI schema now also declares `actions` and `settle_ms` at the root with
empty schemas. Those declarations impose no new constraints. Every original
branch and constraint remains intact.

Gemini CLI 0.65.0 parses its qualified MCP tool names by splitting at the first
underscore after `mcp_`. The old `computer_use` server key was parsed as
`computer`, preventing the intended server identity from matching. Gemini
configuration and its allowlist now use `computer-use`. The public MCP probe
reads that same key. Trace summaries accept both the new and historical names.
Codex, Claude, and Grok keep their existing server keys.

The OSWorld installation document named Claude's artifact `claude`; the
runner expects `claude-code`. The document now uses the actual filename.

## Contract review

`computer-use/tool_contract.go` and its golden add two GUI root property
declarations. `schema_client_compat_test.go` compares the original schema,
the current schema, OpenCode's root transformation, and Codex normalization.
The Claude schema adapter produces identical output before and after this
change. Tool names, action execution, input validation outcomes, error flags,
provider protocol, and image results are unchanged.

Gemini configuration changes are in `hcu/osworld/agents.py`, with matching
probe and trace changes in `runtime.py` and `traces.py`. Tests cover generated
configuration, the probe, and old/new trace names.

`docs/MCP.md`, `docs/AGENT_PROFILES.md`, `docs/OSWORLD.md`, and
`docs/CLIENT_COMPATIBILITY.md` describe client keys and compatibility limits.
Server instructions and evaluated-agent instructions need no edits: accepted
actions and their semantics did not change. No application recipe, benchmark
shortcut, new public tool, or client-specific runtime execution branch is added.

The schema grows only by the two property declarations. No runtime timing or
cost improvement is claimed. Gemini tool identifiers intentionally change in
new runs; historical traces remain readable. Runs need a new attempt when
using the new generated configuration.

## Client-side findings outside HCU

Native Claude Code 2.1.303 tests use isolated configuration, a scripted local
Anthropic endpoint, and a fake MCP peer carrying the real HCU schemas. Four
cases cover default text/image output and explicit structured-content output,
for both success and error. Default success text and PNG bytes reach the next
request. Default error text, `is_error`, and the recovery call reach it, but
the error image does not. That failure remains a client limitation; passing
the diagnostic test does not mean error-image delivery passed.

The opt-in structured channel also causes client-specific text replacement.
HCU already defaults it off and includes evidence in text. No additional
adapter or duplicated text field was needed.

OpenCode and Gemini error-image fixes belong in their client result and
message-conversion paths. Their versioned local patches are validated
separately; the HCU source change cannot update an installed third-party CLI.
No result is relabelled as success and no mutation is automatically repeated.

## Validation

GUI focused tests cover 48 boundary cases plus the existing 4,742-sample
schema/runtime corpus. The latter includes 2,084 schema-valid samples.
GUI schema size is 4,457 bytes raw and 4,206 bytes after Codex normalization,
below the existing 5,000-byte limit.

Gemini focused configuration/probe tests and its actual npm parser probes
pass. The isolated Gemini 0.65.0 CLI reads the generated configuration and
reports `computer-use` connected without model inference. Native Claude's four
diagnostic cases pass their text/error assertions; their recorded error-image
count is zero.

Full `make test` passed: Go formatting, vet and tests; 607 provider tests;
369 Python tests with 26 skips. This run enabled the installed Claude binary
for its native tests. Independent review found no blocking issue; additional
GUI schema tests and 18 Gemini/probe tests passed. Evidence is under
`/datadrive2/hcu-eval/agent-compat-fixes-20261021/`.

Both example queue dry-runs were attempted. This host's available virtual
environment lacks `desktop_env`, and this checkout has no installed
`external/OSWorld-V2`. They stop at dependency import before any guest is
created; they have not passed. No model or real desktop run was launched.
