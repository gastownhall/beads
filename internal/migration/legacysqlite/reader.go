//go:build cgo

// Package legacysqlite reads the small, authenticated SQLite history that
// predates the current Dolt store. It intentionally has no general migration
// registry: each accepted layout is an exact, audited contract.
package legacysqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

var acceptedVersions = map[string]bool{"0.49.6": true, "0.50.3": true}

// Current bounded-string widths come from the canonical Dolt schema in
// internal/storage/schema/migrations/0001_create_issues.up.sql. VARCHAR(255)
// fields use types.MaxFieldLen. Payload and waiters remain TEXT after migration
// 0049 widened the other large content columns to LONGTEXT.
const (
	currentShortVarcharRunes  = 32
	currentCommitVarcharRunes = 64
	currentTitleVarcharRunes  = 500
	currentSpecIDVarcharRunes = 1024
	currentTextBytes          = 65_535
)

var schema = map[string]string{
	"metadata":     `key|TEXT|0|-|1 value|TEXT|1|-|0`,
	"issues":       `id|TEXT|0|-|1 content_hash|TEXT|0|-|0 title|TEXT|1|-|0 description|TEXT|1|''|0 design|TEXT|1|''|0 acceptance_criteria|TEXT|1|''|0 notes|TEXT|1|''|0 status|TEXT|1|'open'|0 priority|INTEGER|1|2|0 issue_type|TEXT|1|'task'|0 assignee|TEXT|0|-|0 estimated_minutes|INTEGER|0|-|0 created_at|DATETIME|1|CURRENT_TIMESTAMP|0 created_by|TEXT|0|''|0 owner|TEXT|0|''|0 updated_at|DATETIME|1|CURRENT_TIMESTAMP|0 closed_at|DATETIME|0|-|0 closed_by_session|TEXT|0|''|0 external_ref|TEXT|0|-|0 spec_id|TEXT|0|-|0 compaction_level|INTEGER|0|0|0 compacted_at|DATETIME|0|-|0 compacted_at_commit|TEXT|0|-|0 original_size|INTEGER|0|-|0 deleted_at|DATETIME|0|-|0 deleted_by|TEXT|0|''|0 delete_reason|TEXT|0|''|0 original_type|TEXT|0|''|0 sender|TEXT|0|''|0 ephemeral|INTEGER|0|0|0 wisp_type|TEXT|0|''|0 pinned|INTEGER|0|0|0 is_template|INTEGER|0|0|0 crystallizes|INTEGER|0|0|0 mol_type|TEXT|0|''|0 work_type|TEXT|0|'mutex'|0 quality_score|REAL|0|-|0 source_system|TEXT|0|''|0 metadata|TEXT|1|'{}'|0 event_kind|TEXT|0|''|0 actor|TEXT|0|''|0 target|TEXT|0|''|0 payload|TEXT|0|''|0 source_repo|TEXT|0|'.'|0 close_reason|TEXT|0|''|0 await_type|TEXT|0|-|0 await_id|TEXT|0|-|0 timeout_ns|INTEGER|0|-|0 waiters|TEXT|0|-|0 hook_bead|TEXT|0|''|0 role_bead|TEXT|0|''|0 agent_state|TEXT|0|''|0 last_activity|DATETIME|0|-|0 role_type|TEXT|0|''|0 rig|TEXT|0|''|0 due_at|DATETIME|0|-|0 defer_until|DATETIME|0|-|0`,
	"dependencies": `issue_id|TEXT|1|-|1 depends_on_id|TEXT|1|-|2 type|TEXT|1|'blocks'|3 created_at|TIMESTAMP|0|CURRENT_TIMESTAMP|0 created_by|TEXT|1|-|0 metadata|TEXT|0|-|0 thread_id|TEXT|0|-|0`,
	"labels":       `issue_id|TEXT|1|-|1 label|TEXT|1|-|2`,
	"comments":     `id|INTEGER|0|-|1 issue_id|TEXT|1|-|0 author|TEXT|1|-|0 text|TEXT|1|-|0 created_at|DATETIME|1|CURRENT_TIMESTAMP|0`,
}

// Export emits current canonical issue JSONL. It opens only a sealed private
// copy, and renames a completed spool to a file destination.
func Export(ctx context.Context, source, output string, stdout io.Writer) error {
	sealed, err := seal(source)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(sealed.dir) }()
	if output != "-" {
		if err := rejectAlias(source, output); err != nil {
			return err
		}
		if err := rejectAlias(sealed.source, output); err != nil {
			return err
		}
	}

	spoolDir := ""
	if output != "-" {
		spoolDir = filepath.Dir(output)
	}
	spool, err := os.CreateTemp(spoolDir, ".bd-legacy-sqlite-*")
	if err != nil {
		return fmt.Errorf("create output spool: %w", err)
	}
	spoolName := spool.Name()
	defer func() { _ = os.Remove(spoolName) }()
	if err := read(ctx, sealed.db, spool); err != nil {
		_ = spool.Close()
		return err
	}
	if err := spool.Close(); err != nil {
		return err
	}
	if output == "-" {
		_, err = spoolTo(stdout, spoolName)
		return err
	}
	return os.Rename(spoolName, output)
}

