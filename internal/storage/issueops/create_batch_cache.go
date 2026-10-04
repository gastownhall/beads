package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// createBatchCache amortizes the per-issue lookups a multi-issue create
// (import, bulk create) would otherwise pay one round trip each for. It is
// built once per CreateIssuesInTxWithContext call and changes HOW the batch
// learns facts, never WHICH facts it acts on:
//
//   - Row presence. The cross-plane collision probe and the new-vs-upsert
//     probe each read one id from one table. The cache reads every explicit
//     id of the batch from both planes up front and then tracks the rows the
//     batch itself inserts, so each answer equals what the per-row read would
//     have returned at that point of the batch. Ids it did not read up front
//     (generated ids) fall through to the per-row read.
//   - Label presence. PersistLabels inserts one label per statement and
//     learns from RowsAffected whether the label was new. The cache reads the
//     batch's stored labels up front, so each issue's new labels are known
//     before its insert, which then lands as one multi-row statement.
//   - Issue rows. A run of issues the presence read shows to be brand new is
//     written with multi-row INSERTs and then finished one by one, in order
//     (deferredCreates); the per-row write would have been a plain insert for
//     each of them.
//   - Audit events. created/label_added rows in the events tables are
//     content-addressed (InsertDerivedEvent) and nothing in the batch reads
//     them back, so the cache buffers them and writes them in one pass at the
//     end of the per-issue loop, deriving each id exactly as the per-row
//     insert would have (same digest, same lowest free ordinal, counting
//     same-content rows already stored and those minted earlier in the batch).
//
// A singular create never builds one: its single lookup of each kind costs
// what the cache's batch read would.
//
// COLLATION ASSUMPTION. The cache (and the dependency pass's in-memory graph,
// create_dep_batch.go) compares ids and labels with Go string equality where
// the per-row SQL compared them under the column collation. Those agree under
// the collation every beads table is created with — Dolt's default
// utf8mb4_0900_bin, which is case-sensitive and NO PAD (the migrations name
// no other). On a case-insensitive or PAD SPACE collation they would not: a
// "Bug"/"bug" label pair would land as one row but buffer two label_added
// events. A schema change to such a collation must revisit these paths.
type createBatchCache struct {
	// probed holds the ids whose presence in both planes was read up front.
	probed map[string]bool
	// present is table -> id -> row present, as of the current point of the
	// batch (the up-front read plus every row the batch has inserted since).
	present map[string]map[string]bool
	// labels is label table -> issue id -> stored label set, for the issues
	// whose labels were read up front (see labelsProbed).
	labels       map[string]map[string]map[string]bool
	labelsProbed map[string]map[string]bool
	// events are the buffered events-plane rows, in mint order.
	events []bufferedAuxEvent
}

type bufferedAuxEvent struct {
	table string
	event AuxEvent
}

// createBatchCacheMinIssues is the batch size at which the up-front reads pay
// for themselves. A one-issue create reads exactly what it would read per row.
const createBatchCacheMinIssues = 2

// newCreateBatchCache reads the presence of every explicit id in issues in
// both planes, and the stored labels of every explicit id that carries
// labels, in a handful of IN-list queries.
func newCreateBatchCache(ctx context.Context, tx DBTX, issues []*types.Issue) (*createBatchCache, error) {
	c := &createBatchCache{
		probed:       map[string]bool{},
		present:      map[string]map[string]bool{"issues": {}, "wisps": {}},
		labels:       map[string]map[string]map[string]bool{"labels": {}, "wisp_labels": {}},
		labelsProbed: map[string]map[string]bool{"labels": {}, "wisp_labels": {}},
	}
	var ids []string
	labelIDs := map[string][]string{}
	for _, issue := range issues {
		if issue == nil || issue.ID == "" || c.probed[issue.ID] {
			continue
		}
		c.probed[issue.ID] = true
		ids = append(ids, issue.ID)
	}
	for _, table := range []string{"issues", "wisps"} {
		found, err := idSetInTx(ctx, tx, table, ids)
		if err != nil {
			return nil, err
		}
		for id := range found {
			c.present[table][id] = true
		}
	}
	seenLabelID := map[string]map[string]bool{"labels": {}, "wisp_labels": {}}
	for _, issue := range issues {
		if issue == nil || issue.ID == "" || len(issue.Labels) == 0 {
			continue
		}
		table := "labels"
		if IsWisp(issue) {
			table = "wisp_labels"
		}
		if !seenLabelID[table][issue.ID] {
			seenLabelID[table][issue.ID] = true
			labelIDs[table] = append(labelIDs[table], issue.ID)
		}
	}
	for _, table := range []string{"labels", "wisp_labels"} {
		if len(labelIDs[table]) == 0 {
			continue
		}
		if err := c.loadLabels(ctx, tx, table, labelIDs[table]); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// idSetInTx returns the subset of ids present in table (issues or wisps).
//
//nolint:gosec // G201: table is one of two hardcoded constants; only placeholders are formatted in.
func idSetInTx(ctx context.Context, tx DBTX, table string, ids []string) (map[string]bool, error) {
	found := map[string]bool{}
	for start := 0; start < len(ids); start += queryBatchSize {
		end := min(start+queryBatchSize, len(ids))
		placeholders, args := buildSQLInClause(ids[start:end])
		rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT id FROM %s WHERE id IN (%s)", table, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("read %s presence for batch: %w", table, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("read %s presence for batch: %w", table, err)
			}
			found[id] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read %s presence for batch: %w", table, err)
		}
	}
	return found, nil
}

