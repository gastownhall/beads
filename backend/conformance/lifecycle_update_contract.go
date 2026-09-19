package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract for publicops.Lifecycle.Update as the
// ROLE reached through storage.Storage.IssueLifecycle() — the accessor-only
// half of a verb whose owning cases all live behind a staging fixture today.
//
// The suite already covers Update at the persistence seam
// (issue_operations_contract.go), and close/reopen already have their
// accessor-adoptable half (lifecycle_close_reopen_contract.go). This file
// exists because the Update cases are unreachable for a backend that cannot
// supply the staging fixture, which is a gap the suite's own headers concede:
// issue_claimer_contract.go:33-37 says outright that "the Update-seam cases run
// only behind IssueOperationsStagingFixture, which demands raw-SQL hooks a
// partial or remote backend cannot supply", and fixed that for Claimer alone.
//
// The reachability problem has two halves, and the raw hooks are only one:
//
//   - RAW HOOKS. Every runner in issue_operations_contract.go takes
//     IssueOperationsStagingFixture (issue_operations_staging.go:14-29), whose
//     Exec/QueryScalar/UpdateRaw/Commit fields are a version-control-aware SQL
//     seam. Nothing below needs one: publicops.UpdateResult and the fixture's
//     GetIssue between them expose every fact these cases assert, including the
//     RowVersion token that says whether a refusal wrote anything.
//   - THE FIXTURE'S SINGLE ROLE FIELD. That fixture's Operations is both the
//     seed route and the subject: several Update cases reach their starting
//     state through fixture.Operations.Create. A backend whose composition
//     refuses Create can therefore never wire them even with raw hooks
//     available. LifecycleCloseReopenFixture and ClaimerFixture already
//     separate the two, and this fixture follows them.
//
// SCOPE: every UpdateRequest and IssuePatch member a backend can express
// without a private extension. That used to stop at the plain content members —
// title, description, design, acceptance criteria, notes, append_notes,
// priority, issue type, the label set, the four nullable members, the actor and
// the id — and the hard half of the verb stayed behind the staging fixture,
// where no partial backend could reach it at all. It no longer does: Metadata,
// Status and close policy, Assignee and the transfer fence, Claim, ParentID,
// Persistence, the Expected* preconditions and Provenance all have their owning
// case HERE now, moved out of issue_operations_contract.go rather than copied.
//
// WHAT MOVING THEM COST, and why it cost nothing in assertion strength. The
// staging cases read their post-state with `SELECT ... FROM issues`; these read
// it off the row GetIssue answers, which carries every column they asserted on —
// the assignee, the status, the storage class, the metadata document, the close
// columns, the RowVersion token that says whether a refusal wrote anything, and
// the labels. Two observations a stored row does not carry got a hook of their
// own rather than a weakened assertion: ListEvents, for the "the refusal wrote
// no event" clause every guard case ends on, and ListDependencies, for the
// parent SET a reparent leaves behind. Both are documented below as the
// out-of-band hooks they are, and both are things an http-client leg answers
// from a getIssue-shaped read.
//
// WHAT STAYED BEHIND, named rather than left to be discovered: the cases whose
// subject is a raw funnel or a per-plane row count. See the header note on
// issue_operations_contract.go for the list and the reason each one carries.
//
// DELIBERATE OVERLAP, named rather than hidden. The plane runner re-pins what
// RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps asserts. The value is
// reachability, not a second opinion: that case seeds its wisp through
// Operations.Create and reads its post-state with QueryScalar, so it is exactly
// the shape a partial backend cannot run. Everything else here — patch
// persistence and the hydrated result, Changed:false on a same-value patch,
// append_notes against a notes replacement, the nullable clears, label replace
// at the role seam, the creation stamp no patch can name, the unknown-id
// refusal for a plain Update, the actorless refusal, and a refused patch
// writing no member — has no owning proof anywhere in the package.
//
// HOW MANY VOTES THE THREE LEGS ARE: two. The server-backed and embedded stores
// share one validate/execute body (internal/storage/issueops/execution.go
// ExecuteUpdate), so they are ONE reading of every rule below; the unit-of-work
// backend reaches the same row bodies through domain/db and maps the patch
// itself (internal/storage/uow/issue_operations.go), which is the second. The
// same-value and clear cases are where that split has teeth: Changed comes out
// of the row-write facts on one side and out of a post-state comparison on the
// other, so "a patch that restated the current values wrote nothing" is a
// genuine two-implementation question rather than one body asserted twice.

// LifecycleUpdateFixture supplies adapter-specific storage access for the
// update-role assertions. Every field but GetIssue is named and typed exactly
// like the per-backend roleFixtureKit hook it is filled from, so a wiring is
// kit plus accessor plus prefix with no adapter in between.
type LifecycleUpdateFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Lifecycle is the role under test, reached through the backend's
	// capability accessor rather than a constructor.
	Lifecycle publicops.Lifecycle
	// CreateIssue seeds a durable issue in the issues plane, including its
	// labels. It is a SEPARATE hook from the subject on purpose: a backend
	// whose Lifecycle refuses Create still runs every case below.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. A nil CreateWisp
	// means "this backend has no wisp plane to resolve against", and the plane
	// runner SKIPS loudly with that reason rather than passing quietly.
	CreateWisp func(context.Context, *types.Issue, string) error
	// GetIssue reads a row back. It is the only read these cases need: the
	// issue it answers carries the patched columns, the label set, and the
	// RowVersion token that tells a refusal apart from a silent write.
	//
	// It is this contract's OUT-OF-BAND hook, built at each wiring site over a
	// seam the backend already publishes, the way CycleDetectorFixture.Exec and
	// LifecycleCloseReopenFixture.Exec are. The frozen role fixture kit reads
	// through QueryScalar, and reading these cases' post-state with raw SQL
	// would reintroduce exactly the dependency the block exists to remove.
	GetIssue func(context.Context, string) (*types.Issue, error)
	// AddDependency seeds ONE edge, so the hydration case can assert the
	// dependency records UpdateResult promises. Nil means the backend cannot
	// seed one: that case then drops the dependency half and keeps its labels
	// half, the way ClaimerFixture.CountHistory drops its history-delta checks.
	//
	// It is also how the graph-shaped cases below reach their starting state
	// without borrowing Lifecycle.Create: an open child edge for the close
	// policy, a live blocker for the same, and the parents a reparent replaces.
	AddDependency func(context.Context, *types.Dependency, string) error
	// SetConfig writes one workspace config key, which is how a case installs
	// the vocabulary a request is read against — claim.pools for the transfer
	// fence's one carve-out, types.custom for the issue-type guard.
	//
	// A nil SetConfig means "this backend has no workspace vocabulary to
	// install", and the two cases whose subject IS a configured term SKIP
	// loudly with that reason rather than passing on their built-in halves.
	SetConfig func(context.Context, string, string) error
	// ListEvents answers the event journal entries recorded against ONE issue.
	// Order does not matter; every case takes a DELTA around the operation
	// under test.
	//
	// It is the second OUT-OF-BAND hook, beside GetIssue, and it exists because
	// "the refusal wrote nothing" has two halves that a row read answers only
	// one of. RowVersion says the ROW did not move; nothing on the row says an
	// event was not appended beside it, and a body that refused after emitting
	// leaves a status_changed in the stream every history consumer reads. The
	// staging cases got this from `SELECT COUNT(*) FROM events`; every in-tree
	// leg answers it without one, through storage.GetEvents or the unit of
	// work's IterEvents.
	//
	// AN OUT-OF-TREE LEG MAY HAVE TO WORK FOR IT. The v0 HTTP journal is keyed
	// on `since` and `limit` rather than on an issue, so a client leg pages the
	// feed and projects the entries for one id itself; that is a real cost, and
	// it is why the hook is nil-able rather than required.
	//
	// A nil ListEvents means "this backend cannot observe its event journal".
	// The event half of each case is then DROPPED — with a t.Log naming what
	// stopped being proven, so a leg reading a green run knows it bought less
	// than the case advertises — and the row half still runs. The alternative
	// is skipping cases whose main subject is not the journal.
	ListEvents func(context.Context, string) ([]*types.Event, error)
	// ListDependencies answers ONE issue's outgoing edges as records — the
	// target id and the edge type. Only the ParentID cases need it, and they
	// need the whole SET rather than one lookup: the clause under test is that
	// a set ParentID "atomically replaces ALL parents with exactly that
	// target", which a case that only checked the new parent's presence cannot
	// see.
	//
	// A nil ListDependencies means the backend cannot report an issue's edges,
	// and those cases SKIP loudly with that reason: a reparent asserted only
	// through Changed is a reparent asserted nowhere.
	ListDependencies func(context.Context, string) ([]*types.Dependency, error)
	// WispExists reports whether the EPHEMERAL plane holds a row at id.
	//
	// GetIssue resolves the durable plane first, so it can prove a row IS a
	// wisp — an ephemeral answer means no durable row shares that id — but
	// never that a wisp is ABSENT under an id the durable plane also holds.
	// That asymmetry is the whole content of the persistence clause below: a
	// restatement that copied a durable row into the wisp plane without
	// deleting the original is invisible to a both-plane read.
	//
	// A nil WispExists means "this backend has no separable ephemeral plane".
	// The absence probe is then DROPPED with a t.Log naming what stopped being
	// proven, and the class-preservation clause it sits under still runs.
	WispExists func(context.Context, string) (bool, error)
	// CountHistoryMatching counts the version-log entries whose message matches
	// a SQL LIKE pattern ("" = every entry). Only the provenance case needs it,
	// and it needs the message rather than a bare count: the clause it pins is
	// that the recorded entry READS as the caller's own string.
	//
	// A nil CountHistoryMatching means "this backend cannot observe history by
	// message", and that case SKIPS loudly with that reason rather than passing
	// quietly. See history_matching.go for the convention.
	CountHistoryMatching func(context.Context, string) (int, error)
}

// RunLifecycleUpdatePersistsThePatchAndHydratesTheResult pins the two halves of
// what an ordinary edit answers: every patched member reaches the row, and
// UpdateResult reports "the post-update issue as a detached post-state snapshot
// with labels and dependency records" with "Comments are omitted"
// (issueops/issueops.go:355-364).
//
// The result half is the one with teeth. A caller that renders what it just
// wrote — which is what every non-interactive front door does — gets an issue
// with no labels and no edges from a backend that answered the bare row, and
// nothing else in the package would notice: the closest neighbor,
// RunLifecycleResultsAreHydratedPostStateSnapshots, covers Close and Reopen
// only, and the Update-seam cases assert result FIELDS rather than the
// relations hanging off them.
//
// One request carries every wire-expressible content member at once, because a
// patch that arrives as one document is applied as one mutation: a backend that
// dropped a member would otherwise still pass a case that sent members singly.
func RunLifecycleUpdatePersistsThePatchAndHydratesTheResult(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-patch"
	peer := fixture.IssuePrefix + "-lup-patch-peer"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id, "lup-tag"))
	if fixture.AddDependency != nil {
		seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(peer))
		// relates-to, not blocks: the edge has to show up in the result without
		// changing what any later edit is allowed to do.
		seedLifecycleUpdateEdge(t, ctx, fixture, id, peer, types.DepRelatesTo)
	}

	result, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Title:              publicops.Field[string]{Set: true, Value: "patched title"},
		Description:        publicops.Field[string]{Set: true, Value: "patched description"},
		Design:             publicops.Field[string]{Set: true, Value: "patched design"},
		AcceptanceCriteria: publicops.Field[string]{Set: true, Value: "patched acceptance"},
		Notes:              publicops.Field[string]{Set: true, Value: "patched notes"},
		Priority:           publicops.Field[int]{Set: true, Value: 0},
		IssueType:          publicops.Field[publicops.IssueType]{Set: true, Value: types.TypeBug},
	}})
	if err != nil {
		t.Fatalf("update %s: %v", id, err)
	}
	if !result.Changed {
		t.Errorf("update of %s reported Changed = false, want a committed edit", id)
	}
	assertLifecycleUpdateContent(t, "update result", result.Issue)
	assertLifecycleUpdateContent(t, "stored row", lifecycleUpdateRow(t, ctx, fixture, id))

	if !lifecycleUpdateHasLabel(result.Issue.Labels, "lup-tag") {
		t.Errorf("update result labels = %v, want the seeded label — the result is promised hydrated", result.Issue.Labels)
	}
	if len(result.Issue.Comments) != 0 {
		t.Errorf("update result carries %d comments, want none — the result doc omits them", len(result.Issue.Comments))
	}
	if fixture.AddDependency != nil && !lifecycleUpdateHasEdge(result.Issue.Dependencies, peer) {
		t.Errorf("update result dependencies = %v, want a record naming %s", result.Issue.Dependencies, peer)
	}
}

