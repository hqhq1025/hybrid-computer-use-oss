# Validation

Validation date: August 29, 2026.

## Environment

- host: Linux `6.17.0-1021-azure`
- runtime image: `happysixd/osworld-docker:latest`
- dedicated Docker server: `29.1.3`
- V2 guest SHA-256:
  `e450e8194d6a1779b762ee768064e3a75ebbcbd2c635376eab6bd021f6d88ef8`
- V1 guest SHA-256:
  `6bf667a852b3c307f61d9f09c42559351f45e0607e428b4997becf534cf4d313`
- Computer Use v0.14 SHA-256:
  `11adcefd856200eda37ec127b586d95105f1d9b52c64e6ee8ff5571c1f5bacf0`
- Codex `0.151.0` SHA-256:
  `9739cbc928b9c573be83256acd46668f5dd4f119d2d09e05246895ca2aaf0c9a`
- Claude Code `2.1.251` SHA-256:
  `fd5f10ff0eb58daec04900466b143ea98aab50abf208a422bc008eaec13f61f7`

## Static And Direct-Container Checks

- Go tests: passed.
- Python tests: passed.
- release source, prompt, skill, and binary verification: passed.
- MCP initialize and tool-list smoke: exactly `AX` and `GUI`.
- release image and source-equivalent direct-container smoke:
  - guest screenshot endpoint ready;
  - source-equivalent image ID:
    `sha256:a82d4de4538a44e52fbccde6c35968e1ec364c238637f44f1ccdcb075a2df573`;
  - Codex `0.151.0` and Claude Code `2.1.251` present in the image;
  - MCP binary version matched;
  - AX application observation succeeded;
  - GUI returned a `1920x1080` screenshot.
- Claude native-schema probe:
  - the unadapted MCP server connected;
  - Claude Code `2.1.251` omitted `AX` and `GUI` from its initialized tools;
  - the adapter-backed QEMU run exposed and used both tools.

The direct-container result is a development diagnostic. It is not
official-QEMU benchmark evidence.

## Official-QEMU Setup Canary

V2 result: `runs/smoke-v2-qemu-20260829/report.json`

- VM startup: first attempt ready.
- task setup: first attempt passed.
- guest root: writable.
- runtime: 4 vCPU, 4 GiB RAM, 32 GiB writable disk layer.
- total duration: 57.6 seconds.

V1 result: `runs/v1-official-qemu-setup-canary-20260829/report.json`

- official V1 qcow2: 50 GiB virtual size, 24,460,197,888-byte file;
- VM startup: first attempt ready;
- task setup: first attempt passed;
- guest root: writable;
- credentials: `user/password`;
- total duration: 43.8 seconds.

## Task Canaries

| Suite | Guest | Agent | Task | Result | Duration | Channels |
|---|---|---|---|---:|---:|---|
| V2 | V2 | Codex 0.151.0, gpt-5.4 xhigh | `001` | `1.0` | 444.6 s | AX 1, GUI 1, shell 19 |
| V1 | V2 | Codex 0.151.0, gpt-5.4 xhigh | `01b269ae-...` | `1.0` | 473.4 s | AX 6, GUI 1, shell 15 |
| V1 | V2 | Claude Code 2.1.251, Opus 5 xhigh | `01b269ae-...` | `1.0` | 250.1 s | AX 1, GUI 4, shell 6 |
| V1 | V1 | Codex 0.151.0, gpt-5.4 xhigh | `01b269ae-...` | `1.0` | 534.4 s | AX 11, GUI 7, shell 15 |

The V2 result and the V1-on-V1 result are valid official-QEMU task canaries.
The V1-on-V2 rows prove compatibility through the unified runner for both
Codex and Claude Code, but they are not exact historical V1 results.

The same V1 task scored `1.0` on both the V1 and V2 guests with Codex,
gpt-5.4 xhigh, Computer Use v0.14, and the same benchmark skill. This confirms
outcome agreement for this one task only. It does not prove guest-wide
behavioral equivalence: the V1 guest needed more AX/GUI calls and more tokens.

## Issues Found And Fixed

### Docker Builder Network

Evidence: the first source-equivalent image build could not use the daemon's
missing default `docker0` bridge.

Root cause: this host's active OSWorld bridge is not Docker's default builder
network.

Fix: `make image-v1` uses `DOCKER_BUILD_NETWORK=host` by default and documents
the override.

### OSS Evaluator Fallback Signature

Evidence: the first V2 task completed agent work but crashed before scoring
with `evaluate_channels() got an unexpected keyword argument 'installer'`.

Root cause: the private evaluator override API accepted `installer`, while the
public OSS fallback did not preserve that call contract.

Fix: the fallback now accepts the same keyword, records the disabled override
reason, and has a focused regression test.

### Shared Fallback Import

Evidence: the first retry failed at process startup with
`ModuleNotFoundError: integrations.common`.

Root cause: the runner added only the OSWorld checkout and integration
subdirectory to `sys.path`, not the repository root.

Fix: the repository root is inserted explicitly and runner `--help` import is
covered by a test using the pinned OSWorld virtual environment.

### V1 Gateway Affinity Label

Evidence: the V1 result metadata used an `osworld-v1-*` affinity ID, but its
Codex gateway URL retained the helper's `osworld-v2-*` default prefix.

Root cause: the new benchmark selector was not passed into the Codex or Claude
gateway endpoint builders. Isolation remained intact because the complete
timestamp/task ID was unique, but proxy accounting could not match the
metadata label.

Fix: the selected benchmark version is now passed explicitly into the gateway
affinity prefix and covered by a focused test.

### Setup Canary V1 Task Resolution

Evidence: the first V1 setup canary failed before container creation because it
looked only in the V2 task directory.

Root cause: `run_setup_canary.py` hard-coded `eval_version="v2"` even though the
unified task loader and full runner already supported V1.

Fix: the canary now accepts `--benchmark-version` and `--task-domain`, records
both in its report, and has a focused CLI regression test.

## Remaining Validation Gaps

- No full 361-task v0.14 round or final audit is claimed.
- These canaries did not run the interactive outcome Judge.
- One passing paired task cannot establish that V1 and V2 guests, or
  direct-container and QEMU backends, are score-equivalent.
