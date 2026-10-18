# Running OSWorld

`hcu.osworld.run_task` runs one OSWorld (V1) or OSWorld-V2 (V2) task in a
fresh official Docker/QEMU guest with Codex, Claude Code, Gemini CLI or Grok
Build as the task agent.
Both benchmark generations use an OSWorld-V2 checkout as the host-side
runner: V1 task files come from its `evaluation_examples` tree, and the guest
image you select supplies the V1 or V2 desktop.

## What one task run does

1. Starts `happysixd/osworld-docker` with a disposable overlay on the guest
   qcow2 image and expands the guest root partition and filesystem within the
   image's existing virtual capacity.
2. Removes inherited agent configuration and context from the guest, including
   `.codex`, `.claude`, `.gemini`, `.grok`, `.mcp.json`, `AGENTS.md`,
   `CLAUDE.md` and `GEMINI.md` (see [Isolation](ISOLATION.md)).
3. Uploads the agent CLI, `computer-use`, the agent configuration, the base
   policy and the selected interaction guide as plain text.
4. Runs the task setup, then checks the model gateway, the uploaded
   executables and the MCP tool list.
5. Runs the agent inside the guest desktop session until it finishes, reports
   the task infeasible, or reaches the model-call budget or the timeout.
6. Runs the official evaluator.
7. Writes the result files and removes the container, or only stops it with
   `--retain-container`.

One run owns one guest, one agent process, one MCP process and one result
directory. Nothing is shared between tasks.

The base file defines common safety and outcome policy; the selected profile
guides interaction. The runner adds `TASK_COMPLETE`, `TASK_INFEASIBLE` and `ASK_USER`
markers separately from those product instructions. See
[Instructions](INSTRUCTIONS.md) for selection and injection routes.

## V1 and V2 differences

| | V1 (OSWorld) | V2 (OSWorld-V2) |
| --- | --- | --- |
| `--benchmark-version` | `v1` | `v2` |
| Task IDs | UUIDs; `--task-domain` is optional when the UUID is unique | `001` to the last task number |
| Guest image | the official `Ubuntu.qcow2` | the official OSWorld-V2 image |
| `--client-password` | `password` | `osworld-public-evaluation` (the default) |
| `--volume-size` | 64 in the example configuration | 100 in the example configuration |

`--client-password` is the guest user's sudo password from the upstream
OSWorld image, not a secret. Setup uses it for privileged commands, and the
runner uses it to expand the root filesystem, align the guest clock and create the
agent's runtime directories, so a wrong password fails before the task starts.

`--volume-size` must not exceed the virtual capacity of the guest image.
It declares the required root-volume size class, not a qcow2 resize request.
Setup expands the partition and filesystem into the available space and
verifies the root filesystem against that size class, allowing 3 GiB for
partition and filesystem overhead. A smaller value does not shrink the disk
or cap the expansion. To request a larger volume, first provide a guest image
with sufficient virtual capacity; the runner does not enlarge qcow2 images.
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
make build-local
```

The checkout above is the known upstream source reference, not an installation
admission check. `install-osworld-v2.py` checks the required upstream structure
before copying `osworld-overlay/` into the checkout; it does not compare Git
revisions. Review upstream changes to the patched files before using a different
checkout. Structural compatibility does not prove evaluator equivalence.
Reinstall the overlay whenever you replace or clean the checkout.

Place the agent CLIs under `artifacts/`, or pass `--agent-binary` to `run_task`
(or `agent_binary` in a batch or queue config).

Run every `hcu.osworld` command from the repository root with the OSWorld-V2
virtual environment, so that both `hcu` and `desktop_env` are importable.
Relative path arguments are resolved against your working directory.

## Guest images and task assets

- V1: download and unzip the official `Ubuntu.qcow2.zip` from the
  `xlangai/ubuntu_osworld` Hugging Face dataset.
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
- V1 setup assets: set `OSWORLD_V1_ASSET_ROOT` to an absolute local directory.
  V1 does not require V2 gated assets or a `task_001` directory.
- V2 setup assets: set `OSWORLD_FILE_BASE_URL` to the absolute local gated
  snapshot, for example `external/OSWorld-V2/cache/osworld_v2_assets`.
- V2 guest image and task websites: follow the OSWorld-V2 README for the
  guest image and for the websites that some tasks use.

### Prepare Selected Setup Assets

After installing the overlay, prepare the exact dependencies before a canary
or a model run. Downloading is an explicit preparation action, never a silent
fallback during setup:

```bash
PY=external/OSWorld-V2/.venv/bin/python
export OSWORLD_V1_ASSET_ROOT=/absolute/path/to/v1-assets
$PY -m hcu.osworld.prepare_assets \
  --benchmark-version v1 \
  --task-id 01b269ae-2111-4a07-81fd-3fcd711993b0 \
  --task-domain libreoffice_calc \
  --asset-root "$OSWORLD_V1_ASSET_ROOT" --download
