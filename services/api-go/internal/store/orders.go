package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

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
		RETURNING id, owner_id, product_type, declared_width, declared_height, declared_unit,
			customer_request, artwork_version, intent, trim_x_px, trim_y_px, trim_width_px, trim_height_px,
			case_version, artwork_status, proof_status, production_status, created_at, updated_at
	`, in.OwnerID, in.ProductType, in.DeclaredWidth, in.DeclaredHeight, in.DeclaredUnit, in.CustomerRequest, in.Intent)
	return scanOrder(row)
}

func (s *Store) GetOrder(ctx context.Context, id string) (*Order, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, owner_id, product_type, declared_width, declared_height, declared_unit,
			customer_request, artwork_version, intent, trim_x_px, trim_y_px, trim_width_px, trim_height_px,
			case_version, artwork_status, proof_status, production_status, created_at, updated_at
		FROM orders WHERE id = $1
	`, id)
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
		&o.CustomerRequest, &o.ArtworkVersion, &o.Intent, &o.TrimXPx, &o.TrimYPx, &o.TrimWidthPx, &o.TrimHeightPx,
		&o.CaseVersion, &o.ArtworkStatus, &o.ProofStatus, &o.ProductionStatus, &o.CreatedAt, &o.UpdatedAt)
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
