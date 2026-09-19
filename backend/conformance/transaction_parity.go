package conformance

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// This file holds the transaction cases whose whole point is that the two Dolt
// backends must answer them IDENTICALLY. Both are silent-wrong-answer shapes:
// nothing errors, nothing logs, and the caller is handed a plausible result
// that is wrong on one backend and right on the other. A caller cannot tell
// which backend it has, so a divergence here is a correctness bug in whichever
// leg deviates, not a documented difference.
//
// Each case takes its expectation from an ORACLE the two backends already
// agree on — the store-level form of the same operation — rather than from the
// other backend. Making two backends agree on the wrong answer would be worse
// than the divergence, because the disagreement is the only signal there is.

// testTransactionUpdateRecordsHistory pins Transaction.UpdateIssue to the same
// history the store-level UpdateIssue records (ga-1huib.7).
//
// The oracle is DoltStorage.UpdateIssue on the SAME store: both backends route
// it through issueops.UpdateIssueInTx, so an "updated" event for a real field
// change is not a backend opinion. Transaction.UpdateIssue is the same
// operation with a transaction around it, and storage.Transaction documents no
// carve-out for it — where event suppression IS intended on that interface it
// is spelled out (RemoveDependency vs RemoveDependencyWithOptions.EmitEvent).
//
// The untouched issue is the control: it proves the assertion is not satisfied
// trivially by every issue carrying an "updated" event, which would make a
// green run meaningless.
func testTransactionUpdateRecordsHistory(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "txh-store", Title: "Store edit"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "txh-tx", Title: "Tx edit"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "txh-untouched", Title: "Never edited"}), "a"))

	// Oracle: the same field change, outside a transaction.
	must(t, s.UpdateIssue(c, "txh-store", map[string]interface{}{"title": "Store edit, revised"}, "a"))

	// Subject: the same field change, inside a transaction.
	must(t, s.RunInTransaction(c, "bd: update txh-tx", func(tx storage.Transaction) error {
		return tx.UpdateIssue(c, "txh-tx", map[string]interface{}{"title": "Tx edit, revised"}, "a")
	}))

	oracle := countIssueEvents(t, s, "txh-store", types.EventUpdated)
	if oracle != 1 {
		t.Fatalf("oracle broken: store-level UpdateIssue recorded %d %q events, want 1", oracle, types.EventUpdated)
	}
	if control := countIssueEvents(t, s, "txh-untouched", types.EventUpdated); control != 0 {
		t.Fatalf("control broken: an issue that was never updated has %d %q events, want 0 — "+
			"the oracle assertion below cannot distinguish anything", control, types.EventUpdated)
	}

	if got := countIssueEvents(t, s, "txh-tx", types.EventUpdated); got != oracle {
		t.Errorf("Transaction.UpdateIssue recorded %d %q events, want %d (what the store-level "+
			"UpdateIssue records for the same change) — a transactional update must not have a "+
			"different audit trail from a plain one", got, types.EventUpdated, oracle)
	}
}

