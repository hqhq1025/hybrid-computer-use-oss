from integrations.common.evaluator_channels import evaluate_official_only


def test_official_only_evaluator_accepts_disabled_override_installer():
    channels = evaluate_official_only(
        task_id="001",
        evaluate=lambda: 1.0,
        installer=lambda task_id: {
            "installed": False,
            "task_id": task_id,
            "reason": "audited_overrides_disabled",
            "modules": [],
        },
    )
    assert channels["official_score"] == 1.0
    assert channels["override_installation"]["reason"] == (
        "audited_overrides_disabled"
    )
