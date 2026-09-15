package graph

// repairRequestAction is what resolveRequestRepair should do, decided by
// decideRepairRequestAction below - a pure function of already-fetched
// state, kept separate from the DB calls so it's unit-testable without a
// live Postgres.
type repairRequestAction int

const (
	// actionReturnExistingJob: an identical retry (same idempotency key,
	// same case_version the key was originally used with) - return the job
	// that already exists/existed for it, whether still in flight or
	// completed. This must succeed even if the order's case_version has
	// since moved on, including as a direct RESULT of that same repair
	// completing - that's what makes a post-completion retry work.
	actionReturnExistingJob repairRequestAction = iota

	// actionRejectCaseVersionMismatch: the idempotency key was already used
	// (in flight or completed) with a DIFFERENT case_version than this
	// request supplies - reuse with different inputs, not a legitimate
	// retry. Reject rather than silently returning the old result or
	// accepting the new version.
	actionRejectCaseVersionMismatch

	// actionRejectDifferentRepairInProgress: a different repair (different
	// idempotency key) is already in flight for this order. v1 only
	// supports one at a time.
	actionRejectDifferentRepairInProgress

	// actionRejectStaleCaseVersion: no repair (in flight or completed)
	// exists for this key - this is a genuinely new request, but the
	// order's case_version has moved past what the caller supplied.
	actionRejectStaleCaseVersion

	// actionCreateNew: genuinely new request, case_version is current -
	// proceed to enqueue a new repair job.
	actionCreateNew
)

// repairRequestState is everything decideRepairRequestAction needs, already
// fetched by the caller.
type repairRequestState struct {
	// ExistingRepairJobCaseVersion is the InputCaseVersion of the job that
	// produced a COMPLETED repair (REPAIRED or REJECTED) for this
	// order+key, if any. A completed repair always takes precedence over
	// any in-flight job info below.
	ExistingRepairJobCaseVersion *int

	// HasActiveJob describes an in-flight (QUEUED/RUNNING) repair job for
	// this order, if any - checked only when ExistingRepairJobCaseVersion
	// is nil.
	HasActiveJob              bool
	ActiveJobIdempotencyKey   *string
	ActiveJobInputCaseVersion int

	// OrderCaseVersion is the order's current case_version, used only when
	// neither a completed repair nor an in-flight job exists for this key.
	OrderCaseVersion int
}

func decideRepairRequestAction(state repairRequestState, requestedKey string, requestedCaseVersion int) repairRequestAction {
	if state.ExistingRepairJobCaseVersion != nil {
		if *state.ExistingRepairJobCaseVersion != requestedCaseVersion {
			return actionRejectCaseVersionMismatch
		}
		return actionReturnExistingJob
	}

	if state.HasActiveJob {
		if state.ActiveJobIdempotencyKey == nil || *state.ActiveJobIdempotencyKey != requestedKey {
			return actionRejectDifferentRepairInProgress
		}
		if state.ActiveJobInputCaseVersion != requestedCaseVersion {
			return actionRejectCaseVersionMismatch
		}
		return actionReturnExistingJob
	}

	if state.OrderCaseVersion != requestedCaseVersion {
		return actionRejectStaleCaseVersion
	}
	return actionCreateNew
}
