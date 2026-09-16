package worker

import (
	"context"
	"testing"
	"time"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/agent"
	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
)

func TestDecideArtworkStatus(t *testing.T) {
	cases := []struct {
		name        string
		findings    []store.FindingInput
		intent      string
		wantArtwork string
		wantProof   string
	}{
		{
			name:        "empty findings never resolves",
			findings:    nil,
			intent:      "border",
			wantArtwork: "NEEDS_REVIEW",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "missing bleed check for full_bleed intent blocks",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "PASS"},
				{CheckName: "color", Result: "WARNING"},
				// bleed is expected for full_bleed intent but absent here.
			},
			intent:      "full_bleed",
			wantArtwork: "NEEDS_REVIEW",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "unrecognized result string blocks",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "PASS"},
				{CheckName: "color", Result: "SORT_OF_OK"},
			},
			intent:      "border",
			wantArtwork: "NEEDS_REVIEW",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "unexpected check name for this intent blocks",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "PASS"},
				{CheckName: "color", Result: "WARNING"},
				{CheckName: "bleed", Result: "PASS"}, // border intent shouldn't carry a bleed finding at all
			},
			intent:      "border",
			wantArtwork: "NEEDS_REVIEW",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "all PASS/WARNING with complete expected set resolves, proof stays NOT_PREPARED",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "PASS"},
				{CheckName: "color", Result: "WARNING"},
			},
			intent:      "border",
			wantArtwork: "RESOLVED",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "complete full_bleed set, all clear, resolves",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "PASS"},
				{CheckName: "color", Result: "PASS"},
				{CheckName: "bleed", Result: "PASS"},
			},
			intent:      "full_bleed",
			wantArtwork: "RESOLVED",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "any NEEDS_INPUT blocks without escalating",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "NEEDS_INPUT"},
				{CheckName: "color", Result: "WARNING"},
			},
			intent:      "border",
			wantArtwork: "BLOCKED",
			wantProof:   "NOT_PREPARED",
		},
		{
			name: "any NEEDS_REVIEW escalates even with a NEEDS_INPUT also present",
			findings: []store.FindingInput{
				{CheckName: "resolution", Result: "NEEDS_INPUT"},
				{CheckName: "color", Result: "NEEDS_REVIEW"},
			},
			intent:      "border",
			wantArtwork: "NEEDS_REVIEW",
			wantProof:   "NOT_PREPARED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArtwork, gotProof := decideArtworkStatus(tc.findings, tc.intent)
			if gotArtwork != tc.wantArtwork {
				t.Errorf("artworkStatus = %q, want %q", gotArtwork, tc.wantArtwork)
			}
			if gotProof != tc.wantProof {
				t.Errorf("proofStatus = %q, want %q", gotProof, tc.wantProof)
			}
		})
	}
}

// slowFakeProvider simulates a provider whose HTTP call takes `delay` -
// deliberately longer than the ctx deadline the test supplies, standing in
// for a slow or hanging Groq call without any real network dependency. Like
// a real *http.Client with a request-scoped context, it must actually
// select on ctx.Done() rather than block for the full delay regardless -
// otherwise this test would prove nothing about ctx cancellation itself.
type slowFakeProvider struct {
	delay     time.Duration
	callCount int
}

func (p *slowFakeProvider) Decide(ctx context.Context, in agent.DecisionInput) (agent.Decision, error) {
	p.callCount++
	select {
	case <-time.After(p.delay):
		return agent.Decision{Action: agent.ActionEscalate}, nil
	case <-ctx.Done():
		return agent.Decision{}, &agent.ClassifiedError{Transient: true, Err: ctx.Err()}
	}
}

func TestDecideWithBoundedRetriesRespectsContextDeadline(t *testing.T) {
	// The provider "takes" 5 seconds - if decideWithBoundedRetries only
	// bounded each individual HTTP call (the old design) rather than the
	// whole retry sequence via ctx, three attempts at this delay would take
	// up to 15s, comfortably exceeding a 30s job lease combined with normal
	// DB overhead. A short ctx deadline here stands in for
	// worker.agentDecisionDeadline.
	provider := &slowFakeProvider{delay: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	retryReservations := 0
	reserveRetry := func() (bool, error) {
		retryReservations++
		return true, nil // would allow retries - ctx expiry must stop them before this is even called
	}

	start := time.Now()
	_, err := decideWithBoundedRetries(ctx, provider, agent.DecisionInput{}, reserveRetry, func(agent.Decision, error) {})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error once the context deadline is exhausted, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("decideWithBoundedRetries took %s - a slow provider must be bounded by ctx's deadline (50ms), "+
			"not its own 5s delay, or a job could be held RUNNING well past its lease", elapsed)
	}
	if retryReservations != 0 {
		t.Fatalf("expected zero retry-budget reservations once ctx had already expired before the first retry check, got %d", retryReservations)
	}
	if provider.callCount != 1 {
		t.Fatalf("expected exactly one call to the provider (the initial attempt, cut short by ctx) - got %d", provider.callCount)
	}
}

func TestDecideWithBoundedRetriesSucceedsFastWithinDeadline(t *testing.T) {
	// Regression: a provider that responds well within the deadline must
	// behave exactly as before - one call, no retries, no error.
	provider := &slowFakeProvider{delay: 1 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryReservations := 0
	reserveRetry := func() (bool, error) {
		retryReservations++
		return true, nil
	}

	decision, err := decideWithBoundedRetries(ctx, provider, agent.DecisionInput{}, reserveRetry, func(agent.Decision, error) {})
	if err != nil {
		t.Fatalf("expected no error for a fast, successful provider call, got %v", err)
	}
	if decision.Action != agent.ActionEscalate {
		t.Fatalf("decision.Action = %q, want %q", decision.Action, agent.ActionEscalate)
	}
	if retryReservations != 0 {
		t.Fatalf("expected zero retries for a successful first attempt, got %d reservation attempt(s)", retryReservations)
	}
	if provider.callCount != 1 {
		t.Fatalf("expected exactly one call to the provider, got %d", provider.callCount)
	}
}