func spoolTo(w io.Writer, name string) (int64, error) {
	f, err := os.Open(name) //nolint:gosec // G304: name is the private spool created by Export.
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(w, f)
}

type sealedDB struct{ dir, db, source string }
type fingerprint struct {
	exists bool
	size   int64
	mod    time.Time
	digest string
	info   os.FileInfo
}
type sourceSet struct{ db, wal, journal fingerprint }

func seal(source string) (sealedDB, error) {
	resolved, err := filepath.Abs(source)
	if err != nil {
		return sealedDB{}, fmt.Errorf("resolve legacy SQLite source: %w", err)
	}
	before, err := fingerprintSource(resolved)
	if err != nil {
		return sealedDB{}, err
	}
	if before.journal.exists {
		return sealedDB{}, fmt.Errorf("legacy SQLite source has rollback journal")
	}
	dir, err := os.MkdirTemp("", "bd-legacy-sqlite-")
	if err != nil {
		return sealedDB{}, err
	}
	fail := func(err error) (sealedDB, error) { _ = os.RemoveAll(dir); return sealedDB{}, err }
	for _, pair := range []struct {
		from, to string
		present  bool
	}{{resolved, filepath.Join(dir, "legacy.db"), true}, {resolved + "-wal", filepath.Join(dir, "legacy.db-wal"), before.wal.exists}} {
		if pair.present {
			if err := copyFile(pair.from, pair.to); err != nil {
				return fail(err)
			}
		}
	}
	if copied, err := fingerprintFile(filepath.Join(dir, "legacy.db"), true); err != nil || copied.digest != before.db.digest {
		if err == nil {
			err = fmt.Errorf("sealed legacy SQLite database does not match source fingerprint")
		}
		return fail(err)
	}
	if before.wal.exists {
		if copied, err := fingerprintFile(filepath.Join(dir, "legacy.db-wal"), true); err != nil || copied.digest != before.wal.digest {
			if err == nil {
				err = fmt.Errorf("sealed legacy SQLite WAL does not match source fingerprint")
			}
			return fail(err)
		}
	}
	after, err := fingerprintSource(resolved)
	if err != nil {
		return fail(err)
	}
	if !sameSet(before, after) {
		return fail(fmt.Errorf("legacy SQLite source changed while sealing"))
	}
	return sealedDB{dir: dir, db: filepath.Join(dir, "legacy.db"), source: resolved}, nil
}

func fingerprintSource(path string) (sourceSet, error) {
	db, err := fingerprintFile(path, true)
	if err != nil {
		return sourceSet{}, err
	}
	wal, err := fingerprintFile(path+"-wal", false)
	if err != nil {
		return sourceSet{}, err
	}
	if _, err := fingerprintFile(path+"-shm", false); err != nil {
		return sourceSet{}, err
	}
	journal, err := fingerprintFile(path+"-journal", false)
	if err != nil {
		return sourceSet{}, err
	}
	return sourceSet{db, wal, journal}, nil
}

