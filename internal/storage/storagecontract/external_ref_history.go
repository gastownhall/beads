package storagecontract

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// ExternalRefHistoryFixture supplies the store hooks the contract needs, all acting on Store.
type ExternalRefHistoryFixture struct {
	// Store is the capability under test.
	Store storage.ExternalRefHistoryBatchQuerier
	// Branch is the branch Store reads.
	Branch string
	// CreateIssues seeds durable issues.
	CreateIssues func(ctx context.Context, ids []string) error
	// DeleteIssue hard-deletes one issue.
	DeleteIssue func(ctx context.Context, id string) error
	// CreateBranch branches name off Branch's HEAD.
	CreateBranch func(ctx context.Context, name string) error
	// CommitOn runs stmts on branch and commits them dated when.
	CommitOn func(ctx context.Context, branch string, when time.Time, stmts ...string) error
	// Merge merges branch into Branch.
	Merge func(ctx context.Context, branch string) error
}

type externalRefAnswer struct {
	ref   string
	found bool
}

// RunExternalRefHistoryBatchContract checks PreviousExternalRefs against PreviousExternalRef.
func RunExternalRefHistoryBatchContract(t *testing.T, ctx context.Context, fx ExternalRefHistoryFixture) {
	t.Helper()
	main, side, late := fx.Branch, fx.Branch+"-xr-side", fx.Branch+"-xr-late"
	base := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Second)
	at := func(h float64) time.Time { return base.Add(time.Duration(h * float64(time.Hour))) }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	commit := func(branch string, h float64, stmts ...string) {
		t.Helper()
		must("commit", fx.CommitOn(ctx, branch, at(h), stmts...))
	}

	must("create", fx.CreateIssues(ctx, []string{"xr-a", "xr-b", "xr-c", "xr-d", "xr-e"}))
	commit(main, 1, "UPDATE issues SET external_ref = CONCAT(id, '-1') WHERE id IN ('xr-a', 'xr-b', 'xr-c', 'xr-e')")
	must("delete xr-c", fx.DeleteIssue(ctx, "xr-c"))
	must("delete xr-e", fx.DeleteIssue(ctx, "xr-e"))
	commit(main, 2, "UPDATE issues SET external_ref = 'xr-a-2' WHERE id = 'xr-a'")
	must("branch", fx.CreateBranch(ctx, side))
	must("re-create xr-e", fx.CreateIssues(ctx, []string{"xr-e"}))
	commit(main, 3, "UPDATE issues SET external_ref = 'xr-e-2' WHERE id = 'xr-e'")
	commit(main, 4, "UPDATE issues SET external_ref = 'xr-b-2' WHERE id = 'xr-b'")
	commit(side, 5, "UPDATE issues SET external_ref = 'xr-a-3' WHERE id = 'xr-a'")
	must("merge", fx.Merge(ctx, side))
	must("create xr-f", fx.CreateIssues(ctx, []string{"xr-f"}))
	commit(main, 6, "UPDATE issues SET external_ref = 'xr-f-1' WHERE id = 'xr-f'")
	must("branch", fx.CreateBranch(ctx, late))
	commit(main, 7, "UPDATE issues SET external_ref = 'xr-d-1' WHERE id = 'xr-d'")
	commit(main, 7, "UPDATE issues SET external_ref = 'xr-d-2' WHERE id = 'xr-d'")
	commit(late, 7, "UPDATE issues SET external_ref = 'xr-b-late' WHERE id = 'xr-b'")
	must("merge", fx.Merge(ctx, late))
	commit(main, 8, "UPDATE issues SET external_ref = 'xr-a-4' WHERE id = 'xr-a'")

	ids := []string{"xr-a", "xr-b", "xr-c", "xr-d", "xr-e", "xr-f", "xr-never"}
	asOfs := []float64{0.5, 1, 1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7, 7.5, 8.5}
	want := map[float64]map[string]externalRefAnswer{}
	for _, h := range asOfs {
		want[h] = map[string]externalRefAnswer{}
		for _, id := range ids {
			ref, found, err := fx.Store.PreviousExternalRef(ctx, id, at(h))
			must("PreviousExternalRef", err)
			want[h][id] = externalRefAnswer{ref, found}
		}
	}
	for _, c := range []struct {
		h    float64
		id   string
		want externalRefAnswer
	}{
		{0.5, "xr-a", externalRefAnswer{}},
		{1, "xr-a", externalRefAnswer{"xr-a-1", true}},
		{2.5, "xr-c", externalRefAnswer{"xr-c-1", true}},
		{2.5, "xr-e", externalRefAnswer{"xr-e-1", true}},
		{3.5, "xr-e", externalRefAnswer{"xr-e-2", true}},
		{5.5, "xr-b", externalRefAnswer{"xr-b-1", true}},
		{5.5, "xr-f", externalRefAnswer{}},
		{6.5, "xr-f", externalRefAnswer{"xr-f-1", true}},
	} {
		if got := want[c.h][c.id]; got != c.want {
			t.Fatalf("fixture check: PreviousExternalRef(%s, %vh) = %+v, want %+v", c.id, c.h, got, c.want)
		}
	}

	for _, h := range asOfs {
		got, err := fx.Store.PreviousExternalRefs(ctx, ids, at(h))
		must("PreviousExternalRefs", err)
		for _, id := range ids {
			ref, found := got[id]
			if g := (externalRefAnswer{ref, found}); g != want[h][id] {
				t.Errorf("PreviousExternalRefs at %vh: %s = %+v, PreviousExternalRef = %+v", h, id, g, want[h][id])
			}
		}
		for id, ref := range got {
			if !want[h][id].found {
				t.Errorf("PreviousExternalRefs at %vh returned %s = %q, which PreviousExternalRef does not report found", h, id, ref)
			}
		}
	}
}

