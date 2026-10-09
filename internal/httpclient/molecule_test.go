package httpclient

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The molecule rules run in ONE place, the server's library. Against a server
// that does not advertise them the client refuses BEFORE dialing with the
// typed capability error: it never drops auto_close_molecule and closes the
// step alone, and never runs a client-side copy of the auto-close or advance.

func requireCapabilityRefusal(t *testing.T, err error, token string, w *stubWire) {
	t.Helper()
	if err == nil {
		t.Fatal("no error, want a pre-dial capability refusal")
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
	}
	if unsup.Capability != token {
		t.Errorf("Capability = %q, want %q", unsup.Capability, token)
	}
	if len(w.calls) != 0 {
		t.Errorf("dialed %v, want none: a pre-dial refusal must never reach the wire", w.calls)
	}
}

func TestAutoCloseMoleculeRefusesBeforeDialingWhenUnadvertised(t *testing.T) {
	close := issueops.CloseRequest{Actor: "alice", IssueID: "bd-1", AutoCloseMolecule: true}

	t.Run("Close", func(t *testing.T) {
		w := &stubWire{close: &apigen.CloseIssueResponse{Revision: "0"}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		_, err = lifecycle.Close(t.Context(), close)
		requireCapabilityRefusal(t, err, wire.CapCloseAutoCloseMolecule, w)
	})

	for name, caps := range map[string][]string{
		"CloseBatch served":   {"issues.close", "issues.batchClose"},
		"CloseBatch composed": {"issues.close"},
	} {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{}
			closer, err := New(testTarget(t), w, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: caps}).BatchCloser()
			if err != nil {
				t.Fatal(err)
			}
			_, err = closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
				Actor: "alice", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, AutoCloseMolecule: true,
			})
			requireCapabilityRefusal(t, err, wire.CapCloseAutoCloseMolecule, w)
		})
	}

	t.Run("without the flag a close still dials", func(t *testing.T) {
		w := &stubWire{close: &apigen.CloseIssueResponse{Revision: "0"}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		plain := close
		plain.AutoCloseMolecule = false
		if _, err := lifecycle.Close(t.Context(), plain); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if w.lastClose.AutoCloseMolecule != nil {
			t.Errorf("auto_close_molecule = %v, want absent when not asked for", *w.lastClose.AutoCloseMolecule)
		}
	})
}

func TestAutoCloseMoleculeRidesTheMemberWhenAdvertised(t *testing.T) {
	snap := &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: []string{
		"issues.close", "issues.batchClose", wire.CapCloseAutoCloseMolecule,
	}}
	root := types.Issue{ID: "bd-root", Status: types.StatusClosed}

	t.Run("Close", func(t *testing.T) {
		w := &stubWire{close: &apigen.CloseIssueResponse{Revision: "0", AutoClosedMolecule: &root}}
		lifecycle, err := New(testTarget(t), w, snap).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		res, err := lifecycle.Close(t.Context(), issueops.CloseRequest{Actor: "alice", IssueID: "bd-1", AutoCloseMolecule: true})
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if w.lastClose.AutoCloseMolecule == nil || !*w.lastClose.AutoCloseMolecule {
			t.Errorf("auto_close_molecule = %v, want true", w.lastClose.AutoCloseMolecule)
		}
		if res.AutoClosedMolecule == nil || res.AutoClosedMolecule.ID != "bd-root" {
			t.Errorf("AutoClosedMolecule = %+v, want bd-root", res.AutoClosedMolecule)
		}
	})

	t.Run("CloseBatch", func(t *testing.T) {
		w := &stubWire{batchClose: &apigen.BatchCloseResponse{Outcomes: []apigen.CloseOutcome{{
			IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1", Status: types.StatusClosed}, AutoClosedMolecule: &root,
		}}}}
		closer, err := New(testTarget(t), w, snap).BatchCloser()
		if err != nil {
			t.Fatal(err)
		}
		res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
			Actor: "alice", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, AutoCloseMolecule: true,
		})
		if err != nil {
			t.Fatalf("CloseBatch: %v", err)
		}
		if w.lastBatchClose.AutoCloseMolecule == nil || !*w.lastBatchClose.AutoCloseMolecule {
			t.Errorf("auto_close_molecule = %v, want true", w.lastBatchClose.AutoCloseMolecule)
		}
		if len(res.Outcomes) != 1 || res.Outcomes[0].AutoClosedMolecule == nil || res.Outcomes[0].AutoClosedMolecule.ID != "bd-root" {
			t.Errorf("Outcomes = %+v, want bd-1 reporting bd-root auto-closed", res.Outcomes)
		}
	})
}

func TestAdvanceMoleculeRefusesBeforeDialingWhenUnadvertised(t *testing.T) {
	token, ok := wire.CapabilityFor(wire.OpAdvanceMolecule)
	if !ok || token == "" {
		t.Fatal("advanceMolecule has no capability token")
	}
	w := &stubWire{}
	stepper, err := stubStore(t, w).MoleculeStepper()
	if err != nil {
		t.Fatal(err)
	}
	_, err = stepper.Advance(t.Context(), issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "bd-1", AutoClaim: true})
	requireCapabilityRefusal(t, err, token, w)
}

// The CLI asks for the auto-close only where the route serves it
// (storage.ServesMoleculeAutoClose): a server silent on the token owns the
// molecule's auto-close, and a close there goes out without the member.
func TestServesMoleculeAutoCloseFollowsTheHandshake(t *testing.T) {
	ctx := t.Context()
	silent := New(testTarget(t), &stubWire{}, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: []string{"issues.close"}})
	if storage.ServesMoleculeAutoClose(ctx, silent) {
		t.Error("ServesMoleculeAutoClose = true against a server silent on the token")
	}
	served := New(testTarget(t), &stubWire{}, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: []string{"issues.close", wire.CapCloseAutoCloseMolecule}})
	if !storage.ServesMoleculeAutoClose(ctx, served) {
		t.Error("ServesMoleculeAutoClose = false against a server advertising the token")
	}
}

// `bd close --continue` asks storage.ServesMoleculeAdvance before it closes,
// so a server silent on issues.advanceMolecule is refused up front instead of
// after the step's close has committed.
func TestServesMoleculeAdvanceFollowsTheHandshake(t *testing.T) {
	ctx := t.Context()
	token, _ := wire.CapabilityFor(wire.OpAdvanceMolecule)
	silent := New(testTarget(t), &stubWire{}, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: []string{"issues.close"}})
	if storage.ServesMoleculeAdvance(ctx, silent) {
		t.Error("ServesMoleculeAdvance = true against a server silent on the token")
	}
	served := New(testTarget(t), &stubWire{}, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: []string{"issues.close", token}})
	if !storage.ServesMoleculeAdvance(ctx, served) {
		t.Error("ServesMoleculeAdvance = false against a server advertising the token")
	}
}
