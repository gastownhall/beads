package storage

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// roleAccessorNames is the capability surface every storage decorator has to
// answer for: every DoltStorage method that hands out a role interface from the
// public facade.
//
// It is DERIVED because a written-out list is the same silence this test
// exists to break. A hand-kept list of twenty-four names promised that adding a
// twenty-fifth role would fail somewhere, and it could not deliver: the
// decorators embed DoltStorage, so the new accessor is promoted rather than
// declared, everything still compiles, and the census simply never hears about
// it. Reflection puts the new role in the census the moment it joins the
// interface, which is the only version of that promise that holds.
var roleAccessorNames = deriveRoleAccessorNames()

// deriveRoleAccessorNames censuses the role accessors DoltStorage declares.
func deriveRoleAccessorNames() []string {
	names, _ := roleAccessorNamesOf(reflect.TypeOf((*DoltStorage)(nil)).Elem())
	return names
}

// roleAccessorNamesOf reports every method of surface that takes nothing and
// returns (role interface, error), where the role belongs to the public facade.
// It also reports the accessor-shaped methods whose interface belongs to no
// facade package, because that is how a census shrinks in silence: a third
// facade package would arrive in exactly that shape and every role in it would
// simply stop being asked about.
func roleAccessorNamesOf(surface reflect.Type) (names, unclassified []string) {
	facade := map[string]bool{
		reflect.TypeOf((*issueops.Reader)(nil)).Elem().PkgPath():    true,
		reflect.TypeOf((*memoryops.Memories)(nil)).Elem().PkgPath(): true,
	}
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	for i := range surface.NumMethod() {
		accessor := surface.Method(i)
		signature := accessor.Type
		if signature.NumIn() != 0 || signature.NumOut() != 2 || signature.Out(1) != errorType {
			continue
		}
		role := signature.Out(0)
		if role.Kind() != reflect.Interface {
			continue
		}
		if !facade[role.PkgPath()] {
			unclassified = append(unclassified, accessor.Name+" -> "+role.PkgPath()+"."+role.Name())
			continue
		}
		names = append(names, accessor.Name)
	}
	sort.Strings(names)
	sort.Strings(unclassified)
	return names, unclassified
}

// TestEveryStoreRoleAccessorIsClassified fails when DoltStorage hands out an
// interface the census cannot place. Every one of the twenty-eight today is a
// facade role, so this costs nothing and closes the path where a role surface
// grows a package and the census quietly stops covering it. (roleAccessorNames
// is derived, so the count is prose and only this sentence goes stale — but it
// does go stale, and the last three sweeps found it saying twenty-seven.)
func TestEveryStoreRoleAccessorIsClassified(t *testing.T) {
	_, unclassified := roleAccessorNamesOf(reflect.TypeOf((*DoltStorage)(nil)).Elem())
	for _, accessor := range unclassified {
		t.Errorf("accessor %s belongs to no known facade package: teach the census that package, "+
			"or every role in it goes uncensused", accessor)
	}
}

// rehearsalSurface is a fabricated store surface: DoltStorage plus one more
// role accessor and two methods that are not accessors at all.
type rehearsalSurface interface {
	DoltStorage
	// Rehearser is the one more accessor the census must find on its own.
	Rehearser() (issueops.Reader, error)
	// Rehearse returns no role, and RehearsalName returns no error: neither is
	// an accessor, and neither may reach the census.
	Rehearse() error
	RehearsalName() (string, error)
}

// TestRoleAccessorCensusGrowsWithTheStoreSurface is the test the old
// hand-written list could not have passed. It fabricates one more role
// accessor and asserts the census finds it without being told, which is the
// whole reason the list is derived: a new accessor is promoted onto every
// decorator by the embedded interface, so nothing else in the build says a
// word about it.
func TestRoleAccessorCensusGrowsWithTheStoreSurface(t *testing.T) {
	rehearsal, unclassified := roleAccessorNamesOf(reflect.TypeOf((*rehearsalSurface)(nil)).Elem())

	want := append(slices.Clone(roleAccessorNames), "Rehearser")
	slices.Sort(want)
	if !slices.Equal(rehearsal, want) {
		t.Errorf("census of the rehearsal surface = %v, want the real accessors plus Rehearser (%v)", rehearsal, want)
	}
	if len(unclassified) != 0 {
		t.Errorf("rehearsal surface reported %v as unclassified; every added method returns a facade role or no role at all", unclassified)
	}
}

