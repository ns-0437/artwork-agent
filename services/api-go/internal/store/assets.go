package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type CreateAssetInput struct {
	OrderID     string
	Kind        string
	StorageKey  string
	SHA256      string
	ContentType string
	WidthPx     *int
	HeightPx    *int
}

func (s *Store) CreateAsset(ctx context.Context, in CreateAssetInput) (*Asset, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO assets (order_id, kind, storage_key, sha256, content_type, width_px, height_px)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, order_id, kind, storage_key, sha256, content_type, width_px, height_px, created_at
	`, in.OrderID, in.Kind, in.StorageKey, in.SHA256, in.ContentType, in.WidthPx, in.HeightPx)
	return scanAsset(row)
}

func (s *Store) GetAsset(ctx context.Context, id string) (*Asset, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, order_id, kind, storage_key, sha256, content_type, width_px, height_px, created_at
		FROM assets WHERE id = $1
	`, id)
	a, err := scanAsset(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

// LatestAssetByKind finds the most recent asset of a given kind for an order,
// e.g. the current original artwork. Orders don't carry a direct FK to "the"
// artwork asset because a new artwork_version uploads a new original asset
// rather than overwriting one.
func (s *Store) LatestAssetByKind(ctx context.Context, orderID, kind string) (*Asset, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, order_id, kind, storage_key, sha256, content_type, width_px, height_px, created_at
		FROM assets WHERE order_id = $1 AND kind = $2
		ORDER BY created_at DESC LIMIT 1
	`, orderID, kind)
	a, err := scanAsset(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

func scanAsset(row pgx.Row) (*Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.OrderID, &a.Kind, &a.StorageKey, &a.SHA256, &a.ContentType, &a.WidthPx, &a.HeightPx, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
