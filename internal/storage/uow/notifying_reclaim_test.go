package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	publicops "github.com/steveyegge/beads/issueops"
)

// reclaimStubUC answers Reclaim with a canned result and implements nothing
// else; the embedded nil interface makes any other call panic.
type reclaimStubUC struct {
	domain.IssueUseCase
	result publicops.ReclaimResult
}

func (s *reclaimStubUC) Reclaim(context.Context, publicops.ReclaimRequest) (publicops.ReclaimResult, error) {
	return s.result, nil
}

// TestRecordingIssueUCReclaimRecordsNothingForADryRun pins the notifying
// decorator's half of "a dry run fires no hook". The rows a preview names are
// still leased and assigned, so recording an update for them would announce a
// change that never happened. The snapshotter is nil on purpose: a dry run
// that reached it would panic, which is how this fails against a decorator
// that records for a preview.
func TestRecordingIssueUCReclaimRecordsNothingForADryRun(t *testing.T) {
	rec := &recorder{}
	uc := &recordingIssueUC{
		IssueUseCase: &reclaimStubUC{result: publicops.ReclaimResult{
			DryRun:    true,
			Reclaimed: []publicops.ReclaimedLease{{ID: "bd-1", PreviousOwner: "w"}, {ID: "bd-2"}},
		}},
		rec: rec,
	}

	result, err := uc.Reclaim(context.Background(), publicops.ReclaimRequest{Actor: "reaper", DryRun: true})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if !result.DryRun || len(result.Reclaimed) != 2 {
		t.Fatalf("result = %+v, want the preview passed through untouched", result)
	}
	if len(rec.entries) != 0 {
		t.Fatalf("recorded %d notification(s) for a dry run, want none", len(rec.entries))
	}
}
