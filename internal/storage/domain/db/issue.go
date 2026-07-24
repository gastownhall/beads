package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func NewIssueSQLRepository(runner Runner) domain.IssueSQLRepository {
	return &issueSQLRepositoryImpl{
		runner: runner,
		events: NewEventsSQLRepository(runner),
	}
}

type issueSQLRepositoryImpl struct {
	runner Runner
	events domain.EventsSQLRepository
}

var _ domain.IssueSQLRepository = (*issueSQLRepositoryImpl)(nil)

// issueSelectColumns aliases the shared canonical column list; the scan side
// delegates to issueops.ScanIssueFrom, which scans it positionally.
const issueSelectColumns = sqlbuild.IssueSelectColumns

var allowedUpdateFields = map[string]struct{}{
	"status": {}, "priority": {}, "title": {}, "assignee": {},
	"description": {}, "design": {}, "acceptance_criteria": {}, "notes": {},
	"issue_type": {}, "estimated_minutes": {}, "external_ref": {}, "spec_id": {},
	"started_at": {}, "closed_at": {}, "close_reason": {}, "closed_by_session": {},
	"source_repo": {}, "sender": {}, "wisp": {}, "wisp_type": {}, "no_history": {}, "pinned": {},
	"mol_type": {}, "event_kind": {}, "actor": {}, "target": {}, "payload": {},
	"due_at": {}, "defer_until": {}, "await_id": {}, "waiters": {},
	"metadata": {},
}

var updateFieldColumnRename = map[string]string{
	"wisp": "ephemeral",
}

func (r *issueSQLRepositoryImpl) Insert(ctx context.Context, issue *types.Issue, actor string, opts domain.InsertIssueOpts) error {
	if issue == nil {
		return errors.New("db: Insert: issue must not be nil")
	}

	normalizeIssueTimestamps(issue)
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	if issue.ID == "" {
		return errors.New("db: Insert: explicit ID required (ID generation belongs to CreateIssueUseCase)")
	}

	table := pickIssueTable(opts.UseWispsTable)
	if err := insertIssueRow(ctx, r.runner, table, issue); err != nil {
		return err
	}
	return r.events.Record(ctx, domain.Event{
		IssueID: issue.ID,
		Type:    types.EventCreated,
		Actor:   actor,
	}, domain.RecordEventOpts{UseWispsTable: opts.UseWispsTable})
}

func (r *issueSQLRepositoryImpl) InsertBatch(ctx context.Context, issues []*types.Issue, actor string, opts domain.InsertIssueOpts) error {
	for _, issue := range issues {
		if err := r.Insert(ctx, issue, actor, opts); err != nil {
			return err
		}
	}
	return nil
}

