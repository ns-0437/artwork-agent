"""Executes the uniform-background bleed extension: builds a new canvas
sized to the required margin, fills the new margin with the verified edge
color, and pastes the original content unresampled at its native resolution.
Required margin is rounded OUTWARD to pixels, matching the brief's own
worked example (18.75px -> 19px).
"""

import math

from PIL import Image

REQUIRED_BLEED_IN = 0.0625


def extend_background(image: Image.Image, edge_color: tuple, effective_ppi: float) -> dict:
    """Returns a dict with the new canvas and the trim rectangle within it:
    {"image", "trim_x_px", "trim_y_px", "trim_width_px", "trim_height_px", "margin_px"}.
    """
    width, height = image.size
    margin_px = math.ceil(REQUIRED_BLEED_IN * effective_ppi)

    new_width = width + 2 * margin_px
    new_height = height + 2 * margin_px

    canvas = Image.new("RGB", (new_width, new_height), edge_color)
    canvas.paste(image.convert("RGB"), (margin_px, margin_px))

    return {
        "image": canvas,
        "trim_x_px": margin_px,
        "trim_y_px": margin_px,
        "trim_width_px": width,
        "trim_height_px": height,
        "margin_px": margin_px,
    }
