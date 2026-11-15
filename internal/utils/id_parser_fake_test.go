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
	"slices"
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
	// separateWisps leaves ephemeral issues out of an unfiltered
	// SearchIssueIDs, so only the resolver's wisp fallback finds them. By
	// default they are merged in, as the Dolt stores do.
	separateWisps bool
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
		if filter.Ephemeral == nil && f.separateWisps && iss.ephemeral {
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

// TestResolvePartialIDExact_AbbreviationErrorNamesTheMatches pins the typed
// error behind ErrAbbreviatedIDNotAllowed: errors.As yields the input and the
// sorted full ids it abbreviates, so a caller can name them (bd delete does),
// while errors.Is and the error text stay what they were.
func TestResolvePartialIDExact_AbbreviationErrorNamesTheMatches(t *testing.T) {
	ctx := context.Background()
	store := &fakeResolverStore{
		issues: []fakeIssue{
			{id: "hq-a3f8zz", ephemeral: false},
			{id: "hq-a3f8e9", ephemeral: false},
		},
		config: map[string]string{"issue_prefix": "hq"},
	}

	_, err := utils.ResolvePartialIDExact(ctx, store, "a3f8")
	var abbrev *utils.AbbreviatedIDError
	if !errors.As(err, &abbrev) {
		t.Fatalf(`ResolvePartialIDExact("a3f8") error = %v; want an *AbbreviatedIDError`, err)
	}
	if abbrev.Input != "a3f8" {
		t.Errorf("Input = %q; want %q", abbrev.Input, "a3f8")
	}
	if want := []string{"hq-a3f8e9", "hq-a3f8zz"}; !slices.Equal(abbrev.Matches, want) {
		t.Errorf("Matches = %v; want %v (sorted)", abbrev.Matches, want)
	}
	if !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf("error %v no longer wraps ErrAbbreviatedIDNotAllowed", err)
	}
	if want := `id is a valid abbreviation, but exact-match resolution is required here: "a3f8" (matches [hq-a3f8e9 hq-a3f8zz])`; err.Error() != want {
		t.Errorf("error text = %q; want %q", err.Error(), want)
	}

	// The wisp branch builds the same error.
	_, err = utils.ResolvePartialIDExact(ctx, &fakeResolverStore{
		issues: []fakeIssue{{id: "hq-wisp-list3t0", ephemeral: true}},
		config: map[string]string{"issue_prefix": "hq"},
	}, "list")
	if !errors.As(err, &abbrev) || !slices.Equal(abbrev.Matches, []string{"hq-wisp-list3t0"}) {
		t.Errorf(`ResolvePartialIDExact("list") error = %v; want an *AbbreviatedIDError naming hq-wisp-list3t0`, err)
	}
}

// TestResolvePartialIDExact_RefusesSentinelTokens pins where #6215's bare-
// sentinel guard has to live once this PR splits the resolver in two.
//
// #6215 (base) refuses "null"/"undefined"/"none"/"nil"/"" before any lookup,
// and put that guard in ResolvePartialID because it was then the only
// ID-resolution entry point in the repository. This PR adds a second one,
// ResolvePartialIDExact, so the guard belongs in the shared resolvePartialID
// body — which is what keeps #6215's own stated guarantee ("bd update null,
// bd comment null and every other command that resolves an ID now fail before
// any lookup") true for the comment write paths this PR routes through the
// exact-only resolver.
//
// Nothing else covers that: #6215's id_parser_sentinel_test.go exercises only
// ResolvePartialID, so leaving the guard in the exported wrapper alone keeps
// every one of its assertions green while a sentinel reaching the exact path
// degrades into an ErrAbbreviatedIDNotAllowed that names an unrelated decoy
// issue — telling the caller to "use the full id" for a token that is really
// just their selector having matched nothing.
func TestResolvePartialIDExact_RefusesSentinelTokens(t *testing.T) {
	ctx := context.Background()
	// hq-null3t0 is #6215's own decoy shape: a real issue whose hash begins
	// with "null", so an unguarded sentinel is a live abbreviation match here
	// rather than a plain not-found.
	store := &fakeResolverStore{
		issues: []fakeIssue{
			{id: "hq-null3t0", ephemeral: false},
			{id: "hq-165vq", ephemeral: false},
		},
		config: map[string]string{"issue_prefix": "hq"},
	}

	for _, input := range []string{"null", "NULL", " null ", "undefined", "none", "nil", "", "   "} {
		got, err := utils.ResolvePartialIDExact(ctx, store, input)
		if err == nil {
			t.Errorf("ResolvePartialIDExact(%q) = (%q, nil); want #6215's sentinel refusal", input, got)
			continue
		}
		if !strings.Contains(err.Error(), "refusing") {
			t.Errorf("ResolvePartialIDExact(%q) error = %q; want #6215's sentinel refusal (guard did not fire on the exact path)", input, err)
		}
		if errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
			t.Errorf("ResolvePartialIDExact(%q) error = %q; a tooling sentinel must be refused as such, not reported as a refused abbreviation of hq-null3t0", input, err)
		}
		if strings.Contains(err.Error(), "hq-null3t0") {
			t.Errorf("ResolvePartialIDExact(%q) error = %q; must not name the decoy issue", input, err)
		}
	}

	// Over-refusal guard, the twin of #6215's accepted-inputs test: an id that
	// merely CONTAINS a sentinel token must still resolve exactly.
	for _, input := range []string{"hq-null3t0", "null3t0"} {
		if got, err := utils.ResolvePartialIDExact(ctx, store, input); err != nil || got != "hq-null3t0" {
			t.Errorf("ResolvePartialIDExact(%q) = (%q, %v); want (%q, nil) — the guard must match the whole token, not a prefix", input, got, err, "hq-null3t0")
		}
	}
}

