// Package worker implements the one real execution path: claim a queued job,
// fetch the order's artwork from storage, call the Python image service, and
// persist the result and findings.
package worker

import (
	"context"
	"log"
	"time"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/pyclient"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/storage"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
)

const (
	pollInterval  = 2 * time.Second
	leaseDuration = 30 * time.Second
)

type Worker struct {
	ID      string
	Store   *store.Store
	Storage storage.Storage
	PyImage *pyclient.Client
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.tick(ctx); err != nil {
				log.Printf("worker tick error: %v", err)
			}
		}
	}
}

func (w *Worker) tick(ctx context.Context) error {
	job, err := w.Store.ClaimNextJob(ctx, w.ID, leaseDuration)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}

	log.Printf("worker %s claimed job %s (%s) for order %s", w.ID, job.ID, job.JobType, job.OrderID)

	switch job.JobType {
	case "inspect":
		w.runInspect(ctx, job)
	default:
		w.fail(ctx, job, "unknown job type: "+job.JobType)
	}
	return nil
}

func (w *Worker) runInspect(ctx context.Context, job *store.Job) {
	// The job is bound to a specific asset (frozen at creation time) - never
	// "whatever is latest right now", so a later upload can't change what an
	// in-flight job inspects.
	if job.InputAssetID == nil {
		w.fail(ctx, job, "job has no bound input asset")
		return
	}

	order, err := w.Store.GetOrder(ctx, job.OrderID)
	if err != nil {
		w.fail(ctx, job, "failed to look up order: "+err.Error())
		return
	}
	if order == nil {
		w.fail(ctx, job, "order no longer exists")
		return
	}

	asset, err := w.Store.GetAsset(ctx, *job.InputAssetID)
	if err != nil {
		w.fail(ctx, job, "failed to look up bound input asset: "+err.Error())
		return
	}
	if asset == nil {
		w.fail(ctx, job, "bound input asset no longer exists")
		return
	}

	data, err := w.Storage.Get(ctx, asset.StorageKey)
	if err != nil {
		w.fail(ctx, job, "failed to read artwork from storage: "+err.Error())
		return
	}

	intent := "border"
	if order.Intent != nil {
		intent = *order.Intent
	}

	inspected, err := w.PyImage.Inspect(data, asset.StorageKey, asset.ContentType, pyclient.InspectInput{
		DeclaredWidth:     order.DeclaredWidth,
		DeclaredHeight:    order.DeclaredHeight,
		DeclaredUnit:      order.DeclaredUnit,
		Intent:            intent,
		ArtworkIsTrimOnly: order.ArtworkIsTrimOnly,
	})
	if err != nil {
		w.fail(ctx, job, "image service inspect failed: "+err.Error())
		return
	}
	log.Printf("job %s inspected order %s: %dx%d %s (%s), %d check(s)",
		job.ID, job.OrderID, inspected.WidthPx, inspected.HeightPx, inspected.Mode, inspected.Format, len(inspected.Checks))

	findings := make([]store.FindingInput, 0, len(inspected.Checks))
	for _, c := range inspected.Checks {
		findings = append(findings, store.FindingInput{
			CheckName:   c.CheckName,
			Result:      c.Result,
			Evidence:    string(c.Evidence),
			RuleVersion: c.RuleVersion,
		})
	}
	artworkStatus, proofStatus := decideArtworkStatus(findings, intent)

	// The asset's dimensions, the job's result, every finding, and the
	// order's state advance all commit together in one transaction, gated on
	// this worker still holding a valid lease and the case not having moved
	// on since this job was bound to job.InputCaseVersion. See
	// store.CompleteInspection.
	ok, err := w.Store.CompleteInspection(ctx, job.ID, w.ID, *job.InputAssetID, job.InputCaseVersion, store.InspectionResult{
		WidthPx:  inspected.WidthPx,
		HeightPx: inspected.HeightPx,
		Mode:     inspected.Mode,
		Format:   inspected.Format,
	}, findings, artworkStatus, proofStatus)
	if err != nil {
		log.Printf("job %s: failed to commit inspection result: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed during inspection - result discarded")
	}
}

// allowedCheckResults is the complete per-check result vocabulary (CLAUDE.md
// point 9). Anything else is malformed, never a silent pass-through.
var allowedCheckResults = map[string]bool{
	"PASS":         true,
	"WARNING":      true,
	"NEEDS_INPUT":  true,
	"NEEDS_REVIEW": true,
}

// expectedChecksForIntent returns the set of check names that MUST be
// present for a given intent. bleed only applies to full-bleed intent (see
// CLAUDE.md point 12) - its absence for border intent is correct, not
// missing data.
func expectedChecksForIntent(intent string) map[string]bool {
	expected := map[string]bool{"resolution": true, "color": true}
	if intent == "full_bleed" {
		expected["bleed"] = true
	}
	return expected
}

// decideArtworkStatus aggregates check RESULTS into a workflow state - it
// does not recompute or second-guess any measurement (see CLAUDE.md point
// 1).
//
// It validates completeness FIRST: every check expected for this intent
// must be present exactly once, with a result from the recognized
// vocabulary. An empty, partial, or malformed findings list must never
// resolve a case just because it happens to contain no NEEDS_REVIEW/
// NEEDS_INPUT - "nothing to block on" is not the same as "everything
// passed" (see CLAUDE.md point 28's incomplete-results-block rule). That
// case escalates to NEEDS_REVIEW, the same as an unsafe finding, since it's
// a system anomaly a human should look at, not something the customer can
// act on.
//
// A WARNING never blocks; NEEDS_REVIEW always does; NEEDS_INPUT blocks for
// now since the clarification round-trip that would resolve it isn't wired
// up until Day 4 - transitioning to AWAITING_CLARIFICATION without a way to
// answer it would be a dead end equivalent to staying BLOCKED.
//
// proof_status always stays NOT_PREPARED here, even when every check
// passes: RESOLVED means only that no supported artwork blocker remains
// (see CLAUDE.md point 3) - preparing a proof is its own step (Day 4's
// agent loop) that must actually create and store a proof artifact before
// claiming one exists.
func decideArtworkStatus(findings []store.FindingInput, intent string) (artworkStatus, proofStatus string) {
	expected := expectedChecksForIntent(intent)
	seen := map[string]bool{}
	hasNeedsReview := false
	hasNeedsInput := false
	malformed := false

	for _, f := range findings {
		if !expected[f.CheckName] || !allowedCheckResults[f.Result] {
			malformed = true
			continue
		}
		seen[f.CheckName] = true
		switch f.Result {
		case "NEEDS_REVIEW":
			hasNeedsReview = true
		case "NEEDS_INPUT":
			hasNeedsInput = true
		}
	}
	for name := range expected {
		if !seen[name] {
			malformed = true
		}
	}

	switch {
	case malformed:
		return "NEEDS_REVIEW", "NOT_PREPARED"
	case hasNeedsReview:
		return "NEEDS_REVIEW", "NOT_PREPARED"
	case hasNeedsInput:
		return "BLOCKED", "NOT_PREPARED"
	default:
		return "RESOLVED", "NOT_PREPARED"
	}
}

func (w *Worker) fail(ctx context.Context, job *store.Job, msg string) {
	log.Printf("job %s failed: %s", job.ID, msg)
	if _, err := w.Store.FailJob(ctx, job.ID, w.ID, msg); err != nil {
		log.Printf("job %s: failed to record failure: %v", job.ID, err)
	}
}
