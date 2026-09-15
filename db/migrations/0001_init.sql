-- Order/case/job schema. Status semantics frozen here per the build brief:
--   artwork_status:    BLOCKED -> AWAITING_CLARIFICATION -> BLOCKED (after reply) -> RESOLVED
--                       (or -> NEEDS_REVIEW for unsafe/unsupported cases)
--   proof_status:      NOT_PREPARED -> AWAITING_CUSTOMER_APPROVAL (system-prepared, not customer action)
--   production_status: NOT_RELEASED (never advances past this in v1)
--   job.status:        QUEUED -> RUNNING -> SUCCEEDED | FAILED

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE orders (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            TEXT NOT NULL,
    product_type        TEXT NOT NULL,
    declared_width      NUMERIC NOT NULL,
    declared_height     NUMERIC NOT NULL,
    declared_unit       TEXT NOT NULL CHECK (declared_unit IN ('in', 'mm')),
    customer_request    TEXT,
    artwork_version     INTEGER NOT NULL DEFAULT 1,
    intent              TEXT CHECK (intent IN ('border', 'full_bleed')),
    trim_x_px           NUMERIC,
    trim_y_px           NUMERIC,
    trim_width_px       NUMERIC,
    trim_height_px      NUMERIC,
    case_version        INTEGER NOT NULL DEFAULT 1,

    artwork_status      TEXT NOT NULL DEFAULT 'BLOCKED'
                            CHECK (artwork_status IN
                                ('BLOCKED', 'AWAITING_CLARIFICATION', 'RESOLVED', 'NEEDS_REVIEW')),
    proof_status        TEXT NOT NULL DEFAULT 'NOT_PREPARED'
                            CHECK (proof_status IN ('NOT_PREPARED', 'AWAITING_CUSTOMER_APPROVAL')),
    production_status   TEXT NOT NULL DEFAULT 'NOT_RELEASED'
                            CHECK (production_status IN ('NOT_RELEASED')),

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE assets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES orders(id),
    kind            TEXT NOT NULL CHECK (kind IN ('original', 'repaired', 'preview')),
    storage_key     TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    width_px        INTEGER,
    height_px       INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_assets_order_id ON assets(order_id);

CREATE TABLE jobs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id            UUID NOT NULL REFERENCES orders(id),
    job_type            TEXT NOT NULL CHECK (job_type IN ('inspect', 'repair')),
    status              TEXT NOT NULL DEFAULT 'QUEUED'
                            CHECK (status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
    worker_id           TEXT,
    lease_expires_at    TIMESTAMPTZ,
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_jobs_order_id ON jobs(order_id);
CREATE INDEX idx_jobs_status_queued ON jobs(status) WHERE status = 'QUEUED';

CREATE TABLE findings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES orders(id),
    job_id          UUID REFERENCES jobs(id),
    check_name      TEXT NOT NULL CHECK (check_name IN ('resolution', 'color', 'bleed')),
    result          TEXT NOT NULL CHECK (result IN ('PASS', 'WARNING', 'NEEDS_INPUT', 'NEEDS_REVIEW')),
    evidence        JSONB NOT NULL,
    rule_version    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_findings_order_id ON findings(order_id);

CREATE TABLE clarifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES orders(id),
    question        TEXT NOT NULL,
    answer          TEXT,
    answered_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_clarifications_order_id ON clarifications(order_id);

CREATE TABLE repairs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id            UUID NOT NULL REFERENCES orders(id),
    idempotency_key     TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('PENDING', 'REPAIRED', 'REJECTED')),
    reason              TEXT,
    source_asset_id     UUID REFERENCES assets(id),
    derived_asset_id    UUID REFERENCES assets(id),
    source_hash         TEXT,
    derived_hash        TEXT,
    diagnosis           JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Prevents duplicate repairs on retry: a retried mutation with the same
    -- idempotency_key hits this constraint instead of racing an insert.
    UNIQUE (order_id, idempotency_key)
);

CREATE TABLE tool_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES orders(id),
    job_id          UUID REFERENCES jobs(id),
    event_type      TEXT NOT NULL CHECK (event_type IN ('tool_call', 'retry')),
    detail          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tool_events_order_id ON tool_events(order_id);
