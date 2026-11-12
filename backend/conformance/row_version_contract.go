package conformance

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file pins what every mutation kind the roles make does to the row's
// revision token — types.Issue.RowVersion, the issues/wisps row_lock cell, the
// wire's `revision`: it ADVANCES or it HOLDS.
//
// THE TOKEN IS WHAT expected_version COMPARES. Every guarded write in the role
// tier (UpdateRequest/CloseRequest/ReopenRequest/DeleteRequest.ExpectedVersion,
// a batch apply item's ExpectedVersion, `bd ... --if-revision`, the v0 wire's
// `expected_version`) refuses exactly when the token moved since the caller
// read it. A kind that advances it makes every token read before it stale; a
// kind that holds it lets that token keep passing. That is the contract a
// caller composes against, and before this file no role-tier case stated it
// per kind: the CLI tier pins six kinds through `bd`
// (cmd/bd/if_revision_embedded_test.go, TestEmbeddedRevisionRemintMatrix),
// and the reopen and label-only answers were documented one way
// (types.Issue.RowVersion, the freshRowLock invariant in
// internal/storage/issueops/lease.go) and coded the other.
//
// THE RULING THE TABLE PINS is the CODE as it behaves on main, where the code
// and the docs disagreed: reopen re-mints and an unguarded label-only write
// does not. Every other answer was determined by reading the two bodies and
// running all four legs. A row that flips is a semantic change for every
// token holder and lands as one: the table row, types.Issue.RowVersion and the
// freshRowLock invariant change in the same commit.
//
// HOW MANY VOTES THE LEGS ARE: two bodies. The dolt and embedded stores, and
// the reference store behind the http leg's in-process bd serve, run
// internal/storage/issueops; the unit-of-work leg runs domain/db (which reuses
// issueops' row_lock helpers and some of its bodies, reopen and the metadata
// swap among them). The http leg adds the wire: its writes go through the
// client and its token comes back as the wire's `revision`, so it is the leg
// that proves a v0 caller sees what the store did.
//
// EVERY ROW PROVES ITS WRITE LANDED. A HOLD answer over a mutation that never
// happened proves nothing, so each act asserts the role's own report of the
// write (Changed, Swapped, Rearmed, Removed, the stored comment) before the
// token is compared, and the three is_blocked rows prove the recompute flipped
// the flag.
//
// THE TWO CLASSES KEEP THE READ HONEST. A GetIssue that answered a stale token
// would pass every HOLD row and fail every ADVANCE row; one that minted a new
// value per read would do the reverse. Neither can pass the table.
//
// FALSIFIED, both bodies at once (the PR that added this file records the
// diffs): a label-only patch that also rewrites row_lock — in issueops
// applyLabelPatch AND in domain/db's label Insert/Delete — fails the five
// label-only rows (UpdateLabelAdd, UpdateLabelRemove, UpdateLabelReplace,
// UpdateLabelOnlyGuardedByExpectedVersion, BatchApplyLabelOnlyUpdateItem) and
// RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion on all four legs; dropping row_lock from reopen's UPDATE (issueops/reopen.go, the
// one body every leg shares) fails the Reopen row on all four legs. Mutating
// the issueops label path alone leaves the unit-of-work leg green: it is the
// other vote.

