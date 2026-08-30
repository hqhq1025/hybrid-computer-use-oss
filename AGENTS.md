# Hybrid Computer Use OSS Operator Guide

This repository packages Computer Use `v0.14` and the host-side integration
needed to run Codex or Claude Code against OSWorld V1 and V2.

This file is both the repository engineering policy and the canonical operator
runbook. Read the real release manifest, source, image hashes and run output
before making repository-specific claims.

## Safety Boundary

Never copy, mount, summarize or inject this file into an evaluated OSWorld
agent. The evaluated agent receives only:

- the original OSWorld task instruction;
- `skills/base/CODEX_BASE_INSTRUCTIONS.md`;
- one selected benchmark skill, normally `skills/hybrid/SKILL.md`;
- the runner's minimal completion and infeasibility contract.

Do not expose evaluator internals, task recipes, coordinates, credentials,
prior-task traces or cross-task memory to the evaluated agent.

Keep `AX` and `GUI` as the only public Computer Use MCP tools. Bash, file
editing, application APIs and browser/CDP remain valid independent hybrid
channels.

Use `docs/MCP.md` as the MCP build, startup, client-configuration and
troubleshooting entry point. Use `docs/ARCHITECTURE.md` and
`docs/AX_AND_CDP.md` for the internal state model and channel boundaries.

## Supported Runtime Matrix

| Benchmark | Runtime | Guest image | Status |
| --- | --- | --- | --- |
| V1 | official `happysixd/osworld-docker` plus QEMU | official-derived V1 core qcow2 | primary |
| V2 | official `happysixd/osworld-docker` plus QEMU | official-derived V2 full qcow2 | primary |
| V1 | direct desktop container | `hybrid-computer-use:v0.14-source` | diagnostic only |
| V2 | direct desktop container | none | unsupported |

The official source qcow2 files remain immutable. The active V1 and V2 images
are derived from those sources by expanding the root disk and installing
ordinary workstation dependencies. They remain official-QEMU environments,
but they are not byte-identical official source images.

Validated derived images:

| Benchmark | Virtual size | SHA-256 |
| --- | ---: | --- |
| V1 core | 64 GiB | `acb8297f6d5e94a2c57eac6db05cc9822939d2c6389ce52cec5eb93306139479` |
| V2 full | 100 GiB | `76d3eebed863d6834254729c2bd7805d080cfbf3f6de1c4036a78d8b010f404a` |

The official V1 source SHA-256 is
`6bf667a852b3c307f61d9f09c42559351f45e0607e428b4997becf534cf4d313`.

## Frozen v0.14 Inputs

| Input | SHA-256 |
| --- | --- |
| MCP binary | `11adcefd856200eda37ec127b586d95105f1d9b52c64e6ee8ff5571c1f5bacf0` |
| release manifest | `bb83e49ca8c23047e8fc5458d8afb5f2859c9f52f3a6d0988facaf07fdf2e4c0` |
| hybrid skill | `61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16` |
| base instructions | `2db42251f92d78881226342d36eb7a269f58cc63442915e2fa224dcbe14346af` |
| Codex `0.151.0` | `9739cbc928b9c573be83256acd46668f5dd4f119d2d09e05246895ca2aaf0c9a` |
| Claude Code `2.1.251` | `fd5f10ff0eb58daec04900466b143ea98aab50abf208a422bc008eaec13f61f7` |

Do not rewrite a release manifest or silently replace a bound artifact. Use a
new run ID for any image, binary, prompt, skill, model or runner change.

## Host Prerequisites

Required:

- Linux x86-64 with `/dev/kvm`;
- Docker with permission to start privileged QEMU containers;
- `qemu-img`, `git`, Python 3.10 or later and enough free disk space;
- the `happysixd/osworld-docker` image;
- a local OSWorld V2 gated asset snapshot;
- a model gateway reachable from the Docker/QEMU network;
- at least 4 GiB guest RAM per active task, plus host and Docker overhead.

Check the host before setup:

```bash
test -c /dev/kvm
docker image inspect happysixd/osworld-docker >/dev/null
qemu-img --version
df -h .
free -h
```

Start with concurrency `1`. QEMU startup and qcow2 overlays create significant
RAM and disk-I/O pressure.

