package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the behavior contract every implementation of
// publicops.Lifecycle must satisfy, independent of how it reaches storage.
// There are three of them — the direct store, the embedded store, and the
// unit-of-work backend — and the first two share an execution path the third
// does not. Behavior asserted only against one backend has repeatedly drifted
// on the others, so each of these runs against all three from one spec.

// RunIssueOperationsCreateRoutesInfraTypesToWisps pins the facade create
// against the same infra-type routing the stores' own CreateIssue applies: a
// configured infra type is ephemeral and lives in the wisp tables, never in
// issues.
func RunIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	for key, value := range map[string]string{"types.custom": "agent", "types.infra": "agent"} {
		if err := fixture.SetConfig(ctx, key, value); err != nil {
			t.Fatalf("SetConfig(%s): %v", key, err)
		}
	}

	result, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "infra bead", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent")},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !result.Issue.Ephemeral {
		t.Errorf("create result Ephemeral = false, want true for infra type %q", result.Issue.IssueType)
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", result.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", result.Issue.ID, 0)

	// A no-history infra create keeps its no-history retention rather than
	// being upgraded to ephemeral, matching CreateIssue.
	noHistory, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "infra no-history", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent"), NoHistory: true},
	})
	if err != nil {
		t.Fatalf("Create no-history: %v", err)
	}
	if noHistory.Issue.Ephemeral {
		t.Errorf("no-history infra create Ephemeral = true, want false")
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", noHistory.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", noHistory.Issue.ID, 0)

	// A non-infra type is unaffected.
	durable, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "durable bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create durable: %v", err)
	}
	if durable.Issue.Ephemeral {
		t.Errorf("durable create Ephemeral = true, want false")
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", durable.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", durable.Issue.ID, 0)
}

// RunIssueOperationsCreateRejectsMissingDependencyTargets pins the facade
// create against reporting success for an issue whose requested relationships
// were never written. The batch engine tolerates a dangling edge so a partial
// import still lands; a guarded single create must refuse the whole request
// with a typed error naming the target, and leave nothing behind.
func RunIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	seed := &types.Issue{ID: fixture.IssuePrefix + "-skipdep-seed", Title: "seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := fixture.CreateIssue(ctx, seed, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		id      string
		request publicops.CreateRequest
		target  string
	}{
		{
			name:   "explicit dependency",
			id:     fixture.IssuePrefix + "-skipdep-explicit",
			target: fixture.IssuePrefix + "-skipdep-missing-dep",
			request: publicops.CreateRequest{
				Dependencies: []publicops.CreateDependency{{TargetID: fixture.IssuePrefix + "-skipdep-missing-dep", Type: types.DepBlocks}},
			},
		},
		{
			name:    "parent",
			id:      fixture.IssuePrefix + "-skipdep-parent",
			target:  fixture.IssuePrefix + "-skipdep-missing-parent",
			request: publicops.CreateRequest{ParentID: fixture.IssuePrefix + "-skipdep-missing-parent"},
		},
		{
			name:   "waits-for spawner",
			id:     fixture.IssuePrefix + "-skipdep-waits",
			target: fixture.IssuePrefix + "-skipdep-missing-spawner",
			request: publicops.CreateRequest{
				WaitsFor: &publicops.WaitsFor{SpawnerID: fixture.IssuePrefix + "-skipdep-missing-spawner"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			request.Actor = "writer"
			request.ForceIDPrefix = true
			request.Issue = &types.Issue{ID: tc.id, Title: tc.name, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			_, err := fixture.Operations.Create(ctx, request)
			if err == nil {
				t.Fatal("Create returned nil error, want a refusal for the missing dependency target")
			}
			if !errors.Is(err, publicops.ErrNotFound) {
				t.Errorf("Create error = %v, want ErrNotFound", err)
			}
			if !errors.Is(err, publicops.ErrValidation) {
				t.Errorf("Create error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("Create error = %v, want it to name the missing target %q", err, tc.target)
			}
			assertIssueOperationsRowCount(t, ctx, fixture, "issues", tc.id, 0)
			assertIssueOperationsRowCount(t, ctx, fixture, "wisps", tc.id, 0)
		})
	}

	// A create whose targets all exist is unaffected.
	result, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{ID: fixture.IssuePrefix + "-skipdep-ok", Title: "ok", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		Dependencies:  []publicops.CreateDependency{{TargetID: seed.ID, Type: types.DepBlocks}},
	})
	if err != nil {
		t.Fatalf("Create with existing target: %v", err)
	}
	if len(result.Issue.Dependencies) != 1 || result.Issue.Dependencies[0].DependsOnID != seed.ID {
		t.Fatalf("Create result dependencies = %#v, want one edge to %s", result.Issue.Dependencies, seed.ID)
	}
}

// RunIssueOperationsCreateRefusesAnOccupiedID pins the create-only half of the
// Lifecycle.Create clause: "an occupied ID returns ErrAlreadyExists and leaves
// persistent state unchanged" (issueops/issueops.go, Lifecycle.Create). The
// issue and wisp tables share one ID space, so ACROSS is asserted in both
// directions here, not only for the plane the create happens to target.
//
// The proxied-server `bd create` route asked its use case for a plain create
// with no create-only guard, so `bd create --id <occupied>` silently UPSERTED
// the stored row and reported success while the direct route refused.
func RunIssueOperationsCreateRefusesAnOccupiedID(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	// seedIssueOperationsLabeledIssue titles the issue after its own ID, which
	// is what the refusal must leave in place.
	occupied := fixture.IssuePrefix + "-occupied-issue"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, occupied, "seeded")

	_, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: occupied, Title: "overwriting title", Status: types.StatusOpen,
			Priority: 1, IssueType: types.TypeBug, Labels: []string{"overwriting"},
		},
	})
	assertIssueOperationsAlreadyExists(t, err, "durable create over an occupied durable ID", occupied)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", occupied, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", occupied, 0)
	// "leaves persistent state unchanged" is the load-bearing half: an upsert
	// reported as a refusal would still have rewritten every column.
	assertIssueOperationsScalarValue(t, ctx, fixture, "occupied issue title", occupied,
		"SELECT title FROM issues WHERE id = ?", []any{occupied})
	assertIssueOperationsScalarValue(t, ctx, fixture, "occupied issue type", string(types.TypeTask),
		"SELECT issue_type FROM issues WHERE id = ?", []any{occupied})
	assertIssueOperationsLabels(t, ctx, fixture, occupied, "after refused durable create", "seeded")

	// An ID occupied by a WISP refuses a durable create.
	wispID := fixture.IssuePrefix + "-occupied-wisp"
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: wispID, Title: "resident wisp", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		},
	}); err != nil {
		t.Fatalf("seed resident wisp: %v", err)
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", wispID, 1)

	_, err = fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: wispID, Title: "durable squatter", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		},
	})
	assertIssueOperationsAlreadyExists(t, err, "durable create over an occupied wisp ID", wispID)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", wispID, 0)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", wispID, 1)
	assertIssueOperationsScalarValue(t, ctx, fixture, "resident wisp title", "resident wisp",
		"SELECT title FROM wisps WHERE id = ?", []any{wispID})

	// And the other direction: an ID occupied by a durable issue refuses an
	// ephemeral create.
	_, err = fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: occupied, Title: "wisp squatter", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		},
	})
	assertIssueOperationsAlreadyExists(t, err, "ephemeral create over an occupied durable ID", occupied)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", occupied, 0)
	assertIssueOperationsScalarValue(t, ctx, fixture, "occupied issue title after wisp squat", occupied,
		"SELECT title FROM issues WHERE id = ?", []any{occupied})
}

// assertIssueOperationsAlreadyExists checks the refusal an occupied ID gets.
// The message must name the ID: a caller refused with a bare "issue already
// exists" cannot act on it.
func assertIssueOperationsAlreadyExists(t *testing.T, err error, label, id string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: Create returned nil error, want ErrAlreadyExists", label)
	}
	if !errors.Is(err, publicops.ErrAlreadyExists) {
		t.Errorf("%s: Create error = %v, want ErrAlreadyExists", label, err)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("%s: Create error = %v, want it to name the occupied ID %q", label, err, id)
	}
}

// RunIssueOperationsCreateRefusesAForeignIDPrefix pins the guard
// CreateRequest.ForceIDPrefix exists to lift: the flag "permits an explicit ID
// outside the configured prefix" (issueops/issueops.go:208-209), so without it
// such an ID is ErrPrefixMismatch — "returned when an issue ID does not match
// the configured prefix" (issueops/errors.go:81-82) — under Create's standing
// promise that "a refusal or validation error also leaves no partial persistent
// state".
//
// Every other create case in this file sets ForceIDPrefix, which is what makes
// this one necessary: the flag is asserted only from the side that bypasses the
// check, so nothing here says the check exists. The refusal is TYPED because
// both front doors decide whether to re-offer the create with --force from
// errors.Is rather than from the message, and it is checked on BOTH planes
// because an ephemeral create routes to a different table and could plausibly
// skip a guard the durable one applies.
func RunIssueOperationsCreateRefusesAForeignIDPrefix(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	// A prefix no fixture configures, so the ID is foreign whatever this
	// workspace calls itself.
	foreign := "lcrforeign-" + fixture.IssuePrefix + "-1"
	foreignWisp := "lcrforeign-" + fixture.IssuePrefix + "-2"

	for _, tc := range []struct {
		name      string
		id        string
		ephemeral bool
		table     string
	}{
		{name: "durable", id: foreign, table: "issues"},
		{name: "ephemeral", id: foreignWisp, ephemeral: true, table: "wisps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
				Actor: "writer",
				Issue: &types.Issue{
					ID: tc.id, Title: tc.name, Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask, Ephemeral: tc.ephemeral,
				},
			})
			if !errors.Is(err, publicops.ErrPrefixMismatch) {
				t.Fatalf("unforced create at the foreign ID %q: err = %v, want ErrPrefixMismatch", tc.id, err)
			}
			assertIssueOperationsRowCount(t, ctx, fixture, "issues", tc.id, 0)
			assertIssueOperationsRowCount(t, ctx, fixture, "wisps", tc.id, 0)

			// The same request with the flag lands, which is what makes the
			// refusal a policy the caller can override rather than a hard limit.
			forced, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
				Actor: "writer", ForceIDPrefix: true,
				Issue: &types.Issue{
					ID: tc.id, Title: tc.name, Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask, Ephemeral: tc.ephemeral,
				},
			})
			if err != nil {
				t.Fatalf("forced create at the foreign ID %q: %v", tc.id, err)
			}
			if forced.Issue.ID != tc.id {
				t.Errorf("forced create result ID = %q, want the requested %q", forced.Issue.ID, tc.id)
			}
			assertIssueOperationsRowCount(t, ctx, fixture, tc.table, tc.id, 1)
		})
	}
}

// RunIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset pins the sentence
// MetadataPatch opens with: "Replace is mutually exclusive with Merge, Set, and
// Unset. Without Replace, operations apply Merge, then Set keys in
// deterministic order, then Unset" (issueops/issueops.go:83-86).
//
// The order is only observable when the three edits COLLIDE, so every key here
// appears in more than one of them: a key merged in and then set and then unset
// must end up absent, and one merged in and unset must not survive because the
// merge ran later. A body applying Unset before Set leaves the same document
// looking plausible — it just carries a key the caller asked to remove — which
// is why the existing metadata cases, none of which collide, cannot see it.
//
// The exclusivity half is asserted through the stored document rather than the
// error alone: a body that refused the combination AFTER applying the Replace
// would return the right sentinel over a rewritten row.
func RunIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-metadata-order"
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: "metadata order", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Metadata: json.RawMessage(`{"keep":"seeded","drop":"seeded"}`),
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}

	ordered, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{
			Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"keep":"merged","contested":"merged","merged":true}`)},
			Set: map[string]json.RawMessage{
				"added":     json.RawMessage(`"set"`),
				"keep":      json.RawMessage(`"set"`),
				"contested": json.RawMessage(`"set"`),
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
	assertIssueOperationsMetadata(t, "ordered metadata patch", ordered.Issue.Metadata,
		`{"added":"set","contested":"set","merged":true}`)
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after the ordered metadata patch",
		`{"added":"set","contested":"set","merged":true}`)

	// Replace beside any incremental edit is refused, and the document the
	// replacement would have written never lands.
	events := newIssueOperationsEventCounter(t, ctx, fixture, id)
	for name, patch := range map[string]publicops.MetadataPatch{
		"replace with set": {
			Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
			Set:     map[string]json.RawMessage{"must_not_persist": json.RawMessage(`true`)},
		},
		"replace with merge": {
			Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
			Merge:   publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"must_not_persist":true}`)},
		},
		"replace with unset": {
			Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
			Unset:   []string{"added"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
				Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{Metadata: patch},
			}); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", name, err)
			}
			assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after "+name,
				`{"added":"set","contested":"set","merged":true}`)
		})
	}
	events.assert(t, "refused replace-plus-incremental patches", 0, nil)
}

// RunIssueOperationsCreateInheritsParentLabels pins
// CreateRequest.InheritLabelsFromParent — "copies the parent's labels at
// creation" — against CreateRequest.Issue's own "Labels are authoritative"
// (both issueops/issueops.go, CreateRequest). One create must satisfy both.
//
// The two `bd create --parent` front doors spell it differently — the direct
// route merges the parent's labels itself so its --dry-run preview can show
// them and leaves the flag off, the proxied route sets the flag — so the merge
// has to be the same set either way.
func RunIssueOperationsCreateInheritsParentLabels(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	parent := fixture.IssuePrefix + "-inherit-parent"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, parent, "shared", "from-parent")

	inherited, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:                   "writer",
		ForceIDPrefix:           true,
		ParentID:                parent,
		InheritLabelsFromParent: true,
		Issue: &types.Issue{
			Title: "inheriting child", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Labels: []string{"own", "shared"},
		},
	})
	if err != nil {
		t.Fatalf("Create inheriting child: %v", err)
	}
	// A label the child and the parent both carry is one label, not two: the
	// count assertion inside this helper is what says so.
	assertIssueOperationsLabels(t, ctx, fixture, inherited.Issue.ID, "inheriting child", "own", "shared", "from-parent")
	// CreateResult.Issue is "a detached snapshot with labels", so the same set
	// has to come back on the result rather than only from the row.
	assertIssueOperationsStringSet(t, "inheriting child result labels", inherited.Issue.Labels, "own", "shared", "from-parent")

	// With inheritance off, the request's own labels are the whole set — the
	// authoritative clause standing alone.
	own, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		ParentID:      parent,
		Issue: &types.Issue{
			Title: "own labels only", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Labels: []string{"own"},
		},
	})
	if err != nil {
		t.Fatalf("Create child without inheritance: %v", err)
	}
	assertIssueOperationsLabels(t, ctx, fixture, own.Issue.ID, "child without inheritance", "own")
	assertIssueOperationsStringSet(t, "child without inheritance result labels", own.Issue.Labels, "own")

	// Inheriting from a label-less parent adds nothing rather than failing.
	bare := fixture.IssuePrefix + "-inherit-bare-parent"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, bare)
	none, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:                   "writer",
		ForceIDPrefix:           true,
		ParentID:                bare,
		InheritLabelsFromParent: true,
		Issue: &types.Issue{
			Title: "nothing to inherit", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask,
		},
	})
	if err != nil {
		t.Fatalf("Create child of label-less parent: %v", err)
	}
	assertIssueOperationsLabels(t, ctx, fixture, none.Issue.ID, "child of label-less parent")
	assertIssueOperationsStringSet(t, "child of label-less parent result labels", none.Issue.Labels)
}

