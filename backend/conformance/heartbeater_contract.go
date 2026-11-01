package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.Heartbeater
// must satisfy. Each case asserts what issueops/heartbeater.go PROMISES, cited
// by symbol.
//
// THREE LEGS, ONE BODY. dolt, embedded dolt and the unit-of-work provider all
// reach internal/storage/issueops.ExecuteHeartbeatInTx, so a per-leg failure
// here is a wrapper's failure: a commit the leg added, a hook it fired, a
// refusal that stopped matching errors.Is.
//
// THE QUIET CASE IS THE POINT. A heartbeat is the hottest write in a fleet and
// it is cheap because it writes the lease row and nothing else. Most of what
// RunHeartbeaterChangesNothingOnTheRow asserts is an ABSENCE — no revision, no
// version, no updated_at, no event, no journal entry, no history entry, no
// hook — and each absence is something a plausible wrapper would add.
//
// THE LIVE-CLAIM FENCE IS AN INVARIANT AND A PREDICATE. The RefusesAfter*
// cases hold the invariant: each ends a claim one way and asserts both that
// the lease row is gone and that the holder's next heartbeat is refused. The
// RefusesAnOrphanedLease* cases hold the predicate: a lease row that survives
// on an issue that is no longer claimed by its holder (what a re-arm racing a
// release or close can leave on Dolt) is refused AND deleted, never refreshed.
type HeartbeaterFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Heartbeater is the surface under test.
	Heartbeater publicops.Heartbeater
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. A nil CreateWisp
	// SKIPS the wisp case loudly.
	CreateWisp func(context.Context, *types.Issue, string) error
	// Claimer takes the claim, and with it the lease row, a heartbeat
	// refreshes.
	Claimer publicops.Claimer
	// Lifecycle ends or moves a claim (close, a status move, a hand-doled
	// claim) and performs the guarded write the revision case lands.
	Lifecycle publicops.Lifecycle
	// Releaser gives a claim up for the release case. Nil SKIPS that case.
	Releaser publicops.Releaser
	// LeaseReclaimer reverts an aged claim for the reclaim case. Nil, or a nil
	// Exec, SKIPS that case.
	LeaseReclaimer publicops.LeaseReclaimer
	// Exec runs raw statements against the backing database: the cases use it
	// to age a lease.
	Exec func(ctx context.Context, statements []SQLStatement) error
	// ExecUncommitted runs raw statements as ANOTHER writer that leaves its
	// change uncommitted in the version-controlled working set (a batch-mode
	// edit): SQL-committed, no version-control commit. Nil means Exec already
	// behaves so.
	ExecUncommitted func(ctx context.Context, statements []SQLStatement) error
	// QueryScalar runs a single-row query and scans it, so the post-state is
	// read RAW rather than through the role being tested.
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many version-control entries the fixture's
	// branch has. Nil SKIPS that one assertion, loudly, in the quiet case.
	CountHistory func(context.Context) (int, error)
	// SetJournalEnabled turns on durable journaling, so the quiet case can
	// observe that a heartbeat records nothing in bd_events_journal. Nil SKIPS
	// that one assertion, loudly.
	SetJournalEnabled func(enabled bool)
	// SetVersionedHistoryEnabled turns on dual-write issue-version history, so
	// the quiet case can observe that a heartbeat records no issue_versions
	// row. Nil SKIPS that one assertion, loudly.
	SetVersionedHistoryEnabled func(enabled bool)
	// HookedHeartbeater is the same role taken off the leg's HOOK-FIRING chain
	// (storage.HookFiringStore on the store legs, uow.NewNotifyingProvider on
	// the unit-of-work leg), built over the runner FiredUpdates reads. Nil,
	// with FiredUpdates, SKIPS the no-hook assertion loudly.
	HookedHeartbeater publicops.Heartbeater
	// FiredUpdates reports, in firing order, the issue id of every on_update
	// the hooked chain ran so far.
	FiredUpdates func(context.Context) ([]string, error)
}

const (
	heartbeaterHolder   = "hb-holder"
	heartbeaterIntruder = "hb-intruder"
	heartbeaterReaper   = "hb-reaper"
)