func (r *issueSQLRepositoryImpl) Update(ctx context.Context, id string, updates map[string]any, actor string, opts domain.IssueTableOpts) error {
	if id == "" {
		return errors.New("db: Update: id must not be empty")
	}
	if len(updates) == 0 {
		return nil
	}

	// Bound the VARCHAR(255) assignment columns before touching SQL, mirroring
	// issueops.updateIssueInTx: an over-length assignee/owner aborts with a typed
	// ErrFieldTooLong instead of a raw backend "data too long" error.
	for _, field := range []string{"assignee", "owner"} {
		if raw, ok := updates[field]; ok {
			if val, ok := raw.(string); ok {
				if err := types.CheckFieldLen(field, val); err != nil {
					return err
				}
			}
		}
	}

	table := pickIssueTable(opts.UseWispsTable)

	_, statusChanging := updates["status"]
	mergeOps := issueops.HasMergeOps(updates)

	// When the status changes we need the prior row to reproduce the embedded
	// lifecycle side effects (issueops.updateIssueInTx): closed_at is set on
	// close and cleared on reopen, started_at is set on the in_progress
	// transition, the audit event type is derived from the transition, and
	// is_blocked is recomputed for neighbors. Read the full old issue once so
	// all four use the same snapshot; the ErrNoRows contract is preserved.
	// Merge operations (metadata edits, note appends) need the same read: they
	// are resolved against the row as seen by THIS unit-of-work transaction.
	var oldIssue *types.Issue
	if statusChanging || mergeOps {
		var err error
		oldIssue, err = r.Get(ctx, id, opts)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("db: Update %s: %w", id, sql.ErrNoRows)
			}
			return fmt.Errorf("db: Update %s: read old issue: %w", id, err)
		}
	}

	// Resolve read-merge-write operation keys (issueops.OpMergeMetadata,
	// OpSetMetadata, OpUnsetMetadata, OpAppendNotes) into concrete column
	// values inside the mutation transaction, mirroring the embedded path
	// (issueops.updateIssueInTx). Callers must pass the OPERATION, never a
	// value pre-merged from an earlier read: this runner is a Dolt sql-server
	// session where FOR UPDATE is a parse-only no-op, so a stale-snapshot merge
	// is only made safe by Dolt's commit-time conflict detection plus the
	// caller redoing the whole unit of work on a serialization failure — and
	// that redo re-runs this in-transaction resolution against the winner's
	// committed row.
	if mergeOps {
		resolved, err := issueops.ResolveMergeOps(oldIssue, updates)
		if err != nil {
			return fmt.Errorf("db: Update %s: %w", id, err)
		}
		updates = resolved
	}

	setClauses := make([]string, 0, len(updates)+3)
	args := make([]any, 0, len(updates)+4)
	for key, value := range updates {
		if _, ok := allowedUpdateFields[key]; !ok {
			return fmt.Errorf("db: Update: field %q is not allowed", key)
		}
		column := key
		if renamed, ok := updateFieldColumnRename[key]; ok {
			column = renamed
		}
		setClauses = append(setClauses, fmt.Sprintf("`%s` = ?", column))
		args = append(args, normalizeUpdateValue(key, value))
	}
	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, time.Now().UTC())

	// Lifecycle parity with issueops.updateIssueInTx: auto-manage closed_at and
	// started_at from the status transition unless the caller set them
	// explicitly. Both helpers no-op when the status is unchanged.
	if statusChanging {
		setClauses, args = issueops.ManageClosedAt(oldIssue, updates, setClauses, args)
		setClauses, args = issueops.ManageStartedAt(oldIssue, updates, setClauses, args)
	}

	// Rewrite row_lock on every generic update, mirroring the classic
	// issueops.updateIssueInTx invariant (update.go): a concurrent
	// status/ownership mutation collides on this shared cell instead of
	// silently cell-merging, and the row's RowVersion CAS token advances so
	// the "generic update path changes RowVersion" contract
	// (types.Issue.RowVersion) holds on the proxied backend too.
	rowLockClause, rowLockArgs := issueops.RowLockClause()
	setClauses = append(setClauses, rowLockClause)
	args = append(args, rowLockArgs...)

	args = append(args, id)

	//nolint:gosec // G201: table is one of two hardcoded constants
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", table, strings.Join(setClauses, ", "))
	res, err := r.runner.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("db: Update %s: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: Update %s: rows affected: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("db: Update %s: %w", id, sql.ErrNoRows)
	}

	// Event-type parity: embedded records EventClosed / EventReopened /
	// EventStatusChanged for status transitions (issueops.DetermineEventType),
	// EventUpdated otherwise.
	eventType := types.EventUpdated
	if statusChanging {
		eventType = issueops.DetermineEventType(oldIssue, updates)
	}
	if err := r.events.Record(ctx, domain.Event{
		IssueID: id,
		Type:    eventType,
		Actor:   actor,
	}, domain.RecordEventOpts{UseWispsTable: opts.UseWispsTable}); err != nil {
		return err
	}

	if statusChanging {
		newStatus := coerceStatus(updates["status"])
		oldActive := oldIssue.Status != types.StatusClosed && oldIssue.Status != types.StatusPinned
		newActive := newStatus != types.StatusClosed && newStatus != types.StatusPinned
		if oldActive != newActive {
			var (
				affectedIssues, affectedWisps []string
				aerr                          error
			)
			if opts.UseWispsTable {
				affectedIssues, affectedWisps, aerr = issueops.AffectedByStatusChangeForWispInTx(ctx, r.runner, id)
			} else {
				affectedIssues, affectedWisps, aerr = issueops.AffectedByStatusChangeInTx(ctx, r.runner, id)
			}
			if aerr != nil {
				return fmt.Errorf("db: Update %s: affected by status change: %w", id, aerr)
			}
			if err := issueops.RecomputeIsBlockedInTx(ctx, r.runner, affectedIssues, affectedWisps); err != nil {
				return fmt.Errorf("db: Update %s: recompute is_blocked: %w", id, err)
			}
		}
	}
	return nil
}

