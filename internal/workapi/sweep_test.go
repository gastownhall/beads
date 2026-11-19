package workapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

	filtered, skips := FilterSweepCandidates(candidates, "", &cutoff)

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

	filtered, skips := FilterSweepCandidates(candidates, "", nil)

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

	filtered, skips := FilterSweepCandidates(candidates, "keep-*", nil)

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

func TestValidateSweepRequestGatesTheWispsPlane(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := ValidateSweepRequest(issueops.SweepRequest{Tier: issueops.SweepWispsPlane}); !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("unfiltered wisps-plane sweep error = %v, want ErrValidation: it reaches no-history (durable-tier) rows", err)
	}
	for _, req := range []issueops.SweepRequest{
		{Tier: issueops.SweepWispsPlane, ClosedBefore: &cutoff},
		{Tier: issueops.SweepWispsPlane, IDPattern: "*"},
	} {
		if err := ValidateSweepRequest(req); err != nil {
			t.Errorf("ValidateSweepRequest(%+v) = %v, want nil", req, err)
		}
	}
}

func TestBuildSweepCandidateFilterForTheWispsPlaneHasNoTierConstraint(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	req := issueops.SweepRequest{Tier: issueops.SweepWispsPlane, ClosedBefore: &cutoff}
	if !SweepSearchesWispsPlaneOnly(req) {
		t.Fatal("SweepSearchesWispsPlaneOnly(wisps-plane) = false; the plane IS the selection")
	}
	filter := BuildSweepCandidateFilter(req)
	if filter.Status == nil || *filter.Status != types.StatusClosed {
		t.Fatalf("Status = %v, want closed", filter.Status)
	}
	if filter.EphemeralTier != nil || filter.Ephemeral != nil {
		t.Fatalf("wisps-plane filter carries a flag constraint (EphemeralTier=%v, Ephemeral=%v); "+
			"that would drop no-history rows", filter.EphemeralTier, filter.Ephemeral)
	}
	if filter.ClosedBefore == nil || !filter.ClosedBefore.Equal(cutoff) || filter.ClosedBefore == &cutoff {
		t.Fatalf("ClosedBefore = %v, want a copy of %v", filter.ClosedBefore, cutoff)
	}
	for _, tier := range []issueops.SweepTier{issueops.SweepEphemeral, issueops.SweepDurable} {
		if SweepSearchesWispsPlaneOnly(issueops.SweepRequest{Tier: tier}) {
			t.Errorf("SweepSearchesWispsPlaneOnly(%s) = true; tier queries merge both planes", tier)
		}
	}
}

func TestSweepLiveDependentsCountOnlyProtectingEdgesFromLiveSources(t *testing.T) {
	dep := func(source, target string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: source, DependsOnID: target, Type: typ}
	}
	incoming := map[string][]*types.Dependency{
		"root":    {dep("step-open", "root", types.DepParentChild), dep("step-done", "root", types.DepParentChild)},
		"tracked": {dep("convoy", "tracked", types.DepTracks)},
		"gate":    {dep("waiter", "gate", types.DepBlocks)},
		"linked":  {dep("reader", "linked", types.DepRelated)},
		"orphan":  {dep("step-done", "orphan", types.DepParentChild), nil},
	}

	sources := SweepLiveDependentSources(incoming)
	if want := []string{"convoy", "step-done", "step-open", "waiter"}; strings.Join(sources, ",") != strings.Join(want, ",") {
		t.Fatalf("SweepLiveDependentSources = %v, want %v (sorted, protecting edges only)", sources, want)
	}

	live := map[string]bool{"step-open": true, "convoy": true, "waiter": true, "reader": true}
	protected := SweepLiveDependentTargets(incoming, live)
	for _, id := range []string{"root", "tracked", "gate"} {
		if !protected[id] {
			t.Errorf("%s not protected; a live source depends on it through a protecting edge", id)
		}
	}
	for _, id := range []string{"linked", "orphan"} {
		if protected[id] {
			t.Errorf("%s protected; its only live source is a non-protecting edge or none is live", id)
		}
	}

	candidates := []*types.Issue{{ID: "root"}, {ID: "linked"}, {ID: "tracked"}, {ID: "orphan"}}
	kept, count := PartitionSweepLiveDependents(candidates, protected)
	if count != 2 || len(kept) != 2 || kept[0].ID != "linked" || kept[1].ID != "orphan" {
		t.Fatalf("PartitionSweepLiveDependents = %d kept %v, count %d; want [linked orphan] and 2", len(kept), kept, count)
	}
}

