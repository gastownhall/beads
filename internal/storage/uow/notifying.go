// Package uow — notifying.go
//
// bd has two write plumbings. The DoltStorage decorator chain fires the
// workspace's script hooks after every mutation it lands
// (internal/storage/hook_decorator.go and its per-role siblings). The
// unit-of-work plumbing — the one proxied-server mode writes through — fired
// nothing, so any script wired to .beads/hooks/ silently missed every mutation
// that went through it.
//
// A NotifyingProvider closes that gap. It wraps a UnitOfWorkProvider so that
// every committed bead mutation performed through a unit of work runs the same
// fire-and-forget hooks the DoltStorage plumbing runs, with the same event per
// operation (see hookEventForOp).
//
// THE WRAP IS AT THE PROVIDER, NOT AT EACH VERB. Every capability this package
// publishes is built from a provider and reaches its writes through the unit of
// work's use cases, so re-binding the accessors to the wrapper and recording at
// the use-case seam covers all of them at once — including roles added later.
// The accessors are declared rather than delegated for the reason
// hook_issue_operations.go gives: an accessor that hands back the INNER
// provider's role builds it on the inner provider and silently drops every hook
// this file exists to fire. notifying_parity_test.go enforces both halves.
//
// Hooks fire strictly post-commit: mutations are buffered as they flow through
// the wrapped use cases (each buffered snapshot is read inside the transaction,
// so it reflects the mutation), and the buffer is drained to the runner only
// after Commit succeeds. A rolled-back unit of work fires nothing, and a
// retried transaction (RunTxResultWithin replays the whole attempt on a fresh
// unit of work) fires only what the winning attempt recorded.
//
// # Where this plumbing deliberately differs from the DoltStorage one
//
// Every per-verb firing rule below is matched 1:1 against the decorator that
// implements it (each override cites its twin). These five are the exceptions,
// and they are listed here rather than argued in five places:
//
//  1. LABEL MULTIPLICITY ON CREATE. The DoltStorage chain replays a legacy
//     sequence for a create that carries labels: on_create with the labels
//     STRIPPED, then one synthetic on_update per label, cumulatively
//     (createHookEvents). This plumbing fires ONE on_create carrying the issue
//     with its labels. The event vocabulary is the same and the information is
//     the same; the multiplicity is a compatibility shim for the pre-decorator
//     CLI, and a new plumbing that reproduced it would cement it.
//
//  2. EDGE MULTIPLICITY. A create or an edge batch that writes several edges
//     leaving one issue fires ONE on_update for that issue, where the
//     DoltStorage create path fires one per edge. The SET of issues told is the
//     same — the created row and every far end, each carrying its records; only
//     the repeat count differs. Per distinct source is the rule its own
//     dependency-editor role applies (hook_dependency_editor.go), and two edges
//     leaving an issue are one change to it.
//
//  3. IMPORT FIRES NOTHING. `bd import` runs the batch-upsert engine on the
//     unit of work's statement runner (importer.go), so its writes never pass a
//     use case for the recorder to see. The DoltStorage plumbing imports
//     through the same engine and fires nothing either; a hook per imported
//     issue would be a new behavior on both, not a fix to this one.
//
//  4. A GUARDED VERB'S PRECONDITION IS NOT AN UPDATE. `bd close
//     --expect-version` spells its compare-and-set as a separate ApplyUpdate
//     that writes nothing; the DoltStorage plumbing passes the precondition
//     into the close. See specWrites.
//
//  5. THE PROMOTION COMMENT. `bd promote` records an audit comment on the
//     promoted issue, and this plumbing's one comment verb fires on_update for
//     it. The DoltStorage route writes that comment through the legacy
//     AddComment, which the decorator wraps only INSIDE a transaction
//     (hookTrackingTransaction.AddComment) and not at the store level, where
//     promote calls it — so an embedded promote fires nothing. One verb cannot
//     tell which caller it is serving, and "a comment landed" is the on_update
//     this vocabulary has.
//
// A sixth is not a divergence but is worth stating: a unit of work whose commit
// message is empty is ROLLED BACK by RunTxResult, and a rolled-back attempt
// fires nothing — so a write that lands nothing reports nothing, on both
// plumbings.
package uow

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// hookRunner is the subset of *hooks.Runner this plumbing needs — the same
// fire-and-forget contract the DoltStorage plumbing uses. Declared as an
// interface so tests can supply a recording fake.
type hookRunner interface {
	Run(event string, issue *types.Issue)
}

// Sinks are the post-commit notification targets. Hook may be nil to disable.
type Sinks struct {
	// Hook is the workspace's script-hook runner (*hooks.Runner). Its behavior
	// is unchanged; the unit-of-work plumbing simply now feeds it too.
	Hook hookRunner
}

func (s Sinks) empty() bool { return s.Hook == nil }

// NewNotifyingProvider wraps inner so committed mutations fire hooks
// post-commit. With no sinks configured — `no-hooks`, or a caller that has no
// runner to give — inner is returned unwrapped, so a bd that cannot fire hooks
// pays nothing for this file.
//
// A workspace whose .beads/hooks/ holds no script is NOT that case: the runner
// exists, the wrapper is built, and the buffering happens; the runner then
// finds no script and does nothing. That is the same deal the DoltStorage chain
// takes (wireStorageDecorators wraps whenever a runner exists), and it is what
// lets a user drop in a hook script without restarting anything.
func NewNotifyingProvider(inner UnitOfWorkProvider, sinks Sinks) UnitOfWorkProvider {
	if sinks.empty() || isNilUnitOfWorkProvider(inner) {
		return inner
	}
	return &notifyingProvider{inner: inner, sinks: sinks}
}

type notifyingProvider struct {
	inner UnitOfWorkProvider
	sinks Sinks
}

func (p *notifyingProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	uw, err := p.inner.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &notifyingUOW{UnitOfWork: uw, rec: &recorder{}, sinks: p.sinks}, nil
}

func (p *notifyingProvider) Close(ctx context.Context) error {
	return p.inner.Close(ctx)
}

// Unwrap returns the provider beneath the hook layer, satisfying
// ProviderUnwrapper.
func (p *notifyingProvider) Unwrap() UnitOfWorkProvider { return p.inner }

// ProviderUnwrapper is implemented by provider decorators that wrap an inner
// provider — the unit-of-work twin of storage.Unwrapper.
type ProviderUnwrapper interface {
	Unwrap() UnitOfWorkProvider
}

// unitOfWorkUnwrapper is the same idea one level down, for decorators that wrap
// a unit of work.
type unitOfWorkUnwrapper interface {
	Unwrap() UnitOfWork
}

