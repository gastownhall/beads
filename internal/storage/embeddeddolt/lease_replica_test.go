//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestReclaimRefusesForeignReplicaLease is the wy-jpd3.7 acceptance: a lease
// granted by ANOTHER replica is not reclaimable here, however stale it looks,
// because this node's view of that holder's liveness is stale by up to one
// sync interval. --any-replica is the deliberate override.
//
// One process plays two replicas via issueops.WithNodeID; the lease row that a
// federated deployment materializes locally (RestoreLeaseOnImportInTx, on the
// JSONL interchange) is stood in for by claiming under the remote node's
// identity, which produces the same row shape.
func TestReclaimRefusesForeignReplicaLease(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "leasereplica")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "leasereplica-1",
		Title:     "claimed on the other machine",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Replica "mini" grants the lease. Staleness comes from a NEGATIVE grace
	// window below (cutoff in the future), not from a short TTL plus a sleep:
	// a real-clock race between TTL and test latency is the flake this suite
	// does not need.
	miniCtx := issueops.WithNodeID(ctx, "mini")
	if err := te.store.ClaimIssue(miniCtx, "leasereplica-1", "alice"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	var grantedNode string
	te.queryScalar(t, ctx, "SELECT granted_node FROM leases WHERE issue_id = ?",
		[]any{"leasereplica-1"}, &grantedNode)
	if grantedNode != "mini" {
		t.Fatalf("granted_node = %q after claim, want %q", grantedNode, "mini")
	}

	// The laptop's reaper sees a stale lease and must decline it.
	laptopCtx := issueops.WithNodeID(ctx, "laptop")
	reclaimed, err := te.store.ReclaimExpiredLeases(laptopCtx, -time.Hour, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases from laptop: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("laptop reclaimed %+v, want nothing — the lease was granted by mini", reclaimed)
	}
	got, err := te.store.GetIssue(ctx, "leasereplica-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Status != types.StatusInProgress || got.Assignee != "alice" {
		t.Fatalf("issue = (%s, %q) after a declined reclaim, want (in_progress, alice)", got.Status, got.Assignee)
	}
	// The lease row itself must survive: a declined reclaim leaves nothing
	// half-torn-down, so the holder can keep heartbeating on the replica that
	// granted it. (This does NOT exercise the guard's copy on the per-row
	// DELETE — inside one transaction nothing can flip granted_node between
	// the snapshot and the DELETE, so that copy is defense in depth against a
	// future caller that re-checks by id, not a separately reachable branch.)
	var leaseRows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", []any{"leasereplica-1"}, &leaseRows)
	if leaseRows != 1 {
		t.Fatalf("lease rows = %d after a declined reclaim, want 1", leaseRows)
	}
	if got.LeaseGrantedNode != "mini" {
		t.Errorf("hydrated LeaseGrantedNode = %q, want %q", got.LeaseGrantedNode, "mini")
	}

	// The granting replica reaps it fine.
	reclaimed, err = te.store.ReclaimExpiredLeases(miniCtx, -time.Hour, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases from mini: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "leasereplica-1" {
		t.Fatalf("mini reclaimed = %+v, want [{leasereplica-1 alice}]", reclaimed)
	}
}

// TestReclaimAnyReplicaOverride pins the escape hatch: when the granting
// replica is gone for good, --any-replica reverts its stale leases anyway.
func TestReclaimAnyReplicaOverride(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "leaseoverride")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "leaseoverride-1",
		Title:     "stranded by a departed replica",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	miniCtx := issueops.WithNodeID(ctx, "mini")
	if err := te.store.ClaimIssue(miniCtx, "leaseoverride-1", "alice"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	laptopCtx := issueops.WithNodeID(ctx, "laptop")
	reclaimed, err := te.store.ReclaimExpiredLeases(laptopCtx, -time.Hour, types.ReclaimFilter{AnyReplica: true}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases --any-replica: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "leaseoverride-1" || reclaimed[0].PreviousOwner != "alice" {
		t.Fatalf("reclaimed = %+v, want [{leaseoverride-1 alice}]", reclaimed)
	}
	got, err := te.store.GetIssue(ctx, "leaseoverride-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Status != types.StatusOpen || got.Assignee != "" {
		t.Errorf("issue = (%s, %q) after --any-replica reclaim, want (open, \"\")", got.Status, got.Assignee)
	}
}

// TestUnknownProvenanceLeaseStaysReclaimable pins the fail-open half of the
// guard, which is what makes this upgrade safe: a lease row whose granting
// replica is unknown — a row granted before the column existed, or granted by
// a deployment that cannot name its replicas — must remain reclaimable, or the
// upgrade itself would strand every in-flight claim's recovery. A heartbeat
// through this node backfills the identity (and never overwrites a known one).
func TestUnknownProvenanceLeaseStaysReclaimable(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "leaselegacy")
	ctx := t.Context()

	for _, id := range []string{"leaselegacy-1", "leaselegacy-2"} {
		issue := &types.Issue{
			ID:        id,
			Title:     "legacy lease row",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}

	// An anonymous replica: NodeID resolves to "", exactly what a pre-0016
	// lease row carries.
	anonCtx := issueops.WithNodeID(ctx, "")
	for _, id := range []string{"leaselegacy-1", "leaselegacy-2"} {
		if err := te.store.ClaimIssue(anonCtx, id, "alice"); err != nil {
			t.Fatalf("ClaimIssue(%s): %v", id, err)
		}
	}

	// A heartbeat from a named node adopts the orphan lease.
	laptopHeartbeat := issueops.WithNodeID(ctx, "laptop")
	if err := te.store.HeartbeatIssue(laptopHeartbeat, "leaselegacy-2", "alice"); err != nil {
		t.Fatalf("HeartbeatIssue: %v", err)
	}
	var adopted string
	te.queryScalar(t, ctx, "SELECT granted_node FROM leases WHERE issue_id = ?", []any{"leaselegacy-2"}, &adopted)
	if adopted != "laptop" {
		t.Errorf("granted_node = %q after a heartbeat on an unknown-provenance lease, want %q", adopted, "laptop")
	}

	// The still-anonymous lease is reclaimable from a NAMED node: unknown
	// provenance is treated as local, never as foreign.
	laptopCtx := issueops.WithNodeID(ctx, "laptop")
	reclaimed, err := te.store.ReclaimExpiredLeases(laptopCtx, -time.Hour, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range reclaimed {
		ids[r.ID] = true
	}
	if !ids["leaselegacy-1"] {
		t.Errorf("reclaimed = %+v, want the unknown-provenance lease leaselegacy-1 reverted", reclaimed)
	}
	if !ids["leaselegacy-2"] {
		t.Errorf("reclaimed = %+v, want the laptop-adopted lease leaselegacy-2 reverted", reclaimed)
	}
}
