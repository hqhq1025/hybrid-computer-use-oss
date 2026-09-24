from integrations.common.evaluator_channels import run_official_evaluator


def test_numeric_result_becomes_score_payload():
    channel = run_official_evaluator(lambda: 1)
    assert channel == {
        "status": "completed",
        "result": {"score": 1.0},
        "error": None,
        "traceback": None,
    }


def test_dict_result_keeps_details_and_normalizes_score():
    channel = run_official_evaluator(lambda: {"score": 1, "detail": "ok"})
    assert channel["status"] == "completed"
    assert channel["result"] == {"score": 1.0, "detail": "ok"}


def test_evaluator_exception_is_recorded_not_raised():
    def broken():
        raise RuntimeError("getter failed")

    channel = run_official_evaluator(broken)
    assert channel["status"] == "error"
    assert channel["result"] is None
    assert channel["error"] == "RuntimeError: getter failed"
    assert "RuntimeError: getter failed" in channel["traceback"]


def test_non_numeric_score_is_recorded_as_evaluator_error():
    channel = run_official_evaluator(lambda: None)
    assert channel["status"] == "error"
    assert channel["error"].startswith("TypeError:")