// testTransactionSearchIncludeDependencies pins Transaction.SearchIssues to
// honoring IssueFilter.IncludeDependencies (ga-1huib.8).
//
// A silently dropped filter field is worse than a missing one: a missing field
// is a compile error, a dropped one returns plausible wrong data. Two controls
// keep the positive assertion honest — an issue with NO dependencies must come
// back in the results with an empty slice (so the fixture cannot pass by having
// dependencies everywhere), and the same search with the flag OFF must hydrate
// nothing (so the case cannot pass on a backend that hydrates unconditionally
// and never reads the flag at all).
func testTransactionSearchIncludeDependencies(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "txdep-blocker", Title: "TxDepHydration Blocker"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "txdep-blocked", Title: "TxDepHydration Blocked"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "txdep-lone", Title: "TxDepHydration Lone"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{
		IssueID:     "txdep-blocked",
		DependsOnID: "txdep-blocker",
		Type:        types.DepBlocks,
	}, "a"))

	var on, off map[string][]*types.Dependency
	must(t, s.RunInTransaction(c, "bd: search txdep", func(tx storage.Transaction) error {
		hydrated, err := tx.SearchIssues(c, "TxDepHydration", types.IssueFilter{IncludeDependencies: true})
		if err != nil {
			return err
		}
		on = dependenciesByIssue(hydrated)

		plain, err := tx.SearchIssues(c, "TxDepHydration", types.IssueFilter{})
		if err != nil {
			return err
		}
		off = dependenciesByIssue(plain)
		return nil
	}))

	if len(on) != 3 {
		t.Fatalf("in-tx SearchIssues(IncludeDependencies) returned %d issues %v, want 3", len(on), issueIDsOf(on))
	}

	deps, ok := on["txdep-blocked"]
	if !ok {
		t.Fatalf("in-tx SearchIssues(IncludeDependencies) dropped txdep-blocked entirely")
	}
	if len(deps) != 1 {
		t.Errorf("txdep-blocked came back with %d dependencies, want 1 — IncludeDependencies was "+
			"accepted and ignored", len(deps))
	} else if deps[0].DependsOnID != "txdep-blocker" {
		t.Errorf("txdep-blocked depends on %q, want %q", deps[0].DependsOnID, "txdep-blocker")
	}

	// Control: a dependency-free issue is still returned, with nothing hydrated.
	lone, ok := on["txdep-lone"]
	if !ok {
		t.Errorf("control broken: txdep-lone (no dependencies) was dropped from the results")
	} else if len(lone) != 0 {
		t.Errorf("control broken: txdep-lone has no dependencies but %d were hydrated — the "+
			"fixture cannot tell a hydrated result from an unhydrated one", len(lone))
	}

	// Control: the flag is read, not ignored in the other direction.
	for _, id := range issueIDsOf(off) {
		if len(off[id]) != 0 {
			t.Errorf("control broken: %s hydrated %d dependencies without IncludeDependencies",
				id, len(off[id]))
		}
	}
}

