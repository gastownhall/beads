//go:build cgo

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// watchBannerWriter buffers a stream and signals every time new data lands,
// so a test can wait for the Nth banner (waitForRender) or for a stream to go
// idle (waitForQuiet) instead of sleeping.
type watchBannerWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	count   int
	renders chan int
}

const showWatchBanner = "Watching for changes..."

func (w *watchBannerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if now := strings.Count(w.buf.String(), showWatchBanner); now > w.count {
		w.count = now
	}
	// Ping on every write, not just a banner occurrence: waitForQuiet (used on
	// the stdout stream, which never carries the banner) needs to know this
	// buffer is still receiving data at all.
	select {
	case w.renders <- w.count:
	default:
	}
	return n, err
}

func (w *watchBannerWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForRender blocks until the banner has been printed at least want times,
// giving up when the process exits or the timeout elapses.
func (w *watchBannerWriter) waitForRender(want int, timeout time.Duration, exited <-chan struct{}) bool {
	deadline := time.After(timeout)
	for {
		w.mu.Lock()
		got := w.count
		w.mu.Unlock()
		if got >= want {
			return true
		}
		select {
		case <-w.renders:
		case <-exited:
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.count >= want
		case <-deadline:
			return false
		}
	}
}

// waitForQuiet blocks until no new data has arrived on this writer for idle,
// or the process exits, or the overall timeout elapses.
//
// The stderr "Watching for changes..." banner is written right after
// render() returns in the child process, but the test's stdout and stderr
// buffers are each filled by their own goroutine copying an independent OS
// pipe (see os/exec), so there is no happens-before relationship between
// "the banner landed in our stderr buffer" and "the render's own bytes
// landed in our stdout buffer". A render that prints several lines (the
// proxied route's full issue view ends with an unconditional trailing
// fmt.Println, written well after the earlier lines once dependency and
// comment lookups finish) can still have bytes in flight on the stdout pipe
// after the banner is already visible. Capturing a "no growth expected"
// baseline right after the banner is therefore racy: the tail of the FIRST
// render can land after the baseline is taken and look like unwanted growth.
// Waiting for the stream to go idle confirms the render already signaled by
// the banner has fully landed before anything measures growth from it.
func (w *watchBannerWriter) waitForQuiet(idle, timeout time.Duration, exited <-chan struct{}) {
	deadline := time.After(timeout)
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-w.renders:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(idle)
		case <-timer.C:
			return
		case <-exited:
			return
		case <-deadline:
			return
		}
	}
}

// showWatch is one running `bd … show … --watch` child: its stdout and stderr
// (locked writers, since a test reads them while the child is still writing)
// and the channel its Wait closes. startShowWatch creates it; interrupt stops
// it. The start / first-render / SIGINT / bounded-exit choreography lives here
// once, so TestProxiedServerShowWatch and the --comments-tail route tests
// (runShowWatchOnce) cannot drift apart.
type showWatch struct {
	args    string
	cmd     *exec.Cmd
	stdout  *watchBannerWriter
	stderr  *watchBannerWriter
	exited  chan struct{}
	waitErr error
}

// startShowWatch starts `bd <argv>` (the caller passes "show", the id and
// --watch, plus any global flags) in dir with env, registers a kill-and-reap
// cleanup, and returns once the first render banner has landed on stderr. The
// banner only proves the render STARTED: stdout is an independent pipe, so a
// caller that reads stdout waits for it to go quiet (waitForQuiet) first.
func startShowWatch(t *testing.T, bd, dir string, env []string, argv ...string) *showWatch {
	t.Helper()
	w := &showWatch{
		args:   strings.Join(argv, " "),
		stdout: &watchBannerWriter{renders: make(chan int, 1)},
		stderr: &watchBannerWriter{renders: make(chan int, 1)},
		exited: make(chan struct{}),
	}
	w.cmd = exec.Command(bd, argv...)
	w.cmd.Dir = dir
	w.cmd.Env = env
	w.cmd.Stdout = w.stdout
	w.cmd.Stderr = w.stderr
	if err := w.cmd.Start(); err != nil {
		t.Fatalf("start bd %s: %v", w.args, err)
	}
	go func() {
		w.waitErr = w.cmd.Wait()
		close(w.exited)
	}()
	t.Cleanup(func() {
		_ = w.cmd.Process.Kill()
		<-w.exited
	})
	if !w.stderr.waitForRender(1, 60*time.Second, w.exited) {
		t.Fatalf("bd %s never started watching\nstdout:\n%s\nstderr:\n%s", w.args, w.stdout.String(), w.stderr.String())
	}
	return w
}

