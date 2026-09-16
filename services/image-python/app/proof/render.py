"""Renders the customer-facing proof image: the final artwork (whichever
asset is current - original or repaired) with a caption banner identifying
the order/version and stating it is a proof awaiting approval, not a
production release.

Distinct from app/repair/preview.py's trim/bleed overlay preview - that one
is an internal audit artifact drawn ON TOP of the artwork for verifying a
repair; this one is the artifact loop step 5 of the brief hands to the
customer, and never overlays anything on top of the artwork content itself -
the caption lives in an added banner strip below it, so the artwork pixels
the customer is approving are never altered.
"""

from PIL import Image, ImageDraw, ImageFont

BANNER_HEIGHT_PX = 32
BANNER_COLOR = (25, 25, 25)
TEXT_COLOR = (255, 255, 255)
TEXT_MARGIN_PX = 8


def render_proof(artwork: Image.Image, caption: str) -> Image.Image:
    base = artwork.convert("RGB")
    proof = Image.new("RGB", (base.width, base.height + BANNER_HEIGHT_PX), BANNER_COLOR)
    proof.paste(base, (0, 0))

    draw = ImageDraw.Draw(proof)
    draw.text((TEXT_MARGIN_PX, base.height + TEXT_MARGIN_PX), caption, fill=TEXT_COLOR, font=ImageFont.load_default())
    return proof