// TestHookFiringStoreDeclaresEveryRoleAccessor is the structural half of the
// decorator's conformance story.
//
// HookFiringStore embeds DoltStorage for pass-through, which means DELETING an
// accessor still compiles: the embedded interface supplies a promoted method
// with the same signature, the decorator silently hands back the inner store's
// surface, and every hook that surface should have fired stops firing. The
// compile-time `var _ interface{...}` blocks at the bottom of hook_decorator.go
// cannot see this — a promoted method satisfies them exactly as well as a
// declared one. Reflection can: the compiler reports a promoted method's
// implementation as living in "<autogenerated>".
func TestHookFiringStoreDeclaresEveryRoleAccessor(t *testing.T) {
	assertRoleAccessorsAreDeclared(t, reflect.TypeOf(&HookFiringStore{}))
}

// assertRoleAccessorsAreDeclared fails for any role accessor the type inherits
// from an embedded field instead of declaring itself. Exported so the telemetry
// package, which cannot be imported from here, has a sibling of the same shape;
// see internal/telemetry/role_accessor_decorator_test.go.
func assertRoleAccessorsAreDeclared(t *testing.T, decorator reflect.Type) {
	t.Helper()
	if len(roleAccessorNames) == 0 {
		t.Fatal("no DoltStorage method hands out a facade role; the census is empty and this test would pass vacuously")
	}
	for _, name := range roleAccessorNames {
		method, ok := decorator.MethodByName(name)
		if !ok {
			t.Errorf("%s has no %s accessor at all", decorator, name)
			continue
		}
		file, _ := runtime.FuncForPC(method.Func.Pointer()).FileLine(method.Func.Pointer())
		if strings.Contains(file, "<autogenerated>") {
			t.Errorf("%s.%s is promoted from the embedded store, not declared on the decorator: "+
				"the decorator hands back the inner surface undecorated", decorator, name)
		}
	}
}

// roleAccessorStore is a DoltStorage whose only real methods are the
// twenty-eight role accessors, each answering with a distinguishable sentinel
// so a test can tell a decorated surface from a passed-through one.
type roleAccessorStore struct {
	DoltStorage
	lifecycle    issueops.Lifecycle
	reader       issueops.Reader
	relations    issueops.Relations
	edges        issueops.EdgeReader
	blocking     issueops.BlockingAnnotator
	tree         issueops.TreeWalker
	graphCounter issueops.GraphCounter
	counter      issueops.Counter
	settings     issueops.WorkspaceConfig
	memories     memoryops.Memories
	versions     issueops.VersionReconciler
	stats        issueops.StatsReporter
	cycles       issueops.CycleDetector
	commenter    issueops.Commenter
	claimer      issueops.ReadyClaimer
	closer       issueops.BatchCloser
	creator      issueops.BatchCreator
	editor       issueops.DependencyEditor
	applier      issueops.BatchApplier
	readyCounter issueops.ReadyCounter
	querier      issueops.Querier
	sweeper      issueops.Sweeper
	deleter      issueops.Deleter
	bootstrapper issueops.Bootstrapper
	verifier     issueops.InitVerifier
	metadataCAS  issueops.MetadataCAS
	releaser     issueops.Releaser
	err          error
}

func newRoleAccessorStore() *roleAccessorStore {
	sentinel := &roleAccessorSentinel{}
	return &roleAccessorStore{
		memories:     &memoryRoleSentinel{},
		lifecycle:    sentinel,
		reader:       sentinel,
		relations:    sentinel,
		edges:        sentinel,
		blocking:     sentinel,
		graphCounter: sentinel,
		counter:      sentinel,
		settings:     sentinel,
		versions:     sentinel,
		stats:        sentinel,
		cycles:       sentinel,
		commenter:    sentinel,
		claimer:      sentinel,
		closer:       sentinel,
		creator:      sentinel,
		editor:       sentinel,
		applier:      sentinel,
		readyCounter: sentinel,
		querier:      sentinel,
		sweeper:      sentinel,
		deleter:      sentinel,
		bootstrapper: sentinel,
		verifier:     sentinel,
		metadataCAS:  sentinel,
		releaser:     sentinel,
	}
}

