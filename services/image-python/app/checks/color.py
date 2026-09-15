"""Color mode/profile advisory. RGB is never a blocker on its own - it is an
advisory that screen colors may differ from print. CMYK passes outright
(supported natively, no conversion needed or performed). Anything else is
reported as NEEDS_REVIEW rather than guessed at.
"""

RULE_VERSION = "color-v1"


def check_color(mode: str, has_icc_profile: bool) -> dict:
    evidence = {"decoded_mode": mode, "has_icc_profile": has_icc_profile}

    if mode == "CMYK":
        return {"check_name": "color", "result": "PASS", "evidence": evidence, "rule_version": RULE_VERSION}

    if mode in ("RGB", "RGBA"):
        evidence["note"] = "RGB artwork - screen colors may differ in print" + (
            "" if has_icc_profile else " (no embedded ICC profile)"
        )
        return {"check_name": "color", "result": "WARNING", "evidence": evidence, "rule_version": RULE_VERSION}

    if mode in ("L", "LA", "1"):
        # Grayscale has no cross-color-space ambiguity the way RGB does.
        return {"check_name": "color", "result": "PASS", "evidence": evidence, "rule_version": RULE_VERSION}

    evidence["note"] = f"unrecognized or unsupported color mode: {mode}"
    return {"check_name": "color", "result": "NEEDS_REVIEW", "evidence": evidence, "rule_version": RULE_VERSION}
