package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const clarificationColumns = `id, order_id, question, answer, answered_at, created_at`

func scanClarification(row pgx.Row) (*Clarification, error) {
	var c Clarification
	err := row.Scan(&c.ID, &c.OrderID, &c.Question, &c.Answer, &c.AnsweredAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetClarification(ctx context.Context, id string) (*Clarification, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+clarificationColumns+` FROM clarifications WHERE id = $1`, id)
	c, err := scanClarification(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// ListClarificationsForOrder returns every clarification for an order,
// oldest first.
func (s *Store) ListClarificationsForOrder(ctx context.Context, orderID string) ([]Clarification, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+clarificationColumns+` FROM clarifications WHERE order_id = $1 ORDER BY created_at ASC`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Clarification
	for rows.Next() {
		var c Clarification
		if err := rows.Scan(&c.ID, &c.OrderID, &c.Question, &c.Answer, &c.AnsweredAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AnswerClarificationAndConfirmTrim records the customer's reply, applies it
// - v1's only supported clarification outcome is a trim-only confirmation
// (see CLAUDE.md point 12) - and enqueues the follow-up inspect job, ALL
// atomically: the clarification is marked answered, the same state reopen
// ConfirmTrim performs (bump case_version, reset artwork_status/
// proof_status) happens, and (this is the fix - previously this job was
// enqueued as a SEPARATE call after this transaction committed, so a crash
// in between left the case reopened with no queued work to resume it) a
// new 'inspect' job is inserted bound to the order's current asset and the
// NEW case_version, all in one transaction. "A validated reply enqueues
// continuation" now means exactly that - one commit, not two.
func (s *Store) AnswerClarificationAndConfirmTrim(
	ctx context.Context,
	clarificationID, rawAnswer, orderID string,
	artworkIsTrimOnly bool,
	expectedCaseVersion int,
) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE clarifications SET answer = $1, answered_at = now()
		WHERE id = $2 AND answered_at IS NULL
	`, rawAnswer, clarificationID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already answered, or doesn't exist
	}

	orderTag, err := tx.Exec(ctx, `
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
	if orderTag.RowsAffected() == 0 {
		return false, nil
	}

	var currentAssetID *string
	if err := tx.QueryRow(ctx, `SELECT current_asset_id FROM orders WHERE id = $1`, orderID).Scan(&currentAssetID); err != nil {
		return false, err
	}
	if currentAssetID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (order_id, job_type, input_asset_id, input_case_version)
			VALUES ($1, 'inspect', $2, $3)
			ON CONFLICT (order_id, job_type) WHERE status IN ('QUEUED', 'RUNNING') DO NOTHING
		`, orderID, *currentAssetID, expectedCaseVersion+1); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
