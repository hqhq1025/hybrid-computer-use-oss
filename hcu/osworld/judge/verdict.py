"""Validate Judge verdicts and decide whether they may score a run."""

from __future__ import annotations

from hcu.osworld.prompts import split_completion_message


JUDGE_RETRYABLE_FAILURES = {
    "judge_timeout",
    "judge_signal_15",
    "judge_nonzero_exit",
    "judge_invalid_output",
    "judge_control_failure",
    "judge_tool_budget_exhausted",
}


def valid_judge_verdict(verdict: object) -> bool:
    if not isinstance(verdict, dict):
        return False
    if verdict.get("judge_type") != "interactive_outcome":
        return False
    if verdict.get("instruction_verdict") not in {"PASS", "FAIL"}:
        return False
    requirements = verdict.get("requirements")
    if not isinstance(requirements, list) or not requirements:
        return False
    for requirement in requirements:
        if not isinstance(requirement, dict):
            return False
        if requirement.get("status") not in {"MET", "NOT_MET"}:
            return False
        coverage_mode = requirement.get("coverage_mode")
        if coverage_mode not in {
            "exact_single",
            "exhaustive_set",
            "representative_sample",
            "not_applicable",
        }:
            return False
        if coverage_mode == "exhaustive_set":
            expected_count = requirement.get("expected_count")
            inspected_count = requirement.get("inspected_count")
            if (
                not isinstance(expected_count, int)
                or isinstance(expected_count, bool)
                or expected_count < 0
                or not isinstance(inspected_count, int)
                or isinstance(inspected_count, bool)
                or inspected_count < 0
            ):
                return False

    verdict_name = verdict["instruction_verdict"]
    failure_reasons = verdict.get("failure_reasons")
    if verdict_name == "PASS":
        if any(row["status"] != "MET" for row in requirements):
            return False
        if failure_reasons != []:
            return False
        for requirement in requirements:
            coverage_mode = requirement["coverage_mode"]
            if coverage_mode == "representative_sample":
                return False
            if (
                coverage_mode == "exhaustive_set"
                and requirement["expected_count"]
                != requirement["inspected_count"]
            ):
                return False
    else:
        if not any(row["status"] == "NOT_MET" for row in requirements):
            return False
        if not isinstance(failure_reasons, list) or not failure_reasons:
            return False
    return True


def classify_judge_attempt(
    *,
    returncode: int | None,
    status: dict | None,
    execution_error: str | None,
    verdict: object,
    tool_budget_exhausted: bool = False,
) -> str | None:
    if tool_budget_exhausted:
        return "judge_tool_budget_exhausted"
    signal_number = (
        status.get("signal")
        if isinstance(status, dict)
        else None
    )
    if signal_number:
        return f"judge_signal_{signal_number}"
    if returncode == 124:
        return "judge_timeout"
    if returncode not in {0, None}:
        return "judge_nonzero_exit"
    if execution_error:
        return "judge_control_failure"
    if not valid_judge_verdict(verdict):
        return "judge_invalid_output"
    return None


def judge_retry_eligible(metadata: object) -> bool:
    if not isinstance(metadata, dict):
        return False
    return metadata.get("failure_classification") in JUDGE_RETRYABLE_FAILURES


def judge_answer_score_eligibility(
    *,
    truncated: bool,
    delivery: dict,
    final_message: str,
    done_payload: dict | None,
    official_evaluator_errors: list[dict],
) -> tuple[bool, str | None]:
    if official_evaluator_errors:
        return (
            False,
            "Official evaluator did not complete; Judge verdict is diagnostic "
            "until evaluator recovery.",
        )
    if truncated:
        return False, "Task Agent run was truncated."
    if not delivery["all_phases_delivered"]:
        return (
            False,
            "Not all declared task phases were delivered: "
            f"missing={delivery['missing_phase_numbers']}.",
        )
    _body, sentinel = split_completion_message(final_message)
    if sentinel is None:
        return False, "Task Agent produced no valid completion declaration."
    if (
        done_payload is not None
        and done_payload.get("returncode") not in {0, None}
    ):
        return False, "Task Agent completion process returned nonzero status."
    return True, None
