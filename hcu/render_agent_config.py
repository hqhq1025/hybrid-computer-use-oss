"""Render a Codex or Claude Code configuration that uses the computer-use MCP.

This reproduces the OSWorld runner's agent setup for local use: the same
Codex feature switches and disabled bundled skills, or the same Claude Code
MCP adapter, tool allowlist and replacement system prompt.

Usage::

    python -m hcu.render_agent_config --agent codex --model gpt-5.5 \\
        --gateway http://127.0.0.1:8611 --mcp artifacts/computer-use \\
        --output ~/.codex/config.toml
"""

from __future__ import annotations

import argparse
from pathlib import Path

from hcu.osworld.agents import (
    CLAUDE_BUILTIN_TOOLS,
    claude_mcp_config,
    claude_mcp_tools,
    claude_system_prompt,
    codex_config,
)
from hcu.paths import DEFAULT_BASE_INSTRUCTIONS_FILE, DEFAULT_SKILL_FILE, REPO_ROOT

CLAUDE_MCP_ADAPTER = (
    REPO_ROOT / "hcu/osworld/guest_files/claude_mcp_schema_adapter.py"
)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent", choices=("codex", "claude-code"), required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--effort", default="xhigh")
    parser.add_argument("--task-id", default="manual")
    parser.add_argument("--gateway", required=True)
    parser.add_argument("--mcp", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument(
        "--base-instructions",
        type=Path,
        default=DEFAULT_BASE_INSTRUCTIONS_FILE,
    )
    parser.add_argument("--skill", type=Path, default=DEFAULT_SKILL_FILE)
    parser.add_argument("--codex-home", default="/opt/osw-runtime/codex-task")
    args = parser.parse_args()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    if args.agent == "codex":
        args.output.write_text(
            codex_config(
                model=args.model,
                effort=args.effort,
                base_url=args.gateway.rstrip("/") + f"/t/{args.task_id}/v1",
                codex_home=args.codex_home,
                base_instructions=str(args.base_instructions),
                mcp_command=str(args.mcp),
            ),
            encoding="utf-8",
        )
    else:
        args.output.write_text(
            claude_mcp_config(
                adapter=str(CLAUDE_MCP_ADAPTER),
                mcp_command=str(args.mcp),
            ),
            encoding="utf-8",
        )
        prompt = args.output.with_suffix(".system-prompt.md")
        prompt.write_text(
            claude_system_prompt(
                args.base_instructions.read_text(encoding="utf-8"),
                args.skill.read_text(encoding="utf-8"),
            ),
            encoding="utf-8",
        )
        print(f"Claude allowed tools: {CLAUDE_BUILTIN_TOOLS},{claude_mcp_tools()}")
        print(f"Claude replacement system prompt: {prompt}")
    print(args.output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
