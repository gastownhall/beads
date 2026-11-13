package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// counterHeldIssueRepo hands out counter values in order and models the held
// id reads as the SQL repository answers them: Taken is a live row in either
// plane or a tombstone, and AdvanceCounterPastHeld moves the counter over the
// run of values above it that a live row, a tombstone or claimed holds. hidden
// ids are held where only Taken sees them: a probe the one-read skip disagrees
// with. Each Taken probe is recorded, and each skip as "advance", so a test
// can tell what decided a candidate. The embedded interface is nil on purpose:
// a call this test did not intend panics rather than passing quietly.
type counterHeldIssueRepo struct {
	IssueSQLRepository
	next    int
	live    map[string]bool
	retired map[string]bool
	hidden  map[string]bool
	probed  []string
}

func (r *counterHeldIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterHeldIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	return r.live[id] || r.retired[id] || r.hidden[id], nil
}

func (r *counterHeldIssueRepo) AdvanceCounterPastHeld(_ context.Context, prefix string, claimed map[string]bool) error {
	r.probed = append(r.probed, "advance")
	for {
		id := fmt.Sprintf("%s-%d", prefix, r.next+1)
		if !r.live[id] && !r.retired[id] && !claimed[id] {
			return nil
		}
		r.next++
	}
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

// TestCounterModeMintSkipsAHeldRunLongerThanTheWalk pins the long lag of
// GH#8456 at the uow mint: more held values in a row than one walk probes, as
// a bulk import ahead of an existing counter leaves. Giving up at the bound
// failed every auto create for good, because the rollback undoes the bumps.
// The mint skips the rest of the run in one AdvanceCounterPastHeld read
// instead and goes on from the first free value. It passes the claimed set to
// that read: the claimed bd-1201..bd-2300 alone outlast a second walk.
func TestCounterModeMintSkipsAHeldRunLongerThanTheWalk(t *testing.T) {
	repo := &counterHeldIssueRepo{live: map[string]bool{}}
	claimed := map[string]bool{}
	for n := 1; n <= 2300; n++ {
		id := fmt.Sprintf("bd-%d", n)
		if n <= 1200 {
			repo.live[id] = true
		} else {
			claimed[id] = true
		}
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", claimed)
	if err != nil {
		t.Fatalf("nextFreeCounterID() = %v", err)
	}
	if id != "bd-2301" {
		t.Fatalf("nextFreeCounterID() = %q, want bd-2301 past the live bd-1..bd-1200 and the claimed bd-1201..bd-2300", id)
	}
	want := make([]string, 0, maxCounterSkips+3)
	for n := 1; n <= maxCounterSkips+1; n++ {
		want = append(want, fmt.Sprintf("bd-%d", n))
	}
	want = append(want, "advance", "bd-2301")
	if got := strings.Join(repo.probed, ","); got != strings.Join(want, ",") {
		t.Fatalf("probed %s, want one walk of %d probes, one advance, then bd-2301", got, maxCounterSkips+1)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound that still ends
// the walk: a counter that keeps landing on held values the one-read skip does
// not see walks one more bound and fails loudly instead of walking forever.
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	repo := &counterHeldIssueRepo{live: map[string]bool{}, hidden: map[string]bool{}}
	for n := 1; n <= maxCounterSkips+1; n++ {
		repo.live[fmt.Sprintf("bd-%d", n)] = true
		repo.hidden[fmt.Sprintf("bd-%d", maxCounterSkips+1+n)] = true
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextFreeCounterID(t.Context(), "bd", nil)
	if err == nil || !strings.Contains(err.Error(), "taken or claimed") {
		t.Fatalf("nextFreeCounterID() = %q, %v; want the skip-bound error", id, err)
	}
	if got, want := len(repo.probed), 2*(maxCounterSkips+1)+1; got != want {
		t.Fatalf("%d probes and skips, want %d: two walks of %d and one advance", got, want, maxCounterSkips+1)
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
