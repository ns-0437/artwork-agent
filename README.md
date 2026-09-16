# Artwork Exception Resolution Agent

Independent portfolio prototype: resolve an artwork blocker on a simulated sticker order — inspect, clarify if needed, repair, verify, prepare a proof.

This is an independent prototype based on publicly available requirements. It has no access to any real company's production pipeline, customer files, printer profiles, or operational metrics, and makes no claim of measured savings or private integration.

Status: **Day 4 complete, hardened, and the full five-step loop is now implemented** — order model, deterministic checks, one verified repair, and a bounded agent loop (clarify → resume → auto-repair → verify → prepare proof) are all working end to end, verified live with a real LLM provider. **The provider is currently Groq, not Claude** (the Anthropic account available had no usable credits) **and not xAI's Grok either** (an unrelated company also using a similar-sounding name) - see CLAUDE.md point 14 for why this distinction matters and how the adapter stays swappable. A post-review pass made the agent loop durable end to end: every step (inspect → agent decision → clarification/repair → prepare proof) is now a queued job enqueued atomically with the state change that produces it, so a crash at any point resumes correctly rather than stalling - **verified live with actual crash-recovery tests**, not just argued for by design. Budgets are reserved atomically before each provider call, clarifications are restricted to the one question this system supports (with explicit Yes/No UI controls), and escalation is now a real persisted `NEEDS_REVIEW` state transition bound to the specific inspection that triggered it. Proof preparation (loop step 5) now renders and stores a real proof artifact and moves `proof_status` to `AWAITING_CUSTOMER_APPROVAL` - the one previously-missing step. See docs/architecture.md's "Post-Day-4 durability, safety, and correctness pass" and "Proof preparation (loop step 5)" sections for detail. Not yet done: evaluation harness and deployment (Day 5).

See [CLAUDE.md](CLAUDE.md) for the project map and constraints, and [docs/build-brief.pdf](docs/build-brief.pdf) for the full spec.

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
