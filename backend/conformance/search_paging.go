package conformance

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// This file holds the paging half of the SearchIssues contract: the keyset
// position (IssueFilter.AfterCreatedAt/AfterID) and the defensive row cap
// (IssueFilter.MaxRows/MaxRowsSource).
//
// WHAT THIS CONTRACT PINS. Both subjects are documented promises on the filter
// itself (types.go:1791-1806 for the position, types.go:1889-1897 for the cap)
// that a backend hand-writing its own search query has to reproduce, and both
// were pinned only by BACKEND-LOCAL tests before this file existed — the keyset
// walk by a duplicated dolt/embeddeddolt pair, the cap by a second duplicated
// dolt/embeddeddolt pair. A duplicated pair is the signal that the assertion
// belongs to the shared suite: the second copy exists because the contract had
// nowhere else to live.
//
// The reader role owns adjacent but genuinely weaker cases, which is why these
// are not duplicates of it. RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder
// makes ONE boundary read at the role level; it exercises neither an overflow
// walk, nor composition with other predicates, nor the AfterID:"" group start.
// RunReaderListMaxRowsIsHonored drives the cap through the ROLE's request
// vocabulary, where Limit defaulting and the probe row sit between the caller
// and the filter; this block drives types.IssueFilter.MaxRows directly and
// walks the boundary. It used to be the only strict one — the role case
// accepted a refusal in place of the cap until both implementations threaded
// it.

// RunSearchPaging runs the keyset-paging and row-cap block against a factory
// store.
//
// ADMISSION RULE. Every case here binds the SearchIssues filter surface — the
// keyset position and MaxRows. A backend whose declared unsupported allowlist
// (RunUnsupportedContract plus its structural completeness gate) refuses that
// subject does not run this block: it composes a curated supported-subset entry
// point from the per-block runners whose subjects it does supply, on the
// RunDeferredReads precedent, and answers RunUnsupportedContract for the rest.
// A backend adopting this block retires the backend-local copies it supersedes
// in the SAME change, so no behavior ever has zero owning proofs.
//
// RunAll composes this entry point, so every in-tree backend driving the full
// suite runs all four cases with no new wiring.
func RunSearchPaging(t *testing.T, f Factory) {
	t.Helper()
	t.Run("KeysetWalkLosesNothingAndRepeatsNothing", func(t *testing.T) { testSearchPagingKeysetWalk(t, f) })
	t.Run("KeysetResumeAnswersTheStrictlyAfterRows", func(t *testing.T) { testSearchPagingKeysetResume(t, f) })
	t.Run("KeysetPositionComposesWithTheFilter", func(t *testing.T) { testSearchPagingKeysetComposes(t, f) })
	t.Run("MaxRowsAnswersTheTypedRefusal", func(t *testing.T) { testSearchPagingMaxRows(t, f) })
}

// --- fixtures ---

// pagingSecond is the fixture clock. created_at is a DATETIME column, so every
// seeded timestamp is a whole second: a keyset cursor may not depend on
// sub-second resolution the column cannot store.
func pagingSecond(offset int) time.Time {
	return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(offset) * time.Second)
}

func seedPagingIssue(t *testing.T, s storage.DoltStorage, id string, at time.Time, status types.Status) {
	t.Helper()
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{
		ID:        id,
		Title:     id,
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    status,
		CreatedAt: at,
		UpdatedAt: at,
	}), "actor"))
}

// keysetOverflowOrder is the total (created_at DESC, id ASC) sequence the
// overflow fixture answers: one newer row, a five-row group sharing one second,
// one older row. The group is larger than the page size the walk uses, which is
// the whole point — a created_at-only cursor loses exactly this shape.
var keysetOverflowOrder = []string{"ks-newer", "ks-a1", "ks-a2", "ks-a3", "ks-a4", "ks-a5", "ks-older"}

func seedKeysetOverflow(t *testing.T, s storage.DoltStorage) {
	t.Helper()
	group := pagingSecond(0)
	seedPagingIssue(t, s, "ks-newer", group.Add(time.Second), types.StatusOpen)
	for _, id := range []string{"ks-a1", "ks-a2", "ks-a3", "ks-a4", "ks-a5"} {
		seedPagingIssue(t, s, id, group, types.StatusOpen)
	}
	seedPagingIssue(t, s, "ks-older", group.Add(-time.Second), types.StatusOpen)
}

func keysetOverflowFilter() types.IssueFilter {
	return types.IssueFilter{IDPrefix: "ks-", SkipWisps: true, SortBy: "created", Limit: 100}
}

// --- cases ---

