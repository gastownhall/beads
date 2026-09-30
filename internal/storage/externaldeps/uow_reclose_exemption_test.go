package externaldeps

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// probeFailingIssueUseCase injects one error per plane so the re-close
// exemption's error polarity can be driven from both probes.
type probeFailingIssueUseCase struct {
	*fakeIssueUseCase
	issueErr error
	wispErr  error
}

func (u probeFailingIssueUseCase) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	if u.issueErr != nil {
		return nil, u.issueErr
	}
	return u.fakeIssueUseCase.GetIssue(ctx, id)
}

func (u probeFailingIssueUseCase) GetWisp(ctx context.Context, id string) (*types.Issue, error) {
	if u.wispErr != nil {
		return nil, u.wispErr
	}
	return u.fakeIssueUseCase.GetWisp(ctx, id)
}

// TestClosedInUOWPolarity pins the re-close exemption's error polarity on the
// unit-of-work arm: a MISS falls through to the wisp plane and finally answers
// "not closed", while a real failure from either probe reaches the caller.
//
// The distinction matters because closedInUOW's answer is consumed on the
// blockers-non-empty path: a swallowed error becomes a false "not closed",
// which the caller reports as ErrCloseBlocked — a permanent-looking external
// refusal, advising --force, for what was an infrastructure blip.
//
// The miss cases carry both spellings on purpose. This decorator sits on the
// DOMAIN seam, which answers a miss with sql.ErrNoRows (wrapped), not the
// storage.ErrNotFound its store-arm siblings raise; a test for the sentinel
// alone would pass against a predicate that turns every ordinary miss into a
// hard error and drops the wisp fallback.
func TestClosedInUOWPolarity(t *testing.T) {
	closedIssue := issue("be-issue")
	closedIssue.Status = types.StatusClosed
	closedWisp := issue("be-wisp")
	closedWisp.Status = types.StatusClosed
	openIssue := issue("be-open")

	boom := errors.New("connection reset by peer")

	for _, tc := range []struct {
		name       string
		id         string
		issueErr   error
		wispErr    error
		wantClosed bool
		wantErr    error
	}{
		{name: "closed on the issues plane", id: closedIssue.ID, wantClosed: true},
		{name: "open on the issues plane", id: openIssue.ID},
		{name: "nil-nil miss falls through to a closed wisp", id: closedWisp.ID, wantClosed: true},
		{
			name:       "domain-seam miss falls through to a closed wisp",
			id:         closedWisp.ID,
			issueErr:   fmt.Errorf("get %s: %w", closedWisp.ID, sql.ErrNoRows),
			wantClosed: true,
		},
		{
			name:       "store-seam miss falls through to a closed wisp",
			id:         closedWisp.ID,
			issueErr:   fmt.Errorf("%w: issue %s", storage.ErrNotFound, closedWisp.ID),
			wantClosed: true,
		},
		{name: "issues-plane failure reaches the caller", id: closedWisp.ID, issueErr: boom, wantErr: boom},
		{name: "wisp-plane failure reaches the caller", id: "be-absent", wispErr: boom, wantErr: boom},
		{name: "on neither plane is not closed and not an error", id: "be-absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := probeFailingIssueUseCase{
				fakeIssueUseCase: &fakeIssueUseCase{
					ready: []*types.Issue{closedIssue, openIssue},
					wisps: []*types.Issue{closedWisp},
				},
				issueErr: tc.issueErr,
				wispErr:  tc.wispErr,
			}

			closed, err := closedInUOW(t.Context(), issues, tc.id)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v — a swallowed probe failure reports as a permanent external refusal", err, tc.wantErr)
				}
				if closed {
					t.Errorf("closed = true on a failed probe, want false")
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if closed != tc.wantClosed {
				t.Errorf("closed = %v, want %v", closed, tc.wantClosed)
			}
		})
	}
}

// TestPinRecloseRefusesWhenNoArmIsWired pins the store arm's degradation for a
// policyBatchApplier that carries neither read: refuse plainly, the way the
// sibling policyBatchCloser's default: arm already does, rather than
// dereferencing a nil read inside a write path.
//
// Neither construction site can produce this today (the store arm sets current,
// the unit-of-work arm sets guarded), so this pins the structure against a
// future composition site, which is the whole reason the sibling carries its
// own default: arm.
func TestPinRecloseRefusesWhenNoArmIsWired(t *testing.T) {
	applier := &policyBatchApplier{}
	item := batchClosingItem{index: 0, kind: publicops.ItemClose, target: publicops.Ref{ID: "be-held"}}

	err := applier.pinReClose(t.Context(), nil, item, []string{"external:remote:payments"})

	if err == nil {
		t.Fatal("pinReClose with neither arm wired returned nil, want the external refusal")
	}
	if !errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("err = %v, want it to wrap storage.ErrCloseBlocked", err)
	}
}
