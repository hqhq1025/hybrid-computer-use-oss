# Running OSWorld

`hcu.osworld.run_task` runs one OSWorld (V1) or OSWorld-V2 (V2) task in a
fresh official Docker/QEMU guest with Codex or Claude Code as the task agent.
Both benchmark generations use a pinned OSWorld-V2 checkout as the host-side
runner: V1 task files come from its `evaluation_examples` tree, and the guest
image you select supplies the V1 or V2 desktop.

## What one task run does

1. Starts `happysixd/osworld-docker` with a disposable overlay on the guest
   qcow2 image and grows the guest root disk to `--volume-size` GiB.
2. Removes inherited `.codex`, `.claude`, `.claude.json`, `AGENTS.md` and
   `CLAUDE.md` state from the guest.
3. Uploads the agent CLI, `computer-use`, the agent configuration, the base
   instructions and the skill.
4. Runs the task setup, then checks the model gateway, the uploaded
   executables and the MCP tool list.
5. Runs the agent inside the guest desktop session until it finishes, reports
   the task infeasible, or reaches the model-call budget or the timeout.
6. Runs the official evaluator.
7. Writes the result files and removes the container, or only stops it with
   `--retain-container`.

One run owns one guest, one agent process, one MCP process and one result
directory. Nothing is shared between tasks.

## V1 and V2 differences

| | V1 (OSWorld) | V2 (OSWorld-V2) |
| --- | --- | --- |
| `--benchmark-version` | `v1` | `v2` |
| Task IDs | UUIDs; `--task-domain` is optional when the UUID is unique | `001` to the last task number |
| Guest image | official `Ubuntu.qcow2` or a `v1-core` dependency image | official V2 image or a `v2-full` dependency image |
| `--client-password` | `password` | `osworld-public-evaluation` (the default) |
| `--volume-size` | 50 for the official image, 64 for `v1-core` | 100 for `v2-full` |

`--client-password` is the guest user's sudo password from the upstream
OSWorld image, not a secret. Setup uses it for privileged commands, and the
runner uses it to grow the root disk, align the guest clock and create the
agent's runtime directories, so a wrong password fails before the task starts.

`--volume-size` may not be smaller than the virtual size of the guest image.
Running a V1 task on a V2 guest works, but the result is compatibility
evidence, not a V1 result.

## Host requirements

- Linux x86-64 with `/dev/kvm`.
- Docker with permission to start privileged QEMU containers, and the
  `happysixd/osworld-docker` image. The image is only the QEMU launcher; it
  does not contain a desktop disk.
- `qemu-img`, `git` and Python 3.10 or later.
- At least 4 GiB of RAM per running task plus host and Docker overhead, and
  disk space for one overlay per running task.

```bash
test -c /dev/kvm
docker image inspect happysixd/osworld-docker >/dev/null
qemu-img --version
free -h
df -h .
```

Start with one task at a time. QEMU startup and qcow2 overlays put
considerable pressure on RAM and disk I/O.

## Install

From the repository root:

```bash
git clone https://github.com/xlang-ai/OSWorld-V2 external/OSWorld-V2
git -C external/OSWorld-V2 checkout d578d2d4e0dc82b43e270fdaa7fa89d9708cd154

python3 -m venv external/OSWorld-V2/.venv
external/OSWorld-V2/.venv/bin/pip install -U pip
external/OSWorld-V2/.venv/bin/pip install -e external/OSWorld-V2

python3 scripts/install-osworld-v2.py external/OSWorld-V2
make agents
make build
```

`install-osworld-v2.py` copies `osworld-overlay/` into the checkout. It
refuses other OSWorld-V2 revisions unless you pass `--force`; review the
upstream changes to the patched files first. Reinstall the overlay whenever
you replace or clean the checkout.

`make agents` downloads the agent releases and the Node runtime pinned in
`configs/agent-artifacts.lock.json` and verifies their archive checksums. To
use another build during development, pass `--agent-binary` to `run_task` (or
`agent_binary` in a batch or queue config); it replaces the agent's file under
the artifact directory.

Run every `hcu.osworld` command from the repository root with the OSWorld-V2
virtual environment, so that both `hcu` and `desktop_env` are importable.
Relative path arguments are resolved against your working directory.

## Guest images and task assets

