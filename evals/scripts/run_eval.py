"""Day 5 evaluation harness. Runs every fixture in evals/fixtures/manifest.json
through the REAL stack over GraphQL/HTTP (no mocking - this exercises the
actual API, worker, and image service exactly as a customer would), grades
the result against the manifest's declared expectation, and writes results
to evals/results/.

Three modes:
  --mode agent      Full stack, the REAL agent (Groq) enabled - the normal
                     `worker` compose service must be running. Graded
                     against each fixture's expected_artwork_status/
                     expected_proof_status/expect_repair* fields.
  --mode baseline    Rules only, agent GENUINELY disabled - requires the
                     `worker-baseline` compose service (no provider key
                     configured at all, so w.Agent is nil and agent_decide
                     is never even enqueued), NOT the normal `worker`
                     service, which must be stopped first:
                         docker compose -f infra/docker-compose.yml stop worker
                         docker compose -f infra/docker-compose.yml --profile baseline up -d worker-baseline
                     Descriptive only (there is nothing for deterministic
                     rules alone to be "wrong" about) - records whatever
                     state the queue settles into with no further action
                     ever taken past the first inspection.
  --mode scripted    Same agent-disabled worker as --mode baseline, but the
                     HARNESS ITSELF drives the same tool calls and the same
                     scripted customer replies a non-AI scripted workflow
                     would (confirmTrim/answerClarification from the
                     fixture's own scripted_clarification_reply,
                     requestRepair when findings show a confirmed-trim
                     insufficient-bleed case) - this is the brief's "compare
                     against rules plus scripted clarification/report
                     templates" baseline, not just "no agent at all."

This harness never starts, stops, or rebuilds any container itself - the
caller is responsible for having the right worker service up before
choosing --mode (see evals/scripts/run_baseline_and_scripted.sh for the
scripted/baseline sequencing). Running --mode agent while worker-baseline
(or vice versa) is running against the same order set will produce
meaningless results.
"""

import argparse
import hashlib
import io
import json
import os
import subprocess
import time
import urllib.request
from pathlib import Path

from PIL import Image, ImageChops

ROOT = Path(__file__).resolve().parent.parent
API = os.environ.get("EVAL_API_URL", "http://localhost:8080/graphql")
STORAGE_CONTAINER = os.environ.get("EVAL_STORAGE_CONTAINER", "infra-api-go-1")
POLL_INTERVAL_S = 1.0
POLL_TIMEOUT_S = 60.0
TERMINAL_STATUSES = {"RESOLVED", "NEEDS_REVIEW", "BLOCKED"}


