package storage

import "context"

// Storage is the one interface both the local-disk (dev) and GCS (Day 5
// deploy) backends implement. Nothing outside this package depends on which
// backend is active.
type Storage interface {
	// Put stores data under a content-addressed key derived from its SHA-256
	// hash and returns that key plus the hex-encoded hash. Storing the same
	// bytes twice returns the same key without rewriting - this is what makes
	// a retried write after a crash a safe no-op.
	Put(ctx context.Context, data []byte) (key string, sha256Hex string, err error)
	Get(ctx context.Context, key string) ([]byte, error)
}
