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
	asset, err := w.Store.LatestAssetByKind(ctx, job.OrderID, "original")
	if err != nil {
		w.fail(ctx, job, "failed to look up original artwork asset: "+err.Error())
		return
	}
	if asset == nil {
		w.fail(ctx, job, "no original artwork asset found for this order")
		return
	}

	data, err := w.Storage.Get(ctx, asset.StorageKey)
	if err != nil {
		w.fail(ctx, job, "failed to read artwork from storage: "+err.Error())
		return
	}

	result, err := w.PyImage.Inspect(data, asset.StorageKey, asset.ContentType)
	if err != nil {
		w.fail(ctx, job, "image service inspect failed: "+err.Error())
		return
	}
	log.Printf("job %s inspected order %s: %dx%d %s (%s)", job.ID, job.OrderID, result.WidthPx, result.HeightPx, result.Mode, result.Format)

	// No checks are implemented yet (Day 2), so there is nothing that could
	// resolve the case - it stays BLOCKED. Day 2 adds real findings here and
	// the short-circuit to RESOLVED when every check is PASS/WARNING-only.
	if err := w.Store.AdvanceOrderState(ctx, job.OrderID, "BLOCKED", "NOT_PREPARED"); err != nil {
		w.fail(ctx, job, "failed to advance order state: "+err.Error())
		return
	}

	if ok, err := w.Store.CompleteJob(ctx, job.ID, w.ID); err != nil {
		log.Printf("job %s: failed to mark complete: %v", job.ID, err)
	} else if !ok {
		log.Printf("job %s: lease was lost before completion could be recorded", job.ID)
	}
}

func (w *Worker) fail(ctx context.Context, job *store.Job, msg string) {
	log.Printf("job %s failed: %s", job.ID, msg)
	if _, err := w.Store.FailJob(ctx, job.ID, w.ID, msg); err != nil {
		log.Printf("job %s: failed to record failure: %v", job.ID, err)
	}
}
