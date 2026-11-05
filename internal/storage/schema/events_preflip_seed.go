package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
// derivation (issueops.InsertDerivedEvent) hash; a restored row's content is
// read back from its source row the same way. preflipEventsCopyColumns is the
// explicit column list it is written with (the recreated table's column order
// differs from the pre-0062 one); only the id is new.
const (
	preflipEventsDigestColumns = "issue_id, event_type, actor, old_value, new_value, comment, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')"
	preflipEventsCopyColumns   = "issue_id, event_type, actor, old_value, new_value, comment, created_at"
)

// preflipEventsSourceColumns are the historical columns the restore reads
// and reconciles by; preflipEventsRequiredColumns adds the id it pages a
// source's rows by and copies them by. A source missing any of them cannot be
// reconciled with the live table.
var (
	preflipEventsSourceColumns   = []string{"issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at"}
	preflipEventsRequiredColumns = append(slices.Clone(preflipEventsSourceColumns), "id")
)

// preflipEventsPageSize bounds how many issues one page of a source holds and
// how many rows one read returns. preflipEventsInsertBatch bounds how many
// candidate ids one probe checks and how many rows one INSERT carries, and is
// how many planned rows accumulate, across issues, before they are written:
// the statement count grows with the number of issues and batches, never with
// the number of rows. Writes are few and large on purpose: every autocommitted
// write rewrites each index leaf it touches, and content-derived ids land all
// over the key space. preflipEventsInsertBytes bounds the event content one
// INSERT carries (update events hold the whole issue as JSON), as
// issueInsertBytesPerStatement does for issues. Memory is bounded by one
// issue's row ids and digests, the planned rows' ids, and one INSERT's
// content: a planned row's content is read back from history, a page at a
// time, only when it is written.
var (
	preflipEventsPageSize    = 200
	preflipEventsInsertBatch = 1000
	preflipEventsInsertBytes = 16 << 20
)

// SetPreflipEventsBatchSizesForTest sets the restore's page and insert batch
// sizes and returns a function that restores the previous ones. Test-only.
func SetPreflipEventsBatchSizesForTest(page, insert int) func() {
	prevPage, prevInsert := preflipEventsPageSize, preflipEventsInsertBatch
	preflipEventsPageSize, preflipEventsInsertBatch = page, insert
	return func() { preflipEventsPageSize, preflipEventsInsertBatch = prevPage, prevInsert }
}

