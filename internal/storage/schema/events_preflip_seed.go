package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/rowid"
)

// The pre-flip events restore (gh 7039), the Go repair behind ignored
// migration 0031. Main migration 0062's committed drop of the versioned
// events table replicates to every pulling clone, but the dolt_ignored
// recreate does not, so those clones re-create the table empty (ignored/0019)
// while the pre-flip rows stay readable only through `AS OF` history. This
// repair re-inserts, from the clone's own history, every pre-flip row whose
// content multiplicity is missing from the live table — never touching rows
// that already exist, never writing anything but ignored-plane rows.

// preflipEventsDropMessage is the commit message main migration 0062 freezes
// for the events-plane drop (Phase 2 of 0062_events_dolt_ignore.up.sql). The
// restore locates the pre-flip table by matching this message verbatim in
// dolt_log; a wording change in either place would fork the matcher from the
// history it must find, so it is pinned here against the frozen file
// (TestPreflipEventsDropMessagePinnedToFrozen0062).
const preflipEventsDropMessage = "schema: drop versioned events table for ignored-plane flip (bd-red8u)"

// preflipEventsColumns is the content-digest column list for events: the same
// frozen list as the aux row-id rekey's events entry, so a row restored here
// carries exactly the digest the rekey derivation assigns it
// (TestPreflipEventsColumnsMatchAuxRekeyDigestList).
const preflipEventsColumns = "issue_id, event_type, actor, old_value, new_value, comment, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')"

// preflipEventsRequiredColumns are the columns a historical parent's events
// table must carry for its rows to be restorable. The digest list pins the
// content; created_at and id are needed by the insert itself. A parent
// missing any of them (a re-tracked table from a post-flip flap, a squashed
// shape) is skipped rather than mis-read.
var preflipEventsRequiredColumns = []string{
	"issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at", "id",
}

const (
	// preflipSeedIssuePageSize bounds one page of live issues (and the two
	// range queries it spawns), keeping the restore's memory flat on rigs
	// with very large issue tables.
	preflipSeedIssuePageSize = 500
	// preflipSeedInsertBatch bounds one multi-row INSERT, so an interruption
	// loses at most one batch of progress and the retry converges.
	preflipSeedInsertBatch = 100
)

// preflipSeedNoticeOut receives the restore's user-visible notices. Unlike
// the terminal-gated writers, it always writes: the restore repairs silent
// data loss, and a headless clone (CI, rigs) is exactly the one whose history
// must be reported restored.
var preflipSeedNoticeOut io.Writer = os.Stderr

// PreflipSeedFaultPoint names the seams the test-only fault hook can fail, to
// prove an interrupted restore converges on retry (the marker stays pending,
// the already-inserted rows are reused, never duplicated).
type PreflipSeedFaultPoint int

const (
	// PreflipFaultHistoricalRead fires before each historical page read.
	PreflipFaultHistoricalRead PreflipSeedFaultPoint = iota
	// PreflipFaultInsertBatch fires before each batch insert.
	PreflipFaultInsertBatch
)

// preflipSeedFaultHook is nil outside tests (SetPreflipSeedFaultHookForTest).
var preflipSeedFaultHook func(point PreflipSeedFaultPoint, ref string) error

// SetPreflipSeedFaultHookForTest installs a hook that can inject a failure at
// the restore's seams; the returned func restores the nil hook.
func SetPreflipSeedFaultHookForTest(hook func(point PreflipSeedFaultPoint, ref string) error) (restore func()) {
	preflipSeedFaultHook = hook
	return func() { preflipSeedFaultHook = nil }
}

func preflipSeedFault(point PreflipSeedFaultPoint, ref string) error {
	if preflipSeedFaultHook == nil {
		return nil
	}
	return preflipSeedFaultHook(point, ref)
}

// preflipSourceSkip marks one historical source this pass must skip: its
// history is unreadable in a way that is expected on drifted clones (schema
// encoding drift, an incompatible re-tracked shape) rather than a transient
// failure. Skipping keeps the already-completed inserts and lets the cursor
// record completion — the rows stay reachable in dolt history either way.
type preflipSourceSkip struct {
	ref    string
	reason string
}

func (e *preflipSourceSkip) Error() string {
	return fmt.Sprintf("skipping pre-flip events source %s: %s", e.ref, e.reason)
}

