//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestSearchIssuesKeysetEmbedded is the same-second-overflow keyset regression
// on the embedded Dolt backend: a same-second group larger than a page pages completely under the
// (created_at DESC, id ASC) keyset with no drop/dup, routed through the public
// SearchIssues.
func TestSearchIssuesKeysetEmbedded(t *testing.T) {
	te := newTestEnv(t, "ks")
	ctx := t.Context()

	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	seeds := []struct {
		id string
		at time.Time
	}{
		{"k-newer", base.Add(time.Second)},
		{"k-a1", base}, {"k-a2", base}, {"k-a3", base}, {"k-a4", base}, {"k-a5", base},
		{"k-older", base.Add(-time.Second)},
	}
	for _, s := range seeds {
		iss := &types.Issue{ID: s.id, Title: s.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: s.at}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	want := []string{"k-newer", "k-a1", "k-a2", "k-a3", "k-a4", "k-a5", "k-older"}
	ids := func(issues []*types.Issue) []string {
		out := make([]string, len(issues))
		for i, iss := range issues {
			out[i] = iss.ID
		}
		return out
	}
	eq := func(got, exp []string) bool {
		if len(got) != len(exp) {
			return false
		}
		for i := range exp {
			if got[i] != exp[i] {
				return false
			}
		}
		return true
	}

	full, err := te.store.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: 100})
	if err != nil {
		t.Fatalf("SearchIssues(full): %v", err)
	}
	if got := ids(full); !eq(got, want) {
		t.Fatalf("full order = %v, want %v", got, want)
	}

	const pageSize = 2
	var collected []string
	seen := map[string]bool{}
	var afterCreatedAt *time.Time
	afterID := ""
	for i := 0; i < 100; i++ {
		page, err := te.store.SearchIssues(ctx, "", types.IssueFilter{
			IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: pageSize,
			AfterCreatedAt: afterCreatedAt, AfterID: afterID,
		})
		if err != nil {
			t.Fatalf("SearchIssues(page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		for _, iss := range page {
			if seen[iss.ID] {
				t.Fatalf("duplicate id %q across pages — same-second overflow lost", iss.ID)
			}
			seen[iss.ID] = true
			collected = append(collected, iss.ID)
		}
		last := page[len(page)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !eq(collected, want) {
		t.Fatalf("keyset paged order = %v, want %v (no drop/dup)", collected, want)
	}
}
