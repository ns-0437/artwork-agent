"""The /inspect endpoint: decodes the uploaded image, resolves the trim
rectangle (or confirms it can't be resolved without customer input), then
runs the three deterministic checks against it. The checks themselves live
in app/checks/ as pure functions with no I/O - this file's job is decoding,
validation, and orchestration, not check logic.

This service owns ONLY deterministic image inspection/repair - no workflow
or business-state logic belongs here (see CLAUDE.md point 4). It reports
what each check found; api-go decides what that means for artwork_status.
"""

import base64
import io
from typing import Optional

from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from PIL import Image

from app.checks import resolution as resolution_check
from app.checks.bleed import check_bleed
from app.checks.color import check_color
from app.checks.trim import resolve_trim
from app.checks.units import to_inches
from app.repair.eligibility import check_eligibility
from app.repair.extend_background import CanvasTooLargeError, extend_background
from app.repair.geometry import validate_aspect_ratio, validate_declared_size
from app.repair.preview import render_preview
from app.repair.verify import verify_repair
from app.proof.render import render_proof

app = FastAPI(title="artwork-agent image service")

MAX_UPLOAD_BYTES = 10 * 1024 * 1024  # mirrors the Go upload cap
MAX_DECODED_PIXELS = 25_000_000  # 25-megapixel decoded limit per the brief
ALLOWED_FORMATS = {"PNG", "JPEG"}  # matches the brief's supported artwork formats


def _decode_upload(data: bytes) -> Image.Image:
    """Shared by /inspect and /repair: validate format/size before the
    expensive full decode, exactly as CLAUDE.md point 27 requires."""
    if len(data) > MAX_UPLOAD_BYTES:
        raise HTTPException(status_code=400, detail="file exceeds 10MB upload limit")

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
        image.load()
    except Exception:
        raise HTTPException(status_code=400, detail="could not decode image - unsupported or corrupt file")

    return image


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
    image = _decode_upload(data)
    width, height = image.size
    has_icc_profile = image.info.get("icc_profile") is not None

    declared_width_in = to_inches(declared_width, declared_unit)
    declared_height_in = to_inches(declared_height, declared_unit)
    size_error = validate_declared_size(declared_width_in, declared_height_in)
    if size_error:
        raise HTTPException(status_code=400, detail=size_error)

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


@app.post("/repair")
async def repair(
    file: UploadFile = File(...),
    declared_width: float = Form(...),
    declared_height: float = Form(...),
    declared_unit: str = Form(...),
):
    """Repair only ever applies to the trim-only case: the whole uploaded
    image IS the trim (that's the precondition api-go checks - see
    CLAUDE.md point 12 - before even calling this), so PPI here is computed
    directly from the image, the same formula resolution.py uses. This
    endpoint doesn't call resolve_trim; there is no ambiguity to resolve.

    On success, the repaired canvas and preview are returned as base64 PNG
    - never written by this service, so it never needs to know about
    storage. On failure (ineligible, mismatched geometry, or verification
    somehow fails), no image data is returned - the original is left
    untouched by construction since this endpoint never mutates its input.
    """
    data = await file.read()
    image = _decode_upload(data)
    width, height = image.size

    eligibility = check_eligibility(image)
    if not eligibility["eligible"]:
        return {"repaired": False, "reason": eligibility["reason"]}

    declared_width_in = to_inches(declared_width, declared_unit)
    declared_height_in = to_inches(declared_height, declared_unit)

    size_error = validate_declared_size(declared_width_in, declared_height_in)
    if size_error:
        return {"repaired": False, "reason": size_error}

    aspect_error = validate_aspect_ratio(width, height, declared_width_in, declared_height_in)
    if aspect_error:
        return {"repaired": False, "reason": aspect_error}

    effective_ppi = min(width / declared_width_in, height / declared_height_in)

    try:
        extended = extend_background(image, eligibility["mode"], eligibility["edge_color"], effective_ppi)
    except CanvasTooLargeError as exc:
        return {"repaired": False, "reason": str(exc)}
    canvas = extended["image"]

    # Encode to PNG - preserving the source's ICC profile when it has one,
    # so a repaired RGB image keeps its color profile rather than silently
    # losing it on save - THEN decode that actual output back and verify
    # against it, not the in-memory canvas. This is what makes verification
    # trustworthy: it catches anything the save/reload round trip itself
    # could have changed, not just what extend_background did in memory.
    icc_profile = image.info.get("icc_profile")
    canvas_buf = io.BytesIO()
    if icc_profile:
        canvas.save(canvas_buf, format="PNG", icc_profile=icc_profile)
    else:
        canvas.save(canvas_buf, format="PNG")
    canvas_buf.seek(0)
    saved_canvas = Image.open(canvas_buf)
    saved_canvas.load()

    verified = verify_repair(
        image, saved_canvas, extended["trim_x_px"], extended["trim_y_px"], extended["trim_width_px"], extended["trim_height_px"]
    )
    if not verified:
        # Never trust an unverified repair - discard the candidate rather
        # than return something that might not actually preserve the
        # original content exactly. Checking the SAVED file (not just the
        # in-memory canvas) is the point: an encode/decode change must not
        # hide under a check that only ever looked at memory.
        return {"repaired": False, "reason": "pixel-equality verification failed against the saved output - repair discarded"}

    preview = render_preview(
        saved_canvas, extended["trim_x_px"], extended["trim_y_px"], extended["trim_width_px"], extended["trim_height_px"]
    )
    preview_buf = io.BytesIO()
    preview.save(preview_buf, format="PNG")

    edge_color = eligibility["edge_color"]
    edge_color_out = list(edge_color) if isinstance(edge_color, tuple) else edge_color

    return {
        "repaired": True,
        "image_base64": base64.b64encode(canvas_buf.getvalue()).decode("ascii"),
        "preview_base64": base64.b64encode(preview_buf.getvalue()).decode("ascii"),
        "width_px": saved_canvas.width,
        "height_px": saved_canvas.height,
        "trim_x_px": extended["trim_x_px"],
        "trim_y_px": extended["trim_y_px"],
        "trim_width_px": extended["trim_width_px"],
        "trim_height_px": extended["trim_height_px"],
        "margin_px": extended["margin_px"],
        "effective_ppi": round(effective_ppi, 2),
        "edge_color": edge_color_out,
        "mode": eligibility["mode"],
    }


@app.post("/proof")
async def proof(
    file: UploadFile = File(...),
    order_id: str = Form(...),
    artwork_version: int = Form(...),
    case_version: int = Form(...),
):
    """Renders a proof from whichever asset api-go says is current (the
    order's already-resolved artwork - original or repaired) - this endpoint
    makes no decision about WHEN a proof should be prepared or what state
    that implies; api-go decides that (only after artwork_status has already
    reached RESOLVED) and calls this purely to render the artifact."""
    data = await file.read()
    image = _decode_upload(data)

    caption = f"PROOF - order {order_id} - artwork v{artwork_version} - case v{case_version} - awaiting customer approval, not released to production"
    rendered = render_proof(image, caption)
    buf = io.BytesIO()
    rendered.save(buf, format="PNG")

    return {
        "image_base64": base64.b64encode(buf.getvalue()).decode("ascii"),
        "width_px": rendered.width,
        "height_px": rendered.height,
    }
