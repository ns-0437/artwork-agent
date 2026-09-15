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

	"github.com/ns-0437/artwork-agent/services/api-go/internal/agent"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/pyclient"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/storage"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
)

const (
	pollInterval  = 2 * time.Second
	leaseDuration = 30 * time.Second

	// Bounded agent loop caps, per the brief: "Cap each execution segment at
	// five tool calls and two transient retries." Persisted on the order
	// (agent_tool_calls_used/agent_retries_used) so the cap survives across
	// replies, not just within one process lifetime.
	maxAgentToolCalls        = 5
	maxAgentTransientRetries = 2
)

type Worker struct {
	ID      string
	Store   *store.Store
	Storage storage.Storage
	PyImage *pyclient.Client

	// Agent is nil-able: when unset, the worker runs deterministic-only
	// (Day 2/3 behavior) and never attempts a clarification/auto-repair
	// decision - useful for tests and for local runs without a provider
	// key configured.
	Agent agent.Provider
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

	// If this job's asset IS the order's current asset and the order
	// already has known trim coordinates (set by a prior successful
	// repair), pass them explicitly - the same as the repair's own
	// recheck does. Without this, re-inspecting a repaired canvas would
	// fall back to the artwork_is_trim_only-based inference, which treats
	// the WHOLE canvas as the trim and would measure zero bleed margin,
	// reintroducing the blocker the repair just cleared. Only apply this
	// when the asset actually matches - a job bound to a stale (pre-repair)
	// asset must not borrow trim coordinates that describe a different one.
	var trimWidthPx, trimHeightPx *int
	if order.CurrentAssetID != nil && *order.CurrentAssetID == asset.ID && order.TrimWidthPx != nil && order.TrimHeightPx != nil {
		tw := int(*order.TrimWidthPx)
		th := int(*order.TrimHeightPx)
		trimWidthPx = &tw
		trimHeightPx = &th
	}

	inspected, err := w.PyImage.Inspect(data, asset.StorageKey, asset.ContentType, pyclient.InspectInput{
		DeclaredWidth:     order.DeclaredWidth,
		DeclaredHeight:    order.DeclaredHeight,
		DeclaredUnit:      order.DeclaredUnit,
		Intent:            intent,
		ArtworkIsTrimOnly: order.ArtworkIsTrimOnly,
		TrimWidthPx:       trimWidthPx,
		TrimHeightPx:      trimHeightPx,
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
		return
	}

	// The agent loop runs AFTER the deterministic commit, using its result
	// as input - it never runs instead of it, and never touches findings or
	// status itself (CLAUDE.md point 1). RESOLVED needs no further action.
	if artworkStatus != "RESOLVED" {
		w.runAgentDecision(ctx, job.OrderID)
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

// trimOnlyClarificationQuestion is deliberately phrased so "yes" means
// artwork_is_trim_only=true, matching interpretYesNo's direct mapping in
// internal/graph/resolvers.go - see the comment where it's used below.
const trimOnlyClarificationQuestion = "Is your uploaded artwork trim-only - meaning it does NOT yet include the printer's required bleed margin? Please answer yes or no."

// runAgentDecision is step 3 of the brief's bounded loop: "Ask one targeted
// clarification, repair an eligible case, or escalate." It runs once, after
// the deterministic checks have already committed a non-RESOLVED status -
// it reads that committed result as input and never recomputes or
// second-guesses it (CLAUDE.md point 1). With no provider configured, this
// is a no-op: the case simply stays at whatever decideArtworkStatus already
// set (BLOCKED/NEEDS_REVIEW), same as Day 2/3 behavior.
func (w *Worker) runAgentDecision(ctx context.Context, orderID string) {
	if w.Agent == nil {
		return
	}

	order, err := w.Store.GetOrder(ctx, orderID)
	if err != nil || order == nil {
		log.Printf("order %s: failed to load order for agent decision: %v", orderID, err)
		return
	}

	if order.AgentToolCallsUsed >= maxAgentToolCalls {
		log.Printf("order %s: agent tool-call budget exhausted (%d used) - escalating", orderID, order.AgentToolCallsUsed)
		w.escalateBudgetExhausted(ctx, order)
		return
	}

	findings, err := w.Store.ListFindings(ctx, orderID)
	if err != nil {
		log.Printf("order %s: failed to load findings for agent decision: %v", orderID, err)
		return
	}

	intent := "border"
	if order.Intent != nil {
		intent = *order.Intent
	}

	input := agent.DecisionInput{
		OrderID:        orderID,
		ProductType:    order.ProductType,
		DeclaredWidth:  order.DeclaredWidth,
		DeclaredHeight: order.DeclaredHeight,
		DeclaredUnit:   order.DeclaredUnit,
		Intent:         intent,
		Findings:       toFindingSummaries(latestFindingsByCheck(findings)),
	}

	var decision agent.Decision
	var decideErr error
	retries := 0
	for {
		decision, decideErr = w.Agent.Decide(ctx, input)
		if decideErr == nil {
			break
		}
		if retries >= maxAgentTransientRetries {
			break
		}
		retries++
		if err := w.Store.RecordAgentRetry(ctx, orderID, toolEventDetail(map[string]interface{}{"error": decideErr.Error()})); err != nil {
			log.Printf("order %s: failed to record agent retry: %v", orderID, err)
		}
	}

	callDetail := map[string]interface{}{"action": decision.Action}
	if decision.Question != "" {
		callDetail["model_suggested_question"] = decision.Question
	}
	if decideErr != nil {
		callDetail["error"] = decideErr.Error()
	}
	if err := w.Store.RecordAgentToolCall(ctx, orderID, toolEventDetail(callDetail)); err != nil {
		log.Printf("order %s: failed to record agent tool_event: %v", orderID, err)
	}

	if decideErr != nil {
		log.Printf("order %s: agent decision failed after %d retries: %v - escalating", orderID, retries, decideErr)
		w.escalateBudgetExhausted(ctx, order)
		return
	}

	switch decision.Action {
	case agent.ActionAskClarification:
		// The PERSISTED question is always this fixed, deliberately-polarized
		// text - never the model's free-form suggestion (logged above for
		// audit only). v1 supports exactly one clarification type
		// (trim-only confirmation), and answerClarification's interpretYesNo
		// maps "yes" straight to artwork_is_trim_only=true: if the model's
		// own phrasing were used instead, an equally sensible question with
		// the OPPOSITE polarity (e.g. "does it already include bleed?")
		// would invert that mapping and silently confirm the wrong thing.
		// Fixing the wording removes that entire class of risk rather than
		// trying to parse or normalize whatever the model asked.
		question := trimOnlyClarificationQuestion
		ok, err := w.Store.CreateClarificationAndAwait(ctx, orderID, question, order.CaseVersion)
		if err != nil {
			log.Printf("order %s: failed to persist clarification: %v", orderID, err)
			return
		}
		if !ok {
			log.Printf("order %s: case_version changed before clarification could be persisted - dropping this decision", orderID)
		}

	case agent.ActionRequestRepair:
		w.triggerAutoRepair(ctx, order)

	case agent.ActionEscalate:
		// decideArtworkStatus already left this NEEDS_REVIEW or BLOCKED;
		// escalate means "no further automated action", not a status change.
	}
}

// triggerAutoRepair enqueues the same repair job requestRepair would, with a
// system-generated idempotency key - runRepair's own precondition check
// (intent=full_bleed AND artwork_is_trim_only=true) is the real safety net
// here: if the agent's judgment is wrong, the job is safely REJECTED with a
// specific reason rather than corrupting anything (see worker.runRepair).
func (w *Worker) triggerAutoRepair(ctx context.Context, order *store.Order) {
	if order.CurrentAssetID == nil {
		log.Printf("order %s: agent chose request_repair but there is no current asset", order.ID)
		return
	}
	asset, err := w.Store.LatestAssetByKind(ctx, order.ID, "original")
	if err != nil || asset == nil {
		log.Printf("order %s: agent chose request_repair but no original asset was found: %v", order.ID, err)
		return
	}
	key := "agent-auto-" + order.ID + "-" + asset.ID
	if _, err := w.Store.CreateJob(ctx, order.ID, "repair", asset.ID, order.CaseVersion, &key); err != nil {
		log.Printf("order %s: failed to enqueue agent-triggered repair: %v", order.ID, err)
	}
}

func (w *Worker) escalateBudgetExhausted(ctx context.Context, order *store.Order) {
	if _, err := w.Store.EscalateToNeedsReview(ctx, order.ID, order.CaseVersion); err != nil {
		log.Printf("order %s: failed to escalate after exhausted agent budget: %v", order.ID, err)
	}
}

func toolEventDetail(v map[string]interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(b)
}

// latestFindingsByCheck keeps only the most recent finding per check_name -
// findings are append-only across every inspection this order has ever had,
// but the agent's decision must be based on the CURRENT state, not history.
func latestFindingsByCheck(findings []store.Finding) []store.Finding {
	latest := map[string]store.Finding{}
	for _, f := range findings {
		existing, ok := latest[f.CheckName]
		if !ok || f.CreatedAt.After(existing.CreatedAt) {
			latest[f.CheckName] = f
		}
	}
	out := make([]store.Finding, 0, len(latest))
	for _, f := range latest {
		out = append(out, f)
	}
	return out
}

func toFindingSummaries(findings []store.Finding) []agent.FindingSummary {
	out := make([]agent.FindingSummary, 0, len(findings))
	for _, f := range findings {
		out = append(out, agent.FindingSummary{
			CheckName: f.CheckName,
			Result:    f.Result,
			Evidence:  f.Evidence,
		})
	}
	return out
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
// A WARNING never blocks; NEEDS_REVIEW always does; NEEDS_INPUT blocks. This
// function itself never sets AWAITING_CLARIFICATION - it only decides
// whether the deterministic result blocks the case, staying at BLOCKED for
// NEEDS_INPUT. Moving to AWAITING_CLARIFICATION (and actually asking a
// question) is the agent loop's job (worker.runAgentDecision), a SEPARATE
// step that runs after this function's result has already committed.
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
