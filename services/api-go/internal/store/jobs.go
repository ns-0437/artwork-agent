package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateJob(ctx context.Context, orderID, jobType string) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO jobs (order_id, job_type) VALUES ($1, $2)
		RETURNING id, order_id, job_type, status, worker_id, lease_expires_at, attempt_count, last_error, created_at, updated_at
	`, orderID, jobType)
	return scanJob(row)
}

func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, order_id, job_type, status, worker_id, lease_expires_at, attempt_count, last_error, created_at, updated_at
		FROM jobs WHERE id = $1
	`, id)
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
	rows, err := s.Pool.Query(ctx, `
		SELECT id, order_id, job_type, status, worker_id, lease_expires_at, attempt_count, last_error, created_at, updated_at
		FROM jobs WHERE order_id = $1 ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.OrderID, &j.JobType, &j.Status, &j.WorkerID, &j.LeaseExpiresAt,
			&j.AttemptCount, &j.LastError, &j.CreatedAt, &j.UpdatedAt); err != nil {
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
		RETURNING id, order_id, job_type, status, worker_id, lease_expires_at, attempt_count, last_error, created_at, updated_at
	`, workerID, leaseSeconds)
	j, err := scanJob(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// CompleteJob and FailJob both require the caller to still hold the lease
// (worker_id + status='RUNNING') - if the lease was reclaimed by another
// worker in the meantime, these become no-ops (ok=false) instead of
// clobbering the reclaiming worker's progress.
func (s *Store) CompleteJob(ctx context.Context, jobID, workerID string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE jobs SET status = 'SUCCEEDED', updated_at = now()
		WHERE id = $1 AND worker_id = $2 AND status = 'RUNNING'
	`, jobID, workerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

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

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.OrderID, &j.JobType, &j.Status, &j.WorkerID, &j.LeaseExpiresAt,
		&j.AttemptCount, &j.LastError, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}