// testSearchPagingKeysetWalk (B1): walking a result set page by page with the
// keyset position never drops or repeats a row, even when a group sharing one
// created_at second is larger than the page.
//
// The one-shot read pins the sequence the walk has to reproduce, so a backend
// that agrees with itself but orders differently from the reference fails on the
// sequence rather than on the walk, and the two failures read differently.
func testSearchPagingKeysetWalk(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	seedKeysetOverflow(t, s)

	full, err := s.SearchIssues(c, "", keysetOverflowFilter())
	must(t, err)
	if got := orderedIDs(full); !reflect.DeepEqual(got, keysetOverflowOrder) {
		t.Fatalf("one-shot ordered read = %v, want %v (created_at DESC, id ASC)", got, keysetOverflowOrder)
	}

	const pageSize = 2
	var walked []string
	seen := make(map[string]bool, len(keysetOverflowOrder))
	var afterCreatedAt *time.Time
	afterID := ""
	for page := 0; page <= len(keysetOverflowOrder); page++ {
		filter := keysetOverflowFilter()
		filter.Limit = pageSize
		filter.AfterCreatedAt = afterCreatedAt
		filter.AfterID = afterID
		rows, err := s.SearchIssues(c, "", filter)
		if err != nil {
			t.Fatalf("SearchIssues(page %d): %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		if len(rows) > pageSize {
			t.Fatalf("page %d answered %d rows over a Limit of %d", page, len(rows), pageSize)
		}
		for _, issue := range rows {
			if seen[issue.ID] {
				t.Fatalf("page %d repeated %q: the same-second group overflowed the page and the position re-delivered it", page, issue.ID)
			}
			seen[issue.ID] = true
			walked = append(walked, issue.ID)
		}
		last := rows[len(rows)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}

	if !reflect.DeepEqual(walked, keysetOverflowOrder) {
		t.Fatalf("keyset walk = %v, want the one-shot sequence %v with nothing dropped and nothing repeated", walked, keysetOverflowOrder)
	}
}

// testSearchPagingKeysetResume (B2): resuming from (AfterCreatedAt, AfterID)
// answers exactly the rows strictly after that position under (created_at DESC,
// id ASC) — same-second rows with a later id first, then strictly older rows,
// and never the cursor row or anything newer.
//
// The second leg is the documented AfterID:"" form (types.go:1799-1800): an
// empty id starts the same-second group from its first id, so the whole group
// comes back rather than none of it.
func testSearchPagingKeysetResume(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	seedKeysetOverflow(t, s)

	midGroup := pagingSecond(0)
	filter := keysetOverflowFilter()
	filter.AfterCreatedAt = &midGroup
	filter.AfterID = "ks-a3"
	tail, err := s.SearchIssues(c, "", filter)
	must(t, err)
	wantTail := []string{"ks-a4", "ks-a5", "ks-older"}
	if got := orderedIDs(tail); !reflect.DeepEqual(got, wantTail) {
		t.Errorf("resume from (group second, ks-a3) = %v, want %v: the cursor row, its lower-id siblings and everything newer are already delivered", got, wantTail)
	}

	groupStart := pagingSecond(0)
	filter = keysetOverflowFilter()
	filter.AfterCreatedAt = &groupStart
	filter.AfterID = ""
	whole, err := s.SearchIssues(c, "", filter)
	must(t, err)
	wantWhole := []string{"ks-a1", "ks-a2", "ks-a3", "ks-a4", "ks-a5", "ks-older"}
	if got := orderedIDs(whole); !reflect.DeepEqual(got, wantWhole) {
		t.Errorf(`resume from (group second, "") = %v, want %v: an empty AfterID starts the same-second group from its first id`, got, wantWhole)
	}
}

// testSearchPagingKeysetComposes (B3): the keyset position NARROWS what the
// other predicates matched — it composes with status and prefix filters and
// does not replace CreatedBefore.
//
// The three legs are cumulative on one fixture so each predicate is shown to be
// load-bearing by the row that only it excludes: the unpositioned read fixes
// what prefix+status matched, adding the position drops the cursor row and
// everything newer, and adding CreatedBefore drops the same-second survivor the
// position admitted. The last leg is the one that catches a backend letting the
// keyset's own upper bound (created_at <=, inclusive) displace CreatedBefore's
// (created_at <, strict).
func testSearchPagingKeysetComposes(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	oldest, older, cursorAt, newest := pagingSecond(0), pagingSecond(60), pagingSecond(120), pagingSecond(180)

	seedPagingIssue(t, s, "kc-a1", newest, types.StatusOpen)
	seedPagingIssue(t, s, "kc-b1", cursorAt, types.StatusOpen)
	seedPagingIssue(t, s, "kc-b2", cursorAt, types.StatusClosed)
	seedPagingIssue(t, s, "kc-b3", cursorAt, types.StatusOpen)
	seedPagingIssue(t, s, "kc-c1", older, types.StatusOpen)
	seedPagingIssue(t, s, "kc-c2", older, types.StatusClosed)
	seedPagingIssue(t, s, "kc-d1", oldest, types.StatusOpen)
	// A second population sharing every timestamp: only IDPrefix separates it.
	seedPagingIssue(t, s, "kd-a1", newest, types.StatusOpen)
	seedPagingIssue(t, s, "kd-b1", cursorAt, types.StatusOpen)
	seedPagingIssue(t, s, "kd-c1", older, types.StatusOpen)
	seedPagingIssue(t, s, "kd-d1", oldest, types.StatusOpen)

	open := types.StatusOpen
	unpositioned := types.IssueFilter{IDPrefix: "kc-", SkipWisps: true, SortBy: "created", Limit: 100, Status: &open}

	rows, err := s.SearchIssues(c, "", unpositioned)
	must(t, err)
	wantMatched := []string{"kc-a1", "kc-b1", "kc-b3", "kc-c1", "kc-d1"}
	if got := orderedIDs(rows); !reflect.DeepEqual(got, wantMatched) {
		t.Fatalf("prefix+status matched %v, want %v", got, wantMatched)
	}

	positioned := unpositioned
	cursor := cursorAt
	positioned.AfterCreatedAt = &cursor
	positioned.AfterID = "kc-b1"
	rows, err = s.SearchIssues(c, "", positioned)
	must(t, err)
	wantPositioned := []string{"kc-b3", "kc-c1", "kc-d1"}
	if got := orderedIDs(rows); !reflect.DeepEqual(got, wantPositioned) {
		t.Errorf("prefix+status resumed from (kc-b1) = %v, want %v: the position narrows the matched set and the other predicates still hold", got, wantPositioned)
	}

	bounded := positioned
	before := cursorAt
	bounded.CreatedBefore = &before
	rows, err = s.SearchIssues(c, "", bounded)
	must(t, err)
	wantBounded := []string{"kc-c1", "kc-d1"}
	if got := orderedIDs(rows); !reflect.DeepEqual(got, wantBounded) {
		t.Errorf("the same resume under CreatedBefore = %v, want %v: the position does not replace CreatedBefore, which still excludes kc-b3", got, wantBounded)
	}
}

const (
	pagingCapFixtureSize = 6
	pagingCapFlagSource  = "--max-rows"
	pagingCapEnvSource   = "BEADS_MAX_ROWS"
)

func seedPagingCapFixture(t *testing.T, s storage.DoltStorage) []string {
	t.Helper()
	ids := make([]string, 0, pagingCapFixtureSize)
	for i := 0; i < pagingCapFixtureSize; i++ {
		id := fmt.Sprintf("mr-%d", i)
		seedPagingIssue(t, s, id, pagingSecond(i), types.StatusOpen)
		ids = append(ids, id)
	}
	return ids
}

func pagingCapFilter(maxRows int, source string) types.IssueFilter {
	return types.IssueFilter{IDPrefix: "mr-", SkipWisps: true, MaxRows: maxRows, MaxRowsSource: source}
}

// testSearchPagingMaxRows (B4): a search whose matching rows exceed
// IssueFilter.MaxRows answers *issueops.ErrTooManyRows carrying the observed
// count, the cap, and the caller's source attribution verbatim; a result at or
// under the cap is unaffected; MaxRows:0 disables the cap; and a Limit at or
// under the cap cannot fire it.
//
// The typed shape is the contract, not a convenience: cmd/bd/max_rows.go
// classifies with errors.As and converts exactly this error to exit code 2, so a
// backend answering any other shape silently loses the circuit breaker while
// every row-count assertion still passes.
//
// This is the STORAGE layer's obligation, which is stricter than the reader
// role's honored-or-refused case: refusing the cap is not an option here.
func testSearchPagingMaxRows(t *testing.T, f Factory) {
	t.Run("UnderTheCapIsAnOrdinaryResult", func(t *testing.T) {
		s := f(t)
		ids := seedPagingCapFixture(t, s)
		for _, maxRows := range []int{pagingCapFixtureSize, pagingCapFixtureSize + 1} {
			rows, err := s.SearchIssues(ctx(), "", pagingCapFilter(maxRows, pagingCapFlagSource))
			if err != nil {
				t.Fatalf("MaxRows=%d over %d matching rows: %v", maxRows, pagingCapFixtureSize, err)
			}
			if got := issueIDs(rows); !reflect.DeepEqual(got, ids) {
				t.Errorf("MaxRows=%d answered %v, want the whole matching set %v", maxRows, got, ids)
			}
		}
	})

	t.Run("OverTheCapAnswersTheTypedRefusal", func(t *testing.T) {
		for _, leg := range []struct {
			maxRows int
			source  string
		}{
			{3, pagingCapFlagSource},
			{2, pagingCapEnvSource},
			{4, pagingCapFlagSource},
		} {
			t.Run(fmt.Sprintf("cap%d", leg.maxRows), func(t *testing.T) {
				s := f(t)
				seedPagingCapFixture(t, s)
				rows, err := s.SearchIssues(ctx(), "", pagingCapFilter(leg.maxRows, leg.source))
				if err == nil {
					t.Fatalf("MaxRows=%d over %d matching rows answered the page %v and no error: the cap was silently ignored",
						leg.maxRows, pagingCapFixtureSize, orderedIDs(rows))
				}
				var tooMany *storageops.ErrTooManyRows
				if !errors.As(err, &tooMany) {
					t.Fatalf("the cap failed with %T (%v); a caller classifies this with errors.As into *issueops.ErrTooManyRows", err, err)
				}
				if tooMany.Cap != leg.maxRows {
					t.Errorf("the cap error reports Cap = %d, want the %d the filter asked for", tooMany.Cap, leg.maxRows)
				}
				if tooMany.Found <= tooMany.Cap {
					t.Errorf("the cap error reports Found = %d against Cap = %d; a cap that fired observed more rows than it allows", tooMany.Found, tooMany.Cap)
				}
				if tooMany.Source != leg.source {
					t.Errorf("the cap error reports Source = %q, want the %q the filter supplied verbatim", tooMany.Source, leg.source)
				}
			})
		}
	})

	t.Run("ZeroDisablesTheCap", func(t *testing.T) {
		s := f(t)
		ids := seedPagingCapFixture(t, s)
		rows, err := s.SearchIssues(ctx(), "", pagingCapFilter(0, ""))
		if err != nil {
			t.Fatalf("MaxRows=0: %v", err)
		}
		if got := issueIDs(rows); !reflect.DeepEqual(got, ids) {
			t.Errorf("MaxRows=0 answered %v, want the whole matching set %v", got, ids)
		}
	})

	// The effective query bound is min(Limit, MaxRows+1) (types.go:1894-1895), so
	// a page the caller already bounded at or under the cap is an ordinary page:
	// the cap has nothing left to detect. A Limit ABOVE the cap still fires it.
	t.Run("ALimitAtOrUnderTheCapCannotFireIt", func(t *testing.T) {
		for _, leg := range []struct{ limit, maxRows int }{
			{2, 3},
			{3, 100},
			{3, 3},
		} {
			t.Run(fmt.Sprintf("limit%d_cap%d", leg.limit, leg.maxRows), func(t *testing.T) {
				s := f(t)
				seedPagingCapFixture(t, s)
				filter := pagingCapFilter(leg.maxRows, pagingCapFlagSource)
				filter.Limit = leg.limit
				rows, err := s.SearchIssues(ctx(), "", filter)
				if err != nil {
					t.Fatalf("Limit=%d MaxRows=%d: %v", leg.limit, leg.maxRows, err)
				}
				if len(rows) != leg.limit {
					t.Errorf("Limit=%d MaxRows=%d answered %d rows, want %d", leg.limit, leg.maxRows, len(rows), leg.limit)
				}
			})
		}
	})

	t.Run("ALimitOverTheCapStillFiresIt", func(t *testing.T) {
		s := f(t)
		seedPagingCapFixture(t, s)
		filter := pagingCapFilter(3, pagingCapEnvSource)
		filter.Limit = 10
		rows, err := s.SearchIssues(ctx(), "", filter)
		if err == nil {
			t.Fatalf("Limit=10 MaxRows=3 answered the page %v and no error: a Limit wider than the cap does not disarm it", orderedIDs(rows))
		}
		var tooMany *storageops.ErrTooManyRows
		if !errors.As(err, &tooMany) {
			t.Fatalf("the cap failed with %T (%v), want *issueops.ErrTooManyRows", err, err)
		}
		if tooMany.Cap != 3 {
			t.Errorf("the cap error reports Cap = %d, want 3", tooMany.Cap)
		}
		if tooMany.Source != pagingCapEnvSource {
			t.Errorf("the cap error reports Source = %q, want %q", tooMany.Source, pagingCapEnvSource)
		}
	})
}