func coerceStatus(v any) types.Status {
	switch s := v.(type) {
	case string:
		return types.Status(s)
	case types.Status:
		return s
	default:
		return ""
	}
}

func (r *issueSQLRepositoryImpl) Claim(ctx context.Context, id, actor string, opts domain.IssueTableOpts) (domain.ClaimRowResult, error) {
	if id == "" {
		return domain.ClaimRowResult{}, errors.New("db: Claim: id must not be empty")
	}
	// The CAS below writes assignee = actor. actor is user-settable (--actor /
	// BEADS_ACTOR), so bound it against the VARCHAR(255) assignee column up front
	// and return a typed ErrFieldTooLong rather than a raw backend error.
	if err := types.CheckFieldLen("actor", actor); err != nil {
		return domain.ClaimRowResult{}, err
	}

	oldIssue, err := r.Get(ctx, id, opts)
	if err != nil {
		return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: read old issue: %w", id, err)
	}

	table := pickIssueTable(opts.UseWispsTable)
	now := time.Now().UTC()
	startedWasZero := oldIssue.StartedAt == nil

	// Rewrite row_lock exactly like the primary claim path (issueops.
	// ClaimIssueInTx). Without this, a claim made through the proxied-server
	// (uow) path leaves row_lock unchanged — open to the cell-merge bug the
	// row_lock invariant guards against (see issueops/lease.go). The lease
	// itself is granted into the ephemeral leases table below, after the CAS.
	rowLockClause, rowLockArgs := issueops.RowLockClause()

	// Mirror the primary path's pool-aware predicate (bd-bguz6): aliases in
	// the claim.pools config are claimable by any actor. This dual must stay
	// in lockstep with issueops.ClaimIssueInTx — the lease comment above is
	// the scar from the last time it drifted.
	pools, err := issueops.ClaimPoolAliasesInTx(ctx, r.runner)
	if err != nil {
		return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: resolve claim pools: %w", id, err)
	}
	assigneePredicate := "assignee = '' OR assignee IS NULL OR assignee = ?"
	assigneeArgs := []any{actor}
	for _, pool := range pools {
		assigneePredicate += " OR assignee = ?"
		assigneeArgs = append(assigneeArgs, pool)
	}

	// Same lockstep for the source statuses (bd-pq7m2): claimable from "open"
	// plus custom active-category statuses, like the primary path — not a
	// hardcoded status = 'open'.
	claimableStatuses, err := issueops.ClaimableSourceStatusesInTx(ctx, r.runner)
	if err != nil {
		return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: resolve claimable statuses: %w", id, err)
	}
	statusPredicate := "status = ?"
	statusArgs := []any{claimableStatuses[0]}
	for _, st := range claimableStatuses[1:] {
		statusPredicate += " OR status = ?"
		statusArgs = append(statusArgs, st)
	}

	var res sql.Result
	if startedWasZero {
		args := append([]any{actor, now, now}, rowLockArgs...)
		args = append(args, id)
		args = append(args, statusArgs...)
		args = append(args, assigneeArgs...)
		//nolint:gosec // G201: table is one of two hardcoded constants
		res, err = r.runner.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, status = 'in_progress', updated_at = ?, started_at = ?, %s
			WHERE id = ? AND (%s) AND (%s)
		`, table, rowLockClause, statusPredicate, assigneePredicate), args...)
	} else {
		args := append([]any{actor, now}, rowLockArgs...)
		args = append(args, id)
		args = append(args, statusArgs...)
		args = append(args, assigneeArgs...)
		//nolint:gosec // G201: table is one of two hardcoded constants
		res, err = r.runner.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET assignee = ?, status = 'in_progress', updated_at = ?, %s
			WHERE id = ? AND (%s) AND (%s)
		`, table, rowLockClause, statusPredicate, assigneePredicate), args...)
	}
	if err != nil {
		return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: rows affected: %w", id, err)
	}

	if rows == 0 {
		var currentAssignee sql.NullString
		var currentStatus types.Status
		//nolint:gosec // G201: table is one of two hardcoded constants
		if err := r.runner.QueryRowContext(ctx,
			fmt.Sprintf("SELECT assignee, status FROM %s WHERE id = ?", table), id,
		).Scan(&currentAssignee, &currentStatus); err != nil {
			return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: read current state: %w", id, err)
		}
		assignee := ""
		if currentAssignee.Valid {
			assignee = currentAssignee.String
		}
		return domain.ClaimRowResult{
			Updated:               false,
			CurrentAssignee:       assignee,
			CurrentAssigneeIsPool: slices.Contains(pools, assignee),
			CurrentStatus:         currentStatus,
			StartedAtWasZero:      startedWasZero,
			OldIssue:              oldIssue,
		}, nil
	}

	// Grant the lease in the ephemeral leases table, mirroring
	// issueops.ClaimIssueInTx. Wisps are never leased. This dual must stay in
	// lockstep with the primary path (see the row_lock comment above).
	if !opts.UseWispsTable {
		if err := issueops.UpsertLeaseInTx(ctx, r.runner, id, actor, now, issueops.LeaseTTL(ctx)); err != nil {
			return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: %w", id, err)
		}
	}

	oldData, _ := json.Marshal(oldIssue)
	newData, _ := json.Marshal(map[string]any{"assignee": actor, "status": "in_progress"})
	if err := r.events.Record(ctx, domain.Event{
		IssueID:  id,
		Type:     types.EventType("claimed"),
		Actor:    actor,
		OldValue: string(oldData),
		NewValue: string(newData),
	}, domain.RecordEventOpts{UseWispsTable: opts.UseWispsTable}); err != nil {
		return domain.ClaimRowResult{}, fmt.Errorf("db: Claim %s: record event: %w", id, err)
	}

	return domain.ClaimRowResult{
		Updated:          true,
		CurrentAssignee:  actor,
		CurrentStatus:    types.StatusInProgress,
		StartedAtWasZero: startedWasZero,
		OldIssue:         oldIssue,
	}, nil
}

