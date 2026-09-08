package conformance

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract for publicops.Lifecycle.Create as the
// ROLE reached through storage.Storage.IssueLifecycle(), beside the Update half
// in lifecycle_update_contract.go and the close/reopen half in
// lifecycle_close_reopen_contract.go. One verb per file across three files, all
// three answered by the same accessor.
//
// It exists for the same reason the Update block does. Every create case in the
// package used to take IssueOperationsStagingFixture, whose QueryScalar field is
// a raw-SQL seam, so a backend that could not open a SQL connection — an
// HTTP-client leg, a Postgres leg — had no create coverage at all: not the
// occupied-id refusal, not the prefix guard, not the missing-target refusal, not
// the field surface a create is supposed to store. Those are the four things a
// caller of this verb finds out about first.
//
// WHAT THE RAW READS BECAME. Each staging case asserted its post-state as a pair
// of per-plane row counts, `SELECT COUNT(*) FROM issues|wisps WHERE id = ?`, and
// each of those is a question about ONE row that two reads answer exactly:
//
//   - "neither plane holds it" is GetIssue answering ErrNotFound. The read
//     resolves both planes, so a not-found is a not-found in both.
//   - "the wisp plane holds it and the issues plane does not" is GetIssue
//     answering an EPHEMERAL row: the durable plane is resolved first, so an
//     ephemeral answer is itself the proof no durable row shares the id.
//   - "the wisp plane does not hold it" — under an id the durable plane DOES
//     hold — is the one a both-plane read cannot answer, and it is what
//     WispExists is for.
//
// HOW MANY VOTES THE THREE LEGS ARE: two. The server-backed and embedded stores
// share one create body (internal/storage/issueops, PreparePublicCreateRequest
// into CreateIssuesInTxWithResult), so they are ONE reading of every rule below;
// the unit-of-work backend copies the request into its own createParams, which
// is the second. The field-surface case is where that split has teeth — a field
// dropped on the way into either shape is a column the caller asked for and did
// not get, reported as a success.

// LifecycleCreateFixture supplies adapter-specific storage access for the
// create-role assertions. Every field but GetIssue and WispExists is named and
// typed exactly like the per-backend roleFixtureKit hook it is filled from, so a
// wiring is kit plus accessor plus prefix with no adapter in between.
type LifecycleCreateFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database. It is also the workspace's CONFIGURED prefix,
	// which is what makes an id outside it foreign to the prefix guard.
	IssuePrefix string
	// Lifecycle is the role under test, reached through the backend's
	// capability accessor rather than a constructor.
	Lifecycle publicops.Lifecycle
	// CreateIssue seeds a durable issue in the issues plane, including its
	// labels. It is a SEPARATE hook from the subject even here, where the
	// subject is Create itself: a case that seeds through the verb it is testing
	// cannot tell a refusal from a seed that never landed, and the parent whose
	// labels an inheriting create copies has to be durable state before the
	// create under test runs.
	CreateIssue func(context.Context, *types.Issue, string) error
	// GetIssue reads a row back from BOTH planes, durable first, and reports
	// ErrNotFound when neither holds one.
	//
	// It is this contract's OUT-OF-BAND hook, built at each wiring site over a
	// seam the backend already publishes, the way LifecycleUpdateFixture.GetIssue
	// and CycleDetectorFixture.Exec are. The issue it answers carries every
	// column these cases assert on — the whole scalar surface, the label set and
	// the ephemeral/no-history flags that say which plane the row lives in.
	GetIssue func(context.Context, string) (*types.Issue, error)
	// WispExists reports whether the EPHEMERAL plane holds a row at id.
	//
	// The id space is SHARED between the two planes, and that is the whole
	// subject of the occupied-id case: a create aimed at either plane must
	// refuse an id the other one already holds, and must leave no row behind
	// when it does. A both-plane read resolves the durable row first, so the
	// stray wisp a refused ephemeral create might have written under an occupied
	// durable id is invisible to it.
	//
	// A nil WispExists means "this backend has no separable ephemeral plane".
	// The absence probes are then DROPPED with a t.Log naming what stopped
	// being proven, and the case around them still runs: the occupied-id
	// refusal itself is the shipped bug this contract exists to catch, and it
	// is proven by the row read.
	WispExists func(context.Context, string) (bool, error)
}

