package store

import "context"

// ProofAssetInput is the rendered proof image's storage location, ready to
// record - the bytes are already written to internal/storage by the caller
// (worker.runPrepareProof) before this is called, same as
// CompleteRepair's asset inputs; storage writes aren't transactional with
// Postgres.
type ProofAssetInput struct {
	StorageKey string
	SHA256     string
	WidthPx    int
	HeightPx   int
}

// CompleteProofPreparation commits the prepare_proof job's SUCCEEDED status,
// the new 'proof' asset, and the order's proof_status transition to
// AWAITING_CUSTOMER_APPROVAL, ALL in one transaction - the same pattern as
// every other Complete* method (point 24). proof_status must never reach
// AWAITING_CUSTOMER_APPROVAL without a real, stored proof asset backing it
// (CLAUDE.md point 3): this is the one and only place that transition
// happens, and it always happens together with the asset insert that
// justifies it.
//
// Returns ok=false (nil error) if the worker's lease is gone or the case
// moved past expectedCaseVersion - the caller treats that as "this attempt
// is void", same as every other Complete* method.
func (s *Store) CompleteProofPreparation(
	ctx context.Context,
	jobID, workerID string,
	expectedCaseVersion int,
	asset ProofAssetInput,
) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	jobTag, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'SUCCEEDED', updated_at = now()
		WHERE id = $1 AND worker_id = $2 AND status = 'RUNNING' AND lease_expires_at > now()
	`, jobID, workerID)
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

	if _, err := tx.Exec(ctx, `
		INSERT INTO assets (order_id, kind, storage_key, sha256, content_type, width_px, height_px)
		VALUES ($1, 'proof', $2, $3, 'image/png', $4, $5)
	`, orderID, asset.StorageKey, asset.SHA256, asset.WidthPx, asset.HeightPx); err != nil {
		return false, err
	}

	orderTag, err := tx.Exec(ctx, `
		UPDATE orders SET proof_status = 'AWAITING_CUSTOMER_APPROVAL', case_version = case_version + 1, updated_at = now()
		WHERE id = $1 AND case_version = $2
	`, orderID, expectedCaseVersion)
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