// RunLifecycleUpdatePreservesTheCreationStamp pins the pair of columns an edit
// is never allowed to touch: created_at and created_by describe the row's
// origin, and Update is a verb about its content. Nothing in IssuePatch can
// express either one (issueops/issueops.go IssuePatch names neither), so a body
// that writes them writes them by accident — and the accident is silent, because
// the caller asked for a title and got one.
//
// IT IS PINNED NOWHERE ELSE IN THE PACKAGE. The string "CreatedBy" appears in no
// contract case, and every backend builds the update's column set itself: the
// two stores through issueops.UpdateFields, the unit-of-work backend through its
// own updateSpec and the allow-list in domain/db. Two implementations, two
// chances to add a column that should not be there — and `bd show`'s "created by
// X on Y" and every provenance report downstream of it read exactly these two.
//
// THE SEEDED STAMP IS DISTANT ON PURPOSE, and this is the case's fixture, not
// its assertion. created_at is DATETIME(0) — no fractional seconds — so a
// creation stamped at "now" and an update that rewrote it to "now" inside the
// same second are the SAME STORED BYTES, and a case seeding the current time
// cannot tell "preserved" from "rewritten". A stamp years in the past can only
// survive by being left alone.
//
// The seeded values are READ BACK BEFORE THE UPDATE rather than assumed. A seed
// hook that dropped the preset stamp would leave the row carrying "now", and the
// case would then be comparing a rewrite against a rewrite and passing. That
// precondition is also the only proof in the package that Create HONORS a preset
// created_at, which every import and restore path depends on.
//
// BOTH SET-CLAUSE SHAPES ARE DRIVEN, because the backends build them on
// different branches: a content-only patch, and a status patch, which is the leg
// where each body additionally derives started_at and closed_at and so assembles
// its column list somewhere else.
func RunLifecycleUpdatePreservesTheCreationStamp(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-created"
	seeded := lifecycleUpdateIssue(id)
	seeded.CreatedAt = lifecycleUpdateSeededCreatedAt
	seeded.CreatedBy = "founder"
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)

	before := lifecycleUpdateRow(t, ctx, fixture, id)
	if !before.CreatedAt.Equal(lifecycleUpdateSeededCreatedAt) {
		t.Fatalf("seeded %s carries created_at %v, want the preset %v — a seed that stamps its own creation time leaves this case unable to tell a preserved stamp from a rewritten one",
			id, before.CreatedAt.UTC(), lifecycleUpdateSeededCreatedAt)
	}
	if before.CreatedBy != "founder" {
		t.Fatalf("seeded %s carries created_by %q, want the preset %q — see above", id, before.CreatedBy, "founder")
	}

	for _, edit := range []struct {
		name  string
		patch publicops.IssuePatch
	}{
		{"a content patch", publicops.IssuePatch{
			Title: publicops.Field[string]{Set: true, Value: "retitled"},
		}},
		{"a status patch", publicops.IssuePatch{
			Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		}},
	} {
		t.Run(edit.name, func(t *testing.T) {
			result, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "editor", IssueID: id, Patch: edit.patch})
			if err != nil {
				t.Fatalf("update %s with %s: %v", id, edit.name, err)
			}
			if !result.Changed {
				t.Fatalf("%s on %s reported Changed = false, want a committed edit — a no-op would not exercise the write at all", edit.name, id)
			}
			assertLifecycleUpdateCreationStamp(t, "the stored row after "+edit.name, lifecycleUpdateRow(t, ctx, fixture, id))
			// The result is held to the same expectation because it is what a
			// front door renders after the write: a row that kept its stamp and
			// a snapshot that reports the editor as the author still shows the
			// caller the wrong provenance.
			assertLifecycleUpdateCreationStamp(t, "the update result after "+edit.name, result.Issue)
		})
	}
}

// lifecycleUpdateSeededCreatedAt is years in the past, which is what makes a
// rewritten created_at observable against a DATETIME(0) column: see
// RunLifecycleUpdatePreservesTheCreationStamp.
var lifecycleUpdateSeededCreatedAt = time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)

func assertLifecycleUpdateCreationStamp(t *testing.T, label string, issue *types.Issue) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s = nil, want the updated issue", label)
	}
	if !issue.CreatedAt.Equal(lifecycleUpdateSeededCreatedAt) {
		t.Errorf("%s created_at = %v, want it unchanged at %v — an edit does not re-create the row",
			label, issue.CreatedAt.UTC(), lifecycleUpdateSeededCreatedAt)
	}
	if issue.CreatedBy != "founder" {
		t.Errorf("%s created_by = %q, want it unchanged at %q — the editor is not the author",
			label, issue.CreatedBy, "founder")
	}
}

// RunLifecycleUpdateReportsNoChangeForASameValuePatch pins UpdateResult.Changed:
// it "reports whether the request persisted a semantic mutation. It is false for
// same-value patches and no-op updates" (issueops/issueops.go:361-363).
//
// Changed is the only signal a polling or replaying caller has that its request
// was absorbed, and an implementation that answered true unconditionally would
// have such a caller mint an empty version-control commit per call. The state
// half is what makes the assertion more than a boolean: RowVersion is "a random
// non-zero value the engine rewrites on every status/ownership-mutating write"
// and specifically changes on "the generic update path" (types.go:70-84), so a
// same-value patch that left it alone is a patch that really wrote nothing —
// not one that wrote the same bytes back and reported false.
//
// The result of a no-op is asserted hydrated too. Nothing in the doc makes the
// snapshot conditional on Changed, and a backend that returned an early bare row
// on the no-op path would hand a replaying caller a different body than the
// first call did.
func RunLifecycleUpdateReportsNoChangeForASameValuePatch(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-samevalue"
	seeded := lifecycleUpdateIssue(id, "lup-tag")
	seeded.Description = "seeded description"
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)
	before := lifecycleUpdateRow(t, ctx, fixture, id)

	result, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Title:       publicops.Field[string]{Set: true, Value: before.Title},
		Description: publicops.Field[string]{Set: true, Value: before.Description},
		Priority:    publicops.Field[int]{Set: true, Value: before.Priority},
		IssueType:   publicops.Field[publicops.IssueType]{Set: true, Value: before.IssueType},
	}})
	if err != nil {
		t.Fatalf("same-value patch on %s: %v", id, err)
	}
	if result.Changed {
		t.Errorf("restating %s's own values reported Changed = true, want false", id)
	}
	if result.Issue == nil {
		t.Fatalf("same-value patch on %s answered a nil Issue, want a post-state snapshot", id)
	}
	if !lifecycleUpdateHasLabel(result.Issue.Labels, "lup-tag") {
		t.Errorf("same-value patch result labels = %v, want the seeded label — a no-op answers the same hydrated snapshot a change does", result.Issue.Labels)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the same-value patch", before)
}

// RunLifecycleUpdateAppendsNotesWithoutReplacingThem pins the only pair of
// IssuePatch members declared mutually exclusive of each other:
// "Notes replaces the notes and is mutually exclusive with AppendNotes" and
// "AppendNotes appends to the notes and is mutually exclusive with Notes"
// (issueops/issueops.go:117-121).
//
// Three obligations, and the package has an owning case for none of them: an
// append keeps what was there and adds a line, a replacement discards it, and
// the two together are a deterministic validation failure that leaves the notes
// where they were. An implementation that treated AppendNotes as a second
// spelling of Notes would silently destroy an agent's running log — the field
// exists so that appending does not require reading first, which is exactly the
// case where the caller cannot notice the loss.
//
// The append against EMPTY notes is the separator's edge: appending to nothing
// stores the text alone rather than a leading blank line.
func RunLifecycleUpdateAppendsNotesWithoutReplacingThem(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-notes"
	seeded := lifecycleUpdateIssue(id)
	seeded.Notes = "first"
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)

	appended, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		AppendNotes: publicops.Field[string]{Set: true, Value: "second"},
	}})
	if err != nil {
		t.Fatalf("append notes on %s: %v", id, err)
	}
	if !appended.Changed {
		t.Errorf("appending notes on %s reported Changed = false, want a committed edit", id)
	}
	if got := lifecycleUpdateRow(t, ctx, fixture, id).Notes; got != "first\nsecond" {
		t.Errorf("notes after the append = %q, want %q — the append must keep what was there", got, "first\nsecond")
	}

	replaced, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Notes: publicops.Field[string]{Set: true, Value: "replaced"},
	}})
	if err != nil {
		t.Fatalf("replace notes on %s: %v", id, err)
	}
	if !replaced.Changed {
		t.Errorf("replacing notes on %s reported Changed = false, want a committed edit", id)
	}
	if got := lifecycleUpdateRow(t, ctx, fixture, id).Notes; got != "replaced" {
		t.Errorf("notes after the replacement = %q, want %q", got, "replaced")
	}

	before := lifecycleUpdateRow(t, ctx, fixture, id)
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Notes:       publicops.Field[string]{Set: true, Value: "both"},
		AppendNotes: publicops.Field[string]{Set: true, Value: "both"},
	}}); !errors.Is(err, storage.ErrValidation) {
		t.Errorf("a patch setting Notes and AppendNotes together = %v, want ErrValidation — the two are mutually exclusive", err)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the mutually-exclusive refusal", before)

	empty := fixture.IssuePrefix + "-lup-notes-empty"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(empty))
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: empty, Patch: publicops.IssuePatch{
		AppendNotes: publicops.Field[string]{Set: true, Value: "only"},
	}}); err != nil {
		t.Fatalf("append notes on empty %s: %v", empty, err)
	}
	if got := lifecycleUpdateRow(t, ctx, fixture, empty).Notes; got != "only" {
		t.Errorf("notes after appending to an empty field = %q, want %q with no leading separator", got, "only")
	}
}

