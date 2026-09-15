import pytest

from app.checks.resolution import check_resolution
from app.checks.units import to_inches


def test_299_ppi_flags_needs_input():
    result = check_resolution(299, 299, 1.0, 1.0)
    assert result["result"] == "NEEDS_INPUT"
    assert result["evidence"]["effective_ppi"] == 299.0


def test_300_ppi_is_the_inclusive_pass_boundary():
    result = check_resolution(300, 300, 1.0, 1.0)
    assert result["result"] == "PASS"
    assert result["evidence"]["effective_ppi"] == 300.0


def test_301_ppi_passes():
    result = check_resolution(301, 301, 1.0, 1.0)
    assert result["result"] == "PASS"


def test_uses_the_smaller_axis_regardless_of_orientation():
    # Portrait: width is the binding constraint.
    portrait = check_resolution(300, 900, 1.0, 3.0)
    assert portrait["result"] == "PASS"
    assert portrait["evidence"]["effective_ppi"] == 300.0

    # Landscape (swapped): height is now the binding constraint, same result.
    landscape = check_resolution(900, 300, 3.0, 1.0)
    assert landscape["result"] == "PASS"
    assert landscape["evidence"]["effective_ppi"] == 300.0


def test_one_axis_below_min_fails_even_if_the_other_is_fine():
    result = check_resolution(300, 200, 1.0, 1.0)
    assert result["result"] == "NEEDS_INPUT"
    assert result["evidence"]["effective_ppi"] == 200.0


def test_mm_to_inches_conversion_matches_the_brief_constant():
    assert to_inches(25.4, "mm") == 1.0
    assert to_inches(76.2, "mm") == pytest.approx(3.0)


def test_in_unit_is_passthrough():
    assert to_inches(3.0, "in") == 3.0


def test_600x600_px_at_3x3in_is_200_ppi():
    # The brief's own worked example.
    result = check_resolution(600, 600, 3.0, 3.0)
    assert result["evidence"]["effective_ppi"] == 200.0
    assert result["result"] == "NEEDS_INPUT"
