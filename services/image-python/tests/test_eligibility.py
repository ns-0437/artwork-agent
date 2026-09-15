from PIL import Image

from app.repair.eligibility import check_eligibility


def test_uniform_solid_image_is_eligible():
    img = Image.new("RGB", (100, 100), (30, 120, 200))
    result = check_eligibility(img)
    assert result["eligible"] is True
    assert result["edge_color"] == (30, 120, 200)


def test_transparent_image_is_ineligible():
    img = Image.new("RGBA", (100, 100), (30, 120, 200, 128))
    result = check_eligibility(img)
    assert result["eligible"] is False
    assert "transparency" in result["reason"]


def test_fully_opaque_rgba_is_still_eligible():
    img = Image.new("RGBA", (100, 100), (30, 120, 200, 255))
    result = check_eligibility(img)
    assert result["eligible"] is True


def test_gradient_edge_is_ineligible():
    img = Image.new("RGB", (100, 100))
    px = img.load()
    for x in range(100):
        for y in range(100):
            px[x, y] = (x * 2, 0, 0)  # left-to-right gradient touches every edge
    result = check_eligibility(img)
    assert result["eligible"] is False
    assert "uniform color" in result["reason"]


def test_foreground_touching_boundary_is_ineligible():
    img = Image.new("RGB", (100, 100), (240, 240, 240))
    px = img.load()
    for x in range(100):
        px[x, 0] = (10, 10, 10)  # a dark line right on the top edge
    result = check_eligibility(img)
    assert result["eligible"] is False
