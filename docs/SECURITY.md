# Security And Isolation

Each benchmark task must use a fresh desktop, task-agent session, MCP process,
and trace directory. Do not mount this repository, host credentials, user
configuration, or another task's artifacts into the evaluated environment.

Before agent startup, remove inherited `.codex`, `.claude`, `.claude.json`,
`AGENTS.md`, and `CLAUDE.md` state. Inject only the selected base instructions,
skill, MCP config, and task-scoped gateway settings.

Model credentials stay on the host-side gateway. The guest receives only a
non-secret placeholder token and a task-scoped endpoint. Bind gateways to an
interface reachable from the container or QEMU guest, but restrict access with
host firewall or cloud network rules.

Do not expose repository-authored task helpers, evaluator code, gold artifacts,
or task-derived memory to the evaluated agent. Standard upstream packages and
general-purpose CLIs are allowed.

The MCP controls an already-running desktop. It is not an isolation boundary.
Run it only inside a disposable VM/container or another environment whose
desktop and filesystem the agent is allowed to control.
