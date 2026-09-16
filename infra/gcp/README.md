# GCP deployment (prepared, not executed)

Per the brief's own fallback ("If cloud setup blocks delivery, provide local containers and a recording, labeling GCP incomplete") this deployment has been **prepared but not run against a real GCP project** in this build. Everything below is ready to execute once a project is available; nothing here has created real cloud resources or incurred cost.

## What's here

- `api-go-service.yaml` - the public GraphQL API + upload endpoint (`services/api-go/cmd/server`), Cloud Run service, public ingress.
- `image-python-service.yaml` - the image inspection/repair/proof service, Cloud Run service, **internal-only ingress** (the brief's "private Python").
- `worker-service.yaml` - the poll/claim/dispatch worker (`services/api-go/cmd/worker`), deployed as a Cloud Run service with `minScale=1`/`maxScale=1` (exactly one instance - see CLAUDE.md's lease-based single-claim design). **Not** a true Cloud Run Job yet - see the caveat in that file. The worker's current code is a long-running poll loop; Cloud Run Jobs run to completion and exit, which is the brief's suggested shape but would need `worker.Worker.Run` restructured to process one batch and return (triggered by Cloud Scheduler or Eventarc) - real code change, deliberately not done in this deployment-config pass.

## Prerequisites (none of this has been run)

```bash
# 1. Cloud SQL (Postgres) instance
gcloud sql instances create artwork-agent-db \
  --database-version=POSTGRES_16 --tier=db-f1-micro --region=REGION
gcloud sql databases create artwork_agent --instance=artwork-agent-db
# Apply db/migrations/*.sql in order against this instance before first deploy.

# 2. Cloud Storage bucket (replaces LocalDisk - see internal/storage/gcs.go)
gsutil mb -l REGION gs://PROJECT_ID-artwork-agent-artifacts
# 7-day object lifecycle per the brief's "seven-day deletion" security essential:
gsutil lifecycle set - gs://PROJECT_ID-artwork-agent-artifacts <<'EOF'
{"rule": [{"action": {"type": "Delete"}, "condition": {"age": 7}}]}
EOF

# 3. Secret Manager secrets (never baked into images or committed)
echo -n "postgres://USER:PASS@/artwork_agent?host=/cloudsql/PROJECT_ID:REGION:artwork-agent-db" | \
  gcloud secrets create artwork-agent-database-url --data-file=-
openssl rand -base64 32 | gcloud secrets create artwork-agent-upload-signing-secret --data-file=-
echo -n "YOUR_GROQ_KEY" | gcloud secrets create artwork-agent-groq-api-key --data-file=-

# 4. Artifact Registry + build/push images
gcloud artifacts repositories create artwork-agent --repository-format=docker --location=REGION
gcloud builds submit services/api-go --tag REGION-docker.pkg.dev/PROJECT_ID/artwork-agent/api-go
gcloud builds submit services/api-go --tag REGION-docker.pkg.dev/PROJECT_ID/artwork-agent/worker
gcloud builds submit services/image-python --tag REGION-docker.pkg.dev/PROJECT_ID/artwork-agent/image-python

# 5. Deploy (after substituting PROJECT_ID/REGION in the YAML files above)
gcloud run services replace infra/gcp/image-python-service.yaml --region=REGION
gcloud run services replace infra/gcp/api-go-service.yaml --region=REGION
gcloud run services replace infra/gcp/worker-service.yaml --region=REGION
```

## Known gaps in this config (honest, not hidden)

- **`API_BASE_URL` is circular on first deploy** - Cloud Run assigns the URL at deploy time, so `api-go-service.yaml`'s `API_BASE_URL` needs a placeholder-then-redeploy pass (deploy once, read the assigned URL, redeploy with it set correctly) - this affects the upload-ticket URLs `createUpload` returns.
- **The worker isn't a real Cloud Run Job** - see `worker-service.yaml`'s comment. Deployed as written, it works (a persistent service, one instance), but doesn't match the brief's suggested "Cloud Run Job for queued work" shape without further refactoring `worker.Worker.Run`.
- **No health/readiness endpoint on the worker** - `cmd/worker` has no HTTP server at all, but Cloud Run services expect one to determine revision health. A minimal `/healthz` listener (or Cloud Run's startup probe override) is needed before this actually deploys cleanly - flagged, not silently worked around.
- **Owner/authentication is still absent** (CLAUDE.md's known gap) - deploying this publicly means `ownerId` remains a client-supplied, unverified field. Not something this deployment pass fixes.
- **Untested against a real GCS bucket** - `internal/storage/gcs.go` builds, vets, and matches the `Storage` interface `LocalDisk` already satisfies, but has not been run against a live bucket (would need real GCP credentials this build doesn't have). Its content-addressing and idempotent-write logic mirror `LocalDisk`'s exactly, but "compiles and type-checks" and "verified against a live bucket" are different claims - only the former is true right now.
