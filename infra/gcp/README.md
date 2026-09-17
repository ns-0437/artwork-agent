# GCP deployment (live)

This has been deployed and verified against a real GCP project - see the
README's "Live demo" section for the URL, the deployed commit, and what
was checked. Everything below reflects what was actually done, not a plan.

## What's here

- `api-go-service.yaml` - the public GraphQL API + upload endpoint (`services/api-go/cmd/server`), Cloud Run service, public ingress.
- `image-python-service.yaml` - the image inspection/repair/proof service, Cloud Run service. IAM-gated (`roles/run.invoker` granted only to the calling service's own service account, never `allUsers`), **not** network-isolated - `ingress: internal` was tried first and reverted because Cloud Run only treats a caller as "internal" if it's on a Serverless VPC connector or uses Direct VPC egress, neither of which is set up here; see the file's own comment for the full reasoning.
- `worker-service.yaml` - the poll/claim/dispatch worker (`services/api-go/cmd/worker`), deployed as a Cloud Run service with `minScale=1`/`maxScale=1` (exactly one instance - see CLAUDE.md's lease-based single-claim design), overriding the shared image's default entrypoint to run `/app/worker` instead of `/app/server`. **Not** a true Cloud Run Job - it's still a long-running poll loop; Cloud Run Jobs are for finite work that exits, which would need `worker.Worker.Run` restructured to process one batch and return (triggered by Cloud Scheduler or Eventarc) - real code change, not done here.

## What was actually provisioned

- **Postgres**: a free-tier hosted Neon instance, not Cloud SQL - chosen to avoid Cloud SQL's ~$10-15/month minimum for a portfolio demo. `db/migrations/*.sql` applied against it directly.
- **Storage**: a GCS bucket (`artwork-agent-demo-artifacts`) with a 7-day object deletion lifecycle, matching the brief's "seven-day deletion" security essential.
- **Secrets**: `artwork-agent-database-url`, `artwork-agent-upload-signing-secret`, `artwork-agent-groq-api-key` in Secret Manager, each with `roles/secretmanager.secretAccessor` granted only to the project's default compute service account (the one api-go/worker actually run as) - not broader.
- **Images**: built via `gcloud builds submit` and pushed to an Artifact Registry repo (`artwork-agent`, `asia-south1`). `api-go` and `worker` share one image (two binaries, selected by `command:` in each service's YAML) - `services/api-go/Dockerfile` builds both.

## Gaps found only by actually deploying (fixed here, not hidden)

- **`gcloud run services replace` doesn't re-resolve a `:latest` tag if the YAML text is unchanged.** Pushing a new image under the same tag and re-running `services replace` silently kept serving the OLD revision/digest - confirmed by comparing `gcloud run revisions describe ... --format="value(spec.containers[0].image)"` against the freshly pushed digest. Forcing a new revision needs a genuine spec change (e.g. `gcloud run services update --update-env-vars=...`, then reapplying the clean YAML once more to remove that env var again).
- **`ingress: internal` rejects same-project Cloud Run callers too**, unless they're on a Serverless VPC connector or use Direct VPC egress - neither configured here, so switching `image-python` to `ingress: all` (IAM-only auth) was the deploy-time fix; see that file's comment.
- **`worker-service.yaml` never overrode the shared image's default `CMD`** (`/app/server`) - fixed by adding `command: ["/app/worker"]`.
- **`cmd/worker` had no HTTP listener at all**, but Cloud Run needs some port to consider a revision healthy - fixed with a minimal `/healthz` endpoint in `cmd/worker/main.go`.
- **A `grep | cut | gcloud secrets create --data-file=-` pipeline left a trailing newline in the Groq secret**, which Go's `net/http` rejected as an invalid header value at request time - every agent decision silently escalated instead of erroring loudly, indistinguishable from the model genuinely choosing to escalate until server logs were read directly. Fixed at both ends: the secret was recreated without the trailing newline, and `agent.NewGroqAdapter` now trims and validates the key at construction so this class of bug can't reach an HTTP request again (see `services/api-go/internal/agent/groq_adapter_test.go`).
- **`api-go`'s outbound calls to `image-python` needed a Cloud Run identity token** once `image-python` stopped being publicly reachable - `pyclient.go` now fetches one from the metadata server, gated on `K_SERVICE` being set so local docker-compose (no metadata server, no auth) is unaffected.

## Known, still-open gaps

- **The worker isn't a real Cloud Run Job** - see `worker-service.yaml`'s comment.
- **Owner/authentication is still absent** (CLAUDE.md's known gap) - `ownerId` remains a client-supplied, unverified field.
- **This is a single-instance portfolio demo**, not a load-bearing service - it isn't designed or expected to hold up under sustained traffic.
