package main

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// pacedPoll stands in for a watch loop's poll interval. Every wait the loop
// asks for is handed to the test, which fires it (or leaves it pending)
// explicitly, and every step lands on one timeline. A loop that asks for its
// next wait before the previous refresh returned, or that re-reads one
// long-lived channel the way a time.Ticker loop does, shows up in that
// timeline or never asks for the wait the test is blocked on (GH#3415).
type pacedPoll struct {
	t        *testing.T
	mu       sync.Mutex
	timeline []string
	requests chan chan time.Time
}

func newPacedPoll(t *testing.T) *pacedPoll {
	return &pacedPoll{t: t, requests: make(chan chan time.Time)}
}

func (p *pacedPoll) record(event string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timeline = append(p.timeline, event)
}

func (p *pacedPoll) events() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.timeline)
}

// poll is the loop's poll function: it records that a wait was requested and
// blocks until the test has taken the wait's channel.
func (p *pacedPoll) poll() <-chan time.Time {
	ch := make(chan time.Time, 1)
	p.record("wait")
	p.requests <- ch
	return ch
}

// next returns the loop's next requested wait, failing if the loop returns or
// stops asking.
func (p *pacedPoll) next(done <-chan struct{}) chan time.Time {
	p.t.Helper()
	select {
	case ch := <-p.requests:
		return ch
	case <-done:
		p.t.Fatalf("loop returned while the test waited for a poll; timeline %v", p.events())
	case <-time.After(10 * time.Second):
		p.t.Fatalf("loop never asked for its next wait; timeline %v", p.events())
	}
	return nil
}

// fire lets a requested wait elapse.
func (p *pacedPoll) fire(ch chan time.Time) {
	p.record("fire")
	ch <- time.Time{}
}

// stopDuringRefresh sends stop while the loop's refresh is blocked, lets the
// refresh return, takes (without firing) any wait the loop then asks for, and
// waits for the loop to end.
func (p *pacedPoll) stopDuringRefresh(stop chan<- os.Signal, started <-chan struct{}, release chan<- struct{}, done <-chan struct{}) {
	p.t.Helper()
	p.fire(p.next(done))
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		p.t.Fatalf("refresh never started; timeline %v", p.events())
	}
	stop <- os.Interrupt
	close(release)
	select {
	case <-p.requests:
	case <-done:
	case <-time.After(10 * time.Second):
		p.t.Fatalf("loop neither asked for a wait nor returned; timeline %v", p.events())
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		p.t.Fatalf("loop did not stop after stop was sent during a refresh; timeline %v", p.events())
	}
}

// TestListWatchLoopWaitsAfterEachRefresh pins `bd list --watch`'s cadence:
// each refresh, whether it succeeds unchanged, fails, or redraws a changed
// snapshot, starts only after a wait the loop asked for once the previous
// refresh (and redraw) returned, and that wait has fired. A fixed-rate ticker
// instead starts the next refresh the moment a slow one returns, which is how
// a large watched --parent tree kept the embedded store busy (GH#3415).
func TestListWatchLoopWaitsAfterEachRefresh(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	initial := []*types.Issue{{ID: "w-1", Status: types.StatusOpen, UpdatedAt: base}}
	changed := []*types.Issue{{ID: "w-1", Status: types.StatusInProgress, UpdatedAt: base.Add(time.Second)}}
	type result struct {
		issues []*types.Issue
		err    error
	}
	script := []result{
		{issues: initial},
		{err: errors.New("backend blip")},
		{issues: changed},
		{issues: changed},
	}

	p := newPacedPoll(t)
	var redrawn [][]*types.Issue
	refreshes := 0
	refresh := func() ([]*types.Issue, bool, error) {
		if refreshes >= len(script) {
			t.Errorf("refresh %d ran past the script", refreshes+1)
			return nil, false, errors.New("past script")
		}
		r := script[refreshes]
		refreshes++
		p.record("refresh")
		defer p.record("refreshed")
		return r.issues, false, r.err
	}
	redraw := func(issues []*types.Issue, _ bool) {
		p.record("redraw")
		redrawn = append(redrawn, issues)
	}

	stderr := captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			listWatchLoop(stop, p.poll, issueSnapshot(initial), refresh, redraw)
		}()
		for range script {
			p.fire(p.next(done))
		}
		p.next(done) // the wait after the last refresh; never fired
		stop <- os.Interrupt
		<-done
	})

	want := []string{
		"wait", "fire", "refresh", "refreshed", // unchanged
		"wait", "fire", "refresh", "refreshed", // failed
		"wait", "fire", "refresh", "refreshed", "redraw", // changed
		"wait", "fire", "refresh", "refreshed", // unchanged again
		"wait",
	}
	if got := p.events(); !slices.Equal(got, want) {
		t.Fatalf("timeline =\n  %v\nwant\n  %v", got, want)
	}
	if len(redrawn) != 1 || redrawn[0][0].Status != types.StatusInProgress {
		t.Fatalf("redraws = %v, want exactly the changed snapshot", redrawn)
	}
	if !strings.Contains(stderr, "Error refreshing issues: backend blip") {
		t.Fatalf("failed refresh not reported on stderr:\n%s", stderr)
	}
	if got := strings.Count(stderr, "Watching for changes..."); got != 1 {
		t.Fatalf("banner printed %d times, want 1 (after the one redraw):\n%s", got, stderr)
	}
	if !strings.HasSuffix(stderr, "\nStopped watching.\n") {
		t.Fatalf("stop did not end with %q:\n%s", "Stopped watching.", stderr)
	}
}

