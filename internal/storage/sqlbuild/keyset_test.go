package sqlbuild

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestKeysetPredicateEmission pins the (created_at DESC, id ASC) keyset predicate
// BuildIssueFilterClauses emits for IssueFilter.AfterCreatedAt/AfterID: the exact
// sargable SQL fragment (single-sourced from KeysetCreatedAtIDPredicate) and its
// three bound args in order (created_at, created_at, id).
func TestKeysetPredicateEmission(t *testing.T) {
	t.Parallel()

	cur := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)

	// No keyset set: predicate absent.
	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses (no keyset): %v", err)
	}
	for _, c := range clauses {
		if strings.Contains(c, KeysetCreatedAtIDPredicate) {
			t.Fatalf("keyset predicate emitted with no AfterCreatedAt set: %v", clauses)
		}
	}
	_ = args

	// Keyset set: exactly one predicate clause equal to the single-sourced
	// constant, with three args in bind order.
	clauses, args, err = BuildIssueFilterClauses("", types.IssueFilter{
		AfterCreatedAt: &cur,
		AfterID:        "bd-42",
	}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses (keyset): %v", err)
	}
	found := 0
	for _, c := range clauses {
		if c == KeysetCreatedAtIDPredicate {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("keyset predicate clause count = %d, want 1; clauses=%v", found, clauses)
	}
	// The cursor time binds as time.Time (twice: sargable + strict bound), then
	// the id — bound as a value, not a formatted string, so the DATETIME columns
	// compare correctly on every backend.
	want := []any{cur, cur, "bd-42"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg[%d] = %v (%T), want %v (%T)", i, args[i], args[i], want[i], want[i])
		}
	}
}

// TestKeysetComposesWithCreatedBefore proves the new keyset field does not
// displace CreatedBefore: both predicates are emitted, and the keyset upper
// bound (created_at <=) is distinct from CreatedBefore's (created_at <).
func TestKeysetComposesWithCreatedBefore(t *testing.T) {
	t.Parallel()

	cur := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)
	before := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{
		CreatedBefore:  &before,
		AfterCreatedAt: &cur,
		AfterID:        "bd-7",
	}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	joined := strings.Join(clauses, " AND ")
	if !strings.Contains(joined, KeysetCreatedAtIDPredicate) {
		t.Fatalf("keyset predicate missing when composed with CreatedBefore: %v", clauses)
	}
	if !strings.Contains(joined, "created_at < ?") {
		t.Fatalf("CreatedBefore predicate (created_at < ?) missing: %v", clauses)
	}
	// CreatedBefore contributes one arg, keyset contributes three.
	if len(args) != 4 {
		t.Fatalf("arg count = %d, want 4 (1 CreatedBefore + 3 keyset)", len(args))
	}
}

// TestPriorityKeysetPredicateEmission pins the (priority ASC, created_at DESC,
// id ASC) keyset predicate BuildIssueFilterClauses emits when the position
// carries a priority: the exact sargable fragment (single-sourced from
// KeysetPriorityCreatedAtIDPredicate) and its five bound args in order
// (priority, priority, created_at, created_at, id).
//
// The created-order predicate must NOT also be emitted as a standalone clause:
// the two are alternatives, and ANDing them would exclude every row of a
// higher-numbered priority whose created_at is newer than the cursor's — the
// exact rows a priority walk exists to reach.
func TestPriorityKeysetPredicateEmission(t *testing.T) {
	t.Parallel()

	cur := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)
	p := 2

	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{
		AfterPriority:  &p,
		AfterCreatedAt: &cur,
		AfterID:        "bd-42",
	}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses (priority keyset): %v", err)
	}
	found, plain := 0, 0
	for _, c := range clauses {
		switch c {
		case KeysetPriorityCreatedAtIDPredicate:
			found++
		case KeysetCreatedAtIDPredicate:
			plain++
		}
	}
	if found != 1 {
		t.Fatalf("priority keyset predicate clause count = %d, want 1; clauses=%v", found, clauses)
	}
	if plain != 0 {
		t.Fatalf("the created-order predicate was ALSO emitted (%d times); the two orders are alternatives, not conjuncts: %v", plain, clauses)
	}
	want := []any{p, p, cur, cur, "bd-42"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg[%d] = %v (%T), want %v (%T)", i, args[i], args[i], want[i], want[i])
		}
	}
}

// TestPriorityKeysetNeedsBothHalvesOfThePosition pins that AfterPriority alone
// is not a position. The timestamp half is what decides whether a position was
// supplied — the same rule AfterID already follows — so a request carrying a
// priority and no instant emits no keyset predicate at all rather than a
// half-built one that would silently drop every row of the cursor's own
// priority.
func TestPriorityKeysetNeedsBothHalvesOfThePosition(t *testing.T) {
	t.Parallel()

	p := 2
	clauses, args, err := BuildIssueFilterClauses("", types.IssueFilter{AfterPriority: &p}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses (priority only): %v", err)
	}
	joined := strings.Join(clauses, " AND ")
	if strings.Contains(joined, "priority >= ?") {
		t.Fatalf("a priority with no instant emitted a keyset predicate: %v", clauses)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want none", args)
	}
}

// TestPriorityKeysetPredicateNestsTheCreatedOne pins the SHAPE that makes the
// predicate correct, not merely its text. Under (priority ASC, created_at DESC,
// id ASC) the created/id comparison is only reachable for rows of the cursor's
// OWN priority, so it must sit inside the priority-equal arm. A flattened
// `(priority > ?) OR (created_at < ?) OR (id > ?)` — the shape the created-only
// predicate's own leading-bound trick invites — admits a row at the cursor's
// priority created AFTER the cursor whenever its id sorts later, which is a
// duplicate on the next page.
func TestPriorityKeysetPredicateNestsTheCreatedOne(t *testing.T) {
	t.Parallel()

	if !strings.Contains(KeysetPriorityCreatedAtIDPredicate, KeysetCreatedAtIDPredicate) {
		t.Fatalf("the priority predicate does not contain the created one verbatim, so the two can drift:\n priority: %s\n created:  %s",
			KeysetPriorityCreatedAtIDPredicate, KeysetCreatedAtIDPredicate)
	}
	if !strings.HasPrefix(KeysetPriorityCreatedAtIDPredicate, "(priority >= ?") {
		t.Fatalf("the priority predicate lost its sargable leading bound: %s", KeysetPriorityCreatedAtIDPredicate)
	}
	if strings.Count(KeysetPriorityCreatedAtIDPredicate, "?") != 5 {
		t.Fatalf("the priority predicate binds %d placeholders, want 5 (priority, priority, created_at, created_at, id): %s",
			strings.Count(KeysetPriorityCreatedAtIDPredicate, "?"), KeysetPriorityCreatedAtIDPredicate)
	}
}