// unwrapUOW peels any chain of decorators off a unit of work. A caller reaches
// for it when it needs the CONCRETE unit of work rather than the surface —
// today only the importer, which asks the transaction for its statement runner
// (importer.go). Nothing about a decorator's behavior is bypassed by this: the
// recording layer wraps the USE CASES, and a caller that wants those still asks
// the unit of work it was handed.
//
// UNEXPORTED, unlike UnwrapProvider beside it, and deliberately: peeling a unit
// of work reaches the transaction, where a write fires no hook, and
// notifying_parity_test.go can only account for the callers it can PARSE. Keep
// it reachable only from this package and that guard stays complete. The
// provider twin is exported because `bd serve` genuinely needs it and peeling
// there loses no coverage — it hands back a provider, not a transaction.
func unwrapUOW(uw UnitOfWork) UnitOfWork {
	for {
		unwrapper, ok := uw.(unitOfWorkUnwrapper)
		if !ok {
			return uw
		}
		uw = unwrapper.Unwrap()
	}
}

// UnwrapProvider peels any chain of decorators and returns the innermost
// provider. A caller that must NOT run the workspace's hook scripts asks for
// this: `bd serve` serves from beneath the hook layer, as it does on the store
// side ((*storage.HookFiringStore).Unwrap).
func UnwrapProvider(p UnitOfWorkProvider) UnitOfWorkProvider {
	for {
		unwrapper, ok := p.(ProviderUnwrapper)
		if !ok {
			return p
		}
		p = unwrapper.Unwrap()
	}
}

// ProviderFiresHooks reports whether a landed write through this provider runs
// the workspace's hook scripts. It is the question a network server has to
// answer before it serves anything, and one a caller cannot answer from the
// provider's own type — the accessors on a notifying provider return notifying
// roles by design.
//
// A provider is asked about its type, not its configuration: NewNotifyingProvider
// hands back the inner provider when no sinks are set, so a wrapper only exists
// where hooks are meant to fire.
func ProviderFiresHooks(p UnitOfWorkProvider) bool {
	_, ok := p.(*notifyingProvider)
	return ok
}

// ── Capability accessors ────────────────────────────────────────────
//
// Each builds its role from THIS provider, so the role's units of work are
// notifying ones. Passing p.inner instead would compile, keep every type
// assertion working, and stop the hooks.

func (p *notifyingProvider) IssueLifecycle() (publicops.Lifecycle, error) {
	return NewIssueOperations(p)
}

func (p *notifyingProvider) IssueReader() (publicops.Reader, error) { return NewIssueReader(p) }

func (p *notifyingProvider) IssueClaimer() (publicops.Claimer, error) { return NewIssueClaimer(p) }

func (p *notifyingProvider) IssueRelations() (publicops.Relations, error) {
	return NewIssueRelations(p)
}

func (p *notifyingProvider) EdgeReader() (publicops.EdgeReader, error) { return NewEdgeReader(p) }

func (p *notifyingProvider) BlockingAnnotator() (publicops.BlockingAnnotator, error) {
	return NewBlockingAnnotator(p)
}

func (p *notifyingProvider) TreeWalker() (publicops.TreeWalker, error) { return NewTreeWalker(p) }

func (p *notifyingProvider) GraphCounter() (publicops.GraphCounter, error) {
	return NewGraphCounter(p)
}

func (p *notifyingProvider) Counter() (publicops.Counter, error) { return NewCounter(p) }

func (p *notifyingProvider) ReadyCounter() (publicops.ReadyCounter, error) {
	return NewReadyCounter(p)
}

func (p *notifyingProvider) ReadyClaimer() (publicops.ReadyClaimer, error) {
	return NewReadyClaimer(p)
}

func (p *notifyingProvider) Querier() (publicops.Querier, error) { return NewQuerier(p) }

func (p *notifyingProvider) StatsReporter() (publicops.StatsReporter, error) {
	return NewStatsReporter(p)
}

func (p *notifyingProvider) CycleDetector() (publicops.CycleDetector, error) {
	return NewCycleDetector(p)
}

func (p *notifyingProvider) Commenter() (publicops.Commenter, error) { return NewCommenter(p) }

func (p *notifyingProvider) BatchCloser() (publicops.BatchCloser, error) { return NewBatchCloser(p) }

func (p *notifyingProvider) BatchCreator() (publicops.BatchCreator, error) {
	return NewBatchCreator(p)
}

func (p *notifyingProvider) DependencyEditor() (publicops.DependencyEditor, error) {
	return NewDependencyEditor(p)
}

// BatchApplier builds on THIS provider, like every role above it, so the
// notifications its items produce are the ones the recording use cases already
// emit for a create, an update, a close and an edge. It needs no recorder of
// its own for that reason: the role composes those use cases rather than
// reaching past them.
func (p *notifyingProvider) BatchApplier() (publicops.BatchApplier, error) {
	return NewBatchApplier(p)
}

func (p *notifyingProvider) Deleter() (publicops.Deleter, error) { return NewDeleter(p) }

func (p *notifyingProvider) Sweeper() (publicops.Sweeper, error) { return NewSweeper(p) }

func (p *notifyingProvider) Importer() (publicops.Importer, error) { return NewImporter(p) }

func (p *notifyingProvider) Bootstrapper() (publicops.Bootstrapper, error) {
	return NewBootstrapper(p)
}

func (p *notifyingProvider) InitVerifier() (publicops.InitVerifier, error) {
	return NewInitVerifier(p)
}

func (p *notifyingProvider) WorkspaceConfig() (publicops.WorkspaceConfig, error) {
	return NewWorkspaceConfig(p)
}

func (p *notifyingProvider) VersionReconciler() (publicops.VersionReconciler, error) {
	return NewVersionReconciler(p)
}

func (p *notifyingProvider) MetadataCAS() (publicops.MetadataCAS, error) { return NewMetadataCAS(p) }

func (p *notifyingProvider) Releaser() (publicops.Releaser, error) { return NewReleaser(p) }

func (p *notifyingProvider) Memories() (memoryops.Memories, error) { return NewMemories(p) }

// EventsJournalCursor builds on THIS provider, like every role above it, so a
// journal read taken through a notifying provider still runs in a unit of work
// this layer opened. Nothing here records, and nothing needs to: this accessor
// reaches only the READ half of EventsJournalUseCase, which the parity guard
// exempts for reading state and changing none.
func (p *notifyingProvider) EventsJournalCursor() (storage.EventsJournalCursor, error) {
	return NewEventsJournalCursor(p)
}

// ── Provider capabilities that are not roles ────────────────────────

// RunNonTx forwards the maintenance escape hatch. It runs on a pinned
// connection OUTSIDE any unit of work, so there is nothing to record: its
// callers are server-wide database maintenance, not bead writes.
func (p *notifyingProvider) RunNonTx(ctx context.Context, fn func(ctx context.Context, conn *sql.Conn) error) error {
	mp, ok := p.inner.(MaintenanceProvider)
	if !ok {
		return fmt.Errorf("uow: provider %T does not support non-transactional maintenance", p.inner)
	}
	return mp.RunNonTx(ctx, fn)
}

// SetPoolLimits forwards pool tuning to the wrapped provider, which owns the
// pool. A provider that is not pool-backed is left alone, as it would be
// without this wrapper.
func (p *notifyingProvider) SetPoolLimits(limits PoolLimits) {
	if tuner, ok := p.inner.(PoolTuner); ok {
		tuner.SetPoolLimits(limits)
	}
}

