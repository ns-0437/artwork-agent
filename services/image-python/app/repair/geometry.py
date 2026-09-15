"""Physical-dimension and aspect-ratio validation for repair.

A single effective_ppi (min across both axes, per the brief's resolution
formula) can falsely appear to satisfy bleed when the artwork's pixel
aspect ratio doesn't match the declared physical aspect ratio: one axis
ends up computed against the WRONG axis's PPI, under- or over-estimating
its margin in inches without the check ever seeing the discrepancy.
Computing genuinely correct per-axis PPI is a larger change; for now,
reject the mismatch outright rather than risk silently stretching the
artwork's implied scale to fit.
"""

from typing import Optional

ASPECT_TOLERANCE = 0.02  # 2% relative tolerance for pixel rounding


def validate_declared_size(declared_width_in: float, declared_height_in: float) -> Optional[str]:
    if declared_width_in <= 0 or declared_height_in <= 0:
        return "declared width/height must be positive"
    return None


def validate_aspect_ratio(
    image_width_px: int, image_height_px: int, declared_width_in: float, declared_height_in: float
) -> Optional[str]:
    image_aspect = image_width_px / image_height_px
    declared_aspect = declared_width_in / declared_height_in
    if abs(image_aspect - declared_aspect) > declared_aspect * ASPECT_TOLERANCE:
        return (
            f"artwork aspect ratio ({image_width_px}x{image_height_px}px) does not match the "
            f"declared sticker dimensions ({declared_width_in:.3f}x{declared_height_in:.3f}in) closely "
            "enough to repair safely - a single PPI can't correctly convert both axes when they "
            "diverge; provide artwork matching the declared aspect ratio, or correct the declared size"
        )
    return None
