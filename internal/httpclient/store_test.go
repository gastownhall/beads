// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/store_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
)

// fakeWire answers the handshake and nothing else.
//
// The embedded WriteWire is nil by design: it makes this double satisfy the
// transport seam however the role beads grow it, and it makes calling an
// operation it does not answer a panic rather than a silent zero — which is what
// a test that reached for one would deserve. A test that DOES need a write
// operation embeds its own stub instead (see the role tests).
type fakeWire struct {
	WriteWire

	res  *apigen.ContextResponse
	err  error
	hits int

	// preflight and do stand in for the dispatch half of the seam. Nil means
	// "this test does not dispatch": both fail loudly rather than answering
	// something plausible, so a test that starts dispatching by accident says
	// so instead of passing on a zero value.
	preflight func(ctx context.Context, op string) error
	do        func(ctx context.Context, req wire.Request, out any) error
}

func (f *fakeWire) ServerContext(context.Context) (*apigen.ContextResponse, error) {
	f.hits++
	return f.res, f.err
}

func (f *fakeWire) Preflight(ctx context.Context, op string) error {
	if f.preflight == nil {
		return fmt.Errorf("fakeWire: unexpected pre-flight for %q", op)
	}
	return f.preflight(ctx, op)
}

func (f *fakeWire) Do(ctx context.Context, req wire.Request, out any) error {
	if f.do == nil {
		return fmt.Errorf("fakeWire: unexpected dispatch of %q", req.Op)
	}
	return f.do(ctx, req, out)
}

// pinnedProjectTarget is this file's own Target builder, distinct from
// helpers_test.go's testTarget: several tests here assert against this
// specific URL and ExpectProjectID (the identity-mismatch and GetMetadata
// project-id tests), so it cannot share the generic "nothing pinned yet"
// helper. S3 reconciliation (gc native-program, 2026-10) renamed it off
// testTarget — the lift had declared two package-scope functions under that
// one name (here and in helpers_test.go), which never compiled.
func pinnedProjectTarget(t *testing.T) Target {
	t.Helper()
	u, err := url.Parse("http://127.0.0.1:7777")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return Target{BaseURL: u, ExpectProjectID: "proj-1"}
}

// TestCommitFamilyIsNoOp is the D3 posture. Without it `bd ready --claim` exits
// 1 after a durable claim: auto-commit defaults on for a registered workspace,
// the claim path calls Commit, and PostRun turns any Commit error into a failed
// exit.
func TestCommitFamilyIsNoOp(t *testing.T) {
	s := New(pinnedProjectTarget(t), nil, nil)
	ctx := context.Background()

	if err := s.Commit(ctx, "msg"); err != nil {
		t.Errorf("Commit: %v, want nil", err)
	}
	if err := s.CommitWithConfig(ctx, "msg"); err != nil {
		t.Errorf("CommitWithConfig: %v, want nil", err)
	}
	if err := s.CommitMergeResolution(ctx, "msg"); err != nil {
		t.Errorf("CommitMergeResolution: %v, want nil", err)
	}
	pending, err := s.CommitPending(ctx, "actor")
	if err != nil {
		t.Errorf("CommitPending: %v, want nil", err)
	}
	if pending {
		t.Error("CommitPending reported pending work; a remote store has none")
	}
	committed, err := s.CommitAll(ctx, "msg")
	if err != nil {
		t.Errorf("CommitAll: %v, want nil", err)
	}
	if committed {
		t.Error("CommitAll claimed it created a commit; a remote store has no working set")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v, want nil", err)
	}
}

// TestCommitFamilyIsNotOnTheUnsupportedMap guards the other direction: if the
// commit family ever drifts onto the allowlist, the no-ops above are dead code
// and the claim path breaks.
func TestCommitFamilyIsNotOnTheUnsupportedMap(t *testing.T) {
	for _, name := range []string{"Commit", "CommitWithConfig", "CommitMergeResolution", "CommitAll", "CommitPending", "Close"} {
		if reason, ok := legitimatelyUnsupported[name]; ok {
			t.Errorf("%q is on the unsupported allowlist (%q) but D3/D4 require a no-op", name, reason)
		}
	}
}