// RunHeartbeaterRefreshesTheHoldersLease pins the refresh itself
// (Heartbeater: "pushes the lease the caller holds forward by the store's
// TTL") and every member of the HeartbeatResult, against the row READ RAW.
func RunHeartbeaterRefreshesTheHoldersLease(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	id := fixture.IssuePrefix + "-refresh"
	heartbeaterSeedClaimed(t, ctx, fixture, id)
	aged := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := fixture.Exec(ctx, []SQLStatement{{
		Query: "UPDATE leases SET lease_expires_at = ?, heartbeat_at = ? WHERE issue_id = ?",
		Args:  []any{aged, aged, id},
	}}); err != nil {
		t.Fatalf("age the lease on %s: %v", id, err)
	}

	before := time.Now().UTC().Add(-time.Second)
	result := heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
	after := time.Now().UTC().Add(time.Second)

	if result.IssueID != id || result.Holder != heartbeaterHolder || result.Rearmed {
		t.Fatalf("result = %+v, want {IssueID:%s Holder:%s Rearmed:false}", result, id, heartbeaterHolder)
	}
	if result.HeartbeatAt.Before(before) || result.HeartbeatAt.After(after) {
		t.Fatalf("HeartbeatAt = %v, want the server clock at the call (between %v and %v)", result.HeartbeatAt, before, after)
	}
	if ttl := result.LeaseExpiresAt.Sub(result.HeartbeatAt); heartbeaterAbs(ttl-storeops.DefaultLeaseTTL) > time.Second {
		t.Fatalf("LeaseExpiresAt - HeartbeatAt = %v, want the store's lease TTL %v", ttl, storeops.DefaultLeaseTTL)
	}
	if moved := heartbeaterScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM leases WHERE issue_id = ? AND holder = ? AND lease_expires_at > ? AND heartbeat_at > ?",
		id, heartbeaterHolder, aged, aged); moved != 1 {
		t.Fatal("the lease row's lease_expires_at and heartbeat_at did not move forward")
	}
	heartbeaterRequireStoredMatches(t, ctx, fixture, id, result, before, after)
}

// RunHeartbeaterASameSecondHeartbeatIsNotARearm pins HeartbeatResult.Rearmed
// against the refresh that changes no value: a heartbeat landing in the same
// second as the claim or the previous heartbeat writes values identical to the
// row's (the lease columns hold whole seconds), which some engines report as
// zero rows affected. It is still a refresh of the caller's own row: Rearmed
// is false, and the lease's granted_at and granted_node do not move.
func RunHeartbeaterASameSecondHeartbeatIsNotARearm(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	for i := 0; i < 3; i++ {
		id := fixture.IssuePrefix + "-samesec-" + string(rune('a'+i))
		heartbeaterSeedClaimed(t, ctx, fixture, id)
		granted := heartbeaterScalar[string](t, ctx, fixture,
			"SELECT CONCAT(CAST(granted_at AS CHAR), '|', COALESCE(granted_node, '')) FROM leases WHERE issue_id = ?", id)
		for beat := 0; beat < 2; beat++ {
			result := heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
			if result.Rearmed {
				t.Fatalf("heartbeat %d on %s right after the claim = %+v, want Rearmed:false: the holder's lease row existed", beat+1, id, result)
			}
		}
		if got := heartbeaterScalar[string](t, ctx, fixture,
			"SELECT CONCAT(CAST(granted_at AS CHAR), '|', COALESCE(granted_node, '')) FROM leases WHERE issue_id = ?", id); got != granted {
			t.Fatalf("granted_at|granted_node on %s moved from %s to %s across refreshes; a refresh never re-grants", id, granted, got)
		}
	}
}

// RunHeartbeaterChangesNothingOnTheRow pins the role's quiet promise
// (Heartbeater: "A REFRESH WRITES ONLY THE LEASE ROW"): the issue row's revision
// (row_lock), its current_revision, its updated_at, its events, the events
// journal, issue_versions, the version-control history and the hooks are all
// exactly where the claim left them.
func RunHeartbeaterChangesNothingOnTheRow(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	if fixture.SetJournalEnabled != nil {
		fixture.SetJournalEnabled(true)
		defer fixture.SetJournalEnabled(false)
	} else {
		t.Log("HeartbeaterFixture.SetJournalEnabled is nil: the journal assertion is skipped on this backend")
	}
	if fixture.SetVersionedHistoryEnabled != nil {
		fixture.SetVersionedHistoryEnabled(true)
		defer fixture.SetVersionedHistoryEnabled(false)
	} else {
		t.Log("HeartbeaterFixture.SetVersionedHistoryEnabled is nil: the issue_versions assertion is skipped on this backend")
	}
	id := fixture.IssuePrefix + "-quiet"
	heartbeaterSeedClaimed(t, ctx, fixture, id)
	// updated_at is stored at second precision on some backends; step past
	// the claim's second so a stamp the heartbeat added cannot hide in it.
	time.Sleep(1100 * time.Millisecond)

	snapshot := func() heartbeaterRowSnapshot {
		return heartbeaterSnapshot(t, ctx, fixture, id)
	}
	before := snapshot()
	fired := heartbeaterFired(t, ctx, fixture, id)

	surface := fixture.Heartbeater
	if fixture.HookedHeartbeater != nil && fixture.FiredUpdates != nil {
		surface = fixture.HookedHeartbeater
	} else {
		t.Log("HeartbeaterFixture.HookedHeartbeater/FiredUpdates is nil: the no-hook assertion is skipped on this backend")
	}
	heartbeat(t, ctx, surface, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})

	after := snapshot()
	if before != after {
		t.Fatalf("a heartbeat changed the issue's durable state:\nbefore %+v\nafter  %+v", before, after)
	}
	if got := heartbeaterFired(t, ctx, fixture, id); got != fired {
		t.Fatalf("on_update fired %d time(s) for %s across a refresh, want 0: a refresh fires no hook", got-fired, id)
	}
}

