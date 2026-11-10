package httpclient

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// TestUpdatePersistenceIsSentOnlyToAServerThatAdvertisesIt pins the plane
// move `bd promote` (issueops.Promote) sends. Against a server advertising
// issues.update.persistence it is one updateIssue carrying
// `patch.persistence`; against one that does not, it is the typed capability
// refusal naming the token, raised BEFORE the dial: the member is never
// dropped (the server would answer "no change" to a move it never saw) and
// never sent to a server that would refuse it as an unknown parameter.
func TestUpdatePersistenceIsSentOnlyToAServerThatAdvertisesIt(t *testing.T) {
	promote := issueops.UpdateRequest{
		Actor: "alice", IssueID: "bd-w1",
		Patch: issueops.IssuePatch{Persistence: set(issueops.PersistenceModePersistent)},
	}

	t.Run("advertised", func(t *testing.T) {
		w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7", Changed: true}}
		lifecycle, err := New(testTarget(t), w, &apigen.ContextResponse{
			BdVersion:    "1.2.3",
			Capabilities: []string{wire.CapIssuesUpdateAllowTemplate, wire.CapIssuesUpdatePersistence},
		}).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		res, err := lifecycle.Update(t.Context(), promote)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if !res.Changed {
			t.Error("Changed = false, want the server's answer")
		}
		if len(w.calls) != 1 || w.calls[0] != "updateIssue:bd-w1" {
			t.Fatalf("calls = %v, want one updateIssue", w.calls)
		}
		if got := w.lastPatch["persistence"]; got != "persistent" {
			t.Errorf("patch.persistence = %v, want \"persistent\"; patch = %v", got, w.lastPatch)
		}
		if len(w.lastPatch) != 1 {
			t.Errorf("patch = %v, want the persistence member alone", w.lastPatch)
		}
	})

	t.Run("unadvertised", func(t *testing.T) {
		w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7"}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		_, err = lifecycle.Update(t.Context(), promote)
		requireCapabilityRefusal(t, err, wire.CapIssuesUpdatePersistence, w)
		if !errors.Is(err, wire.ErrCapabilityAbsent) {
			t.Errorf("err = %v, want it to match wire.ErrCapabilityAbsent", err)
		}
		// The message names the token, so a user knows what the server lacks.
		if !strings.Contains(err.Error(), wire.CapIssuesUpdatePersistence) {
			t.Errorf("refusal %q does not name %s", err.Error(), wire.CapIssuesUpdatePersistence)
		}
	})

	t.Run("an update without the move is unaffected", func(t *testing.T) {
		w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7"}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
			Actor: "alice", IssueID: "bd-1", Patch: issueops.IssuePatch{Title: set("t")},
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if _, sent := w.lastPatch["persistence"]; sent {
			t.Errorf("patch = %v carries persistence nobody asked for", w.lastPatch)
		}
	})
}
