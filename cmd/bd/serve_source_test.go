package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// TestServeDatabaseSourceClassifiesTheWorkspace drives the one decision point
// bd serve has about where it reads and claims from.
//
// The registered arm is the whole point: the store the root command opened for
// a registered backend is opened through the same registry dispatch every
// ordinary bd command uses, so a workspace fully usable from the CLI must be
// servable too. The embedded arm is the permanent refusal, and it is here so
// that widening the classification cannot quietly narrow the refusal.
func TestServeDatabaseSourceClassifiesTheWorkspace(t *testing.T) {
	t.Run("a registered backend is served from its store", func(t *testing.T) {
		const name = "serve-registry"
		registerContractBackend(t, name)
		useStorageModeGlobals(t)
		beadsDir := writeContractBackendConfig(t, name)

		db, err := serveDatabaseSource(beadsDir)
		if err != nil {
			t.Fatalf("serveDatabaseSource() = %v, want the store source", err)
		}
		if db.source != serveSourceStore {
			t.Errorf("source = %v, want serveSourceStore", db.source)
		}
		if db.backend != name {
			t.Errorf("backend = %q, want %q", db.backend, name)
		}
	})

	// The registry outranks every dolt-mode signal, because that is the order
	// the store open already resolves them in: PersistentPreRunE dispatches on
	// backends.Lookup before anything looks at shared-server mode, so a
	// registered workspace with BEADS_DOLT_SHARED_SERVER=1 exported still opens
	// the registered store. Resolving it the other way here would build a Dolt
	// provider over a non-Dolt store and serve a different database than the
	// CLI in the same workspace reaches.
	t.Run("the registry outranks a shared-server environment", func(t *testing.T) {
		const name = "serve-registry-shared"
		registerContractBackend(t, name)
		useStorageModeGlobals(t)
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		beadsDir := writeContractBackendConfig(t, name)

		db, err := serveDatabaseSource(beadsDir)
		if err != nil {
			t.Fatalf("serveDatabaseSource() = %v, want the store source", err)
		}
		if db.source != serveSourceStore {
			t.Errorf("source = %v, want serveSourceStore: the store open gives the registry precedence here", db.source)
		}
	})

	t.Run("an unloadable metadata.json is named, not classified", func(t *testing.T) {
		useStorageModeGlobals(t)
		beadsDir := t.TempDir()
		writeBrokenBeadsConfig(t, beadsDir)

		db, err := serveDatabaseSource(beadsDir)
		if err == nil {
			t.Fatalf("serveDatabaseSource() = %v, nil; want the load failure rather than a classification", db)
		}
		if !strings.Contains(err.Error(), configfile.ConfigPath(beadsDir)) {
			t.Errorf("error does not name the file it could not read: %v", err)
		}
	})
}

// TestServeIssueRolesComeFromBeneathTheHookDecorator.
//
// bd serve documents that hooks do not fire, and a store's accessors hand out
// its decorators by design — `store.IssueClaimer()` on bd's own chain returns a
// claimer that runs the workspace's on_update script for every claim it lands.
// httpapi.Listen refuses exactly that value (storage.RoleFiresHooks), so this is
// the difference between a server that boots and one that does not.
func TestServeIssueRolesComeFromBeneathTheHookDecorator(t *testing.T) {
	// Two layers beneath the hooks, because peeling ONE is the requirement and
	// peeling all of them (storage.UnwrapStore) is the mistake that looks
	// identical on a single-layer chain. The middle store stands in for the
	// telemetry layer bd wires there, which is an Unwrapper too.
	inner := &serveRolesStore{reader: &serveStubReader{}, claimer: &serveStubClaimer{}}
	middle := &serveRolesStore{reader: &serveStubReader{}, claimer: &serveStubClaimer{}, inner: inner}
	chained := wireStorageDecorators(middle, hooks.NewRunner(t.TempDir()), false)

	if _, ok := chained.(*storage.HookFiringStore); !ok {
		t.Fatalf("wireStorageDecorators produced %T, not a hook-firing store; this test proves nothing", chained)
	}
	fromTheStore, err := chained.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer: %v", err)
	}
	if !storage.RoleFiresHooks(fromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing claimer; this test proves nothing")
	}

	roles, err := serveIssueRoles(chained)
	if err != nil {
		t.Fatalf("serveIssueRoles: %v", err)
	}
	reader, claimer := roles.reader, roles.claimer
	// The same predicate httpapi.Listen refuses on, so a regression here is a
	// server that refuses to boot rather than one that runs hooks silently.
	if storage.RoleFiresHooks(claimer) {
		t.Error("bd serve would run this workspace's hooks on every HTTP claim")
	}
	if claimer != issueops.Claimer(middle.claimer) {
		t.Errorf("claimer came from %p, want the layer directly beneath the hooks (%p)", claimer, middle.claimer)
	}
	if reader != issueops.Reader(middle.reader) {
		t.Errorf("reader came from %p, want the layer directly beneath the hooks (%p)", reader, middle.reader)
	}

	t.Run("a workspace with hooks disabled has no layer to peel", func(t *testing.T) {
		// BD_NO_HOOKS=1 wires no hook decorator at all, so the roles are the
		// store's own and the peel must be conditional.
		bare := wireStorageDecorators(middle, hooks.NewRunner(t.TempDir()), true)
		roles, err := serveIssueRoles(bare)
		if err != nil {
			t.Fatalf("serveIssueRoles: %v", err)
		}
		if roles.claimer != issueops.Claimer(middle.claimer) || roles.reader != issueops.Reader(middle.reader) {
			t.Error("serveIssueRoles peeled a layer that was not there")
		}
	})

	t.Run("no open store is an error, not a nil source", func(t *testing.T) {
		// httpapi.Listen refuses a partial role set, but a nil store reaching
		// it as an all-nil set would report "no database source" — true, and
		// useless. Name the real condition here.
		if _, err := serveIssueRoles(nil); err == nil {
			t.Fatal("serveIssueRoles(nil) = nil error; want a refusal naming the missing store")
		}
	})
}