// RunHeartbeaterAGuardedWriteWithThePreHeartbeatRevisionStillWins is the
// reason the quiet case matters to a caller: the holder's revision token
// survives its own heartbeats, so a guarded write it composed from the claim
// still lands.
func RunHeartbeaterAGuardedWriteWithThePreHeartbeatRevisionStillWins(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.Lifecycle == nil {
		t.Skip("HeartbeaterFixture.Lifecycle is nil: this backend cannot land a guarded write")
	}
	id := fixture.IssuePrefix + "-guarded"
	revision := heartbeaterSeedClaimed(t, ctx, fixture, id)

	heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
	heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})

	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: heartbeaterHolder, IssueID: id, ExpectedVersion: &revision,
		Patch: publicops.IssuePatch{Notes: publicops.Field[string]{Set: true, Value: "after two heartbeats"}},
	}); err != nil {
		t.Fatalf("guarded write with the claim's revision after heartbeats = %v, want it to land", err)
	}
}

// RunHeartbeaterRefusesAForeignHolder pins the fence (Heartbeater REFUSALS:
// "an issue another actor holds: a *LeaseConflictError naming the Holder,
// wrapping ErrAlreadyClaimed"), and that the refusal writes nothing.
func RunHeartbeaterRefusesAForeignHolder(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	id := fixture.IssuePrefix + "-foreign"
	heartbeaterSeedClaimed(t, ctx, fixture, id)
	// Age the holder's lease first, so a heartbeat that ignored the holder
	// fence would really CHANGE the row (some engines count only changed rows,
	// and the columns hold whole seconds: an unaged row refreshed in the
	// claim's second changes nothing and would hide the missing fence).
	aged := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := fixture.Exec(ctx, []SQLStatement{{
		Query: "UPDATE leases SET lease_expires_at = ?, heartbeat_at = ? WHERE issue_id = ?",
		Args:  []any{aged, aged, id},
	}}); err != nil {
		t.Fatalf("age the lease on %s: %v", id, err)
	}
	lease := heartbeaterScalar[string](t, ctx, fixture, "SELECT CONCAT(CAST(lease_expires_at AS CHAR), '|', CAST(heartbeat_at AS CHAR), '|', holder) FROM leases WHERE issue_id = ?", id)

	_, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: heartbeaterIntruder, IssueID: id})
	if !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Fatalf("heartbeat by a non-holder = %v, want ErrAlreadyClaimed", err)
	}
	if errors.Is(err, publicops.ErrLeaseNotHeld) {
		t.Fatalf("heartbeat by a non-holder = %v also matches ErrLeaseNotHeld; the two refusals must stay distinct", err)
	}
	conflict := heartbeaterConflict(t, err)
	if conflict.IssueID != id || conflict.Holder != heartbeaterHolder || conflict.Status != types.StatusInProgress {
		t.Fatalf("LeaseConflictError = %+v, want {IssueID:%s Holder:%s Status:%s}", conflict, id, heartbeaterHolder, types.StatusInProgress)
	}
	if got := heartbeaterScalar[string](t, ctx, fixture, "SELECT CONCAT(CAST(lease_expires_at AS CHAR), '|', CAST(heartbeat_at AS CHAR), '|', holder) FROM leases WHERE issue_id = ?", id); got != lease {
		t.Fatalf("a refused heartbeat moved the holder's lease from %s to %s", lease, got)
	}
}

