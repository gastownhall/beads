package httpclient

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// The gate members: await_type, await_id, timeout and
// waiters on createIssue and batchApply create items, behind
// issues.create.gates; await_id and waiters on updateIssue's patch and
// batchApply update items' patch, behind issues.update.gates.
//
// Each pair below is the two sides of version skew:
//
//	new client, new server   the members are sent, verbatim, and nothing else
//	                         about the request changes.
//	new client, old server   the cached handshake lacks the token, so the
//	                         request refuses BEFORE the dial with the typed
//	                         capability error naming it. Never a dropped member:
//	                         a gate created without its await_type is a gate
//	                         that never resolves, and a waiter dropped from a
//	                         patch is a wake-up nobody receives.
//	old client, either       a request naming no gate member is untouched by
//	                         the token, so the client never asks a server for a
//	                         token it does not need.

// gateServer is a server that advertises both gate tokens beside the two
// stubStore's current server advertises.
func gateServer(t *testing.T, w *stubWire, caps ...string) *Store {
	t.Helper()
	return New(testTarget(t), w, &apigen.ContextResponse{
		BdVersion:    "1.2.3",
		Capabilities: append([]string{wire.CapIssuesUpdateAllowTemplate, wire.CapIssuesUpdateClaim}, caps...),
	})
}

func gateIssue() *issueops.Issue {
	return &issueops.Issue{
		Title:     "gate",
		IssueType: "gate",
		AwaitType: "timer",
		AwaitID:   "run-42",
		Timeout:   90 * time.Second,
		Waiters:   []string{"rig/workers/a", "rig/workers/a", "mayor"},
	}
}

func TestCreateSendsTheGateMembersToAServerThatAdvertisesThem(t *testing.T) {
	t.Run("createIssue", func(t *testing.T) {
		w := &stubWire{}
		lifecycle, err := gateServer(t, w, wire.CapIssuesCreateGates).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{Actor: "alice", Issue: gateIssue()}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		body := w.lastCreate
		if got := derefStringPtr(body.AwaitType); got != "timer" {
			t.Errorf("await_type = %q, want timer", got)
		}
		if got := derefStringPtr(body.AwaitId); got != "run-42" {
			t.Errorf("await_id = %q, want run-42", got)
		}
		if body.Timeout == nil || *body.Timeout != int64(90*time.Second) {
			t.Errorf("timeout = %v, want %d nanoseconds", body.Timeout, int64(90*time.Second))
		}
		// Stored as sent: the duplicate is the caller's to keep or drop.
		if body.Waiters == nil || !slices.Equal(*body.Waiters, []string{"rig/workers/a", "rig/workers/a", "mayor"}) {
			t.Errorf("waiters = %v, want the list verbatim", body.Waiters)
		}
	})

	t.Run("batchApply create item", func(t *testing.T) {
		w := &stubWire{}
		applier, err := gateServer(t, w, wire.CapIssuesCreateGates).BatchApplier()
		if err != nil {
			t.Fatal(err)
		}
		req := applyOneCreate()
		req.Items[0].Create.Issue = gateIssue()
		if _, err := applier.ApplyBatch(t.Context(), req); err != nil {
			t.Fatalf("ApplyBatch: %v", err)
		}
		item := w.lastApply.Items[0].Create
		if derefStringPtr(item.AwaitType) != "timer" || derefStringPtr(item.AwaitId) != "run-42" {
			t.Errorf("await_type/await_id = %v/%v, want timer/run-42", item.AwaitType, item.AwaitId)
		}
		if item.Timeout == nil || *item.Timeout != int64(90*time.Second) {
			t.Errorf("timeout = %v, want %d nanoseconds", item.Timeout, int64(90*time.Second))
		}
		if item.Waiters == nil || len(*item.Waiters) != 3 {
			t.Errorf("waiters = %v, want the three verbatim", item.Waiters)
		}
	})
}

