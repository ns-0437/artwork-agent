package graph

import "testing"

func TestDecideRepairRequestAction(t *testing.T) {
	cv := func(v int) *int { return &v }
	key := func(s string) *string { return &s }

	cases := []struct {
		name    string
		state   repairRequestState
		reqKey  string
		reqCase int
		want    repairRequestAction
	}{
		{
			name: "exact retry of a completed repair returns the existing job, even though case_version has since moved on",
			state: repairRequestState{
				ExistingRepairJobCaseVersion: cv(3),
				OrderCaseVersion:             4, // bumped by the repair's own completion
			},
			reqKey:  "abc",
			reqCase: 3, // matches what the ORIGINAL request used
			want:    actionReturnExistingJob,
		},
		{
			name: "same key, different case_version than the completed repair used - rejected as reuse with different inputs",
			state: repairRequestState{
				ExistingRepairJobCaseVersion: cv(3),
				OrderCaseVersion:             4,
			},
			reqKey:  "abc",
			reqCase: 99,
			want:    actionRejectCaseVersionMismatch,
		},
		{
			name: "retry while the same key's job is still in flight returns it",
			state: repairRequestState{
				HasActiveJob:              true,
				ActiveJobIdempotencyKey:   key("abc"),
				ActiveJobInputCaseVersion: 3,
			},
			reqKey:  "abc",
			reqCase: 3,
			want:    actionReturnExistingJob,
		},
		{
			name: "in-flight job with the same key but a different case_version is rejected",
			state: repairRequestState{
				HasActiveJob:              true,
				ActiveJobIdempotencyKey:   key("abc"),
				ActiveJobInputCaseVersion: 3,
			},
			reqKey:  "abc",
			reqCase: 5,
			want:    actionRejectCaseVersionMismatch,
		},
		{
			name: "a different repair already in flight is rejected regardless of case_version",
			state: repairRequestState{
				HasActiveJob:              true,
				ActiveJobIdempotencyKey:   key("other-key"),
				ActiveJobInputCaseVersion: 3,
			},
			reqKey:  "abc",
			reqCase: 3,
			want:    actionRejectDifferentRepairInProgress,
		},
		{
			name: "genuinely new request with a current case_version proceeds",
			state: repairRequestState{
				OrderCaseVersion: 3,
			},
			reqKey:  "abc",
			reqCase: 3,
			want:    actionCreateNew,
		},
		{
			name: "genuinely new request with a stale case_version is rejected",
			state: repairRequestState{
				OrderCaseVersion: 5,
			},
			reqKey:  "abc",
			reqCase: 3,
			want:    actionRejectStaleCaseVersion,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideRepairRequestAction(tc.state, tc.reqKey, tc.reqCase)
			if got != tc.want {
				t.Errorf("decideRepairRequestAction() = %v, want %v", got, tc.want)
			}
		})
	}
}
