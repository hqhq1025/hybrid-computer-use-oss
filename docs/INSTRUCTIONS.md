# Instructions

The agent receives common base policy and, by default, one selected operating
profile. These files are plain text injected at startup, not skills that a
client discovers or activates. Every supported agent receives the same selected
text through its existing context route.

| File | Purpose | Computer-use channels |
| --- | --- | --- |
| `instructions/base.md` | common safety and outcome policy | applies across channels |
| `instructions/hybrid.md` | default interaction guide | Bash, files, browser/CDP, AX, GUI |
| `instructions/ax-only.md` | accessibility interaction guide | Bash, files, browser/CDP, AX |
| `instructions/gui-only.md` | pixel-control interaction guide | Bash, files, browser/CDP, GUI |
| `instructions/hybrid-code.md` | draft add-on for driving AX and GUI from code (CLI, SDK); not selected by any config yet | Bash, AX and GUI through `computer-use` |

The base policy covers honesty, safety and verifying the requested outcome.
The selected profile guides channel choice and interaction. Neither contains
evaluation protocol markers: the OSWorld runner adds its `TASK_COMPLETE`,
`TASK_INFEASIBLE` and `ASK_USER` contract separately from the product instructions.

## Selection

The OSWorld runner selects a profile with `--instructions-file` and base policy
with `--base-instructions-file`. Batch and queue configs use `instructions_file`;
`no_instructions: true` selects no operating profile. Passing
`--instructions-file ""` also omits the profile. Base policy remains injected
in either case.

Standalone `python3 -m hcu.render_agent_config` uses `--instructions-file` for
the profile and retains `--base-instructions` for the base file. Its empty
`--instructions-file ""` has the same profile-only opt-out behavior.

The AX-only and GUI-only profiles are ablations, run together with
`--channels a11y,keyboard` or `--channels gui,keyboard`, which remove the other
tool from the agent's tool list. Selecting text alone does not disable tools.
Do not report these ablations as the hybrid result.

## Delivery

| Agent | Base policy | Selected profile |
| --- | --- | --- |
| Codex | `model_instructions_file` | guest `AGENTS.md`; standalone `developer_instructions` |
| Claude Code | replacement system prompt | profile appended directly, retaining its own H1 |
| Gemini CLI | `GEMINI_SYSTEM_MD` | isolated global `GEMINI.md` |
| Grok Build | replacement system prompt | appended to that prompt |

[Agent profiles](AGENT_PROFILES.md) describes the exact routes. Native client
skill settings and their disabled state are independent of this plain-text
injection.

## Evaluation boundary

Editing either the base or a profile changes model context. Use a new evaluation
attempt and run ID, record the selected files, and never mix revisions within
one run.

Repository development instructions, evaluator details, task recipes,
coordinates and memory from earlier runs must never be added to product
instructions or otherwise injected into an evaluated agent.