// TestRefusalUnwrapsToPortableSentinel is the classification contract the whole
// refusal surface rests on: errors.As reaches *storage.ErrUnsupported through
// the richer http sentinel, and Op names the method that refused.
func TestRefusalUnwrapsToPortableSentinel(t *testing.T) {
	// The handshake has already run, so the refusal decorates from the cache.
	// Decorating must never dial: pass no transport at all.
	s := New(pinnedProjectTarget(t), nil, &apigen.ContextResponse{
		BdVersion:    "1.2.3",
		Capabilities: []string{"issues.list", "issues.get"},
	})

	// GetMetadata on a key other than _project_id is the STABLE sample, and the
	// reason is worth stating so the next re-sampler does not rediscover it: the
	// two properties this test needs are disjoint on ROLE ACCESSORS. It needs a
	// refusal that is store-bound — only (*Store).unsupported fills in the
	// ServerURL, BdVersion and Capabilities asserted below, where the generated
	// shell's stubs hang off an empty value receiver and carry none of them — and
	// it needs one that refuses permanently. Every hand-written accessor is one
	// of D8's seventeen and eventually flips, and every permanent refuser is on
	// the shell, so no accessor is both. This method is: it is hand-written, and
	// no v0 operation exposes the metadata table beyond the identity key.
	_, err := s.GetMetadata(context.Background(), "some-other-key")
	if err == nil {
		t.Fatal("GetMetadata on a non-identity key returned no error")
	}

	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
	}
	if unsup.Op != "GetMetadata" {
		t.Errorf("Op = %q, want %q", unsup.Op, "GetMetadata")
	}
	if unsup.Backend != "http" {
		t.Errorf("Backend = %q, want %q", unsup.Backend, "http")
	}

	var httpErr *ErrHTTPUnsupported
	if !errors.As(err, &httpErr) {
		t.Fatalf("errors.As to *ErrHTTPUnsupported failed for %v", err)
	}
	if httpErr.ServerURL != "http://127.0.0.1:7777" {
		t.Errorf("ServerURL = %q, want the target", httpErr.ServerURL)
	}
	if httpErr.BdVersion != "1.2.3" {
		t.Errorf("BdVersion = %q, want it from the handshake", httpErr.BdVersion)
	}
	if got := strings.Join(httpErr.Capabilities, ","); got != "issues.get,issues.list" {
		t.Errorf("Capabilities = %q, want the advertised tokens sorted", got)
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:7777") {
		t.Errorf("Error() = %q, want it to name the server", err.Error())
	}
}

// TestRefusalDoesNotDial: a refusal is the one path that must stay fast and
// side-effect free, so decorating it uses the cached handshake or nothing.
//
// The sample has to be a STORE-BOUND refusal for this to assert anything at
// all — a generated shell stub cannot reach a transport, so it would report
// zero dials without the store having decided anything. See
// TestRefusalUnwrapsToPortableSentinel for why GetMetadata is the one method
// that is both store-bound and permanently refusing.
func TestRefusalDoesNotDial(t *testing.T) {
	wire := &fakeWire{res: &apigen.ContextResponse{BdVersion: "1.2.3"}}
	s := New(pinnedProjectTarget(t), wire, nil)

	if _, err := s.GetMetadata(context.Background(), "some-other-key"); err == nil {
		t.Fatal("GetMetadata on a non-identity key returned no error")
	}
	if wire.hits != 0 {
		t.Errorf("decorating a refusal dialed the server %d times, want 0", wire.hits)
	}
}

// TestBackstopRefusalNamesNoEmptyServer covers the generated shell's arm: it
// hangs off an empty receiver and cannot reach a target, so it must fall back to
// the portable rendering instead of printing "bd serve at " with nothing after.
//
// The sample was Commenter until client wave ga-f352s wired that accessor, at
// which point a zero Store answered the no-transport BUILD fault instead of the
// shell's refusal — a different error with a different meaning, and the test
// said so. It is Bootstrapper now, which is on the shell permanently: bootstrap
// is a server-side act, so no operation will ever put an accessor in front of it
// here.
func TestBackstopRefusalNamesNoEmptyServer(t *testing.T) {
	_, err := (&Store{}).Bootstrapper()
	if err == nil {
		t.Fatal("Bootstrapper returned no error")
	}
	if strings.Contains(err.Error(), "bd serve at") {
		t.Errorf("Error() = %q, want no dangling server clause", err.Error())
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) || unsup.Op != "Bootstrapper" {
		t.Errorf("backstop refusal did not classify: %v", err)
	}
}

