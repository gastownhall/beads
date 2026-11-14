package httpclient

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestBatchCreateLeavesPriorityAbsentForTheDefault pins wirePriority on all
// three create bodies: a request that asks for the default priority sends NO
// `priority` member, so the server's role applies the default — the one place
// it lives — and a request naming 0 sends `0`, because P0 is a real request.
// Asking for the default while naming a priority is refused before the dial,
// the role's own ErrValidation, rather than silently dropping one of the two.
func TestBatchCreateLeavesPriorityAbsentForTheDefault(t *testing.T) {
	issue := func(priority int) *types.Issue {
		return &types.Issue{Title: "t", IssueType: types.TypeTask, Priority: priority}
	}
	type encoded struct {
		priority *int
		err      error
	}
	shapes := map[string]func(priority int, useDefault bool) encoded{
		"createIssue": func(priority int, useDefault bool) encoded {
			body, err := createBody(issueops.CreateRequest{Actor: "a", Issue: issue(priority), DefaultPriority: useDefault})
			return encoded{body.Priority, err}
		},
		"batchCreateIssues": func(priority int, useDefault bool) encoded {
			item, err := batchCreateItem(0, issueops.BatchCreateItem{Issue: issue(priority), DefaultPriority: useDefault}, "a")
			return encoded{item.Priority, err}
		},
		"applyBatch create item": func(priority int, useDefault bool) encoded {
			item, err := applyCreateItemBody(&issueops.CreateItem{Issue: issue(priority), DefaultPriority: useDefault}, "a")
			if err != nil {
				return encoded{nil, err}
			}
			return encoded{item.Priority, nil}
		},
	}
	for name, encode := range shapes {
		t.Run(name, func(t *testing.T) {
			if got := encode(0, true); got.err != nil || got.priority != nil {
				t.Errorf("DefaultPriority: priority member = %v, err = %v; want the member absent", got.priority, got.err)
			}
			if got := encode(0, false); got.err != nil || got.priority == nil || *got.priority != 0 {
				t.Errorf("priority 0: priority member = %v, err = %v; want an explicit 0", got.priority, got.err)
			}
			if got := encode(3, false); got.err != nil || got.priority == nil || *got.priority != 3 {
				t.Errorf("priority 3: priority member = %v, err = %v; want 3", got.priority, got.err)
			}
			if got := encode(3, true); !errors.Is(got.err, issueops.ErrValidation) {
				t.Errorf("DefaultPriority with priority 3: err = %v, want ErrValidation", got.err)
			}
		})
	}
}

// createPriorityRoles drives one create through each of the three create roles
// and reports the `priority` member that role put on the wire (nil when it is
// absent), or the role's own error.
func createPriorityRoles() map[string]func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) (*int, error) {
	issue := func(priority int) *types.Issue {
		return &types.Issue{Title: "t", IssueType: types.TypeTask, Priority: priority}
	}
	return map[string]func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) (*int, error){
		"Lifecycle.Create": func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) (*int, error) {
			lifecycle, err := s.IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{
				Actor: "a", Issue: issue(priority), DefaultPriority: useDefault,
			}); err != nil {
				return nil, err
			}
			return w.lastCreate.Priority, nil
		},
		"BatchCreator.CreateBatch": func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) (*int, error) {
			creator, err := s.BatchCreator()
			if err != nil {
				t.Fatalf("BatchCreator(): %v", err)
			}
			if _, err := creator.CreateBatch(t.Context(), issueops.CreateBatchRequest{
				Actor: "a", Items: []issueops.BatchCreateItem{{Issue: issue(priority), DefaultPriority: useDefault}},
			}); err != nil {
				return nil, err
			}
			return w.lastBatchCreate.Items[0].Priority, nil
		},
		"BatchApplier.ApplyBatch": func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) (*int, error) {
			applier, err := s.BatchApplier()
			if err != nil {
				t.Fatalf("BatchApplier(): %v", err)
			}
			if _, err := applier.ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
				Actor: "a", Items: []issueops.ApplyItem{{
					Kind:   issueops.ItemCreate,
					Create: &issueops.CreateItem{Issue: issue(priority), DefaultPriority: useDefault},
				}},
			}); err != nil {
				return nil, err
			}
			return w.lastApply.Items[0].Create.Priority, nil
		},
	}
}