// RowVersionFixture supplies the roles whose writes the matrix classifies, the
// out-of-band seeding hooks, and the read the token is taken from.
//
// SEEDING NEVER GOES THROUGH A SUBJECT. CreateIssue, AddDependency and
// SeedClaimer establish preconditions; the eight role fields are the mutations
// under test. On the store legs both sides are the same store; on the http leg
// the subjects are the client and the seeds are the reference store the server
// serves.
type RowVersionFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string

	// The subjects. A nil one SKIPS, by name, exactly the rows that write
	// through it.
	Lifecycle        publicops.Lifecycle
	MetadataCAS      publicops.MetadataCAS
	Claimer          publicops.Claimer
	Releaser         publicops.Releaser
	Heartbeater      publicops.Heartbeater
	DependencyEditor publicops.DependencyEditor
	Commenter        publicops.Commenter
	BatchApplier     publicops.BatchApplier

	// CreateIssue seeds a durable issue in the issues plane with its labels,
	// status, assignee, started_at and closed_at as given. Required.
	CreateIssue func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge out of band, settling the source's blocked
	// state as every store write does. Nil skips the two rows that need a
	// seeded edge.
	AddDependency func(context.Context, *types.Dependency, string) error
	// SeedClaimer takes the claim, and with it the lease row, a heartbeat
	// refresh needs. It is a seed, not a subject: on the http leg it is the
	// reference store's Claimer. Nil skips the refresh row.
	SeedClaimer publicops.Claimer

	// GetIssue reads one row back. RowVersion on what it answers is the token
	// this backend's callers compose expected_version from: the store's own
	// read on the store legs, the wire's `revision` on the http leg. Required.
	GetIssue func(context.Context, string) (*types.Issue, error)
	// QueryScalar reads a raw cell. The three rows whose subject is a derived
	// is_blocked recompute use it to prove the flag really flipped, so their
	// HOLD is about a recompute that ran. Nil DROPS that proof with a t.Log;
	// the token comparison still runs.
	QueryScalar func(context.Context, string, []any, ...any) error
}

// rowVersionMove is the ruled effect of one mutation on one watched row.
type rowVersionMove int

const (
	// rowVersionHolds: the token after equals the token before.
	rowVersionHolds rowVersionMove = iota
	// rowVersionAdvances: the token after differs from the token before. It is
	// minted fresh rather than incremented, so "different" is the whole
	// promise.
	rowVersionAdvances
)

func (m rowVersionMove) String() string {
	if m == rowVersionAdvances {
		return "ADVANCE"
	}
	return "HOLD"
}

// rowVersionWatch is one row a mutation kind observes.
type rowVersionWatch struct {
	// as names the row's part in the mutation, for the failure message.
	as   string
	id   string
	want rowVersionMove
}

// rowVersionKind is one row of the matrix.
type rowVersionKind struct {
	name string
	// roles names the fixture fields the row writes through, for the
	// nil-subject skip.
	roles []string
	// why cites the body that produces the ruled answer, so a flipped row
	// names what someone changed.
	why string
	// arrange seeds the preconditions out of band and returns the rows to
	// watch, each with its ruled move.
	arrange func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch
	// act performs the ONE mutation under test through a subject and fails
	// unless the role reports that it landed.
	act func(t *testing.T, ctx context.Context, f RowVersionFixture, base string)
}

const (
	rowVersionActor  = "rv-writer"
	rowVersionHolder = "rv-holder"
)

