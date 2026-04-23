package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var gateTestStdoutMu sync.Mutex

type gateCloseCall struct {
	id      string
	reason  string
	actor   string
	session string
}

type fakeGateCheckStore struct {
	storage.DoltStorage
	issues       []*types.Issue
	searchFilter types.IssueFilter
	closeCalls   []gateCloseCall
}

func (f *fakeGateCheckStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchFilter = filter
	return f.issues, nil
}

func (f *fakeGateCheckStore) CloseIssue(_ context.Context, id, reason, actor, session string) error {
	f.closeCalls = append(f.closeCalls, gateCloseCall{
		id:      id,
		reason:  reason,
		actor:   actor,
		session: session,
	})
	return nil
}

func captureGateStdout(t *testing.T, fn func()) string {
	t.Helper()

	gateTestStdoutMu.Lock()
	defer gateTestStdoutMu.Unlock()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()

	_ = w.Close()
	os.Stdout = old
	<-done
	_ = r.Close()

	return buf.String()
}

func resetGateCheckFlags(t *testing.T) {
	t.Helper()

	if err := gateCheckCmd.Flags().Set("type", ""); err != nil {
		t.Fatalf("reset type flag: %v", err)
	}
	if err := gateCheckCmd.Flags().Set("dry-run", "false"); err != nil {
		t.Fatalf("reset dry-run flag: %v", err)
	}
	if err := gateCheckCmd.Flags().Set("escalate", "false"); err != nil {
		t.Fatalf("reset escalate flag: %v", err)
	}
	if err := gateCheckCmd.Flags().Set("limit", "100"); err != nil {
		t.Fatalf("reset limit flag: %v", err)
	}

	gateCheckCmd.Flags().Lookup("type").Changed = false
	gateCheckCmd.Flags().Lookup("dry-run").Changed = false
	gateCheckCmd.Flags().Lookup("escalate").Changed = false
	gateCheckCmd.Flags().Lookup("limit").Changed = false
}

func TestShouldCheckGate(t *testing.T) {
	tests := []struct {
		name       string
		awaitType  string
		typeFilter string
		want       bool
	}{
		// Empty filter matches all
		{"empty filter matches gh:run", "gh:run", "", true},
		{"empty filter matches gh:pr", "gh:pr", "", true},
		{"empty filter matches timer", "timer", "", true},
		{"empty filter matches human", "human", "", true},
		{"empty filter matches bead", "bead", "", true},

		// "all" filter matches all
		{"all filter matches gh:run", "gh:run", "all", true},
		{"all filter matches gh:pr", "gh:pr", "all", true},
		{"all filter matches timer", "timer", "all", true},
		{"all filter matches bead", "bead", "all", true},

		// "gh" filter matches all GitHub types
		{"gh filter matches gh:run", "gh:run", "gh", true},
		{"gh filter matches gh:pr", "gh:pr", "gh", true},
		{"gh filter does not match timer", "timer", "gh", false},
		{"gh filter does not match human", "human", "gh", false},
		{"gh filter does not match bead", "bead", "gh", false},

		// Exact type filters
		{"gh:run filter matches gh:run", "gh:run", "gh:run", true},
		{"gh:run filter does not match gh:pr", "gh:pr", "gh:run", false},
		{"gh:pr filter matches gh:pr", "gh:pr", "gh:pr", true},
		{"gh:pr filter does not match gh:run", "gh:run", "gh:pr", false},
		{"timer filter matches timer", "timer", "timer", true},
		{"timer filter does not match gh:run", "gh:run", "timer", false},
		{"bead filter matches bead", "bead", "bead", true},
		{"bead filter does not match timer", "timer", "bead", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				AwaitType: tt.awaitType,
			}
			got := shouldCheckGate(gate, tt.typeFilter)
			if got != tt.want {
				t.Errorf("shouldCheckGate(%q, %q) = %v, want %v",
					tt.awaitType, tt.typeFilter, got, tt.want)
			}
		})
	}
}