func fingerprintFile(path string, required bool) (fingerprint, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !required {
		return fingerprint{}, nil
	}
	if err != nil {
		return fingerprint{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fingerprint{}, fmt.Errorf("legacy SQLite source %q must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fingerprint{}, fmt.Errorf("legacy SQLite source %q must be a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // G304: source is lstat-checked and fingerprinted again after sealing.
	if err != nil {
		return fingerprint{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return fingerprint{}, err
	}
	return fingerprint{true, info.Size(), info.ModTime(), hex.EncodeToString(h.Sum(nil)), info}, nil
}

func sameSet(a, b sourceSet) bool {
	return sameFingerprint(a.db, b.db) && sameFingerprint(a.wal, b.wal) && sameFingerprint(a.journal, b.journal)
}
func sameFingerprint(a, b fingerprint) bool {
	return a.exists == b.exists && (!a.exists || (a.size == b.size && a.mod.Equal(b.mod) && a.digest == b.digest && os.SameFile(a.info, b.info)))
}
func copyFile(from, to string) error {
	in, err := os.Open(from) //nolint:gosec // G304: from is the checked legacy source or its WAL sidecar.
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: to is inside Export's private sealing directory.
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func rejectAlias(source, output string) error {
	for _, protected := range []string{source, source + "-wal", source + "-shm", source + "-journal"} {
		if samePath(protected, output) {
			return fmt.Errorf("--output must not alias legacy SQLite source or sidecar")
		}
	}
	return nil
}

func samePath(a, b string) bool {
	aa, errA := canonicalPath(a)
	bb, errB := canonicalPath(b)
	if errA != nil || errB != nil {
		return false
	}
	if aa == bb {
		return true
	}
	ai, errA := os.Stat(aa)
	bi, errB := os.Stat(bb)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for current := abs; ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		tail = append([]string{filepath.Base(current)}, tail...)
	}
}

func read(ctx context.Context, path string, out io.Writer) error {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_query_only=1&_loc=UTC")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verify(ctx, tx); err != nil {
		return err
	}
	issues, err := loadIssues(ctx, tx)
	if err != nil {
		return err
	}
	if err := loadChildren(ctx, tx, issues); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	for _, issue := range issues {
		if err := enc.Encode(issue); err != nil {
			return err
		}
	}
	return nil
}

func verify(ctx context.Context, db *sql.Tx) error {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&version); err != nil {
		return fmt.Errorf("legacy SQLite release marker: %w", err)
	}
	if !acceptedVersions[version] {
		return fmt.Errorf("unsupported legacy SQLite release %q", version)
	}
	for table, want := range schema {
		if err := verifyTable(ctx, db, table, want); err != nil {
			return err
		}
	}
	for _, table := range []string{"metadata", "issues", "dependencies", "labels", "comments"} {
		if err := verifyFKs(ctx, db, table); err != nil {
			return err
		}
	}
	return nil
}

func verifyTable(ctx context.Context, db *sql.Tx, table, want string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_xinfo("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var cid, hidden int
		var name, typ string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk, &hidden); err != nil {
			return err
		}
		if hidden != 0 {
			return fmt.Errorf("legacy SQLite schema drift in %s hidden column", table)
		}
		defaultText := "-"
		if defaultValue.Valid {
			defaultText = defaultValue.String
		}
		got = append(got, fmt.Sprintf("%s|%s|%d|%s|%d", name, typ, notNull, defaultText, pk))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if strings.Join(got, " ") != want {
		return fmt.Errorf("legacy SQLite schema drift in %s", table)
	}
	return nil
}

func verifyFKs(ctx context.Context, db *sql.Tx, table string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_list("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, seq int
		var target, from, to, update, deleteAction, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &update, &deleteAction, &match); err != nil {
			return err
		}
		if table == "issues" || table == "metadata" || target != "issues" || from != "issue_id" || to != "id" || update != "NO ACTION" || deleteAction != "CASCADE" || match != "NONE" {
			return fmt.Errorf("legacy SQLite foreign-key drift in %s", table)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if table != "issues" && table != "metadata" && count != 1 {
		return fmt.Errorf("legacy SQLite foreign-key drift in %s", table)
	}
	return nil
}

// loadIssuesProjection is the SELECT list feeding issueops.ScanIssueFrom in
// loadIssues. It must emit exactly the canonical issueops.IssueSelectColumns
// prefix — columns the legacy schema lacks are projected as NULL/0 — followed
// by the legacy trailing columns scanned via (*legacyExtras).scanDests. That
// canonical prefix is positional (ScanIssueFrom scans it slot-for-slot), so a
// new column in issueops.IssueSelectColumns needs a matching placeholder here;
// the variadic ScanIssueFrom boundary hides any count mismatch from the
// compiler, so TestLoadIssuesProjectionArity guards the invariant and makes the
// drift fail at test time instead of mid-migration.
const loadIssuesProjection = `id,content_hash,title,description,design,acceptance_criteria,notes,status,priority,issue_type,assignee,estimated_minutes,CAST(created_at AS TEXT),created_by,owner,CAST(updated_at AS TEXT),NULL,NULL,external_ref,spec_id,COALESCE(compaction_level,0),NULL,compacted_at_commit,original_size,source_repo,close_reason,NULL,sender,ephemeral,0,wisp_type,pinned,is_template,await_type,await_id,timeout_ns,NULL,mol_type,event_kind,actor,target,payload,NULL,NULL,work_type,source_system,NULL,0,NULL,NULL,NULL,NULL,closed_by_session,CAST(deleted_at AS TEXT),deleted_by,delete_reason,original_type,crystallizes,quality_score,hook_bead,role_bead,agent_state,CAST(last_activity AS TEXT),role_type,rig,metadata,waiters,ephemeral,pinned,is_template,estimated_minutes,compaction_level,original_size,CAST(closed_at AS TEXT),CAST(compacted_at AS TEXT),CAST(due_at AS TEXT),CAST(defer_until AS TEXT)`

func loadIssues(ctx context.Context, db *sql.Tx) ([]*types.Issue, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+loadIssuesProjection+" FROM issues ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*types.Issue
	for rows.Next() {
		var x legacyExtras
		issue, err := issueops.ScanIssueFrom(rows, x.scanDests()...)
		if err != nil {
			return nil, err
		}
		if err := x.validate(issue); err != nil {
			return nil, err
		}
		result = append(result, issue)
	}
	return result, rows.Err()
}

// scanDests returns the destinations for the legacy trailing columns that
// follow the canonical issueops.IssueSelectColumns prefix in
// loadIssuesProjection, in projection order. ScanIssueFrom appends these after
// the canonical scan destinations.
func (x *legacyExtras) scanDests() []any {
	return []any{
		&x.closedBy, &x.deletedAt, &x.deletedBy, &x.deleteReason, &x.originalType,
		&x.crystallizes, &x.quality, &x.hookBead, &x.roleBead, &x.agentState,
		&x.lastActivity, &x.roleType, &x.rig, &x.metadata, &x.waiters,
		&x.ephemeral, &x.pinned, &x.template, &x.estimatedMinutes, &x.compactionLevel,
		&x.originalSize, &x.closedAt, &x.compactedAt, &x.dueAt, &x.deferUntil,
	}
}

type legacyExtras struct {
	closedBy, deletedAt, deletedBy, deleteReason, originalType, hookBead, roleBead, agentState, lastActivity, metadata, waiters sql.NullString
	closedAt, compactedAt, dueAt, deferUntil                                                                                    sql.NullString
	crystallizes, ephemeral, pinned, template                                                                                   sql.NullInt64
	estimatedMinutes, compactionLevel, originalSize                                                                             sql.NullInt64
	quality                                                                                                                     sql.NullFloat64
	roleType, rig                                                                                                               sql.NullString
}

func (x legacyExtras) validate(issue *types.Issue) error {
	if err := checkUTF8(
		currentString{"issue metadata", nullString(x.metadata)},
		currentString{"issue waiters", nullString(x.waiters)},
	); err != nil {
		return err
	}
	if err := x.applyOptionalTimestamps(issue); err != nil {
		return err
	}
	if err := x.applyMetadataAndWaiters(issue); err != nil {
		return err
	}
	if err := validateIssueStrings(issue); err != nil {
		return err
	}
	if err := validateCurrentTextBytes(issue); err != nil {
		return err
	}
	if err := validateIssueVarchars(issue); err != nil {
		return err
	}
	if err := checkCurrentInts(
		currentInt{"issue estimated_minutes", x.estimatedMinutes},
		currentInt{"issue compaction_level", x.compactionLevel},
		currentInt{"issue original_size", x.originalSize},
	); err != nil {
		return err
	}
	if err := x.applyCanonicalTimestamps(issue); err != nil {
		return err
	}
	if err := checkRequiredScalars(issue); err != nil {
		return err
	}
	if err := x.checkRemovedFields(issue); err != nil {
		return err
	}
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("legacy SQLite issue %s: %w", issue.ID, err)
	}
	return nil
}

// applyOptionalTimestamps parses the legacy issue's optional timestamp columns
// (closed_at, compacted_at, due_at, defer_until) and assigns them to issue.
func (x legacyExtras) applyOptionalTimestamps(issue *types.Issue) error {
	var err error
	if issue.ClosedAt, err = parseOptionalTime("closed_at", x.closedAt); err != nil {
		return fmt.Errorf("legacy SQLite issue %s: %w", issue.ID, err)
	}
	if issue.CompactedAt, err = parseOptionalTime("compacted_at", x.compactedAt); err != nil {
		return fmt.Errorf("legacy SQLite issue %s: %w", issue.ID, err)
	}
	if issue.DueAt, err = parseOptionalTime("due_at", x.dueAt); err != nil {
		return fmt.Errorf("legacy SQLite issue %s: %w", issue.ID, err)
	}
	if issue.DeferUntil, err = parseOptionalTime("defer_until", x.deferUntil); err != nil {
		return fmt.Errorf("legacy SQLite issue %s: %w", issue.ID, err)
	}
	return nil
}

// applyMetadataAndWaiters validates the legacy metadata and waiters JSON blobs
// (well-formed and free of unpaired surrogates) and assigns them to issue.
// metadata is stored verbatim unless it is the empty object; waiters is decoded.
func (x legacyExtras) applyMetadataAndWaiters(issue *types.Issue) error {
	if x.metadata.Valid && x.metadata.String != "" && !json.Valid([]byte(x.metadata.String)) {
		return fmt.Errorf("legacy SQLite issue %s has invalid metadata JSON", issue.ID)
	}
	if x.metadata.Valid && x.metadata.String != "" {
		if err := checkJSONSurrogates(x.metadata.String); err != nil {
			return fmt.Errorf("legacy SQLite issue %s metadata: %w", issue.ID, err)
		}
	}
	if x.metadata.Valid && x.metadata.String != "" && x.metadata.String != "{}" {
		issue.Metadata = []byte(x.metadata.String)
	}
	if x.waiters.Valid && x.waiters.String != "" {
		if !json.Valid([]byte(x.waiters.String)) {
			return fmt.Errorf("issue %s waiters: invalid JSON", issue.ID)
		}
		if err := checkJSONSurrogates(x.waiters.String); err != nil {
			return fmt.Errorf("issue %s waiters: %w", issue.ID, err)
		}
		waiters, err := decodeWaiters(x.waiters.String)
		if err != nil {
			return fmt.Errorf("issue %s waiters: %w", issue.ID, err)
		}
		issue.Waiters = waiters
	}
	return nil
}

// applyCanonicalTimestamps normalizes the required created_at/updated_at values
// to the canonical current-schema representation.
func (x legacyExtras) applyCanonicalTimestamps(issue *types.Issue) error {
	var err error
	if issue.CreatedAt, err = canonicalCurrentDatetime(issue.CreatedAt); err != nil {
		return fmt.Errorf("legacy SQLite issue %s created_at: %w", issue.ID, err)
	}
	if issue.UpdatedAt, err = canonicalCurrentDatetime(issue.UpdatedAt); err != nil {
		return fmt.Errorf("legacy SQLite issue %s updated_at: %w", issue.ID, err)
	}
	return nil
}

// checkRequiredScalars enforces the non-empty ID, non-tombstone status, and
// present created_at/updated_at invariants every legacy issue must satisfy.
func checkRequiredScalars(issue *types.Issue) error {
	if issue.ID == "" {
		return fmt.Errorf("legacy SQLite issue has empty ID")
	}
	if issue.Status == "tombstone" {
		return fmt.Errorf("legacy SQLite issue %s is a tombstone", issue.ID)
	}
	if issue.CreatedAt.IsZero() || issue.UpdatedAt.IsZero() {
		return fmt.Errorf("legacy SQLite issue %s has invalid created_at or updated_at", issue.ID)
	}
	return nil
}

// checkRemovedFields rejects legacy rows that populate columns the current
// schema no longer supports, then validates the tri-state boolean columns.
func (x legacyExtras) checkRemovedFields(issue *types.Issue) error {
	if nonempty(x.closedBy, x.deletedBy, x.deleteReason, x.originalType, x.hookBead, x.roleBead, x.agentState, x.lastActivity, x.roleType, x.rig) || x.deletedAt.Valid || x.crystallizes.Int64 != 0 || x.quality.Valid || (issue.SourceRepo != "" && issue.SourceRepo != ".") {
		return fmt.Errorf("legacy SQLite issue %s uses unsupported removed fields", issue.ID)
	}
	for _, b := range []struct {
		name string
		v    sql.NullInt64
	}{{"ephemeral", x.ephemeral}, {"pinned", x.pinned}, {"is_template", x.template}} {
		if b.v.Valid && b.v.Int64 != 0 && b.v.Int64 != 1 {
			return fmt.Errorf("issue %s has invalid %s boolean", issue.ID, b.name)
		}
	}
	return nil
}

type currentVarchar struct {
	name, value string
	maxRunes    int
}

type currentString struct {
	name, value string
}

func validateIssueStrings(issue *types.Issue) error {
	fields := []currentString{
		{"issue id", issue.ID},
		{"issue title", issue.Title},
		{"issue description", issue.Description},
		{"issue design", issue.Design},
		{"issue acceptance_criteria", issue.AcceptanceCriteria},
		{"issue notes", issue.Notes},
		{"issue status", string(issue.Status)},
		{"issue type", string(issue.IssueType)},
		{"issue assignee", issue.Assignee},
		{"issue created_by", issue.CreatedBy},
		{"issue owner", issue.Owner},
		{"issue spec_id", issue.SpecID},
		{"issue close_reason", issue.CloseReason},
		{"issue sender", issue.Sender},
		{"issue wisp_type", string(issue.WispType)},
		{"issue await_type", issue.AwaitType},
		{"issue await_id", issue.AwaitID},
		{"issue mol_type", string(issue.MolType)},
		{"issue work_type", string(issue.WorkType)},
		{"issue source_system", issue.SourceSystem},
		{"issue event_kind", issue.EventKind},
		{"issue actor", issue.Actor},
		{"issue target", issue.Target},
		{"issue payload", issue.Payload},
	}
	if issue.ExternalRef != nil {
		fields = append(fields, currentString{"issue external_ref", *issue.ExternalRef})
	}
	if issue.CompactedAtCommit != nil {
		fields = append(fields, currentString{"issue compacted_at_commit", *issue.CompactedAtCommit})
	}
	for i, waiter := range issue.Waiters {
		fields = append(fields, currentString{fmt.Sprintf("issue waiters[%d]", i), waiter})
	}
	return checkUTF8(fields...)
}

func checkUTF8(fields ...currentString) error {
	for _, field := range fields {
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("legacy SQLite %s contains invalid UTF-8", field.name)
		}
	}
	return nil
}

func checkJSONSurrogates(raw string) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) {
			return fmt.Errorf("truncated JSON escape")
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return fmt.Errorf("truncated JSON Unicode escape")
		}
		code, err := strconv.ParseUint(raw[i+2:i+6], 16, 16)
		if err != nil {
			return fmt.Errorf("invalid JSON Unicode escape")
		}
		switch {
		case code >= 0xd800 && code <= 0xdbff:
			next := i + 6
			if next+6 > len(raw) || raw[next] != '\\' || raw[next+1] != 'u' {
				return fmt.Errorf("lone high UTF-16 surrogate escape")
			}
			low, err := strconv.ParseUint(raw[next+2:next+6], 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("lone high UTF-16 surrogate escape")
			}
			i = next + 5
		case code >= 0xdc00 && code <= 0xdfff:
			return fmt.Errorf("lone low UTF-16 surrogate escape")
		default:
			i += 5
		}
	}
	return nil
}