// RunLifecycleCreateRejectsMissingDependencyTargets pins the facade create
// against reporting success for an issue whose requested relationships were
// never written. The batch engine tolerates a dangling edge so a partial import
// still lands; a guarded single create must refuse the whole request with a
// typed error naming the target, and leave nothing behind.
//
// All three ways a request can name a target are covered, because each reaches
// the resolver by a different field: an explicit dependency, a parent, and a
// waits-for spawner.
func RunLifecycleCreateRejectsMissingDependencyTargets(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	seed := fixture.IssuePrefix + "-lcc-skipdep-seed"
	seedLifecycleCreateIssue(t, ctx, fixture, lifecycleCreateIssue(seed))

	for _, tc := range []struct {
		name    string
		id      string
		request publicops.CreateRequest
		target  string
	}{
		{
			name:   "explicit dependency",
			id:     fixture.IssuePrefix + "-lcc-skipdep-explicit",
			target: fixture.IssuePrefix + "-lcc-skipdep-missing-dep",
			request: publicops.CreateRequest{
				Dependencies: []publicops.CreateDependency{{TargetID: fixture.IssuePrefix + "-lcc-skipdep-missing-dep", Type: types.DepBlocks}},
			},
		},
		{
			name:    "parent",
			id:      fixture.IssuePrefix + "-lcc-skipdep-parent",
			target:  fixture.IssuePrefix + "-lcc-skipdep-missing-parent",
			request: publicops.CreateRequest{ParentID: fixture.IssuePrefix + "-lcc-skipdep-missing-parent"},
		},
		{
			name:   "waits-for spawner",
			id:     fixture.IssuePrefix + "-lcc-skipdep-waits",
			target: fixture.IssuePrefix + "-lcc-skipdep-missing-spawner",
			request: publicops.CreateRequest{
				WaitsFor: &publicops.WaitsFor{SpawnerID: fixture.IssuePrefix + "-lcc-skipdep-missing-spawner"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			request.Actor = "writer"
			request.ForceIDPrefix = true
			request.Issue = &types.Issue{ID: tc.id, Title: tc.name, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			_, err := fixture.Lifecycle.Create(ctx, request)
			if err == nil {
				t.Fatal("Create returned nil error, want a refusal for the missing dependency target")
			}
			if !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("Create error = %v, want ErrNotFound", err)
			}
			if !errors.Is(err, storage.ErrValidation) {
				t.Errorf("Create error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("Create error = %v, want it to name the missing target %q", err, tc.target)
			}
			// Neither plane holds the refused id, which is what a both-plane
			// not-found says: the refusal left no partial row anywhere.
			assertLifecycleCreateAbsent(t, ctx, fixture, tc.id, "after the refused create")
		})
	}

	// A create whose targets all exist is unaffected, which is what makes the
	// three refusals above a guard rather than a create that never works.
	result, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         lifecycleCreateIssue(fixture.IssuePrefix + "-lcc-skipdep-ok"),
		Dependencies:  []publicops.CreateDependency{{TargetID: seed, Type: types.DepBlocks}},
	})
	if err != nil {
		t.Fatalf("Create with existing target: %v", err)
	}
	if len(result.Issue.Dependencies) != 1 || result.Issue.Dependencies[0].DependsOnID != seed {
		t.Fatalf("Create result dependencies = %#v, want one edge to %s", result.Issue.Dependencies, seed)
	}
}