func TestCheckBeadGate_InvalidFormat(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		awaitID string
	}{
		{name: "empty", awaitID: ""},
		{name: "no colon", awaitID: "my-project-mp-abc"},
		{name: "missing rig", awaitID: ":gt-abc"},
		{name: "missing bead", awaitID: "my-project:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			satisfied, reason := checkBeadGate(ctx, tt.awaitID)
			if satisfied {
				t.Errorf("expected not satisfied for %q", tt.awaitID)
			}
			if reason == "" {
				t.Error("expected reason to be set")
			}
			if !gateTestContainsIgnoreCase(reason, "multi-rig routing removed") {
				t.Errorf("reason %q does not contain %q", reason, "multi-rig routing removed")
			}
		})
	}
}

func TestCheckBeadGate_RigNotFound(t *testing.T) {
	ctx := context.Background()

	// With multi-rig routing removed, all bead gates return the same message
	satisfied, reason := checkBeadGate(ctx, "nonexistent:some-id")
	if satisfied {
		t.Error("expected not satisfied for non-existent rig")
	}
	if reason == "" {
		t.Error("expected reason to be set")
	}
	if !gateTestContainsIgnoreCase(reason, "multi-rig routing removed") {
		t.Errorf("reason %q does not contain %q", reason, "multi-rig routing removed")
	}
}

func TestCheckBeadGate_TargetClosed(t *testing.T) {
	t.Skip("SQLite-specific: created SQLite DB directly; full integration testing requires routes.jsonl + Dolt rig infrastructure")
}

func TestIsNumericID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Numeric IDs
		{"12345", true},
		{"12345678901234567890", true},
		{"0", true},
		{"1", true},

		// Non-numeric (workflow names, etc.)
		{"", false},
		{"release.yml", false},
		{"CI", false},
		{"release", false},
		{"123abc", false},
		{"abc123", false},
		{"12.34", false},
		{"-123", false},
		{"123-456", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isNumericID(tt.input)
			if got != tt.want {
				t.Errorf("isNumericID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNeedsDiscovery(t *testing.T) {
	tests := []struct {
		name      string
		awaitType string
		awaitID   string
		want      bool
	}{
		// gh:run gates
		{"gh:run empty await_id", "gh:run", "", true},
		{"gh:run workflow name hint", "gh:run", "release.yml", true},
		{"gh:run workflow name without ext", "gh:run", "CI", true},
		{"gh:run numeric run ID", "gh:run", "12345", false},
		{"gh:run large numeric ID", "gh:run", "12345678901234567890", false},

		// Other gate types should not need discovery
		{"gh:pr gate", "gh:pr", "", false},
		{"timer gate", "timer", "", false},
		{"human gate", "human", "", false},
		{"bead gate", "bead", "rig:id", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				AwaitType: tt.awaitType,
				AwaitID:   tt.awaitID,
			}
			got := needsDiscovery(gate)
			if got != tt.want {
				t.Errorf("needsDiscovery(%q, %q) = %v, want %v",
					tt.awaitType, tt.awaitID, got, tt.want)
			}
		})
	}
}

func TestGetWorkflowNameHint(t *testing.T) {
	tests := []struct {
		name    string
		awaitID string
		want    string
	}{
		{"empty", "", ""},
		{"numeric ID", "12345", ""},
		{"workflow name", "release.yml", "release.yml"},
		{"workflow name yaml", "ci.yaml", "ci.yaml"},
		{"workflow name no ext", "CI", "CI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitID: tt.awaitID}
			got := getWorkflowNameHint(gate)
			if got != tt.want {
				t.Errorf("getWorkflowNameHint(%q) = %q, want %q", tt.awaitID, got, tt.want)
			}
		})
	}
}

