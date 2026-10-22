# Isolation

Each benchmark task uses a fresh desktop, task-agent session, MCP process and
result directory. Do not mount this repository, host credentials, user
configuration or another task's artifacts into the evaluated environment.

Before the agent starts, the runner removes inherited agent state for every
supported harness: `.codex`, `.claude`, `.claude.json`, `.gemini`, `.grok`,
the context files `AGENTS.md`, `CLAUDE.md` and `GEMINI.md`, and the guest
image's own `.mcp.json`, whose MCP servers are not part of this runner. It then
injects only the base policy, selected interaction guide, MCP configuration and
task-scoped gateway settings.

Base policy and the selected guide are plain text injected at startup.
Selecting no operating profile leaves the base policy in place. The runner
supplies its `TASK_COMPLETE`, `TASK_INFEASIBLE` and `ASK_USER` contract separately;
those markers do not belong in the product instruction files.

Model credentials stay in the host-side gateway. The guest receives a
placeholder token and a task-scoped endpoint. The gateway must listen on an
address the guest can reach, usually the Docker bridge; restrict that address
with host firewall or cloud network rules.

Do not expose repository-authored task helpers, evaluator code, gold artifacts
or memory derived from earlier tasks to the evaluated agent. Standard upstream
packages and general-purpose command-line tools are fine.

The MCP server controls an already-running desktop; it is not an isolation
boundary. Run it only inside a disposable VM or container, or on a desktop
whose files and applications the agent is allowed to control.
