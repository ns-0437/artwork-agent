// Package worker implements the one real execution path: claim a queued job,
// fetch the order's artwork from storage, call the Python image service, and
// persist the result. Day 2 replaces runInspect's body with real findings and
// the short-circuit-to-RESOLVED logic; the claim/dispatch/complete plumbing
// here does not change.
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

	inspected, err := w.PyImage.Inspect(data, asset.StorageKey, asset.ContentType)
	if err != nil {
		w.fail(ctx, job, "image service inspect failed: "+err.Error())
		return
	}
	log.Printf("job %s inspected order %s: %dx%d %s (%s)", job.ID, job.OrderID, inspected.WidthPx, inspected.HeightPx, inspected.Mode, inspected.Format)

	// No checks are implemented yet (Day 2), so nothing could resolve the
	// case - it stays BLOCKED. The asset's dimensions, the job's result, and
	// the order's state advance all commit together in one transaction,
	// gated on this worker still holding a valid lease and the case not
	// having moved on since this job was bound to job.InputCaseVersion. See
	// store.CompleteInspection.
	ok, err := w.Store.CompleteInspection(ctx, job.ID, w.ID, *job.InputAssetID, job.InputCaseVersion, store.InspectionResult{
		WidthPx:  inspected.WidthPx,
		HeightPx: inspected.HeightPx,
		Mode:     inspected.Mode,
		Format:   inspected.Format,
	}, "BLOCKED", "NOT_PREPARED")
	if err != nil {
		log.Printf("job %s: failed to commit inspection result: %v", job.ID, err)
		return
	}
	if !ok {
		w.fail(ctx, job, "lease lost or case_version changed during inspection - result discarded")
	}
}

func (w *Worker) fail(ctx context.Context, job *store.Job, msg string) {
	log.Printf("job %s failed: %s", job.ID, msg)
	if _, err := w.Store.FailJob(ctx, job.ID, w.ID, msg); err != nil {
		log.Printf("job %s: failed to record failure: %v", job.ID, err)
	}
}
