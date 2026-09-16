from app.checks.trim import resolve_trim, unresolved_trim_reason


def test_border_intent_trim_is_always_the_whole_image():
    trim = resolve_trim("border", artwork_is_trim_only=None, image_width_px=1000, image_height_px=800)
    assert trim is not None
    assert (trim.width_px, trim.height_px) == (1000, 800)


def test_full_bleed_unconfirmed_is_ambiguous():
    assert resolve_trim("full_bleed", artwork_is_trim_only=None, image_width_px=1000, image_height_px=1000) is None
    assert resolve_trim("full_bleed", artwork_is_trim_only=False, image_width_px=1000, image_height_px=1000) is None


def test_full_bleed_confirmed_trim_only_is_the_whole_image():
    trim = resolve_trim("full_bleed", artwork_is_trim_only=True, image_width_px=900, image_height_px=900)
    assert trim is not None
    assert (trim.width_px, trim.height_px) == (900, 900)


def test_unresolved_reason_distinguishes_never_asked_from_confirmed_false():
    # Never asked - the generic "not confirmed" wording, which
    # worker.hasUnconfirmedTrimFinding keys off to allow ask_clarification.
    never_asked = unresolved_trim_reason("full_bleed", None)
    assert "not confirmed" in never_asked

    # Already answered "no" - must NOT contain that same substring, so the
    # agent escalates instead of re-asking the same already-answered
    # question forever.
    confirmed_false = unresolved_trim_reason("full_bleed", False)
    assert "not confirmed" not in confirmed_false
    assert "escalate" in confirmed_false


def test_unresolved_reason_for_border_intent_is_the_generic_wording():
    # Border intent never actually reaches this helper in practice (its
    # trim always resolves), but the helper itself should default to the
    # generic wording rather than the full-bleed-specific one.
    assert "not confirmed" in unresolved_trim_reason("border", False)


def test_explicit_trim_dimensions_always_win():
    # Simulates a post-repair recheck: api-go knows the real trim inside a
    # larger canvas, regardless of intent or artwork_is_trim_only.
    trim = resolve_trim(
        "full_bleed",
        artwork_is_trim_only=None,
        image_width_px=950,
        image_height_px=950,
        explicit_trim_width_px=900,
        explicit_trim_height_px=900,
    )
    assert (trim.width_px, trim.height_px) == (900, 900)