## Repository And OSWorld Setup

From the repository root:

```bash
export HYBRID_COMPUTER_USE_ROOT="$PWD"
mkdir -p external

git clone https://github.com/xlang-ai/OSWorld-V2 \
  external/OSWorld-V2
git -C external/OSWorld-V2 checkout \
  d578d2d4e0dc82b43e270fdaa7fa89d9708cd154

python3 -m venv external/OSWorld-V2/.venv
external/OSWorld-V2/.venv/bin/pip install -U pip
external/OSWorld-V2/.venv/bin/pip install -e external/OSWorld-V2

python3 scripts/install-osworld-v2.py external/OSWorld-V2
```

Both V1 and V2 official-QEMU runs use this pinned V2 host runner. V1 task JSON
files are loaded from its `evaluation_examples` tree while the selected V1
qcow2 supplies the historical V1 desktop.

Download and verify the pinned agent binaries and build the MCP:

```bash
make agents
make build
make test
make verify
make smoke
```

`make agents` downloads the exact Codex and Claude Code versions declared in
`configs/agent-artifacts.lock.json`.

## Guest Images

Copy the environment template:

```bash
cp configs/env.example .env
```

Set absolute paths in `.env`. At minimum:

```bash
export HYBRID_COMPUTER_USE_ROOT="/absolute/path/to/hybrid-computer-use-oss"
export OSWORLD_V2_ROOT="$HYBRID_COMPUTER_USE_ROOT/external/OSWorld-V2"
export HCU_ARTIFACT_DIR="$HYBRID_COMPUTER_USE_ROOT/artifacts"
export HCU_RESULTS_DIR="$HYBRID_COMPUTER_USE_ROOT/runs"

export OSWORLD_FILE_BASE_URL="/absolute/path/to/osworld-v2-assets"

export OSWORLD_V1_GUEST_IMAGE="/absolute/path/to/v1-core-64g.qcow2"
export OSWORLD_V1_GUEST_IMAGE_SHA256="acb8297f6d5e94a2c57eac6db05cc9822939d2c6389ce52cec5eb93306139479"
export OSWORLD_V1_VOLUME_SIZE=64
export OSWORLD_V1_CLIENT_PASSWORD=password

export OSWORLD_V2_GUEST_IMAGE="/absolute/path/to/v2-full-100g.qcow2"
export OSWORLD_V2_GUEST_IMAGE_SHA256="76d3eebed863d6834254729c2bd7805d080cfbf3f6de1c4036a78d8b010f404a"
export OSWORLD_V2_VOLUME_SIZE=100
export OSWORLD_V2_CLIENT_PASSWORD=osworld-public-evaluation
```

Load it with:

```bash
set -a
source .env
set +a
```

Do not request a smaller `--volume-size` than the qcow2 virtual capacity. The
validated V1 image requires `64`; the validated V2 image requires `100`.

To rebuild the dependency images from immutable official sources, follow
`docs/GUEST_DEPENDENCIES.md`. Do not modify the official source qcow2 in place.

## Model Gateways

The guest must reach the gateway through a Docker bridge address. A listener
on host loopback alone is insufficient.

Set the reachable address:

```bash
export HCU_DOCKER_GATEWAY="<docker-bridge-gateway-ip>"
export HCU_OPENAI_GATEWAY="http://${HCU_DOCKER_GATEWAY}:8600"
export HCU_ANTHROPIC_GATEWAY="http://${HCU_DOCKER_GATEWAY}:8537"
```

Verify the selected address belongs to a live host interface and confirm the
listener after starting the proxy:

```bash
ip -4 addr
ss -ltnp | grep -E ':(8600|8537)\b'
```

### Codex

`integrations/common/openai_proxy.py` is an Azure OpenAI Responses passthrough
with task affinity and token accounting. It requires a working Azure CLI login:

```bash
az account get-access-token \
  --resource https://cognitiveservices.azure.com \
  >/dev/null

python3 integrations/common/openai_proxy.py \
  --host "$HCU_DOCKER_GATEWAY" \
  --ports 8600 \
  --out "$HCU_RESULTS_DIR/openai-tokens.jsonl" \
  --max-requests-per-task 200 \
  --model-upstreams \
  "gpt-5.5=<azure-host-1>,<azure-host-2>"
```