// testTransactionSearchKeysetWalk pins Transaction.SearchIssues to honoring the
// IssueFilter.AfterCreatedAt/AfterID keyset position (ga-v1nuj).
//
// This is the LIVENESS case of the family. Every other dropped filter field
// costs the caller a wrong row set; a dropped keyset position costs it the loop:
// each page re-answers the first one, so a caller paging to exhaustion never
// advances. Depending on how the caller bounds itself that is an infinite loop
// or a silently truncated read, and neither reports an error.
//
// The oracle is RunSearchPaging's store-level walk over the same fixture — the
// same verb, on the same store, with the transaction taken away. Both backends
// already answer it, so the sequence below is not a backend opinion.
//
// Two controls keep the walk assertion honest:
//
//   - The bounded page loop plus the seen-set is itself the detector: a backend
//     that drops the cursor re-answers page 0 forever, which trips "repeated"
//     on the second page rather than hanging the suite. Reintroducing the drop
//     must turn this case red, or it is not testing what it exists for.
//   - A result set that fits in one page must still work, and the page after it
//     must be empty. Without that leg a backend could pass by answering nothing
//     at all after the first page — dropping rows rather than repeating them —
//     and the walk's no-repeat assertion alone would not notice.
//
// NOTHING IN THIS CASE FAILS THE TEST FROM INSIDE A TRANSACTION CALLBACK.
// t.Fatalf unwinds with runtime.Goexit, which abandons the open transaction
// instead of returning through RunInTransaction's commit/rollback — the suite
// then blocks on the stranded transaction until the whole binary's timeout
// fires, and a ten-minute goroutine dump replaces the assertion that was
// supposed to be the output. Every callback here only collects; the assertions
// run after it has returned.
func testTransactionSearchKeysetWalk(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	seedKeysetOverflow(t, s)

	// Oracle: the store-level one-shot read fixes the sequence the paged walk
	// has to reproduce. A backend that pages correctly but orders differently
	// fails here rather than on the walk, and the two read differently.
	full, err := s.SearchIssues(c, "", keysetOverflowFilter())
	must(t, err)
	if got := orderedIDs(full); !reflect.DeepEqual(got, keysetOverflowOrder) {
		t.Fatalf("oracle broken: store-level one-shot read = %v, want %v (created_at DESC, id ASC)", got, keysetOverflowOrder)
	}

	const pageSize = 2
	var walked []string
	var repeated string
	repeatedPage := -1
	must(t, s.RunInTransaction(c, "bd: keyset walk", func(tx storage.Transaction) error {
		seen := make(map[string]bool, len(keysetOverflowOrder))
		var afterCreatedAt *time.Time
		afterID := ""
		// Bounded on purpose: one more iteration than there are rows. A
		// backend that ignores the position would otherwise spin forever, and
		// a hung suite reports nothing. The bound converts that into the
		// "repeated" report below.
		for page := 0; page <= len(keysetOverflowOrder); page++ {
			filter := keysetOverflowFilter()
			filter.Limit = pageSize
			filter.AfterCreatedAt = afterCreatedAt
			filter.AfterID = afterID
			rows, rowsErr := tx.SearchIssues(c, "", filter)
			if rowsErr != nil {
				return fmt.Errorf("page %d: %w", page, rowsErr)
			}
			if len(rows) == 0 {
				return nil
			}
			if len(rows) > pageSize {
				return fmt.Errorf("page %d answered %d rows over a Limit of %d", page, len(rows), pageSize)
			}
			for _, issue := range rows {
				if seen[issue.ID] {
					repeated, repeatedPage = issue.ID, page
					return nil
				}
				seen[issue.ID] = true
				walked = append(walked, issue.ID)
			}
			last := rows[len(rows)-1]
			at := last.CreatedAt.UTC()
			afterCreatedAt = &at
			afterID = last.ID
		}
		return nil
	}))

	if repeated != "" {
		t.Fatalf("in-tx page %d repeated %q after walking %v: the keyset position was accepted and "+
			"ignored, so the walk never advanced past its first page", repeatedPage, repeated, walked)
	}
	if !reflect.DeepEqual(walked, keysetOverflowOrder) {
		t.Fatalf("in-tx keyset walk = %v, want the one-shot sequence %v with nothing dropped and nothing repeated",
			walked, keysetOverflowOrder)
	}

	// Control: a result set that fits in one page is not a special case. The
	// whole set comes back at once, and the page positioned after its last row
	// is empty — which is what terminates the walk above.
	var single, tail []string
	must(t, s.RunInTransaction(c, "bd: keyset single page", func(tx storage.Transaction) error {
		filter := keysetOverflowFilter()
		filter.Limit = len(keysetOverflowOrder)
		rows, rowsErr := tx.SearchIssues(c, "", filter)
		if rowsErr != nil {
			return fmt.Errorf("single page: %w", rowsErr)
		}
		single = orderedIDs(rows)
		if len(rows) == 0 {
			return nil
		}
		last := rows[len(rows)-1]
		at := last.CreatedAt.UTC()
		filter.AfterCreatedAt = &at
		filter.AfterID = last.ID
		past, pastErr := tx.SearchIssues(c, "", filter)
		if pastErr != nil {
			return fmt.Errorf("page past the end: %w", pastErr)
		}
		tail = orderedIDs(past)
		return nil
	}))
	if !reflect.DeepEqual(single, keysetOverflowOrder) {
		t.Fatalf("control broken: a single page sized to the whole set = %v, want %v", single, keysetOverflowOrder)
	}
	if len(tail) != 0 {
		t.Fatalf("control broken: the page positioned after the last row answered %v, want nothing — "+
			"a walk positioned past the end must terminate", tail)
	}
}

