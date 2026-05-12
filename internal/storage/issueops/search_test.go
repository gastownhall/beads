package issueops

import (
	"errors"
	"strings"
	"testing"
)

func TestEffectiveSearchLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limit   int
		maxRows int
		want    int
	}{
		{name: "no limit, no cap → unlimited", limit: 0, maxRows: 0, want: 0},
		{name: "limit only → limit", limit: 10, maxRows: 0, want: 10},
		{name: "cap only → cap+1", limit: 0, maxRows: 5, want: 6},
		{name: "limit under cap → limit", limit: 3, maxRows: 5, want: 3},
		{name: "limit equals cap → limit (no overage detection needed)", limit: 5, maxRows: 5, want: 5},
		{name: "limit over cap → cap+1", limit: 100, maxRows: 5, want: 6},
		{name: "cap=1 limit=0 → 2", limit: 0, maxRows: 1, want: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EffectiveSearchLimit(tc.limit, tc.maxRows)
			if got != tc.want {
				t.Errorf("EffectiveSearchLimit(%d, %d) = %d, want %d", tc.limit, tc.maxRows, got, tc.want)
			}
		})
	}
}

func TestEnforceMaxRowsCap_Disabled_NoError(t *testing.T) {
	t.Parallel()
	if err := EnforceMaxRowsCap(1_000_000, 0, ""); err != nil {
		t.Errorf("EnforceMaxRowsCap with MaxRows=0 returned error: %v (want nil)", err)
	}
}

func TestEnforceMaxRowsCap_UnderCap_NoError(t *testing.T) {
	t.Parallel()
	if err := EnforceMaxRowsCap(3, 5, "--max-rows"); err != nil {
		t.Errorf("EnforceMaxRowsCap(3, 5, ...) returned error: %v (want nil)", err)
	}
}

func TestEnforceMaxRowsCap_AtCap_NoError(t *testing.T) {
	t.Parallel()
	if err := EnforceMaxRowsCap(5, 5, "--max-rows"); err != nil {
		t.Errorf("EnforceMaxRowsCap(5, 5, ...) returned error: %v (want nil at exact cap)", err)
	}
}

// TestSearchIssues_MaxRows_Exceeded_ReturnsErrTooManyRows exercises the cap
// helper that searchTableInTx invokes after scan. When the row count exceeds
// the cap, the helper returns a *ErrTooManyRows carrying Found/Cap/Source.
// The full SearchIssues integration path (issuing LIMIT cap+1 against a real
// table, then handing the count to this helper) is tested by the per-backend
// validator beads against live Dolt and embedded fixtures.
func TestSearchIssues_MaxRows_Exceeded_ReturnsErrTooManyRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		found  int
		cap    int
		source string
	}{
		{name: "cap-of-5-found-6 from --max-rows", found: 6, cap: 5, source: "--max-rows"},
		{name: "cap-of-1-found-2 from BEADS_MAX_ROWS", found: 2, cap: 1, source: "BEADS_MAX_ROWS"},
		{name: "cap-of-10-found-100 from library caller", found: 100, cap: 10, source: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := EnforceMaxRowsCap(tc.found, tc.cap, tc.source)
			if err == nil {
				t.Fatalf("EnforceMaxRowsCap(%d, %d, %q) returned nil, want *ErrTooManyRows", tc.found, tc.cap, tc.source)
			}

			var typed *ErrTooManyRows
			if !errors.As(err, &typed) {
				t.Fatalf("EnforceMaxRowsCap: errors.As to *ErrTooManyRows failed; got %T %v", err, err)
			}

			if typed.Found != tc.found {
				t.Errorf("ErrTooManyRows.Found = %d, want %d", typed.Found, tc.found)
			}
			if typed.Cap != tc.cap {
				t.Errorf("ErrTooManyRows.Cap = %d, want %d", typed.Cap, tc.cap)
			}
			if typed.Source != tc.source {
				t.Errorf("ErrTooManyRows.Source = %q, want %q", typed.Source, tc.source)
			}
		})
	}
}

func TestErrTooManyRows_Error_WithSource(t *testing.T) {
	t.Parallel()
	err := &ErrTooManyRows{Found: 6, Cap: 5, Source: "--max-rows"}
	got := err.Error()
	for _, want := range []string{"6", "5", "--max-rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
}

func TestErrTooManyRows_Error_WithoutSource(t *testing.T) {
	t.Parallel()
	err := &ErrTooManyRows{Found: 6, Cap: 5}
	got := err.Error()
	for _, want := range []string{"6", "5"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "--") {
		t.Errorf("Error() = %q, should not contain dash prefix when Source empty", got)
	}
}
