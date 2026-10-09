//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_project_swap_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// serveOneProject boots a reference store, seeds one open claimable row, and
// serves all sixteen roles under a chosen project id. It returns the bound base
// URL and the reference store, so a post-swap assertion can prove B's row went
// untouched. It reuses serveConfig for the role wiring and overrides only the
// published identity.
func serveOneProject(t *testing.T, projectID, prefix, issueID string) (*url.URL, *embeddeddolt.EmbeddedDoltStore) {
	t.Helper()
	ctx := context.Background()
	beadsDir := t.TempDir()
	ref, err := embeddeddolt.Open(ctx, beadsDir, servedDatabase, "main")
	if err != nil {
		t.Fatalf("open reference store for %s: %v", projectID, err)
	}
	t.Cleanup(func() { _ = ref.Close() })
	if err := ref.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set the issue prefix for %s: %v", projectID, err)
	}
	issue := &types.Issue{ID: issueID, Title: issueID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := ref.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed %s on %s: %v", issueID, projectID, err)
	}

	cfg := serveConfig(t, ref)
	cfg.Workspace = domain.ContextInfo{ProjectID: projectID, Database: servedDatabase}
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("bind the server for %s: %v", projectID, err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("the in-process server for %s did not shut down", projectID)
		}
	})

	base, err := url.Parse("http://" + srv.Addr())
	if err != nil {
		t.Fatalf("parse the bound address for %s: %v", projectID, err)
	}
	return base, ref
}

// TestServedReadsAndWritesRefuseAfterAPostHandshakeServerSwap is the headline of
// ga-b8ddd.12's client half. Two servers sit behind one reverse proxy with an
// atomically-swappable target: server A owns the workspace's project, server B a
// different one. The client connects and handshakes against A — caching A's
// identity, which matches its pin — and is then swapped, transparently, onto B.
//
// Before the stamp, the swap was a SILENT wrong-server: the cached handshake made
// the client believe it was still talking to A, and the next baseline read would
// have rendered B's rows. With the per-request stamp, the NEXT read AND a write
// both carry the pinned id, so B refuses each with the typed project-mismatch
// diagnostic before it answers — the swap is caught, not papered over.
func TestServedReadsAndWritesRefuseAfterAPostHandshakeServerSwap(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	const projA = "proj-A-the-workspace-owner"
	const projB = "proj-B-a-different-workspace"
	const target = "swap-1"
	ctx := t.Context()

	baseA, _ := serveOneProject(t, projA, "swap", target)
	baseB, refB := serveOneProject(t, projB, "swap", target)

	// The atomically-swappable target the proxy forwards to. A pointer swap is the
	// whole mechanism: the client's connection to the proxy never changes, so from
	// the client's side the identity under it changed with nothing else.
	var backend atomic.Pointer[url.URL]
	backend.Store(baseA)

	transport := http.DefaultTransport
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		to := backend.Load()
		out := r.Clone(r.Context())
		// A cloned server request cannot be used as a client request until its
		// RequestURI is cleared; the scheme and host are the only fields that route
		// it to the current backend.
		out.RequestURI = ""
		out.URL.Scheme = to.Scheme
		out.URL.Host = to.Host

		// The proxy DELIBERATELY does NOT rewrite Host. r.Clone preserves the
		// inbound Host — the proxy's own loopback authority — and it is left that
		// way on purpose: the served host gate admits it because it strips the port
		// and sees a loopback host, so the request clears the gate and REACHES the
		// stamp check. A future "fix" that set out.Host = to.Host would make the
		// forwarded request indistinguishable from one aimed straight at B, and a
		// wrong Host would be turned away by the host gate (a DIFFERENT 400 that
		// carries no server_project_id) before the stamp ran — the assertions below
		// would then pass for the wrong reason, or fail. Leave out.Host untouched.

		resp, err := transport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	base, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse the proxy URL: %v", err)
	}
	client, err := wire.New(base, nil, wire.Options{ExpectProjectID: projA})
	if err != nil {
		t.Fatalf("build the client: %v", err)
	}
	subject := New(Target{BaseURL: base}, client, nil)

	reader, err := subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	claimer, err := subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}

	// Connect + handshake against A: this caches A's identity, and it matches the
	// pin, so the client is now (correctly) convinced it is talking to its own
	// workspace. A baseline read against A must also succeed, so the post-swap
	// refusal below is unambiguously the swap's doing and not a client that refuses
	// everything.
	snap, err := client.ServerContext(ctx)
	if err != nil {
		t.Fatalf("handshake against A: %v", err)
	}
	if snap.ProjectId != projA {
		t.Fatalf("handshake cached project %q, want A's %q", snap.ProjectId, projA)
	}
	if _, err := reader.List(ctx, issueops.ListRequest{}); err != nil {
		t.Fatalf("a read against the matching server A failed before the swap: %v", err)
	}

	// The swap. Nothing about the client or its cached handshake changes; only the
	// server on the other end of the proxy does.
	backend.Store(baseB)

	// The NEXT read refuses with the typed mismatch — not a silent page of B's rows.
	if _, err := reader.List(ctx, issueops.ListRequest{}); !errors.Is(err, wire.ErrProjectMismatch) {
		t.Fatalf("the post-swap read returned %v, want a wire.ErrProjectMismatch refusal", err)
	} else {
		assertSwapMismatch(t, err, projA, projB)
	}

	// And a write refuses the same way, before it can mutate B.
	_, claimErr := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "alice", IssueID: target})
	if !errors.Is(claimErr, wire.ErrProjectMismatch) {
		t.Fatalf("the post-swap claim returned %v, want a wire.ErrProjectMismatch refusal", claimErr)
	}
	assertSwapMismatch(t, claimErr, projA, projB)

	// The write refused before it dispatched: B's row is exactly as seeded, open
	// and unclaimed. The server raises project_mismatch ahead of any unit of work,
	// so a refused claim on B mutates nothing.
	after, err := refB.GetIssue(ctx, target)
	if err != nil {
		t.Fatalf("re-read B's target row: %v", err)
	}
	if after.Status != types.StatusOpen || after.Assignee != "" {
		t.Errorf("B's row = {status %q, assignee %q}, want it still open and unclaimed — the refused claim wrote", after.Status, after.Assignee)
	}
}

// assertSwapMismatch pins that the refusal is the wrong-server one, naming both
// ids — which is what tells the stamp check apart from a host-gate refusal.
func assertSwapMismatch(t *testing.T, err error, expected, serverOwns string) {
	t.Helper()
	var mismatch *wire.ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error is %T (%v), want *wire.ProjectMismatchError", err, err)
	}
	if mismatch.Expected != expected {
		t.Errorf("mismatch.Expected = %q, want the workspace's %q", mismatch.Expected, expected)
	}
	if mismatch.Got != serverOwns {
		t.Errorf("mismatch.Got = %q, want the swapped-in server's %q", mismatch.Got, serverOwns)
	}
}
