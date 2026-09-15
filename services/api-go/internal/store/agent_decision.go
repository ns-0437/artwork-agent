package store

import (
	"context"
	"encoding/json"
)

// AgentDecisionOutcome is what CompleteAgentDecision needs to apply one
// agent_decide job's result. Action is exactly one of "ask_clarification",
// "request_repair", "escalate" - the caller (worker.runAgentDecision) has
// already validated the decision against the actual findings before
// building this (e.g. ask_clarification is only used when an
// unconfirmed-trim finding is actually present - see CLAUDE.md).
type AgentDecisionOutcome struct {
	Action string
	Reason string // audit note - required for escalate, optional elsewhere

	// ask_clarification:
	ClarificationQuestion string

	// request_repair:
	RepairAssetID        string
	RepairIdempotencyKey string
}

// CompleteAgentDecision commits the agent_decide job's SUCCEEDED result and
// applies its ONE side effect - atomically, in ONE transaction, the same
// pattern as CompleteInspection/CompleteRepair. This is what closes the
// second durability gap (the first being CompleteInspection's own
// follow-up-job insert): previously, applying a decision (persisting a
// clarification, enqueueing a repair, or escalating) happened as a SEPARATE
// call after marking the job done, so a crash in between left the job
// SUCCEEDED with no visible effect and no queued continuation. Now either
// both commit or neither does.
//
// The order's case_version is locked (SELECT ... FOR UPDATE) and checked
// against expectedCaseVersion for every action, including request_repair -
// even though that branch only inserts a new job (no orders row write), a
// stale case_version means this decision was computed against artwork the
// order has since moved past and must not be allowed to enqueue anything.
//
// Returns ok=false (nil error) if the worker's lease on the agent_decide
// job is gone, or the case_version guard fails - the caller should treat
// this as "this attempt is void" and not retry the same write.
func (s *Store) CompleteAgentDecision(
	ctx context.Context,
	jobID, workerID string,
	expectedCaseVersion int,
	outcome AgentDecisionOutcome,
) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	resultJSON, err := json.Marshal(map[string]interface{}{
		"action": outcome.Action,
		"reason": outcome.Reason,
	})
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

	var currentCaseVersion int
	if err := tx.QueryRow(ctx, `SELECT case_version FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&currentCaseVersion); err != nil {
		return false, err
	}
	if currentCaseVersion != expectedCaseVersion {
		return false, nil
	}

	switch outcome.Action {
	case "ask_clarification":
		if _, err := tx.Exec(ctx, `
			INSERT INTO clarifications (order_id, question) VALUES ($1, $2)
		`, orderID, outcome.ClarificationQuestion); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET artwork_status = 'AWAITING_CLARIFICATION', case_version = case_version + 1, updated_at = now()
			WHERE id = $1
		`, orderID); err != nil {
			return false, err
		}

	case "request_repair":
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (order_id, job_type, input_asset_id, input_case_version, idempotency_key)
			VALUES ($1, 'repair', $2, $3, $4)
			ON CONFLICT (order_id, job_type) WHERE status IN ('QUEUED', 'RUNNING') DO NOTHING
		`, orderID, outcome.RepairAssetID, expectedCaseVersion, outcome.RepairIdempotencyKey); err != nil {
			return false, err
		}
		// No order-state write here: decideArtworkStatus already left the
		// order at NEEDS_REVIEW (the bleed-insufficient finding that made
		// repair eligible in the first place); the repair job's own
		// completion is what moves it forward from there.

	case "escalate":
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET artwork_status = 'NEEDS_REVIEW', case_version = case_version + 1, updated_at = now()
			WHERE id = $1
		`, orderID); err != nil {
			return false, err
		}

	default:
		return false, errUnknownAgentAction
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

var errUnknownAgentAction = errAgentAction{}

type errAgentAction struct{}

func (errAgentAction) Error() string { return "unknown agent decision action" }