// RunExternalRefHistoryBatchBenchmark times PreviousExternalRefs against per-issue PreviousExternalRef calls.
func RunExternalRefHistoryBatchBenchmark(b *testing.B, ctx context.Context, fx ExternalRefHistoryFixture) {
	issueCount := benchEnvInt(b, "BEADS_BENCH_ISSUES", 200)
	commitCount := benchEnvInt(b, "BEADS_BENCH_COMMITS", 200)
	must := func(what string, err error) {
		b.Helper()
		if err != nil {
			b.Fatalf("%s: %v", what, err)
		}
	}
	benchIDs := func(prefix string, n int) []string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("%s-%05d", prefix, i)
		}
		return ids
	}

	linked := benchIDs("bench", issueCount)
	must("create", fx.CreateIssues(ctx, linked))
	base := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Second)
	must("commit", fx.CommitOn(ctx, fx.Branch, base, "UPDATE issues SET external_ref = CONCAT('https://example.invalid/', id)"))
	for i := range commitCount {
		id := linked[i%issueCount]
		must("commit", fx.CommitOn(ctx, fx.Branch, base.Add(time.Duration(i+1)*time.Minute),
			fmt.Sprintf("UPDATE issues SET external_ref = 'https://example.invalid/%s/%d' WHERE id = '%s'", id, i, id)))
	}
	asOf := base.Add(time.Duration(commitCount+60) * time.Minute)
	created := benchIDs("bench-new", max(issueCount/2, 1))
	must("create", fx.CreateIssues(ctx, created))
	must("commit", fx.CommitOn(ctx, fx.Branch, asOf.Add(time.Hour), "UPDATE issues SET external_ref = CONCAT('https://example.invalid/', id) WHERE id LIKE 'bench-new-%'"))
	withNew := slices.Concat(linked, created)

	refs, err := fx.Store.PreviousExternalRefs(ctx, withNew, asOf)
	must("PreviousExternalRefs", err)
	for _, id := range withNew {
		ref, found, err := fx.Store.PreviousExternalRef(ctx, id, asOf)
		must("PreviousExternalRef", err)
		if got, ok := refs[id]; ok != found || got != ref {
			b.Fatalf("PreviousExternalRefs[%s] = (%q, %v), PreviousExternalRef = (%q, %v)", id, got, ok, ref, found)
		}
	}

	for _, set := range []struct {
		name string
		ids  []string
	}{{"linked", linked}, {"with_new", withNew}} {
		b.Run(set.name+"/per_issue_query", func(b *testing.B) {
			for b.Loop() {
				for _, id := range set.ids {
					if _, _, err := fx.Store.PreviousExternalRef(ctx, id, asOf); err != nil {
						b.Fatalf("PreviousExternalRef(%s): %v", id, err)
					}
				}
			}
		})
		b.Run(set.name+"/batch", func(b *testing.B) {
			for b.Loop() {
				if _, err := fx.Store.PreviousExternalRefs(ctx, set.ids, asOf); err != nil {
					b.Fatalf("PreviousExternalRefs: %v", err)
				}
			}
		})
	}
}

func benchEnvInt(b *testing.B, name string, fallback int) int {
	b.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		b.Fatalf("%s=%q: want a positive integer", name, raw)
	}
	return n
}