func validateCurrentTextBytes(issue *types.Issue) error {
	if len(issue.Payload) > currentTextBytes {
		return fmt.Errorf("legacy SQLite issue payload is %d bytes (current TEXT maximum %d)", len(issue.Payload), currentTextBytes)
	}
	waiters := issueops.FormatJSONStringArray(issue.Waiters)
	if len(waiters) > currentTextBytes {
		return fmt.Errorf("legacy SQLite issue waiters serialize to %d bytes (current TEXT maximum %d)", len(waiters), currentTextBytes)
	}
	return nil
}

func decodeWaiters(raw string) ([]string, error) {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	values, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array of strings")
	}
	waiters := make([]string, len(values))
	for i, value := range values {
		waiter, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("element %d is not a string", i)
		}
		if err := checkUTF8(currentString{fmt.Sprintf("waiters[%d]", i), waiter}); err != nil {
			return nil, err
		}
		waiters[i] = waiter
	}
	return waiters, nil
}

func validateIssueVarchars(issue *types.Issue) error {
	fields := []currentVarchar{
		{"issue id", issue.ID, types.MaxFieldLen},
		{"issue title", issue.Title, currentTitleVarcharRunes},
		{"issue status", string(issue.Status), currentShortVarcharRunes},
		{"issue type", string(issue.IssueType), currentShortVarcharRunes},
		{"issue assignee", issue.Assignee, types.MaxFieldLen},
		{"issue created_by", issue.CreatedBy, types.MaxFieldLen},
		{"issue owner", issue.Owner, types.MaxFieldLen},
		{"issue spec_id", issue.SpecID, currentSpecIDVarcharRunes},
		{"issue sender", issue.Sender, types.MaxFieldLen},
		{"issue wisp_type", string(issue.WispType), currentShortVarcharRunes},
		{"issue await_type", issue.AwaitType, currentShortVarcharRunes},
		{"issue await_id", issue.AwaitID, types.MaxFieldLen},
		{"issue mol_type", string(issue.MolType), currentShortVarcharRunes},
		{"issue event_kind", issue.EventKind, currentShortVarcharRunes},
		{"issue actor", issue.Actor, types.MaxFieldLen},
		{"issue target", issue.Target, types.MaxFieldLen},
		{"issue work_type", string(issue.WorkType), currentShortVarcharRunes},
		{"issue source_system", issue.SourceSystem, types.MaxFieldLen},
	}
	if issue.ExternalRef != nil {
		fields = append(fields, currentVarchar{"issue external_ref", *issue.ExternalRef, types.MaxFieldLen})
	}
	if issue.CompactedAtCommit != nil {
		fields = append(fields, currentVarchar{"issue compacted_at_commit", *issue.CompactedAtCommit, currentCommitVarcharRunes})
	}
	return checkCurrentVarchars(fields...)
}

