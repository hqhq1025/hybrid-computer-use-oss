# Skills

The evaluated agent receives the shared base instructions and exactly one
benchmark skill:

| File | Purpose | Computer-use channels |
| --- | --- | --- |
| `skills/hybrid/SKILL.md` | default profile | Bash, files, browser/CDP, AX, GUI |
| `skills/ax-only/SKILL.md` | accessibility ablation | Bash, files, browser/CDP, AX |
| `skills/gui-only/SKILL.md` | pixel-control ablation | Bash, files, browser/CDP, GUI |
| `skills/base/CODEX_BASE_INSTRUCTIONS.md` | replaces coding-oriented agent defaults | defines task completion, honesty and environment rules |

Select a skill with `--skill-file` and the base instructions with
`--base-instructions-file`. The AX-only and GUI-only profiles are ablations;
do not report them as the hybrid result.

The skills are model input. Editing them changes what the agent sees, so treat
an edit like a change to the MCP contract: give the run a new ID and never mix
skill versions within one comparison. Repository instructions, evaluator
details, task recipes, coordinates and memory from earlier runs must never be
added to a skill.
