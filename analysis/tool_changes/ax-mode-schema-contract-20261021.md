# Publish AX argument fields by mode

Status: `testing`
Created: 2026-10-21 (UTC)
Change ID: `ax-mode-schema-contract-20261021`
Baseline: macOS candidate r11, package version `1.0.0`.
Candidate: r12 diagnostic; no product promotion.
Source: `/datadrive2/repos/hcu-macos-observation-fix-20261021`.
Baseline image: `multiapp5-r11-rebuilt-20261022` prepared base.

## Problem and evidence

The AX schema listed all mode fields in one flat object. The decoder rejected
fields that belonged to another mode. Thus a client could accept arguments
that the server had already documented as invalid through its runtime rules.

The fixed r11 comparison trace contains `observe` with `limit:100` at
`evidence/multiapp5-r11-20261022-a3/02-hcu/agent.jsonl:12`, item 4, under
`/datadrive2/benchmarks/macagentbench-20261021/`. The next call removed `limit`
and succeeded. The original call also violated the published maximum of 50.
A separate check changed only that number to 20: the original schema accepted
the call, while the decoder still rejected `limit` for `observe`.
The trace did not contain the `limit:20` diagnostic call.

## Change

`computer-use/tool_contract.go` adds four closed `oneOf` branches with constant
mode values: `apps`, `observe`, `find`, and `act`. Branches share root field
definitions by local references to keep the model schema within Codex's
compaction budget. The existing decoder allowlists select branch membership.

The schema also requires each mode's mandatory fields, excludes `none` from
`observe`, and separates a new `find` query from a cursor continuation.
Queries require at least one field. Target selection uses exactly one of
`app` and `window_ref`. Runtime acceptance, default values, action behavior,
and the removed `assert_ax` mode remain unchanged. Waits still use `act`.
This fixes a cross-platform argument contract, without application conditions.

The Claude schema adapter retains shared root constraints when it flattens
branches. It removes redundant references to the same root field before
merging, so the flattened schema cannot contain self-references.
The Python PTC adapter inlines both definition and root-property references.
Its records-only `read` branch and call options remain valid under the closed
AX branches, including nested `find` alternatives.

Changed product paths are `computer-use/tool_contract.go`, decoder comments in
`computer-use/ax_public_request.go`,
`hcu/osworld/guest_files/claude_mcp_schema_adapter.py`, and
`sdk/python/hcu_sdk/ptc.py`. Tests cover mode schemas, preflight, consistency,
SDK output and adapter output; the public contract golden is regenerated.

## Model contract review

- MCP schema: revised deliberately; golden records the four branches.
- Tool and parameter descriptions: existing mode descriptions and `find`
  page-size guidance remain correct. No new parameter or capability exists.
- Server instructions: unchanged because channel selection and input safety
  have not changed.
- Developer documentation: `docs/MCP.md` lists each mode's fields;
  `docs/CLIENT_COMPATIBILITY.md` records client transformation limits.
- Evaluated-agent instructions: `instructions/base.md` and
  `instructions/hybrid.md` keep their general guidance. They delegate call
  shapes to tool definitions, so no task recipe or extra instruction is added.
- PTC and Claude tool definitions: updated to retain their existing public
  capabilities after the core schema revision.

## Validation and limits

Focused Go checks pass for 5,989 schema/runtime samples, including 2,452
schema-valid calls. The consistency test no longer exempts mode-field,
mode-required-field or `observe:none` discrepancies.
Independent positive and negative calls exercise the original schema,
OpenCode root closure, and Codex normalization. Rejected calls reach no
provider and send no input. The Claude adapter tests use the actual golden
tools/list schema and pass.

Codex's normalized AX schema is 4,666 bytes, below its 5,000-byte compaction
budget. Action fields, query fields and mode branches remain visible.
This is a transformation regression test, not a claim of a live model run.
Codex normalization already discards numeric bounds and `minProperties`;
tests explicitly retain those limitations while checking server enforcement.
Claude's compatibility view still flattens mode membership. The server's
decoder and original schema remain authoritative for every client.

The first full Go run exposed the PTC adapter's unsupported root references;
that failure prompted the accompanying SDK fix. The final `make test` passed
Go formatting, vet, all Go tests, and all 609 provider tests. Its pytest stage
used a system Python without `docker`, causing 2 failures and 24 import errors.
The complete pytest suite then passed with the existing benchmark virtual
environment and this worktree first on `PYTHONPATH`: 364 passed, 32 skipped.
No product dependency check was weakened to bypass the failed environment.
Logs are `/tmp/hcu-r12-schema-make-test.log` and
`/tmp/hcu-r12-schema-pytest-venv.log` on cpu001.

The SDK daemon check includes 53 Python tests and validates the actual PTC
schemas with the Go JSON Schema validator. It verifies every AX mode, the
records-only read mode, call options and negative mode combinations without
adding a Python JSON Schema dependency. The documentation checker reported
existing issues in unchanged prose; the new contract text has no hard findings.
Native r12 compilation and 21 standalone sampling regression tests passed.
After normal permission grants for the isolated installation, tools/list
published the four modes; invalid observe/limit and find/cursor combinations
were rejected without input. Apps, observe, screenshots and a keyboard action
with final screenshot succeeded. A real GPT-5.5 xhigh Codex session successfully
called AX apps and returned READY in 6.7 seconds. This confirms that the
client can use the deployed tool; it does not inspect every field of the
model-bound schema or prove fewer model argument errors.
Evidence is under
`/datadrive2/benchmarks/macagentbench-20261021/evidence/mab-20261021-observation-fix-native-a1/`,
in `native-canary-authorized/report.json` and
`codex-canary-authorized/result.json`. Earlier permission-denied diagnostics
remain preserved. The r11 a3 run, its image, and its evidence remain unchanged.
No new task score, latency, token saving, or release readiness is claimed.

## Safety and disposition

Argument rejection still happens before observations or input. The schema
does not weaken stale-ref, window ownership, timeout or partial-input rules.
Mode branches add schema bytes; staying below the tested compaction budget
preserves descriptions and nested action fields. Future client changes can
still require a new compatibility check. r12 remains an unreleased candidate.
