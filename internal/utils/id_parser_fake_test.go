package utils_test

// Fake, in-memory implementation of utils.PartialIDResolverStore. Unlike the
// other tests in this package, this one needs no cgo, no Dolt binary, and no
// Docker test container — it exercises ResolvePartialID/ResolvePartialIDExact
// against a hand-rolled store, so it can actually run in any environment
// (including ones where the Docker-backed newTestStore tests in
// id_parser_test.go / id_parser_exact_test.go skip themselves).

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

type fakeIssue struct {
	id        string
	ephemeral bool
}

// fakeResolverStore is a minimal PartialIDResolverStore backed by a plain
// slice, replicating just enough of the real store's query semantics for
// ResolvePartialID/ResolvePartialIDExact: exact-ID lookup (SearchIssues with
// filter.IDs) and substring "LIKE"-style search optionally scoped to
// ephemeral issues (SearchIssueIDs).
type fakeResolverStore struct {
	issues []fakeIssue
	config map[string]string
}

func (f *fakeResolverStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, iss := range f.issues {
		if len(filter.IDs) > 0 && !containsStr(filter.IDs, iss.id) {
			continue
		}
		out = append(out, &types.Issue{ID: iss.id, Ephemeral: iss.ephemeral})
	}
	return out, nil
}

func (f *fakeResolverStore) SearchIssueIDs(_ context.Context, query string, filter types.IssueFilter) ([]string, error) {
	var out []string
	for _, iss := range f.issues {
		if filter.Ephemeral != nil && iss.ephemeral != *filter.Ephemeral {
			continue
		}
		if query != "" && !strings.Contains(iss.id, query) {
			continue
		}
		out = append(out, iss.id)
	}
	return out, nil
}

func (f *fakeResolverStore) GetConfig(_ context.Context, key string) (string, error) {
	return f.config[key], nil
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestResolvePartialIDExact_FakeStore_ReproducesReportedIncident is a
// Docker-free, always-runnable reproduction of a real incident:
// "bd comment list <id>" (a typo for "bd comments list") resolved "list"
// against a wisp "hq-wisp-list3t0" via leading-prefix abbreviation and wrote
// the rest of the command line to it as a comment. It asserts three things
// in one place: (1) the fixture genuinely reproduces the old collision via
// ResolvePartialID, (2) ResolvePartialIDExact refuses the same input, and
// (3) ResolvePartialIDExact still resolves genuinely exact references
// (happy path untouched).
func TestResolvePartialIDExact_FakeStore_ReproducesReportedIncident(t *testing.T) {
	ctx := context.Background()
	store := &fakeResolverStore{
		issues: []fakeIssue{
			{id: "hq-wisp-list3t0", ephemeral: true},
			{id: "hq-165vq", ephemeral: false},
		},
		config: map[string]string{"issue_prefix": "hq"},
	}

	// Sanity check: confirm the fixture reproduces the OLD (still-default,
	// still-correct-for-read-paths) fuzzy behavior before asserting the fix.
	gotFuzzy, err := utils.ResolvePartialID(ctx, store, "list")
	if err != nil || gotFuzzy != "hq-wisp-list3t0" {
		t.Fatalf("fixture sanity check failed: ResolvePartialID(%q) = (%q, %v); want (%q, nil) — incident did not reproduce",
			"list", gotFuzzy, err, "hq-wisp-list3t0")
	}

	// The fix: the write-path resolver must refuse the same input instead of
	// silently landing on the unrelated wisp.
	if got, err := utils.ResolvePartialIDExact(ctx, store, "list"); err == nil {
		t.Fatalf(`ResolvePartialIDExact("list") = (%q, nil); want a "not found" error, not a silent match onto hq-wisp-list3t0`, got)
	}

	// Happy path: an id that genuinely, exactly names an issue must still
	// resolve under the exact-only resolver.
	if got, err := utils.ResolvePartialIDExact(ctx, store, "hq-165vq"); err != nil || got != "hq-165vq" {
		t.Errorf("ResolvePartialIDExact(%q) = (%q, %v); want (%q, nil)", "hq-165vq", got, err, "hq-165vq")
	}
	if got, err := utils.ResolvePartialIDExact(ctx, store, "165vq"); err != nil || got != "hq-165vq" {
		t.Errorf("ResolvePartialIDExact(%q) = (%q, %v); want (%q, nil) — bare full hash must still resolve exactly", "165vq", got, err, "hq-165vq")
	}
	if got, err := utils.ResolvePartialIDExact(ctx, store, "hq-wisp-list3t0"); err != nil || got != "hq-wisp-list3t0" {
		t.Errorf("ResolvePartialIDExact(%q) = (%q, %v); want (%q, nil) — full wisp id must still resolve exactly", "hq-wisp-list3t0", got, err, "hq-wisp-list3t0")
	}
}
