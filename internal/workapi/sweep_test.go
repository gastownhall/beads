package workapi

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The DATABASE-FREE half of issueops.Sweeper: what a request means, which
// candidates survive the recheck, and what counts as a citation. The
// conformance contract asserts what only a real backend can show.

func TestValidateSweepRequestRequiresATier(t *testing.T) {
	for _, test := range []struct {
		name string
		tier issueops.SweepTier
	}{
		{"unset", ""},
		{"unrecognized", issueops.SweepTier("wisps")},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSweepRequest(issueops.SweepRequest{Tier: test.tier, IDPattern: "*"})
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("ValidateSweepRequest(tier=%q) error = %v, want ErrValidation", test.tier, err)
			}
		})
	}
}

// TestValidateSweepRequestGatesTheDurableTier is the safety invariant Q9 moved
// below the front doors: an unfiltered DURABLE sweep is a refusal, and the
// same request on the ephemeral tier is not.
func TestValidateSweepRequestGatesTheDurableTier(t *testing.T) {
	cutoff := time.Now()
	for _, test := range []struct {
		name    string
		request issueops.SweepRequest
		refused bool
	}{
		{"durable, no filter", issueops.SweepRequest{Tier: issueops.SweepDurable}, true},
		{"durable, cutoff", issueops.SweepRequest{Tier: issueops.SweepDurable, ClosedBefore: &cutoff}, false},
		{"durable, pattern", issueops.SweepRequest{Tier: issueops.SweepDurable, IDPattern: "*"}, false},
		// The deliberate keystroke the refusal points at: "*" is how a caller
		// says "everything closed" on purpose.
		{"durable, star", issueops.SweepRequest{Tier: issueops.SweepDurable, IDPattern: "*"}, false},
		{"ephemeral, no filter", issueops.SweepRequest{Tier: issueops.SweepEphemeral}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSweepRequest(test.request)
			if got := errors.Is(err, issueops.ErrValidation); got != test.refused {
				t.Fatalf("ValidateSweepRequest() error = %v, want refused=%v", err, test.refused)
			}
		})
	}
}

// TestValidateSweepRequestRefusesAMalformedPattern pins the defect fix: both
// front doors used to discard filepath.Match's error, so a bad glob reported
// "nothing matched" on a command whose job is to delete what it matched.
func TestValidateSweepRequestRefusesAMalformedPattern(t *testing.T) {
	err := ValidateSweepRequest(issueops.SweepRequest{Tier: issueops.SweepDurable, IDPattern: "[bad"})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("ValidateSweepRequest(pattern=%q) error = %v, want ErrValidation", "[bad", err)
	}
	if !strings.Contains(err.Error(), "[bad") {
		t.Fatalf("refusal %q does not quote the pattern the caller sent", err)
	}
}

func TestBuildSweepCandidateFilterSelectsOneClosedTier(t *testing.T) {
	cutoff := time.Date(2026, 4, 26, 18, 30, 18, 0, time.UTC)
	for _, test := range []struct {
		tier      issueops.SweepTier
		ephemeral bool
	}{
		{issueops.SweepEphemeral, true},
		{issueops.SweepDurable, false},
	} {
		t.Run(string(test.tier), func(t *testing.T) {
			filter := BuildSweepCandidateFilter(issueops.SweepRequest{Tier: test.tier, ClosedBefore: &cutoff})
			if filter.Status == nil || *filter.Status != types.StatusClosed {
				t.Fatalf("filter.Status = %v, want closed", filter.Status)
			}
			if filter.EphemeralTier == nil || *filter.EphemeralTier != test.ephemeral {
				t.Fatalf("filter.EphemeralTier = %v, want %v", filter.EphemeralTier, test.ephemeral)
			}
			// The tier field replaces the raw flag on purpose: an Ephemeral
			// filter would (a) miss typed wisps minted without the flag and
			// (b) route the candidate search to the wisps plane alone, hiding
			// legacy typed wisps in the issues table.
			if filter.Ephemeral != nil {
				t.Fatalf("filter.Ephemeral = %v, want nil — the sweep selects by tier, not the raw flag", filter.Ephemeral)
			}
			if filter.ClosedBefore == nil || !filter.ClosedBefore.Equal(cutoff) {
				t.Fatalf("filter.ClosedBefore = %v, want %v", filter.ClosedBefore, cutoff)
			}
		})
	}
}

