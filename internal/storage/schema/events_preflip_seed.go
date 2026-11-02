package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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

// preflipEventsSourceColumns are the historical columns the restore reads
// and reconciles by; preflipEventsRequiredColumns adds the id it pages a
// source's rows by. A source missing any of them cannot be reconciled with the
// live table.
var (
	preflipEventsSourceColumns   = []string{"issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at"}
	preflipEventsRequiredColumns = append(slices.Clone(preflipEventsSourceColumns), "id")
)

// preflipEventsPageSize bounds how many issues, and how many of one issue's
// rows, the restore reads at once; preflipEventsInsertBatch bounds how many
// rows one INSERT carries and how many planned ids one probe checks. Memory
// is bounded by one issue's distinct content, not by its row count.
var (
	preflipEventsPageSize    = 200
	preflipEventsInsertBatch = 200
)

// SetPreflipEventsBatchSizesForTest sets the restore's page and insert batch
// sizes and returns a function that restores the previous ones. Test-only.
func SetPreflipEventsBatchSizesForTest(page, insert int) func() {
	prevPage, prevInsert := preflipEventsPageSize, preflipEventsInsertBatch
	preflipEventsPageSize, preflipEventsInsertBatch = page, insert
	return func() { preflipEventsPageSize, preflipEventsInsertBatch = prevPage, prevInsert }
}

// preflipEventsInsertFaultHook, when set by a test, runs after each restored
// batch is inserted; an error aborts the restore there, emulating a live
// failure after partial insertion. Production leaves it nil.
var preflipEventsInsertFaultHook func() error

// preflipEventsSourceFaultHook, when set by a test, runs as each page of one
// issue's source rows finishes iterating; an error stands in for that
// historical read failing there. Production leaves it nil.
var preflipEventsSourceFaultHook func(ref, issue string) error

// SetPreflipEventsSourceFaultHookForTest installs fn as the restore's
// source-read fault hook and returns a function that restores the previous
// value. Test-only.
func SetPreflipEventsSourceFaultHookForTest(fn func(ref, issue string) error) func() {
	prev := preflipEventsSourceFaultHook
	preflipEventsSourceFaultHook = fn
	return func() { preflipEventsSourceFaultHook = prev }
}

// SetPreflipEventsInsertFaultHookForTest installs fn as the restore's
// per-batch fault hook and returns a function that restores the previous
// value. Test-only.
func SetPreflipEventsInsertFaultHookForTest(fn func() error) func() {
	prev := preflipEventsInsertFaultHook
	preflipEventsInsertFaultHook = fn
	return func() { preflipEventsInsertFaultHook = prev }
}

// preflipEventsNoticeOut is where the restore reports what it did. Like
// lostTableNoticeOut it is deliberately NOT terminal-gated: the counts are
// the only sign on a piped or served open that history came back.
var preflipEventsNoticeOut io.Writer = os.Stderr

