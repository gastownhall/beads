//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestLeaseLifecycleEmbedded confirms the lease columns exist in the embedded
// backend and that claim → heartbeat → reclaim are wired through EmbeddedDoltStore.
func TestLeaseLifecycleEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "lease")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "lease-1",
		Title:     "lease lifecycle",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Claim with a 1s lease so it expires within the test.
	claimCtx := issueops.WithLeaseTTL(ctx, time.Second)
	if err := te.store.ClaimIssue(claimCtx, "lease-1", "alice"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	// The claim stamped a non-zero row_lock and a lease.
	var rowLock int64
	te.queryScalar(t, ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{"lease-1"}, &rowLock)
	if rowLock == 0 {
		t.Error("row_lock = 0 after claim, want non-zero")
	}

	// Owner can heartbeat; a stranger cannot. Heartbeat with the same short TTL
	// so the lease still expires within the test (a default-TTL heartbeat would
	// push expiry minutes out).
	if err := te.store.HeartbeatIssue(claimCtx, "lease-1", "alice"); err != nil {
		t.Fatalf("owner HeartbeatIssue: %v", err)
	}
	if err := te.store.HeartbeatIssue(ctx, "lease-1", "mallory"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("stranger heartbeat err = %v, want ErrAlreadyClaimed", err)
	}

	// Let the lease expire, then reclaim it.
	time.Sleep(2500 * time.Millisecond)
	reclaimed, err := te.store.ReclaimExpiredLeases(ctx, 0, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "lease-1" || reclaimed[0].PreviousOwner != "alice" {
		t.Fatalf("reclaimed = %+v, want [{lease-1 alice}]", reclaimed)
	}

	got, err := te.store.GetIssue(ctx, "lease-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("status = %q after reclaim, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Errorf("assignee = %q after reclaim, want empty", got.Assignee)
	}
}

// TestReclaimExpiredLeaseSurvivesRestartEmbedded pins bd-lrgn1 acceptance (5):
// the leases table is dolt_ignored (unversioned, node-local) but still durable,
// so a lease granted before a server restart is visible after it and an expired
// lease can be reclaimed by the restarted server. Closing and reopening the
// embedded store is a real engine restart from disk.
func TestReclaimExpiredLeaseSurvivesRestartEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	const prefix = "lease_restart"

	store, err := embeddeddolt.Open(ctx, beadsDir, prefix, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		store.Close()
		t.Fatalf("Commit: %v", err)
	}

	issue := &types.Issue{
		ID:        "lease-restart-1",
		Title:     "lease across restart",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
		store.Close()
		t.Fatalf("CreateIssue: %v", err)
	}
	claimCtx := issueops.WithLeaseTTL(ctx, time.Second)
	if err := store.ClaimIssue(claimCtx, "lease-restart-1", "alice"); err != nil {
		store.Close()
		t.Fatalf("ClaimIssue: %v", err)
	}

	// Restart: close the engine, reopen from disk.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store2, err := embeddeddolt.Open(ctx, beadsDir, prefix, "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	// The lease row survived the restart with its holder intact — this guards
	// against a vacuous pass below, since reclaim JOINs on the leases table and
	// would return nothing if restart had dropped the row.
	te := &testEnv{store: store2, dataDir: filepath.Join(beadsDir, "embeddeddolt"), database: prefix}
	var holder, statusBefore, expiresStr string
	te.queryScalar(t, ctx,
		"SELECT l.holder, i.status, CAST(l.lease_expires_at AS CHAR) FROM leases l JOIN issues i ON i.id = l.issue_id WHERE l.issue_id = ?",
		[]any{"lease-restart-1"}, &holder, &statusBefore, &expiresStr)
	if holder != "alice" {
		t.Fatalf("lease holder after restart = %q, want alice", holder)
	}
	if statusBefore != "in_progress" {
		t.Fatalf("issue status after restart = %q, want in_progress", statusBefore)
	}

	// Wait until the wall clock is unambiguously past the STORED expiry:
	// DATETIME is second-granular and may round the claim's now+TTL up, so
	// racing a fixed sleep against it flakes (reclaim compares with strict <).
	expires, err := time.Parse("2006-01-02 15:04:05", expiresStr)
	if err != nil {
		t.Fatalf("parse stored lease_expires_at %q: %v", expiresStr, err)
	}
	if wait := time.Until(expires.Add(1500 * time.Millisecond)); wait > 0 {
		if wait > 10*time.Second {
			t.Fatalf("stored lease_expires_at %s is unexpectedly far in the future", expiresStr)
		}
		time.Sleep(wait)
	}

	reclaimed, err := store2.ReclaimExpiredLeases(ctx, 0, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases after restart: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "lease-restart-1" || reclaimed[0].PreviousOwner != "alice" {
		t.Fatalf("reclaimed = %+v, want [{lease-restart-1 alice}]", reclaimed)
	}

	got, err := store2.GetIssue(ctx, "lease-restart-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("status = %q after restart reclaim, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Errorf("assignee = %q after restart reclaim, want empty", got.Assignee)
	}
	var leaseRows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", []any{"lease-restart-1"}, &leaseRows)
	if leaseRows != 0 {
		t.Errorf("lease rows after reclaim = %d, want 0", leaseRows)
	}
}

// TestHeartbeatMatchesActorAcrossSpellingEmbedded pins ga-v2k49 (the
// remaining-sites follow-up to ga-wzl83/ga-5ksp5): a heartbeat succeeds when
// actor is a different layer's spelling of the same identity the issue was
// claimed under, and still refuses a genuinely different identity. The
// primary UPDATE's own holder=? predicate stays verbatim SQL by design (see
// lease.go); this test exercises the fallback path that makes the caller's
// experience correct anyway, including that it re-arms the lease row under
// actor's current spelling so the NEXT heartbeat hits the fast path.
func TestHeartbeatMatchesActorAcrossSpellingEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "lease_spelling")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "lease-spelling-1",
		Title:     "heartbeat across spelling",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := te.store.ClaimIssue(ctx, "lease-spelling-1", "gastown.mayor"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	// Same identity, dot-separator form sanitized to double-underscore (the
	// exact ga-wzl83 repro shape) — must succeed, not ErrAlreadyClaimed.
	if err := te.store.HeartbeatIssue(ctx, "lease-spelling-1", "gastown__mayor"); err != nil {
		t.Fatalf("cross-spelling HeartbeatIssue: %v, want success (same identity, different separator)", err)
	}

	// The re-arm re-wrote holder under the caller's current spelling, so the
	// NEXT heartbeat under that same spelling hits the primary UPDATE's fast
	// path directly (0 rows would mean it fell to the fallback again).
	var holder string
	te.queryScalar(t, ctx, "SELECT holder FROM leases WHERE issue_id = ?", []any{"lease-spelling-1"}, &holder)
	if holder != "gastown__mayor" {
		t.Errorf("lease holder after cross-spelling heartbeat = %q, want re-armed to %q", holder, "gastown__mayor")
	}

	// Control: a genuinely different identity — canonicalActor's own doc
	// comment names this exact pair as one that must stay distinct — is still
	// refused. Without this, a broken canonicalization that matched everyone
	// would pass the assertion above for the wrong reason.
	if err := te.store.HeartbeatIssue(ctx, "lease-spelling-1", "gastown.dog-3"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("stranger heartbeat err = %v, want ErrAlreadyClaimed", err)
	}
}

func TestHeartbeatRejectsWispEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "lease_wisp")
	ctx := t.Context()

	wisp := &types.Issue{
		ID:        "lease-wisp-1",
		Title:     "ephemeral work",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := te.store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	if err := te.store.HeartbeatIssue(ctx, "lease-wisp-1", "alice"); !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("wisp heartbeat err = %v, want ErrNotClaimable", err)
	}
}
