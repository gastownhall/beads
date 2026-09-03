//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/debug"
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

	// The laptop's reaper sees a stale lease and must decline it — and must
	// SAY SO. Every failure mode of the audit's queries is deliberately silent
	// (a failed query must never abort an otherwise-fine reclaim), so silence
	// is indistinguishable from "nothing to report" — which is exactly the
	// state this line exists to make visible. Without an assertion here a
	// malformed GROUP BY, or a Dolt release that stopped binding a parameter,
	// would leave the audit permanently mute with a fully green suite
	// (wy-s1ntth F4). The audit is stderr-only by contract — bd reclaim --json
	// owns stdout — so capturing stderr is the only way to observe it.
	laptopCtx := issueops.WithNodeID(ctx, "laptop")
	var reclaimed []types.ReclaimedLease
	var err error
	audit := captureStderr(t, func() {
		reclaimed, err = te.store.ReclaimExpiredLeases(laptopCtx, -time.Hour, types.ReclaimFilter{}, "reaper")
	})
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases from laptop: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("laptop reclaimed %+v, want nothing — the lease was granted by mini", reclaimed)
	}
	// One summary line per run, naming the granting replica, the count and
	// this node — the operator-actionable facts. Counted rather than merely
	// contained: the whole point of the wy-sp2l4 bound is that the DEFAULT
	// output is one line however many leases were skipped.
	if n := strings.Count(audit, "reclaim: skipped "); n != 1 {
		t.Fatalf("stderr carried %d foreign-skip summary lines, want exactly 1; stderr was:\n%s", n, audit)
	}
	for _, want := range []string{
		"reclaim: skipped 1 stale lease granted by another replica",
		`"mini" (1)`,
		`not this node ("laptop")`,
		"--any-replica",
	} {
		if !strings.Contains(audit, want) {
			t.Errorf("foreign-skip audit = %q, want it to contain %q", audit, want)
		}
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

	// The granting replica reaps it fine — and stays SILENT while doing it.
	// This is the other half of the assertion above: a summary line printed
	// unconditionally would satisfy the "it spoke" check while reporting
	// nothing real, so pin that the audit speaks only when there is something
	// to report.
	quiet := captureStderr(t, func() {
		reclaimed, err = te.store.ReclaimExpiredLeases(miniCtx, -time.Hour, types.ReclaimFilter{}, "reaper")
	})
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases from mini: %v", err)
	}
	if strings.Contains(quiet, "reclaim: skipped ") {
		t.Errorf("mini's own reclaim printed a foreign-skip audit, want none; stderr was:\n%s", quiet)
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

// TestForeignSkipDetailIsBounded pins the bound that actually replaced the
// unbounded per-row loop (wy-sp2l4 F5, asserted here per wy-s1ntth F6): the
// `LIMIT ?` in reportForeignSkipDetail. The pure formatter's boundedness is
// already covered in issueops, but the formatter is not what stops a federated
// store with hundreds of stranded remote leases from printing hundreds of
// lines a minute — the LIMIT is, and nothing exercised it against a real
// store. A Dolt release that stopped binding LIMIT ?, or a rewrite that
// dropped the clause, would restore the original unbounded chatter while every
// existing test stayed green.
//
// Seeds more foreign stale leases than the cap, turns the per-issue expansion
// on (`bd -v`), and asserts the expansion stops at the cap with an exact
// collapsed tail.
func TestForeignSkipDetailIsBounded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// detailCap must equal issueops.foreignSkipDetailRows, which is unexported.
	// The summary line advertises the same constant ("bd -v lists up to N of
	// them"), and the cross-check below turns a retune of that constant into a
	// loud failure here rather than a test that silently bounds at a stale
	// number.
	const detailCap = 20
	const seeded = 25

	te := newTestEnv(t, "leasedetail")
	ctx := t.Context()

	// One granting replica, so the summary stays a single named group and the
	// interesting variance is entirely in the per-issue expansion. IDs are
	// zero-padded because the detail query is ORDER BY issue_id — padding
	// makes "the first detailCap by id" a stable, checkable set.
	miniCtx := issueops.WithNodeID(ctx, "mini")
	var wantShown []string
	for i := 1; i <= seeded; i++ {
		id := fmt.Sprintf("leasedetail-%02d", i)
		issue := &types.Issue{
			ID:        id,
			Title:     "stranded on the other machine",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
		if err := te.store.ClaimIssue(miniCtx, id, "alice"); err != nil {
			t.Fatalf("ClaimIssue(%s): %v", id, err)
		}
		if i <= detailCap {
			wantShown = append(wantShown, id)
		}
	}

	// `bd -v`. BD_DEBUG is read once at package init, so the env var cannot be
	// set from inside the test binary; SetVerbose is the same switch through
	// the other door (debug.Enabled() is `enabled || verboseMode`).
	debug.SetVerbose(true)
	t.Cleanup(func() { debug.SetVerbose(false) })

	laptopCtx := issueops.WithNodeID(ctx, "laptop")
	var reclaimed []types.ReclaimedLease
	var err error
	audit := captureStderr(t, func() {
		reclaimed, err = te.store.ReclaimExpiredLeases(laptopCtx, -time.Hour, types.ReclaimFilter{}, "reaper")
	})
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases from laptop: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("laptop reclaimed %+v, want nothing — every lease was granted by mini", reclaimed)
	}

	summary, detail, tail := splitForeignSkipAudit(audit)
	if summary == "" {
		t.Fatalf("no foreign-skip summary line on stderr; stderr was:\n%s", audit)
	}
	// The TOTAL stays exact even though the expansion is truncated: the tail's
	// count comes from the grouped COUNT(*), not from a second pass over rows
	// the LIMIT already discarded.
	if want := fmt.Sprintf("skipped %d stale leases", seeded); !strings.Contains(summary, want) {
		t.Errorf("summary = %q, want the exact total %q", summary, want)
	}
	if want := fmt.Sprintf("lists up to %d of them.", detailCap); !strings.Contains(summary, want) {
		t.Errorf("summary = %q, want it to advertise the cap as %q — if foreignSkipDetailRows was retuned, "+
			"update detailCap in this test to match", summary, want)
	}

	if len(detail) != detailCap {
		t.Fatalf("verbose expansion printed %d detail lines, want exactly %d (the LIMIT); stderr was:\n%s",
			len(detail), detailCap, audit)
	}
	for i, id := range wantShown {
		if !strings.Contains(detail[i], " "+id+" ") {
			t.Errorf("detail line %d = %q, want it to name %s — the expansion is ORDER BY issue_id LIMIT %d",
				i, detail[i], id, detailCap)
		}
	}
	if want := fmt.Sprintf("reclaim:   ... and %d more.", seeded-detailCap); tail != want {
		t.Errorf("collapsed tail = %q, want %q", tail, want)
	}
}

