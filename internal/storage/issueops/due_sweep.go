package issueops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// DueTriggerActor is the actor recorded on a due-date event. A constant rather
// than the invoking session's actor, for DeferWakeActor's reason: the trigger
// is the system honoring a deadline, not something the reader who happened to
// run the sweep did.
const DueTriggerActor = "bd-due-trigger"

// DueMissGrace is how far forward the sweep pushes a bead's due date once its
// deadline has passed with the work still open.
//
// A due date that stays in the past fires once and is then indistinguishable
// from every other overdue bead — nothing escalates a second time, which is
// exactly the failure a deadline exists to prevent. Rescheduling turns a missed
// date into a repeating nudge, and doubles as the sweep's idempotency
// mechanism: the advanced date is what stops the same bead re-firing on the
// next read.
const DueMissGrace = 24 * time.Hour

// DueMissEscalateAt is how many misses it takes before a bead's priority is
// raised once.
//
// Rescheduling alone (DueMissGrace above) keeps a deadline visible, but at a
// FIXED priority: a bead missed once and a bead missed twenty times sit at the
// same place in the ready front, so a chronically missed deadline is
// indistinguishable from a fresh one and nothing ever changes. One raise at a
// small threshold is the whole escalation — it moves the bead where a human
// looks, once, and then stops. It does NOT walk the bead up to P0 over
// successive misses: an escalation that repeats is just the nag again, one
// rung higher each time, and would eventually flatten every priority in the
// workspace to P0.
//
// Three is the smallest count that distinguishes a pattern from an accident:
// one miss is a bad day, two is bad luck, three is the deadline being wrong or
// the work being stuck, and either wants a person.
const DueMissEscalateAt = 3

// DueSweepResult reports what one sweep fired, per table. Issues are
// permanent-table ids and are the only ones that decide whether the caller
// mints a Dolt commit; wisp tables are dolt_ignored.
type DueSweepResult struct {
	Issues []string
	Wisps  []string
	// Escalated names the beads whose miss count reached DueMissEscalateAt in
	// THIS sweep and whose priority was therefore raised — split by plane the
	// same way Issues and Wisps are, and a SUBSET of them. It is what a caller
	// publishes as the actionable half of its summary: "fired 9, escalated 1"
	// says where to look, where "fired 9" alone does not.
	Escalated      []string
	EscalatedWisps []string
}

// DueSweepCommitMessage names a sweep's dolt commit. n is the number of
// permanent issues that fired; callers with n == 0 should not commit at all.
func DueSweepCommitMessage(n int) string {
	return fmt.Sprintf("bd: trigger %d due bead(s)", n)
}

// SweepDueBeadsInTx fires every open bead whose due date has arrived.
//
// For each one it records a types.EventDue audit event — the rail `bd events`
// and the events journal already carry — counts the miss, and moves the due
// date forward by one DueMissGrace so the bead is not re-fired on the next
// read and keeps nagging instead of going silent.
//
// It is the exact shape of WakeExpiredDefersInTx and shares its contract: the
// caller owns Dolt versioning (commit iff len(result.Issues) > 0) and must
// treat a sweep failure as advisory — a ready listing never fails because the
// sweep could not run. The snapshot-then-recheck structure means a bead closed
// or rescheduled between the SELECT and its UPDATE is simply skipped.
func SweepDueBeadsInTx(ctx context.Context, tx DBTX) (DueSweepResult, error) {
	var result DueSweepResult
	live, err := dueSweepLiveStatusesInTx(ctx, tx)
	if err != nil {
		return result, err
	}
	issues, escalated, err := sweepDueBeadsInTable(ctx, tx, "issues", "events", live)
	if err != nil {
		return result, err
	}
	result.Issues = issues
	result.Escalated = escalated
	// Wisps carry the same due_at/due_missed columns and are tolerated absent
	// for pre-wisp databases, like every other wisp probe.
	wisps, escalatedWisps, err := sweepDueBeadsInTable(ctx, tx, "wisps", "wisp_events", live)
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return result, nil
		}
		return result, err
	}
	result.Wisps = wisps
	result.EscalatedWisps = escalatedWisps
	return result, nil
}

