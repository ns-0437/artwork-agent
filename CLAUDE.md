# Artwork Exception Resolution Agent

Independent six-day portfolio prototype built for a specific AI Agent Engineer application (stack: Go, TypeScript, GraphQL, Postgres, GCP). Not affiliated with, and has no access to, any real company's production systems.

**Deliverable:** resolve an artwork blocker on a simulated sticker order — inspect the file, clarify customer intent if needed, perform one eligible repair (uniform-background bleed extension), verify it, and prepare a proof — with the case persisted so work resumes after replies or failures.

Spec of record: `docs/build-brief.pdf` (the six-day build brief). When this file and the brief disagree, the brief wins; update this file to match.

## Code structure

```
web/                    TypeScript UI — case view, upload, clarification Q&A. Polls case status.
services/api-go/        Go GraphQL API + worker. Owns workflow, validation, state transitions, the agent loop.
services/image-python/  Python/Pillow. Owns ONLY deterministic image inspection + the one repair. No workflow logic.
db/migrations/          Postgres schema (orders, jobs, assets, findings, clarifications, repairs, tool_events).
evals/                  Fixture generation + evaluation harness (dev/held-out split, baseline vs agent).
infra/                  Docker Compose (local) + Cloud Run/GCP deploy config.
docs/                   Architecture, evaluation results, case study, demo notes.
```

(This section is a skeleton as of Day 0 — update it as real files land under each directory.)

## Points to remember

