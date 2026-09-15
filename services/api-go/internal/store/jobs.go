package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

const jobColumns = `id, order_id, job_type, status, worker_id, lease_expires_at, attempt_count, last_error, input_asset_id, input_case_version, result::text, created_at, updated_at`

// CreateJob enqueues a job bound to a specific input asset and the order's
// case_version at creation time - the worker must act on exactly this asset,
// never "whatever is latest" when it happens to run, so a later upload can't
// silently change what an in-flight job inspects.
//
// At most one QUEUED/RUNNING job of a given type may exist per order
// (enforced by a partial unique index), so a repeated startResolution call
// while one is already in flight is a no-op: this returns (nil, nil) and the
// caller should look up the active job with GetActiveJob instead of treating
// it as failure.
func (s *Store) CreateJob(ctx context.Context, orderID, jobType, inputAssetID string, inputCaseVersion int) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO jobs (order_id, job_type, input_asset_id, input_case_version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (order_id, job_type) WHERE status IN ('QUEUED', 'RUNNING') DO NOTHING
		RETURNING `+jobColumns, orderID, jobType, inputAssetID, inputCaseVersion)
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
			&j.AttemptCount, &j.LastError, &j.InputAssetID, &j.InputCaseVersion, &j.Result, &j.CreatedAt, &j.UpdatedAt); err != nil {
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

// CompleteInspection commits the asset's decoded dimensions, the job's
// result and SUCCEEDED status, and the order's state advance in ONE
// transaction. This is deliberately not three separate calls: a SUCCEEDED
// status must never be reachable without the result actually being stored,
// and the order must never move forward on behalf of a worker that has lost
// its lease or whose view of the case (expectedCaseVersion) is stale.
//
// Returns ok=false (with a nil error) if either guard fails - the whole
// transaction rolls back, including the asset update. The caller should
// treat that as "this attempt is void" (see worker.runInspect), not retry
// the same write.
func (s *Store) CompleteInspection(
	ctx context.Context,
	jobID, workerID, assetID string,
	expectedCaseVersion int,
	result InspectionResult,
	artworkStatus, proofStatus string,
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

	orderTag, err := tx.Exec(ctx, `
		UPDATE orders SET artwork_status = $1, proof_status = $2, case_version = case_version + 1, updated_at = now()
		WHERE id = (SELECT order_id FROM jobs WHERE id = $3) AND case_version = $4
	`, artworkStatus, proofStatus, jobID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	if orderTag.RowsAffected() == 0 {
		return false, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.OrderID, &j.JobType, &j.Status, &j.WorkerID, &j.LeaseExpiresAt,
		&j.AttemptCount, &j.LastError, &j.InputAssetID, &j.InputCaseVersion, &j.Result, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}