// SetPreflipEventsNoticeOutForTest redirects the pre-upgrade events restore's
// notices to w and returns a function that restores the previous writer.
// Test-only.
func SetPreflipEventsNoticeOutForTest(w io.Writer) func() {
	prev := preflipEventsNoticeOut
	preflipEventsNoticeOut = w
	return func() { preflipEventsNoticeOut = prev }
}

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
// multiplicity, whatever the order. Rows whose issue id no longer exists are
// skipped (fk_events_issue would refuse them) and counted: the issue was
// deleted, or renamed after the flip, and history holds only its old id.
//
// It writes only the ignored events table: no journal entry, no staging, no
// Dolt commit. A clone with no such commit, or whose source has no events
// table, completes silently. A source whose history cannot be read (Dolt's
// schema-encoding drift) or whose events table lacks a column the restore
// reads is skipped with a warning and the others are still restored; any
// other failure, and every failure of a live statement, fails the pass with
// 0031 pending.
func seedEventsFromPreflipHistory(ctx context.Context, db DBConn) error {
	sources, err := preflipEventsSources(ctx, db)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return nil
	}
	restored, orphans := 0, 0
	var unreadable []string
	for _, ref := range sources {
		n, err := restorePreflipEventsFrom(ctx, db, ref, &orphans)
		restored += n
		if err != nil {
			if !unreadablePreflipSource(ctx, err) {
				return fmt.Errorf("restoring pre-upgrade events from %s: %w", ref, err)
			}
			unreadable = append(unreadable, fmt.Sprintf("%s (%v)", ref, err))
		}
	}
	if len(unreadable) > 0 {
		fmt.Fprintf(preflipEventsNoticeOut,
			"Warning: could not restore pre-upgrade audit events from %s; this clone's audit history is incomplete. "+
				"Recover the rows from a clone or backup that can read that history, or repair the storage; "+
				"see https://beads.gascity.com/getting-started/upgrading#pre-upgrade-audit-events-on-adopting-clones\n",
			strings.Join(unreadable, ", "))
	}
	if restored > 0 {
		fmt.Fprintf(preflipEventsNoticeOut,
			"Restored %d pre-upgrade audit event(s) from this clone's history (gastownhall/beads#7039).\n", restored)
	}
	if orphans > 0 {
		fmt.Fprintf(preflipEventsNoticeOut,
			"Skipped %d pre-upgrade audit event(s) whose issue id no longer exists (deleted or renamed).\n", orphans)
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

// preflipSourceError marks a failure reading a source commit's history, as
// opposed to a statement against the live database.
type preflipSourceError struct{ err error }

func (e *preflipSourceError) Error() string { return e.err.Error() }
func (e *preflipSourceError) Unwrap() error { return e.err }

// errPreflipSourceIncompatible is a source whose events table lacks a column
// the restore reads.
var errPreflipSourceIncompatible = errors.New("its events table lacks columns the restore reads")

// unreadablePreflipSource reports whether err means only that one source
// cannot be restored from: drift or an incompatible shape in its history.
// Cancellation, transient and live-statement failures are not. A canceled
// ctx wins over the error's shape: a read cut short can surface as anything,
// drift included, and must leave 0031 pending rather than skip the source.
func unreadablePreflipSource(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var src *preflipSourceError
	if !errors.As(err, &src) {
		return false
	}
	return errors.Is(src.err, errPreflipSourceIncompatible) || isSchemaEncodingDriftErr(src.err)
}

// restorePreflipEventsFrom reconciles one source commit into the live events
// table, one source issue at a time, and returns how many rows it inserted.
// Skipped rows are counted in orphans.
func restorePreflipEventsFrom(ctx context.Context, db DBConn, ref string, orphans *int) (int, error) {
	present, err := preflipEventsSourceUsable(ctx, db, ref)
	if err != nil || !present {
		return 0, err
	}
	w := &preflipEventsWriter{ctx: ctx, db: db}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return w.inserted, err
		}
		issues, err := preflipEventsIssuePage(ctx, db, ref, after)
		if err != nil {
			return w.inserted, &preflipSourceError{err}
		}
		if len(issues) == 0 {
			return w.inserted, nil
		}
		after = issues[len(issues)-1]
		if err := restorePreflipEventsPage(ctx, db, w, ref, issues, orphans); err != nil {
			return w.inserted, err
		}
	}
}

