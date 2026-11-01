package schema

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/rowid"
)

// eventsFlipDropCommitMessage is the exact message main migration 0062 gives
// the commit that drops the versioned events table. The pre-flip audit rows
// live at that commit's first parent, so the message is how a clone finds them
// in its own history (gastownhall/beads#7039). It is frozen with 0062's bytes:
// TestEventsFlipDropCommitMessageMatches0062 pins the two together.
const eventsFlipDropCommitMessage = "schema: drop versioned events table for ignored-plane flip (bd-red8u)"

// preflipEventsDigestColumns is the events content the restore reconciles by,
// in rowid digest order: the same frozen columns, with created_at rendered to
// the second, that the aux row-id re-key (auxRekeyTables) and the insert-time
// derivation (issueops.InsertDerivedEvent) hash. preflipEventsInsertColumns is
// the explicit column list the restored rows are written with.
const (
	preflipEventsDigestColumns = "issue_id, event_type, actor, old_value, new_value, comment, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')"
	preflipEventsInsertColumns = "issue_id, event_type, actor, old_value, new_value, comment, created_at, id"
)

// preflipEventsSourceColumns are the historical columns the restore reads; a
// source missing any of them cannot be reconciled with the live table.
var preflipEventsSourceColumns = []string{"issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at"}

// preflipEventsPageSize bounds how many historical issues the restore plans
// at once, and preflipEventsInsertBatch how many rows one INSERT carries.
var (
	preflipEventsPageSize    = 200
	preflipEventsInsertBatch = 200
)

// preflipEventsNoticeOut is where the restore reports what it did. Like
// lostTableNoticeOut it is deliberately NOT terminal-gated: the counts are
// the only sign on a piped or served open that history came back.
var preflipEventsNoticeOut io.Writer = os.Stderr

// seedEventsFromPreflipHistory restores, into the dolt_ignored events table,
// the audit rows this clone still holds in history from before main migration
// 0062 moved events off the versioned plane (gastownhall/beads#7039). It is
// the pre-migration repair for ignored migration 0031, so it runs after
// ignored/0019 has created the table and before the 0031 marker is recorded.
//
// Sources are the first parents of every reachable commit carrying 0062's
// exact drop message, in sorted hash order. Each source is reconciled with the
// live table by content, not by id: the pre-flip rows carry pre-rekey ids, so
// for every live issue the restore inserts max(0, source - live) copies of
// each content digest, under the lowest content-derived ids no row holds yet.
// Existing rows and their ids are never touched, a retry adds nothing twice,
// and across several sources the result is each digest's maximum
// multiplicity, whatever the order.
//
// It writes only the ignored events table: no journal entry, no staging, no
// Dolt commit. A clone with no such commit, or whose source has no events
// table, completes silently.
func seedEventsFromPreflipHistory(ctx context.Context, db DBConn) error {
	sources, err := preflipEventsSources(ctx, db)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return nil
	}
	restored := 0
	for _, ref := range sources {
		n, err := restorePreflipEventsFrom(ctx, db, ref)
		restored += n
		if err != nil {
			return fmt.Errorf("restoring pre-upgrade events from %s: %w", ref, err)
		}
	}
	if restored > 0 {
		fmt.Fprintf(preflipEventsNoticeOut,
			"Restored %d pre-upgrade audit event(s) from this clone's history (gastownhall/beads#7039).\n", restored)
	}
	return nil
}

// preflipEventsSources returns the sorted, de-duplicated first parents of the
// reachable commits carrying eventsFlipDropCommitMessage. A parent that is not
// itself reachable (a shallow clone) is dropped.
func preflipEventsSources(ctx context.Context, db DBConn) ([]string, error) {
	drops, err := preflipQueryStrings(ctx, db, "SELECT commit_hash FROM dolt_log WHERE message = ?", eventsFlipDropCommitMessage)
	if err != nil {
		return nil, fmt.Errorf("reading the events flip commits: %w", err)
	}
	seen := make(map[string]bool)
	var sources []string
	for _, drop := range drops {
		parents, err := preflipQueryStrings(ctx, db,
			"SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = ? AND parent_index = 0", drop)
		if err != nil {
			return nil, fmt.Errorf("reading the parent of %s: %w", drop, err)
		}
		for _, parent := range parents {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			reachable, err := preflipQueryStrings(ctx, db, "SELECT commit_hash FROM dolt_log WHERE commit_hash = ?", parent)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", parent, err)
			}
			if len(reachable) == 0 {
				continue
			}
			if err := validateMigrationRef(parent); err != nil {
				return nil, fmt.Errorf("parent of %s: %w", drop, err)
			}
			sources = append(sources, parent)
		}
	}
	sort.Strings(sources)
	return sources, nil
}