// downgradePreflipHistoricalErr classifies an error from a historical read.
// Encoding drift (isSchemaEncodingDriftErr, gh 4380/dolt 11131) is a
// permanent property of this clone's history: it becomes a preflipSourceSkip
// so the restore completes without those rows. Everything else —
// cancellation first of all — propagates, leaving the marker pending so the
// next open retries.
func downgradePreflipHistoricalErr(ctx context.Context, ref string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	if isSchemaEncodingDriftErr(err) {
		return &preflipSourceSkip{ref: ref, reason: "schema encoding drift in this clone's history: " + firstLineOf(err.Error())}
	}
	return fmt.Errorf("%s: %w", ref, err)
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// preflipEventRow is one events row in the digest column order of
// preflipEventsColumns (created_at already rendered as text).
type preflipEventRow struct {
	fields []sql.NullString
}

func (r preflipEventRow) digest() string { return rowid.Digest(r.fields) }

// preflipSourceDigest is the source side of one digest: how many pre-flip
// rows carry it, and one sample row to re-insert.
type preflipSourceDigest struct {
	count  int
	sample preflipEventRow
}

// planPreflipSeedInserts reconciles one issue page: for each digest the
// historical source holds more copies of than the live table, it derives ids
// for the missing copies at the lowest ordinals not already taken by a live
// id. Digests are visited in sorted order so the plan is deterministic.
func planPreflipSeedInserts(source map[string]preflipSourceDigest, liveCounts map[string]int, liveIDs map[string]bool) []preflipSeedInsert {
	digests := make([]string, 0, len(source))
	for digest := range source {
		digests = append(digests, digest)
	}
	sort.Strings(digests)

	var plan []preflipSeedInsert
	for _, digest := range digests {
		missing := source[digest].count - liveCounts[digest]
		for ; missing > 0; missing-- {
			id := firstFreePreflipEventID(digest, liveIDs)
			plan = append(plan, preflipSeedInsert{row: source[digest].sample, id: id})
		}
	}
	return plan
}

type preflipSeedInsert struct {
	row preflipEventRow
	id  string
}

// firstFreePreflipEventID returns the content-derived id at the lowest
// ordinal no live row already holds, and records it as taken. Live rows with
// legacy ids never collide with derived ones, so this only has to dodge ids
// an earlier (possibly interrupted) restore pass already wrote.
func firstFreePreflipEventID(digest string, liveIDs map[string]bool) string {
	for ordinal := 0; ; ordinal++ {
		id := rowid.New("events", ordinal, digest)
		if !liveIDs[id] {
			liveIDs[id] = true
			return id
		}
	}
}

// missingPreflipEventColumns returns the required columns absent from a
// historical parent's shape, in preflipEventsRequiredColumns order.
func missingPreflipEventColumns(columns map[string]bool) []string {
	var missing []string
	for _, col := range preflipEventsRequiredColumns {
		if !columns[col] {
			missing = append(missing, col)
		}
	}
	return missing
}

// seedEventsFromPreflipHistory is the pre-migration repair for ignored/0031:
// restore this clone's pre-flip events rows from its own dolt history. See
// the marker file's header for the contract; the summary of failure modes:
//
//   - no readable source in history (fresh installs, pre-flip databases,
//     post-squash clones): complete silently, restore nothing;
//   - a source that exists but cannot be read (encoding drift, an
//     incompatible re-tracked shape): skip it, warn once, keep everything
//     already restored, complete;
//   - anything else (cancellation, transient errors, live-table failures):
//     return the error, leave the marker pending, converge on retry.
func seedEventsFromPreflipHistory(ctx context.Context, db DBConn) error {
	parent, skips, err := resolvePreflipEventsSource(ctx, db)
	if err != nil {
		return err
	}
	for _, skip := range skips {
		fmt.Fprint(preflipSeedNoticeOut, formatPreflipSkippedSourceWarning(skip))
	}
	if parent == "" {
		return nil
	}
	restored, orphans, err := seedEventsFromParentCommit(ctx, db, parent)
	if err != nil {
		return err
	}
	if summary := formatPreflipSeedSummary(restored, orphans); summary != "" {
		fmt.Fprint(preflipSeedNoticeOut, summary)
	}
	return nil
}

// resolvePreflipEventsSource walks the commits carrying 0062's frozen drop
// message (dolt_log lists newest-first) and returns the first parent whose
// events table is readable. Newest-first is the correct order twice over: a
// genuine re-flap's later table carries the earlier one's rows, so seeding
// from the newest readable source cannot lose rows, while unioning sources
// could double-count them; and an unreadable newest source must not hide the
// readable one beneath it. Every unreadable candidate is reported so the
// clone's operator learns its history is degraded; an absent drop commit is
// not a skip and stays silent (fresh installs must not warn).
func resolvePreflipEventsSource(ctx context.Context, db DBConn) (parent string, skips []*preflipSourceSkip, err error) {
	rows, err := db.QueryContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message = ?", preflipEventsDropMessage)
	if err != nil {
		return "", nil, fmt.Errorf("reading dolt_log for the events drop commit: %w", err)
	}
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			_ = rows.Close()
			return "", nil, fmt.Errorf("scanning dolt_log: %w", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", nil, fmt.Errorf("reading dolt_log: %w", err)
	}
	_ = rows.Close()

	for _, hash := range hashes {
		candidate, err := preflipFirstParent(ctx, db, hash)
		if err != nil {
			classified := downgradePreflipHistoricalErr(ctx, "dolt_commit_ancestors of "+hash, err)
			var skip *preflipSourceSkip
			if errors.As(classified, &skip) {
				skips = append(skips, skip)
				continue
			}
			return "", skips, classified
		}
		if candidate == "" {
			skips = append(skips, &preflipSourceSkip{ref: hash, reason: "commit has no first parent"})
			continue
		}

		has, err := queryHasRows(ctx, db, fmt.Sprintf("SHOW TABLES AS OF '%s' LIKE 'events'", candidate))
		if err != nil {
			classified := downgradePreflipHistoricalErr(ctx, "SHOW TABLES AS OF "+candidate, err)
			var skip *preflipSourceSkip
			if errors.As(classified, &skip) {
				skips = append(skips, skip)
				continue
			}
			return "", skips, classified
		}
		if !has {
			// A drop commit whose parent already lacked the table carries no
			// restorable rows; it is a historical curiosity, not degradation.
			continue
		}

		columns, err := preflipHistoricalEventsColumns(ctx, db, candidate)
		if err != nil {
			classified := downgradePreflipHistoricalErr(ctx, "SHOW COLUMNS AS OF "+candidate, err)
			var skip *preflipSourceSkip
			if errors.As(classified, &skip) {
				skips = append(skips, skip)
				continue
			}
			return "", skips, classified
		}
		if missing := missingPreflipEventColumns(columns); len(missing) > 0 {
			skips = append(skips, &preflipSourceSkip{
				ref:    candidate,
				reason: "incompatible events shape, missing " + strings.Join(missing, ", "),
			})
			continue
		}
		return candidate, skips, nil
	}
	return "", skips, nil
}

func preflipFirstParent(ctx context.Context, db DBConn, hash string) (string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = ? AND parent_index = 0", hash)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var parent string
	if err := rows.Scan(&parent); err != nil {
		return "", err
	}
	return parent, rows.Err()
}

func preflipHistoricalEventsColumns(ctx context.Context, db DBConn, ref string) (map[string]bool, error) {
	// The AS OF clause must trail the FROM (ignored_cursor_untrack.go's
	// spelling rule); `SHOW COLUMNS AS OF … FROM x` is a syntax error.
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SHOW COLUMNS FROM events AS OF '%s'", ref))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var field, ctype string
		var null, key, extra, def sql.NullString
		if err := rows.Scan(&field, &ctype, &null, &key, &def, &extra); err != nil {
			return nil, err
		}
		columns[field] = true
	}
	return columns, rows.Err()
}

