// Package worker implements the one real execution path: claim a queued job,
// fetch the order's artwork from storage, call the Python image service, and
// persist the result and findings.
package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"strings"
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

	// agentDecisionDeadline bounds the TOTAL wall-clock time runAgentDecision
	// may spend calling the provider, across the initial attempt and every
	// retry combined - not per attempt. This must stay comfortably below
	// leaseDuration: three attempts at the adapter's own HTTP timeout
	// (agent.groqHTTPTimeout) could otherwise exceed a 30s lease outright,
	// and once a lease expires mid-call, ClaimNextJob will hand the SAME job
	// to a different worker while the first is still mid-flight - wasted
	// work, not corruption (every completion path is still gated on
	// worker_id+lease), but real waste worth preventing rather than
	// tolerating. Deriving a bounded context and threading it through every
	// Decide call (see decideWithBoundedRetries) means a slow or hanging
	// provider can never keep a job RUNNING past its lease, regardless of
	// how many retries fire - this is enforced by the code, not by hoping
	// two constants (lease duration, HTTP timeout) stay in sync by hand.
	agentDecisionDeadline = 20 * time.Second
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
	case "agent_decide":
		w.runAgentDecision(ctx, job)
	case "prepare_proof":
		w.runPrepareProof(ctx, job)
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

	// Whether to enqueue a follow-up job is decided HERE and passed into the
	// SAME transaction as the commit below - a separate "commit, then
	// enqueue" step (the previous design) left a durability gap: a crash
	// between the two committed the inspection with no queued work to
	// resume it. Exactly one of the two follow-ups applies: RESOLVED means
	// the loop's last remaining step is preparing a proof; anything else
	// (with a provider configured) means the agent needs to look at it.
	enqueueAgentDecision := w.Agent != nil && artworkStatus != "RESOLVED"
	enqueuePrepareProof := artworkStatus == "RESOLVED"

	// The asset's dimensions, the job's result, every finding, the order's
	// state advance, and (if applicable) the follow-up job all commit
	// together in one transaction, gated on this worker still holding a
	// valid lease and the case not having moved on since this job was bound
	// to job.InputCaseVersion. See store.CompleteInspection.
	ok, err := w.Store.CompleteInspection(ctx, job.ID, w.ID, *job.InputAssetID, job.InputCaseVersion, store.InspectionResult{
		WidthPx:  inspected.WidthPx,
		HeightPx: inspected.HeightPx,
		Mode:     inspected.Mode,
		Format:   inspected.Format,
	}, findings, artworkStatus, proofStatus, enqueueAgentDecision, enqueuePrepareProof)
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

	// Same reasoning as runInspect's own enqueuePrepareProof: decided here,
	// committed in the SAME transaction as the repair result, so a crash
	// between "repair verified and resolved" and "proof prepared" leaves a
	// queued job to resume rather than a stalled case.
	enqueuePrepareProof := artworkStatus == "RESOLVED"

	ok, err := w.Store.CompleteRepair(ctx, job.ID, w.ID, job.InputCaseVersion, outcome, findings, artworkStatus, proofStatus, enqueuePrepareProof)
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
	ok, err := w.Store.CompleteRepair(ctx, job.ID, w.ID, job.InputCaseVersion, outcome, nil, "", "", false)
	if err != nil {
		log.Printf("job %s: failed to commit rejected repair: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed while recording rejected repair")
	}
}