func TestSweepLiveDependentTargetsPropagatesThroughProtectedCandidates(t *testing.T) {
	dep := func(source, target string) *types.Dependency {
		return &types.Dependency{IssueID: source, DependsOnID: target, Type: types.DepParentChild}
	}
	incoming := map[string][]*types.Dependency{
		"root":    {dep("middle", "root")},
		"middle":  {dep("leaf", "middle")},
		"cycle-a": {dep("cycle-b", "cycle-a")},
		"cycle-b": {dep("cycle-a", "cycle-b")},
	}

	protected := SweepLiveDependentTargets(incoming, map[string]bool{"leaf": true})
	for _, id := range []string{"middle", "root"} {
		if !protected[id] {
			t.Errorf("%s not protected; it is transitively depended on by a live source", id)
		}
	}
	for _, id := range []string{"cycle-a", "cycle-b"} {
		if protected[id] {
			t.Errorf("%s protected without a live path into its closed cycle", id)
		}
	}
}

func TestValidateSweepRequestRefusesANegativeLimit(t *testing.T) {
	err := ValidateSweepRequest(issueops.SweepRequest{Tier: issueops.SweepEphemeral, Limit: -1})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("negative Limit error = %v, want ErrValidation", err)
	}
	if err := ValidateSweepRequest(issueops.SweepRequest{Tier: issueops.SweepEphemeral, Limit: 0}); err != nil {
		t.Fatalf("zero Limit (no cap) error = %v, want nil", err)
	}
}

func TestLimitSweepCandidatesTakesTheOldestClosedFirst(t *testing.T) {
	at := func(day int) *time.Time {
		ts := time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)
		return &ts
	}
	candidates := []*types.Issue{
		{ID: "c", ClosedAt: at(3)},
		{ID: "b2", ClosedAt: at(1)},
		{ID: "a", ClosedAt: at(2)},
		{ID: "b1", ClosedAt: at(1)},
	}

	kept, remaining := LimitSweepCandidates(candidates, 3)
	var got []string
	for _, issue := range kept {
		got = append(got, issue.ID)
	}
	if strings.Join(got, ",") != "b1,b2,a" || remaining != 1 {
		t.Fatalf("LimitSweepCandidates(3) = %v, remaining %d; want [b1 b2 a] (closed_at, then id) and 1", got, remaining)
	}
	if candidates[0].ID != "c" {
		t.Fatal("LimitSweepCandidates reordered the caller's slice")
	}
	for _, limit := range []int{0, 4, 10} {
		if kept, remaining := LimitSweepCandidates(candidates, limit); len(kept) != 4 || remaining != 0 {
			t.Errorf("LimitSweepCandidates(%d) kept %d, remaining %d; want all 4 and 0", limit, len(kept), remaining)
		}
	}
}

// TestRetainedTypesFromSettingParsesBothForms pins the two spellings the
// types.retained setting takes — the JSON array `bd config set` writes for
// list keys and the legacy comma-separated form — and that an unset value
// retains nothing.
func TestRetainedTypesFromSettingParsesBothForms(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  []string
	}{
		{"unset", "", nil},
		{"blank", "   ", nil},
		{"csv", "op, fact ,,op", []string{"op", "fact"}},
		{"json", `["op"," fact ",""]`, []string{"op", "fact"}},
		{"unknown names are kept", "not-a-registered-type", []string{"not-a-registered-type"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := RetainedTypesFromSetting(test.value)
			if len(got) != len(test.want) {
				t.Fatalf("RetainedTypesFromSetting(%q) = %v, want %v", test.value, got, test.want)
			}
			for _, name := range test.want {
				if !got[name] {
					t.Errorf("RetainedTypesFromSetting(%q) = %v, missing %q", test.value, got, name)
				}
			}
		})
	}
}

