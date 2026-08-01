package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestIssueOperationsCloseSessionSurvivesAllPersistenceMovesWithRealDolt
// verifies the public UOW result preserves close provenance after every
// directed persistence transition between durable and wisp-backed records.
func TestIssueOperationsCloseSessionSurvivesAllPersistenceMovesWithRealDolt(t *testing.T) {
	ctx := context.Background()
	provider := newTestUOWProvider(t)
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			return "", err
		}
		return "initialize close-session fixture", nil
	}); err != nil {
		t.Fatalf("initialize close-session fixture: %v", err)
	}

	operations, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{ID: "bd-close-session", Title: "close provenance", IssueType: types.TypeTask, Priority: 2},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const session = "close-session-xyz"
	assertSession := func(operation string, issue *issueops.Issue) {
		t.Helper()
		if issue == nil {
			t.Fatalf("%s returned nil issue", operation)
		}
		if issue.ClosedBySession != session {
			t.Fatalf("%s ClosedBySession = %q, want %q", operation, issue.ClosedBySession, session)
		}
	}
	closed, err := operations.Close(ctx, issueops.CloseRequest{
		Actor: "tester", IssueID: created.Issue.ID, Session: session,
	})
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertSession("Close()", closed.Issue)

	// These transitions cover every directed pair of supported persistence
	// modes: persistent<->ephemeral, persistent<->no_history, and
	// ephemeral<->no_history.
	for _, mode := range []issueops.PersistenceMode{
		issueops.PersistenceModeEphemeral,
		issueops.PersistenceModePersistent,
		issueops.PersistenceModeNoHistory,
		issueops.PersistenceModeEphemeral,
		issueops.PersistenceModeNoHistory,
		issueops.PersistenceModePersistent,
	} {
		moved, err := operations.Update(ctx, issueops.UpdateRequest{
			Actor: "tester", IssueID: created.Issue.ID,
			Patch: issueops.IssuePatch{
				Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: mode},
			},
		})
		if err != nil {
			t.Fatalf("Update(persistence=%q) error = %v", mode, err)
		}
		assertSession("Update(persistence="+string(mode)+")", moved.Issue)
	}
}