// rowVersionKinds is the matrix. Each row is independent: it seeds its own ids
// under base and watches only them.
var rowVersionKinds = []rowVersionKind{
	{
		name:  "UpdateScalarField",
		roles: []string{"Lifecycle"},
		why:   "updateIssueInTx (issueops/update.go) and domain/db's Update append a fresh row_lock to every update that survives the no-op filter",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Title: "before"})
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "after"},
			}})
			if !result.Changed || result.Issue == nil || result.Issue.Title != "after" {
				t.Fatalf("title update = %+v, want Changed with the new title", result)
			}
		},
	},
	{
		name:  "UpdateSameValuePatch",
		roles: []string{"Lifecycle"},
		why:   "DiscardNoopIssueUpdates drops a restated value before row_lock is touched, on both bodies",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Title: "same"})
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "same"},
			}})
			if result.Changed {
				t.Fatalf("same-value update = %+v, want Changed false: a restatement is the precondition this HOLD is about", result)
			}
		},
	},
	{
		name:  "UpdateLabelAdd",
		roles: []string{"Lifecycle"},
		why:   "a label-only patch writes the label rows and their events, never the issue row (issueops applyLabelPatch; domain ApplyUpdate's label branch)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Labels: []string{"rv-keep"}})
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				Labels: publicops.LabelPatch{Add: []string{"rv-new"}},
			}})
			rowVersionRequireLabels(t, result, "rv-keep", "rv-new")
		},
	},
	{
		name:  "UpdateLabelRemove",
		roles: []string{"Lifecycle"},
		why:   "a label-only patch writes the label rows and their events, never the issue row",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Labels: []string{"rv-keep", "rv-drop"}})
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				Labels: publicops.LabelPatch{Remove: []string{"rv-drop"}},
			}})
			rowVersionRequireLabels(t, result, "rv-keep")
		},
	},
	{
		name:  "UpdateLabelReplace",
		roles: []string{"Lifecycle"},
		why:   "a label-only patch writes the label rows and their events, never the issue row",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Labels: []string{"rv-old"}})
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"rv-b", "rv-c"}}},
			}})
			rowVersionRequireLabels(t, result, "rv-b", "rv-c")
		},
	},
	{
		// expected_version is the only write guard on main; a guard a later
		// change adds is a different request field and pins its own answer
		// beside this row. The CLI refuses
		// --if-revision beside a label-only edit before the role runs
		// (TestEmbeddedIfRevisionRejectsLabelAndParentEdits); the role and the
		// v0 wire accept it, and this is what they do.
		name:  "UpdateLabelOnlyGuardedByExpectedVersion",
		roles: []string{"Lifecycle"},
		why:   "the version check reads row_lock (CheckVersionInTx; domain ApplyUpdate's guard read) and the label branch still never writes the issue row",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Labels: []string{"rv-keep"}})
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			token := rowVersionRead(t, ctx, f, base)
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, ExpectedVersion: &token, Patch: publicops.IssuePatch{
				Labels: publicops.LabelPatch{Add: []string{"rv-guarded"}},
			}})
			rowVersionRequireLabels(t, result, "rv-guarded", "rv-keep")
		},
	},
	{
		name:  "UpdateParentOnly",
		roles: []string{"Lifecycle"},
		why:   "a ParentID-only patch rewrites parent-child edges and the derived is_blocked cell, never row_lock (issueops applyParentPatch; domain Reparent)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-parent"})
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			return []rowVersionWatch{{"child", base, rowVersionHolds}, {"parent", base + "-parent", rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				ParentID: publicops.Field[string]{Set: true, Value: base + "-parent"},
			}})
			if !result.Changed {
				t.Fatalf("reparent = %+v, want Changed", result)
			}
		},
	},
	{
		name:  "UpdateAppendNotes",
		roles: []string{"Lifecycle"},
		why:   "append_notes resolves to a notes column write inside updateIssueInTx / domain/db's Update, which re-mint",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Notes: "first"})
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result := rowVersionUpdate(t, ctx, f, publicops.UpdateRequest{IssueID: base, Patch: publicops.IssuePatch{
				AppendNotes: publicops.Field[string]{Set: true, Value: "second"},
			}})
			if !result.Changed {
				t.Fatalf("append_notes = %+v, want Changed", result)
			}
		},
	},
	{
		name:  "MetadataCASSwap",
		roles: []string{"MetadataCAS"},
		why:   "a swap that lands rewrites the metadata column through UpdateIssueInTx (issueops/metadata_cas.go, the one body every leg runs), which re-mints",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			value := json.RawMessage(`"rv"`)
			result, err := f.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
				Actor: rowVersionActor, IssueID: base, Key: "rv_key", Value: &value,
			})
			if err != nil || !result.Swapped {
				t.Fatalf("CompareAndSetKey creating an absent key = %+v, %v; want Swapped", result, err)
			}
		},
	},
	{
		name:  "Claim",
		roles: []string{"Claimer"},
		why:   "the claim compare-and-set stamps RowLockClause (issueops/claim.go; domain/db's Claim)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: rowVersionHolder, IssueID: base})
			if err != nil || !result.Changed {
				t.Fatalf("Claim = %+v, %v; want Changed", result, err)
			}
		},
	},
	{
		name:  "ClaimByTheCurrentHolder",
		roles: []string{"Claimer"},
		why:   "an in_progress row the actor already holds is an idempotent success that writes nothing (issueops.Claimer)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeedHeld(t, ctx, f, base)
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: rowVersionHolder, IssueID: base})
			if err != nil || result.Changed {
				t.Fatalf("re-claim by the holder = %+v, %v; want an idempotent Changed false", result, err)
			}
		},
	},
	{
		name:  "Release",
		roles: []string{"Releaser"},
		why:   "the release compare-and-set rewrites status, assignee and started_at with a fresh row_lock (issueops/unclaim.go)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeedHeld(t, ctx, f, base)
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: rowVersionHolder, IssueID: base})
			if err != nil || !result.Changed {
				t.Fatalf("Release = %+v, %v; want Changed", result, err)
			}
		},
	},
	{
		name:  "HeartbeatRefresh",
		roles: []string{"Heartbeater", "SeedClaimer"},
		why:   "a refresh writes only the leases table (issueops.Heartbeater: A REFRESH WRITES ONLY THE LEASE ROW)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			if _, err := f.SeedClaimer.Claim(ctx, publicops.ClaimRequest{Actor: rowVersionHolder, IssueID: base}); err != nil {
				t.Fatalf("seed the claim a refresh needs on %s: %v", base, err)
			}
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: rowVersionHolder, IssueID: base})
			if err != nil || result.Rearmed {
				t.Fatalf("Heartbeat over a live lease = %+v, %v; want a refresh (Rearmed false)", result, err)
			}
		},
	},
	{
		name:  "HeartbeatRearm",
		roles: []string{"Heartbeater"},
		why:   "a re-arm is recorded as a mutation and remints row_lock (issueops.Heartbeater: A RE-ARM IS RECORDED AS A MUTATION)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeedHeld(t, ctx, f, base)
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: rowVersionHolder, IssueID: base})
			if err != nil || !result.Rearmed {
				t.Fatalf("Heartbeat over a claim seeded with no lease row = %+v, %v; want Rearmed", result, err)
			}
		},
	},
	{
		name:  "Close",
		roles: []string{"Lifecycle"},
		why:   "the close UPDATE stamps a fresh row_lock (issueops/close.go)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: rowVersionActor, IssueID: base, Reason: "rv"})
			if err != nil || !result.Changed {
				t.Fatalf("Close = %+v, %v; want Changed", result, err)
			}
		},
	},
	{
		name:  "CloseAnAlreadyClosedRow",
		roles: []string{"Lifecycle"},
		why:   "an idempotent re-close returns AlreadyClosed before any write (issueops/close.go)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeedClosed(t, ctx, f, base)
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: rowVersionActor, IssueID: base, Reason: "again"})
			if err != nil || result.Changed {
				t.Fatalf("re-close = %+v, %v; want an idempotent Changed false", result, err)
			}
		},
	},
	{
		name:  "Reopen",
		roles: []string{"Lifecycle"},
		why:   "the reopen UPDATE stamps a fresh row_lock (issueops/reopen.go, which every leg runs); the row the RowVersion docs used to contradict",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeedClosed(t, ctx, f, base)
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: rowVersionActor, IssueID: base})
			if err != nil || !result.Changed {
				t.Fatalf("Reopen = %+v, %v; want Changed", result, err)
			}
		},
	},
	{
		name:  "DependencyEditorAddABlocksEdge",
		roles: []string{"DependencyEditor"},
		why:   "an edge is an auxiliary-table write, and the source's is_blocked recompute preserves row_lock by design (the freshRowLock invariant)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-source"})
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-target"})
			rowVersionRequireBlocked(t, ctx, f, base+"-source", false)
			return []rowVersionWatch{{"source", base + "-source", rowVersionHolds}, {"target", base + "-target", rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			if _, err := f.DependencyEditor.AddDependencies(ctx, publicops.AddDependenciesRequest{
				Actor: rowVersionActor,
				Edges: []publicops.DependencyEdge{{IssueID: base + "-source", DependsOnID: base + "-target", Type: publicops.DepBlocks}},
			}); err != nil {
				t.Fatalf("AddDependencies: %v", err)
			}
			rowVersionRequireBlocked(t, ctx, f, base+"-source", true)
		},
	},
	{
		name:  "DependencyEditorRemoveABlocksEdge",
		roles: []string{"DependencyEditor", "AddDependency"},
		why:   "an edge removal is an auxiliary-table write, and the source's is_blocked recompute preserves row_lock by design",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-source"})
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-target"})
			rowVersionSeedEdge(t, ctx, f, base+"-source", base+"-target")
			rowVersionRequireBlocked(t, ctx, f, base+"-source", true)
			return []rowVersionWatch{{"source", base + "-source", rowVersionHolds}, {"target", base + "-target", rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.DependencyEditor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
				Actor: rowVersionActor, IssueID: base + "-source", DependsOnID: base + "-target",
			})
			if err != nil || !result.Removed {
				t.Fatalf("RemoveDependency = %+v, %v; want Removed", result, err)
			}
			rowVersionRequireBlocked(t, ctx, f, base+"-source", false)
		},
	},
	{
		name:  "CommenterAdd",
		roles: []string{"Commenter"},
		why:   "a comment writes the comment row and its event, never the anchor row (RunCommenterLeavesTheAnchorIssueUntouched)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			return []rowVersionWatch{{"anchor", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Commenter.AddComment(ctx, publicops.AddCommentRequest{Author: rowVersionActor, IssueID: base, Text: "rv comment"})
			if err != nil || result.Comment == nil || result.Comment.Text != "rv comment" {
				t.Fatalf("AddComment = %+v, %v; want the stored comment", result, err)
			}
		},
	},
	{
		name:  "BatchApplyUpdateItem",
		roles: []string{"BatchApplier"},
		why:   "an update item is ExecuteUpdate (issueops/batch_apply.go) or ApplyUpdate (uow/batch_applier.go): the generic update path",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base, Title: "before"})
			return []rowVersionWatch{{"subject", base, rowVersionAdvances}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			rowVersionApply(t, ctx, f, base, publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "after"}})
		},
	},
	{
		name:  "BatchApplyLabelOnlyUpdateItem",
		roles: []string{"BatchApplier"},
		why:   "a label-only update item takes the same label branch as Lifecycle.Update",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base})
			return []rowVersionWatch{{"subject", base, rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			rowVersionApply(t, ctx, f, base, publicops.IssuePatch{Labels: publicops.LabelPatch{Add: []string{"rv-batch"}}})
		},
	},
	{
		name:  "CloseABlockerSettlesItsDependent",
		roles: []string{"Lifecycle", "AddDependency"},
		why:   "the closed blocker re-mints (issueops/close.go); the dependent's is_blocked recompute preserves row_lock by design (the freshRowLock invariant)",
		arrange: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) []rowVersionWatch {
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-blocker"})
			rowVersionSeed(t, ctx, f, &types.Issue{ID: base + "-dependent"})
			rowVersionSeedEdge(t, ctx, f, base+"-dependent", base+"-blocker")
			rowVersionRequireBlocked(t, ctx, f, base+"-dependent", true)
			return []rowVersionWatch{{"blocker", base + "-blocker", rowVersionAdvances}, {"dependent", base + "-dependent", rowVersionHolds}}
		},
		act: func(t *testing.T, ctx context.Context, f RowVersionFixture, base string) {
			result, err := f.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: rowVersionActor, IssueID: base + "-blocker", Reason: "rv"})
			if err != nil || !result.Changed {
				t.Fatalf("Close of the blocker = %+v, %v; want Changed", result, err)
			}
			rowVersionRequireBlocked(t, ctx, f, base+"-dependent", false)
		},
	},
}