// SetEventsJournalEnabled forwards durable-journal activation to the provider
// that actually binds it to a transaction (doltSQLProvider.BeginTx). Activation
// is per instance, and the instance that matters is the inner one; the wrapper
// holds no journal state of its own.
//
// It fires no hook and records nothing, which is not an omission: the journal
// is written at the issueops seam inside the mutation's transaction, so every
// write this wrapper already records is journaled by the layer beneath it.
func (p *notifyingProvider) SetEventsJournalEnabled(enabled bool) {
	if configurer, ok := p.inner.(storage.EventsJournalConfigurer); ok {
		configurer.SetEventsJournalEnabled(enabled)
	}
}

// eventsMaintenanceRunner is issueops.EventsMaintenanceRunner under this file's
// import alias, named once so the forwarder below reads as an ordinary
// capability check.
type eventsMaintenanceRunner = storageissueops.EventsMaintenanceRunner

// RunEventsMaintenanceTx forwards one events-journal retention step to the
// inner provider's transaction machinery. It is maintenance on a dolt_ignored
// table — a prefix delete of records already committed — so there is nothing
// for a hook to describe and no bead a script could be handed.
//
// A provider that cannot maintain the journal fails here rather than silently
// succeeding, matching RunNonTx above: the caller logs it and carries on.
//
// The forwarder is deliberately not the only defense. Auto-prune's resolution
// peels ProviderUnwrapper before asserting this capability, so it reaches the
// inner provider even through a decorator that never declared the method. Both
// halves are cheap and the failure they prevent — retention silently not
// running behind a wrapper — is invisible from the outside.
func (p *notifyingProvider) RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, storageissueops.DBTX) error) error {
	runner, ok := p.inner.(eventsMaintenanceRunner)
	if !ok {
		return fmt.Errorf("uow: provider %T does not support events-journal maintenance", p.inner)
	}
	return runner.RunEventsMaintenanceTx(ctx, fn)
}

var (
	_ UnitOfWorkProvider        = (*notifyingProvider)(nil)
	_ MaintenanceProvider       = (*notifyingProvider)(nil)
	_ PoolTuner                 = (*notifyingProvider)(nil)
	_ IssueLifecycleSource      = (*notifyingProvider)(nil)
	_ IssueReaderSource         = (*notifyingProvider)(nil)
	_ IssueClaimerSource        = (*notifyingProvider)(nil)
	_ RelationsSource           = (*notifyingProvider)(nil)
	_ EdgeReaderSource          = (*notifyingProvider)(nil)
	_ BlockingAnnotatorSource   = (*notifyingProvider)(nil)
	_ TreeWalkerSource          = (*notifyingProvider)(nil)
	_ GraphCounterSource        = (*notifyingProvider)(nil)
	_ CounterSource             = (*notifyingProvider)(nil)
	_ ReadyCounterSource        = (*notifyingProvider)(nil)
	_ ReadyClaimerSource        = (*notifyingProvider)(nil)
	_ QuerierSource             = (*notifyingProvider)(nil)
	_ StatsReporterSource       = (*notifyingProvider)(nil)
	_ CycleDetectorSource       = (*notifyingProvider)(nil)
	_ CommenterSource           = (*notifyingProvider)(nil)
	_ BatchCloserSource         = (*notifyingProvider)(nil)
	_ BatchCreatorSource        = (*notifyingProvider)(nil)
	_ DependencyEditorSource    = (*notifyingProvider)(nil)
	_ DeleterSource             = (*notifyingProvider)(nil)
	_ SweeperSource             = (*notifyingProvider)(nil)
	_ ImporterSource            = (*notifyingProvider)(nil)
	_ BootstrapperSource        = (*notifyingProvider)(nil)
	_ InitVerifierSource        = (*notifyingProvider)(nil)
	_ WorkspaceConfigSource     = (*notifyingProvider)(nil)
	_ VersionReconcilerSource   = (*notifyingProvider)(nil)
	_ MemoriesSource            = (*notifyingProvider)(nil)
	_ EventsJournalCursorSource = (*notifyingProvider)(nil)
)

// ── The unit of work ────────────────────────────────────────────────

// notifyingUOW wraps a UnitOfWork, buffering committed mutations and firing
// hooks after commit. The embedded UnitOfWork provides passthrough for the use
// cases and methods that record nothing.
type notifyingUOW struct {
	UnitOfWork
	rec   *recorder
	sinks Sinks

	issueUC   domain.IssueUseCase
	depUC     domain.DependencyUseCase
	labelUC   domain.LabelUseCase
	commentUC domain.CommentUseCase
}

func (u *notifyingUOW) IssueUseCase() domain.IssueUseCase {
	if u.issueUC == nil {
		u.issueUC = &recordingIssueUC{IssueUseCase: u.UnitOfWork.IssueUseCase(), rec: u.rec, snap: u.snapshotter()}
	}
	return u.issueUC
}

func (u *notifyingUOW) DependencyUseCase() domain.DependencyUseCase {
	if u.depUC == nil {
		u.depUC = &recordingDepUC{DependencyUseCase: u.UnitOfWork.DependencyUseCase(), rec: u.rec, snap: u.snapshotter()}
	}
	return u.depUC
}

func (u *notifyingUOW) LabelUseCase() domain.LabelUseCase {
	if u.labelUC == nil {
		u.labelUC = &recordingLabelUC{LabelUseCase: u.UnitOfWork.LabelUseCase(), rec: u.rec, snap: u.snapshotter()}
	}
	return u.labelUC
}

func (u *notifyingUOW) CommentUseCase() domain.CommentUseCase {
	if u.commentUC == nil {
		u.commentUC = &recordingCommentUC{CommentUseCase: u.UnitOfWork.CommentUseCase(), rec: u.rec, snap: u.snapshotter()}
	}
	return u.commentUC
}

// Commit commits the underlying unit of work, then fires the buffered hooks in
// the order the mutations happened. Hooks are fire-and-forget, so the user's
// committed mutation always succeeds.
func (u *notifyingUOW) Commit(ctx context.Context, message string) error {
	if err := u.UnitOfWork.Commit(ctx, message); err != nil {
		return err
	}
	entries := u.rec.drain()
	if u.sinks.Hook == nil {
		return nil
	}
	for _, entry := range entries {
		u.sinks.Hook.Run(entry.event, entry.issue)
	}
	return nil
}

// Close rolls the unit of work back unless it committed, so whatever the
// attempt buffered is dropped rather than fired.
func (u *notifyingUOW) Close(ctx context.Context) {
	u.rec.drain()
	u.UnitOfWork.Close(ctx)
}

func (u *notifyingUOW) snapshotter() *snapshotter {
	return &snapshotter{
		issues: u.UnitOfWork.IssueUseCase(),
		labels: u.UnitOfWork.LabelUseCase(),
		deps:   u.UnitOfWork.DependencyUseCase(),
	}
}

