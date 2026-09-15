"""Verifies a repair by decoding both the original and the repaired canvas
and comparing DECODED PIXEL ARRAYS for the original content region exactly -
never raw file bytes or hashes (a lossless re-encode can differ byte-for-byte
while being pixel-identical, and that must still pass) and never a
perceptual/similarity score. An encode/decode change must not hide under an
approximate check.
"""

from PIL import Image, ImageChops


def verify_repair(
    original: Image.Image,
    repaired_canvas: Image.Image,
    trim_x_px: int,
    trim_y_px: int,
    trim_width_px: int,
    trim_height_px: int,
) -> bool:
    original_rgb = original.convert("RGB")
    region = repaired_canvas.crop(
        (trim_x_px, trim_y_px, trim_x_px + trim_width_px, trim_y_px + trim_height_px)
    ).convert("RGB")

    if original_rgb.size != region.size:
        return False

    diff = ImageChops.difference(original_rgb, region)
    return diff.getbbox() is None  # None means pixel-identical