// RunLifecycleRowVersionAdvancesPerMutationKind runs the matrix: for every
// mutation kind in rowVersionKinds, the watched rows' tokens are read, the
// mutation lands through its subject, and each token is read again and held to
// the ruled move.
func RunLifecycleRowVersionAdvancesPerMutationKind(t *testing.T, ctx context.Context, fixture RowVersionFixture) {
	t.Helper()
	rowVersionRequireHooks(t, fixture)
	for _, kind := range rowVersionKinds {
		t.Run(kind.name, func(t *testing.T) {
			if missing := rowVersionMissing(fixture, kind.roles); missing != "" {
				t.Skipf("RowVersionFixture.%s is nil: this backend does not serve it, so the %s row did not run", missing, kind.name)
			}
			base := fixture.IssuePrefix + "-rv-" + strings.ToLower(kind.name)
			watches := kind.arrange(t, ctx, fixture, base)
			before := make([]int64, len(watches))
			for i, watch := range watches {
				before[i] = rowVersionRead(t, ctx, fixture, watch.id)
				if before[i] == 0 {
					t.Fatalf("the %s %s carries RowVersion 0 before the mutation; every row the current create path writes is stamped non-zero", watch.as, watch.id)
				}
			}
			kind.act(t, ctx, fixture, base)
			for i, watch := range watches {
				after := rowVersionRead(t, ctx, fixture, watch.id)
				if moved := after != before[i]; moved != (watch.want == rowVersionAdvances) {
					t.Errorf("%s: the %s's RowVersion went %d -> %d, want it to %s (%s). A flipped row changes what every "+
						"expected_version holder sees: land it with this table, types.Issue.RowVersion and the freshRowLock invariant",
						kind.name, watch.as, before[i], after, watch.want, kind.why)
				}
			}
		})
	}
}

// RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion is the default-path pin a
// label edit stands on, stated as what a caller observes rather than as a
// token comparison alone: an unguarded label-only Update answers the UNCHANGED
// token, leaves the stored one where it was, and a write guarded by a token
// read before the edits still lands. `bd label`, `bd update --add-label`,
// gascity's v0 label writes and a batch apply's label item all stand on it.
func RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion(t *testing.T, ctx context.Context, fixture RowVersionFixture) {
	t.Helper()
	rowVersionRequireHooks(t, fixture)
	if fixture.Lifecycle == nil {
		t.Skip("RowVersionFixture.Lifecycle is nil: this backend does not serve IssueLifecycle()")
	}
	id := fixture.IssuePrefix + "-rv-labelonly"
	rowVersionSeed(t, ctx, fixture, &types.Issue{ID: id, Labels: []string{"rv-a", "rv-b"}})
	token := rowVersionRead(t, ctx, fixture, id)

	for _, edit := range []struct {
		name   string
		labels publicops.LabelPatch
		want   []string
	}{
		{"add and remove", publicops.LabelPatch{Add: []string{"rv-c"}, Remove: []string{"rv-a"}}, []string{"rv-b", "rv-c"}},
		{"replace", publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"rv-x"}}}, []string{"rv-x"}},
	} {
		result := rowVersionUpdate(t, ctx, fixture, publicops.UpdateRequest{IssueID: id, Patch: publicops.IssuePatch{Labels: edit.labels}})
		rowVersionRequireLabels(t, result, edit.want...)
		if result.Issue.RowVersion != token {
			t.Errorf("label-only update (%s) answered RowVersion %d, want the unchanged %d: a caller composing its next "+
				"guard from the result would be refused for a row nobody else touched", edit.name, result.Issue.RowVersion, token)
		}
		if stored := rowVersionRead(t, ctx, fixture, id); stored != token {
			t.Errorf("label-only update (%s) moved the stored RowVersion %d -> %d, want it held", edit.name, token, stored)
		}
	}

	// The consequence a token holder lives on: the token read before both edits
	// still guards a write.
	guard := token
	landed, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: rowVersionActor, IssueID: id, ExpectedVersion: &guard,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "guarded after the label edits"}},
	})
	if err != nil {
		t.Fatalf("a write guarded by the token read before two label-only edits was refused: %v", err)
	}
	if !landed.Changed || landed.Issue == nil || landed.Issue.RowVersion == token {
		t.Fatalf("the write guarded by the pre-edit token = %+v, want it to land and advance the token", landed)
	}
}

