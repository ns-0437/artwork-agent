from PIL import Image

from app.repair.extend_background import extend_background
from app.repair.verify import verify_repair


def test_a_correct_extension_verifies():
    img = Image.new("RGB", (900, 900), (10, 20, 30))
    result = extend_background(img, edge_color=(10, 20, 30), effective_ppi=300.0)

    ok = verify_repair(img, result["image"], result["trim_x_px"], result["trim_y_px"], result["trim_width_px"], result["trim_height_px"])
    assert ok is True


def test_a_tampered_trim_region_fails_verification():
    img = Image.new("RGB", (900, 900), (10, 20, 30))
    result = extend_background(img, edge_color=(10, 20, 30), effective_ppi=300.0)

    canvas = result["image"].copy()
    tx, ty = result["trim_x_px"], result["trim_y_px"]
    canvas.putpixel((tx + 5, ty + 5), (255, 0, 0))  # corrupt one pixel inside the trim region

    ok = verify_repair(img, canvas, tx, ty, result["trim_width_px"], result["trim_height_px"])
    assert ok is False


def test_a_lossless_reencode_still_verifies_pixel_identical():
    import io

    img = Image.new("RGB", (300, 300), (77, 88, 99))
    result = extend_background(img, edge_color=(77, 88, 99), effective_ppi=300.0)

    # Simulate the canvas having been saved and reloaded as PNG (a lossless
    # re-encode) before verification - pixel content is identical even
    # though the file bytes would differ from an in-memory image.
    buf = io.BytesIO()
    result["image"].save(buf, format="PNG")
    buf.seek(0)
    reloaded = Image.open(buf)
    reloaded.load()

    ok = verify_repair(img, reloaded, result["trim_x_px"], result["trim_y_px"], result["trim_width_px"], result["trim_height_px"])
    assert ok is True
