package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestIsMachineCheckableGate(t *testing.T) {
	tests := []struct {
		name  string
		issue *types.Issue
		want  bool
	}{
		{
			name:  "nil issue",
			issue: nil,
			want:  false,
		},
		{
			name: "non-gate issue",
			issue: &types.Issue{
				IssueType: "task",
			},
			want: false,
		},
		{
			name: "gate with human await type",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "human",
			},
			want: false,
		},
		{
			name: "gate with gh:pr await type",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "gh:pr",
			},
			want: true,
		},
		{
			name: "gate with gh:run await type",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "gh:run",
			},
			want: true,
		},
		{
			name: "gate with timer await type",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "timer",
			},
			want: true,
		},
		{
			name: "gate with bead await type",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "bead",
			},
			want: true,
		},
		{
			name: "gate with empty await type",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMachineCheckableGate(tt.issue)
			if got != tt.want {
				t.Errorf("isMachineCheckableGate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckGateSatisfaction_NonGateIssues(t *testing.T) {
	// Non-gate issues should always pass (return nil)
	tests := []struct {
		name  string
		issue *types.Issue
	}{
		{
			name:  "nil issue",
			issue: nil,
		},
		{
			name: "task issue",
			issue: &types.Issue{
				IssueType: "task",
				Title:     "Regular task",
			},
		},
		{
			name: "bug issue",
			issue: &types.Issue{
				IssueType: "bug",
				Title:     "A bug",
			},
		},
		{
			name: "gate with human await (not machine-checkable)",
			issue: &types.Issue{
				IssueType: "gate",
				AwaitType: "human",
				Title:     "Human gate",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGateSatisfaction(tt.issue)
			if err != nil {
				t.Errorf("checkGateSatisfaction() returned error for non-machine-checkable issue: %v", err)
			}
		})
	}
}

func TestCheckGateSatisfaction_GHPRWithoutAwaitID(t *testing.T) {
	// gh:pr gate without an await_id is unsatisfied (no PR to check)
	issue := &types.Issue{
		IssueType: "gate",
		AwaitType: "gh:pr",
		AwaitID:   "",
		Title:     "PR gate without ID",
	}

	err := checkGateSatisfaction(issue)
	if err == nil {
		t.Error("checkGateSatisfaction() should return error for gh:pr gate without await_id")
	}
	if err != nil && !strings.Contains(err.Error(), "no PR number") {
		t.Errorf("error should mention 'no PR number', got: %v", err)
	}
}

func TestCheckGateSatisfaction_GHRunWithoutAwaitID(t *testing.T) {
	// gh:run gate without an await_id is unsatisfied (no run to check)
	issue := &types.Issue{
		IssueType: "gate",
		AwaitType: "gh:run",
		AwaitID:   "",
		Title:     "Run gate without ID",
	}

	err := checkGateSatisfaction(issue)
	if err == nil {
		t.Error("checkGateSatisfaction() should return error for gh:run gate without await_id")
	}
	if err != nil && !strings.Contains(err.Error(), "no run ID") {
		t.Errorf("error should mention 'no run ID', got: %v", err)
	}
}

func TestCheckGateSatisfaction_BeadGateInvalidFormat(t *testing.T) {
	// bead gate with invalid await_id should return an error
	issue := &types.Issue{
		IssueType: "gate",
		AwaitType: "bead",
		AwaitID:   "invalid-no-colon",
		Title:     "Bead gate with bad format",
	}

	err := checkGateSatisfaction(issue)
	if err == nil {
		t.Error("checkGateSatisfaction() should return error for bead gate with invalid await_id format")
	}
}

func TestCheckGateSatisfaction_ErrorMessageFormat(t *testing.T) {
	// Verify error messages contain the force override hint
	issue := &types.Issue{
		IssueType: "gate",
		AwaitType: "bead",
		AwaitID:   "invalid-no-colon",
		Title:     "Test gate",
	}

	err := checkGateSatisfaction(issue)
	if err == nil {
		t.Fatal("expected error")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "--force") {
		t.Errorf("error message should mention --force, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "gate condition not satisfied") {
		t.Errorf("error message should mention 'gate condition not satisfied', got: %s", errMsg)
	}
}

// unreadableGateStore is a DoltStorage stand-in whose GetIssue always fails
// with err. It embeds a nil DoltStorage and overrides only the read the bead
// arm of checkGateSatisfaction makes.
type unreadableGateStore struct {
	storage.DoltStorage
	err error
}

func (s *unreadableGateStore) GetIssue(context.Context, string) (*types.Issue, error) {
	return nil, s.err
}

func TestCheckGateSatisfaction_BeadGateUnreadableStoreRefuses(t *testing.T) {
	// A bead gate whose store read fails (anything but not-found) must refuse
	// the close with the read error, not fall through to the fail-open
	// warning the gh:* and timer arms use.
	saveAndRestoreGlobals(t)
	store = &unreadableGateStore{err: errors.New("dolt exploded")}

	issue := &types.Issue{
		IssueType: "gate",
		AwaitType: "bead",
		AwaitID:   "bd-abc",
		Title:     "Bead gate on an unreadable store",
	}

	err := checkGateSatisfaction(issue)
	if err == nil {
		t.Fatal("checkGateSatisfaction() let a bead gate close although its store could not be read")
	}
	for _, want := range []string{"could not check bead gate", "dolt exploded", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// The close pre-check reads a bead gate's target through the route that can
// serve it: the proxied route has no local store, so it must not build a
// store-backed getter there (#5861).
func TestCloseBeadGateGetter_RouteSelection(t *testing.T) {
	oldMode := proxiedServerMode
	t.Cleanup(func() { proxiedServerMode = oldMode })

	proxiedServerMode = true
	if _, ok := closeBeadGateGetter().(proxiedFreshReadGetter); !ok {
		t.Errorf("proxied-server mode: got %T, want proxiedFreshReadGetter", closeBeadGateGetter())
	}

	proxiedServerMode = false
	if _, ok := closeBeadGateGetter().(routedBeadGateGetter); !ok {
		t.Errorf("direct mode: got %T, want routedBeadGateGetter", closeBeadGateGetter())
	}
}
