package dolt

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestRowVersion exercises the read-only RowVersion token — the issues/wisps
// row_lock cell surfaced on types.Issue. RowVersion is stable across reads with
// no intervening write, changes on every status/ownership-mutating write, is
// hydrated by the list path as well as the point read, and — the reason the
// token exists — distinguishes two writes that land in the same wall-clock
// second, where updated_at (DATETIME, second granularity) is identical.
func TestRowVersion(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	get := func(t *testing.T, id string) *types.Issue {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if iss == nil {
			t.Fatalf("GetIssue(%s) returned nil", id)
		}
		return iss
	}

	t.Run("create stamps a nonzero version and reads are stable", func(t *testing.T) {
		createPerm(t, ctx, store, "rv-stable")
		first := get(t, "rv-stable")
		// The issueops create path writes freshRowLock(), so a freshly created
		// issue already carries a nonzero version.
		if first.RowVersion == 0 {
			t.Fatalf("RowVersion after create = 0, want nonzero (create stamps row_lock)")
		}
		// Two reads with no write between must return the identical version.
		second := get(t, "rv-stable")
		if second.RowVersion != first.RowVersion {
			t.Fatalf("RowVersion changed across reads with no write: %d -> %d", first.RowVersion, second.RowVersion)
		}
	})

	t.Run("mutating write changes the version", func(t *testing.T) {
		createPerm(t, ctx, store, "rv-mutate")
		before := get(t, "rv-mutate").RowVersion
		if _, err := store.CloseIssueChecked(ctx, "rv-mutate", "tester",
			storage.CloseIssueOptions{Reason: "done"}); err != nil {
			t.Fatalf("CloseIssueChecked: %v", err)
		}
		after := get(t, "rv-mutate").RowVersion
		if after == before {
			t.Fatalf("RowVersion unchanged after a mutating write: still %d", before)
		}
	})

	t.Run("SearchIssues hydrates the version", func(t *testing.T) {
		createPerm(t, ctx, store, "rv-search")
		want := get(t, "rv-search").RowVersion

		results, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		var got int64
		found := false
		for _, iss := range results {
			if iss.ID == "rv-search" {
				got = iss.RowVersion
				found = true
			}
		}
		if !found {
			t.Fatalf("SearchIssues did not return rv-search")
		}
		if got == 0 {
			t.Fatalf("SearchIssues hydrated RowVersion = 0; the list path did not scan row_lock")
		}
		if got != want {
			t.Fatalf("SearchIssues RowVersion = %d, GetIssue RowVersion = %d; list and point reads disagree", got, want)
		}
	})

	// The B4 property: two mutating writes in the same wall-clock second yield
	// identical updated_at (DATETIME truncates to the second) but DIFFERENT
	// RowVersions. A caller holding only updated_at could not tell the two writes
	// apart; RowVersion can. This is the whole reason for the slice.
	t.Run("same-second writes get distinct versions", func(t *testing.T) {
		createPerm(t, ctx, store, "rv-b4")

		mutate := func(rev int) *types.Issue {
			t.Helper()
			updates := map[string]interface{}{"title": fmt.Sprintf("rv-b4 rev %d", rev)}
			if err := store.UpdateIssue(ctx, "rv-b4", updates, "tester"); err != nil {
				t.Fatalf("UpdateIssue rev %d: %v", rev, err)
			}
			return get(t, "rv-b4")
		}

		// Two back-to-back writes almost always land in one second; retry only to
		// absorb the rare wall-clock second boundary between the pair.
		const attempts = 8
		sameSecondSeen := false
		for i := 0; i < attempts && !sameSecondSeen; i++ {
			a := mutate(2 * i)
			b := mutate(2*i + 1)

			// RowVersion must always distinguish two distinct writes.
			if a.RowVersion == b.RowVersion {
				t.Fatalf("two mutating writes shared RowVersion %d", a.RowVersion)
			}

			if a.UpdatedAt.Unix() == b.UpdatedAt.Unix() {
				// updated_at is byte-identical at second granularity here, yet the
				// versions differ — exactly the indistinguishability the token fixes.
				if !a.UpdatedAt.Truncate(time.Second).Equal(b.UpdatedAt.Truncate(time.Second)) {
					t.Fatalf("same Unix second but truncated updated_at differ: %v vs %v", a.UpdatedAt, b.UpdatedAt)
				}
				t.Logf("same-second writes: updated_at=%v identical; RowVersion %d != %d",
					a.UpdatedAt.Truncate(time.Second), a.RowVersion, b.RowVersion)
				sameSecondSeen = true
			}
		}
		if !sameSecondSeen {
			t.Fatalf("no write pair landed in the same wall-clock second across %d attempts; cannot demonstrate the B4 property", attempts)
		}
	})
}
