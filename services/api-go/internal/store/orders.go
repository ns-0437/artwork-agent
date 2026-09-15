package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const orderColumns = `id, owner_id, product_type, declared_width, declared_height, declared_unit,
	customer_request, artwork_version, intent, artwork_is_trim_only, current_asset_id, trim_x_px, trim_y_px, trim_width_px, trim_height_px,
	case_version, artwork_status, proof_status, production_status, agent_tool_calls_used, agent_retries_used, created_at, updated_at`

type CreateOrderInput struct {
	OwnerID         string
	ProductType     string
	DeclaredWidth   float64
	DeclaredHeight  float64
	DeclaredUnit    string
	CustomerRequest *string
	Intent          *string
}

func (s *Store) CreateOrder(ctx context.Context, in CreateOrderInput) (*Order, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO orders (owner_id, product_type, declared_width, declared_height, declared_unit, customer_request, intent)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+orderColumns, in.OwnerID, in.ProductType, in.DeclaredWidth, in.DeclaredHeight, in.DeclaredUnit, in.CustomerRequest, in.Intent)
	return scanOrder(row)
}

func (s *Store) GetOrder(ctx context.Context, id string) (*Order, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1`, id)
	o, err := scanOrder(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return o, nil
}

func scanOrder(row pgx.Row) (*Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.OwnerID, &o.ProductType, &o.DeclaredWidth, &o.DeclaredHeight, &o.DeclaredUnit,
		&o.CustomerRequest, &o.ArtworkVersion, &o.Intent, &o.ArtworkIsTrimOnly, &o.CurrentAssetID, &o.TrimXPx, &o.TrimYPx, &o.TrimWidthPx, &o.TrimHeightPx,
		&o.CaseVersion, &o.ArtworkStatus, &o.ProofStatus, &o.ProductionStatus, &o.AgentToolCallsUsed, &o.AgentRetriesUsed, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// UpdateOrderStateIfVersion is for a standalone, client-submitted mutation
// (answerClarification, requestRepair, from Day 3/4 onward) that must be
// rejected if the case has moved since the client last observed it. Zero
// rows affected means the caller's case_version was stale.
//
// A worker/agent-driven state advance that must commit alongside other
// writes (e.g. persisting a job's result) should NOT call this as a separate
// statement - two separate pool.Exec calls can't share a transaction, so a
// crash between them could leave the result stored but the order state
// unadvanced, or vice versa. See store.CompleteInspection for the pattern:
// do the order UPDATE inline, in the same transaction, gated the same way.
func (s *Store) UpdateOrderStateIfVersion(ctx context.Context, orderID string, expectedCaseVersion int, artworkStatus, proofStatus string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE orders
		SET artwork_status = $1, proof_status = $2, case_version = case_version + 1, updated_at = now()
		WHERE id = $3 AND case_version = $4
	`, artworkStatus, proofStatus, orderID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordAgentToolCall bumps agent_tool_calls_used and logs a tool_events row
// for one decision-call attempt (successful or not) - the persisted half of
// the bounded loop's 5-tool-call cap (CLAUDE.md point/brief: "cap each
// execution segment at five tool calls"). detailJSON is a JSON-encoded blob
// (action taken, or the error) for the audit trail.
func (s *Store) RecordAgentToolCall(ctx context.Context, orderID string, detailJSON string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET agent_tool_calls_used = agent_tool_calls_used + 1, updated_at = now() WHERE id = $1
	`, orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tool_events (order_id, event_type, detail) VALUES ($1, 'tool_call', $2::jsonb)
	`, orderID, detailJSON); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordAgentRetry bumps agent_retries_used and logs a tool_events row - the
// persisted half of the bounded loop's 2-transient-retry cap.
func (s *Store) RecordAgentRetry(ctx context.Context, orderID string, detailJSON string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET agent_retries_used = agent_retries_used + 1, updated_at = now() WHERE id = $1
	`, orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tool_events (order_id, event_type, detail) VALUES ($1, 'retry', $2::jsonb)
	`, orderID, detailJSON); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EscalateToNeedsReview is used when the agent loop itself can't produce a
// decision (budget exhausted, or every retry failed) - the case still needs
// SOME terminal state, and NEEDS_REVIEW (a human should look at it) is the
// honest one, not a silent retry-forever or a guessed resolution.
func (s *Store) EscalateToNeedsReview(ctx context.Context, orderID string, expectedCaseVersion int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE orders SET artwork_status = 'NEEDS_REVIEW', case_version = case_version + 1, updated_at = now()
		WHERE id = $1 AND case_version = $2
	`, orderID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ConfirmTrim records whether the customer confirms the current upload is
// trim-only (no bleed margin present) - the only trim-confirmation mode v1
// supports. This reopens the case (bumps case_version, resets
// artwork_status to BLOCKED and proof_status to NOT_PREPARED) since it
// changes what the checks can determine - any prior inspection result was
// computed without this knowledge and is now stale. Zero rows affected
// means the caller's case_version was stale (see UpdateOrderStateIfVersion).
func (s *Store) ConfirmTrim(ctx context.Context, orderID string, artworkIsTrimOnly bool, expectedCaseVersion int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE orders SET
			artwork_is_trim_only = $1,
			case_version = case_version + 1,
			artwork_status = 'BLOCKED',
			proof_status = 'NOT_PREPARED',
			updated_at = now()
		WHERE id = $2 AND case_version = $3
	`, artworkIsTrimOnly, orderID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
