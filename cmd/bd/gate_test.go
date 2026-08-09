package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
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

// fakeBeadGateGetter fakes the one lookup checkBeadGate performs.
type fakeBeadGateGetter struct {
	issues map[string]*types.Issue
	err    error
}

func (f *fakeBeadGateGetter) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.issues[id], nil
}

func TestCheckBeadGate_CrossRigStaysPending(t *testing.T) {
	ctx := context.Background()

	// The cross-rig <rig>:<bead-id> form cannot be evaluated since multi-rig
	// routing was removed; it must stay pending with the explanatory message,
	// never consult the store, and never resolve.
	tests := []struct {
		name    string
		awaitID string
	}{
		{name: "missing rig", awaitID: ":gt-abc"},
		{name: "missing bead", awaitID: "my-project:"},
		{name: "well-formed cross-rig", awaitID: "nonexistent:some-id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			satisfied, reason := checkBeadGate(ctx, nil, tt.awaitID)
			if satisfied {
				t.Errorf("expected not satisfied for %q", tt.awaitID)
			}
			if !gateTestContainsIgnoreCase(reason, "multi-rig routing removed") {
				t.Errorf("reason %q does not contain %q", reason, "multi-rig routing removed")
			}
		})
	}
}

func TestCheckBeadGate_EmptyAwaitID(t *testing.T) {
	satisfied, reason := checkBeadGate(context.Background(), nil, "")
	if satisfied {
		t.Error("expected not satisfied for empty await_id")
	}
	if reason == "" {
		t.Error("expected reason to be set")
	}
}

func TestCheckBeadGate_LocalBead(t *testing.T) {
	// A plain (no-colon) await_id is a bead in this rig's own database
	// (wy-hgms2): closed resolves the gate, anything else stays pending with
	// a status-bearing reason.
	ctx := context.Background()
	st := &fakeBeadGateGetter{
		issues: map[string]*types.Issue{
			"bd-closed": {ID: "bd-closed", Status: types.StatusClosed},
			"bd-open":   {ID: "bd-open", Status: types.StatusOpen},
		},
	}

	satisfied, reason := checkBeadGate(ctx, st, "bd-closed")
	if !satisfied {
		t.Errorf("expected satisfied for closed local bead, got reason %q", reason)
	}
	if !gateTestContainsIgnoreCase(reason, "closed") {
		t.Errorf("reason %q does not mention closed", reason)
	}

	satisfied, reason = checkBeadGate(ctx, st, "bd-open")
	if satisfied {
		t.Error("expected not satisfied for open local bead")
	}
	if !gateTestContainsIgnoreCase(reason, "open") {
		t.Errorf("reason %q does not mention the bead status", reason)
	}
}

func TestCheckBeadGate_LocalBeadNotFound(t *testing.T) {
	st := &fakeBeadGateGetter{issues: map[string]*types.Issue{}}
	satisfied, reason := checkBeadGate(context.Background(), st, "bd-missing")
	if satisfied {
		t.Error("expected not satisfied for missing local bead")
	}
	if !gateTestContainsIgnoreCase(reason, "not found") {
		t.Errorf("reason %q does not mention not found", reason)
	}
}

func TestCheckBeadGate_LocalBeadLookupError(t *testing.T) {
	st := &fakeBeadGateGetter{err: errors.New("dolt exploded")}
	satisfied, reason := checkBeadGate(context.Background(), st, "bd-abc")
	if satisfied {
		t.Error("expected not satisfied on lookup error")
	}
	if !gateTestContainsIgnoreCase(reason, "dolt exploded") {
		t.Errorf("reason %q does not carry the lookup error", reason)
	}
}

func TestCheckBeadGate_NilStoreStaysPending(t *testing.T) {
	satisfied, reason := checkBeadGate(context.Background(), nil, "bd-abc")
	if satisfied {
		t.Error("expected not satisfied with no store")
	}
	if reason == "" {
		t.Error("expected reason to be set")
	}
}

