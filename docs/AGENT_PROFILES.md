# Codex and Claude Code Profiles

`hcu/osworld/agents.py` builds both profiles; `python3 -m
hcu.render_agent_config` writes the same files for use outside OSWorld.

## Shared inputs

Both agents use:

- the same `computer-use` binary;
- the agent releases pinned in `configs/agent-artifacts.lock.json`
  (currently Codex 0.151.0 and Claude Code 2.1.251);
- the base instructions in `skills/base/` and one selected benchmark skill, or
  none for the no-skill ablation;
- one fresh task environment and one MCP process per task;
- a task-scoped model gateway URL and a placeholder credential;
- Bash, which the hybrid profile keeps enabled.

## Codex

Enabled:

- shell tool and unified exec;
- `apply_patch` and `view_image`;
- the built-in browser runtime, external browser connection and full CDP
  access;
- the `computer_use` MCP server with `AX` and `GUI`.

Disabled:

- web search;
- plugins and plugin sharing;
- skill search and skill dependency installation;
- memories and memory generation;
- goals, apps, hooks and multi-agent;
- workspace dependencies and image generation;
- planning and the interactive user-input tool;
- bundled system skills;
- analytics, OTEL export, update checks and persistent command history;
- the built-in Computer tool, so every desktop mutation comes from the
  `computer_use` MCP or from explicit shell or CDP activity;
- auth elicitation, tool suggestions, unbounded retries and remote
  compaction.

The base instructions are selected with `model_instructions_file`, and the
benchmark skill is written to `/home/user/AGENTS.md`.

## Claude Code

Allowed tools:

```text
Bash, Edit, Read, mcp__computer_use__AX, mcp__computer_use__GUI
```

Disabled or unavailable:

- `NotebookEdit`;
- `Glob`, `Grep`, `WebFetch`, `WebSearch` and `Write` as separate tools;
- slash commands and Claude in Chrome;
- prompt suggestions and auto-memory;
- subagents, workflows and scheduled tasks;
- host and project settings, plugins, hooks and `CLAUDE.md`;
- every other built-in or MCP tool.

`Bash` covers command-line search and retrieval, and `Edit` and `Read` cover
direct file work, so removing the overlapping built-ins shrinks the tool
surface without removing a channel.

Claude Code runs with `--permission-mode bypassPermissions`, which is only
acceptable inside the disposable benchmark guest. It uses an isolated
`CLAUDE_CONFIG_DIR`, `--bare`, `--strict-mcp-config` and no setting sources.
Environment variables turn off telemetry, error reporting, automatic updates,
attribution, feedback surveys and nonessential model traffic.

Claude Code 2.1.251 omits tools whose schema is a root-level `oneOf` from its
initialized tool list. `hcu/osworld/guest_files/claude_mcp_schema_adapter.py`
sits between Claude Code and the server and flattens only the schemas shown to
Claude. The server still validates the original discriminated unions before
any side effect.

## Prompt replacement

`skills/base/CODEX_BASE_INSTRUCTIONS.md` replaces the coding-oriented default
instructions of both agents. Claude receives one system prompt:

```text
<base instructions>

# Benchmark computer-use skill

<selected SKILL.md>
```

Codex receives the base instructions through its config and finds the skill
only through the task-local `AGENTS.md`. Repository development instructions
must never reach the evaluated agent.