1. **Rules are deterministic; the model never computes or overrides a measurement.** PPI, color-mode/profile, and bleed-coverage math live in `services/image-python`, are unit-tested, and are versioned (`rule_version`). The agent (Go + LLM adapter) only chooses among `ask_clarification` / `request_repair` / `escalate` based on the rules' output — it cannot recalculate or second-guess a check result.
2. **REPAIRED is only set after exact *decoded-pixel* equality verification** of the original content region (see `services/image-python/app/repair/verify.py` when it exists) — decode both images and compare pixel arrays, never compare raw file bytes or hashes. A lossless re-encode can differ byte-for-byte while being pixel-identical, and that must still pass. `source_hash`/`derived_hash` are for the audit trail and idempotency lookups only — they are not the equality proof.
3. **Status ceiling, precisely:** `production_status` stays `NOT_RELEASED` always — nothing in v1 changes it. `proof_status` starts `NOT_PREPARED` and moves to `AWAITING_CUSTOMER_APPROVAL` **automatically** once the workflow prepares a proof (step 5 of the loop) — that transition is a system action, not a customer clicking "approve." Never implement anything that sets proof/production status *beyond* `AWAITING_CUSTOMER_APPROVAL` / `NOT_RELEASED`, and never require a manual trigger to get proof_status *to* `AWAITING_CUSTOMER_APPROVAL` once resolution completes.
4. **Python owns only image inspection/repair; Go owns workflow, validation, and state.** Don't let business logic (state transitions, eligibility gating on order fields, retries) creep into `services/image-python` — it should be a pure function of image bytes + declared trim/intent in, structured findings/repair result out.
5. **Every mutation needs an idempotency key + expected `case_version` check** to prevent duplicate repairs or state corruption on retry. `requestRepair`, `answerClarification`, and any agent-triggered mutation must validate both before writing.
6. **Bounded agent loop:** cap at 5 tool calls + 2 transient retries per execution segment; persist the budget across replies so a resumed case doesn't reset it. One schema-repair attempt precedes deterministic fallback.
7. **No claim of company savings, private integration, or Sticker Mule pipeline access** anywhere in code, README, comments, or docs. This is an independent prototype based on public requirements only.
8. **`artwork_status` transitions:** BLOCKED → AWAITING_CLARIFICATION → BLOCKED (after reply) → RESOLVED (after all supported blockers verified clear); unsafe/unsupported cases go to NEEDS_REVIEW instead. New artwork or dimension changes reopen the case (back to BLOCKED). **Short-circuit to RESOLVED**: if the first inspection already finds every check PASS/WARNING-only (nothing NEEDS_INPUT, no repair required), the case completes straight through to RESOLVED + proof prepared on that same pass — don't force a clean case through a clarification or repair round it doesn't need.
9. **Result vocabulary per check is exactly:** PASS / WARNING / NEEDS_INPUT / NEEDS_REVIEW. `REPAIRED` is a repair-only status, set post-verification. Don't invent alternate strings — evals and the UI key off these exact values.
10. **CMYK inputs are inspect-only in v1** — no repair path for them. Only RGB/PNG-style uniform-background extension is implemented.
11. **Repair eligibility is a conservative demo rule, not a manufacturing-suitability proof.** Reject gradients, transparency, textured edges, and foreground objects touching the boundary — don't loosen this to get more fixtures to pass.
12. **Bleed check requires full-bleed intent AND a confirmed trim rectangle.** If either is missing, return NEEDS_INPUT — never infer trim placement by guessing from edge pixels.
13. **Out of scope, don't add:** Stores/Notify/Reply/Ship integration, SVG/PDF parsing, generative upscaling, automatic color conversion, print certification, payments, production release, factory integration, multi-agent fleet, arbitrary cut contours.
14. **Provider adapter is single and replaceable** (Claude first, through Day 4). A Grok comparison is optional Day-5 scope only, gated on core evaluation gates already passing — never let it displace reliability work.
15. **Untrusted input handling:** treat all customer text/files as untrusted — enforce file signature checks, 10MB upload cap, 25-megapixel decoded limit, sandboxed decoding, and timeouts in `services/image-python`'s entry points.
16. **One execution path, built real from Day 1.** `startResolution` → enqueue job (QUEUED) → Go worker claims it → calls the Python inspect endpoint → persists findings → updates status is the single pipeline used from Day 1 through Day 4. Day 1 doesn't build a throwaway stub that gets rebuilt on Day 2 — Day 2 just makes the Python side return real checks instead of a minimal decode. Never fork a second "real" path later.
17. **Storage is behind one interface.** `internal/storage` in `services/api-go` has a `LocalDisk` implementation for dev (content-addressed by hash under e.g. `./data/artifacts`) and a `GCS` implementation added at Day 5 deploy time. Call sites never change between the two.
18. **Job claims are one atomic SQL statement**, not read-then-write: `UPDATE jobs SET status='RUNNING', worker_id=$1, lease_expires_at=... WHERE id=$2 AND status='QUEUED' AND (lease expired or unheld) RETURNING *`. Only the worker holding a live lease may progress or complete the job; a reaper reclaims expired leases.
19. **Repair identity is a DB unique constraint** (`UNIQUE(order_id, idempotency_key)` on `repairs`), not just an app-level check — a retried request finds the existing row atomically instead of racing an insert.
20. **`case_version` is checked at write time, in the UPDATE's WHERE clause**, not just read-then-compare — zero rows affected means stale, and the caller gets a version-conflict error rather than a silent overwrite.
21. **Crash-safety is a tested scenario, not an assumption.** Day 4 includes a chaos test: kill the worker after the repaired artifact is written to storage but before the DB row marking REPAIRED commits. Recovery must not double-write or leave two conflicting REPAIRED states — content-addressed storage keys make a retried write a safe no-op.
22. **Fixtures and the dev/held-out split start Day 1-2, not Day 5.** A small seed set lands with the Day 1 skeleton so the real execution path has something to run against immediately; it grows through Day 2 as checks land. Day 5 is reserved for running the eval harness against the already-built set, freezing results, and deployment — not for building fixtures from scratch.

## Working rules

- Commit after every meaningful unit of work (passing test, working endpoint, migration, fixed bug, rule implemented). Messages explain *why*, not *what*.
- Update this file whenever the directory structure or a load-bearing constraint changes — it's living documentation.
