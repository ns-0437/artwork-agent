# Architecture (Day 1 stub)

```
web/ (TypeScript, React) --GraphQL--> services/api-go (Go)
                                          |         \
                                     Postgres        Go worker (same image,
                                     (orders,          different entrypoint)
                                      jobs, assets,        |
                                      findings, ...)   services/image-python
                                                        (FastAPI + Pillow)
```

- **web/** polls `order(id)` over GraphQL to show case status; it never talks to Postgres or the image service directly.
- **services/api-go** is one Go module with two entrypoints from the same image: `cmd/server` (GraphQL API + upload endpoint) and `cmd/worker` (polls Postgres for queued jobs). They share `internal/store`, `internal/storage`.
- **services/image-python** is a private HTTP service reachable only from the worker. It does deterministic image inspection/repair and nothing else - no order/job/state knowledge.
- **Storage** is behind one interface (`internal/storage.Storage`): a content-addressed local-disk implementation for dev, a GCS implementation added Day 5. The worker fetches original artwork bytes from storage and forwards them to Python over HTTP, so Python never needs to know which storage backend is active.

## State fields (frozen Day 1, see CLAUDE.md for the full rationale)

- `artwork_status`: `BLOCKED` → `AWAITING_CLARIFICATION` → `BLOCKED` (after reply) → `RESOLVED` (or `NEEDS_REVIEW` for unsafe/unsupported cases). A case that passes every expected check on the first inspection short-circuits straight to `RESOLVED` - it never manufactures an unnecessary clarification/repair round. WARNING-level findings never block resolution. A new upload reopens the case back to `BLOCKED` atomically (`store.RecordArtworkUpload`).
- `proof_status`: `NOT_PREPARED` → `AWAITING_CUSTOMER_APPROVAL`, but ONLY once an explicit proof-preparation step actually creates and stores a proof artifact (Day 4's agent loop). `artwork_status=RESOLVED` alone never implies this - `decideArtworkStatus` always leaves `proof_status=NOT_PREPARED`.
- `production_status`: `NOT_RELEASED` always, in every v1 code path.
- `job.status`: `QUEUED` → `RUNNING` → `SUCCEEDED` | `FAILED`, claimed via one atomic SQL statement (`internal/store.ClaimNextJob`) so two workers can never both claim the same row.

## Day 1 execution path (built real, not stubbed)

1. `createOrder` mutation inserts an `orders` row.
2. `createUpload` mutation issues a short-lived, order-scoped signed upload URL (HMAC-signed token; a local stand-in for a signed cloud-storage URL).
3. The client `POST`s the artwork to that URL; the handler stores it content-addressed via `internal/storage` and records an `assets` row (`kind='original'`).
4. `startResolution` mutation enqueues a `jobs` row (`job_type='inspect'`, `status='QUEUED'`).
5. The worker claims the job atomically, fetches the asset bytes from storage, calls `services/image-python`'s `/inspect` endpoint, and (Day 1) advances the order's state and marks the job `SUCCEEDED`.

Day 2 replaces step 5's body with real findings persistence and the short-circuit-to-RESOLVED logic; steps 1-4 and the claim/dispatch/complete plumbing in step 5 do not change.

## Day 2: deterministic checks

`/inspect` now runs three checks (`services/image-python/app/checks/`) against the decoded image and the order's declared width/height/unit/intent/trim-confirmation, returning PASS/WARNING/NEEDS_INPUT/NEEDS_REVIEW per check plus evidence and a `rule_version`. The worker persists every finding, the job's result, and the order's state advance in one transaction (`store.CompleteInspection`), then `worker.decideArtworkStatus` aggregates the findings:

- First, completeness: every check *expected for this intent* must be present exactly once with a recognized result - otherwise `NEEDS_REVIEW` (system anomaly, not a customer-actionable finding).
- Any `NEEDS_REVIEW` → `artwork_status = NEEDS_REVIEW`.
- Else any `NEEDS_INPUT` → stays `BLOCKED` (not `AWAITING_CLARIFICATION` - that transition needs Day 4's clarification round-trip to mean anything).
- Else (every expected check `PASS`/`WARNING`) → short-circuits to `RESOLVED`, `proof_status` stays `NOT_PREPARED` (see CLAUDE.md point 3 - resolving the artwork blocker and preparing a proof are different steps).

**Trim rectangle policy (v1):** `app/checks/trim.py`'s `resolve_trim` is the single source of truth. Border intent needs no confirmation - the whole image is unambiguously the trim. Full-bleed intent needs an explicit customer confirmation (`orders.artwork_is_trim_only`, set via the `confirmTrim` mutation) that the upload is trim-only (no bleed margin yet), in which case trim = image bounds; without it, both resolution and bleed report `NEEDS_INPUT` rather than guessing a boundary from pixel content, which the brief explicitly disallows. Both `resolution.check_resolution` and `bleed.check_bleed` take the confirmed trim dimensions explicitly (never the raw canvas size), so a repair that later produces a canvas larger than its trim stays correct - see CLAUDE.md point 12.

## Day 3: verified repair

`confirmTrim(orderId, artworkIsTrimOnly, caseVersion)` records whether the customer confirms the current upload is trim-only (no bleed margin) - it reopens the case (bumps `case_version`, resets `artwork_status`/`proof_status`) since it changes what the checks can determine.

`requestRepair(orderId, idempotencyKey, caseVersion)` enqueues a `job_type='repair'` job bound to the current original asset, gated the same way `startResolution` is (one in-flight job per order+type) plus its own idempotency key so a retry after completion returns the existing outcome instead of duplicating it.

The worker's `runRepair`:

1. Checks the precondition (`intent='full_bleed' AND artwork_is_trim_only=true`) itself, before calling Python at all - `/repair` has no way to resolve trim ambiguity, so Go must have already resolved it.
2. Calls `/repair`, which runs eligibility (`app/repair/eligibility.py`: opaque + uniform-color edge band), and if eligible, extends the canvas (`extend_background.py`: new canvas filled with the verified edge color, original pasted unresampled, margin rounded outward), then verifies (`verify.py`: decoded-pixel equality of the original content region - never file bytes/hashes, never a perceptual score). An unverified candidate is discarded and reported as a failure; nothing is written to storage in that case.
3. On success, stores the repaired canvas and an annotated preview (`preview.py`: trim + canvas-edge overlays, drawn on a *copy*) via `internal/storage`, then re-runs `/inspect` against the new canvas with the now-known trim dimensions passed explicitly (never re-derived from the larger canvas).
4. Commits everything - job SUCCEEDED, the `repairs` row, the new assets, the order's trim coordinates, fresh findings, and the state advance - in one transaction (`store.CompleteRepair`), gated on the worker's lease and `case_version` exactly like `CompleteInspection`.

An ineligible or precondition-failing attempt still commits (a REJECTED `repairs` row with a specific reason, `artwork_status → NEEDS_REVIEW`) but touches nothing else - no new assets, no trim coordinates, original untouched.

**v1 simplification:** only one repair attempt may be in flight per order regardless of idempotency key (see CLAUDE.md point 25).

## Post-Day-3 correctness fixes

Four gaps found on review, fixed before Day 4:

1. **Current asset tracking.** `orders.current_asset_id` (migration `0004`) now names "the artwork right now" explicitly, set atomically by `RecordArtworkUpload` and `CompleteRepair`. `resolveStartResolution` and `worker.runInspect` use it (plus the order's known trim coordinates, when the job's asset matches) instead of always re-fetching the latest *original* - otherwise a re-inspection after a successful repair would silently fall back to the pristine (missing-bleed) upload.
2. **Idempotency order.** `requestRepair` now checks the idempotency key BEFORE `case_version` freshness (`internal/graph/repair_idempotency.go:decideRepairRequestAction`, unit-tested standalone). An exact replay of a completed request succeeds even though `case_version` moved on as a result of that same completion; the same key resubmitted with a *different* `case_version` is rejected explicitly, not silently served the old result.
3. **Color fidelity + verification depth.** `/repair` rejects CMYK outright (never converts it), preserves the source's actual mode (RGB or grayscale) and ICC profile through the whole pipeline, and verifies pixel-equality against the PNG it actually saved and reopened - not the in-memory canvas - so a save/reload artifact can't hide.
4. **Geometry validation.** `/inspect` and `/repair` reject non-positive declared dimensions (also enforced by a DB `CHECK` constraint); `/repair` additionally rejects an artwork/declared aspect-ratio mismatch outright (a single min-axis PPI can otherwise falsely satisfy bleed) and caps the expanded canvas size before allocating it.

## Day 4: bounded agent loop

`internal/agent.Provider` is one interface, one method (`Decide`), returning exactly `ask_clarification` / `request_repair` / `escalate`. **The active implementation is `groq_adapter.go`, running on Groq (a fast open-model inference host), not Claude and not xAI's Grok** - the Anthropic account for this build had no usable credits; Groq was substituted as a documented, honest stand-in. Confusing Groq with Grok would misrepresent what actually ran - see CLAUDE.md point 14.

`worker.runAgentDecision` runs after `runInspect`'s deterministic commit, only when the result isn't already `RESOLVED`:

1. Refuses to call the provider at all if `orders.agent_tool_calls_used` has already hit the 5-call cap - escalates straight to `NEEDS_REVIEW` instead (verified live: pre-set the counter to 5, confirmed zero new `tool_events` and immediate escalation).
2. Otherwise calls `Decide` with the latest findings (deduplicated per check - findings are append-only history, the agent must reason about the CURRENT state only), retrying up to twice on a transient failure (observed live: Groq occasionally returns "tool choice is required, but model did not call a tool", which retries and then succeeds).
3. Logs every attempt to `tool_events` (including the model's suggested clarification wording, kept for audit only - see below) and increments the persisted counters regardless of outcome.
4. Applies the decision: `ask_clarification` persists a **fixed** question and moves to `AWAITING_CLARIFICATION`; `request_repair` enqueues a repair job the same way the manual mutation does (`runRepair`'s own precondition check is the real safety net if the decision is wrong); `escalate` does nothing further (the case is already at `NEEDS_REVIEW`/`BLOCKED`).

**A real bug found and fixed during Day 4 live testing:** the model's own suggested clarification wording could have the OPPOSITE yes/no polarity from what `answerClarification`'s `interpretYesNo` assumes (e.g. "does it already include bleed?" vs. the intended "is it trim-only?"), which would silently invert `artwork_is_trim_only` on a "yes" answer. Fixed by never using the model's wording for the persisted, customer-facing question - it's logged to `tool_events` for audit only, while the actual question is always the fixed `trimOnlyClarificationQuestion` constant. Since v1 supports exactly one clarification type, there's no upside to trusting free-form phrasing for it.

`answerClarification` (now implemented) validates the clarification is unanswered and `case_version` still matches, interprets a deterministic yes/no (never via the model), applies it as a trim confirmation, and automatically enqueues a fresh inspect job against the current asset - "a validated reply enqueues continuation" from the brief, without the customer needing to separately call `startResolution`.

**Full loop verified live, end to end, with the real provider:** upload (no trim confirmed) → agent asks the fixed clarification → answered "yes" → case reopens, re-inspects → bleed comes back `NEEDS_REVIEW` (confirmed trim, insufficient margin) → agent decides `request_repair` → repair runs, verifies, and the case reaches `RESOLVED`. Three jobs (inspect, inspect, repair), one clarification, one repair row - all consistent with the audit trail in `tool_events`.

**Not implemented:** step 5 of the brief's loop ("prepare a proof") - `proof_status` still never leaves `NOT_PREPARED` anywhere in the code (see CLAUDE.md point 3). The actual "kill the worker mid-write" chaos test (CLAUDE.md point 21) also has not been run - the transactional design should make it safe, but that's a claim to verify, not assume, before the case study says so.

## Known gaps (tracked, not yet fixed)

- **No authentication or ownership checks.** `ownerId` on an order is a free-text field the client supplies - nothing verifies the caller actually owns the order they're mutating. Dev ports are bound to `127.0.0.1` specifically because of this gap (see CLAUDE.md point 26); this needs closing before any deployment beyond a local demo.
- **Upload tickets are single-use in intent but not enforced as such.** The HMAC token can be replayed against `/uploads/{token}` until it expires (10 minutes) - each replay creates a new `assets` row rather than being rejected as a reused ticket.
- **Proof preparation (loop step 5) is not implemented.** `proof_status` has no code path that ever sets it to `AWAITING_CUSTOMER_APPROVAL`.
- **The crash-safety chaos test has not been run.** Design analysis says it should be safe; nobody has actually killed a worker mid-write and confirmed it yet.
