"""Renders an annotated before/after preview with trim and canvas-edge
(bleed) overlays. Overlays belong ONLY on this preview image - never on the
repaired file itself, which must stay byte-for-byte whatever extend_background
produced.
"""

from PIL import Image, ImageDraw

TRIM_COLOR = (0, 170, 0)
CANVAS_COLOR = (200, 0, 0)
OUTLINE_WIDTH = 2


def render_preview(canvas: Image.Image, trim_x_px: int, trim_y_px: int, trim_width_px: int, trim_height_px: int) -> Image.Image:
    preview = canvas.convert("RGB").copy()
    draw = ImageDraw.Draw(preview)

    draw.rectangle(
        [trim_x_px, trim_y_px, trim_x_px + trim_width_px - 1, trim_y_px + trim_height_px - 1],
        outline=TRIM_COLOR,
        width=OUTLINE_WIDTH,
    )
    draw.rectangle(
        [0, 0, preview.width - 1, preview.height - 1],
        outline=CANVAS_COLOR,
        width=OUTLINE_WIDTH,
    )
    return preview
