"""The /inspect endpoint: decodes the uploaded image, resolves the trim
rectangle (or confirms it can't be resolved without customer input), then
runs the three deterministic checks against it. The checks themselves live
in app/checks/ as pure functions with no I/O - this file's job is decoding,
validation, and orchestration, not check logic.

This service owns ONLY deterministic image inspection/repair - no workflow
or business-state logic belongs here (see CLAUDE.md point 4). It reports
what each check found; api-go decides what that means for artwork_status.
"""

import io
from typing import Optional

from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from PIL import Image

from app.checks import resolution as resolution_check
from app.checks.bleed import check_bleed
from app.checks.color import check_color
from app.checks.trim import resolve_trim
from app.checks.units import to_inches

app = FastAPI(title="artwork-agent image service")

MAX_UPLOAD_BYTES = 10 * 1024 * 1024  # mirrors the Go upload cap
MAX_DECODED_PIXELS = 25_000_000  # 25-megapixel decoded limit per the brief
ALLOWED_FORMATS = {"PNG", "JPEG"}  # matches the brief's supported artwork formats


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.post("/inspect")
async def inspect(
    file: UploadFile = File(...),
    declared_width: float = Form(...),
    declared_height: float = Form(...),
    declared_unit: str = Form(...),
    intent: str = Form(...),
    artwork_is_trim_only: Optional[bool] = Form(None),
    # Set by api-go on a post-repair recheck, where it already knows exactly
    # where it placed the original content - never guessed from this file.
    trim_width_px: Optional[int] = Form(None),
    trim_height_px: Optional[int] = Form(None),
):
    data = await file.read()
    if len(data) > MAX_UPLOAD_BYTES:
        raise HTTPException(status_code=400, detail="file exceeds 10MB upload limit")

    # Image.open() only reads the header - it does not decode pixel data, so
    # format and declared dimensions can be checked before the expensive (and
    # decompression-bomb-exploitable) full decode in image.load() below.
    try:
        image = Image.open(io.BytesIO(data))
    except Exception:
        raise HTTPException(status_code=400, detail="could not decode image - unsupported or corrupt file")

    if image.format not in ALLOWED_FORMATS:
        raise HTTPException(
            status_code=400,
            detail=f"unsupported image format: {image.format} (allowed: {', '.join(sorted(ALLOWED_FORMATS))})",
        )

    width, height = image.size
    if width * height > MAX_DECODED_PIXELS:
        raise HTTPException(status_code=400, detail="decoded image exceeds 25-megapixel limit")

    has_icc_profile = image.info.get("icc_profile") is not None

    try:
        image.load()  # now safe to fully decode
    except Exception:
        raise HTTPException(status_code=400, detail="could not decode image - unsupported or corrupt file")

    declared_width_in = to_inches(declared_width, declared_unit)
    declared_height_in = to_inches(declared_height, declared_unit)

    trim = resolve_trim(intent, artwork_is_trim_only, width, height, trim_width_px, trim_height_px)

    checks = []
    effective_ppi = None
    if trim is None:
        checks.append({
            "check_name": "resolution",
            "result": "NEEDS_INPUT",
            "evidence": {"reason": "trim rectangle not confirmed - confirm whether the upload already includes bleed"},
            "rule_version": resolution_check.RULE_VERSION,
        })
    else:
        resolution_result = resolution_check.check_resolution(trim.width_px, trim.height_px, declared_width_in, declared_height_in)
        checks.append(resolution_result)
        effective_ppi = resolution_result["evidence"]["effective_ppi"]

    checks.append(check_color(image.mode, has_icc_profile))

    bleed_result = check_bleed(
        intent,
        trim_confirmed=trim is not None,
        image_width_px=width,
        image_height_px=height,
        trim_width_px=trim.width_px if trim else None,
        trim_height_px=trim.height_px if trim else None,
        effective_ppi=effective_ppi,
    )
    if bleed_result is not None:
        checks.append(bleed_result)

    return {
        "width_px": width,
        "height_px": height,
        "mode": image.mode,
        "format": image.format,
        "trim_width_px": trim.width_px if trim else None,
        "trim_height_px": trim.height_px if trim else None,
        "checks": checks,
    }
