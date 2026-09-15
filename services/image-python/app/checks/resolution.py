"""Resolution check: effective PPI at the requested physical size.

Takes the TRIM region explicitly - never the whole decoded image - so this
stays correct once repair can produce a canvas larger than its trim. Before
a trim rectangle is confirmed, the caller must not invoke this at all (see
trim.py); post-repair, the caller passes the trim api-go already knows it
placed the original content at, not the new canvas's full dimensions.
"""

RULE_VERSION = "resolution-v1"
MIN_PPI = 300


def check_resolution(trim_width_px: int, trim_height_px: int, declared_width_in: float, declared_height_in: float) -> dict:
    effective_ppi = min(trim_width_px / declared_width_in, trim_height_px / declared_height_in)

    evidence = {
        "effective_ppi": round(effective_ppi, 2),
        "min_required_ppi": MIN_PPI,
        "trim_width_px": trim_width_px,
        "trim_height_px": trim_height_px,
        "declared_width_in": declared_width_in,
        "declared_height_in": declared_height_in,
    }

    if effective_ppi < MIN_PPI:
        evidence["note"] = "below the 300 PPI guideline - offer a smaller printable size or request higher-resolution source art"
        return {"check_name": "resolution", "result": "NEEDS_INPUT", "evidence": evidence, "rule_version": RULE_VERSION}

    return {"check_name": "resolution", "result": "PASS", "evidence": evidence, "rule_version": RULE_VERSION}
