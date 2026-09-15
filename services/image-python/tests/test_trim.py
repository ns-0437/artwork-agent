from app.checks.trim import resolve_trim


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
