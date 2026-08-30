"""Typed provider failure responses for the Linux Provider V2 boundary."""

from . import action_evidence
from .gui_input import InputEmissionError
from .recovery import bounded_recovery_context


def _bounded_recovery_response(operation):
    try:
        return bounded_recovery_context(operation)
    except Exception:
        return {
            "sourceAppStatus": "unknown",
            "sourceWindowStatus": "unknown",
            "inventoryComplete": False,
        }


def operation_failure_response(exc, recovery_operation=None):
    evidence = action_evidence.snapshot()
    emitted = bool(
        exc.input_events_emitted or evidence.get("input_emitted")
    )
    facts_complete = bool(exc.failure_facts_complete)
    if emitted != bool(exc.input_events_emitted):
        facts_complete = False
    failure_code = exc.failure_code
    failure_phase = exc.failure_phase
    error_text = str(exc)
    underlying_failure_code = ""
    if emitted:
        underlying_failure_code = failure_code
        failure_code = "effect_unknown"
        failure_phase = "post_action_observation"
        error_text = (
            "post_action_observation_failed; input was emitted; "
            "the final effect is unknown."
        )
        facts_complete = False
    response = {
        "ok": False,
        "error": error_text,
        "failureCode": failure_code,
        "failurePhase": failure_phase,
        "input_events_emitted": emitted,
        "partial_input_possible": False,
        "mutation_unknown": emitted,
        "failureFactsComplete": facts_complete,
    }
    if underlying_failure_code:
        response["underlyingFailureCode"] = underlying_failure_code
    if exc.failure_subject:
        response["failureSubject"] = exc.failure_subject
    if exc.replacement_window:
        response["replacementWindow"] = exc.replacement_window
    if emitted:
        response["guiLeaseState"] = "suspect"
    read_only_stale_window = bool(
        not emitted
        and failure_code == "stale_window_ref"
        and failure_phase == "precondition"
        and facts_complete
    )
    if recovery_operation is not None and (emitted or read_only_stale_window):
        response["recoveryContext"] = _bounded_recovery_response(
            recovery_operation
        )
    if evidence:
        evidence["input_emitted"] = bool(
            evidence.get("input_emitted") or emitted
        )
        response["actionEvidence"] = evidence
    if exc.candidates:
        response["candidates"] = exc.candidates
        response["candidateCount"] = exc.candidate_count
        response["candidatesTruncated"] = (
            exc.candidate_count > len(exc.candidates)
        )
    return response


def unexpected_operation_failure_response(exc, recovery_operation=None):
    evidence = action_evidence.snapshot()
    emitted = bool(
        evidence.get("input_emitted")
        or (
            isinstance(exc, InputEmissionError)
            and exc.events_emitted
        )
    )
    if evidence:
        evidence["input_emitted"] = emitted
    response = {
        "ok": False,
        "error": (
            "effect_unknown: an unexpected accessibility runtime failure "
            "occurred after input; inspect fresh state before retrying."
            if emitted
            else "input_failed: the accessibility runtime failed before input."
        ),
        "failureCode": "effect_unknown" if emitted else "input_failed",
        "failurePhase": "post_action_observation" if emitted else "emit",
        "input_events_emitted": emitted,
        "partial_input_possible": bool(
            emitted or isinstance(exc, InputEmissionError)
        ),
        "mutation_unknown": emitted,
        "failureFactsComplete": False,
    }
    if emitted:
        response["guiLeaseState"] = "suspect"
        if recovery_operation is not None:
            response["recoveryContext"] = _bounded_recovery_response(
                recovery_operation
            )
    if evidence:
        response["actionEvidence"] = evidence
    return response
