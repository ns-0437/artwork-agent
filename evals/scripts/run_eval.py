"""Day 5 evaluation harness. Runs every fixture in evals/fixtures/manifest.json
through the REAL stack over GraphQL/HTTP (no mocking - this exercises the
actual API, worker, and image service exactly as a customer would), grades
the result against the manifest's declared expectation, and writes frozen
results to evals/results/.

Two modes:
  --mode agent     (default) full stack, agent enabled - graded against each
                    fixture's expected_artwork_status/expected_proof_status/
                    expect_repair* fields.
  --mode baseline   rules-only comparison: skips confirmTrim/clarification
                    steps that only matter to the agent, does not wait for or
                    answer any clarification, and records whatever state the
                    single deterministic inspection left the case in - NOT
                    graded pass/fail (there is nothing for deterministic
                    rules alone to be "wrong" about; this is descriptive,
                    used only for the agent-vs-baseline comparison numbers).

Usage:
    python evals/scripts/run_eval.py --mode agent
    python evals/scripts/run_eval.py --mode baseline
(requires the stack running - docker compose -f infra/docker-compose.yml up)
"""

import argparse
import hashlib
import json
import os
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
API = os.environ.get("EVAL_API_URL", "http://localhost:8080/graphql")
POLL_INTERVAL_S = 1.0
POLL_TIMEOUT_S = 60.0
TERMINAL_STATUSES = {"RESOLVED", "NEEDS_REVIEW"}


