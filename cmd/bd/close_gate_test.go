package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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
			err := checkGateSatisfaction(tt.issue, nil)
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

	err := checkGateSatisfaction(issue, nil)
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

	err := checkGateSatisfaction(issue, nil)
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

	err := checkGateSatisfaction(issue, nil)
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

	err := checkGateSatisfaction(issue, nil)
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
	issue := &types.Issue{
		IssueType: "gate",
		AwaitType: "bead",
		AwaitID:   "bd-abc",
		Title:     "Bead gate on an unreadable store",
	}

	err := checkGateSatisfaction(issue, &unreadableGateStore{err: errors.New("dolt exploded")})
	if err == nil {
		t.Fatal("checkGateSatisfaction() let a bead gate close although its store could not be read")
	}
	for _, want := range []string{"could not check bead gate", "dolt exploded", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestCheckGateSatisfaction_BeadGateNeedsASighting(t *testing.T) {
	// The close pre-check applies bd gate check's rule: an awaited bead the
	// gate's store does not have satisfies the gate only if an earlier check
	// saw it there, so a gate on a typo'd await_id cannot be closed past. Nor
	// can a gate that a rename moved on after bd close read it.
	withBeadGateTown(t)
	gateStore := &beadGateLocalStore{issues: map[string]*types.Issue{}}

	for _, awaitID := range []string{"bd-typo123", "other:ot-typo123"} {
		gate := &types.Issue{ID: "bd-gate", IssueType: "gate", AwaitType: "bead", AwaitID: awaitID}
		gateStore.issues[gate.ID] = gate
		err := checkGateSatisfaction(gate, gateStore)
		if err == nil {
			t.Fatalf("%s: checkGateSatisfaction() let a gate close on a bead no check ever saw", awaitID)
		}
		for _, want := range []string{"no earlier gate check saw it", "--force"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error should mention %q, got: %v", awaitID, want, err)
			}
		}

		gate.Metadata = json.RawMessage(`{"await_seen":"` + awaitID + `"}`)
		if err := checkGateSatisfaction(gate, gateStore); err != nil {
			t.Errorf("%s: a seen bead that is gone should satisfy the gate, got: %v", awaitID, err)
		}

		moved := *gate
		moved.AwaitID = awaitID + "-renamed"
		gateStore.issues[gate.ID] = &moved
		err = checkGateSatisfaction(gate, gateStore)
		if err == nil || !strings.Contains(err.Error(), "the gate changed") {
			t.Errorf("%s: a gate moved on since it was read should not be closed past, got: %v", awaitID, err)
		}
	}
}

func TestCloseDirectPreflight_BeadGateReadsTheGatesOwnStore(t *testing.T) {
	// A gate reached through a route is checked against the store that owns
	// it, as bd gate check in its rig would. The launcher's store does not
	// have the awaited bead: checking there would read the open bead as
	// deleted and, the gate's sighting being recorded, let the close through.
	saveAndRestoreGlobals(t)
	withBeadGateTown(t)
	store = &beadGateLocalStore{issues: map[string]*types.Issue{}}

	for _, tt := range []struct {
		name        string
		target      types.Status
		wantRefusal string
	}{
		{name: "awaited bead open in the gate's rig", target: types.StatusOpen, wantRefusal: "bead rg-target is open"},
		{name: "awaited bead closed in the gate's rig", target: types.StatusClosed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				ID:        "rg-gate",
				Status:    types.StatusOpen,
				IssueType: "gate",
				AwaitType: "bead",
				AwaitID:   "rg-target",
				Metadata:  json.RawMessage(`{"await_seen":"rg-target"}`),
			}
			gateStore := &beadGateLocalStore{issues: map[string]*types.Issue{
				"rg-target": {ID: "rg-target", Status: tt.target},
			}}
			results := []*RoutedResult{{Issue: gate, Store: gateStore, Routed: true, ResolvedID: gate.ID}}

			plan := closeDirectPreflight(results, []string{gate.ID}, []string{"done"}, false)
			if tt.wantRefusal == "" {
				if plan.refusals[0] != "" || len(plan.items) != 1 {
					t.Fatalf("close refused (%q), want it to go to the batch", plan.refusals[0])
				}
				if plan.items[0].store != gateStore {
					t.Error("the close does not go to the gate's own store")
				}
				return
			}
			if len(plan.items) != 0 {
				t.Fatalf("the gate went to the batch, want a refusal mentioning %q", tt.wantRefusal)
			}
			if !strings.Contains(plan.refusals[0], tt.wantRefusal) {
				t.Errorf("refusal %q does not mention %q", plan.refusals[0], tt.wantRefusal)
			}
		})
	}
}

