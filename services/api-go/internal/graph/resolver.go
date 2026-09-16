// Package graph is the GraphQL layer: schema definition (schema.go), field
// resolvers (this file and resolvers.go), and the HTTP handler (handler.go).
// The schema is hand-written against graphql-go/graphql rather than
// codegen'd, so a reviewer can read the whole API surface in these three
// files without running a generator.
package graph

import (
	"time"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/upload"
)

type Resolver struct {
	Store   *store.Store
	Uploads *upload.Manager
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