// dueSweepLiveStatusesInTx lists the statuses a bead can be in and still come
// due: work that is neither finished nor deliberately hidden.
//
// It is an explicit LIST built from the workspace's own vocabulary rather than
// a `NOT IN ('closed', 'deferred')` exclusion, for the reason
// workapi.NotDoneStatusesForSweep records: a workspace can configure custom
// statuses, and an exclusion would fire a bead parked in a custom DONE status
// forever while never reaching one in a custom ACTIVE status. Reading the
// vocabulary is required, not best-effort — guessing it wrong shows up as an
// event storm on finished work.
//
// deferred is excluded even though it is not a done category: a deferred bead
// is hidden until its own date by contract, and WakeExpiredDefersInTx — which
// runs first in the same transaction — is what returns it to open. Once woken
// it is `open` and this sweep sees it in the same pass.
func dueSweepLiveStatusesInTx(ctx context.Context, tx DBTX) ([]types.Status, error) {
	custom, err := ResolveCustomStatusesDetailedInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("due sweep: resolve custom statuses: %w", err)
	}
	return dueSweepLiveStatuses(custom), nil
}

// dueSweepLiveStatuses is the vocabulary decision itself, split from the read
// above so it can be checked without a database.
func dueSweepLiveStatuses(custom []types.CustomStatus) []types.Status {
	var live []types.Status
	for _, status := range workapi.NotDoneStatusesForSweep(custom) {
		if status == types.StatusDeferred {
			continue
		}
		live = append(live, status)
	}
	return live
}

// dueCandidate is one row the sweep picked up, carrying just the fields needed
// to reschedule it and decide whether this miss escalates.
type dueCandidate struct {
	id        string
	dueAt     time.Time
	dueMissed int
	priority  int
}

// the status placeholders are generated, never interpolated values.
//
//nolint:gosec // G201: table and eventsTable are hardcoded constants from the caller;
func sweepDueBeadsInTable(ctx context.Context, tx DBTX, table, eventsTable string, live []types.Status) (fired, escalated []string, err error) {
	if len(live) == 0 {
		return nil, nil, nil
	}
	statusList, statusArgs := statusPlaceholders(live)
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, due_at, due_missed, priority
		FROM %s
		WHERE due_at IS NOT NULL AND due_at <= UTC_TIMESTAMP()
		  AND status IN (%s)
	`, table, statusList), statusArgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("due sweep: scan %s: %w", table, err)
	}
	var candidates []dueCandidate
	for rows.Next() {
		var c dueCandidate
		if err := rows.Scan(&c.id, &c.dueAt, &c.dueMissed, &c.priority); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("due sweep: scan %s row: %w", table, err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, fmt.Errorf("due sweep: iterate %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("due sweep: close %s rows: %w", table, err)
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	now := time.Now().UTC()
	for _, c := range candidates {
		next := now.Add(DueMissGrace)

		// The miss is counted on the same UPDATE that reschedules, so a bead
		// can never be rescheduled without its miss being recorded: a counter
		// written separately could be lost to a crash in between, and the
		// escalation would then never arrive for a bead that had genuinely
		// missed its threshold.
		//
		// The raise fires on the sweep that REACHES the threshold and on no
		// other, so escalation happens once per bead rather than on every
		// subsequent miss (see DueMissEscalateAt). A bead already at P0 has
		// nowhere to go and is counted, rescheduled, and left at P0.
		missed := c.dueMissed + 1
		raisedTo, raised := escalatedPriority(c.priority, missed)
		priorityClause := ""
		args := []any{next}
		if raised {
			priorityClause = ", priority = ?"
			args = append(args, raisedTo)
		}

		// The predicate is repeated so a bead closed, deferred, or rescheduled
		// between the SELECT and here matches nothing and is left alone rather
		// than clobbered.
		args = append(args, missed, now, freshRowLock(), c.id, c.dueAt)
		args = append(args, statusArgs...)
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET due_at = ?%s, due_missed = ?, updated_at = ?, row_lock = ?
			WHERE id = ? AND due_at = ? AND status IN (%s)
		`, table, priorityClause, statusList), args...)
		if err != nil {
			return fired, escalated, fmt.Errorf("due sweep reschedule %s: %w", c.id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fired, escalated, fmt.Errorf("due sweep reschedule %s rows affected: %w", c.id, err)
		}
		if n == 0 {
			continue // rescued concurrently — leave it be
		}
		if err := RecordFullEventInTable(ctx, tx, eventsTable, c.id, types.EventDue,
			DueTriggerActor, c.dueAt.UTC().Format(time.RFC3339), next.Format(time.RFC3339)); err != nil {
			return fired, escalated, fmt.Errorf("record due event for %s: %w", c.id, err)
		}
		// A reschedule is a field change, so it journals as an update — past
		// the rows-affected recheck, so a concurrently-rescued bead records
		// nothing.
		if err := RecordEventInTx(ctx, tx, EventUpdate, c.id, DueTriggerActor); err != nil {
			return fired, escalated, err
		}
		fired = append(fired, c.id)
		if raised {
			escalated = append(escalated, c.id)
		}
	}
	return fired, escalated, nil
}