// TestResolvePartialID_FakeStore_QualifiedInputsNeverResolveToAnotherBead pins
// the leading-prefix matcher (#4939) against inputs that a looser matcher
// resolves to an unrelated bead. Each input names no issue in its store but
// shares a fragment with the store's only bead (the decoy), so a matcher that
// compares less than the whole hash part — a substring test, or only the text
// after the last dash — hands back the decoy. Each must stay a plain not-found
// on both resolvers: the default one would act on the decoy, and the
// exact-only one would claim the input abbreviates it.
//
// Every case also resolves a genuine leading-prefix abbreviation of its decoy,
// so a not-found cannot pass merely because the fixture answers nothing.
func TestResolvePartialID_FakeStore_QualifiedInputsNeverResolveToAnotherBead(t *testing.T) {
	ctx := context.Background()
	// The controls are abbreviations, which announce themselves on stderr;
	// that notice is pinned in id_parser_unit_test.go.
	t.Setenv("BD_NO_PARTIAL_ID_NOTICE", "1")

	cases := []struct {
		name    string
		prefix  string
		decoy   fakeIssue
		control string   // a leading-prefix abbreviation of decoy
		inputs  []string // ids that name no issue in the store
	}{
		{
			// The reported gcy-g4o incident: "oqf" is only a substring of
			// the wisp hash "goqfo".
			name:    "substring of a wisp hash",
			prefix:  "gcy",
			decoy:   fakeIssue{id: "gcy-wisp-goqfo", ephemeral: true},
			control: "goq",
			inputs:  []string{"gcy-oqf", "oqf"},
		},
		{
			// A routed command tries prefix routing only after the local
			// lookup reports not-found, so a local hit here would act on
			// bd-8wnq2 instead of the hr- rig's issue.
			name:    "foreign prefix",
			prefix:  "bd",
			decoy:   fakeIssue{id: "bd-8wnq2"},
			control: "8wnq",
			inputs:  []string{"hr-8wnq"},
		},
		{
			name:    "wisp-qualified id against a plain hash",
			prefix:  "gcy",
			decoy:   fakeIssue{id: "gcy-g4oab"},
			control: "g4o",
			inputs:  []string{"gcy-wisp-g4o"},
		},
		{
			name:    "wisp-qualified id against a mol-qualified hash",
			prefix:  "bd",
			decoy:   fakeIssue{id: "bd-mol-t3xyz"},
			control: "bd-mol-t3",
			inputs:  []string{"bd-wisp-t3"},
		},
		{
			name:    "fragment after an interior dash",
			prefix:  "bd",
			decoy:   fakeIssue{id: "bd-mol-abcde"},
			control: "bd-mol-ab",
			inputs:  []string{"abc"},
		},
		{
			// Nothing follows the last dash, and an empty fragment is a
			// prefix of everything.
			name:    "trailing dash",
			prefix:  "bd",
			decoy:   fakeIssue{id: "bd-mol-abcde"},
			control: "bd-mol-ab",
			inputs:  []string{"bd-ol-", "ol-"},
		},
		{
			// A trailing dash is fine after a leading prefix of the hash
			// (the control), and nowhere else.
			name:    "trailing dash against a numeric tail",
			prefix:  "bd",
			decoy:   fakeIssue{id: "bd-x-aap-1"},
			control: "bd-x-aap-",
			inputs:  []string{"aap-", "bd-aap-"},
		},
		{
			name:    "lone dash",
			prefix:  "bd",
			decoy:   fakeIssue{id: "bd-a3f8e9"},
			control: "a3f8",
			inputs:  []string{"-"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeResolverStore{
				issues: []fakeIssue{tc.decoy},
				config: map[string]string{"issue_prefix": tc.prefix},
			}
			if got, err := utils.ResolvePartialID(ctx, store, tc.control); err != nil || got != tc.decoy.id {
				t.Fatalf("fixture sanity check failed: ResolvePartialID(%q) = (%q, %v); want (%q, nil)", tc.control, got, err, tc.decoy.id)
			}

			for _, input := range tc.inputs {
				got, err := utils.ResolvePartialID(ctx, store, input)
				if err == nil {
					t.Errorf("ResolvePartialID(%q) = %q; want not found — %q names no issue here", input, got, input)
				} else if !strings.Contains(err.Error(), "no issue found matching") {
					t.Errorf("ResolvePartialID(%q) error = %v; want a plain not-found", input, err)
				}

				got, err = utils.ResolvePartialIDExact(ctx, store, input)
				if err == nil {
					t.Errorf("ResolvePartialIDExact(%q) = %q; want not found — %q names no issue here", input, got, input)
				} else if errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) || !strings.Contains(err.Error(), "no issue found matching") {
					t.Errorf("ResolvePartialIDExact(%q) error = %v; want a plain not-found — %q abbreviates no issue here", input, err, input)
				}
			}
		})
	}
}

