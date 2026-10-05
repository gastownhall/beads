// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/store.go@49d1df2f6)
// to OSS beads under the MIT license.
// Package httpstore is the enterprise client for the v0 `bd serve` wire: a
// storage.DoltStorage registered under the name "http" whose issueops role
// accessors speak HTTP to a remote server and whose every other method refuses
// with a typed sentinel.
//
// This package is the store skeleton — the mechanically generated refusing
// base, the sentinel, the commit-graph posture, the benign local stubs and the
// registration glue. It issues no wire calls: transport and encoding live in
// sibling packages, and each role accessor here refuses until its role bead
// wires it.
//
// See engdocs/design/http-client-backend.md.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
)

// Backend is the metadata.json backend identifier for this store.
const Backend = "http"

// ErrNoTransport reports a binary that registered this backend without linking a
// wire client. It is a build-wiring fault, not a user's: the workspace and the
// server may both be fine.
var ErrNoTransport = errors.New("this build has no http transport linked")

// Store is the http client backend.
type Store struct {
	// unsupportedDoltStorage (generated, unsupported_gen.go) provides the
	// storage.DoltStorage surface this backend refuses — the D8 allowlist v1,
	// each method returning the typed sentinel. It is generated as the exact
	// complement of this package's hand-written method set, so there is no
	// ambiguous-selector overlap.
	unsupportedDoltStorage

	target Target
	wire   WireClient
	local  *localMetadata

	// mu guards the lazily fetched handshake snapshot. D6 makes the context
	// fetch lazy (first post-baseline dispatch) and cached for the process, so
	// a store opened before any dispatch carries none.
	mu     sync.Mutex
	ctxRes *apigen.ContextResponse

	// policy settles, once per store, whether the server enforces bd's
	// external-dependency policy itself (policy.go).
	policy policyProbe

	// vocabularyCache memoizes the workspace's status and type vocabulary, which
	// the `--parent` walk's derived-default inversion recognizes with
	// (vocabulary.go). It is embedded rather than a field so the zero Store the
	// refusal contract constructs stays usable.
	vocabularyCache
}

// Compile-time proof that the hand-written methods plus the generated shell
// cover the full storage seam, and that the negative maintenance marker is
// implemented. The first assertion is the skip-list drift guard: a skipped
// method this package does not actually implement is a missing method, and a
// method both implemented and stubbed is an ambiguous selector.
var (
	_ storage.DoltStorage            = (*Store)(nil)
	_ storage.NonCommitGraphBackend  = (*Store)(nil)
	_ storage.RemoteWorkspaceBackend = (*Store)(nil)
)

// New builds the store around an already-resolved target and transport.
//
// snapshot pre-seeds the handshake context, or is nil to let D6 fetch it lazily
// on the first post-baseline dispatch. The shipped core ALWAYS passes nil: open
// is the one production constructor, and connect — which does run a handshake —
// is a separate process that writes the sidecar and exits, so no in-process
// snapshot survives to the first command. It is the tests that pass one, to pin
// the server version or capability set a refusal renders without standing up a
// live handshake (see store_test.go and the cmd/bd refusal tests). It stays a
// constructor parameter rather than a private-field poke because those cmd/bd
// tests are in another package and cannot reach ctxRes.
//
// wire may be nil in tests that only exercise the refusal surface.
func New(target Target, wire WireClient, snapshot *apigen.ContextResponse) *Store {
	return &Store{target: target, wire: wire, ctxRes: snapshot}
}

// NewFromConfig opens the workspace read-write. It is the backends.Backend
// Open hook.
func NewFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return open(ctx, beadsDir)
}

// NewReadOnlyFromConfig opens the workspace for a read-only command.
//
// It is the same store: every write on this backend is either served by the
// server (which enforces its own posture) or refuses, so there is no local
// mutable state a read-only mode would need to protect. The two hooks stay
// distinct because backends.Register requires both and because a later
// read-intent handshake belongs here rather than in a caller.
func NewReadOnlyFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return open(ctx, beadsDir)
}

