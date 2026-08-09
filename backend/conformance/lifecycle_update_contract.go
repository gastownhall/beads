package conformance

import (
	"context"
	"errors"
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
// SCOPE: the cases below exercise only the UpdateRequest and IssuePatch members
// a backend can express without a private extension — title, description,
// design, acceptance criteria, notes, append_notes, priority, issue type, the
// label set, the four nullable members, the actor and the id. Everything the
// staging cases own that needs more than that — Metadata, Status and close
// policy, Assignee and the transfer fence, Claim, ParentID, Persistence, the
// Expected* preconditions, Provenance — is deliberately absent: those have
// owning cases already, and pinning them here would only make the block
// unadoptable for the backends it exists to serve.
//
// DELIBERATE OVERLAP, named rather than hidden. The plane runner re-pins what
// RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps asserts. The value is
// reachability, not a second opinion: that case seeds its wisp through
// Operations.Create and reads its post-state with QueryScalar, so it is exactly
// the shape a partial backend cannot run. Everything else here — patch
// persistence and the hydrated result, Changed:false on a same-value patch,
// append_notes against a notes replacement, the nullable clears, label replace
// at the role seam, the unknown-id refusal for a plain Update, the actorless
// refusal, and a refused patch writing no member — has no owning proof
// anywhere in the package.
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
	// half, the way ClaimerFixture.CountClaimEvents drops its event check.
	AddDependency func(context.Context, *types.Dependency, string) error
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
	} {
		if field.got != field.want {
			t.Errorf("%s %s %s = %q, want it unchanged at %q", id, label, field.name, field.got, field.want)
		}
	}
	if got.Priority != want.Priority {
		t.Errorf("%s %s priority = %d, want it unchanged at %d", id, label, got.Priority, want.Priority)
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
