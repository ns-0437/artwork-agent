package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// LocalDisk is the dev-only Storage backend. Files live under baseDir,
// content-addressed by SHA-256 with a two-character fan-out directory.
type LocalDisk struct {
	baseDir string
}

func NewLocalDisk(baseDir string) (*LocalDisk, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	return &LocalDisk{baseDir: baseDir}, nil
}

func (l *LocalDisk) keyPath(key string) string {
	return filepath.Join(l.baseDir, key)
}

func (l *LocalDisk) Put(ctx context.Context, data []byte) (string, string, error) {
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	key := fmt.Sprintf("%s/%s", hexSum[:2], hexSum)
	path := l.keyPath(key)

	if _, err := os.Stat(path); err == nil {
		// Already stored under this content hash - safe no-op.
		return key, hexSum, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", "", err
	}
	return key, hexSum, nil
}

func (l *LocalDisk) Get(ctx context.Context, key string) ([]byte, error) {
	return os.ReadFile(l.keyPath(key))
}
