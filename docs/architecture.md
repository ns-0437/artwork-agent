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

`worker.runAgentDecision` processes one `agent_decide` job (see the durability section below for why it's a job, not an in-process call after `runInspect`'s commit):

1. Reserves tool-call budget atomically BEFORE calling the provider - escalates immediately if the reservation fails (cap already hit), never calls the provider on spec.
2. Calls `Decide` with findings scoped to the ONE inspection this `agent_decide` job was created for (via `agent_source_job_id` → `ListFindingsForJob`, never "latest per check across order history"), retrying up to twice but ONLY for transient failures (network, 429/5xx, Groq's `tool_use_failed`) - a permanent failure (bad auth, malformed request) escalates on the first attempt instead of wasting retry budget.
3. Logs every attempt to `tool_events` (including the model's suggested clarification wording, kept for audit only - see below).
4. Validates the decision against those SAME findings before acting: `ask_clarification` requires an actual unconfirmed-trim finding to be present; `request_repair` requires an actual confirmed-trim insufficient-bleed finding. A mismatch escalates instead of acting on a decision that doesn't match what was actually found - the model's stated reasoning is never trusted at face value.
5. Applies the (validated) decision and commits it atomically with the job's completion via `store.CompleteAgentDecision`: `ask_clarification` persists a **fixed** question and moves to `AWAITING_CLARIFICATION`; `request_repair` enqueues a repair job the same way the manual mutation does (`runRepair`'s own precondition check remains the deeper safety net); `escalate` is now a REAL state transition - it persists `artwork_status = NEEDS_REVIEW` and a reason, not a no-op.

**A real bug found and fixed during Day 4 live testing:** the model's own suggested clarification wording could have the OPPOSITE yes/no polarity from what `answerClarification`'s `interpretYesNo` assumes (e.g. "does it already include bleed?" vs. the intended "is it trim-only?"), which would silently invert `artwork_is_trim_only` on a "yes" answer. Fixed by never using the model's wording for the persisted, customer-facing question - it's logged to `tool_events` for audit only, while the actual question is always the fixed `trimOnlyClarificationQuestion` constant. Since v1 supports exactly one clarification type, there's no upside to trusting free-form phrasing for it. The web UI now presents this as two explicit Yes/No buttons rather than a free-text field, for the same reason.

`answerClarification` validates the clarification is unanswered and `case_version` still matches, interprets a deterministic yes/no (never via the model), applies it as a trim confirmation, and enqueues a fresh inspect job against the current asset - all in the same transaction (see below).

## Post-Day-4 durability, safety, and correctness pass

A second review pass (after Day 4's happy path was confirmed working) found four gaps in the agent loop and fixed all of them, verified live including a crash-recovery test against a hand-reproduced crash STATE (no process was actually killed - see below):

1. **Agent continuation is now durable via the job queue, not in-process calls after commit.** Previously, `runInspect` committed the inspection `SUCCEEDED`, then called `runAgentDecision` as a separate in-process function call - a crash between those two steps left a committed result with nothing queued to resume it. Now every handoff in the loop is its own job-queue entry, enqueued in the SAME transaction as the state change that produces it (CLAUDE.md point 24):
   - Inspection completion (`CompleteInspection`) optionally enqueues an `agent_decide` job, bound via a new `jobs.agent_source_job_id` column to the specific inspect job whose findings it must act on.
   - Agent-decision completion (`CompleteAgentDecision`) is what actually applies the decision (persist clarification / enqueue repair / escalate) - atomically with marking the `agent_decide` job `SUCCEEDED`.
   - Clarification-answer completion (`AnswerClarificationAndConfirmTrim`) enqueues the follow-up `inspect` job in the same transaction as recording the answer.
   This reuses the exact lease/reclaim machinery already built for `inspect`/`repair` jobs (`ClaimNextJob`'s `status='RUNNING' AND lease_expires_at < now()` branch) - no new crash-recovery mechanism was needed, just extending the existing one to cover the agent's own steps.
2. **`ask_clarification` restricted to the one supported question, escalating everything else; free-text UI replaced with explicit Yes/No buttons.** Previously every `ask_clarification` became the trim-only question regardless of which finding triggered it. Now `worker.hasUnconfirmedTrimFinding` checks the ACTUAL bound findings before persisting the fixed question - a low-resolution `NEEDS_INPUT` (which has no trim question that would help) now escalates instead. The system prompt was also narrowed to describe `ask_clarification` as covering only the trim case. `web/src/pages/CaseView.tsx` replaced its free-text answer field with two buttons that send literal `"yes"`/`"no"`.
3. **Tool-call and retry budgets are reserved atomically before each attempt, not recorded after.** `store.ReserveAgentToolCall`/`ReserveAgentRetry` are conditional `UPDATE ... WHERE used < cap` statements checked BEFORE calling the provider; a failed reservation stops the loop without ever making the call. Provider errors are now classified transient vs. permanent (`internal/agent/errors.go`) so only genuinely retryable failures (network errors, 429/5xx, Groq's `tool_use_failed`) consume retry budget - an auth or malformed-request error escalates immediately instead of retrying something that can't succeed.
4. **Escalation is now a real, persisted `NEEDS_REVIEW` + reason state transition, bound to one inspection's findings.** Previously `ActionEscalate` was a no-op (the case just stayed wherever `decideArtworkStatus` had already left it, with no record of *why* the agent gave up). `CompleteAgentDecision`'s `escalate` branch now writes `artwork_status = NEEDS_REVIEW` and persists the reason. Every `agent_decide` job is bound via `agent_source_job_id`/`ListFindingsForJob` to the ONE inspection whose findings it must act on - never an aggregate of "the latest finding per check name" across the order's entire history, which could otherwise mix in stale results from a since-superseded inspection.

**Live verification performed for all four fixes** (see CLAUDE.md points 6/6a/21/24/28/40/43 for the code-level detail):
- Auto-repair path: upload (trim confirmed) → insufficient-bleed finding → agent `request_repair` → repair job → `RESOLVED`, all via the durable job chain (`inspect` → `agent_decide` → `repair`).
- Clarification path: upload (trim unconfirmed) → agent `ask_clarification` → fixed question persisted → answered "yes" via the same path the UI buttons use → case reopens and resolves via the same auto-repair chain.
- Escalation path: a low-resolution fixture under `border` intent produced a `NEEDS_INPUT` finding unrelated to trim; the model itself chose `escalate` (narrowed prompt), Go's validation had nothing to override, and the reason `"model chose to escalate"` was persisted with `artwork_status = NEEDS_REVIEW`.
- **Crash recovery**, the specific test previously listed as not done - a simulated crash STATE, not an actual worker process being killed: with the worker stopped, an `inspect` job was hand-set to `RUNNING` with an expired lease and a fake `worker_id`, reproducing the DB state a crash mid-write would leave (no `docker kill` or SIGKILL was sent to anything - the row was edited directly via SQL). Confirmed the order had zero findings and was still `BLOCKED` beforehand - the reproduced state left no side effects. A fresh worker process was started and, via its normal poll loop, reclaimed the stale-leased job (a different `worker_id` picked it up), completed the full chain exactly once, and `agent_tool_calls_used`/`tool_events` showed no double-counting from the crashed attempt.

## Proof preparation (loop step 5)

The brief's fifth loop step - "Verify authorized repairs and recheck blockers. Prepare a proof, update the artwork state, and retain the complete audit trail" - was the one step left unimplemented through the post-Day-4 pass above. It's now a third durable job type, `prepare_proof` (migration `0007_prepare_proof.sql`), following the exact same pattern as `agent_decide`:

- `worker.runInspect` and `worker.runRepair` each compute `enqueuePrepareProof := artworkStatus == "RESOLVED"` and pass it into `CompleteInspection`/`CompleteRepair`, which enqueue the `prepare_proof` job - bound to whichever asset just became current (the original, for a case that short-circuits to `RESOLVED` on first inspection; the newly repaired canvas, for a repair-cleared one) - in the SAME transaction as the state advance that first reached `RESOLVED`. A crash between "case resolved" and "proof prepared" leaves a queued job to resume, not a case stuck at `RESOLVED` with `proof_status` stuck at `NOT_PREPARED` forever.
- `worker.runPrepareProof` fetches that asset's bytes and calls a new `services/image-python` endpoint, `/proof` (`app/proof/render.py:render_proof`) - a pure Pillow function with no order/workflow knowledge, matching every other check/repair function (CLAUDE.md point 4). It renders the final artwork with an identifying caption (order id, artwork version, case version) added as a **strip below the image, never drawn on top of it** - unlike the repair preview's trim/bleed overlay (which IS drawn on the image, because that preview is an internal audit artifact, not what the customer is approving), the proof must never alter the pixels it's presenting for approval.
- `store.CompleteProofPreparation` commits the job's `SUCCEEDED` status, the new `kind='proof'` asset, and the `proof_status → AWAITING_CUSTOMER_APPROVAL` transition atomically - the only place in the codebase that transition is allowed to happen (CLAUDE.md point 3).

**Live-verified:** the clean-upload short-circuit path (upload → inspect → `prepare_proof` → proof asset stored, `AWAITING_CUSTOMER_APPROVAL`), the auto-repair path (inspect → agent_decide → repair → `prepare_proof` → same result), and the negative case - a `NEEDS_REVIEW`/escalated order correctly gets no `prepare_proof` job and no proof asset at all, `proof_status` staying `NOT_PREPARED`. A crash-recovery test was also run specifically against the new job type (same hand-reproduced-crash-STATE method as the agent-loop test above, not an actual killed process): the state a crash mid-`prepare_proof` would leave was reproduced by hand, confirmed to leave zero side effects, and a fresh worker process reclaimed and completed it exactly once.

## Second durability/correctness pass (post-proof-preparation)

A further review pass found four gaps in edge cases the first hardening pass hadn't covered - all fixed and live-verified before proceeding to Day 5's evaluation harness:

1. **Silently dropped continuations.** Every internal job-enqueue site used `ON CONFLICT (order_id, job_type) ... DO NOTHING`, which is correct for a genuine duplicate but WRONG for a stale one: if an active job of that type already existed bound to a DIFFERENT (superseded) asset/case_version, the insert of the new, correct continuation silently no-opped while the transaction still reported success - the case could then get stuck with nothing left to process its current state. Fixed with `store.ensureJobEnqueued`, used everywhere a job is enqueued as a side effect of completing another (never for the client-facing `CreateJob` path, which surfaces its outcome to the caller instead): it reuses an active job only when bound to the exact asset/version needed, otherwise marks the stale one `FAILED` first so the fresh one is never dropped. Live-verified by hand-inserting a stale active `inspect` job during an `AWAITING_CLARIFICATION` case and confirming the answer still correctly enqueued a fresh, correctly-bound job that ran the case through to `RESOLVED`.
2. **Clarifications not bound to their originating artwork.** A clarification asked about one upload had no link to which upload that was - after a replacement upload (which bumps `case_version`/`artwork_version` but left old clarifications untouched), the OLD question could still be answered using the order's now-current `case_version`, silently applying the reply to artwork it was never asked about. Fixed via migration `0008_clarification_binding.sql` (`clarifications.artwork_version`, `clarifications.invalidated_at`): a replacement upload now invalidates any still-unanswered clarification in the same transaction, and answering one now also requires it to match the order's current `artwork_version` and be the most recently created active one. Live-verified: invalidated a pending clarification via a replacement upload, then confirmed answering it (even with the correct current `case_version`) was rejected.
3. **Job leases too short to cover the agent's own retries.** Three provider attempts (one call + two retries) at the adapter's prior 20s HTTP timeout could take up to 60s, exceeding the 30s job lease - not corrupting anything (every completion is still gated on worker/lease ownership) but wasting real provider calls and budget when a lease expires mid-call and a second worker reclaims the same job. Fixed with a `context.WithTimeout`-bounded deadline (20s, comfortably inside the lease) threaded through the whole retry sequence via an extracted, independently unit-tested `decideWithBoundedRetries` function - a slow or hanging provider can now never hold a job past its lease, regardless of retry count. Covered by a Go unit test using a fake provider that "takes" 5 seconds against a 50ms test deadline, confirming the call returns promptly rather than blocking for the fake's full delay.
4. **The earlier test only proved lease reclamation, not a crash after an artifact write - and neither test involved an actual killed process; both reproduce the DB/storage STATE a crash would leave, by hand.** Hand-setting an expired lease (the earlier test) never exercises a worker that already wrote to `internal/storage` - which happens BEFORE the database transaction, since storage isn't transactional with Postgres - and then crashed before that transaction committed. Reproduced directly this time: got the real repaired/preview bytes from `services/image-python`'s `/repair` endpoint, manually wrote them into the storage volume at their real content-addressed paths (reproducing "storage write already succeeded"), then reproduced the crash state (a `repair` job left `RUNNING` with an expired lease and no corresponding DB rows - no process was sent a kill signal to get here). A fresh worker reclaimed it, reprocessed the repair FROM SCRATCH (calling `/repair` again, producing the same bytes deterministically), and its own storage write recognized the content-addressed path already existed - a safe no-op - before committing exactly ONE `repairs` row and ONE set of `repaired`/`preview`/`proof` assets (hash-verified), reaching `RESOLVED`/`AWAITING_CUSTOMER_APPROVAL` rather than getting stuck. An actual process-kill test (verifying behavior under a real SIGKILL mid-syscall, e.g. a partially-written file) has not been done.

## Day 5: evaluation harness and first results

**Fixture set:** 34 fixtures (16 dev, 18 held-out) in `evals/fixtures/manifest.json`, covering clean art, the exact 300 PPI boundary (299/300/301, isolated from bleed noise via border intent), color-profile variants (RGB with/without ICC, grayscale, CMYK), the repair-eligible and clarification-round-trip paths, four distinct repair-ineligible reasons (gradient, texture, foreground object, transparency), an RGBA-but-opaque repair-eligible case, and a mixed-issue case (low resolution AND missing bleed at once). Four additional held-out fixtures are reserved and have never been run - see `evals/CHANGES.md`.

**Harness (`evals/scripts/run_eval.py`):** drives every fixture through the real GraphQL API end to end - no mocking. Three modes:

- `--mode agent` - the real stack, Groq enabled.
- `--mode baseline` - a GENUINELY agent-disabled worker (`worker-baseline`, no provider key configured at all, so `w.Agent` is actually `nil` and `agent_decide` is never enqueued) - descriptive only, no further action ever taken past the first inspection.
- `--mode scripted` - the same agent-disabled worker, but the harness itself drives the same tool calls (`confirmTrim`/`answerClarification`/`requestRepair`) and the same scripted customer replies a non-AI scripted workflow would use, deterministically. This is the brief's "compare against rules plus scripted clarification/report templates," not just "no agent at all."

Grading re-reads actual bytes from the storage backend (via the GraphQL-exposed `Asset.storageKey`) rather than trusting database metadata: the original asset's hash is re-verified against the source file, a repaired canvas's trim region is independently pixel-compared against the original (outside the system's own `verify_repair` check), and every proof asset is confirmed to actually decode as an image. A run that times out is failed outright regardless of what the last snapshot showed; any unexpected `FAILED` job, or a repair attempted on a fixture nothing about it calls for, fails the fixture.

`--mode scripted` also has a deterministic escalation fallback (`store.EscalateCase`, a new mutation): if neither confirming trim nor requesting repair resolves the case, the script escalates with a fixed rule-based reason - added after an initial version of this comparison was reviewed and found unfair for omitting it (a script with no way to give up on a case will always look worse than the agent purely for lacking an escalation path, regardless of whether either one's actual *decisions* differ). `agent.Decision.TokenUsage` (Groq's own reported token accounting, logged to `tool_events`, exposed via `Order.toolEvents`) gives a zero-guesswork cost signal: nonzero for the agent, always exactly zero for baseline/scripted, since no provider call happens there.

### Results (first frozen pass, this fixture set)

| Mode | n | RESOLVED | NEEDS_REVIEW | BLOCKED | avg latency | tokens |
|---|---|---|---|---|---|---|
| agent (Groq) | 34 | 18 | 16 | 0 | 18.3s | 19,650 |
| baseline (rules only, no agent, no script) | 34 | 12 | 14 | 8 | 14.8s | 0 |
| scripted (rules + deterministic script, incl. escalation fallback) | 34 | 18 | 16 | 0 | 17.9s | 0 |

Agent: **34/34 evaluation cases passed** (16/16 dev, 18/18 held-out) - **not** the same claim as "34/34 orders resolved": only **18/34 orders actually reached `RESOLVED`**, the rest correctly ended at `NEEDS_REVIEW` (16) and passed evaluation on that basis. 0 timed out, 0 falsely-resolved held-out cases, 0 unexpected job failures. Every one of the 6 cases that reached `RESOLVED` via repair had its trim region independently pixel-verified against the original (`repair_trim_pixels_identical: true`); every one of the 18 `RESOLVED` cases had its proof asset independently confirmed to open as a valid image; every one of the 34 cases had its original asset's stored bytes re-read and re-hashed against the uploaded file.

**What this shows, once the scripted comparison was made fair:**
- **Rules alone leave 8/34 cases silently `BLOCKED` forever** - nothing ever proposes a next step for a case needing trim confirmation, an eligible repair, or a "give up and flag this" decision. Having *either* the agent or a script that can act AND escalate closes all 8.
- **Agent and scripted matched EXACTLY, fixture for fixture, on all 34 cases** once the script was given the same escalation capability (18 resolved / 16 escalated / 0 stuck, identically) - zero mismatches. The LLM's decisions never did anything a hand-written deterministic rule ("if nothing else applies, escalate with a reason") wouldn't have, for this fixture set. An earlier pass in this same project reported the agent "escalating 6 cases a script left `BLOCKED`" - that was measuring "has an escalation path" vs. "doesn't," not a difference in judgment; it's corrected here.
- **This is not evidence that AI improves resolution over a well-designed deterministic workflow** - for this fixture set, it doesn't. The LLM component's value is a single, swappable decision point available for genuinely ambiguous judgment calls a fixed rule set can't anticipate; this fixture set doesn't happen to exercise that difference.
- **`mixed-issues-a`/`-b`'s outcome is not blind held-out evidence** - see `evals/CHANGES.md`: the fixture's assertion was relaxed after observing the model's actual behavior in an early run, so it's a valid regression check but not independent confirmation of generalization.

**Honest boundaries on this pass:**
- This is 34 fixtures generated by this same project, not an independent test set - "release targets, not results," per the brief.
- Four held-out fixtures were deliberately withheld from every run above until the scripted-comparison fix (point 3) was frozen, then run once as a genuinely blind pass: 4/4 passed, 2/4 resolved, 0 falsely resolved - consistent with the 34-fixture pattern, but a small check (n=4), not broad proof, and reported as its own line rather than folded into the numbers above (see `evals/CHANGES.md` and `docs/case_study.md`).
- The crash-recovery tests behind this system's durability claims are simulated crash *states*, not an actual killed process (CLAUDE.md points 21/48) - a real process-kill test remains undone.
- The GCS storage backend (`internal/storage/gcs.go`) has since been verified against a real bucket as part of the live GCP deployment - see `infra/gcp/README.md`.
- No dollar cost figure or Grok/Claude comparison is included - out of scope for this pass given the brief's own "defer this comparison before compromising reliability."

See `docs/case_study.md` for the full narrative writeup of this project, framed around the engineering evidence (durable execution, verified repair, a correctly-scoped clarification loop, real proof generation, and this measurement process itself) rather than an AI-resolution claim.

## Known gaps (tracked, not yet fixed)

- **No authentication or ownership checks.** `ownerId` on an order is a free-text field the client supplies - nothing verifies the caller actually owns the order they're mutating. Dev ports are bound to `127.0.0.1` specifically because of this gap (see CLAUDE.md point 26). The live deployment accepted this rather than closing it first - setting `READ_ONLY_DEMO=true` instead, rejecting every mutation server-side (see `infra/gcp/README.md`), which is a read-only boundary, not a substitute for real per-owner access control.
- **Upload tickets are single-use in intent but not enforced as such.** The HMAC token can be replayed against `/uploads/{token}` until it expires (10 minutes) - each replay creates a new `assets` row rather than being rejected as a reused ticket.
