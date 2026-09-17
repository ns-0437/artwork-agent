# Artwork Exception Resolution Agent

Resolves an artwork blocker on a simulated sticker order: inspect the file, clarify customer intent when needed, perform one eligible repair, verify it, prepare a proof, and persist the case so work survives a crash or a delayed reply.

**Independent portfolio prototype.** No access to any real company's production pipeline, customer files, printer profiles, or operational metrics. Makes no claim of measured savings or private integration.

## Live demo

**[ns-0437.github.io/artwork-agent](https://ns-0437.github.io/artwork-agent/)** - the real stack, not a mock: static frontend on GitHub Pages, `api-go` + `worker` + `image-python` on Cloud Run (`asia-south1`, project `artwork-agent-demo`), Postgres on a free-tier Neon instance, artifacts in a GCS bucket with a 7-day deletion lifecycle.

Deployed revision: commit [`2abca30`](https://github.com/ns-0437/artwork-agent/commit/2abca30fe24d58b7808d29a85f3d17922ed0ebad) (`master`, 2026-09-17) - `artwork-agent-api-00006-9f2`, `artwork-agent-worker-00005-9c5`, `artwork-agent-image-python-00004-xnm` on Cloud Run. Verified against the live deployment, not just locally: an unauthenticated request to `image-python` returns `403`; the full upload -> inspect -> repair -> proof loop was driven end to end through the deployed frontend and reached `RESOLVED` / `AWAITING_CUSTOMER_APPROVAL`. The provider-error-vs-model-escalation distinction (see below) was verified separately: a local integration test against the real Groq API with a deliberately invalid key (a genuine 401 from Groq, not a stubbed/mocked response), run against the local stack rather than the live deployment - see `evals/scripts/run_eval.py`'s `no_provider_error` check.

`image-python`'s "private Python" requirement (per the brief) is enforced by IAM alone (`roles/run.invoker` granted only to the calling service's own service account, never `allUsers`/`allAuthenticatedUsers`) - not by network ingress. `ingress: internal` was tried first and reverted: Cloud Run only treats a caller as "internal" if it's attached to a Serverless VPC connector or uses Direct VPC egress, neither of which is set up here, so a sibling Cloud Run service's call left over the public internet and got rejected identically to an outside caller. `ingress: all` + IAM-only auth was the chosen tradeoff for a portfolio demo's cost/complexity budget: it avoids needing a VPC connector. That is a narrower claim than "free" - Cloud Run's own compute/request billing, and any connector's cost if one is added later, are separate from this tradeoff and weren't estimated here.

**Public, unauthenticated, read-only by default.** There's no ownership check on orders (known gap, below), so the live deployment sets `READ_ONLY_DEMO=true`: every GraphQL mutation is rejected server-side (`graph.Resolver.guarded`, checked before any of the 7 mutations' own logic runs), with reads left open - no credentials in the frontend bundle, nothing for the client to bypass. This is stronger than the order-count cap (`MAX_DEMO_ORDERS=150`) alone, which only limits *creating new* orders and does nothing to stop repeated writes against an *existing* one. `maxScale=5` on every Cloud Run service caps concurrent instances, not total spend - it's a parallelism limit, not a cost ceiling. The recorded demo shows the actual write flow (repair -> proof, escalation) against a locally-run stack, or the live deployment with `READ_ONLY_DEMO` temporarily unset for that recording.

Two things the demo intentionally doesn't do: this is a single-instance walkthrough, not a load-bearing service - don't expect it to stay up under sustained traffic; and the worker's `agent_decide` step calls a live Groq model, so repeated runs against the same synthetic test image can land on `request_repair` or `escalate` inconsistently (the same non-determinism the evaluation harness's methodology section already documents), which is expected model behavior, not a deployment fault.

## Results, in one table

34 fixtures (16 dev, 18 held-out), driven through the real stack end to end, three ways. Full methodology and honest boundaries: [docs/case_study.md](docs/case_study.md).

