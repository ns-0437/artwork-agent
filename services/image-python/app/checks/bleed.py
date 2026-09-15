"""Bleed coverage check.

Only runs for full-bleed intent (border designs don't need it and must not
auto-fail for lacking it - the caller simply doesn't call this for
intent='border').

v1 has no trim-selection input: there is no way to confirm whether an
uploaded full-bleed-intent image is (a) trim-only with no bleed margin
added yet, or (b) trim+bleed combined with the trim positioned somewhere
inside it. Per the brief - "If trim/placement is unknown, return NEEDS_INPUT
rather than guessing from edge pixels" - this check always returns
NEEDS_INPUT for now rather than inferring a boundary from image content.

Day 3's repair sets trim coordinates explicitly (it knows exactly where it
placed the original content when it built the new canvas), which is what
makes a real measurement possible; that path re-runs this check with known
trim coordinates instead of calling it blind like this.
"""

RULE_VERSION = "bleed-v1"
REQUIRED_BLEED_IN = 0.0625


def check_bleed(intent: str) -> dict | None:
    if intent != "full_bleed":
        return None

    return {
        "check_name": "bleed",
        "result": "NEEDS_INPUT",
        "evidence": {
            "required_bleed_in": REQUIRED_BLEED_IN,
            "reason": "trim rectangle not confirmed - cannot measure bleed coverage without guessing from image content",
        },
        "rule_version": RULE_VERSION,
    }
