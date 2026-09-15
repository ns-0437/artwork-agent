// Package worker implements the one real execution path: claim a queued job,
// fetch the order's artwork from storage, call the Python image service, and
// persist the result and findings.
package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	case "repair":
		w.runRepair(ctx, job)
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

func (w *Worker) runRepair(ctx context.Context, job *store.Job) {
	if job.InputAssetID == nil {
		w.fail(ctx, job, "job has no bound input asset")
		return
	}
	if job.IdempotencyKey == nil {
		w.fail(ctx, job, "repair job has no idempotency_key - was not created via requestRepair")
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

	intent := "border"
	if order.Intent != nil {
		intent = *order.Intent
	}

	// Repair only ever applies to the confirmed trim-only case: that's what
	// makes "the whole original image is the trim" a known fact rather than
	// a guess (see CLAUDE.md point 12). Checked here, before calling Python
	// at all, rather than letting the endpoint attempt something built on
	// an unconfirmed assumption.
	if intent != "full_bleed" || order.ArtworkIsTrimOnly == nil || !*order.ArtworkIsTrimOnly {
		w.completeRejectedRepair(ctx, job, asset,
			"repair requires full-bleed intent with artwork_is_trim_only confirmed true - confirm trim before requesting repair")
		return
	}

	data, err := w.Storage.Get(ctx, asset.StorageKey)
	if err != nil {
		w.fail(ctx, job, "failed to read artwork from storage: "+err.Error())
		return
	}

	repaired, err := w.PyImage.Repair(data, asset.StorageKey, pyclient.RepairInput{
		DeclaredWidth:  order.DeclaredWidth,
		DeclaredHeight: order.DeclaredHeight,
		DeclaredUnit:   order.DeclaredUnit,
	})
	if err != nil {
		w.fail(ctx, job, "image service repair failed: "+err.Error())
		return
	}

	if !repaired.Repaired {
		log.Printf("job %s: order %s not eligible for repair: %s", job.ID, job.OrderID, repaired.Reason)
		w.completeRejectedRepair(ctx, job, asset, repaired.Reason)
		return
	}

	canvasBytes, err := base64.StdEncoding.DecodeString(repaired.ImageBase64)
	if err != nil {
		w.fail(ctx, job, "failed to decode repaired image: "+err.Error())
		return
	}
	previewBytes, err := base64.StdEncoding.DecodeString(repaired.PreviewBase64)
	if err != nil {
		w.fail(ctx, job, "failed to decode preview image: "+err.Error())
		return
	}

	derivedKey, derivedHash, err := w.Storage.Put(ctx, canvasBytes)
	if err != nil {
		w.fail(ctx, job, "failed to store repaired image: "+err.Error())
		return
	}
	previewKey, previewHash, err := w.Storage.Put(ctx, previewBytes)
	if err != nil {
		w.fail(ctx, job, "failed to store preview image: "+err.Error())
		return
	}

	// Re-run checks against the NEW canvas with the now-known trim
	// coordinates passed explicitly - never re-derived from the larger
	// canvas size (CLAUDE.md point 12).
	rechecked, err := w.PyImage.Inspect(canvasBytes, derivedKey, "image/png", pyclient.InspectInput{
		DeclaredWidth:     order.DeclaredWidth,
		DeclaredHeight:    order.DeclaredHeight,
		DeclaredUnit:      order.DeclaredUnit,
		Intent:            intent,
		ArtworkIsTrimOnly: order.ArtworkIsTrimOnly,
		TrimWidthPx:       &repaired.TrimWidthPx,
		TrimHeightPx:      &repaired.TrimHeightPx,
	})
	if err != nil {
		w.fail(ctx, job, "post-repair recheck failed: "+err.Error())
		return
	}

	findings := make([]store.FindingInput, 0, len(rechecked.Checks))
	for _, c := range rechecked.Checks {
		findings = append(findings, store.FindingInput{
			CheckName:   c.CheckName,
			Result:      c.Result,
			Evidence:    string(c.Evidence),
			RuleVersion: c.RuleVersion,
		})
	}
	artworkStatus, proofStatus := decideArtworkStatus(findings, intent)

	diagnosisJSON, err := json.Marshal(map[string]interface{}{
		"margin_px":      repaired.MarginPx,
		"effective_ppi":  repaired.EffectivePPI,
		"edge_color":     repaired.EdgeColor,
		"trim_x_px":      repaired.TrimXPx,
		"trim_y_px":      repaired.TrimYPx,
		"trim_width_px":  repaired.TrimWidthPx,
		"trim_height_px": repaired.TrimHeightPx,
	})
	if err != nil {
		w.fail(ctx, job, "failed to encode repair diagnosis: "+err.Error())
		return
	}

	outcome := store.RepairOutcome{
		Repaired:      true,
		SourceAssetID: asset.ID,
		SourceHash:    asset.SHA256,
		DerivedAsset: store.RepairAssetInput{
			StorageKey: derivedKey, SHA256: derivedHash, WidthPx: repaired.WidthPx, HeightPx: repaired.HeightPx,
		},
		PreviewAsset: store.RepairAssetInput{
			StorageKey: previewKey, SHA256: previewHash, WidthPx: repaired.WidthPx, HeightPx: repaired.HeightPx,
		},
		TrimXPx:      repaired.TrimXPx,
		TrimYPx:      repaired.TrimYPx,
		TrimWidthPx:  repaired.TrimWidthPx,
		TrimHeightPx: repaired.TrimHeightPx,
		Diagnosis:    string(diagnosisJSON),
	}

	ok, err := w.Store.CompleteRepair(ctx, job.ID, w.ID, job.InputCaseVersion, outcome, findings, artworkStatus, proofStatus)
	if err != nil {
		log.Printf("job %s: failed to commit repair result: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed during repair - result discarded")
	}
}

// completeRejectedRepair records an ineligible repair attempt (precondition
// not met, or services/image-python's own eligibility check failed) without
// ever touching the original asset or storage - CompleteRepair's ineligible
// branch only writes the repairs row and moves the order to NEEDS_REVIEW.
func (w *Worker) completeRejectedRepair(ctx context.Context, job *store.Job, asset *store.Asset, reason string) {
	outcome := store.RepairOutcome{
		Repaired:      false,
		Reason:        reason,
		SourceAssetID: asset.ID,
		SourceHash:    asset.SHA256,
	}
	ok, err := w.Store.CompleteRepair(ctx, job.ID, w.ID, job.InputCaseVersion, outcome, nil, "", "")
	if err != nil {
		log.Printf("job %s: failed to commit rejected repair: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed while recording rejected repair")
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
