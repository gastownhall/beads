package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// This file is the planning half of `bd dolt pull --renumber-children`
// (bd-28sb25, GH#4796). Two clones that each run `bd create --parent P`
// offline both mint P.71 for different work; the pull's merge then hits an
// issues add/add collision it rightly refuses to settle. The repair keeps the
// remote's identities and moves the LOCAL colliding children above both
// sides' high-water marks before merging.

// commitHashPattern is Dolt's commit hash alphabet. Revisions are read with
// AS OF, which takes a literal rather than a bind parameter, so every hash is
// checked against it before it is interpolated.
var commitHashPattern = regexp.MustCompile(`^[0-9a-v]{32}$`)

// renumberRevisions are the three commits a renumber plan compares.
type renumberRevisions struct {
	ours, theirs, base string
}

// PlanChildRenumbers compares branch's tip with ref's tip (normally the
// remote-tracking ref a fetch just updated) and proposes renames for the
// local hierarchical children both sides added since their merge base with
// different content. It reads committed revisions only and writes nothing,
// so it is safe after a failed pull, whatever state that left behind.
func PlanChildRenumbers(ctx context.Context, db DBConn, branch, ref string) (*storage.ChildRenumberReport, error) {
	revs, err := resolveRenumberRevisions(ctx, db, branch, ref)
	if err != nil {
		return nil, err
	}
	report := &storage.ChildRenumberReport{
		Branch:     branch,
		LocalHead:  revs.ours,
		RemoteHead: revs.theirs,
		MergeBase:  revs.base,
		Renames:    []storage.ChildRenumber{},
	}
	if revs.base == revs.ours || revs.base == revs.theirs {
		// A fast-forward either way adds nothing on one side.
		return report, nil
	}
	renames, err := planRenamesAt(ctx, db, revs)
	if err != nil {
		return nil, err
	}
	report.Renames = renames
	return report, nil
}

func resolveRenumberRevisions(ctx context.Context, db DBConn, branch, ref string) (renumberRevisions, error) {
	var revs renumberRevisions
	if err := db.QueryRowContext(ctx, "SELECT HASHOF(?)", branch).Scan(&revs.ours); err != nil {
		return revs, fmt.Errorf("resolve %s: %w", branch, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT HASHOF(?)", ref).Scan(&revs.theirs); err != nil {
		return revs, fmt.Errorf("resolve %s: %w", ref, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT DOLT_MERGE_BASE(?, ?)", revs.ours, revs.theirs).Scan(&revs.base); err != nil {
		return revs, fmt.Errorf("merge base of %s and %s: %w", branch, ref, err)
	}
	for _, h := range []string{revs.ours, revs.theirs, revs.base} {
		if !commitHashPattern.MatchString(h) {
			return revs, fmt.Errorf("unexpected commit hash %q", h)
		}
	}
	return revs, nil
}

func planRenamesAt(ctx context.Context, db DBConn, revs renumberRevisions) ([]storage.ChildRenumber, error) {
	ours, err := idsAsOf(ctx, db, "issues", revs.ours)
	if err != nil {
		return nil, err
	}
	theirs, err := idsAsOf(ctx, db, "issues", revs.theirs)
	if err != nil {
		return nil, err
	}
	base, err := idsAsOf(ctx, db, "issues", revs.base)
	if err != nil {
		return nil, err
	}
	var added []string
	for id := range ours {
		if theirs[id] && !base[id] {
			if _, _, ok := issueops.ParseHierarchicalID(id); ok {
				added = append(added, id)
			}
		}
	}
	if len(added) == 0 {
		return []storage.ChildRenumber{}, nil
	}
	sort.Strings(added)
	collisions, err := rowsThatDiffer(ctx, db, added, revs.ours, revs.theirs)
	if err != nil {
		return nil, err
	}
	if len(collisions) == 0 {
		return []storage.ChildRenumber{}, nil
	}

	taken := make(map[string]bool, len(ours)+len(theirs))
	for id := range ours {
		taken[id] = true
	}
	for id := range theirs {
		taken[id] = true
	}
	for _, rev := range []string{revs.ours, revs.theirs} {
		retired, err := idsAsOf(ctx, db, issueops.IDTombstoneTable, rev)
		if err != nil {
			return nil, err
		}
		for id := range retired {
			taken[id] = true
		}
	}
	// The wisp plane and its tombstones are clone-local (dolt-ignored), so
	// they are read from the working set, not from a revision.
	for _, table := range []string{"wisps", issueops.WispIDTombstoneTable} {
		local, err := idsAsOf(ctx, db, table, "")
		if err != nil {
			return nil, err
		}
		for id := range local {
			taken[id] = true
		}
	}

	floors := make(map[string]int)
	for _, id := range collisions {
		parent, _, _ := issueops.ParseHierarchicalID(id)
		if _, done := floors[parent]; done {
			continue
		}
		floor := 0
		for _, rev := range []string{revs.ours, revs.theirs} {
			n, err := counterAsOf(ctx, db, parent, rev)
			if err != nil {
				return nil, err
			}
			floor = max(floor, n)
		}
		for id := range taken {
			if p, n, ok := issueops.ParseHierarchicalID(id); ok && p == parent {
				floor = max(floor, n)
			}
		}
		floors[parent] = floor
	}

	local := make([]string, 0, len(ours))
	for id := range ours {
		local = append(local, id)
	}
	return issueops.AssignChildRenumbers(collisions, local, floors, taken)
}

// asOfClause renders " AS OF '<rev>'" for a validated commit hash, or nothing
// for the working set.
func asOfClause(rev string) string {
	if rev == "" {
		return ""
	}
	return " AS OF '" + rev + "'"
}

// tableExistsAt reports whether table exists at rev ("" = working set). The
// probe runs before any SELECT because a failing SELECT can pin a pooled
// Dolt session to its pre-statement catalog (be-bv7x).
func tableExistsAt(ctx context.Context, db DBConn, table, rev string) (bool, error) {
	//nolint:gosec // G201: rev is a validated commit hash (AS OF takes a literal); table is a constant.
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SHOW TABLES%s LIKE '%s'", asOfClause(rev), table))
	if err != nil {
		return false, fmt.Errorf("probe %s at %q: %w", table, rev, err)
	}
	defer rows.Close()
	found := rows.Next()
	return found, rows.Err()
}

// idsAsOf returns the id column of table at rev ("" = working set). A table
// the revision predates holds no ids; any other failure propagates.
func idsAsOf(ctx context.Context, db DBConn, table, rev string) (map[string]bool, error) {
	ok, err := tableExistsAt(ctx, db, table, rev)
	if err != nil || !ok {
		return map[string]bool{}, err
	}
	//nolint:gosec // G201: rev is a validated commit hash (AS OF takes a literal); table is a constant.
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT id FROM `%s`%s", table, asOfClause(rev)))
	if err != nil {
		return nil, fmt.Errorf("read %s ids at %q: %w", table, rev, err)
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read %s ids at %q: %w", table, rev, err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s ids at %q: %w", table, rev, err)
	}
	return ids, nil
}

// counterAsOf reads parent's child_counters high-water mark at rev; a missing
// row (or table) is 0.
func counterAsOf(ctx context.Context, db DBConn, parent, rev string) (int, error) {
	ok, err := tableExistsAt(ctx, db, "child_counters", rev)
	if err != nil || !ok {
		return 0, err
	}
	var n int
	//nolint:gosec // G201: rev is a validated commit hash (AS OF takes a literal).
	err = db.QueryRowContext(ctx, fmt.Sprintf("SELECT last_child FROM child_counters%s WHERE parent_id = ?", asOfClause(rev)), parent).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read child counter %s at %s: %w", parent, rev, err)
	}
	return n, nil
}

