package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
