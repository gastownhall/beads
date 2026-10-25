package httpapi

import (
	"net/http"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The three create shapes leave the default priority to the ROLE: an absent
// `priority` reaches it as DefaultPriority with a zero Issue.Priority, and an
// explicit `0` reaches it as P0 with the flag off. The handler never makes up
// the number — before, it left Issue.Priority at 0 and an absent member stored
// P0 (critical), against the document's "absent means the default".

func TestCreateLeavesAnAbsentPriorityToTheRole(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantDefault bool
		wantValue   int
	}{
		{"absent", `{"actor":"alice","title":"t"}`, true, 0},
		{"explicit zero", `{"actor":"alice","title":"t","priority":0}`, false, 0},
		{"explicit three", `{"actor":"alice","title":"t","priority":3}`, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := &roleLifecycle{createResult: issueops.CreateResult{Issue: createdIssue("bd-7")}}
			ts := newCreateServer(t, lifecycle)
			resp := ts.createIssue(t, tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := lifecycle.createRequests()
			if len(got) != 1 {
				t.Fatalf("the role was called %d times, want 1", len(got))
			}
			if got[0].DefaultPriority != tc.wantDefault || got[0].Issue.Priority != tc.wantValue {
				t.Errorf("role got DefaultPriority=%v Priority=%d, want %v and %d",
					got[0].DefaultPriority, got[0].Issue.Priority, tc.wantDefault, tc.wantValue)
			}
		})
	}
}

func TestBatchCreateLeavesAnAbsentPriorityToTheRole(t *testing.T) {
	creator := &roleBatchCreator{}
	ts := newBatchCreateServer(t, creator)
	resp := ts.claim(t, batchCreatePath, `{"actor":"alice","items":[
		{"title":"absent"},
		{"title":"zero","priority":0},
		{"title":"three","priority":3}
	]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := creator.createRequests()
	if len(reqs) != 1 || len(reqs[0].Items) != 3 {
		t.Fatalf("role calls = %v, want one request of three items", reqs)
	}
	for i, want := range []struct {
		useDefault bool
		priority   int
	}{{true, 0}, {false, 0}, {false, 3}} {
		item := reqs[0].Items[i]
		if item.DefaultPriority != want.useDefault || item.Issue.Priority != want.priority {
			t.Errorf("item %d: DefaultPriority=%v Priority=%d, want %v and %d",
				i, item.DefaultPriority, item.Issue.Priority, want.useDefault, want.priority)
		}
	}
}

func TestApplyBatchLeavesAnAbsentPriorityToTheRole(t *testing.T) {
	applier := &roleBatchApplier{result: issueops.ApplyBatchResult{
		Items: []issueops.ItemResult{
			{Kind: issueops.ItemCreate, IssueID: "bd-1", Changed: true},
			{Kind: issueops.ItemCreate, IssueID: "bd-2", Changed: true},
		},
	}}
	ts := newApplyBatchServer(t, applier)
	resp := ts.claim(t, batchApplyPath, `{"actor":"alice","items":[
		{"kind":"create","create":{"title":"absent"}},
		{"kind":"create","create":{"title":"zero","priority":0}}
	]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := applier.requests()
	if len(reqs) != 1 || len(reqs[0].Items) != 2 {
		t.Fatalf("role calls = %v, want one request of two items", reqs)
	}
	absent, zero := reqs[0].Items[0].Create, reqs[0].Items[1].Create
	if !absent.DefaultPriority || absent.Issue.Priority != 0 {
		t.Errorf("absent priority: DefaultPriority=%v Priority=%d, want true and 0", absent.DefaultPriority, absent.Issue.Priority)
	}
	if zero.DefaultPriority || zero.Issue.Priority != 0 {
		t.Errorf("explicit 0: DefaultPriority=%v Priority=%d, want false and 0", zero.DefaultPriority, zero.Issue.Priority)
	}
}
