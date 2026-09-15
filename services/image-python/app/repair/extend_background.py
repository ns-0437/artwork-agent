"""Executes the uniform-background bleed extension in the ELIGIBLE image's
own working mode (RGB or L, as determined by eligibility.check_eligibility)
- never force-converted to RGB from a different color space. Builds a new
canvas sized to the required margin, fills the new margin with the verified
edge color, and pastes the original content unresampled at its native
resolution. Required margin is rounded OUTWARD to pixels, matching the
brief's own worked example (18.75px -> 19px).

The projected canvas size is checked against MAX_CANVAS_PIXELS BEFORE
Image.new() ever runs - an abnormally large effective_ppi (e.g. from a tiny
declared physical size) must not be allowed to drive a memory-exhausting
allocation.
"""

import math

from PIL import Image

REQUIRED_BLEED_IN = 0.0625
MAX_CANVAS_PIXELS = 25_000_000  # matches the decoded-image pixel cap elsewhere


class CanvasTooLargeError(ValueError):
    pass


def extend_background(image: Image.Image, mode: str, edge_color, effective_ppi: float) -> dict:
    width, height = image.size
    margin_px = math.ceil(REQUIRED_BLEED_IN * effective_ppi)

    new_width = width + 2 * margin_px
    new_height = height + 2 * margin_px

    if new_width * new_height > MAX_CANVAS_PIXELS:
        raise CanvasTooLargeError(
            f"expanded canvas ({new_width}x{new_height} = {new_width * new_height} px) would exceed "
            f"the {MAX_CANVAS_PIXELS}-pixel limit - refusing to allocate it"
        )

    working_image = image if image.mode == mode else image.convert(mode)
    canvas = Image.new(mode, (new_width, new_height), edge_color)
    canvas.paste(working_image, (margin_px, margin_px))

    return {
        "image": canvas,
        "trim_x_px": margin_px,
        "trim_y_px": margin_px,
        "trim_width_px": width,
        "trim_height_px": height,
        "margin_px": margin_px,
    }
