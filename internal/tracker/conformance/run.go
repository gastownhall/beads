package conformance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// Snapshot is the normalized local state inspected by the shared suite.
type Snapshot struct {
	Issues       map[string]types.Issue
	Dependencies map[string][]string
	Config       map[string]string
	Metadata     map[string]string
	LastSync     string
}

// Fixture contains deterministic HTTP and local-store dependencies for one
// adapter test. StoreFactory is the seam an adapter's front door uses to
// obtain a store, so a future API-only front door can be observed deciding
// not to open one.
type Fixture struct {
	HTTP         *HTTPDouble
	StoreFactory StoreFactory
}

// StoreFactory opens a tracker store for a front-door operation.
type StoreFactory interface {
	Open() tracker.Store
}

// Factory supplies the same map-backed store to each front-door operation.
type Factory struct {
	Store *Store
}

// NewFactory returns a fixture factory with an empty store.
func NewFactory() *Factory { return &Factory{Store: NewStore()} }

// Open returns the tracker store.
func (f *Factory) Open() tracker.Store { return f.Store }

// Setup binds an existing tracker.Engine to fixture assertions. Refusal and
// APIOnly are adapter-specific front doors; keeping them callbacks avoids
// inventing a backend-neutral command API.
type Setup struct {
	Engine   *tracker.Engine
	Store    tracker.Store
	Snapshot func(context.Context) (Snapshot, error)
	// MutationCount detects state-identical writes when the backend exposes
	// a counter. Real backends may leave it nil and compare snapshots only.
	MutationCount func() int
	// SeedExternalRefPlanes prepares the durable/wisp collision that proves
	// durable issues win external-ref lookup. It returns the durable issue ID.
	SeedExternalRefPlanes func(context.Context) (string, string, error)
	Expected              Expected
	Refusal               func(context.Context) (*tracker.SyncResult, error)
	APIOnly               func(context.Context, func() tracker.Store) error
}

// Expected identifies adapter-specific values that the generic suite must
// observe without baking a provider's naming conventions into the harness.
type Expected struct {
	ExternalRef string
	ConfigKey   string
	MetadataKey string
}