func (r *issueSQLRepositoryImpl) Get(ctx context.Context, id string, opts domain.IssueTableOpts) (*types.Issue, error) {
	if id == "" {
		return nil, errors.New("db: Get: id must not be empty")
	}
	table := pickIssueTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	row := r.runner.QueryRowContext(ctx, fmt.Sprintf("SELECT %s FROM %s %s WHERE id = ?",
		issueSelectColumns, table, sqlbuild.LeaseJoin(table)), id)
	issue, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("db: Get %s: %w", id, err)
	}
	return issue, nil
}

func (r *issueSQLRepositoryImpl) GetByIDs(ctx context.Context, ids []string, opts domain.IssueTableOpts) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	table := pickIssueTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	q := fmt.Sprintf("SELECT %s FROM %s %s WHERE id IN (%s)",
		issueSelectColumns, table, sqlbuild.LeaseJoin(table), strings.Join(placeholders, ","))
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: GetByIDs: %w", err)
	}
	defer rows.Close()

	var out []*types.Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("db: GetByIDs: scan: %w", err)
		}
		out = append(out, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: GetByIDs: rows: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) Exists(ctx context.Context, id string, opts domain.IssueTableOpts) (bool, error) {
	if id == "" {
		return false, errors.New("db: Exists: id must not be empty")
	}
	table := pickIssueTable(opts.UseWispsTable)
	//nolint:gosec // G201: table is one of two hardcoded constants
	row := r.runner.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE id = ? LIMIT 1", table), id)
	var one int
	err := row.Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: Exists %s: %w", id, err)
	}
	return true, nil
}