// preflipEventsSourceUsable reports whether ref carries an events table, and
// fails with errPreflipSourceIncompatible when it lacks a column the restore
// reads. The shape is probed before anything selects
// from it, as ReadMigrationContentHashes does: a failing statement would pin
// a pooled Dolt session to its pre-statement catalog (be-bv7x).
func preflipEventsSourceUsable(ctx context.Context, db DBConn, ref string) (bool, error) {
	//nolint:gosec // G201: ref is validated by preflipEventsSources; AS OF requires a literal.
	hasTable, err := queryHasRows(ctx, db, "SHOW TABLES AS OF '"+ref+"' LIKE 'events'")
	if err != nil {
		return false, &preflipSourceError{err}
	}
	if !hasTable {
		return false, nil
	}
	//nolint:gosec // G201: as above.
	columns, err := preflipQueryStrings(ctx, db, "SHOW COLUMNS FROM events AS OF '"+ref+"'")
	if err != nil {
		return false, &preflipSourceError{err}
	}
	have := make(map[string]bool, len(columns))
	for _, c := range columns {
		have[strings.ToLower(c)] = true
	}
	for _, c := range preflipEventsRequiredColumns {
		if !have[c] {
			return false, &preflipSourceError{fmt.Errorf("%w: no %s", errPreflipSourceIncompatible, c)}
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
// table, issue by issue. Each issue's source content is read whole, and every
// result set closed, before anything is written for it: the restore runs on
// the migration's pinned connection, which cannot interleave an open result
// with a write. A source read that fails part-way through an issue therefore
// writes nothing for that issue.
func restorePreflipEventsPage(ctx context.Context, db DBConn, w *preflipEventsWriter, ref string, issues []string, orphans *int) error {
	//nolint:gosec // G202: only ? placeholders are concatenated.
	existing, err := preflipQueryStrings(ctx, db,
		"SELECT id FROM issues WHERE id IN ("+preflipPlaceholders(len(issues))+")", preflipStringArgs(issues)...)
	if err != nil {
		return fmt.Errorf("reading live issues: %w", err)
	}
	live := make(map[string]bool, len(existing))
	for _, id := range existing {
		live[id] = true
	}
	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !live[issue] {
			if err := countPreflipOrphans(ctx, db, ref, issue, orphans); err != nil {
				return err
			}
			continue
		}
		source, err := readPreflipEventsContent(ctx, db, ref, issue)
		if err != nil {
			return &preflipSourceError{err}
		}
		current, err := readPreflipEventsContent(ctx, db, "", issue)
		if err != nil {
			return fmt.Errorf("reading live events: %w", err)
		}
		for _, need := range planPreflipEventsNeeds(source, current) {
			probe := func(ids []string) (map[string]bool, error) { return heldPreflipEventIDs(ctx, db, ids) }
			if err := allocatePreflipEvents(need, probe, w.add); err != nil {
				return err
			}
		}
		if err := w.flush(); err != nil {
			return err
		}
	}
	return nil
}

// countPreflipOrphans adds to orphans the source rows of an issue that no
// longer exists: fk_events_issue would refuse them.
func countPreflipOrphans(ctx context.Context, db DBConn, ref, issue string, orphans *int) error {
	//nolint:gosec // G201: ref is validated by preflipEventsSources.
	counts, err := preflipQueryStrings(ctx, db, "SELECT COUNT(*) FROM events AS OF '"+ref+"' WHERE issue_id = ?", issue)
	if err != nil {
		return &preflipSourceError{err}
	}
	for _, c := range counts {
		n, err := strconv.Atoi(c)
		if err != nil {
			return &preflipSourceError{fmt.Errorf("counting events of deleted issue %s: %w", issue, err)}
		}
		*orphans += n
	}
	return nil
}

// readPreflipEventsContent reads one issue's events content, grouped by
// digest, from the source commit ref, or from the live table when ref is
// empty. Rows are read in id-ordered pages so no result set stays open long
// and memory holds only the issue's distinct content.
func readPreflipEventsContent(ctx context.Context, db DBConn, ref, issue string) (preflipEventsContent, error) {
	table := "events"
	if ref != "" {
		table = "events AS OF '" + ref + "'"
	}
	content := make(preflipEventsContent)
	after := ""
	for {
		n, last, err := readPreflipEventsRowPage(ctx, db, table, issue, after, content)
		if err == nil && ref != "" && preflipEventsSourceFaultHook != nil {
			err = preflipEventsSourceFaultHook(ref, issue)
		}
		if err != nil {
			return nil, err
		}
		if n < preflipEventsPageSize {
			return content, nil
		}
		after = last
	}
}

// readPreflipEventsRowPage adds to content the next page of the issue's rows
// after the given id, and returns how many it read and the last id.
func readPreflipEventsRowPage(ctx context.Context, db DBConn, table, issue, after string, content preflipEventsContent) (int, string, error) {
	//nolint:gosec // G201: table is "events" or an AS OF over a validated ref; the limit is an int.
	rows, err := db.QueryContext(ctx,
		"SELECT id, "+preflipEventsDigestColumns+" FROM "+table+" WHERE issue_id = ? AND id > ? ORDER BY id LIMIT "+strconv.Itoa(preflipEventsPageSize),
		issue, after)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	n, last := 0, after
	for rows.Next() {
		fields := make([]sql.NullString, len(preflipEventsSourceColumns))
		dests := []any{&last}
		for i := range fields {
			dests = append(dests, &fields[i])
		}
		if err := rows.Scan(dests...); err != nil {
			return 0, "", err
		}
		content.add(fields)
		n++
	}
	return n, last, rows.Err()
}

// preflipEventsNeed is one content the live table holds fewer copies of than
// the source.
type preflipEventsNeed struct {
	digest  string
	fields  []sql.NullString
	missing int
}

// planPreflipEventsNeeds returns, in digest order, how many copies of each of
// an issue's source contents the live table is missing: max(0, source - live),
// whatever ids the live copies hold.
func planPreflipEventsNeeds(source, live preflipEventsContent) []preflipEventsNeed {
	digests := make([]string, 0, len(source))
	for digest := range source {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	var needs []preflipEventsNeed
	for _, digest := range digests {
		g := source[digest]
		missing := g.count
		if have, ok := live[digest]; ok {
			missing -= have.count
		}
		if missing > 0 {
			needs = append(needs, preflipEventsNeed{digest: digest, fields: g.fields, missing: missing})
		}
	}
	return needs
}

// allocatePreflipEvents emits need.missing rows of the need's content under
// the lowest content-derived ids no row holds. Candidate ids are checked with
// probe at most preflipEventsInsertBatch at a time. A derived id is a function
// of content, so usually only a same-content row of the same issue holds it --
// unless the row was re-pointed since (fk_events_issue cascades an issue
// rename onto events.issue_id but not onto the id) -- so probing the table,
// not the issue's rows, is what keeps a plain INSERT from colliding.
func allocatePreflipEvents(need preflipEventsNeed, probe func([]string) (map[string]bool, error), emit func(preflipEventsRow) error) error {
	ordinal := 0
	for need.missing > 0 {
		ids := make([]string, min(need.missing, preflipEventsInsertBatch))
		for i := range ids {
			ids[i] = rowid.New("events", ordinal, need.digest)
			ordinal++
		}
		held, err := probe(ids)
		if err != nil {
			return fmt.Errorf("reading live event ids: %w", err)
		}
		for _, id := range ids {
			if held[id] {
				continue
			}
			if err := emit(preflipEventsRow{fields: need.fields, id: id}); err != nil {
				return err
			}
			need.missing--
		}
	}
	return nil
}

// heldPreflipEventIDs returns which of ids some live events row holds.
func heldPreflipEventIDs(ctx context.Context, db DBConn, ids []string) (map[string]bool, error) {
	//nolint:gosec // G201: only ? placeholders are interpolated.
	found, err := preflipQueryStrings(ctx, db, "SELECT id FROM events WHERE id IN ("+preflipPlaceholders(len(ids))+")", preflipStringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(found))
	for _, id := range found {
		held[id] = true
	}
	return held, nil
}

// preflipEventsWriter streams restored rows into the live table in bounded,
// parameterized batches with an explicit column list (the recreated table's
// column order differs from the pre-0062 one).
type preflipEventsWriter struct {
	ctx      context.Context
	db       DBConn
	batch    []preflipEventsRow
	inserted int
}

func (w *preflipEventsWriter) add(row preflipEventsRow) error {
	w.batch = append(w.batch, row)
	if len(w.batch) >= preflipEventsInsertBatch {
		return w.flush()
	}
	return nil
}

func (w *preflipEventsWriter) flush() error {
	if len(w.batch) == 0 {
		return nil
	}
	values := make([]string, len(w.batch))
	args := make([]any, 0, len(w.batch)*8)
	for i, row := range w.batch {
		values[i] = "(?, ?, ?, ?, ?, ?, ?, ?)"
		for _, f := range row.fields {
			args = append(args, f)
		}
		args = append(args, row.id)
	}
	//nolint:gosec // G201: only ? placeholders are interpolated.
	if _, err := w.db.ExecContext(w.ctx,
		"INSERT INTO events ("+preflipEventsInsertColumns+") VALUES "+strings.Join(values, ", "), args...); err != nil {
		return fmt.Errorf("inserting restored events: %w", err)
	}
	w.inserted += len(w.batch)
	w.batch = w.batch[:0]
	if preflipEventsInsertFaultHook != nil {
		return preflipEventsInsertFaultHook()
	}
	return nil
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