func (s *roleAccessorStore) IssueLifecycle() (issueops.Lifecycle, error) { return s.lifecycle, s.err }
func (s *roleAccessorStore) IssueReader() (issueops.Reader, error)       { return s.reader, s.err }
func (s *roleAccessorStore) IssueRelations() (issueops.Relations, error) { return s.relations, s.err }
func (s *roleAccessorStore) Counter() (issueops.Counter, error)          { return s.counter, s.err }
func (s *roleAccessorStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return s.settings, s.err
}
func (s *roleAccessorStore) Memories() (memoryops.Memories, error) {
	return s.memories, s.err
}
func (s *roleAccessorStore) VersionReconciler() (issueops.VersionReconciler, error) {
	return s.versions, s.err
}
func (s *roleAccessorStore) StatsReporter() (issueops.StatsReporter, error) {
	return s.stats, s.err
}

func (s *roleAccessorStore) CycleDetector() (issueops.CycleDetector, error) {
	return s.cycles, s.err
}
func (s *roleAccessorStore) EdgeReader() (issueops.EdgeReader, error) { return s.edges, s.err }
func (s *roleAccessorStore) TreeWalker() (issueops.TreeWalker, error) { return s.tree, s.err }
func (s *roleAccessorStore) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return s.blocking, s.err
}
func (s *roleAccessorStore) Commenter() (issueops.Commenter, error) { return s.commenter, s.err }
func (s *roleAccessorStore) ReadyCounter() (issueops.ReadyCounter, error) {
	return s.readyCounter, s.err
}
func (s *roleAccessorStore) Querier() (issueops.Querier, error) { return s.querier, s.err }
func (s *roleAccessorStore) Sweeper() (issueops.Sweeper, error) {
	return s.sweeper, s.err
}
func (s *roleAccessorStore) Deleter() (issueops.Deleter, error) {
	return s.deleter, s.err
}
func (s *roleAccessorStore) Bootstrapper() (issueops.Bootstrapper, error) {
	return s.bootstrapper, s.err
}
func (s *roleAccessorStore) InitVerifier() (issueops.InitVerifier, error) {
	return s.verifier, s.err
}
func (s *roleAccessorStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return s.claimer, s.err
}
func (s *roleAccessorStore) BatchCloser() (issueops.BatchCloser, error) { return s.closer, s.err }
func (s *roleAccessorStore) BatchCreator() (issueops.BatchCreator, error) {
	return s.creator, s.err
}
func (s *roleAccessorStore) DependencyEditor() (issueops.DependencyEditor, error) {
	return s.editor, s.err
}
func (s *roleAccessorStore) GraphCounter() (issueops.GraphCounter, error) {
	return s.graphCounter, s.err
}
func (s *roleAccessorStore) MetadataCAS() (issueops.MetadataCAS, error) {
	return s.metadataCAS, s.err
}
func (s *roleAccessorStore) BatchApplier() (issueops.BatchApplier, error) {
	return s.applier, s.err
}
func (s *roleAccessorStore) Releaser() (issueops.Releaser, error) {
	return s.releaser, s.err
}

// roleAccessorSentinel implements twenty-seven of the twenty-eight roles at
// once — every one but memoryops.Memories, whose List collides with
// issueops.Reader.List and needs the second sentinel below.
// Nothing calls its methods; identity is the whole point.
type roleAccessorSentinel struct{}