// TestRetainedTypesFromSettingCanonicalizes pins the one canonicalizing read
// of types.retained: each stored name protects itself, its canonical form under
// both alias tables, and its lower-case form when that is a built-in type. So
// a value an older server, an older client or `bd sql` stored with an alias or
// a case variant still protects the type rows actually carry, and a custom
// type is matched verbatim. A name the write check would refuse (`[op]`,
// `"op"`, `op fact`) also protects each fragment of it, read the same way, so
// a value stored past that check still protects what it names. The want sets
// are exact: the read may over-protect by these forms and by nothing else.
func TestRetainedTypesFromSettingCanonicalizes(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  []string
	}{
		{"a built-in matches itself", "chore", []string{"chore"}},
		{"the Normalize alias table", "feat", []string{"feat", "feature"}},
		{"an alias in another case", "Feat", []string{"Feat", "feature"}},
		{"an alias only Normalize knows", "investigation", []string{"investigation", "spike"}},
		{"an alias only NormalizeIssueType knows", "mol", []string{"mol", "molecule"}},
		{"the other one", "MR", []string{"MR", "merge-request"}},
		{"the tables agree", "adr", []string{"adr", "decision"}},
		{"a built-in in another case", "Chore", []string{"Chore", "chore"}},
		{"upper case", "TASK", []string{"TASK", "task"}},
		{"the event built-in", "Event", []string{"Event", "event"}},
		{"a custom type is verbatim", "op", []string{"op"}},
		{"a mixed-case custom type is verbatim", "OpRecord", []string{"OpRecord"}},
		{"the union of every name", `["op","feat","Bug"]`, []string{"op", "feat", "feature", "Bug", "bug"}},
		{"the comma form unions too", "fact, mol", []string{"fact", "mol", "molecule"}},
		{"a bracketed name protects the name inside", "[op]", []string{"[op]", "op"}},
		{"a quoted name protects the name inside", `"op"`, []string{`"op"`, "op"}},
		{"a spaced name protects each word", "op fact", []string{"op fact", "op", "fact"}},
		{"a truncated JSON array protects its names", `["op","fact"`, []string{`["op"`, "op", `"fact"`, "fact"}},
		{"a fragment is read canonically too", "[Feat]", []string{"[Feat]", "Feat", "feature"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := RetainedTypesFromSetting(test.value)
			want := make(map[string]bool, len(test.want))
			for _, name := range test.want {
				want[name] = true
			}
			if !maps.Equal(got, want) {
				t.Errorf("RetainedTypesFromSetting(%q) = %v, want %v", test.value, got, want)
			}
		})
	}
}

// TestPartitionSweepRetainedTypesMatchesTheCanonicalRead pins that the
// partition matches rows against the canonical read rather than the stored
// spelling: a stored `feat` or `Chore` holds back the `feature` and `chore`
// rows bd actually writes.
func TestPartitionSweepRetainedTypesMatchesTheCanonicalRead(t *testing.T) {
	feature := &types.Issue{ID: "bd-f", IssueType: types.TypeFeature}
	chore := &types.Issue{ID: "bd-c", IssueType: types.TypeChore}
	task := &types.Issue{ID: "bd-t", IssueType: types.TypeTask}
	kept, held := PartitionSweepRetainedTypes([]*types.Issue{feature, chore, task}, RetainedTypesFromSetting("feat,Chore"))
	if held != 2 || len(kept) != 1 || kept[0] != task {
		t.Errorf("PartitionSweepRetainedTypes(feat,Chore) kept %v, held %d; want only %s kept and 2 held", kept, held, task.ID)
	}
}

// TestPartitionSweepRetainedTypes is the option C partition: a candidate of a
// retained type is held back and counted. A retaining sweep never calls it —
// the bodies skip the read under RetainIDs — so it has no RetainIDs input.
func TestPartitionSweepRetainedTypes(t *testing.T) {
	op := &types.Issue{ID: "be-op", IssueType: types.IssueType("op")}
	task := &types.Issue{ID: "be-task", IssueType: types.TypeTask}
	fact := &types.Issue{ID: "be-fact", IssueType: types.IssueType("fact")}
	candidates := []*types.Issue{op, task, fact}

	for _, test := range []struct {
		name     string
		retained map[string]bool
		wantKept []string
		wantHeld int
	}{
		{"nothing retained", nil, []string{"be-op", "be-task", "be-fact"}, 0},
		{"retained types are held back", map[string]bool{"op": true, "fact": true}, []string{"be-task"}, 2},
		{"an unknown name matches nothing", map[string]bool{"never-registered": true}, []string{"be-op", "be-task", "be-fact"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			kept, held := PartitionSweepRetainedTypes(candidates, test.retained)
			if held != test.wantHeld {
				t.Errorf("held = %d, want %d", held, test.wantHeld)
			}
			var got []string
			for _, issue := range kept {
				got = append(got, issue.ID)
			}
			if strings.Join(got, ",") != strings.Join(test.wantKept, ",") {
				t.Errorf("kept = %v, want %v (candidate order preserved)", got, test.wantKept)
			}
		})
	}
	if len(candidates) != 3 || candidates[0] != op {
		t.Errorf("PartitionSweepRetainedTypes reordered or truncated its input: %v", candidates)
	}
}

