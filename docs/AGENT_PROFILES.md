# Codex And Claude Code Profiles

## Shared Inputs

Both agents use:

- the same v0.14 `computer-use` binary;
- pinned agent artifacts from `configs/agent-artifacts.lock.json`;
- the same base instructions in `skills/base/`;
- the same selected benchmark skill;
- one fresh task environment and one MCP process per task;
- task-scoped model gateway URLs;
- Bash enabled for the formal hybrid profile.

Current Linux x86-64 pins:

| Agent | Version | Binary SHA-256 |
|---|---:|---|
| Codex | `0.151.0` | `9739cbc928b9c573be83256acd46668f5dd4f119d2d09e05246895ca2aaf0c9a` |
| Claude Code | `2.1.251` | `fd5f10ff0eb58daec04900466b143ea98aab50abf208a422bc008eaec13f61f7` |

## Codex

Enabled:

- shell tool and unified exec;
- `apply_patch` and `view_image`;
- built-in browser runtime;
- external browser connection and full CDP access;
- the `computer_use` MCP server with `AX` and `GUI`.

Disabled:

- web search;
- plugins and plugin sharing;
- skill search and skill dependency installation;
- memories and memory generation;
- goals;
- apps;
- hooks;
- multi-agent;
- workspace dependencies;
- image generation;
- planning;
- interactive user-input tool;
- bundled system skills;
- analytics, OTEL export, update checks, and persistent command history;
- the built-in Computer tool, so desktop mutation remains attributable to the
  `computer_use` MCP or explicit Bash/CDP activity;
- auth elicitation, tool suggestions, unbounded retries, and remote
  compaction.

The base instructions are selected with `model_instructions_file`. The
benchmark skill is written to `/home/user/AGENTS.md`.

## Claude Code

Enabled built-ins:

```text
Bash, Edit, Read
```

Enabled MCP tools:

```text
mcp__computer_use__AX
mcp__computer_use__GUI
```

Disabled or unavailable:

- `NotebookEdit`;
- `Glob`, `Grep`, `WebFetch`, `WebSearch`, and `Write` as separate tools;
- slash commands;
- Claude in Chrome;
- prompt suggestions;
- auto-memory;
- subagents;
- workflows and scheduled tasks;
- host/project settings, plugins, hooks, and `CLAUDE.md`;
- every unlisted built-in or MCP tool.

`Bash` supplies general command-line search and retrieval, while `Edit` and
`Read` cover direct file work. Removing the overlapping built-ins keeps the
tool surface smaller without disabling those hybrid capabilities.

Claude Code runs with `--permission-mode bypassPermissions` only inside the
isolated benchmark guest. It uses an isolated `CLAUDE_CONFIG_DIR`,
`--bare`, `--strict-mcp-config`, and no setting sources. Telemetry, error
reporting, automatic updates, attribution, feedback surveys, and nonessential
model traffic are disabled through environment variables.

Claude Code `2.1.251` connects to a server that advertises root-level MCP
`oneOf` schemas but omits those tools from its initialized tool list. The
adapter in `integrations/common/claude_mcp_schema_adapter.py` flattens only the
schema advertised to Claude; with the adapter, `AX` and `GUI` are exposed and
the QEMU task can use them. The unmodified MCP server still validates the
original discriminated union before any side effect.

## Prompt Replacement

The old coding-oriented defaults are replaced with
`skills/base/CODEX_BASE_INSTRUCTIONS.md`. Claude receives:

```text
base instructions

# Benchmark computer-use skill

selected SKILL.md
```

Codex receives the base instructions through config and discovers the selected
skill only through the task-local `AGENTS.md`. Repository development
instructions must never be injected into the evaluated agent.