func (*roleAccessorSentinel) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	return issueops.CreateResult{}, nil
}
func (*roleAccessorSentinel) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	return issueops.UpdateResult{}, nil
}
func (*roleAccessorSentinel) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	return issueops.CloseResult{}, nil
}
func (*roleAccessorSentinel) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	return issueops.ReopenResult{}, nil
}
func (*roleAccessorSentinel) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, nil
}
func (*roleAccessorSentinel) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, nil
}
func (*roleAccessorSentinel) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, nil
}
func (*roleAccessorSentinel) Related(context.Context, issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	return nil, nil
}
func (*roleAccessorSentinel) ReadEdges(context.Context, issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	return issueops.EdgeReadResult{}, nil
}
func (*roleAccessorSentinel) AnnotateBlocking(context.Context, issueops.BlockingRequest) (issueops.BlockingResult, error) {
	return issueops.BlockingResult{}, nil
}
func (*roleAccessorSentinel) Count(context.Context, issueops.CountRequest) (issueops.CountResult, error) {
	return issueops.CountResult{}, nil
}
func (*roleAccessorSentinel) CountByGroup(context.Context, issueops.CountByGroupRequest) (issueops.CountByGroupResult, error) {
	return issueops.CountByGroupResult{}, nil
}
func (*roleAccessorSentinel) CountEdges(context.Context, issueops.EdgeCountRequest) (issueops.EdgeCountResult, error) {
	return issueops.EdgeCountResult{}, nil
}
func (*roleAccessorSentinel) GetSetting(context.Context, issueops.GetSettingRequest) (issueops.SettingResult, error) {
	return issueops.SettingResult{}, nil
}
func (*roleAccessorSentinel) ListSettings(context.Context, issueops.ListSettingsRequest) (issueops.ListSettingsResult, error) {
	return issueops.ListSettingsResult{}, nil
}
func (*roleAccessorSentinel) SetSetting(context.Context, issueops.SetSettingRequest) (issueops.SetSettingResult, error) {
	return issueops.SetSettingResult{}, nil
}
func (*roleAccessorSentinel) UnsetSetting(context.Context, issueops.UnsetSettingRequest) (issueops.UnsetSettingResult, error) {
	return issueops.UnsetSettingResult{}, nil
}
func (*roleAccessorSentinel) RecordedVersion(context.Context, issueops.RecordedVersionRequest) (issueops.RecordedVersionResult, error) {
	return issueops.RecordedVersionResult{}, nil
}
func (*roleAccessorSentinel) ReconcileVersion(context.Context, issueops.VersionReconcileRequest) (issueops.VersionReconcileResult, error) {
	return issueops.VersionReconcileResult{}, nil
}
func (*roleAccessorSentinel) Stats(context.Context, issueops.StatsRequest) (issueops.StatsResult, error) {
	return issueops.StatsResult{}, nil
}
func (*roleAccessorSentinel) AssigneeStats(context.Context, issueops.AssigneeStatsRequest) (issueops.StatsResult, error) {
	return issueops.StatsResult{}, nil
}

func (*roleAccessorSentinel) DetectCycles(context.Context, issueops.DetectCyclesRequest) (issueops.CycleReport, error) {
	return issueops.CycleReport{}, nil
}
func (*roleAccessorSentinel) ApplyBatch(context.Context, issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	return issueops.ApplyBatchResult{}, nil
}

func (*roleAccessorSentinel) CountReady(context.Context, issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	return issueops.ReadyCountResult{}, nil
}

func (*roleAccessorSentinel) Query(context.Context, issueops.QueryRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, nil
}

func (*roleAccessorSentinel) Delete(context.Context, issueops.DeleteRequest) (issueops.DeleteResult, error) {
	return issueops.DeleteResult{}, nil
}

func (*roleAccessorSentinel) Sweep(context.Context, issueops.SweepRequest) (issueops.SweepResult, error) {
	return issueops.SweepResult{}, nil
}
func (*roleAccessorSentinel) Bootstrap(context.Context, issueops.BootstrapRequest) (issueops.BootstrapResult, error) {
	return issueops.BootstrapResult{}, nil
}
func (*roleAccessorSentinel) VerifyIdentity(context.Context, issueops.VerifyIdentityRequest) (issueops.VerifyIdentityResult, error) {
	return issueops.VerifyIdentityResult{}, nil
}
func (*roleAccessorSentinel) AddComment(context.Context, issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	return issueops.AddCommentResult{}, nil
}
func (*roleAccessorSentinel) ClaimNext(context.Context, issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	return issueops.ClaimNextResult{}, nil
}
func (*roleAccessorSentinel) CloseBatch(context.Context, issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	return issueops.CloseBatchResult{}, nil
}
func (*roleAccessorSentinel) CreateBatch(context.Context, issueops.CreateBatchRequest) (issueops.CreateBatchResult, error) {
	return issueops.CreateBatchResult{}, nil
}
func (*roleAccessorSentinel) AddDependencies(context.Context, issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	return issueops.AddDependenciesResult{}, nil
}
func (*roleAccessorSentinel) RemoveDependency(context.Context, issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	return issueops.RemoveDependencyResult{}, nil
}
func (*roleAccessorSentinel) CompareAndSetKey(context.Context, issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	return issueops.CompareAndSetKeyResult{}, nil
}
func (*roleAccessorSentinel) Release(context.Context, issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	return issueops.ReleaseResult{}, nil
}