func TestCheckGHPRUsesStateWithoutMergedField(t *testing.T) {
	resolved, escalated, reason, err := checkGHPRWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:pr",
		AwaitID:   "3488",
	}, fakeGHRunner(t,
		`{"state":"MERGED","title":"Fix gate"}`,
		"pr", "view", "3488", "--json", "state,title",
	))
	if err != nil {
		t.Fatalf("checkGHPR returned error: %v", err)
	}
	if !resolved {
		t.Fatal("expected merged PR to resolve")
	}
	if escalated {
		t.Fatal("did not expect merged PR to escalate")
	}
	if !gateTestContains(reason, "was merged") {
		t.Fatalf("reason = %q, want merged message", reason)
	}
}

func TestCheckGHPRUsesRepositoryFromMetadata(t *testing.T) {
	resolved, escalated, reason, err := checkGHPRWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:pr",
		AwaitID:   "608",
		Metadata:  json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
	}, fakeGHRunner(t,
		`{"state":"MERGED","title":"Cross-repo gate"}`,
		"pr", "view", "608", "--json", "state,title", "--repo", "srobroek/agentic-packages",
	))
	if err != nil {
		t.Fatalf("checkGHPR returned error: %v", err)
	}
	if !resolved || escalated {
		t.Fatalf("resolved, escalated = %v, %v; want true, false (%s)", resolved, escalated, reason)
	}
}

func TestCheckGHRunUsesRepositoryFromMetadata(t *testing.T) {
	resolved, escalated, reason, err := checkGHRunWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:run",
		AwaitID:   "12345",
		Metadata:  json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
	}, nil,
		fakeGHRunner(t,
			`{"status":"completed","conclusion":"success","name":"CI"}`,
			"run", "view", "12345", "--json", "status,conclusion,name", "--repo", "srobroek/agentic-packages",
		),
	)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if !resolved || escalated {
		t.Fatalf("resolved, escalated = %v, %v; want true, false (%s)", resolved, escalated, reason)
	}
}

// TestCheckGHRun_CrossRepoDiscoveryUsesInjectedRunner covers the standards
// note on the SF1 review: discoverRunIDByWorkflowNameInRepo was hard-wired to
// runGHCommand, so the cross-repo discovery path (a workflow-name hint plus
// metadata.repo) could not be exercised through the injected ghCommandRunner
// seam at all. Both the discovery "run list" call and the follow-up "run
// view" call must go through the same fake runner - if either one reached
// the real runGHCommand this test would fail (or hang) instead of using the
// canned response below.
func TestCheckGHRun_CrossRepoDiscoveryUsesInjectedRunner(t *testing.T) {
	var calls [][]string
	fakeRunner := func(args ...string) (stdout, stderr []byte, err error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "run":
			if len(args) > 1 && args[1] == "list" {
				return []byte(`[{"databaseId":999,"name":"release","status":"completed","conclusion":"success","workflowName":"release.yml"}]`), nil, nil
			}
			if len(args) > 1 && args[1] == "view" {
				return []byte(`{"status":"completed","conclusion":"success","name":"CI"}`), nil, nil
			}
		}
		t.Fatalf("unexpected gh invocation: %v", args)
		return nil, nil, nil
	}

	resolved, escalated, reason, err := checkGHRunWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:run",
		AwaitID:   "release.yml",
		Metadata:  json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
	}, nil, fakeRunner)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if !resolved || escalated {
		t.Fatalf("resolved, escalated = %v, %v; want true, false (%s)", resolved, escalated, reason)
	}

	wantCalls := [][]string{
		{"run", "list", "--workflow", "release.yml", "--json", "databaseId,name,status,conclusion,createdAt,workflowName", "--limit", "5", "--repo", "srobroek/agentic-packages"},
		{"run", "view", "999", "--json", "status,conclusion,name", "--repo", "srobroek/agentic-packages"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("gh invocations = %v, want %v", calls, wantCalls)
	}
	for i, want := range wantCalls {
		if !slices.Equal(calls[i], want) {
			t.Errorf("gh invocation %d = %v, want %v", i, calls[i], want)
		}
	}
}