func (r *issueSQLRepositoryImpl) CountForPrefix(ctx context.Context, prefix string, opts domain.IssueTableOpts) (int, error) {
	if prefix == "" {
		return 0, errors.New("db: CountForPrefix: prefix must not be empty")
	}
	table := pickIssueTable(opts.UseWispsTable)
	var count int
	//nolint:gosec // G201: table is one of two hardcoded constants
	err := r.runner.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s
		WHERE id LIKE CONCAT(?, '-%%')
		  AND INSTR(SUBSTRING(id, LENGTH(?) + 2), '.') = 0
	`, table), prefix, prefix).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: CountForPrefix %s: %w", prefix, err)
	}
	return count, nil
}

func (r *issueSQLRepositoryImpl) NextCounterID(ctx context.Context, prefix string) (int, error) {
	if prefix == "" {
		return 0, errors.New("db: NextCounterID: prefix must not be empty")
	}

	res, err := r.runner.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
	if err != nil {
		return 0, fmt.Errorf("db: NextCounterID: increment %q: %w", prefix, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: NextCounterID: rows affected %q: %w", prefix, err)
	}

	if rows == 0 {
		if err := r.seedCounterFromExisting(ctx, prefix); err != nil {
			return 0, fmt.Errorf("db: NextCounterID: seed %q: %w", prefix, err)
		}
		res, err = r.runner.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
		if err != nil {
			return 0, fmt.Errorf("db: NextCounterID: increment after seed %q: %w", prefix, err)
		}
		rows, err = res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("db: NextCounterID: rows affected after seed %q: %w", prefix, err)
		}
		if rows == 0 {
			if _, err := r.runner.ExecContext(ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES (?, 1)", prefix); err != nil {
				return 0, fmt.Errorf("db: NextCounterID: insert initial %q: %w", prefix, err)
			}
		}
	}

	var nextID int
	if err := r.runner.QueryRowContext(ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix).Scan(&nextID); err != nil {
		return 0, fmt.Errorf("db: NextCounterID: read last_id %q: %w", prefix, err)
	}
	return nextID, nil
}

func (r *issueSQLRepositoryImpl) seedCounterFromExisting(ctx context.Context, prefix string) error {
	var existing int
	err := r.runner.QueryRowContext(ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read existing counter %q: %w", prefix, err)
	}

	rows, err := r.runner.QueryContext(ctx, "SELECT id FROM issues WHERE id LIKE CONCAT(?, '-%')", prefix)
	if err != nil {
		return fmt.Errorf("scan issues for %q: %w", prefix, err)
	}
	defer rows.Close()

	maxNum := 0
	pfxDash := prefix + "-"
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		suffix := strings.TrimPrefix(id, pfxDash)
		if strings.Contains(suffix, ".") {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil && n > maxNum {
			maxNum = n
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate issues for %q: %w", prefix, err)
	}

	if maxNum > 0 {
		if _, err := r.runner.ExecContext(ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES (?, ?)", prefix, maxNum); err != nil {
			return fmt.Errorf("seed counter %q at %d: %w", prefix, maxNum, err)
		}
	}
	return nil
}

func normalizeIssueTimestamps(issue *types.Issue) {
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	} else {
		issue.CreatedAt = issue.CreatedAt.UTC()
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	} else {
		issue.UpdatedAt = issue.UpdatedAt.UTC()
	}
}

func pickIssueTable(useWisps bool) string {
	if useWisps {
		return "wisps"
	}
	return "issues"
}

//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func insertIssueRow(ctx context.Context, runner Runner, table string, issue *types.Issue) error {
	// Bound the VARCHAR(255) assignment columns at the raw-SQL chokepoint, so
	// every proxied-server (uow) create — single, batch, and import — rejects an
	// over-length assignee/owner with a typed ErrFieldTooLong instead of a raw
	// backend "data too long" error. Mirrors ValidateWithCustom on the embedded
	// create path.
	if err := types.CheckFieldLen("assignee", issue.Assignee); err != nil {
		return err
	}
	if err := types.CheckFieldLen("owner", issue.Owner); err != nil {
		return err
	}
	// Stamp a fresh non-zero row_lock at create, exactly like the classic
	// insertIssueIntoTable (issueops/helpers.go). Without it a proxied-server
	// (uow) create leaves row_lock at the schema DEFAULT 0, so the row's
	// RowVersion CAS token is stale-zero on read — the backend-divergent break
	// the RowVersion contract (types.Issue.RowVersion) forbids. The duplicate-key
	// path rewrites it too so an upsert also advances the token.
	_, err := runner.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, started_at, closed_at, external_ref, spec_id,
			compaction_level, compacted_at, compacted_at_commit, original_size,
			sender, ephemeral, no_history, wisp_type, pinned, is_template,
			mol_type, work_type, source_system, source_repo, close_reason,
			event_kind, actor, target, payload,
			await_type, await_id, timeout_ns, waiters,
			due_at, defer_until, metadata,
			row_lock
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?,
			?
		)
		ON DUPLICATE KEY UPDATE
			content_hash = VALUES(content_hash),
			title = VALUES(title),
			description = VALUES(description),
			design = VALUES(design),
			acceptance_criteria = VALUES(acceptance_criteria),
			notes = VALUES(notes),
			status = VALUES(status),
			priority = VALUES(priority),
			issue_type = VALUES(issue_type),
			assignee = VALUES(assignee),
			estimated_minutes = VALUES(estimated_minutes),
			updated_at = VALUES(updated_at),
			started_at = VALUES(started_at),
			closed_at = VALUES(closed_at),
			external_ref = VALUES(external_ref),
			source_repo = VALUES(source_repo),
			close_reason = VALUES(close_reason),
			metadata = VALUES(metadata),
			row_lock = VALUES(row_lock)
	`, table),
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		string(issue.Status), issue.Priority, string(issue.IssueType), nullString(issue.Assignee), nullIntPtr(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.StartedAt, issue.ClosedAt, nullStringPtr(issue.ExternalRef), issue.SpecID,
		issue.CompactionLevel, issue.CompactedAt, nullStringPtr(issue.CompactedAtCommit), nullIntVal(issue.OriginalSize),
		issue.Sender, issue.Ephemeral, issue.NoHistory, string(issue.WispType), issue.Pinned, issue.IsTemplate,
		string(issue.MolType), string(issue.WorkType), issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONStringArray(issue.Waiters),
		issue.DueAt, issue.DeferUntil, jsonMetadata(issue.Metadata),
		issueops.FreshRowLock(),
	)
	if err != nil {
		return fmt.Errorf("db: insert into %s: %w", table, err)
	}
	return nil
}

