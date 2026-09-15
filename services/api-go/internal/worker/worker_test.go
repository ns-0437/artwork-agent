package worker

import (
	"testing"

	"github.com/ns-0437/artwork-agent/services/api-go/internal/store"
)

func TestDecideArtworkStatus(t *testing.T) {
	cases := []struct {
		name             string
		findings         []store.FindingInput
		intent           string
		wantArtwork      string
		wantProof        string
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
