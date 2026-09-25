"""Render one agent configuration that uses the computer-use MCP.

This reproduces the OSWorld runner's agent setup for local use: the same
Codex feature switches and disabled bundled skills, the Claude Code MCP
adapter with its tool allowlist and replacement system prompt, the Gemini CLI
settings with its isolated context file, or the Grok Build config bound to a
gateway.

Usage::

    python -m hcu.render_agent_config --agent codex --model gpt-5.5 \\
        --gateway http://127.0.0.1:8611 --mcp artifacts/computer-use \\
        --output ~/.codex/config.toml
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from hcu.osworld import agents
from hcu.osworld.guest import GUEST_CODEX_HOME
from hcu.paths import DEFAULT_BASE_INSTRUCTIONS_FILE, DEFAULT_SKILL_FILE, REPO_ROOT

CLAUDE_MCP_ADAPTER = (
    REPO_ROOT / "hcu/osworld/guest_files/claude_mcp_schema_adapter.py"
)
AGENTS = ("codex", "claude-code", "gemini-cli", "grok-build")


def render(args: argparse.Namespace) -> None:
    gateway = args.gateway.rstrip("/")
    base_instructions = args.base_instructions.read_text(encoding="utf-8")
    skill = args.skill.read_text(encoding="utf-8") if args.skill.is_file() else ""
    if args.agent == "codex":
        args.output.write_text(
            agents.codex_config(
                model=args.model,
                effort=args.effort,
                base_url=f"{gateway}/t/{args.task_id}/v1",
                codex_home=args.codex_home,
                base_instructions=str(args.base_instructions),
                mcp_command=str(args.mcp),
                channels=args.channels,
            ),
            encoding="utf-8",
        )
    elif args.agent == "claude-code":
        args.output.write_text(
            agents.claude_mcp_config(
                adapter=str(CLAUDE_MCP_ADAPTER),
                mcp_command=str(args.mcp),
                channels=args.channels,
            ),
            encoding="utf-8",
        )
        prompt = args.output.with_suffix(".system-prompt.md")
        prompt.write_text(
            agents.claude_system_prompt(base_instructions, skill),
            encoding="utf-8",
        )
        print(
            f"Claude allowed tools: "
            f"{agents.CLAUDE_BUILTIN_TOOLS},{agents.claude_mcp_tools()}"
        )
        print(f"Claude replacement system prompt: {prompt}")
    elif args.agent == "gemini-cli":
        args.output.write_text(
            agents.gemini_settings(
                model=args.model,
                effort=args.effort,
                mcp_command=str(args.mcp),
                channels=args.channels,
            ),
            encoding="utf-8",
        )
        # Gemini CLI reads the skill as the global context file next to its
        # isolated settings, and the base instructions replace its core prompt.
        context = args.output.parent / "GEMINI.md"
        context.write_text(skill, encoding="utf-8")
        args.output.with_name("base-instructions.md").write_text(
            base_instructions, encoding="utf-8"
        )
        args.output.with_name("experiments.json").write_text(
            agents.gemini_experiments(), encoding="utf-8"
        )
        print(f"Gemini context file: {context}")
        print(
            "Gemini environment: "
            + json.dumps({
                "GEMINI_CLI_HOME": str(args.output.parent.parent),
                "GEMINI_SYSTEM_MD": str(
                    args.output.with_name("base-instructions.md")
                ),
                "GEMINI_EXP": str(args.output.with_name("experiments.json")),
                "GOOGLE_GEMINI_BASE_URL": f"{gateway}/t/{args.task_id}",
                "GEMINI_API_KEY": "placeholder",
            })
        )
    elif args.agent == "grok-build":
        args.output.write_text(
            agents.grok_config(
                model=args.model,
                base_url=f"{gateway}/t/{args.task_id}/v1",
                effort=args.effort,
                mcp_command=str(args.mcp),
                channels=args.channels,
            ),
            encoding="utf-8",
        )
        prompt = args.output.with_name("system-prompt.md")
        prompt.write_text(
            agents.grok_system_prompt(base_instructions, skill), encoding="utf-8"
        )
        print(f"Grok system prompt: {prompt}")
        print(
            "Grok environment: "
            + json.dumps({
                "GROK_HOME": str(args.output.parent),
                "GROK_GATEWAY_TOKEN": "placeholder",
            })
        )
    print(args.output)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent", choices=AGENTS, required=True)
    parser.add_argument(
        "--model",
        default="",
        help="Model name; defaults to the agent's own default.",
    )
    parser.add_argument(
        "--effort",
        default="",
        help="Reasoning effort or, for Gemini CLI, a thinking level.",
    )
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
    parser.add_argument(
        "--channels",
        default="",
        choices=("", "a11y,keyboard", "gui,keyboard"),
        help="Expose only one desktop channel; empty exposes both tools.",
    )
    parser.add_argument("--codex-home", default=GUEST_CODEX_HOME)
    args = parser.parse_args()
    profile = agents.AGENT_PROFILES[args.agent]
    args.model = args.model or profile["default_model"]
    args.effort = args.effort or profile["default_effort"]
    args.output.parent.mkdir(parents=True, exist_ok=True)
    render(args)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
