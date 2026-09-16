"""Determines the trim rectangle for an order, or confirms it cannot be
determined without customer input. Centralizes trim policy so resolution.py
and bleed.py never reason about confirmation themselves - they always
receive already-resolved trim dimensions, or aren't called at all.
"""

from dataclasses import dataclass
from typing import Optional


@dataclass
class TrimRegion:
    width_px: int
    height_px: int


def resolve_trim(
    intent: str,
    artwork_is_trim_only: Optional[bool],
    image_width_px: int,
    image_height_px: int,
    explicit_trim_width_px: Optional[int] = None,
    explicit_trim_height_px: Optional[int] = None,
) -> Optional[TrimRegion]:
    """Returns the confirmed trim rectangle, or None if it cannot be
    determined without guessing from image content.

    - Explicit trim dimensions (post-repair re-check, where api-go already
      knows exactly where it placed the original content) always win.
    - intent == 'border': the whole image IS the trim, unambiguously - there
      is no separate bleed region to be confused with.
    - intent == 'full_bleed' and artwork_is_trim_only is True: the customer
      has explicitly confirmed the upload contains no bleed margin, so trim
      bounds = image bounds.
    - Otherwise (full_bleed intent, unconfirmed or explicitly not trim-only
      without explicit coordinates): genuinely ambiguous - the caller must
      not guess a boundary from pixel content.
    """
    if explicit_trim_width_px is not None and explicit_trim_height_px is not None:
        return TrimRegion(explicit_trim_width_px, explicit_trim_height_px)

    if intent == "border":
        return TrimRegion(image_width_px, image_height_px)

    if intent == "full_bleed" and artwork_is_trim_only is True:
        return TrimRegion(image_width_px, image_height_px)

    return None


def unresolved_trim_reason(intent: str, artwork_is_trim_only: Optional[bool]) -> str:
    """The reason text for a NEEDS_INPUT resolution/bleed finding when
    resolve_trim returns None. Distinguishes two genuinely different cases
    so the caller (worker.hasUnconfirmedTrimFinding, via a substring match
    on "trim rectangle not confirmed") only treats the FIRST as something a
    clarification can fix:

    - Never asked (artwork_is_trim_only is None): genuinely unconfirmed -
      ask_clarification is the right next step.
    - Already confirmed NOT trim-only (artwork_is_trim_only is False) for
      full-bleed intent: the customer says bleed is already included, but
      v1 has no mechanism to locate the trim boundary inside an
      already-bled upload without guessing from pixel content (out of
      scope - see CLAUDE.md's "no arbitrary cut contours"). Asking the
      SAME trim-only question again would just get the same answer again -
      this must escalate for manual review instead of looping.
    """
    if intent == "full_bleed" and artwork_is_trim_only is False:
        return (
            "artwork_is_trim_only is confirmed false (customer says bleed is already included), but this "
            "system has no way to locate the trim boundary inside an already-bled upload in v1 - escalate for "
            "manual review rather than re-asking the same trim-only question"
        )
    return "trim rectangle not confirmed - confirm whether the upload already includes bleed"