// TestServeResolvedModeNamesTheRegisteredBackend. The startup label is
// cosmetic, but "embedded (external dolt)" for a registered backend is two
// wrong statements about the topology an operator is trying to identify.
func TestServeResolvedModeNamesTheRegisteredBackend(t *testing.T) {
	// A registered workspace has no dolt mode, and this is the field that
	// carries one — see the ContextInfo gap noted on serveResolvedMode.
	info := domain.ContextInfo{DoltMode: configfile.DoltModeEmbedded}

	got := serveResolvedMode(info, serveDatabase{source: serveSourceStore, backend: "acme"})
	if got != "acme (registered backend)" {
		t.Errorf("mode = %q, want %q", got, "acme (registered backend)")
	}
	if strings.Contains(got, configfile.DoltModeEmbedded) || strings.Contains(got, "dolt") {
		t.Errorf("mode reports a Dolt topology for a registered backend: %q", got)
	}
}

// writeBrokenBeadsConfig plants a metadata.json that configfile.Load cannot
// parse, which is the one input serveDatabaseSource must refuse rather than
// classify: falling back to a default here would serve the embedded default
// for a workspace whose real backend is unknown.
func writeBrokenBeadsConfig(t *testing.T, beadsDir string) {
	t.Helper()
	if err := os.WriteFile(configfile.ConfigPath(beadsDir), []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write broken metadata.json: %v", err)
	}
}

// serveRolesStore is the smallest DoltStorage the role extraction can be
// pointed at: it publishes its own roles and unwraps to an inner store with
// different ones, so a peel of the wrong depth lands on identifiably wrong
// values. The embedded DoltStorage is nil, so an accessor serveIssueRoles
// starts calling without a stub here panics rather than passing quietly.
//
// Only the reader and the claimer carry identifiable values. That is enough to
// pin the peel DEPTH, which is the whole property under test: serveIssueRoles
// peels once and then calls every accessor on that one store value, so no role
// can come from a different layer than these two did. The rest return nil
// because the extraction does not inspect them.
type serveRolesStore struct {
	storage.DoltStorage
	reader  *serveStubReader
	claimer *serveStubClaimer
	inner   storage.DoltStorage
}

func (s *serveRolesStore) IssueReader() (issueops.Reader, error)   { return s.reader, nil }
func (s *serveRolesStore) IssueClaimer() (issueops.Claimer, error) { return s.claimer, nil }
func (s *serveRolesStore) Unwrap() storage.DoltStorage             { return s.inner }

func (*serveRolesStore) WorkspaceConfig() (issueops.WorkspaceConfig, error)     { return nil, nil }
func (*serveRolesStore) StatsReporter() (issueops.StatsReporter, error)         { return nil, nil }
func (*serveRolesStore) CycleDetector() (issueops.CycleDetector, error)         { return nil, nil }
func (*serveRolesStore) EdgeReader() (issueops.EdgeReader, error)               { return nil, nil }
func (*serveRolesStore) BlockingAnnotator() (issueops.BlockingAnnotator, error) { return nil, nil }
func (*serveRolesStore) TreeWalker() (issueops.TreeWalker, error)               { return nil, nil }
func (*serveRolesStore) ReadyCounter() (issueops.ReadyCounter, error)           { return nil, nil }
func (*serveRolesStore) Querier() (issueops.Querier, error)                     { return nil, nil }
func (*serveRolesStore) Sweeper() (issueops.Sweeper, error)                     { return nil, nil }
func (*serveRolesStore) Deleter() (issueops.Deleter, error)                     { return nil, nil }
func (*serveRolesStore) BatchCreator() (issueops.BatchCreator, error)           { return nil, nil }
func (*serveRolesStore) Memories() (memoryops.Memories, error)                  { return nil, nil }

type serveStubReader struct{}

func (*serveStubReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, errors.ErrUnsupported
}

func (*serveStubReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, errors.ErrUnsupported
}

func (*serveStubReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, errors.ErrUnsupported
}

type serveStubClaimer struct{}

func (*serveStubClaimer) Claim(context.Context, issueops.ClaimRequest) (issueops.ClaimResult, error) {
	return issueops.ClaimResult{}, errors.ErrUnsupported
}