// TestCreateRefusesTheGateMembersAgainstAServerWithoutTheToken is a new client
// against an old server, one member at a time, on both create shapes.
func TestCreateRefusesTheGateMembersAgainstAServerWithoutTheToken(t *testing.T) {
	members := map[string]func(*issueops.Issue){
		"AwaitType": func(i *issueops.Issue) { i.AwaitType = "human" },
		"AwaitID":   func(i *issueops.Issue) { i.AwaitID = "bead:bd-1" },
		"Timeout":   func(i *issueops.Issue) { i.Timeout = time.Minute },
		"Waiters":   func(i *issueops.Issue) { i.Waiters = []string{"mayor"} },
	}
	for name, populate := range members {
		t.Run("createIssue/"+name, func(t *testing.T) {
			issue := &issueops.Issue{Title: "t"}
			populate(issue)
			w := &stubWire{}
			// The update token alone is not the create token.
			lifecycle, err := gateServer(t, w, wire.CapIssuesUpdateGates).IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			_, err = lifecycle.Create(t.Context(), issueops.CreateRequest{Actor: "alice", Issue: issue})
			requireCapabilityRefusal(t, err, wire.CapIssuesCreateGates, w)
			if !errors.Is(err, wire.ErrCapabilityAbsent) {
				t.Errorf("err = %v, want it to match wire.ErrCapabilityAbsent", err)
			}
			if !strings.Contains(err.Error(), wire.CapIssuesCreateGates) {
				t.Errorf("refusal %q does not name %s", err.Error(), wire.CapIssuesCreateGates)
			}
		})
		t.Run("batchApply/"+name, func(t *testing.T) {
			issue := &issueops.Issue{Title: "t"}
			populate(issue)
			// The gate item is the SECOND of two: the whole plan is checked
			// before the dial, so the plain create ahead of it is never sent
			// on its own.
			req := applyOneCreate()
			req.Items = append(req.Items, issueops.ApplyItem{
				Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Issue: issue},
			})
			w := &stubWire{}
			_, err := applyRoleWithSnapshot(t, w, &apigen.ContextResponse{
				BdVersion: "1.2.3", Capabilities: []string{wire.CapIssuesUpdateGates},
			}).ApplyBatch(t.Context(), req)
			requireCapabilityRefusal(t, err, wire.CapIssuesCreateGates, w)
		})
	}
}

func TestUpdateSendsTheGateMembersToAServerThatAdvertisesThem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch issueops.IssuePatch
		want  map[string]any
	}{
		{"await_id", issueops.IssuePatch{AwaitID: set("12345")}, map[string]any{"await_id": "12345"}},
		// The empty string CLEARS await_id, so presence alone decides.
		{"await_id clear", issueops.IssuePatch{AwaitID: set("")}, map[string]any{"await_id": ""}},
		{"waiters", issueops.IssuePatch{Waiters: set([]string{"a", "a", "b"})}, map[string]any{"waiters": []string{"a", "a", "b"}}},
		// A set nil list CLEARS; it travels as [] because null is refused.
		{"waiters clear", issueops.IssuePatch{Waiters: set[[]string](nil)}, map[string]any{"waiters": []string{}}},
	} {
		t.Run("updateIssue/"+tc.name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7", Changed: true}}
			lifecycle, err := gateServer(t, w, wire.CapIssuesUpdateGates).IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
				Actor: "alice", IssueID: "bd-g1", Patch: tc.patch,
			}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			assertPatchDocument(t, w.lastPatch, tc.want)
		})
		t.Run("batchApply/"+tc.name, func(t *testing.T) {
			w := &stubWire{}
			applier, err := gateServer(t, w, wire.CapIssuesUpdateGates).BatchApplier()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applier.ApplyBatch(t.Context(), applyOneUpdate(tc.patch)); err != nil {
				t.Fatalf("ApplyBatch: %v", err)
			}
			assertPatchDocument(t, w.lastApply.Items[0].Update.Patch, tc.want)
		})
	}
}