Only pass upstream hostnames to `--model-upstreams`, without a scheme or URL
path. The runner appends a task-scoped `/t/<affinity-id>/v1` path.

Alternatively, point `HCU_OPENAI_GATEWAY` at another
Responses-compatible gateway that is reachable from the guest and preserves
`previous_response_id` affinity.

### Claude Code

Configure an Anthropic-compatible upstream without storing secrets in the
repository:

```bash
export OSW_ANTHROPIC_UPSTREAM_BASE="https://<anthropic-compatible-host>"
export OSW_ANTHROPIC_UPSTREAM_TOKEN="<secret>"

python3 integrations/common/anthropic_proxy.py \
  --host "$HCU_DOCKER_GATEWAY" \
  --port 8537 \
  --out "$HCU_RESULTS_DIR/anthropic-tokens.jsonl" \
  --max-requests-per-task 200
```

## Preflight

Run this before a real task:

```bash
test -x "$OSWORLD_V2_ROOT/.venv/bin/python"
test -d "$OSWORLD_FILE_BASE_URL/task_001"
test -f "$OSWORLD_V1_GUEST_IMAGE"
test -f "$OSWORLD_V2_GUEST_IMAGE"

echo "$OSWORLD_V1_GUEST_IMAGE_SHA256  $OSWORLD_V1_GUEST_IMAGE" |
  sha256sum -c -
echo "$OSWORLD_V2_GUEST_IMAGE_SHA256  $OSWORLD_V2_GUEST_IMAGE" |
  sha256sum -c -

qemu-img info "$OSWORLD_V1_GUEST_IMAGE"
qemu-img info "$OSWORLD_V2_GUEST_IMAGE"

make verify
```

The runner also validates image, MCP, release, skill, base-instruction and
agent hashes before task execution.

## Setup Canaries

Use setup canaries before spending model tokens.

V1:

```bash
"$OSWORLD_V2_ROOT/.venv/bin/python" \
  integrations/osworld-v2/hybrid/run_setup_canary.py \
  --benchmark-version v1 \
  --task-domain libreoffice_calc \
  --task-id 01b269ae-2111-4a07-81fd-3fcd711993b0 \
  --guest-image "$OSWORLD_V1_GUEST_IMAGE" \
  --expected-guest-image-sha256 "$OSWORLD_V1_GUEST_IMAGE_SHA256" \
  --volume-size "$OSWORLD_V1_VOLUME_SIZE" \
  --ram-size 4G \
  --client-password "$OSWORLD_V1_CLIENT_PASSWORD" \
  --result-dir "$HCU_RESULTS_DIR/v1-setup-$(date -u +%Y%m%dT%H%M%SZ)"
```

V2:

```bash
"$OSWORLD_V2_ROOT/.venv/bin/python" \
  integrations/osworld-v2/hybrid/run_setup_canary.py \
  --benchmark-version v2 \
  --task-id 001 \
  --guest-image "$OSWORLD_V2_GUEST_IMAGE" \
  --expected-guest-image-sha256 "$OSWORLD_V2_GUEST_IMAGE_SHA256" \
  --volume-size "$OSWORLD_V2_VOLUME_SIZE" \
  --ram-size 4G \
  --client-password "$OSWORLD_V2_CLIENT_PASSWORD" \
  --result-dir "$HCU_RESULTS_DIR/v2-setup-$(date -u +%Y%m%dT%H%M%SZ)"
```

Expected evidence: first-attempt VM readiness, first-attempt setup success and
a writable guest root.

## Common Runner Binding

The following values should be explicit in reproducible runs:

```bash
RUNNER="$HYBRID_COMPUTER_USE_ROOT/integrations/osworld-v2/hybrid/run_hybrid.py"
PYTHON="$OSWORLD_V2_ROOT/.venv/bin/python"
SKILL="$HYBRID_COMPUTER_USE_ROOT/skills/hybrid/SKILL.md"
BASE_PROMPT="$HYBRID_COMPUTER_USE_ROOT/skills/base/CODEX_BASE_INSTRUCTIONS.md"
RELEASE="$HCU_ARTIFACT_DIR/releases/v0.14/release.json"

SKILL_SHA=61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16
BASE_PROMPT_SHA=2db42251f92d78881226342d36eb7a269f58cc63442915e2fa224dcbe14346af
RELEASE_SHA=bb83e49ca8c23047e8fc5458d8afb5f2859c9f52f3a6d0988facaf07fdf2e4c0
```

