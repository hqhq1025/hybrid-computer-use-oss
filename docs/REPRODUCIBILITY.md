# Experiment Configuration

Use `hcu.osworld.reproduce` to create a portable OSWorld queue configuration
with a selected task list and copies of the agent instructions.

## Prerequisites

Follow [OSWorld installation](OSWORLD.md#install) to install the upstream
checkout, overlay, agent clients and guest image. Evaluations require Linux
x86-64, Docker, KVM, local task assets and a model gateway reachable from the
guest network. V2 task definitions and assets require access to the upstream
Hugging Face datasets.

Provision at least 4 GiB guest RAM per concurrent task, host overhead, and disk
space for the images and a writable overlay per task. Start at concurrency 1.
Load the environment variables in `configs/env.example` with your local paths.

## Select A Configuration

| Benchmark | Client | Example configuration |
| --- | --- | --- |
| V1 | Codex | `configs/osworld-v1-codex-queue.example.json` |
| V1 | Claude Code | `configs/osworld-v1-claude-code-queue.example.json` |
| V1 | Gemini CLI | `configs/osworld-v1-gemini-cli-queue.example.json` |
| V1 | Grok Build | `configs/osworld-v1-grok-build-queue.example.json` |
| V2 | Codex | `configs/osworld-v2-codex-queue.example.json` |
| V2 | Claude Code | `configs/osworld-v2-claude-code-queue.example.json` |

Each example selects one task. Agent and Node versions are listed in
`configs/agent-artifacts.lock.json`; model, effort and budgets are set in the
configuration. The default instructions are `instructions/base.md` and
`instructions/hybrid.md`. AX-only and GUI-only profiles select an MCP track;
the client's separate shell, file and browser tools remain available.

## Create A Queue

From the repository root, prepare the V1 Codex example:

```bash
PY=external/OSWorld-V2/.venv/bin/python
$PY -m hcu.osworld.reproduce \
  --config configs/osworld-v1-codex-queue.example.json \
  --run-id v1-codex-example \
  --protocol-id official-v1 --protocol-revision 1 \
  --output runs/prepared/v1-codex-example
export HCU_EXPERIMENT_DIR="$PWD/runs/prepared/v1-codex-example"
```

The output directory must be new. Preparation creates configuration files
without launching a guest or making model requests. It sets a 1800-second
per-phase timeout by default; use `--timeout-seconds` to select another
positive value. The model budget comes from the input configuration.

The output layout is:

```text
experiment.json
config.json
instructions/base.md
instructions/profile.md  (when a profile is selected)
```

`experiment.json` describes task membership, exclusions, protocol, versions
and instruction paths. `config.json` uses `${HCU_EXPERIMENT_DIR}` to locate
the copied instructions. Keep these files together when moving the directory.
Use a new run ID and output directory when changing the configuration.

## Select Tasks

`--tasks TASKS.json` accepts a list of task IDs or task objects, or the upstream
`test_all.json` domain-to-ID mapping. `--exclude EXCLUSIONS.json` accepts the
same formats and must name members of the selection. Duplicate IDs, unsafe
paths, empty selections and unmatched exclusions are rejected.

For the V1 selection of 361 tasks:

```bash
$PY -m hcu.osworld.reproduce \
  --config configs/osworld-v1-codex-queue.example.json \
  --tasks configs/osworld-v1-tasks.json \
  --exclude configs/osworld-v1-excluded-tasks.json \
  --run-id v1-codex-full \
  --protocol-id official-v1-non-drive --protocol-revision 1 \
  --output runs/prepared/v1-codex-full
export HCU_EXPERIMENT_DIR="$PWD/runs/prepared/v1-codex-full"
```

The supplied V1 list has 369 task IDs. The eight Google Drive tasks are
excluded from this evaluation scope, leaving 361. Preparation rejects a
selection that still includes them.

For V2, select task IDs from the installed benchmark release and use a V2
configuration, run ID and protocol. Check the complete task membership before
starting the queue.

## Start The Queue

Preview the generated commands:

```bash
$PY -m hcu.osworld.run_batch \
  --config "$HCU_EXPERIMENT_DIR/config.json" --dry-run
```

Prepare the selected [assets](OSWORLD.md#prepare-selected-setup-assets), run a
[setup canary](OSWORLD.md#setup-canaries), and start the matching
[gateway](OSWORLD.md#model-gateways). Use the token-log path in `config.json`
for the gateway's `--out`; gateway and runner budgets must match. The token
log lives outside the queue-owned result directory.

Start execution with:

```bash
$PY -m hcu.osworld.run_queue --queue "$HCU_EXPERIMENT_DIR/config.json"
```

Keep the configuration, image, instructions, client versions and model route
unchanged throughout the queue. Use a new run ID for a different setup.

## Read Results

Generate JSON, CSV and Markdown summaries:

```bash
$PY -m hcu.osworld.summarize \
  --config "$HCU_EXPERIMENT_DIR/config.json"
```

The default output is the run's `summary/` directory. `--output-dir` selects
another location. Load the same environment variables before summarizing;
the summarizer does not execute an `environment_file`.

The summary uses the complete selected task count as its denominator,
preserves continuous official evaluator scores, and lists missing tasks,
truncation and errors separately. `aggregate.final_score` is `null` until
all required results are complete. Budget or timeout truncations have an
effective score of zero; unknown token and timing values remain `null`.

See [Results](OSWORLD.md#results) for individual task files and aggregate
fields. Keep results and traces in your run directory; review them for
private data before sharing them.
