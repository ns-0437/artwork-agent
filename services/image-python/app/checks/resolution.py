"""Resolution check: effective PPI at the requested physical size.

v1 treats the whole decoded image as the trim region for this check - there
is no trim-selection input yet, and the brief's own worked example ("A 600 x
600 pixel trim at 3 x 3 inches is 200 PPI") measures the full image the same
way. This does not change for full-bleed intent: resolution is about pixel
density of the content, independent of whether a bleed margin is also
present (see bleed.py for how trim/bleed ambiguity is actually handled).
"""

RULE_VERSION = "resolution-v1"
MIN_PPI = 300


def check_resolution(image_width_px: int, image_height_px: int, declared_width_in: float, declared_height_in: float) -> dict:
    effective_ppi = min(image_width_px / declared_width_in, image_height_px / declared_height_in)

    evidence = {
        "effective_ppi": round(effective_ppi, 2),
        "min_required_ppi": MIN_PPI,
        "trim_width_px": image_width_px,
        "trim_height_px": image_height_px,
        "declared_width_in": declared_width_in,
        "declared_height_in": declared_height_in,
    }

    if effective_ppi < MIN_PPI:
        evidence["note"] = "below the 300 PPI guideline - offer a smaller printable size or request higher-resolution source art"
        return {"check_name": "resolution", "result": "NEEDS_INPUT", "evidence": evidence, "rule_version": RULE_VERSION}

    return {"check_name": "resolution", "result": "PASS", "evidence": evidence, "rule_version": RULE_VERSION}
