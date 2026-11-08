package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// This file backs the GH#6368 triage: a table listed in dolt_ignore but still
// tracked on the remote makes every pull refuse forever, surfaced as a generic
// exit 1. The engine checks for stomped working tables BEFORE checking whether
// the incoming history is already contained in HEAD, so even a remote that is
// a strict ancestor of this clone — a pull with nothing to merge — is refused
// with "local changes would be stomped by merge". The events plane flip
// (main migration 0062) makes that the ordinary state of every migrated clone
// with local ignored events rows.
//
// The pieces below classify that refusal into a Beads-owned error type and,
// in the normal pull wrappers, recognize the one shape that is safe to treat
// as the no-op pull it really is. Direct merge entry points
// (MergeAndSettle/MergeAndSettleWithStrategy) keep refusing: an operator
// naming a ref explicitly must not have the refusal silently swallowed.

// IgnoredTablesMergeError is the engine's stomped-tables merge refusal with
// every named table positively confirmed as an ignored working table on the
// session that refused the merge (dolt_status_ignored). Tables are deduped and
// sorted; Err is the engine's original error, preserved for the operator and
// reachable through Unwrap.
//
// It does NOT mean the remote carries new work: the engine stomps the named
// working tables before it ever compares histories, so the refusal alone says
// nothing about containment. Callers that want to treat the pull as the no-op
// it can be must prove containment separately (see
// resolveStompedIgnoredTables).
type IgnoredTablesMergeError struct {
	Tables []string
	Err    error
}

func (e *IgnoredTablesMergeError) Error() string {
	return fmt.Sprintf("merge refused to protect ignored working table(s) %s: %s",
		strings.Join(e.Tables, ", "), e.Err)
}

func (e *IgnoredTablesMergeError) Unwrap() error { return e.Err }

// The engine's stomped-tables diagnostic is bounded: one marker, the names,
// one terminator. Parsing anything else risks inventing a table name out of
// surrounding error text, so both anchors must be present and the segment
// between them must hold nothing but names.
const (
	stompedTablesMarker     = "local changes would be stomped by merge:"
	stompedTablesTerminator = "Please commit your changes before you merge."
)

// ParseStompedTableNames extracts the table names from the engine's
// stomped-tables diagnostic, wherever the error is wrapped. ok is false for
// every error it cannot parse exactly.
//
// The engine emits the names newline- and tab-separated
// ("...stomped by merge:\n\tevents\n\t..."); the field report carried the
// single-table shape inline ("...stomped by merge: events\n Please commit").
// The segment is split ONLY on those separators: a token carrying internal
// whitespace is not a table name, and one ambiguous token declines the whole
// diagnostic rather than yielding a wrong diagnosis.
func ParseStompedTableNames(err error) ([]string, bool) {
	if err == nil {
		return nil, false
	}
	msg := err.Error()
	marker := strings.Index(msg, stompedTablesMarker)
	if marker < 0 {
		return nil, false
	}
	rest := msg[marker+len(stompedTablesMarker):]
	end := strings.Index(rest, stompedTablesTerminator)
	if end < 0 {
		return nil, false
	}

	var names []string
	seen := make(map[string]bool)
	for _, token := range strings.FieldsFunc(rest[:end], func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}) {
		name := strings.TrimSpace(token)
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, " \f\v") {
			return nil, false
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, false
	}
	sort.Strings(names)
	return names, true
}

// confirmIgnoredWorkingTables reports whether every table in tables is
// positively listed as an ignored working table on this session. It is the
// engine-side half of the classification: dolt_ignore exempts a table from
// staging, and dolt_status_ignored is the engine reporting that exemption in
// effect, so a name the diagnostic reports but this query does not confirm —
// an ordinary dirty tracked table, a name from a stale diagnostic — keeps the
// original error rather than a diagnosis. A failed or unreadable query is the
// same answer: evidence that is not positively there is not evidence.
func confirmIgnoredWorkingTables(ctx context.Context, db DBConn, tables []string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM dolt_status_ignored WHERE ignored")
	if err != nil {
		return false, fmt.Errorf("query dolt_status_ignored: %w", err)
	}
	defer func() { _ = rows.Close() }()

	present := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("scan dolt_status_ignored row: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate dolt_status_ignored: %w", err)
	}
	for _, table := range tables {
		if !present[table] {
			return false, nil
		}
	}
	return true, nil
}