func rowVersionRequireHooks(t *testing.T, fixture RowVersionFixture) {
	t.Helper()
	if fixture.CreateIssue == nil || fixture.GetIssue == nil {
		t.Fatal("RowVersionFixture.CreateIssue and GetIssue are required: every row seeds and reads through them")
	}
}

// rowVersionMissing names the first of fields the fixture leaves nil, or "".
func rowVersionMissing(fixture RowVersionFixture, fields []string) string {
	present := map[string]bool{
		"Lifecycle":        fixture.Lifecycle != nil,
		"MetadataCAS":      fixture.MetadataCAS != nil,
		"Claimer":          fixture.Claimer != nil,
		"Releaser":         fixture.Releaser != nil,
		"Heartbeater":      fixture.Heartbeater != nil,
		"DependencyEditor": fixture.DependencyEditor != nil,
		"Commenter":        fixture.Commenter != nil,
		"BatchApplier":     fixture.BatchApplier != nil,
		"SeedClaimer":      fixture.SeedClaimer != nil,
		"AddDependency":    fixture.AddDependency != nil,
	}
	for _, field := range fields {
		if !present[field] {
			return field
		}
	}
	return ""
}

func rowVersionSeed(t *testing.T, ctx context.Context, f RowVersionFixture, issue *types.Issue) {
	t.Helper()
	if issue.Title == "" {
		issue.Title = issue.ID
	}
	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}
	issue.Priority = 2
	issue.IssueType = types.TypeTask
	if err := f.CreateIssue(ctx, issue, "rv-seed"); err != nil {
		t.Fatalf("seed %s: %v", issue.ID, err)
	}
}

