package httpclient

import (
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

// TestCreateSendsTheDefaultPriorityToAServerThatPredatesIt is a NEW client
// against an OLD server, on all three create roles. A handshake without
// issues.create.defaultPriority is a server that reads an absent `priority` as
// 0 and would store P0 (critical) for every create that asked for the
// default, so against it the role sends issueops.DefaultCreatePriority (2)
// explicitly. Against a server that advertises the token the member stays
// absent and the server's role applies the default. An explicit 0 is sent as 0
// to both.
func TestCreateSendsTheDefaultPriorityToAServerThatPredatesIt(t *testing.T) {
	issue := func(priority int) *types.Issue {
		return &types.Issue{Title: "t", IssueType: types.TypeTask, Priority: priority}
	}
	roles := map[string]func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) *int{
		"Lifecycle.Create": func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) *int {
			lifecycle, err := s.IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{
				Actor: "a", Issue: issue(priority), DefaultPriority: useDefault,
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			return w.lastCreate.Priority
		},
		"BatchCreator.CreateBatch": func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) *int {
			creator, err := s.BatchCreator()
			if err != nil {
				t.Fatalf("BatchCreator(): %v", err)
			}
			if _, err := creator.CreateBatch(t.Context(), issueops.CreateBatchRequest{
				Actor: "a", Items: []issueops.BatchCreateItem{{Issue: issue(priority), DefaultPriority: useDefault}},
			}); err != nil {
				t.Fatalf("CreateBatch: %v", err)
			}
			return w.lastBatchCreate.Items[0].Priority
		},
		"BatchApplier.ApplyBatch": func(t *testing.T, s *Store, w *stubWire, priority int, useDefault bool) *int {
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
				t.Fatalf("ApplyBatch: %v", err)
			}
			return w.lastApply.Items[0].Create.Priority
		},
	}
	show := func(p *int) any {
		if p == nil {
			return "absent"
		}
		return *p
	}
	for name, run := range roles {
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
				if got := show(run(t, store(w), w, 0, true)); got != server.wantDefault {
					t.Errorf("DefaultPriority: sent priority %v, want %v", got, server.wantDefault)
				}
				w = &stubWire{}
				if got := show(run(t, store(w), w, 0, false)); got != 0 {
					t.Errorf("explicit 0: sent priority %v, want 0", got)
				}
			})
		}
	}
}