func TestCheckGHRun_DryRunDoesNotPersistDiscoveredRunID(t *testing.T) {
	origDiscover := discoverRunIDByWorkflowNameFunc
	origUpdate := updateGateAwaitIDFunc
	origStatus := checkGHRunStatusFunc
	t.Cleanup(func() {
		discoverRunIDByWorkflowNameFunc = origDiscover
		updateGateAwaitIDFunc = origUpdate
		checkGHRunStatusFunc = origStatus
	})

	updateCalls := 0
	discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
		if workflowHint != "release.yml" {
			t.Fatalf("unexpected workflow hint %q", workflowHint)
		}
		return "12345", nil
	}
	updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
		updateCalls++
		t.Fatalf("unexpected await_id persistence for %s -> %s", gateID, runID)
		return nil
	}
	checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
		if runID != "12345" {
			t.Fatalf("expected discovered run ID 12345, got %q", runID)
		}
		return true, false, "workflow 'release' succeeded", nil
	}

	resolved, escalated, reason, err := checkGHRun(&types.Issue{
		ID:      "bd-gate",
		AwaitID: "release.yml",
	}, false)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if !resolved {
		t.Fatal("expected dry-run check to resolve using discovered run status")
	}
	if escalated {
		t.Fatal("did not expect escalation for successful workflow")
	}
	if reason == "" {
		t.Fatal("expected resolution reason")
	}
	if updateCalls != 0 {
		t.Fatalf("expected no await_id updates during dry-run, got %d", updateCalls)
	}
}

func TestCheckGHRun_PersistsDiscoveredRunIDOutsideDryRun(t *testing.T) {
	origDiscover := discoverRunIDByWorkflowNameFunc
	origUpdate := updateGateAwaitIDFunc
	origStatus := checkGHRunStatusFunc
	t.Cleanup(func() {
		discoverRunIDByWorkflowNameFunc = origDiscover
		updateGateAwaitIDFunc = origUpdate
		checkGHRunStatusFunc = origStatus
	})

	updateCalls := 0
	discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
		if workflowHint != "release.yml" {
			t.Fatalf("unexpected workflow hint %q", workflowHint)
		}
		return "67890", nil
	}
	updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
		updateCalls++
		if gateID != "bd-gate" {
			t.Fatalf("expected gate ID bd-gate, got %q", gateID)
		}
		if runID != "67890" {
			t.Fatalf("expected discovered run ID 67890, got %q", runID)
		}
		return nil
	}
	checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
		if runID != "67890" {
			t.Fatalf("expected discovered run ID 67890, got %q", runID)
		}
		return false, false, "workflow 'release' is queued", nil
	}

	resolved, escalated, reason, err := checkGHRun(&types.Issue{
		ID:      "bd-gate",
		AwaitID: "release.yml",
	}, true)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if resolved {
		t.Fatal("did not expect queued workflow to resolve")
	}
	if escalated {
		t.Fatal("did not expect queued workflow to escalate")
	}
	if reason == "" {
		t.Fatal("expected pending reason")
	}
	if updateCalls != 1 {
		t.Fatalf("expected one await_id update outside dry-run, got %d", updateCalls)
	}
}

func TestCheckGHRun_ReturnsErrorWhenPersistingDiscoveredRunIDFails(t *testing.T) {
	origDiscover := discoverRunIDByWorkflowNameFunc
	origUpdate := updateGateAwaitIDFunc
	origStatus := checkGHRunStatusFunc
	t.Cleanup(func() {
		discoverRunIDByWorkflowNameFunc = origDiscover
		updateGateAwaitIDFunc = origUpdate
		checkGHRunStatusFunc = origStatus
	})

	discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
		if workflowHint != "release.yml" {
			t.Fatalf("unexpected workflow hint %q", workflowHint)
		}
		return "12345", nil
	}
	updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
		if gateID != "bd-gate" {
			t.Fatalf("expected gate ID bd-gate, got %q", gateID)
		}
		if runID != "12345" {
			t.Fatalf("expected discovered run ID 12345, got %q", runID)
		}
		return errors.New("write failed")
	}
	checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
		t.Fatalf("did not expect status check after await_id persistence failure, got %q", runID)
		return false, false, "", nil
	}

	resolved, escalated, reason, err := checkGHRun(&types.Issue{
		ID:      "bd-gate",
		AwaitID: "release.yml",
	}, true)
	if err == nil {
		t.Fatal("expected checkGHRun to return an error when await_id persistence fails")
	}
	if resolved {
		t.Fatal("did not expect resolution when await_id persistence fails")
	}
	if escalated {
		t.Fatal("did not expect escalation when await_id persistence fails")
	}
	if reason != "" {
		t.Fatalf("expected empty reason on persistence failure, got %q", reason)
	}
	if !gateTestContains(err.Error(), "failed to update gate with discovered run ID") {
		t.Fatalf("expected wrapped persistence error, got %v", err)
	}
}

