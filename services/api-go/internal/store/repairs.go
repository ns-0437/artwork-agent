package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

const repairColumns = `id, order_id, idempotency_key, status, reason, source_asset_id, derived_asset_id, source_hash, derived_hash, diagnosis::text, job_id, created_at`

func scanRepair(row pgx.Row) (*Repair, error) {
	var rp Repair
	err := row.Scan(&rp.ID, &rp.OrderID, &rp.IdempotencyKey, &rp.Status, &rp.Reason,
		&rp.SourceAssetID, &rp.DerivedAssetID, &rp.SourceHash, &rp.DerivedHash, &rp.Diagnosis, &rp.JobID, &rp.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &rp, nil
}

// GetRepairByIdempotencyKey finds a previously completed repair for this
// order+key, if any - used to make requestRepair idempotent across retries
// that arrive AFTER a repair job has already finished (the in-flight case is
// separately covered by the jobs partial unique index).
func (s *Store) GetRepairByIdempotencyKey(ctx context.Context, orderID, idempotencyKey string) (*Repair, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT `+repairColumns+` FROM repairs WHERE order_id = $1 AND idempotency_key = $2
	`, orderID, idempotencyKey)
	rp, err := scanRepair(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return rp, nil
}

// ListRepairsForOrder returns every repair attempt for an order, oldest
// first.
func (s *Store) ListRepairsForOrder(ctx context.Context, orderID string) ([]Repair, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+repairColumns+` FROM repairs WHERE order_id = $1 ORDER BY created_at ASC`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Repair
	for rows.Next() {
		var rp Repair
		if err := rows.Scan(&rp.ID, &rp.OrderID, &rp.IdempotencyKey, &rp.Status, &rp.Reason,
			&rp.SourceAssetID, &rp.DerivedAssetID, &rp.SourceHash, &rp.DerivedHash, &rp.Diagnosis, &rp.JobID, &rp.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, rows.Err()
}

// RepairEligibleOutcome is what CompleteRepair needs to persist a
// successful, verified repair: the new assets' bytes are already stored by
// the caller (storage writes aren't transactional with Postgres, so they
// happen first - see worker.runRepair) - this only records the resulting
// rows.
type RepairAssetInput struct {
	StorageKey string
	SHA256     string
	WidthPx    int
	HeightPx   int
}

// RepairOutcome is the full result of one repair attempt, ready to persist.
type RepairOutcome struct {
	Repaired bool
	Reason   string // populated when !Repaired, or as an audit note when Repaired

	SourceAssetID string
	SourceHash    string

	// Only meaningful when Repaired is true:
	DerivedAsset RepairAssetInput
	PreviewAsset RepairAssetInput
	TrimXPx      int
	TrimYPx      int
	TrimWidthPx  int
	TrimHeightPx int
	Diagnosis    string // JSON-encoded
}

// CompleteRepair commits the job's SUCCEEDED result, the repairs row
// (REPAIRED or REJECTED, keyed by the job's idempotency_key so a retry with
// the same key can never duplicate it), and - only when the repair was
// eligible and verified - the new 'repaired'/'preview' assets, the order's
// now-known trim coordinates, fresh findings from the post-repair recheck,
// the order's state advance, and (when enqueuePrepareProof is true) a
// follow-up prepare_proof job bound to the new repaired asset, ALL in one
// transaction. An ineligible or unverified repair leaves the order at
// NEEDS_REVIEW and the original completely untouched.
//
// Returns ok=false (nil error) if the worker's lease is gone, the case
// moved past expectedCaseVersion, or (defensively) the idempotency key was
// somehow already recorded by another completed attempt - the caller
// treats that as "this attempt is void", same as CompleteInspection.
func (s *Store) CompleteRepair(
	ctx context.Context,
	jobID, workerID string,
	expectedCaseVersion int,
	outcome RepairOutcome,
	findings []FindingInput,
	artworkStatus, proofStatus string,
	enqueuePrepareProof bool,
) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	var idempotencyKey *string
	if err := tx.QueryRow(ctx, `SELECT idempotency_key FROM jobs WHERE id = $1`, jobID).Scan(&idempotencyKey); err != nil {
		return false, err
	}
	if idempotencyKey == nil {
		return false, errNoIdempotencyKey
	}

	resultJSON, err := json.Marshal(map[string]interface{}{"repaired": outcome.Repaired, "reason": outcome.Reason})
	if err != nil {
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

	repairStatus := "REJECTED"
	if outcome.Repaired {
		repairStatus = "REPAIRED"
	}

	var derivedAssetID *string
	if outcome.Repaired {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO assets (order_id, kind, storage_key, sha256, content_type, width_px, height_px)
			VALUES ($1, 'repaired', $2, $3, 'image/png', $4, $5) RETURNING id
		`, orderID, outcome.DerivedAsset.StorageKey, outcome.DerivedAsset.SHA256, outcome.DerivedAsset.WidthPx, outcome.DerivedAsset.HeightPx).Scan(&id); err != nil {
			return false, err
		}
		derivedAssetID = &id

		// The preview is recorded as an asset (kind='preview') for the
		// audit trail - the repairs row itself only references the
		// repaired file, not the preview, matching the existing schema.
		if _, err := tx.Exec(ctx, `
			INSERT INTO assets (order_id, kind, storage_key, sha256, content_type, width_px, height_px)
			VALUES ($1, 'preview', $2, $3, 'image/png', $4, $5)
		`, orderID, outcome.PreviewAsset.StorageKey, outcome.PreviewAsset.SHA256, outcome.PreviewAsset.WidthPx, outcome.PreviewAsset.HeightPx); err != nil {
			return false, err
		}
	}

	repairTag, err := tx.Exec(ctx, `
		INSERT INTO repairs (order_id, idempotency_key, status, reason, source_asset_id, derived_asset_id, source_hash, derived_hash, diagnosis, job_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::jsonb, $10)
		ON CONFLICT (order_id, idempotency_key) DO NOTHING
	`, orderID, *idempotencyKey, repairStatus, nullableString(outcome.Reason), outcome.SourceAssetID, derivedAssetID,
		outcome.SourceHash, nullableString(outcome.DerivedAsset.SHA256), outcome.Diagnosis, jobID)
	if err != nil {
		return false, err
	}
	if repairTag.RowsAffected() == 0 {
		// Defensive: the partial job index should prevent a concurrent
		// repair job for this order, so this should be unreachable in
		// practice. Don't double-apply side effects if it ever happens.
		return false, nil
	}

	if outcome.Repaired {
		for _, f := range findings {
			if _, err := tx.Exec(ctx, `
				INSERT INTO findings (order_id, job_id, check_name, result, evidence, rule_version)
				VALUES ($1, $2, $3, $4, $5::jsonb, $6)
			`, orderID, jobID, f.CheckName, f.Result, f.Evidence, f.RuleVersion); err != nil {
				return false, err
			}
		}

		// current_asset_id moves to the repaired canvas - a later
		// startResolution (or future proof generation) must act on this,
		// never fall back to the pristine original, or it could
		// reintroduce a blocker (e.g. missing bleed) this repair cleared.
		orderTag, err := tx.Exec(ctx, `
			UPDATE orders SET
				artwork_status = $1, proof_status = $2, case_version = case_version + 1,
				current_asset_id = $3,
				trim_x_px = $4, trim_y_px = $5, trim_width_px = $6, trim_height_px = $7,
				updated_at = now()
			WHERE id = $8 AND case_version = $9
		`, artworkStatus, proofStatus, derivedAssetID, outcome.TrimXPx, outcome.TrimYPx, outcome.TrimWidthPx, outcome.TrimHeightPx,
			orderID, expectedCaseVersion)
		if err != nil {
			return false, err
		}
		if orderTag.RowsAffected() == 0 {
			return false, nil
		}

		if enqueuePrepareProof {
			if err := ensureJobEnqueued(ctx, tx, orderID, "prepare_proof", *derivedAssetID, expectedCaseVersion+1, nil, nil); err != nil {
				return false, err
			}
		}
	} else {
		orderTag, err := tx.Exec(ctx, `
			UPDATE orders SET artwork_status = 'NEEDS_REVIEW', case_version = case_version + 1, updated_at = now()
			WHERE id = $1 AND case_version = $2
		`, orderID, expectedCaseVersion)
		if err != nil {
			return false, err
		}
		if orderTag.RowsAffected() == 0 {
			return false, nil
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var errNoIdempotencyKey = errRepairJobMissingKey{}

type errRepairJobMissingKey struct{}

func (errRepairJobMissingKey) Error() string {
	return "repair job has no idempotency_key - it was not created via requestRepair"
}