// TestBuildSweepCandidateFilterCopiesTheCutoff pins the no-mutation promise on
// the one pointer the request carries.
func TestBuildSweepCandidateFilterCopiesTheCutoff(t *testing.T) {
	cutoff := time.Date(2026, 4, 26, 18, 30, 18, 0, time.UTC)
	filter := BuildSweepCandidateFilter(issueops.SweepRequest{Tier: issueops.SweepDurable, ClosedBefore: &cutoff})
	if filter.ClosedBefore == &cutoff {
		t.Fatal("filter shares the caller's *time.Time; a builder must not hand back an alias of a request field")
	}
}

// TestFilterSweepCandidatesRechecksClosedAtCutoff is the recheck's own table,
// moved here from cmd/bd when the role took it below both front doors.
func TestFilterSweepCandidatesRechecksClosedAtCutoff(t *testing.T) {
	cutoff := time.Date(2026, 3, 27, 20, 1, 44, 0, time.UTC)
	oldClosedAt := cutoff.Add(-time.Second)
	recentClosedAt := cutoff.Add(time.Second)

	candidates := []*types.Issue{
		{ID: "old-closed", Status: types.StatusClosed, ClosedAt: &oldClosedAt},
		{ID: "recent-closed", Status: types.StatusClosed, ClosedAt: &recentClosedAt},
		{ID: "missing-closed-at", Status: types.StatusClosed},
		{ID: "open-with-old-closed-at", Status: types.StatusOpen, ClosedAt: &oldClosedAt},
		{ID: "pinned-old", Status: types.StatusClosed, ClosedAt: &oldClosedAt, Pinned: true},
		nil,
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{ClosedBefore: &cutoff})

	if len(filtered) != 1 || filtered[0].ID != "old-closed" {
		t.Fatalf("filtered IDs = %v, want only old-closed", sweepCandidateIDs(filtered))
	}
	for _, check := range []struct {
		name string
		got  int
	}{
		{"Pinned", skips.Pinned},
		{"ClosedAtOrAfterCutoff", skips.ClosedAtOrAfterCutoff},
		{"UnknownClosedAt", skips.UnknownClosedAt},
		{"NotClosed", skips.NotClosed},
		{"Unreadable", skips.Unreadable},
	} {
		if check.got != 1 {
			t.Errorf("skips.%s = %d, want 1", check.name, check.got)
		}
	}
}

func TestFilterSweepCandidatesWithoutCutoffStillRequiresClosedAt(t *testing.T) {
	closedAt := time.Date(2026, 4, 26, 18, 30, 18, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "closed", Status: types.StatusClosed, ClosedAt: &closedAt},
		{ID: "closed-missing-time", Status: types.StatusClosed},
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{})

	if len(filtered) != 1 || filtered[0].ID != "closed" {
		t.Fatalf("filtered IDs = %v, want only closed", sweepCandidateIDs(filtered))
	}
	if skips.UnknownClosedAt != 1 {
		t.Fatalf("skips.UnknownClosedAt = %d, want 1", skips.UnknownClosedAt)
	}
	if skips.ClosedAtOrAfterCutoff != 0 {
		t.Fatalf("skips.ClosedAtOrAfterCutoff = %d, want 0 without a cutoff", skips.ClosedAtOrAfterCutoff)
	}
}

// TestFilterSweepCandidatesNarrowsByPatternBeforeProtecting pins the ORDER
// issueops.Sweeper.Sweep states: a pinned row the pattern excluded is not
// counted as protected, because it was never a candidate.
func TestFilterSweepCandidatesNarrowsByPatternBeforeProtecting(t *testing.T) {
	closedAt := time.Date(2026, 4, 26, 18, 30, 18, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "keep-1", Status: types.StatusClosed, ClosedAt: &closedAt},
		{ID: "keep-pinned", Status: types.StatusClosed, ClosedAt: &closedAt, Pinned: true},
		{ID: "other-pinned", Status: types.StatusClosed, ClosedAt: &closedAt, Pinned: true},
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{IDPattern: "keep-*"})

	if len(filtered) != 1 || filtered[0].ID != "keep-1" {
		t.Fatalf("filtered IDs = %v, want only keep-1", sweepCandidateIDs(filtered))
	}
	if skips.Pinned != 1 {
		t.Fatalf("skips.Pinned = %d, want 1 — only the pinned row the pattern ADMITTED is a protection", skips.Pinned)
	}
}

