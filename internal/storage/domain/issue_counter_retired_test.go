package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// counterHeldIssueRepo hands out counter values in order and models the two
// id probes as the SQL repository answers them: Taken is a live row in either
// plane or a tombstone, Retired a tombstone only. Each probe is recorded,
// Retired ones as "retired:<id>", so a test can tell which one decided a
// candidate. The embedded interface is nil on purpose: a call this test did
// not intend panics rather than passing quietly.
type counterHeldIssueRepo struct {
	IssueSQLRepository
	next    int
	live    map[string]bool
	retired map[string]bool
	probed  []string
}

func (r *counterHeldIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterHeldIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	return r.live[id] || r.retired[id], nil
}

func (r *counterHeldIssueRepo) Retired(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, "retired:"+id)
	return r.retired[id], nil
}

// TestCounterModeMintWalksPastRetiredValues pins the uow counter-mode mint: a
// counter value a retaining sweep retired is skipped and the counter advanced
// again, so a fresh create is never handed an id it would be refused.
func TestCounterModeMintWalksPastRetiredValues(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 4, retired: map[string]bool{"bd-5": true, "bd-6": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextFreeCounterID() = %v", err)
	}
	if id != "bd-7" {
		t.Fatalf("nextFreeCounterID() = %q, want bd-7 past the retired bd-5 and bd-6", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-5,bd-6,bd-7" {
		t.Fatalf("probed %s, want one Taken probe each for bd-5,bd-6,bd-7", got)
	}
}

// TestCounterModeMintWalksPastLiveRows pins GH#8456 at the uow mint: a
// counter value a live row holds (an explicit, imported or wisp id, none of
// which move the counter) is skipped like a retired one, decided by the one
// both-planes Taken probe, so the create is neither handed an id it would
// overwrite nor one it would be refused.
func TestCounterModeMintWalksPastLiveRows(t *testing.T) {
	repo := &counterHeldIssueRepo{
		next:    4,
		live:    map[string]bool{"bd-5": true, "bd-7": true},
		retired: map[string]bool{"bd-6": true},
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", map[string]bool{"bd-8": true})
	if err != nil {
		t.Fatalf("nextFreeCounterID() = %v", err)
	}
	if id != "bd-9" {
		t.Fatalf("nextFreeCounterID() = %q, want bd-9 past the live bd-5 and bd-7, the retired bd-6 and the claimed bd-8", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-5,bd-6,bd-7,bd-9" {
		t.Fatalf("probed %s, want one Taken probe each for bd-5,bd-6,bd-7,bd-9 and none for the claimed bd-8", got)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound: a counter that
// keeps landing on held values fails loudly instead of walking forever.
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	repo := &counterHeldIssueRepo{live: map[string]bool{}}
	for n := 1; n <= maxCounterSkips+1; n++ {
		repo.live[fmt.Sprintf("bd-%d", n)] = true
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", nil)
	if err == nil || !strings.Contains(err.Error(), "taken or claimed") {
		t.Fatalf("nextFreeCounterID() = %q, %v; want the skip-bound error", id, err)
	}
}

// TestCounterModeMintWalksPastClaimedValues pins the batch half of the
// counter-mode mint: a value the caller's batch claimed (an explicit id a
// later item names) is skipped like a taken one, without the taken read.
func TestCounterModeMintWalksPastClaimedValues(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 4}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", map[string]bool{"bd-5": true})
	if err != nil {
		t.Fatalf("nextFreeCounterID() = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("nextFreeCounterID() = %q, want bd-6 past the claimed bd-5", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-6" {
		t.Fatalf("probed %s, want only bd-6: a claimed value costs no taken read", got)
	}
}
