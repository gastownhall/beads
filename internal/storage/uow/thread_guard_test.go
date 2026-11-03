package uow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// threadGuardFixture is what the thread-guard cases need from a backend. The
// dolt package's thread_guard_test.go builds the same cases on the direct
// store; this one builds them on the unit-of-work deleter, whose guard runs
// the store body's check on its statement runner (deleter.go).
type threadGuardFixture struct {
	deleter publicops.Deleter
	imager  publicops.Imager
	create  func(context.Context, *types.Issue) error
	addEdge func(ctx context.Context, from, to string, typ types.DependencyType) error
	// setDescription rewrites one thread member's (a wisp's) description.
	setDescription func(ctx context.Context, id, text string) error
	scalar         func(ctx context.Context, query string, args []any, dest ...any) error
}

const (
	threadGuardMailLabel    = "thread-mail"
	threadGuardExcludeLabel = "thread-keep"
)

// threadGuardTables are the ten tables a delete can touch. A refused delete
// must leave every count where it was.
var threadGuardTables = []string{
	"issues", "wisps",
	"labels", "wisp_labels",
	"dependencies", "wisp_dependencies",
	"comments", "wisp_comments",
	"events", "wisp_events",
}

func TestThreadGuardUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "tg")
	kit := newUOWRoleFixtureKit(provider, "tg")
	deleter, err := NewDeleter(provider)
	if err != nil {
		t.Fatalf("NewDeleter(): %v", err)
	}
	imager, err := NewImager(provider)
	if err != nil {
		t.Fatalf("NewImager(): %v", err)
	}
	runThreadGuardCases(t, ctx, threadGuardFixture{
		deleter: deleter,
		imager:  imager,
		create: func(ctx context.Context, issue *types.Issue) error {
			if issue.Ephemeral {
				return kit.CreateWisp(ctx, issue, "thread-guard")
			}
			return kit.CreateIssue(ctx, issue, "thread-guard")
		},
		addEdge: func(ctx context.Context, from, to string, typ types.DependencyType) error {
			return kit.AddDependency(ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: typ}, "thread-guard")
		},
		setDescription: func(ctx context.Context, id, text string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "edit " + id, uw.IssueUseCase().UpdateWisp(ctx, id, map[string]any{"description": text}, "thread-guard")
			})
		},
		scalar: kit.QueryScalar,
	})
}