func TestMatchesSweepPatternAdmitsEverythingWhenEmpty(t *testing.T) {
	if !MatchesSweepPattern("", "anything-at-all") {
		t.Fatal("an empty pattern must admit every id")
	}
	if MatchesSweepPattern("[bad", "anything") {
		t.Fatal("a malformed pattern must not match; ValidateSweepRequest is what refuses it")
	}
}

// TestNotDoneStatusesForSweepIncludesActiveCustomStatuses is the clause
// issueops.SweepRequest.ProtectReferenced calls required rather than
// best-effort: a workspace's own active status has to protect what it cites.
func TestNotDoneStatusesForSweepIncludesActiveCustomStatuses(t *testing.T) {
	statuses := NotDoneStatusesForSweep([]types.CustomStatus{
		{Name: "reviewing", Category: types.CategoryActive},
		{Name: "shipped", Category: types.CategoryDone},
	})
	found := map[types.Status]bool{}
	for _, s := range statuses {
		found[s] = true
	}
	if !found["reviewing"] {
		t.Error("an active custom status must be scanned for citations")
	}
	if found["shipped"] {
		t.Error("a done custom status must not be scanned: it is not a live bead")
	}
	if !found[types.StatusOpen] || !found[types.StatusInProgress] {
		t.Error("the built-in active statuses must always be scanned")
	}
}

func TestCandidateIDMatcherWordBoundaries(t *testing.T) {
	candidates := map[string]bool{
		"be-ref-001":   true,
		"be-ref-001.1": true,
		"be-ref-002":   true,
	}
	matcher := NewCandidateIDMatcher(candidates)

	found := make(map[string]bool)
	matcher.FindAll("see (be-ref-001), be-ref-001.1 and xbe-ref-002 but not be-ref-002x", found)

	if !found["be-ref-001"] {
		t.Fatal("expected be-ref-001 to match at punctuation boundaries")
	}
	if !found["be-ref-001.1"] {
		t.Fatal("expected be-ref-001.1 to match at punctuation boundaries")
	}
	if found["be-ref-002"] {
		t.Fatal("did not expect be-ref-002 to match inside word boundaries")
	}
}

// TestPartitionSweepReferencedBoundsTheSample pins the published cap: the
// sample is at most issueops.SweepReferencedSampleLimit long while the COUNT
// is the whole protected set, so a caller can tell a truncated sample from a
// complete one.
func TestPartitionSweepReferencedBoundsTheSample(t *testing.T) {
	const protected = issueops.SweepReferencedSampleLimit + 25
	candidates := make([]*types.Issue, 0, protected+1)
	referenced := make(map[string]bool, protected)
	for i := 0; i < protected; i++ {
		id := fmt.Sprintf("be-ref-%03d", i)
		candidates = append(candidates, &types.Issue{ID: id})
		referenced[id] = true
	}
	candidates = append(candidates, &types.Issue{ID: "be-free"})

	kept, count, sample := PartitionSweepReferenced(candidates, referenced)

	if len(kept) != 1 || kept[0].ID != "be-free" {
		t.Fatalf("kept = %v, want only the uncited candidate", sweepCandidateIDs(kept))
	}
	if count != protected {
		t.Fatalf("referenced count = %d, want %d — the COUNT is the whole set", count, protected)
	}
	if len(sample) != issueops.SweepReferencedSampleLimit {
		t.Fatalf("sample length = %d, want the published cap %d", len(sample), issueops.SweepReferencedSampleLimit)
	}
	if sample[0] != "be-ref-000" {
		t.Fatalf("sample[0] = %q, want the first candidate — the sample keeps candidate order", sample[0])
	}
}

