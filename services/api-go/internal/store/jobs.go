package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

const jobColumns = `id, order_id, job_type, status, worker_id, lease_expires_at, attempt_count, last_error, input_asset_id, input_case_version, idempotency_key, agent_source_job_id, result::text, created_at, updated_at`

// CreateJob enqueues a job bound to a specific input asset and the order's
// case_version at creation time - the worker must act on exactly this asset,
// never "whatever is latest" when it happens to run, so a later upload can't
// silently change what an in-flight job inspects. idempotencyKey is nil for
// inspect jobs; repair jobs carry the client-supplied key so the eventual
// CompleteRepair call can tie the repairs row back to it.
//
// At most one QUEUED/RUNNING job of a given type may exist per order
// (enforced by a partial unique index), so a repeated startResolution or
// requestRepair call while one is already in flight is a no-op: this
// returns (nil, nil) and the caller should look up the active job with
// GetActiveJob instead of treating it as failure.
func (s *Store) CreateJob(ctx context.Context, orderID, jobType, inputAssetID string, inputCaseVersion int, idempotencyKey *string) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO jobs (order_id, job_type, input_asset_id, input_case_version, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (order_id, job_type) WHERE status IN ('QUEUED', 'RUNNING') DO NOTHING
		RETURNING `+jobColumns, orderID, jobType, inputAssetID, inputCaseVersion, idempotencyKey)
	j, err := scanJob(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// GetActiveJob finds the current QUEUED/RUNNING job of a type for an order,
// if any.
func (s *Store) GetActiveJob(ctx context.Context, orderID, jobType string) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT `+jobColumns+`
		FROM jobs WHERE order_id = $1 AND job_type = $2 AND status IN ('QUEUED', 'RUNNING')
		ORDER BY created_at DESC LIMIT 1
	`, orderID, jobType)
	j, err := scanJob(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id)
	j, err := scanJob(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

func (s *Store) ListJobsForOrder(ctx context.Context, orderID string) ([]Job, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+jobColumns+` FROM jobs WHERE order_id = $1 ORDER BY created_at ASC`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.OrderID, &j.JobType, &j.Status, &j.WorkerID, &j.LeaseExpiresAt,
			&j.AttemptCount, &j.LastError, &j.InputAssetID, &j.InputCaseVersion, &j.IdempotencyKey, &j.AgentSourceJobID, &j.Result, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimNextJob atomically claims one QUEUED job, or one whose lease has
// expired, for workerID. This is a single UPDATE ... WHERE ... RETURNING
// guarded by FOR UPDATE SKIP LOCKED, not read-then-write, so two workers
// racing for the same row can never both succeed.
func (s *Store) ClaimNextJob(ctx context.Context, workerID string, leaseDuration time.Duration) (*Job, error) {
	leaseSeconds := leaseDuration.Seconds()
	row := s.Pool.QueryRow(ctx, `
		UPDATE jobs SET
			status = 'RUNNING',
			worker_id = $1,
			lease_expires_at = now() + ($2 * interval '1 second'),
			attempt_count = attempt_count + 1,
			updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'QUEUED'
			   OR (status = 'RUNNING' AND lease_expires_at < now())
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobColumns, workerID, leaseSeconds)
	j, err := scanJob(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// FailJob requires the caller to still hold the lease (worker_id +
// status='RUNNING') - if the lease was reclaimed by another worker in the
// meantime, this is a no-op (ok=false) instead of clobbering the reclaiming
// worker's progress.
func (s *Store) FailJob(ctx context.Context, jobID, workerID, errMsg string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE jobs SET status = 'FAILED', last_error = $3, updated_at = now()
		WHERE id = $1 AND worker_id = $2 AND status = 'RUNNING'
	`, jobID, workerID, errMsg)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

type InspectionResult struct {
	WidthPx  int    `json:"width_px"`
	HeightPx int    `json:"height_px"`
	Mode     string `json:"mode"`
	Format   string `json:"format"`
}

// FindingInput is one check's result, ready to persist. Evidence is a JSON
// text blob (already serialized by the caller, which received it as JSON
// from services/image-python) rather than a Go struct, since api-go does not
// need to interpret it - it just stores and later returns what the rules
// engine reported.
type FindingInput struct {
	CheckName   string
	Result      string
	Evidence    string
	RuleVersion string
}

// CompleteInspection commits the asset's decoded dimensions, the job's
// result and SUCCEEDED status, the findings from each check, the order's
// state advance, and - when enqueueAgentDecision or enqueuePrepareProof is
// true - a follow-up job, ALL in ONE transaction. This is deliberately not
// several separate calls: a SUCCEEDED status must never be reachable
// without its result and findings actually being stored, the order must
// never move forward on behalf of a worker that has lost its lease or whose
// view of the case (expectedCaseVersion) is stale, and (the reason this
// enqueues its own follow-up job rather than leaving that to the caller) a
// crash between "inspection committed" and "next step invoked" must never
// leave the case stuck with no queued work to resume it.
//
// The two follow-up flags are mutually exclusive by construction (the
// caller computes enqueueAgentDecision only when artworkStatus != RESOLVED,
// and enqueuePrepareProof only when it IS RESOLVED) but are passed
// separately rather than as one enum, matching CompleteAgentDecision's own
// job-type-specific inserts.
//
// The agent_decide follow-up is bound via agent_source_job_id to jobID
// itself (THIS inspection), so whatever reads it later acts on exactly
// these findings - never "the latest finding per check across the order's
// entire history".
//
// Returns ok=false (with a nil error) if either guard fails - the whole
// transaction rolls back, including the asset update, findings inserts, and
// any follow-up job insert. The caller should treat that as "this attempt
// is void" (see worker.runInspect), not retry the same write.
func (s *Store) CompleteInspection(
	ctx context.Context,
	jobID, workerID, assetID string,
	expectedCaseVersion int,
	result InspectionResult,
	findings []FindingInput,
	artworkStatus, proofStatus string,
	enqueueAgentDecision, enqueuePrepareProof bool,
) (bool, error) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return false, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	if _, err := tx.Exec(ctx, `
		UPDATE assets SET width_px = $1, height_px = $2 WHERE id = $3
	`, result.WidthPx, result.HeightPx, assetID); err != nil {
		return false, err
	}

	jobTag, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'SUCCEEDED', result = $1::jsonb, updated_at = now()
		WHERE id = $2 AND worker_id = $3 AND status = 'RUNNING' AND lease_expires_at > now()
	`, string(resultJSON), jobID, workerID)
	if err != nil {
		return false, err
	}
	if jobTag.RowsAffected() == 0 {
		return false, nil
	}

	orderID, err := orderIDForJob(ctx, tx, jobID)
	if err != nil {
		return false, err
	}

	for _, f := range findings {
		if _, err := tx.Exec(ctx, `
			INSERT INTO findings (order_id, job_id, check_name, result, evidence, rule_version)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		`, orderID, jobID, f.CheckName, f.Result, f.Evidence, f.RuleVersion); err != nil {
			return false, err
		}
	}

	orderTag, err := tx.Exec(ctx, `
		UPDATE orders SET artwork_status = $1, proof_status = $2, case_version = case_version + 1, updated_at = now()
		WHERE id = $3 AND case_version = $4
	`, artworkStatus, proofStatus, orderID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	if orderTag.RowsAffected() == 0 {
		return false, nil
	}

	if enqueueAgentDecision {
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (order_id, job_type, input_asset_id, input_case_version, agent_source_job_id)
			VALUES ($1, 'agent_decide', $2, $3, $4)
			ON CONFLICT (order_id, job_type) WHERE status IN ('QUEUED', 'RUNNING') DO NOTHING
		`, orderID, assetID, expectedCaseVersion+1, jobID); err != nil {
			return false, err
		}
	}

	if enqueuePrepareProof {
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (order_id, job_type, input_asset_id, input_case_version)
			VALUES ($1, 'prepare_proof', $2, $3)
			ON CONFLICT (order_id, job_type) WHERE status IN ('QUEUED', 'RUNNING') DO NOTHING
		`, orderID, assetID, expectedCaseVersion+1); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func orderIDForJob(ctx context.Context, tx pgx.Tx, jobID string) (string, error) {
	var orderID string
	if err := tx.QueryRow(ctx, `SELECT order_id FROM jobs WHERE id = $1`, jobID).Scan(&orderID); err != nil {
		return "", err
	}
	return orderID, nil
}

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.OrderID, &j.JobType, &j.Status, &j.WorkerID, &j.LeaseExpiresAt,
		&j.AttemptCount, &j.LastError, &j.InputAssetID, &j.InputCaseVersion, &j.IdempotencyKey, &j.AgentSourceJobID, &j.Result, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}