// TestGetMetadataServesProjectIdentity is D4's workspace-identity disposition.
// cmd/bd swallows an error here into "skip validation", so refusing would
// silently disarm wrong-server protection.
func TestGetMetadataServesProjectIdentity(t *testing.T) {
	wire := &fakeWire{res: &apigen.ContextResponse{ProjectId: "proj-1"}}
	s := New(pinnedProjectTarget(t), wire, nil)
	ctx := context.Background()

	got, err := s.GetMetadata(ctx, "_project_id")
	if err != nil {
		t.Fatalf("GetMetadata(_project_id): %v", err)
	}
	if got != "proj-1" {
		t.Errorf("GetMetadata(_project_id) = %q, want %q", got, "proj-1")
	}

	// The handshake is fetched once and cached for the process (D6).
	if _, err := s.GetMetadata(ctx, "_project_id"); err != nil {
		t.Fatalf("second GetMetadata: %v", err)
	}
	if wire.hits != 1 {
		t.Errorf("ServerContext called %d times, want 1 (cached)", wire.hits)
	}

	// Any other key refuses rather than answering "" — that would be a dropped
	// read dressed as an empty one.
	if _, err := s.GetMetadata(ctx, "other"); err == nil {
		t.Error("GetMetadata(other) returned no error, want a refusal")
	}
}

// TestGetMetadataWithoutHandshakeSkipsValidation: ("", nil) is the documented
// "new or pre-identity database" answer, which is the honest state before a
// handshake has run.
func TestGetMetadataWithoutHandshakeSkipsValidation(t *testing.T) {
	s := New(pinnedProjectTarget(t), nil, nil)
	got, err := s.GetMetadata(context.Background(), "_project_id")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if got != "" {
		t.Errorf("GetMetadata = %q, want empty before the handshake", got)
	}
}

