package domain

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// counterWalkIssueRepo models the repository policy the counter-mode mint
// leans on: NextCounterID increments a bare counter, Taken reports the ids
// anything holds (a live row in either plane or a retaining tombstone — one
// predicate, as in issueops.IDTakenInTx), and AdvanceCounterPastHeldIDs jumps
// the counter to the highest numeric top-level suffix the taken set holds,
// never decreasing it. The embedded interface is nil on purpose: a call this
// test did not intend panics rather than passing quietly. Retired stays
// implemented (answering false) so the file builds against a walk that still
// probes tombstones alone: a policy regression reads as a wrong result, not
// a missing-method build break.
type counterWalkIssueRepo struct {
	IssueSQLRepository
	next       int
	taken      map[string]bool
	probed     []string
	advances   int
	takenErr   error
	advanceErr error
}

func (r *counterWalkIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterWalkIssueRepo) Retired(context.Context, string) (bool, error) {
	return false, nil
}

func (r *counterWalkIssueRepo) Taken(_ context.Context, id string) (bool, error) {
	if r.takenErr != nil {
		return false, r.takenErr
	}
	r.probed = append(r.probed, id)
	return r.taken[id], nil
}

// AdvanceCounterPastHeldIDs models the repository's GREATEST upsert: jump to
// the highest numeric top-level suffix any taken id holds under prefix,
// ignoring child ids and nonnumeric suffixes, and never move backwards.
func (r *counterWalkIssueRepo) AdvanceCounterPastHeldIDs(_ context.Context, prefix string) error {
	r.advances++
	if r.advanceErr != nil {
		return r.advanceErr
	}
	pfxDash := prefix + "-"
	maxNum := 0
	for id := range r.taken {
		suffix, ok := strings.CutPrefix(id, pfxDash)
		if !ok || strings.Contains(suffix, ".") {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil && n > maxNum {
			maxNum = n
		}
	}
	if maxNum > r.next {
		r.next = maxNum
	}
	return nil
}

// TestCounterModeMintWalksPastTakenValues pins the uow counter-mode mint: a
// counter value anything holds — a live row in either plane or a retaining
// tombstone, the one Taken predicate — is skipped, and on the first held
// value the counter advances past the highest held numeric id once, so a
// fresh create is never handed an id it would overwrite or be refused.
func TestCounterModeMintWalksPastTakenValues(t *testing.T) {
	repo := &counterWalkIssueRepo{next: 4, taken: map[string]bool{"bd-5": true, "bd-6": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd", nil)
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-7" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-7 past the held bd-5 and bd-6", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-5,bd-7" {
		t.Fatalf("probed %s, want bd-5,bd-7: the one advance jumps past bd-6", got)
	}
	if repo.advances != 1 {
		t.Fatalf("advances = %d, want 1 on the first held value", repo.advances)
	}
}

// TestCounterModeMintWalksPastClaimedValues pins the batch half of the
// counter-mode mint: a value the caller's batch claimed (an explicit id a
// later item names) is skipped without the occupancy read and without an
// advance — no table holds a claimed id yet, so a scan cannot heal it.
func TestCounterModeMintWalksPastClaimedValues(t *testing.T) {
	repo := &counterWalkIssueRepo{next: 4, taken: map[string]bool{}}
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
	if repo.advances != 0 {
		t.Fatalf("advances = %d, want 0: a claimed value is held by nothing to scan", repo.advances)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound for the one run
// that can still exhaust it: values the caller's batch claimed, which no
// table holds and no advance can heal. Held values jump the counter, so they
// no longer reach the bound.
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	repo := &counterWalkIssueRepo{taken: map[string]bool{}}
	claimed := map[string]bool{}
	for n := 1; n <= maxRetiredCounterSkips+1; n++ {
		claimed[fmt.Sprintf("bd-%d", n)] = true
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextUnretiredCounterID(t.Context(), "bd", claimed); err == nil {
		t.Fatalf("nextUnretiredCounterID() = %q, nil; want the skip-bound error", id)
	}
	if repo.advances != 0 {
		t.Fatalf("advances = %d, want 0 on a claimed-only run", repo.advances)
	}
}

var (
	errCounterWalkProbe   = errors.New("counter walk probe failed")
	errCounterWalkAdvance = errors.New("counter walk advance failed")
)

// TestCounterModeMintPropagatesOccupancyReadErrors pins that a failing
// occupancy read surfaces to the caller instead of minting past it.
func TestCounterModeMintPropagatesOccupancyReadErrors(t *testing.T) {
	repo := &counterWalkIssueRepo{next: 4, takenErr: errCounterWalkProbe}
	u := &issueUseCaseImpl{issueRepo: repo}
	if _, err := u.nextUnretiredCounterID(t.Context(), "bd", nil); !errors.Is(err, errCounterWalkProbe) {
		t.Fatalf("nextUnretiredCounterID() error = %v, want the occupancy-read error", err)
	}
}

// TestCounterModeMintPropagatesAdvanceErrors pins that a failing counter
// advance surfaces to the caller instead of falling back to the bounded walk.
func TestCounterModeMintPropagatesAdvanceErrors(t *testing.T) {
	repo := &counterWalkIssueRepo{next: 4, taken: map[string]bool{"bd-5": true}, advanceErr: errCounterWalkAdvance}
	u := &issueUseCaseImpl{issueRepo: repo}
	if _, err := u.nextUnretiredCounterID(t.Context(), "bd", nil); !errors.Is(err, errCounterWalkAdvance) {
		t.Fatalf("nextUnretiredCounterID() error = %v, want the advance error", err)
	}
}
