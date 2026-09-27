package issueops

import (
	"fmt"
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

// TestFormatForeignSkipSummary pins the BOUND on the foreign-skip audit
// (wy-sp2l4 F5). Foreign stale leases are never reclaimed, so this line repeats
// on every reclaim run forever — a supervisor on a 1-minute timer used to print
// one line per stranded lease, per minute, indefinitely. The property that
// matters is that the line's size is set by foreignSkipNamedNodes and not by
// the deployment, while the TOTAL it reports stays exact.
func TestFormatForeignSkipSummary(t *testing.T) {
	t.Run("one replica, one lease reads singular", func(t *testing.T) {
		got := formatForeignSkipSummary([]foreignSkipGroup{{"mini", 1}}, 1, "laptop")
		for _, want := range []string{"skipped 1 stale lease ", "another replica", `"mini" (1)`, `"laptop"`, "--any-replica"} {
			if !strings.Contains(got, want) {
				t.Errorf("summary = %q, want it to contain %q", got, want)
			}
		}
		if strings.Contains(got, "stale leases") {
			t.Errorf("summary = %q, want the singular spelling", got)
		}
		if lines := strings.Count(got, "\n"); lines != 1 {
			t.Errorf("summary spans %d lines, want exactly 1", lines)
		}
	})

	t.Run("names every replica up to the cap", func(t *testing.T) {
		groups := []foreignSkipGroup{{"a", 3}, {"b", 2}, {"c", 1}}
		got := formatForeignSkipSummary(groups, 6, "here")
		if !strings.Contains(got, "other replicas") {
			t.Errorf("summary = %q, want the plural replica spelling", got)
		}
		for _, want := range []string{`"a" (3)`, `"b" (2)`, `"c" (1)`} {
			if !strings.Contains(got, want) {
				t.Errorf("summary = %q, want it to contain %q", got, want)
			}
		}
		if strings.Contains(got, "more replica") {
			t.Errorf("summary = %q, want no collapsed tail at exactly the cap", got)
		}
	})

	t.Run("collapses the tail past the cap and keeps the total exact", func(t *testing.T) {
		groups := []foreignSkipGroup{{"a", 40}, {"b", 4}, {"c", 2}, {"d", 1}, {"e", 1}}
		got := formatForeignSkipSummary(groups, 48, "here")
		if !strings.Contains(got, "skipped 48 stale leases") {
			t.Errorf("summary = %q, want the exact total 48", got)
		}
		if !strings.Contains(got, "and 2 more replicas (2)") {
			t.Errorf("summary = %q, want the tail collapsed into a count", got)
		}
		for _, unwanted := range []string{`"d"`, `"e"`} {
			if strings.Contains(got, unwanted) {
				t.Errorf("summary = %q, want %s collapsed rather than named", got, unwanted)
			}
		}
		if lines := strings.Count(got, "\n"); lines != 1 {
			t.Errorf("summary spans %d lines, want exactly 1", lines)
		}
	})

	t.Run("output size is bounded by the cap, not the deployment", func(t *testing.T) {
		var few, many []foreignSkipGroup
		total := 0
		for i := 0; i < 500; i++ {
			g := foreignSkipGroup{node: fmt.Sprintf("replica-%03d", i), count: 1}
			many = append(many, g)
			if i < foreignSkipNamedNodes {
				few = append(few, g)
			}
			total++
		}
		short := formatForeignSkipSummary(few, foreignSkipNamedNodes, "here")
		long := formatForeignSkipSummary(many, total, "here")
		// The only growth allowed is the collapsed tail and the wider counts.
		if len(long) > len(short)+40 {
			t.Errorf("500 replicas produced %d bytes vs %d for %d — the line grows with the deployment",
				len(long), len(short), foreignSkipNamedNodes)
		}
		if !strings.Contains(long, "and 497 more replicas (497)") {
			t.Errorf("summary = %q, want the 497 unnamed replicas collapsed exactly", long)
		}
	})
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
