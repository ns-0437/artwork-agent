from app.checks.bleed import check_bleed


def test_border_intent_does_not_run_the_check_at_all():
    # Must not auto-fail for missing bleed - it doesn't produce a finding.
    assert check_bleed("border") is None


def test_full_bleed_intent_needs_input_without_a_confirmed_trim():
    result = check_bleed("full_bleed")
    assert result is not None
    assert result["result"] == "NEEDS_INPUT"
    assert result["check_name"] == "bleed"
