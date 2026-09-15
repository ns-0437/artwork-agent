"""Repair eligibility: supported color mode + opaque + uniform-color edge
band. Conservative by design (CLAUDE.md point 11).

CMYK is explicitly rejected, not converted - naively converting CMYK to RGB
would reinterpret the channel values as if they meant something else,
silently corrupting the artwork's color rather than repairing it (CLAUDE.md
point 10: CMYK is inspect-only in v1, no repair path). Only RGB and
grayscale (L) are supported; RGBA/LA are accepted when fully opaque and
reduced to RGB/L (the alpha channel carries no information once it's
uniformly 255).
"""

from typing import Tuple, Union

from PIL import Image

EDGE_BAND_PX = 4
COLOR_TOLERANCE = 6  # max per-channel spread allowed within the edge band

SUPPORTED_MODES = {"RGB", "RGBA", "L", "LA"}


def _edge_band_pixels(image: Image.Image, band_px: int):
    width, height = image.size
    band = max(1, min(band_px, width // 2, height // 2))
    pixels = []
    for box in (
        (0, 0, width, band),  # top
        (0, height - band, width, height),  # bottom
        (0, 0, band, height),  # left
        (width - band, 0, width, height),  # right
    ):
        pixels.extend(image.crop(box).getdata())
    return pixels


def _spread(vals) -> int:
    return max(vals) - min(vals)


def check_eligibility(image: Image.Image) -> dict:
    """Returns {"eligible", "reason", "edge_color", "mode"}.

    "mode" is the working color mode ("RGB" or "L") the repair should
    operate in when eligible - matching the original's actual color
    information, never force-converted from a color space that would
    corrupt it (CMYK) or silently dropped (anything unrecognized).
    """
    if image.mode == "CMYK":
        return {
            "eligible": False,
            "reason": "CMYK artwork is inspect-only in v1 - repair does not support CMYK",
            "edge_color": None,
            "mode": None,
        }

    if image.mode not in SUPPORTED_MODES:
        return {
            "eligible": False,
            "reason": f"unsupported color mode for repair: {image.mode}",
            "edge_color": None,
            "mode": None,
        }

    working = image
    if image.mode in ("RGBA", "LA"):
        alpha = image.split()[-1]
        if alpha.getextrema()[0] < 255:
            return {
                "eligible": False,
                "reason": "image has transparency - repair requires a fully opaque image",
                "edge_color": None,
                "mode": None,
            }
        working = image.convert("RGB" if image.mode == "RGBA" else "L")

    pixels = _edge_band_pixels(working, EDGE_BAND_PX)
    if not pixels:
        return {"eligible": False, "reason": "image too small to sample an edge band", "edge_color": None, "mode": None}

    edge_color: Union[Tuple[int, int, int], int]
    if working.mode == "RGB":
        r_vals = [p[0] for p in pixels]
        g_vals = [p[1] for p in pixels]
        b_vals = [p[2] for p in pixels]
        if _spread(r_vals) > COLOR_TOLERANCE or _spread(g_vals) > COLOR_TOLERANCE or _spread(b_vals) > COLOR_TOLERANCE:
            return {
                "eligible": False,
                "reason": "edge band is not a uniform color - gradient, texture, or foreground content touches the boundary",
                "edge_color": None,
                "mode": None,
            }
        edge_color = (sum(r_vals) // len(r_vals), sum(g_vals) // len(g_vals), sum(b_vals) // len(b_vals))
    else:  # "L"
        vals = list(pixels)
        if _spread(vals) > COLOR_TOLERANCE:
            return {
                "eligible": False,
                "reason": "edge band is not a uniform color - gradient, texture, or foreground content touches the boundary",
                "edge_color": None,
                "mode": None,
            }
        edge_color = sum(vals) // len(vals)

    return {
        "eligible": True,
        "reason": "opaque image with a uniform-color edge band",
        "edge_color": edge_color,
        "mode": working.mode,
    }
