package issueops

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// These are the parts of issueops.GraphCounter's answer that are decided
// without a database — the request vocabulary and the missing-anchor rule — so
// they are pinned here in milliseconds and the conformance contract is left to
// assert what only a real backend can show.

func TestValidateEdgeCountRequestChecksTheDirectionBeforeAnythingElse(t *testing.T) {
	// The empty request names no ids and no direction. It must refuse on the
	// direction rather than fall through to the empty answer a well-formed
	// id-less request gets, which is the ordering ValidateEdgeCountRequest
	// documents.
	err := ValidateEdgeCountRequest(publicops.EdgeCountRequest{})
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("empty request = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "direction") {
		t.Errorf("empty request refusal = %q, want it to name the direction", err)
	}

	// An empty id would also be a refusal, so a request carrying BOTH faults
	// has to report the direction: a caller told only about the id would fix it
	// and get the same refusal back.
	both := ValidateEdgeCountRequest(publicops.EdgeCountRequest{IDs: []string{""}})
	if !strings.Contains(both.Error(), "direction") {
		t.Errorf("refusal for a request missing both = %q, want it to name the direction", both)
	}
}

func TestValidateEdgeCountRequestRefusals(t *testing.T) {
	for _, test := range []struct {
		name string
		req  publicops.EdgeCountRequest
		want bool
	}{
		{"a bare outbound request", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionOut}, false},
		{"a bare inbound request", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionIn}, false},
		{"a direction outside the set", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirection("both")}, true},
		{"a status on the outbound direction", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionOut, Status: string(types.StatusOpen)}, true},
		{"a status on the inbound direction", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionIn, Status: string(types.StatusOpen)}, false},
		{"an unrecognized status on the inbound direction", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionIn, Status: "never-a-status-here"}, false},
		{"an empty id beside a real one", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionOut, IDs: []string{"bd-1", ""}}, true},
		{"an empty dependency type", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionOut, Types: []types.DependencyType{""}}, true},
		{"an over-long dependency type", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionOut,
			Types:     []types.DependencyType(nil)}, false},
		{"a workspace's own dependency type", publicops.EdgeCountRequest{
			Direction: publicops.EdgeDirectionOut,
			Types:     []types.DependencyType{"workspace-invented-type"}}, false},
	} {
		err := ValidateEdgeCountRequest(test.req)
		if got := errors.Is(err, storage.ErrValidation); got != test.want {
			t.Errorf("%s: ErrValidation = %t (err %v), want %t", test.name, got, err, test.want)
		}
	}
}

func TestValidateEdgeCountRequestRefusesATypeWiderThanTheColumn(t *testing.T) {
	tooWide := types.DependencyType(strings.Repeat("x", types.MaxDependencyTypeLen+1))
	err := ValidateEdgeCountRequest(publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionOut,
		Types:     []types.DependencyType{tooWide},
	})
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("an unstorable dependency type = %v, want ErrValidation", err)
	}
}

func TestFinishEdgeCountReportsAMissingAnchorRatherThanItsOrphanedRows(t *testing.T) {
	// A tally keyed to an anchor no plane holds is orphaned data. Counting it
	// would contradict the Missing flag beside it, which is the whole reason
	// FinishEdgeCount reads the presence set rather than the tally's keys.
	result := FinishEdgeCount(
		[]string{"present-with-edges", "present-bare", "ghost"},
		map[string]struct{}{"present-with-edges": {}, "present-bare": {}},
		map[string]int64{"present-with-edges": 3, "ghost": 9},
	)

	want := []publicops.AnchorEdgeCount{
		{ID: "present-with-edges", Count: 3, Missing: false},
		{ID: "present-bare", Count: 0, Missing: false},
		{ID: "ghost", Count: 0, Missing: true},
	}
	if len(result.Anchors) != len(want) {
		t.Fatalf("anchors = %v, want %v", result.Anchors, want)
	}
	for i := range want {
		if result.Anchors[i] != want[i] {
			t.Errorf("anchor %d = %+v, want %+v", i, result.Anchors[i], want[i])
		}
	}
}

func TestFinishEdgeCountAnswersInAnchorOrderAndNeverNil(t *testing.T) {
	result := FinishEdgeCount(nil, nil, nil)
	if result.Anchors == nil {
		t.Fatal("Anchors is nil for an empty anchor list; the contract promises a non-nil slice")
	}
	if len(result.Anchors) != 0 {
		t.Fatalf("Anchors = %v for an empty anchor list, want none", result.Anchors)
	}

	// The anchor list is the order, not the tally map — whose iteration order Go
	// randomizes on purpose.
	ordered := FinishEdgeCount(
		[]string{"c", "a", "b"},
		map[string]struct{}{"a": {}, "b": {}, "c": {}},
		map[string]int64{"a": 1, "b": 2, "c": 3},
	)
	for i, want := range []string{"c", "a", "b"} {
		if ordered.Anchors[i].ID != want {
			t.Fatalf("anchor %d = %q, want %q (the answer follows the anchor list)", i, ordered.Anchors[i].ID, want)
		}
	}
}