// mergeRefContainedInHead reports whether ref — a freshly fetched remote
// tracking ref — names a commit HEAD already contains, which makes the merge
// the engine refused a content no-op.
//
// One read each, on the SAME session that refused the merge: the merge base of
// HEAD and the ref, and the ref's own hash. Both must resolve to a non-NULL,
// non-empty hash, and they must be equal (true for a strict ancestor and for
// an equal tip). Any read that fails, and any NULL or empty hash — an
// unresolvable ref, a session that cannot see the fetch — is not proof of
// containment, and only proof counts.
func mergeRefContainedInHead(ctx context.Context, db DBConn, ref string) (bool, error) {
	var mergeBase, refHash sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DOLT_MERGE_BASE('HEAD', ?)", ref).Scan(&mergeBase); err != nil {
		return false, fmt.Errorf("read merge base of HEAD and %s: %w", ref, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT DOLT_HASHOF(?)", ref).Scan(&refHash); err != nil {
		return false, fmt.Errorf("read hash of %s: %w", ref, err)
	}
	if !mergeBase.Valid || mergeBase.String == "" || !refHash.Valid || refHash.String == "" {
		return false, nil
	}
	return mergeBase.String == refHash.String, nil
}

// MergeNotOpen reads dolt_merge_status.is_merging and requires an explicit,
// successful observation that no merge is open. The refused merge this guards
// never opened (the engine stomps before it opens anything), so anything other
// than a clean false — a failed read, a missing system table, a missing row —
// means the session state is unknown and the refusal must not be reinterpreted.
// (mergeInProgress's missing-table/missing-row fallbacks answer "not merging"
// here; this is deliberately stricter.)
func MergeNotOpen(ctx context.Context, db DBConn) (bool, error) {
	var merging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		return false, fmt.Errorf("query merge status: %w", err)
	}
	return !merging, nil
}

// ClassifyIgnoredTablesRefusal is the classification half of the GH#6368
// triage, for pull wrappers that do not run the merge themselves (the shared
// server's post-transport handler): mergeErr is parsed for the engine's
// stomped-tables diagnostic and, when it parses, every named table must be
// positively confirmed as an ignored working table on db's session
// (dolt_status_ignored). confirmed is true only for that case, with the typed
// error carrying the (deduped, sorted) names and the original error; every
// other outcome — an unparseable diagnostic, a name the session does not
// confirm, evidence that fails to read — leaves the caller holding the
// original error.
//
// confirmed does NOT mean the pull is a no-op: the engine stomps the named
// tables before it ever compares histories, so containment must be proven
// separately before the refusal may be treated as one.
func ClassifyIgnoredTablesRefusal(ctx context.Context, db DBConn, mergeErr error) (typed *IgnoredTablesMergeError, confirmed bool) {
	tables, ok := ParseStompedTableNames(mergeErr)
	if !ok {
		return nil, false
	}
	confirmedIgnored, err := confirmIgnoredWorkingTables(ctx, db, tables)
	if err != nil || !confirmedIgnored {
		return nil, false
	}
	return &IgnoredTablesMergeError{Tables: tables, Err: mergeErr}, true
}

// resolveStompedIgnoredTables decides what the normal pull wrappers do with a
// merge error from MergeAndSettleWithStrategy (GH#6368):
//
//   - not the stomped-tables diagnostic, a name the session does not confirm
//     ignored, or any classification evidence that fails to read: the original
//     error, unchanged (handled false, so callers keep their framing).
//   - confirmed ignored tables whose fetched ref is PROVEN contained in HEAD
//     on the refusing session, with an explicit observation that no merge is
//     open: nil. The refusal guarded a content no-op, so the pull reports the
//     no-op success and the caller's successful-pull maintenance
//     (recompute, pending repair) runs as for any pull.
//   - confirmed ignored tables without that proof — divergence, an unreadable
//     ref, unknown session state: the typed IgnoredTablesMergeError, so `bd
//     sync` can report the operator-only outcome instead of a generic exit 1.
//
// db must be the same session the merge ran on: the ignore evidence and the
// hash reads are session state.
func resolveStompedIgnoredTables(ctx context.Context, db DBConn, ref string, mergeErr error) (outcome error, handled bool) {
	classified, confirmed := ClassifyIgnoredTablesRefusal(ctx, db, mergeErr)
	if !confirmed {
		return mergeErr, false
	}
	contained, cerr := mergeRefContainedInHead(ctx, db, ref)
	notOpen, merr := MergeNotOpen(ctx, db)
	if cerr == nil && merr == nil && contained && notOpen {
		return nil, true
	}
	return classified, true
}