// TestCandidateIDMatcherLargeFixture is the NFR-02 budget from be-5sn, moved
// here with the scan itself: 10K bodies of ~5KB each, matched against 100
// candidates, in under five seconds. It is written against the MATCHER,
// because the store round trips it used to include measured the fixture.
func TestCandidateIDMatcherLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large fixture bench")
	}

	const (
		bodyCount          = 10_000
		candidateCount     = 100
		seededRefCount     = 20
		bodyPadding        = 5_000 // bytes of filler per bead
		maxDurationSeconds = 5
	)

	candidates := make(map[string]bool, candidateCount)
	for i := 0; i < candidateCount; i++ {
		candidates[fmt.Sprintf("be-ref-%03d", i)] = true
	}

	seeded := make([]string, seededRefCount)
	i := 0
	for id := range candidates {
		if i >= seededRefCount {
			break
		}
		seeded[i] = id
		i++
	}

	// Padding text that does NOT contain any candidate ID substring.
	pad := strings.Repeat("x", bodyPadding)

	bodies := make([]string, bodyCount)
	seededIdx := 0
	for j := 0; j < bodyCount; j++ {
		bodies[j] = pad
		if seededIdx < seededRefCount && j%500 == 0 {
			bodies[j] = fmt.Sprintf("%s %s %s", pad[:100], seeded[seededIdx], pad[100:])
			seededIdx++
		}
	}

	matcher := NewCandidateIDMatcher(candidates)
	found := make(map[string]bool)
	start := time.Now()
	for _, body := range bodies {
		matcher.FindAll(body, found)
	}
	elapsed := time.Since(start)

	// The race detector adds multi-x overhead that invalidates a wall-clock
	// budget, so only enforce the bound in non-race builds. The correctness
	// assertions below still run under -race.
	if !raceEnabled && elapsed > maxDurationSeconds*time.Second {
		t.Errorf("scan took %v; must complete in <%ds on a %d-body fixture", elapsed, maxDurationSeconds, bodyCount)
	}

	if len(found) != seededIdx {
		t.Errorf("expected %d referenced IDs, got %d", seededIdx, len(found))
	}
	for _, id := range seeded[:seededIdx] {
		if !found[id] {
			t.Errorf("expected %s to be in the reference set but it was not", id)
		}
	}
}

func sweepCandidateIDs(issues []*types.Issue) []string {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids
}

// TestFilterSweepCandidatesProtectsLabeledCandidates is the whole of be-edf at
// the seam every sweep route runs through: a closed ephemeral bead carrying a
// protected label is HELD BACK and counted, not deleted.
//
// The negative control is the load-bearing half. `unlabeled` and `other-label`
// are identical to `protected` in every field the filter reads except the one
// under test, so a guard that protected everything — or that resolved its set
// to "match anything" — fails here rather than passing as a stricter version of
// the same behavior.
func TestFilterSweepCandidatesProtectsLabeledCandidates(t *testing.T) {
	closedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "unlabeled", Status: types.StatusClosed, ClosedAt: &closedAt},
		{ID: "other-label", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"gt:message"}},
		{ID: "protected", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"bd:protected"}},
		{ID: "protected-among-many", Status: types.StatusClosed, ClosedAt: &closedAt,
			Labels: []string{"a", "bd:protected", "z"}},
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{
		ProtectedLabels: []string{"bd:protected"},
	})

	got := sweepCandidateIDs(filtered)
	want := []string{"unlabeled", "other-label"}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered IDs = %v, want %v", got, want)
	}
	if skips.Labeled != 2 {
		t.Errorf("skips.Labeled = %d, want 2", skips.Labeled)
	}
	if skips.Pinned != 0 {
		t.Errorf("skips.Pinned = %d, want 0 — a labeled row is counted as labeled, in exactly one bucket", skips.Pinned)
	}
}

// TestFilterSweepCandidatesWithoutProtectedLabelsProtectsNothing is the
// no-opt-in control: the rows above, unchanged, all sweep when no labels are
// requested. Without it the test above could pass against a filter that held
// back every labeled row regardless of the request.
func TestFilterSweepCandidatesWithoutProtectedLabelsProtectsNothing(t *testing.T) {
	closedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "protected", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"bd:protected"}},
		{ID: "unlabeled", Status: types.StatusClosed, ClosedAt: &closedAt},
	}

	for _, tc := range []struct {
		name string
		req  issueops.SweepRequest
	}{
		{"absent", issueops.SweepRequest{}},
		{"empty slice", issueops.SweepRequest{ProtectedLabels: []string{}}},
		{"only empty strings", issueops.SweepRequest{ProtectedLabels: []string{"", ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filtered, skips := FilterSweepCandidates(candidates, tc.req)
			if len(filtered) != 2 {
				t.Fatalf("filtered IDs = %v, want both", sweepCandidateIDs(filtered))
			}
			if skips.Labeled != 0 {
				t.Errorf("skips.Labeled = %d, want 0", skips.Labeled)
			}
		})
	}
}

