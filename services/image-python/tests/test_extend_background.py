from PIL import Image

from app.repair.extend_background import extend_background


def test_extends_canvas_by_the_rounded_up_margin():
    img = Image.new("RGB", (900, 900), (10, 20, 30))
    result = extend_background(img, edge_color=(10, 20, 30), effective_ppi=300.0)

    # 0.0625in * 300 PPI = 18.75px -> rounds outward to 19px.
    assert result["margin_px"] == 19
    assert result["image"].size == (900 + 2 * 19, 900 + 2 * 19)
    assert (result["trim_x_px"], result["trim_y_px"]) == (19, 19)
    assert (result["trim_width_px"], result["trim_height_px"]) == (900, 900)


def test_pastes_original_unresampled_at_the_trim_offset():
    img = Image.new("RGB", (50, 40), (200, 100, 50))
    px = img.load()
    px[10, 10] = (1, 2, 3)  # a distinctive marker pixel

    result = extend_background(img, edge_color=(200, 100, 50), effective_ppi=300.0)
    canvas = result["image"]
    tx, ty = result["trim_x_px"], result["trim_y_px"]

    assert canvas.getpixel((tx + 10, ty + 10)) == (1, 2, 3)
    assert canvas.getpixel((tx, ty)) == (200, 100, 50)


def test_new_margin_is_filled_with_the_edge_color():
    img = Image.new("RGB", (60, 60), (5, 5, 5))
    result = extend_background(img, edge_color=(5, 5, 5), effective_ppi=300.0)
    canvas = result["image"]
    assert canvas.getpixel((0, 0)) == (5, 5, 5)
    assert canvas.getpixel((canvas.width - 1, canvas.height - 1)) == (5, 5, 5)