// SetPreflipEventsInsertBytesForTest sets the event content one restore INSERT
// may carry and returns a function that restores the previous budget.
// Test-only.
func SetPreflipEventsInsertBytesForTest(n int) func() {
	prev := preflipEventsInsertBytes
	preflipEventsInsertBytes = n
	return func() { preflipEventsInsertBytes = prev }
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

// RestorePreflipEventsForTest runs the pre-upgrade events restore on its own,
// outside a migration pass, so a test can observe exactly the statements it
// issues. Test-only.
func RestorePreflipEventsForTest(ctx context.Context, db DBConn) error {
	return seedEventsFromPreflipHistory(ctx, db)
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
// for every live issue the restore copies max(0, source - live) rows of each
// content digest, under the lowest content-derived ids no row holds yet.
// Existing rows and their ids are never touched, a retry adds nothing twice,
// and across several sources the result is each digest's maximum
// multiplicity, whatever the order. Rows whose issue id no longer exists are
// skipped (fk_events_issue would refuse them) and counted: the issue was
// deleted, or renamed after the flip, and history holds only its old id.
//
// It writes only the ignored events table: no journal entry, no staging, no
// Dolt commit. A clone with no such commit, whose source has no events table,
// or whose source is beyond a shallow clone's depth, completes silently.
//
// Reading history must never cost the clone its ability to open: bd reads
// nothing else this old, so damage there (a missing chunk, Dolt's
// schema-encoding drift, a shape the restore cannot reconcile) would otherwise
// fail every open for the sake of a best-effort restore. Any such failure
// skips the history it hit with one warning, the other sources are still
// restored, and the marker records. Only a canceled pass, a failure that looks
// like the connection rather than the history (see preflipTransientErr), and
// every failure of a live statement fail the pass with 0031 pending, for the
// next open to retry.
func seedEventsFromPreflipHistory(ctx context.Context, db DBConn) error {
	sources, unreadable, err := preflipEventsSources(ctx, db)
	if err != nil {
		return err
	}
	restored, orphans := 0, 0
	announced := false
	announce := func() {
		if !announced {
			announced = true
			fmt.Fprintln(stderr, "Checking this clone's history for pre-upgrade audit events to restore…")
		}
	}
	for _, ref := range sources {
		n, err := restorePreflipEventsFrom(ctx, db, ref, &orphans, announce)
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
// reachable commits carrying eventsFlipDropCommitMessage, and a description of
// every part of the commit graph it could not read (see
// unreadablePreflipHistory). A parent that is not itself present (a shallow
// clone's depth ends at the drop commit) is dropped: there is nothing local to
// restore from.
func preflipEventsSources(ctx context.Context, db DBConn) ([]string, []string, error) {
	drops, err := preflipQueryStrings(ctx, db, "SELECT commit_hash FROM dolt_log WHERE message = ?", eventsFlipDropCommitMessage)
	if err != nil {
		if preflipGhostCommitErr(err) {
			return nil, nil, nil
		}
		if !unreadablePreflipHistory(ctx, err) {
			return nil, nil, fmt.Errorf("reading the events flip commits: %w", err)
		}
		return nil, []string{fmt.Sprintf("this clone's commit history (%v)", err)}, nil
	}
	seen := make(map[string]bool)
	var sources, unreadable []string
	for _, drop := range drops {
		parents, err := preflipQueryStrings(ctx, db,
			"SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = ? AND parent_index = 0", drop)
		if err != nil {
			if preflipGhostCommitErr(err) {
				continue
			}
			if !unreadablePreflipHistory(ctx, err) {
				return nil, nil, fmt.Errorf("reading the parent of %s: %w", drop, err)
			}
			unreadable = append(unreadable, fmt.Sprintf("the parent of %s (%v)", drop, err))
			continue
		}
		for _, parent := range parents {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			reachable, err := preflipQueryStrings(ctx, db, "SELECT commit_hash FROM dolt_log WHERE commit_hash = ?", parent)
			if err != nil {
				if preflipGhostCommitErr(err) {
					continue
				}
				if !unreadablePreflipHistory(ctx, err) {
					return nil, nil, fmt.Errorf("reading %s: %w", parent, err)
				}
				unreadable = append(unreadable, fmt.Sprintf("%s (%v)", parent, err))
				continue
			}
			if len(reachable) == 0 {
				continue
			}
			if err := validateMigrationRef(parent); err != nil {
				unreadable = append(unreadable, fmt.Sprintf("the parent of %s (%v)", drop, err))
				continue
			}
			sources = append(sources, parent)
		}
	}
	sort.Strings(sources)
	return sources, unreadable, nil
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
// cannot be restored from: its history failed to read (see
// unreadablePreflipHistory). Live-statement failures are not.
func unreadablePreflipSource(ctx context.Context, err error) bool {
	var src *preflipSourceError
	if !errors.As(err, &src) {
		return false
	}
	return unreadablePreflipHistory(ctx, src.err)
}

// unreadablePreflipHistory reports whether err, returned by a read of this
// clone's history, may skip what it was reading rather than fail the pass.
// Every such error may, except two kinds that say nothing about the history:
// a canceled ctx, which wins over the error's shape (a read cut short can
// surface as anything, drift included), and a transient failure of the
// connection or the process (preflipTransientErr). Those leave 0031 pending
// for the next open; anything else would fail that open too, and every open
// after it.
func unreadablePreflipHistory(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	return !preflipTransientErr(err)
}

// preflipTransientErrPhrases are the connection-failure wordings
// internal/storage/dolt retries on (isRetryableError, isConnectionError), and
// a cancellation reported from inside the server.
var preflipTransientErrPhrases = []string{
	"bad connection",
	"invalid connection",
	"connection refused",
	"connection reset",
	"broken pipe",
	"i/o timeout",
	"lost connection",
	"gone away",
	"context canceled",
	"context deadline exceeded",
}

// preflipTransientErr reports whether err looks like the connection, the
// server or the caller failing rather than the history being unreadable.
func preflipTransientErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, phrase := range preflipTransientErrPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// preflipGhostCommitErr reports Dolt's refusal to read a commit a shallow
// clone does not hold (doltdb.ErrGhostCommitEncountered).
func preflipGhostCommitErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "shallow clone")
}

// restorePreflipEventsFrom reconciles one source commit into the live events
// table, a page of source issues at a time, and returns how many rows it
// inserted. Skipped rows are counted in orphans. announce is called once the
// source turns out to hold events.
func restorePreflipEventsFrom(ctx context.Context, db DBConn, ref string, orphans *int, announce func()) (int, error) {
	present, err := preflipEventsSourceUsable(ctx, db, ref)
	if err != nil || !present {
		return 0, err
	}
	w := &preflipEventsWriter{ctx: ctx, db: db, ref: ref}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return w.inserted, err
		}
		issues, err := preflipEventsIssuePage(ctx, db, ref, after)
		if err != nil {
			return w.abandon(&preflipSourceError{err})
		}
		if len(issues) == 0 {
			return w.inserted, w.flush()
		}
		announce()
		after = issues[len(issues)-1]
		if err := restorePreflipEventsPage(ctx, db, w, ref, issues, orphans); err != nil {
			return w.abandon(err)
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

// preflipEventsContent is one issue's events content: each digest's row
// count and, for a source, the ids of the rows carrying it, in id order.
type preflipEventsContent map[string]*preflipEventsGroup

type preflipEventsGroup struct {
	count int
	ids   []string
}

// add records one row of the given digest, and its id when keepID is set.
func (c preflipEventsContent) add(digest, id string, keepID bool) {
	g, ok := c[digest]
	if !ok {
		g = &preflipEventsGroup{}
		c[digest] = g
	}
	g.count++
	if keepID {
		g.ids = append(g.ids, id)
	}
}

// preflipEventsRow is one planned copy: the source row's id and the id the
// copy takes.
type preflipEventsRow struct {
	src string
	id  string
}

// preflipEventsValues is one copy ready to write: its content, read back from
// history, and its new id.
type preflipEventsValues struct {
	fields []sql.NullString
	id     string
}

// restorePreflipEventsPage reconciles one page of source issues with the live
// table and plans the copies each issue is missing. Each issue's source
// content is read whole, and every result set closed, before anything is
// written: the restore runs on the migration's pinned connection, which
// cannot interleave an open result with a write. Only an issue that already
// holds live events has its live content read; on an adopting clone that is
// none of them. A source read that fails part-way through an issue plans
// nothing for that issue.
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
	held := make(map[string]bool)
	if len(existing) > 0 {
		//nolint:gosec // G202: only ? placeholders are concatenated.
		withEvents, err := preflipQueryStrings(ctx, db,
			"SELECT DISTINCT issue_id FROM events WHERE issue_id IN ("+preflipPlaceholders(len(existing))+")", preflipStringArgs(existing)...)
		if err != nil {
			return fmt.Errorf("reading live events: %w", err)
		}
		for _, id := range withEvents {
			held[id] = true
		}
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
		current := preflipEventsContent{}
		if held[issue] {
			if current, err = readPreflipEventsContent(ctx, db, "", issue); err != nil {
				return fmt.Errorf("reading live events: %w", err)
			}
		}
		if err := w.plan(planPreflipEventsNeeds(source, current)); err != nil {
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
// empty. A source keeps every row's id, so its rows can be copied by id; the
// live table only needs counting. Rows are read in id-ordered pages so no
// result set stays open long.
func readPreflipEventsContent(ctx context.Context, db DBConn, ref, issue string) (preflipEventsContent, error) {
	table := "events"
	if ref != "" {
		table = "events AS OF '" + ref + "'"
	}
	content := make(preflipEventsContent)
	after := ""
	for {
		n, last, err := readPreflipEventsRowPage(ctx, db, table, issue, after, ref != "", content)
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
func readPreflipEventsRowPage(ctx context.Context, db DBConn, table, issue, after string, keepIDs bool, content preflipEventsContent) (int, string, error) {
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
		content.add(rowid.Digest(fields), last, keepIDs)
		n++
	}
	return n, last, rows.Err()
}

// preflipEventsNeed is one content the live table holds fewer copies of than
// the source: the source rows to copy, one per missing copy, and the ids
// allocatePreflipEvents assigns them.
type preflipEventsNeed struct {
	digest string
	srcIDs []string
	dstIDs []string
}

// planPreflipEventsNeeds returns, in digest order, the copies of each of an
// issue's source contents the live table is missing: max(0, source - live),
// whatever ids the live copies hold. Each need copies the lowest-id source
// rows of its content, so a plan is the same on every run.
func planPreflipEventsNeeds(source, live preflipEventsContent) []*preflipEventsNeed {
	digests := make([]string, 0, len(source))
	for digest := range source {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	var needs []*preflipEventsNeed
	for _, digest := range digests {
		g := source[digest]
		missing := g.count
		if have, ok := live[digest]; ok {
			missing -= have.count
		}
		missing = min(missing, len(g.ids))
		if missing > 0 {
			needs = append(needs, &preflipEventsNeed{digest: digest, srcIDs: g.ids[:missing]})
		}
	}
	return needs
}

// allocatePreflipEvents assigns every need's source rows the lowest
// content-derived ids no row holds, filling each need's dstIDs. All needs'
// candidates are checked together, at most preflipEventsInsertBatch ids per
// probe, and a need whose candidates were taken proposes its next ordinals in
// another round. A derived id is a function of content, so usually only a
// same-content row of the same issue holds it -- unless the row was re-pointed
// since (fk_events_issue cascades an issue rename onto events.issue_id but not
// onto the id) -- so probing the table, not the issue's rows, is what keeps a
// plain INSERT from colliding. Needs never share a digest (a digest names its
// issue), so no two needs propose the same id.
func allocatePreflipEvents(needs []*preflipEventsNeed, probe func([]string) (map[string]bool, error)) error {
	type proposal struct {
		need *preflipEventsNeed
		ids  []string
	}
	next := make(map[*preflipEventsNeed]int, len(needs))
	open := make([]*preflipEventsNeed, 0, len(needs))
	for _, need := range needs {
		if len(need.dstIDs) < len(need.srcIDs) {
			open = append(open, need)
		}
	}
	for len(open) > 0 {
		var proposals []proposal
		var candidates []string
		for _, need := range open {
			ids := make([]string, len(need.srcIDs)-len(need.dstIDs))
			for i := range ids {
				ids[i] = rowid.New("events", next[need]+i, need.digest)
			}
			next[need] += len(ids)
			proposals = append(proposals, proposal{need: need, ids: ids})
			candidates = append(candidates, ids...)
		}
		held := make(map[string]bool)
		for start := 0; start < len(candidates); start += preflipEventsInsertBatch {
			found, err := probe(candidates[start:min(start+preflipEventsInsertBatch, len(candidates))])
			if err != nil {
				return fmt.Errorf("reading live event ids: %w", err)
			}
			for id := range found {
				held[id] = true
			}
		}
		open = open[:0]
		for _, p := range proposals {
			for _, id := range p.ids {
				if !held[id] {
					p.need.dstIDs = append(p.need.dstIDs, id)
				}
			}
			if len(p.need.dstIDs) < len(p.need.srcIDs) {
				open = append(open, p.need)
			}
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

// preflipEventsWriter collects one source's planned copies across issues and
// writes them in bounded batches.
type preflipEventsWriter struct {
	ctx      context.Context
	db       DBConn
	ref      string
	pending  []*preflipEventsNeed
	queued   int
	inserted int
}

// plan queues needs, and writes everything queued once a batch is full.
func (w *preflipEventsWriter) plan(needs []*preflipEventsNeed) error {
	for _, need := range needs {
		w.pending = append(w.pending, need)
		w.queued += len(need.srcIDs)
	}
	if w.queued >= preflipEventsInsertBatch {
		return w.flush()
	}
	return nil
}

// flush allocates ids for everything queued and copies it into the live table.
func (w *preflipEventsWriter) flush() error {
	if w.queued == 0 {
		return nil
	}
	pending := w.pending
	w.pending, w.queued = nil, 0
	probe := func(ids []string) (map[string]bool, error) { return heldPreflipEventIDs(w.ctx, w.db, ids) }
	if err := allocatePreflipEvents(pending, probe); err != nil {
		return err
	}
	var rows []preflipEventsRow
	for _, need := range pending {
		for i, src := range need.srcIDs {
			rows = append(rows, preflipEventsRow{src: src, id: need.dstIDs[i]})
		}
	}
	for start := 0; start < len(rows); start += preflipEventsInsertBatch {
		if err := w.copyRows(rows[start:min(start+preflipEventsInsertBatch, len(rows))]); err != nil {
			return err
		}
	}
	return nil
}

// abandon ends the source on err. A source whose history became unreadable
// part-way keeps what it restored: the copies planned for the issues read
// before the failure are written first. Any other failure fails the pass, and
// the next pass reconciles from scratch, so nothing more is written.
func (w *preflipEventsWriter) abandon(err error) (int, error) {
	if unreadablePreflipSource(w.ctx, err) {
		if flushErr := w.flush(); flushErr != nil {
			return w.inserted, flushErr
		}
	}
	return w.inserted, err
}

// copyRows reads the planned rows' content back from the source commit by
// primary key, a page at a time, and writes it under the new ids in as few
// INSERTs as preflipEventsInsertBytes allows. Each read is closed before
// anything is written (see restorePreflipEventsPage).
func (w *preflipEventsWriter) copyRows(rows []preflipEventsRow) error {
	var values []preflipEventsValues
	size := 0
	for start := 0; start < len(rows); start += preflipEventsPageSize {
		page, err := w.readSourceRows(rows[start:min(start+preflipEventsPageSize, len(rows))])
		if err != nil {
			return &preflipSourceError{err}
		}
		for _, v := range page {
			rowSize := 64
			for _, f := range v.fields {
				rowSize += len(f.String)
			}
			if len(values) > 0 && size+rowSize > preflipEventsInsertBytes {
				if err := w.insert(values); err != nil {
					return err
				}
				values, size = values[:0], 0
			}
			values = append(values, v)
			size += rowSize
		}
	}
	if len(values) > 0 {
		return w.insert(values)
	}
	return nil
}

// readSourceRows returns the content of the planned rows' source rows, in
// plan order, each paired with its new id. created_at is read as the digest
// renders it (DATETIME(0) text), as issueops binds it on insert.
func (w *preflipEventsWriter) readSourceRows(planned []preflipEventsRow) ([]preflipEventsValues, error) {
	ids := make([]string, len(planned))
	for i, row := range planned {
		ids[i] = row.src
	}
	//nolint:gosec // G201: ref is validated by preflipEventsSources; only ? placeholders otherwise.
	rows, err := w.db.QueryContext(w.ctx,
		"SELECT id, "+preflipEventsDigestColumns+" FROM events AS OF '"+w.ref+"' WHERE id IN ("+preflipPlaceholders(len(ids))+")",
		preflipStringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string][]sql.NullString, len(ids))
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
		byID[id] = fields
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]preflipEventsValues, 0, len(planned))
	for _, row := range planned {
		if fields, ok := byID[row.src]; ok {
			values = append(values, preflipEventsValues{fields: fields, id: row.id})
		}
	}
	return values, nil
}

// insert writes values with one parameterized INSERT and an explicit column
// list.
func (w *preflipEventsWriter) insert(values []preflipEventsValues) error {
	placeholders := make([]string, len(values))
	args := make([]any, 0, len(values)*(len(preflipEventsSourceColumns)+1))
	for i, v := range values {
		placeholders[i] = "(?, ?, ?, ?, ?, ?, ?, ?)"
		for _, f := range v.fields {
			args = append(args, f)
		}
		args = append(args, v.id)
	}
	//nolint:gosec // G201: only ? placeholders are interpolated.
	if _, err := w.db.ExecContext(w.ctx,
		"INSERT INTO events ("+preflipEventsCopyColumns+", id) VALUES "+strings.Join(placeholders, ", "), args...); err != nil {
		return fmt.Errorf("inserting restored events: %w", err)
	}
	w.inserted += len(values)
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
