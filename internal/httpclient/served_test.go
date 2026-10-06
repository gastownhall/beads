//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// The served-surface composition: the http client, a real in-process `bd serve`,
// and the embedded-Dolt reference store the server serves.
//
// This is the shape composition_test.go specifies, stood up. Behavior parity for
// this backend cannot be proved by bare RunAll — its Factory contract wants a
// write-ready store and its cases seed through raw CreateIssue/SetConfig, both of
// which this backend refuses — so the per-role fixture contracts are run instead,
// with their SEED HANDLES BOUND TO THE REFERENCE STORE and only the subject role
// bound to the client. A fixture that seeded through the client would prove the
// client agrees with itself.
//
// ONE STORE AND ONE SERVER FOR THE WHOLE PACKAGE, which is the unit-of-work
// wiring's arrangement rather than the embedded one's. Every contract case here
// scopes its own query — by label, by exact id, or by a query expression naming
// its own label — precisely so several of them can share one database, and a
// store per case would cost half a second each for nothing. It also makes the
// composition harder rather than easier: a filter this client dropped would come
// back carrying another case's rows.

// servedIssuePrefix is the id namespace every fixture below sub-namespaces. It
// is not the workspace's `issue_prefix`, which the reference store sets for
// itself; explicit-id creates do not check one against the other.
const servedIssuePrefix = "http"

type servedComposition struct {
	// reference is the store the server serves. It is the seed handle and the
	// dual-run oracle, and it is NEVER the subject of a contract.
	reference *embeddeddolt.EmbeddedDoltStore
	dataDir   string
	database  string
	baseURL   string
	// client is the http store under test.
	client *Store
}

var (
	compositionOnce sync.Once
	compositionEnv  *servedComposition
	compositionErr  error
	compositionStop func()
)

// TestMain owns the composition's lifetime: one embedded database and one bound
// listener for the package, torn down after the last case.
func TestMain(m *testing.M) {
	code := m.Run()
	if compositionStop != nil {
		compositionStop()
	}
	os.Exit(code)
}

func composition(t *testing.T) *servedComposition {
	t.Helper()
	compositionOnce.Do(func() { compositionEnv, compositionStop, compositionErr = startServedComposition() })
	if compositionErr != nil {
		t.Fatalf("stand up the served composition: %v", compositionErr)
	}
	return compositionEnv
}

func startServedComposition() (*servedComposition, func(), error) {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "httpstore-served-")
	if err != nil {
		return nil, nil, err
	}
	cleanups := []func(){func() { _ = os.RemoveAll(root) }}
	stop := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(err error) (*servedComposition, func(), error) {
		stop()
		return nil, nil, err
	}

	const database = "httpstore"
	beadsDir := filepath.Join(root, ".beads")
	reference, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		return fail(fmt.Errorf("open the embedded reference store: %w", err))
	}
	cleanups = append(cleanups, func() { _ = reference.Close() })
	if err := reference.SetConfig(ctx, "issue_prefix", servedIssuePrefix); err != nil {
		return fail(fmt.Errorf("identify the reference workspace: %w", err))
	}
	if err := reference.Commit(ctx, "bd init"); err != nil {
		return fail(fmt.Errorf("commit the reference workspace: %w", err))
	}

	cfg, err := serveRoles(reference)
	if err != nil {
		return fail(err)
	}
	cfg.Addr = "127.0.0.1:0"
	cfg.Stdout = io.Discard
	cfg.Stderr = io.Discard
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		return fail(fmt.Errorf("bind the in-process bd serve: %w", err))
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(serveCtx) }()
	cleanups = append(cleanups, func() { cancelServe(); <-done })

	base, err := url.Parse("http://" + srv.Addr())
	if err != nil {
		return fail(err)
	}
	transport, err := wire.New(base, nil, wire.Options{})
	if err != nil {
		return fail(fmt.Errorf("build the wire client: %w", err))
	}

	return &servedComposition{
		reference: reference,
		dataDir:   filepath.Join(beadsDir, "embeddeddolt"),
		database:  database,
		baseURL:   base.String(),
		client:    New(Target{BaseURL: base}, servedWire{transport}, nil),
	}, stop, nil
}