// RunLifecycleCreateRefusesAnOccupiedID pins the create-only half of the
// Lifecycle.Create clause: "an occupied ID returns ErrAlreadyExists and leaves
// persistent state unchanged" (issueops/issueops.go, Lifecycle.Create). The
// issue and wisp tables share one ID space, so ACROSS is asserted in both
// directions here, not only for the plane the create happens to target.
//
// The proxied-server `bd create` route asked its use case for a plain create
// with no create-only guard, so `bd create --id <occupied>` silently UPSERTED
// the stored row and reported success while the direct route refused.
//
// THIS CASE AND conformance.go's testCreateDuplicate PIN OPPOSITE SEMANTICS OF
// THE SAME CORE. That one drives the raw CreateIssue verb, which UPSERTS, and
// asserts only that the second write leaves exactly one row; this one drives the
// create-only role and asserts a typed refusal with the stored row unchanged.
// They read like duplicates and are not: retiring either against the other
// deletes the only proof of one of the two behaviors.
func RunLifecycleCreateRefusesAnOccupiedID(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	occupied := fixture.IssuePrefix + "-lcc-occupied-issue"
	seeded := lifecycleCreateIssue(occupied, "lcc-seeded")
	seedLifecycleCreateIssue(t, ctx, fixture, seeded)

	_, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: occupied, Title: "overwriting title", Status: types.StatusOpen,
			Priority: 1, IssueType: types.TypeBug, Labels: []string{"lcc-overwriting"},
		},
	})
	assertLifecycleCreateAlreadyExists(t, err, "durable create over an occupied durable ID", occupied)
	// "leaves persistent state unchanged" is the load-bearing half: an upsert
	// reported as a refusal would still have rewritten every column.
	stored := lifecycleCreateRow(t, ctx, fixture, occupied)
	if stored.Ephemeral || stored.NoHistory {
		t.Errorf("%s came back {ephemeral %v, no-history %v}, want the durable row the seed left", occupied, stored.Ephemeral, stored.NoHistory)
	}
	if stored.Title != occupied {
		t.Errorf("occupied issue title = %q, want the seeded %q", stored.Title, occupied)
	}
	if stored.IssueType != types.TypeTask {
		t.Errorf("occupied issue type = %q, want the seeded %q", stored.IssueType, types.TypeTask)
	}
	assertLifecycleCreateLabels(t, "after the refused durable create", stored, "lcc-seeded")
	assertLifecycleCreateWispAbsent(t, ctx, fixture, occupied, "after the refused durable create")

	// An ID occupied by a WISP refuses a durable create.
	wispID := fixture.IssuePrefix + "-lcc-occupied-wisp"
	if _, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: wispID, Title: "resident wisp", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		},
	}); err != nil {
		t.Fatalf("seed resident wisp: %v", err)
	}
	assertLifecycleCreateResidentWisp(t, ctx, fixture, wispID, "seeded", "resident wisp")

	_, err = fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: wispID, Title: "durable squatter", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		},
	})
	assertLifecycleCreateAlreadyExists(t, err, "durable create over an occupied wisp ID", wispID)
	// An ephemeral answer from a durable-first read is the whole assertion: the
	// refused durable create wrote no row in the plane it was aimed at, and the
	// wisp it collided with still reads as it was seeded.
	assertLifecycleCreateResidentWisp(t, ctx, fixture, wispID, "after the refused durable create", "resident wisp")

	// And the other direction: an ID occupied by a durable issue refuses an
	// ephemeral create.
	_, err = fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: occupied, Title: "wisp squatter", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		},
	})
	assertLifecycleCreateAlreadyExists(t, err, "ephemeral create over an occupied durable ID", occupied)
	assertLifecycleCreateWispAbsent(t, ctx, fixture, occupied, "after the refused ephemeral create")
	if got := lifecycleCreateRow(t, ctx, fixture, occupied).Title; got != occupied {
		t.Errorf("occupied issue title after the wisp squat = %q, want the seeded %q", got, occupied)
	}
}

