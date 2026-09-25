# Changelog

## 1.0.0

First open-source release. Changes relative to the internal v0.14 packaging
this repository started from:

### MCP server

- The version is 1.0.0 and comes from `VERSION`. The v0.14 binary reported
  `0.70.0-beta.70+osw-v013-gui-native-1080p`; a plain `go build` now reports
  `dev`.
- The tool schemas, descriptions and error texts no longer mention
  ComputerAction, A11Y BATCH or `COMPUTER_USE_MAX_INLINE_CHARS`; batch
  errors say AX BATCH. Everything else the model sees is byte-identical to
  v0.14, and a golden test now guards the whole contract.
- The provider package moved to `computer-use/native/linux/provider/`, and
  the private provider protocol ID is `computer-use-provider-v2` (was
  `osw-provider-v2`). MCP clients do not see either.
- The binary no longer concatenates the provider source at startup; only the
  tests use it.
- Removed unreachable code in the server and the provider, including the
  unused resource-identity scan and the legacy trace converter.

### OSWorld runner

- Removed the interactive outcome Judge (`hcu/osworld/judge`, the
  `--judge-*` options, the `judge_*` config keys, `live_judge` in
  `result.json` and the queue's Judge columns). The official evaluator is the
  only scorer.
- Nothing the task agent sees names the benchmark or a release any more:
  guest files live under `/opt/agent-runtime` and `/tmp/agent-task`, the
  gateway affinity ID is `task-<run>`, the skills no longer carry a version,
  and the AX and GUI tool texts no longer mention A11Y BATCH, ComputerAction
  or `COMPUTER_USE_MAX_INLINE_CHARS`.
- The runner is the `hcu` Python package. `python -m hcu.osworld.run_task`
  replaces `integrations/osworld-v2/hybrid/run_hybrid.py`, and `run_batch`,
  `run_queue`, `setup_canary` and `setup_canary_queue` moved the same way.
  `python -m hcu.render_agent_config` replaces
  `scripts/render-agent-config.py`.
- Removed the SHA-256 admission and provenance checks together with their
  options: `--expected-*-sha256`, `--precomputed-guest-image-sha256`,
  `--tool-version`, `--release-manifest`, `--mcp-profile`,
  `--mcp-contract-manifest`, `--provider-name`,
  `--audited-evaluator-overrides`, the queue's implementation and benchmark
  bindings, and `releases/`. `make agents` still verifies the archive
  checksums of the agent CLIs.
- Removed the legacy direct-container V1 runner (`integrations/osworld-v1`).
- Fixed: an exception in the official evaluator is recorded as an evaluator
  error; before, it aborted the run before `result.json` was written.
- Fixed: relative path arguments are resolved against the caller's directory
  instead of the OSWorld checkout.
- Fixed: `run_queue` used its own config key names, did not pass
  `--benchmark-version`, `--task-domain`, `--client-password` or
  `--artifact-dir`, and did not expand `${VAR}` references, so V1 configs
  ran as V2. `run_batch` and `run_queue` now share one config schema and
  reject unknown keys.
- Fixed: the overlay lacked `desktop_env/cache.py`, so installing it into a
  clean OSWorld-V2 checkout failed.
- The default MCP binary is `artifacts/computer-use`.
- The Codex model provider in the generated config is `osworld_gateway`
  ("OSWorld task-scoped model gateway"), and the Claude Code launcher's
  placeholder credential is `hcu-task-gateway`.
- Failure summaries no longer end with a digest.

### Gateways

- Fixed: the per-task request budget did not apply to V1 task agents
  (`osworld-v1-hybrid-` tasks). The legacy `osw-hybrid-` prefix is no longer
  budgeted.
- The OpenAI gateway requires `--upstreams` instead of defaulting to
  internal Azure resources. The default ports match the runner: 8611 for
  OpenAI and 8537 for Anthropic.

### Build and repository

- `VERSION` is the only version source for the binary, the desktop image and
  the Python package, which is now `hybrid-computer-use` 1.0.0 under
  `MIT AND Apache-2.0`.
- `make build` builds with `--network none`. `make verify` is gone, and
  `make test` also runs gofmt, `go vet` and the provider tests.
- Removed the direct-container desktop image and its smoke test
  (`make image-v1`, `make smoke-v1-direct`); they needed a private base
  image and the QEMU runtime is the only evaluation path.
- `guest-images/workstation-deps-v014-beta1` became
  `guest-images/workstation-deps`.
- Files modified from OSWorld-V2 carry Apache-2.0 headers, and
  `THIRD_PARTY_NOTICES.md` lists them.
- Rewrote the documentation and added CI.
