package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/google/uuid"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/agent"
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

	disk, err := storage.NewFromEnv(ctx)
	if err != nil {
		log.Fatalf("failed to init storage: %v", err)
	}

	imageServiceURL := envOr("IMAGE_SERVICE_URL", "http://localhost:8081")
	py := pyclient.New(imageServiceURL)

	w := &worker.Worker{
		ID:      "worker-" + uuid.NewString()[:8],
		Store:   st,
		Storage: disk,
		PyImage: py,
		Agent:   buildAgentProvider(),
	}
	// cmd/worker is otherwise a plain poll loop with no HTTP surface, but
	// Cloud Run (see infra/gcp/worker-service.yaml) needs some port
	// listening to consider a revision healthy - this exists for that,
	// not for any real traffic.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(rw http.ResponseWriter, r *http.Request) {
			rw.WriteHeader(http.StatusOK)
		})
		addr := ":" + envOr("PORT", "8082")
		log.Printf("worker healthz listening on %s", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("healthz server failed: %v", err)
		}
	}()

	log.Printf("starting worker %s (agent provider: %v)", w.ID, w.Agent != nil)
	w.Run(ctx)
}

// buildAgentProvider picks the agent loop's provider from environment
// config. Groq (fast inference host for open models - NOT xAI's Grok, see
// CLAUDE.md) is used here as a practical stand-in while Anthropic account
// credits were unavailable; the brief's intended Claude-first choice would
// slot in here as another adapter.Provider implementation without changing
// anything else - the interface is what makes this replaceable. No key
// configured means no provider: the worker runs deterministic-only, same as
// Day 2/3.
func buildAgentProvider() agent.Provider {
	if key := os.Getenv("GROQ_API_KEY"); key != "" {
		return agent.NewGroqAdapter(key, envOr("GROQ_MODEL", ""))
	}
	return nil
}
