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

- `artwork_status`: `BLOCKED` → `AWAITING_CLARIFICATION` → `BLOCKED` (after reply) → `RESOLVED` (or `NEEDS_REVIEW` for unsafe/unsupported cases). A case that passes every check on the first inspection short-circuits straight to `RESOLVED` - it never manufactures an unnecessary clarification/repair round. WARNING-level findings never block resolution.
- `proof_status`: `NOT_PREPARED` → `AWAITING_CUSTOMER_APPROVAL` (set automatically once the workflow prepares a proof - a system action, not the customer approving anything).
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

`/inspect` now runs three checks (`services/image-python/app/checks/`) against the decoded image and the order's declared width/height/unit/intent, returning PASS/WARNING/NEEDS_INPUT/NEEDS_REVIEW per check plus evidence and a `rule_version`. The worker persists every finding, the job's result, and the order's state advance in one transaction (`store.CompleteInspection`), then `worker.decideArtworkStatus` aggregates the findings:

- Any `NEEDS_REVIEW` → `artwork_status = NEEDS_REVIEW`.
- Else any `NEEDS_INPUT` → stays `BLOCKED` (not `AWAITING_CLARIFICATION` - that transition needs Day 4's clarification round-trip to mean anything).
- Else (every check `PASS`/`WARNING`) → short-circuits to `RESOLVED`, `proof_status = AWAITING_CUSTOMER_APPROVAL`.

**Trim rectangle policy (v1, documented limitation, not a bug):** there is no trim-selection input yet. The resolution check treats the whole decoded image as the trim region (matches the brief's own worked example). The bleed check only runs for `intent='full_bleed'` and always returns `NEEDS_INPUT` for now, since guessing a trim boundary from image content is explicitly disallowed by the brief ("return NEEDS_INPUT rather than guessing from edge pixels"). Day 3's repair sets trim coordinates explicitly when it builds the new canvas, which is what makes a real bleed measurement possible - see CLAUDE.md point 12.

## Known gaps (tracked, not yet fixed)

- **No authentication or ownership checks.** `ownerId` on an order is a free-text field the client supplies - nothing verifies the caller actually owns the order they're mutating. Dev ports are bound to `127.0.0.1` specifically because of this gap (see CLAUDE.md point 26); this needs closing before any deployment beyond a local demo.
- **Upload tickets are single-use in intent but not enforced as such.** The HMAC token can be replayed against `/uploads/{token}` until it expires (10 minutes) - each replay creates a new `assets` row rather than being rejected as a reused ticket.