//nolint:gosec // G201: table is one of two hardcoded constants; only placeholders are formatted in.
func (c *createBatchCache) loadLabels(ctx context.Context, tx DBTX, table string, ids []string) error {
	for _, id := range ids {
		c.labelsProbed[table][id] = true
	}
	for start := 0; start < len(ids); start += queryBatchSize {
		end := min(start+queryBatchSize, len(ids))
		placeholders, args := buildSQLInClause(ids[start:end])
		rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT issue_id, label FROM %s WHERE issue_id IN (%s)", table, placeholders), args...)
		if err != nil {
			return fmt.Errorf("read stored labels for batch: %w", err)
		}
		for rows.Next() {
			var issueID, label string
			if err := rows.Scan(&issueID, &label); err != nil {
				_ = rows.Close()
				return fmt.Errorf("read stored labels for batch: %w", err)
			}
			c.addLabel(table, issueID, label)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read stored labels for batch: %w", err)
		}
	}
	return nil
}

func (c *createBatchCache) addLabel(table, issueID, label string) {
	set := c.labels[table][issueID]
	if set == nil {
		set = map[string]bool{}
		c.labels[table][issueID] = set
	}
	set[label] = true
}

// rowPresent answers "is there a row with id in table right now" when the
// cache can; ok is false for an id it did not read up front.
func (c *createBatchCache) rowPresent(table, id string) (present, ok bool) {
	if c == nil || !c.probed[id] {
		return false, false
	}
	return c.present[table][id], true
}

// rowCount adapts rowPresent to the COUNT(*) shape the per-row probes scan.
func (c *createBatchCache) rowCount(ctx context.Context, tx DBTX, table, id string) (int, error) {
	if present, ok := c.rowPresent(table, id); ok {
		if present {
			return 1, nil
		}
		return 0, nil
	}
	var count int
	//nolint:gosec // G201: table is one of two hardcoded constants.
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, table), id).Scan(&count)
	return count, err
}

// markInserted records that the batch wrote (inserted or upserted) a row with
// id into table.
func (c *createBatchCache) markInserted(table, id string) {
	if c == nil {
		return
	}
	c.present[table][id] = true
}

// storedLabels returns the labels stored for issueID in table, reading them
// when the up-front read did not cover the issue (a generated id).
//
//nolint:gosec // G201: table is one of two hardcoded constants.
func (c *createBatchCache) storedLabels(ctx context.Context, tx DBTX, table, issueID string) (map[string]bool, error) {
	if !c.labelsProbed[table][issueID] {
		if err := c.loadLabels(ctx, tx, table, []string{issueID}); err != nil {
			return nil, err
		}
	}
	return c.labels[table][issueID], nil
}

// bufferEvent queues one events-plane row for flushEvents.
func (c *createBatchCache) bufferEvent(table string, e AuxEvent) {
	c.events = append(c.events, bufferedAuxEvent{table: table, event: normalizeAuxEvent(table, e)})
}

// auxEventKey is the same-content key InsertDerivedEventReturningID matches on.
type auxEventKey struct {
	issueID, eventType, actor   string
	oldValue, newValue, comment sql.NullString
	createdAt                   string
}

func keyOfAuxEvent(e AuxEvent) auxEventKey {
	return auxEventKey{
		issueID: e.IssueID, eventType: string(e.EventType), actor: e.Actor,
		oldValue: e.OldValue, newValue: e.NewValue, comment: e.Comment,
		createdAt: e.CreatedAt,
	}
}