// testTransactionSearchFilterParity pins the rest of the filter surface
// Transaction.SearchIssues accepted and dropped (ga-v1nuj).
//
// Same shape as the keyset case and the same oracle rule: each leg's expected
// row set is what the STORE-LEVEL SearchIssues answers for the identical filter
// on the identical store, never what the other backend answers. Every filter
// carries SkipWisps so the oracle queries the issues table alone — the
// transaction's single-table search is not a wisp-merge, and letting that
// unrelated divergence into these legs would make them assert two things at
// once.
//
// The load-bearing control is per leg: the filter must exclude at least one row
// the unfiltered read returns. A predicate that happens to match everything
// makes "ignored" and "honored" the same answer, and the leg would pass on a
// backend that never read the field.
func testTransactionSearchFilterParity(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	seedFilterParityFixture(t, s)

	started := parityStarted(1)
	blocked := true
	for _, leg := range []struct {
		name   string
		filter types.IssueFilter
	}{
		{"Statuses", types.IssueFilter{Statuses: []types.Status{types.StatusOpen, types.StatusInProgress}}},
		{"ExcludeLabels", types.IssueFilter{ExcludeLabels: []string{"area-api"}}},
		{"LabelPattern", types.IssueFilter{LabelPattern: "area-*"}},
		{"LabelRegex", types.IssueFilter{LabelRegex: "^area-api$"}},
		{"IsBlocked", types.IssueFilter{IsBlocked: &blocked}},
		{"StartedAfter", types.IssueFilter{StartedAfter: &started}},
		{"StartedBefore", types.IssueFilter{StartedBefore: &started}},
	} {
		t.Run(leg.name, func(t *testing.T) {
			filter := leg.filter
			filter.IDPrefix = parityPrefix
			filter.SkipWisps = true
			filter.Limit = 100

			oracle, err := s.SearchIssues(c, "", filter)
			must(t, err)
			want := issueIDs(oracle)

			// Control: the predicate has to exclude something, or the leg
			// cannot tell an honored field from an ignored one.
			if len(want) == 0 {
				t.Fatalf("oracle broken: %s matched nothing on the fixture", leg.name)
			}
			if len(want) == parityFixtureSize {
				t.Fatalf("control broken: %s matched all %d fixture rows, so ignoring it and honoring it "+
					"give the same answer", leg.name, parityFixtureSize)
			}

			var got []string
			must(t, s.RunInTransaction(c, "bd: parity "+leg.name, func(tx storage.Transaction) error {
				rows, rowsErr := tx.SearchIssues(c, "", filter)
				if rowsErr != nil {
					return rowsErr
				}
				got = issueIDs(rows)
				return nil
			}))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("in-tx SearchIssues(%s) = %v, want %v (what the store-level search answers for the "+
					"same filter) — the field was accepted and ignored", leg.name, got, want)
			}
		})
	}

	t.Run("SortBy", func(t *testing.T) {
		for _, leg := range []struct {
			sortBy   string
			sortDesc bool
			limit    int
		}{
			{"created", false, 100},
			{"created", true, 100},
			{"title", false, 100},
			// "id" is the only Go-side sort key (sqlbuild.IsGoSideSort): it
			// renders no ORDER BY and is sorted — and, when bounded, trimmed —
			// in Go, so it exercises a branch none of the SQL-side keys above
			// reach. The bounded leg drives the Go-side eff-trim (len(ids) >
			// eff), the one place this path withholds the SQL LIMIT and cuts
			// the page in Go.
			{"id", false, 100},
			{"id", true, 100},
			{"id", false, 2},
		} {
			filter := types.IssueFilter{IDPrefix: parityPrefix, SkipWisps: true, Limit: leg.limit, SortBy: leg.sortBy, SortDesc: leg.sortDesc}
			oracle, err := s.SearchIssues(c, "", filter)
			must(t, err)
			want := orderedIDs(oracle)

			// Control: a bound below the fixture size must actually cut the
			// oracle's page, or the parity check below cannot tell a Go-side
			// trim from a path that ignored the bound.
			if leg.limit < parityFixtureSize && len(want) != leg.limit {
				t.Fatalf("control broken: SortBy=%q oracle under Limit=%d answered %d rows %v, want %d",
					leg.sortBy, leg.limit, len(want), want, leg.limit)
			}

			var got []string
			must(t, s.RunInTransaction(c, "bd: parity sort", func(tx storage.Transaction) error {
				rows, rowsErr := tx.SearchIssues(c, "", filter)
				if rowsErr != nil {
					return rowsErr
				}
				got = orderedIDs(rows)
				return nil
			}))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("in-tx SearchIssues(SortBy=%q, SortDesc=%v, Limit=%d) = %v, want %v — the sort was accepted and ignored",
					leg.sortBy, leg.sortDesc, leg.limit, got, want)
			}
			// Control: the two directions must not be the same sequence, or
			// the comparison above proves nothing about SortDesc.
			if leg.sortDesc {
				ascFilter := filter
				ascFilter.SortDesc = false
				asc, ascErr := s.SearchIssues(c, "", ascFilter)
				must(t, ascErr)
				if reflect.DeepEqual(orderedIDs(asc), want) {
					t.Fatalf("control broken: %s ASC and DESC answer the same sequence %v", leg.sortBy, want)
				}
			}
		}
	})

	t.Run("MaxRows", func(t *testing.T) {
		filter := types.IssueFilter{IDPrefix: parityPrefix, SkipWisps: true, MaxRows: 2, MaxRowsSource: "--max-rows"}

		// Oracle: the store-level search refuses this cap with the typed error
		// cmd/bd classifies into exit code 2.
		_, oracleErr := s.SearchIssues(c, "", filter)
		var oracleTooMany *storageops.ErrTooManyRows
		if !errors.As(oracleErr, &oracleTooMany) {
			t.Fatalf("oracle broken: store-level MaxRows=2 over %d rows failed with %T (%v), want *issueops.ErrTooManyRows",
				parityFixtureSize, oracleErr, oracleErr)
		}

		var txErr error
		var rows []*types.Issue
		must(t, s.RunInTransaction(c, "bd: parity maxrows", func(tx storage.Transaction) error {
			rows, txErr = tx.SearchIssues(c, "", filter)
			return nil
		}))
		var txTooMany *storageops.ErrTooManyRows
		if !errors.As(txErr, &txTooMany) {
			t.Fatalf("in-tx SearchIssues(MaxRows=2) returned %d rows %v and %v; want *issueops.ErrTooManyRows — "+
				"the defensive row cap was accepted and ignored", len(rows), orderedIDs(rows), txErr)
		}
		if txTooMany.Cap != oracleTooMany.Cap {
			t.Errorf("in-tx cap error reports Cap = %d, want %d", txTooMany.Cap, oracleTooMany.Cap)
		}
		if txTooMany.Source != oracleTooMany.Source {
			t.Errorf("in-tx cap error reports Source = %q, want the caller's %q", txTooMany.Source, oracleTooMany.Source)
		}

		// Control: the same search under a cap it cannot exceed is an ordinary
		// result, so the leg above is detecting the cap and not a broken search.
		under := filter
		under.MaxRows = parityFixtureSize
		var underRows []string
		must(t, s.RunInTransaction(c, "bd: parity maxrows under", func(tx storage.Transaction) error {
			ok, okErr := tx.SearchIssues(c, "", under)
			if okErr != nil {
				return fmt.Errorf("MaxRows=%d over %d rows: %w", parityFixtureSize, parityFixtureSize, okErr)
			}
			underRows = issueIDs(ok)
			return nil
		}))
		if len(underRows) != parityFixtureSize {
			t.Fatalf("control broken: MaxRows=%d answered %v, want the whole %d-row fixture",
				parityFixtureSize, underRows, parityFixtureSize)
		}
	})

	t.Run("LabelHydration", func(t *testing.T) {
		filter := types.IssueFilter{IDPrefix: parityPrefix, SkipWisps: true, Limit: 100}

		oracle, err := s.SearchIssues(c, "", filter)
		must(t, err)
		want := labelsByIssue(oracle)
		if len(want[parityLabeled]) == 0 {
			t.Fatalf("oracle broken: store-level search hydrated no labels for %s", parityLabeled)
		}
		if len(want[parityUnlabeled]) != 0 {
			t.Fatalf("control broken: %s carries labels, so a backend hydrating nothing would still match it",
				parityUnlabeled)
		}

		var got map[string][]string
		must(t, s.RunInTransaction(c, "bd: parity labels", func(tx storage.Transaction) error {
			rows, rowsErr := tx.SearchIssues(c, "", filter)
			if rowsErr != nil {
				return rowsErr
			}
			got = labelsByIssue(rows)
			return nil
		}))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("in-tx SearchIssues hydrated labels %v, want %v (what the store-level search hydrates) — "+
				"a caller reading Issue.Labels from a transaction got an empty slice for a labeled issue",
				got, want)
		}

		// Control: SkipLabels is read, not ignored in the other direction.
		skipped := filter
		skipped.SkipLabels = true
		var skippedLabels map[string][]string
		must(t, s.RunInTransaction(c, "bd: parity labels skipped", func(tx storage.Transaction) error {
			rows, rowsErr := tx.SearchIssues(c, "", skipped)
			if rowsErr != nil {
				return rowsErr
			}
			skippedLabels = labelsByIssue(rows)
			return nil
		}))
		for _, id := range sortedLabelKeys(skippedLabels) {
			if len(skippedLabels[id]) != 0 {
				t.Errorf("control broken: %s hydrated %v under SkipLabels", id, skippedLabels[id])
			}
		}
	})
}

