package issueops

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// TestReclaimReplicaSQL pins the granting-replica predicate: when it is armed,
// what it lets through, and the two ways it disarms. The predicate is the
// whole guard — the snapshot SELECT and the per-row DELETE both append it —
// so its shape is worth a direct test rather than only end-to-end coverage.
func TestReclaimReplicaSQL(t *testing.T) {
	tests := []struct {
		name      string
		filter    types.ReclaimFilter
		localNode string
		wantSQL   bool
		wantArgs  []any
	}{
		{
			name:      "armed on a named node",
			localNode: "laptop",
			wantSQL:   true,
			wantArgs:  []any{"laptop"},
		},
		{
			name:      "disarmed by --any-replica",
			filter:    types.ReclaimFilter{AnyReplica: true},
			localNode: "laptop",
		},
		{
			// A deployment that cannot name itself would otherwise compare
			// every lease against "" — i.e. reclaim ONLY unknown-provenance
			// leases and strand its own. Degrade to the historical behavior.
			name:      "disarmed when this node has no identity",
			localNode: "",
		},
		{
			name:      "--any-replica wins over a named node",
			filter:    types.ReclaimFilter{AnyReplica: true, Labels: []string{"lane-a"}},
			localNode: "mini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := reclaimReplicaSQL(tt.filter, tt.localNode)
			if tt.wantSQL {
				if !strings.Contains(sql, "granted_node") {
					t.Fatalf("sql = %q, want a granted_node predicate", sql)
				}
				// Unknown provenance must stay eligible: the guard protects
				// only leases that POSITIVELY name another replica.
				if !strings.Contains(sql, "= ''") {
					t.Errorf("sql = %q, want unknown provenance ('') to stay eligible", sql)
				}
				if strings.Count(sql, "?") != 1 {
					t.Errorf("sql = %q, want exactly one placeholder", sql)
				}
				if len(args) != 1 || args[0] != tt.wantArgs[0] {
					t.Errorf("args = %v, want %v", args, tt.wantArgs)
				}
				return
			}
			if sql != "" || args != nil {
				t.Errorf("guard armed: sql = %q, args = %v; want disarmed", sql, args)
			}
		})
	}
}

// TestReclaimFilterIsEmptyIgnoresAnyReplica pins that --any-replica is an
// override rather than a scope: a reaper that passes only --any-replica is
// still reporting a GLOBAL sweep, and a supervisor auditing its reclaim log
// must not read it as "narrowed to a partition".
func TestReclaimFilterIsEmptyIgnoresAnyReplica(t *testing.T) {
	if !(types.ReclaimFilter{AnyReplica: true}).IsEmpty() {
		t.Error("ReclaimFilter{AnyReplica: true}.IsEmpty() = false, want true (an override is not a scope)")
	}
	if (types.ReclaimFilter{AnyReplica: true, Labels: []string{"lane-a"}}).IsEmpty() {
		t.Error("a filter with labels reported empty")
	}
}

// TestNodeIDContextOverride pins the test seam that lets one process be two
// replicas. Without an override NodeID falls through to config.NodeID(), which
// is the real machine — an explicitly empty override must NOT fall through,
// since "this node has no identity" is a case the guard has to be able to
// exercise.
func TestNodeIDContextOverride(t *testing.T) {
	ctx := t.Context()
	if got := NodeID(WithNodeID(ctx, "mini")); got != "mini" {
		t.Errorf("NodeID with override = %q, want %q", got, "mini")
	}
	// The empty-override case needs config.NodeID() to be NON-empty, or the
	// assertion is tautological: with both sides "" it passes even if NodeID
	// ignored the context entirely. Give the fallback a value so "" can only
	// come from the override actually winning.
	t.Setenv("BEADS_NODE_ID", "fallback-node")
	if err := config.Initialize(); err != nil {
		t.Fatalf("re-initialize config: %v", err)
	}
	if got := config.NodeID(); got != "fallback-node" {
		t.Fatalf("precondition: config.NodeID() = %q, want %q — the empty-override assertion below would be vacuous", got, "fallback-node")
	}
	if got := NodeID(ctx); got != "fallback-node" {
		t.Errorf("NodeID with no override = %q, want the config fallback %q", got, "fallback-node")
	}
	if got := NodeID(WithNodeID(ctx, "")); got != "" {
		t.Errorf("NodeID with empty override = %q, want %q (must not fall through to config)", got, "")
	}
}