// scriptedSweepGraph answers the resolver's callbacks from a fixed edge map
// and liveness set, recording every id each callback was asked about, so the
// tests assert the traversal's SHAPE — one batch per frontier, each id at most
// once — rather than only its answer.
type scriptedSweepGraph struct {
	incoming map[string][]*types.Dependency
	live     map[string]bool
	fetches  [][]string
	scans    [][]string
}

func (g *scriptedSweepGraph) fetch(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	g.fetches = append(g.fetches, append([]string(nil), ids...))
	out := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		if deps := g.incoming[id]; len(deps) > 0 {
			out[id] = append(out[id], deps...)
		}
	}
	return out, nil
}

func (g *scriptedSweepGraph) scan(_ context.Context, ids []string) (map[string]bool, error) {
	g.scans = append(g.scans, append([]string(nil), ids...))
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if g.live[id] {
			out[id] = true
		}
	}
	return out, nil
}

func (g *scriptedSweepGraph) resolve(t *testing.T, candidates []string) map[string]bool {
	t.Helper()
	protected, err := ResolveSweepLiveDependents(context.Background(), candidates, g.fetch, g.scan)
	if err != nil {
		t.Fatalf("ResolveSweepLiveDependents: %v", err)
	}
	return protected
}

// batchIDs flattens the recorded batches, keeping order, for "each id at most
// once" and "which level asked about which id" assertions.
func (g *scriptedSweepGraph) batchIDs() []string {
	var ids []string
	for _, batch := range g.fetches {
		ids = append(ids, batch...)
	}
	return ids
}

func sweepDep(source, target string) *types.Dependency {
	return &types.Dependency{IssueID: source, DependsOnID: target, Type: types.DepParentChild}
}