- V1: `scripts/fetch-osworld-v1-image.sh DIR` downloads the official
  `Ubuntu.qcow2.zip` from a pinned Hugging Face revision, verifies the archive
  checksum and extracts the image.
- V2 task definitions: the OSWorld-V2 task classes (`task_*.py`) are gated and
  not part of the GitHub checkout. Accept the terms of the Hugging Face
  datasets `xlangai/osworld_v2_tasks` and `xlangai/osworld_v2_assets_gated`,
  then run from the checkout:

  ```bash
  cd external/OSWorld-V2
  uvx --from huggingface_hub hf auth login
  uv run scripts/tools/download_osworld_v2_tasks.py \
    --benchmark-release osworld-v2-2026.08.08
  uv run scripts/tools/download_osworld_v2_assets.py \
    --benchmark-release osworld-v2-2026.08.08 \
    --target-dir cache/osworld_v2_assets --clean
  ```

  The first script writes `evaluation_examples/task_class/task_*.py`; without
  it, V2 task lookup fails with a missing `examples_v2_backup/<id>.json`.
- Task assets: the runner requires `OSWORLD_FILE_BASE_URL` to point at the
  local asset snapshot (a directory containing `task_001`, for example
  `external/OSWorld-V2/cache/osworld_v2_assets`) for V1 and V2 runs alike, so
  that no task resolves assets online.
- V2 guest image and task websites: follow the OSWorld-V2 README for the
  guest image and for the websites that some tasks use.
- Dependency images: [Guest dependencies](GUEST_DEPENDENCIES.md) describes how
  to derive `v1-core` and `v2-full` images that add common workstation
  packages to the official images. Never modify an official image in place.

## Environment

Copy the template and fill in absolute paths:

```bash
cp configs/env.example .env
set -a; source .env; set +a
```

| Variable | Meaning |
| --- | --- |
| `HYBRID_COMPUTER_USE_ROOT` | repository root |
| `OSWORLD_V2_ROOT` | OSWorld-V2 checkout; defaults to `external/OSWorld-V2` |
| `HCU_ARTIFACT_DIR` | agent CLIs and `computer-use`; defaults to `artifacts/` |
| `HCU_RESULTS_DIR` | default result root; defaults to `runs/` |
| `OSWORLD_FILE_BASE_URL` | local task asset snapshot |
| `OSWORLD_V1_GUEST_IMAGE`, `OSWORLD_V2_GUEST_IMAGE` | guest images, used by the example configs |
| `HCU_OPENAI_GATEWAY`, `HCU_ANTHROPIC_GATEWAY`, `HCU_GEMINI_GATEWAY` | gateway base URLs reachable from the guest |
| `OSWORLD_V2_REQUIRE_DEDICATED_DOCKER` | set to `1` to refuse any Docker daemon other than `OSWORLD_V2_DOCKER_HOST` |

## Model gateways

Agents inside the guest never receive model credentials. They send requests
to a gateway on the host at `<gateway>/t/<task-id>/...`; the gateway adds the
operator's credential, records one JSONL row per request and enforces the
per-task request budget. The runner reads the token log back to report usage
and to stop an agent that exhausted its budget.

The guest reaches the host through the Docker bridge, so bind the gateway to
the bridge gateway address (commonly `172.17.0.1`), not to loopback. Restrict
that address with a host firewall; the gateway spends your model quota for
anyone who can reach it.

### OpenAI Responses (Codex)

`hcu.gateway.openai_proxy` forwards `/v1/responses` requests to Azure OpenAI
with an Azure CLI token, so run `az login` first. It pins each task to one
upstream for the whole run, because `previous_response_id` only resolves in the
region that produced it, and it never retries or switches upstreams.

```bash
python3 -m hcu.gateway.openai_proxy \
  --host 172.17.0.1 --ports 8611 \
  --out "$HCU_RESULTS_DIR/openai-tokens.jsonl" \
  --upstreams <resource>.openai.azure.com \
  --max-requests-per-task 200
```

Upstreams are host names without a scheme or path. Any other
Responses-compatible endpoint that keeps `previous_response_id` working and
accepts the `/t/<task-id>/v1` prefix can replace this proxy, but the runner's
budget enforcement reads the proxy's token log.