```

For every task in a prepared V1 experiment, use the same config as the runner:

```bash
$PY -m hcu.osworld.prepare_assets \
  --config "$HCU_EXPERIMENT_DIR/config.json" \
  --asset-root "$OSWORLD_V1_ASSET_ROOT" --download
```

Config mode takes benchmark version and task domains from the config, prepares
in selection order, reports every failed task and exits nonzero if any fail.
It cannot be combined with single-task selection or `--sources-file`; prepare
dynamic task inventories individually first. Use the V2 root for a V2 config.

Without `--download`, the command cannot download missing HTTP sources; it
still checks local files and writes `setup-assets.json`, so it is not a
read-only command. No Docker guest or model is started.

For V2, first obtain the gated snapshot, set `OSWORLD_FILE_BASE_URL`, then use
`--benchmark-version v2 --task-id 001 --asset-root "$OSWORLD_FILE_BASE_URL"`.
Static file declarations are inspected without executing task setup.
V1 dependencies include both initial setup and evaluator `postconfig` retrieval.
Automatic manifests record resolved absolute local files, so checkout-relative
task paths are not reinterpreted under the asset root. Reprepare dependencies
after relocating the checkout or its local files.
Runtime-computed dependencies and directory declarations need an explicit
`--sources-file SOURCES.json`: a JSON array of exact source URLs, absolute
local filenames, or filenames relative to the asset root. This inventory
records setup dependencies, not task solutions. An empty directory is not
proof of completeness.

The command records each task under `tasks["v1:<id>"]` or `tasks["v2:<id>"]` in
`setup-assets.json`, with mappings for downloaded HTTP sources. It updates the
manifest atomically and rejects concurrent preparers. Keep the appropriate
root variable exported for the subsequent runner/canary; `--asset-root` only
sets it inside the preparation process. Gated files must be legally obtained
beforehand; this command does not forward `HF_TOKEN`.

Preflight validates selected task dependencies before guest creation.
Controller setup retrieval only accepts prepared local files and fails on
missing or unbound sources instead of fetching them online. Task websites,
application APIs and evaluator networking remain separate; this is not a
whole-task offline guarantee.

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
| `HCU_QUEUE` | default config for `run_queue` when `--queue` is omitted |
| `OSWORLD_FILE_BASE_URL` | local task asset snapshot |
| `OSWORLD_V1_ASSET_ROOT` | local V1 setup asset snapshot and source manifest |
| `OSWORLD_V1_GUEST_IMAGE`, `OSWORLD_V2_GUEST_IMAGE` | guest images, used by the example configs |
| `HCU_OPENAI_GATEWAY`, `HCU_ANTHROPIC_GATEWAY`, `HCU_GEMINI_GATEWAY` | gateway base URLs reachable from the guest |
| `OSWORLD_V2_REQUIRE_DEDICATED_DOCKER` | set to `1` to refuse any Docker daemon other than `OSWORLD_V2_DOCKER_HOST` |

## Model gateways

Agents inside the guest never receive model credentials. They send requests
to a gateway on the host at `<gateway>/t/<task-id>/...`; the gateway adds the
operator's credential, records one JSONL row per request and enforces the
per-task successful-generation budget. The runner reads the token log to
report usage and recognize budget rejection without cutting off the last
permitted response's tool execution.

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
Both SSE streams and complete JSON-array streaming responses are recognized;
malformed or incomplete responses do not count as successful generations.

### Budgets

Only task agents are budgeted, identified by the `task-` prefix of their
affinity ID; other traffic is not. All gateways count complete successful
generation responses delivered to the agent transport. Upstream failures,
disconnections, incomplete streams and non-generation calls do not consume a
step. Delivery is not proof of tool execution or task completion.
Every attempt gets an independent random identifier in its affinity ID, so
concurrent runs of the same task do not share counters or failure state.

Concurrent generations reserve available slots so they cannot exceed the
budget. The final permitted response may execute its tools and finish; the
gateway rejects the next generation request rather than the runner killing
the agent as soon as the counter reaches its limit.

Five consecutive failed generations trip a separate retry circuit breaker.
The runner also stops after 1800 seconds without a new successful generation,
including a stalled tool after the last response; a shorter explicit phase
timeout still applies. These bounds prevent failed requests from running
forever without charging them as successful model steps.

Gateways restore budget and retry state from their token logs. Use a fresh log
per experiment and do not reuse a log created with another budget policy.
The gateway and runner limits must agree. The runner rejects a mismatch when
it first sees a gateway limit in the log; this is not a pre-launch validation.
The checks also run after reading a matching completion marker. Budget or retry
validation errors preserve available turn traces, stderr and final output before
normal guest cleanup; unavailable guest files cannot be reconstructed.

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
on the untouched setup state.

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
--agent gemini-cli  --gemini-gateway-base "$HCU_GEMINI_GATEWAY"
--agent grok-build  --gateway-base "$HCU_OPENAI_GATEWAY"
```

