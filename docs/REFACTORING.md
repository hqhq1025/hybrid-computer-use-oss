# Refactoring Review

## P0: Release Identity Is Split

The promoted image is tagged `v0.14`, while its binary version and image labels
still contain candidate-era `v013`/`unreleased-beta` names. Runtime behavior is
correctly bound by hashes, but operators can misread logs and inventory.

Recommended next release: generate product version, OCI labels, binary version,
release manifest, and SBOM from one release descriptor. Do not rewrite v0.14.

## P1: V1 And V2 Harnesses Duplicate Agent Bootstrap

Codex TOML, Claude flags, MCP registration, context cleanup, skill injection,
completion sentinels, and trace normalization exist in both runners.

Recommended refactor: move these into one tested `hybrid_runtime` Python
package. Keep only environment lifecycle and evaluator adapters in the V1/V2
frontends.

## P1: V2 Runner Is Too Large

The source runner combines VM lifecycle, artifact upload, agent launch,
multi-phase control, token accounting, official evaluation, optional audit,
interactive Judge, and result serialization in one file.

Recommended split:

```text
runtime_binding.py
guest_install.py
agent_codex.py
agent_claude.py
task_loop.py
official_evaluator.py
judge.py
result_store.py
```

The public `run_batch.py` intentionally stays a thin subprocess coordinator.
Do not merge the strict queue's benchmark binding, host-pressure, and recovery
state back into it; those are different operational tiers.

## P1: Unified QEMU Runner Still Has V2 Names

The runner now accepts both V1 and V2 task suites, but its package path and
several private run/cache identifiers still say `osworld-v2`. This is
operationally harmless but obscures the benchmark/backend separation.

Recommended refactor: move the shared implementation to
`integrations/osworld-qemu/`, retain thin V1/V2 entrypoints, and version all
result metadata with explicit `benchmark_version`, `guest_image_sha256`, and
`runtime_backend` fields. Preserve old paths only as command-line shims.

## P1: Provider Naming Is Misleading

The private wire protocol is V2, but the Linux package remains named
`provider_v1`. This is historical residue and makes architecture reviews harder.
Rename only in a new release because embedded paths and tests are part of the
binary.

## P2: Claude Schema Adaptation Is A Compatibility Layer

The adapter is intentionally narrow, but flattening a discriminated union makes
the client-visible schema less precise. Keep server-side strict validation.
Remove the adapter when Claude Code accepts the native MCP schema.

## P2: Agent CLI Upgrades Need A Canary Gate

Agent CLIs are run-configuration dependencies, not part of the v0.14 MCP
release. Their flags, built-in tools, feature defaults, and config schema can
change independently.

Keep `configs/agent-artifacts.lock.json` immutable within a reported round.
Before changing it, validate config parsing, tool exposure, one Codex task and
one Claude Code task. A CLI upgrade always starts a new run ID and must not be
mixed into an older batch.

## P2: Large Hand-Written Schemas And Evidence Normalization

`tool_contract.go` and `main.go` contain substantial hand-built schema and
response-normalization logic. Generate schemas from typed mode structs and move
evidence normalization into a dedicated package, while retaining byte-for-byte
contract tests.

## P2: Go/Python Boundary

Embedding the Python provider makes deployment convenient, but creates two
state machines and a JSONL boundary inside one executable. Keep the split while
Linux accessibility remains Python/GI based, but formalize provider protocol
fixtures and version negotiation as a small standalone package.

## Removed From This OSS Extraction

- stale V1 `v15`/`v3` defaults;
- fixed `/datadrive2/...` artifact and result paths;
- implicit dependence on private evaluator overrides;
- benchmark tasks, gold artifacts, credentials, private traces, and VM images.