// RunLifecycleCreateRefusesAForeignIDPrefix pins the guard
// CreateRequest.ForceIDPrefix exists to lift: the flag "permits an explicit ID
// outside the configured prefix" (issueops/issueops.go:208-209), so without it
// such an ID is ErrPrefixMismatch — "returned when an issue ID does not match
// the configured prefix" (issueops/errors.go:81-82) — under Create's standing
// promise that "a refusal or validation error also leaves no partial persistent
// state".
//
// Every other create case sets ForceIDPrefix, which is what makes this one
// necessary: the flag is asserted only from the side that bypasses the check, so
// nothing else says the check exists. The refusal is TYPED because both front
// doors decide whether to re-offer the create with --force from errors.Is rather
// than from the message, and it is checked on BOTH planes because an ephemeral
// create routes to a different table and could plausibly skip a guard the
// durable one applies.
func RunLifecycleCreateRefusesAForeignIDPrefix(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	// A prefix no fixture configures, so the ID is foreign whatever this
	// workspace calls itself.
	for _, tc := range []struct {
		name      string
		id        string
		ephemeral bool
	}{
		{name: "durable", id: "lccforeign-" + fixture.IssuePrefix + "-1"},
		{name: "ephemeral", id: "lccforeign-" + fixture.IssuePrefix + "-2", ephemeral: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
				Actor: "writer",
				Issue: &types.Issue{
					ID: tc.id, Title: tc.name, Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask, Ephemeral: tc.ephemeral,
				},
			})
			if !errors.Is(err, storage.ErrPrefixMismatch) {
				t.Fatalf("unforced create at the foreign ID %q: err = %v, want ErrPrefixMismatch", tc.id, err)
			}
			assertLifecycleCreateAbsent(t, ctx, fixture, tc.id, "after the unforced foreign-prefix create")

			// The same request with the flag lands, which is what makes the
			// refusal a policy the caller can override rather than a hard limit.
			forced, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
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
			stored := lifecycleCreateRow(t, ctx, fixture, tc.id)
			if got := stored.Ephemeral || stored.NoHistory; got != tc.ephemeral {
				t.Errorf("forced create at %q landed in the %s plane, want the %s one",
					tc.id, lifecycleCreatePlaneName(got), lifecycleCreatePlaneName(tc.ephemeral))
			}
		})
	}
}

// RunLifecycleCreateInheritsParentLabels pins
// CreateRequest.InheritLabelsFromParent — "copies the parent's labels at
// creation" — against CreateRequest.Issue's own "Labels are authoritative" (both
// issueops/issueops.go, CreateRequest). One create must satisfy both.
//
// The two `bd create --parent` front doors spell it differently — the direct
// route merges the parent's labels itself so its --dry-run preview can show them
// and leaves the flag off, the proxied route sets the flag — so the merge has to
// be the same set either way.
func RunLifecycleCreateInheritsParentLabels(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	parent := fixture.IssuePrefix + "-lcc-inherit-parent"
	seedLifecycleCreateIssue(t, ctx, fixture, lifecycleCreateIssue(parent, "lcc-shared", "lcc-from-parent"))

	inherited, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:                   "writer",
		ForceIDPrefix:           true,
		ParentID:                parent,
		InheritLabelsFromParent: true,
		Issue: &types.Issue{
			Title: "inheriting child", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Labels: []string{"lcc-own", "lcc-shared"},
		},
	})
	if err != nil {
		t.Fatalf("Create inheriting child: %v", err)
	}
	// A label the child and the parent both carry is one label, not two: the
	// set comparison is what says so.
	assertLifecycleCreateLabels(t, "inheriting child result", inherited.Issue, "lcc-own", "lcc-shared", "lcc-from-parent")
	assertLifecycleCreateLabels(t, "inheriting child row",
		lifecycleCreateRow(t, ctx, fixture, inherited.Issue.ID), "lcc-own", "lcc-shared", "lcc-from-parent")

	// With inheritance off, the request's own labels are the whole set — the
	// authoritative clause standing alone.
	own, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		ParentID:      parent,
		Issue: &types.Issue{
			Title: "own labels only", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Labels: []string{"lcc-own"},
		},
	})
	if err != nil {
		t.Fatalf("Create child without inheritance: %v", err)
	}
	assertLifecycleCreateLabels(t, "child without inheritance result", own.Issue, "lcc-own")
	assertLifecycleCreateLabels(t, "child without inheritance row", lifecycleCreateRow(t, ctx, fixture, own.Issue.ID), "lcc-own")

	// Inheriting from a label-less parent adds nothing rather than failing.
	bare := fixture.IssuePrefix + "-lcc-inherit-bare-parent"
	seedLifecycleCreateIssue(t, ctx, fixture, lifecycleCreateIssue(bare))
	none, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
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
	assertLifecycleCreateLabels(t, "child of label-less parent result", none.Issue)
	assertLifecycleCreateLabels(t, "child of label-less parent row", lifecycleCreateRow(t, ctx, fixture, none.Issue.ID))
}