// RunIssueOperationsUpdateFoldsMetadataIntoOneEvent pins a compound update to a
// single event. A guarded update is one atomic mutation, so its history must
// read as one entry; a metadata patch riding along with field edits must not
// write the row twice and fabricate a second event in the stream every history
// consumer sees.
func RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	issue := &types.Issue{
		ID: fixture.IssuePrefix + "-metadata-event", Title: "metadata event", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{"keep":"old"}`),
	}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	events := newIssueOperationsEventCounter(t, ctx, fixture, issue.ID)

	updated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		Metadata: publicops.MetadataPatch{
			Set: map[string]json.RawMessage{"added": json.RawMessage(`"value"`)},
		},
	}})
	if err != nil {
		t.Fatalf("compound update: %v", err)
	}
	if !updated.Changed || updated.Issue.Status != types.StatusInProgress {
		t.Fatalf("compound update result = %#v", updated)
	}
	assertIssueOperationsMetadata(t, "compound update", updated.Issue.Metadata, `{"added":"value","keep":"old"}`)
	events.assert(t, "compound update", 1, map[types.EventType]int{types.EventStatusChanged: 1, types.EventUpdated: 0})

	// A metadata-only patch still records its own single event.
	metadataOnly, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Unset: []string{"keep"}},
	}})
	if err != nil || !metadataOnly.Changed {
		t.Fatalf("metadata-only update = %#v, %v", metadataOnly, err)
	}
	assertIssueOperationsMetadata(t, "metadata-only update", metadataOnly.Issue.Metadata, `{"added":"value"}`)
	events.assert(t, "metadata-only update", 1, map[types.EventType]int{types.EventUpdated: 1})

	// A metadata patch that changes nothing stays elided.
	noOp, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}},
	}})
	if err != nil || noOp.Changed {
		t.Fatalf("no-op metadata update = %#v, %v", noOp, err)
	}
	events.assert(t, "no-op metadata update", 0, nil)
}

// RunIssueOperationsUpdateClosePolicy pins what a generic status update does
// when it crosses from a non-done status into the done category: it answers to
// the same policy `bd close` does — the open-children refusal and the
// live-direct-blocker refusal — and ForceClosePolicy is how a caller overrides
// them. Until now the contract had no boundary-crossing case at all, and that
// gap is exactly how two earlier attempts at a shared policy check reached a
// backend that could not satisfy them without any test noticing.
func RunIssueOperationsUpdateClosePolicy(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	parentID := fixture.IssuePrefix + "-closepolicy-parent"
	seedClosePolicyIssue(t, ctx, fixture, parentID, publicops.CreateRequest{})
	seedClosePolicyIssue(t, ctx, fixture, fixture.IssuePrefix+"-closepolicy-child", publicops.CreateRequest{ParentID: parentID})

	blockerID := fixture.IssuePrefix + "-closepolicy-blocker"
	blockedID := fixture.IssuePrefix + "-closepolicy-blocked"
	seedClosePolicyIssue(t, ctx, fixture, blockerID, publicops.CreateRequest{})
	seedClosePolicyIssue(t, ctx, fixture, blockedID, publicops.CreateRequest{
		Dependencies: []publicops.CreateDependency{{TargetID: blockerID, Type: types.DepBlocks}},
	})

	// An open child refuses, with the typed error and its count, and writes
	// nothing — not the row, not an event.
	events := newIssueOperationsEventCounter(t, ctx, fixture, parentID)
	var openChildrenErr *publicops.CloseOpenChildrenError
	_, err := fixture.Operations.Update(ctx, closePolicyStatusRequest(parentID, false))
	if !errors.As(err, &openChildrenErr) {
		t.Fatalf("update %s into done with an open child: err = %v, want CloseOpenChildrenError", parentID, err)
	}
	if openChildrenErr.OpenChildren != 1 {
		t.Errorf("refusal reported %d open children, want 1", openChildrenErr.OpenChildren)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusOpen)
	events.assert(t, "refused crossing", 0, nil)

	// A claim rides the same atomic update. An open-child refusal must leave
	// every part of that compound request inert, including the would-be claim.
	var beforeAssignee string
	var beforeRowVersion, beforeClosedAt string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, ''), CAST(row_lock AS CHAR), COALESCE(CAST(closed_at AS CHAR), '') FROM issues WHERE id = ?", []any{parentID}, &beforeAssignee, &beforeRowVersion, &beforeClosedAt); err != nil {
		t.Fatalf("read compound-refusal state for %s: %v", parentID, err)
	}
	claimAndClose := closePolicyStatusRequest(parentID, false)
	claimAndClose.Claim = true
	_, err = fixture.Operations.Update(ctx, claimAndClose)
	openChildrenErr = nil
	if !errors.As(err, &openChildrenErr) {
		t.Fatalf("claiming update %s into done with an open child: err = %v, want CloseOpenChildrenError", parentID, err)
	}
	var afterAssignee string
	var afterRowVersion, afterClosedAt string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, ''), CAST(row_lock AS CHAR), COALESCE(CAST(closed_at AS CHAR), '') FROM issues WHERE id = ?", []any{parentID}, &afterAssignee, &afterRowVersion, &afterClosedAt); err != nil {
		t.Fatalf("read compound-refusal state after %s: %v", parentID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusOpen)
	if afterAssignee != beforeAssignee {
		t.Errorf("compound refusal assignee = %q, want unchanged %q", afterAssignee, beforeAssignee)
	}
	if afterRowVersion != beforeRowVersion {
		t.Errorf("compound refusal row version = %q, want unchanged %q", afterRowVersion, beforeRowVersion)
	}
	if afterClosedAt != beforeClosedAt {
		t.Errorf("compound refusal closed_at = %v, want unchanged %v", afterClosedAt, beforeClosedAt)
	}
	events.assert(t, "refused claiming crossing", 0, nil)

	// A live direct blocker refuses too.
	_, err = fixture.Operations.Update(ctx, closePolicyStatusRequest(blockedID, false))
	if !errors.Is(err, publicops.ErrCloseBlocked) {
		t.Fatalf("update %s into done with a live blocker: err = %v, want ErrCloseBlocked", blockedID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, blockedID, types.StatusOpen)

	// Force bypasses close policy and nothing else. A stale ExpectedVersion is
	// an orthogonal precondition, checked ahead of the policy and never waived
	// by it — the same ordering a checked close applies.
	stale := int64(-1)
	staleRequest := closePolicyStatusRequest(parentID, true)
	staleRequest.ExpectedVersion = &stale
	if _, err := fixture.Operations.Update(ctx, staleRequest); !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("forced crossing with a stale version: err = %v, want ErrVersionMismatch", err)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusOpen)

	// ForceClosePolicy bypasses both, and only those.
	for _, id := range []string{parentID, blockedID} {
		forced, err := fixture.Operations.Update(ctx, closePolicyStatusRequest(id, true))
		if err != nil {
			t.Fatalf("forced update %s into done: %v", id, err)
		}
		if !forced.Changed || forced.Issue.Status != types.StatusClosed {
			t.Fatalf("forced update %s into done = %#v, want a committed close", id, forced)
		}
	}

	// A done-to-done restatement is filtered out as a no-op before any policy
	// could observe it, so it needs no force even though the child is still open.
	reclose, err := fixture.Operations.Update(ctx, closePolicyStatusRequest(parentID, false))
	if err != nil {
		t.Fatalf("restate %s as done: %v", parentID, err)
	}
	if reclose.Changed {
		t.Errorf("restating %s as done reported Changed = true, want a no-op", parentID)
	}

	// A status change that does not reach the done category is untouched by any
	// of this, open child or not.
	nonCrossing := closePolicyStatusRequest(parentID, false)
	nonCrossing.Patch.Status.Value = types.StatusInProgress
	if _, err := fixture.Operations.Update(ctx, nonCrossing); err != nil {
		t.Fatalf("non-crossing status update on %s: %v", parentID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusInProgress)
}

// RunIssueOperationsUpdateClosedFieldsMatchClose pins the close-lifecycle
// columns a generic update leaves behind (ga-kjkv1). A status update that
// crosses into closed is a close by another name, so it must land the row a
// close lands: close_reason and closed_by_session written, not inherited from
// whatever the previous close left. It also pins the closed_at coherence guard,
// which is reachable only through the raw column map an external-sync or
// backfill caller uses — the typed patch carries no closed_at.
//
// A shared helper alone does not keep the two funnels honest: both already call
// ManageClosedAt, and the pin auto-clear that sits three lines away from it
// exists in issueops and is absent from domain/db. Only a case that asserts the
// stored row on every backend catches that shape of divergence.
func RunIssueOperationsUpdateClosedFieldsMatchClose(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	// A generic close on a row a previous close stamped must not inherit that
	// close's reason or session. This is the misattribution ga-kjkv1 fixes:
	// `bd show` renders a stale closed_by_session as "Closed by session".
	recloseID := fixture.IssuePrefix + "-closedfields-reclose"
	seedClosePolicyIssue(t, ctx, fixture, recloseID, publicops.CreateRequest{})
	if _, err := fixture.Operations.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: recloseID, Reason: "first pass", Session: "session-one",
	}); err != nil {
		t.Fatalf("close %s: %v", recloseID, err)
	}
	assertClosedFields(t, ctx, fixture, recloseID, "after close", "first pass", "session-one", true)

	// Reopen through the generic funnel, not the reopen verb: this is the path
	// that used to leave close_reason and closed_by_session in place.
	if err := fixture.UpdateRaw(ctx, recloseID, map[string]any{"status": string(types.StatusOpen)}, "writer"); err != nil {
		t.Fatalf("generic reopen of %s: %v", recloseID, err)
	}
	assertClosedFields(t, ctx, fixture, recloseID, "after generic reopen", "", "", false)

	if err := fixture.UpdateRaw(ctx, recloseID, map[string]any{"status": string(types.StatusClosed)}, "writer"); err != nil {
		t.Fatalf("generic re-close of %s: %v", recloseID, err)
	}
	assertClosedFields(t, ctx, fixture, recloseID, "after generic re-close", "", "", true)

	// An explicit key still wins over its default, so the CLI's own
	// closed_by_session pass-through keeps working.
	explicitID := fixture.IssuePrefix + "-closedfields-explicit"
	seedClosePolicyIssue(t, ctx, fixture, explicitID, publicops.CreateRequest{})
	if err := fixture.UpdateRaw(ctx, explicitID, map[string]any{
		"status": string(types.StatusClosed), "closed_by_session": "session-two", "close_reason": "handled",
	}, "writer"); err != nil {
		t.Fatalf("generic close of %s with explicit close fields: %v", explicitID, err)
	}
	assertClosedFields(t, ctx, fixture, explicitID, "explicit close fields", "handled", "session-two", true)

	// The close-crossing defaults have to be observable on a row that carries
	// stale attribution at the moment it closes. A freshly created row already
	// has both columns empty, and the re-close above routes through a generic
	// reopen that blanks them first, so neither case can tell a funnel that
	// writes the columns from one that merely inherits them. Seeding the stale
	// values onto an OPEN row does: the columns are allowlisted by name, and
	// with no closed_at in the map the coherence guard has nothing to refuse.
	staleID := fixture.IssuePrefix + "-closedfields-stale"
	seedClosePolicyIssue(t, ctx, fixture, staleID, publicops.CreateRequest{})
	if err := fixture.UpdateRaw(ctx, staleID, map[string]any{
		"close_reason": "stale", "closed_by_session": "stale-sess",
	}, "writer"); err != nil {
		t.Fatalf("seed stale close attribution on open %s: %v", staleID, err)
	}
	assertClosedFields(t, ctx, fixture, staleID, "stale attribution staged while open", "stale", "stale-sess", false)

	if err := fixture.UpdateRaw(ctx, staleID, map[string]any{"status": string(types.StatusClosed)}, "writer"); err != nil {
		t.Fatalf("generic close of %s over stale attribution: %v", staleID, err)
	}
	assertClosedFields(t, ctx, fixture, staleID, "generic close over stale attribution", "", "", true)

	// The coherence guard. Stamping closed_at on a row that stays open is
	// refused by name, typed as a validation error, and writes nothing.
	guardID := fixture.IssuePrefix + "-closedfields-guard"
	seedClosePolicyIssue(t, ctx, fixture, guardID, publicops.CreateRequest{})
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	events := newIssueOperationsEventCounter(t, ctx, fixture, guardID)
	err := fixture.UpdateRaw(ctx, guardID, map[string]any{"closed_at": stamp}, "writer")
	assertClosedAtRefusal(t, err, "stamping closed_at on an open row", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusOpen)
	assertClosedFields(t, ctx, fixture, guardID, "after refused closed_at stamp", "", "", false)
	events.assert(t, "refused closed_at stamp", 0, nil)

	// So is a stamp riding a status that does not land closed.
	err = fixture.UpdateRaw(ctx, guardID, map[string]any{"status": string(types.StatusInProgress), "closed_at": stamp}, "writer")
	assertClosedAtRefusal(t, err, "stamping closed_at on a non-closed transition", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusOpen)

	// Landing status and closed_at together is the coherent write, so it is
	// allowed — an external-sync or backfill caller depends on it.
	if err := fixture.UpdateRaw(ctx, guardID, map[string]any{
		"status": string(types.StatusClosed), "closed_at": stamp,
	}, "writer"); err != nil {
		t.Fatalf("landing status and closed_at together on %s: %v", guardID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusClosed)
	assertClosedFields(t, ctx, fixture, guardID, "coherent close with explicit closed_at", "", "", true)

	// Restamping closed_at on a row that is already closed is the repair path
	// for rows a pre-invariant close left blank; it stays open.
	repaired := stamp.Add(time.Hour)
	if err := fixture.UpdateRaw(ctx, guardID, map[string]any{"closed_at": repaired}, "writer"); err != nil {
		t.Fatalf("repairing closed_at on closed %s: %v", guardID, err)
	}

	// Clearing closed_at while the status stays closed is the other incoherent
	// half, and it is refused too.
	err = fixture.UpdateRaw(ctx, guardID, map[string]any{"closed_at": nil}, "writer")
	assertClosedAtRefusal(t, err, "clearing closed_at on a closed row", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusClosed)
	assertClosedFields(t, ctx, fixture, guardID, "after refused closed_at clear", "", "", true)

	// The same refusal must hold when the explicit closed_at happens to equal
	// the value already stored. That is a no-op by VALUE and an incoherent
	// write by INTENT — the caller is asking to reopen the row and keep its
	// closed_at — so the guard has to see the key before the no-op filter can
	// drop it. Otherwise this write and the identical one carrying a stamp one
	// nanosecond off get opposite answers, and the reopen silently clears the
	// column the caller explicitly asked to keep.
	err = fixture.UpdateRaw(ctx, guardID, map[string]any{
		"status": string(types.StatusOpen), "closed_at": repaired,
	}, "writer")
	assertClosedAtRefusal(t, err, "reopening while restating the row's own closed_at", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusClosed)
	assertClosedFields(t, ctx, fixture, guardID, "after refused no-op-valued closed_at reopen", "", "", true)

	// Clearing it as part of a reopen is coherent, so it is allowed.
	if err := fixture.UpdateRaw(ctx, guardID, map[string]any{
		"status": string(types.StatusOpen), "closed_at": nil,
	}, "writer"); err != nil {
		t.Fatalf("reopening %s with an explicit closed_at clear: %v", guardID, err)
	}
	assertClosedFields(t, ctx, fixture, guardID, "reopen with explicit closed_at clear", "", "", false)
}

// assertClosedAtRefusal checks that a coherence refusal is typed as a
// validation error and names both the column and the issue, so a raw-map caller
// with no override can tell exactly which write was rejected.
func assertClosedAtRefusal(t *testing.T, err error, label, id string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: err = nil, want a refusal", label)
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("%s: err = %v, want ErrValidation", label, err)
	}
	for _, want := range []string{"closed_at", id} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refusal %q does not mention %q", label, err.Error(), want)
		}
	}
}

// assertClosedFields reads the close-lifecycle columns back from storage. The
// stored empty string and SQL NULL are the same "nothing recorded" state to
// every reader, so both collapse to "" here.
func assertClosedFields(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label, wantReason, wantSession string, wantClosedAt bool) {
	t.Helper()
	var reason, session, closedAt string
	if err := fixture.QueryScalar(ctx,
		"SELECT COALESCE(close_reason, ''), COALESCE(closed_by_session, ''), COALESCE(CAST(closed_at AS CHAR), '') FROM issues WHERE id = ?",
		[]any{id}, &reason, &session, &closedAt); err != nil {
		t.Fatalf("read close fields for %s (%s): %v", id, label, err)
	}
	if reason != wantReason {
		t.Errorf("%s %s close_reason = %q, want %q", id, label, reason, wantReason)
	}
	if session != wantSession {
		t.Errorf("%s %s closed_by_session = %q, want %q", id, label, session, wantSession)
	}
	if gotClosedAt := closedAt != ""; gotClosedAt != wantClosedAt {
		t.Errorf("%s %s closed_at = %q, want set = %v", id, label, closedAt, wantClosedAt)
	}
}

// RunIssueOperationsUpdateAssigneeTransferFence pins what an assignee edit does
// when it takes an issue away from a live foreign holder: it is refused with
// ErrAlreadyClaimed, and ForceAssigneeTransfer, an ExpectedAssignee
// compare-and-set, or a configured claim.pools alias are the only ways past it.
// The contract had no assignee-transfer case at all, and that gap is exactly how
// one backend came to permit a transfer the other two refuse.
func RunIssueOperationsUpdateAssigneeTransferFence(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	heldID := fixture.IssuePrefix + "-xferfence-held"
	seedClosePolicyIssue(t, ctx, fixture, heldID, publicops.CreateRequest{})
	claimed, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true})
	if err != nil {
		t.Fatalf("claim %s for holder: %v", heldID, err)
	}
	if !claimed.Changed {
		t.Fatalf("claiming %s reported Changed = false, want a committed claim", heldID)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")

	// The fence itself: an unforced transfer away from the live holder is
	// refused, and the refusal writes nothing — not the row, not an event.
	events := newIssueOperationsEventCounter(t, ctx, fixture, heldID)
	if _, err := fixture.Operations.Update(ctx, assigneeTransferRequest(heldID, "rival", "rival")); !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Fatalf("unforced transfer of %s away from its holder: err = %v, want ErrAlreadyClaimed", heldID, err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")
	events.assert(t, "refused transfer", 0, nil)

	// A stale precondition is orthogonal to the fence and is checked ahead of
	// it, so a request that fails both reports the precondition — the same
	// ordering a forced close-policy crossing gets.
	staleVersion := int64(-1)
	staleVersionRequest := assigneeTransferRequest(heldID, "rival", "rival")
	staleVersionRequest.ExpectedVersion = &staleVersion
	if _, err := fixture.Operations.Update(ctx, staleVersionRequest); !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("fenced transfer of %s with a stale version: err = %v, want ErrVersionMismatch", heldID, err)
	}
	staleStatus := types.StatusOpen
	staleStatusRequest := assigneeTransferRequest(heldID, "rival", "rival")
	staleStatusRequest.ExpectedStatus = &staleStatus
	if _, err := fixture.Operations.Update(ctx, staleStatusRequest); !errors.Is(err, publicops.ErrStatusMismatch) {
		t.Fatalf("fenced transfer of %s with a stale status: err = %v, want ErrStatusMismatch", heldID, err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")

	// Restating the holder's own name is not a transfer, so a third party may
	// do it unforced — and it changes nothing.
	reassert, err := fixture.Operations.Update(ctx, assigneeTransferRequest(heldID, "bystander", "holder"))
	if err != nil {
		t.Fatalf("reassert %s's current assignee: %v", heldID, err)
	}
	if reassert.Changed {
		t.Errorf("reasserting %s's current assignee reported Changed = true, want a no-op", heldID)
	}

	// An ExpectedAssignee compare-and-set naming the holder replaces the fence:
	// the caller proved its view of the claim is current.
	casRequest := assigneeTransferRequest(heldID, "rival", "rival")
	holder := "holder"
	casRequest.ExpectedAssignee = &holder
	cas, err := fixture.Operations.Update(ctx, casRequest)
	if err != nil {
		t.Fatalf("compare-and-set transfer of %s: %v", heldID, err)
	}
	if !cas.Changed || cas.Issue.Assignee != "rival" {
		t.Fatalf("compare-and-set transfer of %s = %#v, want a committed transfer to rival", heldID, cas.Issue)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "rival")

	// ForceAssigneeTransfer is the unconditional override.
	forcedRequest := assigneeTransferRequest(heldID, "usurper", "usurper")
	forcedRequest.ForceAssigneeTransfer = true
	forced, err := fixture.Operations.Update(ctx, forcedRequest)
	if err != nil {
		t.Fatalf("forced transfer of %s: %v", heldID, err)
	}
	if !forced.Changed || forced.Issue.Assignee != "usurper" {
		t.Fatalf("forced transfer of %s = %#v, want a committed transfer to usurper", heldID, forced.Issue)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "usurper")

	// A holder that is a configured claim.pools alias is a group placeholder,
	// not an owner, so taking work from the pool needs no force.
	if err := fixture.SetConfig(ctx, "claim.pools", "pool-crew"); err != nil {
		t.Fatalf("SetConfig(claim.pools): %v", err)
	}
	pooledID := fixture.IssuePrefix + "-xferfence-pooled"
	seedClosePolicyIssue(t, ctx, fixture, pooledID, publicops.CreateRequest{})
	pooledRequest := assigneeTransferRequest(pooledID, "seed", "pool-crew")
	pooledRequest.Claim = true
	if _, err := fixture.Operations.Update(ctx, pooledRequest); err != nil {
		t.Fatalf("assign %s to the pool: %v", pooledID, err)
	}
	assertLiveAssignee(t, ctx, fixture, pooledID, "pool-crew")
	taken, err := fixture.Operations.Update(ctx, assigneeTransferRequest(pooledID, "member", "member"))
	if err != nil {
		t.Fatalf("unforced transfer of pooled %s: %v", pooledID, err)
	}
	if !taken.Changed || taken.Issue.Assignee != "member" {
		t.Fatalf("unforced transfer of pooled %s = %#v, want a committed transfer to member", pooledID, taken.Issue)
	}
	assertLiveAssignee(t, ctx, fixture, pooledID, "member")

	// The alias set is the only carve-out: a real holder is still fenced while
	// pools are configured.
	if _, err := fixture.Operations.Update(ctx, assigneeTransferRequest(pooledID, "rival", "rival")); !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Fatalf("unforced transfer of %s away from a non-pool holder: err = %v, want ErrAlreadyClaimed", pooledID, err)
	}
	assertLiveAssignee(t, ctx, fixture, pooledID, "member")
}

// assigneeTransferRequest builds the bare assignee edit whose fencing this case
// pins: actor asks for the issue to be assigned to newAssignee.
func assigneeTransferRequest(id, actor, newAssignee string) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:   actor,
		IssueID: id,
		Patch:   publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: newAssignee}},
	}
}

// assertLiveAssignee checks the stored holder of an in-progress issue. Every
// state this case asserts is a live claim — the fence only speaks over one — so
// the expected status is fixed, and reading it back proves an assignee edit
// leaves the claim's status alone.
func assertLiveAssignee(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, wantAssignee string) {
	t.Helper()
	var assignee, status string
	if err := fixture.QueryScalar(ctx, "SELECT assignee, status FROM issues WHERE id = ?", []any{id}, &assignee, &status); err != nil {
		t.Fatalf("read assignee and status for %s: %v", id, err)
	}
	if assignee != wantAssignee {
		t.Errorf("%s assignee = %q, want %q", id, assignee, wantAssignee)
	}
	if types.Status(status) != types.StatusInProgress {
		t.Errorf("%s status = %q, want %q", id, status, types.StatusInProgress)
	}
}

// closePolicyStatusRequest builds the generic status update that crosses into
// the done category — the operation whose policy this case pins.
func closePolicyStatusRequest(id string, force bool) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:            "writer",
		IssueID:          id,
		ForceClosePolicy: force,
		Patch:            publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}},
	}
}

func assertClosePolicyStatus(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, want types.Status) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("read status for %s: %v", id, err)
	}
	if types.Status(got) != want {
		t.Errorf("%s status = %q, want %q", id, got, want)
	}
}

// seedClosePolicyIssue creates one open task at an explicit ID, carrying any
// relationships the close-policy case needs. It goes through Create rather than
// the fixture's raw seed hook so the edges recompute is_blocked exactly as a
// real create would.
func seedClosePolicyIssue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, request publicops.CreateRequest) {
	t.Helper()
	request.Actor = "seed"
	request.ForceIDPrefix = true
	request.Issue = &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if _, err := fixture.Operations.Create(ctx, request); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func assertIssueOperationsRowCount(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, table, id string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's hardcoded table names
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	if got != want {
		t.Errorf("%s rows for %s = %d, want %d", table, id, got, want)
	}
}

func assertIssueOperationsMetadata(t *testing.T, label string, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s metadata %s: %v", label, got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("%s want metadata %s: %v", label, want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s metadata = %s, want %s", label, got, want)
	}
}

// issueOperationsEventCounter reports how many event rows each operation adds
// for one issue.
type issueOperationsEventCounter struct {
	ctx     context.Context
	fixture IssueOperationsStagingFixture
	id      string
	total   int
	byType  map[types.EventType]int
}

func newIssueOperationsEventCounter(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string) *issueOperationsEventCounter {
	t.Helper()
	counter := &issueOperationsEventCounter{ctx: ctx, fixture: fixture, id: id, byType: map[types.EventType]int{}}
	counter.total = counter.count(t, "")
	for _, eventType := range []types.EventType{types.EventUpdated, types.EventStatusChanged} {
		counter.byType[eventType] = counter.count(t, eventType)
	}
	return counter
}

func (c *issueOperationsEventCounter) count(t *testing.T, eventType types.EventType) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM events WHERE issue_id = ?"
	args := []any{c.id}
	if eventType != "" {
		query += " AND event_type = ?"
		args = append(args, string(eventType))
	}
	var got int
	if err := c.fixture.QueryScalar(c.ctx, query, args, &got); err != nil {
		t.Fatalf("count events for %s (%q): %v", c.id, eventType, err)
	}
	return got
}

// assert checks the rows added since the previous assert and re-baselines.
func (c *issueOperationsEventCounter) assert(t *testing.T, label string, wantTotal int, wantByType map[types.EventType]int) {
	t.Helper()
	total := c.count(t, "")
	if got := total - c.total; got != wantTotal {
		t.Errorf("%s wrote %d event rows, want %d", label, got, wantTotal)
	}
	c.total = total
	for eventType, want := range wantByType {
		current := c.count(t, eventType)
		if got := current - c.byType[eventType]; got != want {
			t.Errorf("%s wrote %d %q events, want %d", label, got, eventType, want)
		}
	}
	for _, eventType := range []types.EventType{types.EventUpdated, types.EventStatusChanged} {
		c.byType[eventType] = c.count(t, eventType)
	}
}

// RunIssueOperationsUpdateClaimConflictCarriesTheLosingState pins the payload a
// lost claim comes back with. The leaf promises a *ClaimConflictError "carrying
// the state that beat it" (issueops/issueops.go:399-401) and says which sentinel
// each shape wears: a foreign assignment is ErrAlreadyClaimed, an ineligible
// status is ErrNotClaimable (issueops.go:215-217).
//
// The sentinel alone was already reachable; the TYPED fields were not. A caller
// that reports who won without parsing prose reads them, and the two
// implementations that build this error do so from separate reads — the
// store-backed body re-selects the row after a lost CAS
// (internal/storage/issueops/claim.go:154), the unit-of-work one takes what the
// repository handed back (internal/storage/domain/issue.go:566) — so nothing but
// a case over both spellings keeps the payload honest.
func RunIssueOperationsUpdateClaimConflictCarriesTheLosingState(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	heldID := fixture.IssuePrefix + "-claimconflict-held"
	seedClosePolicyIssue(t, ctx, fixture, heldID, publicops.CreateRequest{})
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true}); err != nil {
		t.Fatalf("claim %s for holder: %v", heldID, err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")

	// A foreign live claim: the refusal names the holder and the status that
	// beat the compare-and-set, and writes nothing.
	events := newIssueOperationsEventCounter(t, ctx, fixture, heldID)
	_, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "rival", IssueID: heldID, Claim: true})
	conflict := assertIssueOperationsClaimConflict(t, err, "foreign live claim", heldID)
	if conflict != nil {
		if conflict.Assignee != "holder" {
			t.Errorf("foreign claim conflict Assignee = %q, want %q", conflict.Assignee, "holder")
		}
		if conflict.Status != types.StatusInProgress {
			t.Errorf("foreign claim conflict Status = %q, want %q", conflict.Status, types.StatusInProgress)
		}
	}
	if !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Errorf("foreign claim error = %v, want ErrAlreadyClaimed", err)
	}
	if errors.Is(err, publicops.ErrNotClaimable) {
		t.Errorf("foreign claim error = %v, want it NOT to match ErrNotClaimable — the leaf gives the two shapes different sentinels", err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")
	events.assert(t, "refused foreign claim", 0, nil)

	// An ineligible status: nobody holds the issue, so the refusal carries the
	// status rather than an assignee, and wears the other sentinel.
	deferredID := fixture.IssuePrefix + "-claimconflict-deferred"
	seedClosePolicyIssue(t, ctx, fixture, deferredID, publicops.CreateRequest{})
	if err := fixture.UpdateRaw(ctx, deferredID, map[string]any{"status": string(types.StatusDeferred)}, "writer"); err != nil {
		t.Fatalf("defer %s: %v", deferredID, err)
	}
	deferredEvents := newIssueOperationsEventCounter(t, ctx, fixture, deferredID)
	_, err = fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "claimant", IssueID: deferredID, Claim: true})
	conflict = assertIssueOperationsClaimConflict(t, err, "ineligible status claim", deferredID)
	if conflict != nil {
		if conflict.Status != types.StatusDeferred {
			t.Errorf("ineligible-status conflict Status = %q, want %q", conflict.Status, types.StatusDeferred)
		}
		if conflict.Assignee != "" {
			t.Errorf("ineligible-status conflict Assignee = %q, want empty — nobody held it", conflict.Assignee)
		}
	}
	if !errors.Is(err, publicops.ErrNotClaimable) {
		t.Errorf("ineligible-status claim error = %v, want ErrNotClaimable", err)
	}
	if errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Errorf("ineligible-status claim error = %v, want it NOT to match ErrAlreadyClaimed", err)
	}
	assertIssueOperationsAssigneeAndStatus(t, ctx, fixture, deferredID, "", types.StatusDeferred)
	deferredEvents.assert(t, "refused ineligible claim", 0, nil)
}

// assertIssueOperationsClaimConflict checks the refusal is the typed conflict
// naming the issue, and hands it back so the caller can assert the payload. It
// reports rather than fatals on the type so one bad shape does not hide the
// other arm's evidence.
func assertIssueOperationsClaimConflict(t *testing.T, err error, label, id string) *publicops.ClaimConflictError {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: err = nil, want a claim conflict", label)
	}
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Errorf("%s: err = %v (%T), want *ClaimConflictError", label, err, err)
		return nil
	}
	if conflict.IssueID != id {
		t.Errorf("%s: conflict IssueID = %q, want %q", label, conflict.IssueID, id)
	}
	return conflict
}

// RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses pins the claim
// eligibility rule at the Lifecycle seam: the leaf says an issue is claimable
// from "built-in StatusOpen or a configured active status"
// (issueops/issueops.go:213-217), so a workspace that spells its own
// draft -> ready -> in_progress lifecycle can claim from ready, and a wip
// custom stays fenced.
//
// Both claim bodies resolve the vocabulary through
// issueops.ClaimableSourceStatusesInTx, but they build the SQL predicate around
// it separately (internal/storage/issueops/claim.go:65 vs
// internal/storage/domain/db/issue.go:373), and the only test that covered this
// spoke to one store's ClaimIssue rather than to the guarded verb.
func RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	if err := fixture.SetConfig(ctx, "status.custom", "ready:active,reviewing:wip"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}

	// The create path validates status against a vocabulary that does not parse
	// the "name:category" spelling, so each row is created open and moved with
	// the raw funnel — the same way a custom-status row comes to exist in a real
	// workspace.
	readyID := fixture.IssuePrefix + "-customclaim-ready"
	reviewingID := fixture.IssuePrefix + "-customclaim-reviewing"
	for _, seed := range []struct {
		id     string
		status types.Status
	}{{readyID, "ready"}, {reviewingID, "reviewing"}} {
		seedClosePolicyIssue(t, ctx, fixture, seed.id, publicops.CreateRequest{})
		if err := fixture.UpdateRaw(ctx, seed.id, map[string]any{"status": string(seed.status)}, "writer"); err != nil {
			t.Fatalf("move %s to %s: %v", seed.id, seed.status, err)
		}
	}

	claimed, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "agent-a", IssueID: readyID, Claim: true})
	if err != nil {
		t.Fatalf("claim %s from a configured active status: %v", readyID, err)
	}
	if !claimed.Changed {
		t.Errorf("claiming %s from a configured active status reported Changed = false, want a committed claim", readyID)
	}
	if claimed.Issue.Assignee != "agent-a" || claimed.Issue.Status != types.StatusInProgress {
		t.Errorf("claim result = assignee %q status %q, want agent-a/in_progress", claimed.Issue.Assignee, claimed.Issue.Status)
	}
	assertLiveAssignee(t, ctx, fixture, readyID, "agent-a")

	// A wip custom is not an active custom, so the anti-steal fence still holds
	// and the row is untouched.
	events := newIssueOperationsEventCounter(t, ctx, fixture, reviewingID)
	_, err = fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "agent-b", IssueID: reviewingID, Claim: true})
	if !errors.Is(err, publicops.ErrNotClaimable) {
		t.Fatalf("claim %s from a configured wip status: err = %v, want ErrNotClaimable", reviewingID, err)
	}
	assertIssueOperationsAssigneeAndStatus(t, ctx, fixture, reviewingID, "", "reviewing")
	events.assert(t, "refused wip-status claim", 0, nil)
}

// RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps pins the plane restriction
// the leaf declares on UpdateRequest.IssuePlaneOnly (issueops/issueops.go:251-260):
// with the flag set, an ID that names a wisp is ErrNotFound rather than an
// ephemeral row to update; with the zero value the same ID resolves and the
// update lands.
//
// It was pinned only against a stubbed unit of work, while the store-backed
// backends implement it in their shared execution body with no test at all.
func RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	wispID := fixture.IssuePrefix + "-planeonly-wisp"
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "seed", ForceIDPrefix: true,
		Issue: &types.Issue{ID: wispID, Title: "seeded title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	}); err != nil {
		t.Fatalf("seed wisp %s: %v", wispID, err)
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", wispID, 1)

	var beforeRowLock string
	if err := fixture.QueryScalar(ctx, "SELECT CAST(row_lock AS CHAR) FROM wisps WHERE id = ?", []any{wispID}, &beforeRowLock); err != nil {
		t.Fatalf("read wisp row lock for %s: %v", wispID, err)
	}
	restricted := publicops.UpdateRequest{
		Actor: "writer", IssueID: wispID, IssuePlaneOnly: true,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "restricted title"}},
	}
	if _, err := fixture.Operations.Update(ctx, restricted); !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("issue-plane-only update of wisp %s: err = %v, want ErrNotFound", wispID, err)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "wisp title after refused plane-only update", "seeded title",
		"SELECT title FROM wisps WHERE id = ?", []any{wispID})
	assertIssueOperationsScalarValue(t, ctx, fixture, "wisp row lock after refused plane-only update", beforeRowLock,
		"SELECT CAST(row_lock AS CHAR) FROM wisps WHERE id = ?", []any{wispID})

	// The zero value keeps the both-plane auto-resolve, so the same edit lands.
	unrestricted := restricted
	unrestricted.IssuePlaneOnly = false
	landed, err := fixture.Operations.Update(ctx, unrestricted)
	if err != nil {
		t.Fatalf("both-plane update of wisp %s: %v", wispID, err)
	}
	if !landed.Changed || landed.Issue.Title != "restricted title" {
		t.Fatalf("both-plane update of wisp %s = %#v, want the title edit committed", wispID, landed)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "wisp title after both-plane update", "restricted title",
		"SELECT title FROM wisps WHERE id = ?", []any{wispID})

	// The restriction is about the PLANE, not about the flag: a durable issue
	// updates normally with it set.
	durableID := fixture.IssuePrefix + "-planeonly-durable"
	seedClosePolicyIssue(t, ctx, fixture, durableID, publicops.CreateRequest{})
	durable, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: durableID, IssuePlaneOnly: true,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "durable title"}},
	})
	if err != nil {
		t.Fatalf("issue-plane-only update of durable %s: %v", durableID, err)
	}
	if !durable.Changed || durable.Issue.Title != "durable title" {
		t.Fatalf("issue-plane-only update of durable %s = %#v, want the title edit committed", durableID, durable)
	}
}

// RunIssueOperationsUpdateLabelPatchOrdering pins the order LabelPatch applies
// its three edits in: Replace, then Add, then Remove, "so removal wins when the
// same label appears in more than one edit" (issueops/issueops.go:56-58). A
// label named in every edit therefore ends up absent, and a patch that restates
// the current set is a no-op.
//
// The store-backed backends resolve the whole patch to a target set before
// touching the label tables (internal/storage/issueops/aggregate.go:276); the
// unit-of-work one replays the three edits as three separate use-case calls
// (internal/storage/domain/issue.go:648-680) and had no LabelPatch coverage of
// any kind.
func RunIssueOperationsUpdateLabelPatchOrdering(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-labelpatch"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, id, "old", "shared")
	assertIssueOperationsLabels(t, ctx, fixture, id, "seeded", "old", "shared")

	patched, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{
			Replace: publicops.Field[[]string]{Set: true, Value: []string{"replace", "shared"}},
			Add:     []string{"add", "shared"},
			Remove:  []string{"old", "shared"},
		},
	}})
	if err != nil {
		t.Fatalf("ordered label patch on %s: %v", id, err)
	}
	if !patched.Changed {
		t.Errorf("ordered label patch on %s reported Changed = false, want a committed edit", id)
	}
	// "shared" was replaced in, added again, and removed: removal wins. "old"
	// survived neither the replacement nor the removal.
	assertIssueOperationsStringSet(t, "ordered label patch result labels", patched.Issue.Labels, "add", "replace")
	assertIssueOperationsLabels(t, ctx, fixture, id, "after ordered label patch", "add", "replace")

	// A patch that restates the current set changes nothing.
	restated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"replace", "add"}}},
	}})
	if err != nil {
		t.Fatalf("restated label patch on %s: %v", id, err)
	}
	if restated.Changed {
		t.Errorf("restating %s's label set reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after restated label patch", "add", "replace")
}

// RunIssueOperationsUpdateLabelPatchValueRules pins what LabelPatch now says
// about the VALUES its edits carry, which it said nothing about before: the
// create-side field rules apply, so an overlong label is ErrFieldTooLong and
// the whole update writes nothing; repetition is free in both directions.
//
// The overlong leg is the one with teeth. The label tables are VARCHAR(255),
// so a backend that let the value through would SILENTLY TRUNCATE it and the
// caller would find a label it never asked for — which is why the case asserts
// the refusal AND that no row with that prefix landed, rather than only the
// error.
//
// The empty-string leg was the last thing in this file to be adjudicated
// (bd-yby99.29): the store bodies wrote a labels row keyed on "" and the
// unit-of-work one dropped the entry. Dropping won, so the assertion is a
// NO-OP rather than a partial write — an Add carrying only "" must leave the
// label set alone AND report Changed false, which is what tells a dropped
// entry apart from one that was written and then swept.
func RunIssueOperationsUpdateLabelPatchValueRules(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-labelvalues"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, id, "kept")

	overlong := strings.Repeat("x", types.MaxFieldLen+1)
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Add: []string{overlong}},
	}}); !errors.Is(err, publicops.ErrFieldTooLong) {
		t.Fatalf("adding a %d-character label: err = %v, want ErrFieldTooLong", len(overlong), err)
	}
	var truncated int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label LIKE 'xxx%'", []any{id}, &truncated); err != nil {
		t.Fatalf("look for a truncated label on %s: %v", id, err)
	}
	if truncated != 0 {
		t.Errorf("%s carries %d label rows from the refused overlong add, want none: the column would truncate it silently", id, truncated)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after the refused overlong add", "kept")

	// The same value twice in one edit is applied once.
	duplicated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Add: []string{"twice", "twice"}},
	}})
	if err != nil {
		t.Fatalf("adding one label twice in one edit on %s: %v", id, err)
	}
	if !duplicated.Changed {
		t.Errorf("adding a new label twice on %s reported Changed = false, want a committed edit", id)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after the duplicated add", "kept", "twice")

	// Removing a label the issue does not carry is a no-op.
	absent, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Remove: []string{"never-applied"}},
	}})
	if err != nil {
		t.Fatalf("removing an absent label from %s: %v", id, err)
	}
	if absent.Changed {
		t.Errorf("removing a label %s does not carry reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after removing an absent label", "kept", "twice")

	// An empty-string entry is dropped, and dropping it is a NO-OP: an Add
	// carrying only "" must not move Changed, because a backend that wrote the
	// row and swept it later would also leave the label set correct here.
	emptyOnly, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Add: []string{""}},
	}})
	if err != nil {
		t.Fatalf("adding an empty-string label to %s: %v", id, err)
	}
	if emptyOnly.Changed {
		t.Errorf("adding only an empty-string label to %s reported Changed = true, want a no-op", id)
	}
	var emptyRows int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ''", []any{id}, &emptyRows); err != nil {
		t.Fatalf("look for an empty label row on %s: %v", id, err)
	}
	if emptyRows != 0 {
		t.Errorf("%s carries %d label rows keyed on the empty string, want none", id, emptyRows)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after adding an empty-string label", "kept", "twice")

	// The same entry alongside a real one drops only itself, which is the
	// reason dropping beat refusing: one stray value does not fail the edit.
	mixed, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"kept", ""}}},
	}})
	if err != nil {
		t.Fatalf("replacing labels on %s with a real value and an empty one: %v", id, err)
	}
	if !mixed.Changed {
		t.Errorf("replacing %s's labels down to one value reported Changed = false, want a committed edit", id)
	}
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ''", []any{id}, &emptyRows); err != nil {
		t.Fatalf("look for an empty label row on %s after the mixed replace: %v", id, err)
	}
	if emptyRows != 0 {
		t.Errorf("%s carries %d label rows keyed on the empty string after a mixed replace, want none", id, emptyRows)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after the mixed replace", "kept")
}

// RunIssueOperationsUpdateParentIDReplacesTheParentEdge pins what a set
// IssuePatch.ParentID does (issueops/issueops.go:144-147): a nonempty value
// replaces the parent with exactly that target and "does not inherit labels" —
// the create-time InheritLabelsFromParent behavior must NOT follow a reparent —
// and a set empty value removes the parent-child edge. Both restatements are
// no-ops.
//
// The label clause is asserted nowhere today, and the unit-of-work backend
// reparents through its own use case (internal/storage/domain/dependency.go:296)
// rather than the shared target-set body the two stores share.
func RunIssueOperationsUpdateParentIDReplacesTheParentEdge(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	oldParentID := fixture.IssuePrefix + "-reparent-old"
	newParentID := fixture.IssuePrefix + "-reparent-new"
	childID := fixture.IssuePrefix + "-reparent-child"
	seedClosePolicyIssue(t, ctx, fixture, oldParentID, publicops.CreateRequest{})
	seedIssueOperationsLabeledIssue(t, ctx, fixture, newParentID, "parent-only-label")
	seedClosePolicyIssue(t, ctx, fixture, childID, publicops.CreateRequest{ParentID: oldParentID})
	assertIssueOperationsParents(t, ctx, fixture, childID, "seeded", oldParentID)

	reparented, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: newParentID},
	}})
	if err != nil {
		t.Fatalf("reparent %s: %v", childID, err)
	}
	if !reparented.Changed {
		t.Errorf("reparenting %s reported Changed = false, want a committed edit", childID)
	}
	assertIssueOperationsParents(t, ctx, fixture, childID, "after reparent", newParentID)
	assertIssueOperationsStringSet(t, "reparent result labels", reparented.Issue.Labels)
	assertIssueOperationsLabels(t, ctx, fixture, childID, "after reparent")

	restated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: newParentID},
	}})
	if err != nil {
		t.Fatalf("restate %s's parent: %v", childID, err)
	}
	if restated.Changed {
		t.Errorf("restating %s's parent reported Changed = true, want a no-op", childID)
	}
	assertIssueOperationsParents(t, ctx, fixture, childID, "after restated parent", newParentID)

	cleared, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: ""},
	}})
	if err != nil {
		t.Fatalf("clear %s's parent: %v", childID, err)
	}
	if !cleared.Changed {
		t.Errorf("clearing %s's parent reported Changed = false, want a committed edit", childID)
	}
	assertIssueOperationsParents(t, ctx, fixture, childID, "after cleared parent")

	recleared, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: ""},
	}})
	if err != nil {
		t.Fatalf("re-clear %s's parent: %v", childID, err)
	}
	if recleared.Changed {
		t.Errorf("re-clearing %s's parent reported Changed = true, want a no-op", childID)
	}
}

// RunIssueOperationsUpdateParentIDReplacesEveryParent pins the word ALL in the
// leaf's ParentID clause (issueops/issueops.go:144-147): a set nonempty value
// "atomically replaces all parents with exactly that target". A child can carry
// more than one parent edge — create takes ParentID and an explicit
// DepParentChild dependency in the same request — so "all" is a load-bearing
// word and not a restatement of the single-parent case.
func RunIssueOperationsUpdateParentIDReplacesEveryParent(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	firstID := fixture.IssuePrefix + "-multiparent-first"
	secondID := fixture.IssuePrefix + "-multiparent-second"
	thirdID := fixture.IssuePrefix + "-multiparent-third"
	childID := fixture.IssuePrefix + "-multiparent-child"
	for _, id := range []string{firstID, secondID, thirdID} {
		seedClosePolicyIssue(t, ctx, fixture, id, publicops.CreateRequest{})
	}
	seedClosePolicyIssue(t, ctx, fixture, childID, publicops.CreateRequest{
		ParentID:     firstID,
		Dependencies: []publicops.CreateDependency{{TargetID: secondID, Type: types.DepParentChild}},
	})
	assertIssueOperationsParents(t, ctx, fixture, childID, "seeded with two parents", firstID, secondID)

	replaced, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: childID, Patch: publicops.IssuePatch{
		ParentID: publicops.Field[string]{Set: true, Value: thirdID},
	}})
	if err != nil {
		t.Fatalf("replace every parent of %s: %v", childID, err)
	}
	if !replaced.Changed {
		t.Errorf("replacing every parent of %s reported Changed = false, want a committed edit", childID)
	}
	assertIssueOperationsParents(t, ctx, fixture, childID, "after replacing every parent", thirdID)
	for _, stale := range []string{firstID, secondID} {
		var present int
		if err := fixture.QueryScalar(ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = ?",
			[]any{childID, stale, string(types.DepParentChild)}, &present); err != nil {
			t.Fatalf("look up replaced parent %s of %s: %v", stale, childID, err)
		}
		if present != 0 {
			t.Errorf("%s kept its edge to replaced parent %s, want every prior parent removed", childID, stale)
		}
	}
}

// RunIssueOperationsUpdateMetadataReplaceClearsAndValidates pins
// MetadataPatch.Replace itself: it "replaces the complete metadata document",
// "a nil or empty Value clears metadata", and "a nonempty Value must be valid
// JSON". The exclusivity rule beside it is already pinned; the three clauses
// about the value are not — the clear was asserted only against a private
// unit-of-work helper, and neither arm was pinned behaviorally on any backend.
//
// It also pins the REPRESENTATION the clause now states: metadata is never SQL
// NULL, and an issue created with no metadata holds the same empty document a
// clear leaves behind. The create-side leg is what makes that one fact rather
// than two — without it, "cleared reads as {}" would still leave a caller
// unable to write one filter that matches both ways of having no metadata.
func RunIssueOperationsUpdateMetadataReplaceClearsAndValidates(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	bare := fixture.IssuePrefix + "-metadata-bare"
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: bare, Title: "no metadata at all", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", bare, err)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, bare, "created with no metadata", `{}`)
	assertIssueOperationsMetadataIsNotNull(t, ctx, fixture, bare, "created with no metadata")

	id := fixture.IssuePrefix + "-metadata-replace"
	issue := &types.Issue{
		ID: id, Title: "metadata replace", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Metadata: json.RawMessage(`{"keep":"old","drop":"old"}`),
	}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}

	// Invalid JSON is refused, and the stored document survives untouched.
	events := newIssueOperationsEventCounter(t, ctx, fixture, id)
	_, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"broken":`)}},
	}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("metadata replacement with invalid JSON: err = %v, want ErrValidation", err)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after refused replacement", `{"keep":"old","drop":"old"}`)
	events.assert(t, "refused metadata replacement", 0, nil)

	// A nonempty document replaces the whole value rather than merging into it.
	replaced, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"fresh":"new"}`)}},
	}})
	if err != nil {
		t.Fatalf("metadata replacement on %s: %v", id, err)
	}
	if !replaced.Changed {
		t.Errorf("metadata replacement on %s reported Changed = false, want a committed edit", id)
	}
	assertIssueOperationsMetadata(t, "metadata replacement", replaced.Issue.Metadata, `{"fresh":"new"}`)
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after replacement", `{"fresh":"new"}`)

	// A nil Value clears the document.
	cleared, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: nil}},
	}})
	if err != nil {
		t.Fatalf("metadata clear on %s: %v", id, err)
	}
	if !cleared.Changed {
		t.Errorf("metadata clear on %s reported Changed = false, want a committed edit", id)
	}
	// MetadataPatch.Replace now states the representation: clearing stores the
	// empty JSON document and the column is never NULL. Both halves are pinned,
	// because a backend that stored NULL would satisfy a JSON comparison of the
	// scanned value (the helper reads a NULL back as "null") and only the IS
	// NULL probe tells them apart — which is exactly the predicate a consumer
	// filtering on cleared metadata writes.
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after clear", `{}`)
	assertIssueOperationsMetadataIsNotNull(t, ctx, fixture, id, "after clear")

	// An empty Value clears an already-clear document, which is a no-op.
	recleared, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage{}}},
	}})
	if err != nil {
		t.Fatalf("metadata re-clear on %s: %v", id, err)
	}
	if recleared.Changed {
		t.Errorf("re-clearing %s's metadata reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after re-clear", `{}`)
}

// RunIssueOperationsRequestValuesAreNotMutated pins the leaf's promise that
// "implementations never mutate caller-owned request values"
// (issueops/issueops.go:377-384) and that results are detached snapshots
// (issueops.go:329-346). Everything a request carries by reference is at
// risk — the labels slice, the metadata bytes, the external-reference pointer,
// the issue struct itself — and the create body has a documented reason to want
// to write back into it: infra-type routing sets Ephemeral and ID minting fills
// in an ID, both on the attempt clone rather than on what the caller passed.
//
// The non-mutation half was pinned only on the unit-of-work backend and the
// detachment half only in one store's wisp test, so neither was stated once and
// answered by all three.
func RunIssueOperationsRequestValuesAreNotMutated(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	targetID := fixture.IssuePrefix + "-detach-target"
	seedClosePolicyIssue(t, ctx, fixture, targetID, publicops.CreateRequest{})

	externalRef := "caller-ref"
	callerLabels := []string{"caller-label"}
	callerMetadata := json.RawMessage(`{"caller":"owned"}`)
	callerIssue := &types.Issue{
		Title: "caller title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Labels: callerLabels, Metadata: callerMetadata, ExternalRef: &externalRef,
	}
	callerDependencies := []publicops.CreateDependency{{TargetID: targetID, Type: types.DepBlocks}}
	created, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer", Issue: callerIssue, Dependencies: callerDependencies,
	})
	if err != nil {
		t.Fatalf("create from a caller-owned request: %v", err)
	}

	// Nothing the caller handed over came back changed — including the ID field
	// the create filled in on its own copy and the Dependencies field it built
	// there.
	if callerIssue.ID != "" {
		t.Errorf("create wrote the minted ID %q back into the caller's issue", callerIssue.ID)
	}
	if callerIssue.Ephemeral {
		t.Error("create wrote its routing decision back into the caller's issue")
	}
	if len(callerIssue.Dependencies) != 0 {
		t.Errorf("create wrote %d dependency records back into the caller's issue", len(callerIssue.Dependencies))
	}
	if !reflect.DeepEqual(callerLabels, []string{"caller-label"}) {
		t.Errorf("create mutated the caller's labels slice: %v", callerLabels)
	}
	if string(callerMetadata) != `{"caller":"owned"}` {
		t.Errorf("create mutated the caller's metadata bytes: %s", callerMetadata)
	}
	if externalRef != "caller-ref" {
		t.Errorf("create mutated the caller's external reference: %q", externalRef)
	}
	if !reflect.DeepEqual(callerDependencies, []publicops.CreateDependency{{TargetID: targetID, Type: types.DepBlocks}}) {
		t.Errorf("create mutated the caller's dependency slice: %#v", callerDependencies)
	}

	// The result is a detached snapshot: corrupting it reaches neither the
	// caller's own values nor the stored row.
	createdID := created.Issue.ID
	if len(created.Issue.Labels) != 1 {
		t.Fatalf("create result labels = %v, want exactly the one requested label", created.Issue.Labels)
	}
	created.Issue.Labels[0] = "corrupted-label"
	if callerLabels[0] != "caller-label" {
		t.Errorf("the create result's labels alias the caller's slice: %v", callerLabels)
	}
	assertIssueOperationsLabels(t, ctx, fixture, createdID, "after corrupting the create result", "caller-label")
	assertIssueOperationsStoredMetadata(t, ctx, fixture, createdID, "after corrupting the create result", `{"caller":"owned"}`)

	// The same for update: its patch carries caller-owned collections too.
	patchAdd := []string{"added-label"}
	patchRemove := []string{"caller-label"}
	patchSet := map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}
	patchRef := "patched-ref"
	updated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: createdID,
		Patch: publicops.IssuePatch{
			ExternalRef: publicops.Field[*string]{Set: true, Value: &patchRef},
			Labels:      publicops.LabelPatch{Add: patchAdd, Remove: patchRemove},
			Metadata:    publicops.MetadataPatch{Set: patchSet},
		},
	})
	if err != nil {
		t.Fatalf("update from a caller-owned request: %v", err)
	}
	if !reflect.DeepEqual(patchAdd, []string{"added-label"}) || !reflect.DeepEqual(patchRemove, []string{"caller-label"}) {
		t.Errorf("update mutated the caller's label slices: add %v remove %v", patchAdd, patchRemove)
	}
	if !reflect.DeepEqual(patchSet, map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}) {
		t.Errorf("update mutated the caller's metadata map: %v", patchSet)
	}
	if patchRef != "patched-ref" {
		t.Errorf("update mutated the caller's external reference: %q", patchRef)
	}

	assertIssueOperationsStringSet(t, "update result labels", updated.Issue.Labels, "added-label")
	updated.Issue.Labels[0] = "corrupted-label"
	if patchAdd[0] != "added-label" {
		t.Errorf("the update result's labels alias the caller's slice: %v", patchAdd)
	}
	assertIssueOperationsLabels(t, ctx, fixture, createdID, "after corrupting the update result", "added-label")
	assertIssueOperationsStoredMetadata(t, ctx, fixture, createdID, "after corrupting the update result", `{"caller":"owned","added":"value"}`)
}

// RunIssueOperationsUpdateProvenanceLabelsHistory pins
// UpdateRequest.Provenance against the history the backend actually writes
// (issueops/issueops.go:261-270): the entry reads as the caller's own string,
// and the label "NEVER changes WHETHER history is recorded" — an update that
// records one records one with the field empty, and one that records none
// records none with it set.
//
// Every existing assertion about this reads a stub's captured commit message.
// All three fixtures are Dolt-backed, so the real log is readable here, which is
// the only place the claim "the entry reads as the caller's string" can be
// settled.
func RunIssueOperationsUpdateProvenanceLabelsHistory(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-provenance"
	seedClosePolicyIssue(t, ctx, fixture, id, publicops.CreateRequest{})

	const label = "conformance: provenance label"
	history := newIssueOperationsHistoryCounter(t, ctx, fixture)
	labeled, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
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
	const noopLabel = "conformance: provenance no-op"
	noOp, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
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
	unlabeled, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
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

// RunIssueOperationsUpdatePersistentPreservesUnversionedClass pins the half of
// the Persistence clause nobody asserts: "Persistent preserves an existing
// durable unversioned class" (issueops/issueops.go:135-136). The refusal beside
// it — an unversioned row cannot be demoted to a wisp mode — is pinned; this
// clause says the legal direction is a no-op that does NOT normalize the row
// into versioned storage.
func RunIssueOperationsUpdatePersistentPreservesUnversionedClass(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-unversioned"
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "seed", ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: id, Title: "unversioned", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			StorageClass: types.StorageClassUnversioned,
		},
	}); err != nil {
		t.Fatalf("seed unversioned %s: %v", id, err)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "seeded storage class", string(types.StorageClassUnversioned),
		"SELECT COALESCE(storage_class, '') FROM issues WHERE id = ?", []any{id})

	restated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModePersistent},
	}})
	if err != nil {
		t.Fatalf("restate %s as persistent: %v", id, err)
	}
	if restated.Changed {
		t.Errorf("restating unversioned %s as persistent reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "storage class after a persistent restatement", string(types.StorageClassUnversioned),
		"SELECT COALESCE(storage_class, '') FROM issues WHERE id = ?", []any{id})
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", id, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", id, 0)
}

// RunIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits pins the two
// compare-and-set preconditions as PRECONDITIONS ON A PLAIN EDIT. The
// assignee-transfer case above pins the ORDER the two guards resolve in beside
// a fenced transfer; here they gate an ordinary field update that no fence
// would touch, which is how `bd update --if-status`/`--if-assignee` actually
// reach the contract.
//
// The clauses: ExpectedAssignee "requires the current assignee to match" and
// ExpectedStatus "requires the current status to match"
// (issueops/issueops.go:250-256), under Lifecycle's standing promise that a
// "refusal or validation error leaves persistent state unchanged"
// (issueops/issueops.go:406-408). The SENTINELS come from
// RunIssueOperationsUpdateAssigneeTransferFence in this file.
//
// Both routes of `bd update` now send these guards to this contract instead of
// applying them beside their own read; the proxied one joined when its
// hand-rolled read-merge-write was deleted (bd-xt6de). A refusal that leaked a
// partial write would exit 13 — "another actor won the race, nothing was
// written, do not retry" — while having written.
func RunIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-guardgate"
	seedClosePolicyIssue(t, ctx, fixture, id, publicops.CreateRequest{})
	events := newIssueOperationsEventCounter(t, ctx, fixture, id)

	priorityEdit := func(priority int) publicops.UpdateRequest {
		return publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
			Priority: publicops.Field[int]{Set: true, Value: priority},
		}}
	}
	assertPriority := func(label string, want int) {
		t.Helper()
		assertIssueOperationsScalarValue(t, ctx, fixture, label, strconv.Itoa(want),
			"SELECT priority FROM issues WHERE id = ?", []any{id})
	}

	// The seeded row is open and unassigned, so the empty string is a real
	// "expected unassigned" guard rather than the absence of one — the
	// distinction `--if-assignee ''` depends on.
	unassigned := ""
	matching := priorityEdit(1)
	matching.ExpectedAssignee = &unassigned
	openStatus := types.StatusOpen
	matching.ExpectedStatus = &openStatus
	if result, err := fixture.Operations.Update(ctx, matching); err != nil || !result.Changed {
		t.Fatalf("guarded edit with both preconditions holding = %#v, %v; want the edit applied", result, err)
	}
	assertPriority("priority after a satisfied guard", 1)
	events.assert(t, "satisfied guard", 1, nil)

	// A stale status refuses, and the refusal writes nothing — not the field
	// the request carried, not an event.
	staleStatus := types.StatusInProgress
	staleStatusEdit := priorityEdit(0)
	staleStatusEdit.ExpectedStatus = &staleStatus
	if _, err := fixture.Operations.Update(ctx, staleStatusEdit); !errors.Is(err, publicops.ErrStatusMismatch) {
		t.Fatalf("edit guarded on a stale status: err = %v, want ErrStatusMismatch", err)
	}
	assertPriority("priority after a stale status guard", 1)
	events.assert(t, "stale status guard", 0, nil)

	// A stale assignee refuses the same way, including when the status guard
	// beside it still holds: both preconditions must hold, not either.
	staleAssignee := "nobody"
	staleAssigneeEdit := priorityEdit(0)
	staleAssigneeEdit.ExpectedAssignee = &staleAssignee
	staleAssigneeEdit.ExpectedStatus = &openStatus
	if _, err := fixture.Operations.Update(ctx, staleAssigneeEdit); !errors.Is(err, publicops.ErrAssigneeMismatch) {
		t.Fatalf("edit guarded on a stale assignee: err = %v, want ErrAssigneeMismatch", err)
	}
	assertPriority("priority after a stale assignee guard", 1)
	events.assert(t, "stale assignee guard", 0, nil)

	// The guard tracks the row rather than the request that set it: once an
	// assignee lands, the empty-string guard that just held is the stale one.
	assign := publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Assignee: publicops.Field[string]{Set: true, Value: "holder"},
	}}
	if _, err := fixture.Operations.Update(ctx, assign); err != nil {
		t.Fatalf("assign %s: %v", id, err)
	}
	events.assert(t, "assign", 1, nil)
	nowStale := priorityEdit(0)
	nowStale.ExpectedAssignee = &unassigned
	if _, err := fixture.Operations.Update(ctx, nowStale); !errors.Is(err, publicops.ErrAssigneeMismatch) {
		t.Fatalf("edit guarded on unassigned after an assignment: err = %v, want ErrAssigneeMismatch", err)
	}
	assertPriority("priority after the once-current guard went stale", 1)
	events.assert(t, "once-current guard", 0, nil)

	holder := "holder"
	nowCurrent := priorityEdit(0)
	nowCurrent.ExpectedAssignee = &holder
	if result, err := fixture.Operations.Update(ctx, nowCurrent); err != nil || !result.Changed {
		t.Fatalf("edit guarded on the current holder = %#v, %v; want the edit applied", result, err)
	}
	assertPriority("priority after a guard naming the current holder", 0)
}

// seedIssueOperationsLabeledIssue creates one open task at an explicit ID
// carrying labels, through the store seed hook rather than the guarded create,
// so the labels are already durable state when the case under test runs.
func seedIssueOperationsLabeledIssue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, labels ...string) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: labels}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed labeled %s: %v", id, err)
	}
}

// assertIssueOperationsLabels reads the stored label set back one membership
// query at a time. GROUP_CONCAT ordering is not portable across the three
// fixtures' SQL engines, and the set is what the contract is about.
func assertIssueOperationsLabels(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string, want ...string) {
	t.Helper()
	var total int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", []any{id}, &total); err != nil {
		t.Fatalf("count labels for %s (%s): %v", id, label, err)
	}
	if total != len(want) {
		t.Errorf("%s %s stored label count = %d, want %d (%v)", id, label, total, len(want), want)
	}
	for _, value := range want {
		var present int
		if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", []any{id, value}, &present); err != nil {
			t.Fatalf("look up label %q on %s (%s): %v", value, id, label, err)
		}
		if present != 1 {
			t.Errorf("%s %s stored label %q count = %d, want 1", id, label, value, present)
		}
	}
}

// assertIssueOperationsParents reads the stored outgoing parent-child edges.
func assertIssueOperationsParents(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string, want ...string) {
	t.Helper()
	var total int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = ?", []any{id, string(types.DepParentChild)}, &total); err != nil {
		t.Fatalf("count parents for %s (%s): %v", id, label, err)
	}
	if total != len(want) {
		t.Errorf("%s %s parent edge count = %d, want %d (%v)", id, label, total, len(want), want)
	}
	for _, parent := range want {
		var present int
		if err := fixture.QueryScalar(ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = ?",
			[]any{id, parent, string(types.DepParentChild)}, &present); err != nil {
			t.Fatalf("look up parent %s of %s (%s): %v", parent, id, label, err)
		}
		if present != 1 {
			t.Errorf("%s %s parent edge to %s count = %d, want 1", id, label, parent, present)
		}
	}
}

// assertIssueOperationsStringSet compares a result slice as a set, because no
// leaf clause promises an order for labels.
func assertIssueOperationsStringSet(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	gotSorted := append([]string(nil), got...)
	wantSorted := append([]string(nil), want...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(gotSorted, wantSorted) && (len(gotSorted) != 0 || len(wantSorted) != 0) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// assertIssueOperationsStoredMetadata reads the metadata column back and
// compares it as a document, since the three fixtures do not agree on
// whitespace or key order in the stored JSON.
func assertIssueOperationsStoredMetadata(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label, want string) {
	t.Helper()
	var stored string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(CAST(metadata AS CHAR), '') FROM issues WHERE id = ?", []any{id}, &stored); err != nil {
		t.Fatalf("read metadata for %s (%s): %v", id, label, err)
	}
	if stored == "" {
		stored = "null"
	}
	assertIssueOperationsMetadata(t, id+" "+label, json.RawMessage(stored), want)
}

// assertIssueOperationsMetadataIsNotNull is the half assertIssueOperations-
// StoredMetadata cannot make: it reads a NULL column back as the JSON literal
// "null" and compares values, so a backend that stored NULL where the leaf
// promises an empty document would need this probe to be caught. It is the
// predicate a consumer filtering on cleared metadata actually writes.
func assertIssueOperationsMetadataIsNotNull(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string) {
	t.Helper()
	var isNull bool
	if err := fixture.QueryScalar(ctx, "SELECT metadata IS NULL FROM issues WHERE id = ?", []any{id}, &isNull); err != nil {
		t.Fatalf("probe metadata nullability for %s (%s): %v", id, label, err)
	}
	if isNull {
		t.Errorf("%s metadata (%s) is SQL NULL, want the empty JSON document: metadata is never NULL", id, label)
	}
}

// assertIssueOperationsScalarValue reads one scalar and compares it, reporting
// rather than fatalling so a case keeps collecting evidence.
func assertIssueOperationsScalarValue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, label, want, query string, args []any) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if got != want {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

// assertIssueOperationsAssigneeAndStatus reads back the two columns a claim
// writes, for the refusals that must leave both alone.
func assertIssueOperationsAssigneeAndStatus(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, wantAssignee string, wantStatus types.Status) {
	t.Helper()
	var assignee, status string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, ''), status FROM issues WHERE id = ?", []any{id}, &assignee, &status); err != nil {
		t.Fatalf("read assignee and status for %s: %v", id, err)
	}
	if assignee != wantAssignee {
		t.Errorf("%s assignee = %q, want %q", id, assignee, wantAssignee)
	}
	if types.Status(status) != wantStatus {
		t.Errorf("%s status = %q, want %q", id, status, wantStatus)
	}
}

// issueOperationsHistoryCounter reports how many version-control entries each
// operation adds. It takes deltas rather than reading the top of the log
// because two commits made inside one second tie on date, so their relative
// order is not something to assert on.
type issueOperationsHistoryCounter struct {
	ctx     context.Context
	fixture IssueOperationsStagingFixture
	total   int
}

func newIssueOperationsHistoryCounter(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) *issueOperationsHistoryCounter {
	t.Helper()
	counter := &issueOperationsHistoryCounter{ctx: ctx, fixture: fixture}
	counter.total = counter.count(t, "")
	return counter
}

func (c *issueOperationsHistoryCounter) count(t *testing.T, message string) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM dolt_log"
	var args []any
	if message != "" {
		query += " WHERE message = ?"
		args = append(args, message)
	}
	var got int
	if err := c.fixture.QueryScalar(c.ctx, query, args, &got); err != nil {
		t.Fatalf("count history entries (%q): %v", message, err)
	}
	return got
}

// assertTotal checks the entries added since the previous assertTotal and
// re-baselines.
func (c *issueOperationsHistoryCounter) assertTotal(t *testing.T, label string, want int) {
	t.Helper()
	total := c.count(t, "")
	if got := total - c.total; got != want {
		t.Errorf("%s recorded %d history entries, want %d", label, got, want)
	}
	c.total = total
}

// assertMessage checks how many entries carry an exact message, which is the
// only way to tell the caller's spelling from the implementation's default.
func (c *issueOperationsHistoryCounter) assertMessage(t *testing.T, label, message string, want int) {
	t.Helper()
	if got := c.count(t, message); got != want {
		t.Errorf("%s left %d history entries reading %q, want %d", label, got, message, want)
	}
}