| | agent (Groq) | rules only, no agent | rules + deterministic script |
|---|---:|---:|---:|
| evaluation cases passed | 34/34 | n/a | n/a |
| orders resolved | 18/34 | 12/34 | 18/34 |
| orders escalated (`NEEDS_REVIEW`) | 16/34 | 14/34 | 16/34 |
| orders left stuck | 0 | 8 | 0 |
| provider tokens used | 19,650 | 0 | 0 |

**"Passed" and "resolved" are different claims** - a correctly-escalated case passes without the order ever resolving. **Once the script had the same escalation capability the agent has, it matched the agent exactly on all 34 fixtures.** This project does not claim the LLM improved resolution over a well-designed deterministic workflow - for this fixture set, it didn't. A separate, smaller check on 4 held-out fixtures reserved specifically for a genuinely blind pass (never run until the comparison logic above was frozen) also passed 4/4, 2/4 resolved - a small check (n=4), not broad proof.

> *"I built and deployed a durable artwork exception-resolution workflow. My evaluation found that a deterministic script matched the agent on the tested cases, so I would favor the scripted path for this scope while retaining the agent version for further evaluation."*

Frozen evaluation commit (tagged for reproducibility): `git checkout eval-frozen-day5`. Raw results: `evals/results/*.json`. What changed and why between runs: `evals/CHANGES.md`.

## Demo

[docs/demo_script.md](docs/demo_script.md) is a three-minute walkthrough script (clarification → verified repair → proof → escalation), matching the build brief's demo structure. Every beat in it was rehearsed live against the running stack immediately before being written down - not simulated from memory.

## Running locally

```bash
docker compose -f infra/docker-compose.yml up --build
```

This starts Postgres (migrations applied automatically on first init), the Python image-inspection service, the Go API + worker, and the web UI. Updating from an older checkout with a schema mismatch: `docker compose -f infra/docker-compose.yml down -v` first (Postgres only applies migrations to a fresh volume).

- Web UI: http://localhost:5173
- GraphQL API: http://localhost:8080/graphql
- Image service (private in deployment, exposed locally for debugging): http://localhost:8081

No `.env` file is required for local use - `infra/docker-compose.yml` bakes in working defaults. See `.env.example` for what each variable does.

## Screenshots

Not included in this repository - the agent tooling used to build and verify this project could drive the UI and confirm behavior via its text output, but could not reliably capture screen images in this session. The demo script above documents the exact UI states (clarification prompt, job list, findings, final status) a live run produces. The [live demo](#live-demo) above is the fastest way to see it directly; `docker compose up` (below) reproduces it locally in under five minutes.

## Provider note

The active decision-making provider is **Groq**, not Claude and not xAI's Grok - the Anthropic account available for this build had no usable credits, so Groq was substituted as a documented, honest stand-in. Groq and Grok are unrelated companies with similar-sounding names. The provider is a single, swappable interface (`internal/agent.Provider`); trying a different model means implementing that interface again.

## What's prepared but not executed

- **An actual process-kill test.** Crash recovery is verified against hand-reproduced DB/storage states a crash would leave, not an actual killed process - see `CLAUDE.md` points 21 and 48.
- **Cost-per-case in dollars, and a Grok/Claude comparison.** Token counts are reported; a dollar figure and the brief's optional model comparison are out of scope for this pass.

## Known, tracked limitations

- No authentication or ownership check on orders (`ownerId` is a free-text, unverified client field) - dev ports are bound to `127.0.0.1` specifically because of this. The live deployment sets `READ_ONLY_DEMO=true` (all mutations rejected server-side) rather than leaving this open - a read-only boundary, not a substitute for real per-owner access control, which remains a genuine known gap.
- Upload tickets are replayable until they expire (10 minutes) rather than enforced single-use.

## More detail

- [docs/case_study.md](docs/case_study.md) - the full project narrative and evaluation writeup.
- [docs/architecture.md](docs/architecture.md) - day-by-day technical build log.
- [CLAUDE.md](CLAUDE.md) - the complete list of load-bearing constraints and the bugs each one fixed.
- [docs/build-brief.pdf](docs/build-brief.pdf) - the six-day spec this project was built against.