// TestResolvePartialID_FakeStore_SharedHashAcrossPrefixesIsAmbiguous pins
// mc-ke8mg. When two prefixes share a hash and neither is the configured
// prefix, the bare hash names both issues equally well. The resolver used to
// keep whichever one the store returned last, silently, and without the
// partial-ID notice, because the match counts as exact. So the store's return
// order picked the issue a command acted on. Both resolvers must refuse with
// ErrAmbiguousID and name every candidate, whatever order the store answers in.
func TestResolvePartialID_FakeStore_SharedHashAcrossPrefixesIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	config := map[string]string{"issue_prefix": "bd", "allowed_prefixes": "hacker-news,ops"}
	resolvers := []struct {
		name    string
		resolve func(context.Context, utils.PartialIDResolverStore, string) (string, error)
	}{
		{"ResolvePartialID", utils.ResolvePartialID},
		{"ResolvePartialIDExact", utils.ResolvePartialIDExact},
	}

	cases := []struct {
		name    string
		a, b    fakeIssue
		input   string
		wantErr string
	}{
		{
			name:    "issues",
			a:       fakeIssue{id: "hacker-news-abc123"},
			b:       fakeIssue{id: "ops-abc123"},
			input:   "abc123",
			wantErr: "ambiguous issue ID: \"abc123\" matches 2 issues: [hacker-news-abc123 ops-abc123]\nUse the full ID to disambiguate",
		},
		{
			// Wisp IDs carry a "wisp-" infix, which both of the resolver's
			// searches strip before they compare hashes.
			name:    "wisps",
			a:       fakeIssue{id: "hacker-news-wisp-t3st", ephemeral: true},
			b:       fakeIssue{id: "ops-wisp-t3st", ephemeral: true},
			input:   "t3st",
			wantErr: "ambiguous issue ID: \"t3st\" matches 2 issues: [hacker-news-wisp-t3st ops-wisp-t3st]\nUse the full ID to disambiguate",
		},
		{
			// An issue and a wisp. The issue's hash matches in the first
			// search, so the wisp fallback must still run to find the wisp
			// when the store leaves wisps out of that search.
			name:    "issue and wisp",
			a:       fakeIssue{id: "ops-abc123"},
			b:       fakeIssue{id: "hacker-news-wisp-abc123", ephemeral: true},
			input:   "abc123",
			wantErr: "ambiguous issue ID: \"abc123\" matches 2 issues: [hacker-news-wisp-abc123 ops-abc123]\nUse the full ID to disambiguate",
		},
	}

	// The Dolt stores list wisps in the resolver's first, unfiltered search;
	// a store that does not leaves them to the wisp fallback. Both shapes must
	// reach the same answer.
	shapes := []struct {
		name          string
		separateWisps bool
	}{
		{"wisps merged", false},
		{"wisps separate", true},
	}

	for _, tc := range cases {
		for _, shape := range shapes {
			t.Run(tc.name+"/"+shape.name, func(t *testing.T) {
				// Try both store orders, since the order is what used to decide.
				for _, issues := range [][]fakeIssue{{tc.a, tc.b}, {tc.b, tc.a}} {
					store := &fakeResolverStore{issues: issues, config: config, separateWisps: shape.separateWisps}
					for _, r := range resolvers {
						got, err := r.resolve(ctx, store, tc.input)
						if !errors.Is(err, utils.ErrAmbiguousID) {
							t.Errorf("%s(%q) with %s first = (%q, %v); want an ErrAmbiguousID error", r.name, tc.input, issues[0].id, got, err)
						} else if err.Error() != tc.wantErr {
							t.Errorf("%s(%q) error = %q; want %q", r.name, tc.input, err, tc.wantErr)
						}
					}
				}

				// A full ID still names exactly one issue. A hash that only one
				// prefix uses is still an exact match, including a wisp that a
				// merging store lists in both searches.
				for _, r := range resolvers {
					store := &fakeResolverStore{issues: []fakeIssue{tc.a, tc.b}, config: config, separateWisps: shape.separateWisps}
					if got, err := r.resolve(ctx, store, tc.b.id); err != nil || got != tc.b.id {
						t.Errorf("%s(%q) = (%q, %v); want (%q, nil)", r.name, tc.b.id, got, err, tc.b.id)
					}
					for _, only := range []fakeIssue{tc.a, tc.b} {
						store = &fakeResolverStore{issues: []fakeIssue{only}, config: config, separateWisps: shape.separateWisps}
						if got, err := r.resolve(ctx, store, tc.input); err != nil || got != only.id {
							t.Errorf("%s(%q) with only %s = (%q, %v); want (%q, nil)", r.name, tc.input, only.id, got, err, only.id)
						}
					}
				}
			})
		}
	}

	// An issue the input only abbreviates (ops-abc1234) does not stop the
	// wisp fallback from finding a wisp that shares the matched hash, and is
	// no candidate itself.
	for _, shape := range shapes {
		store := &fakeResolverStore{
			issues:        []fakeIssue{{id: "ops-abc123"}, {id: "ops-abc1234"}, {id: "hacker-news-wisp-abc123", ephemeral: true}},
			config:        config,
			separateWisps: shape.separateWisps,
		}
		want := "ambiguous issue ID: \"abc123\" matches 2 issues: [hacker-news-wisp-abc123 ops-abc123]\nUse the full ID to disambiguate"
		for _, r := range resolvers {
			if got, err := r.resolve(ctx, store, "abc123"); err == nil || err.Error() != want {
				t.Errorf("%s(%q) with an abbreviated issue beside, %s = (%q, %v); want error %q", r.name, "abc123", shape.name, got, err, want)
			}
		}
	}

	// The configured prefix's own spelling is looked up before any search.
	// So a bare hash that prefix uses still resolves to it, even when other
	// prefixes share the hash.
	store := &fakeResolverStore{
		issues: []fakeIssue{{id: "hacker-news-abc123"}, {id: "ops-abc123"}, {id: "bd-abc123"}},
		config: config,
	}
	for _, r := range resolvers {
		if got, err := r.resolve(ctx, store, "abc123"); err != nil || got != "bd-abc123" {
			t.Errorf("%s(%q) = (%q, %v); want (%q, nil)", r.name, "abc123", got, err, "bd-abc123")
		}
	}
}

