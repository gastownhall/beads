package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// handlerMolecule is a tiny stateful store behind the roles the advance is
// composed over (BatchGetter, Relations, EdgeReader, Claimer): root m (a
// molecule) <- m.1 (closed), m.2 (open). The handler tests below prove the
// wire edge; the rule itself is pinned by the molecule contract on every leg.
type handlerMolecule struct {
	mu     sync.Mutex
	issues map[string]*types.Issue
	deps   []*types.Dependency
	claims []issueops.ClaimRequest
}

func newHandlerMolecule() *handlerMolecule {
	h := &handlerMolecule{issues: map[string]*types.Issue{
		"m":   {ID: "m", Title: "molecule", IssueType: types.TypeMolecule, Status: types.StatusOpen},
		"m.1": {ID: "m.1", Title: "one", IssueType: types.TypeTask, Status: types.StatusClosed},
		"m.2": {ID: "m.2", Title: "two", IssueType: types.TypeTask, Status: types.StatusOpen},
		"o":   {ID: "o", Title: "orphan", IssueType: types.TypeTask, Status: types.StatusClosed},
	}}
	h.deps = []*types.Dependency{
		{IssueID: "m.1", DependsOnID: "m", Type: types.DepParentChild},
		{IssueID: "m.2", DependsOnID: "m", Type: types.DepParentChild},
	}
	return h
}

func (h *handlerMolecule) GetMany(_ context.Context, req issueops.GetManyRequest) (issueops.GetManyResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := issueops.GetManyResult{Issues: []*issueops.Issue{}, Missing: []string{}}
	for _, id := range req.IDs {
		if issue := h.issues[id]; issue != nil {
			clone := *issue
			out.Issues = append(out.Issues, &clone)
		} else {
			out.Missing = append(out.Missing, id)
		}
	}
	return out, nil
}

func (h *handlerMolecule) Related(_ context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.issues[req.ID] == nil {
		return nil, fmt.Errorf("%w: %s", issueops.ErrNotFound, req.ID)
	}
	var out []*issueops.RelatedIssue
	for _, dep := range h.deps {
		if dep.DependsOnID == req.ID {
			out = append(out, &issueops.RelatedIssue{Issue: *h.issues[dep.IssueID], DependencyType: dep.Type})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (h *handlerMolecule) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out issueops.EdgeReadResult
	for _, id := range req.IDs {
		anchor := issueops.AnchorEdges{ID: id, Missing: h.issues[id] == nil}
		for _, dep := range h.deps {
			if dep.IssueID == id {
				anchor.Edges = append(anchor.Edges, dep)
			}
		}
		out.Anchors = append(out.Anchors, anchor)
	}
	return out, nil
}

func (h *handlerMolecule) Claim(_ context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.claims = append(h.claims, req)
	issue := h.issues[req.IssueID]
	issue.Status, issue.Assignee = types.StatusInProgress, req.Actor
	clone := *issue
	return issueops.ClaimResult{Issue: &clone, Changed: true}, nil
}

func newAdvanceServer(t *testing.T, h *handlerMolecule) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BatchGetter: h, Relations: h, EdgeReader: h, Claimer: h}))
}