// runPrepareProof is loop step 5 of the brief ("Prepare a proof, update the
// artwork state, and retain the complete audit trail") - the one step that
// was not implemented through Day 4. It runs as its own durable job (see
// runInspect/runRepair's enqueuePrepareProof), enqueued atomically in the
// SAME transaction as the commit that first reached RESOLVED, so a crash
// between "case resolved" and "proof prepared" leaves a queued job to
// resume rather than a case stuck at RESOLVED with proof_status stuck at
// NOT_PREPARED forever.
func (w *Worker) runPrepareProof(ctx context.Context, job *store.Job) {
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

	rendered, err := w.PyImage.PrepareProof(data, asset.StorageKey, pyclient.ProofInput{
		OrderID:        job.OrderID,
		ArtworkVersion: order.ArtworkVersion,
		CaseVersion:    job.InputCaseVersion,
	})
	if err != nil {
		w.fail(ctx, job, "image service proof rendering failed: "+err.Error())
		return
	}

	imageBytes, err := base64.StdEncoding.DecodeString(rendered.ImageBase64)
	if err != nil {
		w.fail(ctx, job, "failed to decode rendered proof: "+err.Error())
		return
	}
	storageKey, hash, err := w.Storage.Put(ctx, imageBytes)
	if err != nil {
		w.fail(ctx, job, "failed to store proof image: "+err.Error())
		return
	}

	ok, err := w.Store.CompleteProofPreparation(ctx, job.ID, w.ID, job.InputCaseVersion, store.ProofAssetInput{
		StorageKey: storageKey,
		SHA256:     hash,
		WidthPx:    rendered.WidthPx,
		HeightPx:   rendered.HeightPx,
	})
	if err != nil {
		log.Printf("job %s: failed to commit prepared proof: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed while preparing proof")
	}
}

// trimOnlyClarificationQuestion is deliberately phrased so "yes" means
// artwork_is_trim_only=true, matching interpretYesNo's direct mapping in
// internal/graph/resolvers.go - see the comment where it's used below.
const trimOnlyClarificationQuestion = "Is your uploaded artwork trim-only - meaning it does NOT yet include the printer's required bleed margin? Please answer yes or no."

// decideWithBoundedRetries calls provider.Decide once, then retries only for
// transient errors (never permanent ones - bad auth or a malformed request
// won't succeed on retry) up to whatever reserveRetry's budget allows,
// STOPPING as soon as ctx's deadline has passed rather than attempting
// another retry - this is the enforcement point for agentDecisionDeadline.
// Extracted as its own function (independent of *Worker/*store.Store) so it
// can be unit-tested with a fake provider and a short ctx timeout, without a
// real database or network - see worker_test.go.
func decideWithBoundedRetries(
	ctx context.Context,
	provider agent.Provider,
	input agent.DecisionInput,
	reserveRetry func() (bool, error),
	onAttempt func(agent.Decision, error),
) (agent.Decision, error) {
	decision, err := provider.Decide(ctx, input)
	onAttempt(decision, err)

	for err != nil && agent.IsTransient(err) {
		if ctx.Err() != nil {
			break // deadline already exhausted - the caller escalates rather than hanging further
		}
		reserved, rErr := reserveRetry()
		if rErr != nil || !reserved {
			break
		}
		decision, err = provider.Decide(ctx, input)
		onAttempt(decision, err)
	}
	return decision, err
}

