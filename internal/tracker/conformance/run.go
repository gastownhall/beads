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
	Issues   map[string]types.Issue
	Config   map[string]string
	Metadata map[string]string
	LastSync string
}

// Fixture contains deterministic HTTP and local-store dependencies for one
// adapter test. StoreFactory counts opens so API-only operations can prove
// that they do not initialize local persistence.
type Fixture struct {
	HTTP         *HTTPDouble
	StoreFactory *Factory
}

// Factory is a fake UOW factory. Each Open returns the same map-backed store,
// while OpenCount records initialization attempts.
type Factory struct {
	Store *Store
	opens int
}

// NewFactory returns a fixture factory with an empty store.
func NewFactory() *Factory { return &Factory{Store: NewStore()} }

// Open returns the tracker store and records one UOW open.
func (f *Factory) Open() tracker.Store { f.opens++; return f.Store }

// OpenCount reports the number of UOW opens.
func (f *Factory) OpenCount() int { return f.opens }

// Setup binds an existing tracker.Engine to fixture assertions. Refusal and
// APIOnly are adapter-specific front doors; keeping them callbacks avoids
// inventing a backend-neutral command API.
type Setup struct {
	Engine   *tracker.Engine
	Store    *Store
	Expected Expected
	Refusal  func(context.Context) (*tracker.SyncResult, error)
	APIOnly  func(context.Context, func() tracker.Store) error
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
	newSetup := func(t *testing.T) Setup {
		fixture := &Fixture{HTTP: NewHTTPDouble(), StoreFactory: NewFactory()}
		setup := build(t, fixture)
		if setup.Engine == nil || setup.Store == nil || setup.Refusal == nil || setup.APIOnly == nil || setup.Expected.ExternalRef == "" || setup.Expected.ConfigKey == "" || setup.Expected.MetadataKey == "" {
			t.Fatal("setup must provide Engine, Store, Expected refs/config/metadata, Refusal, and APIOnly")
		}
		return setup
	}

	t.Run("pull_persists_normalized_fields_and_last_sync", func(t *testing.T) {
		s := newSetup(t)
		result, err := s.Engine.Sync(ctx, tracker.SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		if result == nil || !result.Success {
			t.Fatalf("pull result = %+v", result)
		}
		snapshot := s.Store.Snapshot()
		if len(snapshot.Issues) == 0 {
			t.Fatal("pull created no local issue")
		}
		for _, issue := range snapshot.Issues {
			if issue.ExternalRef == nil || *issue.ExternalRef != s.Expected.ExternalRef || len(issue.Labels) == 0 {
				t.Fatalf("pull lost external ref/labels: %+v", issue)
			}
		}
		if result.LastSync == "" || snapshot.LastSync != result.LastSync {
			t.Fatalf("pull last_sync result=%q snapshot=%q", result.LastSync, snapshot.LastSync)
		}
	})

	t.Run("push_persists_config_and_metadata", func(t *testing.T) {
		s := newSetup(t)
		result, err := s.Engine.Sync(ctx, tracker.SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("push: %v", err)
		}
		if result == nil || !result.Success {
			t.Fatalf("push result = %+v", result)
		}
		snapshot := s.Store.Snapshot()
		if snapshot.Config[s.Expected.ConfigKey] == "" || snapshot.Metadata[s.Expected.MetadataKey] == "" {
			t.Fatalf("push did not persist config/metadata: %+v", snapshot)
		}
	})

	t.Run("dry_run_does_not_mutate", func(t *testing.T) {
		s := newSetup(t)
		before, writes := s.Store.Snapshot(), s.Store.MutationCount()
		for name, opts := range map[string]tracker.SyncOptions{"pull": {Pull: true, DryRun: true}, "push": {Push: true, DryRun: true}} {
			result, err := s.Engine.Sync(ctx, opts)
			if err != nil {
				t.Fatalf("%s dry-run: %v", name, err)
			}
			if result == nil || !result.Success {
				t.Fatalf("%s dry-run result = %+v", name, result)
			}
		}
		if !reflect.DeepEqual(before, s.Store.Snapshot()) || writes != s.Store.MutationCount() {
			t.Fatal("dry-run changed local state")
		}
	})

	t.Run("refusal_is_explicit_and_does_not_mutate", func(t *testing.T) {
		s := newSetup(t)
		before, writes := s.Store.Snapshot(), s.Store.MutationCount()
		_, err := s.Refusal(ctx)
		if err == nil {
			t.Fatal("refused operation succeeded")
		}
		var refusal *storage.ErrUnsupported
		if !errors.As(err, &refusal) {
			t.Fatalf("refusal is not typed: %v", err)
		}
		if !reflect.DeepEqual(before, s.Store.Snapshot()) || writes != s.Store.MutationCount() {
			t.Fatal("refusal changed local state")
		}
	})

	t.Run("api_only_does_not_open_uow", func(t *testing.T) {
		fixture := &Fixture{HTTP: NewHTTPDouble(), StoreFactory: NewFactory()}
		s := build(t, fixture)
		before := fixture.StoreFactory.OpenCount()
		if err := s.APIOnly(ctx, fixture.StoreFactory.Open); err != nil {
			t.Fatalf("api-only: %v", err)
		}
		if got := fixture.StoreFactory.OpenCount(); got != before {
			t.Fatalf("api-only opened UOW: %d -> %d", before, got)
		}
	})
}
