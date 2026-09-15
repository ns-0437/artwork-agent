"""The /inspect endpoint: decodes the uploaded image, then runs the three
deterministic checks (resolution, color, bleed) against it. The checks
themselves live in app/checks/ as pure functions with no I/O - this file's
job is decoding, validation, and orchestration, not check logic.

This service owns ONLY deterministic image inspection/repair - no workflow
or business-state logic belongs here (see CLAUDE.md point 4). It reports
what each check found; api-go decides what that means for artwork_status.
"""

import io

from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from PIL import Image

from app.checks.bleed import check_bleed
from app.checks.color import check_color
from app.checks.resolution import check_resolution
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

    checks = [
        check_resolution(width, height, declared_width_in, declared_height_in),
        check_color(image.mode, has_icc_profile),
    ]
    bleed_result = check_bleed(intent)
    if bleed_result is not None:
        checks.append(bleed_result)

    return {
        "width_px": width,
        "height_px": height,
        "mode": image.mode,
        "format": image.format,
        "checks": checks,
    }