// RunLifecycleUpdateClearsTheNullableMembers pins the four IssuePatch members
// modeled as Field[*T] — EstimatedMinutes, ExternalRef, DueAt and DeferUntil
// (issueops/issueops.go:135-139). A pointer is the only thing a clear can write,
// and a set field carrying a nil pointer is the request that means "make this
// column empty".
//
// Nothing in the package covers a clear. That matters because clearing is the
// half a caller cannot work around: an implementation that read a nil pointer as
// "member omitted" would leave a stale due date or a stale external reference in
// place and report success, and the caller's only recourse would be to write a
// sentinel value into a column that has a real empty state.
//
// The second clear is the no-op leg: already-empty columns cleared again report
// Changed false, which is what tells "the clear landed" apart from "the clear
// was ignored twice".
func RunLifecycleUpdateClearsTheNullableMembers(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-clear"
	minutes := 45
	ref := "conformance-ref"
	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	deferUntil := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	seeded := lifecycleUpdateIssue(id)
	seeded.EstimatedMinutes = &minutes
	seeded.ExternalRef = &ref
	seeded.DueAt = &due
	seeded.DeferUntil = &deferUntil
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)

	before := lifecycleUpdateRow(t, ctx, fixture, id)
	if before.EstimatedMinutes == nil || before.ExternalRef == nil || before.DueAt == nil || before.DeferUntil == nil {
		t.Fatalf("seeded %s with {minutes %v, ref %v, due %v, defer %v}, want all four set — the clear has nothing to prove otherwise",
			id, before.EstimatedMinutes, before.ExternalRef, before.DueAt, before.DeferUntil)
	}

	clear := publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		EstimatedMinutes: publicops.Field[*int]{Set: true},
		ExternalRef:      publicops.Field[*string]{Set: true},
		DueAt:            publicops.Field[*time.Time]{Set: true},
		DeferUntil:       publicops.Field[*time.Time]{Set: true},
	}}
	cleared, err := fixture.Lifecycle.Update(ctx, clear)
	if err != nil {
		t.Fatalf("clear the nullable members of %s: %v", id, err)
	}
	if !cleared.Changed {
		t.Errorf("clearing the nullable members of %s reported Changed = false, want a committed edit", id)
	}
	assertLifecycleUpdateCleared(t, "update result", cleared.Issue)
	assertLifecycleUpdateCleared(t, "stored row", lifecycleUpdateRow(t, ctx, fixture, id))

	after := lifecycleUpdateRow(t, ctx, fixture, id)
	again, err := fixture.Lifecycle.Update(ctx, clear)
	if err != nil {
		t.Fatalf("re-clear the nullable members of %s: %v", id, err)
	}
	if again.Changed {
		t.Errorf("re-clearing the already-empty members of %s reported Changed = true, want false", id)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the re-clear", after)
}

// RunLifecycleUpdateReplacesTheLabelSet pins LabelPatch.Replace, which "supplies
// the starting complete label set when Set is true" (issueops/issueops.go:78-79),
// at the role seam and on its own.
//
// COMPLETE is the word being pinned. Replace is a set assignment, not a merge:
// labels the request omits go away, and a Replace carrying nothing clears the
// set rather than being read as "no label edit requested" — the one place where
// the omitted/empty distinction Field exists for is load-bearing on a slice.
//
// RunIssueOperationsUpdateLabelPatchOrdering covers Replace as the first of
// three edits applied in order, but only behind the staging fixture and only
// while Add and Remove are also in play; the empty replacement is covered
// nowhere. The result set is asserted alongside the stored one because the
// labels on UpdateResult.Issue are what a front door renders after the write.
func RunLifecycleUpdateReplacesTheLabelSet(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-labels"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id, "kept", "dropped"))

	replaced, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"kept", "added"}}},
	}})
	if err != nil {
		t.Fatalf("replace the labels of %s: %v", id, err)
	}
	if !replaced.Changed {
		t.Errorf("replacing the labels of %s reported Changed = false, want a committed edit", id)
	}
	assertLifecycleUpdateLabels(t, "replacement result", replaced.Issue, "added", "kept")
	assertLifecycleUpdateLabels(t, "stored row after the replacement", lifecycleUpdateRow(t, ctx, fixture, id), "added", "kept")

	before := lifecycleUpdateRow(t, ctx, fixture, id)
	restated, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"added", "kept"}}},
	}})
	if err != nil {
		t.Fatalf("restate the labels of %s: %v", id, err)
	}
	if restated.Changed {
		t.Errorf("restating %s's own label set reported Changed = true, want a no-op", id)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the restated label set", before)

	emptied, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true}},
	}})
	if err != nil {
		t.Fatalf("clear the labels of %s: %v", id, err)
	}
	if !emptied.Changed {
		t.Errorf("clearing the labels of %s reported Changed = false, want a committed edit — an empty Replace is a replacement, not an omission", id)
	}
	assertLifecycleUpdateLabels(t, "cleared result", emptied.Issue)
	assertLifecycleUpdateLabels(t, "stored row after the clear", lifecycleUpdateRow(t, ctx, fixture, id))
}

// RunLifecycleUpdateResolvesBothPlanesUnlessRestricted pins what an id resolves
// against: "The zero value keeps the both-plane auto-resolve every caller gets
// today", while a set IssuePlaneOnly makes "an ID that names a wisp ErrNotFound
// rather than an ephemeral row to update" (issueops/issueops.go:251-260).
//
// Both halves are one case because either alone passes for the wrong reason: a
// backend that resolved nothing satisfies the refusal, and one that ignored the
// flag satisfies the auto-resolve. The durable leg is the third control — the
// restriction is about the plane the id names, not about the flag being set.
//
// This re-pins RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps at a seam that
// needs no staging fixture: that case seeds its wisp through Operations.Create
// and reads its post-state through QueryScalar, and a backend that has neither
// gets no plane coverage at all.
//
// A nil CreateWisp means the backend has no wisp plane to resolve against, and
// this case SKIPS loudly rather than passing on the durable leg alone.
func RunLifecycleUpdateResolvesBothPlanesUnlessRestricted(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.CreateWisp == nil {
		t.Skip("fixture cannot seed a wisp: CreateWisp is nil, so there is no second plane for an id to resolve against")
	}

	wisp := fixture.IssuePrefix + "-lup-plane-wisp"
	seedLifecycleUpdateWisp(t, ctx, fixture, lifecycleUpdateIssue(wisp))
	before := lifecycleUpdateRow(t, ctx, fixture, wisp)

	restricted := publicops.UpdateRequest{
		Actor: "writer", IssueID: wisp, IssuePlaneOnly: true,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "restricted title"}},
	}
	if _, err := fixture.Lifecycle.Update(ctx, restricted); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("issue-plane-only update of wisp %s = %v, want ErrNotFound", wisp, err)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, wisp, "after the refused plane-only update", before)

	unrestricted := restricted
	unrestricted.IssuePlaneOnly = false
	landed, err := fixture.Lifecycle.Update(ctx, unrestricted)
	if err != nil {
		t.Fatalf("both-plane update of wisp %s: %v", wisp, err)
	}
	if !landed.Changed {
		t.Errorf("both-plane update of wisp %s reported Changed = false, want the edit committed", wisp)
	}
	if got := lifecycleUpdateRow(t, ctx, fixture, wisp).Title; got != "restricted title" {
		t.Errorf("wisp %s title after the both-plane update = %q, want %q", wisp, got, "restricted title")
	}

	durable := fixture.IssuePrefix + "-lup-plane-durable"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(durable))
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: durable, IssuePlaneOnly: true,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "durable title"}},
	}); err != nil {
		t.Fatalf("issue-plane-only update of durable %s: %v", durable, err)
	}
	if got := lifecycleUpdateRow(t, ctx, fixture, durable).Title; got != "durable title" {
		t.Errorf("durable %s title after the plane-only update = %q, want %q — the restriction names the plane, not the flag", durable, got, "durable title")
	}
}

// RunLifecycleUpdateRefusesUnknownIDsAndActorlessRequests pins the two refusals
// every caller of this verb classifies on before it can classify anything else,
// each with the state clause the Lifecycle doc attaches to it: "Deterministic
// request validation failures match ErrValidation" and "Refusals and
// deterministic validation failures leave persistent state unchanged"
// (issueops/issueops.go:391-404).
//
// The actorless legs are the ones with teeth: the id names a real, patchable
// row, so an implementation that validated after writing fails on the row rather
// than on the error. The actor is the audit trail the storage commit carries, so
// a defaulted one would write an unattributable edit.
//
// The unknown-id leg has no owning case for a plain Update anywhere: the
// package's other Update ErrNotFound is the IssuePlaneOnly wisp leg, which is a
// refusal about a row that DOES exist.
func RunLifecycleUpdateRefusesUnknownIDsAndActorlessRequests(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-refuse"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))
	before := lifecycleUpdateRow(t, ctx, fixture, id)
	edit := publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "refused title"}}

	unknown := fixture.IssuePrefix + "-lup-nobody"
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: unknown, Patch: edit}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("update of an unknown id = %v, want ErrNotFound", err)
	}

	refusals := map[string]publicops.UpdateRequest{
		"update without an actor": {IssueID: id, Patch: edit},
		"update without an id":    {Actor: "writer", Patch: edit},
		"update without either":   {Patch: edit},
	}
	for name, request := range refusals {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.Lifecycle.Update(ctx, request); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", name, err)
			}
			assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after "+name, before)
		})
	}
}

// RunLifecycleUpdateRefusalWritesNoMemberOfThePatch pins the ATOMICITY half of
// the same clause the case above pins the identity half of: a refused update
// "leaves persistent state unchanged" (issueops/issueops.go:403-404), and
// Update "validates guards and commits the complete request as one atomic
// mutation" (:415-416).
//
// One member of a multi-member patch is invalid — a priority outside the
// canonical P0-P4 range — and the rest are ordinary edits that would each have
// committed alone. A backend that validated per member as it applied them, or
// that committed the members it had already written before reaching the bad one,
// leaves an issue carrying half a patch: the caller sees a refusal, retries, and
// the retry is now a partial no-op against a row it never agreed to.
//
// The RowVersion check is what makes this more than a field comparison. It is
// rewritten on "the generic update path" (types.go:70-84), so a version that did
// not move is the row-level evidence that no write happened at all — a backend
// that wrote the good members and then rolled back to the same values would fail
// here rather than pass a value-only assertion.
func RunLifecycleUpdateRefusalWritesNoMemberOfThePatch(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-atomic"
	seeded := lifecycleUpdateIssue(id, "lup-tag")
	seeded.Description = "seeded description"
	seeded.Notes = "seeded notes"
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)
	before := lifecycleUpdateRow(t, ctx, fixture, id)

	_, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Title:       publicops.Field[string]{Set: true, Value: "clobbered title"},
		Description: publicops.Field[string]{Set: true, Value: "clobbered description"},
		Notes:       publicops.Field[string]{Set: true, Value: "clobbered notes"},
		Priority:    publicops.Field[int]{Set: true, Value: lifecycleUpdateInvalidPriority},
		Labels:      publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"clobbered"}}},
	}})
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("update of %s with an out-of-range priority = %v, want ErrValidation", id, err)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the refused multi-member patch", before)
	assertLifecycleUpdateLabels(t, "stored row after the refused multi-member patch", lifecycleUpdateRow(t, ctx, fixture, id), "lup-tag")
}

// RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits pins the three
// compare-and-set preconditions as PRECONDITIONS ON A PLAIN EDIT — the way
// `bd update --if-status` / `--if-assignee` / `--if-version` reach the
// contract. The assignee-transfer case below pins the ORDER two of them resolve
// in beside a fenced transfer; here they gate an ordinary field update that no
// fence would touch.
//
// The clauses: ExpectedAssignee "requires the current assignee to match",
// ExpectedStatus "requires the current status to match", and ExpectedVersion
// requires the row's optimistic-concurrency token to match
// (issueops/issueops.go:250-256), under Lifecycle's standing promise that a
// "refusal or validation error leaves persistent state unchanged"
// (issueops/issueops.go:406-408). A refusal that leaked a partial write would
// exit 13 — "another actor won the race, nothing was written, do not retry" —
// while having written.
//
// EXPECTED VERSION IS THE HALF THAT WAS UNREACHABLE ON THIS VERB, and it is why
// this case needed moving rather than copying. This is the first SATISFIED
// ExpectedVersion leg on Lifecycle.Update: every Update-side assertion of the
// precondition sent a STALE sentinel (-1) that no row could ever hold, and a
// refusal-only case passes against a body that refuses every guarded request
// outright. The satisfied leg is what tells a real compare-and-set from a
// blanket refusal, and it needs the row's CURRENT token — which is exactly the
// observation the staging fixture got from `SELECT row_lock` and a partial
// backend could not make. It is on the row GetIssue answers
// (types.Issue.RowVersion, types.go:75-94), so it needs no hook of its own.
//
// THE CLOSE AND REOPEN VERBS ALREADY HAD THEIRS, and this case does not
// supersede them: RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps drives a
// satisfied token on a real row through Close AND Reopen, and pins an ORDERING
// clause — the precondition is checked ahead of the idempotent no-op — that
// nothing here asserts. Three verbs, three bodies; retiring either side against
// the other deletes coverage.
//
// The stale token here is the CURRENT one minus one rather than a sentinel: -1
// is refused by a body that range-checks the field, and this case is about the
// comparison.
func RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-guardgate"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))
	events := newLifecycleUpdateEventCounter(t, ctx, fixture, id)

	priorityEdit := func(priority int) publicops.UpdateRequest {
		return publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
			Priority: publicops.Field[int]{Set: true, Value: priority},
		}}
	}
	assertPriority := func(label string, want int) {
		t.Helper()
		if got := lifecycleUpdateRow(t, ctx, fixture, id).Priority; got != want {
			t.Errorf("%s = %d, want %d", label, got, want)
		}
	}

	// The seeded row is open and unassigned, so the empty string is a real
	// "expected unassigned" guard rather than the absence of one — the
	// distinction `--if-assignee ''` depends on.
	unassigned := ""
	matching := priorityEdit(1)
	matching.ExpectedAssignee = &unassigned
	openStatus := types.StatusOpen
	matching.ExpectedStatus = &openStatus
	if result, err := fixture.Lifecycle.Update(ctx, matching); err != nil || !result.Changed {
		t.Fatalf("guarded edit with both preconditions holding = %#v, %v; want the edit applied", result, err)
	}
	assertPriority("priority after a satisfied guard", 1)
	events.assert(t, "satisfied guard", 1)

	// A stale status refuses, and the refusal writes nothing — not the field
	// the request carried, not an event.
	staleStatus := types.StatusInProgress
	staleStatusEdit := priorityEdit(0)
	staleStatusEdit.ExpectedStatus = &staleStatus
	if _, err := fixture.Lifecycle.Update(ctx, staleStatusEdit); !errors.Is(err, storage.ErrStatusMismatch) {
		t.Fatalf("edit guarded on a stale status: err = %v, want ErrStatusMismatch", err)
	}
	assertPriority("priority after a stale status guard", 1)
	events.assert(t, "stale status guard", 0)

	// A stale assignee refuses the same way, including when the status guard
	// beside it still holds: both preconditions must hold, not either.
	staleAssignee := "nobody"
	staleAssigneeEdit := priorityEdit(0)
	staleAssigneeEdit.ExpectedAssignee = &staleAssignee
	staleAssigneeEdit.ExpectedStatus = &openStatus
	if _, err := fixture.Lifecycle.Update(ctx, staleAssigneeEdit); !errors.Is(err, storage.ErrAssigneeMismatch) {
		t.Fatalf("edit guarded on a stale assignee: err = %v, want ErrAssigneeMismatch", err)
	}
	assertPriority("priority after a stale assignee guard", 1)
	events.assert(t, "stale assignee guard", 0)

	// THE VERSION GUARD, both directions. The satisfied leg reads the row's own
	// token and hands it straight back, which is the compare-and-set a caller
	// that already read the row performs; the stale leg is the same request one
	// token behind.
	current := lifecycleUpdateRow(t, ctx, fixture, id)
	currentVersion := current.RowVersion
	if currentVersion == 0 {
		t.Fatalf("%s carries RowVersion 0, so neither version guard below could fail: the token is the subject", id)
	}
	versionEdit := priorityEdit(2)
	versionEdit.ExpectedVersion = &currentVersion
	if result, err := fixture.Lifecycle.Update(ctx, versionEdit); err != nil || !result.Changed {
		t.Fatalf("edit guarded on the row's current version = %#v, %v; want the edit applied", result, err)
	}
	assertPriority("priority after a satisfied version guard", 2)
	events.assert(t, "satisfied version guard", 1)

	before := lifecycleUpdateRow(t, ctx, fixture, id)
	staleVersion := before.RowVersion - 1
	staleVersionEdit := priorityEdit(0)
	staleVersionEdit.ExpectedVersion = &staleVersion
	if _, err := fixture.Lifecycle.Update(ctx, staleVersionEdit); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Fatalf("edit guarded on a stale version: err = %v, want ErrVersionMismatch", err)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the stale version guard", before)
	events.assert(t, "stale version guard", 0)

	// The guard tracks the row rather than the request that set it: once an
	// assignee lands, the empty-string guard that just held is the stale one.
	assign := publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Assignee: publicops.Field[string]{Set: true, Value: "holder"},
	}}
	if _, err := fixture.Lifecycle.Update(ctx, assign); err != nil {
		t.Fatalf("assign %s: %v", id, err)
	}
	events.assert(t, "assign", 1)
	nowStale := priorityEdit(0)
	nowStale.ExpectedAssignee = &unassigned
	if _, err := fixture.Lifecycle.Update(ctx, nowStale); !errors.Is(err, storage.ErrAssigneeMismatch) {
		t.Fatalf("edit guarded on unassigned after an assignment: err = %v, want ErrAssigneeMismatch", err)
	}
	assertPriority("priority after the once-current guard went stale", 2)
	events.assert(t, "once-current guard", 0)

	holder := "holder"
	nowCurrent := priorityEdit(0)
	nowCurrent.ExpectedAssignee = &holder
	if result, err := fixture.Lifecycle.Update(ctx, nowCurrent); err != nil || !result.Changed {
		t.Fatalf("edit guarded on the current holder = %#v, %v; want the edit applied", result, err)
	}
	assertPriority("priority after a guard naming the current holder", 0)

	// THE ORDER-DEPENDENT COMPOSITION, and the one arm nothing else covers: an
	// EARLIER guard that holds beside a LATER guard that is stale.
	//
	// Every refusal above puts the stale guard first, so a body that checked
	// only the first present precondition — an `else if` where an `if` belongs,
	// which is one refactor slip — refused all of them correctly and let this
	// one through. The assignee guard names the current holder and the status
	// guard names a status the row does not have, so the answer must be the
	// LATER guard's sentinel, not silence.
	//
	// A fresh counter: the edit above committed and this arm is about what a
	// REFUSAL writes, so it must start from zero rather than inherit that one.
	maskedEvents := newLifecycleUpdateEventCounter(t, ctx, fixture, id)
	maskedEdit := priorityEdit(3)
	maskedEdit.ExpectedAssignee = &holder
	maskedEdit.ExpectedStatus = &staleStatus
	if _, err := fixture.Lifecycle.Update(ctx, maskedEdit); !errors.Is(err, storage.ErrStatusMismatch) {
		t.Fatalf("edit with a holding assignee guard and a stale status guard: err = %v, want ErrStatusMismatch", err)
	}
	assertPriority("priority after a stale guard behind a holding one", 0)
	maskedEvents.assert(t, "guard masked by the one before it", 0)
}

// RunLifecycleUpdateConditionalGuardAcceptsRespelledAssignee pins the
// ga-5ksp5 fix beside RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits
// above rather than inside it: that test's later "order-dependent composition"
// arm depends on the row's assignee still being exactly "holder" all the way
// to the end, so reassigning it mid-test to exercise a second spelling would
// invalidate its own assumption.
//
// The two Gas Town identity spellings a caller can send for the same actor
// (dot vs underscore separator, ga-wzl83) must not desync from the row's
// stored spelling: an ExpectedAssignee guard is satisfied by either spelling
// of the CURRENT holder, and still refused by a spelling of a genuinely
// different identity. Separate from RunLifecycleUpdateAssigneeTransferFence
// in this file, which pins the same respelling rule for the CLAIM/TRANSFER
// fence (AuthorizeAssigneeTransferWithPools) — this pins it for the plain
// compare-and-set guard (CheckExpectedFieldsInTx / ApplyUpdate's own check),
// the third, previously-split verbatim-comparison surface the ga-3ipxu gate
// review on #5439 found.
func RunLifecycleUpdateConditionalGuardAcceptsRespelledAssignee(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-respelledguard"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))

	priorityEdit := func(priority int) publicops.UpdateRequest {
		return publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
			Priority: publicops.Field[int]{Set: true, Value: priority},
		}}
	}
	assertPriority := func(label string, want int) {
		t.Helper()
		if got := lifecycleUpdateRow(t, ctx, fixture, id).Priority; got != want {
			t.Errorf("%s = %d, want %d", label, got, want)
		}
	}

	assign := publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Assignee: publicops.Field[string]{Set: true, Value: "gastown.mayor"},
	}}
	if _, err := fixture.Lifecycle.Update(ctx, assign); err != nil {
		t.Fatalf("assign %s under a dotted identity: %v", id, err)
	}

	// A guard naming the SAME identity under a different (and
	// doubled-separator) spelling is satisfied, same as the row's own
	// verbatim spelling would be.
	respelled := "gastown__mayor"
	respelledEdit := priorityEdit(1)
	respelledEdit.ExpectedAssignee = &respelled
	if result, err := fixture.Lifecycle.Update(ctx, respelledEdit); err != nil || !result.Changed {
		t.Fatalf("edit guarded on a different spelling of the current holder = %#v, %v; want the edit applied", result, err)
	}
	assertPriority("priority after a guard naming the holder under a different spelling", 1)

	// Canonicalization must not over-match: a spelling of a genuinely
	// different identity — not just a respelling of the holder — still
	// refuses, and still writes nothing.
	foreign := "gastown_dog-1"
	foreignEdit := priorityEdit(3)
	foreignEdit.ExpectedAssignee = &foreign
	if _, err := fixture.Lifecycle.Update(ctx, foreignEdit); !errors.Is(err, storage.ErrAssigneeMismatch) {
		t.Fatalf("edit guarded on an unrelated identity: err = %v, want ErrAssigneeMismatch", err)
	}
	assertPriority("priority after a guard naming an unrelated identity", 1)
}

