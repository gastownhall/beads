package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// counterHeldIssueRepo hands out counter values in order. retired ids are
// tombstoned (Retired and Taken both report them); live ids are held by a row
// of either plane (only Taken reports them). AdvanceCounterPastHeldIDs moves
// the counter to the highest held numeric suffix, or to advanceTo when set (a
// scan that saw less than the probes later do). The embedded interface is nil
// on purpose: a call this test did not intend panics rather than passing
// quietly.
type counterHeldIssueRepo struct {
	IssueSQLRepository
	next      int
	retired   map[string]bool
	live      map[string]bool
	advanceTo int
	probed    []string
	advances  int

	takenErr   error
	advanceErr error
}

func (r *counterHeldIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterHeldIssueRepo) Retired(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	return r.retired[id], nil
}

func (r *counterHeldIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	if r.takenErr != nil {
		return false, r.takenErr
	}
	return r.retired[id] || r.live[id], nil
}

func (r *counterHeldIssueRepo) AdvanceCounterPastHeldIDs(_ context.Context, prefix string) error {
	r.advances++
	if r.advanceErr != nil {
		return r.advanceErr
	}
	target := r.advanceTo
	if target == 0 {
		for _, held := range []map[string]bool{r.retired, r.live} {
			for id := range held {
				if n, err := strconv.Atoi(strings.TrimPrefix(id, prefix+"-")); err == nil && n > target {
					target = n
				}
			}
		}
	}
	if target > r.next {
		r.next = target
	}
	return nil
}

func heldRun(from, to int) map[string]bool {
	held := map[string]bool{}
	for n := from; n <= to; n++ {
		held[fmt.Sprintf("bd-%d", n)] = true
	}
	return held
}

// TestCounterModeMintWalksPastRetiredValues pins the uow counter-mode mint: a
// counter value a retaining sweep retired is never handed out. The first held
// value advances the counter past every held id once, so the retired run
// costs one probe, not one per value.
func TestCounterModeMintWalksPastRetiredValues(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 4, retired: map[string]bool{"bd-5": true, "bd-6": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-7" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-7 past the retired bd-5 and bd-6", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-5,bd-7" {
		t.Fatalf("probed %s, want bd-5,bd-7 (one advance past the retired run)", got)
	}
	if repo.advances != 1 {
		t.Fatalf("advanced %d times, want 1", repo.advances)
	}
}

// TestCounterModeMintSkipsLiveHolders pins bd-wnp679: a counter value a live
// row of either plane already holds (an explicit create ahead of a lagging
// counter) is not handed out, so the create cannot overwrite or twin it.
func TestCounterModeMintSkipsLiveHolders(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 1, live: map[string]bool{"bd-2": true, "bd-4": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-5" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-5 past the live bd-2 and bd-4", id)
	}
}

// TestCounterModeMintFreeValueNeedsNoAdvance pins that a collision-free mint
// pays one occupancy probe and never scans for held ids.
func TestCounterModeMintFreeValueNeedsNoAdvance(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 1, live: map[string]bool{"bd-9": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-2" || repo.advances != 0 {
		t.Fatalf("nextUnretiredCounterID() = %q after %d advances, want bd-2 after none", id, repo.advances)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound: a counter that
// keeps landing on values the caller's batch claimed fails loudly instead of
// walking forever. (A held run no longer exhausts it: the first held value
// advances past the whole run.)
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	repo := &counterHeldIssueRepo{}
	u := &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextUnretiredCounterID(t.Context(), "bd", heldRun(1, maxRetiredCounterSkips+1)); err == nil {
		t.Fatalf("nextUnretiredCounterID() = %q, nil; want the skip-bound error", id)
	}
	if len(repo.probed) != 0 || repo.advances != 0 {
		t.Fatalf("probed %d ids and advanced %d times, want neither for a claimed run", len(repo.probed), repo.advances)
	}
}

// TestCounterModeMintWalksPastClaimedValues pins the batch half of the
// counter-mode mint: a value the caller's batch claimed (an explicit id a
// later item names) is skipped without an occupancy read.
func TestCounterModeMintWalksPastClaimedValues(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 4}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", map[string]bool{"bd-5": true})
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-6 past the claimed bd-5", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-6" {
		t.Fatalf("probed %s, want only bd-6: a claimed value costs no occupancy read", got)
	}
}

// TestCounterModeMintAdvancesPastAHeldRunLongerThanTheBound pins bd-wnp679
// AC-2: a held run longer than the skip bound (an import ahead of the counter)
// is crossed by one advance, not walked value by value into the bound.
func TestCounterModeMintAdvancesPastAHeldRunLongerThanTheBound(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 1, live: heldRun(2, maxRetiredCounterSkips+500)}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if want := fmt.Sprintf("bd-%d", maxRetiredCounterSkips+501); id != want {
		t.Fatalf("nextUnretiredCounterID() = %q, want %s past the held run", id, want)
	}
	if repo.advances != 1 || len(repo.probed) != 2 {
		t.Fatalf("advanced %d times over %d probes, want 1 advance over 2 probes", repo.advances, len(repo.probed))
	}
}

// TestCounterModeMintAdvancesOnlyOnce pins that the held-id scan runs at most
// once per mint: a value still held after the advance is walked past.
func TestCounterModeMintAdvancesOnlyOnce(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 4, live: map[string]bool{"bd-5": true, "bd-6": true, "bd-8": true}, advanceTo: 5}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-7" || repo.advances != 1 {
		t.Fatalf("nextUnretiredCounterID() = %q after %d advances, want bd-7 after 1", id, repo.advances)
	}
}

// TestCounterModeMintHeldAfterClaimedRunAdvances pins that claimed skips do
// not spend the one advance: the first HELD value still triggers it.
func TestCounterModeMintHeldAfterClaimedRunAdvances(t *testing.T) {
	repo := &counterHeldIssueRepo{next: 4, live: map[string]bool{"bd-6": true, "bd-9": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", map[string]bool{"bd-5": true})
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-10" || repo.advances != 1 {
		t.Fatalf("nextUnretiredCounterID() = %q after %d advances, want bd-10 after 1", id, repo.advances)
	}
}

// TestCounterModeMintPropagatesProbeAndAdvanceErrors pins that a failed
// occupancy probe or advance fails the mint rather than handing out an id.
func TestCounterModeMintPropagatesProbeAndAdvanceErrors(t *testing.T) {
	probeErr := fmt.Errorf("probe failed")
	repo := &counterHeldIssueRepo{takenErr: probeErr}
	u := &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil); err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("nextUnretiredCounterID() = %q, %v; want the probe error", id, err)
	}

	advErr := fmt.Errorf("advance failed")
	repo = &counterHeldIssueRepo{live: map[string]bool{"bd-1": true}, advanceErr: advErr}
	u = &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil); err == nil || !strings.Contains(err.Error(), "advance failed") {
		t.Fatalf("nextUnretiredCounterID() = %q, %v; want the advance error", id, err)
	}
}
