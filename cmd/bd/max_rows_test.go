//go:build cgo

// be-u8z9: unit tests for workapi.WithFetchOneExtra, the probe-row bump
// helper used by the CLI-layer --max-rows / BEADS_MAX_ROWS enforcement
// exercised end-to-end in max_rows_embedded_test.go.

package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

func TestWithFetchOneExtra_LimitEqualsCap_BumpsBothForTruncationProbe(t *testing.T) {
	got := workapi.WithFetchOneExtra(types.IssueFilter{Limit: 5, MaxRows: 5, MaxRowsSource: "--max-rows"})
	if got.Limit != 6 {
		t.Errorf("Limit == MaxRows: Limit = %d, want 6 (bumped so the query still fetches the truncation-detection probe row)", got.Limit)
	}
	if got.MaxRows != 6 {
		t.Errorf("Limit == MaxRows: MaxRows = %d, want 6 (bumped in lockstep so the probe row alone doesn't trip EnforceMaxRowsCap)", got.MaxRows)
	}
	if got.MaxRowsSource != "--max-rows" {
		t.Errorf("MaxRowsSource must be preserved unchanged, got %q", got.MaxRowsSource)
	}
}

// TestWithFetchOneExtra_LimitOverCap_OnlyBumpsLimit covers the tighter-cap
// case (--limit 100 --max-rows 5, LimitSet_CapTighter): MaxRows must NOT
// bump, or a genuine cap violation would report the wrong Cap value (N+1
// instead of the user's true --max-rows=N) in the error message.
func TestWithFetchOneExtra_LimitOverCap_OnlyBumpsLimit(t *testing.T) {
	got := workapi.WithFetchOneExtra(types.IssueFilter{Limit: 100, MaxRows: 5})
	if got.Limit != 101 {
		t.Errorf("Limit = %d, want 101", got.Limit)
	}
	if got.MaxRows != 5 {
		t.Errorf("MaxRows must stay unbumped so a real cap violation reports the true cap; got %d, want 5", got.MaxRows)
	}
}

// TestWithFetchOneExtra_LimitUnderCap_OnlyBumpsLimit covers the
// looser-cap case (--limit 5 --max-rows 100, LimitSet_CapLooser): the
// probe-row bump alone never crosses EffectiveSearchLimit's `limit >
// maxRows` branch here, so no MaxRows adjustment is needed.
func TestWithFetchOneExtra_LimitUnderCap_OnlyBumpsLimit(t *testing.T) {
	got := workapi.WithFetchOneExtra(types.IssueFilter{Limit: 5, MaxRows: 100})
	if got.Limit != 6 {
		t.Errorf("Limit = %d, want 6", got.Limit)
	}
	if got.MaxRows != 100 {
		t.Errorf("MaxRows must stay unbumped, got %d, want 100", got.MaxRows)
	}
}

// TestWithFetchOneExtra_NoLimit_Unaffected covers the unlimited case
// (Limit == 0): workapi.WithFetchOneExtra is a no-op regardless of MaxRows.
func TestWithFetchOneExtra_NoLimit_Unaffected(t *testing.T) {
	got := workapi.WithFetchOneExtra(types.IssueFilter{Limit: 0, MaxRows: 5})
	if got.Limit != 0 || got.MaxRows != 5 {
		t.Errorf("unlimited Limit must pass through unchanged, got Limit=%d MaxRows=%d", got.Limit, got.MaxRows)
	}
}