// rowVersionSeedHeld seeds the state a won claim leaves ON THE ROW, with no
// lease row: in_progress, assigned to rowVersionHolder, started.
func rowVersionSeedHeld(t *testing.T, ctx context.Context, f RowVersionFixture, id string) {
	t.Helper()
	started := time.Now().UTC().Add(-time.Hour)
	rowVersionSeed(t, ctx, f, &types.Issue{ID: id, Status: types.StatusInProgress, Assignee: rowVersionHolder, StartedAt: &started})
}

func rowVersionSeedClosed(t *testing.T, ctx context.Context, f RowVersionFixture, id string) {
	t.Helper()
	closed := time.Now().UTC().Add(-time.Hour)
	rowVersionSeed(t, ctx, f, &types.Issue{ID: id, Status: types.StatusClosed, ClosedAt: &closed})
}

func rowVersionSeedEdge(t *testing.T, ctx context.Context, f RowVersionFixture, from, to string) {
	t.Helper()
	if err := f.AddDependency(ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: types.DepBlocks}, "rv-seed"); err != nil {
		t.Fatalf("seed the edge %s -> %s: %v", from, to, err)
	}
}

func rowVersionRead(t *testing.T, ctx context.Context, f RowVersionFixture, id string) int64 {
	t.Helper()
	issue, err := f.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	if issue == nil {
		t.Fatalf("read %s answered no issue", id)
	}
	return issue.RowVersion
}

// rowVersionUpdate sends request as rowVersionActor and fails on any error.
func rowVersionUpdate(t *testing.T, ctx context.Context, f RowVersionFixture, request publicops.UpdateRequest) publicops.UpdateResult {
	t.Helper()
	request.Actor = rowVersionActor
	result, err := f.Lifecycle.Update(ctx, request)
	if err != nil {
		t.Fatalf("Update(%s): %v", request.IssueID, err)
	}
	return result
}

func rowVersionApply(t *testing.T, ctx context.Context, f RowVersionFixture, id string, patch publicops.IssuePatch) {
	t.Helper()
	result, err := f.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor: rowVersionActor,
		Items: []publicops.ApplyItem{batchApplyUpdate(publicops.Ref{ID: id}, patch)},
	})
	if err != nil {
		t.Fatalf("ApplyBatch updating %s: %v", id, err)
	}
	if len(result.Items) != 1 || !result.Items[0].Changed {
		t.Fatalf("ApplyBatch updating %s answered %+v, want one Changed item", id, result.Items)
	}
}

func rowVersionRequireLabels(t *testing.T, result publicops.UpdateResult, want ...string) {
	t.Helper()
	if !result.Changed || result.Issue == nil {
		t.Fatalf("label-only update = %+v, want Changed with the hydrated row", result)
	}
	got := slices.Sorted(slices.Values(result.Issue.Labels))
	if want = slices.Sorted(slices.Values(want)); !slices.Equal(got, want) {
		t.Fatalf("labels after the edit = %v, want %v", got, want)
	}
}

// rowVersionRequireBlocked proves a derived is_blocked recompute ran, so the
// HOLD a row asserts is about a recompute rather than a no-op.
func rowVersionRequireBlocked(t *testing.T, ctx context.Context, f RowVersionFixture, id string, want bool) {
	t.Helper()
	if f.QueryScalar == nil {
		t.Logf("RowVersionFixture.QueryScalar is nil: not proving %s's is_blocked is %v; the token comparison still runs", id, want)
		return
	}
	var blocked bool
	if err := f.QueryScalar(ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{id}, &blocked); err != nil {
		t.Fatalf("read is_blocked of %s: %v", id, err)
	}
	if blocked != want {
		t.Fatalf("%s is_blocked = %v, want %v: the recompute this row is about did not happen", id, blocked, want)
	}
}
