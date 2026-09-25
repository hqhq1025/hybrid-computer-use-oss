# Task Agent Profiles

`hcu/osworld/agents.py` builds one profile per agent; `python3 -m
hcu.render_agent_config` writes the Codex and Claude Code ones for use outside
OSWorld. Four agents are supported: Codex, Claude Code, Gemini CLI and Grok
Build. Each has a default model and reasoning effort; `--model` and `--effort`
override them.

| Agent | Gateway | Default model | Default effort | Can resume |
| --- | --- | --- | --- | --- |
| `codex` | OpenAI Responses | `gpt-5.5` | `xhigh` | yes |
| `claude-code` | Anthropic | `claude-opus-5` | `xhigh` | yes |
| `gemini-cli` | Gemini | `gemini-3.8-flash` | `high` | yes |
| `grok-build` | OpenAI Responses | `grok-4.7` | `xhigh` | no |

## Shared inputs

All agents use:

- the same `computer-use` binary;
- the releases pinned in `configs/agent-artifacts.lock.json` (currently Codex
  0.151.0, Claude Code 2.1.251, Gemini CLI 0.60.0, Grok Build 1.0.40 and the
  Node 20.19.5 runtime Gemini CLI needs);
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
benchmark skill is written to `/home/user/AGENTS.md` under the guest user's home.

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

## Gemini CLI

The npm package from the artifact lock is uploaded as a tarball and unpacked
into `/opt/agent-runtime/gemini/cli` in the guest, and the pinned Node binary
runs it as `~/.local/bin/gemini-node` so the watchdog can find the agent
process by name. `settings.json` keeps only this MCP server:

- ambient features are off: agents, auto-memory, task tracker, plan mode,
  skills, hooks, extensions, telemetry and usage statistics;
- context discovery is limited to the global `GEMINI.md` in the isolated
  `GEMINI_CLI_HOME`, so no workspace file can reach the model;
- tools that talk to the user or the web, or add planning and memory layers,
  are excluded: `google_web_search`, `web_fetch`, `save_memory`,
  `write_todos`, `ask_user`, `enter_plan_mode`, `exit_plan_mode`,
  `invoke_agent`, `activate_skill`, `update_topic`;
- the compression and loop-detection aliases are bound to the selected model,
  and `--effort` becomes that model's `thinkingLevel` (`native` keeps the
  model default);
- `GEMINI_API_KEY` holds a placeholder; `GOOGLE_GEMINI_BASE_URL` points at the
  task gateway;
- a local experiments file raises the CLI's 60-second response-header timeout
  to 600 seconds, because the gateway relays a streamed response only after it
  completes.

## Grok Build

`config.toml` in an isolated `GROK_HOME` registers the gateway as the only
model (Responses API, `env_key = "GROK_GATEWAY_TOKEN"`), disables auto-update,
subagents, workflows, memory, web search and the plugins of other harnesses,
and keeps the `computer_use` MCP server. The launcher passes the same flags the
paper runs used, including `--always-approve`, `--no-plan`,
`--no-subagents`, `--disable-web-search`, `--verbatim` and the disallowed
tools list.

Grok Build 1.0.40 drops `--system-prompt-override` when it resumes a session
and falls back to its default prompt. A resumed turn would therefore run under
different instructions, so Grok Build cannot resume: a task that needs a
second turn (an `ASK_USER` answer or a resume-based retry) fails with that
reason. Everything else, including the `--resume` command shape, is ready for
a release that fixes it.

## Prompt replacement

The same base instructions (`skills/base/CODEX_BASE_INSTRUCTIONS.md`) and skill
reach every agent, and each harness reads them differently:

| Agent | Base instructions | Skill |
| --- | --- | --- |
| Codex | `model_instructions_file` in `config.toml` | task-local `AGENTS.md` |
| Claude Code | `--system-prompt-file` | appended to that prompt under a `# Benchmark computer-use skill` heading |
| Gemini CLI | `GEMINI_SYSTEM_MD` replaces the core prompt | global `GEMINI.md` |
| Grok Build | `--system-prompt-override` | appended to that override |

Repository development instructions must never reach the evaluated agent.