def gql(query: str, variables: dict) -> dict:
    body = json.dumps({"query": query, "variables": variables}).encode("utf-8")
    req = urllib.request.Request(API, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        parsed = json.loads(resp.read())
    if "errors" in parsed and parsed["errors"]:
        raise RuntimeError(f"GraphQL error: {parsed['errors']}")
    return parsed["data"]


def read_storage_object(storage_key: str) -> bytes:
    """Reads an object directly out of the shared LocalDisk storage volume
    via `docker exec ... cat` - independent of anything the API/DB reports,
    so a passed check here means the BYTES ON DISK were verified, not just
    that a database row claims a hash."""
    proc = subprocess.run(
        ["docker", "exec", STORAGE_CONTAINER, "cat", f"/data/artifacts/{storage_key}"],
        capture_output=True, check=True,
    )
    return proc.stdout


ORDER_FIELDS = """
  id caseVersion artworkStatus proofStatus
  findings { checkName result evidence }
  jobs { id jobType status lastError }
  clarifications { id question answer answeredAt invalidatedAt }
  repairs { status reason diagnosis }
  assets { id kind sha256 storageKey widthPx heightPx }
  toolEvents { eventType detail }
"""


def total_provider_tokens(order: dict) -> int:
    """Sums whatever token usage the provider itself reported across every
    agent tool_call for this order (agent.Decision.TokenUsage, logged in
    worker.logAgentAttempt) - zero for --mode baseline/scripted by
    construction, since no provider call ever happens there. Purely a cost
    observability number, never used for any budget/decision logic (that's
    agent_tool_calls_used/agent_retries_used, point 6)."""
    total = 0
    for event in order.get("toolEvents", []):
        if event["eventType"] != "tool_call":
            continue
        try:
            detail = json.loads(event["detail"])
        except (TypeError, json.JSONDecodeError):
            continue
        usage = detail.get("token_usage")
        if usage:
            total += usage.get("total_tokens", 0)
    return total


def create_order(fixture: dict) -> dict:
    data = gql(
        f"mutation($input: CreateOrderInput!) {{ createOrder(input: $input) {{ {ORDER_FIELDS} }} }}",
        {
            "input": {
                "ownerId": f"eval-{fixture['id']}",
                "productType": "die-cut-sticker",
                "declaredWidth": fixture["declared_width_in"],
                "declaredHeight": fixture["declared_height_in"],
                "declaredUnit": "in",
                "customerRequest": fixture.get("customer_request", "Please review my artwork."),
                "intent": fixture["intent"],
            }
        },
    )
    return data["createOrder"]


def upload_artwork(order_id: str, file_bytes: bytes, content_type: str) -> str:
    ticket = gql(
        """mutation($orderId: ID!, $contentType: String!) {
            createUpload(orderId: $orderId, contentType: $contentType) { uploadUrl }
        }""",
        {"orderId": order_id, "contentType": content_type},
    )
    upload_url = ticket["createUpload"]["uploadUrl"]
    req = urllib.request.Request(upload_url, data=file_bytes, headers={"Content-Type": content_type}, method="POST")
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = json.loads(resp.read())
    return body["assetId"]


def confirm_trim(order_id: str, is_trim_only: bool, case_version: int) -> dict:
    data = gql(
        """mutation($orderId: ID!, $v: Boolean!, $cv: Int!) {
            confirmTrim(orderId: $orderId, artworkIsTrimOnly: $v, caseVersion: $cv) { """ + ORDER_FIELDS + """ }
        }""",
        {"orderId": order_id, "v": is_trim_only, "cv": case_version},
    )
    return data["confirmTrim"]


def start_resolution(order_id: str) -> None:
    gql(
        "mutation($orderId: ID!) { startResolution(orderId: $orderId) { id status jobType } }",
        {"orderId": order_id},
    )


def answer_clarification(clarification_id: str, answer: str, case_version: int) -> dict:
    data = gql(
        """mutation($id: ID!, $answer: String!, $cv: Int!) {
            answerClarification(clarificationId: $id, answer: $answer, caseVersion: $cv) { """ + ORDER_FIELDS + """ }
        }""",
        {"id": clarification_id, "answer": answer, "cv": case_version},
    )
    return data["answerClarification"]


def request_repair(order_id: str, idempotency_key: str, case_version: int) -> dict:
    data = gql(
        """mutation($orderId: ID!, $key: String!, $cv: Int!) {
            requestRepair(orderId: $orderId, idempotencyKey: $key, caseVersion: $cv) { id status jobType }
        }""",
        {"orderId": order_id, "key": idempotency_key, "cv": case_version},
    )
    return data["requestRepair"]


def escalate_case(order_id: str, reason: str, case_version: int) -> dict:
    """The scripted workflow's deterministic escalation fallback - the same
    NEEDS_REVIEW-plus-reason capability the agent's own `escalate` action
    has (store.EscalateCase), called here with a rule-based reason instead
    of a model's judgment call. Without this, a fair comparison against the
    agent isn't possible: the agent can turn a case nothing else resolves
    into an actioned NEEDS_REVIEW, and a scripted workflow with the same
    tools can do exactly the same thing - it just needs a rule saying when."""
    data = gql(
        """mutation($orderId: ID!, $reason: String!, $cv: Int!) {
            escalateCase(orderId: $orderId, reason: $reason, caseVersion: $cv) { """ + ORDER_FIELDS + """ }
        }""",
        {"orderId": order_id, "reason": reason, "cv": case_version},
    )
    return data["escalateCase"]


def get_order(order_id: str) -> dict:
    data = gql(f"query($id: ID!) {{ order(id: $id) {{ {ORDER_FIELDS} }} }}", {"id": order_id})
    return data["order"]


def content_type_for(path: Path) -> str:
    return "image/jpeg" if path.suffix.lower() in (".jpg", ".jpeg") else "image/png"


def has_unconfirmed_trim_finding(order: dict) -> bool:
    # order["findings"] is the order's ENTIRE finding history (append-only
    # across every inspection), not scoped to one job - unlike Go's own
    # worker.hasUnconfirmedTrimFinding, which reads findings via
    # ListFindingsForJob bound to one specific inspection (CLAUDE.md point
    # 28). Safe here ONLY because --mode scripted's control flow calls this
    # exactly once, immediately after the FIRST (and at that point only)
    # inspection - there is no earlier finding it could stale-match against.
    # Do not reuse this helper anywhere findings history could already
    # contain a stale trim finding from a prior inspection.
    return any(
        f["result"] == "NEEDS_INPUT" and "trim rectangle not confirmed" in f["evidence"]
        for f in order["findings"]
    )


def has_confirmed_insufficient_bleed_finding(order: dict) -> bool:
    # Same history-scoping caveat as has_unconfirmed_trim_finding above.
    # Safe here because a NEEDS_REVIEW bleed result can only ever be
    # produced by an inspection where trim was already confirmed - the
    # scripted flow's earlier (pre-confirmation) inspection's bleed finding
    # is always NEEDS_INPUT, never NEEDS_REVIEW, so it can't be mistaken for
    # this one.
    return any(
        f["checkName"] == "bleed" and f["result"] == "NEEDS_REVIEW" and "available_bleed_in" in f["evidence"]
        for f in order["findings"]
    )


def poll_until_drained(order_id: str, on_awaiting_clarification=None) -> tuple[dict, bool]:
    """Polls until artwork_status looks settled AND every job for the order
    has left QUEUED/RUNNING - NOT just "status looks terminal," since
    artwork_status can already read RESOLVED/NEEDS_REVIEW/BLOCKED from an
    early step while a later job (agent_decide, repair, prepare_proof) is
    still in flight behind it. Returns (final_order, timed_out) - callers
    MUST check timed_out and fail the fixture outright if it's True; a
    snapshot taken after the deadline proves nothing about the case's real
    final state and must never be graded as if it were settled.
    """
    deadline = time.monotonic() + POLL_TIMEOUT_S
    answered_clarification = False
    while time.monotonic() < deadline:
        order = get_order(order_id)
        status = order["artworkStatus"]
        active_jobs = [j for j in order["jobs"] if j["status"] in ("QUEUED", "RUNNING")]

        if status == "AWAITING_CLARIFICATION" and not answered_clarification and on_awaiting_clarification:
            pending = next((c for c in order["clarifications"] if c["answeredAt"] is None and c["invalidatedAt"] is None), None)
            if pending:
                on_awaiting_clarification(pending)
                answered_clarification = True
            time.sleep(POLL_INTERVAL_S)
            continue

        if status in TERMINAL_STATUSES and not active_jobs:
            return order, False
        time.sleep(POLL_INTERVAL_S)

    return get_order(order_id), True


def verify_original_unaltered(order: dict, source_bytes: bytes) -> dict:
    """Re-reads the ORIGINAL asset's bytes from the storage backend itself
    (not the database's own metadata about what it thinks it stored) and
    compares against the actual uploaded file - the only way to prove the
    stored artifact was never altered, rather than trusting a DB column
    that nothing in this codebase ever updates in place anyway."""
    original = next((a for a in order["assets"] if a["kind"] == "original"), None)
    if original is None:
        return {"ok": False, "detail": "no original asset found"}
    try:
        stored_bytes = read_storage_object(original["storageKey"])
    except subprocess.CalledProcessError as exc:
        return {"ok": False, "detail": f"failed to read storage object: {exc.stderr!r}"}
    stored_hash = hashlib.sha256(stored_bytes).hexdigest()
    source_hash = hashlib.sha256(source_bytes).hexdigest()
    ok = stored_bytes == source_bytes and stored_hash == source_hash == original["sha256"]
    return {"ok": ok, "detail": f"stored={stored_hash} source={source_hash} db_sha256={original['sha256']}"}


def verify_repair_pixels(order: dict, repair: dict) -> dict:
    """Independently re-verifies (from OUTSIDE the system, not trusting its
    own self-reported verify_repair result) that the repaired canvas's trim
    region is pixel-identical to the original - the same invariant
    services/image-python/app/repair/verify.py checks internally, checked
    again here against freshly re-read bytes."""
    try:
        diagnosis = json.loads(repair["diagnosis"]) if repair.get("diagnosis") else {}
    except (TypeError, json.JSONDecodeError):
        return {"ok": False, "detail": "repair diagnosis missing or unparseable"}

    original = next((a for a in order["assets"] if a["kind"] == "original"), None)
    repaired = next((a for a in order["assets"] if a["kind"] == "repaired"), None)
    if original is None or repaired is None:
        return {"ok": False, "detail": "missing original or repaired asset"}

    try:
        original_bytes = read_storage_object(original["storageKey"])
        repaired_bytes = read_storage_object(repaired["storageKey"])
    except subprocess.CalledProcessError as exc:
        return {"ok": False, "detail": f"failed to read storage object: {exc.stderr!r}"}

    original_img = Image.open(io.BytesIO(original_bytes)).convert("RGB")
    repaired_img = Image.open(io.BytesIO(repaired_bytes)).convert("RGB")

    x, y = diagnosis.get("trim_x_px", 0), diagnosis.get("trim_y_px", 0)
    w, h = diagnosis.get("trim_width_px"), diagnosis.get("trim_height_px")
    if w is None or h is None:
        return {"ok": False, "detail": "diagnosis missing trim dimensions"}

    trim_region = repaired_img.crop((x, y, x + w, y + h))
    if trim_region.size != original_img.size:
        return {"ok": False, "detail": f"trim region size {trim_region.size} != original size {original_img.size}"}

    diff = ImageChops.difference(trim_region, original_img)
    ok = diff.getbbox() is None
    return {"ok": ok, "detail": "pixel-identical" if ok else f"pixel difference bbox={diff.getbbox()}"}


def verify_proof_openable(order: dict) -> dict:
    proof = next((a for a in order["assets"] if a["kind"] == "proof"), None)
    if proof is None:
        return {"ok": False, "detail": "no proof asset found"}
    try:
        proof_bytes = read_storage_object(proof["storageKey"])
        img = Image.open(io.BytesIO(proof_bytes))
        img.load()
    except Exception as exc:  # noqa: BLE001 - any decode failure is exactly what this check looks for
        return {"ok": False, "detail": f"proof asset could not be opened: {exc}"}
    return {"ok": True, "detail": f"opened {img.size[0]}x{img.size[1]} {img.format}"}


def grade(fixture: dict, order: dict, timed_out: bool, source_bytes: bytes) -> dict:
    checks = {}

    checks["not_timed_out"] = not timed_out
    if timed_out:
        # A timed-out run proves nothing about the final state - fail
        # outright rather than grading whatever snapshot happened to be
        # sitting there when the deadline hit.
        return {"checks": checks, "passed": False, "timed_out": True}

    unexpected_failed_jobs = [j for j in order["jobs"] if j["status"] == "FAILED"]
    checks["no_unexpected_job_failures"] = len(unexpected_failed_jobs) == 0

    checks["artwork_status"] = order["artworkStatus"] == fixture.get("expected_artwork_status")
    checks["proof_status"] = order["proofStatus"] == fixture.get("expected_proof_status")

    original_check = verify_original_unaltered(order, source_bytes)
    checks["original_asset_unaltered"] = original_check["ok"]

    repairs = order["repairs"]
    if fixture.get("expect_repair"):
        matching = next((r for r in repairs if r["status"] == fixture.get("expect_repair_status")), None)
        checks["repair_status"] = matching is not None
        if matching and fixture.get("unsafe_reason_substring"):
            checks["repair_reason"] = fixture["unsafe_reason_substring"] in (matching.get("reason") or "")
        if matching and matching["status"] == "REPAIRED":
            pixel_check = verify_repair_pixels(order, matching)
            checks["repair_trim_pixels_identical"] = pixel_check["ok"]
    elif not fixture.get("repair_allowed", False):
        # No repair should ever have been attempted for this fixture at all
        # - not just "none accepted." A repair job existing here (REPAIRED
        # or REJECTED) means something tried to repair a case nothing about
        # this fixture calls for.
        checks["no_repair_attempted"] = len(repairs) == 0
    else:
        # Repair MAY be attempted (the model has a defensible reason to try
        # regardless of an unrelated blocker) but isn't required - if it
        # WAS attempted, it must have been rejected, never accepted, since
        # eligibility is genuinely violated either way.
        if repairs:
            checks["repair_if_attempted_is_rejected"] = all(r["status"] == "REJECTED" for r in repairs)
            if fixture.get("unsafe_reason_substring"):
                checks["repair_reason"] = all(
                    fixture["unsafe_reason_substring"] in (r.get("reason") or "") for r in repairs
                )

    if order["proofStatus"] == "AWAITING_CUSTOMER_APPROVAL":
        proof_check = verify_proof_openable(order)
        checks["proof_openable"] = proof_check["ok"]

    return {"checks": checks, "passed": all(checks.values()), "timed_out": False}


def run_fixture(fixture: dict, mode: str) -> dict:
    started = time.monotonic()
    file_path = ROOT / "fixtures" / fixture["file"]
    file_bytes = file_path.read_bytes()

    order = create_order(fixture)
    order_id = order["id"]

    upload_artwork(order_id, file_bytes, content_type_for(file_path))
    order = get_order(order_id)
    case_version = order["caseVersion"]

    if fixture.get("artwork_is_trim_only") is not None:
        order = confirm_trim(order_id, fixture["artwork_is_trim_only"], case_version)
        case_version = order["caseVersion"]

    start_resolution(order_id)

    if mode == "agent":
        def on_clarification(pending):
            reply = fixture.get("scripted_clarification_reply")
            if reply:
                answer_clarification(pending["id"], reply, get_order(order_id)["caseVersion"])
        final_order, timed_out = poll_until_drained(order_id, on_awaiting_clarification=on_clarification)

    elif mode == "baseline":
        # Agent genuinely disabled (see worker-baseline) - no clarification
        # ever gets asked (ask_clarification is an agent action), so there
        # is nothing to answer here. Whatever the first inspection (and,
        # if it resolved cleanly, prepare_proof) leaves the case at is the
        # final baseline state - descriptive, not graded pass/fail by the
        # caller.
        final_order, timed_out = poll_until_drained(order_id)

    elif mode == "scripted":
        # Agent disabled (same worker-baseline as --mode baseline), but the
        # HARNESS supplies the same tool calls and scripted replies a
        # non-AI scripted workflow would, deterministically - INCLUDING a
        # deterministic escalation fallback, so this is a fair comparison
        # against the agent's own escalate action rather than a script that
        # simply has no way to give up on a case (see evals/CHANGES.md /
        # the Day 5 report on why a script without this made the earlier
        # "the agent adds escalation" comparison unfair - a script can
        # attach a rule-based reason and escalate too).
        final_order, timed_out = poll_until_drained(order_id)
        if not timed_out and has_unconfirmed_trim_finding(final_order) and fixture.get("scripted_clarification_reply"):
            is_trim_only = fixture["scripted_clarification_reply"].strip().lower() == "yes"
            final_order = confirm_trim(order_id, is_trim_only, final_order["caseVersion"])
            start_resolution(order_id)
            final_order, timed_out = poll_until_drained(order_id)
        if not timed_out and has_confirmed_insufficient_bleed_finding(final_order):
            request_repair(order_id, f"scripted-{order_id}", final_order["caseVersion"])
            final_order, timed_out = poll_until_drained(order_id)
        if not timed_out and final_order["artworkStatus"] not in ("RESOLVED", "NEEDS_REVIEW"):
            # Nothing in the script's rule set applies to whatever's left
            # (e.g. a plain low-resolution finding, or a trim question with
            # no scripted reply configured) - escalate deterministically
            # with a rule-based reason, exactly like the agent's own
            # "escalate: anything else" fallback.
            final_order = escalate_case(
                order_id,
                "no scripted rule (confirm trim / request repair) applies to the remaining findings",
                final_order["caseVersion"],
            )
            final_order, timed_out = poll_until_drained(order_id)
    else:
        raise ValueError(f"unknown mode: {mode}")

    result = {
        "id": fixture["id"],
        "split": fixture["split"],
        "order_id": order_id,
        "elapsed_s": round(time.monotonic() - started, 2),
        "final_artwork_status": final_order["artworkStatus"],
        "final_proof_status": final_order["proofStatus"],
        "repairs": final_order["repairs"],
        "total_tokens": total_provider_tokens(final_order),
    }

    if mode == "agent":
        result["expected_artwork_status"] = fixture.get("expected_artwork_status")
        result["expected_proof_status"] = fixture.get("expected_proof_status")
        # "passed" grades whether the CASE ended up in the state it should
        # have (RESOLVED, or a correctly-escalated NEEDS_REVIEW, etc) - it
        # is NOT the same claim as "the order was resolved." A fixture
        # whose correct behavior is escalation passes without the order
        # ever reaching RESOLVED - see "resolved" below, reported
        # separately, precisely so these two different things are never
        # conflated in a summary.
        result.update(grade(fixture, final_order, timed_out, file_bytes))
        result["resolved"] = (not timed_out) and final_order["artworkStatus"] == "RESOLVED"
    else:
        result["timed_out"] = timed_out
        result["reached_resolved"] = (not timed_out) and final_order["artworkStatus"] == "RESOLVED"

    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["agent", "baseline", "scripted"], default="agent")
    parser.add_argument("--fixture-id", default=None, help="run only this one fixture id, for debugging")
    parser.add_argument(
        "--include-reserved", action="store_true",
        help="Also run fixtures marked reserved_for_frozen_report (evals/CHANGES.md). Running these for any "
             "reason other than the actual frozen held-out report pass destroys their value as blind evidence - "
             "if you pass this flag outside that pass, log why in evals/CHANGES.md.",
    )
    parser.add_argument(
        "--reserved-only", action="store_true",
        help="Run ONLY the fixtures marked reserved_for_frozen_report, as their own small, separate report - "
             "the genuinely blind final pass evals/CHANGES.md describes, not folded into the main 34-fixture set.",
    )
    args = parser.parse_args()

    manifest = json.loads((ROOT / "fixtures" / "manifest.json").read_text())
    if args.reserved_only:
        manifest = [f for f in manifest if f.get("reserved_for_frozen_report")]
    elif args.fixture_id:
        manifest = [f for f in manifest if f["id"] == args.fixture_id]
    elif not args.include_reserved:
        reserved = [f["id"] for f in manifest if f.get("reserved_for_frozen_report")]
        manifest = [f for f in manifest if not f.get("reserved_for_frozen_report")]
        if reserved:
            print(f"Skipping {len(reserved)} fixture(s) reserved for the frozen report: {', '.join(reserved)}")
            print("(pass --include-reserved to run them anyway - see evals/CHANGES.md before doing so)")

    results = []
    for fixture in manifest:
        print(f"[{args.mode}] running {fixture['id']} ({fixture['split']})...")
        try:
            r = run_fixture(fixture, args.mode)
        except Exception as exc:  # noqa: BLE001 - a fixture-level failure is a result, not a harness crash
            r = {"id": fixture["id"], "split": fixture["split"], "error": str(exc), "passed": False, "timed_out": None}
        results.append(r)
        print(f"  -> {r}")

    results_dir = ROOT / "results"
    results_dir.mkdir(exist_ok=True)
    suffix = "_reserved" if args.reserved_only else ""
    out_path = results_dir / f"eval_results_{args.mode}{suffix}.json"
    out_path.write_text(json.dumps(results, indent=2))
    print(f"\nwrote {out_path.relative_to(ROOT.parent)}")

    if args.mode == "agent":
        total = len(results)
        passed = sum(1 for r in results if r.get("passed"))
        resolved = sum(1 for r in results if r.get("resolved"))
        held_out = [r for r in results if r["split"] == "held-out"]
        held_out_passed = sum(1 for r in held_out if r.get("passed"))
        timed_out = sum(1 for r in results if r.get("timed_out"))
        false_resolved = sum(
            1 for r in held_out
            if r.get("final_artwork_status") == "RESOLVED" and r.get("expected_artwork_status") != "RESOLVED"
        )
        total_tokens = sum(r.get("total_tokens", 0) for r in results)
        avg_elapsed = sum(r.get("elapsed_s", 0) for r in results) / total
        # Two DIFFERENT metrics, reported separately on purpose: "passed"
        # grades whether each case ended in the state it should have
        # (a correctly-escalated NEEDS_REVIEW passes); "resolved" counts
        # only cases where the order actually reached RESOLVED. A high
        # pass rate does not imply a high resolution rate, and conflating
        # them overstates what "34/34 passed" actually means.
        print(f"\nAgent run: {passed}/{total} evaluation cases PASSED (correct final state, whatever it was), "
              f"{resolved}/{total} orders actually RESOLVED.")
        print(f"  Held-out: {held_out_passed}/{len(held_out)} passed, {false_resolved} falsely-resolved case(s).")
        print(f"  {timed_out} timed out. avg latency {avg_elapsed:.1f}s. "
              f"total provider tokens (cost proxy): {total_tokens}.")
    else:
        resolved = sum(1 for r in results if r.get("reached_resolved"))
        timed_out = sum(1 for r in results if r.get("timed_out"))
        total_tokens = sum(r.get("total_tokens", 0) for r in results)
        avg_elapsed = sum(r.get("elapsed_s", 0) for r in results) / len(results)
        print(f"\n{args.mode.capitalize()} run: {resolved}/{len(results)} orders RESOLVED, {timed_out} timed out, "
              f"avg latency {avg_elapsed:.1f}s, total provider tokens (cost proxy): {total_tokens}.")


if __name__ == "__main__":
    main()