// rowsThatDiffer returns the ids whose issues rows differ between the two
// revisions. Identical rows merge cleanly, so they are the same issue, not a
// collision.
func rowsThatDiffer(ctx context.Context, db DBConn, ids []string, ours, theirs string) ([]string, error) {
	a, err := issueRowsAsOf(ctx, db, ids, ours)
	if err != nil {
		return nil, err
	}
	b, err := issueRowsAsOf(ctx, db, ids, theirs)
	if err != nil {
		return nil, err
	}
	var differ []string
	for _, id := range ids {
		if !sameRow(a[id], b[id]) {
			differ = append(differ, id)
		}
	}
	return differ, nil
}

func sameRow(a, b map[string]*string) bool {
	if len(a) != len(b) {
		return false
	}
	for col, av := range a {
		bv, ok := b[col]
		if !ok || (av == nil) != (bv == nil) || (av != nil && *av != *bv) {
			return false
		}
	}
	return true
}

// issueRowsAsOf reads the full issues rows for ids at rev, as column ->
// rendered value (nil for NULL).
func issueRowsAsOf(ctx context.Context, db DBConn, ids []string, rev string) (map[string]map[string]*string, error) {
	placeholders := make([]byte, 0, 2*len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	//nolint:gosec // G201: rev is a validated commit hash (AS OF takes a literal); the IN list is placeholders.
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM issues%s WHERE id IN (%s)", asOfClause(rev), placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("read issues at %s: %w", rev, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]*string, len(ids))
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		dests := make([]any, len(cols))
		for i := range vals {
			dests[i] = &vals[i]
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, fmt.Errorf("read issues at %s: %w", rev, err)
		}
		row := make(map[string]*string, len(cols))
		var id string
		for i, c := range cols {
			if c == "id" {
				id = vals[i].String
			}
			if vals[i].Valid {
				v := vals[i].String
				row[c] = &v
			} else {
				row[c] = nil
			}
		}
		out[id] = row
	}
	return out, rows.Err()
}