// runAgentDecision is step 3 of the brief's bounded loop: "Ask one targeted
// clarification, repair an eligible case, or escalate." It processes ONE
// agent_decide job, itself a durable, resumable unit of work created
// atomically alongside the inspection it decides about (CompleteInspection).
// It reads that inspection's findings via job.AgentSourceJobID - never "the
// latest finding per check across this order's entire history" - and never
// recomputes or second-guesses them (CLAUDE.md point 1).
//
// Every terminal path (success, budget exhaustion, a failed decision, or a
// decision Go's own validation rejects) ends in EXACTLY ONE call to
// CompleteAgentDecision, which commits the job's result and applies the
// decision atomically - there is no path where this function returns having
// left the job SUCCEEDED/FAILED with no corresponding state change, or vice
// versa.
func (w *Worker) runAgentDecision(ctx context.Context, job *store.Job) {
	escalate := func(reason string) {
		log.Printf("job %s: escalating: %s", job.ID, reason)
		ok, err := w.Store.CompleteAgentDecision(ctx, job.ID, w.ID, job.InputCaseVersion, store.AgentDecisionOutcome{
			Action: agent.ActionEscalate,
			Reason: reason,
		})
		if err != nil {
			log.Printf("job %s: failed to commit escalation: %v", job.ID, err)
			return
		}
		if !ok {
			w.fail(ctx, job, "lease lost or case_version changed while escalating ("+reason+")")
		}
	}

	if w.Agent == nil {
		escalate("no agent provider configured")
		return
	}
	if job.AgentSourceJobID == nil {
		escalate("agent_decide job has no source job to read findings from")
		return
	}

	order, err := w.Store.GetOrder(ctx, job.OrderID)
	if err != nil || order == nil {
		// Can't even confirm the order exists - don't guess at escalating.
		// Leave the job leased; if this is transient, the lease will expire
		// and another attempt (this worker or another) will retry it.
		log.Printf("job %s: failed to load order: %v", job.ID, err)
		return
	}

	findings, err := w.Store.ListFindingsForJob(ctx, *job.AgentSourceJobID)
	if err != nil {
		log.Printf("job %s: failed to load findings: %v", job.ID, err)
		return
	}
	if len(findings) == 0 {
		escalate("no findings recorded for the source inspection")
		return
	}

	intent := "border"
	if order.Intent != nil {
		intent = *order.Intent
	}

	input := agent.DecisionInput{
		OrderID:        job.OrderID,
		ProductType:    order.ProductType,
		DeclaredWidth:  order.DeclaredWidth,
		DeclaredHeight: order.DeclaredHeight,
		DeclaredUnit:   order.DeclaredUnit,
		Intent:         intent,
		Findings:       toFindingSummaries(findings),
	}

	// Reserve BEFORE calling the provider, not after - an unreserved call
	// can't be accounted for. If reservation itself errors, don't guess
	// that the provider is safe to call anyway.
	reserved, err := w.Store.ReserveAgentToolCall(ctx, job.OrderID, maxAgentToolCalls)
	if err != nil {
		log.Printf("job %s: failed to reserve agent tool-call budget: %v", job.ID, err)
		return
	}
	if !reserved {
		escalate("agent tool-call budget exhausted")
		return
	}

	// agentCtx bounds the ENTIRE retry sequence below (point on
	// agentDecisionDeadline) - derived from the job's outer ctx so it's
	// still cancelled if the worker itself shuts down, but with its own
	// tighter deadline so a slow/hanging provider can never hold this job
	// RUNNING anywhere near the job's lease expiry. Store calls (budget
	// reservation, logging) deliberately keep using the OUTER ctx, not
	// agentCtx - those are quick DB round trips that should still complete
	// (and be attempted) even after the provider-call deadline has passed.
	agentCtx, cancel := context.WithTimeout(ctx, agentDecisionDeadline)
	defer cancel()

	decision, decideErr := decideWithBoundedRetries(agentCtx, w.Agent, input,
		func() (bool, error) { return w.Store.ReserveAgentRetry(ctx, job.OrderID, maxAgentTransientRetries) },
		func(d agent.Decision, e error) { w.logAgentAttempt(ctx, job.OrderID, d, e) },
	)

	if decideErr != nil {
		// The PERSISTED reason (visible over GraphQL/eval results/UI) is
		// always this short, fixed, credential-free label - never
		// decideErr.Error() itself, which can embed a provider's raw HTTP
		// response body. Full detail still goes to server logs (not
		// publicly queryable - see CLAUDE.md's known auth gaps) for actual
		// debugging. This is distinct on purpose from "model chose to
		// escalate" below: a failed provider call was never actually
		// evaluated by the model at all.
		category := agent.ErrorCategory(decideErr)
		log.Printf("job %s: agent provider call failed (category=%s): %v", job.ID, category, decideErr)
		escalate("provider error (" + category + ") -> escalated for review")
		return
	}

	outcome := store.AgentDecisionOutcome{Action: decision.Action}

	switch decision.Action {
	case agent.ActionAskClarification:
		// Only act on this when the ACTUAL findings show the one blocker
		// this system can clarify (trim not confirmed) - a model choosing
		// ask_clarification for anything else (e.g. low resolution) has no
		// question that would help, so escalate instead of asking a
		// nonsensical one. This check is independent of whatever the model
		// said - Go verifies it against the real findings, not the model's
		// stated reasoning.
		if !hasUnconfirmedTrimFinding(findings) {
			escalate("model chose ask_clarification but no unconfirmed-trim finding is present in these findings")
			return
		}
		// The PERSISTED question is always this fixed, deliberately-polarized
		// text - never the model's free-form suggestion (logged above for
		// audit only). v1 supports exactly one clarification type, and
		// answerClarification's interpretYesNo maps "yes" straight to
		// artwork_is_trim_only=true: if the model's own phrasing were used
		// instead, an equally sensible question with the OPPOSITE polarity
		// (e.g. "does it already include bleed?") would invert that mapping
		// and silently confirm the wrong thing.
		outcome.ClarificationQuestion = trimOnlyClarificationQuestion

	case agent.ActionRequestRepair:
		if !hasConfirmedInsufficientBleedFinding(findings) {
			escalate("model chose request_repair but no confirmed-trim insufficient-bleed finding is present in these findings")
			return
		}
		if order.CurrentAssetID == nil {
			escalate("model chose request_repair but there is no current asset")
			return
		}
		asset, err := w.Store.LatestAssetByKind(ctx, job.OrderID, "original")
		if err != nil || asset == nil {
			escalate("model chose request_repair but no original asset was found")
			return
		}
		outcome.RepairAssetID = asset.ID
		outcome.RepairIdempotencyKey = "agent-auto-" + job.OrderID + "-" + asset.ID

	case agent.ActionEscalate:
		outcome.Reason = "model chose to escalate"

	default:
		escalate("model returned an unrecognized action: " + decision.Action)
		return
	}

	ok, err := w.Store.CompleteAgentDecision(ctx, job.ID, w.ID, job.InputCaseVersion, outcome)
	if err != nil {
		log.Printf("job %s: failed to commit agent decision: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed while applying agent decision")
	}
}