func TestQueryGitHubRunsForWorkflowUsesRepository(t *testing.T) {
	runs, err := queryGitHubRunsForWorkflowInRepoWithRunner(
		"release.yml",
		5,
		"srobroek/agentic-packages",
		fakeGHRunner(t,
			`[{"databaseId":12345,"name":"release","status":"completed","conclusion":"success","workflowName":"release.yml"}]`,
			"run", "list", "--workflow", "release.yml", "--json", "databaseId,name,status,conclusion,createdAt,workflowName", "--limit", "5", "--repo", "srobroek/agentic-packages",
		),
	)
	if err != nil {
		t.Fatalf("queryGitHubRunsForWorkflowInRepo returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].DatabaseID != 12345 {
		t.Fatalf("runs = %#v, want one run with database ID 12345", runs)
	}
}

func TestGitHubRepoFromIssueRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"missing_owner", json.RawMessage(`{"repo":"missing-owner"}`)},
		{"shell_metacharacter", json.RawMessage(`{"repo":"owner/repo;echo"}`)},
		{"metadata_not_an_object", json.RawMessage(`"not-an-object"`)},
		// SF3: an explicit JSON null must be rejected rather than silently
		// falling back to the current repository - the dangerous direction,
		// since it could point a cross-repo check at the wrong repo.
		{"repo_null", json.RawMessage(`{"repo":null}`)},
		{"repo_number", json.RawMessage(`{"repo":42}`)},
		{"repo_bool", json.RawMessage(`{"repo":true}`)},
		{"repo_object", json.RawMessage(`{"repo":{"owner":"a","name":"b"}}`)},
		{"repo_array", json.RawMessage(`{"repo":["a","b"]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if repo, err := githubRepoFromIssue(&types.Issue{Metadata: tt.metadata}); err == nil {
				t.Fatalf("githubRepoFromIssue(%s) = %q, nil; want validation error", tt.metadata, repo)
			}
		})
	}
}

// TestGitHubRepoFromIssueAllowsMissingRepoKey verifies metadata without a
// "repo" key at all (as opposed to an explicit null) still falls back to the
// current repository without error - only an explicit malformed value is
// rejected (SF3).
func TestGitHubRepoFromIssueAllowsMissingRepoKey(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"nil_metadata", nil},
		{"null_metadata", json.RawMessage(`null`)},
		{"empty_object", json.RawMessage(`{}`)},
		{"unrelated_key", json.RawMessage(`{"priority":"high"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, err := githubRepoFromIssue(&types.Issue{Metadata: tt.metadata})
			if err != nil {
				t.Fatalf("githubRepoFromIssue(%s) returned error: %v", tt.metadata, err)
			}
			if repo != "" {
				t.Fatalf("githubRepoFromIssue(%s) = %q, want empty", tt.metadata, repo)
			}
		})
	}
}