// TestUpdateRefusesTheGateMembersAgainstAServerWithoutTheToken is a new client
// against an old server on both patch documents.
func TestUpdateRefusesTheGateMembersAgainstAServerWithoutTheToken(t *testing.T) {
	for name, patch := range map[string]issueops.IssuePatch{
		"AwaitID": {AwaitID: set("12345")},
		"Waiters": {Waiters: set([]string{"mayor"})},
		// Beside an ordinary member the whole update still refuses: sending
		// the title alone would be half the edit the caller asked for.
		"Waiters beside a title": {Title: set("t"), Waiters: set([]string{"mayor"})},
	} {
		t.Run("updateIssue/"+name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7"}}
			// The create token alone is not the update token.
			lifecycle, err := gateServer(t, w, wire.CapIssuesCreateGates).IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			_, err = lifecycle.Update(t.Context(), issueops.UpdateRequest{Actor: "alice", IssueID: "bd-g1", Patch: patch})
			requireCapabilityRefusal(t, err, wire.CapIssuesUpdateGates, w)
			if !strings.Contains(err.Error(), wire.CapIssuesUpdateGates) {
				t.Errorf("refusal %q does not name %s", err.Error(), wire.CapIssuesUpdateGates)
			}
		})
		t.Run("batchApply/"+name, func(t *testing.T) {
			// A plain create first: no partial plan is ever sent.
			req := applyOneCreate()
			req.Items = append(req.Items, applyOneUpdate(patch).Items...)
			w := &stubWire{}
			_, err := applyRoleWithSnapshot(t, w, &apigen.ContextResponse{
				BdVersion: "1.2.3", Capabilities: []string{wire.CapIssuesCreateGates},
			}).ApplyBatch(t.Context(), req)
			requireCapabilityRefusal(t, err, wire.CapIssuesUpdateGates, w)
		})
	}
}

// TestARequestWithoutGateMembersIgnoresTheGateTokens is the old-client half
// of the skew: a create or patch naming no gate member dials an old server as
// it always has, and carries no gate member to a new one.
func TestARequestWithoutGateMembersIgnoresTheGateTokens(t *testing.T) {
	for _, server := range []struct {
		name string
		caps []string
	}{
		{"old server", nil},
		{"new server", []string{wire.CapIssuesCreateGates, wire.CapIssuesUpdateGates}},
	} {
		t.Run(server.name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "7", Changed: true}}
			store := gateServer(t, w, server.caps...)
			lifecycle, err := store.IssueLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{
				Actor: "alice", Issue: &issueops.Issue{Title: "plain"},
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			body := w.lastCreate
			if body.AwaitType != nil || body.AwaitId != nil || body.Timeout != nil || body.Waiters != nil {
				t.Errorf("a plain create carries a gate member: %+v", body)
			}
			if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
				Actor: "alice", IssueID: "bd-1", Patch: issueops.IssuePatch{Title: set("t")},
			}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			for _, m := range []string{"await_id", "waiters"} {
				if _, sent := w.lastPatch[m]; sent {
					t.Errorf("a title patch carries %s: %v", m, w.lastPatch)
				}
			}
			applier, err := store.BatchApplier()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applier.ApplyBatch(t.Context(), applyOneCreate()); err != nil {
				t.Fatalf("ApplyBatch: %v", err)
			}
			if w.lastApply.Items[0].Create.Waiters != nil {
				t.Errorf("a plain create item carries waiters")
			}
		})
	}
}

func applyOneUpdate(patch issueops.IssuePatch) issueops.ApplyBatchRequest {
	return issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{{
			Kind:   issueops.ItemUpdate,
			Update: &issueops.UpdateItem{Target: issueops.Ref{ID: "bd-g1"}, Patch: patch},
		}},
	}
}