// Run executes the common tracker Engine/UOW contract.
func Run(t *testing.T, build func(*testing.T, *Fixture) Setup) {
	t.Helper()
	ctx := context.Background()
	newSetup := func(t *testing.T) (Setup, *Fixture) {
		fixture := &Fixture{HTTP: NewHTTPDouble(), StoreFactory: NewFactory()}
		setup := build(t, fixture)
		if setup.Engine == nil || setup.Store == nil || setup.Snapshot == nil || setup.SeedExternalRefPlanes == nil || setup.Refusal == nil || setup.APIOnly == nil || setup.Expected.ExternalRef == "" || setup.Expected.ConfigKey == "" || setup.Expected.MetadataKey == "" {
			t.Fatal("setup must provide Engine, Store, Snapshot, external-ref seed, Expected refs/config/metadata, Refusal, and APIOnly")
		}
		return setup, fixture
	}

	t.Run("pull_persists_normalized_fields_and_last_sync", func(t *testing.T) {
		s, _ := newSetup(t)
		result, err := s.Engine.Sync(ctx, tracker.SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		if result == nil || !result.Success {
			t.Fatalf("pull result = %+v", result)
		}
		snapshot, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot after pull: %v", err)
		}
		if len(snapshot.Issues) == 0 {
			t.Fatal("pull created no local issue")
		}
		var pulled *types.Issue
		for _, issue := range snapshot.Issues {
			if issue.ExternalRef != nil && *issue.ExternalRef == s.Expected.ExternalRef {
				copy := issue
				pulled = &copy
				break
			}
		}
		// Labels are certified on the UPDATE path only. Create-path label
		// parity is a known blind spot: tracker's UOW store creates through
		// domain.CreateIssueParams{Issue: issue} with Labels unset, and the
		// domain create writes labels only from params.Labels, so a proxied
		// create drops every label a direct create persists (bd-p0n1).
		// Adopters must therefore not seed labels through Store.CreateIssue —
		// doing so puts the two legs on divergent state and makes this suite
		// certify a parity it never checked.
		if pulled == nil || len(pulled.Labels) != 1 || pulled.Labels[0] != "bug" || pulled.Status != types.StatusClosed {
			t.Fatalf("pull lost normalized fields: %+v", pulled)
		}
		// The pulled dependency must be visible in the same Snapshot every
		// other assertion reads, so the dry-run and refusal comparisons cover
		// the dependency plane too.
		dependent := ""
		for id, targets := range snapshot.Dependencies {
			if id == pulled.ID {
				continue
			}
			for _, target := range targets {
				if target == pulled.ID {
					dependent = id
				}
			}
		}
		if dependent == "" {
			t.Fatalf("pull did not persist a dependency onto %s: %+v", pulled.ID, snapshot.Dependencies)
		}
		if result.LastSync == "" || snapshot.LastSync != result.LastSync {
			t.Fatalf("pull last_sync result=%q snapshot=%q", result.LastSync, snapshot.LastSync)
		}
	})

	t.Run("push_persists_config_and_metadata", func(t *testing.T) {
		s, _ := newSetup(t)
		result, err := s.Engine.Sync(ctx, tracker.SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("push: %v", err)
		}
		if result == nil || !result.Success {
			t.Fatalf("push result = %+v", result)
		}
		snapshot, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot after push: %v", err)
		}
		if snapshot.Config[s.Expected.ConfigKey] == "" || snapshot.Metadata[s.Expected.MetadataKey] == "" {
			t.Fatalf("push did not persist config/metadata: %+v", snapshot)
		}
	})

	t.Run("dry_run_does_not_mutate", func(t *testing.T) {
		s, _ := newSetup(t)
		assertUnchanged := noMutationCheck(t, ctx, s)
		for name, opts := range map[string]tracker.SyncOptions{"pull": {Pull: true, DryRun: true}, "push": {Push: true, DryRun: true}} {
			result, err := s.Engine.Sync(ctx, opts)
			if err != nil {
				t.Fatalf("%s dry-run: %v", name, err)
			}
			if result == nil || !result.Success {
				t.Fatalf("%s dry-run result = %+v", name, result)
			}
		}
		assertUnchanged()
	})

	t.Run("refusal_is_explicit_and_does_not_mutate", func(t *testing.T) {
		s, _ := newSetup(t)
		assertUnchanged := noMutationCheck(t, ctx, s)
		_, err := s.Refusal(ctx)
		if err == nil {
			t.Fatal("refused operation succeeded")
		}
		var refusal *storage.ErrUnsupported
		if !errors.As(err, &refusal) {
			t.Fatalf("refusal is not typed: %v", err)
		}
		assertUnchanged()
	})

	t.Run("external_ref_resolution_prefers_issue_plane", func(t *testing.T) {
		s, _ := newSetup(t)
		want, onlyID, err := s.SeedExternalRefPlanes(ctx)
		if err != nil {
			t.Fatalf("seed external-ref planes: %v", err)
		}

		snapshot, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot seeded planes: %v", err)
		}
		if issue, ok := snapshot.Issues[onlyID]; !ok || !issue.Ephemeral {
			t.Fatalf("snapshot omitted seeded wisp %q: %+v", onlyID, snapshot.Issues)
		}

		got, err := s.Store.GetIssueByExternalRef(ctx, s.Expected.ExternalRef)
		if err != nil {
			t.Fatalf("resolve %q: %v", s.Expected.ExternalRef, err)
		}
		// Resolving to the wisp would make the pull dedup update the ephemeral
		// row instead of the durable bead — a silent write to the wrong issue.
		if got == nil || got.ID != want {
			t.Fatalf("external_ref %q resolved to %v, want issues-plane %q: the issues plane must win over the wisp plane", s.Expected.ExternalRef, got, want)
		}
		got, err = s.Store.GetIssueByExternalRef(ctx, s.Expected.ExternalRef+"-wisp-only")
		if err != nil || got == nil || got.ID != onlyID {
			t.Fatalf("wisp-only external_ref resolved to (%v, %v), want %q", got, err, onlyID)
		}
	})

	// The API-only front door is handed a store opener it is free to ignore.
	// This is a harness-contract check, not evidence that a real command skips
	// persistence: today's adopters supply synthetic callbacks that never call
	// open, so asserting the open count could not fail. The adapter child work
	// that drives a real front door through open is what proves the boundary.
	t.Run("api_only_completes_without_store", func(t *testing.T) {
		s, fixture := newSetup(t)
		if err := s.APIOnly(ctx, fixture.StoreFactory.Open); err != nil {
			t.Fatalf("api-only: %v", err)
		}
	})
}

// noMutationCheck captures state and, where available, the write counter.
// Checking both catches idempotent writes that a state comparison cannot see.
func noMutationCheck(t *testing.T, ctx context.Context, s Setup) func() {
	t.Helper()
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot before operation: %v", err)
	}
	writes := 0
	if s.MutationCount != nil {
		writes = s.MutationCount()
	}
	return func() {
		t.Helper()
		after, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot after operation: %v", err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("operation changed local state")
		}
		if s.MutationCount != nil && writes != s.MutationCount() {
			t.Fatal("operation issued local writes")
		}
	}
}