// seedEventsFromParentCommit reconciles the live table against the events
// table as of parentRef, one page of live issues at a time. Rows whose issue
// no longer exists are counted as orphans and skipped: re-inserting them
// would fail the foreign key, and an issue deleted after the flip is not
// history this clone can still read anyway.
func seedEventsFromParentCommit(ctx context.Context, db DBConn, parentRef string) (restored, orphans int, err error) {
	after := ""
	next, err := fetchPreflipIssuePage(ctx, db, after)
	if err != nil {
		return 0, 0, fmt.Errorf("reading live issues: %w", err)
	}
	for len(next) > 0 {
		page := next
		after = page[len(page)-1]
		next, err = fetchPreflipIssuePage(ctx, db, after)
		if err != nil {
			return restored, orphans, fmt.Errorf("reading live issues: %w", err)
		}

		upper := ""
		if len(next) > 0 {
			upper = next[0]
		}
		pageRestored, pageOrphans, err := seedPreflipIssueRange(ctx, db, parentRef, page, upper)
		restored += pageRestored
		orphans += pageOrphans
		if err != nil {
			return restored, orphans, err
		}
	}
	return restored, orphans, nil
}

func fetchPreflipIssuePage(ctx context.Context, db DBConn, after string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id FROM issues WHERE id > ? ORDER BY id LIMIT ?", after, preflipSeedIssuePageSize)
	if err != nil {
		// The issues table is main-plane and exists by the time any ignored
		// migration runs; a failure here is a live read, never historical
		// drift, so it propagates rather than being downgraded.
		return nil, err
	}
	defer rows.Close()
	var page []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		page = append(page, id)
	}
	return page, rows.Err()
}