Every result directory must be new. The runner deliberately refuses to write
into an existing result directory.

## Run One V1 Task

Codex:

```bash
RUN_ID="v1-$(date -u +%Y%m%dT%H%M%SZ)-<task-id>"

"$PYTHON" "$RUNNER" \
  --benchmark-version v1 \
  --task-id "<task-uuid>" \
  --task-domain "<optional-domain>" \
  --agent codex \
  --model gpt-5.5 \
  --effort xhigh \
  --gateway-base "$HCU_OPENAI_GATEWAY" \
  --artifact-dir "$HCU_ARTIFACT_DIR" \
  --guest-image "$OSWORLD_V1_GUEST_IMAGE" \
  --expected-guest-image-sha256 "$OSWORLD_V1_GUEST_IMAGE_SHA256" \
  --volume-size "$OSWORLD_V1_VOLUME_SIZE" \
  --ram-size 4G \
  --client-password "$OSWORLD_V1_CLIENT_PASSWORD" \
  --skill-file "$SKILL" \
  --expected-skill-sha256 "$SKILL_SHA" \
  --base-instructions-file "$BASE_PROMPT" \
  --expected-base-instructions-sha256 "$BASE_PROMPT_SHA" \
  --tool-version v0.14 \
  --release-manifest "$RELEASE" \
  --expected-release-manifest-sha256 "$RELEASE_SHA" \
  --max-model-calls 200 \
  --timeout 0 \
  --judge-model gpt-5.4 \
  --judge-effort xhigh \
  --judge-timeout 600 \
  --judge-tool-limit 30 \
  --judge-retry-tool-limit 15 \
  --token-log "$HCU_RESULTS_DIR/$RUN_ID.tokens.jsonl" \
  --result-dir "$HCU_RESULTS_DIR/osworld-v1/$RUN_ID"
```

Claude Code uses the same command with:

```text
--agent claude-code
--model claude-opus-5
--claude-gateway-base "$HCU_ANTHROPIC_GATEWAY"
```

Keep `--gateway-base` configured as well because the independent Judge uses
Codex even when the Task Agent is Claude Code.

## Run One V2 Task

Codex:

```bash
RUN_ID="v2-$(date -u +%Y%m%dT%H%M%SZ)-<task-id>"

"$PYTHON" "$RUNNER" \
  --benchmark-version v2 \
  --task-id "<task-id>" \
  --agent codex \
  --model gpt-5.5 \
  --effort xhigh \
  --gateway-base "$HCU_OPENAI_GATEWAY" \
  --artifact-dir "$HCU_ARTIFACT_DIR" \
  --guest-image "$OSWORLD_V2_GUEST_IMAGE" \
  --expected-guest-image-sha256 "$OSWORLD_V2_GUEST_IMAGE_SHA256" \
  --volume-size "$OSWORLD_V2_VOLUME_SIZE" \
  --ram-size 4G \
  --client-password "$OSWORLD_V2_CLIENT_PASSWORD" \
  --skill-file "$SKILL" \
  --expected-skill-sha256 "$SKILL_SHA" \
  --base-instructions-file "$BASE_PROMPT" \
  --expected-base-instructions-sha256 "$BASE_PROMPT_SHA" \
  --tool-version v0.14 \
  --release-manifest "$RELEASE" \
  --expected-release-manifest-sha256 "$RELEASE_SHA" \
  --max-model-calls 200 \
  --timeout 0 \
  --judge-model gpt-5.4 \
  --judge-effort xhigh \
  --judge-timeout 600 \
  --judge-tool-limit 30 \
  --judge-retry-tool-limit 15 \
  --token-log "$HCU_RESULTS_DIR/$RUN_ID.tokens.jsonl" \
  --result-dir "$HCU_RESULTS_DIR/osworld-v2/$RUN_ID"
```

Claude Code again changes the Task Agent fields to:

```text
--agent claude-code
--model claude-opus-5
--claude-gateway-base "$HCU_ANTHROPIC_GATEWAY"
```

