# Artwork Exception Resolution Agent

Independent portfolio prototype: resolve an artwork blocker on a simulated sticker order — inspect, clarify if needed, repair, verify, prepare a proof.

This is an independent prototype based on publicly available requirements. It has no access to any real company's production pipeline, customer files, printer profiles, or operational metrics, and makes no claim of measured savings or private integration.

Status: **Day 1** — order model + one real execution path (create order → upload artwork → enqueue job → worker inspects through Python → persist results → separate artwork/proof/production status). No deterministic checks yet (Day 2), no repair (Day 3), no agent loop (Day 4).

See [CLAUDE.md](CLAUDE.md) for the project map and constraints, and [docs/build-brief.pdf](docs/build-brief.pdf) for the full spec.

## Running locally

```
docker compose -f infra/docker-compose.yml up --build
```

This starts Postgres (with `db/migrations/0001_init.sql` applied on first init), the Python image-inspection service, the Go API + worker, and the web UI.

- Web UI: http://localhost:5173
- GraphQL API: http://localhost:8080/graphql
- Image service (private in deployment, exposed locally for debugging): http://localhost:8081

No `.env` file is required for local use — `infra/docker-compose.yml` bakes in working defaults. See `.env.example` for what each variable does if you want to override one.

## Seed fixtures

`evals/fixtures/` holds a small seed set (dev/held-out split by design, per `evals/scripts/generate_fixtures.py`) used to exercise the execution path during development. It grows through Day 2 and is expanded to the full 60-fixture evaluation set on Day 5.
