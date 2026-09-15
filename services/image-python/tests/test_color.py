from app.checks.color import check_color


def test_cmyk_passes_outright():
    result = check_color("CMYK", has_icc_profile=True)
    assert result["result"] == "PASS"


def test_rgb_is_an_advisory_not_a_blocker():
    result = check_color("RGB", has_icc_profile=False)
    assert result["result"] == "WARNING"


def test_rgb_with_profile_is_still_only_an_advisory():
    result = check_color("RGB", has_icc_profile=True)
    assert result["result"] == "WARNING"
    assert "no embedded ICC profile" not in result["evidence"]["note"]


def test_rgb_without_profile_note_mentions_missing_profile():
    result = check_color("RGB", has_icc_profile=False)
    assert "no embedded ICC profile" in result["evidence"]["note"]


def test_grayscale_passes():
    result = check_color("L", has_icc_profile=False)
    assert result["result"] == "PASS"


def test_unrecognized_mode_needs_review_not_guessed_at():
    result = check_color("P", has_icc_profile=False)
    assert result["result"] == "NEEDS_REVIEW"
