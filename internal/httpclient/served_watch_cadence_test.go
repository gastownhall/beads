//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_watch_cadence_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// The cadence knob, exercised from the package that asked for it.
//
// WHY IT IS HERE AND NOT ONLY IN internal/httpapi. The in-package cases prove
// that Listen wires Config.WatchCadence through to the stream loop, and they
// prove it end to end over a socket — but they are written with watchPoll and
// watchBeat in scope, so they cannot distinguish a knob a consumer can reach
// from one only the package itself can. The served-surface tier is the
// consumer: it stands a real bd serve up in process, in another package, from
// exported surface alone, which is exactly the position a served-watch
// conformance suite occupies.
//
// WHAT IT MEASURES IS THE CLOCK. An idle stream emits a heartbeat comment at
// either cadence; the override is the difference between forty milliseconds and
// twenty seconds. So the case bounds the wait far below the production
// heartbeat — with the knob wired it returns almost immediately, and with it
// severed it fails on the bound rather than passing slowly.
//
// It deliberately does NOT go through newServedEnv. That harness is the role
// contracts' and its shape belongs to the suite that owns them; this needs a
// server with one extra Config field and a raw text/event-stream reader, which
// is a smaller thing than a role fixture.
func TestTheServedStreamHonorsAShrunkCadence(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	reference, err := embeddeddolt.Open(ctx, t.TempDir(), servedDatabase, "main")
	if err != nil {
		t.Fatalf("open the reference store: %v", err)
	}
	t.Cleanup(func() { _ = reference.Close() })
	if err := reference.SetConfig(ctx, "issue_prefix", "hwcd"); err != nil {
		t.Fatalf("set the issue prefix: %v", err)
	}

	cfg := serveConfig(t, reference)
	cfg.Addr = "127.0.0.1:0"
	// The whole point of the file, written in the only vocabulary another
	// package has: an exported constructor that demands this test's own TB.
	cfg.WatchCadence = httpapi.TestOnlyWatchCadence(t, 5*time.Millisecond, 40*time.Millisecond)

	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("bind the in-process server: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the in-process server did not shut down")
		}
	})

	stream := "http://" + srv.Addr() + "/v0/beads/events:watch?since=0"
	reqCtx, hangUp := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(hangUp)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, stream, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// No whole-response timeout on the client: the response never completes, and
	// the request context is what ends it.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET events:watch = %d, want 200", resp.StatusCode)
	}

	frames := bufio.NewReader(resp.Body)
	// The bound is generous against the shrunk cadence (40ms) and impossible
	// against the production one (20s), which is what makes it an assertion
	// about the knob rather than about the runner.
	const bound = 5 * time.Second

	if got := frameWithinBound(t, frames, bound); !strings.HasPrefix(got, "retry: ") {
		t.Fatalf("first frame = %q, want the reconnection delay", got)
	}
	if got := frameWithinBound(t, frames, bound); got != ": heartbeat" {
		t.Fatalf("idle frame = %q, want a heartbeat comment", got)
	}
}

// frameWithinBound reads one SSE frame and fails the case if it takes longer
// than the bound.
//
// The read runs on its own goroutine because a t.Fatalf there is not allowed and
// because the bound is the assertion: a blocking read against the production
// heartbeat would otherwise return the same frame twenty seconds later and pass.
// The reader unblocks when the cleanup closes the body.
func frameWithinBound(t *testing.T, frames *bufio.Reader, bound time.Duration) string {
	t.Helper()

	type read struct {
		frame string
		err   error
	}
	out := make(chan read, 1)
	go func() {
		var lines []string
		for {
			line, err := frames.ReadString('\n')
			if err != nil {
				out <- read{err: err}
				return
			}
			if line == "\n" {
				out <- read{frame: strings.Join(lines, "\n")}
				return
			}
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}
	}()

	select {
	case got := <-out:
		if got.err != nil {
			t.Fatalf("reading the stream: %v", got.err)
		}
		return got.frame
	case <-time.After(bound):
		t.Fatalf("no frame within %s; the served stream is running the production cadence, so httpapi.Config.WatchCadence did not reach it", bound)
		return ""
	}
}
