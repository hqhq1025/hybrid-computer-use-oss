# Skills

The evaluated agent receives the shared base instructions and exactly one
benchmark skill. Every supported harness gets the same text, delivered
through that harness's own mechanism (see
[Agent profiles](AGENT_PROFILES.md)):

"Hybrid" is the product's own name for the full configuration (see the
package name and the repository title); it is not a benchmark or release
label, and the skill file itself carries no version.

| File | Purpose | Computer-use channels |
| --- | --- | --- |
| `skills/hybrid/SKILL.md` | default profile | Bash, files, browser/CDP, AX, GUI |
| `skills/ax-only/SKILL.md` | accessibility ablation | Bash, files, browser/CDP, AX |
| `skills/gui-only/SKILL.md` | pixel-control ablation | Bash, files, browser/CDP, GUI |
| `skills/base/CODEX_BASE_INSTRUCTIONS.md` | replaces coding-oriented agent defaults | defines task completion, honesty and environment rules |

Select a skill with `--skill-file` and the base instructions with
`--base-instructions-file`; `--skill-file ""` runs the agent without a skill.
The AX-only and GUI-only profiles are ablations, run together with
`--channels a11y,keyboard` or `--channels gui,keyboard`, which remove the other
tool from the agent's tool list. Do not report them as the hybrid result.

The skills are model input. Editing them changes what the agent sees, so treat
an edit like a change to the MCP contract: give the run a new ID and never mix
skill versions within one comparison. Repository instructions, evaluator
details, task recipes, coordinates and memory from earlier runs must never be
added to a skill.
