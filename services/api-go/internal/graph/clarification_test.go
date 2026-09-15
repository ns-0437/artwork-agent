package graph

import "testing"

func TestInterpretYesNo(t *testing.T) {
	cases := []struct {
		input   string
		wantVal bool
		wantOK  bool
	}{
		{"yes", true, true},
		{"Yes", true, true},
		{"  yes  ", true, true},
		{"y", true, true},
		{"Y", true, true},
		{"yes, it does", true, true},
		{"no", false, true},
		{"No", false, true},
		{"n", false, true},
		{"no it does not", false, true},
		{"", false, false},
		{"maybe", false, false},
		{"not sure", false, false},
		{"yesterday", false, false}, // must not prefix-match past a word boundary
		{"nothing", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			gotVal, gotOK := interpretYesNo(tc.input)
			if gotOK != tc.wantOK {
				t.Fatalf("interpretYesNo(%q) ok = %v, want %v", tc.input, gotOK, tc.wantOK)
			}
			if gotOK && gotVal != tc.wantVal {
				t.Errorf("interpretYesNo(%q) = %v, want %v", tc.input, gotVal, tc.wantVal)
			}
		})
	}
}
