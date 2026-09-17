// Package graph is the GraphQL layer: schema definition (schema.go), field
// resolvers (this file and resolvers.go), and the HTTP handler (handler.go).
// The schema is hand-written against graphql-go/graphql rather than
// codegen'd, so a reviewer can read the whole API surface in these three
// files without running a generator.
package graph

import (
	"errors"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/upload"
)

type Resolver struct {
	Store   *store.Store
	Uploads *upload.Manager

	// ReadOnly, when true, rejects every mutation before it reaches its
	// real resolver - see guarded(). There's no authentication on this
	// API (a known, documented gap), so the public deployment sets this
	// true rather than leaving unauthenticated writes open: an order
	// creation cap alone still let anyone repeatedly mutate/re-process
	// ANY existing order, capped or not. Local dev leaves this false.
	ReadOnly bool
}

// ErrReadOnlyDemo is what every mutation returns when Resolver.ReadOnly is
// true - a single, honest message rather than a per-mutation guess at
// what to say.
var ErrReadOnlyDemo = errors.New("this public demo is read-only - no writes are accepted here (no auth on this API, so this is enforced server-side, not left to the frontend). Run it locally via docker-compose (see the README) to test the full interactive flow, or watch the recorded demo")

// guarded wraps a mutation resolver so Resolver.ReadOnly is checked in
// exactly one place, before any of the 7 mutation fields' own logic runs -
// adding an 8th mutation later means wrapping it here too, not trusting
// each resolver to remember.
func (r *Resolver) guarded(fn graphql.FieldResolveFn) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		if r.ReadOnly {
			return nil, ErrReadOnlyDemo
		}
		return fn(p)
	}
}

func fmtTime(t time.Time) string { return t.Format(time.RFC3339) }

func orderToMap(o *store.Order) map[string]interface{} {
	return map[string]interface{}{
		"id":                o.ID,
		"ownerId":           o.OwnerID,
		"productType":       o.ProductType,
		"declaredWidth":     o.DeclaredWidth,
		"declaredHeight":    o.DeclaredHeight,
		"declaredUnit":      o.DeclaredUnit,
		"customerRequest":   o.CustomerRequest,
		"artworkVersion":    o.ArtworkVersion,
		"intent":            o.Intent,
		"artworkIsTrimOnly": o.ArtworkIsTrimOnly,
		"caseVersion":       o.CaseVersion,
		"artworkStatus":     o.ArtworkStatus,
		"proofStatus":       o.ProofStatus,
		"productionStatus":  o.ProductionStatus,
		"createdAt":         fmtTime(o.CreatedAt),
		"updatedAt":         fmtTime(o.UpdatedAt),
	}
}

func jobToMap(j *store.Job) map[string]interface{} {
	return map[string]interface{}{
		"id":           j.ID,
		"jobType":      j.JobType,
		"status":       j.Status,
		"attemptCount": j.AttemptCount,
		"lastError":    j.LastError,
		"result":       j.Result,
		"createdAt":    fmtTime(j.CreatedAt),
		"updatedAt":    fmtTime(j.UpdatedAt),
	}
}

func findingToMap(f *store.Finding) map[string]interface{} {
	return map[string]interface{}{
		"id":          f.ID,
		"checkName":   f.CheckName,
		"result":      f.Result,
		"evidence":    f.Evidence,
		"ruleVersion": f.RuleVersion,
		"createdAt":   fmtTime(f.CreatedAt),
	}
}

func assetToMap(a *store.Asset) map[string]interface{} {
	return map[string]interface{}{
		"id":          a.ID,
		"kind":        a.Kind,
		"contentType": a.ContentType,
		"widthPx":     a.WidthPx,
		"heightPx":    a.HeightPx,
		"sha256":      a.SHA256,
		"storageKey":  a.StorageKey,
		"createdAt":   fmtTime(a.CreatedAt),
	}
}

func repairToMap(rp *store.Repair) map[string]interface{} {
	return map[string]interface{}{
		"id":             rp.ID,
		"idempotencyKey": rp.IdempotencyKey,
		"status":         rp.Status,
		"reason":         rp.Reason,
		"diagnosis":      rp.Diagnosis,
		"createdAt":      fmtTime(rp.CreatedAt),
	}
}

func clarificationToMap(c *store.Clarification) map[string]interface{} {
	m := map[string]interface{}{
		"id":        c.ID,
		"question":  c.Question,
		"answer":    c.Answer,
		"createdAt": fmtTime(c.CreatedAt),
	}
	if c.AnsweredAt != nil {
		m["answeredAt"] = fmtTime(*c.AnsweredAt)
	} else {
		m["answeredAt"] = nil
	}
	if c.InvalidatedAt != nil {
		m["invalidatedAt"] = fmtTime(*c.InvalidatedAt)
	} else {
		m["invalidatedAt"] = nil
	}
	return m
}

func toolEventToMap(e *store.ToolEvent) map[string]interface{} {
	return map[string]interface{}{
		"id":        e.ID,
		"eventType": e.EventType,
		"detail":    e.Detail,
		"createdAt": fmtTime(e.CreatedAt),
	}
}
