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

// CreateAsset is a plain insert - used by call sites that manage their own
// transaction and consistency around it (e.g. Day 3's repair completion,
// which inserts a 'repaired'/'preview' asset alongside a repairs row and an
// order-state update, all in one transaction it controls). It is NOT used
// for original-artwork uploads - see RecordArtworkUpload for why.
func (s *Store) CreateAsset(ctx context.Context, in CreateAssetInput) (*Asset, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO assets (order_id, kind, storage_key, sha256, content_type, width_px, height_px)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, order_id, kind, storage_key, sha256, content_type, width_px, height_px, created_at
	`, in.OrderID, in.Kind, in.StorageKey, in.SHA256, in.ContentType, in.WidthPx, in.HeightPx)
	return scanAsset(row)
}

// RecordArtworkUpload inserts a new original-artwork asset and, in the same
// transaction, reopens the case: bumps artwork_version and case_version,
// resets artwork_status to BLOCKED and proof_status to NOT_PREPARED (any
// previously prepared proof was prepared against the OLD artwork and is now
// meaningless), and clears trim confirmation and coordinates (a replacement
// upload might not even be the same content). This must be one transaction
// - a partial write (asset recorded but the order not reopened) would let a
// stale RESOLVED status from a prior upload persist against artwork the
// system has never actually inspected.
//
// Any job already in flight against the old case_version will fail to
// commit its result once this runs, via the same case_version guard in
// CompleteInspection - no separate cancellation is needed.
func (s *Store) RecordArtworkUpload(ctx context.Context, in CreateAssetInput) (*Asset, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	// artwork_version starts at 1 (the schema default) meaning "this is the
	// first upload" - only a REPLACEMENT upload should bump it further, so
	// check whether an original asset already exists before inserting.
	var priorOriginalCount int
	if in.Kind == "original" {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM assets WHERE order_id = $1 AND kind = 'original'
		`, in.OrderID).Scan(&priorOriginalCount); err != nil {
			return nil, err
		}
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO assets (order_id, kind, storage_key, sha256, content_type, width_px, height_px)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, order_id, kind, storage_key, sha256, content_type, width_px, height_px, created_at
	`, in.OrderID, in.Kind, in.StorageKey, in.SHA256, in.ContentType, in.WidthPx, in.HeightPx)
	asset, err := scanAsset(row)
	if err != nil {
		return nil, err
	}

	if in.Kind == "original" {
		isReplacement := priorOriginalCount > 0
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET
				artwork_version = artwork_version + CASE WHEN $2 THEN 1 ELSE 0 END,
				case_version = case_version + 1,
				artwork_status = 'BLOCKED',
				proof_status = 'NOT_PREPARED',
				artwork_is_trim_only = NULL,
				trim_x_px = NULL, trim_y_px = NULL, trim_width_px = NULL, trim_height_px = NULL,
				updated_at = now()
			WHERE id = $1
		`, in.OrderID, isReplacement); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return asset, nil
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

// ListAssetsForOrder returns every asset (original/repaired/preview) for an
// order, oldest first - used to show the repair's exports (CLAUDE.md's
// "Exports" requirement) without needing separate per-kind queries.
func (s *Store) ListAssetsForOrder(ctx context.Context, orderID string) ([]Asset, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, order_id, kind, storage_key, sha256, content_type, width_px, height_px, created_at
		FROM assets WHERE order_id = $1 ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.OrderID, &a.Kind, &a.StorageKey, &a.SHA256, &a.ContentType, &a.WidthPx, &a.HeightPx, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAsset(row pgx.Row) (*Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.OrderID, &a.Kind, &a.StorageKey, &a.SHA256, &a.ContentType, &a.WidthPx, &a.HeightPx, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
