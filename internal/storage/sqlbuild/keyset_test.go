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