// memoryRoleSentinel is the memory role's sentinel, and the one role
// memoryRoleSentinel is the remaining role's sentinel, and the one role
// that cannot share the struct above: memoryops.Memories.List and
// issueops.Reader.List are the same method name with different signatures, so
// no single Go type can satisfy both. A second sentinel value is all the split
// costs; identity is still the whole point.
type memoryRoleSentinel struct{}

func (*memoryRoleSentinel) Remember(context.Context, memoryops.RememberRequest) (memoryops.RememberResult, error) {
	return memoryops.RememberResult{}, nil
}
func (*memoryRoleSentinel) Recall(context.Context, memoryops.RecallRequest) (memoryops.RecallResult, error) {
	return memoryops.RecallResult{}, nil
}
func (*memoryRoleSentinel) Forget(context.Context, memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	return memoryops.ForgetResult{}, nil
}
func (*memoryRoleSentinel) List(context.Context, memoryops.ListRequest) (memoryops.ListResult, error) {
	return memoryops.ListResult{}, nil
}

// TestHookFiringStoreWrapsTheWriteRolesAndPassesTheReadsThrough is the
// behavioural half, and it catches what reflection cannot: an accessor that IS
// declared but reads `return h.inner.Commenter()`. That edit compiles, keeps
// satisfying DoltStorage, and silently stops every completion hook on that
// role.
//
// THE PASS-THROUGH ROLES are asserted the other way round on purpose, in one
// paragraph rather than four near-identical ones (bd-8ri3m). Reads fire no
// completion hooks, so IssueReader, IssueRelations, Counter, StatsReporter,
// CycleDetector, EdgeReader, BlockingAnnotator, TreeWalker, GraphCounter,
// ReadyCounter, Querier and InitVerifier deliberately return the inner surface
// unwrapped,
// each in its own hook_*.go. The ones in that column that are NOT reads are
// WorkspaceConfig, Memories, VersionReconciler, Bootstrapper, Sweeper and
// Deleter: a settings write changes the workspace rather than a bead, a
// remembered insight names no bead and there is no on_remember to fire, a
// version marker names no bead at all, a bootstrap lands on a workspace whose
// hooks are not installed yet, a swept row is gone, and the hook vocabulary has
// no name for a deletion — so this decorator's issue-shaped hook vocabulary has
// nothing to hand a hook script. This test pins every one of them as a decision
// rather than leaving it indistinguishable from the regression above.
func TestHookFiringStoreWrapsTheWriteRolesAndPassesTheReadsThrough(t *testing.T) {
	inner := newRoleAccessorStore()
	store := NewHookFiringStore(inner, nil)

	for _, test := range []struct {
		name    string
		got     func() (any, error)
		want    any
		wrapped bool
	}{
		{"IssueLifecycle", func() (any, error) { return store.IssueLifecycle() }, inner.lifecycle, true},
		{"Commenter", func() (any, error) { return store.Commenter() }, inner.commenter, true},
		{"ReadyClaimer", func() (any, error) { return store.ReadyClaimer() }, inner.claimer, true},
		{"BatchCloser", func() (any, error) { return store.BatchCloser() }, inner.closer, true},
		{"BatchCreator", func() (any, error) { return store.BatchCreator() }, inner.creator, true},
		{"DependencyEditor", func() (any, error) { return store.DependencyEditor() }, inner.editor, true},
		{"MetadataCAS", func() (any, error) { return store.MetadataCAS() }, inner.metadataCAS, true},
		{"BatchApplier", func() (any, error) { return store.BatchApplier() }, inner.applier, true},
		{"Releaser", func() (any, error) { return store.Releaser() }, inner.releaser, true},
		{"IssueReader", func() (any, error) { return store.IssueReader() }, inner.reader, false},
		{"IssueRelations", func() (any, error) { return store.IssueRelations() }, inner.relations, false},
		{"EdgeReader", func() (any, error) { return store.EdgeReader() }, inner.edges, false},
		{"BlockingAnnotator", func() (any, error) { return store.BlockingAnnotator() }, inner.blocking, false},
		{"TreeWalker", func() (any, error) { return store.TreeWalker() }, inner.tree, false},
		{"GraphCounter", func() (any, error) { return store.GraphCounter() }, inner.graphCounter, false},
		{"Counter", func() (any, error) { return store.Counter() }, inner.counter, false},
		{"WorkspaceConfig", func() (any, error) { return store.WorkspaceConfig() }, inner.settings, false},
		{"Memories", func() (any, error) { return store.Memories() }, inner.memories, false},
		{"VersionReconciler", func() (any, error) { return store.VersionReconciler() }, inner.versions, false},
		{"StatsReporter", func() (any, error) { return store.StatsReporter() }, inner.stats, false},
		{"CycleDetector", func() (any, error) { return store.CycleDetector() }, inner.cycles, false},
		{"ReadyCounter", func() (any, error) { return store.ReadyCounter() }, inner.readyCounter, false},
		{"Querier", func() (any, error) { return store.Querier() }, inner.querier, false},
		{"Sweeper", func() (any, error) { return store.Sweeper() }, inner.sweeper, false},
		{"Deleter", func() (any, error) { return store.Deleter() }, inner.deleter, false},
		{"Bootstrapper", func() (any, error) { return store.Bootstrapper() }, inner.bootstrapper, false},
		{"InitVerifier", func() (any, error) { return store.InitVerifier() }, inner.verifier, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface, err := test.got()
			if err != nil {
				t.Fatalf("%s() error = %v", test.name, err)
			}
			same := surface == test.want
			switch {
			case test.wrapped && same:
				t.Fatalf("%s() returned the inner surface undecorated, so its completion hooks never fire", test.name)
			case !test.wrapped && !same:
				t.Fatalf("%s() = %T, want the inner surface unwrapped — reads fire no hooks", test.name, surface)
			}
		})
	}
}

