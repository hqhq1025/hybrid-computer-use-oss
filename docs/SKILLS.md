# Skills

The repository ships three immutable benchmark profiles plus one shared base
prompt:

| Profile | Purpose | Public computer-use channels |
| --- | --- | --- |
| `skills/hybrid/SKILL.md` | Default formal hybrid profile | Bash, files, browser/CDP, AX, GUI |
| `skills/ax-only/SKILL.md` | Diagnostic accessibility ablation | Bash, files, browser/CDP, AX |
| `skills/gui-only/SKILL.md` | Diagnostic pixel-control ablation | Bash, files, browser/CDP, GUI |
| `skills/base/CODEX_BASE_INSTRUCTIONS.md` | Replaces coding-oriented agent defaults | Defines task completion, honesty, and environment rules |

The v0.14 formal hybrid skill SHA-256 is:

```text
61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16
```

The AX-only and GUI-only profiles are ablations. They must not replace the
hybrid result when reporting product capability.

Repository instructions, evaluator details, task recipes, coordinates, and
private run memory must never be injected into these skills.