// --- filter-parity fixture ---

const (
	parityPrefix      = "txf-"
	parityFixtureSize = 5
	parityLabeled     = "txf-api"
	parityUnlabeled   = "txf-plain"
)

// parityStarted is the fixture clock for started_at. DATETIME stores whole
// seconds, so every value is one.
func parityStarted(offset int) time.Time {
	return time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC).Add(time.Duration(offset) * time.Minute)
}

// seedFilterParityFixture builds one fixture that makes every leg of
// testTransactionSearchFilterParity discriminating: two labels on distinct
// issues, three distinct statuses, one issue blocked by an open dependency, and
// started_at values on either side of the cursor the started legs use.
func seedFilterParityFixture(t *testing.T, s storage.DoltStorage) {
	t.Helper()
	c := ctx()
	early, late := parityStarted(0), parityStarted(2)

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{
		ID: parityLabeled, Title: "Aardvark", Priority: 1, Status: types.StatusOpen,
		CreatedAt: pagingSecond(4), UpdatedAt: pagingSecond(4), StartedAt: &early,
	}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{
		ID: parityUnlabeled, Title: "Buffalo", Priority: 2, Status: types.StatusInProgress,
		CreatedAt: pagingSecond(3), UpdatedAt: pagingSecond(3), StartedAt: &late,
	}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{
		ID: "txf-ui", Title: "Cheetah", Priority: 0, Status: types.StatusClosed,
		CreatedAt: pagingSecond(2), UpdatedAt: pagingSecond(2),
	}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{
		ID: "txf-blocked", Title: "Dingo", Priority: 3, Status: types.StatusOpen,
		CreatedAt: pagingSecond(1), UpdatedAt: pagingSecond(1),
	}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{
		ID: "txf-blocker", Title: "Emu", Priority: 1, Status: types.StatusOpen,
		CreatedAt: pagingSecond(0), UpdatedAt: pagingSecond(0),
	}), "actor"))

	must(t, s.AddLabel(c, parityLabeled, "area-api", "actor"))
	must(t, s.AddLabel(c, "txf-ui", "area-ui", "actor"))
	must(t, s.AddDependency(c, &types.Dependency{
		IssueID:     "txf-blocked",
		DependsOnID: "txf-blocker",
		Type:        types.DepBlocks,
	}, "actor"))
}

