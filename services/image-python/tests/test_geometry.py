from app.repair.geometry import validate_aspect_ratio, validate_declared_size


def test_positive_dimensions_are_valid():
    assert validate_declared_size(3.0, 3.0) is None


def test_zero_width_is_rejected():
    assert validate_declared_size(0.0, 3.0) is not None


def test_negative_height_is_rejected():
    assert validate_declared_size(3.0, -1.0) is not None


def test_matching_aspect_ratio_is_valid():
    assert validate_aspect_ratio(900, 900, 3.0, 3.0) is None
    assert validate_aspect_ratio(1200, 600, 4.0, 2.0) is None


def test_mismatched_aspect_ratio_is_rejected():
    # Declared square, but artwork is clearly not square.
    error = validate_aspect_ratio(900, 450, 3.0, 3.0)
    assert error is not None
    assert "aspect ratio" in error


def test_small_rounding_differences_are_tolerated():
    # 899x900 vs a declared 3x3in trim is a sub-1% mismatch from pixel
    # rounding, not a real aspect mismatch - must not falsely reject.
    assert validate_aspect_ratio(899, 900, 3.0, 3.0) is None