// TestRepoMetadataForGateRestrictsToGitHubTypes covers SF4: repo metadata
// inheritance/validation must only run for gh:* gate types. A human or timer
// gate blocking an issue with non-GitHub-shaped "repo" metadata (legal per
// the metadata contract - "any valid JSON") must not fail gate creation.
func TestRepoMetadataForGateRestrictsToGitHubTypes(t *testing.T) {
	badRepoMetadata := json.RawMessage(`{"repo":"not-owner-slash-repo"}`)

	nonGitHubTypes := []string{"human", "timer", "bead"}
	for _, gateType := range nonGitHubTypes {
		t.Run("ignores_bad_repo_metadata_for_"+gateType, func(t *testing.T) {
			metadata, err := repoMetadataForGate(gateType, &types.Issue{Metadata: badRepoMetadata})
			if err != nil {
				t.Fatalf("repoMetadataForGate(%q) returned error: %v; non-GitHub gates must tolerate arbitrary repo metadata", gateType, err)
			}
			if metadata != nil {
				t.Fatalf("repoMetadataForGate(%q) = %s, want nil metadata", gateType, metadata)
			}
		})
	}

	githubTypes := []string{"gh:run", "gh:pr"}
	for _, gateType := range githubTypes {
		t.Run("rejects_bad_repo_metadata_for_"+gateType, func(t *testing.T) {
			if _, err := repoMetadataForGate(gateType, &types.Issue{Metadata: badRepoMetadata}); err == nil {
				t.Fatalf("repoMetadataForGate(%q) = nil error, want validation error", gateType)
			}
		})

		t.Run("inherits_valid_repo_for_"+gateType, func(t *testing.T) {
			metadata, err := repoMetadataForGate(gateType, &types.Issue{
				Metadata: json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
			})
			if err != nil {
				t.Fatalf("repoMetadataForGate(%q) returned error: %v", gateType, err)
			}
			var decoded struct {
				Repo string `json:"repo"`
			}
			if unmarshalErr := json.Unmarshal(metadata, &decoded); unmarshalErr != nil {
				t.Fatalf("repoMetadataForGate(%q) = %s, not valid JSON: %v", gateType, metadata, unmarshalErr)
			}
			if decoded.Repo != "srobroek/agentic-packages" {
				t.Fatalf("repoMetadataForGate(%q) repo = %q, want srobroek/agentic-packages", gateType, decoded.Repo)
			}
		})
	}

	t.Run("no_metadata_no_repo", func(t *testing.T) {
		metadata, err := repoMetadataForGate("gh:run", &types.Issue{})
		if err != nil {
			t.Fatalf("repoMetadataForGate(gh:run) returned error: %v", err)
		}
		if metadata != nil {
			t.Fatalf("repoMetadataForGate(gh:run) = %s, want nil", metadata)
		}
	})
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
	}, nil)
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
	}, func(gateID, runID string) error { return updateGateAwaitIDFunc(nil, gateID, runID) })
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
	}, func(gateID, runID string) error { return updateGateAwaitIDFunc(nil, gateID, runID) })
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
	resolved, escalated, reason, err := checkGHRunStatusInRepoWithRunner(
		"12345",
		"",
		fakeGHRunner(t,
			`{"status":"completed","conclusion":"success","name":"release"}`,
			"run", "view", "12345", "--json", "status,conclusion,name",
		),
	)
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
				if err := gateCheckCmd.RunE(gateCheckCmd, nil); err != nil {
					t.Fatalf("gateCheckCmd.RunE: %v", err)
				}
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

func TestCheckGHPR_StateHandling(t *testing.T) {
	tests := []struct {
		name           string
		ghJSON         string
		wantResolved   bool
		wantEscalated  bool
		reasonContains string
	}{
		{
			name:           "MERGED resolves gate",
			ghJSON:         `{"state":"MERGED","title":"Add feature X"}`,
			wantResolved:   true,
			wantEscalated:  false,
			reasonContains: "was merged",
		},
		{
			name:           "CLOSED escalates without merge",
			ghJSON:         `{"state":"CLOSED","title":"Stale PR"}`,
			wantResolved:   false,
			wantEscalated:  true,
			reasonContains: "closed without merging",
		},
		{
			name:           "OPEN leaves gate pending",
			ghJSON:         `{"state":"OPEN","title":"WIP"}`,
			wantResolved:   false,
			wantEscalated:  false,
			reasonContains: "still open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitID: "https://github.com/org/repo/pull/1"}
			resolved, escalated, reason, err := checkGHPRWithRunner(gate, fakeGHRunner(t,
				tt.ghJSON,
				"pr", "view", gate.AwaitID, "--json", "state,title",
			))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != tt.wantResolved {
				t.Errorf("resolved = %v, want %v", resolved, tt.wantResolved)
			}
			if escalated != tt.wantEscalated {
				t.Errorf("escalated = %v, want %v", escalated, tt.wantEscalated)
			}
			if !gateTestContainsIgnoreCase(reason, tt.reasonContains) {
				t.Errorf("reason %q does not contain %q", reason, tt.reasonContains)
			}
		})
	}
}

