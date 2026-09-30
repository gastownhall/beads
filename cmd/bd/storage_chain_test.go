package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/telemetry"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// stubChainStore is a stand-in for a concrete DoltStorage. The embedded
// interface exists only for decorator identity tests; ActiveDatabaseSize is
// implemented explicitly so the sizing-capability test never reaches a nil
// promoted method.
type stubChainStore struct {
	storage.DoltStorage
	databaseSize int64
}

func (s *stubChainStore) ActiveDatabaseSize(context.Context) (int64, error) {
	return s.databaseSize, nil
}

// clearTelemetryEnv is defined once for the package, in
// command_telemetry_test.go; it unsets every BD_OTEL_* / OTEL_* variable
// telemetry.Enabled or the SDK looks at, so each test starts from a known
// baseline.

func TestWireStorageDecorators_NilStorePassesThrough(t *testing.T) {
	if got := wireStorageDecorators(nil, hooks.NewRunner("/nonexistent"), false); got != nil {
		t.Errorf("wireStorageDecorators(nil, ...) = %v; want nil", got)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	ext, ok := hf.Unwrap().(*externaldeps.Store)
	if !ok {
		t.Fatalf("second decorator: got %T; want *externaldeps.Store", hf.Unwrap())
	}
	if inner := ext.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("external dependency policy should wrap raw store directly when telemetry off; got %T", inner)
	}
}

// Asserts the full HookFiringStore → externaldeps.Store → InstrumentedStorage
// → raw chain that the rest of bd depends on for storage spans + bd.storage.* / bd.issue.count
// metrics. This is the regression test for the original PR-3475 bug, where
// WrapStorage was implemented but never called.
func TestWireStorageDecorators_TelemetryOn_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	ext, ok := hf.Unwrap().(*externaldeps.Store)
	if !ok {
		t.Fatalf("second decorator: got %T; want *externaldeps.Store", hf.Unwrap())
	}
	inst, ok := ext.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("middle decorator: got %T; want *telemetry.InstrumentedStorage", ext.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}

	if peeled := storage.UnwrapStore(got); peeled.(*stubChainStore) != raw {
		t.Errorf("storage.UnwrapStore should peel both decorator layers; got %T", peeled)
	}
}

func TestDoltBackupSizeUnwrapsStorageDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{databaseSize: 99}
	wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	size, available, err := doltBackupSizeForStore(t.Context(), wrapped)
	if err != nil {
		t.Fatalf("doltBackupSizeForStore: %v", err)
	}
	if !available || size != 99 {
		t.Fatalf("doltBackupSizeForStore = (%d, %v), want (99, true)", size, available)
	}
}

func TestGCStoreSizeUnwrapsStorageDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{databaseSize: 99}
	wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	if got := storeSizeBytesForStore(t.Context(), wrapped); got != 99 {
		t.Fatalf("storeSizeBytesForStore = %d, want 99", got)
	}
}

func TestWireStorageDecorators_TelemetryOn_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)

	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	inst, ok := ext.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("expected *telemetry.InstrumentedStorage when hooks disabled; got %T", ext.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)
	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	if ext.Unwrap().(*stubChainStore) != raw {
		t.Errorf("with telemetry off and hooks disabled, expected external decorator around raw store; got %T", ext.Unwrap())
	}
}

func TestWireStorageDecorators_NilHookRunner(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, nil, false)
	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	if ext.Unwrap().(*stubChainStore) != raw {
		t.Errorf("with telemetry off and nil hookRunner, expected external decorator around raw store; got %T", ext.Unwrap())
	}
}

// policyChainStub is a whole store whose ready, claim, count and close roles
// RECORD the request they were handed, and whose external-edge reads are
// counted — or, for a client of a policy-enforcing server, fail the test.
// Every other accessor comes from the serve stubs through the serveRoleSource
// interface, which promotes the accessors without promoting their Unwrap.
type policyChainStub struct {
	serveRoleSource
	serveStubRest

	t           *testing.T
	forbidEdges bool
	edges       map[string][]*types.Dependency
	edgeReads   int

	ready   []issueops.ReadyRequest
	counted []issueops.ReadyRequest
	listed  []issueops.ReadyListRequest
	listing issueops.ReadyListing
	claims  []issueops.ClaimNextRequest
	batches []issueops.CloseBatchRequest
	closes  []issueops.CloseRequest
}

