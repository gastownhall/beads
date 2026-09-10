package beads_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/telemetry"
)

// TestReadyClaimerLayersHooksOutsideTelemetry pins the decorator order the
// claim accessor produces against the order cmd/bd assembles the chain in
// (storage_chain.go: telemetry first, hooks outermost), the same property
// TestIssueLifecycleLayersHooksOutsideTelemetry pins for the write role. A
// blind delegation at either decorator would still satisfy Storage and would
// still compile.
func TestReadyClaimerLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	claimer, err := storage.NewHookFiringStore(instrumented, nil).ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer() error = %v", err)
	}
	if got := reflect.TypeOf(claimer).String(); got != "*storage.hookReadyClaimer" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

// TestBatchCloserLayersHooksOutsideTelemetry is the same pin for the
// close-many role.
func TestBatchCloserLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	closer, err := storage.NewHookFiringStore(instrumented, nil).BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser() error = %v", err)
	}
	if got := reflect.TypeOf(closer).String(); got != "*storage.hookBatchCloser" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

// TestReadyClaimerExposesTypedUnsupportedError pins the typed error a backend
// returns when it cannot serve the role, reachable through the public alias
// without importing internal/storage.
func TestReadyClaimerExposesTypedUnsupportedError(t *testing.T) {
	claimer, err := (*dolt.DoltStore)(nil).ReadyClaimer()
	if claimer != nil {
		t.Fatalf("ReadyClaimer() claimer = %T, want nil", claimer)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("ReadyClaimer() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestBatchCloserExposesTypedUnsupportedError(t *testing.T) {
	closer, err := (*dolt.DoltStore)(nil).BatchCloser()
	if closer != nil {
		t.Fatalf("BatchCloser() closer = %T, want nil", closer)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("BatchCloser() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestBatchCreatorLayersHooksOutsideTelemetry is the same pin for the
// create-many role.
func TestBatchCreatorLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	creator, err := storage.NewHookFiringStore(instrumented, nil).BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator() error = %v", err)
	}
	if got := reflect.TypeOf(creator).String(); got != "*storage.hookBatchCreator" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

func TestBatchCreatorExposesTypedUnsupportedError(t *testing.T) {
	creator, err := (*dolt.DoltStore)(nil).BatchCreator()
	if creator != nil {
		t.Fatalf("BatchCreator() creator = %T, want nil", creator)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("BatchCreator() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestDependencyEditorLayersHooksOutsideTelemetry is the same decorator-order
// pin for the edge-write role.
func TestDependencyEditorLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	editor, err := storage.NewHookFiringStore(instrumented, nil).DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor() error = %v", err)
	}
	if got := reflect.TypeOf(editor).String(); got != "*storage.hookDependencyEditor" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

// TestBatchApplierLayersHooksOutsideTelemetry is the same pin for the
// apply-many role, and it has more to lose than its siblings: this decorator is
// the one place four hook vocabularies fire from one call, so an accessor that
// stopped wrapping would silently drop on_create, on_update AND on_close for
// every plan a caller applies.
func TestBatchApplierLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	applier, err := storage.NewHookFiringStore(instrumented, nil).BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier() error = %v", err)
	}
	if got := reflect.TypeOf(applier).String(); got != "*storage.hookBatchApplier" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

func TestBatchApplierExposesTypedUnsupportedError(t *testing.T) {
	applier, err := (*dolt.DoltStore)(nil).BatchApplier()
	if applier != nil {
		t.Fatalf("BatchApplier() applier = %T, want nil", applier)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("BatchApplier() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestCommenterLayersHooksOutsideTelemetry is the same pin for the comment
// role.
func TestCommenterLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	commenter, err := storage.NewHookFiringStore(instrumented, nil).Commenter()
	if err != nil {
		t.Fatalf("Commenter() error = %v", err)
	}
	if got := reflect.TypeOf(commenter).String(); got != "*storage.hookCommenter" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

// TestMetadataCASLayersHooksOutsideTelemetry is the same pin for the
// conditional metadata write, and it is the answer the OTHER destructive-ish
// write roles do not give: a swap that lands names a bead and moves a column,
// which is on_update — a hook the vocabulary publishes and a row a script can
// still read back — so this decorator wraps rather than recursing the way
// Sweeper and Deleter do.
func TestMetadataCASLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	cas, err := storage.NewHookFiringStore(instrumented, nil).MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS() error = %v", err)
	}
	if got := reflect.TypeOf(cas).String(); got != "*storage.hookMetadataCAS" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

func TestMetadataCASExposesTypedUnsupportedError(t *testing.T) {
	cas, err := (*dolt.DoltStore)(nil).MetadataCAS()
	if cas != nil {
		t.Fatalf("MetadataCAS() surface = %T, want nil", cas)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("MetadataCAS() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestReleaserLayersHooksOutsideTelemetry is the same pin for the claim
// release. It gives the same answer the metadata swap does and for the same
// reason: a release moves assignee and status, which is on_update — a hook the
// vocabulary publishes and a row a script can still read back — so this
// decorator wraps rather than recursing the way Sweeper and Deleter do.
func TestReleaserLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	releaser, err := storage.NewHookFiringStore(instrumented, nil).Releaser()
	if err != nil {
		t.Fatalf("Releaser() error = %v", err)
	}
	if got := reflect.TypeOf(releaser).String(); got != "*storage.hookReleaser" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

func TestReleaserExposesTypedUnsupportedError(t *testing.T) {
	releaser, err := (*dolt.DoltStore)(nil).Releaser()
	if releaser != nil {
		t.Fatalf("Releaser() surface = %T, want nil", releaser)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Releaser() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestIssueRelationsKeepsTelemetryOutermost is the READ role's version of the
// pin, and it is deliberately the other answer: the hook decorator adds no
// layer to a read, so the outermost thing a caller gets is the instrumented
// surface. A wrapper appearing here would mean a hook layer landed on a path
// with nothing to fire.
func TestIssueRelationsKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	relations, err := storage.NewHookFiringStore(instrumented, nil).IssueRelations()
	if err != nil {
		t.Fatalf("IssueRelations() error = %v", err)
	}
	if got := reflect.TypeOf(relations).String(); got != "*telemetry.instrumentedIssueRelations" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestDependencyEditorExposesTypedUnsupportedError(t *testing.T) {
	editor, err := (*dolt.DoltStore)(nil).DependencyEditor()
	if editor != nil {
		t.Fatalf("DependencyEditor() editor = %T, want nil", editor)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("DependencyEditor() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestCommenterExposesTypedUnsupportedError(t *testing.T) {
	commenter, err := (*dolt.DoltStore)(nil).Commenter()
	if commenter != nil {
		t.Fatalf("Commenter() commenter = %T, want nil", commenter)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Commenter() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestIssueRelationsExposesTypedUnsupportedError(t *testing.T) {
	relations, err := (*dolt.DoltStore)(nil).IssueRelations()
	if relations != nil {
		t.Fatalf("IssueRelations() relations = %T, want nil", relations)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("IssueRelations() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestCounterKeepsTelemetryOutermost is the READ answer for the reason
// TestIssueRelationsKeepsTelemetryOutermost gives: counting fires no completion
// hooks, so the hook decorator adds no layer.
func TestCounterKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	counter, err := storage.NewHookFiringStore(instrumented, nil).Counter()
	if err != nil {
		t.Fatalf("Counter() error = %v", err)
	}
	if got := reflect.TypeOf(counter).String(); got != "*telemetry.instrumentedCounter" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

// TestWorkspaceConfigKeepsTelemetryOutermost is the first place in this file
// where the read answer is given for a role that WRITES.
//
// The hook decorator's vocabulary is on_create / on_update / on_close and every
// one of them hands a hook script an ISSUE. A settings write changes the
// workspace rather than a bead, so there is nothing to hand one — and the
// legacy config verbs this decorator inherits fire nothing either.
func TestWorkspaceConfigKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	settings, err := storage.NewHookFiringStore(instrumented, nil).WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig() error = %v", err)
	}
	if got := reflect.TypeOf(settings).String(); got != "*telemetry.instrumentedWorkspaceConfig" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

// TestMemoriesKeepsTelemetryOutermost is the settings role's answer for the
// second plane that rides in the config table.
//
// Remember and Forget WRITE, so the reflex is to expect the hook wrapper here.
// The hook vocabulary is what decides it: on_create, on_update and on_close each
// hand a script an ISSUE, and a remembered insight is not one. There is no
// on_remember to fire and inventing one is a hook proposal, not a role commit.
// See internal/storage/hook_memories.go.
func TestMemoriesKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	memories, err := storage.NewHookFiringStore(instrumented, nil).Memories()
	if err != nil {
		t.Fatalf("Memories() error = %v", err)
	}
	if got := reflect.TypeOf(memories).String(); got != "*telemetry.instrumentedMemories" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

// TestMemoriesExposesTypedUnsupportedError pins the typed refusal a backend
// gives when it cannot serve the memory role, reachable through the public
// alias without importing internal/storage.
func TestMemoriesExposesTypedUnsupportedError(t *testing.T) {
	memories, err := (*dolt.DoltStore)(nil).Memories()
	if memories != nil {
		t.Fatalf("Memories() memories = %T, want nil", memories)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Memories() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestVersionReconcilerKeepsTelemetryOutermost is the settings role's reason
// plus one this role has on its own: it runs from PersistentPreRun on every
// startup, so a hook wrapper here would run a user's script before every
// command — including the ones that go on to fail on their own arguments —
// with no bead to hand it.
func TestVersionReconcilerKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	reconciler, err := storage.NewHookFiringStore(instrumented, nil).VersionReconciler()
	if err != nil {
		t.Fatalf("VersionReconciler() error = %v", err)
	}
	if got := reflect.TypeOf(reconciler).String(); got != "*telemetry.instrumentedVersionReconciler" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

// TestBootstrapperKeepsTelemetryOutermost has a reason that is neither the
// settings role's nor the version marker's. A bootstrap writes, and loudly —
// it is what turns a database into a workspace — but this decorator's hook
// vocabulary is issue-shaped and a bootstrap names no issue; and on a workspace
// this new, `bd init` has not installed .beads/hooks/ yet, so a hook fired here
// would run whatever the previous project in that directory left behind. See
// internal/storage/hook_bootstrapper.go.
func TestBootstrapperKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	bootstrapper, err := storage.NewHookFiringStore(instrumented, nil).Bootstrapper()
	if err != nil {
		t.Fatalf("Bootstrapper() error = %v", err)
	}
	if got := reflect.TypeOf(bootstrapper).String(); got != "*telemetry.instrumentedBootstrapper" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

// TestInitVerifierKeepsTelemetryOutermost is the ordinary read-role pin: reads
// fire no hooks, so the hook decorator recurses and the telemetry wrapper is
// what a caller holds.
func TestInitVerifierKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	verifier, err := storage.NewHookFiringStore(instrumented, nil).InitVerifier()
	if err != nil {
		t.Fatalf("InitVerifier() error = %v", err)
	}
	if got := reflect.TypeOf(verifier).String(); got != "*telemetry.instrumentedInitVerifier" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestBootstrapperExposesTypedUnsupportedError(t *testing.T) {
	bootstrapper, err := (*dolt.DoltStore)(nil).Bootstrapper()
	if bootstrapper != nil {
		t.Fatalf("Bootstrapper() bootstrapper = %T, want nil", bootstrapper)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Bootstrapper() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestInitVerifierExposesTypedUnsupportedError(t *testing.T) {
	verifier, err := (*dolt.DoltStore)(nil).InitVerifier()
	if verifier != nil {
		t.Fatalf("InitVerifier() verifier = %T, want nil", verifier)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("InitVerifier() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestVersionReconcilerExposesTypedUnsupportedError(t *testing.T) {
	reconciler, err := (*dolt.DoltStore)(nil).VersionReconciler()
	if reconciler != nil {
		t.Fatalf("VersionReconciler() reconciler = %T, want nil", reconciler)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("VersionReconciler() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestWorkspaceConfigExposesTypedUnsupportedError(t *testing.T) {
	settings, err := (*dolt.DoltStore)(nil).WorkspaceConfig()
	if settings != nil {
		t.Fatalf("WorkspaceConfig() settings = %T, want nil", settings)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("WorkspaceConfig() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestStatsReporterKeepsTelemetryOutermost is the READ answer again: reporting
// fires no completion hooks, so the hook decorator recurses.
func TestStatsReporterKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	reporter, err := storage.NewHookFiringStore(instrumented, nil).StatsReporter()
	if err != nil {
		t.Fatalf("StatsReporter() error = %v", err)
	}
	if got := reflect.TypeOf(reporter).String(); got != "*telemetry.instrumentedStatsReporter" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestStatsReporterExposesTypedUnsupportedError(t *testing.T) {
	reporter, err := (*dolt.DoltStore)(nil).StatsReporter()
	if reporter != nil {
		t.Fatalf("StatsReporter() reporter = %T, want nil", reporter)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("StatsReporter() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestCycleDetectorKeepsTelemetryOutermost is the READ answer again: a cycle
// sweep fires no completion hooks, so the hook decorator adds no layer.
func TestCycleDetectorKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	detector, err := storage.NewHookFiringStore(instrumented, nil).CycleDetector()
	if err != nil {
		t.Fatalf("CycleDetector() error = %v", err)
	}
	if got := reflect.TypeOf(detector).String(); got != "*telemetry.instrumentedCycleDetector" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestCycleDetectorExposesTypedUnsupportedError(t *testing.T) {
	detector, err := (*dolt.DoltStore)(nil).CycleDetector()
	if detector != nil {
		t.Fatalf("CycleDetector() detector = %T, want nil", detector)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("CycleDetector() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestEdgeReaderKeepsTelemetryOutermost is the READ answer again: reading edges
// fires no completion hooks, so the hook decorator adds no layer.
func TestEdgeReaderKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	edges, err := storage.NewHookFiringStore(instrumented, nil).EdgeReader()
	if err != nil {
		t.Fatalf("EdgeReader() error = %v", err)
	}
	if got := reflect.TypeOf(edges).String(); got != "*telemetry.instrumentedEdgeReader" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestEdgeReaderExposesTypedUnsupportedError(t *testing.T) {
	edges, err := (*dolt.DoltStore)(nil).EdgeReader()
	if edges != nil {
		t.Fatalf("EdgeReader() reader = %T, want nil", edges)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("EdgeReader() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestBlockingAnnotatorKeepsTelemetryOutermost is the READ answer again:
// annotating a page fires no completion hooks, so the hook decorator adds no
// layer.
func TestBlockingAnnotatorKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	annotator, err := storage.NewHookFiringStore(instrumented, nil).BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator() error = %v", err)
	}
	if got := reflect.TypeOf(annotator).String(); got != "*telemetry.instrumentedBlockingAnnotator" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestBlockingAnnotatorExposesTypedUnsupportedError(t *testing.T) {
	annotator, err := (*dolt.DoltStore)(nil).BlockingAnnotator()
	if annotator != nil {
		t.Fatalf("BlockingAnnotator() annotator = %T, want nil", annotator)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("BlockingAnnotator() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestTreeWalkerKeepsTelemetryOutermost is the READ answer again: a tree walk
// fires no completion hooks, so the hook decorator adds no layer.
func TestTreeWalkerKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	walker, err := storage.NewHookFiringStore(instrumented, nil).TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker() error = %v", err)
	}
	if got := reflect.TypeOf(walker).String(); got != "*telemetry.instrumentedTreeWalker" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestTreeWalkerExposesTypedUnsupportedError(t *testing.T) {
	walker, err := (*dolt.DoltStore)(nil).TreeWalker()
	if walker != nil {
		t.Fatalf("TreeWalker() walker = %T, want nil", walker)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("TreeWalker() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestGraphCounterKeepsTelemetryOutermost is the READ answer again: counting
// edges fires no completion hooks, so the hook decorator adds no layer.
func TestGraphCounterKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	counter, err := storage.NewHookFiringStore(instrumented, nil).GraphCounter()
	if err != nil {
		t.Fatalf("GraphCounter() error = %v", err)
	}
	if got := reflect.TypeOf(counter).String(); got != "*telemetry.instrumentedGraphCounter" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestGraphCounterExposesTypedUnsupportedError(t *testing.T) {
	counter, err := (*dolt.DoltStore)(nil).GraphCounter()
	if counter != nil {
		t.Fatalf("GraphCounter() counter = %T, want nil", counter)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("GraphCounter() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestCounterExposesTypedUnsupportedError(t *testing.T) {
	counter, err := (*dolt.DoltStore)(nil).Counter()
	if counter != nil {
		t.Fatalf("Counter() counter = %T, want nil", counter)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Counter() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestReadyCounterKeepsTelemetryOutermost is the READ answer again: sizing the
// ready set fires no completion hooks, so the hook decorator adds no layer.
func TestReadyCounterKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	counter, err := storage.NewHookFiringStore(instrumented, nil).ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter() error = %v", err)
	}
	if got := reflect.TypeOf(counter).String(); got != "*telemetry.instrumentedReadyCounter" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestReadyCounterExposesTypedUnsupportedError(t *testing.T) {
	counter, err := (*dolt.DoltStore)(nil).ReadyCounter()
	if counter != nil {
		t.Fatalf("ReadyCounter() counter = %T, want nil", counter)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("ReadyCounter() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestQuerierKeepsTelemetryOutermost is the READ answer again: a query fires no
// completion hooks, so the hook decorator adds no layer.
func TestQuerierKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	querier, err := storage.NewHookFiringStore(instrumented, nil).Querier()
	if err != nil {
		t.Fatalf("Querier() error = %v", err)
	}
	if got := reflect.TypeOf(querier).String(); got != "*telemetry.instrumentedQuerier" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestQuerierExposesTypedUnsupportedError(t *testing.T) {
	querier, err := (*dolt.DoltStore)(nil).Querier()
	if querier != nil {
		t.Fatalf("Querier() querier = %T, want nil", querier)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Querier() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestSweeperKeepsTelemetryOutermost is a WRITE role that answers the way the
// reads do: there is no on_delete hook to fire
// (internal/storage/hook_sweeper.go), so the hook decorator adds no layer.
// Pinning it here is what keeps that a decision rather than a wrapper someone
// forgot to write.
func TestSweeperKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	sweeper, err := storage.NewHookFiringStore(instrumented, nil).Sweeper()
	if err != nil {
		t.Fatalf("Sweeper() error = %v", err)
	}
	if got := reflect.TypeOf(sweeper).String(); got != "*telemetry.instrumentedSweeper" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestSweeperExposesTypedUnsupportedError(t *testing.T) {
	sweeper, err := (*dolt.DoltStore)(nil).Sweeper()
	if sweeper != nil {
		t.Fatalf("Sweeper() sweeper = %T, want nil", sweeper)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Sweeper() error = %v, want *beads.ErrUnsupported", err)
	}
}

// TestDeleterKeepsTelemetryOutermost is the second write role to answer the way
// the reads do: there is no on_delete hook to fire
// (internal/storage/hook_deleter.go), so the hook decorator adds no layer.
func TestDeleterKeepsTelemetryOutermost(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	deleter, err := storage.NewHookFiringStore(instrumented, nil).Deleter()
	if err != nil {
		t.Fatalf("Deleter() error = %v", err)
	}
	if got := reflect.TypeOf(deleter).String(); got != "*telemetry.instrumentedDeleter" {
		t.Fatalf("outer layer = %s, want the telemetry wrapper unwrapped by the hook decorator", got)
	}
}

func TestDeleterExposesTypedUnsupportedError(t *testing.T) {
	deleter, err := (*dolt.DoltStore)(nil).Deleter()
	if deleter != nil {
		t.Fatalf("Deleter() deleter = %T, want nil", deleter)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Deleter() error = %v, want *beads.ErrUnsupported", err)
	}
}