// restorePreflipEventsFrom reconciles one source commit into the live events
// table and returns how many rows it inserted.
func restorePreflipEventsFrom(ctx context.Context, db DBConn, ref string) (int, error) {
	present, err := preflipEventsSourceUsable(ctx, db, ref)
	if err != nil || !present {
		return 0, err
	}
	restored := 0
	after := ""
	for {
		issues, err := preflipEventsIssuePage(ctx, db, ref, after)
		if err != nil {
			return restored, err
		}
		if len(issues) == 0 {
			return restored, nil
		}
		after = issues[len(issues)-1]
		n, err := restorePreflipEventsPage(ctx, db, ref, issues)
		restored += n
		if err != nil {
			return restored, err
		}
	}
}

// preflipEventsSourceUsable reports whether ref carries an events table with
// every column the restore reads. The shape is probed before anything selects
// from it, as ReadMigrationContentHashes does: a failing statement would pin
// a pooled Dolt session to its pre-statement catalog (be-bv7x).
func preflipEventsSourceUsable(ctx context.Context, db DBConn, ref string) (bool, error) {
	//nolint:gosec // G201: ref is validated by preflipEventsSources; AS OF requires a literal.
	hasTable, err := queryHasRows(ctx, db, "SHOW TABLES AS OF '"+ref+"' LIKE 'events'")
	if err != nil || !hasTable {
		return false, err
	}
	//nolint:gosec // G201: as above.
	columns, err := preflipQueryStrings(ctx, db, "SHOW COLUMNS FROM events AS OF '"+ref+"'")
	if err != nil {
		return false, err
	}
	have := make(map[string]bool, len(columns))
	for _, c := range columns {
		have[strings.ToLower(c)] = true
	}
	for _, c := range preflipEventsSourceColumns {
		if !have[c] {
			return false, nil
		}
	}
	return true, nil
}

// preflipEventsIssuePage returns the next page of distinct issue ids the
// source holds events for, in id order, after the given id.
func preflipEventsIssuePage(ctx context.Context, db DBConn, ref, after string) ([]string, error) {
	//nolint:gosec // G201: ref is validated by preflipEventsSources; the limit is an int.
	return preflipQueryStrings(ctx, db,
		"SELECT DISTINCT issue_id FROM events AS OF '"+ref+"' WHERE issue_id > ? ORDER BY issue_id LIMIT "+strconv.Itoa(preflipEventsPageSize),
		after)
}

// preflipEventsContent is one issue's events content: each digest's fields
// and how many rows carry it.
type preflipEventsContent map[string]*preflipEventsGroup

type preflipEventsGroup struct {
	fields []sql.NullString
	count  int
}

func (c preflipEventsContent) add(fields []sql.NullString) string {
	digest := rowid.Digest(fields)
	g, ok := c[digest]
	if !ok {
		g = &preflipEventsGroup{fields: fields}
		c[digest] = g
	}
	g.count++
	return digest
}

// preflipEventsRow is one planned insert.
type preflipEventsRow struct {
	fields []sql.NullString
	id     string
}

// restorePreflipEventsPage reconciles one page of source issues with the live
// table. Every source row for the page is read, and its result set closed,
// before anything is written: the restore runs on the migration's pinned
// connection, which cannot interleave an open result with a write.
func restorePreflipEventsPage(ctx context.Context, db DBConn, ref string, issues []string) (int, error) {
	source, err := readPreflipEventsContent(ctx, db, "events AS OF '"+ref+"'", issues, nil)
	if err != nil {
		return 0, err
	}
	taken := make(map[string]bool)
	live, err := readPreflipEventsContent(ctx, db, "events", issues, taken)
	if err != nil {
		return 0, fmt.Errorf("reading live events: %w", err)
	}
	for {
		plan := planPreflipEventsInserts(issues, source, live, taken)
		if len(plan) == 0 {
			return 0, nil
		}
		// A derived id is a function of content, so only a same-content row of
		// the same issue can hold it -- unless the row was re-pointed since
		// (fk_events_issue cascades an issue rename onto events.issue_id but not
		// onto the id). Probe the whole plan against the table and re-plan
		// around any id already held, so a plain INSERT cannot collide.
		held, err := heldPreflipEventIDs(ctx, db, plan)
		if err != nil {
			return 0, fmt.Errorf("reading live event ids: %w", err)
		}
		if len(held) > 0 {
			for _, id := range held {
				taken[id] = true
			}
			continue
		}
		return insertPreflipEvents(ctx, db, plan)
	}
}