// showPriority renders a wire `priority` member for a comparison: its value,
// or "absent".
func showPriority(p *int) any {
	if p == nil {
		return "absent"
	}
	return *p
}

// TestCreateSendsTheDefaultPriorityToAServerThatPredatesIt is a NEW client
// against an OLD server, on all three create roles. A handshake without
// issues.create.defaultPriority is a server that reads an absent `priority` as
// 0 and would store P0 (critical) for every create that asked for the
// default, so against it the role sends issueops.DefaultCreatePriority (2)
// explicitly. Against a server that advertises the token the member stays
// absent and the server's role applies the default. An explicit 0 is sent as 0
// to both.
func TestCreateSendsTheDefaultPriorityToAServerThatPredatesIt(t *testing.T) {
	for name, run := range createPriorityRoles() {
		for _, server := range []struct {
			name        string
			caps        []string
			wantDefault any
		}{
			{name: "old server", caps: nil, wantDefault: issueops.DefaultCreatePriority},
			{name: "current server", caps: []string{wire.CapIssuesCreateDefaultPriority}, wantDefault: "absent"},
		} {
			t.Run(name+"/"+server.name, func(t *testing.T) {
				store := func(w *stubWire) *Store {
					return New(testTarget(t), w, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: server.caps})
				}
				w := &stubWire{}
				sent, err := run(t, store(w), w, 0, true)
				if err != nil {
					t.Fatalf("DefaultPriority: %v", err)
				}
				if got := showPriority(sent); got != server.wantDefault {
					t.Errorf("DefaultPriority: sent priority %v, want %v", got, server.wantDefault)
				}
				w = &stubWire{}
				sent, err = run(t, store(w), w, 0, false)
				if err != nil {
					t.Fatalf("explicit 0: %v", err)
				}
				if got := showPriority(sent); got != 0 {
					t.Errorf("explicit 0: sent priority %v, want 0", got)
				}
			})
		}
	}
}

// handshakeWire is a stubWire whose handshake answers with res and err, so a
// test can hand the store a handshake that says nothing, or one that fails.
type handshakeWire struct {
	*stubWire
	res *apigen.ContextResponse
	err error
}

func (w handshakeWire) ServerContext(context.Context) (*apigen.ContextResponse, error) {
	return w.res, w.err
}

// TestCreateDefaultPriorityWithoutAUsableHandshake covers the handshakes that
// cannot say whether the server applies the default, on all three create
// roles. One that answers no context has no token to read, so the role sends
// issueops.DefaultCreatePriority explicitly, as it does to an older server. One
// that fails fails the create before it is dialed, rather than guessing. A
// request that names its priority never asks, so a failing handshake does not
// stop it.
func TestCreateDefaultPriorityWithoutAUsableHandshake(t *testing.T) {
	boom := errors.New("handshake failed")
	for name, run := range createPriorityRoles() {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{}
			sent, err := run(t, New(testTarget(t), handshakeWire{stubWire: w}, nil), w, 0, true)
			if err != nil {
				t.Fatalf("no context, DefaultPriority: %v", err)
			}
			if got := showPriority(sent); got != issueops.DefaultCreatePriority {
				t.Errorf("no context, DefaultPriority: sent priority %v, want %v", got, issueops.DefaultCreatePriority)
			}

			w = &stubWire{}
			if _, err := run(t, New(testTarget(t), handshakeWire{stubWire: w, err: boom}, nil), w, 0, true); !errors.Is(err, boom) {
				t.Errorf("failed handshake, DefaultPriority: err = %v, want the handshake's error", err)
			}
			if len(w.calls) != 0 {
				t.Errorf("failed handshake, DefaultPriority: dialed %v, want nothing", w.calls)
			}

			w = &stubWire{}
			sent, err = run(t, New(testTarget(t), handshakeWire{stubWire: w, err: boom}, nil), w, 0, false)
			if err != nil {
				t.Fatalf("failed handshake, explicit 0: %v", err)
			}
			if got := showPriority(sent); got != 0 {
				t.Errorf("failed handshake, explicit 0: sent priority %v, want 0", got)
			}
		})
	}
}