`--model` and `--effort` default to the agent's own values (see
[Agent profiles](AGENT_PROFILES.md)), so they can be omitted. Grok Build cannot
resume a session, and Gemini CLI needs a Node runtime under `artifacts/`;
both are described there.

Other useful options:

| Option | Default | Effect |
| --- | --- | --- |
| `--instructions-file` | `instructions/hybrid.md` | interaction guide injected at startup; an empty string omits the profile but retains base policy |
| `--channels` | both | `a11y,keyboard` exposes only `AX`, `gui,keyboard` only `GUI`; the other tool is absent from the agent's tool list |
| `--base-instructions-file` | `instructions/base.md` | replacement common safety and outcome policy |
| `--mcp-binary` | `$HCU_ARTIFACT_DIR/computer-use` | MCP server uploaded to the guest |
| `--max-model-calls` | `0` | successful task-agent generation budget; `0` disables the count limit, so set it for real runs |
| `--timeout` | `0` | extra wall-clock limit per phase in seconds; `0` omits this deadline, not the no-progress bound |
| `--volume-size`, `--ram-size` | `40`, `4G` | required root-volume size class in GiB (within image capacity) and guest RAM |
| `--retain-container` | off | keep the stopped container for inspection |
| `--enable-vnc`, `--enable-recording` | off | noVNC access and a screen recording |

Every run needs a new result directory; the runner refuses to write into an
existing one.

## Batches

`hcu.osworld.run_batch` runs the tasks of one JSON config with bounded
concurrency, one `run_task` process per task:

```bash
$PY -m hcu.osworld.run_batch --config configs/osworld-v2-codex-queue.example.json \
  --concurrency 1 --dry-run
```

`--dry-run` validates the config and prints the generated commands; remove it
to launch. Config keys map to `run_task` options (`gateway_base`,
`guest_image`, `max_model_calls`, `channels`, `instructions_file`, and so on).
String values may reference environment variables such as `${HCU_ARTIFACT_DIR}`, and each task may
override `volume_size`, `ram_size` and `task_domain`. Unknown keys are
rejected.

Set `no_instructions: true` to omit the selected operating profile while
retaining base policy. The old skill-selection flag and config keys have no
compatibility aliases.

`hcu.osworld.run_queue --queue CONFIG` reads the same config and adds a
rolling queue: bounded concurrency, launch staggering, a rate-limit circuit
breaker, and restart recovery that adopts live attempts, re-queues interrupted
ones and renders `QUEUE_STATUS.md`. Concurrency is `max_active_tasks`. The
queue-only keys are listed in `hcu/osworld/batch_config.py`.