// serveRoles takes the whole role set off the reference store's own accessors.
//
// httpapi.Listen requires ALL of them together — a server missing one would
// bind, answer every other route, and fail that one with a nil dereference on a
// live server — and it refuses a hook-firing role. These come off a raw store
// with no decorators, so there is nothing to unwrap.
func serveRoles(store storage.DoltStorage) (httpapi.Config, error) {
	var cfg httpapi.Config
	var err error
	if cfg.Reader, err = store.IssueReader(); err != nil {
		return cfg, err
	}
	if cfg.Claimer, err = store.IssueClaimer(); err != nil {
		return cfg, err
	}
	if cfg.ReadyClaimer, err = store.ReadyClaimer(); err != nil {
		return cfg, err
	}
	if cfg.Releaser, err = store.Releaser(); err != nil {
		return cfg, err
	}
	if cfg.Lifecycle, err = store.IssueLifecycle(); err != nil {
		return cfg, err
	}
	if cfg.Settings, err = store.WorkspaceConfig(); err != nil {
		return cfg, err
	}
	if cfg.Stats, err = store.StatsReporter(); err != nil {
		return cfg, err
	}
	if cfg.CycleDetector, err = store.CycleDetector(); err != nil {
		return cfg, err
	}
	if cfg.EdgeReader, err = store.EdgeReader(); err != nil {
		return cfg, err
	}
	if cfg.GraphCounter, err = store.GraphCounter(); err != nil {
		return cfg, err
	}
	if cfg.Relations, err = store.IssueRelations(); err != nil {
		return cfg, err
	}
	if cfg.Commenter, err = store.Commenter(); err != nil {
		return cfg, err
	}
	if cfg.BlockingAnnotator, err = store.BlockingAnnotator(); err != nil {
		return cfg, err
	}
	if cfg.TreeWalker, err = store.TreeWalker(); err != nil {
		return cfg, err
	}
	if cfg.ReadyCounter, err = store.ReadyCounter(); err != nil {
		return cfg, err
	}
	if cfg.Counter, err = store.Counter(); err != nil {
		return cfg, err
	}
	if cfg.Querier, err = store.Querier(); err != nil {
		return cfg, err
	}
	if cfg.Sweeper, err = store.Sweeper(); err != nil {
		return cfg, err
	}
	if cfg.Deleter, err = store.Deleter(); err != nil {
		return cfg, err
	}
	if cfg.BatchCreator, err = store.BatchCreator(); err != nil {
		return cfg, err
	}
	if cfg.BatchCloser, err = store.BatchCloser(); err != nil {
		return cfg, err
	}
	if cfg.DependencyEditor, err = store.DependencyEditor(); err != nil {
		return cfg, err
	}
	if cfg.MetadataCAS, err = store.MetadataCAS(); err != nil {
		return cfg, err
	}
	if cfg.BatchApplier, err = store.BatchApplier(); err != nil {
		return cfg, err
	}
	if cfg.Memories, err = store.Memories(); err != nil {
		return cfg, err
	}
	if cfg.BatchGetter, err = store.BatchGetter(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// servedWire adapts *wire.Client onto the store's transport seam.
//
// It exists here rather than in the package because the production adapter is
// the ACTIVATION path's — `bd connect` resolves a target, builds the credential
// ladder and registers the dialer — and inventing half of it here would be a
// second wiring for that bead to reconcile. What the seam actually needs from
// the transport is Preflight and Do, and *wire.Client satisfies both verbatim;
// only the handshake accessor is shaped differently, because the store caches
// the ContextResponse and the client caches the gated Snapshot around it.
type servedWire struct{ *wire.Client }

func (w servedWire) ServerContext(ctx context.Context) (*apigen.ContextResponse, error) {
	snap, err := w.Client.Handshake(ctx)
	if err != nil {
		return nil, err
	}
	return &snap.Context, nil
}

// tamperedClient is a second client onto the same server whose outbound
// requests are rewritten on the way out.
//
// It exists to produce RED evidence: a composition that could not tell a
// correctly encoded request from a subtly wrong one would pass every test in
// this package while shipping a client that answers the wrong question. The
// tamper runs after the encoder and before the transport, which is exactly
// where a hand-written encoder's mistakes live.
func (c *servedComposition) tamperedClient(t *testing.T, tamper func(*wire.Request)) *Store {
	t.Helper()
	base, err := url.Parse(c.baseURL)
	if err != nil {
		t.Fatalf("parse the composition base URL: %v", err)
	}
	transport, err := wire.New(base, nil, wire.Options{})
	if err != nil {
		t.Fatalf("build a second wire client: %v", err)
	}
	return New(Target{BaseURL: base}, tamperedWire{WireClient: servedWire{transport}, tamper: tamper}, nil)
}

type tamperedWire struct {
	WireClient
	tamper func(*wire.Request)
}

func (w tamperedWire) Do(ctx context.Context, req wire.Request, out any) error {
	w.tamper(&req)
	return w.WireClient.Do(ctx, req, out)
}

// seedIssue is the seed handle every fixture binds: the REFERENCE store's own
// create, never the client's. The client refuses CreateIssue outright, so this
// is not merely a convention here — it is the only thing that can seed at all.
func (c *servedComposition) seedIssue(ctx context.Context, issue *types.Issue, actor string) error {
	return c.reference.CreateIssue(ctx, issue, actor)
}

func (c *servedComposition) seedDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return c.reference.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true})
}

