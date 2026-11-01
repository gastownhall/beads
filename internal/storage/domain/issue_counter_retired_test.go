package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// counterRetiredIssueRepo hands out counter values in order and reports the
// listed ids retired. The embedded interface is nil on purpose: a call this
// test did not intend panics rather than passing quietly.
type counterRetiredIssueRepo struct {
	IssueSQLRepository
	next    int
	retired map[string]bool
	probed  []string
}

func (r *counterRetiredIssueRepo) NextCounterID(context.Context, string) (int, error) {
	r.next++
	return r.next, nil
}

func (r *counterRetiredIssueRepo) Retired(_ context.Context, id string) (bool, error) {
	r.probed = append(r.probed, id)
	return r.retired[id], nil
}

// TestCounterModeMintWalksPastRetiredValues pins the uow counter-mode mint: a
// counter value a retaining sweep retired is skipped and the counter advanced
// again, so a fresh create is never handed an id it would be refused.
func TestCounterModeMintWalksPastRetiredValues(t *testing.T) {
	repo := &counterRetiredIssueRepo{next: 4, retired: map[string]bool{"bd-5": true, "bd-6": true}}
	u := &issueUseCaseImpl{issueRepo: repo}
	id, err := u.nextUnretiredCounterID(t.Context(), "bd")
	if err != nil {
		t.Fatalf("nextUnretiredCounterID() = %v", err)
	}
	if id != "bd-7" {
		t.Fatalf("nextUnretiredCounterID() = %q, want bd-7 past the retired bd-5 and bd-6", id)
	}
	if got := strings.Join(repo.probed, ","); got != "bd-5,bd-6,bd-7" {
		t.Fatalf("probed %s, want bd-5,bd-6,bd-7", got)
	}
}

// TestCounterModeMintGivesUpAfterTheSkipBound pins the bound: a counter that
// keeps landing on retired values fails loudly instead of walking forever.
func TestCounterModeMintGivesUpAfterTheSkipBound(t *testing.T) {
	repo := &counterRetiredIssueRepo{retired: map[string]bool{}}
	for n := 1; n <= maxRetiredCounterSkips+1; n++ {
		repo.retired[fmt.Sprintf("bd-%d", n)] = true
	}
	u := &issueUseCaseImpl{issueRepo: repo}
	if id, err := u.nextUnretiredCounterID(t.Context(), "bd"); err == nil {
		t.Fatalf("nextUnretiredCounterID() = %q, nil; want the skip-bound error", id)
	}
}