func TestAdvanceMoleculeClaimsTheNextStepThroughTheRole(t *testing.T) {
	h := newHandlerMolecule()
	ts := newAdvanceServer(t, h)

	resp := ts.postBody(t, "/v0/beads/issues/m.1:advanceMolecule", "application/json", `{"actor":"alice","auto_claim":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["molecule_id"] != "m" || body["claimed"] != true || body["complete"] != false {
		t.Fatalf("body = %v, want molecule m, claimed, incomplete", body)
	}
	next, _ := body["next_step"].(map[string]any)
	if next["id"] != "m.2" || next["assignee"] != "alice" {
		t.Errorf("next_step = %v, want m.2 assigned to alice", next)
	}
	closed, _ := body["closed_step"].(map[string]any)
	if closed["id"] != "m.1" {
		t.Errorf("closed_step = %v, want m.1", closed)
	}
	if len(h.claims) != 1 || h.claims[0] != (issueops.ClaimRequest{Actor: "alice", IssueID: "m.2"}) {
		t.Errorf("the Claimer received %+v, want one claim of m.2 for alice", h.claims)
	}
}

func TestAdvanceMoleculeWithoutAutoClaimWritesNothing(t *testing.T) {
	h := newHandlerMolecule()
	ts := newAdvanceServer(t, h)
	resp := ts.postBody(t, "/v0/beads/issues/m.1:advanceMolecule", "application/json", `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["claimed"] != false || len(h.claims) != 0 {
		t.Errorf("body = %v claims = %v, want nothing claimed", body, h.claims)
	}
}

func TestAdvanceMoleculeOutsideAMoleculeOmitsTheMolecule(t *testing.T) {
	ts := newAdvanceServer(t, newHandlerMolecule())
	resp := ts.postBody(t, "/v0/beads/issues/o:advanceMolecule", "application/json", `{"actor":"alice","auto_claim":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if _, ok := body["molecule_id"]; ok {
		t.Errorf("molecule_id = %v, want it absent for a step in no molecule", body["molecule_id"])
	}
}

func TestAdvanceMoleculeRefusesBadRequestsBeforeTheRole(t *testing.T) {
	h := newHandlerMolecule()
	ts := newAdvanceServer(t, h)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/v0/beads/issues/m.1:advanceMolecule", `{"actor":"alice","extra":1}`, http.StatusBadRequest},
		{"/v0/beads/issues/m.1:advanceMolecule", `{"auto_claim":true}`, http.StatusBadRequest},
		{"/v0/beads/issues/m.1:advanceMolecule", `{"actor":"alice","auto_claim":"yes"}`, http.StatusBadRequest},
		{"/v0/beads/issues/nope:advanceMolecule", `{"actor":"alice"}`, http.StatusNotFound},
	} {
		resp := ts.postBody(t, tc.path, "application/json", tc.body)
		if resp.StatusCode != tc.status {
			t.Errorf("%s %s: status = %d, want %d: %s", tc.path, tc.body, resp.StatusCode, tc.status, readAll(t, resp))
		}
	}
	if len(h.claims) != 0 {
		t.Errorf("a refused request reached the Claimer: %+v", h.claims)
	}
}

func TestCloseForwardsAutoCloseMoleculeAndReportsTheRoot(t *testing.T) {
	root := closedIssue("bd-root")
	lifecycle := &roleLifecycle{closeResult: issueops.CloseResult{
		Issue: closedIssue("bd-1"), Changed: true, AutoClosedMolecule: root,
	}}
	ts := newCloseServer(t, lifecycle)
	resp := ts.closeIssue(t, closePath, `{"actor":"alice","auto_close_molecule":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := lifecycle.closeRequests()
	if len(got) != 1 || !got[0].AutoCloseMolecule {
		t.Fatalf("the role received %+v, want AutoCloseMolecule set", got)
	}
	body := decodeBody(t, resp)
	auto, _ := body["auto_closed_molecule"].(map[string]any)
	if auto["id"] != "bd-root" {
		t.Errorf("auto_closed_molecule = %v, want bd-root", body["auto_closed_molecule"])
	}
	if _, ok := body["molecule_auto_close_refusal"]; ok {
		t.Errorf("molecule_auto_close_refusal present on a clean auto-close: %v", body)
	}

	lifecycle = &roleLifecycle{closeResult: issueops.CloseResult{
		Issue: closedIssue("bd-1"), Changed: true, MoleculeAutoCloseRefusal: "cannot close blocked issue",
	}}
	ts = newCloseServer(t, lifecycle)
	resp = ts.closeIssue(t, closePath, `{"actor":"alice"}`)
	if got := lifecycle.closeRequests(); len(got) != 1 || got[0].AutoCloseMolecule {
		t.Fatalf("an absent member reached the role as %+v, want AutoCloseMolecule unset (opt-in)", got)
	}
	body = decodeBody(t, resp)
	if body["molecule_auto_close_refusal"] != "cannot close blocked issue" {
		t.Errorf("molecule_auto_close_refusal = %v, want the refusal", body["molecule_auto_close_refusal"])
	}
	if _, ok := body["auto_closed_molecule"]; ok {
		t.Errorf("auto_closed_molecule present with nothing auto-closed: %v", body)
	}
}

func TestBatchCloseForwardsAutoCloseMoleculeAndReportsItPerItem(t *testing.T) {
	closer := &roleBatchCloser{result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{
		{IssueID: "bd-1", Issue: closedIssue("bd-1"), Changed: true},
		{IssueID: "bd-2", Issue: closedIssue("bd-2"), Changed: true, AutoClosedMolecule: closedIssue("bd-root")},
	}}}
	ts := newBatchCloseServer(t, closer)
	resp := ts.batchClose(t, `{"actor":"alice","items":[{"id":"bd-1"},{"id":"bd-2"}],"auto_close_molecule":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	closer.mu.Lock()
	requests := append([]issueops.CloseBatchRequest(nil), closer.requests...)
	closer.mu.Unlock()
	if len(requests) != 1 || !requests[0].AutoCloseMolecule {
		t.Fatalf("the role received %+v, want AutoCloseMolecule set", requests)
	}
	outcomes := outcomesOf(t, resp)
	if _, ok := outcomes[0]["auto_closed_molecule"]; ok {
		t.Errorf("outcome 0 = %v, want no auto-closed root", outcomes[0])
	}
	auto, _ := outcomes[1]["auto_closed_molecule"].(map[string]any)
	if auto["id"] != "bd-root" {
		t.Errorf("outcome 1 auto_closed_molecule = %v, want bd-root", outcomes[1]["auto_closed_molecule"])
	}
}