The queue requires an explicit `run_id` and stores each experiment under
`$HCU_RESULTS_DIR/osworld-<v1|v2>/<run_id>/` (default result root: `runs/`).
Task results are in `hybrid/<task-id>/`, logs in `logs/`, and
`QUEUE_OWNER.json`, `QUEUE_STATE.json` and `QUEUE_STATUS.md` belong to that
same run directory. Configuration files may share a directory without sharing
results. Interrupted-attempt archives also remain within their owning run.

Restarting the same run requires the recorded resolved configuration, task
membership, commands and relevant runtime environment to agree. Conflicting
reuse of a `run_id`, or an existing run directory without a valid queue owner,
fails before launching tasks. Legacy results beside a config are not imported
or used to skip work. Live-process recovery checks the complete runner command,
not just a saved PID or a result-path substring. These are explicit ownership
records, not content-hash checks; do not replace binaries, images or instruction
contents in place during a run.
Both `OSWORLD_V1_ASSET_ROOT` and `OSWORLD_FILE_BASE_URL` belong to that runtime
binding. Changing the asset root requires a new run ID.

The queue loads `environment_file` before resolving configuration variables
and uses those resolved values for capacity preflight, token logs and task
commands. A referenced variable that is not set is an error.

Without `--queue`, the queue reads `HCU_QUEUE`, falling back to
`configs/osworld-v2-codex-queue.example.json`. A configuration selects one
top-level `benchmark_version` (`v1` or `v2`, default `v2`) for every task;
task-level generation overrides and mixed-generation queues are unsupported.
The status title reflects that selection. Static strata retain their
configured labels, including diagnostic words; task status and evaluator
errors appear separately.

Give every experiment its own `run_id` and token log, and do not mix guest
images, MCP binaries, instruction revisions, prompts, agent versions or model
routes in one round. Editing the base or selected profile changes model
context and requires a new evaluation attempt.

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

For a completed or partial queue, generate a fixed-denominator report without
starting Docker, the benchmark, or a model:

```bash
python3 -m hcu.osworld.summarize --config "$HCU_EXPERIMENT_DIR/config.json"
```

Load the same configuration variables beforehand; the summarizer does not
execute an `environment_file`. The default input is
`$HCU_RESULTS_DIR/osworld-<version>/<run_id>/`; `--run-dir` selects it explicitly.
`--output-dir` defaults to that run's `summary/` and contains `summary.json`,
`tasks.csv` and `summary.md`. This entrypoint summarizes queue runs with a
matching `QUEUE_OWNER.json`, not unowned single-task or `run_batch` directories.

The selected config supplies the full denominator. Each row preserves its RAW
score and reports missing, invalid, truncated, budget, timeout, evaluator or
execution failure states separately. `aggregate.final_score` is `null` until
the evidence is complete; `effective_lower_bound` treats unknowns as zero but
is explicitly not a final score. The observed RAW mean is diagnostic only.
Known truncations have effective score zero even when RAW is positive.
They still require a sealed result with `result.txt`, terminal task metadata and
a finished evaluator status before the report can publish a final score.
Partial continuous evaluator scores are not rounded to binary outcomes.

An evaluator must return an explicit, finite numeric score. Missing scores,
NaN, infinity and evaluator exceptions produce `raw_evaluator_status="error"`
and a `null` score, including in `result.txt`; they are not valid zero scores.
The error details and a text representation of any rejected return value are
retained in the result artifacts. A genuine zero remains a completed score.

Unknown token/time measurements remain `null`; each aggregate reports known
and unknown task counts and the known sum separately. Archived interruptions
are listed, not selected as better attempts, and their costs are not silently
added to the current-attempt total. Configuration/identity conflicts, duplicate
result locations and cross-run evidence links fail instead of being combined.

## Operational notes

- Host loopback is not reachable from the guest. Use the bridge address for
  gateways and check it with `ss -ltnp`.
- Select the correct asset root for V1/V2 and prepare the selected task's
  exact dependencies; an unrelated task directory cannot satisfy preflight.
- A passing evaluator does not prove that the task was done as asked; read
  the trace and the artifacts as well.
- This repository's `AGENTS.md` is written for developers and must never
  reach an evaluated agent.