// Unwrap returns the unit of work beneath the recording layer, satisfying
// unitOfWorkUnwrapper.
func (u *notifyingUOW) Unwrap() UnitOfWork { return u.UnitOfWork }

// ── The buffer ──────────────────────────────────────────────────────

// mutationEntry is a buffered post-commit hook notification: the resolved hook
// event and the post-mutation issue snapshot to pass to the runner.
type mutationEntry struct {
	event string
	issue *types.Issue
}

// recorder buffers hook notifications accumulated during one unit of work.
type recorder struct {
	entries []mutationEntry
}

// record resolves op to a script-hook event and buffers a notification. Ops
// with no hook and mutations with no snapshot are dropped — the latter matches
// the DoltStorage plumbing, whose re-read before firing is best-effort too.
func (r *recorder) record(op string, issue *types.Issue) {
	event, ok := hookEventForOp(op)
	if !ok || issue == nil {
		return
	}
	r.entries = append(r.entries, mutationEntry{event: event, issue: cloneIssueForHook(issue)})
}

func (r *recorder) drain() []mutationEntry { e := r.entries; r.entries = nil; return e }

// batchNotificationBuffer is this decorator seen from the BATCH COMPOSITIONS in
// this package. They need it because the close verbs are shared: a single close
// and a batch item reach the same recordingIssueUC method, and the two have
// different firing rules — the single close announces an idempotent re-close,
// the batch verbs do not (ga-2yaqp.1). Gating inside the verb would change both.
//
// So the composition marks the buffer before the item's close and rewinds to
// that mark when the close turned out to have persisted nothing. Rewinding
// rather than not-recording is what keeps the rule where it belongs: the verb
// stays honest about what it did, and the composition decides what is worth
// telling a script about.
//
// It is an interface rather than a concrete field because a UnitOfWork is only
// sometimes this decorator — with hooks disabled NewNotifyingProvider hands back
// the inner provider unwrapped, and then there is no buffer and nothing to do.
type batchNotificationBuffer interface {
	markNotifications() int
	rewindNotifications(mark int)
}

func (u *notifyingUOW) markNotifications() int { return len(u.rec.entries) }

func (u *notifyingUOW) rewindNotifications(mark int) {
	if mark >= 0 && mark <= len(u.rec.entries) {
		u.rec.entries = u.rec.entries[:mark]
	}
}

// markBatchNotifications returns a rewind token for a batch item's close, or -1
// when this unit of work buffers nothing.
func markBatchNotifications(uw UnitOfWork) int {
	if buf, ok := uw.(batchNotificationBuffer); ok {
		return buf.markNotifications()
	}
	return -1
}

// rewindBatchNotifications drops whatever a batch item's close buffered. The
// caller has established the item changed nothing, which is the only thing that
// makes discarding a recorded mutation safe: nothing observes the buffer before
// Commit drains it.
func rewindBatchNotifications(uw UnitOfWork, mark int) {
	if mark < 0 {
		return
	}
	if buf, ok := uw.(batchNotificationBuffer); ok {
		buf.rewindNotifications(mark)
	}
}

// The recorder's op vocabulary. These name what happened; hookEventForOp maps
// them to the three events internal/hooks publishes.
const (
	opCreate    = "create"
	opUpdate    = "update"
	opClose     = "close"
	opDepAdd    = "dep_add"
	opDepRemove = "dep_remove"
)

// hookEventForOp maps a mutation to its script-hook event, matching the
// DoltStorage plumbing verb for verb: creates fire on_create, closes fire
// on_close, and everything else that changes a bead — field updates, reopens,
// claims, labels, comments, dependency edits — fires on_update, which is what
// HookFiringStore fires for each of them. Deletes are absent on purpose: they
// fire no script hook there either (hook_deleter.go), and the vocabulary has no
// name for a deletion.
func hookEventForOp(op string) (string, bool) {
	switch op {
	case opCreate:
		return hooks.EventCreate, true
	case opUpdate, opDepAdd, opDepRemove:
		return hooks.EventUpdate, true
	case opClose:
		return hooks.EventClose, true
	default:
		return "", false
	}
}

// cloneIssueForHook deep-copies a snapshot before it is buffered. The runner
// marshals the issue on its own goroutine while the caller goes on to mutate
// the result it was handed — `bd update` and `bd reopen` both strip fields off
// theirs — so the buffer must not share it. The DoltStorage decorator clones
// for the same reason.
func cloneIssueForHook(issue *types.Issue) *types.Issue {
	if issue == nil {
		return nil
	}
	return storageissueops.CloneCreateRequest(publicops.CreateRequest{Issue: issue}).Issue
}

// ── Snapshots ───────────────────────────────────────────────────────

// snapshotter reads post-mutation state inside the transaction, so a buffered
// notification carries the issue as the mutation left it. Every read is
// best-effort: a failure drops the notification rather than the mutation.
//
// EVERY buffered snapshot is read here rather than taken off the verb's own
// result, and that is what makes the payload uniform. A hook script on the
// DoltStorage plumbing is handed a hydrated issue — the row with its LABELS,
// and for an edge change its dependency records too (fireHookByID and
// fireDependencyHookByID both re-read) — so hydrating in one place is the only
// way the two plumbings hand a script the same thing. A verb result carries
// whatever that verb happened to build.
type snapshotter struct {
	issues domain.IssueUseCase
	labels domain.LabelUseCase
	deps   domain.DependencyUseCase
}

// issue reads the issue plane, wisp the wisp plane. Which one a mutation
// touched is never in doubt at the call sites below — the use cases spell the
// plane in the method name.
func (s *snapshotter) issue(ctx context.Context, id string) *types.Issue {
	issue, err := s.issues.GetIssue(ctx, id)
	if err != nil {
		return nil
	}
	return withLabels(issue, func() ([]string, error) { return s.labels.GetLabels(ctx, id) })
}

func (s *snapshotter) wisp(ctx context.Context, id string) *types.Issue {
	issue, err := s.issues.GetWisp(ctx, id)
	if err != nil {
		return nil
	}
	return withLabels(issue, func() ([]string, error) { return s.labels.GetWispLabels(ctx, id) })
}

// withLabels attaches the read labels to issue, leaving the row alone when the
// label read fails: a partial snapshot still beats no notification.
func withLabels(issue *types.Issue, read func() ([]string, error)) *types.Issue {
	if issue == nil {
		return nil
	}
	labels, err := read()
	if err != nil {
		return issue
	}
	issue.Labels = labels
	return issue
}

// anyPlane resolves an id whose plane the mutation did not pin, the way the
// package's own operationIssue does: wisps first, then issues.
//
// The DoltStorage plumbing probes the other way round, and the order cannot
// matter: an id names a row on ONE plane. Both planes mint from the same
// per-prefix counter (issue_counter, via NextCounterID), and a promotion MOVES
// the row rather than copying it, so the two tables never hold the same id. If
// they ever could, the probe order would be a coin toss on both plumbings
// rather than a difference between them.
func (s *snapshotter) anyPlane(ctx context.Context, id string) *types.Issue {
	if issue := s.wisp(ctx, id); issue != nil {
		return issue
	}
	return s.issue(ctx, id)
}