// interrupt sends SIGINT and requires the watch to exit 0 within 30s.
func (w *showWatch) interrupt(t *testing.T) {
	t.Helper()
	if err := w.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal bd %s: %v", w.args, err)
	}
	select {
	case <-w.exited:
		if w.waitErr != nil {
			t.Fatalf("bd %s exited with %v after SIGINT, want 0\nstderr:\n%s", w.args, w.waitErr, w.stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("bd %s ignored SIGINT\nstderr:\n%s", w.args, w.stderr.String())
	}
}

// TestProxiedServerShowWatch pins `bd show --watch` under --proxied-server to
// the direct route's contract: render once, redraw when the issue's
// status/updated_at snapshot changes, and stop cleanly on SIGINT. Proxied mode
// used to refuse it outright (proxy.watch.unsupported), so every consumer on
// the default proxied transport had to reimplement the poll itself.
func TestProxiedServerShowWatch(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("redraws_on_change_and_stops_on_interrupt", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swr")
		issue := bdProxiedCreate(t, bd, p.dir, "Watch me", "--type", "task")

		w := startShowWatch(t, bd, p.dir, bdProxiedEnv(p.dir), "show", issue.ID, "--watch")
		stdout, stderr := w.stdout, w.stderr
		if strings.Contains(stderr.String(), "not supported") {
			t.Fatalf("bd show --watch refused under --proxied-server:\n%s", stderr.String())
		}

		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--status", "in_progress")

		if !stderr.waitForRender(2, 60*time.Second, w.exited) {
			t.Fatalf("bd show --watch did not redraw after the status change\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}

		w.interrupt(t)
		if !strings.Contains(stderr.String(), "Stopped watching.") {
			t.Errorf("stderr lacks the stop line:\n%s", stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "Watch me"); got < 2 {
			t.Errorf("stdout rendered the issue %d time(s), want an initial render and a redraw:\n%s", got, out)
		}
		if !strings.Contains(strings.ToUpper(out), "IN_PROGRESS") {
			t.Errorf("redraw does not show the new status:\n%s", out)
		}
	})

	// A poll that fails keeps the last render and prints nothing: the route
	// used to print "Error refreshing <id>" every 2s once the issue was
	// deleted, and under --json an error object on stdout each time.
	t.Run("deleted_issue_polls_stay_quiet", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swd")
		issue := bdProxiedCreate(t, bd, p.dir, "Watch then delete", "--type", "task")

		w := startShowWatch(t, bd, p.dir, bdProxiedEnv(p.dir), "--json", "show", issue.ID, "--watch")
		stdout, stderr, exited := w.stdout, w.stderr, w.exited
		// The banner only proves the render STARTED (it is printed right
		// after render() returns, on an independent pipe from stdout); wait
		// for stdout itself to go idle before trusting its length as a
		// growth baseline, or the initial render's own trailing bytes can
		// still be in flight and look like post-delete growth.
		stdout.waitForQuiet(500*time.Millisecond, 10*time.Second, exited)
		before := len(stdout.String())
		bdProxiedDelete(t, bd, p.dir, issue.ID, "--force")

		// Several poll intervals, so a per-tick report would have fired.
		select {
		case <-exited:
			t.Fatalf("bd show --watch exited after the issue was deleted\nstderr:\n%s", stderr.String())
		case <-time.After(3 * showWatchPollInterval):
		}
		if after := stdout.String()[before:]; after != "" {
			t.Errorf("stdout grew after the delete:\n%s", after)
		}
		if strings.Contains(stderr.String(), "Error") || strings.Contains(stderr.String(), "not found") {
			t.Errorf("stderr reported the failed polls:\n%s", stderr.String())
		}
	})

	t.Run("missing_id_fails_instead_of_watching", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swm")
		stdout, stderr, err, timedOut := bdProxiedRunDeadline(t, bd, p.dir, 60*time.Second, "show", "swm-nonexistent999", "--watch")
		if timedOut {
			t.Fatalf("bd show --watch on a missing id kept watching\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if err == nil {
			t.Fatalf("bd show --watch on a missing id exited 0\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if want := "Issue swm-nonexistent999 not found"; !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	})

	t.Run("requires_exactly_one_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swo")
		a := bdProxiedCreate(t, bd, p.dir, "Watch A", "--type", "task")
		b := bdProxiedCreate(t, bd, p.dir, "Watch B", "--type", "task")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, a.ID, b.ID, "--watch")
		if combined := stdout + stderr; !strings.Contains(combined, "watch mode requires exactly one issue ID") {
			t.Errorf("want the direct route's one-id error, got:\n%s", combined)
		}
	})
}
