package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// watchPoll is one scripted fetch result.
type watchPoll struct {
	issue *types.Issue
	err   error
}

// scriptedWatchSource replays a fixed sequence of fetch results and counts
// renders, so the loop's redraw rule can be checked without a store.
type scriptedWatchSource struct {
	initial *types.Issue
	polls   []watchPoll
	renders int
	// rendersAtFetch records the render count as each poll starts, i.e. the
	// result of the poll before it. Written only on the loop's goroutine and
	// read after the loop returns.
	rendersAtFetch []int
}

func (s *scriptedWatchSource) source() issueWatchSource {
	return issueWatchSource{
		render: func(context.Context) *types.Issue {
			s.renders++
			return s.initial
		},
		fetch: func(context.Context) (*types.Issue, error) {
			s.rendersAtFetch = append(s.rendersAtFetch, s.renders)
			next := s.polls[0]
			s.polls = s.polls[1:]
			return next.issue, next.err
		},
	}
}

// driveWatchLoop runs the loop over src for every scripted poll, stops it, and
// returns whether it reported watching plus everything it wrote to stderr.
func driveWatchLoop(t *testing.T, src *scriptedWatchSource) (bool, string) {
	t.Helper()
	polls := len(src.polls)
	var watched bool
	stderr := captureStderr(t, func() {
		tick := make(chan time.Time)
		stop := make(chan os.Signal, 1)
		done := make(chan bool)
		go func() { done <- watchIssueLoop(context.Background(), src.source(), tick, stop) }()
		// tick is unbuffered, so each send lands only once the loop is back in
		// its select, i.e. after the previous poll (and any redraw) finished.
		for range polls {
			tick <- time.Time{}
		}
		stop <- os.Interrupt
		watched = <-done
	})
	return watched, stderr
}

// TestWatchIssueLoopRedrawsOnlyOnSnapshotChange pins the redraw rule both
// `bd show --watch` routes share: an unchanged snapshot or a failed read does
// not redraw, a status or updated_at change does, and stop ends the loop.
func TestWatchIssueLoopRedrawsOnlyOnSnapshotChange(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	open := &types.Issue{ID: "w-1", Status: types.StatusOpen, UpdatedAt: base}
	sameAgain := &types.Issue{ID: "w-1", Status: types.StatusOpen, UpdatedAt: base}
	inProgress := &types.Issue{ID: "w-1", Status: types.StatusInProgress, UpdatedAt: base.Add(time.Second)}
	touched := &types.Issue{ID: "w-1", Status: types.StatusInProgress, UpdatedAt: base.Add(2 * time.Second)}

	src := &scriptedWatchSource{
		initial: open,
		polls: []watchPoll{
			{issue: sameAgain},
			{err: errors.New("backend blip")},
			{issue: inProgress},
			{issue: inProgress},
			{issue: touched},
		},
	}
	watched, _ := driveWatchLoop(t, src)
	if !watched {
		t.Fatal("watchIssueLoop reported nothing watched after a successful render")
	}
	// Poll order: unchanged, failed read, status change, unchanged,
	// updated_at change.
	if want := []int{1, 1, 1, 2, 2}; !slices.Equal(src.rendersAtFetch, want) {
		t.Fatalf("renders before each poll = %v, want %v", src.rendersAtFetch, want)
	}
	if src.renders != 3 {
		t.Fatalf("renders = %d, want 3 (initial, status change, updated_at change)", src.renders)
	}
}

// TestWatchIssueLoopFailedPollsStayQuiet pins the poll-failure rule both routes
// share: a poll that fails (a deleted issue, a unit of work that will not open)
// keeps the last render and prints nothing, however many ticks it lasts, and a
// later successful poll with a change redraws as usual. The proxied route used
// to print an error line — under --json, an error object — on every tick.
func TestWatchIssueLoopFailedPollsStayQuiet(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	open := &types.Issue{ID: "w-2", Status: types.StatusOpen, UpdatedAt: base}
	closed := &types.Issue{ID: "w-2", Status: types.StatusClosed, UpdatedAt: base.Add(time.Second)}

	src := &scriptedWatchSource{
		initial: open,
		polls: []watchPoll{
			{err: errors.New("issue w-2 not found")},
			{err: errors.New("open unit of work: connection refused")},
			{err: errors.New("issue w-2 not found")},
			{issue: closed},
		},
	}
	watched, stderr := driveWatchLoop(t, src)
	if !watched {
		t.Fatal("watchIssueLoop reported nothing watched after a successful render")
	}
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "connection refused") || strings.Contains(stderr, "Error") {
		t.Fatalf("failed polls printed to stderr:\n%s", stderr)
	}
	// Initial banner, the redraw's banner after the recovery, and the stop line.
	if got := strings.Count(stderr, "Watching for changes..."); got != 2 {
		t.Fatalf("banner printed %d times, want 2 (initial + one redraw):\n%s", got, stderr)
	}
	if src.renders != 2 {
		t.Fatalf("renders = %d, want 2 (initial, redraw after recovery)", src.renders)
	}
}

// TestWatchIssueLoopStopsWhenNothingToWatch pins that a failed initial render
// returns at once rather than polling an issue that was never shown.
func TestWatchIssueLoopStopsWhenNothingToWatch(t *testing.T) {
	src := issueWatchSource{
		render: func(context.Context) *types.Issue { return nil },
		fetch: func(context.Context) (*types.Issue, error) {
			t.Fatal("fetch called after a failed initial render")
			return nil, nil
		},
	}
	if watchIssueLoop(context.Background(), src, make(chan time.Time), make(chan os.Signal)) {
		t.Fatal("watchIssueLoop reported watching after a failed initial render")
	}
}