// seedPreflipIssueRange restores one page of live issues, spanning exactly
// [page[0], upper) (unbounded when upper is empty — trailing orphans must be
// counted even past the last live issue).
func seedPreflipIssueRange(ctx context.Context, db DBConn, parentRef string, page []string, upper string) (restored, orphans int, err error) {
	liveSet := make(map[string]bool, len(page))
	for _, id := range page {
		liveSet[id] = true
	}

	liveCounts, liveIDs, err := readPreflipLiveRange(ctx, db, page[0], upper)
	if err != nil {
		return 0, 0, fmt.Errorf("reading live events in [%s, %s): %w", page[0], upper, err)
	}

	if err := preflipSeedFault(PreflipFaultHistoricalRead, parentRef); err != nil {
		return 0, 0, err
	}
	source, orphans, err := readPreflipSourceRange(ctx, db, parentRef, page[0], upper, liveSet)
	if err != nil {
		classified := downgradePreflipHistoricalErr(ctx, "events AS OF "+parentRef, err)
		var skip *preflipSourceSkip
		if errors.As(classified, &skip) {
			// The chosen source turned out unreadable past resolution (drift
			// on a later page). Keep what earlier pages restored, warn, and
			// complete: the skipped rows stay reachable in dolt history.
			fmt.Fprint(preflipSeedNoticeOut, formatPreflipSkippedSourceWarning(skip))
			return 0, orphans, nil
		}
		return 0, orphans, classified
	}

	plan := planPreflipSeedInserts(source, liveCounts, liveIDs)
	for start := 0; start < len(plan); start += preflipSeedInsertBatch {
		end := start + preflipSeedInsertBatch
		if end > len(plan) {
			end = len(plan)
		}
		batch := plan[start:end]
		if err := preflipSeedFault(PreflipFaultInsertBatch, parentRef); err != nil {
			return restored, orphans, err
		}
		if err := execPreflipInsertBatch(ctx, db, batch); err != nil {
			return restored, orphans, fmt.Errorf("restoring pre-flip events rows: %w", err)
		}
		restored += len(batch)
	}
	return restored, orphans, nil
}

func readPreflipLiveRange(ctx context.Context, db DBConn, lower, upper string) (map[string]int, map[string]bool, error) {
	query := "SELECT " + preflipEventsColumns + ", id FROM events WHERE issue_id >= ?"
	args := []any{lower}
	if upper != "" {
		query += " AND issue_id < ?"
		args = append(args, upper)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	ids := make(map[string]bool)
	for rows.Next() {
		fields := make([]sql.NullString, 7)
		var id string
		scan := make([]any, 0, 8)
		for i := range fields {
			scan = append(scan, &fields[i])
		}
		scan = append(scan, &id)
		if err := rows.Scan(scan...); err != nil {
			return nil, nil, err
		}
		counts[rowid.Digest(fields)]++
		ids[id] = true
	}
	return counts, ids, rows.Err()
}

func readPreflipSourceRange(ctx context.Context, db DBConn, ref, lower, upper string, liveSet map[string]bool) (map[string]preflipSourceDigest, int, error) {
	query := "SELECT " + preflipEventsColumns + " FROM events AS OF '" + ref + "' WHERE issue_id >= ?"
	args := []any{lower}
	if upper != "" {
		query += " AND issue_id < ?"
		args = append(args, upper)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	source := make(map[string]preflipSourceDigest)
	orphans := 0
	for rows.Next() {
		fields := make([]sql.NullString, 7)
		scan := make([]any, 0, 7)
		for i := range fields {
			scan = append(scan, &fields[i])
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, 0, err
		}
		if !fields[0].Valid || !liveSet[fields[0].String] {
			orphans++
			continue
		}
		row := preflipEventRow{fields: fields}
		entry := source[row.digest()]
		entry.count++
		entry.sample = row
		source[row.digest()] = entry
	}
	return source, orphans, rows.Err()
}

func execPreflipInsertBatch(ctx context.Context, db DBConn, batch []preflipSeedInsert) error {
	var query strings.Builder
	query.WriteString("INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id) VALUES ")
	args := make([]any, 0, len(batch)*8)
	for i, insert := range batch {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString("(?,?,?,?,?,?,?,?)")
		for _, field := range insert.row.fields {
			args = append(args, field)
		}
		args = append(args, insert.id)
	}
	_, err := db.ExecContext(ctx, query.String(), args...)
	return err
}

// formatPreflipSeedSummary renders the restore's completion notice: only
// when something was restored (an empty restore is a fresh install's normal
// state, not news worth a line), mentioning skipped orphan rows when any.
func formatPreflipSeedSummary(restored, orphans int) string {
	if restored <= 0 {
		return ""
	}
	summary := fmt.Sprintf("bd: restored %d pre-flip events row(s) from this clone's dolt history", restored)
	if orphans > 0 {
		summary += fmt.Sprintf(" (skipped %d for deleted issues)", orphans)
	}
	return summary + "\n"
}

// formatPreflipSkippedSourceWarning renders one unreadable-source warning.
func formatPreflipSkippedSourceWarning(skip *preflipSourceSkip) string {
	return fmt.Sprintf("bd: warning: %s: those rows stay reachable in dolt history; see docs/getting-started/upgrading.md\n", skip)
}
