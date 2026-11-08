package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// GH#6368: a table that is dolt_ignore'd locally but still tracked on the
// remote (events after migration 0062, or any table that was pushed before its
// ignore rule landed) makes DOLT_MERGE refuse with "local changes would be
// stomped by merge" even though `dolt status`, which honors dolt_ignore,
// reports a clean working set. The engine runs that stomp check BEFORE it asks
// whether the incoming commit is already contained in HEAD, so a remote that
// is merely behind this clone — nothing to merge at all — is refused too, and
// bd sync can never reach the push that would bring the remote level.
//
// What lives here is the pull-side handling of that one refusal: recognize it
// from the engine's own bounded diagnostic, prove a no-op from the store's own
// SQL (merge base and open-merge state), and otherwise classify it against the
// engine's own ignore evidence (dolt_status_ignored). It deliberately never
// stages, deletes, or overwrites the ignored working table, and never merges
// divergent history: absorbing pre-flip history is driver-owned (see
// internal/storage/embeddeddolt/merge_events_flip_test.go). Only the normal
// pull wrappers use it; MergeAndSettle keeps returning the raw refusal.

// stompRefusalPrefix and stompRefusalTerminator bound the engine's refusal
// (dolt_merge.go in the pinned Dolt): "error: local changes would be stomped
// by merge:\n\t<table>\n\t<table>\n Please commit your changes before you
// merge." Reports also show the single-table inline form "...merge: events\n
// Please commit...".
const (
	stompRefusalPrefix     = "local changes would be stomped by merge:"
	stompRefusalTerminator = "Please commit your changes before you merge."
)

// StompedTables extracts the table names from the engine's stomp refusal
// inside err. ok is false when err carries no such refusal, carries it more
// than once, has no terminator, or names no table: an ambiguous diagnostic is
// never guessed at. Names are returned deduplicated and sorted.
//
// Only the bounded span between the prefix and the terminator is read, one
// name per line, so unrelated text a CLI wrapper adds around the diagnostic
// never becomes a table name.
func StompedTables(err error) (tables []string, ok bool) {
	if err == nil {
		return nil, false
	}
	msg := err.Error()
	if strings.Count(msg, stompRefusalPrefix) != 1 {
		return nil, false
	}
	_, rest, _ := strings.Cut(msg, stompRefusalPrefix)
	body, _, found := strings.Cut(rest, stompRefusalTerminator)
	if !found {
		return nil, false
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(body, "\n") {
		name := strings.Trim(line, " \t\r")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tables = append(tables, name)
	}
	if len(tables) == 0 {
		return nil, false
	}
	sort.Strings(tables)
	return tables, true
}

// ProveMergeNoop reports whether merging ref into the session's HEAD would
// merge nothing: ref is contained in HEAD (their merge base IS ref's commit)
// and no merge is open on the session. It answers true only on complete,
// positive evidence — a NULL or empty hash, a missing ref, an unreadable or
// absent merge state is an error or false, never a no-op.
//
// db must be the single session the pull merges on: HEAD and merge state are
// session (branch) state.
func ProveMergeNoop(ctx context.Context, db DBConn, ref string) (bool, error) {
	var base, tip sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DOLT_MERGE_BASE('HEAD', ?), DOLT_HASHOF(?)", ref, ref).Scan(&base, &tip); err != nil {
		return false, fmt.Errorf("read merge base of HEAD and %s: %w", ref, err)
	}
	if !base.Valid || !tip.Valid || base.String == "" || base.String != tip.String {
		return false, nil
	}
	// Explicit observation only: mergeInProgress's missing-table/no-row
	// fallback to false is right for a diagnosis, wrong for a success decision.
	var merging sql.NullBool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		return false, fmt.Errorf("read merge status: %w", err)
	}
	if !merging.Valid || merging.Bool {
		return false, nil
	}
	return true, nil
}

// settleIgnoredTableStomp is the normal pull wrappers' handling of a merge
// error: when err is the engine's stomp refusal and the merge of ref is a
// proven no-op, the pull succeeded with nothing to merge and nil is returned;
// otherwise err is returned. db is the pinned session the merge ran on.
func settleIgnoredTableStomp(ctx context.Context, db DBConn, ref string, err error) error {
	if _, ok := StompedTables(err); !ok {
		return err
	}
	if noop, proofErr := ProveMergeNoop(ctx, db, ref); proofErr == nil && noop {
		return nil
	}
	return err
}
