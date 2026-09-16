# Artwork Exception Resolution Agent

Independent portfolio prototype: resolve an artwork blocker on a simulated sticker order — inspect, clarify if needed, repair, verify, prepare a proof.

This is an independent prototype based on publicly available requirements. It has no access to any real company's production pipeline, customer files, printer profiles, or operational metrics, and makes no claim of measured savings or private integration.

Status: **Day 4 complete, hardened, and the full five-step loop is now implemented** — order model, deterministic checks, one verified repair, and a bounded agent loop (clarify → resume → auto-repair → verify → prepare proof) are all working end to end, verified live with a real LLM provider. **The provider is currently Groq, not Claude** (the Anthropic account available had no usable credits) **and not xAI's Grok either** (an unrelated company also using a similar-sounding name) - see CLAUDE.md point 14 for why this distinction matters and how the adapter stays swappable. A post-review pass made the agent loop durable end to end: every step (inspect → agent decision → clarification/repair → prepare proof) is now a queued job enqueued atomically with the state change that produces it, so a crash at any point resumes correctly rather than stalling - **tested against hand-reproduced crash STATES (no process has actually been killed - see CLAUDE.md points 21/48)**, not just argued for by design. Budgets are reserved atomically before each provider call, clarifications are restricted to the one question this system supports (with explicit Yes/No UI controls), and escalation is now a real persisted `NEEDS_REVIEW` state transition bound to the specific inspection that triggered it. Proof preparation (loop step 5) now renders and stores a real proof artifact and moves `proof_status` to `AWAITING_CUSTOMER_APPROVAL` - the one previously-missing step. A second review pass then found and fixed four further edge cases (a follow-up job silently dropped when a stale one occupied its slot, a clarification answerable against artwork it was never asked about after a replacement upload, job leases too short to cover the agent's own retries, and a crash test that only proved lease reclamation rather than a crash after an artifact write) - all fixed and live-verified. See docs/architecture.md's "Post-Day-4 durability, safety, and correctness pass", "Proof preparation (loop step 5)", and "Second durability/correctness pass" sections for detail.

**Day 5 (evaluation harness): first frozen results, corrected for a fair comparison.** 34 fixtures (16 dev, 18 held-out; four more reserved, not yet run) driven through the real stack end to end three ways: agent (Groq), a genuinely agent-disabled baseline, and a scripted workflow (rules + a deterministic script - including an escalation fallback, added after an earlier pass was reviewed and found unfair without one) with no LLM involved. **34/34 evaluation cases passed - a different claim from "34/34 resolved": only 18/34 orders actually reached `RESOLVED`**, the rest correctly escalated to `NEEDS_REVIEW`. Every repair's trim region and every proof asset was independently re-verified by re-reading actual bytes from the storage backend, not trusted from DB metadata. Once the script had the same escalation tool the agent has, **its outcome matched the agent's exactly on all 34 cases** (same 18 resolved, same 16 escalated, zero token cost vs. 19,650 for the agent) - this project does not claim AI improved resolution over a well-designed deterministic workflow; it doesn't, for this fixture set. See [docs/case_study.md](docs/case_study.md) for the full writeup (framed around the engineering evidence: durable execution, verified repair, a correctly-scoped clarification loop, real proof generation, and this measurement process itself) and `docs/architecture.md`'s "Day 5" section for the raw numbers. GCS storage backend and Cloud Run deployment configs are prepared (`infra/gcp/`) but not executed against a live project.

See [docs/case_study.md](docs/case_study.md) for the full project narrative, [CLAUDE.md](CLAUDE.md) for the project map and constraints, and [docs/build-brief.pdf](docs/build-brief.pdf) for the full spec.

## Running locally

```
docker compose -f infra/docker-compose.yml up --build
```

This starts Postgres (with every `db/migrations/*.sql` file applied, in order, on first init), the Python image-inspection service, the Go API + worker, and the web UI. If you're updating from an older checkout and the schema doesn't match, run `docker compose -f infra/docker-compose.yml down -v` first to force a clean re-init (Postgres only applies migrations to a fresh volume).

- Web UI: http://localhost:5173
- GraphQL API: http://localhost:8080/graphql
- Image service (private in deployment, exposed locally for debugging): http://localhost:8081

No `.env` file is required for local use — `infra/docker-compose.yml` bakes in working defaults. See `.env.example` for what each variable does if you want to override one.

## Seed fixtures

`evals/fixtures/` holds a small seed set (dev/held-out split by design, per `evals/scripts/generate_fixtures.py`) used to exercise the execution path during development. It grows through Day 2 and is expanded to the full 60-fixture evaluation set on Day 5.
