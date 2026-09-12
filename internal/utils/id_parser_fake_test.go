package utils_test

// Fake, in-memory implementation of utils.PartialIDResolverStore. Unlike the
// other tests in this package, this one needs no cgo, no Dolt binary, and no
// Docker test container — it exercises ResolvePartialID/ResolvePartialIDExact
// against a hand-rolled store, so it can actually run in any environment
// (including ones where the Docker-backed newTestStore tests in
// id_parser_test.go / id_parser_exact_test.go skip themselves).

import (
	"context"
	"errors"
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
	// silently landing on the unrelated wisp. Asserting errors.Is (not just
	// err != nil) pins the WISP branch's abbrevOnly tracking specifically:
	// without it, "list" still fails (via the generic "no issue found"
	// fallback), so a regression that silently drops the wisp-branch
	// abbrevOnly candidate — reintroducing this exact incident's false
	// not-found — would pass this test undetected.
	if got, err := utils.ResolvePartialIDExact(ctx, store, "list"); err == nil {
		t.Fatalf(`ResolvePartialIDExact("list") = (%q, nil); want a "not found" error, not a silent match onto hq-wisp-list3t0`, got)
	} else if !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Fatalf(`ResolvePartialIDExact("list") error = %v; want errors.Is(err, ErrAbbreviatedIDNotAllowed) — "list" IS a valid leading-prefix abbreviation of hq-wisp-list3t0, so the error must say so truthfully, not fall back to a generic "no issue found"`, err)
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

// TestResolvePartialIDExact_AbbreviationRefusalIsDistinguishableFromNotFound
// is a Docker-free regression test for the follow-up steveyegge's PR #5393
// review flagged (item c): an abbreviation that DOES match a real issue must
// error differently from an input that matches nothing at all, because "no
// issue found matching %q" is false in the first case — the issue exists,
// only the abbreviation was refused. Exact-only callers (bd comment) use
// errors.Is(err, ErrAbbreviatedIDNotAllowed) to tell the two apart and give a
// truthful, actionable message instead of claiming the issue is missing.
func TestResolvePartialIDExact_AbbreviationRefusalIsDistinguishableFromNotFound(t *testing.T) {
	ctx := context.Background()
	store := &fakeResolverStore{
		issues: []fakeIssue{
			{id: "hq-a3f8e9", ephemeral: false},
		},
		config: map[string]string{"issue_prefix": "hq"},
	}

	// A real, valid leading-prefix abbreviation of an existing issue: must
	// wrap ErrAbbreviatedIDNotAllowed, not a plain "not found".
	got, err := utils.ResolvePartialIDExact(ctx, store, "a3f8")
	if err == nil {
		t.Fatalf(`ResolvePartialIDExact("a3f8") = (%q, nil); want an ErrAbbreviatedIDNotAllowed error — "a3f8" is a real abbreviation of hq-a3f8e9`, got)
	}
	if !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf(`ResolvePartialIDExact("a3f8") error = %q; want it to wrap ErrAbbreviatedIDNotAllowed (errors.Is)`, err)
	}
	if strings.Contains(err.Error(), "no issue found matching") {
		t.Errorf(`ResolvePartialIDExact("a3f8") error = %q; must NOT claim "no issue found" — hq-a3f8e9 exists`, err)
	}

	// An input that matches nothing at all, not even via abbreviation: must
	// stay a plain not-found, and must NOT wrap ErrAbbreviatedIDNotAllowed —
	// proves the two failure modes are genuinely distinguishable, not just
	// differently worded copies of the same check.
	got, err = utils.ResolvePartialIDExact(ctx, store, "zzzzzz")
	if err == nil {
		t.Fatalf(`ResolvePartialIDExact("zzzzzz") = (%q, nil); want a not-found error`, got)
	}
	if errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf(`ResolvePartialIDExact("zzzzzz") error = %q; want a plain not-found, not ErrAbbreviatedIDNotAllowed — nothing matches "zzzzzz" even via abbreviation`, err)
	}
	if !strings.Contains(err.Error(), "no issue found matching") {
		t.Errorf(`ResolvePartialIDExact("zzzzzz") error = %q; want the genuine "no issue found matching" wording`, err)
	}

	// Sanity: the exact id still resolves under the same store/config,
	// confirming the fixture didn't accidentally break the happy path.
	if got, err := utils.ResolvePartialIDExact(ctx, store, "hq-a3f8e9"); err != nil || got != "hq-a3f8e9" {
		t.Errorf("ResolvePartialIDExact(%q) = (%q, %v); want (%q, nil)", "hq-a3f8e9", got, err, "hq-a3f8e9")
	}
}