// RunLifecycleCreateWritesEveryScalarField pins the whole scalar and pointer
// surface a create carries — seventeen members — against the row it leaves
// behind. Lifecycle.Create takes a whole issue rather than a patch, and each
// backend copies that issue into its own create shape, so a field dropped on the
// way in is a column the caller asked for and did not get, reported as a
// success.
//
// EVERY VALUE IS DISTINCT AND NON-ZERO, which is what makes a dropped field
// observable — a field the body never copied arrives at its zero value, and a
// request that agreed with that zero value could not tell the two apart. The
// status is in_progress rather than open for exactly that reason: open is what
// an empty status defaults to.
//
// The ROW is the subject, because a result hydrated from the request would echo
// what the caller just handed in. The result is then held to the same
// expectation, because it is what a front door renders: a create that stored
// every column and hydrated one of them away is a field the caller still cannot
// see.
//
// THE CREATION STAMP RIDES ALONG, in both directions, because create is the one
// verb entitled to write it and nothing else says so — Lifecycle.Update's own
// contract pins that an edit LEAVES the pair alone, which is a statement about
// the wrong verb. A caller supplying created_at and created_by is every import,
// restore and tracker-sync path in the tree: they exist to reproduce a history,
// and a body that overwrote the pair with "now" and "the importing agent" would
// relabel the whole backlog as today's work. A caller supplying NEITHER gets a
// stamp anyway, which is what `bd list --stale` and every age-based report read.
//
// The preset is years in the past on purpose: created_at is DATETIME(0), so a
// preset at "now" and a stamp of "now" are the same stored bytes and the case
// could not tell "honored" from "overwritten". The auto-stamp is BOUNDED rather
// than merely non-empty, because the zero time is non-empty too.
func RunLifecycleCreateWritesEveryScalarField(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lcc-createsurface"
	minutes := 33
	externalRef := "created-ref"
	dueAt := time.Date(2033, 5, 6, 7, 8, 9, 0, time.UTC)
	deferUntil := time.Date(2033, 4, 5, 6, 7, 8, 0, time.UTC)
	createdAt := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	created, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: id, Title: "created title", Description: "created description", Design: "created design",
			AcceptanceCriteria: "created acceptance", Notes: "created notes",
			SpecID: "created-spec", AwaitID: "created-await",
			Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeBug,
			Assignee: "created-assignee", Owner: "created-owner", ClosedBySession: "created-session",
			EstimatedMinutes: &minutes, ExternalRef: &externalRef,
			DueAt: &dueAt, DeferUntil: &deferUntil,
			CreatedAt: createdAt, CreatedBy: "created-author",
		},
	})
	if err != nil {
		t.Fatalf("full scalar create of %s: %v", id, err)
	}
	if created.Issue == nil {
		t.Fatalf("full scalar create of %s returned no issue", id)
	}

	// The preset stamp, on the row and on the result.
	assertLifecycleCreateCreationStamp(t, id, "in the stored row", lifecycleCreateRow(t, ctx, fixture, id), createdAt, "created-author")
	assertLifecycleCreateCreationStamp(t, id, "in the create result", created.Issue, createdAt, "created-author")

	// The other direction: a request that names no creation time still gets
	// one. An empty created_at is a row that sorts and ages as though it were
	// created at the zero time.
	stamped := fixture.IssuePrefix + "-lcc-createstamp"
	lower := time.Now().UTC().Add(-lifecycleCreateClockSlack)
	if _, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         lifecycleCreateIssue(stamped),
	}); err != nil {
		t.Fatalf("create %s without a creation time: %v", stamped, err)
	}
	upper := time.Now().UTC().Add(lifecycleCreateClockSlack)
	autoStamp := lifecycleCreateRow(t, ctx, fixture, stamped).CreatedAt.UTC()
	if autoStamp.Before(lower) || autoStamp.After(upper) {
		t.Errorf("%s created_at = %v after a create that named none, want a stamp between %v and %v — a bare non-empty check would accept the zero time",
			stamped, autoStamp, lower, upper)
	}

	want := []lifecycleCreateMember{
		{"title", "created title"},
		{"description", "created description"},
		{"design", "created design"},
		{"acceptance criteria", "created acceptance"},
		{"notes", "created notes"},
		{"spec id", "created-spec"},
		{"await id", "created-await"},
		{"status", string(types.StatusInProgress)},
		{"priority", "1"},
		{"issue type", string(types.TypeBug)},
		{"assignee", "created-assignee"},
		{"owner", "created-owner"},
		{"closed by session", "created-session"},
		{"estimated minutes", "33"},
		{"external ref", "created-ref"},
		{"due at", dueAt.Format(time.RFC3339)},
		{"defer until", deferUntil.Format(time.RFC3339)},
	}
	assertLifecycleCreateMembers(t, id, "in the stored row", lifecycleCreateRow(t, ctx, fixture, id), want)
	assertLifecycleCreateMembers(t, id, "in the create result", created.Issue, want)
}