// readPreflipEventsContent reads the given issues' events content from table
// (the live table or an AS OF source), grouped by issue and digest. When taken
// is non-nil every row's id is recorded in it.
func readPreflipEventsContent(ctx context.Context, db DBConn, table string, issues []string, taken map[string]bool) (map[string]preflipEventsContent, error) {
	idColumn := "''"
	if taken != nil {
		idColumn = "id"
	}
	//nolint:gosec // G201: table is "events" or an AS OF over a validated ref; only ? placeholders carry values.
	rows, err := db.QueryContext(ctx,
		"SELECT "+idColumn+", "+preflipEventsDigestColumns+" FROM "+table+" WHERE issue_id IN ("+preflipPlaceholders(len(issues))+")",
		preflipStringArgs(issues)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	content := make(map[string]preflipEventsContent)
	for rows.Next() {
		var id string
		fields := make([]sql.NullString, len(preflipEventsSourceColumns))
		dests := []any{&id}
		for i := range fields {
			dests = append(dests, &fields[i])
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}
		issue := fields[0].String
		if content[issue] == nil {
			content[issue] = make(preflipEventsContent)
		}
		content[issue].add(fields)
		if taken != nil {
			taken[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return content, nil
}

// planPreflipEventsInserts returns, in a deterministic order, the rows that
// bring each issue's live content up to the source's multiplicity, each under
// the lowest content-derived id not in taken.
func planPreflipEventsInserts(issues []string, source, live map[string]preflipEventsContent, taken map[string]bool) []preflipEventsRow {
	var plan []preflipEventsRow
	assigned := make(map[string]bool)
	for _, issue := range issues {
		groups := source[issue]
		digests := make([]string, 0, len(groups))
		for digest := range groups {
			digests = append(digests, digest)
		}
		sort.Strings(digests)
		for _, digest := range digests {
			g := groups[digest]
			missing := g.count
			if have, ok := live[issue][digest]; ok {
				missing -= have.count
			}
			ordinal := 0
			for ; missing > 0; missing-- {
				for {
					id := rowid.New("events", ordinal, digest)
					ordinal++
					if !taken[id] && !assigned[id] {
						assigned[id] = true
						plan = append(plan, preflipEventsRow{fields: g.fields, id: id})
						break
					}
				}
			}
		}
	}
	return plan
}

// heldPreflipEventIDs returns the planned ids some live events row already
// holds.
func heldPreflipEventIDs(ctx context.Context, db DBConn, plan []preflipEventsRow) ([]string, error) {
	var held []string
	for start := 0; start < len(plan); start += preflipEventsInsertBatch {
		batch := plan[start:min(start+preflipEventsInsertBatch, len(plan))]
		ids := make([]string, len(batch))
		for i, row := range batch {
			ids[i] = row.id
		}
		//nolint:gosec // G201: only ? placeholders are interpolated.
		found, err := preflipQueryStrings(ctx, db, "SELECT id FROM events WHERE id IN ("+preflipPlaceholders(len(ids))+")", preflipStringArgs(ids)...)
		if err != nil {
			return nil, err
		}
		held = append(held, found...)
	}
	return held, nil
}

// insertPreflipEvents writes the plan in bounded, parameterized batches with
// an explicit column list (the recreated table's column order differs from
// the pre-0062 one).
func insertPreflipEvents(ctx context.Context, db DBConn, plan []preflipEventsRow) (int, error) {
	inserted := 0
	for start := 0; start < len(plan); start += preflipEventsInsertBatch {
		batch := plan[start:min(start+preflipEventsInsertBatch, len(plan))]
		values := make([]string, len(batch))
		args := make([]any, 0, len(batch)*8)
		for i, row := range batch {
			values[i] = "(?, ?, ?, ?, ?, ?, ?, ?)"
			for _, f := range row.fields {
				args = append(args, f)
			}
			args = append(args, row.id)
		}
		//nolint:gosec // G201: only ? placeholders are interpolated.
		if _, err := db.ExecContext(ctx,
			"INSERT INTO events ("+preflipEventsInsertColumns+") VALUES "+strings.Join(values, ", "), args...); err != nil {
			return inserted, fmt.Errorf("inserting restored events: %w", err)
		}
		inserted += len(batch)
	}
	return inserted, nil
}

func preflipQueryStrings(ctx context.Context, db DBConn, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		dests := make([]any, len(columns))
		var first sql.NullString
		dests[0] = &first
		for i := 1; i < len(dests); i++ {
			dests[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}
		out = append(out, first.String)
	}
	return out, rows.Err()
}

func preflipPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func preflipStringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return args
}
