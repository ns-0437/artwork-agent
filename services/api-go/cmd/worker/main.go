package main

import (
	"context"
	"log"
	"os"

	"github.com/google/uuid"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/pyclient"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/storage"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/worker"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()

	dsn := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/artwork_agent?sslmode=disable")
	st, err := store.NewStore(ctx, dsn)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer st.Close()

	storageDir := envOr("STORAGE_LOCAL_DIR", "./data/artifacts")
	disk, err := storage.NewLocalDisk(storageDir)
	if err != nil {
		log.Fatalf("failed to init local storage: %v", err)
	}

	imageServiceURL := envOr("IMAGE_SERVICE_URL", "http://localhost:8081")
	py := pyclient.New(imageServiceURL)

	w := &worker.Worker{
		ID:      "worker-" + uuid.NewString()[:8],
		Store:   st,
		Storage: disk,
		PyImage: py,
	}
	log.Printf("starting worker %s", w.ID)
	w.Run(ctx)
}