// lifecycleCreateMember names one member of the scalar surface and the value it
// is expected to hold, so a surface assertion reports WHICH member disagreed.
type lifecycleCreateMember struct {
	name  string
	value string
}

// lifecycleCreateMembers renders the seventeen-member scalar surface off an
// issue as text, so the row and the result answer one expectation.
func lifecycleCreateMembers(issue *types.Issue) []lifecycleCreateMember {
	minutes := ""
	if issue.EstimatedMinutes != nil {
		minutes = strconv.Itoa(*issue.EstimatedMinutes)
	}
	return []lifecycleCreateMember{
		{"title", issue.Title},
		{"description", issue.Description},
		{"design", issue.Design},
		{"acceptance criteria", issue.AcceptanceCriteria},
		{"notes", issue.Notes},
		{"spec id", issue.SpecID},
		{"await id", issue.AwaitID},
		{"status", string(issue.Status)},
		{"priority", strconv.Itoa(issue.Priority)},
		{"issue type", string(issue.IssueType)},
		{"assignee", issue.Assignee},
		{"owner", issue.Owner},
		{"closed by session", issue.ClosedBySession},
		{"estimated minutes", minutes},
		{"external ref", issueTextOrEmpty(issue.ExternalRef)},
		{"due at", lifecycleCreateStamp(issue.DueAt)},
		{"defer until", lifecycleCreateStamp(issue.DeferUntil)},
	}
}

func assertLifecycleCreateMembers(t *testing.T, id, label string, issue *types.Issue, want []lifecycleCreateMember) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s %s: no issue to read the scalar surface off", id, label)
	}
	got := lifecycleCreateMembers(issue)
	if len(got) != len(want) {
		t.Fatalf("%s %s reported %d members, want %d", id, label, len(got), len(want))
	}
	for i, member := range want {
		if got[i].value != member.value {
			t.Errorf("%s %s %s = %q, want %q", id, label, member.name, got[i].value, member.value)
		}
	}
}

// lifecycleCreateClockSlack widens the window the auto-stamp arm allows around a
// creation time the implementation wrote from its own clock. Every backend here
// stamps in Go, in this process, so the two clocks are the same one; the slack
// covers the truncation to whole seconds and a slow call, not a clock skew.
const lifecycleCreateClockSlack = 5 * time.Second