func TestCheckGHPR_NoMergedFieldRequested(t *testing.T) {
	gate := &types.Issue{AwaitID: "https://github.com/org/repo/pull/99"}
	resolved, _, reason, err := checkGHPRWithRunner(gate, fakeGHRunner(t,
		`{"state":"MERGED","title":"Test PR"}`,
		"pr", "view", gate.AwaitID, "--json", "state,title",
	))
	if err != nil {
		t.Fatalf("checkGHPR failed (likely requested 'merged' field): %v", err)
	}
	if !resolved {
		t.Errorf("expected resolved=true for MERGED state")
	}
	if !gateTestContainsIgnoreCase(reason, "was merged") {
		t.Errorf("reason %q should contain 'was merged'", reason)
	}
}

func fakeGHRunner(t *testing.T, stdout string, wantArgs ...string) ghCommandRunner {
	t.Helper()
	return func(args ...string) ([]byte, []byte, error) {
		t.Helper()
		if !slices.Equal(args, wantArgs) {
			t.Fatalf("gh arguments = %q, want %q", args, wantArgs)
		}
		return []byte(stdout), nil, nil
	}
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

// TestFilterIssueGates covers the bead-scoping helper behind `bd gate list <issue-id>`:
// only gate-type dependencies are returned, --all controls closed visibility, and the
// limit is honored. Regression guard for the bug where `bd gate list <bead>` silently
// ignored the argument and returned the DB-wide gate list.
func TestFilterIssueGates(t *testing.T) {
	gate := types.IssueType("gate")
	task := types.IssueType("task")
	deps := []*types.Issue{
		{ID: "g-open", IssueType: gate, Status: types.StatusOpen},
		{ID: "g-closed", IssueType: gate, Status: types.StatusClosed},
		{ID: "t-blocker", IssueType: task, Status: types.StatusOpen}, // not a gate
		nil, // defensive: skipped
		{ID: "g-open2", IssueType: gate, Status: types.StatusOpen},
	}

	t.Run("open_only_excludes_closed_and_nongates", func(t *testing.T) {
		got := filterIssueGates(deps, false, 0)
		ids := gateIDs(got)
		if len(got) != 2 || ids[0] != "g-open" || ids[1] != "g-open2" {
			t.Fatalf("expected [g-open g-open2], got %v", ids)
		}
	})

	t.Run("all_includes_closed_gates_only", func(t *testing.T) {
		got := filterIssueGates(deps, true, 0)
		ids := gateIDs(got)
		if len(got) != 3 {
			t.Fatalf("expected 3 gates (incl. closed), got %v", ids)
		}
		for _, id := range ids {
			if id == "t-blocker" {
				t.Fatalf("non-gate dependency leaked into result: %v", ids)
			}
		}
	})

	t.Run("limit_caps_results", func(t *testing.T) {
		got := filterIssueGates(deps, true, 1)
		if len(got) != 1 || got[0].ID != "g-open" {
			t.Fatalf("expected limit=1 -> [g-open], got %v", gateIDs(got))
		}
	})

	t.Run("empty_deps", func(t *testing.T) {
		if got := filterIssueGates(nil, true, 0); len(got) != 0 {
			t.Fatalf("expected no gates, got %v", gateIDs(got))
		}
	})
}

func gateIDs(gs []*types.Issue) []string {
	ids := make([]string, 0, len(gs))
	for _, g := range gs {
		ids = append(ids, g.ID)
	}
	return ids
}