func open(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	target, err := LoadTarget(beadsDir)
	if err != nil {
		return nil, err
	}
	if dialer == nil {
		return nil, fmt.Errorf("%w: cannot dial %s", ErrNoTransport, target)
	}
	wire, err := dialer(ctx, target)
	if err != nil {
		return nil, err
	}
	s := New(target, wire, nil)
	s.local = newLocalMetadata(beadsDir)
	return s, nil
}

// snapshot returns the cached handshake, fetching it once if a transport is
// present. It no longer swallows the handshake error (ga-b8ddd.11, Option C):
// a wrong-server *wire.ProjectMismatchError has to reach GetMetadata so cmd/bd's
// pre-write workspace-identity check can FIRE on it rather than skip on a
// nil-turned-empty id. Per-request stamping (ga-b8ddd.12) now enforces identity
// on EVERY read and write independently — a drifted server refuses each stamped
// request before it answers — so this handshake path is the pre-write gate BESIDE
// that per-request one, no longer the only thing between a drifted server and a
// wrong-workspace answer. It dials nothing beyond the one lazy handshake
// ServerContext already owns — a successful fetch is cached, a failed one is
// reported and left uncached for the next attempt.
func (s *Store) snapshot(ctx context.Context) (*apigen.ContextResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctxRes != nil || s.wire == nil {
		return s.ctxRes, nil
	}
	res, err := s.wire.ServerContext(ctx)
	if err != nil {
		return nil, err
	}
	s.ctxRes = res
	return res, nil
}

// cachedSnapshot returns the handshake only if one has already been fetched.
// Decorating an error must never dial: a refusal is the one path that has to
// stay fast and side-effect free, and a taxonomy field is not worth a round trip
// the caller did not ask for.
func (s *Store) cachedSnapshot() *apigen.ContextResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctxRes
}

// Close releases the store. Nothing local is held open, so this is a no-op
// (design D4, "Close").
func (s *Store) Close() error { return nil }

// CommitGraphUnsupported is the negative maintenance marker (see
// storage.NonCommitGraphBackend): it tells cmd/bd's PostRun to skip the
// Dolt-only maintenance tail. Only non-Dolt backends implement it, so the
// default path is unaffected.
func (s *Store) CommitGraphUnsupported() bool { return true }

// WorkspaceMismatchRecovery names the one action that resolves a wrong-server
// identity mismatch for an http workspace (storage.RemoteWorkspaceBackend):
// there is no local database to reconcile, so bd doctor/bootstrap do not apply —
// reconnecting does. cmd/bd's pre-write identity check renders this in place of
// the Dolt recovery, sourcing the string from the wire error so the write path
// and the post-baseline read path say the same thing (design D1/D6).
func (s *Store) WorkspaceMismatchRecovery() string { return wire.ProjectMismatchRecovery }

// The commit family is a NO-OP on this backend, not unsupported (design D3).
// The server's writes are durable when it writes the response, so a client-side
// "commit" of a remote store has nothing to flush. Refusing instead would break
// working commands rather than unsupported ones: doltAutoCommit defaults on for
// a registered workspace, so `bd ready --claim` calls Commit after a claim that
// already landed, and PersistentPostRunE turns any Commit error into a failed
// exit — exit 1 after a durable claim.
func (s *Store) Commit(_ context.Context, _ string) error                { return nil }
func (s *Store) CommitWithConfig(_ context.Context, _ string) error      { return nil }
func (s *Store) CommitMergeResolution(_ context.Context, _ string) error { return nil }

// CommitAll joins that family rather than the unsupported shell, for the same
// D3 reason, and answers (false, nil): its bool reports whether a commit was
// CREATED, so `bd vc commit` prints "Nothing to commit" instead of claiming
// history this client does not own. A remote store has no working set to sweep.
func (s *Store) CommitAll(_ context.Context, _ string) (bool, error) { return false, nil }

// CommitPending reports nothing pending, for the same reason: write paths call
// it opportunistically and must not error.
func (s *Store) CommitPending(_ context.Context, _ string) (bool, error) { return false, nil }
