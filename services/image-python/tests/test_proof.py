from PIL import Image

from app.proof.render import render_proof, BANNER_HEIGHT_PX


def test_proof_is_taller_than_artwork_by_the_banner_height():
    artwork = Image.new("RGB", (100, 50), (10, 20, 30))
    proof = render_proof(artwork, "PROOF - order abc - v1")
    assert proof.width == 100
    assert proof.height == 50 + BANNER_HEIGHT_PX


def test_proof_preserves_original_artwork_pixels_unaltered():
    # The banner is an added strip below the artwork, never an overlay drawn
    # on top of it - every pixel the customer is approving must survive
    # untouched.
    artwork = Image.new("RGB", (20, 20), (200, 0, 0))
    proof = render_proof(artwork, "caption")
    for x in range(0, 20, 5):
        for y in range(0, 20, 5):
            assert proof.getpixel((x, y)) == (200, 0, 0)


def test_proof_handles_non_rgb_source_mode():
    # A grayscale (or RGBA) source must not crash - the function converts to
    # RGB itself rather than assuming the caller already did.
    artwork = Image.new("L", (10, 10), 128)
    proof = render_proof(artwork, "caption")
    assert proof.mode == "RGB"