func checkCurrentVarchars(fields ...currentVarchar) error {
	for _, field := range fields {
		if n := utf8.RuneCountInString(field.value); n > field.maxRunes {
			return fmt.Errorf("legacy SQLite %s is %d characters (current VARCHAR(%d) maximum)", field.name, n, field.maxRunes)
		}
	}
	return nil
}

type currentInt struct {
	name  string
	value sql.NullInt64
}

func checkCurrentInts(fields ...currentInt) error {
	for _, field := range fields {
		if field.value.Valid && (field.value.Int64 < math.MinInt32 || field.value.Int64 > math.MaxInt32) {
			return fmt.Errorf("legacy SQLite %s is %d (current INT range %d..%d)", field.name, field.value.Int64, math.MinInt32, math.MaxInt32)
		}
	}
	return nil
}

func nonempty(values ...sql.NullString) bool {
	for _, v := range values {
		if v.Valid && v.String != "" {
			return true
		}
	}
	return false
}
func nullString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999"} {
		if t, e := time.Parse(layout, s); e == nil {
			canonical, err := canonicalCurrentDatetime(t)
			if err != nil {
				return time.Time{}, fmt.Errorf("timestamp %q: %w", s, err)
			}
			return canonical, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}

func canonicalCurrentDatetime(t time.Time) (time.Time, error) {
	canonical := t.UTC().Round(time.Second)
	if year := canonical.Year(); year < 0 || year > 9999 {
		return time.Time{}, fmt.Errorf("timestamp rounds outside current DATETIME range")
	}
	return canonical, nil
}