func TestBuildEdgeCountQueryResolvesTheTargetWithoutTheGeneratedColumn(t *testing.T) {
	// Inside an aggregate the pure-Go GMS analyzer can prune the base columns
	// the STORED `depends_on_id` derives from and then fail to resolve it, which
	// is why every count over these tables spells the target out from the base
	// columns. A query that named the generated column would work on one engine
	// and fail on the other. The inbound count goes further (gascity#7666): a
	// COALESCE in the filter position matches no index, so the target predicate
	// is one guarded arm per typed column, each a bare indexed column the arm
	// groups by, re-grouped by anchor outside — a row carrying more than one
	// typed target still counts once, under its precedence column.
	inbound := buildEdgeCountQuery("dependencies", "issues", 2, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionIn,
	})
	if strings.Contains(inbound, "depends_on_id") {
		t.Errorf("the inbound count names the generated column:\n%s", inbound)
	}
	if strings.Contains(inbound, "COALESCE") {
		t.Errorf("the inbound count must not filter on the COALESCE wrapper:\n%s", inbound)
	}
	if strings.Count(inbound, "UNION ALL") != 2 {
		t.Errorf("the inbound count must be the three target arms joined by UNION ALL, got %d joins:\n%s", strings.Count(inbound, "UNION ALL"), inbound)
	}
	for _, want := range []string{
		"SELECT d.depends_on_issue_id AS anchor, COUNT(*) AS n FROM dependencies d WHERE d.depends_on_issue_id IN (?,?) GROUP BY d.depends_on_issue_id",
		"SELECT d.depends_on_wisp_id AS anchor, COUNT(*) AS n FROM dependencies d WHERE d.depends_on_wisp_id IN (?,?) AND d.depends_on_issue_id IS NULL GROUP BY d.depends_on_wisp_id",
		"SELECT d.depends_on_external AS anchor, COUNT(*) AS n FROM dependencies d WHERE d.depends_on_external IN (?,?) AND d.depends_on_issue_id IS NULL AND d.depends_on_wisp_id IS NULL GROUP BY d.depends_on_external",
		"SELECT anchor, SUM(n) AS n FROM (",
	} {
		if !strings.Contains(inbound, want) {
			t.Errorf("the inbound count missing guarded arm %q:\n%s", want, inbound)
		}
	}
	if strings.Contains(inbound, "JOIN") {
		t.Errorf("an unnarrowed inbound count joins the dependent's plane for nothing:\n%s", inbound)
	}

	statusNarrowed := buildEdgeCountQuery("wisp_dependencies", "wisps", 1, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionIn, Status: string(types.StatusOpen),
	})
	if strings.Count(statusNarrowed, "JOIN wisps s ON s.id = d.issue_id") != 3 {
		t.Errorf("every arm of the status-narrowed count must join the DEPENDENT's own plane:\n%s", statusNarrowed)
	}
	if strings.Count(statusNarrowed, "AND s.status = ?") != 3 {
		t.Errorf("every arm of the status-narrowed count must narrow by status:\n%s", statusNarrowed)
	}

	outbound := buildEdgeCountQuery("dependencies", "issues", 3, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionOut,
	})
	if !strings.Contains(outbound, "d.issue_id IN (?,?,?)") {
		t.Errorf("the outbound count is not keyed by source over the whole batch:\n%s", outbound)
	}
	if strings.Contains(outbound, "UNION ALL") {
		t.Errorf("the outbound count anchors on the source column alone and needs no target arms:\n%s", outbound)
	}
}

func TestEdgeCountArgsBindInPlaceholderOrder(t *testing.T) {
	// The inbound query inlines one UNION ALL per target arm, each restating the
	// full placeholder sequence, so the args repeat the ids, the type filter and
	// the status once per arm — three of each, in arm order.
	args := edgeCountArgs([]string{"a", "b"}, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionIn,
		Types:     []types.DependencyType{types.DepBlocks},
		Status:    string(types.StatusOpen),
	})
	perArm := []any{"a", "b", string(types.DepBlocks), string(types.StatusOpen)}
	want := append(append(append([]any{}, perArm...), perArm...), perArm...)
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}

	// The status binds only where the query names it, so an outbound request
	// must not carry a fourth argument its SQL has no placeholder for. The
	// outbound query has a single source-keyed predicate, so its args bind once.
	outbound := edgeCountArgs([]string{"a"}, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionOut,
		Types:     []types.DependencyType{types.DepBlocks},
	})
	if len(outbound) != 2 {
		t.Fatalf("outbound args = %v, want the ids plus the type filter only", outbound)
	}

	// An inbound request with no status still binds the ids and the type filter
	// three times — once per target arm.
	inboundNoStatus := edgeCountArgs([]string{"a"}, publicops.EdgeCountRequest{
		Direction: publicops.EdgeDirectionIn,
		Types:     []types.DependencyType{types.DepBlocks},
	})
	if len(inboundNoStatus) != 6 {
		t.Fatalf("inbound args without a status = %v, want the ids plus the type filter three times", inboundNoStatus)
	}
}
