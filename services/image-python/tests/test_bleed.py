from app.checks.bleed import check_bleed


def test_border_intent_does_not_run_the_check_at_all():
    # Must not auto-fail for missing bleed - it doesn't produce a finding.
    assert check_bleed("border", trim_confirmed=False) is None
    assert check_bleed("border", trim_confirmed=True) is None


def test_full_bleed_intent_needs_input_without_a_confirmed_trim():
    result = check_bleed("full_bleed", trim_confirmed=False)
    assert result is not None
    assert result["result"] == "NEEDS_INPUT"
    assert result["check_name"] == "bleed"


def test_confirmed_trim_with_no_margin_needs_review():
    # trim == image bounds (the trim-only case, pre-repair): zero margin.
    result = check_bleed(
        "full_bleed",
        trim_confirmed=True,
        image_width_px=900,
        image_height_px=900,
        trim_width_px=900,
        trim_height_px=900,
        effective_ppi=300.0,
    )
    assert result["result"] == "NEEDS_REVIEW"
    assert result["evidence"]["available_bleed_in"] == 0.0


def test_confirmed_trim_with_sufficient_margin_passes():
    # 900px trim inside a 920px canvas at 300 PPI: 10px margin per side =
    # 10/300 = 0.0333in... not quite enough. Use exact numbers instead:
    # required 0.0625in * 300 PPI = 18.75px needed per side.
    result = check_bleed(
        "full_bleed",
        trim_confirmed=True,
        image_width_px=900 + 2 * 19,  # 19px margin per side, just over 18.75
        image_height_px=900 + 2 * 19,
        trim_width_px=900,
        trim_height_px=900,
        effective_ppi=300.0,
    )
    assert result["result"] == "PASS"


def test_confirmed_trim_with_insufficient_margin_needs_review():
    result = check_bleed(
        "full_bleed",
        trim_confirmed=True,
        image_width_px=900 + 2 * 10,  # only 10px margin per side, below 18.75
        image_height_px=900 + 2 * 10,
        trim_width_px=900,
        trim_height_px=900,
        effective_ppi=300.0,
    )
    assert result["result"] == "NEEDS_REVIEW"
    assert result["evidence"]["available_bleed_in"] < 0.0625