// The close pre-check reads a bead gate's target through the route that can
// serve it: the proxied route has no local store, so it must not build a
// store-backed getter there (#5861), and the direct route reads from the
// store that owns the gate.
func TestCloseBeadGateGetter_RouteSelection(t *testing.T) {
	oldMode := proxiedServerMode
	t.Cleanup(func() { proxiedServerMode = oldMode })
	gateStore := &beadGateLocalStore{}

	proxiedServerMode = true
	if _, ok := closeBeadGateGetter(gateStore).(proxiedFreshReadGetter); !ok {
		t.Errorf("proxied-server mode: got %T, want proxiedFreshReadGetter", closeBeadGateGetter(gateStore))
	}

	proxiedServerMode = false
	getter, ok := closeBeadGateGetter(gateStore).(routedBeadGateGetter)
	if !ok {
		t.Fatalf("direct mode: got %T, want routedBeadGateGetter", closeBeadGateGetter(gateStore))
	}
	if getter.localStore != gateStore {
		t.Error("direct mode: the getter does not read the gate's own store")
	}
}

// TestCloseCheckOne_CloseGuardsOutrankGateSatisfaction pins `bd close`'s
// refusal order on the direct preflight route (closeDirectCheckOne): the close
// guards (storeissueops.CheckClosable, the role's own rule) answer before gate
// satisfaction, so a pinned, held or template gate whose condition is unmet
// prints the guard's sentence, as it always has. Force waives the pin and the
// holder (and the gate check with them) but never the template, and a row
// already closed skips the guards (ga-ktn9pe.4.8). closeProxiedCheckOne makes
// the same call in the same order; it resolves the row through a unit of work,
// so this test does not drive it.
func TestCloseCheckOne_CloseGuardsOutrankGateSatisfaction(t *testing.T) {
	actorMu.Lock()
	savedActor, savedPending := actor, actorGitFallbackPending
	actor, actorGitFallbackPending = "alice", false
	actorMu.Unlock()
	t.Cleanup(func() {
		actorMu.Lock()
		actor, actorGitFallbackPending = savedActor, savedPending
		actorMu.Unlock()
	})

	// An unexpired timer gate: checkGateSatisfaction refuses it unforced.
	gate := func(mut func(*types.Issue)) *types.Issue {
		issue := &types.Issue{
			ID:        "bd-g1",
			IssueType: "gate",
			AwaitType: "timer",
			Status:    types.StatusOpen,
			CreatedAt: time.Now(),
			Timeout:   time.Hour,
		}
		mut(issue)
		return issue
	}
	const gateRefusal = "cannot close bd-g1: gate condition not satisfied"
	cases := []struct {
		name  string
		issue *types.Issue
		force bool
		want  string
	}{
		{"pinned", gate(func(i *types.Issue) { i.Pinned = true }), false,
			"cannot modify pinned issue bd-g1 (use --force to override)"},
		{"pinned status", gate(func(i *types.Issue) { i.Status = types.StatusPinned }), false,
			"cannot modify pinned issue bd-g1 (use --force to override)"},
		{"template", gate(func(i *types.Issue) { i.IsTemplate = true }), false,
			"cannot modify template bd-g1: templates are read-only; use 'bd mol pour' to create a work item"},
		{"forced template", gate(func(i *types.Issue) { i.IsTemplate = true }), true,
			"cannot modify template bd-g1: templates are read-only; use 'bd mol pour' to create a work item"},
		{"held", gate(func(i *types.Issue) { i.Assignee = "bob" }), false,
			`cannot close bd-g1: assignee is "bob", actor is "alice"; reclaim or use --force to override`},
		{"unguarded gate", gate(func(*types.Issue) {}), false, gateRefusal},
		{"forced pin", gate(func(i *types.Issue) { i.Pinned = true; i.Assignee = "bob" }), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := func(route, got string) {
				t.Helper()
				if tc.want == gateRefusal {
					if !strings.HasPrefix(got, gateRefusal) {
						t.Fatalf("%s refusal = %q, want the gate refusal", route, got)
					}
					return
				}
				if got != tc.want {
					t.Fatalf("%s refusal = %q, want %q", route, got, tc.want)
				}
			}
			check("direct", closeDirectCheckOne("bd-g1", tc.issue, nil, tc.force))
		})
	}

	// The already-closed skip: a forced close leaves pinned=true behind, and
	// the plain re-close must reach the engine as the idempotent no-op.
	residue := &types.Issue{ID: "bd-c1", Status: types.StatusClosed, Pinned: true, Assignee: "bob"}
	if got := closeDirectCheckOne("bd-c1", residue, nil, false); got != "" {
		t.Fatalf("closed residue refusal = %q, want none", got)
	}
}
