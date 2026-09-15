"""Day 1: a real (not stubbed) /inspect endpoint that decodes the uploaded
image and reports its actual dimensions/mode. Day 2 adds the three checks
(resolution, color, bleed) as their own modules under app/checks/ and calls
them from here - this endpoint's shape and the request/response contract
don't change.

This service owns ONLY deterministic image inspection/repair - no workflow
or business-state logic belongs here (see CLAUDE.md point 4).
"""

import io

from fastapi import FastAPI, File, HTTPException, UploadFile
from PIL import Image

app = FastAPI(title="artwork-agent image service")

MAX_UPLOAD_BYTES = 10 * 1024 * 1024  # mirrors the Go upload cap
MAX_DECODED_PIXELS = 25_000_000  # 25-megapixel decoded limit per the brief
ALLOWED_FORMATS = {"PNG", "JPEG"}  # matches the brief's supported artwork formats


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.post("/inspect")
async def inspect(file: UploadFile = File(...)):
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

    try:
        image.load()  # now safe to fully decode
    except Exception:
        raise HTTPException(status_code=400, detail="could not decode image - unsupported or corrupt file")

    return {
        "width_px": width,
        "height_px": height,
        "mode": image.mode,
        "format": image.format,
    }
