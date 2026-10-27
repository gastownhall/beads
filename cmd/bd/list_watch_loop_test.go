package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestListWatchLoopWaitsFullPollAfterEachRefresh drives listWatchLoop, the
// `bd list --watch` refresh loop (GH#3415), with a poll function the test
// releases by hand and a refresh that can block mid-flight. It pins three
// rules:
//
//   - every refresh starts only after a wait that was requested after the
//     previous refresh returned has fired (poll is not re-armed while a
//     refresh is still running, so a slow refresh can never collapse the
//     loop into back-to-back refreshes);
//   - refreshes succeed, fail and redraw-on-change through the same loop:
//     an unchanged snapshot redraws nothing, a changed snapshot redraws,
//     a failed refresh logs the error and keeps waiting;
//   - a stop that arrives while a refresh is running ends the loop with
//     "Stopped watching." before any further refresh starts.
func TestListWatchLoopWaitsFullPollAfterEachRefresh(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	same := []*types.Issue{{ID: "l-1", Title: "Leaf", Status: types.StatusOpen, UpdatedAt: base}}
	changed := []*types.Issue{{ID: "l-1", Title: "Leaf changed", Status: types.StatusOpen, UpdatedAt: base.Add(time.Second)}}

	var mu sync.Mutex
	var events []string
	var waits []chan time.Time
	record := func(ev string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}
	awaitWaits := func(n int) chan time.Time {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			ready := len(waits) >= n
			var ch chan time.Time
			if ready {
				ch = waits[n-1]
			}
			snapshot := slices.Clone(events)
			mu.Unlock()
			if ready {
				return ch
			}
			if time.Now().After(deadline) {
				t.Fatalf("loop never requested wait %d (waits so far: %d, events: %v)", n, len(waits), snapshot)
			}
			time.Sleep(time.Millisecond)
		}
	}
	awaitEvent := func(ev string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			found := slices.Contains(events, ev)
			mu.Unlock()
			if found {
				return
			}
			if time.Now().After(deadline) {
				mu.Lock()
				snapshot := slices.Clone(events)
				mu.Unlock()
				t.Fatalf("event %q never happened; events so far: %v", ev, snapshot)
			}
			time.Sleep(time.Millisecond)
		}
	}

	poll := func() <-chan time.Time {
		ch := make(chan time.Time)
		mu.Lock()
		n := len(waits) + 1
		events = append(events, fmt.Sprintf("wait-requested:%d", n))
		waits = append(waits, ch)
		mu.Unlock()
		return ch
	}

	refreshCalls := 0
	refreshRelease := make(chan struct{})
	refresh := func() ([]*types.Issue, bool, error) {
		// Only the loop's goroutine ever calls refresh, so the counter and
		// the per-call event ordering need no locking against each other.
		refreshCalls++
		call := refreshCalls
		record(fmt.Sprintf("refresh-start:%d", call))
		if call == 4 {
			<-refreshRelease
		}
		record(fmt.Sprintf("refresh-return:%d", call))
		switch call {
		case 2, 4:
			// Refresh 2 changes the snapshot (redraw); refresh 4 returns the
			// same snapshot the redraw left behind, so the stop that lands
			// during it is the only thing the loop does next.
			return changed, false, nil
		case 3:
			return nil, false, errors.New("failed to search issues: context canceled")
		default:
			return same, false, nil
		}
	}
	redraw := func(issues []*types.Issue, truncated bool) {
		record("redraw:" + issueSnapshot(issues))
	}

	stop := make(chan os.Signal, 1)
	stderr := captureStderr(t, func() {
		done := make(chan struct{})
		go func() {
			listWatchLoop(stop, poll, issueSnapshot(same), refresh, redraw)
			close(done)
		}()

		// Refresh 1: unchanged snapshot, no redraw.
		record("wait-fired:1")
		awaitWaits(1) <- time.Time{}
		awaitEvent("refresh-return:1")

		// Refresh 2: changed snapshot, one redraw.
		record("wait-fired:2")
		awaitWaits(2) <- time.Time{}
		awaitEvent("redraw:" + issueSnapshot(changed))

		// Refresh 3: failed refresh; the loop logs and keeps waiting.
		record("wait-fired:3")
		awaitWaits(3) <- time.Time{}
		awaitEvent("refresh-return:3")

		// Refresh 4: still running when the stop signal arrives.
		record("wait-fired:4")
		awaitWaits(4) <- time.Time{}
		awaitEvent("refresh-start:4")
		stop <- os.Interrupt
		close(refreshRelease)
		<-done
	})

	mu.Lock()
	snapshot := slices.Clone(events)
	mu.Unlock()
	pos := func(ev string) int { return slices.Index(snapshot, ev) }

	// Every refresh started only after the wait armed for it fired, and each
	// wait was requested only after the previous refresh returned — the
	// strict interleave that keeps a slow refresh from collapsing the loop
	// into back-to-back refreshes.
	for k := 1; k <= 4; k++ {
		if p, s := pos(fmt.Sprintf("wait-fired:%d", k)), pos(fmt.Sprintf("refresh-start:%d", k)); p < 0 || s < 0 || p > s {
			t.Fatalf("refresh %d started without its wait having fired first: %v", k, snapshot)
		}
		if k > 1 {
			if p, s := pos(fmt.Sprintf("wait-requested:%d", k)), pos(fmt.Sprintf("refresh-return:%d", k-1)); p < s {
				t.Fatalf("wait %d was requested before refresh %d returned: %v", k, k-1, snapshot)
			}
		}
	}
	// The redraw sits inside refresh 2's processing, before the loop waits
	// again.
	if p := pos("redraw:" + issueSnapshot(changed)); p < pos("refresh-return:2") || p > pos("wait-requested:3") {
		t.Fatalf("redraw did not run inside refresh 2's processing: %v", snapshot)
	}
	// The stop that arrived mid-refresh-4 cost exactly one more wait (armed
	// by the select that then took the ready stop case) and no more
	// refreshes.
	if p1, p2 := pos("wait-requested:5"), pos("refresh-return:4"); p1 < p2 {
		t.Fatalf("wait 5 requested before refresh 4 returned: %v", snapshot)
	}
	if slices.Contains(snapshot, "refresh-start:5") || slices.Contains(snapshot, "refresh-return:5") {
		t.Fatalf("loop ran another refresh after the stop signal: %v", snapshot)
	}
	if pos("wait-requested:6") >= 0 {
		t.Fatalf("loop armed more than one wait after the stop signal: %v", snapshot)
	}
	if !strings.Contains(stderr, "Error refreshing issues:") {
		t.Errorf("failed refresh did not log its error:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Stopped watching.") {
		t.Errorf("stop during a refresh did not end the loop with the stop line:\n%s", stderr)
	}
	if got := strings.Count(stderr, "Watching for changes..."); got != 1 {
		t.Errorf("banner printed %d times, want 1 (the redraw banner only; the initial banner is watchIssues'), stderr:\n%s", got, stderr)
	}
}
