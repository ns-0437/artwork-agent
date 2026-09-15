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

// CreateClarificationAndAwait persists a targeted question and moves the
// case to AWAITING_CLARIFICATION, atomically - "Persist a clarification and
// exit" (the brief's loop step 4). Zero rows affected on the order update
// means the caller's case_version was stale; the clarification insert is
// rolled back along with it (all-or-nothing).
func (s *Store) CreateClarificationAndAwait(ctx context.Context, orderID, question string, expectedCaseVersion int) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO clarifications (order_id, question) VALUES ($1, $2)
	`, orderID, question); err != nil {
		return false, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE orders SET artwork_status = 'AWAITING_CLARIFICATION', case_version = case_version + 1, updated_at = now()
		WHERE id = $1 AND case_version = $2
	`, orderID, expectedCaseVersion)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// AnswerClarificationAndConfirmTrim records the customer's reply and applies
// it - v1's only supported clarification outcome is a trim-only
// confirmation (see CLAUDE.md point 12) - atomically: the clarification is
// marked answered, and the SAME state reopen ConfirmTrim performs (bump
// case_version, reset artwork_status/proof_status) happens in the same
// transaction, gated on expectedCaseVersion. "A validated reply enqueues
// continuation" - the caller enqueues the follow-up inspect job separately,
// once this commit succeeds.
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

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