func TestCheckGHRunStatus_Success(t *testing.T) {
	installFakeGHScript(t, `{"status":"completed","conclusion":"success","name":"release"}`)

	resolved, escalated, reason, err := checkGHRunStatus("12345")
	if err != nil {
		t.Fatalf("checkGHRunStatus returned error: %v", err)
	}
	if !resolved {
		t.Fatal("expected successful workflow run to resolve the gate")
	}
	if escalated {
		t.Fatal("did not expect successful workflow run to escalate the gate")
	}
	if reason != "workflow 'release' succeeded" {
		t.Fatalf("checkGHRunStatus reason = %q, want %q", reason, "workflow 'release' succeeded")
	}
}

func TestGateCheck_GHRunWorkflowDiscoveryPersistence(t *testing.T) {
	tests := []struct {
		name            string
		dryRun          bool
		wantUpdateCalls int
		wantCloseCalls  int
		wantOutput      string
	}{
		{
			name:            "dry run keeps discovered run ID in memory only",
			dryRun:          true,
			wantUpdateCalls: 0,
			wantCloseCalls:  0,
			wantOutput:      "would resolve - workflow 'release' succeeded",
		},
		{
			name:            "live run persists discovered run ID before closing",
			dryRun:          false,
			wantUpdateCalls: 1,
			wantCloseCalls:  1,
			wantOutput:      "resolved - workflow 'release' succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origStore := store
			origRootCtx := rootCtx
			origJSONOutput := jsonOutput
			origReadonlyMode := readonlyMode
			origActor := actor
			origDiscover := discoverRunIDByWorkflowNameFunc
			origUpdate := updateGateAwaitIDFunc
			origStatus := checkGHRunStatusFunc
			t.Cleanup(func() {
				store = origStore
				rootCtx = origRootCtx
				jsonOutput = origJSONOutput
				readonlyMode = origReadonlyMode
				actor = origActor
				discoverRunIDByWorkflowNameFunc = origDiscover
				updateGateAwaitIDFunc = origUpdate
				checkGHRunStatusFunc = origStatus
				resetGateCheckFlags(t)
			})

			resetGateCheckFlags(t)

			fakeStore := &fakeGateCheckStore{
				issues: []*types.Issue{
					{
						ID:        "bd-gate",
						IssueType: "gate",
						AwaitType: "gh:run",
						AwaitID:   "release.yml",
					},
				},
			}

			store = fakeStore
			rootCtx = context.Background()
			jsonOutput = false
			readonlyMode = false
			actor = "test-actor"

			if err := gateCheckCmd.Flags().Set("dry-run", map[bool]string{true: "true", false: "false"}[tt.dryRun]); err != nil {
				t.Fatalf("set dry-run flag: %v", err)
			}
			if err := gateCheckCmd.Flags().Set("type", "gh:run"); err != nil {
				t.Fatalf("set type flag: %v", err)
			}
			if err := gateCheckCmd.Flags().Set("escalate", "false"); err != nil {
				t.Fatalf("set escalate flag: %v", err)
			}
			if err := gateCheckCmd.Flags().Set("limit", "100"); err != nil {
				t.Fatalf("set limit flag: %v", err)
			}

			updateCalls := 0
			discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
				if workflowHint != "release.yml" {
					t.Fatalf("unexpected workflow hint %q", workflowHint)
				}
				return "12345", nil
			}
			updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
				updateCalls++
				if gateID != "bd-gate" {
					t.Fatalf("expected gate ID bd-gate, got %q", gateID)
				}
				if runID != "12345" {
					t.Fatalf("expected discovered run ID 12345, got %q", runID)
				}
				return nil
			}
			checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
				if runID != "12345" {
					t.Fatalf("expected discovered run ID 12345, got %q", runID)
				}
				return true, false, "workflow 'release' succeeded", nil
			}

			output := captureGateStdout(t, func() {
				gateCheckCmd.Run(gateCheckCmd, nil)
			})

			if updateCalls != tt.wantUpdateCalls {
				t.Fatalf("updateGateAwaitIDFunc call count = %d, want %d", updateCalls, tt.wantUpdateCalls)
			}
			if len(fakeStore.closeCalls) != tt.wantCloseCalls {
				t.Fatalf("CloseIssue call count = %d, want %d", len(fakeStore.closeCalls), tt.wantCloseCalls)
			}
			if !gateTestContains(output, tt.wantOutput) {
				t.Fatalf("output %q does not contain %q", output, tt.wantOutput)
			}
			if !gateTestContains(output, "Checked 1 gates: 1 resolved, 0 escalated, 0 errors") {
				t.Fatalf("summary output missing expected counts: %q", output)
			}
			if fakeStore.searchFilter.IssueType == nil || *fakeStore.searchFilter.IssueType != "gate" {
				t.Fatalf("expected gate filter, got %+v", fakeStore.searchFilter)
			}
			if len(fakeStore.searchFilter.ExcludeStatus) != 1 || fakeStore.searchFilter.ExcludeStatus[0] != types.StatusClosed {
				t.Fatalf("expected closed-status exclusion, got %+v", fakeStore.searchFilter.ExcludeStatus)
			}
			if fakeStore.searchFilter.Limit != 100 {
				t.Fatalf("expected limit 100, got %d", fakeStore.searchFilter.Limit)
			}
			if tt.wantCloseCalls == 1 {
				call := fakeStore.closeCalls[0]
				if call.id != "bd-gate" {
					t.Fatalf("expected CloseIssue for bd-gate, got %q", call.id)
				}
				if call.reason != "workflow 'release' succeeded" {
					t.Fatalf("expected CloseIssue reason to match status, got %q", call.reason)
				}
				if call.actor != "test-actor" {
					t.Fatalf("expected CloseIssue actor test-actor, got %q", call.actor)
				}
			}
		})
	}
}