// The *WithEdges snapshots are what a dependency mutation records: the issue
// plus its dependency records, so a hook script sees the graph the edit
// produced rather than the row alone. This is what fireDependencyHookByID hands
// a script on the DoltStorage plumbing.
func (s *snapshotter) issueWithEdges(ctx context.Context, id string) *types.Issue {
	return withEdges(s.issue(ctx, id), id, func() (map[string][]*types.Dependency, error) {
		return s.deps.GetIssueDependencyRecords(ctx, []string{id})
	})
}

func (s *snapshotter) wispWithEdges(ctx context.Context, id string) *types.Issue {
	return withEdges(s.wisp(ctx, id), id, func() (map[string][]*types.Dependency, error) {
		return s.deps.GetWispDependencyRecords(ctx, []string{id})
	})
}

func (s *snapshotter) anyPlaneWithEdges(ctx context.Context, id string) *types.Issue {
	if issue := s.wispWithEdges(ctx, id); issue != nil {
		return issue
	}
	return s.issueWithEdges(ctx, id)
}

// withEdges attaches the read records to issue, leaving the row alone when the
// edge read fails: a partial snapshot still beats no notification.
func withEdges(issue *types.Issue, id string, read func() (map[string][]*types.Dependency, error)) *types.Issue {
	if issue == nil {
		return nil
	}
	records, err := read()
	if err != nil {
		return issue
	}
	issue.Dependencies = records[id]
	return issue
}

// ── Issue use case ──────────────────────────────────────────────────

type recordingIssueUC struct {
	domain.IssueUseCase
	rec  *recorder
	snap *snapshotter
}

func (u *recordingIssueUC) CreateIssue(ctx context.Context, params domain.CreateIssueParams, actor string) (domain.CreateIssueResult, error) {
	res, err := u.IssueUseCase.CreateIssue(ctx, params, actor)
	if err == nil && res.Issue != nil {
		u.created(ctx, res.Issue.ID, params, u.snap.issue)
	}
	return res, err
}

func (u *recordingIssueUC) CreateIssues(ctx context.Context, params []domain.CreateIssueParams, actor string) (domain.CreateIssuesResult, error) {
	res, err := u.IssueUseCase.CreateIssues(ctx, params, actor)
	if err == nil {
		for i, issue := range res.Issues {
			if issue == nil || i >= len(params) {
				continue
			}
			u.created(ctx, issue.ID, params[i], u.snap.issue)
		}
	}
	return res, err
}

func (u *recordingIssueUC) CreateWisp(ctx context.Context, params domain.CreateIssueParams, actor string) (domain.CreateIssueResult, error) {
	res, err := u.IssueUseCase.CreateWisp(ctx, params, actor)
	if err == nil && res.Issue != nil {
		u.created(ctx, res.Issue.ID, params, u.snap.wisp)
	}
	return res, err
}

func (u *recordingIssueUC) CreateWisps(ctx context.Context, params []domain.CreateIssueParams, actor string) (domain.CreateIssuesResult, error) {
	res, err := u.IssueUseCase.CreateWisps(ctx, params, actor)
	if err == nil {
		for i, issue := range res.Issues {
			if issue == nil || i >= len(params) {
				continue
			}
			u.created(ctx, issue.ID, params[i], u.snap.wisp)
		}
	}
	return res, err
}

// created records what one create wrote: the create itself, then an update for
// EVERY issue whose graph the create edited — the created row included.
//
// The far ends are not incidental. A create names its edges, and a REVERSE edge
// (`bd create --blocks X`) leaves the EXISTING issue rather than the new one, so
// the create mutated X's graph and X's watchers have to hear about it. The
// DoltStorage plumbing fires that update from the request's edge list
// (storage.CreatePublicCreateDependencies feeding dependencyHookEvents), which
// is where the source/target swap is decided; this reads the same three inputs
// off the params — ParentID, WaitsFor, Dependencies — because a reverse edge
// never appears in the created issue's own records.
//
// THE CREATED ROW IS A SOURCE LIKE ANY OTHER. A forward edge (`bd create
// --parent X`) leaves the new issue, and the DoltStorage plumbing fires an
// on_update for it carrying its dependency records — its on_create does not,
// which is the whole reason the update follows. Skipping it here on the grounds
// that "the create already said so" would drop the only event that carries the
// new issue's graph.
//
// ONE event per distinct source, in first-edit order, rather than the
// plumbing's one-per-edge: two edges leaving the same issue are one change to
// it, which is the rule the dependency-editor role already applies
// (hook_dependency_editor.go). See the multiplicity note in the file header.
//
// Every source is resolved across BOTH PLANES, including the created row's own.
// A create's edges may name a row on the other plane — an issue that blocks a
// wisp — and a plane-pinned read of the far end would miss it and silently drop
// the event.
func (u *recordingIssueUC) created(
	ctx context.Context,
	createdID string,
	params domain.CreateIssueParams,
	plane func(context.Context, string) *types.Issue,
) {
	u.rec.record(opCreate, plane(ctx, createdID))
	for _, source := range createEdgeSources(createdID, params) {
		u.rec.record(opDepAdd, u.snap.anyPlaneWithEdges(ctx, source))
	}
}

// createEdgeSources returns the distinct issue ids a create's edges LEAVE, in
// the order the edges are written, resolving the reverse-edge swap exactly as
// storage.CreatePublicCreateDependencies does.
func createEdgeSources(createdID string, params domain.CreateIssueParams) []string {
	var sources []string
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		sources = append(sources, id)
	}
	if params.ParentID != "" {
		add(createdID)
	}
	if params.WaitsFor != nil {
		add(createdID)
	}
	for _, dependency := range params.Dependencies {
		if dependency.SwapDirection {
			add(dependency.TargetID)
			continue
		}
		add(createdID)
	}
	return sources
}

// ApplyIssueGraph and ApplyWispGraph report a whole plan the way one create
// reports itself: a create per node, then an update per distinct issue whose
// GRAPH the plan wrote.
//
// The edges matter here more than anywhere else. applyGraph writes them through
// depRepo.Insert — BENEATH the use-case seam this recorder wraps — so nothing
// about them reaches the buffer on its own, and a plan is mostly edges. Worse,
// an explicit edge may leave an issue the plan did not create (GraphEdge.FromID
// names a live row), so the issue whose graph changed can be one no create event
// mentions at all.
func (u *recordingIssueUC) ApplyIssueGraph(ctx context.Context, plan domain.GraphPlan, actor string) (domain.GraphApplyResult, error) {
	res, err := u.IssueUseCase.ApplyIssueGraph(ctx, plan, actor)
	if err == nil {
		u.appliedGraph(ctx, plan, res, u.snap.issue)
	}
	return res, err
}

