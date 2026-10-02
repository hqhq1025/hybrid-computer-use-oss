# Task Agent Profiles

`hcu/osworld/agents.py` builds one profile per agent; `python3 -m
hcu.render_agent_config` writes these configurations for use outside
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
- the common safety and outcome policy in `instructions/base.md`, plus one
  selected interaction guide (normally `instructions/hybrid.md`); selecting
  no operating profile omits only the guide, not the base policy;
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
selected profile is written to `/home/user/AGENTS.md` under the guest user's home.
Outside OSWorld, the configuration generator embeds the selected profile in
`developer_instructions` instead, without replacing the user's workspace
instructions. Its `--codex-home` must match the client's actual `CODEX_HOME`.

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

Standalone generation writes the MCP JSON, replacement prompt and a sibling
`.launch.py` script. Run that script with Python and `claude` on `PATH` to
apply the selected model, effort and task-scoped gateway. It uses manual tool
approvals on the host, not the guest's permission bypass. The generated client
profile does not provide VM isolation; see [MCP](MCP.md) for a launch example.

`hcu/osworld/guest_files/claude_mcp_schema_adapter.py` connects Claude Code to
the server and presents client-compatible schemas. The server validates the
original discriminated unions before any side effect.

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

The standalone generator requires `--output <home>/.gemini/settings.json`
and reports `GEMINI_CLI_HOME=<home>`. Settings and global `GEMINI.md` live in
that `.gemini` directory. Use the printed environment when launching the CLI;
other output layouts are rejected.

## Grok Build

`config.toml` in an isolated `GROK_HOME` registers the gateway as the only
model (Responses API, `env_key = "GROK_GATEWAY_TOKEN"`), disables auto-update,
subagents, workflows, memory, web search and the plugins of other harnesses,
and keeps the `computer_use` MCP server. The launcher passes
`--always-approve`, `--no-plan`,
`--no-subagents`, `--disable-web-search`, `--verbatim` and the disallowed
tools list.

The Grok Build profile supports a single turn per task. Select a resumable
client for tasks that require an `ASK_USER` answer or a resume-based retry.

## Prompt replacement

The same base policy (`instructions/base.md`) and selected profile text reach
every agent. They are always injected plain text, not discoverable or activated
skills. Each harness reads them differently:

| Agent | Base instructions | Selected profile |
| --- | --- | --- |
| Codex | `model_instructions_file` in `config.toml` | task-local `AGENTS.md` |
| Claude Code | `--system-prompt-file` | profile appended directly to that prompt, retaining its own H1 |
| Gemini CLI | `GEMINI_SYSTEM_MD` replaces the core prompt | global `GEMINI.md` |
| Grok Build | `--system-prompt-override` | appended to that override |

This table describes the isolated OSWorld runner. Standalone Codex generation
uses `developer_instructions` for the selected profile; standalone Claude
configuration uses the generator's local Python interpreter for the MCP adapter.
Repository development instructions must never reach the evaluated agent.
Standalone Grok output must be named `config.toml`; its parent becomes
`GROK_HOME`. Other filenames are rejected because the client would ignore them.

Use `--instructions-file` to select the profile in either the runner or the
standalone generator. The runner uses `--base-instructions-file` for base
policy; the standalone generator keeps `--base-instructions`. An empty
`--instructions-file ""` omits only the profile. Batch and queue configs use
`instructions_file` and `no_instructions`; old skill-selection names are not
aliases. Native client skill-disable settings above are unchanged.
An omitted or JSON `null` `instructions_file` uses the default hybrid profile;
an empty string or `no_instructions: true` disables it. Experiment preparation
and result validation preserve these same selection rules.

The runner supplies the `TASK_COMPLETE`, `TASK_INFEASIBLE` and `ASK_USER` protocol
separately. Product instructions contain no protocol markers. Editing the base
or profile changes model context and requires a new evaluation attempt; see
[Instructions](INSTRUCTIONS.md).