// RunHeartbeaterCommitsOnlyItsOwnTables pins the commit discipline against a
// DIRTY working set (Heartbeater: "A REFRESH WRITES ONLY THE LEASE ROW"; "A
// RE-ARM IS RECORDED AS A MUTATION"): another writer has left an uncommitted
// versioned change (a batch-mode edit, here a config row). A refresh and a
// refusal make no version-control commit and leave that change uncommitted;
// a re-arm makes exactly one commit, of its own tables, and still leaves the
// other writer's change uncommitted rather than sweeping it in under the
// heartbeat's message.
func RunHeartbeaterCommitsOnlyItsOwnTables(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	if fixture.CountHistory == nil || fixture.Lifecycle == nil {
		t.Skip("HeartbeaterFixture.CountHistory/Lifecycle is nil: this backend has no version-control history to observe")
	}
	refreshed := fixture.IssuePrefix + "-dirty-refresh"
	heartbeaterSeedClaimed(t, ctx, fixture, refreshed)
	refused := fixture.IssuePrefix + "-dirty-refused"
	heartbeaterSeed(t, ctx, fixture, refused)
	rearmed := fixture.IssuePrefix + "-dirty-rearm"
	heartbeaterSeed(t, ctx, fixture, rearmed)
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: heartbeaterHolder, IssueID: rearmed,
		Patch: publicops.IssuePatch{
			Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
			Assignee: publicops.Field[string]{Set: true, Value: heartbeaterHolder},
		},
	}); err != nil {
		t.Fatalf("hand-dole %s: %v", rearmed, err)
	}

	probe := "heartbeat.dirty-probe." + fixture.IssuePrefix
	dirty := fixture.Exec
	if fixture.ExecUncommitted != nil {
		dirty = fixture.ExecUncommitted
	}
	if err := dirty(ctx, []SQLStatement{{
		Query: "REPLACE INTO config (`key`, value) VALUES (?, ?)", Args: []any{probe, "uncommitted"},
	}}); err != nil {
		t.Fatalf("dirty the working set: %v", err)
	}
	defer func() {
		_ = dirty(ctx, []SQLStatement{{Query: "DELETE FROM config WHERE `key` = ?", Args: []any{probe}}})
	}()
	foreignDirty := func(when string) {
		t.Helper()
		if n := heartbeaterScalar[int](t, ctx, fixture,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'"); n == 0 {
			t.Fatalf("%s: the other writer's uncommitted config change was committed; a heartbeat commits only its own tables", when)
		}
	}
	history := func() int {
		t.Helper()
		n, err := fixture.CountHistory(ctx)
		if err != nil {
			t.Fatalf("CountHistory: %v", err)
		}
		return n
	}
	foreignDirty("before any heartbeat")
	before := history()

	heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: refreshed})
	if got := history(); got != before {
		t.Fatalf("a refresh moved the version-control history %d -> %d; a refresh makes no commit", before, got)
	}
	foreignDirty("after a refresh")

	if _, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: refused}); err == nil {
		t.Fatalf("heartbeat on unclaimed %s succeeded, want a refusal", refused)
	}
	if got := history(); got != before {
		t.Fatalf("a refusal moved the version-control history %d -> %d; a refusal makes no commit", before, got)
	}
	foreignDirty("after a refusal")

	if result := heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: rearmed}); !result.Rearmed {
		t.Fatalf("heartbeat on hand-doled %s = %+v, want Rearmed", rearmed, result)
	}
	if got := history(); got != before+1 {
		t.Fatalf("a re-arm moved the version-control history %d -> %d, want exactly one commit", before, got)
	}
	foreignDirty("after a re-arm")
}

// RunHeartbeaterRefusesAnOrphanedLeaseOnAReleasedIssue and
// ...OnAClosedIssue and ...OnAnIssueSomeoneElseHolds pin the live-claim
// predicate (Heartbeater: "THE LIVE-CLAIM FENCE IS A PREDICATE"): a lease row
// that names the caller but survives on an issue that is open, closed, or
// in_progress under another assignee is refused, never refreshed, and the
// refusal deletes it (Heartbeater: the one write a refusal makes). The row
// is planted raw: it is the state a re-arm racing a release or close leaves.
func RunHeartbeaterRefusesAnOrphanedLeaseOnAReleasedIssue(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	id := fixture.IssuePrefix + "-orphan-open"
	heartbeaterSeed(t, ctx, fixture, id)
	heartbeaterRefusesAnOrphan(t, ctx, fixture, id, types.StatusOpen, "")
}

func RunHeartbeaterRefusesAnOrphanedLeaseOnAClosedIssue(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	if fixture.Lifecycle == nil {
		t.Skip("HeartbeaterFixture.Lifecycle is nil: this backend cannot close the claim")
	}
	id := fixture.IssuePrefix + "-orphan-closed"
	heartbeaterSeedClaimed(t, ctx, fixture, id)
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: heartbeaterHolder, IssueID: id, Reason: "done"}); err != nil {
		t.Fatalf("close %s: %v", id, err)
	}
	heartbeaterRefusesAnOrphan(t, ctx, fixture, id, types.StatusClosed, "")
}

func RunHeartbeaterRefusesAnOrphanedLeaseOnAnIssueSomeoneElseHolds(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	requireHeartbeaterExec(t, fixture)
	if fixture.Lifecycle == nil {
		t.Skip("HeartbeaterFixture.Lifecycle is nil: this backend cannot hand-dole a claim")
	}
	id := fixture.IssuePrefix + "-orphan-moved"
	heartbeaterSeed(t, ctx, fixture, id)
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: heartbeaterIntruder, IssueID: id,
		Patch: publicops.IssuePatch{
			Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
			Assignee: publicops.Field[string]{Set: true, Value: heartbeaterIntruder},
		},
	}); err != nil {
		t.Fatalf("hand-dole %s to %s: %v", id, heartbeaterIntruder, err)
	}
	heartbeaterRefusesAnOrphan(t, ctx, fixture, id, types.StatusInProgress, heartbeaterIntruder)
}