func (u *recordingIssueUC) ApplyWispGraph(ctx context.Context, plan domain.GraphPlan, actor string) (domain.GraphApplyResult, error) {
	res, err := u.IssueUseCase.ApplyWispGraph(ctx, plan, actor)
	if err == nil {
		u.appliedGraph(ctx, plan, res, u.snap.wisp)
	}
	return res, err
}

// appliedGraph records the plan: every created node, then one edge-carrying
// update per distinct source, resolved across both planes. It is created()'s
// rule at plan scale, including the part that reads oddly — a node that is also
// an edge source gets BOTH its create and its update, because the create event
// carries no dependency records and the update is the only one that does.
func (u *recordingIssueUC) appliedGraph(
	ctx context.Context,
	plan domain.GraphPlan,
	res domain.GraphApplyResult,
	plane func(context.Context, string) *types.Issue,
) {
	for _, id := range graphIDs(plan, res) {
		u.rec.record(opCreate, plane(ctx, id))
	}
	for _, source := range graphEdgeSources(plan, res) {
		u.rec.record(opDepAdd, u.snap.anyPlaneWithEdges(ctx, source))
	}
}

// graphIDs returns the created ids in the plan's node order. The result maps
// plan keys to minted ids, and a map ranges in no order — hook scripts see the
// batch in the order it was written, as they do for a batch create.
func graphIDs(plan domain.GraphPlan, res domain.GraphApplyResult) []string {
	ids := make([]string, 0, len(res.IDs))
	for _, node := range plan.Nodes {
		if id, ok := res.IDs[node.Key]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// graphEdgeSources returns the distinct issue ids the plan's edges LEAVE, in
// the order applyGraph names them: node parent links, then explicit edges, then
// per-node inline deps. It reads the same three inputs applyGraph writes from
// and resolves plan-local keys through the same minted-id map, so a key that
// named a node and an id that named a live row both land on the row the insert
// touched.
//
// None of the three swaps direction — a node's parent link leaves the CHILD, an
// explicit edge leaves resolveEdgeRef(FromKey, FromID), and an inline dep leaves
// its own node (types.NewGraphNodeDependency) — so the source is always the id
// on the left.
func graphEdgeSources(plan domain.GraphPlan, res domain.GraphApplyResult) []string {
	resolve := func(key, id string) string {
		if id != "" {
			return id
		}
		return res.IDs[key]
	}

	var sources []string
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		sources = append(sources, id)
	}

	for _, node := range plan.Nodes {
		if node.ParentKey == "" && node.ParentID == "" {
			continue
		}
		if resolve(node.ParentKey, node.ParentID) == "" {
			continue
		}
		add(res.IDs[node.Key])
	}
	for _, edge := range plan.Edges {
		add(resolve(edge.FromKey, edge.FromID))
	}
	for _, node := range plan.Nodes {
		if len(node.Deps) > 0 {
			add(res.IDs[node.Key])
		}
	}
	return sources
}

func (u *recordingIssueUC) UpdateIssue(ctx context.Context, id string, updates map[string]any, actor string) error {
	if err := u.IssueUseCase.UpdateIssue(ctx, id, updates, actor); err != nil {
		return err
	}
	u.rec.record(opUpdate, u.snap.issue(ctx, id))
	return nil
}

// CompareAndSetMetadataKey records an update for a swap that MOVED the value,
// and nothing for one that did not — the same line hookMetadataCAS draws on the
// DoltStorage chain, which fires on_update only when the row changed.
//
// It reads the fact rather than inferring it. The use case reports whether a
// row write landed, which is strictly better than the decorator's comparison of
// the request's two ends, and it is the only caller of this method that has it.
//
// THE SNAPSHOT IS anyPlane, because this role resolves the id across both
// planes itself: a swap on a wisp is an update to a wisp, and reading only the
// issues table would record a nil for it.
func (u *recordingIssueUC) CompareAndSetMetadataKey(ctx context.Context, plan storage.CompareAndSetKeyPlan) (publicops.CompareAndSetKeyResult, bool, error) {
	result, wrote, err := u.IssueUseCase.CompareAndSetMetadataKey(ctx, plan)
	if err != nil || !wrote {
		return result, wrote, err
	}
	u.rec.record(opUpdate, u.snap.anyPlane(ctx, plan.IssueID))
	return result, wrote, nil
}

// ReleaseIssue records an update for a release that landed.
//
// It is DECLARED rather than inherited, which is the whole reason this method
// exists: an accessor promoted onto an embedder compiles perfectly and records
// nothing, so a release through a notifying unit of work would silently lose
// the hook the DoltStorage chain fires for the same write.
//
// It reads the ROLE'S OWN verdict — ReleaseResult.Changed — which is the same
// fact the bool beside it carries and the one a notification is about. An
// ephemeral release changes a wisp and versions nothing, and it is still an
// update somebody asked to be notified about.
//
// THE SNAPSHOT IS anyPlane, because this role resolves the id across both
// planes itself: releasing a wisp is an update to a wisp, and reading only the
// issues table would record a nil for it.
func (u *recordingIssueUC) ReleaseIssue(ctx context.Context, req publicops.ReleaseRequest) (publicops.ReleaseResult, bool, error) {
	result, wrote, err := u.IssueUseCase.ReleaseIssue(ctx, req)
	if err != nil || !result.Changed {
		return result, wrote, err
	}
	u.rec.record(opUpdate, u.snap.anyPlane(ctx, req.IssueID))
	return result, wrote, nil
}

func (u *recordingIssueUC) UpdateWisp(ctx context.Context, id string, updates map[string]any, actor string) error {
	if err := u.IssueUseCase.UpdateWisp(ctx, id, updates, actor); err != nil {
		return err
	}
	u.rec.record(opUpdate, u.snap.wisp(ctx, id))
	return nil
}

// ApplyUpdate is the guarded update every public update runs through, and it
// serves both planes, so the fallback snapshot resolves the id rather than
// pinning a table.
//
// A spec that only states EXPECTATIONS writes nothing — it is the
// compare-and-set precondition `bd close --expect-version` and `bd reopen`
// spell as a separate call before the operation they guard (issue_operations.go)
// — so it records nothing. The DoltStorage plumbing passes that precondition
// INTO the close and fires the close hook alone; recording here would put an
// extra on_update in front of every guarded close.
func (u *recordingIssueUC) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	issue, err := u.IssueUseCase.ApplyUpdate(ctx, id, spec, actor)
	if err != nil || !specWrites(spec) {
		return issue, err
	}
	u.rec.record(opUpdate, u.snap.anyPlane(ctx, id))
	return issue, nil
}

// specWrites reports whether spec asks ApplyUpdate to change anything. It
// clears the three expectation fields and asks whether ANYTHING is left, rather
// than listing the writing fields: a field added to UpdateSpec then counts as a
// write until someone says otherwise, which is the safe direction for a hook.
func specWrites(spec domain.UpdateSpec) bool {
	spec.ExpectedVersion, spec.ExpectedAssignee, spec.ExpectedStatus = nil, nil, nil
	if len(spec.Fields) == 0 {
		spec.Fields = nil
	}
	if len(spec.AddLabels) == 0 {
		spec.AddLabels = nil
	}
	if len(spec.RemoveLabels) == 0 {
		spec.RemoveLabels = nil
	}
	return !reflect.DeepEqual(spec, domain.UpdateSpec{})
}

