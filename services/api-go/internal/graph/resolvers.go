package graph

import (
	"errors"
	"fmt"

	"github.com/graphql-go/graphql"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
)

func (r *Resolver) resolveOrder(p graphql.ResolveParams) (interface{}, error) {
	id, _ := p.Args["id"].(string)
	o, err := r.Store.GetOrder(p.Context, id)
	if err != nil || o == nil {
		return nil, err
	}
	return orderToMap(o), nil
}

func (r *Resolver) resolveJob(p graphql.ResolveParams) (interface{}, error) {
	id, _ := p.Args["id"].(string)
	j, err := r.Store.GetJob(p.Context, id)
	if err != nil || j == nil {
		return nil, err
	}
	return jobToMap(j), nil
}

func (r *Resolver) resolveOrderFindings(p graphql.ResolveParams) (interface{}, error) {
	src := p.Source.(map[string]interface{})
	orderID := src["id"].(string)
	findings, err := r.Store.ListFindings(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(findings))
	for i := range findings {
		out = append(out, findingToMap(&findings[i]))
	}
	return out, nil
}

func (r *Resolver) resolveOrderJobs(p graphql.ResolveParams) (interface{}, error) {
	src := p.Source.(map[string]interface{})
	orderID := src["id"].(string)
	jobs, err := r.Store.ListJobsForOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		out = append(out, jobToMap(&jobs[i]))
	}
	return out, nil
}

func (r *Resolver) resolveOrderAssets(p graphql.ResolveParams) (interface{}, error) {
	src := p.Source.(map[string]interface{})
	orderID := src["id"].(string)
	assets, err := r.Store.ListAssetsForOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(assets))
	for i := range assets {
		out = append(out, assetToMap(&assets[i]))
	}
	return out, nil
}

func (r *Resolver) resolveOrderRepairs(p graphql.ResolveParams) (interface{}, error) {
	src := p.Source.(map[string]interface{})
	orderID := src["id"].(string)
	repairs, err := r.Store.ListRepairsForOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(repairs))
	for i := range repairs {
		out = append(out, repairToMap(&repairs[i]))
	}
	return out, nil
}

func (r *Resolver) resolveCreateOrder(p graphql.ResolveParams) (interface{}, error) {
	input, ok := p.Args["input"].(map[string]interface{})
	if !ok {
		return nil, errors.New("missing input")
	}

	in := store.CreateOrderInput{
		OwnerID:        input["ownerId"].(string),
		ProductType:    input["productType"].(string),
		DeclaredWidth:  input["declaredWidth"].(float64),
		DeclaredHeight: input["declaredHeight"].(float64),
		DeclaredUnit:   input["declaredUnit"].(string),
	}
	if v, ok := input["customerRequest"].(string); ok {
		in.CustomerRequest = &v
	}
	if v, ok := input["intent"].(string); ok {
		in.Intent = &v
	}

	o, err := r.Store.CreateOrder(p.Context, in)
	if err != nil {
		return nil, err
	}
	return orderToMap(o), nil
}

func (r *Resolver) resolveCreateUpload(p graphql.ResolveParams) (interface{}, error) {
	orderID := p.Args["orderId"].(string)
	contentType := p.Args["contentType"].(string)

	o, err := r.Store.GetOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("order %s not found", orderID)
	}

	uploadURL, expiresAt, err := r.Uploads.CreateTicket(orderID, contentType)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"uploadUrl": uploadURL,
		"expiresAt": fmtTime(expiresAt),
	}, nil
}

func (r *Resolver) resolveStartResolution(p graphql.ResolveParams) (interface{}, error) {
	orderID := p.Args["orderId"].(string)

	o, err := r.Store.GetOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("order %s not found", orderID)
	}

	if o.CurrentAssetID == nil {
		return nil, errors.New("no artwork uploaded for this order yet")
	}
	// The CURRENT asset, not "whatever was most recently uploaded" - after a
	// successful repair this is the repaired canvas, not the pristine
	// original. Falling back to the original here would reintroduce
	// whatever blocker the repair cleared (e.g. missing bleed).
	asset, err := r.Store.GetAsset(p.Context, *o.CurrentAssetID)
	if err != nil {
		return nil, err
	}
	if asset == nil {
		return nil, fmt.Errorf("order %s: current asset %s no longer exists", orderID, *o.CurrentAssetID)
	}

	// The job freezes which asset and case_version it applies to right now;
	// the worker will act on exactly this asset even if a newer one is
	// uploaded before it runs.
	job, err := r.Store.CreateJob(p.Context, orderID, "inspect", asset.ID, o.CaseVersion, nil)
	if err != nil {
		return nil, err
	}
	if job == nil {
		// An inspect job is already QUEUED/RUNNING for this order - the
		// partial unique index rejected the insert. Idempotent no-op:
		// return the in-flight job instead of erroring or duplicating it.
		job, err = r.Store.GetActiveJob(p.Context, orderID, "inspect")
		if err != nil {
			return nil, err
		}
		if job == nil {
			return nil, errors.New("failed to start or find an active resolution job")
		}
	}
	return jobToMap(job), nil
}