// heartbeaterRefusesAnOrphan plants a lease row for heartbeaterHolder on id
// (which the holder does not hold), heartbeats as the holder, and asserts the
// refusal and that the row is gone.
func heartbeaterRefusesAnOrphan(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, id string, status types.Status, foreign string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := fixture.Exec(ctx, []SQLStatement{
		{Query: "DELETE FROM leases WHERE issue_id = ?", Args: []any{id}},
		{
			Query: "INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at, granted_node) VALUES (?, ?, ?, ?, ?, '')",
			Args:  []any{id, heartbeaterHolder, now.Add(-time.Minute), now.Add(time.Hour), now.Add(-time.Minute)},
		},
	}); err != nil {
		t.Fatalf("plant an orphaned lease on %s: %v", id, err)
	}

	_, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
	conflict := heartbeaterConflict(t, err)
	if foreign != "" {
		if !errors.Is(err, publicops.ErrAlreadyClaimed) || conflict.Holder != foreign {
			t.Fatalf("heartbeat over an orphaned lease on an issue %s holds = %v (%+v), want ErrAlreadyClaimed naming %s", foreign, err, conflict, foreign)
		}
	} else if !errors.Is(err, publicops.ErrLeaseNotHeld) || conflict.Holder != "" {
		t.Fatalf("heartbeat over an orphaned lease on a %s issue = %v (%+v), want ErrLeaseNotHeld", status, err, conflict)
	}
	if conflict.IssueID != id || conflict.Status != status || conflict.Ephemeral {
		t.Fatalf("LeaseConflictError = %+v, want {IssueID:%s Status:%s Ephemeral:false}", conflict, id, status)
	}
	if n := heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 0 {
		t.Fatalf("lease rows on %s after the refusal = %d, want 0: a refusal deletes the caller's orphaned lease", id, n)
	}
	// And it stays refused: the next heartbeat finds no row to refresh.
	if _, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id}); err == nil {
		t.Fatalf("a second heartbeat on %s succeeded after the orphan was refused", id)
	}
}

// heartbeaterRequireStoredMatches pins the reported times to the stored row
// (LeaseRenewal: "exactly what the lease row now holds"): lease_expires_at and
// heartbeat_at as stored are the result's, within the columns' one-second
// precision, and the stored expiry is the call's clock plus the store's TTL.
func heartbeaterRequireStoredMatches(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, id string, result publicops.HeartbeatResult, before, after time.Time) {
	t.Helper()
	window := func(at time.Time) (time.Time, time.Time) { return at.Add(-time.Second), at.Add(time.Second) }
	hbLo, hbHi := window(result.HeartbeatAt)
	exLo, exHi := window(result.LeaseExpiresAt)
	if n := heartbeaterScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM leases WHERE issue_id = ? AND holder = ? AND heartbeat_at BETWEEN ? AND ? AND lease_expires_at BETWEEN ? AND ?",
		id, result.Holder, hbLo, hbHi, exLo, exHi); n != 1 {
		t.Fatalf("the stored lease row on %s does not hold the reported heartbeat_at %v and lease_expires_at %v (within 1s)",
			id, result.HeartbeatAt, result.LeaseExpiresAt)
	}
	ttlLo, ttlHi := before.Add(storeops.DefaultLeaseTTL), after.Add(storeops.DefaultLeaseTTL)
	if n := heartbeaterScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM leases WHERE issue_id = ? AND lease_expires_at BETWEEN ? AND ?",
		id, ttlLo, ttlHi); n != 1 {
		t.Fatalf("the stored lease_expires_at on %s is not the call's clock plus the lease TTL %v", id, storeops.DefaultLeaseTTL)
	}
	if !result.HeartbeatAt.Equal(result.HeartbeatAt.Truncate(time.Second)) || !result.LeaseExpiresAt.Equal(result.LeaseExpiresAt.Truncate(time.Second)) {
		t.Fatalf("result times %v / %v carry sub-second digits the lease columns cannot hold", result.HeartbeatAt, result.LeaseExpiresAt)
	}
}

// RunHeartbeaterRefusesAfterClose, ...AfterRelease, ...AfterReclaim and
// ...AfterStatusMovedOffInProgress pin the in_progress fence through the
// lease-row invariant (Heartbeater: "a lease row exists only while its issue
// is in_progress with that holder as assignee"): each ends the claim one way,
// asserts the lease row went with it, and that the former holder's heartbeat
// is refused with ErrLeaseNotHeld, which also matches ErrNotClaimable.
func RunHeartbeaterRefusesAfterClose(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.Lifecycle == nil {
		t.Skip("HeartbeaterFixture.Lifecycle is nil: this backend cannot close the claim")
	}
	heartbeaterRefusesAfter(t, ctx, fixture, "closed", types.StatusClosed, func(id string) error {
		_, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: heartbeaterHolder, IssueID: id, Reason: "done"})
		return err
	})
}

func RunHeartbeaterRefusesAfterRelease(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.Releaser == nil {
		t.Skip("HeartbeaterFixture.Releaser is nil: this backend cannot release the claim")
	}
	heartbeaterRefusesAfter(t, ctx, fixture, "released", types.StatusOpen, func(id string) error {
		_, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: heartbeaterHolder, IssueID: id})
		return err
	})
}