// statusPlaceholders renders a status set as a bound-parameter list, so the
// vocabulary reaches SQL as arguments rather than interpolated text.
func statusPlaceholders(statuses []types.Status) (string, []any) {
	marks := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, status := range statuses {
		marks[i] = "?"
		args[i] = string(status)
	}
	return strings.Join(marks, ", "), args
}

// escalatedPriority decides whether THIS miss raises the bead's priority, and
// to what.
//
// It raises on the miss that REACHES DueMissEscalateAt and on no other, which
// is what makes the escalation a step rather than a ramp: `>=` would raise
// again on every subsequent miss and walk a chronically-late bead to P0 one
// rung per miss, until a workspace with a stale backlog has nothing but P0s
// and the priority field has stopped meaning anything.
//
// P0 is the ceiling and is expressed as "there is somewhere to go" rather than
// as a named floor value: a bead already at the top is counted and rescheduled
// like any other, and simply has no raise to apply.
func escalatedPriority(current, missed int) (raised int, ok bool) {
	if missed != DueMissEscalateAt || current <= 0 {
		return current, false
	}
	return current - 1, true
}

// ScheduledSweepResult aggregates the two lazy time-based sweeps a read path
// runs before serving the ready front: returning expired defers to open, and
// firing beads whose due date has arrived.
type ScheduledSweepResult struct {
	Defers WakeDefersResult
	Due    DueSweepResult
}

// IssueRows reports how many PERMANENT-table rows the sweeps changed. Only
// these decide whether the caller mints a Dolt commit; wisp tables are
// dolt_ignored.
func (r ScheduledSweepResult) IssueRows() int { return len(r.Defers.Issues) + len(r.Due.Issues) }

// WispRows reports how many wisp-plane rows the sweeps changed. A wisp-only
// change still needs a plain SQL commit, but mints no version commit.
func (r ScheduledSweepResult) WispRows() int { return len(r.Defers.Wisps) + len(r.Due.Wisps) }

// CommitMessage names the Dolt commit for a sweep that changed permanent rows.
// Callers with IssueRows() == 0 should not commit at all.
func (r ScheduledSweepResult) CommitMessage() string {
	switch {
	case len(r.Due.Issues) == 0:
		return WakeDefersCommitMessage(len(r.Defers.Issues))
	case len(r.Defers.Issues) == 0:
		return DueSweepCommitMessage(len(r.Due.Issues))
	default:
		return fmt.Sprintf("bd: wake %d expired defer(s), trigger %d due bead(s)",
			len(r.Defers.Issues), len(r.Due.Issues))
	}
}

// RunScheduledSweepsInTx runs both time-based sweeps in one transaction. It is
// what read paths call: the two fire at the same moment, are advisory in the
// same way, and sharing a transaction means a ready listing pays for one write
// round trip rather than two.
//
// The defer wake runs FIRST so a bead whose defer expires and whose due date
// has also passed is returned to open before the due sweep looks at it, and
// fires in the same pass instead of waiting for the next read.
func RunScheduledSweepsInTx(ctx context.Context, tx DBTX) (ScheduledSweepResult, error) {
	var result ScheduledSweepResult
	defers, err := WakeExpiredDefersInTx(ctx, tx)
	if err != nil {
		return result, err
	}
	result.Defers = defers
	due, err := SweepDueBeadsInTx(ctx, tx)
	if err != nil {
		return result, err
	}
	result.Due = due
	return result, nil
}
