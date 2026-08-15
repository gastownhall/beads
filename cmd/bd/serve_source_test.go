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
	stubs := func(inner storage.DoltStorage) *serveRolesDoltStore {
		return &serveRolesDoltStore{serveRolesStore: &serveRolesStore{
			reader:       &serveStubReader{},
			claimer:      &serveStubClaimer{},
			batchCloser:  &serveStubBatchCloser{},
			readyClaimer: &serveStubReadyClaimer{},
			releaser:     &serveStubReleaser{},
			lifecycle:    &serveStubLifecycle{},
			dependencies: &serveStubDependencyEditor{},
			batchApplier: &serveStubBatchApplier{},
			metadataCAS:  &serveStubMetadataCAS{},
			counter:      &serveStubCounter{},
			edgeCounter:  &serveStubGraphCounter{},
			relations:    &serveStubRelations{},
			commenter:    &serveStubCommenter{},
			batchCreator: &serveStubBatchCreator{},
			inner:        inner,
		}}
	}
	inner := stubs(nil)
	middle := stubs(inner)
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
	// Same precondition for the lifecycle: RoleFiresHooks has to KNOW about the
	// decorator's lifecycle wrapper, or the peel assertion below would pass on a
	// predicate that answers false for everything.
	lifecycleFromTheStore, err := chained.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	if !storage.RoleFiresHooks(lifecycleFromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing lifecycle; this test proves nothing")
	}
	// And for the dependency editor, the third role the decorator wraps. Without
	// this its case in the RoleFiresHooks switch is held only by a comment, and
	// the peel assertion below would pass on a predicate that answers false for
	// everything.
	editorFromTheStore, err := chained.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor: %v", err)
	}
	if !storage.RoleFiresHooks(editorFromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing dependency editor; this test proves nothing")
	}
	// And for the batch applier, the FOURTH and widest role the decorator wraps:
	// one call fires on_create, on_update and the close hooks, once per landed
	// item plus once per distinct edge source. Without this precondition its case
	// in the RoleFiresHooks switch is held only by a comment.
	applierFromTheStore, err := chained.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier: %v", err)
	}
	if !storage.RoleFiresHooks(applierFromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing batch applier; this test proves nothing")
	}
	// And for the compare-and-set, the FIFTH. It arrived with no precondition of
	// its own, and its case in the RoleFiresHooks switch spent that time held
	// only by a comment — which is how a rebase that left the case with an EMPTY
	// BODY went unnoticed by the compiler, by vet, by lint and by CI. A Go type
	// switch does not fall through, so `case *hookMetadataCAS:` with nothing
	// under it answers FALSE, and the Listen refusal this whole test exists to
	// protect was silently disarmed. Both cases are pinned now.
	casFromTheStore, err := chained.MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS: %v", err)
	}
	if !storage.RoleFiresHooks(casFromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing compare-and-set; this test proves nothing")
	}

	// And for the releaser, the SIXTH. Its case in the RoleFiresHooks switch
	// would otherwise be held only by a comment, which is exactly how the
	// compare-and-set's case above spent time silently empty.
	releaserFromTheStore, err := chained.Releaser()
	if err != nil {
		t.Fatalf("Releaser: %v", err)
	}
	if !storage.RoleFiresHooks(releaserFromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing releaser; this test proves nothing")
	}

	// And for the commenter, the SEVENTH — and the one that was OUTSIDE the
	// RoleFiresHooks switch entirely until the add-comment operation went on the
	// wire. hook_commenter.go has fired the update hook for every comment it
	// lands since it was written; nothing took the role off a store, so nothing
	// noticed. An unpeeled one runs the workspace's script once per comment.
	commenterFromTheStore, err := chained.Commenter()
	if err != nil {
		t.Fatalf("Commenter: %v", err)
	}
	if !storage.RoleFiresHooks(commenterFromTheStore) {
		t.Fatal("the store's own accessor no longer returns a hook-firing commenter; this test proves nothing")
	}

	// THE THREE THAT WERE BLIND BESIDE THE COMMENTER. Each is served over a
	// published operation and each was outside the RoleFiresHooks switch, so
	// httpapi.Listen admitted a hook-firing value for all three; the class-level
	// scan that finds the next one is
	// TestRoleFiresHooksKnowsEveryHookFiringRole in internal/storage, and these
	// are the store-side halves of the same property.
	for _, role := range []struct {
		name string
		from func() (any, error)
	}{
		{"ready claimer", func() (any, error) { return chained.ReadyClaimer() }},
		{"batch closer", func() (any, error) { return chained.BatchCloser() }},
		{"batch creator", func() (any, error) { return chained.BatchCreator() }},
	} {
		fromTheStore, err := role.from()
		if err != nil {
			t.Fatalf("the store's %s accessor: %v", role.name, err)
		}
		if !storage.RoleFiresHooks(fromTheStore) {
			t.Fatalf("the store's own accessor no longer returns a hook-firing %s; this test proves nothing", role.name)
		}
	}

	roles, err := serveIssueRoles(chained, false)
	if err != nil {
		t.Fatalf("serveIssueRoles: %v", err)
	}
	if storage.RoleFiresHooks(roles.commenter) {
		t.Error("bd serve would run this workspace's hooks on every HTTP comment")
	}
	// And the peel really landed on the layer beneath the hooks for all three,
	// which is what "RoleFiresHooks is false" alone does not say: a peel two
	// layers deep would also answer false and would drop the telemetry span.
	if storage.RoleFiresHooks(roles.readyClaimer) || roles.readyClaimer != issueops.ReadyClaimer(middle.readyClaimer) {
		t.Errorf("ready claimer came from %p, want the layer directly beneath the hooks (%p)", roles.readyClaimer, middle.readyClaimer)
	}
	if storage.RoleFiresHooks(roles.batchCloser) || roles.batchCloser != issueops.BatchCloser(middle.batchCloser) {
		t.Errorf("batch closer came from %p, want the layer directly beneath the hooks (%p)", roles.batchCloser, middle.batchCloser)
	}
	if storage.RoleFiresHooks(roles.batchCreator) || roles.batchCreator != issueops.BatchCreator(middle.batchCreator) {
		t.Errorf("batch creator came from %p, want the layer directly beneath the hooks (%p)", roles.batchCreator, middle.batchCreator)
	}
	if roles.commenter != issueops.Commenter(middle.commenter) {
		t.Errorf("commenter came from %p, want the layer directly beneath the hooks (%p)", roles.commenter, middle.commenter)
	}
	if storage.RoleFiresHooks(roles.releaser) {
		t.Error("bd serve would run this workspace's hooks on every HTTP release")
	}
	if roles.releaser != issueops.Releaser(middle.releaser) {
		t.Errorf("releaser came from %p, want the layer directly beneath the hooks (%p)", roles.releaser, middle.releaser)
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

	// The lifecycle is the SECOND role the hook decorator wraps, and it wraps
	// four verbs rather than one — so an unpeeled lifecycle would run this
	// workspace's on_create, on_update and close hooks for every HTTP mutation.
	if storage.RoleFiresHooks(roles.lifecycle) {
		t.Error("bd serve would run this workspace's hooks on every HTTP lifecycle mutation")
	}
	if roles.lifecycle != issueops.Lifecycle(middle.lifecycle) {
		t.Errorf("lifecycle came from %p, want the layer directly beneath the hooks (%p)", roles.lifecycle, middle.lifecycle)
	}

	// The dependency editor is the THIRD, and it fires the update hook once per
	// DISTINCT SOURCE ISSUE — so an unpeeled editor would run this workspace's
	// script several times for one HTTP batch.
	if storage.RoleFiresHooks(roles.dependencyEditor) {
		t.Error("bd serve would run this workspace's hooks on every HTTP dependency edit")
	}
	if roles.dependencyEditor != issueops.DependencyEditor(middle.dependencies) {
		t.Errorf("dependency editor came from %p, want the layer directly beneath the hooks (%p)",
			roles.dependencyEditor, middle.dependencies)
	}

	// The batch applier is the FOURTH, and the one where the peel is worth the
	// most: its wrapper fires four hook vocabularies from one call — per landed
	// item, plus once per distinct edge source — so an unpeeled applier serving
	// one hundred-item plan is up to a hundred of this workspace's own
	// subprocesses spawned inside a single HTTP request.
	if storage.RoleFiresHooks(roles.batchApplier) {
		t.Error("bd serve would run this workspace's hooks once per item of every HTTP plan")
	}
	if roles.batchApplier != issueops.BatchApplier(middle.batchApplier) {
		t.Errorf("batch applier came from %p, want the layer directly beneath the hooks (%p)",
			roles.batchApplier, middle.batchApplier)
	}

	// The compare-and-set is the FIFTH, and a coordination loop is its designed
	// caller — so an unpeeled one runs this workspace's on_update script once per
	// contended retry, at whatever rate the clients poll.
	if storage.RoleFiresHooks(roles.metadataCAS) {
		t.Error("bd serve would run this workspace's hooks on every HTTP compare-and-set")
	}
	if roles.metadataCAS != issueops.MetadataCAS(middle.metadataCAS) {
		t.Errorf("compare-and-set came from %p, want the layer directly beneath the hooks (%p)",
			roles.metadataCAS, middle.metadataCAS)
	}

	t.Run("a workspace with hooks disabled has no layer to peel", func(t *testing.T) {
		// BD_NO_HOOKS=1 wires no hook decorator at all, so the roles are the
		// store's own and the peel must be conditional.
		bare := wireStorageDecorators(middle, hooks.NewRunner(t.TempDir()), true)
		roles, err := serveIssueRoles(bare, false)
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
		if _, err := serveIssueRoles(nil, false); err == nil {
			t.Fatal("serveIssueRoles(nil) = nil error; want a refusal naming the missing store")
		}
	})

	// The events journal is the one CONDITIONAL role, and both polarities
	// matter. A backend that cannot read the journal is an ordinary backend —
	// the journal is off by default, and a registered third-party backend has
	// no obligation to implement a Dolt-shaped seam — so requiring it
	// unconditionally would refuse to start servers that have no use for it.
	// Requiring it for a workspace that DID enable one is the other half: a
	// server that bound anyway would answer that route with a nil dereference.
	t.Run("the journal role is required only when the workspace has one", func(t *testing.T) {
		// serveRolesStore implements no journal seam, which is the case under
		// test: assert that rather than assume it.
		if _, ok := storage.UnwrapStore(chained).(storage.EventsJournalCursor); ok {
			t.Fatal("the fixture store reads the journal; this test proves nothing")
		}

		roles, err := serveIssueRoles(chained, false)
		if err != nil {
			t.Fatalf("serveIssueRoles with the journal off: %v", err)
		}
		if roles.eventsJournal != nil {
			t.Error("a store that cannot read the journal produced a reader")
		}

		if _, err := serveIssueRoles(chained, true); err == nil {
			t.Fatal("serveIssueRoles accepted a journal-enabled workspace on a backend that cannot read the journal")
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

// serveRolesStore is the smallest source the role extraction can be pointed at:
// it publishes its own roles and unwraps to an inner store with different ones,
// so a peel of the wrong depth lands on identifiably wrong values.
//
// IT EMBEDS NOTHING, and the assertion below is why. A stub that embedded a nil
// storage.DoltStorage would answer every accessor it forgot with a promoted
// method on a nil interface — a segfault inside serveIssueRoles rather than a
// compile error, which is how GraphCounter reached this file. Declaring the
// whole of serveRoleSource means the next role added there stops the build here,
// naming the method.
//
// Only the reader and the claimer carry identifiable values. That is enough to
// pin the peel DEPTH, which is the whole property under test: serveIssueRoles
// peels once and then calls every accessor on that one store value, so no role
// can come from a different layer than these two did. The rest return nil
// because the extraction does not inspect them.
type serveRolesStore struct {
	reader       *serveStubReader
	claimer      *serveStubClaimer
	batchCloser  *serveStubBatchCloser
	readyClaimer *serveStubReadyClaimer
	releaser     *serveStubReleaser
	lifecycle    *serveStubLifecycle
	dependencies *serveStubDependencyEditor
	batchApplier *serveStubBatchApplier
	metadataCAS  *serveStubMetadataCAS
	counter      *serveStubCounter
	edgeCounter  *serveStubGraphCounter
	relations    *serveStubRelations
	commenter    *serveStubCommenter
	batchCreator *serveStubBatchCreator
	inner        storage.DoltStorage
}

var _ serveRoleSource = (*serveRolesStore)(nil)

// serveRolesDoltStore is serveRolesStore where a WHOLE store is required:
// wireStorageDecorators builds the chain this test peels, and it takes a
// storage.DoltStorage.
//
// The roles above sit one embed shallower than the nil store beneath them, so
// they shadow it: every accessor serveIssueRoles reaches is declared, and only
// the rest of the interface — none of which this test calls — falls through to
// the nil embed. A new role therefore fails the assertion above rather than
// being quietly promoted here.
type serveRolesDoltStore struct {
	*serveRolesStore
	serveStubRest
}

// serveStubRest carries the remainder of storage.DoltStorage for a stub that
// declares its own roles, one embed deeper than those roles. It is the shared
// half of the shadowing described above; serve_store_identity_test.go's stub
// uses it for the same reason.
type serveStubRest struct{ storage.DoltStorage }

func (s *serveRolesStore) IssueReader() (issueops.Reader, error)   { return s.reader, nil }
func (s *serveRolesStore) IssueClaimer() (issueops.Claimer, error) { return s.claimer, nil }
func (s *serveRolesStore) Unwrap() storage.DoltStorage             { return s.inner }

func (s *serveRolesStore) BatchCloser() (issueops.BatchCloser, error)   { return s.batchCloser, nil }
func (s *serveRolesStore) ReadyClaimer() (issueops.ReadyClaimer, error) { return s.readyClaimer, nil }

// Releaser carries an identifiable value for the same reason, and it is the
// SIXTH role the decorator wraps: a peel of the wrong depth would hand bd serve
// a releaser that runs the workspace's hooks once per claim it frees — which a
// reaper draining abandoned work does in a tight loop.
func (s *serveRolesStore) Releaser() (issueops.Releaser, error) { return s.releaser, nil }

// IssueLifecycle carries an identifiable value for the same reason the reader
// and the claimer do: it is one of the roles the hook decorator wraps, so a peel
// of the wrong depth would hand bd serve a lifecycle that runs the workspace's
// hooks on every close.
func (s *serveRolesStore) IssueLifecycle() (issueops.Lifecycle, error) { return s.lifecycle, nil }

// DependencyEditor carries an identifiable value for the same reason, and it is
// the last of the three the decorator wraps: a peel of the wrong depth would
// hand bd serve an editor that runs the workspace's hooks on every edge it
// writes.
func (s *serveRolesStore) DependencyEditor() (issueops.DependencyEditor, error) {
	return s.dependencies, nil
}

// BatchApplier carries an identifiable value for the same reason, and it is the
// FOURTH the decorator wraps: a peel of the wrong depth would hand bd serve an
// applier that runs the workspace's hooks once per item of every plan it
// applies.
func (s *serveRolesStore) BatchApplier() (issueops.BatchApplier, error) {
	return s.batchApplier, nil
}

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
func (*serveRolesStore) Memories() (memoryops.Memories, error)                  { return nil, nil }

// MetadataCAS carries an identifiable value for the same reason, and it is the
// FIFTH the decorator wraps: a peel of the wrong depth would hand bd serve a
// compare-and-set that runs the workspace's on_update script once per contended
// retry round of every coordination loop pointed at this server.
func (s *serveRolesStore) MetadataCAS() (issueops.MetadataCAS, error) { return s.metadataCAS, nil }

// Counter is declared for a different reason than every accessor above it, and
// the reason is the one engdocs/ADDING_AN_ISSUEOPS_ROLE.md calls "the step with
// no number": a role this stub does NOT declare arrives PROMOTED from the
// embedded storage.DoltStorage, which is nil here, so the first caller to reach
// it is a nil dereference in somebody else's test rather than a compile error.
//
// The count role is not one the hook decorator wraps — it is a READ, so
// hook_counter.go recurses and hands back the inner surface unwrapped — which is
// exactly why it needed this: the recursion lands on this type, and this type
// had no Counter to land on. It went unnoticed until `bd serve` began binding
// the role, and then it surfaced on one CI runner as a panic in a test about
// hook peeling.
func (s *serveRolesStore) Counter() (issueops.Counter, error) { return s.counter, nil }

// GraphCounter is declared for Counter's reason, and it is the SECOND role to
// need this file edited for a promotion rather than for a hook: the edge count
// is a read, so hook_graph_counter.go recurses and the recursion lands here.
// The count role's own comment above says this went unnoticed until `bd serve`
// began binding it; this one was found the same way, by this test panicking the
// moment the binding landed.
func (s *serveRolesStore) GraphCounter() (issueops.GraphCounter, error) { return s.edgeCounter, nil }

// IssueRelations is the FIRST role added to serveIssueRoles since this type
// stopped embedding a nil store, and it is worth recording what that changed —
// because the two comments above it describe the old regime and are now history
// rather than instruction.
//
// Counter and GraphCounter had to be FOUND. Neither is wrapped by the hook
// decorator (both are reads, so their decorators recurse), so neither was
// noticed until `bd serve` began binding it — GraphCounter on a full-package CI
// shard, because no -run pattern anyone reaches for names this test.
//
// This one could not be missed. serveRoleSource names the accessor, the
// assertion above requires it, and omitting it is `*serveRolesStore does not
// implement serveRoleSource (missing method IssueRelations)` at build time, in
// this file, naming the method. Which is the whole return on #5539 landing
// first, measured on the next role rather than asserted about it.
func (s *serveRolesStore) IssueRelations() (issueops.Relations, error) { return s.relations, nil }

// BatchCreator carries an identifiable value rather than nil, and it moved off
// nil for the reason the three preconditions below give: it is one of the roles
// the hook decorator wraps, so a peel of the wrong depth hands bd serve a
// creator that runs the workspace's on_create once per item of every batch.
func (s *serveRolesStore) BatchCreator() (issueops.BatchCreator, error) {
	return s.batchCreator, nil
}

// serveStubBatchCreator is the batch-create role's stand-in, ErrUnsupported like
// every stub here.
type serveStubBatchCreator struct{}

func (*serveStubBatchCreator) CreateBatch(context.Context, issueops.CreateBatchRequest) (issueops.CreateBatchResult, error) {
	return issueops.CreateBatchResult{}, errors.ErrUnsupported
}

// Commenter is the SEVENTH role the hook decorator wraps and the second added
// under the regime IssueRelations describes: omitting it is a build error in
// this file naming the method, not a nil dereference somewhere else.
//
// It carries an identifiable value rather than nil, unlike the reads above,
// because it IS one of the wrapped roles: hook_commenter.go fires the update
// hook for every comment it lands, so a peel of the wrong depth would hand
// bd serve a commenter that runs the workspace's script once per comment — and
// a comment is exactly the write an agent makes in a loop.
func (s *serveRolesStore) Commenter() (issueops.Commenter, error) { return s.commenter, nil }

// serveStubCommenter is the add-comment role's stand-in, ErrUnsupported like
// every stub here.
type serveStubCommenter struct{}

func (*serveStubCommenter) AddComment(context.Context, issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	return issueops.AddCommentResult{}, errors.ErrUnsupported
}

// serveStubRelations is the neighbor role's stand-in, ErrUnsupported like every
// stub here.
type serveStubRelations struct{}

func (*serveStubRelations) Related(context.Context, issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	return nil, errors.ErrUnsupported
}

// serveStubGraphCounter is the edge-count role's stand-in, ErrUnsupported like
// every stub here.
type serveStubGraphCounter struct{}

func (*serveStubGraphCounter) CountEdges(context.Context, issueops.EdgeCountRequest) (issueops.EdgeCountResult, error) {
	return issueops.EdgeCountResult{}, errors.ErrUnsupported
}

// serveStubCounter is the count role's stand-in. It answers ErrUnsupported like
// every stub here: this file's subject is which LAYER a role comes from, never
// what the role answers.
type serveStubCounter struct{}

func (*serveStubCounter) Count(context.Context, issueops.CountRequest) (issueops.CountResult, error) {
	return issueops.CountResult{}, errors.ErrUnsupported
}

func (*serveStubCounter) CountByGroup(context.Context, issueops.CountByGroupRequest) (issueops.CountByGroupResult, error) {
	return issueops.CountByGroupResult{}, errors.ErrUnsupported
}

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

// serveStubReadyClaimer is one of the roles the hook decorator does NOT wrap,
// so it carries no identifiable-value assertion below: this stub exists so the
// role set is complete.
type serveStubBatchCloser struct{}

func (*serveStubBatchCloser) CloseBatch(context.Context, issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	return issueops.CloseBatchResult{}, errors.ErrUnsupported
}

type serveStubReadyClaimer struct{}

func (*serveStubReadyClaimer) ClaimNext(context.Context, issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	return issueops.ClaimNextResult{}, errors.ErrUnsupported
}

type serveStubReleaser struct{}

func (*serveStubReleaser) Release(context.Context, issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	return issueops.ReleaseResult{}, errors.ErrUnsupported
}

type serveStubLifecycle struct{}

func (*serveStubLifecycle) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	return issueops.CreateResult{}, errors.ErrUnsupported
}

func (*serveStubLifecycle) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	return issueops.UpdateResult{}, errors.ErrUnsupported
}

func (*serveStubLifecycle) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	return issueops.CloseResult{}, errors.ErrUnsupported
}

func (*serveStubLifecycle) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	return issueops.ReopenResult{}, errors.ErrUnsupported
}

type serveStubDependencyEditor struct{}

func (*serveStubDependencyEditor) AddDependencies(context.Context, issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	return issueops.AddDependenciesResult{}, errors.ErrUnsupported
}

func (*serveStubDependencyEditor) RemoveDependency(context.Context, issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	return issueops.RemoveDependencyResult{}, errors.ErrUnsupported
}

type serveStubMetadataCAS struct{}

func (*serveStubMetadataCAS) CompareAndSetKey(context.Context, issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	return issueops.CompareAndSetKeyResult{}, errors.ErrUnsupported
}

type serveStubBatchApplier struct{}

func (*serveStubBatchApplier) ApplyBatch(context.Context, issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	return issueops.ApplyBatchResult{}, errors.ErrUnsupported
}