// RunLifecycleUpdateMetadataPatchOrdersMergeSetUnset pins the sentence
// MetadataPatch opens with: "Replace is mutually exclusive with Merge, Set, and
// Unset. Without Replace, operations apply Merge, then Set keys in
// deterministic order, then Unset" (issueops/issueops.go:83-86).
//
// The order is only observable when the three edits COLLIDE, so every key here
// appears in more than one of them: a key merged in and then set and then unset
// must end up absent, and one merged in and unset must not survive because the
// merge ran later. A body applying Unset before Set leaves the same document
// looking plausible — it just carries a key the caller asked to remove — which
// is why the other metadata cases, none of which collide, cannot see it.
//
// The exclusivity half is asserted through the stored document rather than the
// error alone: a body that refused the combination AFTER applying the Replace
// would return the right sentinel over a rewritten row.
func RunLifecycleUpdateMetadataPatchOrdersMergeSetUnset(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-metaorder"
	seeded := lifecycleUpdateIssue(id)
	seeded.Metadata = json.RawMessage(`{"keep":"seeded","drop":"seeded"}`)
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)

	ordered, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{
			Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"keep":"merged","contested":"merged","merged":true}`)},
			Set: map[string]json.RawMessage{
				"added":     json.RawMessage(`"set"`),
				"keep":      json.RawMessage(`"set"`),
				"contested": json.RawMessage(`"set"`),
				// NOT STRINGS, deliberately. Every other Set value in this file
				// is a JSON string, so a body that accepted only strings — the
				// unit-of-work leg validates Set values by shape in its own
				// gate, before the shared apply — passed every case here. A
				// number and a nested object are the two shapes a caller
				// actually stores.
				"count":  json.RawMessage(`7`),
				"nested": json.RawMessage(`{"a":[1,2],"b":{"c":true}}`),
			},
			Unset: []string{"keep", "drop"},
		},
	}})
	if err != nil {
		t.Fatalf("ordered metadata patch on %s: %v", id, err)
	}
	if !ordered.Changed {
		t.Errorf("ordered metadata patch on %s reported Changed = false, want a committed edit", id)
	}
	// "keep" was merged, then set, then unset — removal is last, so it is gone.
	// "drop" was seeded and unset. "merged" and "added" are what survives.
	//
	// "contested" is what makes the MERGE≺SET half of this case falsifiable, and
	// it is the reason a key colliding in Merge and Set is not enough on its own:
	// "keep" collides too, but Unset removes it, so a body running Set BEFORE
	// Merge produces the identical document and this case would pass over a
	// broken order. "contested" survives the patch, so it records which of the
	// two wrote last — Set does, per issueops.go's Merge≺Set≺Unset promise.
	const patched = `{"added":"set","contested":"set","count":7,"merged":true,"nested":{"a":[1,2],"b":{"c":true}}}`
	assertLifecycleUpdateMetadata(t, "ordered metadata patch result", ordered.Issue, patched)
	assertLifecycleUpdateMetadata(t, "stored row after the ordered metadata patch", lifecycleUpdateRow(t, ctx, fixture, id), patched)

	// Replace beside any incremental edit is refused, and the document the
	// replacement would have written never lands.
	events := newLifecycleUpdateEventCounter(t, ctx, fixture, id)
	for _, refusal := range []struct {
		name  string
		patch publicops.MetadataPatch
	}{
		{"replace with set", publicops.MetadataPatch{
			Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
			Set:     map[string]json.RawMessage{"must_not_persist": json.RawMessage(`true`)},
		}},
		{"replace with merge", publicops.MetadataPatch{
			Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
			Merge:   publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"must_not_persist":true}`)},
		}},
		{"replace with unset", publicops.MetadataPatch{
			Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
			Unset:   []string{"added"},
		}},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
				Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{Metadata: refusal.patch},
			}); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", refusal.name, err)
			}
			assertLifecycleUpdateMetadata(t, "stored row after "+refusal.name, lifecycleUpdateRow(t, ctx, fixture, id), patched)
		})
	}
	events.assert(t, "refused replace-plus-incremental patches", 0)
}

// RunLifecycleUpdateClosePolicy pins what a generic status update does when it
// crosses from a non-done status into the done category: it answers to the same
// policy `bd close` does — the open-children refusal and the live-direct-blocker
// refusal — and ForceClosePolicy is how a caller overrides them. Until this case
// existed the contract had no boundary-crossing case at all, and that gap is
// exactly how two earlier attempts at a shared policy check reached a backend
// that could not satisfy them without any test noticing.
//
// The graph is seeded through CreateIssue and AddDependency rather than through
// Lifecycle.Create, which is the whole point of the fixture's split: a backend
// whose composition refuses Create still has to answer for what its Update does
// at the done boundary.
func RunLifecycleUpdateClosePolicy(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.AddDependency == nil {
		t.Skip("fixture cannot seed an edge: AddDependency is nil, so neither the open-child nor the live-blocker policy has a graph to refuse over")
	}

	parentID := fixture.IssuePrefix + "-lup-closepolicy-parent"
	childID := fixture.IssuePrefix + "-lup-closepolicy-child"
	blockerID := fixture.IssuePrefix + "-lup-closepolicy-blocker"
	blockedID := fixture.IssuePrefix + "-lup-closepolicy-blocked"
	for _, id := range []string{parentID, childID, blockerID, blockedID} {
		seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))
	}
	seedLifecycleUpdateEdge(t, ctx, fixture, childID, parentID, types.DepParentChild)
	seedLifecycleUpdateEdge(t, ctx, fixture, blockedID, blockerID, types.DepBlocks)

	// An open child refuses, with the typed error and its count, and writes
	// nothing — not the row, not an event.
	events := newLifecycleUpdateEventCounter(t, ctx, fixture, parentID)
	var openChildrenErr *publicops.CloseOpenChildrenError
	_, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateClosingRequest(parentID, false))
	if !errors.As(err, &openChildrenErr) {
		t.Fatalf("update %s into done with an open child: err = %v, want CloseOpenChildrenError", parentID, err)
	}
	if openChildrenErr.OpenChildren != 1 {
		t.Errorf("refusal reported %d open children, want 1", openChildrenErr.OpenChildren)
	}
	assertLifecycleUpdateStatus(t, ctx, fixture, parentID, types.StatusOpen)
	events.assert(t, "refused crossing", 0)

	// A claim rides the same atomic update. An open-child refusal must leave
	// every part of that compound request inert, including the would-be claim.
	before := lifecycleUpdateRow(t, ctx, fixture, parentID)
	claimAndClose := lifecycleUpdateClosingRequest(parentID, false)
	claimAndClose.Claim = true
	_, err = fixture.Lifecycle.Update(ctx, claimAndClose)
	openChildrenErr = nil
	if !errors.As(err, &openChildrenErr) {
		t.Fatalf("claiming update %s into done with an open child: err = %v, want CloseOpenChildrenError", parentID, err)
	}
	// RowVersion, assignee and closed_at together are what the staging case read
	// out of the row with raw SQL: the version says nothing was written at all,
	// the assignee says the would-be claim did not land, and closed_at says the
	// close-lifecycle column the crossing would have stamped stayed empty.
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, parentID, "after the compound refusal", before)
	events.assert(t, "refused claiming crossing", 0)

	// A live direct blocker refuses too.
	_, err = fixture.Lifecycle.Update(ctx, lifecycleUpdateClosingRequest(blockedID, false))
	if !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("update %s into done with a live blocker: err = %v, want ErrCloseBlocked", blockedID, err)
	}
	assertLifecycleUpdateStatus(t, ctx, fixture, blockedID, types.StatusOpen)

	// Force bypasses close policy and nothing else. A stale ExpectedVersion is
	// an orthogonal precondition, checked ahead of the policy and never waived
	// by it — the same ordering a checked close applies.
	staleVersion := lifecycleUpdateRow(t, ctx, fixture, parentID).RowVersion - 1
	staleRequest := lifecycleUpdateClosingRequest(parentID, true)
	staleRequest.ExpectedVersion = &staleVersion
	if _, err := fixture.Lifecycle.Update(ctx, staleRequest); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Fatalf("forced crossing with a stale version: err = %v, want ErrVersionMismatch", err)
	}
	assertLifecycleUpdateStatus(t, ctx, fixture, parentID, types.StatusOpen)

	// ForceClosePolicy bypasses both, and only those.
	for _, id := range []string{parentID, blockedID} {
		forced, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateClosingRequest(id, true))
		if err != nil {
			t.Fatalf("forced update %s into done: %v", id, err)
		}
		if !forced.Changed || forced.Issue.Status != types.StatusClosed {
			t.Fatalf("forced update %s into done = %#v, want a committed close", id, forced)
		}
	}

	// A done-to-done restatement is filtered out as a no-op before any policy
	// could observe it, so it needs no force even though the child is still open.
	reclose, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateClosingRequest(parentID, false))
	if err != nil {
		t.Fatalf("restate %s as done: %v", parentID, err)
	}
	if reclose.Changed {
		t.Errorf("restating %s as done reported Changed = true, want a no-op", parentID)
	}

	// A status change that does not reach the done category is untouched by any
	// of this, open child or not.
	nonCrossing := lifecycleUpdateClosingRequest(parentID, false)
	nonCrossing.Patch.Status.Value = types.StatusInProgress
	if _, err := fixture.Lifecycle.Update(ctx, nonCrossing); err != nil {
		t.Fatalf("non-crossing status update on %s: %v", parentID, err)
	}
	assertLifecycleUpdateStatus(t, ctx, fixture, parentID, types.StatusInProgress)
}

// RunLifecycleUpdateAssigneeTransferFence pins what an assignee edit does when
// it takes an issue away from a live foreign holder: it is refused with
// ErrAlreadyClaimed, and ForceAssigneeTransfer, an ExpectedAssignee
// compare-and-set, or a configured claim.pools alias are the only ways past it.
// The contract had no assignee-transfer case at all before this one, and that
// gap is exactly how one backend came to permit a transfer the other two refuse.
func RunLifecycleUpdateAssigneeTransferFence(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.SetConfig == nil {
		t.Skip("fixture cannot install workspace vocabulary: SetConfig is nil, so the claim.pools carve-out — the fence's only configured term — is unreachable")
	}

	heldID := fixture.IssuePrefix + "-lup-xferfence-held"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(heldID))
	claimed, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true})
	if err != nil {
		t.Fatalf("claim %s for holder: %v", heldID, err)
	}
	if !claimed.Changed {
		t.Fatalf("claiming %s reported Changed = false, want a committed claim", heldID)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, heldID, "holder")

	// The fence itself: an unforced transfer away from the live holder is
	// refused, and the refusal writes nothing — not the row, not an event.
	events := newLifecycleUpdateEventCounter(t, ctx, fixture, heldID)
	if _, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateTransferRequest(heldID, "rival", "rival")); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("unforced transfer of %s away from its holder: err = %v, want ErrAlreadyClaimed", heldID, err)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, heldID, "holder")
	events.assert(t, "refused transfer", 0)

	// A stale precondition is orthogonal to the fence and is checked ahead of
	// it, so a request that fails both reports the precondition — the same
	// ordering a forced close-policy crossing gets.
	staleVersion := lifecycleUpdateRow(t, ctx, fixture, heldID).RowVersion - 1
	staleVersionRequest := lifecycleUpdateTransferRequest(heldID, "rival", "rival")
	staleVersionRequest.ExpectedVersion = &staleVersion
	if _, err := fixture.Lifecycle.Update(ctx, staleVersionRequest); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Fatalf("fenced transfer of %s with a stale version: err = %v, want ErrVersionMismatch", heldID, err)
	}
	staleStatus := types.StatusOpen
	staleStatusRequest := lifecycleUpdateTransferRequest(heldID, "rival", "rival")
	staleStatusRequest.ExpectedStatus = &staleStatus
	if _, err := fixture.Lifecycle.Update(ctx, staleStatusRequest); !errors.Is(err, storage.ErrStatusMismatch) {
		t.Fatalf("fenced transfer of %s with a stale status: err = %v, want ErrStatusMismatch", heldID, err)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, heldID, "holder")

	// Restating the holder's own name is not a transfer, so a third party may
	// do it unforced — and it changes nothing.
	reassert, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateTransferRequest(heldID, "bystander", "holder"))
	if err != nil {
		t.Fatalf("reassert %s's current assignee: %v", heldID, err)
	}
	if reassert.Changed {
		t.Errorf("reasserting %s's current assignee reported Changed = true, want a no-op", heldID)
	}

	// An ExpectedAssignee compare-and-set naming the holder replaces the fence:
	// the caller proved its view of the claim is current.
	casRequest := lifecycleUpdateTransferRequest(heldID, "rival", "rival")
	holder := "holder"
	casRequest.ExpectedAssignee = &holder
	cas, err := fixture.Lifecycle.Update(ctx, casRequest)
	if err != nil {
		t.Fatalf("compare-and-set transfer of %s: %v", heldID, err)
	}
	if !cas.Changed || cas.Issue.Assignee != "rival" {
		t.Fatalf("compare-and-set transfer of %s = %#v, want a committed transfer to rival", heldID, cas.Issue)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, heldID, "rival")

	// ForceAssigneeTransfer is the unconditional override.
	forcedRequest := lifecycleUpdateTransferRequest(heldID, "usurper", "usurper")
	forcedRequest.ForceAssigneeTransfer = true
	forced, err := fixture.Lifecycle.Update(ctx, forcedRequest)
	if err != nil {
		t.Fatalf("forced transfer of %s: %v", heldID, err)
	}
	if !forced.Changed || forced.Issue.Assignee != "usurper" {
		t.Fatalf("forced transfer of %s = %#v, want a committed transfer to usurper", heldID, forced.Issue)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, heldID, "usurper")

	// A holder that is a configured claim.pools alias is a group placeholder,
	// not an owner, so taking work from the pool needs no force.
	if err := fixture.SetConfig(ctx, "claim.pools", "lup-pool-crew"); err != nil {
		t.Fatalf("SetConfig(claim.pools): %v", err)
	}
	pooledID := fixture.IssuePrefix + "-lup-xferfence-pooled"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(pooledID))
	pooledRequest := lifecycleUpdateTransferRequest(pooledID, "seed", "lup-pool-crew")
	pooledRequest.Claim = true
	if _, err := fixture.Lifecycle.Update(ctx, pooledRequest); err != nil {
		t.Fatalf("assign %s to the pool: %v", pooledID, err)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, pooledID, "lup-pool-crew")
	taken, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateTransferRequest(pooledID, "member", "member"))
	if err != nil {
		t.Fatalf("unforced transfer of pooled %s: %v", pooledID, err)
	}
	if !taken.Changed || taken.Issue.Assignee != "member" {
		t.Fatalf("unforced transfer of pooled %s = %#v, want a committed transfer to member", pooledID, taken.Issue)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, pooledID, "member")

	// The alias set is the only carve-out: a real holder is still fenced while
	// pools are configured.
	if _, err := fixture.Lifecycle.Update(ctx, lifecycleUpdateTransferRequest(pooledID, "rival", "rival")); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("unforced transfer of %s away from a non-pool holder: err = %v, want ErrAlreadyClaimed", pooledID, err)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, pooledID, "member")
}