// runThreadGuardCases is the whole suite. Every case seeds its own thread
// under its own tag, so they share one store.
func runThreadGuardCases(t *testing.T, ctx context.Context, f threadGuardFixture) {
	t.Run("MembershipFailsOnAReplyFromOutsideTheArchive", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "outside", false)
		outsider := seedThreadMail(t, ctx, f, "tg-outside-x", true, threadGuardMailLabel)
		threadGuardEdge(t, ctx, f, outsider, th.m1)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)

		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, false, th.m2), []publicops.ThreadGuardFailure{
			{Root: th.root, Check: publicops.ThreadGuardMembership, IDs: []string{outsider}},
		})
	})

	t.Run("RevisionFailsOnAChangedSurvivor", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "revision", false)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)
		if err := f.setDescription(ctx, th.root, "edited after the archive"); err != nil {
			t.Fatalf("edit the root: %v", err)
		}

		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, false, th.m2), []publicops.ThreadGuardFailure{
			{Root: th.root, Check: publicops.ThreadGuardRevision, IDs: []string{th.root}},
		})
	})

	t.Run("EligibilityFailsOnAnOpenMember", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "eligible", true)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)

		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, false, th.m2), []publicops.ThreadGuardFailure{
			{Root: th.root, Check: publicops.ThreadGuardEligibility, IDs: []string{th.m2}},
		})
	})

	t.Run("EligibilityFailsOnAMemberClosedAfterTheCutoff", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "cutoff", false)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)
		guard.Cutoff = time.Now().Add(-48 * time.Hour)

		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, false, th.m2), []publicops.ThreadGuardFailure{
			{Root: th.root, Check: publicops.ThreadGuardEligibility, IDs: []string{th.m1, th.m2, th.root}},
		})
	})

	t.Run("CoverageFailsOnAnUnguardedMemberLabelledBead", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "coverage", false)
		stray := seedThreadMail(t, ctx, f, "tg-coverage-y", true, threadGuardMailLabel)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)

		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, false, th.m2, stray), []publicops.ThreadGuardFailure{
			{Check: publicops.ThreadGuardCoverage, IDs: []string{stray}},
		})
	})

	t.Run("ReportsEveryFailedThreadAndCheck", func(t *testing.T) {
		first := seedGuardedThread(t, ctx, f, "every-a", true)
		second := seedGuardedThread(t, ctx, f, "every-b", false)
		guard := threadGuardFor(t, ctx, f, first.root, first.root, first.m1, first.m2)
		guard.Threads = append(guard.Threads, threadGuardFor(t, ctx, f, second.root, second.root, second.m1, second.m2).Threads...)
		if err := f.setDescription(ctx, second.m1, "edited after the archive"); err != nil {
			t.Fatalf("edit a member: %v", err)
		}
		stray := seedThreadMail(t, ctx, f, "tg-every-y", true, threadGuardMailLabel)

		assertThreadGuardRefuses(t, ctx, f, first, threadGuardDelete(guard, false, first.m2, second.m2, stray), []publicops.ThreadGuardFailure{
			{Root: first.root, Check: publicops.ThreadGuardEligibility, IDs: []string{first.m2}},
			{Root: second.root, Check: publicops.ThreadGuardRevision, IDs: []string{second.m1}},
			{Check: publicops.ThreadGuardCoverage, IDs: []string{stray}},
		})
	})

	t.Run("AnExcludedBeadJoinsNoThread", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "exclude", false)
		// kept replies to a member and carries the exclude label; beyond replies
		// only to kept, so the walk reaches it only by walking through kept.
		kept := seedThreadMail(t, ctx, f, "tg-exclude-kept", true, threadGuardMailLabel, threadGuardExcludeLabel)
		beyond := seedThreadMail(t, ctx, f, "tg-exclude-beyond", true, threadGuardMailLabel)
		threadGuardEdge(t, ctx, f, kept, th.m1)
		threadGuardEdge(t, ctx, f, beyond, kept)

		listed := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2, kept)
		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(listed, false, th.m2), []publicops.ThreadGuardFailure{
			{Root: th.root, Check: publicops.ThreadGuardMembership, IDs: []string{kept}},
		})

		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)
		result, err := f.deleter.Delete(ctx, threadGuardDelete(guard, false, th.m2))
		if err != nil {
			t.Fatalf("delete past an excluded reply: %v", err)
		}
		if result.Deleted != 1 {
			t.Errorf("Deleted = %d, want 1", result.Deleted)
		}
		assertThreadGuardRows(t, ctx, f, map[string]int{th.m2: 0, kept: 1, beyond: 1, th.m1: 1})
	})

	t.Run("APassDeletesExactlyTheIDs", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "pass", false)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)
		before := threadGuardCounts(t, ctx, f)

		result, err := f.deleter.Delete(ctx, threadGuardDelete(guard, false, th.m1, th.m2))
		if err != nil {
			t.Fatalf("guarded delete: %v", err)
		}
		if result.Deleted != 2 {
			t.Errorf("Deleted = %d, want 2", result.Deleted)
		}
		assertThreadGuardRows(t, ctx, f, map[string]int{th.m1: 0, th.m2: 0, th.root: 1, th.neighbor: 1})
		after := threadGuardCounts(t, ctx, f)
		if got := before["wisps"] - after["wisps"]; got != 2 {
			t.Errorf("wisps went %d -> %d, want exactly the two deleted", before["wisps"], after["wisps"])
		}
		if got := after["issues"]; got != before["issues"] {
			t.Errorf("issues went %d -> %d, want no durable row deleted", before["issues"], got)
		}
		if want, got := "see [deleted:"+th.m2+"] for context", threadGuardDescription(t, ctx, f, th.neighbor); got != want {
			t.Errorf("neighbor description = %q, want %q", got, want)
		}
	})

	t.Run("DryRunRefusesTheWayTheRealRunDoes", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "dryrun", false)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)
		if err := f.setDescription(ctx, th.m1, "edited after the archive"); err != nil {
			t.Fatalf("edit a member: %v", err)
		}
		want := []publicops.ThreadGuardFailure{
			{Root: th.root, Check: publicops.ThreadGuardRevision, IDs: []string{th.m1}},
		}
		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, true, th.m2), want)
		assertThreadGuardRefuses(t, ctx, f, th, threadGuardDelete(guard, false, th.m2), want)
	})

	t.Run("ACascadeIsRefusedBeforeAnythingIsRead", func(t *testing.T) {
		th := seedGuardedThread(t, ctx, f, "cascade", false)
		guard := threadGuardFor(t, ctx, f, th.root, th.root, th.m1, th.m2)
		req := threadGuardDelete(guard, false, th.m2)
		req.Cascade = true
		before := threadGuardCounts(t, ctx, f)
		if _, err := f.deleter.Delete(ctx, req); !errors.Is(err, publicops.ErrValidation) {
			t.Fatalf("thread guard with cascade: err = %v, want ErrValidation", err)
		}
		if after := threadGuardCounts(t, ctx, f); !reflect.DeepEqual(after, before) {
			t.Errorf("row counts changed on a refused cascade: %v -> %v", before, after)
		}
	})
}