// TestLocalMetadataRoundTrips is D3/D4's local disposition: the deferred tip
// write in PostRun must succeed, and tips-shown timestamps are per-user state.
func TestLocalMetadataRoundTrips(t *testing.T) {
	beadsDir := t.TempDir()
	s := New(pinnedProjectTarget(t), nil, nil)
	s.local = newLocalMetadata(beadsDir)
	ctx := context.Background()

	got, err := s.GetLocalMetadata(ctx, "tip.shown")
	if err != nil {
		t.Fatalf("GetLocalMetadata on a fresh workspace: %v", err)
	}
	if got != "" {
		t.Errorf("GetLocalMetadata = %q, want empty for a missing key", got)
	}

	if err := s.SetLocalMetadata(ctx, "tip.shown", "2026-08-08"); err != nil {
		t.Fatalf("SetLocalMetadata: %v", err)
	}
	if err := s.SetLocalMetadata(ctx, "other", "value"); err != nil {
		t.Fatalf("second SetLocalMetadata: %v", err)
	}

	got, err = s.GetLocalMetadata(ctx, "tip.shown")
	if err != nil {
		t.Fatalf("GetLocalMetadata: %v", err)
	}
	if got != "2026-08-08" {
		t.Errorf("GetLocalMetadata = %q, want the written value", got)
	}

	info, err := os.Stat(filepath.Join(beadsDir, LocalMetadataFileName))
	if err != nil {
		t.Fatalf("stat local metadata file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("local metadata file mode = %o, want 600", perm)
	}
}

// TestLocalMetadataWithoutWorkspaceDoesNotError: an ephemeral --server-url
// workspace has nowhere to write, and PostRun treats a write error as fatal.
func TestLocalMetadataWithoutWorkspaceDoesNotError(t *testing.T) {
	s := New(pinnedProjectTarget(t), nil, nil)
	if err := s.SetLocalMetadata(context.Background(), "tip.shown", "now"); err != nil {
		t.Errorf("SetLocalMetadata with no workspace: %v, want nil", err)
	}
}

func TestLoadTarget(t *testing.T) {
	t.Run("missing sidecar is not connected", func(t *testing.T) {
		if _, err := LoadTarget(t.TempDir()); !errors.Is(err, ErrNotConnected) {
			t.Errorf("LoadTarget on a bare dir = %v, want ErrNotConnected", err)
		}
	})

	t.Run("round trip", func(t *testing.T) {
		dir := t.TempDir()
		want := pinnedProjectTarget(t)
		if err := SaveTarget(dir, want); err != nil {
			t.Fatalf("SaveTarget: %v", err)
		}
		got, err := LoadTarget(dir)
		if err != nil {
			t.Fatalf("LoadTarget: %v", err)
		}
		if got.String() != want.String() {
			t.Errorf("BaseURL = %q, want %q", got.String(), want.String())
		}
		if got.ExpectProjectID != want.ExpectProjectID {
			t.Errorf("ExpectProjectID = %q, want %q", got.ExpectProjectID, want.ExpectProjectID)
		}
		info, err := os.Stat(TargetPath(dir))
		if err != nil {
			t.Fatalf("stat sidecar: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("sidecar mode = %o, want 600", perm)
		}
	})

	t.Run("rejects a non-http scheme", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(TargetPath(dir), []byte(`{"url":"ftp://host/x"}`), 0o600); err != nil {
			t.Fatalf("write sidecar: %v", err)
		}
		if _, err := LoadTarget(dir); err == nil || !strings.Contains(err.Error(), "scheme") {
			t.Errorf("LoadTarget = %v, want a scheme complaint", err)
		}
	})
}

// swapDialer installs d for the duration of one test. The dialer is init-time
// wiring in production; tests are the only place it moves.
func swapDialer(t *testing.T, d WireDialer) {
	t.Helper()
	prev := dialer
	dialer = d
	t.Cleanup(func() { dialer = prev })
}

// TestOpenWithoutADialerRefuses: this bead ships no transport, so Open must say
// so plainly rather than hand back a store that cannot dial.
func TestOpenWithoutADialerRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTarget(dir, pinnedProjectTarget(t)); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	swapDialer(t, nil)

	if _, err := NewFromConfig(context.Background(), dir); !errors.Is(err, ErrNoTransport) {
		t.Fatalf("NewFromConfig without a dialer = %v, want ErrNoTransport", err)
	}
}

// TestOpenDialsThroughTheRegisteredWire proves the injection seam: the wire-core
// bead registers a dialer and Open builds a store around it, with the sidecar's
// target threaded through and the workspace bound for local metadata.
func TestOpenDialsThroughTheRegisteredWire(t *testing.T) {
	dir := t.TempDir()
	want := pinnedProjectTarget(t)
	if err := SaveTarget(dir, want); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}

	var dialed Target
	wire := &fakeWire{res: &apigen.ContextResponse{ProjectId: "proj-1"}}
	swapDialer(t, func(_ context.Context, target Target) (WireClient, error) {
		dialed = target
		return wire, nil
	})

	opened, err := NewFromConfig(context.Background(), dir)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if dialed.String() != want.String() {
		t.Errorf("dialed %q, want the sidecar's target %q", dialed.String(), want.String())
	}
	if dialed.ExpectProjectID != want.ExpectProjectID {
		t.Errorf("dialed ExpectProjectID = %q, want %q", dialed.ExpectProjectID, want.ExpectProjectID)
	}

	s, ok := opened.(*Store)
	if !ok {
		t.Fatalf("NewFromConfig returned %T, want *Store", opened)
	}
	if s.local == nil {
		t.Error("store has no local metadata file bound; the PostRun tip write would be dropped")
	}
	if err := s.SetLocalMetadata(context.Background(), "tip.shown", "now"); err != nil {
		t.Errorf("SetLocalMetadata after Open: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LocalMetadataFileName)); err != nil {
		t.Errorf("local metadata not written beside the sidecar: %v", err)
	}
}
