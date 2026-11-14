package domain

import (
	"context"
	"strings"
	"testing"
)

// counterRetiredIssueRepo hands out counter values in order, reports the ids
// held in the id space, and models the counter jump RaiseCounterPastHeldIDs
// performs by moving its counter to raiseTo. The embedded interface is nil on
// purpose: a call this test did not intend panics rather than passing quietly.
type counterRetiredIssueRepo struct {
	IssueSQLRepository
	next    int
	held    map[string]bool
	allHeld bool
	raiseTo int
	raises  []string
	probed  []string
}

func (r *counterRetiredIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterRetiredIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	return r.allHeld || r.held[id], nil
}

func (r *counterRetiredIssueRepo) RaiseCounterPastHeldIDs(_ context.Context, prefix string) error {
	r.raises = append(r.raises, prefix)
	r.next = r.raiseTo
	return nil
}

// TestCounterModeMintJumpsPastHeldValues pins the uow counter-mode mint: a
// counter value the id space holds — a live row of either plane, or one a
// sweep retired — is taken, and the first taken candidate jumps the counter
// past the highest held suffix in one scan, so the mint lands one past it
// instead of on it.
func TestCounterModeMintJumpsPastHeldValues(t *testing.T) {
	repo := &counterRetiredIssueRepo{next: 1, held: map[string]bool{"bd-2": true, "bd-100": true}, raiseTo: 100}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-101" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-101, one past the highest held suffix", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-2,bd-101" {
		t.Fatalf("probed %s, want bd-2,bd-101", got)
	}
	if got := strings.Join(repo.raises, ","); got != "bd" {
		t.Fatalf("raised %s, want one jump for bd", got)
	}
}

// TestCounterModeMintJumpsOnlyOnce pins the once-per-mint bound: a candidate
// still held after the jump (a writer the scan could not see) is walked past
// one increment at a time, without a second scan.
func TestCounterModeMintJumpsOnlyOnce(t *testing.T) {
	repo := &counterRetiredIssueRepo{next: 1, held: map[string]bool{"bd-2": true, "bd-101": true}, raiseTo: 100}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-102" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-102 past the post-jump collision", id)
	}
	if got := len(repo.raises); got != 1 {
		t.Fatalf("raised %d times, want 1: the jump scans at most once per mint", got)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound: a counter that
// keeps landing on held values past the jump fails loudly instead of walking
// forever.
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	// Every candidate is held, even past the jump, so the walk runs out of
	// bound instead of finding a free value.
	repo := &counterRetiredIssueRepo{held: map[string]bool{}, allHeld: true, raiseTo: 1000}
	u := &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil); err == nil {
		t.Fatalf("nextUnretiredCounterID() = %q, nil; want the skip-bound error", id)
	}
	if got := len(repo.raises); got != 1 {
		t.Fatalf("raised %d times, want 1: the bound walk does not scan again", got)
	}
}

// TestCounterModeMintWalksPastClaimedValues pins the batch half of the
// counter-mode mint: a value the caller's batch claimed (an explicit id a
// later item names) is skipped like a held one, without the taken read and
// without a jump — the rows it names are in no table yet for a scan to see.
func TestCounterModeMintWalksPastClaimedValues(t *testing.T) {
	repo := &counterRetiredIssueRepo{next: 4, held: map[string]bool{}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", map[string]bool{"bd-5": true})
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-6 past the claimed bd-5", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-6" {
		t.Fatalf("probed %s, want only bd-6: a claimed value costs no taken read", got)
	}
	if len(repo.raises) != 0 {
		t.Fatalf("raised %v, want none: a claimed value triggers no jump", repo.raises)
	}
}