func parseOptionalTime(name string, raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	parsed, err := parseTime(raw.String)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &parsed, nil
}

func loadChildren(ctx context.Context, db *sql.Tx, issues []*types.Issue) error {
	byID := map[string]*types.Issue{}
	for _, i := range issues {
		byID[i.ID] = i
	}
	if err := loadLabels(ctx, db, byID); err != nil {
		return err
	}
	if err := loadDependencies(ctx, db, byID); err != nil {
		return err
	}
	if err := validateDependencyGraph(issues); err != nil {
		return err
	}
	return loadComments(ctx, db, byID)
}

func loadLabels(ctx context.Context, db *sql.Tx, byID map[string]*types.Issue) error {
	labels, err := db.QueryContext(ctx, "SELECT issue_id,label FROM labels ORDER BY issue_id,label")
	if err != nil {
		return err
	}
	defer labels.Close()
	for labels.Next() {
		var id, label string
		if err := labels.Scan(&id, &label); err != nil {
			return err
		}
		if err := checkUTF8(
			currentString{"label issue_id", id},
			currentString{"label", label},
		); err != nil {
			return err
		}
		if err := checkCurrentVarchars(
			currentVarchar{"label issue_id", id, types.MaxFieldLen},
			currentVarchar{"label", label, types.MaxFieldLen},
		); err != nil {
			return err
		}
		issue := byID[id]
		if issue == nil {
			return fmt.Errorf("orphan label for %s", id)
		}
		issue.Labels = append(issue.Labels, label)
	}
	return labels.Err()
}

