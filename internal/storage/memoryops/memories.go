// Package memoryops holds the transaction-level body of
// memoryops.Memories: the kv.memory. encoding and the four operations, each
// written so that the existence PROBE and the act it qualifies happen inside
// ONE transaction.
//
// It lives here rather than in internal/workapi/store<role>/ for the reason
// internal/storage/issueops' relations.go and edges.go do: this role's writes
// are "read the row, then write over it and report what was there", and
// storage.DoltStorage publishes methods rather than transactions, so a body
// written against it could not scope the two calls together. The shipped
// commands did exactly that — GetConfig, then SetConfig — and papered over the
// gap with `existing, _ :=`, which makes "Remembered" versus "Updated" and the
// value `bd forget` prints hopeful rather than true.
//
// There is no exported constructor and no exported body type, so there is
// nothing for a depguard entry to deny: every function here takes a *sql.Tx,
// which no front door holds. That is why the cmd-bd-role-constructors list in
// .golangci.yml gains no entry for this role.
//
// TWO THINGS THIS PACKAGE DELIBERATELY DOES NOT DO:
//
//   - NO CACHE WORK. DoltStore's config-adjacent caches key on status.custom,
//     types.custom and types.infra only (dolt/config.go), and no kv.* key feeds
//     one, so these raw-row writes leave nothing stale behind them. Stated so
//     the next reader does not have to re-derive it from the cache code.
//   - NO DOLT COMMIT. The store wrappers run these under the same runners the
//     legacy SetConfig/DeleteConfig calls used (withRetryTx on the server-backed
//     store, withConn(write) on the embedded one), and durability on that route
//     stays the CLI's auto-commit epilogue. The unit-of-work route keeps its
//     per-write commit. The two routes' Dolt history shapes differ today and
//     continue to.
package memoryops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
)

// StorageKey encodes a user key as the config-table key it is stored under.
//
// IT IS THE ONE ENCODE IN THE TREE, and the strip below is the one decode. Both
// implementations of the role reach them — the store-backed InTx functions in
// this file and the unit-of-work body, which composes the config use case
// instead of these functions but must agree with them byte for byte or the two
// routes stop seeing each other's memories. A second copy of
// `kvkeys.Prefix + kvkeys.MemoryPrefix + key` is the drift kvkeys was created to
// end.
func StorageKey(userKey string) string {
	return kvkeys.MemoryConfigKeyPrefix + userKey
}

// MemoriesFromConfig narrows a whole config map to the memory plane, stripping
// the prefix so the keys are the ones the caller named.
//
// THE PREFIX IS THE FULL "kv.memory.", not "kv.". The config table holds
// settings rows, generic `bd kv` rows and memory rows at once, and the two
// mistakes this function exists to not make are filtering on the shorter prefix
// — which serves every `bd kv set` value as a memory — and trimming a different
// number of bytes than it matched, which re-keys every answer. Both are silent:
// they produce a plausible map.
func MemoriesFromConfig(all map[string]string) map[string]string {
	memories := make(map[string]string)
	for key, value := range all {
		if userKey, ok := strings.CutPrefix(key, kvkeys.MemoryConfigKeyPrefix); ok {
			memories[userKey] = value
		}
	}
	return memories
}

// RememberInTx stores one memory and reports whether it replaced one, both in
// the caller's transaction.
//
// The probe is not tolerant: a read that fails fails the write. That is
// strictly better than the shipped `existing, _ :=`, because a database that
// cannot be read is a database the write was about to fail against anyway — and
// the alternative is reporting "Remembered" over a value the caller has just
// destroyed.
//
// replaced is about the ROW, not the value: a memory stored as the empty string
// reports true even though a Recall of it would report Found false. See
// memoryops.RememberResult.Replaced, which states that divergence rather than
// smoothing it over.
func RememberInTx(ctx context.Context, tx *sql.Tx, key, content string) (replaced bool, err error) {
	storageKey := StorageKey(key)
	replaced, err = configRowExistsInTx(ctx, tx, storageKey)
	if err != nil {
		return false, err
	}
	if err := issueops.SetConfigInTx(ctx, tx, storageKey, content); err != nil {
		return false, err
	}
	return replaced, nil
}

// RecallInTx reads one memory in the caller's transaction. A key nothing stored
// answers "" with a nil error: the seam cannot tell that from a row stored
// empty, and the role's Found field is where that conflation is stated.
func RecallInTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	return issueops.GetConfigInTx(ctx, tx, StorageKey(key))
}

// ForgetInTx removes one memory and returns what it held, both in the caller's
// transaction — which is what makes the returned value the one that was
// actually deleted rather than the one an earlier read happened to see.
//
// found is Recall's found, not the row's: a memory stored as the empty string
// reports found false and IS NOT DELETED. That keeps Forget's answer and
// Recall's answer the same statement, which is what a front door printing
// "No memory with key ..." after a successful Recall would otherwise contradict.
//
// The delete names the one encoded key. There is no LIKE and no prefix sweep in
// it, so forgetting "a" cannot take "a-b" with it.
func ForgetInTx(ctx context.Context, tx *sql.Tx, key string) (previous string, found bool, err error) {
	storageKey := StorageKey(key)
	previous, err = issueops.GetConfigInTx(ctx, tx, storageKey)
	if err != nil {
		return "", false, err
	}
	if previous == "" {
		return "", false, nil
	}
	if err := issueops.DeleteConfigInTx(ctx, tx, storageKey); err != nil {
		return "", false, err
	}
	return previous, true, nil
}

// ListInTx reads the whole memory plane in the caller's transaction, keyed by
// user key.
//
// It reads the whole config table and narrows in Go rather than issuing a LIKE.
// The plane is tens of rows inside a table of tens of rows, so the query would
// buy nothing; what it would cost is a second definition of which rows are
// memories, expressed in a pattern language where `_` is a wildcard and
// "kv.memory." contains one.
func ListInTx(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	all, err := issueops.GetAllConfigInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return MemoriesFromConfig(all), nil
}

// configRowExistsInTx reports whether the row is there, regardless of what it
// holds.
//
// issueops.GetConfigInTx cannot answer this: it maps a missing row to "" and is
// right to, because every other caller wants a value. Replaced is a statement
// about the ROW, so this role needs the distinction that helper deliberately
// drops — hence the one query in this package that is not composed from the
// shared config helpers.
func configRowExistsInTx(ctx context.Context, tx *sql.Tx, storageKey string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM config WHERE `key` = ?", storageKey).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe memory row %s: %w", storageKey, err)
	}
	return true, nil
}
