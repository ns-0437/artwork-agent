"""Repair eligibility: opaque image + a uniform-color edge band. Conservative
by design - rejects gradients, transparency, textured edges, and foreground
objects touching the boundary. Passing eligibility is a demo-scope
conservative rule, not proof of manufacturing suitability (CLAUDE.md point
11).
"""

from typing import Optional, Tuple

from PIL import Image

EDGE_BAND_PX = 4
COLOR_TOLERANCE = 6  # max per-channel spread allowed within the edge band


def _edge_band_pixels(image: Image.Image, band_px: int):
    rgb = image.convert("RGB")
    width, height = rgb.size
    band = max(1, min(band_px, width // 2, height // 2))

    pixels = []
    for region_box in (
        (0, 0, width, band),  # top
        (0, height - band, width, height),  # bottom
        (0, 0, band, height),  # left
        (width - band, 0, width, height),  # right
    ):
        pixels.extend(rgb.crop(region_box).getdata())
    return pixels


def check_eligibility(image: Image.Image) -> dict:
    """Returns {"eligible": bool, "reason": str, "edge_color": (r,g,b) | None}."""
    if image.mode in ("RGBA", "LA") or (image.mode == "P" and "transparency" in image.info):
        alpha = image.convert("RGBA").split()[-1]
        if alpha.getextrema()[0] < 255:
            return {
                "eligible": False,
                "reason": "image has transparency - repair requires a fully opaque image",
                "edge_color": None,
            }

    pixels = _edge_band_pixels(image, EDGE_BAND_PX)
    if not pixels:
        return {"eligible": False, "reason": "image too small to sample an edge band", "edge_color": None}

    r_vals = [p[0] for p in pixels]
    g_vals = [p[1] for p in pixels]
    b_vals = [p[2] for p in pixels]

    def spread(vals):
        return max(vals) - min(vals)

    if spread(r_vals) > COLOR_TOLERANCE or spread(g_vals) > COLOR_TOLERANCE or spread(b_vals) > COLOR_TOLERANCE:
        return {
            "eligible": False,
            "reason": "edge band is not a uniform color - gradient, texture, or foreground content touches the boundary",
            "edge_color": None,
        }

    edge_color: Tuple[int, int, int] = (
        sum(r_vals) // len(r_vals),
        sum(g_vals) // len(g_vals),
        sum(b_vals) // len(b_vals),
    )
    return {"eligible": True, "reason": "opaque image with a uniform-color edge band", "edge_color": edge_color}