// TestResolvePartialID_FakeStore_ExactWispHashBeatsAbbreviation pins that a
// wisp the store lists in the resolver's first search (the Dolt stores merge
// wisps into it) matches there on its hash, as an issue does. ResolvePartialID
// used to pass the wisp over and settle on an issue the input only
// abbreviates; ResolvePartialIDExact, which takes no abbreviations, already
// found the wisp through the wisp fallback.
func TestResolvePartialID_FakeStore_ExactWispHashBeatsAbbreviation(t *testing.T) {
	ctx := context.Background()
	store := &fakeResolverStore{
		issues: []fakeIssue{{id: "ops-abc1234"}, {id: "hacker-news-wisp-abc123", ephemeral: true}},
		config: map[string]string{"issue_prefix": "bd", "allowed_prefixes": "hacker-news,ops"},
	}
	resolvers := []struct {
		name    string
		resolve func(context.Context, utils.PartialIDResolverStore, string) (string, error)
	}{
		{"ResolvePartialID", utils.ResolvePartialID},
		{"ResolvePartialIDExact", utils.ResolvePartialIDExact},
	}
	for _, r := range resolvers {
		if got, err := r.resolve(ctx, store, "abc123"); err != nil || got != "hacker-news-wisp-abc123" {
			t.Errorf("%s(%q) = (%q, %v); want (%q, nil)", r.name, "abc123", got, err, "hacker-news-wisp-abc123")
		}
	}
}