func loadDependencies(ctx context.Context, db *sql.Tx, byID map[string]*types.Issue) error {
	deps, err := db.QueryContext(ctx, "SELECT issue_id,depends_on_id,type,CAST(created_at AS TEXT),created_by,metadata,thread_id FROM dependencies ORDER BY issue_id,depends_on_id,type")
	if err != nil {
		return err
	}
	defer deps.Close()
	seenDeps := make(map[string]bool)
	for deps.Next() {
		if err := appendLegacyDependencyRow(deps, byID, seenDeps); err != nil {
			return err
		}
	}
	return deps.Err()
}

// appendLegacyDependencyRow validates one legacy dependencies row and appends
// the resulting edge to its owning issue. seenDeps carries the per-load
// (issue_id, depends_on_id) set so duplicate legacy edges are rejected across
// rows.
func appendLegacyDependencyRow(deps *sql.Rows, byID map[string]*types.Issue, seenDeps map[string]bool) error {
	var id, to, typ, at, by string
	var metadata, thread sql.NullString
	if err := deps.Scan(&id, &to, &typ, &at, &by, &metadata, &thread); err != nil {
		return err
	}
	if err := checkUTF8(
		currentString{"dependency issue_id", id},
		currentString{"dependency depends_on_id", to},
		currentString{"dependency type", typ},
		currentString{"dependency created_by", by},
	); err != nil {
		return err
	}
	if err := checkCurrentVarchars(
		currentVarchar{"dependency issue_id", id, types.MaxFieldLen},
		currentVarchar{"dependency depends_on_id", to, types.MaxFieldLen},
		currentVarchar{"dependency type", typ, currentShortVarcharRunes},
		currentVarchar{"dependency created_by", by, types.MaxFieldLen},
	); err != nil {
		return err
	}
	if by == "" {
		return fmt.Errorf("dependency created_by is empty for %s -> %s", id, to)
	}
	issue := byID[id]
	if issue == nil || byID[to] == nil {
		return fmt.Errorf("orphan dependency %s -> %s", id, to)
	}
	if issue.Ephemeral != byID[to].Ephemeral {
		return fmt.Errorf("dependency %s -> %s crosses ephemeral storage", id, to)
	}
	key := id + "\x00" + to
	if seenDeps[key] {
		return fmt.Errorf("multiple legacy dependencies for %s -> %s", id, to)
	}
	seenDeps[key] = true
	created, e := parseTime(at)
	if e != nil {
		return e
	}
	if created.IsZero() {
		return fmt.Errorf("dependency created_at is zero for %s -> %s", id, to)
	}
	if (metadata.Valid && metadata.String != "") || (thread.Valid && thread.String != "") {
		return fmt.Errorf("dependency %s -> %s uses unsupported metadata or thread ID", id, to)
	}
	if !types.DependencyType(typ).IsValid() {
		return fmt.Errorf("dependency %s -> %s has invalid type", id, to)
	}
	d := &types.Dependency{IssueID: id, DependsOnID: to, Type: types.DependencyType(typ), CreatedAt: created, CreatedBy: by, Metadata: nullString(metadata), ThreadID: nullString(thread)}
	issue.Dependencies = append(issue.Dependencies, d)
	return nil
}