func RunHeartbeaterRefusesAfterReclaim(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.LeaseReclaimer == nil || fixture.Exec == nil {
		t.Skip("HeartbeaterFixture.LeaseReclaimer/Exec is nil: this backend cannot reclaim the claim")
	}
	heartbeaterRefusesAfter(t, ctx, fixture, "reclaimed", types.StatusOpen, func(id string) error {
		if err := fixture.Exec(ctx, []SQLStatement{{
			Query: "UPDATE leases SET lease_expires_at = ? WHERE issue_id = ?",
			Args:  []any{time.Now().UTC().Add(-time.Hour), id},
		}}); err != nil {
			return err
		}
		result, err := fixture.LeaseReclaimer.Reclaim(ctx, publicops.ReclaimRequest{
			Actor: heartbeaterReaper, Filter: publicops.ReclaimFilter{IDs: []string{id}},
		})
		if err == nil && len(result.Reclaimed) != 1 {
			t.Fatalf("Reclaimed = %+v, want exactly %s", result.Reclaimed, id)
		}
		return err
	})
}

func RunHeartbeaterRefusesAfterStatusMovedOffInProgress(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.Lifecycle == nil {
		t.Skip("HeartbeaterFixture.Lifecycle is nil: this backend cannot move the status")
	}
	heartbeaterRefusesAfter(t, ctx, fixture, "blocked", types.StatusBlocked, func(id string) error {
		_, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
			Actor: heartbeaterHolder, IssueID: id,
			Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusBlocked}},
		})
		return err
	})
}

// RunHeartbeaterRefusesAWisp pins the plane rule (Heartbeater REFUSALS: "a
// wisp ... ErrLeaseNotHeld"): wisps are never leased, so even a wisp the
// caller holds in_progress is refused, and no lease row appears for it.
func RunHeartbeaterRefusesAWisp(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.CreateWisp == nil || fixture.QueryScalar == nil {
		t.Skip("HeartbeaterFixture.CreateWisp/QueryScalar is nil: this backend cannot seed a wisp")
	}
	id := fixture.IssuePrefix + "-wisp"
	if err := fixture.CreateWisp(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusInProgress, Assignee: heartbeaterHolder,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}

	_, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
	if !errors.Is(err, publicops.ErrLeaseNotHeld) || !errors.Is(err, publicops.ErrNotClaimable) {
		t.Fatalf("heartbeat on a wisp = %v, want ErrLeaseNotHeld matching ErrNotClaimable", err)
	}
	if !strings.Contains(err.Error(), "ephemeral") {
		t.Fatalf("heartbeat on a wisp = %q, want the refusal to say the issue is ephemeral", err)
	}
	if conflict := heartbeaterConflict(t, err); conflict.IssueID != id || conflict.Holder != "" {
		t.Fatalf("LeaseConflictError = %+v, want {IssueID:%s Holder:\"\"}", conflict, id)
	}
	if n := heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 0 {
		t.Fatalf("lease rows for wisp %s = %d, want 0: wisps are never leased", id, n)
	}
}

// RunHeartbeaterRearmsAHandDoledClaim pins the self-heal (HeartbeatResult.
// Rearmed): an issue put in_progress with the caller as assignee by a generic
// update holds no lease row, and the holder's heartbeat grants one rather than
// refusing. The re-arm is recorded as a mutation (Heartbeater: "A RE-ARM IS
// RECORDED AS A MUTATION"): it remints the revision, which is what makes it
// collide with a racing release or close, and records a lease_rearmed event;
// the refresh after it does neither.
func RunHeartbeaterRearmsAHandDoledClaim(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.Lifecycle == nil || fixture.QueryScalar == nil {
		t.Skip("HeartbeaterFixture.Lifecycle/QueryScalar is nil: this backend cannot hand-dole a claim")
	}
	id := fixture.IssuePrefix + "-doled"
	heartbeaterSeed(t, ctx, fixture, id)
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: heartbeaterHolder, IssueID: id,
		Patch: publicops.IssuePatch{
			Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
			Assignee: publicops.Field[string]{Set: true, Value: heartbeaterHolder},
		},
	}); err != nil {
		t.Fatalf("hand-dole %s: %v", id, err)
	}
	if n := heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 0 {
		t.Fatalf("lease rows after a generic update = %d, want 0: the case needs a claim with no lease", n)
	}
	revision := heartbeaterScalar[int64](t, ctx, fixture, "SELECT row_lock FROM issues WHERE id = ?", id)

	before := time.Now().UTC().Add(-time.Second)
	result := heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
	after := time.Now().UTC().Add(time.Second)
	if !result.Rearmed || result.Holder != heartbeaterHolder {
		t.Fatalf("result = %+v, want Rearmed:true Holder:%s", result, heartbeaterHolder)
	}
	heartbeaterRequireStoredMatches(t, ctx, fixture, id, result, before, after)
	if n := heartbeaterScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM leases WHERE issue_id = ? AND holder = ? AND lease_expires_at > ?",
		id, heartbeaterHolder, time.Now().UTC()); n != 1 {
		t.Fatal("the re-armed heartbeat left no live lease row for the holder")
	}
	rearmed := heartbeaterScalar[int64](t, ctx, fixture, "SELECT row_lock FROM issues WHERE id = ?", id)
	if rearmed == revision {
		t.Fatalf("row_lock stayed %d across a re-arming heartbeat; a re-arm remints it so a racing release or close collides with it", revision)
	}
	rearmEvents := func() int {
		return heartbeaterScalar[int](t, ctx, fixture,
			"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", id, string(types.EventLeaseRearmed))
	}
	if n := rearmEvents(); n != 1 {
		t.Fatalf("lease_rearmed events on %s after a re-arm = %d, want 1: a re-arm is recorded as a mutation", id, n)
	}
	// The next heartbeat refreshes the lease it just granted: no re-arm, and
	// no revision change.
	if again := heartbeat(t, ctx, fixture.Heartbeater, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id}); again.Rearmed {
		t.Fatalf("second heartbeat after the re-arm = %+v, want Rearmed:false", again)
	}
	if got := heartbeaterScalar[int64](t, ctx, fixture, "SELECT row_lock FROM issues WHERE id = ?", id); got != rearmed {
		t.Fatalf("row_lock moved from %d to %d across a refresh after the re-arm", rearmed, got)
	}
	if n := rearmEvents(); n != 1 {
		t.Fatalf("lease_rearmed events on %s after a refresh = %d, want still 1: a refresh records nothing", id, n)
	}
}