An OpenAI-compatible upstream that accepts the Responses API needs no Azure
token. `--upstream-kind openai` keeps the request path unchanged and sends a
bearer token, which is how Grok Build's model is served:

```bash
OSW_OPENAI_UPSTREAM_TOKEN="<secret>" \
python3 -m hcu.gateway.openai_proxy \
  --host 172.17.0.1 --ports 8611 \
  --out "$HCU_RESULTS_DIR/openai-tokens.jsonl" \
  --upstream-kind openai --upstreams api.x.ai \
  --max-requests-per-task 200
```

### Anthropic Messages (Claude Code)

```bash
export OSW_ANTHROPIC_UPSTREAM_BASE="https://<anthropic-compatible-host>"
export OSW_ANTHROPIC_UPSTREAM_TOKEN="<secret>"
python3 -m hcu.gateway.anthropic_proxy \
  --host 172.17.0.1 --port 8537 \
  --out "$HCU_RESULTS_DIR/anthropic-tokens.jsonl" \
  --max-requests-per-task 200
```

### Gemini API (Gemini CLI)

Gemini CLI speaks Google's `generateContent` protocol with a placeholder key in
`x-goog-api-key`, so it needs its own gateway:

```bash
export OSW_GEMINI_UPSTREAM_TOKEN="<secret>"
python3 -m hcu.gateway.gemini_proxy \
  --host 172.17.0.1 --port 8571 \
  --out "$HCU_RESULTS_DIR/gemini-tokens.jsonl" \
  --max-requests-per-task 200
```

`--upstream-base` defaults to `https://generativelanguage.googleapis.com`; any
Gemini-compatible origin works. Request paths under `/v1beta` are forwarded
unchanged, and the gateway budgets generation calls only: token counting does
not use up a step.

### Budgets

Only task agents are budgeted, identified by the `task-` prefix of their
affinity ID; other traffic is not. The OpenAI gateway counts admitted
requests. The Anthropic and Gemini gateways count successful responses that
reached the agent, so an upstream failure or a dropped connection does not use
up a step. Every gateway rebuilds its counters from the token log when it
restarts, so keep one log per experiment.

The runner derives its default token log from the agent's gateway
(`<gateway>-tokens.jsonl` under the results root), and `--token-log` overrides
it.

## Setup canaries

A setup canary starts the guest and runs the task setup without an agent. Use
one before spending model quota:

```bash
PY=external/OSWorld-V2/.venv/bin/python

$PY -m hcu.osworld.setup_canary \
  --benchmark-version v1 \
  --task-id 01b269ae-2111-4a07-81fd-3fcd711993b0 \
  --task-domain libreoffice_calc \
  --guest-image "$OSWORLD_V1_GUEST_IMAGE" \
  --volume-size 64 --ram-size 4G \
  --client-password password \
  --result-dir "runs/setup-v1-$(date -u +%Y%m%dT%H%M%SZ)"

$PY -m hcu.osworld.setup_canary \
  --benchmark-version v2 \
  --task-id 001 \
  --guest-image "$OSWORLD_V2_GUEST_IMAGE" \
  --volume-size 100 --ram-size 4G \
  --result-dir "runs/setup-v2-$(date -u +%Y%m%dT%H%M%SZ)"
```

A healthy canary reports a ready VM and a successful setup on the first
attempt and a writable guest root. `--run-evaluator` also runs the evaluator
on the untouched setup state. `hcu.osworld.setup_canary_queue` runs canaries
for many tasks with bounded concurrency.

## Run one task

Codex on a V2 task:

```bash
$PY -m hcu.osworld.run_task \
  --benchmark-version v2 \
  --task-id 001 \
  --agent codex --model gpt-5.5 --effort xhigh \
  --gateway-base "$HCU_OPENAI_GATEWAY" \
  --guest-image "$OSWORLD_V2_GUEST_IMAGE" \
  --volume-size 100 --ram-size 4G \
  --max-model-calls 200 \
  --token-log "$HCU_RESULTS_DIR/openai-tokens.jsonl" \
  --result-dir "runs/osworld-v2/001-$(date -u +%Y%m%dT%H%M%SZ)"
```

For a V1 task, pass `--benchmark-version v1`, the task UUID, the V1 image,
its volume size and `--client-password password`.

For the other agents, change the agent options and point the matching gateway
option and token log:

```text
--agent claude-code --model claude-opus-5 --claude-gateway-base "$HCU_ANTHROPIC_GATEWAY"
--agent gemini-cli  --claude-gateway-base ignored --gemini-gateway-base "$HCU_GEMINI_GATEWAY"
--agent grok-build  --gateway-base "$HCU_OPENAI_GATEWAY"
```

`--model` and `--effort` default to the agent's own values (see
[Agent profiles](AGENT_PROFILES.md)), so they can be omitted. Grok Build cannot
resume a session, and Gemini CLI needs the Node runtime from `make agents`;
both are described there.

Other useful options:

| Option | Default | Effect |
| --- | --- | --- |
| `--skill-file` | `skills/hybrid/SKILL.md` | skill given to the agent; an empty string gives no skill |
| `--channels` | both | `a11y,keyboard` exposes only `AX`, `gui,keyboard` only `GUI`; the other tool is absent from the agent's tool list |
| `--base-instructions-file` | `skills/base/CODEX_BASE_INSTRUCTIONS.md` | replacement base instructions |
| `--mcp-binary` | `$HCU_ARTIFACT_DIR/computer-use` | MCP server uploaded to the guest |
| `--max-model-calls` | `0` | task-agent request budget; `0` disables it, so set it for real runs |
| `--timeout` | `0` | extra wall-clock limit per phase in seconds; `0` leaves termination to the model-call budget |
| `--volume-size`, `--ram-size` | `40`, `4G` | guest root disk in GiB and guest RAM |
| `--retain-container` | off | keep the stopped container for inspection |
| `--enable-vnc`, `--enable-recording` | off | noVNC access and a screen recording |

Every run needs a new result directory; the runner refuses to write into an
existing one.

## Batches

`hcu.osworld.run_batch` runs the tasks of one JSON config with bounded
concurrency, one `run_task` process per task:

```bash
$PY -m hcu.osworld.run_batch --config configs/osworld-v2-queue.example.json \
  --concurrency 1 --dry-run
```

`--dry-run` validates the config and prints the generated commands; remove it
to launch. Config keys map to `run_task` options (`gateway_base`,
`guest_image`, `max_model_calls`, `channels`, `no_skill`, and so on), string values may reference
environment variables such as `${HCU_ARTIFACT_DIR}`, and each task may
override `volume_size`, `ram_size` and `task_domain`. Unknown keys are
rejected.

`hcu.osworld.run_queue --queue CONFIG` reads the same config and adds a
rolling queue with host-safety limits: memory, disk, inode, load and I/O
pressure thresholds, launch staggering, a rate-limit circuit breaker, and
restart recovery that adopts live attempts, re-queues interrupted ones and
renders `QUEUE_STATUS.md`. Concurrency is `max_active_tasks`. The queue-only
keys are listed in `hcu/osworld/batch_config.py`.

Give every experiment its own `run_id` and token log, and do not mix guest
images, MCP binaries, skills, prompts, agent versions or model routes in one
round.

## Results

A result directory contains, among others:

| File | Contents |
| --- | --- |
| `result.json` | score, termination reason, truncation, trace summary and evaluator status |
| `meta.json` | run configuration and status transitions |
| `agent.jsonl` | the agent's event stream |
| `task-agent-token-usage.jsonl` | the task agent's rows from the token log |
| `raw_evaluator.json` | the official evaluator's output |
| `phase-*.png`, `final-*.png` | screenshots before and after evaluation |
| `cleanup.json` | container cleanup record |

```bash
jq '{score, termination_reason, truncated, raw_evaluator_status}' <result-dir>/result.json
```

Report the evaluator score unchanged. A truncated run, one that hit the budget
or the timeout, counts as a failure even if the evaluator happens to pass.

## Operational notes

- Host loopback is not reachable from the guest. Use the bridge address for
  gateways and check it with `ss -ltnp`.
- `OSWORLD_FILE_BASE_URL` must be an absolute local directory containing
  `task_001`.
- Hosts whose Docker daemon lost its default `docker0` bridge can still build
  the release binary: `make build` uses `--network none`.
- A passing evaluator does not prove that the task was done as asked; read
  the trace and the artifacts as well.
- This repository's `AGENTS.md` is written for developers and must never
  reach an evaluated agent.
