package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrOrderLimitReached is returned by CreateOrder once the total order
// count reaches Store.MaxOrders - the public deployment has no
// authentication (CLAUDE.md's known gap), so this bounds worst-case cost
// (Groq calls, GCS storage, Cloud Run compute, Neon rows) from unbounded
// anonymous writes rather than leaving it to documentation alone.
var ErrOrderLimitReached = errors.New("this portfolio demo has reached its order limit (kept low to bound hosting cost on an unauthenticated public deployment) - run it locally via docker-compose (see the README) for unlimited testing")

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
	if s.MaxOrders > 0 {
		var count int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&count); err != nil {
			return nil, err
		}
		if count >= s.MaxOrders {
			return nil, ErrOrderLimitReached
		}
	}
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

// ReserveAgentToolCall atomically reserves ONE unit of the persisted
// tool-call budget BEFORE the provider is ever called: the UPDATE only
// succeeds if agent_tool_calls_used is still below maxCalls, in a single
// statement (not read-then-write), so two concurrent attempts can never
// both reserve past the cap. Returns reserved=false (no error) if the
// budget is already exhausted - the caller must NOT call the provider in
// that case. If this call itself errors (e.g. a DB problem), the caller
// must also not call the provider - an unreserved call can't be accounted
// for.
func (s *Store) ReserveAgentToolCall(ctx context.Context, orderID string, maxCalls int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE orders SET agent_tool_calls_used = agent_tool_calls_used + 1, updated_at = now()
		WHERE id = $1 AND agent_tool_calls_used < $2
	`, orderID, maxCalls)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ReserveAgentRetry is the same pattern for the persisted retry budget -
// checked against the TOTAL retries this case has ever used (across every
// tool call it has made), not a per-call local counter that would reset on
// every new decision attempt and let the true total exceed the cap.
func (s *Store) ReserveAgentRetry(ctx context.Context, orderID string, maxRetries int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE orders SET agent_retries_used = agent_retries_used + 1, updated_at = now()
		WHERE id = $1 AND agent_retries_used < $2
	`, orderID, maxRetries)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// LogAgentToolEvent is audit-trail only - it does NOT gate or count toward
// any budget (see ReserveAgentToolCall/ReserveAgentRetry for that). A
// logging failure is deliberately non-fatal to the caller's decision flow.
func (s *Store) LogAgentToolEvent(ctx context.Context, orderID, eventType, detailJSON string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO tool_events (order_id, event_type, detail) VALUES ($1, $2, $3::jsonb)
	`, orderID, eventType, detailJSON)
	return err
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

// EscalateCase is a direct, non-agent path to the same NEEDS_REVIEW + reason
// state transition CompleteAgentDecision's escalate branch applies for the
// agent loop (point 4 - "escalation is a real, persisted state transition")
// - here callable by ANY client (a scripted ops workflow, a future human
// reviewer action) with a rule-based reason of its own, not just the agent.
// This exists specifically so a deterministic script can escalate a case
// nothing else about it resolves, rather than leaving it silently BLOCKED
// forever - see evals/scripts/run_eval.py's --mode scripted.
//
// Refuses to escalate a case already RESOLVED (case_version guard plus an
// explicit status check) - escalation is for a case with an actual
// unresolved blocker, not a way to walk back a completed one. The reason is
// logged to tool_events for the same audit trail the agent's own escalation
// attempts already write to.
func (s *Store) EscalateCase(ctx context.Context, orderID, reason string, expectedCaseVersion int) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE orders SET artwork_status = 'NEEDS_REVIEW', case_version = case_version + 1, updated_at = now()
		WHERE id = $1 AND case_version = $2 AND artwork_status != 'RESOLVED'
	`, orderID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	detailJSON, err := json.Marshal(map[string]interface{}{"source": "script", "action": "escalate", "reason": reason})
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tool_events (order_id, event_type, detail) VALUES ($1, 'tool_call', $2::jsonb)
	`, orderID, string(detailJSON)); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