V1 and V2 use different guest passwords:

```text
V1: password
V2: osworld-public-evaluation
```

This is an upstream OSWorld guest-account contract, not a dependency-image
credential and not a model API secret. Official OSWorld passes
`client_password` into `DesktopEnv` and `SetupController` for setup operations
that require sudo. The hybrid overlay also uses it for root-volume expansion,
runtime timezone alignment and creation of isolated agent runtime directories.

Using the wrong password fails during setup or guest clock alignment before
the task begins.

## Batch Runs

The example queue files support environment-variable expansion:

```bash
"$PYTHON" integrations/osworld-v2/run_batch.py \
  --config configs/osworld-v1-queue.example.json \
  --concurrency 1 \
  --dry-run

"$PYTHON" integrations/osworld-v2/run_batch.py \
  --config configs/osworld-v2-queue.example.json \
  --concurrency 1 \
  --dry-run
```

Inspect every generated command, then remove `--dry-run`. Give every
experiment a unique `run_id` and token log. Do not mix different guest images,
MCP binaries, skills, prompts, agent versions or model routes in one round.

The simple batch runner is intended for bounded local batches. The stricter
`integrations/osworld-v2/hybrid/run_queue.py` additionally supports immutable
run bindings, host-pressure gates and paired experiments.

## Agent Profiles

Codex receives:

- shell and file tools;
- built-in browser/CDP support;
- `computer_use.AX` and `computer_use.GUI`;
- the replacement base instructions;
- the selected benchmark skill through task-local `/home/user/AGENTS.md`.

Claude Code receives:

- `Bash`, `Edit` and `Read`;
- `mcp__computer_use__AX` and `mcp__computer_use__GUI`;
- an isolated config directory and strict MCP configuration;
- the schema adapter required by Claude Code `2.1.251`.

The benchmark configuration disables web search, plugins, memories, subagents,
hooks, update checks and other ambient features that could alter isolation or
introduce cross-task state. See `docs/AGENT_PROFILES.md`.

## Result Interpretation

The primary files are:

```text
<result-dir>/result.json
<result-dir>/meta.json
<result-dir>/agent.jsonl
<result-dir>/task-agent-token-usage.jsonl
<result-dir>/raw_evaluator.json
<result-dir>/judge/verdict.json
<result-dir>/cleanup.json
```

Check at least:

```bash
jq '{
  score,
  duration_sec,
  termination_reason,
  truncated,
  trace_summary,
  judge: {
    verdict: .live_judge.verdict.instruction_verdict,
    summary: .live_judge.verdict.summary
  }
}' "<result-dir>/result.json"
```

RAW evaluator output and the interactive outcome Judge are separate evidence.
Do not rewrite RAW scores. A truncated run is an error even if a narrow
evaluator happens to pass.

## Known Operational Traps

- Pulling `happysixd/osworld-docker` does not provide the desktop qcow2.
- A V1 task on a V2 guest is compatibility evidence, not an exact V1 result.
- V1 and V2 passwords differ.
- The volume size may not be smaller than the selected qcow2 capacity.
- Host-loopback gateways are not automatically reachable from QEMU guests.
- `OSWORLD_FILE_BASE_URL` must be an absolute local snapshot and contain
  `task_001`.
- The pinned OSWorld overlay must be reinstalled after replacing or cleaning
  the external checkout.
- Result directories are immutable per attempt.
- `AGENTS.md` in this repository is never the evaluated-agent skill.
- Direct-container V1 results are diagnostics, not official-QEMU results.
- Dependency availability does not guarantee task correctness. Inspect the
  artifact and Judge result separately.

## Change And Validation Policy

For MCP behavior, image content, tool descriptions, runtime dependencies or
security-boundary changes:

1. create or update the matching change record;
2. build a new immutable artifact or image identity;
3. use a new run ID;
4. run focused tests and real-container or real-QEMU smoke checks;
5. record gains, regressions and unresolved risks;
6. do not assign a new product version until promotion is explicitly accepted.

Before reporting repository changes, run:

```bash
make test
make verify
make smoke
```

For guest-image changes, also run the profile verifier and a real QEMU setup
canary. For runner changes, run `pytest -q tests/test_qemu_runner_config.py`
and batch `--dry-run` for both benchmark versions.