// TestListWatchLoopStopDuringRefreshEndsLoop pins that a Ctrl+C arriving while
// a refresh is running ends `bd list --watch` once that refresh returns,
// before any further refresh. A ticker that fired during the refresh would
// leave a tick ready alongside the stop and let the next refresh win.
func TestListWatchLoopStopDuringRefreshEndsLoop(t *testing.T) {
	p := newPacedPoll(t)
	started := make(chan struct{})
	release := make(chan struct{})
	refreshes := 0
	refresh := func() ([]*types.Issue, bool, error) {
		refreshes++
		p.record("refresh")
		defer p.record("refreshed")
		if refreshes == 1 {
			close(started)
			<-release
		}
		return nil, false, nil
	}

	stderr := captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			listWatchLoop(stop, p.poll, "", refresh, func([]*types.Issue, bool) { p.record("redraw") })
		}()
		p.stopDuringRefresh(stop, started, release, done)
	})

	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1 (stop sent during the first); timeline %v", refreshes, p.events())
	}
	if !strings.HasSuffix(stderr, "\nStopped watching.\n") {
		t.Fatalf("stop did not end with %q:\n%s", "Stopped watching.", stderr)
	}
}

// TestProxiedListWatchLoopWaitsAfterEachRefresh pins the proxied `bd list
// --watch` cadence, the direct route's: each refresh starts only after a wait
// the loop asked for once the previous refresh (and redraw) returned, and that
// wait has fired. One refresh outlasts the interval: while it runs, the wait it
// started from fires again, as a time.Ticker's channel would, and the loop
// must still ask for a fresh wait rather than refresh again at once (GH#3415).
// The one redraw must draw the dependency records its own refresh read.
func TestProxiedListWatchLoopWaitsAfterEachRefresh(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	initial := []*types.Issue{{ID: "w-1", Status: types.StatusOpen, UpdatedAt: base}}
	changed := []*types.Issue{
		{ID: "w-1", Status: types.StatusInProgress, UpdatedAt: base.Add(time.Second)},
		{ID: "w-2", Status: types.StatusOpen, UpdatedAt: base.Add(time.Second)},
	}
	initialDeps := map[string][]*types.Dependency{
		"w-1": {{IssueID: "w-1", DependsOnID: "w-0", Type: types.DepBlocks}},
	}
	changedDeps := map[string][]*types.Dependency{
		"w-2": {{IssueID: "w-2", DependsOnID: "w-1", Type: types.DepParentChild}},
	}
	type result struct {
		issues []*types.Issue
		deps   map[string][]*types.Dependency
		err    error
	}
	script := []result{
		{issues: initial, deps: initialDeps},
		{issues: changed, deps: changedDeps}, // the slow one
		{err: errors.New("backend blip")},
		{issues: changed, deps: changedDeps},
	}
	const slow = 2

	p := newPacedPoll(t)
	started := make(chan struct{})
	release := make(chan struct{})
	refreshes := 0
	load := func() ([]*types.Issue, bool, map[string][]*types.Dependency, error) {
		if refreshes >= len(script) {
			t.Errorf("refresh %d ran past the script", refreshes+1)
			return nil, false, nil, errors.New("past script")
		}
		r := script[refreshes]
		refreshes++
		p.record("refresh")
		defer p.record("refreshed")
		if refreshes == slow {
			close(started)
			<-release
		}
		return r.issues, false, r.deps, r.err
	}
	type drawn struct {
		issues []*types.Issue
		deps   map[string][]*types.Dependency
	}
	var redrawn []drawn
	draw := func(issues []*types.Issue, _ bool, deps map[string][]*types.Dependency) {
		p.record("redraw")
		redrawn = append(redrawn, drawn{issues: issues, deps: deps})
	}

	stderr := captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			proxiedListWatchLoop(stop, p.poll, issueSnapshot(initial), load, draw)
		}()
		for i := range script {
			wait := p.next(done)
			p.fire(wait)
			if i+1 != slow {
				continue
			}
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatalf("slow refresh never started; timeline %v", p.events())
			}
			p.fire(wait) // the interval elapses again while the refresh runs
			close(release)
		}
		p.next(done) // the wait after the last refresh; never fired
		stop <- os.Interrupt
		<-done
	})

	want := []string{
		"wait", "fire", "refresh", "refreshed", // unchanged
		"wait", "fire", "refresh", "fire", "refreshed", "redraw", // changed, outlasting the interval
		"wait", "fire", "refresh", "refreshed", // failed
		"wait", "fire", "refresh", "refreshed", // unchanged again
		"wait",
	}
	if got := p.events(); !slices.Equal(got, want) {
		t.Fatalf("timeline =\n  %v\nwant\n  %v", got, want)
	}
	if len(redrawn) != 1 || !slices.Equal(redrawn[0].issues, changed) || !reflect.DeepEqual(redrawn[0].deps, changedDeps) {
		t.Fatalf("redraws = %+v, want exactly the changed snapshot with its own refresh's dependency records %v", redrawn, changedDeps)
	}
	if !strings.Contains(stderr, "Error refreshing issues: backend blip") {
		t.Fatalf("failed refresh not reported on stderr:\n%s", stderr)
	}
	if got := strings.Count(stderr, "Watching for changes..."); got != 1 {
		t.Fatalf("banner printed %d times, want 1 (after the one redraw):\n%s", got, stderr)
	}
	if !strings.HasSuffix(stderr, "\nStopped watching.\n") {
		t.Fatalf("stop did not end with %q:\n%s", "Stopped watching.", stderr)
	}
}

// TestProxiedListWatchLoopStopDuringRefreshEndsLoop pins that a Ctrl+C
// arriving while a proxied refresh is running ends `bd list --watch` once that
// refresh returns, before any further refresh.
func TestProxiedListWatchLoopStopDuringRefreshEndsLoop(t *testing.T) {
	p := newPacedPoll(t)
	started := make(chan struct{})
	release := make(chan struct{})
	refreshes := 0
	load := func() ([]*types.Issue, bool, map[string][]*types.Dependency, error) {
		refreshes++
		p.record("refresh")
		defer p.record("refreshed")
		if refreshes == 1 {
			close(started)
			<-release
		}
		return nil, false, nil, nil
	}

	stderr := captureStderr(t, func() {
		stop := make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			proxiedListWatchLoop(stop, p.poll, "", load,
				func([]*types.Issue, bool, map[string][]*types.Dependency) { p.record("redraw") })
		}()
		p.stopDuringRefresh(stop, started, release, done)
	})

	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1 (stop sent during the first); timeline %v", refreshes, p.events())
	}
	if !strings.HasSuffix(stderr, "\nStopped watching.\n") {
		t.Fatalf("stop did not end with %q:\n%s", "Stopped watching.", stderr)
	}
}