// RunLifecycleUpdateClaimIsAMutationWhenThePatchRestoresTheRow pins what
// UpdateResult.Changed counts when a claim rides an update: the claim ITSELF is
// the mutation, so a request that grants a lease reports Changed even though the
// patch beside it puts every public field back where it was.
//
// Every other claim assertion in the package claims an unclaimed row with no
// patch, so the field diff alone already reports true and nothing distinguishes
// "the claim is the mutation" from "the fields happened to differ". A backend
// that derived Changed purely from a before/after comparison of the public issue
// — which is exactly how the unit-of-work backend derives it — would answer
// false here and tell a polling caller its claim did nothing.
//
// Both edges are asserted, because a flag with only one is a flag that cannot
// fail on its own claim:
//
//   - The CONTROL runs the same restoring patch WITHOUT the claim and must
//     report false. Without it, a body that hardcoded Changed = true would pass.
//   - The IDEMPOTENT RE-CLAIM by the holder must report false. Without it, a
//     body that reported Changed for any request carrying Claim would pass, and
//     a caller polling for work would see a fresh grant on every call.
//
// THE TWO BACKEND SHAPES NEED OPPOSITE PATCHES, which is why this case carries
// both. The two stores claim FIRST and then diff the patch against the row the
// claim left, so a patch that RESTORES the pre-claim state is a genuine write
// there and would report Changed with the claim accounting removed entirely.
// The unit-of-work backend applies one spec and compares the post-state to the
// PRE-claim snapshot, so a patch that RESTATES the post-claim state is the one
// it would report Changed for anyway. Each patch isolates the claim on the
// backends the other one masks.
//
// Both rows are seeded WITH a started_at. A claim stamps that column on the
// first transition into in_progress, and a stamp landing on an empty column is a
// field difference of its own — enough to report Changed on every backend with
// the claim accounting gone. The precondition is read back rather than assumed,
// because a seed hook that dropped it would leave this case unable to fail on
// its own claim.
//
// THE OTHER HALF IS THE CLAIM'S FOOTPRINT: assignee and status are the only
// public columns it may write. Changed alone cannot say so — a claim that also
// grabbed, say, ownership would report the same true — so every member outside
// the claim's own two is snapshotted and held. The snapshot is taken ONCE,
// before any claim, and asserted after each of them: re-reading it between
// claims would re-anchor it to whatever the last claim wrote. The seeded values
// are all distinct from the actor name and from the empty column a claim would
// otherwise be indistinguishable against.
func RunLifecycleUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	startedAt := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	restoringID := fixture.IssuePrefix + "-lup-claimrestore"
	restatingID := fixture.IssuePrefix + "-lup-claimrestate"
	for _, id := range []string{restoringID, restatingID} {
		seeded := lifecycleUpdateBystanderIssue(id)
		seeded.StartedAt = &startedAt
		seedLifecycleUpdateIssue(t, ctx, fixture, seeded)
		if stamp := lifecycleUpdateRow(t, ctx, fixture, id).StartedAt; stamp == nil || !stamp.UTC().Equal(startedAt) {
			t.Fatalf("seeded started_at for %s = %v, want %v — an empty column would let a claim's own stamp report Changed", id, stamp, startedAt)
		}
	}
	restoringBystanders := lifecycleUpdateRow(t, ctx, fixture, restoringID)
	restatingBystanders := lifecycleUpdateRow(t, ctx, fixture, restatingID)

	// Open and unassigned is the seeded state, so this patch restores it.
	restoring := publicops.IssuePatch{
		Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusOpen},
		Assignee: publicops.Field[string]{Set: true, Value: ""},
	}

	control, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "claimant", IssueID: restoringID, Patch: restoring})
	if err != nil {
		t.Fatalf("restating %s's status and assignee: %v", restoringID, err)
	}
	if control.Changed {
		t.Fatalf("restating %s's status and assignee reported Changed = true, want a no-op: the claim below has to be the only difference", restoringID)
	}

	claiming, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "claimant", IssueID: restoringID, Claim: true, Patch: restoring,
	})
	if err != nil {
		t.Fatalf("claiming update of %s with a restoring patch: %v", restoringID, err)
	}
	if !claiming.Changed {
		t.Errorf("claiming update of %s reported Changed = false, want true: the claim is the mutation", restoringID)
	}
	if claiming.Issue.Status != types.StatusOpen || claiming.Issue.Assignee != "" {
		t.Errorf("claiming update of %s = status %q assignee %q, want the restored open/unassigned state",
			restoringID, claiming.Issue.Status, claiming.Issue.Assignee)
	}
	// The row says the same thing, which is what makes Changed above an answer
	// about the claim rather than about a field the patch failed to restore.
	assertLifecycleUpdateAssigneeAndStatus(t, ctx, fixture, restoringID, "", types.StatusOpen)
	assertLifecycleUpdateBystandersUnmoved(t, ctx, fixture, restoringID, "after the restoring claim", restoringBystanders)

	// The mirror shape. There is no control for it — the same patch without a
	// claim moves an unclaimed row for real — so it leans on the control above
	// for the "reports Changed for everything" direction and carries only the
	// claim's own arm.
	restating := publicops.IssuePatch{
		Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		Assignee: publicops.Field[string]{Set: true, Value: "claimant"},
	}
	restated, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "claimant", IssueID: restatingID, Claim: true, Patch: restating,
	})
	if err != nil {
		t.Fatalf("claiming update of %s with a restating patch: %v", restatingID, err)
	}
	if !restated.Changed {
		t.Errorf("claiming update of %s reported Changed = false, want true: the claim is the mutation", restatingID)
	}
	assertLifecycleUpdateAssigneeAndStatus(t, ctx, fixture, restatingID, "claimant", types.StatusInProgress)
	assertLifecycleUpdateBystandersUnmoved(t, ctx, fixture, restatingID, "after the restating claim", restatingBystanders)

	// The other edge, on the row the first shape left open and unassigned. A
	// first claim grants the lease and counts.
	granted, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "claimant", IssueID: restoringID, Claim: true})
	if err != nil {
		t.Fatalf("claim %s: %v", restoringID, err)
	}
	if !granted.Changed {
		t.Errorf("claiming unclaimed %s reported Changed = false, want a committed claim", restoringID)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, restoringID, "claimant")

	// The same actor re-claiming its own live claim grants nothing, so it does
	// not count.
	regranted, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "claimant", IssueID: restoringID, Claim: true})
	if err != nil {
		t.Fatalf("re-claim %s: %v", restoringID, err)
	}
	if regranted.Changed {
		t.Errorf("re-claiming %s as its own holder reported Changed = true, want a no-op", restoringID)
	}
	assertLifecycleUpdateLiveAssignee(t, ctx, fixture, restoringID, "claimant")
	// Still the pre-claim snapshot, three claims later.
	assertLifecycleUpdateBystandersUnmoved(t, ctx, fixture, restoringID, "after the bare claim and re-claim", restoringBystanders)
}

// RunLifecycleUpdateParentIDReplacesTheParentEdge pins what a set
// IssuePatch.ParentID does (issueops/issueops.go:144-147): a nonempty value
// replaces the parent with exactly that target and "does not inherit labels" —
// the create-time InheritLabelsFromParent behavior must NOT follow a reparent —
// and a set empty value removes the parent-child edge. Both restatements are
// no-ops.
//
// The label clause is asserted nowhere else, and the unit-of-work backend
// reparents through its own use case (internal/storage/domain/dependency.go:296)
// rather than the shared target-set body the two stores share.
func RunLifecycleUpdateParentIDReplacesTheParentEdge(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.AddDependency == nil || fixture.ListDependencies == nil {
		t.Skip("fixture cannot seed or read an edge: AddDependency or ListDependencies is nil, so a reparent has no observable parent set")
	}

	oldParentID := fixture.IssuePrefix + "-lup-reparent-old"
	newParentID := fixture.IssuePrefix + "-lup-reparent-new"
	childID := fixture.IssuePrefix + "-lup-reparent-child"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(oldParentID))
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(newParentID, "lup-parent-only-label"))
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(childID))
	seedLifecycleUpdateEdge(t, ctx, fixture, childID, oldParentID, types.DepParentChild)
	assertLifecycleUpdateParents(t, ctx, fixture, childID, "seeded", oldParentID)

	reparented, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: newParentID},
	}})
	if err != nil {
		t.Fatalf("reparent %s: %v", childID, err)
	}
	if !reparented.Changed {
		t.Errorf("reparenting %s reported Changed = false, want a committed edit", childID)
	}
	assertLifecycleUpdateParents(t, ctx, fixture, childID, "after reparent", newParentID)
	assertLifecycleUpdateLabels(t, "reparent result", reparented.Issue)
	assertLifecycleUpdateLabels(t, "stored row after reparent", lifecycleUpdateRow(t, ctx, fixture, childID))

	restated, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: newParentID},
	}})
	if err != nil {
		t.Fatalf("restate %s's parent: %v", childID, err)
	}
	if restated.Changed {
		t.Errorf("restating %s's parent reported Changed = true, want a no-op", childID)
	}
	assertLifecycleUpdateParents(t, ctx, fixture, childID, "after restated parent", newParentID)

	cleared, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: ""},
	}})
	if err != nil {
		t.Fatalf("clear %s's parent: %v", childID, err)
	}
	if !cleared.Changed {
		t.Errorf("clearing %s's parent reported Changed = false, want a committed edit", childID)
	}
	assertLifecycleUpdateParents(t, ctx, fixture, childID, "after cleared parent")

	recleared, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: ""},
	}})
	if err != nil {
		t.Fatalf("re-clear %s's parent: %v", childID, err)
	}
	if recleared.Changed {
		t.Errorf("re-clearing %s's parent reported Changed = true, want a no-op", childID)
	}
}