func (w *Worker) logAgentAttempt(ctx context.Context, orderID string, decision agent.Decision, decideErr error) {
	detail := map[string]interface{}{}
	if decideErr != nil {
		// error_category only, not decideErr.Error() - toolEvents has no
		// auth gate (CLAUDE.md's known limitations), and a provider's raw
		// HTTP error body isn't something to persist/expose on spec. Full
		// detail still reaches server logs via the caller (runAgentDecision).
		detail["error_category"] = agent.ErrorCategory(decideErr)
		detail["transient"] = agent.IsTransient(decideErr)
	} else {
		detail["action"] = decision.Action
		if decision.Question != "" {
			detail["model_suggested_question"] = decision.Question
		}
		if decision.TokenUsage.TotalTokens > 0 {
			detail["token_usage"] = decision.TokenUsage
		}
	}
	if err := w.Store.LogAgentToolEvent(ctx, orderID, "tool_call", toolEventDetail(detail)); err != nil {
		log.Printf("order %s: failed to log agent tool_event: %v", orderID, err)
	}
}

// hasUnconfirmedTrimFinding reports whether the given findings actually
// contain the one blocker ask_clarification exists for - it doesn't matter
// what the model said, only what the deterministic findings say.
func hasUnconfirmedTrimFinding(findings []store.Finding) bool {
	for _, f := range findings {
		if f.Result == "NEEDS_INPUT" && strings.Contains(f.Evidence, "trim rectangle not confirmed") {
			return true
		}
	}
	return false
}

// hasConfirmedInsufficientBleedFinding reports whether the given findings
// actually contain a confirmed-trim, insufficient-margin bleed result -
// the one blocker request_repair exists for.
func hasConfirmedInsufficientBleedFinding(findings []store.Finding) bool {
	for _, f := range findings {
		if f.CheckName == "bleed" && f.Result == "NEEDS_REVIEW" && strings.Contains(f.Evidence, "available_bleed_in") {
			return true
		}
	}
	return false
}

func toolEventDetail(v map[string]interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(b)
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