func TestResolveSweepLiveDependents(t *testing.T) {
	// The chain R ← S1 ← S2 with S1 closed and unselected and S2 open is the
	// reported shape (#7043): the candidates' own edge read finds S1, the
	// liveness scan finds it closed, and only a SECOND edge read — the one
	// the callers used not to make — finds the live S2 that protects R.
	t.Run("protects through an unselected closed middle", func(t *testing.T) {
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"r":  {sweepDep("s1", "r")},
				"s1": {sweepDep("s2", "s1")},
			},
			live: map[string]bool{"s2": true},
		}
		protected := g.resolve(t, []string{"r"})
		if len(protected) != 1 || !protected["r"] {
			t.Errorf("protected = %v; want exactly r — s1 is protected by the walk but "+
				"is no candidate and must not appear", protected)
		}
		if len(g.fetches) != 2 {
			t.Errorf("fetch batches = %v; want r then s1", g.fetches)
		}
	})

	t.Run("empty candidates read nothing", func(t *testing.T) {
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{"r": {sweepDep("s1", "r")}},
			live:     map[string]bool{"s1": true},
		}
		protected := g.resolve(t, nil)
		if len(protected) != 0 {
			t.Errorf("protected = %v; want empty", protected)
		}
		if len(g.fetches) != 0 || len(g.scans) != 0 {
			t.Errorf("fetches = %v, scans = %v; want no reads at all", g.fetches, g.scans)
		}
	})

	t.Run("protects through two unselected middles and prunes live sources", func(t *testing.T) {
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"r":  {sweepDep("m1", "r")},
				"m1": {sweepDep("m2", "m1")},
				"m2": {sweepDep("leaf", "m2")},
				// The live leaf's own sources are irrelevant — its path
				// already protects — and must not be read.
				"leaf": {sweepDep("never-fetched", "leaf")},
			},
			live: map[string]bool{"leaf": true},
		}
		protected := g.resolve(t, []string{"r"})
		if len(protected) != 1 || !protected["r"] {
			t.Errorf("protected = %v; want exactly r", protected)
		}
		for _, id := range g.batchIDs() {
			if id == "never-fetched" {
				t.Errorf("fetched %s; a live source's own incoming edges are not read", id)
			}
		}
	})

	t.Run("retains exactly when a live source reaches the candidate", func(t *testing.T) {
		g := &scriptedSweepGraph{
			// A chain with no live row anywhere: discovery crosses m1 and m2
			// (both not live), finds no further sources, and protects nothing.
			incoming: map[string][]*types.Dependency{
				"r":  {sweepDep("m1", "r")},
				"m1": {sweepDep("m2", "m1")},
			},
			live: map[string]bool{},
		}
		if protected := g.resolve(t, []string{"r"}); len(protected) != 0 {
			t.Errorf("protected = %v; want empty — no live source reaches r", protected)
		}

		// A related edge from a live row is a link, not structure: it must not
		// protect, and the resolver must not even scan its source.
		related := &types.Dependency{IssueID: "relater", DependsOnID: "r2", Type: types.DepRelated}
		waits := &types.Dependency{IssueID: "waiter", DependsOnID: "r2", Type: types.DepWaitsFor}
		g2 := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{"r2": {related, waits}},
			live:     map[string]bool{"relater": true, "waiter": true},
		}
		if protected := g2.resolve(t, []string{"r2"}); len(protected) != 0 {
			t.Errorf("protected = %v; want empty — related and waits-for edges do not protect", protected)
		}
		if len(g2.scans) != 0 {
			t.Errorf("scans = %v; want none — no protecting edge reached a source", g2.scans)
		}

		// The same graph with a protecting edge from a live row does protect.
		g3 := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{"r3": {{
				IssueID: "convoy", DependsOnID: "r3", Type: types.DepTracks,
			}}},
			live: map[string]bool{"convoy": true},
		}
		if protected := g3.resolve(t, []string{"r3"}); len(protected) != 1 || !protected["r3"] {
			t.Errorf("protected = %v; want exactly r3 — a tracks edge from a live row protects", protected)
		}
	})

	t.Run("expands a source the liveness scan cannot see", func(t *testing.T) {
		// A missing (or done-custom-status) source answers the scan with
		// not-live; the resolver takes that answer as eligibility for the next
		// frontier rather than applying a second existence policy.
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{"r": {sweepDep("ghost", "r")}},
			live:     map[string]bool{},
		}
		if protected := g.resolve(t, []string{"r"}); len(protected) != 0 {
			t.Errorf("protected = %v; want empty", protected)
		}
		if len(g.scans) != 1 || len(g.scans[0]) != 1 || g.scans[0][0] != "ghost" {
			t.Errorf("scans = %v; want exactly [ghost]", g.scans)
		}
		if len(g.fetches) != 2 {
			t.Errorf("fetch batches = %v; want r then ghost", g.fetches)
		}
	})

	t.Run("terminates on a closed cycle", func(t *testing.T) {
		// a ← b ← a is a cycle among non-live rows; with no live path into it
		// nothing is protected, and each id is read at most once.
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"r": {sweepDep("a", "r")},
				"a": {sweepDep("b", "a")},
				"b": {sweepDep("a", "b")},
			},
			live: map[string]bool{},
		}
		if protected := g.resolve(t, []string{"r"}); len(protected) != 0 {
			t.Errorf("protected = %v; want empty — no live source reaches the cycle", protected)
		}
		seen := make(map[string]int)
		for _, id := range g.batchIDs() {
			seen[id]++
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("id %s fetched %d times; the known set must read each id once", id, n)
			}
		}

		// With a live row beyond the cycle, protection crosses it to the root.
		g2 := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"r": {sweepDep("a", "r")},
				"a": {sweepDep("b", "a")},
				"b": {sweepDep("a", "b"), sweepDep("leaf", "b")},
			},
			live: map[string]bool{"leaf": true},
		}
		protected := g2.resolve(t, []string{"r"})
		if len(protected) != 1 || !protected["r"] {
			t.Errorf("protected = %v; want exactly r — the live leaf protects through the cycle", protected)
		}
	})

	t.Run("deduplicates candidates and duplicate edges", func(t *testing.T) {
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"r": {sweepDep("s1", "r"), sweepDep("s1", "r")},
			},
			live: map[string]bool{"s1": true},
		}
		protected := g.resolve(t, []string{"r", "r"})
		if len(protected) != 1 || !protected["r"] {
			t.Errorf("protected = %v; want exactly r", protected)
		}
		if len(g.fetches) != 1 || len(g.fetches[0]) != 1 {
			t.Errorf("fetch batches = %v; want one batch with one id", g.fetches)
		}
		if len(g.scans) != 1 || len(g.scans[0]) != 1 {
			t.Errorf("scans = %v; want one scan with one id", g.scans)
		}
	})

	t.Run("protects each of several candidates independently", func(t *testing.T) {
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"kept":  {sweepDep("live-1", "kept")},
				"swept": {sweepDep("closed-1", "swept")},
				"both":  {sweepDep("live-2", "both"), sweepDep("closed-2", "both")},
			},
			live: map[string]bool{"live-1": true, "live-2": true},
		}
		protected := g.resolve(t, []string{"kept", "swept", "both"})
		if len(protected) != 2 || !protected["kept"] || !protected["both"] {
			t.Errorf("protected = %v; want kept and both", protected)
		}
	})
}