func TestWorkflowNameMatches(t *testing.T) {
	tests := []struct {
		name         string
		hint         string
		workflowName string
		runName      string
		want         bool
	}{
		// Exact matches
		{"exact workflow name", "Release", "Release", "release.yml", true},
		{"exact run name", "release.yml", "Release", "release.yml", true},
		{"case insensitive workflow", "release", "Release", "release.yml", true},
		{"case insensitive run", "RELEASE.YML", "Release", "release.yml", true},

		// Hint with suffix, match display name without
		{"hint yml vs display name", "release.yml", "release", "ci.yml", true},
		{"hint yaml vs display name", "release.yaml", "release", "ci.yaml", true},

		// Hint without suffix, match filename with suffix
		{"hint base vs filename yml", "release", "CI", "release.yml", true},
		{"hint base vs filename yaml", "release", "CI", "release.yaml", true},

		// No match
		{"no match different name", "release", "CI", "ci.yml", false},
		{"no match partial", "rel", "Release", "release.yml", false},
		{"empty hint", "", "Release", "release.yml", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowNameMatches(tt.hint, tt.workflowName, tt.runName)
			if got != tt.want {
				t.Errorf("workflowNameMatches(%q, %q, %q) = %v, want %v",
					tt.hint, tt.workflowName, tt.runName, got, tt.want)
			}
		})
	}
}

func installFakeGHScript(t *testing.T, stdout string) {
	t.Helper()

	dir := t.TempDir()

	var (
		scriptPath string
		script     string
	)

	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(dir, "gh.cmd")
		script = "@echo off\r\necho " + stdout + "\r\n"
	} else {
		scriptPath = filepath.Join(dir, "gh")
		script = "#!/bin/sh\ncat <<'EOF'\n" + stdout + "\nEOF\n"
	}

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(scriptPath, 0o755); err != nil {
			t.Fatalf("chmod fake gh: %v", err)
		}
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gateTestContainsIgnoreCase checks if haystack contains needle (case-insensitive)
func gateTestContainsIgnoreCase(haystack, needle string) bool {
	return gateTestContains(gateTestLowerCase(haystack), gateTestLowerCase(needle))
}

func gateTestContains(s, substr string) bool {
	return len(s) >= len(substr) && gateTestFindSubstring(s, substr) >= 0
}

func gateTestLowerCase(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func gateTestFindSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