// RunHeartbeaterAnswersNotFoundForAnAbsentID pins Heartbeater REFUSALS: "an
// IssueID that names no row on either plane: ErrNotFound".
func RunHeartbeaterAnswersNotFoundForAnAbsentID(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	_, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{
		Actor: heartbeaterHolder, IssueID: fixture.IssuePrefix + "-absent",
	})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("heartbeat on an absent id = %v, want ErrNotFound", err)
	}
}

// RunHeartbeaterRefusesABlankOrOverLongActor pins the request rules
// (HeartbeatRequest.Actor, Heartbeater REFUSALS): each is a
// *HeartbeatFieldError naming its field, matching ErrValidation.
func RunHeartbeaterRefusesABlankOrOverLongActor(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-refusals"
	for _, test := range []struct {
		name    string
		request publicops.HeartbeatRequest
		field   string
	}{
		{"an empty actor", publicops.HeartbeatRequest{IssueID: id}, publicops.HeartbeatFieldActor},
		{"a blank actor", publicops.HeartbeatRequest{Actor: "  \t", IssueID: id}, publicops.HeartbeatFieldActor},
		{"an over-long actor", publicops.HeartbeatRequest{Actor: strings.Repeat("a", types.MaxFieldLen+1), IssueID: id}, publicops.HeartbeatFieldActor},
		{"an empty id", publicops.HeartbeatRequest{Actor: heartbeaterHolder}, publicops.HeartbeatFieldIssueID},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.Heartbeater.Heartbeat(ctx, test.request)
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("Heartbeat(%s) = %v, want ErrValidation", test.name, err)
			}
			var field *publicops.HeartbeatFieldError
			if !errors.As(err, &field) || field.Field != test.field {
				t.Fatalf("Heartbeat(%s) = %v, want a *HeartbeatFieldError naming %q", test.name, err, test.field)
			}
		})
	}
}

// RunHeartbeaterDoesNotMutateTheRequest pins the role's "never mutate
// caller-owned request values".
func RunHeartbeaterDoesNotMutateTheRequest(t *testing.T, ctx context.Context, fixture HeartbeaterFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-nomutate"
	heartbeaterSeedClaimed(t, ctx, fixture, id)
	request := publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id}
	want := request
	heartbeat(t, ctx, fixture.Heartbeater, request)
	if request != want {
		t.Fatalf("request after Heartbeat = %+v, want %+v", request, want)
	}
}

// heartbeaterRefusesAfter seeds and claims one issue, ends the claim with end,
// and asserts the invariant and the refusal.
func heartbeaterRefusesAfter(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, suffix string, status types.Status, end func(id string) error) {
	t.Helper()
	if fixture.QueryScalar == nil {
		t.Skip("HeartbeaterFixture.QueryScalar is nil: this backend cannot read the lease row")
	}
	id := fixture.IssuePrefix + "-after-" + suffix
	heartbeaterSeedClaimed(t, ctx, fixture, id)
	if n := heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 1 {
		t.Fatalf("lease rows for claimed %s = %d, want 1", id, n)
	}
	if err := end(id); err != nil {
		t.Fatalf("end the claim on %s (%s): %v", id, suffix, err)
	}
	if n := heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 0 {
		t.Fatalf("lease rows for %s %s = %d, want 0: the path that ended the claim left its lease row", suffix, id, n)
	}

	_, err := fixture.Heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: heartbeaterHolder, IssueID: id})
	if !errors.Is(err, publicops.ErrLeaseNotHeld) || !errors.Is(err, publicops.ErrNotClaimable) {
		t.Fatalf("heartbeat after the claim was %s = %v, want ErrLeaseNotHeld matching ErrNotClaimable", suffix, err)
	}
	if errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Fatalf("heartbeat after the claim was %s = %v also matches ErrAlreadyClaimed", suffix, err)
	}
	conflict := heartbeaterConflict(t, err)
	if conflict.IssueID != id || conflict.Holder != "" || conflict.Status != status {
		t.Fatalf("LeaseConflictError = %+v, want {IssueID:%s Holder:\"\" Status:%s}", conflict, id, status)
	}
	if n := heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); n != 0 {
		t.Fatalf("a refused heartbeat left %d lease row(s) for %s", n, id)
	}
}