func (c *servedComposition) seedComment(ctx context.Context, issueID, author, text string) error {
	_, err := c.reference.AddIssueComment(ctx, issueID, author, text)
	return err
}

// queryScalar is the composition's raw-row hook: a single-row query against the
// REFERENCE store, which is where the rows a contract seeded actually live.
//
// It opens its own SQL handle per call, exactly as the per-case harness does
// (served_harness_test.go states the measurement): a handle held for the life of
// the composition and a live server on the same database deadlock, and the
// symptom is a hung run rather than a failing one.
//
// It exists because a count or a listing cannot tell a row it EXCLUDED from a
// row that was never seeded — both surfaces hide it — so the cases that assert
// an exclusion read the stored row directly.
func (c *servedComposition) queryScalar(ctx context.Context, query string, args []any, dest ...any) error {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, c.dataDir, c.database, "main")
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	return db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

// countHistory reads the reference branch's commit log. The client cannot
// observe history at all — v0 publishes no history surface — so the
// writes-nothing cases would otherwise skip themselves, which is the one thing
// they exist to prevent.
func (c *servedComposition) countHistory(ctx context.Context) (int, error) {
	var entries int
	if err := c.queryScalar(ctx, "SELECT COUNT(*) FROM dolt_log", nil, &entries); err != nil {
		return 0, err
	}
	return entries, nil
}

// fixture binds the composition into the shell's servedFixture shape, which
// re-checks the one invariant that cannot be read off a wiring later: the seed
// handle is not the subject.
func (c *servedComposition) fixture(t *testing.T) servedFixture {
	t.Helper()
	return newServedFixture(t, c.reference, c.client, c.baseURL)
}

// bind is how every case below reaches its role: through servedFixture.bindRole,
// which returns the accessor's error UNCHANGED. A contract wired before its role
// bead lands therefore FAILS rather than skips — refusals are owned by
// RunUnsupportedContract, not by skipped conformance cases.
func bindRole[T any](t *testing.T, f servedFixture, accessor func(*Store) (T, error)) T {
	t.Helper()
	var role T
	if err := f.bindRole(func(s *Store) error {
		var err error
		role, err = accessor(s)
		return err
	}); err != nil {
		t.Fatalf("binding the role against the http client: %v", err)
	}
	return role
}

// assertServedRefusal is the shape a parked case's refusal must still have: the
// contract cannot run, but the REFUSAL is itself a promise — it names a field,
// it classifies as unsupported, and it is not a silent drop.
func assertServedRefusal(t *testing.T, what string, err error, field string) {
	t.Helper()
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("%s = %v, want a typed *storage.ErrUnsupported", what, err)
	}
	var inexpressible *InexpressibleError
	if !errors.As(err, &inexpressible) {
		t.Fatalf("%s = %v, want an *InexpressibleError carrying its ledger row", what, err)
	}
	if inexpressible.Refused.Row.Field != field {
		t.Errorf("%s refused over %q, want the refusal to name %q", what, inexpressible.Refused.Row.Field, field)
	}
}
