package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestClient_GateLifecycleAndShutdown(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	createResp, err := client.GateCreate(&GateCreateArgs{
		Title:     "Test Gate",
		AwaitType: "human",
		AwaitID:   "",
		Timeout:   5 * time.Minute,
		Waiters:   []string{"mayor/"},
	})
	if err != nil {
		t.Fatalf("GateCreate: %v", err)
	}

	var created GateCreateResult
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("unmarshal GateCreateResult: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected created gate ID")
	}

	listResp, err := client.GateList(&GateListArgs{All: false})
	if err != nil {
		t.Fatalf("GateList: %v", err)
	}
	var openGates []*types.Issue
	if err := json.Unmarshal(listResp.Data, &openGates); err != nil {
		t.Fatalf("unmarshal GateList: %v", err)
	}
	if len(openGates) != 1 || openGates[0].ID != created.ID {
		t.Fatalf("unexpected open gates: %+v", openGates)
	}

	showResp, err := client.GateShow(&GateShowArgs{ID: created.ID})
	if err != nil {
		t.Fatalf("GateShow: %v", err)
	}
	var gate types.Issue
	if err := json.Unmarshal(showResp.Data, &gate); err != nil {
		t.Fatalf("unmarshal GateShow: %v", err)
	}
	if gate.ID != created.ID || gate.IssueType != types.TypeGate {
		t.Fatalf("unexpected gate: %+v", gate)
	}

	waitResp, err := client.GateWait(&GateWaitArgs{ID: created.ID, Waiters: []string{"deacon/"}})
	if err != nil {
		t.Fatalf("GateWait: %v", err)
	}
	var waitResult GateWaitResult
	if err := json.Unmarshal(waitResp.Data, &waitResult); err != nil {
		t.Fatalf("unmarshal GateWaitResult: %v", err)
	}
	if waitResult.AddedCount != 1 {
		t.Fatalf("expected 1 waiter added, got %d", waitResult.AddedCount)
	}

	closeResp, err := client.GateClose(&GateCloseArgs{ID: created.ID, Reason: "done"})
	if err != nil {
		t.Fatalf("GateClose: %v", err)
	}
	var closedGate types.Issue
	if err := json.Unmarshal(closeResp.Data, &closedGate); err != nil {
		t.Fatalf("unmarshal GateClose: %v", err)
	}
	if closedGate.Status != types.StatusClosed {
		t.Fatalf("expected closed status, got %q", closedGate.Status)
	}

	listResp, err = client.GateList(&GateListArgs{All: false})
	if err != nil {
		t.Fatalf("GateList open: %v", err)
	}
	if err := json.Unmarshal(listResp.Data, &openGates); err != nil {
		t.Fatalf("unmarshal GateList open: %v", err)
	}
	if len(openGates) != 0 {
		t.Fatalf("expected no open gates, got %+v", openGates)
	}

	listResp, err = client.GateList(&GateListArgs{All: true})
	if err != nil {
		t.Fatalf("GateList all: %v", err)
	}
	if err := json.Unmarshal(listResp.Data, &openGates); err != nil {
		t.Fatalf("unmarshal GateList all: %v", err)
	}
	if len(openGates) != 1 || openGates[0].ID != created.ID {
		t.Fatalf("expected 1 total gate, got %+v", openGates)
	}

	if err := client.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