// splitForeignSkipAudit picks the foreign-skip audit out of captured stderr and
// separates its three parts: the one summary line, the per-issue detail lines,
// and the collapsed tail (empty when nothing was truncated). Filtering by the
// audit's own prefixes rather than counting raw lines keeps the assertions
// honest if anything else in the process writes to stderr during the capture.
func splitForeignSkipAudit(stderr string) (summary string, detail []string, tail string) {
	for _, line := range strings.Split(stderr, "\n") {
		switch {
		case strings.HasPrefix(line, "reclaim: skipped "):
			summary = line
		case strings.HasPrefix(line, "reclaim:   ... and "):
			tail = line
		case strings.HasPrefix(line, "reclaim:   "):
			detail = append(detail, line)
		}
	}
	return summary, detail, tail
}

// captureStderr returns everything written to os.Stderr while fn runs, with
// the real stderr restored afterwards even if fn calls t.Fatalf. The replica
// audit is stderr-only by contract (warnReplica — bd reclaim --json owns
// stdout), so this is the only place its output can be observed at all.
//
// The pipe is drained on a goroutine rather than read after the fact: a
// verbose expansion plus whatever else the process logs can exceed the pipe
// buffer, and a blocked write inside the reclaim would deadlock the test.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	drained := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		_ = r.Close()
		drained <- b.String()
	}()
	orig := os.Stderr
	os.Stderr = w
	func() {
		defer func() {
			os.Stderr = orig
			_ = w.Close()
		}()
		fn()
	}()
	return <-drained
}