// RunLifecycleUpdateParentIDReplacesEveryParent pins the word ALL in the leaf's
// ParentID clause (issueops/issueops.go:144-147): a set nonempty value
// "atomically replaces all parents with exactly that target". A child can carry
// more than one parent edge — nothing in the schema forbids a second — so "all"
// is a load-bearing word and not a restatement of the single-parent case.
func RunLifecycleUpdateParentIDReplacesEveryParent(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.AddDependency == nil || fixture.ListDependencies == nil {
		t.Skip("fixture cannot seed or read an edge: AddDependency or ListDependencies is nil, so the parent SET a replacement leaves behind is unobservable")
	}

	firstID := fixture.IssuePrefix + "-lup-multiparent-first"
	secondID := fixture.IssuePrefix + "-lup-multiparent-second"
	thirdID := fixture.IssuePrefix + "-lup-multiparent-third"
	childID := fixture.IssuePrefix + "-lup-multiparent-child"
	for _, id := range []string{firstID, secondID, thirdID, childID} {
		seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))
	}
	seedLifecycleUpdateEdge(t, ctx, fixture, childID, firstID, types.DepParentChild)
	seedLifecycleUpdateEdge(t, ctx, fixture, childID, secondID, types.DepParentChild)
	assertLifecycleUpdateParents(t, ctx, fixture, childID, "seeded with two parents", firstID, secondID)

	replaced, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: thirdID},
	}})
	if err != nil {
		t.Fatalf("replace every parent of %s: %v", childID, err)
	}
	if !replaced.Changed {
		t.Errorf("replacing every parent of %s reported Changed = false, want a committed edit", childID)
	}
	// The whole SET, not a membership probe: a body that added the new parent
	// beside the old ones satisfies "the new parent is there" and fails here.
	assertLifecycleUpdateParents(t, ctx, fixture, childID, "after replacing every parent", thirdID)
}

// RunLifecycleUpdatePersistentPreservesUnversionedClass pins the half of the
// Persistence clause nobody else asserts: "Persistent preserves an existing
// durable unversioned class" (issueops/issueops.go:135-136). The refusal beside
// it — an unversioned row cannot be demoted to a wisp mode — is pinned
// elsewhere; this clause says the legal direction is a no-op that does NOT
// normalize the row into versioned storage.
//
// WHAT A PERSISTENCE RESTATEMENT MUST NOT DO is where the two observations come
// in. A persistence MOVE deletes the row from one plane and re-inserts it into
// the other, so "the row is still where it was" needs both halves: the durable
// read has to answer it (a moved row would come back ephemeral, or not at all),
// AND the ephemeral plane has to be empty at that id (a copy that forgot its
// delete leaves a row a both-plane read never reaches). GetWisp is what makes
// the second half expressible without a per-plane row count.
func RunLifecycleUpdatePersistentPreservesUnversionedClass(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-unversioned"
	seeded := lifecycleUpdateIssue(id)
	seeded.StorageClass = types.StorageClassUnversioned
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)
	if got := lifecycleUpdateRow(t, ctx, fixture, id).StorageClass; got != types.StorageClassUnversioned {
		t.Fatalf("seeded storage class for %s = %q, want %q — the clause has nothing to preserve otherwise", id, got, types.StorageClassUnversioned)
	}

	restated, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModePersistent},
	}})
	if err != nil {
		t.Fatalf("restate %s as persistent: %v", id, err)
	}
	if restated.Changed {
		t.Errorf("restating unversioned %s as persistent reported Changed = true, want a no-op", id)
	}

	after := lifecycleUpdateRow(t, ctx, fixture, id)
	if after.StorageClass != types.StorageClassUnversioned {
		t.Errorf("%s storage class after a persistent restatement = %q, want it preserved at %q — persistent must not normalize the row into versioned storage",
			id, after.StorageClass, types.StorageClassUnversioned)
	}
	if after.Ephemeral || after.NoHistory {
		t.Errorf("%s came back {ephemeral %v, no-history %v} after a persistent restatement, want a durable-plane row", id, after.Ephemeral, after.NoHistory)
	}
	assertLifecycleUpdateWispAbsent(t, ctx, fixture, id, "after the persistent restatement")
}

// RunLifecycleUpdateProvenanceLabelsHistory pins UpdateRequest.Provenance
// against the history the backend actually writes
// (issueops/issueops.go:261-270): the entry reads as the caller's own string,
// and the label "NEVER changes WHETHER history is recorded" — an update that
// records one records one with the field empty, and one that records none
// records none with it set.
//
// The claim "the entry reads as the caller's string" can only be settled
// against a real version log, which is why the message-scoped hook exists; a
// backend that cannot observe one SKIPS rather than passing on the count alone.
func RunLifecycleUpdateProvenanceLabelsHistory(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lup-provenance"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))

	const label = "conformance: lifecycle update provenance label"
	history := newLifecycleUpdateHistoryCounter(t, ctx, fixture)
	labeled, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: id, Provenance: label,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "labeled title"}},
	})
	if err != nil {
		t.Fatalf("labeled update of %s: %v", id, err)
	}
	if !labeled.Changed {
		t.Fatalf("labeled update of %s reported Changed = false, want a durable mutation to label", id)
	}
	history.assertTotal(t, "labeled update", 1)
	history.assertMessage(t, "labeled update", label, 1)

	// The label decides how the entry reads, never whether one exists: a no-op
	// update carrying it records nothing.
	const noopLabel = "conformance: lifecycle update provenance no-op"
	noOp, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: id, Provenance: noopLabel,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "labeled title"}},
	})
	if err != nil {
		t.Fatalf("no-op labeled update of %s: %v", id, err)
	}
	if noOp.Changed {
		t.Fatalf("restating %s's title reported Changed = true, want a no-op", id)
	}
	history.assertTotal(t, "no-op labeled update", 0)
	history.assertMessage(t, "no-op labeled update", noopLabel, 0)

	// And an update with no label still records its one entry, under whatever
	// default the implementation picked.
	unlabeled, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: id,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "unlabeled title"}},
	})
	if err != nil {
		t.Fatalf("unlabeled update of %s: %v", id, err)
	}
	if !unlabeled.Changed {
		t.Fatalf("unlabeled update of %s reported Changed = false, want a durable mutation", id)
	}
	history.assertTotal(t, "unlabeled update", 1)
	history.assertMessage(t, "unlabeled update", label, 1)
}

// lifecycleUpdateInvalidPriority is outside the canonical P0-P4 range
// types.ValidateIssuePriority enforces, so a patch carrying it is a
// deterministic validation failure whatever else the patch asks for.
const lifecycleUpdateInvalidPriority = 9

func lifecycleUpdateIssue(id string, labels ...string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    labels,
	}
}

func seedLifecycleUpdateIssue(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func seedLifecycleUpdateWisp(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, issue *types.Issue) {
	t.Helper()
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", issue.ID, err)
	}
}

func seedLifecycleUpdateEdge(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, from, to string, kind types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: kind}, "seed"); err != nil {
		t.Fatalf("seed %s %s -> %s: %v", kind, from, to, err)
	}
}

func lifecycleUpdateRow(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id string) *types.Issue {
	t.Helper()
	issue, err := fixture.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	if issue == nil {
		t.Fatalf("read back %s: no row", id)
	}
	return issue
}

// assertLifecycleUpdateRowUnchanged checks that every member a refusal or a
// no-op could have written is where a previous read left it, RowVersion
// included: a rewritten version is a write that happened even when the values
// came out the same.
//
// The assignee and the closed-at stamp are here because a compound request can
// carry a claim and a done-category crossing beside the patch, and a refusal has
// to leave those inert too — the two columns a partial write would show up in
// that no content member covers.
func assertLifecycleUpdateRowUnchanged(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id, label string, want *types.Issue) {
	t.Helper()
	got := lifecycleUpdateRow(t, ctx, fixture, id)
	if got.RowVersion != want.RowVersion {
		t.Errorf("%s %s row version = %d, want it unchanged at %d — something was written", id, label, got.RowVersion, want.RowVersion)
	}
	for _, field := range []struct {
		name      string
		got, want string
	}{
		{"title", got.Title, want.Title},
		{"description", got.Description, want.Description},
		{"design", got.Design, want.Design},
		{"acceptance criteria", got.AcceptanceCriteria, want.AcceptanceCriteria},
		{"notes", got.Notes, want.Notes},
		{"status", string(got.Status), string(want.Status)},
		{"issue type", string(got.IssueType), string(want.IssueType)},
		{"assignee", got.Assignee, want.Assignee},
		{"closed at", lifecycleUpdateStamp(got.ClosedAt), lifecycleUpdateStamp(want.ClosedAt)},
	} {
		if field.got != field.want {
			t.Errorf("%s %s %s = %q, want it unchanged at %q", id, label, field.name, field.got, field.want)
		}
	}
	if got.Priority != want.Priority {
		t.Errorf("%s %s priority = %d, want it unchanged at %d", id, label, got.Priority, want.Priority)
	}
}

// lifecycleUpdateStamp renders a nullable timestamp for comparison, spelling an
// unset one as the empty string the way the column reads when it is NULL.
func lifecycleUpdateStamp(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// lifecycleUpdateClosingRequest builds the generic status update that crosses
// into the done category — the operation whose policy the close-policy case
// pins.
func lifecycleUpdateClosingRequest(id string, force bool) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:            "writer",
		IssueID:          id,
		ForceClosePolicy: force,
		Patch:            publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}},
	}
}

// lifecycleUpdateTransferRequest builds the bare assignee edit whose fencing the
// transfer case pins: actor asks for the issue to be assigned to newAssignee.
func lifecycleUpdateTransferRequest(id, actor, newAssignee string) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:   actor,
		IssueID: id,
		Patch:   publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: newAssignee}},
	}
}

// lifecycleUpdateBystanderIssue seeds an issue whose every non-claim member
// carries a distinct, non-empty value. None of them is anything a claim could
// write by coincidence: not the actor, not a status, not empty.
func lifecycleUpdateBystanderIssue(id string) *types.Issue {
	minutes := 7
	ref := "lup-claim-ref"
	due := time.Date(2033, 9, 8, 7, 6, 5, 0, time.UTC)
	deferUntil := time.Date(2033, 8, 7, 6, 5, 4, 0, time.UTC)
	issue := lifecycleUpdateIssue(id)
	issue.Description = "claim description"
	issue.Design = "claim design"
	issue.AcceptanceCriteria = "claim acceptance"
	issue.Notes = "claim notes"
	issue.SpecID = "claim-spec"
	issue.AwaitID = "claim-await"
	issue.Owner = "claim-owner"
	issue.ClosedBySession = "claim-session"
	issue.EstimatedMinutes = &minutes
	issue.ExternalRef = &ref
	issue.DueAt = &due
	issue.DeferUntil = &deferUntil
	return issue
}