// heartbeaterRowSnapshot is every durable trace a heartbeat must not leave.
// A field the backend cannot observe holds -1.
type heartbeaterRowSnapshot struct {
	RowLock         int64
	CurrentRevision int64
	UpdatedAt       string
	Events          int
	JournalMaxSeq   int64
	IssueVersions   int
	History         int
}

func heartbeaterSnapshot(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, id string) heartbeaterRowSnapshot {
	t.Helper()
	snap := heartbeaterRowSnapshot{
		RowLock:         heartbeaterScalar[int64](t, ctx, fixture, "SELECT row_lock FROM issues WHERE id = ?", id),
		CurrentRevision: heartbeaterScalar[int64](t, ctx, fixture, "SELECT current_revision FROM issues WHERE id = ?", id),
		UpdatedAt:       heartbeaterScalar[string](t, ctx, fixture, "SELECT CAST(updated_at AS CHAR) FROM issues WHERE id = ?", id),
		Events:          heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM events WHERE issue_id = ?", id),
		JournalMaxSeq:   -1,
		IssueVersions:   -1,
		History:         -1,
	}
	if fixture.SetJournalEnabled != nil {
		snap.JournalMaxSeq = heartbeaterScalar[int64](t, ctx, fixture, "SELECT COALESCE(MAX(seq), 0) FROM bd_events_journal")
	}
	if fixture.SetVersionedHistoryEnabled != nil {
		snap.IssueVersions = heartbeaterScalar[int](t, ctx, fixture, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", id)
	}
	if fixture.CountHistory != nil {
		n, err := fixture.CountHistory(ctx)
		if err != nil {
			t.Fatalf("CountHistory: %v", err)
		}
		snap.History = n
	}
	return snap
}

func heartbeaterFired(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, id string) int {
	t.Helper()
	if fixture.HookedHeartbeater == nil || fixture.FiredUpdates == nil {
		return 0
	}
	ids, err := fixture.FiredUpdates(ctx)
	if err != nil {
		t.Fatalf("FiredUpdates: %v", err)
	}
	n := 0
	for _, fired := range ids {
		if fired == id {
			n++
		}
	}
	return n
}

func requireHeartbeaterExec(t *testing.T, fixture HeartbeaterFixture) {
	t.Helper()
	if fixture.Exec == nil || fixture.QueryScalar == nil {
		t.Skip("HeartbeaterFixture.Exec/QueryScalar is nil: this backend cannot read or age a lease row")
	}
}

func heartbeaterSeed(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, id string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// heartbeaterSeedClaimed creates id and claims it as heartbeaterHolder through
// the leg's Claimer, answering the claim's row version.
func heartbeaterSeedClaimed(t *testing.T, ctx context.Context, fixture HeartbeaterFixture, id string) int64 {
	t.Helper()
	if fixture.Claimer == nil {
		t.Skip("HeartbeaterFixture.Claimer is nil: this backend cannot take the claim a heartbeat refreshes")
	}
	heartbeaterSeed(t, ctx, fixture, id)
	claimed, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: heartbeaterHolder, IssueID: id})
	if err != nil {
		t.Fatalf("claim %s as %s: %v", id, heartbeaterHolder, err)
	}
	if claimed.Issue == nil {
		t.Fatalf("claim %s answered no issue", id)
	}
	return claimed.Issue.RowVersion
}

func heartbeat(t *testing.T, ctx context.Context, surface publicops.Heartbeater, request publicops.HeartbeatRequest) publicops.HeartbeatResult {
	t.Helper()
	result, err := surface.Heartbeat(ctx, request)
	if err != nil {
		t.Fatalf("Heartbeat(%+v): %v", request, err)
	}
	return result
}

func heartbeaterConflict(t *testing.T, err error) *publicops.LeaseConflictError {
	t.Helper()
	var conflict *publicops.LeaseConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("refusal %v (%T) is not a *LeaseConflictError", err, err)
	}
	return conflict
}

func heartbeaterScalar[T any](t *testing.T, ctx context.Context, fixture HeartbeaterFixture, query string, args ...any) T {
	t.Helper()
	var value T
	if err := fixture.QueryScalar(ctx, query, args, &value); err != nil {
		t.Fatalf("%s %v: %v", query, args, err)
	}
	return value
}

func heartbeaterAbs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