func assertPatchDocument(t *testing.T, got, want map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("patch = %v, want exactly %v", got, want)
	}
	for member, value := range want {
		sent, ok := got[member]
		if !ok {
			t.Errorf("patch = %v, missing %s", got, member)
			continue
		}
		switch v := value.(type) {
		case []string:
			list, ok := sent.([]string)
			if !ok || list == nil || !slices.Equal(list, v) {
				t.Errorf("patch.%s = %#v, want %#v", member, sent, v)
			}
		default:
			if sent != v {
				t.Errorf("patch.%s = %#v, want %#v", member, sent, v)
			}
		}
	}
}

// TestABoundRefusalRebuildsTheGateFieldError pins the read side of the bounds:
// the server answers the role's *issueops.GateFieldError as a 400
// invalid_argument / invalid_value with `param` naming the member at its
// level, and each role hands the caller the same typed error a local
// backend raises, the problem still in the chain.
func TestABoundRefusalRebuildsTheGateFieldError(t *testing.T) {
	problem := func(param string) error {
		return &wire.ProblemError{
			Op: "op", Status: 400, Code: "invalid_argument", Reason: "invalid_value",
			Param: param, Detail: "the member is past its bound; nothing was written",
			Err: issueops.ErrValidation,
		}
	}
	assertField := func(t *testing.T, err error, field string) {
		t.Helper()
		var fieldErr *issueops.GateFieldError
		if !errors.As(err, &fieldErr) {
			t.Fatalf("err = %v, want a *GateFieldError naming %s", err, field)
		}
		if fieldErr.Field != field {
			t.Errorf("GateFieldError.Field = %q, want %q", fieldErr.Field, field)
		}
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("err = %v, want ErrValidation through", err)
		}
		var wireErr *wire.ProblemError
		if !errors.As(err, &wireErr) {
			t.Errorf("err = %v lost the problem it was read from", err)
		}
	}
	caps := []string{wire.CapIssuesCreateGates, wire.CapIssuesUpdateGates}

	t.Run("createIssue", func(t *testing.T) {
		w := &stubWire{errs: []error{problem("await_type")}}
		lifecycle, err := gateServer(t, w, caps...).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		_, err = lifecycle.Create(t.Context(), issueops.CreateRequest{Actor: "alice", Issue: gateIssue()})
		assertField(t, err, issueops.GateFieldAwaitType)
	})
	t.Run("updateIssue", func(t *testing.T) {
		w := &stubWire{errs: []error{problem("patch.waiters")}}
		lifecycle, err := gateServer(t, w, caps...).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		_, err = lifecycle.Update(t.Context(), issueops.UpdateRequest{
			Actor: "alice", IssueID: "bd-g1", Patch: issueops.IssuePatch{Waiters: set([]string{"a"})},
		})
		assertField(t, err, issueops.GateFieldWaiters)
	})
	t.Run("batchApply", func(t *testing.T) {
		index := 0
		bad := problem("items[0].update.patch.await_id").(*wire.ProblemError)
		bad.ItemIndex = &index
		kind := "update"
		bad.ItemKind = &kind
		w := &stubWire{errs: []error{bad}}
		applier, err := gateServer(t, w, caps...).BatchApplier()
		if err != nil {
			t.Fatal(err)
		}
		_, err = applier.ApplyBatch(t.Context(), applyOneUpdate(issueops.IssuePatch{AwaitID: set("x")}))
		assertField(t, err, issueops.GateFieldAwaitID)
		var itemErr *issueops.ItemError
		if !errors.As(err, &itemErr) || itemErr.Index != 0 {
			t.Errorf("err = %v, want the *ItemError naming item 0", err)
		}
	})
	t.Run("a refusal naming another member is untouched", func(t *testing.T) {
		w := &stubWire{errs: []error{problem("patch.title")}}
		lifecycle, err := gateServer(t, w, caps...).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		_, err = lifecycle.Update(t.Context(), issueops.UpdateRequest{
			Actor: "alice", IssueID: "bd-g1", Patch: issueops.IssuePatch{Title: set("t")},
		})
		var fieldErr *issueops.GateFieldError
		if errors.As(err, &fieldErr) {
			t.Errorf("err = %v reads as a GateFieldError for a title refusal", err)
		}
	})
}
