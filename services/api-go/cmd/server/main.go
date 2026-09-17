package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/graph"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/storage"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/upload"
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

	// Unset (0/unlimited) is the correct default for local dev - the public
	// deployment sets MAX_DEMO_ORDERS explicitly (see infra/gcp/api-go-service.yaml)
	// to bound cost exposure from unauthenticated writes (CLAUDE.md's known gap).
	if v := os.Getenv("MAX_DEMO_ORDERS"); v != "" {
		max, err := strconv.Atoi(v)
		if err != nil {
			log.Fatalf("invalid MAX_DEMO_ORDERS %q: %v", v, err)
		}
		st.MaxOrders = max
	}

	disk, err := storage.NewFromEnv(ctx)
	if err != nil {
		log.Fatalf("failed to init storage: %v", err)
	}

	uploadSecret := envOr("UPLOAD_SIGNING_SECRET", "dev-only-insecure-secret")
	baseURL := envOr("API_BASE_URL", "http://localhost:8080")
	uploads := upload.NewManager([]byte(uploadSecret), disk, st, baseURL)

	// No auth on this API (a known, documented gap) - the public deployment
	// sets READ_ONLY_DEMO=true (see infra/gcp/api-go-service.yaml) so every
	// mutation is rejected server-side, not left to an order-count cap
	// alone (which only bounds creation, not repeated writes/reprocessing
	// against any EXISTING order) or to the frontend not offering the
	// buttons. Local dev leaves this unset/false.
	readOnly, err := strconv.ParseBool(envOr("READ_ONLY_DEMO", "false"))
	if err != nil {
		log.Fatalf("invalid READ_ONLY_DEMO: %v", err)
	}

	resolver := &graph.Resolver{Store: st, Uploads: uploads, ReadOnly: readOnly}
	schema, err := graph.NewSchema(resolver)
	if err != nil {
		log.Fatalf("failed to build graphql schema: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/graphql", graph.Handler(schema))
	mux.Handle("/uploads/", uploads.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := ":" + envOr("PORT", "8080")
	log.Printf("api-go server listening on %s", addr)
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Fatal(err)
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