// guardedThread is one seeded mail thread — root <- m1 <- m2 over replies-to,
// all closed thread-mail wisps — and a durable neighbor whose description
// cites m2 over a related edge, which a successful delete of m2 rewrites.
type guardedThread struct {
	root, m1, m2, neighbor string
}

func seedGuardedThread(t *testing.T, ctx context.Context, f threadGuardFixture, tag string, openLeaf bool) guardedThread {
	t.Helper()
	th := guardedThread{
		root:     seedThreadMail(t, ctx, f, "tg-"+tag+"-root", true, threadGuardMailLabel),
		m1:       seedThreadMail(t, ctx, f, "tg-"+tag+"-m1", true, threadGuardMailLabel),
		m2:       seedThreadMail(t, ctx, f, "tg-"+tag+"-m2", !openLeaf, threadGuardMailLabel),
		neighbor: "tg-" + tag + "-neighbor",
	}
	threadGuardEdge(t, ctx, f, th.m1, th.root)
	threadGuardEdge(t, ctx, f, th.m2, th.m1)
	if err := f.create(ctx, &types.Issue{
		ID:          th.neighbor,
		Title:       th.neighbor,
		Description: "see " + th.m2 + " for context",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}); err != nil {
		t.Fatalf("seed neighbor %s: %v", th.neighbor, err)
	}
	if err := f.addEdge(ctx, th.neighbor, th.m2, types.DepRelated); err != nil {
		t.Fatalf("seed neighbor edge: %v", err)
	}
	return th
}

// seedThreadMail creates one wisp carrying labels, closed an hour ago unless
// closed is false.
func seedThreadMail(t *testing.T, ctx context.Context, f threadGuardFixture, id string, closed bool, labels ...string) string {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Labels:    labels,
	}
	if closed {
		at := time.Now().Add(-time.Hour).UTC()
		issue.Status = types.StatusClosed
		issue.ClosedAt = &at
	}
	if err := f.create(ctx, issue); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return id
}

func threadGuardEdge(t *testing.T, ctx context.Context, f threadGuardFixture, reply, to string) {
	t.Helper()
	if err := f.addEdge(ctx, reply, to, types.DepRepliesTo); err != nil {
		t.Fatalf("seed reply %s -> %s: %v", reply, to, err)
	}
}