// flushEvents writes the buffered events, per table, under the ids the
// per-row InsertDerivedEvent would have minted: it reads the stored rows that
// could share content with a buffered one (same issue, same second), then
// assigns each buffered row the lowest free ordinal of its digest, counting
// those stored rows and the rows minted before it in this flush.
func (c *createBatchCache) flushEvents(ctx context.Context, tx DBTX) error {
	if c == nil || len(c.events) == 0 {
		return nil
	}
	byTable := map[string][]AuxEvent{}
	var tables []string
	for _, b := range c.events {
		if _, ok := byTable[b.table]; !ok {
			tables = append(tables, b.table)
		}
		byTable[b.table] = append(byTable[b.table], b.event)
	}
	c.events = nil
	for _, table := range tables {
		if err := flushAuxEvents(ctx, tx, table, byTable[table]); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gosec // G201: table is a hardcoded routing constant; only placeholders are formatted in.
func flushAuxEvents(ctx context.Context, tx DBTX, table string, events []AuxEvent) error {
	taken := map[auxEventKey]map[string]bool{}
	var issueIDs, createdAts []string
	seenIssue, seenAt := map[string]bool{}, map[string]bool{}
	for _, e := range events {
		if !seenIssue[e.IssueID] {
			seenIssue[e.IssueID] = true
			issueIDs = append(issueIDs, e.IssueID)
		}
		if !seenAt[e.CreatedAt] {
			seenAt[e.CreatedAt] = true
			createdAts = append(createdAts, e.CreatedAt)
		}
	}
	atPlaceholders, atArgs := buildSQLInClause(createdAts)
	for start := 0; start < len(issueIDs); start += queryBatchSize {
		end := min(start+queryBatchSize, len(issueIDs))
		idPlaceholders, idArgs := buildSQLInClause(issueIDs[start:end])
		args := append(append([]any{}, idArgs...), atArgs...)
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT id, issue_id, event_type, actor, old_value, new_value, comment,
			       DATE_FORMAT(created_at, '%%Y-%%m-%%d %%H:%%i:%%s')
			FROM %s
			WHERE issue_id IN (%s) AND created_at IN (%s)`, table, idPlaceholders, atPlaceholders), args...)
		if err != nil {
			return fmt.Errorf("scan same-content events in %s: %w", table, err)
		}
		for rows.Next() {
			var id string
			var k auxEventKey
			if err := rows.Scan(&id, &k.issueID, &k.eventType, &k.actor, &k.oldValue, &k.newValue, &k.comment, &k.createdAt); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan same-content events in %s: %w", table, err)
			}
			if taken[k] == nil {
				taken[k] = map[string]bool{}
			}
			taken[k][id] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("scan same-content events in %s: %w", table, err)
		}
	}

	const cols = 8
	const rowsPerInsert = queryBatchSize
	for start := 0; start < len(events); start += rowsPerInsert {
		end := min(start+rowsPerInsert, len(events))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*cols)
		for _, e := range events[start:end] {
			k := keyOfAuxEvent(e)
			if taken[k] == nil {
				taken[k] = map[string]bool{}
			}
			id := firstFreeDerivedID(table, auxEventDigest(e), taken[k])
			taken[k][id] = true
			values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, id, e.IssueID, string(e.EventType), e.Actor, e.OldValue, e.NewValue, e.Comment, e.CreatedAt)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
			VALUES %s`, table, strings.Join(values, ", ")), args...); err != nil {
			return fmt.Errorf("record events in %s: %w", table, err)
		}
	}
	return nil
}

// deferrable reports whether issue's row can join a multi-row INSERT: the
// batch knows, from its up-front read and its own writes, that no row with
// this id exists in either plane, so the per-row write would be a plain
// insert (InsertIssueIfNew's existence probe answers 0; ConflictSkip and
// RejectStaleUpserts only act on an existing row). CreateOnly batches keep the
// per-row path for its shard coordination write.
func (c *createBatchCache) deferrable(issue *types.Issue, opts storage.BatchCreateOptions) bool {
	if c == nil || issue == nil || issue.ID == "" || opts.CreateOnly || !c.probed[issue.ID] {
		return false
	}
	return !c.present["issues"][issue.ID] && !c.present["wisps"][issue.ID]
}

// deferredCreate is one prepared issue whose row write is pending.
type deferredCreate struct {
	issue                  *types.Issue
	issueTable, eventTable string
}

// deferredCreates is a run of prepared, brand-new issues. Writing their rows
// together and then finishing each in order is invisible to the batch: every
// finishing step (lease, events, labels, comments, journal, version seam)
// reads only its own issue's rows, which are all written by then.
type deferredCreates struct {
	created []deferredCreate
}

// flush writes the pending rows per table in multi-row INSERTs, then
// finishes each issue in order, returning one result per pending issue.
func (d *deferredCreates) flush(ctx context.Context, tx DBTX, bc *BatchContext, actor string) ([]CreateIssueResult, error) {
	if len(d.created) == 0 {
		return nil, nil
	}
	byTable := map[string][]*types.Issue{}
	var tables []string
	for _, c := range d.created {
		if _, ok := byTable[c.issueTable]; !ok {
			tables = append(tables, c.issueTable)
		}
		byTable[c.issueTable] = append(byTable[c.issueTable], c.issue)
	}
	for _, table := range tables {
		if err := insertIssueRowsIntoTable(ctx, tx, table, byTable[table], bc.Opts.RejectStaleUpserts); err != nil {
			return nil, err
		}
	}
	results := make([]CreateIssueResult, len(d.created))
	for i, c := range d.created {
		result, err := finishCreateIssueInTx(ctx, tx, bc, c.issue, actor, c.issueTable, c.eventTable, true)
		if err != nil {
			return nil, err
		}
		results[i] = result
	}
	return results, nil
}
