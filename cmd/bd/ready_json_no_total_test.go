package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// readyPageFake serves GetReadyWorkWithCounts from a fixed ready set and
// records the filters it was asked for. Its embedded DoltStorage is nil, so any
// other read panics and the test says so.
type readyPageFake struct {
	storage.DoltStorage
	ready      []*types.IssueWithCounts
	limits     []int
	totalCalls int
}

func (f *readyPageFake) IsRemoteBackendStore() bool { return true }

func (f *readyPageFake) GetReadyWorkWithCounts(_ context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	f.limits = append(f.limits, filter.Limit)
	if filter.Limit > 0 && filter.Limit < len(f.ready) {
		return f.ready[:filter.Limit], nil
	}
	return f.ready, nil
}

func (f *readyPageFake) GetReadyWorkWithCountsAndTotal(_ context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	f.totalCalls++
	if filter.Limit > 0 && filter.Limit < len(f.ready) {
		return f.ready[:filter.Limit], len(f.ready), nil
	}
	return f.ready, len(f.ready), nil
}

func readyRows(n int) []*types.IssueWithCounts {
	rows := make([]*types.IssueWithCounts, n)
	for i := range rows {
		rows[i] = &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-" + string(rune('a'+i)), Title: "t", Status: types.StatusOpen}}
	}
	return rows
}

func runReadyJSONWithoutTotalForTest(t *testing.T, s storage.DoltStorage, filter types.WorkFilter) ([]types.IssueWithCounts, string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	var err error
	stdout, stderr := captureStdio(t, func() {
		err = runReadyJSONWithoutTotal(context.Background(), s, filter)
	})
	if err != nil {
		t.Fatalf("runReadyJSONWithoutTotal: %v\nstderr: %s", err, stderr)
	}
	var rows []types.IssueWithCounts
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	return rows, stderr
}

// TestReadyJSONOnARemoteBackendAsksForNoTotal pins `bd ready --json` against a
// remote backend with no envelope: one listing one row past the limit, never
// the counted listing (which costs a remote store a countReadyWork request),
// and a hint that says more exist without saying how many.
func TestReadyJSONOnARemoteBackendAsksForNoTotal(t *testing.T) {
	t.Run("truncated", func(t *testing.T) {
		s := &readyPageFake{ready: readyRows(5)}
		rows, stderr := runReadyJSONWithoutTotalForTest(t, s, types.WorkFilter{Limit: 2})
		if len(rows) != 2 {
			t.Fatalf("printed %d rows, want the 2 the limit allows", len(rows))
		}
		if len(s.limits) != 1 || s.limits[0] != 3 || s.totalCalls != 0 {
			t.Fatalf("listings asked for limits %v and %d counted listings, want one listing of 3 and none counted", s.limits, s.totalCalls)
		}
		if !strings.Contains(stderr, "Showing 2 ready issues; more matched but were hidden by --limit.") {
			t.Fatalf("stderr = %q, want the truncation hint without a total", stderr)
		}
	})
	t.Run("whole set", func(t *testing.T) {
		s := &readyPageFake{ready: readyRows(2)}
		rows, stderr := runReadyJSONWithoutTotalForTest(t, s, types.WorkFilter{Limit: 2})
		if len(rows) != 2 || stderr != "" {
			t.Fatalf("rows = %d, stderr = %q; want both rows and no hint", len(rows), stderr)
		}
	})
	t.Run("unlimited", func(t *testing.T) {
		s := &readyPageFake{ready: readyRows(3)}
		rows, stderr := runReadyJSONWithoutTotalForTest(t, s, types.WorkFilter{})
		if len(rows) != 3 || stderr != "" || len(s.limits) != 1 || s.limits[0] != 0 {
			t.Fatalf("rows = %d, stderr = %q, limits = %v; want every row, no hint, one unlimited listing", len(rows), stderr, s.limits)
		}
	})
	t.Run("cap at the limit keeps the counted listing", func(t *testing.T) {
		s := &readyPageFake{ready: readyRows(5)}
		rows, stderr := runReadyJSONWithoutTotalForTest(t, s, types.WorkFilter{Limit: 2, MaxRows: 2})
		if len(rows) != 2 || s.totalCalls != 1 || len(s.limits) != 0 {
			t.Fatalf("rows = %d, counted = %d, limits = %v; want the counted listing alone", len(rows), s.totalCalls, s.limits)
		}
		if !strings.Contains(stderr, "Showing 2 ready issues; more matched") {
			t.Fatalf("stderr = %q, want the truncation hint without a total", stderr)
		}
	})
}

// TestReadyJSONPrintsNoTotalOnlyWithoutTheEnvelopeOnARemoteBackend pins when
// the total is skipped: the envelope prints it as pagination.total, and a local
// store takes it in the page's own transaction for the stderr hint.
func TestReadyJSONPrintsNoTotalOnlyWithoutTheEnvelopeOnARemoteBackend(t *testing.T) {
	remote := &readyPageFake{}
	local := &routingSettingsFake{}

	t.Setenv("BD_JSON_ENVELOPE", "")
	if !readyJSONPrintsNoTotal(remote) {
		t.Error("remote backend without the envelope: want the total skipped")
	}
	if readyJSONPrintsNoTotal(local) {
		t.Error("local store: want the total kept")
	}
	t.Setenv("BD_JSON_ENVELOPE", "1")
	if readyJSONPrintsNoTotal(remote) {
		t.Error("remote backend with the envelope: want the total kept for pagination.total")
	}
}

func TestReadyHiddenHintAgreesInNumber(t *testing.T) {
	if got := readyHiddenHint(1); got != "Showing 1 ready issue; more matched but were hidden by --limit." {
		t.Errorf("readyHiddenHint(1) = %q", got)
	}
	if got := readyHiddenHint(2); got != "Showing 2 ready issues; more matched but were hidden by --limit." {
		t.Errorf("readyHiddenHint(2) = %q", got)
	}
}