func newPolicyChainStub(t *testing.T) *policyChainStub {
	return &policyChainStub{
		t: t,
		serveRoleSource: &serveRolesStore{
			reader: &serveStubReader{}, claimer: &serveStubClaimer{}, batchCloser: &serveStubBatchCloser{},
			readyClaimer: &serveStubReadyClaimer{}, releaser: &serveStubReleaser{}, lifecycle: &serveStubLifecycle{},
			dependencies: &serveStubDependencyEditor{}, batchApplier: &serveStubBatchApplier{},
			metadataCAS: &serveStubMetadataCAS{}, counter: &serveStubCounter{}, edgeCounter: &serveStubGraphCounter{},
			relations: &serveStubRelations{}, commenter: &serveStubCommenter{}, batchCreator: &serveStubBatchCreator{},
		},
	}
}

func (s *policyChainStub) readEdges() (map[string][]*types.Dependency, error) {
	if s.forbidEdges {
		s.t.Errorf("a client of a policy-enforcing server read dependency records")
		return nil, errors.New("forbidden")
	}
	s.edgeReads++
	return s.edges, nil
}

func (s *policyChainStub) GetExternalBlockingDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	return s.readEdges()
}

func (s *policyChainStub) GetAllDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	return s.readEdges()
}

func (s *policyChainStub) GetReadyWork(context.Context, types.WorkFilter) ([]*types.Issue, error) {
	return nil, nil
}

func (s *policyChainStub) IssueReader() (issueops.Reader, error) { return policyStubReader{s}, nil }
func (s *policyChainStub) ReadyCounter() (issueops.ReadyCounter, error) {
	return policyStubCounter{s}, nil
}
func (s *policyChainStub) ReadyLister() (issueops.ReadyLister, error) {
	return policyStubLister{s}, nil
}
func (s *policyChainStub) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return policyStubClaimer{s}, nil
}
func (s *policyChainStub) BatchCloser() (issueops.BatchCloser, error) {
	return policyStubCloser{s}, nil
}
func (s *policyChainStub) IssueLifecycle() (issueops.Lifecycle, error) {
	return policyStubLifecycle{s}, nil
}
func (s *policyChainStub) Close() error { return nil }

type policyStubReader struct{ *policyChainStub }

func (r policyStubReader) Ready(_ context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	r.ready = append(r.ready, req)
	return issueops.IssuePage{}, nil
}
func (policyStubReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, nil
}
func (policyStubReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, issueops.ErrNotFound
}

type policyStubCounter struct{ *policyChainStub }

func (c policyStubCounter) CountReady(_ context.Context, req issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	c.counted = append(c.counted, req)
	return issueops.ReadyCountResult{}, nil
}

type policyStubLister struct{ *policyChainStub }

func (l policyStubLister) ListReady(_ context.Context, req issueops.ReadyListRequest) (issueops.ReadyListing, error) {
	l.listed = append(l.listed, req)
	listing := l.listing
	if listing.Items == nil {
		listing.Items = []*types.IssueWithCounts{}
	}
	return listing, nil
}

type policyStubClaimer struct{ *policyChainStub }

func (c policyStubClaimer) ClaimNext(_ context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	c.claims = append(c.claims, req)
	return issueops.ClaimNextResult{}, nil
}

type policyStubCloser struct{ *policyChainStub }

func (c policyStubCloser) CloseBatch(_ context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	c.batches = append(c.batches, req)
	result := issueops.CloseBatchResult{Outcomes: make([]issueops.CloseOutcome, len(req.Items))}
	for i, item := range req.Items {
		result.Outcomes[i] = issueops.CloseOutcome{IssueID: item.IssueID}
	}
	return result, nil
}

type policyStubLifecycle struct{ *policyChainStub }

func (policyStubLifecycle) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	return issueops.CreateResult{}, nil
}
func (policyStubLifecycle) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	return issueops.UpdateResult{}, nil
}
func (l policyStubLifecycle) Close(_ context.Context, req issueops.CloseRequest) (issueops.CloseResult, error) {
	l.closes = append(l.closes, req)
	return issueops.CloseResult{}, nil
}
func (policyStubLifecycle) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	return issueops.ReopenResult{}, nil
}