// labelsByIssue keys hydrated labels by issue ID, normalizing the empty slice
// and nil to the same value so a comparison asserts on content rather than on
// which zero value a backend happened to leave behind.
func labelsByIssue(issues []*types.Issue) map[string][]string {
	byID := make(map[string][]string, len(issues))
	for _, issue := range issues {
		labels := append([]string(nil), issue.Labels...)
		sort.Strings(labels)
		if labels == nil {
			labels = []string{}
		}
		byID[issue.ID] = labels
	}
	return byID
}

// sortedKeys returns a map's keys in a stable order so failure messages name
// the same issues in the same order on every run.
func sortedLabelKeys(byID map[string][]string) []string {
	keys := make([]string, 0, len(byID))
	for k := range byID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// countIssueEvents counts an issue's history events of one type.
func countIssueEvents(t *testing.T, s storage.DoltStorage, id string, want types.EventType) int {
	t.Helper()
	events, err := s.GetEvents(ctx(), id, 0)
	if err != nil {
		t.Fatalf("GetEvents(%s): %v", id, err)
	}
	n := 0
	for _, e := range events {
		if e.EventType == want {
			n++
		}
	}
	return n
}

// dependenciesByIssue keys search results by issue ID so a case can assert on
// what was hydrated for each one, including the issues that got nothing.
func dependenciesByIssue(issues []*types.Issue) map[string][]*types.Dependency {
	byID := make(map[string][]*types.Dependency, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue.Dependencies
	}
	return byID
}

// issueIDsOf returns the map's issue IDs in a stable order so failure messages
// name the same issues in the same order on every run.
func issueIDsOf(byID map[string][]*types.Dependency) []string {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