// TestHookFiringStoreRoleAccessorsPropagateInnerErrors pins the other half of
// the recursion: an accessor that recurses must not swallow the inner store's
// refusal on the way back.
func TestHookFiringStoreRoleAccessorsPropagateInnerErrors(t *testing.T) {
	want := errors.New("inner refused")
	inner := newRoleAccessorStore()
	inner.err = want
	store := NewHookFiringStore(inner, nil)

	for _, test := range []struct {
		name string
		got  func() (any, error)
	}{
		{"IssueLifecycle", func() (any, error) { return store.IssueLifecycle() }},
		{"Commenter", func() (any, error) { return store.Commenter() }},
		{"ReadyClaimer", func() (any, error) { return store.ReadyClaimer() }},
		{"BatchCloser", func() (any, error) { return store.BatchCloser() }},
		{"BatchCreator", func() (any, error) { return store.BatchCreator() }},
		{"DependencyEditor", func() (any, error) { return store.DependencyEditor() }},
		{"MetadataCAS", func() (any, error) { return store.MetadataCAS() }},
		{"BatchApplier", func() (any, error) { return store.BatchApplier() }},
		{"Releaser", func() (any, error) { return store.Releaser() }},
		{"IssueReader", func() (any, error) { return store.IssueReader() }},
		{"IssueRelations", func() (any, error) { return store.IssueRelations() }},
		{"EdgeReader", func() (any, error) { return store.EdgeReader() }},
		{"BlockingAnnotator", func() (any, error) { return store.BlockingAnnotator() }},
		{"TreeWalker", func() (any, error) { return store.TreeWalker() }},
		{"GraphCounter", func() (any, error) { return store.GraphCounter() }},
		{"Counter", func() (any, error) { return store.Counter() }},
		{"WorkspaceConfig", func() (any, error) { return store.WorkspaceConfig() }},
		{"Memories", func() (any, error) { return store.Memories() }},
		{"VersionReconciler", func() (any, error) { return store.VersionReconciler() }},
		{"StatsReporter", func() (any, error) { return store.StatsReporter() }},
		{"CycleDetector", func() (any, error) { return store.CycleDetector() }},
		{"ReadyCounter", func() (any, error) { return store.ReadyCounter() }},
		{"Querier", func() (any, error) { return store.Querier() }},
		{"Sweeper", func() (any, error) { return store.Sweeper() }},
		{"Deleter", func() (any, error) { return store.Deleter() }},
		{"Bootstrapper", func() (any, error) { return store.Bootstrapper() }},
		{"InitVerifier", func() (any, error) { return store.InitVerifier() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.got(); !errors.Is(err, want) {
				t.Fatalf("%s() error = %v, want %v", test.name, err, want)
			}
		})
	}
}