// TestFilterSweepCandidatesMatchesLabelsWholeAndExactly pins that the guard is
// not a prefix or substring test. A near-miss label must NOT protect: a guard
// that over-matches silently stops a sweep from ever clearing anything, which
// presents as `bd purge` quietly doing nothing rather than as an error.
func TestFilterSweepCandidatesMatchesLabelsWholeAndExactly(t *testing.T) {
	closedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "prefix", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"bd:protected-ish"}},
		{ID: "suffix", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"x-bd:protected"}},
		{ID: "case", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"BD:PROTECTED"}},
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{
		ProtectedLabels: []string{"bd:protected"},
	})

	if len(filtered) != 3 {
		t.Fatalf("filtered IDs = %v, want all three — matching is whole-label and exact", sweepCandidateIDs(filtered))
	}
	if skips.Labeled != 0 {
		t.Errorf("skips.Labeled = %d, want 0", skips.Labeled)
	}
}

// TestFilterSweepCandidatesNarrowsByPatternBeforeProtectingLabels is the label
// twin of the pinned ordering pin: a labeled row the pattern EXCLUDED was never
// a candidate, so it is not counted as a protection.
func TestFilterSweepCandidatesNarrowsByPatternBeforeProtectingLabels(t *testing.T) {
	closedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "keep-1", Status: types.StatusClosed, ClosedAt: &closedAt},
		{ID: "keep-labeled", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"bd:protected"}},
		{ID: "other-labeled", Status: types.StatusClosed, ClosedAt: &closedAt, Labels: []string{"bd:protected"}},
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{
		IDPattern:       "keep-*",
		ProtectedLabels: []string{"bd:protected"},
	})

	if len(filtered) != 1 || filtered[0].ID != "keep-1" {
		t.Fatalf("filtered IDs = %v, want only keep-1", sweepCandidateIDs(filtered))
	}
	if skips.Labeled != 1 {
		t.Fatalf("skips.Labeled = %d, want 1 — only the labeled row the pattern ADMITTED is a protection", skips.Labeled)
	}
}

// TestFilterSweepCandidatesProtectsLabeledAheadOfTheClosedAtDefenses pins the
// bucket a row lands in when two reasons apply. The protections run first, so a
// labeled row that is ALSO not-closed reports as Labeled — the fact its owner
// needs — and the defense counters stay reserved for rows nothing protected.
// This is the shape Pinned already had.
func TestFilterSweepCandidatesProtectsLabeledAheadOfTheClosedAtDefenses(t *testing.T) {
	candidates := []*types.Issue{
		{ID: "labeled-open", Status: types.StatusOpen, Labels: []string{"bd:protected"}},
		{ID: "labeled-no-closed-at", Status: types.StatusClosed, Labels: []string{"bd:protected"}},
	}

	filtered, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{
		ProtectedLabels: []string{"bd:protected"},
	})

	if len(filtered) != 0 {
		t.Fatalf("filtered IDs = %v, want none", sweepCandidateIDs(filtered))
	}
	if skips.Labeled != 2 {
		t.Errorf("skips.Labeled = %d, want 2", skips.Labeled)
	}
	if skips.NotClosed != 0 || skips.UnknownClosedAt != 0 {
		t.Errorf("defense buckets = NotClosed %d, UnknownClosedAt %d, want 0 — a candidate lands in exactly one bucket",
			skips.NotClosed, skips.UnknownClosedAt)
	}
}

// TestFilterSweepCandidatesPinnedWinsOverLabeled pins the one remaining bucket
// ambiguity: a row that is both pinned and labeled counts ONCE, as Pinned,
// because pinned is checked first. Stated so the counters sum to the candidate
// set rather than double-counting.
func TestFilterSweepCandidatesPinnedWinsOverLabeled(t *testing.T) {
	closedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	candidates := []*types.Issue{
		{ID: "both", Status: types.StatusClosed, ClosedAt: &closedAt, Pinned: true, Labels: []string{"bd:protected"}},
	}

	_, skips := FilterSweepCandidates(candidates, issueops.SweepRequest{
		ProtectedLabels: []string{"bd:protected"},
	})

	if skips.Pinned != 1 || skips.Labeled != 0 {
		t.Fatalf("Pinned = %d, Labeled = %d, want 1 and 0", skips.Pinned, skips.Labeled)
	}
}