func (u *recordingIssueUC) ClaimIssue(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	res, err := u.IssueUseCase.ClaimIssue(ctx, id, actor)
	if err == nil && !res.AlreadyClaimed {
		u.rec.record(opUpdate, u.snap.issue(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) ClaimIssueIfOpen(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	res, err := u.IssueUseCase.ClaimIssueIfOpen(ctx, id, actor)
	if err == nil && !res.AlreadyClaimed {
		u.rec.record(opUpdate, u.snap.issue(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) ClaimWisp(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	res, err := u.IssueUseCase.ClaimWisp(ctx, id, actor)
	if err == nil && !res.AlreadyClaimed {
		u.rec.record(opUpdate, u.snap.wisp(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) ClaimWispIfOpen(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	res, err := u.IssueUseCase.ClaimWispIfOpen(ctx, id, actor)
	if err == nil && !res.AlreadyClaimed {
		u.rec.record(opUpdate, u.snap.wisp(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (domain.ClaimReadyResult, error) {
	res, err := u.IssueUseCase.ClaimReadyIssue(ctx, filter, actor)
	if err == nil && res.Claimed && res.Issue != nil {
		u.rec.record(opUpdate, u.snap.issue(ctx, res.Issue.ID))
	}
	return res, err
}

func (u *recordingIssueUC) ClaimReadyWisp(ctx context.Context, filter types.WorkFilter, actor string) (domain.ClaimReadyResult, error) {
	res, err := u.IssueUseCase.ClaimReadyWisp(ctx, filter, actor)
	if err == nil && res.Claimed && res.Issue != nil {
		u.rec.record(opUpdate, u.snap.wisp(ctx, res.Issue.ID))
	}
	return res, err
}

// CloseIssue and its three siblings record on SUCCESS, not on "something
// changed": the DoltStorage plumbing fires on_close for an idempotent re-close
// too — HookFiringStore.CloseIssueChecked says so in as many words. A close
// that found the issue already closed still answers "it is closed", and a
// script that reconciles on that answer must not be told only sometimes.
//
// THE BATCH COMPOSITIONS OVERRIDE THAT, and they do it above these verbs rather
// than inside them, because these are the SAME methods a single close reaches.
// closeBatchItem and uowApplyRun.applyClose rewind the recorded notification
// when the item persisted nothing, matching hookBatchCloser and hookBatchApplier
// on the other plumbing (ga-2yaqp.1) — otherwise a teardown replayed against an
// already-closed convoy runs on_close once per item on every pass. Gate here
// and the single close would lose its re-close firing with it.
//
// The snapshot read is PLANE-PINNED to the verb that was called, while the verb
// itself tolerates an id from either plane. That gap is unreachable as shipped:
// every caller resolves the plane first (issue_operations.go and batch_closer.go
// both route through workapi.GetIssueOrWisp or operationIssue and then call the
// matching verb), so a wisp id never arrives at the issues-plane verb. Were one
// to, the read would miss and the notification would be dropped rather than
// mis-addressed — which is why this stays pinned instead of falling back to the
// verb's own result: a result-shaped fallback would paper over the routing bug
// that produced it. The reopen pair below reads the same way.
func (u *recordingIssueUC) CloseIssue(ctx context.Context, id string, params domain.CloseIssueParams, actor string) (domain.CloseIssueResult, error) {
	res, err := u.IssueUseCase.CloseIssue(ctx, id, params, actor)
	if err == nil {
		u.rec.record(opClose, u.snap.issue(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) CloseIssueChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	res, err := u.IssueUseCase.CloseIssueChecked(ctx, id, params, actor, force)
	if err == nil {
		u.rec.record(opClose, u.snap.issue(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) CloseWisp(ctx context.Context, id string, params domain.CloseIssueParams, actor string) (domain.CloseIssueResult, error) {
	res, err := u.IssueUseCase.CloseWisp(ctx, id, params, actor)
	if err == nil {
		u.rec.record(opClose, u.snap.wisp(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) CloseWispChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	res, err := u.IssueUseCase.CloseWispChecked(ctx, id, params, actor, force)
	if err == nil {
		u.rec.record(opClose, u.snap.wisp(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) ReopenIssue(ctx context.Context, id string, params domain.ReopenIssueParams, actor string) (domain.ReopenIssueResult, error) {
	res, err := u.IssueUseCase.ReopenIssue(ctx, id, params, actor)
	if err == nil && res.Reopened {
		u.rec.record(opUpdate, u.snap.issue(ctx, id))
	}
	return res, err
}

func (u *recordingIssueUC) ReopenWisp(ctx context.Context, id string, params domain.ReopenIssueParams, actor string) (domain.ReopenIssueResult, error) {
	res, err := u.IssueUseCase.ReopenWisp(ctx, id, params, actor)
	if err == nil && res.Reopened {
		u.rec.record(opUpdate, u.snap.wisp(ctx, id))
	}
	return res, err
}

// ── Dependency use case ─────────────────────────────────────────────

type recordingDepUC struct {
	domain.DependencyUseCase
	rec  *recorder
	snap *snapshotter
}

func (u *recordingDepUC) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if err := u.DependencyUseCase.AddDependency(ctx, dep, actor); err != nil {
		return err
	}
	u.rec.record(opDepAdd, u.snap.issueWithEdges(ctx, dep.IssueID))
	return nil
}

func (u *recordingDepUC) AddWispDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if err := u.DependencyUseCase.AddWispDependency(ctx, dep, actor); err != nil {
		return err
	}
	u.rec.record(opDepAdd, u.snap.wispWithEdges(ctx, dep.IssueID))
	return nil
}

// AddDependencies fires once per DISTINCT SOURCE ISSUE the batch edited, in
// first-edit order — the multiplicity hookDependencyEditor produces on the
// other plumbing. Edges are routed per source, so the snapshot resolves each
// source's plane rather than assuming one.
func (u *recordingDepUC) AddDependencies(ctx context.Context, deps []*types.Dependency, actor string, opts domain.BulkAddDepsOpts) (domain.BulkAddDepsResult, error) {
	res, err := u.DependencyUseCase.AddDependencies(ctx, deps, actor, opts)
	if err != nil {
		return res, err
	}
	seen := make(map[string]bool, len(res.Added))
	for _, dep := range res.Added {
		if dep == nil || dep.IssueID == "" || seen[dep.IssueID] {
			continue
		}
		seen[dep.IssueID] = true
		u.rec.record(opDepAdd, u.snap.anyPlaneWithEdges(ctx, dep.IssueID))
	}
	return res, nil
}

func (u *recordingDepUC) RemoveDependency(ctx context.Context, issueID, dependsOnID, actor string) error {
	if err := u.DependencyUseCase.RemoveDependency(ctx, issueID, dependsOnID, actor); err != nil {
		return err
	}
	u.rec.record(opDepRemove, u.snap.issueWithEdges(ctx, issueID))
	return nil
}

func (u *recordingDepUC) RemoveWispDependency(ctx context.Context, wispID, dependsOnID, actor string) error {
	if err := u.DependencyUseCase.RemoveWispDependency(ctx, wispID, dependsOnID, actor); err != nil {
		return err
	}
	u.rec.record(opDepRemove, u.snap.wispWithEdges(ctx, wispID))
	return nil
}

// RemoveDependencyBySource reads the source's plane itself and reports whether
// an edge was actually removed, so nothing is recorded when nothing changed.
func (u *recordingDepUC) RemoveDependencyBySource(ctx context.Context, sourceID, dependsOnID, actor string) (bool, error) {
	removed, err := u.DependencyUseCase.RemoveDependencyBySource(ctx, sourceID, dependsOnID, actor)
	if err != nil || !removed {
		return removed, err
	}
	u.rec.record(opDepRemove, u.snap.anyPlaneWithEdges(ctx, sourceID))
	return removed, nil
}

func (u *recordingDepUC) Reparent(ctx context.Context, childID, newParentID, actor string) error {
	if err := u.DependencyUseCase.Reparent(ctx, childID, newParentID, actor); err != nil {
		return err
	}
	u.rec.record(opUpdate, u.snap.issueWithEdges(ctx, childID))
	return nil
}

func (u *recordingDepUC) ReparentWisp(ctx context.Context, childWispID, newParentID, actor string) error {
	if err := u.DependencyUseCase.ReparentWisp(ctx, childWispID, newParentID, actor); err != nil {
		return err
	}
	u.rec.record(opUpdate, u.snap.wispWithEdges(ctx, childWispID))
	return nil
}

// ── Label use case ──────────────────────────────────────────────────

type recordingLabelUC struct {
	domain.LabelUseCase
	rec  *recorder
	snap *snapshotter
}

// labeled records the update a label write is, from the issue as the write
// left it. One event per call, not per label: a label write is one change to
// one issue.
func (u *recordingLabelUC) labeled(ctx context.Context, issueID string) {
	u.rec.record(opUpdate, u.snap.issue(ctx, issueID))
}

func (u *recordingLabelUC) wispLabeled(ctx context.Context, wispID string) {
	u.rec.record(opUpdate, u.snap.wisp(ctx, wispID))
}

func (u *recordingLabelUC) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if err := u.LabelUseCase.AddLabel(ctx, issueID, label, actor); err != nil {
		return err
	}
	u.labeled(ctx, issueID)
	return nil
}

func (u *recordingLabelUC) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if err := u.LabelUseCase.RemoveLabel(ctx, issueID, label, actor); err != nil {
		return err
	}
	u.labeled(ctx, issueID)
	return nil
}

func (u *recordingLabelUC) AddLabels(ctx context.Context, issueID string, labels []string, actor string) error {
	if err := u.LabelUseCase.AddLabels(ctx, issueID, labels, actor); err != nil {
		return err
	}
	u.labeled(ctx, issueID)
	return nil
}

func (u *recordingLabelUC) RemoveLabels(ctx context.Context, issueID string, labels []string, actor string) error {
	if err := u.LabelUseCase.RemoveLabels(ctx, issueID, labels, actor); err != nil {
		return err
	}
	u.labeled(ctx, issueID)
	return nil
}

func (u *recordingLabelUC) SetLabels(ctx context.Context, issueID string, labels []string, actor string) error {
	if err := u.LabelUseCase.SetLabels(ctx, issueID, labels, actor); err != nil {
		return err
	}
	u.labeled(ctx, issueID)
	return nil
}

// InheritFromParent records only when it actually wrote a label; a child that
// inherits nothing is not an update.
func (u *recordingLabelUC) InheritFromParent(ctx context.Context, childID, parentID, actor string, skipExisting []string) ([]string, error) {
	inherited, err := u.LabelUseCase.InheritFromParent(ctx, childID, parentID, actor, skipExisting)
	if err == nil && len(inherited) > 0 {
		u.labeled(ctx, childID)
	}
	return inherited, err
}

func (u *recordingLabelUC) AddWispLabel(ctx context.Context, wispID, label, actor string) error {
	if err := u.LabelUseCase.AddWispLabel(ctx, wispID, label, actor); err != nil {
		return err
	}
	u.wispLabeled(ctx, wispID)
	return nil
}

func (u *recordingLabelUC) RemoveWispLabel(ctx context.Context, wispID, label, actor string) error {
	if err := u.LabelUseCase.RemoveWispLabel(ctx, wispID, label, actor); err != nil {
		return err
	}
	u.wispLabeled(ctx, wispID)
	return nil
}

func (u *recordingLabelUC) AddWispLabels(ctx context.Context, wispID string, labels []string, actor string) error {
	if err := u.LabelUseCase.AddWispLabels(ctx, wispID, labels, actor); err != nil {
		return err
	}
	u.wispLabeled(ctx, wispID)
	return nil
}

func (u *recordingLabelUC) RemoveWispLabels(ctx context.Context, wispID string, labels []string, actor string) error {
	if err := u.LabelUseCase.RemoveWispLabels(ctx, wispID, labels, actor); err != nil {
		return err
	}
	u.wispLabeled(ctx, wispID)
	return nil
}

func (u *recordingLabelUC) SetWispLabels(ctx context.Context, wispID string, labels []string, actor string) error {
	if err := u.LabelUseCase.SetWispLabels(ctx, wispID, labels, actor); err != nil {
		return err
	}
	u.wispLabeled(ctx, wispID)
	return nil
}

func (u *recordingLabelUC) InheritFromWispParent(ctx context.Context, childWispID, parentWispID, actor string, skipExisting []string) ([]string, error) {
	inherited, err := u.LabelUseCase.InheritFromWispParent(ctx, childWispID, parentWispID, actor, skipExisting)
	if err == nil && len(inherited) > 0 {
		u.wispLabeled(ctx, childWispID)
	}
	return inherited, err
}

// ── Comment use case ────────────────────────────────────────────────

type recordingCommentUC struct {
	domain.CommentUseCase
	rec  *recorder
	snap *snapshotter
}

// AddCommentToIssue records an update, which is what a comment fires on the
// DoltStorage plumbing: there is no on_comment event, and a comment is a change
// to the issue as far as a script is concerned.
func (u *recordingCommentUC) AddCommentToIssue(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	comment, err := u.CommentUseCase.AddCommentToIssue(ctx, issueID, author, text)
	if err == nil {
		u.rec.record(opUpdate, u.snap.issue(ctx, issueID))
	}
	return comment, err
}

func (u *recordingCommentUC) AddCommentToWisp(ctx context.Context, wispID, author, text string) (*types.Comment, error) {
	comment, err := u.CommentUseCase.AddCommentToWisp(ctx, wispID, author, text)
	if err == nil {
		u.rec.record(opUpdate, u.snap.wisp(ctx, wispID))
	}
	return comment, err
}