func TestResolveSweepLiveDependentsBatchesByFrontier(t *testing.T) {
	t.Run("a wide frontier is one fetch per level", func(t *testing.T) {
		// Two candidates share one source: the shared source must be
		// discovered once, in ONE liveness scan with the frontier's other
		// fresh sources — not once per candidate that reaches it — and the
		// not-live sources are the one next edge read.
		g := &scriptedSweepGraph{
			incoming: map[string][]*types.Dependency{
				"r1": {sweepDep("shared", "r1"), sweepDep("own1", "r1")},
				"r2": {sweepDep("shared", "r2"), sweepDep("own2", "r2")},
			},
			live: map[string]bool{"shared": true},
		}
		protected := g.resolve(t, []string{"r1", "r2"})
		if len(protected) != 2 || !protected["r1"] || !protected["r2"] {
			t.Errorf("protected = %v; want r1 and r2", protected)
		}
		if fmt.Sprint(g.fetches) != "[[r1 r2] [own1 own2]]" {
			t.Errorf("fetch batches = %v; want one batch per frontier: [r1 r2] then [own1 own2]",
				g.fetches)
		}
		if fmt.Sprint(g.scans) != "[[own1 own2 shared]]" {
			t.Errorf("scan batches = %v; want ONE scan with the 3 unique sources", g.scans)
		}
	})

	t.Run("a deep chain reaches its live leaf with no depth cap", func(t *testing.T) {
		const depth = 50
		incoming := make(map[string][]*types.Dependency, depth+1)
		chain := make([]string, 0, depth+2)
		root := "root"
		chain = append(chain, root)
		for i := 0; i < depth; i++ {
			next := fmt.Sprintf("m%02d", i)
			incoming[chain[len(chain)-1]] = []*types.Dependency{sweepDep(next, chain[len(chain)-1])}
			chain = append(chain, next)
		}
		leaf := "leaf"
		incoming[chain[len(chain)-1]] = []*types.Dependency{sweepDep(leaf, chain[len(chain)-1])}
		g := &scriptedSweepGraph{incoming: incoming, live: map[string]bool{leaf: true}}

		protected := g.resolve(t, []string{root})
		if len(protected) != 1 || !protected[root] {
			t.Fatalf("protected = %v; want exactly root — a %d-level chain needs no depth cap",
				protected, depth)
		}
		if len(g.fetches) != depth+1 {
			t.Errorf("fetch batches = %d; want %d (root plus each middle once)", len(g.fetches), depth+1)
		}
		seen := make(map[string]int)
		for _, id := range g.batchIDs() {
			seen[id]++
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("id %s fetched %d times; the known set must read each id once", id, n)
			}
		}
		for _, id := range g.batchIDs() {
			if id == leaf {
				t.Errorf("the live leaf was fetched; live sources are never expanded")
			}
		}
	})
}