// assertLifecycleUpdateBystandersUnmoved holds every member outside a claim's
// own two — assignee and status — against a snapshot taken before any claim.
// started_at is included: a claim stamps it on the first transition into
// in_progress, and this case seeds it so that stamp is not a difference of its
// own.
func assertLifecycleUpdateBystandersUnmoved(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id, label string, want *types.Issue) {
	t.Helper()
	got := lifecycleUpdateRow(t, ctx, fixture, id)
	for _, field := range []struct {
		name      string
		got, want string
	}{
		{"title", got.Title, want.Title},
		{"description", got.Description, want.Description},
		{"design", got.Design, want.Design},
		{"acceptance criteria", got.AcceptanceCriteria, want.AcceptanceCriteria},
		{"notes", got.Notes, want.Notes},
		{"spec id", got.SpecID, want.SpecID},
		{"await id", got.AwaitID, want.AwaitID},
		{"issue type", string(got.IssueType), string(want.IssueType)},
		{"owner", got.Owner, want.Owner},
		{"closed by session", got.ClosedBySession, want.ClosedBySession},
		{"external ref", issueTextOrEmpty(got.ExternalRef), issueTextOrEmpty(want.ExternalRef)},
		{"due at", lifecycleUpdateStamp(got.DueAt), lifecycleUpdateStamp(want.DueAt)},
		{"defer until", lifecycleUpdateStamp(got.DeferUntil), lifecycleUpdateStamp(want.DeferUntil)},
		{"started at", lifecycleUpdateStamp(got.StartedAt), lifecycleUpdateStamp(want.StartedAt)},
		{"metadata", string(got.Metadata), string(want.Metadata)},
	} {
		if field.got != field.want {
			t.Errorf("%s %s %s = %q, want the pre-claim %q — a claim writes the assignee and the status, nothing else",
				id, label, field.name, field.got, field.want)
		}
	}
	if got.Priority != want.Priority {
		t.Errorf("%s %s priority = %d, want the pre-claim %d", id, label, got.Priority, want.Priority)
	}
	gotMinutes, wantMinutes := 0, 0
	if got.EstimatedMinutes != nil {
		gotMinutes = *got.EstimatedMinutes
	}
	if want.EstimatedMinutes != nil {
		wantMinutes = *want.EstimatedMinutes
	}
	if gotMinutes != wantMinutes {
		t.Errorf("%s %s estimated minutes = %d, want the pre-claim %d", id, label, gotMinutes, wantMinutes)
	}
}

// issueTextOrEmpty spells an unset string pointer as the empty string, which is
// how the column reads when it is NULL.
func issueTextOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// assertLifecycleUpdateStatus reads one row's stored status back.
func assertLifecycleUpdateStatus(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id string, want types.Status) {
	t.Helper()
	if got := lifecycleUpdateRow(t, ctx, fixture, id).Status; got != want {
		t.Errorf("%s status = %q, want %q", id, got, want)
	}
}

// assertLifecycleUpdateAssigneeAndStatus reads back the two columns a claim
// writes, for the refusals that must leave both alone.
func assertLifecycleUpdateAssigneeAndStatus(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id, wantAssignee string, wantStatus types.Status) {
	t.Helper()
	got := lifecycleUpdateRow(t, ctx, fixture, id)
	if got.Assignee != wantAssignee {
		t.Errorf("%s assignee = %q, want %q", id, got.Assignee, wantAssignee)
	}
	if got.Status != wantStatus {
		t.Errorf("%s status = %q, want %q", id, got.Status, wantStatus)
	}
}

// assertLifecycleUpdateLiveAssignee checks the stored holder of an in-progress
// issue. Every state the transfer fence speaks over is a LIVE claim, so the
// expected status is fixed, and reading it back proves an assignee edit leaves
// the claim's status alone.
func assertLifecycleUpdateLiveAssignee(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id, wantAssignee string) {
	t.Helper()
	assertLifecycleUpdateAssigneeAndStatus(t, ctx, fixture, id, wantAssignee, types.StatusInProgress)
}

// assertLifecycleUpdateParents reads one row's outgoing parent-child edges as a
// SET and compares it whole.
func assertLifecycleUpdateParents(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id, label string, want ...string) {
	t.Helper()
	edges, err := fixture.ListDependencies(ctx, id)
	if err != nil {
		t.Fatalf("read the edges of %s (%s): %v", id, label, err)
	}
	var parents []string
	for _, edge := range edges {
		if edge != nil && edge.Type == types.DepParentChild {
			parents = append(parents, edge.DependsOnID)
		}
	}
	sort.Strings(parents)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if len(parents) != len(sorted) {
		t.Errorf("%s %s parents = %v, want %v", id, label, parents, want)
		return
	}
	for i := range parents {
		if parents[i] != sorted[i] {
			t.Errorf("%s %s parents = %v, want %v", id, label, parents, want)
			return
		}
	}
}

// assertLifecycleUpdateMetadata compares a metadata document rather than its
// bytes, because the three fixtures do not agree on whitespace or key order.
func assertLifecycleUpdateMetadata(t *testing.T, label string, issue *types.Issue, want string) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s = nil, want the issue carrying the metadata", label)
	}
	stored := issue.Metadata
	if len(stored) == 0 {
		stored = json.RawMessage(`{}`)
	}
	var got, expected any
	if err := json.Unmarshal(stored, &got); err != nil {
		t.Fatalf("%s metadata %s: %v", label, stored, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("%s want metadata %s: %v", label, want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("%s metadata = %s, want %s", label, stored, want)
	}
}

// assertLifecycleUpdateWispAbsent checks the EPHEMERAL plane holds no row at id.
// It is the half a both-plane read cannot make: that read resolves the durable
// row first, so a stray wisp under the same id never reaches it.
func assertLifecycleUpdateWispAbsent(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id, label string) {
	t.Helper()
	if fixture.WispExists == nil {
		t.Logf("fixture has no WispExists: %s %s is DROPPED — the class-preservation clause above still "+
			"runs, but a restatement that copied the row into the wisp plane would be invisible", id, label)
		return
	}
	exists, err := fixture.WispExists(ctx, id)
	if err != nil {
		t.Fatalf("probe the ephemeral plane at %s (%s): %v", id, label, err)
	}
	if exists {
		t.Errorf("%s %s: the ephemeral plane holds a row at this id, want none", id, label)
	}
}

// lifecycleUpdateEventCounter reports how many event rows each operation adds
// for one issue. A nil counter is the "this backend cannot observe its event
// journal" case: every method is a no-op, so a case keeps its row assertions
// and drops only the journal half.
// It counts the journal WHOLE rather than by type, because every case that uses
// it is asking the same question — "did this refusal append anything at all" —
// and a per-type count answers a weaker one: a body that emitted the wrong event
// type would satisfy a check that only looked at the type it expected.
type lifecycleUpdateEventCounter struct {
	ctx     context.Context
	fixture LifecycleUpdateFixture
	id      string
	total   int
}

func newLifecycleUpdateEventCounter(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture, id string) *lifecycleUpdateEventCounter {
	t.Helper()
	if fixture.ListEvents == nil {
		// Loudly, not silently. The case still runs and still proves what the
		// row can say, but "the refusal appended no event" is not among it, and
		// a reader of a green run has to be told which half they bought.
		t.Logf("fixture has no ListEvents: this backend cannot observe its event journal, so %s's "+
			"event-delta assertions are DROPPED and only the row half of \"the refusal wrote nothing\" runs", id)
		return nil
	}
	counter := &lifecycleUpdateEventCounter{ctx: ctx, fixture: fixture, id: id}
	counter.total = counter.read(t)
	return counter
}

func (c *lifecycleUpdateEventCounter) read(t *testing.T) int {
	t.Helper()
	events, err := c.fixture.ListEvents(c.ctx, c.id)
	if err != nil {
		t.Fatalf("read the event journal of %s: %v", c.id, err)
	}
	return len(events)
}

// assert checks the rows added since the previous assert and re-baselines.
func (c *lifecycleUpdateEventCounter) assert(t *testing.T, label string, wantTotal int) {
	t.Helper()
	if c == nil {
		return
	}
	total := c.read(t)
	if got := total - c.total; got != wantTotal {
		t.Errorf("%s wrote %d event rows, want %d", label, got, wantTotal)
	}
	c.total = total
}

// lifecycleUpdateHistoryCounter reports how many version-log entries each
// operation adds. It takes deltas rather than reading the top of the log because
// two commits made inside one second tie on date, so their relative order is not
// something to assert on.
type lifecycleUpdateHistoryCounter struct {
	ctx     context.Context
	fixture LifecycleUpdateFixture
	total   int
}

// newLifecycleUpdateHistoryCounter is the single choke point for the fixture's
// history hook, so it is also where a backend that cannot observe history skips
// LOUDLY rather than passing quietly.
func newLifecycleUpdateHistoryCounter(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) *lifecycleUpdateHistoryCounter {
	t.Helper()
	if fixture.CountHistoryMatching == nil {
		t.Skip("fixture has no CountHistoryMatching: this backend cannot observe history, so issueops.go:261-270 is UNPINNED here")
	}
	counter := &lifecycleUpdateHistoryCounter{ctx: ctx, fixture: fixture}
	counter.total = counter.count(t, "")
	return counter
}

// count answers the entries carrying message exactly, or every entry when
// message is empty.
func (c *lifecycleUpdateHistoryCounter) count(t *testing.T, message string) int {
	t.Helper()
	pattern := ""
	if message != "" {
		pattern = historyPatternForExactMessage(t, message)
	}
	got, err := c.fixture.CountHistoryMatching(c.ctx, pattern)
	if err != nil {
		t.Fatalf("count history entries (%q): %v", message, err)
	}
	return got
}

// assertTotal checks the entries added since the previous assertTotal and
// re-baselines.
func (c *lifecycleUpdateHistoryCounter) assertTotal(t *testing.T, label string, want int) {
	t.Helper()
	total := c.count(t, "")
	if got := total - c.total; got != want {
		t.Errorf("%s recorded %d history entries, want %d", label, got, want)
	}
	c.total = total
}

// assertMessage checks how many entries carry an exact message, which is the
// only way to tell the caller's spelling from the implementation's default.
func (c *lifecycleUpdateHistoryCounter) assertMessage(t *testing.T, label, message string, want int) {
	t.Helper()
	if got := c.count(t, message); got != want {
		t.Errorf("%s left %d history entries reading %q, want %d", label, got, message, want)
	}
}

func assertLifecycleUpdateContent(t *testing.T, label string, issue *types.Issue) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s = nil, want the patched issue", label)
	}
	for _, field := range []struct {
		name      string
		got, want string
	}{
		{"title", issue.Title, "patched title"},
		{"description", issue.Description, "patched description"},
		{"design", issue.Design, "patched design"},
		{"acceptance criteria", issue.AcceptanceCriteria, "patched acceptance"},
		{"notes", issue.Notes, "patched notes"},
		{"issue type", string(issue.IssueType), string(types.TypeBug)},
	} {
		if field.got != field.want {
			t.Errorf("%s %s = %q, want %q", label, field.name, field.got, field.want)
		}
	}
	if issue.Priority != 0 {
		t.Errorf("%s priority = %d, want 0 — a zero value a caller set is still a value", label, issue.Priority)
	}
}

func assertLifecycleUpdateCleared(t *testing.T, label string, issue *types.Issue) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s = nil, want the cleared issue", label)
	}
	if issue.EstimatedMinutes != nil {
		t.Errorf("%s estimated minutes = %d, want it cleared", label, *issue.EstimatedMinutes)
	}
	if issue.ExternalRef != nil {
		t.Errorf("%s external ref = %q, want it cleared", label, *issue.ExternalRef)
	}
	if issue.DueAt != nil {
		t.Errorf("%s due at = %v, want it cleared", label, *issue.DueAt)
	}
	if issue.DeferUntil != nil {
		t.Errorf("%s defer until = %v, want it cleared", label, *issue.DeferUntil)
	}
}

func assertLifecycleUpdateLabels(t *testing.T, label string, issue *types.Issue, want ...string) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s = nil, want the labeled issue", label)
	}
	got := append([]string(nil), issue.Labels...)
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if len(got) != len(sorted) {
		t.Errorf("%s labels = %v, want %v", label, issue.Labels, want)
		return
	}
	for i := range got {
		if got[i] != sorted[i] {
			t.Errorf("%s labels = %v, want %v", label, issue.Labels, want)
			return
		}
	}
}

func lifecycleUpdateHasLabel(labels []string, want string) bool {
	for _, name := range labels {
		if name == want {
			return true
		}
	}
	return false
}

func lifecycleUpdateHasEdge(dependencies []*types.Dependency, wantTarget string) bool {
	for _, dependency := range dependencies {
		if dependency != nil && dependency.DependsOnID == wantTarget {
			return true
		}
	}
	return false
}