// threadGuardFor archives members as one thread under root: it images them
// now and records each revision, the way a caller archives before deleting.
// The cutoff is an hour from now, after every seed's close.
func threadGuardFor(t *testing.T, ctx context.Context, f threadGuardFixture, root string, members ...string) *publicops.ThreadGuard {
	t.Helper()
	images, err := f.imager.Image(ctx, members)
	if err != nil {
		t.Fatalf("archive %v: %v", members, err)
	}
	thread := publicops.GuardedThread{Root: root}
	for _, img := range images {
		thread.Members = append(thread.Members, publicops.GuardedMember{ID: img.ID, Revision: img.Revision})
	}
	return &publicops.ThreadGuard{
		Cutoff:       time.Now().Add(time.Hour),
		MemberLabel:  threadGuardMailLabel,
		ExcludeLabel: threadGuardExcludeLabel,
		Edge:         string(types.DepRepliesTo),
		Threads:      []publicops.GuardedThread{thread},
	}
}

// threadGuardDelete is a forced delete of ids under guard: forced because each
// thread's neighbor depends on its m2, and the dependents guard is not what
// these cases are about.
func threadGuardDelete(guard *publicops.ThreadGuard, dryRun bool, ids ...string) publicops.DeleteRequest {
	return publicops.DeleteRequest{Actor: "thread-guard", IDs: ids, Force: true, DryRun: dryRun, ThreadGuard: guard}
}

// assertThreadGuardRefuses runs req and wants exactly the failures in want,
// with no row changed in any of the ten tables and th's neighbor not rewritten.
func assertThreadGuardRefuses(t *testing.T, ctx context.Context, f threadGuardFixture, th guardedThread, req publicops.DeleteRequest, want []publicops.ThreadGuardFailure) {
	t.Helper()
	before := threadGuardCounts(t, ctx, f)
	description := threadGuardDescription(t, ctx, f, th.neighbor)

	result, err := f.deleter.Delete(ctx, req)
	if !errors.Is(err, publicops.ErrThreadGuardMismatch) {
		t.Fatalf("Delete(%v) err = %v, want ErrThreadGuardMismatch", req.IDs, err)
	}
	var mismatch *publicops.ThreadGuardMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Delete(%v) err = %T, want *ThreadGuardMismatchError", req.IDs, err)
	}
	if !reflect.DeepEqual(mismatch.Failed, want) {
		t.Errorf("failures = %+v\nwant       %+v", mismatch.Failed, want)
	}
	if result.Deleted != 0 {
		t.Errorf("refused delete reported Deleted = %d", result.Deleted)
	}
	if after := threadGuardCounts(t, ctx, f); !reflect.DeepEqual(after, before) {
		t.Errorf("row counts changed on a refused delete:\n before %v\n after  %v", before, after)
	}
	if got := threadGuardDescription(t, ctx, f, th.neighbor); got != description {
		t.Errorf("neighbor description went %q -> %q on a refused delete", description, got)
	}
}

func threadGuardCounts(t *testing.T, ctx context.Context, f threadGuardFixture) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(threadGuardTables))
	for _, table := range threadGuardTables {
		var n int
		if err := f.scalar(ctx, "SELECT COUNT(*) FROM "+table, nil, &n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

func threadGuardDescription(t *testing.T, ctx context.Context, f threadGuardFixture, id string) string {
	t.Helper()
	var description string
	if err := f.scalar(ctx, "SELECT description FROM issues WHERE id = ?", []any{id}, &description); err != nil {
		t.Fatalf("read %s description: %v", id, err)
	}
	return description
}

// assertThreadGuardRows wants each id present in exactly the given number of
// rows across both planes.
func assertThreadGuardRows(t *testing.T, ctx context.Context, f threadGuardFixture, want map[string]int) {
	t.Helper()
	for id, n := range want {
		var got int
		if err := f.scalar(ctx,
			"SELECT (SELECT COUNT(*) FROM issues WHERE id = ?) + (SELECT COUNT(*) FROM wisps WHERE id = ?)",
			[]any{id, id}, &got); err != nil {
			t.Fatalf("count rows for %s: %v", id, err)
		}
		if got != n {
			t.Errorf("%s has %d rows, want %d", id, got, n)
		}
	}
}