// serverEnforcedChainStub is a client of a bd server that applies the policy.
type serverEnforcedChainStub struct{ *policyChainStub }

func (serverEnforcedChainStub) PolicyEnforcedByServer() bool { return true }

func TestWireStorageDecorators_ServerEnforcedStoreSkipsPolicy(t *testing.T) {
	for _, telemetryOn := range []bool{false, true} {
		clearTelemetryEnv(t)
		if telemetryOn {
			t.Setenv("BD_OTEL_STDOUT", "true")
		}
		raw := serverEnforcedChainStub{newPolicyChainStub(t)}
		got := wireStorageDecorators(raw, hooks.NewRunner(t.TempDir()), false)

		hf, ok := got.(*storage.HookFiringStore)
		if !ok {
			t.Fatalf("telemetry=%v: outer decorator %T, want *storage.HookFiringStore", telemetryOn, got)
		}
		next := hf.Unwrap()
		if telemetryOn {
			inst, ok := next.(*telemetry.InstrumentedStorage)
			if !ok {
				t.Fatalf("telemetry=on: beneath hooks is %T, want telemetry (no external-dependency policy)", next)
			}
			next = inst.Unwrap()
		}
		if _, ok := next.(serverEnforcedChainStub); !ok {
			t.Fatalf("telemetry=%v: chain reaches %T, want the store directly (hooks → telemetry → store)", telemetryOn, next)
		}
		if _, ok := wireExternalDependencyPolicy(raw).(*externaldeps.Store); ok {
			t.Fatalf("the routed-store composition wrapped a server-enforced store")
		}
	}
}

// TestServerEnforcedStoreForwardsReadyClaimCountAndClose drives the whole
// cmd/bd chain, and serve's store arm over it, against a remote client whose
// dependency-record reads fail the test: every ready, claim, count and close
// reaches the remote role with the caller's request untouched.
func TestServerEnforcedStoreForwardsReadyClaimCountAndClose(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	stub := newPolicyChainStub(t)
	stub.forbidEdges = true
	chain := wireStorageDecorators(serverEnforcedChainStub{stub}, hooks.NewRunner(t.TempDir()), false)
	roles, err := serveIssueRoles(chain, false)
	if err != nil {
		t.Fatalf("serveIssueRoles: %v", err)
	}
	ctx := t.Context()
	driveReadyClaimCountClose(t, ctx, chain, roles)

	if len(stub.ready) != 2 || len(stub.counted) != 2 || len(stub.claims) != 2 || len(stub.batches) != 2 || len(stub.closes) != 1 {
		t.Fatalf("remote roles saw ready=%d count=%d claim=%d batch=%d close=%d, want 2/2/2/2/1",
			len(stub.ready), len(stub.counted), len(stub.claims), len(stub.batches), len(stub.closes))
	}
	for _, req := range append(slices.Clone(stub.ready), stub.counted...) {
		if req.ExcludeIDs != nil {
			t.Errorf("a client-side policy narrowed a forwarded request: ExcludeIDs=%v", req.ExcludeIDs)
		}
	}
	if _, err := chain.GetReadyWork(ctx, types.WorkFilter{}); err != nil {
		t.Fatalf("store-level GetReadyWork: %v", err)
	}
}

