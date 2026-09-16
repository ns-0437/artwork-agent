"""Generates the Day 5 dev/held-out fixture set. Fixtures are split by
original *design* before any variant is added, so a design's variants never
end up split across dev and held-out.

Each design also carries the metadata evals/scripts/run_eval.py needs to
drive it through the real API and grade the result: customer_request,
whether/how to confirm trim before starting resolution, a scripted
clarification reply for the cases that deliberately leave it unconfirmed,
and the expected final artwork_status/proof_status/repair outcome.

Usage: `python evals/scripts/generate_fixtures.py` (re)writes every fixture
image and evals/fixtures/manifest.json from the DESIGNS list below.
"""

import json
import random
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent
FIXTURES_DIR = ROOT / "fixtures"

random.seed(42)


def make_solid_image(width_px: int, height_px: int, color, mode="RGB") -> Image.Image:
    return Image.new(mode, (width_px, height_px), color)


def make_gradient_image(width_px: int, height_px: int) -> Image.Image:
    """Left-to-right gradient touching every edge - non-uniform edge band,
    repair-ineligible."""
    img = Image.new("RGB", (width_px, height_px))
    px = img.load()
    for x in range(width_px):
        shade = int(255 * x / max(width_px - 1, 1))
        for y in range(height_px):
            px[x, y] = (shade, 40, 200 - shade // 2)
    return img


def make_textured_edge_image(width_px: int, height_px: int, base_color) -> Image.Image:
    """A checkerboard-noise edge band (high per-channel spread within the
    tolerance window) on an otherwise-uniform interior - repair-ineligible
    for a different reason than the gradient case (texture, not a smooth
    ramp)."""
    img = Image.new("RGB", (width_px, height_px), base_color)
    px = img.load()
    band = 6
    for x in range(width_px):
        for y in range(height_px):
            if x < band or x >= width_px - band or y < band or y >= height_px - band:
                if (x // 2 + y // 2) % 2 == 0:
                    px[x, y] = tuple(min(255, c + 60) for c in base_color)
    return img


def make_object_touching_edge_image(width_px: int, height_px: int, base_color, object_color) -> Image.Image:
    """A foreground rectangle touching the left edge - repair-ineligible:
    the edge band is not uniform because real content reaches the
    boundary, not just background."""
    img = Image.new("RGB", (width_px, height_px), base_color)
    px = img.load()
    obj_w = max(1, width_px // 6)
    for x in range(0, obj_w):
        for y in range(height_px // 4, 3 * height_px // 4):
            px[x, y] = object_color
    return img


def make_transparent_edge_image(width_px: int, height_px: int, color) -> Image.Image:
    """RGBA with partial (non-255) alpha in the edge band - repair requires
    full opacity, so this is ineligible for a third, distinct reason."""
    img = Image.new("RGBA", (width_px, height_px), (*color, 255))
    px = img.load()
    band = 6
    for x in range(width_px):
        for y in range(height_px):
            if x < band or x >= width_px - band or y < band or y >= height_px - band:
                r, g, b, _ = px[x, y]
                px[x, y] = (r, g, b, 180)
    return img


def make_rgba_opaque_image(width_px: int, height_px: int, color) -> Image.Image:
    """RGBA but fully opaque everywhere - eligible for repair once reduced
    to RGB (alpha carries no information)."""
    return Image.new("RGBA", (width_px, height_px), (*color, 255))


def make_grayscale_image(width_px: int, height_px: int, shade: int) -> Image.Image:
    return Image.new("L", (width_px, height_px), shade)


def make_cmyk_image(width_px: int, height_px: int, color) -> Image.Image:
    return Image.new("CMYK", (width_px, height_px), color)


def save(img: Image.Image, path: Path, icc_profile: bytes | None = None, image_format: str | None = None) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    kwargs = {}
    if icc_profile is not None:
        kwargs["icc_profile"] = icc_profile
    if image_format is not None:
        kwargs["format"] = image_format
    img.save(path, **kwargs)


# A tiny sRGB-ish ICC profile isn't needed for the fixtures that just need
# *an* embedded profile - Pillow requires real profile bytes to embed one,
# so fixtures that want "has_icc_profile=True" reuse a profile lifted from
# an in-memory image Pillow itself can produce (mode "RGB" default has
# none) - simplest reliable option: skip embedding for now and rely on the
# no-profile RGB advisory wording ("no embedded ICC profile") being the
# thing under test either way, since check_color only reports presence/
# absence, not profile correctness. (No fixture currently asserts
# has_icc_profile=True; if one is added later, generate the profile bytes
# with a real color-management library rather than fabricating a header.)


# Each entry is one *design*. "variants" (when present) generates several
# manifest entries / images from one design (e.g. a 299/300/301 PPI
# triplet) sharing everything except the overridden fields.
DESIGNS = [
    # --- dev: existing Day 1-4 fixtures, kept as-is ---
    {
        "id": "clean-a", "split": "dev", "kind": "solid", "color": (30, 120, 200),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 1000, "image_height_px": 1000,
        "customer_request": "Please check my sticker file before printing - I already added bleed myself.",
        "artwork_is_trim_only": False,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "notes": "Customer confirms 'not trim-only' (bleed already included) for full-bleed intent - v1 has no way "
        "to locate the trim boundary inside an already-bled upload, so this must escalate for manual review "
        "rather than the agent re-asking the same trim-only question the customer already answered (CLAUDE.md "
        "point 44).",
    },
    {
        "id": "lowres-a", "split": "dev", "kind": "solid", "color": (80, 180, 90),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "border",
        "image_width_px": 600, "image_height_px": 600,
        "customer_request": "Quick check please.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "notes": "3x3in declared size but only ~200 effective PPI - should flag resolution; border intent means no clarification question applies, so the agent escalates rather than asking one.",
    },
    {
        "id": "missing-bleed-a", "split": "dev", "kind": "solid", "color": (150, 90, 200),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "expect_repair": True, "expect_repair_status": "REPAIRED",
        "notes": "Full-bleed intent, trim-only confirmed upfront, exactly at trim size (zero bleed margin), uniform solid background - repair-eligible; agent should auto-repair to RESOLVED.",
    },
    {
        "id": "gradient-edge-a", "split": "dev", "kind": "gradient",
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Full-bleed, trim-only, but a left-to-right gradient touches every edge - repair-INELIGIBLE. Must be refused with a specific reason and leave the original untouched.",
    },

    # --- dev: new categories ---
    {
        "id": "clean-border-a", "split": "dev", "kind": "solid", "color": (200, 100, 30),
        "declared_width_in": 2.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 700, "image_height_px": 700,
        "customer_request": "Border sticker, please review.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "Clean border-intent artwork, no trim confirmation needed at all.",
    },
    {
        "id": "missing-bleed-clarify-a", "split": "dev", "kind": "solid", "color": (90, 150, 220),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Not sure if I already added bleed - please check.",
        "artwork_is_trim_only": None, "scripted_clarification_reply": "yes",
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "expect_repair": True, "expect_repair_status": "REPAIRED",
        "notes": "Full-bleed, trim confirmation deliberately left unanswered - exercises the ask_clarification -> answer 'yes' -> resume -> auto-repair -> RESOLVED round trip end to end.",
    },
    {
        "id": "textured-edge-a", "split": "dev", "kind": "textured", "color": (120, 120, 160),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Checkerboard-noise edge band, trim-only - repair-ineligible (texture, distinct from the gradient case).",
    },
    {
        "id": "object-touching-edge-a", "split": "dev", "kind": "object_touching",
        "color": (240, 240, 240), "object_color": (20, 20, 20),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "A dark foreground rectangle touches the left edge - repair-ineligible (real content at the boundary, not background).",
    },
    {
        "id": "transparency-edge-a", "split": "dev", "kind": "transparent_edge", "color": (100, 180, 100),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "transparency",
        "notes": "RGBA with partial alpha in the edge band - repair-ineligible (requires full opacity).",
    },
    {
        "id": "rgba-opaque-a", "split": "dev", "kind": "rgba_opaque", "color": (210, 160, 40),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "expect_repair": True, "expect_repair_status": "REPAIRED",
        "notes": "RGBA but fully opaque - eligible for repair once reduced to RGB (alpha carries no information).",
    },
    {
        "id": "grayscale-a", "split": "dev", "kind": "grayscale", "shade": 128,
        "declared_width_in": 2.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 700, "image_height_px": 700,
        "customer_request": "Grayscale design, please review.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "Grayscale (L mode) - color check PASSes outright, no RGB advisory.",
    },
    {
        "id": "cmyk-a", "split": "dev", "kind": "cmyk", "color": (10, 10, 10, 0), "image_format": "JPEG",
        "declared_width_in": 2.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 700, "image_height_px": 700,
        "customer_request": "CMYK design, please review.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "CMYK JPEG - color check PASSes outright (natively supported, never converted); inspect-only, no repair path exercised here.",
    },
    {
        "id": "mixed-issues-a", "split": "dev", "kind": "gradient",
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 600, "image_height_px": 600,  # 200 PPI at trim, AND zero bleed margin
        "customer_request": "Please check everything before printing.",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Low resolution (200 PPI) AND missing bleed (zero margin) at once, on a gradient (non-uniform) "
        "edge: bleed's NEEDS_REVIEW makes the agent correctly ATTEMPT repair (the finding matches "
        "hasConfirmedInsufficientBleedFinding), but eligibility rejects it (non-uniform edge) - resulting in "
        "NEEDS_REVIEW either way, whether or not the agent is enabled, since the low-resolution blocker has no "
        "supported fix and the repair attempt is correctly rejected rather than silently skipped.",
    },
    {
        "id": "ppi-boundary-dev", "split": "dev", "kind": "ppi_triplet", "color": (50, 140, 210),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "border",
        "customer_request": "Please confirm print resolution.",
        "artwork_is_trim_only": None,
        "notes": "Isolates the exact 300 PPI threshold (border intent avoids bleed noise): 299 PPI must NEEDS_INPUT/escalate, 300 and 301 PPI must PASS/RESOLVED.",
    },

    # --- held-out: existing Day 1-4 fixtures, kept as-is ---
    {
        "id": "clean-b", "split": "held-out", "kind": "solid", "color": (200, 60, 60),
        "declared_width_in": 2.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 700, "image_height_px": 700,
        "customer_request": "Please check my sticker file before printing.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "Clean 2x2in artwork at >=300 PPI, RGB, border intent (no bleed required).",
    },
    {
        "id": "rgb-noprofile-a", "split": "held-out", "kind": "solid", "color": (240, 200, 40),
        "declared_width_in": 2.5, "declared_height_in": 2.5, "intent": "border",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Please review.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "RGB, no embedded ICC profile - color check should return an advisory, not a blocker.",
    },
    {
        "id": "border-a", "split": "held-out", "kind": "solid", "color": (60, 60, 60),
        "declared_width_in": 4.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 1200, "image_height_px": 600,
        "customer_request": "Wide border sticker.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "Border intent - missing bleed must not auto-fail since bleed isn't required.",
    },

    # --- held-out: new categories (distinct designs from dev's, same coverage) ---
    {
        "id": "clean-full-bleed-b", "split": "held-out", "kind": "solid", "color": (40, 160, 210),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 1000, "image_height_px": 1000,
        "customer_request": "Please check before printing - I already added bleed myself.",
        "artwork_is_trim_only": False,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "notes": "Held-out counterpart to clean-a - confirmed 'not trim-only' for full-bleed intent must escalate, "
        "not loop back into the same clarification (CLAUDE.md point 44).",
    },
    {
        "id": "lowres-b", "split": "held-out", "kind": "solid", "color": (90, 200, 100),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "border",
        "image_width_px": 600, "image_height_px": 600,
        "customer_request": "Quick check please.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "notes": "Held-out counterpart to lowres-a - below 300 PPI, escalates.",
    },
    {
        "id": "missing-bleed-b", "split": "held-out", "kind": "solid", "color": (160, 100, 210),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "expect_repair": True, "expect_repair_status": "REPAIRED",
        "notes": "Held-out counterpart to missing-bleed-a - repair-eligible, must auto-repair to RESOLVED.",
    },
    {
        "id": "missing-bleed-clarify-b", "split": "held-out", "kind": "solid", "color": (100, 160, 230),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Not sure if I already added bleed.",
        "artwork_is_trim_only": None, "scripted_clarification_reply": "yes",
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "expect_repair": True, "expect_repair_status": "REPAIRED",
        "notes": "Held-out counterpart to missing-bleed-clarify-a - full clarify -> resume -> repair loop.",
    },
    {
        "id": "gradient-edge-b", "split": "held-out", "kind": "gradient",
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Held-out counterpart to gradient-edge-a - must be refused, original untouched.",
    },
    {
        "id": "textured-edge-b", "split": "held-out", "kind": "textured", "color": (140, 130, 170),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Held-out counterpart to textured-edge-a.",
    },
    {
        "id": "object-touching-edge-b", "split": "held-out", "kind": "object_touching",
        "color": (245, 245, 245), "object_color": (15, 15, 15),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Held-out counterpart to object-touching-edge-a.",
    },
    {
        "id": "transparency-edge-b", "split": "held-out", "kind": "transparent_edge", "color": (110, 190, 110),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "transparency",
        "notes": "Held-out counterpart to transparency-edge-a.",
    },
    {
        "id": "rgba-opaque-b", "split": "held-out", "kind": "rgba_opaque", "color": (220, 170, 50),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 900, "image_height_px": 900,
        "customer_request": "Is this ready to print?",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "expect_repair": True, "expect_repair_status": "REPAIRED",
        "notes": "Held-out counterpart to rgba-opaque-a.",
    },
    {
        "id": "grayscale-b", "split": "held-out", "kind": "grayscale", "shade": 90,
        "declared_width_in": 2.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 700, "image_height_px": 700,
        "customer_request": "Grayscale design, please review.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "Held-out counterpart to grayscale-a.",
    },
    {
        "id": "cmyk-b", "split": "held-out", "kind": "cmyk", "color": (20, 20, 20, 0), "image_format": "JPEG",
        "declared_width_in": 2.0, "declared_height_in": 2.0, "intent": "border",
        "image_width_px": 700, "image_height_px": 700,
        "customer_request": "CMYK design, please review.",
        "artwork_is_trim_only": None,
        "expected_artwork_status": "RESOLVED", "expected_proof_status": "AWAITING_CUSTOMER_APPROVAL",
        "notes": "Held-out counterpart to cmyk-a.",
    },
    {
        "id": "mixed-issues-b", "split": "held-out", "kind": "gradient",
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "full_bleed",
        "image_width_px": 600, "image_height_px": 600,
        "customer_request": "Please check everything before printing.",
        "artwork_is_trim_only": True,
        "expected_artwork_status": "NEEDS_REVIEW", "expected_proof_status": "NOT_PREPARED",
        "expect_repair": True, "expect_repair_status": "REJECTED", "unsafe_reason_substring": "not a uniform color",
        "notes": "Held-out counterpart to mixed-issues-a.",
    },
    {
        "id": "ppi-boundary-held-out", "split": "held-out", "kind": "ppi_triplet", "color": (60, 150, 220),
        "declared_width_in": 3.0, "declared_height_in": 3.0, "intent": "border",
        "customer_request": "Please confirm print resolution.",
        "artwork_is_trim_only": None,
        "notes": "Held-out counterpart to ppi-boundary-dev - a genuinely different design (different color/base), same 299/300/301 PPI boundary coverage.",
    },
]


def _base_manifest_fields(design: dict) -> dict:
    fields = {}
    for key in (
        "customer_request", "artwork_is_trim_only", "scripted_clarification_reply",
        "expected_artwork_status", "expected_proof_status", "expect_repair",
        "expect_repair_status", "unsafe_reason_substring", "trim_width_px", "trim_height_px", "notes",
    ):
        if key in design:
            fields[key] = design[key]
    return fields


def _write_variant(design: dict, variant_id: str, image_width_px: int, image_height_px: int,
                    declared_width_in: float, declared_height_in: float,
                    expected_artwork_status: str, expected_proof_status: str,
                    extra_notes: str, manifest: list) -> None:
    img = make_solid_image(image_width_px, image_height_px, design["color"])
    ext = "jpg" if design.get("image_format") == "JPEG" else "png"
    filename = f"{variant_id}_base.{ext}"
    out_path = FIXTURES_DIR / design["split"] / filename
    save(img, out_path, image_format=design.get("image_format"))

    entry = _base_manifest_fields(design)
    entry.update({
        "id": variant_id,
        "split": design["split"],
        "file": f"{design['split']}/{filename}",
        "declared_width_in": declared_width_in,
        "declared_height_in": declared_height_in,
        "intent": design["intent"],
        "image_width_px": image_width_px,
        "image_height_px": image_height_px,
        "expected_artwork_status": expected_artwork_status,
        "expected_proof_status": expected_proof_status,
        "notes": extra_notes,
    })
    manifest.append(entry)
    print(f"wrote {out_path.relative_to(ROOT)}")


def main():
    manifest = []
    for design in DESIGNS:
        kind = design["kind"]

        if kind == "ppi_triplet":
            # 3x3in declared; 299/300/301 effective PPI at that size.
            for ppi, expected in ((299, "NEEDS_REVIEW"), (300, "RESOLVED"), (301, "RESOLVED")):
                px = round(ppi * design["declared_width_in"])
                variant_id = f"{design['id']}-{ppi}"
                proof_status = "AWAITING_CUSTOMER_APPROVAL" if expected == "RESOLVED" else "NOT_PREPARED"
                _write_variant(
                    design, variant_id, px, px,
                    design["declared_width_in"], design["declared_height_in"],
                    expected, proof_status,
                    f"PPI boundary case: {px}px at {design['declared_width_in']}in = {ppi} effective PPI "
                    f"(300 PPI guideline; {ppi} {'is' if ppi < 300 else 'meets or exceeds'} the threshold).",
                    manifest,
                )
            continue

        if kind == "solid":
            img = make_solid_image(design["image_width_px"], design["image_height_px"], design["color"])
        elif kind == "gradient":
            img = make_gradient_image(design["image_width_px"], design["image_height_px"])
        elif kind == "textured":
            img = make_textured_edge_image(design["image_width_px"], design["image_height_px"], design["color"])
        elif kind == "object_touching":
            img = make_object_touching_edge_image(
                design["image_width_px"], design["image_height_px"], design["color"], design["object_color"]
            )
        elif kind == "transparent_edge":
            img = make_transparent_edge_image(design["image_width_px"], design["image_height_px"], design["color"])
        elif kind == "rgba_opaque":
            img = make_rgba_opaque_image(design["image_width_px"], design["image_height_px"], design["color"])
        elif kind == "grayscale":
            img = make_grayscale_image(design["image_width_px"], design["image_height_px"], design["shade"])
        elif kind == "cmyk":
            img = make_cmyk_image(design["image_width_px"], design["image_height_px"], design["color"])
        else:
            raise ValueError(f"unknown fixture kind: {kind}")

        ext = "jpg" if design.get("image_format") == "JPEG" else "png"
        filename = f"{design['id']}_base.{ext}"
        out_path = FIXTURES_DIR / design["split"] / filename
        save(img, out_path, image_format=design.get("image_format"))

        entry = _base_manifest_fields(design)
        entry.update({
            "id": design["id"],
            "split": design["split"],
            "file": f"{design['split']}/{filename}",
            "declared_width_in": design["declared_width_in"],
            "declared_height_in": design["declared_height_in"],
            "intent": design["intent"],
            "image_width_px": design["image_width_px"],
            "image_height_px": design["image_height_px"],
        })
        manifest.append(entry)
        print(f"wrote {out_path.relative_to(ROOT)}")

    manifest_path = FIXTURES_DIR / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2))
    n_dev = sum(1 for m in manifest if m["split"] == "dev")
    n_held = sum(1 for m in manifest if m["split"] == "held-out")
    print(f"wrote {manifest_path.relative_to(ROOT)} ({len(manifest)} fixtures: {n_dev} dev, {n_held} held-out)")


if __name__ == "__main__":
    main()
