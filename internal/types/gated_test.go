package types

import "testing"

func gateIssue(id string, status Status) *Issue {
	return &Issue{ID: id, IssueType: TypeGate, Status: status, AwaitType: "human"}
}

func TestGateIsHolding(t *testing.T) {
	tests := []struct {
		name    string
		depType DependencyType
		target  *Issue
		want    bool
	}{
		{"open gate over a blocks edge", DepBlocks, gateIssue("g1", StatusOpen), true},
		{"open gate over conditional-blocks", DepConditionalBlocks, gateIssue("g1", StatusOpen), true},
		// waits-for is NOT the same rule: the is_blocked recompute sends that
		// leg through waitsForGateBlockedSQL (fanout over the target's OPEN
		// parent-child children, or metadata.also_blocks with an open spawner),
		// never the closed/pinned status test. A plain waits-for edge onto an
		// open childless gate leaves the dependent IN bd ready, so decorating it
		// would make the surfaces disagree with readiness.
		{"open gate over waits-for", DepWaitsFor, gateIssue("g1", StatusOpen), false},
		{"closed gate", DepBlocks, gateIssue("g1", StatusClosed), false},
		{"pinned gate", DepBlocks, gateIssue("g1", StatusPinned), false},
		// parent-child is structural: it propagates blockedness through the
		// is_blocked recompute, not as a direct blocking edge. Inheritance is
		// its own question (wy-raudbw) and must not sneak in here.
		{"gate over a parent-child edge", DepParentChild, gateIssue("g1", StatusOpen), false},
		{"gate over a relates-to edge", DepRelatesTo, gateIssue("g1", StatusOpen), false},
		{"open non-gate blocker", DepBlocks, &Issue{ID: "b1", IssueType: TypeTask, Status: StatusOpen}, false},
		{"nil target", DepBlocks, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GateIsHolding(tt.depType, tt.target); got != tt.want {
				t.Errorf("GateIsHolding(%s, %v) = %v, want %v", tt.depType, tt.target, got, tt.want)
			}
		})
	}
}

func TestGatesHoldingSelectsOnlyLiveGates(t *testing.T) {
	deps := []*IssueWithDependencyMetadata{
		{Issue: *gateIssue("open-gate", StatusOpen), DependencyType: DepBlocks},
		{Issue: *gateIssue("closed-gate", StatusClosed), DependencyType: DepBlocks},
		{Issue: Issue{ID: "plain", IssueType: TypeTask, Status: StatusOpen}, DependencyType: DepBlocks},
		{Issue: *gateIssue("second-gate", StatusOpen), DependencyType: DepConditionalBlocks},
		nil,
	}
	gates := GatesHolding(&Issue{ID: "subject", Status: StatusOpen}, deps)
	if len(gates) != 2 {
		t.Fatalf("GatesHolding returned %d gates, want 2: %+v", len(gates), gates)
	}
	if gates[0].ID != "open-gate" || gates[1].ID != "second-gate" {
		t.Errorf("GatesHolding = %s,%s; want open-gate,second-gate in dependency order", gates[0].ID, gates[1].ID)
	}
}

// TestGatesHoldingSubjectStatus pins the SUBJECT-side half of the rule. The
// is_blocked recompute's unmark leg (unmarkAllBlockedSQL) forces is_blocked=0
// for a closed or pinned subject whatever its dependencies say, so `bd ready`
// never withholds such a bead on a gate's account and no surface may claim it
// is gated.
func TestGatesHoldingSubjectStatus(t *testing.T) {
	deps := []*IssueWithDependencyMetadata{
		{Issue: *gateIssue("open-gate", StatusOpen), DependencyType: DepBlocks},
	}
	tests := []struct {
		name    string
		subject *Issue
		want    int
	}{
		{"open subject", &Issue{ID: "s1", Status: StatusOpen}, 1},
		{"in_progress subject", &Issue{ID: "s1", Status: StatusInProgress}, 1},
		{"deferred subject", &Issue{ID: "s1", Status: StatusDeferred}, 1},
		{"closed subject", &Issue{ID: "s1", Status: StatusClosed}, 0},
		{"pinned subject", &Issue{ID: "s1", Status: StatusPinned}, 0},
		{"nil subject", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GatesHolding(tt.subject, deps); len(got) != tt.want {
				t.Errorf("GatesHolding(%v) returned %d gates, want %d", tt.subject, len(got), tt.want)
			}
		})
	}
}

// SubjectCanBeGated is the clause on its own, because the listing route reaches
// it without hydrated dependencies and must ask the SAME question.
func TestSubjectCanBeGated(t *testing.T) {
	for _, tt := range []struct {
		status Status
		want   bool
	}{
		{StatusOpen, true},
		{StatusInProgress, true},
		{StatusBlocked, true},
		{StatusDeferred, true},
		{StatusClosed, false},
		{StatusPinned, false},
	} {
		if got := SubjectCanBeGated(&Issue{ID: "s1", Status: tt.status}); got != tt.want {
			t.Errorf("SubjectCanBeGated(%s) = %v, want %v", tt.status, got, tt.want)
		}
	}
	if SubjectCanBeGated(nil) {
		t.Error("SubjectCanBeGated(nil) = true, want false")
	}
}

func TestGatesHoldingEmpty(t *testing.T) {
	if got := GatesHolding(&Issue{ID: "subject", Status: StatusOpen}, nil); got != nil {
		t.Errorf("GatesHolding(subject, nil) = %v, want nil", got)
	}
}

func TestGateDescriptionRoundTrip(t *testing.T) {
	desc := GateDescription("bd-abc", "Need design review")
	if got := GateReason(desc); got != "Need design review" {
		t.Errorf("GateReason(%q) = %q, want the reason back", desc, got)
	}
	bare := GateDescription("bd-abc", "")
	if got := GateReason(bare); got != "" {
		t.Errorf("GateReason(%q) = %q, want empty", bare, got)
	}
	if got := GateReason("a description written by something else"); got != "" {
		t.Errorf("GateReason on a foreign description = %q, want empty", got)
	}
	// The marker is SHARED (cmd/bd/state.go writes it on state-change events),
	// so this is a text read-back and not a provenance check. Pinned so the
	// doc comment and the behavior cannot drift: any description carrying the
	// marker yields a reason.
	if got := GateReason("Changed state from a to b" + ReasonMarker + "because"); got != "because" {
		t.Errorf("GateReason on a shared-marker description = %q, want %q", got, "because")
	}
}

func TestGateRefsProjection(t *testing.T) {
	if got := GateRefs(nil); got != nil {
		t.Errorf("GateRefs(nil) = %v, want nil so the field stays absent", got)
	}
	gate := &Issue{
		ID:          "g1",
		IssueType:   TypeGate,
		Status:      StatusOpen,
		AwaitType:   "gh:pr",
		Description: GateDescription("bd-abc", "waiting on review"),
	}
	refs := GateRefs([]*Issue{gate, {ID: "g2", IssueType: TypeGate, Status: StatusOpen}})
	if len(refs) != 2 {
		t.Fatalf("GateRefs returned %d refs, want 2", len(refs))
	}
	if refs[0] != (GateRef{ID: "g1", Type: "gh:pr", Reason: "waiting on review"}) {
		t.Errorf("refs[0] = %+v", refs[0])
	}
	// A gate with no await type still names a kind rather than an empty one.
	if refs[1] != (GateRef{ID: "g2", Type: "gate"}) {
		t.Errorf("refs[1] = %+v, want the gate fallback kind and no reason", refs[1])
	}
}