func (r *Resolver) resolveConfirmTrim(p graphql.ResolveParams) (interface{}, error) {
	orderID := p.Args["orderId"].(string)
	artworkIsTrimOnly := p.Args["artworkIsTrimOnly"].(bool)
	expectedCaseVersion := p.Args["caseVersion"].(int)

	ok, err := r.Store.ConfirmTrim(p.Context, orderID, artworkIsTrimOnly, expectedCaseVersion)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("order %s: case_version %d is stale, refresh and retry", orderID, expectedCaseVersion)
	}

	o, err := r.Store.GetOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("order %s not found", orderID)
	}
	return orderToMap(o), nil
}

func (r *Resolver) resolveAnswerClarification(p graphql.ResolveParams) (interface{}, error) {
	return nil, errors.New("answerClarification is not implemented until Day 4")
}

func (r *Resolver) resolveRequestRepair(p graphql.ResolveParams) (interface{}, error) {
	orderID := p.Args["orderId"].(string)
	idempotencyKey := p.Args["idempotencyKey"].(string)
	expectedCaseVersion := p.Args["caseVersion"].(int)

	o, err := r.Store.GetOrder(p.Context, orderID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("order %s not found", orderID)
	}

	// Idempotency is checked BEFORE case_version freshness: an exact retry
	// of a request that already completed must succeed even though
	// case_version has since moved on - often as a direct result of that
	// same repair completing. Checking case_version first would make every
	// post-completion retry fail as "stale".
	var existingCaseVersion *int
	existingRepair, err := r.Store.GetRepairByIdempotencyKey(p.Context, orderID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	var existingJob *store.Job
	if existingRepair != nil {
		if existingRepair.JobID == nil {
			return nil, errors.New("repair already recorded but has no associated job")
		}
		existingJob, err = r.Store.GetJob(p.Context, *existingRepair.JobID)
		if err != nil {
			return nil, err
		}
		if existingJob == nil {
			return nil, errors.New("repair's job record is missing")
		}
		existingCaseVersion = &existingJob.InputCaseVersion
	}

	activeJob, err := r.Store.GetActiveJob(p.Context, orderID, "repair")
	if err != nil {
		return nil, err
	}

	state := repairRequestState{
		ExistingRepairJobCaseVersion: existingCaseVersion,
		OrderCaseVersion:             o.CaseVersion,
	}
	if activeJob != nil {
		state.HasActiveJob = true
		state.ActiveJobIdempotencyKey = activeJob.IdempotencyKey
		state.ActiveJobInputCaseVersion = activeJob.InputCaseVersion
	}

	switch decideRepairRequestAction(state, idempotencyKey, expectedCaseVersion) {
	case actionReturnExistingJob:
		if existingJob != nil {
			return jobToMap(existingJob), nil
		}
		return jobToMap(activeJob), nil

	case actionRejectCaseVersionMismatch:
		usedVersion := 0
		if existingCaseVersion != nil {
			usedVersion = *existingCaseVersion
		} else if activeJob != nil {
			usedVersion = activeJob.InputCaseVersion
		}
		return nil, fmt.Errorf(
			"idempotency key %q was already used to start a repair at case_version %d, not %d - "+
				"replay the exact original request to retry, or use a new key for a new repair",
			idempotencyKey, usedVersion, expectedCaseVersion)

	case actionRejectDifferentRepairInProgress:
		return nil, errors.New("a different repair is already in progress for this order - wait for it to finish before retrying")

	case actionRejectStaleCaseVersion:
		return nil, fmt.Errorf("order %s: case_version %d is stale (current %d), refresh and retry", orderID, expectedCaseVersion, o.CaseVersion)
	}

	// actionCreateNew
	if o.CurrentAssetID == nil {
		return nil, errors.New("no artwork uploaded for this order yet")
	}
	// Repair always targets the pristine original, never a previously
	// repaired canvas - v1 supports exactly one repair per case.
	asset, err := r.Store.LatestAssetByKind(p.Context, orderID, "original")
	if err != nil {
		return nil, err
	}
	if asset == nil {
		return nil, errors.New("no artwork uploaded for this order yet")
	}

	job, err := r.Store.CreateJob(p.Context, orderID, "repair", asset.ID, o.CaseVersion, &idempotencyKey)
	if err != nil {
		return nil, err
	}
	if job == nil {
		// Race: a concurrent call created the active job between our check
		// above and this insert. Look it up instead of erroring.
		job, err = r.Store.GetActiveJob(p.Context, orderID, "repair")
		if err != nil {
			return nil, err
		}
		if job == nil {
			return nil, errors.New("failed to start or find an active repair job")
		}
	}
	return jobToMap(job), nil
}