type issueScanner interface {
	Scan(dest ...any) error
}

// scanIssue delegates to the classic scan so both stacks hydrate issues with
// identical semantics (bd-6dnrw.44 item 12, extract-don't-duplicate per .46).
// The shared scan reads created_at/updated_at as strings with format
// fallbacks where a hand-rolled sql.NullTime scan hard-fails on any driver
// that hands timestamps back as text.
func scanIssue(s issueScanner) (*types.Issue, error) {
	return issueops.ScanIssueFrom(s)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullStringPtr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullIntPtr(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullIntVal(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

func jsonMetadata(raw json.RawMessage) any {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func formatJSONStringArray(items []string) string {
	if len(items) == 0 {
		return ""
	}
	b, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return string(b)
}

var timestampUpdateFields = map[string]struct{}{
	"started_at": {}, "closed_at": {}, "due_at": {}, "defer_until": {},
}

func normalizeUpdateValue(key string, value any) any {
	if _, ok := timestampUpdateFields[key]; ok {
		switch v := value.(type) {
		case time.Time:
			return v.UTC()
		case *time.Time:
			if v == nil {
				return nil
			}
			t := v.UTC()
			return t
		}
		return value
	}
	switch key {
	case "status":
		if s, ok := value.(types.Status); ok {
			return string(s)
		}
	case "issue_type":
		if t, ok := value.(types.IssueType); ok {
			return string(t)
		}
	case "metadata":
		switch v := value.(type) {
		case json.RawMessage:
			return string(v)
		case []byte:
			return string(v)
		}
	}
	return value
}

func (r *issueSQLRepositoryImpl) SearchAcrossIssuesAndWisps(ctx context.Context, query string, filter types.IssueFilter) (domain.SearchPage, error) {
	return r.searchAcrossIssuesAndWisps(ctx, query, filter)
}

func (r *issueSQLRepositoryImpl) SearchAcrossIssuesAndWispsWithCounts(ctx context.Context, query string, filter types.IssueFilter) (domain.SearchCountsPage, error) {
	return r.searchAcrossIssuesAndWispsWithCounts(ctx, query, filter)
}

func (r *issueSQLRepositoryImpl) GetReadyWork(ctx context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	return r.getReadyWorkUnion(ctx, filter)
}

func (r *issueSQLRepositoryImpl) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	return r.getReadyWorkWithCountsUnion(ctx, filter)
}

func (r *issueSQLRepositoryImpl) Delete(ctx context.Context, id string, opts domain.IssueTableOpts) error {
	table := "issues"
	if opts.UseWispsTable {
		table = "wisps"
	}
	//nolint:gosec // G201: table is a hardcoded constant.
	res, err := r.runner.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", table), id)
	if err != nil {
		return fmt.Errorf("db: IssueSQLRepository.Delete %s from %s: %w", id, table, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: IssueSQLRepository.Delete rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}
	// A deleted issue holds no lease (no-op for wisps, which are never leased).
	if err := issueops.DeleteLeaseInTx(ctx, r.runner, id); err != nil {
		return err
	}
	return nil
}

func (r *issueSQLRepositoryImpl) DeleteByIDs(ctx context.Context, ids []string, opts domain.IssueTableOpts) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	table := "issues"
	if opts.UseWispsTable {
		table = "wisps"
	}
	total := 0
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		//nolint:gosec // G201: table is a hardcoded constant; placeholders are ?.
		res, err := r.runner.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", table, strings.Join(placeholders, ",")),
			args...)
		if err != nil {
			return total, fmt.Errorf("db: IssueSQLRepository.DeleteByIDs from %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("db: IssueSQLRepository.DeleteByIDs rows affected: %w", err)
		}
		total += int(n)
		if !opts.UseWispsTable {
			// Deleted issues hold no leases.
			//nolint:gosec // G201: placeholders are ?.
			if _, err := r.runner.ExecContext(ctx,
				fmt.Sprintf("DELETE FROM leases WHERE issue_id IN (%s)", strings.Join(placeholders, ",")),
				args...); err != nil {
				return total, fmt.Errorf("db: IssueSQLRepository.DeleteByIDs leases: %w", err)
			}
		}
	}
	return total, nil
}

func (r *issueSQLRepositoryImpl) PartitionWispIDs(ctx context.Context, ids []string) ([]string, []string, error) {
	return issueops.PartitionWispIDsInTx(ctx, r.runner, ids)
}

func (r *issueSQLRepositoryImpl) FindAllDependents(ctx context.Context, ids []string) ([]string, error) {
	set, err := issueops.FindAllDependentsInTx(ctx, r.runner, ids)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) AffectedByDeletion(ctx context.Context, issueIDs, wispIDs []string) ([]string, []string, error) {
	return issueops.AffectedByDeletionInTx(ctx, r.runner, issueIDs, wispIDs)
}

func (r *issueSQLRepositoryImpl) RecomputeIsBlocked(ctx context.Context, issueIDs, wispIDs []string) error {
	return issueops.RecomputeIsBlockedInTx(ctx, r.runner, issueIDs, wispIDs)
}

func (r *issueSQLRepositoryImpl) AsOf(ctx context.Context, id, ref string) (*types.Issue, error) {
	return issueops.AsOfInTx(ctx, r.runner, id, ref)
}

func (r *issueSQLRepositoryImpl) Close(ctx context.Context, id string, params domain.CloseRowParams, actor string, opts domain.IssueTableOpts) (domain.CloseRowResult, error) {
	res, err := issueops.CloseIssueInTx(ctx, r.runner, id, params.Reason, actor, params.Session)
	if err != nil {
		return domain.CloseRowResult{}, fmt.Errorf("db: IssueSQLRepository.Close %s: %w", id, err)
	}
	return domain.CloseRowResult{
		Updated:       !res.AlreadyClosed,
		AlreadyClosed: res.AlreadyClosed,
		IsWisp:        res.IsWisp,
	}, nil
}

func (r *issueSQLRepositoryImpl) Reopen(ctx context.Context, id string, params domain.ReopenRowParams, actor string, opts domain.IssueTableOpts) (domain.ReopenRowResult, error) {
	res, err := issueops.ReopenIssueInTx(ctx, r.runner, id, params.Reason, actor)
	if err != nil {
		return domain.ReopenRowResult{}, fmt.Errorf("db: IssueSQLRepository.Reopen %s: %w", id, err)
	}
	return domain.ReopenRowResult{
		Updated:     !res.AlreadyOpen,
		AlreadyOpen: res.AlreadyOpen,
		IsWisp:      res.IsWisp,
	}, nil
}

func (r *issueSQLRepositoryImpl) GetNewlyUnblockedByClose(ctx context.Context, closedID string) ([]*types.Issue, error) {
	out, err := issueops.GetNewlyUnblockedByCloseInTx(ctx, r.runner, closedID)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.GetNewlyUnblockedByClose %s: %w", closedID, err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	out, err := issueops.ClaimReadyIssueInTx(ctx, r.runner, filter, actor)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.ClaimReadyIssue: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) ClaimReadyWisp(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	out, err := issueops.ClaimReadyIssueInTx(ctx, r.runner, filter, actor)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.ClaimReadyWisp: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	out, err := issueops.GetBlockedIssuesInTx(ctx, r.runner, filter)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.GetBlockedIssues: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	stats := &types.Statistics{}
	if err := issueops.ScanIssueCountsInTx(ctx, r.runner, stats); err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.GetStatistics: scan counts: %w", err)
	}
	var blocked int
	if err := r.runner.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM issues
		WHERE is_blocked = 1 AND status <> 'closed' AND status <> 'pinned'
	`).Scan(&blocked); err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.GetStatistics: count blocked: %w", err)
	}
	stats.BlockedIssues = &blocked
	ready := stats.OpenIssues - blocked
	if ready < 0 {
		ready = 0
	}
	stats.ReadyIssues = &ready
	return stats, nil
}

func (r *issueSQLRepositoryImpl) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	n, err := issueops.CountIssuesInTx(ctx, r.runner, query, filter)
	if err != nil {
		return 0, fmt.Errorf("db: IssueSQLRepository.CountIssues: %w", err)
	}
	return int64(n), nil
}

func (r *issueSQLRepositoryImpl) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	out, err := issueops.CountIssuesByGroupInTx(ctx, r.runner, filter, groupBy)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.CountIssuesByGroup: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) History(ctx context.Context, id string) ([]*storage.HistoryEntry, error) {
	out, err := issueops.HistoryInTx(ctx, r.runner, id)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.History: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) IterEvents(ctx context.Context, id string, limit int) (storage.Iter[types.Event], error) {
	events, err := issueops.GetEventsInTx(ctx, r.runner, id, limit)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.IterEvents: %w", err)
	}
	return storage.NewSliceIter(events), nil
}

func (r *issueSQLRepositoryImpl) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	out, err := issueops.GetStaleIssuesInTx(ctx, r.runner, filter)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.GetStaleIssues: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	out, err := issueops.GetEpicsEligibleForClosureInTx(ctx, r.runner)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.GetEpicsEligibleForClosure: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) UnclaimIssue(ctx context.Context, id, actor string, force bool) error {
	if err := issueops.UnclaimIssueInTx(ctx, r.runner, id, actor, force); err != nil {
		return fmt.Errorf("db: IssueSQLRepository.UnclaimIssue: %w", err)
	}
	return nil
}

func (r *issueSQLRepositoryImpl) ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	out, err := issueops.ReclaimExpiredLeasesInTx(ctx, r.runner, cutoff, actor)
	if err != nil {
		return nil, fmt.Errorf("db: IssueSQLRepository.ReclaimExpiredLeases: %w", err)
	}
	return out, nil
}

const deleteBatchSize = 200
