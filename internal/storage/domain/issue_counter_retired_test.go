package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// counterRetiredIssueRepo hands out counter values in order and reports the
// listed ids taken. The embedded interface is nil on purpose: a call this
// test did not intend panics rather than passing quietly.
type counterRetiredIssueRepo struct {
	IssueSQLRepository
	next   int
	taken  map[string]bool
	probed []string
}

func (r *counterRetiredIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterRetiredIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	return r.taken[id], nil
}

// TestCounterModeMintWalksPastRetiredValues pins the uow counter-mode mint: a
// counter value a retaining sweep retired is skipped and the counter advanced
// again, so a fresh create is never handed an id it would be refused.
func TestCounterModeMintWalksPastRetiredValues(t *testing.T) {
	repo := &counterRetiredIssueRepo{next: 4, taken: map[string]bool{"bd-5": true, "bd-6": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextFreeCounterID() = %v", err)
	}
	if id != "bd-7" {
		t.Fatalf("nextFreeCounterID() = %q, want bd-7 past the retired bd-5 and bd-6", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-5,bd-6,bd-7" {
		t.Fatalf("probed %s, want bd-5,bd-6,bd-7", got)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound: a counter that
// keeps landing on retired values fails loudly instead of walking forever.
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	repo := &counterRetiredIssueRepo{taken: map[string]bool{}}
	for n := 1; n <= maxCounterSkips+1; n++ {
		repo.taken[fmt.Sprintf("bd-%d", n)] = true
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextFreeCounterID(t.Context(), "bd", nil); err == nil {
		t.Fatalf("nextFreeCounterID() = %q, nil; want the skip-bound error", id)
	}
}

// TestCounterModeMintWalksPastClaimedValues pins the batch half of the
// counter-mode mint: a value the caller's batch claimed (an explicit id a
// later item names) is skipped like a taken one, without the probe.
func TestCounterModeMintWalksPastClaimedValues(t *testing.T) {
	repo := &counterRetiredIssueRepo{next: 4, taken: map[string]bool{}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", map[string]bool{"bd-5": true})
	if err != nil {
		t.Fatalf("nextFreeCounterID() = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("nextFreeCounterID() = %q, want bd-6 past the claimed bd-5", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-6" {
		t.Fatalf("probed %s, want only bd-6: a claimed value costs no probe", got)
	}
}

// mintProbeIssueRepo is a self-contained probe recorder for the counter mint:
// it answers Taken from its map and records every Taken and Retired call, so
// a test can pin WHICH probe decides a candidate, not only the outcome.
type mintProbeIssueRepo struct {
	IssueSQLRepository
	next          int
	taken         map[string]bool
	probedTaken   []string
	probedRetired []string
}

func (r *mintProbeIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *mintProbeIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	r.probedTaken = append(r.probedTaken, id)
	return r.taken[id], nil
}

func (r *mintProbeIssueRepo) Retired(_ context.Context, id string) (bool, error) {
	r.probedRetired = append(r.probedRetired, id)
	return false, nil
}

// mintProbeCfgRepo answers the two config reads mintTopLevelID makes.
type mintProbeCfgRepo struct {
	ConfigSQLRepository
	prefix string
}

func (r mintProbeCfgRepo) GetConfig(_ context.Context, key string) (string, error) {
	switch key {
	case "issue_prefix":
		return r.prefix, nil
	case "issue_id_mode":
		return "counter", nil
	}
	return "", nil
}

// TestCounterModeMintProbesTakenNotRetired pins the domain counter walk's
// probe (GH#8456): every unclaimed candidate is decided by the one
// both-planes Taken read, never by Retired — a tombstone-only read cannot see
// the live row ahead of the counter — so a value a live row holds is passed
// over.
func TestCounterModeMintProbesTakenNotRetired(t *testing.T) {
	repo := &mintProbeIssueRepo{next: 4, taken: map[string]bool{"bd-5": true}}
	u := &issueUseCaseImpl{issueRepo: repo, cfgRepo: mintProbeCfgRepo{prefix: "bd"}}
	id, err := u.mintTopLevelID(t.Context(), &types.Issue{Title: "live row ahead", IssueType: types.TypeTask, Priority: 2}, "tester", false, nil)
	if err != nil {
		t.Fatalf("mintTopLevelID() = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("mintTopLevelID() = %q, want bd-6, past the live bd-5", id)
	}
	if got := strings.Join(repo.probedTaken, ","); got != "bd-5,bd-6" {
		t.Fatalf("Taken probed %s, want bd-5,bd-6, one probe per unclaimed candidate", got)
	}
	if len(repo.probedRetired) != 0 {
		t.Fatalf("Retired probed %v, want none: the counter walk decides through Taken", repo.probedRetired)
	}
}
