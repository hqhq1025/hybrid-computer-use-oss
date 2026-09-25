# Changelog

## 1.0.0

First open-source release. The MCP server, the runner, the configuration and
the documentation are all described as they are in this release; the section
at the end lists what operators of an earlier internal build need to change.

### MCP server

- The version is 1.0.0 and comes from `VERSION`; a plain `go build` reports
  `dev`.
- Batch errors say AX BATCH, and the tool schemas, descriptions and error
  texts no longer mention ComputerAction, A11Y BATCH or
  `COMPUTER_USE_MAX_INLINE_CHARS`. A golden test guards the whole public
  contract: `initialize`, `tools/list`, the argument-validation errors and
  the CLI help.
- The provider package lives at `computer-use/native/linux/provider/`, and
  the private provider protocol ID is `computer-use-provider-v2`. MCP clients
  see neither.
- The binary no longer concatenates the provider source at startup; only the
  tests read it.
- Removed unreachable code in the server and the provider, including the
  unused resource-identity scan and the legacy trace converter.
- Eight provider tuning variables became constants, because only tests ever
  set them: the stability wait, the application-resolution attempts and
  interval, and the table, child, fanout, managed-enumeration and find
  budgets.

### OSWorld runner

- Runs four agents: Codex, Claude Code, Gemini CLI and Grok Build. Each has
  its own gateway, model and effort defaults, guest configuration, version
  probe and trace summarizer, and there is one example queue per harness per
  benchmark generation. The Gemini CLI npm package is unpacked in the guest
  and runs on the Node runtime from `make agents`, which now downloads the
  Gemini CLI package, Grok Build and Node 20 alongside Codex and Claude Code.
- Grok Build 1.0.40 drops its system-prompt override when it resumes a
  session, so it rejects a second turn instead of continuing under different
  instructions.
- Added the ablation options: `--channels a11y,keyboard` or `gui,keyboard`
  exposes one desktop tool (through `COMPUTER_USE_CHANNELS` in the agent's MCP
  configuration), and `--skill-file ""` runs without a skill. Batch and queue
  configs accept `channels` and `no_skill`.
- The default token log is `<gateway>-tokens.jsonl` under the results root,
  matching the logs the gateways write.
- Removed the interactive outcome Judge: the queue step, its command-line
  options, its config keys, its field in `result.json` and its columns in the
  queue status. The official evaluator is the only scorer.
- Nothing the task agent sees names the benchmark or a release: guest files
  live under `/opt/agent-runtime` and `/tmp/agent-task`, the gateway affinity
  ID is `task-<run>`, the skills carry no version, and the tool texts avoid
  the internal names listed above.
- The runner is the `hcu` Python package, with `python -m
  hcu.osworld.run_task`, `run_batch`, `run_queue` and `setup_canary` as the
  entry points, and `python -m hcu.render_agent_config` rendering any of the
  four agent profiles.
- Removed the SHA-256 admission and provenance checks and the options that
  only served them (expected-digest, tool-version, release-manifest, provider
  and audited-override options, the queue's implementation and benchmark
  bindings, and the `releases/` directory). `make agents` still verifies the
  archive checksums it downloads.
- Removed the legacy direct-container V1 runner; the QEMU guest is the only
  evaluation path.
- `run_queue` runs each task once, keyed by its own ID. The concurrency keys
  are `max_active_tasks`, `max_total_live_tasks` and `max_starting_tasks`.
- Removed the queue's host-safety layer: kernel, load, memory, disk, inode and
  I/O pressure thresholds, the critical-pressure abort, and the Docker
  provider's pressure gate around container cleanup. The queue keeps bounded
  concurrency, launch staggering, the rate-limit circuit and restart
  recovery. `setup_canary_queue` and the guest-image builders are gone as
  well; the dependency images themselves still exist.
- `--agent-binary` replaces the per-agent binary options and the helper script
  that copied local builds into `artifacts/`.
- Fixed: an exception in the official evaluator is recorded as an evaluator
  error; before, it aborted the run before `result.json` was written.
- Fixed: relative path arguments are resolved against the caller's directory
  instead of the OSWorld checkout.
- Fixed: `run_queue` used its own config key names, did not pass the benchmark
  version, task domain, client password or artifact directory, and did not
  expand `${VAR}` references, so V1 configs ran as V2. `run_batch` and
  `run_queue` now share one config schema and reject unknown keys.
- Fixed: the overlay lacked `desktop_env/cache.py`, so installing it into a
  clean OSWorld-V2 checkout failed.
- The default MCP binary is `artifacts/computer-use`.
- The generated Codex config names its model provider, and the Claude Code
  launcher uses a placeholder credential; neither carries a secret.
- Failure summaries no longer end with a digest.

### Gateways

- The per-task request budget now applies to every task agent. The OpenAI
  gateway counts admitted requests; the Anthropic and Gemini gateways count
  successful responses that reached the agent.
- The OpenAI gateway reaches an OpenAI-compatible upstream as well as Azure
  OpenAI, and its upstream list is always explicit rather than defaulting to a
  hosted resource. The gateway can serve a local relay over plain HTTP. The
  default ports match the runner: 8611 for OpenAI, 8537 for Anthropic and
  8571 for Gemini.

### Build and repository

- `VERSION` is the only version source for the binary and the Python package,
  which is `hybrid-computer-use` 1.0.0 under `MIT AND Apache-2.0`.
- `make build` builds with `--network none`, and `make test` runs gofmt,
  `go vet`, the Go tests, the provider tests and the Python tests.
- Removed the direct-container desktop image and its smoke test; they needed a
  base image that is not redistributed, and the QEMU runtime is the only
  evaluation path.
- Files modified from OSWorld-V2 carry Apache-2.0 headers, and
  `THIRD_PARTY_NOTICES.md` lists them.
- Rewrote the documentation and added CI.

### Upgrading from an earlier internal build

- Entry points moved into the `hcu` package: `python -m hcu.osworld.run_task`,
  `run_batch`, `run_queue`, `setup_canary`, and `python -m
  hcu.render_agent_config`. The old integration paths and their wrapper
  scripts are gone.
- `run_batch` and `run_queue` share one config schema that rejects unknown
  keys, so configurations carrying Judge keys, queue-side keys, digest
  options or per-agent binary paths must be updated. The queue's default
  configuration path is `HCU_QUEUE`; `HCU_V2_QUEUE` still works.
- The OpenAI gateway needs `--upstreams`, and a non-Azure upstream needs
  `--upstream-kind openai` with a bearer token.
