# Artwork Exception Resolution Agent

Resolves an artwork blocker on a simulated sticker order: inspect the file, clarify customer intent when needed, perform one eligible repair, verify it, prepare a proof, and persist the case so work survives a crash or a delayed reply.

**Independent portfolio prototype.** No access to any real company's production pipeline, customer files, printer profiles, or operational metrics. Makes no claim of measured savings or private integration.

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

> *"I built and evaluated an artwork exception-resolution workflow. The agent matched a deterministic implementation on the tested cases, so I would favor the scripted path for this scope while retaining the agent version for further evaluation."*

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

Not included in this repository - the agent tooling used to build and verify this project could drive the UI and confirm behavior via its text output, but could not reliably capture screen images in this session. The demo script above documents the exact UI states (clarification prompt, job list, findings, final status) a live run produces; running `docker compose up` and following it takes under five minutes.

## Provider note

The active decision-making provider is **Groq**, not Claude and not xAI's Grok - the Anthropic account available for this build had no usable credits, so Groq was substituted as a documented, honest stand-in. Groq and Grok are unrelated companies with similar-sounding names. The provider is a single, swappable interface (`internal/agent.Provider`); trying a different model means implementing that interface again.

## What's prepared but not executed

- **Cloud deployment.** Cloud Run configs (`infra/gcp/`) and a GCS storage backend are written and pass `go test`, but neither has been run against a live GCP project.
- **An actual process-kill test.** Crash recovery is verified against hand-reproduced DB/storage states a crash would leave, not an actual killed process - see `CLAUDE.md` points 21 and 48.
- **Cost-per-case in dollars, and a Grok/Claude comparison.** Token counts are reported; a dollar figure and the brief's optional model comparison are out of scope for this pass.

## Known, tracked limitations

- No authentication or ownership check on orders (`ownerId` is a free-text, unverified client field) - dev ports are bound to `127.0.0.1` specifically because of this.
- Upload tickets are replayable until they expire (10 minutes) rather than enforced single-use.

## More detail

- [docs/case_study.md](docs/case_study.md) - the full project narrative and evaluation writeup.
- [docs/architecture.md](docs/architecture.md) - day-by-day technical build log.
- [CLAUDE.md](CLAUDE.md) - the complete list of load-bearing constraints and the bugs each one fixed.
- [docs/build-brief.pdf](docs/build-brief.pdf) - the six-day spec this project was built against.
