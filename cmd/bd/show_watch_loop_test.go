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
// Each wait the loop asks for fires only once the loop has asked for it, i.e.
// after the previous poll (and any redraw) finished.
func driveWatchLoop(t *testing.T, src *scriptedWatchSource) (bool, string) {
	t.Helper()
	polls := len(src.polls)
	p := newPacedPoll(t)
	var watched bool
	stderr := captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watched = watchIssueLoop(context.Background(), src.source(), p.poll, stop)
		}()
		for range polls {
			p.fire(p.next(done))
		}
		p.next(done) // the wait after the last poll; never fired
		stop <- os.Interrupt
		<-done
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
	poll := func() <-chan time.Time {
		t.Fatal("poll requested after a failed initial render")
		return nil
	}
	if watchIssueLoop(context.Background(), src, poll, make(chan os.Signal)) {
		t.Fatal("watchIssueLoop reported watching after a failed initial render")
	}
}

// TestWatchIssueLoopWaitsAfterEachFetch pins `bd show --watch`'s cadence on
// both routes: each fetch, whether unchanged, failed or redrawn, starts only
// after a wait the loop asked for once the previous fetch (and redraw)
// returned, and that wait has fired. A fixed-rate ticker instead starts the
// next fetch the moment a slow one returns (GH#3415).
func TestWatchIssueLoopWaitsAfterEachFetch(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	open := &types.Issue{ID: "w-3", Status: types.StatusOpen, UpdatedAt: base}
	closed := &types.Issue{ID: "w-3", Status: types.StatusClosed, UpdatedAt: base.Add(time.Second)}
	polls := []watchPoll{
		{issue: open},
		{err: errors.New("backend blip")},
		{issue: closed},
		{issue: closed},
	}

	p := newPacedPoll(t)
	src := issueWatchSource{
		render: func(context.Context) *types.Issue {
			p.record("render")
			return open
		},
		fetch: func(context.Context) (*types.Issue, error) {
			p.record("fetch")
			defer p.record("fetched")
			next := polls[0]
			polls = polls[1:]
			return next.issue, next.err
		},
	}
	scripted := len(polls)
	var watched bool
	captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watched = watchIssueLoop(context.Background(), src, p.poll, stop)
		}()
		for range scripted {
			p.fire(p.next(done))
		}
		p.next(done) // the wait after the last fetch; never fired
		stop <- os.Interrupt
		<-done
	})

	if !watched {
		t.Fatal("watchIssueLoop reported nothing watched after a successful render")
	}
	want := []string{
		"render",
		"wait", "fire", "fetch", "fetched", // unchanged
		"wait", "fire", "fetch", "fetched", // failed
		"wait", "fire", "fetch", "fetched", "render", // changed
		"wait", "fire", "fetch", "fetched", // unchanged again
		"wait",
	}
	if got := p.events(); !slices.Equal(got, want) {
		t.Fatalf("timeline =\n  %v\nwant\n  %v", got, want)
	}
}

// TestWatchIssueLoopStopDuringFetchEndsLoop pins that a Ctrl+C arriving while
// a fetch is running ends `bd show --watch` once that fetch returns, before
// any further fetch.
func TestWatchIssueLoopStopDuringFetchEndsLoop(t *testing.T) {
	issue := &types.Issue{ID: "w-4", Status: types.StatusOpen}
	p := newPacedPoll(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fetches := 0
	src := issueWatchSource{
		render: func(context.Context) *types.Issue { return issue },
		fetch: func(context.Context) (*types.Issue, error) {
			fetches++
			if fetches == 1 {
				close(started)
				<-release
			}
			return issue, nil
		},
	}
	var watched bool
	stderr := captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watched = watchIssueLoop(context.Background(), src, p.poll, stop)
		}()
		p.stopDuringRefresh(stop, started, release, done)
	})

	if !watched {
		t.Fatal("watchIssueLoop reported nothing watched after a successful render")
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1 (stop sent during the first); timeline %v", fetches, p.events())
	}
	if !strings.HasSuffix(stderr, "\nStopped watching.\n") {
		t.Fatalf("stop did not end with %q:\n%s", "Stopped watching.", stderr)
	}
}