def gql(query: str, variables: dict) -> dict:
    body = json.dumps({"query": query, "variables": variables}).encode("utf-8")
    req = urllib.request.Request(API, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        parsed = json.loads(resp.read())
    if "errors" in parsed and parsed["errors"]:
        raise RuntimeError(f"GraphQL error: {parsed['errors']}")
    return parsed["data"]


ORDER_FIELDS = """
  id caseVersion artworkStatus proofStatus
  findings { checkName result evidence }
  jobs { jobType status lastError }
  clarifications { id question answer answeredAt }
  repairs { status reason }
  assets { kind sha256 }
"""


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


def get_order(order_id: str) -> dict:
    data = gql(f"query($id: ID!) {{ order(id: $id) {{ {ORDER_FIELDS} }} }}", {"id": order_id})
    return data["order"]


def content_type_for(path: Path) -> str:
    return "image/jpeg" if path.suffix.lower() in (".jpg", ".jpeg") else "image/png"


def run_fixture_agent(fixture: dict) -> dict:
    """Full stack, agent enabled - drives through confirmTrim/clarification
    exactly as a real customer session would, polls to a terminal status,
    and grades against the manifest's declared expectation."""
    started = time.monotonic()
    file_path = ROOT / "fixtures" / fixture["file"]
    file_bytes = file_path.read_bytes()
    source_sha256 = hashlib.sha256(file_bytes).hexdigest()

    order = create_order(fixture)
    order_id = order["id"]
    case_version = order["caseVersion"]

    upload_artwork(order_id, file_bytes, content_type_for(file_path))
    order = get_order(order_id)
    case_version = order["caseVersion"]
    original_asset_sha256 = next((a["sha256"] for a in order["assets"] if a["kind"] == "original"), None)

    if fixture.get("artwork_is_trim_only") is not None:
        order = confirm_trim(order_id, fixture["artwork_is_trim_only"], case_version)
        case_version = order["caseVersion"]

    start_resolution(order_id)

    deadline = time.monotonic() + POLL_TIMEOUT_S
    answered_clarification = False
    final_order = None
    while time.monotonic() < deadline:
        order = get_order(order_id)
        status = order["artworkStatus"]
        active_jobs = [j for j in order["jobs"] if j["status"] in ("QUEUED", "RUNNING")]

        if status == "AWAITING_CLARIFICATION" and not answered_clarification:
            pending = next((c for c in order["clarifications"] if c["answeredAt"] is None), None)
            reply = fixture.get("scripted_clarification_reply")
            if pending and reply:
                order = answer_clarification(pending["id"], reply, order["caseVersion"])
                answered_clarification = True
            time.sleep(POLL_INTERVAL_S)
            continue

        # "Terminal" means BOTH the status looks final AND the job queue has
        # actually drained - artwork_status can already read RESOLVED or
        # NEEDS_REVIEW from the initial inspect while an agent_decide/repair/
        # prepare_proof job is still queued or running behind it (e.g. a
        # repair attempt that will get REJECTED, or a proof still being
        # rendered after RESOLVED). Stopping on status alone was a harness
        # bug that raced ahead of jobs still in flight and produced false
        # failures - not a bug in the system under test.
        if status in TERMINAL_STATUSES and not active_jobs:
            final_order = order
            break
        time.sleep(POLL_INTERVAL_S)

    if final_order is None:
        final_order = get_order(order_id)

    elapsed_s = time.monotonic() - started

    result = {
        "id": fixture["id"],
        "split": fixture["split"],
        "order_id": order_id,
        "elapsed_s": round(elapsed_s, 2),
        "final_artwork_status": final_order["artworkStatus"],
        "final_proof_status": final_order["proofStatus"],
        "expected_artwork_status": fixture.get("expected_artwork_status"),
        "expected_proof_status": fixture.get("expected_proof_status"),
        "repairs": final_order["repairs"],
        "original_asset_unaltered": original_asset_sha256 == source_sha256,
    }

    checks = []
    checks.append(("artwork_status", final_order["artworkStatus"] == fixture.get("expected_artwork_status")))
    checks.append(("proof_status", final_order["proofStatus"] == fixture.get("expected_proof_status")))
    checks.append(("original_asset_unaltered", result["original_asset_unaltered"]))

    if fixture.get("expect_repair"):
        matching_repair = next(
            (r for r in final_order["repairs"] if r["status"] == fixture.get("expect_repair_status")), None
        )
        checks.append(("repair_status", matching_repair is not None))
        if matching_repair and fixture.get("unsafe_reason_substring"):
            reason = (matching_repair.get("reason") or "")
            checks.append(("repair_reason", fixture["unsafe_reason_substring"] in reason))

    result["checks"] = {name: ok for name, ok in checks}
    result["passed"] = all(ok for _, ok in checks)
    return result


def run_fixture_baseline(fixture: dict) -> dict:
    """Rules-only: no clarification answering, no waiting past the first
    inspect job. Descriptive only - see module docstring."""
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

    start_resolution(order_id)

    deadline = time.monotonic() + POLL_TIMEOUT_S
    final_order = None
    while time.monotonic() < deadline:
        order = get_order(order_id)
        inspect_jobs = [j for j in order["jobs"] if j["jobType"] == "inspect"]
        if inspect_jobs and all(j["status"] in ("SUCCEEDED", "FAILED") for j in inspect_jobs):
            final_order = order
            break
        time.sleep(POLL_INTERVAL_S)

    if final_order is None:
        final_order = get_order(order_id)

    return {
        "id": fixture["id"],
        "split": fixture["split"],
        "order_id": order_id,
        "elapsed_s": round(time.monotonic() - started, 2),
        "final_artwork_status": final_order["artworkStatus"],
        "final_proof_status": final_order["proofStatus"],
        "reached_resolved": final_order["artworkStatus"] == "RESOLVED",
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["agent", "baseline"], default="agent")
    args = parser.parse_args()

    manifest = json.loads((ROOT / "fixtures" / "manifest.json").read_text())

    results = []
    for fixture in manifest:
        print(f"[{args.mode}] running {fixture['id']} ({fixture['split']})...")
        try:
            if args.mode == "agent":
                r = run_fixture_agent(fixture)
            else:
                r = run_fixture_baseline(fixture)
        except Exception as exc:  # noqa: BLE001 - a fixture-level failure is a result, not a harness crash
            r = {"id": fixture["id"], "split": fixture["split"], "error": str(exc), "passed": False}
        results.append(r)
        print(f"  -> {r}")

    results_dir = ROOT / "results"
    results_dir.mkdir(exist_ok=True)
    out_path = results_dir / f"eval_results_{args.mode}.json"
    out_path.write_text(json.dumps(results, indent=2))
    print(f"\nwrote {out_path.relative_to(ROOT.parent)}")

    if args.mode == "agent":
        total = len(results)
        passed = sum(1 for r in results if r.get("passed"))
        held_out = [r for r in results if r["split"] == "held-out"]
        held_out_passed = sum(1 for r in held_out if r.get("passed"))
        false_resolved = sum(
            1 for r in held_out
            if r.get("final_artwork_status") == "RESOLVED" and r.get("expected_artwork_status") != "RESOLVED"
        )
        print(f"\nAgent run: {passed}/{total} passed overall, {held_out_passed}/{len(held_out)} held-out passed, "
              f"{false_resolved} falsely-resolved held-out case(s).")
    else:
        resolved = sum(1 for r in results if r.get("reached_resolved"))
        print(f"\nBaseline run: {resolved}/{len(results)} reached RESOLVED with rules alone (no agent).")


if __name__ == "__main__":
    main()