// TestServeStoreArmAppliesPolicyOnce: serve peels only the hook layer, so its
// store-arm roles are the policy's role wrappers. Each request must read the
// external edges exactly once and hand the backend role the exclusion.
func TestServeStoreArmAppliesPolicyOnce(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	stub := newPolicyChainStub(t)
	stub.edges = map[string][]*types.Dependency{
		"bd-held": {{IssueID: "bd-held", DependsOnID: "external:nowhere:thing", Type: types.DepBlocks}},
	}
	chain := wireStorageDecorators(stub, hooks.NewRunner(t.TempDir()), false)
	roles, err := serveIssueRoles(chain, false)
	if err != nil {
		t.Fatalf("serveIssueRoles: %v", err)
	}
	ctx := t.Context()
	for name, call := range map[string]func() error{
		"Ready": func() error {
			_, err := roles.reader.Ready(ctx, issueops.ReadyRequest{Sort: "priority"})
			return err
		},
		"CountReady": func() error {
			_, err := roles.readyCounter.CountReady(ctx, issueops.ReadyRequest{Sort: "priority"})
			return err
		},
		"ClaimNext": func() error {
			_, err := roles.readyClaimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "w", Filter: issueops.ReadyRequest{Sort: "priority"}})
			return err
		},
		"CloseBatch": func() error {
			result, err := roles.batchCloser.CloseBatch(ctx, issueops.CloseBatchRequest{
				Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-held"}, {IssueID: "bd-free"}},
				ClaimNext: &issueops.ReadyRequest{Sort: "priority"},
			})
			if err == nil && !errors.Is(result.Outcomes[0].Err, storage.ErrCloseBlocked) {
				t.Errorf("serve batch close of externally blocked bd-held: outcome %+v, want ErrCloseBlocked", result.Outcomes[0])
			}
			return err
		},
	} {
		before := stub.edgeReads
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := stub.edgeReads - before; got != 1 {
			t.Errorf("serve %s read the external edges %d times, want exactly 1", name, got)
		}
	}
	if len(stub.batches) != 1 || len(stub.batches[0].Items) != 1 || stub.batches[0].Items[0].IssueID != "bd-free" {
		t.Fatalf("backend closer got %+v, want only the unblocked bd-free", stub.batches)
	}
	for _, req := range []issueops.ReadyRequest{stub.ready[0], stub.counted[0], stub.claims[0].Filter, *stub.batches[0].ClaimNext} {
		if !slices.Equal(req.ExcludeIDs, []string{"bd-held"}) {
			t.Errorf("backend role got ExcludeIDs=%v, want [bd-held]", req.ExcludeIDs)
		}
	}
}

func driveReadyClaimCountClose(t *testing.T, ctx context.Context, chain storage.DoltStorage, roles serveRoles) {
	t.Helper()
	readers := []issueops.Reader{roles.reader}
	counters := []issueops.ReadyCounter{roles.readyCounter}
	claimers := []issueops.ReadyClaimer{roles.readyClaimer}
	closers := []issueops.BatchCloser{roles.batchCloser}
	if r, err := chain.IssueReader(); err == nil {
		readers = append(readers, r)
	} else {
		t.Fatal(err)
	}
	if c, err := chain.ReadyCounter(); err == nil {
		counters = append(counters, c)
	} else {
		t.Fatal(err)
	}
	if c, err := chain.ReadyClaimer(); err == nil {
		claimers = append(claimers, c)
	} else {
		t.Fatal(err)
	}
	if c, err := chain.BatchCloser(); err == nil {
		closers = append(closers, c)
	} else {
		t.Fatal(err)
	}
	for _, r := range readers {
		if _, err := r.Ready(ctx, issueops.ReadyRequest{Sort: "priority"}); err != nil {
			t.Fatalf("Ready: %v", err)
		}
	}
	for _, c := range counters {
		if _, err := c.CountReady(ctx, issueops.ReadyRequest{Sort: "priority"}); err != nil {
			t.Fatalf("CountReady: %v", err)
		}
	}
	for _, c := range claimers {
		if _, err := c.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "w", Filter: issueops.ReadyRequest{Sort: "priority"}}); err != nil {
			t.Fatalf("ClaimNext: %v", err)
		}
	}
	for _, c := range closers {
		if _, err := c.CloseBatch(ctx, issueops.CloseBatchRequest{
			Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}},
			ClaimNext: &issueops.ReadyRequest{Sort: "priority"},
		}); err != nil {
			t.Fatalf("CloseBatch: %v", err)
		}
	}
	lifecycle, err := chain.IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Close(ctx, issueops.CloseRequest{IssueID: "bd-1", Actor: "w"}); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// GetIssue answers the re-close exemption the external close guard asks about
// an externally blocked item: every issue in this stub is open.
func (s *policyChainStub) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return &types.Issue{ID: id, Status: types.StatusOpen}, nil
}
