"""Bleed coverage check - only runs for full-bleed intent (border designs
don't need it and must not auto-fail for lacking it; the caller simply
doesn't call this for intent='border').

Without a confirmed trim rectangle, this always returns NEEDS_INPUT rather
than guessing a boundary from image content, per the brief. Once trim is
confirmed, this measures the actual margin between the trim and the full
canvas, using the SAME effective_ppi the resolution check computed from the
trim region - never recomputed from total canvas size (see CLAUDE.md point
1: a repair-time recheck must use the trim's PPI, not the larger canvas's).
"""

from typing import Optional

RULE_VERSION = "bleed-v1"
REQUIRED_BLEED_IN = 0.0625


def check_bleed(
    intent: str,
    trim_confirmed: bool,
    image_width_px: Optional[int] = None,
    image_height_px: Optional[int] = None,
    trim_width_px: Optional[int] = None,
    trim_height_px: Optional[int] = None,
    effective_ppi: Optional[float] = None,
) -> Optional[dict]:
    if intent != "full_bleed":
        return None

    if not trim_confirmed:
        return {
            "check_name": "bleed",
            "result": "NEEDS_INPUT",
            "evidence": {
                "required_bleed_in": REQUIRED_BLEED_IN,
                "reason": "trim rectangle not confirmed - cannot measure bleed coverage without guessing from image content",
            },
            "rule_version": RULE_VERSION,
        }

    margin_x_px = (image_width_px - trim_width_px) / 2
    margin_y_px = (image_height_px - trim_height_px) / 2
    available_bleed_in = min(margin_x_px, margin_y_px) / effective_ppi

    evidence = {
        "required_bleed_in": REQUIRED_BLEED_IN,
        "available_bleed_in": round(available_bleed_in, 4),
        "trim_width_px": trim_width_px,
        "trim_height_px": trim_height_px,
        "image_width_px": image_width_px,
        "image_height_px": image_height_px,
    }

    if available_bleed_in + 1e-9 >= REQUIRED_BLEED_IN:
        return {"check_name": "bleed", "result": "PASS", "evidence": evidence, "rule_version": RULE_VERSION}

    evidence["note"] = "insufficient bleed margin - eligible for uniform-background extension repair if the edge is opaque and uniform"
    return {"check_name": "bleed", "result": "NEEDS_REVIEW", "evidence": evidence, "rule_version": RULE_VERSION}
