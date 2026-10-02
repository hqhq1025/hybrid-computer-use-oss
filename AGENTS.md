# Agent Guide

Instructions for coding agents that work on this repository. Operators should
read [README.md](README.md) and [docs/OSWORLD.md](docs/OSWORLD.md) instead.

## Safety boundary

Never copy, mount, summarize or inject this file or any other repository
documentation into an evaluated OSWorld agent. The evaluated agent receives
only:

- the original OSWorld task instruction;
- `instructions/base.md`, the common safety and outcome policy;
- one selected interaction guide, normally `instructions/hybrid.md`, unless
  the run explicitly selects no operating profile;
- the runner's `TASK_COMPLETE`, `TASK_INFEASIBLE` and `ASK_USER` contract.

Product instructions are plain text injected at startup, not discoverable or
activated skills. Keep runner protocol markers out of those files.

Do not expose evaluator internals, task recipes, coordinates, credentials,
earlier traces or cross-task memory to an evaluated agent.

## Where things live

| Path | Contents |
| --- | --- |
| `computer-use/` | Go MCP server; `native/linux/provider/` is the embedded Python provider |
| `computer-use/testdata/contract/` | golden copy of the model-visible MCP contract |
| `hcu/osworld/` | OSWorld runner, batch and queue coordinators |
| `hcu/gateway/` | task-scoped OpenAI and Anthropic gateways |
| `osworld-overlay/` | files installed into the pinned OSWorld-V2 checkout |
| `instructions/` | model input: common base policy and selected interaction guide |
| `tests/` | runner and gateway tests |

## Rules

- Keep `AX` and `GUI` as the only public MCP tools. Bash, file editing,
  application APIs and browser/CDP remain separate channels of the agent.
- Treat model-visible text as an interface: tool schemas, descriptions,
  server instructions, error texts, base policy and interaction guides. Do not
  change it unless the task asks for it. The contract golden test fails on
  MCP-visible changes; regenerate it only for an intended change.
- Editing product instructions changes model context and requires a new
  evaluation attempt. Do not mix instruction revisions within a run or claim
  that a text migration proves performance.
- Change the Go server and the Python provider together when you touch their
  private protocol, and update `computer-use/testdata/provider-v2/`.
- Real OSWorld runs spend model quota. Ask before starting one, and run a
  setup canary first.
- `VERSION` holds the release version; do not hard-code versions elsewhere.

## Checks

Run before reporting a change:

```bash
make test
```

For changes to the runner, also dry-run a batch for both benchmark versions
with the OSWorld-V2 virtual environment:

```bash
external/OSWorld-V2/.venv/bin/python -m hcu.osworld.run_batch \
  --config configs/osworld-v1-codex-queue.example.json --dry-run
external/OSWorld-V2/.venv/bin/python -m hcu.osworld.run_batch \
  --config configs/osworld-v2-codex-queue.example.json --dry-run
```