// assertLifecycleCreateCreationStamp holds the pair a create is entitled to
// write against what the caller asked for. created_at is compared with Equal
// rather than as text, because the column is second-granular and the two sides
// carry different locations.
func assertLifecycleCreateCreationStamp(t *testing.T, id, label string, issue *types.Issue, wantAt time.Time, wantBy string) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s %s: no issue to read the creation stamp off", id, label)
	}
	if !issue.CreatedAt.Equal(wantAt) {
		t.Errorf("%s %s created_at = %v, want the preset %v — a create that stamps its own clock relabels an import as today's work",
			id, label, issue.CreatedAt.UTC(), wantAt)
	}
	if issue.CreatedBy != wantBy {
		t.Errorf("%s %s created_by = %q, want the preset %q — the importing agent is not the author", id, label, issue.CreatedBy, wantBy)
	}
}

// lifecycleCreateStamp renders a nullable timestamp at second precision in UTC,
// which is what the DATETIME columns carrying these members store.
func lifecycleCreateStamp(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func lifecycleCreateIssue(id string, labels ...string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    labels,
	}
}

func seedLifecycleCreateIssue(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func lifecycleCreateRow(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture, id string) *types.Issue {
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

// assertLifecycleCreateAbsent checks that NEITHER plane holds a row at id. The
// read resolves both, so a not-found is the whole assertion — it is the same
// fact the staging case spelled as two per-plane row counts at zero.
func assertLifecycleCreateAbsent(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture, id, label string) {
	t.Helper()
	issue, err := fixture.GetIssue(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return
		}
		t.Fatalf("read back %s (%s): %v", id, label, err)
	}
	if issue != nil {
		t.Errorf("%s %s: a row exists at this id, want neither plane to hold one", id, label)
	}
}

// assertLifecycleCreateWispAbsent checks the EPHEMERAL plane holds no row at id.
// It is the half a both-plane read cannot make: that read resolves the durable
// row first, so a stray wisp under an occupied durable id never reaches it.
//
// A backend with no separable ephemeral plane DEGRADES here rather than skipping
// the case around it. The occupied-id refusal itself — the `bd create --id
// <occupied>` upsert this contract exists to catch — is proven by the row read,
// and losing one absence probe is not a reason to stop proving it. The drop is
// logged so a green run is not mistaken for the whole assertion.
func assertLifecycleCreateWispAbsent(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture, id, label string) {
	t.Helper()
	if fixture.WispExists == nil {
		t.Logf("fixture has no WispExists: %s %s is DROPPED — this backend cannot say whether the "+
			"ephemeral plane holds a row under an id the durable plane already holds", id, label)
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

// assertLifecycleCreateResidentWisp checks that id resolves to an EPHEMERAL row
// carrying title. The durable plane is resolved first, so an ephemeral answer is
// itself the proof that no durable row shares the id.
func assertLifecycleCreateResidentWisp(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture, id, label, wantTitle string) {
	t.Helper()
	issue := lifecycleCreateRow(t, ctx, fixture, id)
	if !issue.Ephemeral && !issue.NoHistory {
		t.Errorf("%s %s resolved to a durable row, want the wisp the seed left — the durable plane is read first, so this id is now occupied twice", id, label)
	}
	if issue.Title != wantTitle {
		t.Errorf("%s %s title = %q, want %q", id, label, issue.Title, wantTitle)
	}
}

// assertLifecycleCreateAlreadyExists checks the refusal an occupied ID gets. The
// message must name the ID: a caller refused with a bare "issue already exists"
// cannot act on it.
func assertLifecycleCreateAlreadyExists(t *testing.T, err error, label, id string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: Create returned nil error, want ErrAlreadyExists", label)
	}
	if !errors.Is(err, storage.ErrAlreadyExists) {
		t.Errorf("%s: Create error = %v, want ErrAlreadyExists", label, err)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("%s: Create error = %v, want it to name the occupied ID %q", label, err, id)
	}
}

// assertLifecycleCreateLabels compares a label set, because no leaf clause
// promises an order for labels.
func assertLifecycleCreateLabels(t *testing.T, label string, issue *types.Issue, want ...string) {
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

// lifecycleCreatePlaneName spells which plane a row landed in, for the failure
// message on a create whose routing came out wrong.
func lifecycleCreatePlaneName(ephemeral bool) string {
	if ephemeral {
		return "ephemeral"
	}
	return "durable"
}