func loadComments(ctx context.Context, db *sql.Tx, byID map[string]*types.Issue) error {
	comments, err := db.QueryContext(ctx, "SELECT id,issue_id,author,text,CAST(created_at AS TEXT) FROM comments ORDER BY issue_id,created_at,id")
	if err != nil {
		return err
	}
	defer comments.Close()
	type commentIdentity struct {
		issueID, author, text string
		createdAt             time.Time
	}
	seenComments := make(map[commentIdentity]int64)
	for comments.Next() {
		var id int64
		var issueID, author, text, at string
		if err := comments.Scan(&id, &issueID, &author, &text, &at); err != nil {
			return err
		}
		if err := checkUTF8(
			currentString{"comment issue_id", issueID},
			currentString{"comment author", author},
			currentString{"comment text", text},
		); err != nil {
			return err
		}
		if err := checkCurrentVarchars(
			currentVarchar{"comment issue_id", issueID, types.MaxFieldLen},
			currentVarchar{"comment author", author, types.MaxFieldLen},
		); err != nil {
			return err
		}
		issue := byID[issueID]
		if issue == nil {
			return fmt.Errorf("orphan comment for %s", issueID)
		}
		if issue.Ephemeral && len(text) > currentTextBytes {
			return fmt.Errorf("legacy SQLite ephemeral comment text is %d bytes (current TEXT maximum %d)", len(text), currentTextBytes)
		}
		created, e := parseTime(at)
		if e != nil {
			return e
		}
		if created.IsZero() {
			return fmt.Errorf("comment created_at is zero for issue %s", issueID)
		}
		identity := commentIdentity{issueID: issueID, author: author, text: text, createdAt: created}
		if priorID, exists := seenComments[identity]; exists {
			return fmt.Errorf("legacy SQLite comments %d and %d share current import identity", priorID, id)
		}
		seenComments[identity] = id
		issue.Comments = append(issue.Comments, &types.Comment{ID: strconv.FormatInt(id, 10), IssueID: issueID, Author: author, Text: text, CreatedAt: created})
	}
	return comments.Err()
}

func validateDependencyGraph(issues []*types.Issue) error {
	scheduling := make(map[string][]string)
	hierarchy := make(map[string][]string)
	var blocking []*types.Dependency
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep.IssueID == dep.DependsOnID {
				return fmt.Errorf("dependency %s -> %s is a self-dependency", dep.IssueID, dep.DependsOnID)
			}
			switch dep.Type {
			case types.DepBlocks, types.DepConditionalBlocks:
				blocking = append(blocking, dep)
				scheduling[dep.IssueID] = append(scheduling[dep.IssueID], dep.DependsOnID)
			case types.DepParentChild:
				hierarchy[dep.IssueID] = append(hierarchy[dep.IssueID], dep.DependsOnID)
				scheduling[dep.IssueID] = append(scheduling[dep.IssueID], dep.DependsOnID)
			}
		}
	}
	if hasDirectedCycle(scheduling) {
		return fmt.Errorf("legacy SQLite dependency graph has a scheduling cycle")
	}
	for _, dep := range blocking {
		if types.ExtractPrefix(dep.IssueID) == types.ExtractPrefix(dep.DependsOnID) &&
			(reachable(hierarchy, dep.IssueID, dep.DependsOnID) ||
				reachable(hierarchy, dep.DependsOnID, dep.IssueID)) {
			return fmt.Errorf("blocking dependency %s -> %s conflicts with parent-child hierarchy", dep.IssueID, dep.DependsOnID)
		}
	}
	return nil
}

func hasDirectedCycle(graph map[string][]string) bool {
	indegree := make(map[string]int, len(graph))
	for from, targets := range graph {
		if _, ok := indegree[from]; !ok {
			indegree[from] = 0
		}
		for _, target := range targets {
			indegree[target]++
		}
	}
	queue := make([]string, 0, len(indegree))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, target := range graph[id] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	return visited != len(indegree)
}

func reachable(graph map[string][]string, start, target string) bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, next := range graph[node] {
			if next == target {
				return true
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}
