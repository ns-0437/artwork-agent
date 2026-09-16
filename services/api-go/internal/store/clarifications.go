package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const clarificationColumns = `id, order_id, question, answer, answered_at, artwork_version, invalidated_at, created_at`

func scanClarification(row pgx.Row) (*Clarification, error) {
	var c Clarification
	err := row.Scan(&c.ID, &c.OrderID, &c.Question, &c.Answer, &c.AnsweredAt, &c.ArtworkVersion, &c.InvalidatedAt, &c.CreatedAt)
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
		if err := rows.Scan(&c.ID, &c.OrderID, &c.Question, &c.Answer, &c.AnsweredAt, &c.ArtworkVersion, &c.InvalidatedAt, &c.CreatedAt); err != nil {
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
// proof_status) happens, and a new 'inspect' job is inserted bound to the
// order's current asset and the NEW case_version, all in one transaction.
// "A validated reply enqueues continuation" means exactly that - one
// commit, not two (a crash in between would otherwise leave the case
// reopened with no queued work to resume it).
//
// The clarification being answered must be the order's CURRENTLY ACTIVE one:
// unanswered, not invalidated, bound to the order's CURRENT artwork_version,
// and the most recently created such row. Without this, a clarification left
// over from BEFORE a replacement upload (which bumps artwork_version and
// invalidates any still-unanswered question - see RecordArtworkUpload) could
// otherwise be answered using the order's now-current case_version (which a
// client could easily still supply, having no reason to know the artwork
// underneath it changed) and get silently applied to artwork the question
// was never actually about. Returns ok=false for any of: already answered,
// invalidated, bound to a stale artwork_version, superseded by a newer
// clarification, or a stale case_version - the caller (resolveAnswerClarification)
// treats all of these the same way: a rejected, non-corrupting no-op.
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

	var currentArtworkVersion int
	if err := tx.QueryRow(ctx, `SELECT artwork_version FROM orders WHERE id = $1`, orderID).Scan(&currentArtworkVersion); err != nil {
		return false, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE clarifications SET answer = $1, answered_at = now()
		WHERE id = $2
		  AND order_id = $3
		  AND answered_at IS NULL
		  AND invalidated_at IS NULL
		  AND artwork_version = $4
		  AND id = (
		      SELECT id FROM clarifications
		      WHERE order_id = $3 AND answered_at IS NULL AND invalidated_at IS NULL
		      ORDER BY created_at DESC LIMIT 1
		  )
	`, rawAnswer, clarificationID, orderID, currentArtworkVersion)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already answered, invalidated, stale artwork_version, or superseded
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
		if err := ensureJobEnqueued(ctx, tx, orderID, "inspect", *currentAssetID, expectedCaseVersion+1, nil, nil); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
