"""Generates the seed fixture set for Day 1-2 and (later) the full 60-image
dev/held-out set for Day 5. Fixtures are split by original *design* before any
variant is added, so a design's clean/low-PPI/no-bleed variants never end up
split across dev and held-out.

Day 1 usage: `python evals/scripts/generate_fixtures.py` writes a small seed
batch (~12 images) so the real execution path has something to exercise
immediately. Day 2 re-runs it with more variants per design; Day 5 expands to
the full 60.
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


def save(img: Image.Image, path: Path, icc_profile: bytes | None = None) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if icc_profile is not None:
        img.save(path, icc_profile=icc_profile)
    else:
        img.save(path)


# Each entry is one *design*; "split" decides dev vs held-out for every
# variant generated from it.
DESIGNS = [
    {
        "id": "clean-a",
        "split": "dev",
        "notes": "Clean 3x3in artwork at >=300 PPI, RGB, full bleed already present.",
        "declared_width_in": 3.0,
        "declared_height_in": 3.0,
        "intent": "full_bleed",
        "image_width_px": 1000,  # 1000/3.125 ~ 320 PPI including bleed margin
        "image_height_px": 1000,
        "color": (30, 120, 200),
    },
    {
        "id": "clean-b",
        "split": "held-out",
        "notes": "Clean 2x2in artwork at >=300 PPI, RGB, border intent (no bleed required).",
        "declared_width_in": 2.0,
        "declared_height_in": 2.0,
        "intent": "border",
        "image_width_px": 700,
        "image_height_px": 700,
        "color": (200, 60, 60),
    },
    {
        "id": "lowres-a",
        "split": "dev",
        "notes": "3x3in declared size but only ~200 effective PPI - should flag resolution.",
        "declared_width_in": 3.0,
        "declared_height_in": 3.0,
        "intent": "border",
        "image_width_px": 600,
        "image_height_px": 600,
        "color": (80, 180, 90),
    },
    {
        "id": "rgb-noprofile-a",
        "split": "held-out",
        "notes": "RGB, no embedded ICC profile - color check should return an advisory, not a blocker.",
        "declared_width_in": 2.5,
        "declared_height_in": 2.5,
        "intent": "border",
        "image_width_px": 900,
        "image_height_px": 900,
        "color": (240, 200, 40),
    },
    {
        "id": "missing-bleed-a",
        "split": "dev",
        "notes": "Full-bleed intent but image sized exactly at trim - bleed check should block.",
        "declared_width_in": 3.0,
        "declared_height_in": 3.0,
        "intent": "full_bleed",
        "image_width_px": 900,  # exactly 300 PPI at the trim size, no extra bleed margin
        "image_height_px": 900,
        "color": (150, 90, 200),
    },
    {
        "id": "border-a",
        "split": "held-out",
        "notes": "Border intent - missing bleed must not auto-fail since bleed isn't required.",
        "declared_width_in": 4.0,
        "declared_height_in": 2.0,
        "intent": "border",
        "image_width_px": 1200,
        "image_height_px": 600,
        "color": (60, 60, 60),
    },
]


def main():
    manifest = []
    for design in DESIGNS:
        img = make_solid_image(design["image_width_px"], design["image_height_px"], design["color"])
        filename = f"{design['id']}_base.png"
        out_path = FIXTURES_DIR / design["split"] / filename
        save(img, out_path)

        manifest.append(
            {
                "id": design["id"],
                "split": design["split"],
                "file": f"{design['split']}/{filename}",
                "declared_width_in": design["declared_width_in"],
                "declared_height_in": design["declared_height_in"],
                "intent": design["intent"],
                "image_width_px": design["image_width_px"],
                "image_height_px": design["image_height_px"],
                "notes": design["notes"],
            }
        )
        print(f"wrote {out_path.relative_to(ROOT)}")

    manifest_path = FIXTURES_DIR / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2))
    print(f"wrote {manifest_path.relative_to(ROOT)} ({len(manifest)} fixtures)")


if __name__ == "__main__":
    main()
