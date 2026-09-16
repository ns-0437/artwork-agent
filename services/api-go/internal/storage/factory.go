package storage

import (
	"context"
	"fmt"
	"os"
)

// NewFromEnv selects the Storage backend by STORAGE_BACKEND ("local", the
// default, or "gcs") so cmd/server and cmd/worker share one selection point
// instead of duplicating this logic - both entrypoints must always agree on
// which backend is active, since they read and write the same content-
// addressed keys.
func NewFromEnv(ctx context.Context) (Storage, error) {
	switch backend := envOr("STORAGE_BACKEND", "local"); backend {
	case "local":
		dir := envOr("STORAGE_LOCAL_DIR", "./data/artifacts")
		return NewLocalDisk(dir)
	case "gcs":
		bucket := os.Getenv("GCS_BUCKET")
		if bucket == "" {
			return nil, fmt.Errorf("storage: STORAGE_BACKEND=gcs requires GCS_BUCKET to be set")
		}
		return NewGCS(ctx, bucket)
	default:
		return nil, fmt.Errorf("storage: unknown STORAGE_BACKEND %q (expected \"local\" or \"gcs\")", backend)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
