"""Build the Judge prompt from the task and the agent's final answer."""

from __future__ import annotations

import json
from pathlib import Path

from hcu.osworld.prompts import INFEASIBLE_SENTINEL
from hcu.osworld.task import task_value


JUDGE_RETRY_CONVERGENCE_PROMPT = """

Bounded recovery attempt:
- A prior independent Judge attempt ended before producing a valid verdict.
- Read `/tmp/osw-live-judge-sealed/prior_judge_execution_evidence.jsonl`
  first. It contains only completed tool events from that attempt, with
  reasoning removed. Reuse its successful reads and outputs instead of
  repeating them.
- Do not repeat broad discovery or inventory every object.
- Reuse sealed evidence and inspect only the smallest unresolved,
  outcome-determinative facts.
- Prefer one structured API/artifact read over many UI reads.
- Do not use GUI navigation, repeated accessibility trees, browser-profile
  archaeology, or broad filesystem searches unless the unresolved fact exists
  only there.
- For a source-derived artifact, inspect the delivered artifact and
  authoritative source first, then run one bounded reconciliation. Do not
  re-check send or attachment state before semantic correctness.
- If the task says source records may arrive later or requests notifications,
  refresh the source and enumerate unread/activity/new records before PASS.
  Sampling only conversations visible at setup is not complete evidence.
- If the instruction restricts the source set, do not perform external web
  research for expected content. Use only the permitted sources; external
  material cannot supplement, correct or contradict them.
- If a concrete contradiction is found, stop immediately and return FAIL with
  the exact expected and observed values.
- If the decisive source cannot be inspected by the midpoint of this retry
  budget, stop exploration and return FAIL with INSPECTION_BLOCKED.
- Stop inspection early enough to produce the required schema-valid verdict.
- A concrete contradiction is sufficient for FAIL; complete positive evidence
  is required for PASS.
""".strip()


def judge_instruction_text(
    instruction: str,
    phases: list[dict],
) -> str:
    if not phases:
        return instruction
    sections = []
    for index, phase in enumerate(phases, start=1):
        name = str(phase.get("name") or f"Phase {index}")
        phase_instruction = str(phase.get("instruction") or instruction)
        sections.append(f"Phase {index} - {name}\n{phase_instruction}")
    return "\n\n".join(sections)


def judge_user_context(task, phase_details: list[dict]) -> dict | None:
    simulator = task_value(task, "user_simulator")
    exchanges = []
    for phase in phase_details:
        for exchange in phase.get("agent", {}).get("user_exchanges", []):
            exchanges.append({
                "phase_index": exchange.get("phase_index"),
                "phase_name": exchange.get("phase_name"),
                "turn_index": exchange.get("turn_index"),
                "question": exchange.get("question"),
                "answer": exchange.get("answer"),
            })

    context: dict[str, object] = {}
    if isinstance(simulator, dict):
        for key in ("knowledge", "response", "default_response"):
            if key in simulator:
                context[key] = simulator[key]
    if exchanges:
        context["actual_exchanges"] = exchanges
    if not context:
        return None
    return json.loads(json.dumps(context, ensure_ascii=False, default=str))


def build_judge_prompt(
    template_path: Path,
    *,
    instruction: str,
    final_response: str,
    declared_infeasible: bool,
    current_date: str,
    timezone_name: str,
    user_context: dict | None = None,
) -> str:
    template = template_path.read_text(encoding="utf-8")
    rendered = template.format(
        instruction=instruction,
        final_response=final_response or "(no final response recorded)",
        completion_declaration=(
            INFEASIBLE_SENTINEL
            if declared_infeasible
            else "No TASK_INFEASIBLE declaration recorded."
        ),
    )
    prompt = (
        "Authoritative run-bound date context:\n"
        f"- task current date: {current_date}\n"
        f"- task timezone: {timezone_name}\n"
        "Use this immutable date for relative terms such as today, tomorrow, "
        "and weekdays. The live guest clock is diagnostic only.\n\n"
        + rendered
    )
    if user_context is not None:
        prompt += (
            "\n\n## Task-Provided User Context\n\n"
            "The harness supplied the following sanitized user-response "
            "context. Treat its knowledge and fixed responses as "
            "authoritative user facts or clarification rules for judging the "
            "outcome. They do not prove that the Task Agent asked for or "
            "received those facts; only entries under `actual_exchanges` "
            "prove an exchange occurred. Do not infer any omitted task "
            "configuration, evaluator rule, or gold answer from this block.\n\n"
            "```json\n"
            + json.dumps(user_context, ensure_ascii=False, indent=2)
            + "\n```\n"
        )
    return prompt
