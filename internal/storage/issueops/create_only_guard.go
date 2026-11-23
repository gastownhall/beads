package issueops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// EnsureIssueIDAvailableInTx serializes same-shard creates and rejects
// occupied and retired IDs.
//
// After the shard coordination write it reads the id's occupancy in ONE
// statement across both row tables and both tombstone tables (see
// probeIDOccupancyInTx): a live row in either plane is ErrAlreadyExists, and an
// id a retaining sweep retired, with no live row, is
// *publicops.IdempotencyExpiredError. A live row wins when both are present.
func EnsureIssueIDAvailableInTx(ctx context.Context, tx DBTX, id string) error {
	if tx == nil {
		return fmt.Errorf("ensure issue ID available: transaction is nil")
	}
	if id == "" {
		return fmt.Errorf("ensure issue ID available: ID is empty")
	}
	if err := CoordinateIDCreatesInTx(ctx, tx, []string{id}); err != nil {
		return err
	}
	return checkIssueIDAvailableInTx(ctx, tx, id)
}

// checkIssueIDAvailableInTx is EnsureIssueIDAvailableInTx without the
// coordination write, for a caller that already holds id's cell (a batch that
// coordinated every id it names up front, newCreateBatchCache).
func checkIssueIDAvailableInTx(ctx context.Context, tx DBTX, id string) error {
	live, retired, err := probeIDOccupancyInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("check issue %q: %w", id, err)
	}
	if live {
		return fmt.Errorf("%w: %s", storage.ErrAlreadyExists, id)
	}
	if retired != nil {
		return idempotencyExpired(retired)
	}
	return nil
}

// CoordinateIDCreatesInTx writes the create coordination cell of every id's
// shard (local_metadata, 4096 shards), each shard once, in sorted key order,
// in one statement per queryBatchSize shards. Empty ids are ignored.
//
// It is the lock that orders a guarded create of an id against a retaining
// sweep of that id. Every path that decides "may this id be written" takes it
// BEFORE it reads the id's occupancy: the create-only guard
// (EnsureIssueIDAvailableInTx), the upsert-shaped creates and imports
// (prepareCreateIssueInTx, newCreateBatchCache, RefuseRetiredIDInTx). A
// retaining sweep takes it for every id it retires BEFORE it deletes
// (sweepRetiredIDsInTx, the domain delete's Retire path). So:
//
//   - On a READ COMMITTED backend (Postgres' upsert and import doors), a
//     create that comes second waits on the cell until the sweep commits, and
//     its occupancy read, a later statement, sees the tombstone. Without the
//     cell the read could pass before the sweep commits and the upsert that
//     follows would insert a fresh row next to the new tombstone.
//   - A sweep that comes second meets a cell the create wrote after the
//     sweep's snapshot: a serialization failure on Postgres' SERIALIZABLE
//     envelope, a write-write conflict on Dolt, either way a retry that sees
//     the created row.
//
// Sorted order means two callers that each take ALL their cells in one call
// cannot deadlock each other. A transaction that calls it more than once, with
// cells it did not already take, can: it may hold a later shard while it waits
// on an earlier one that a sorted locker holds while waiting on the later.
// So a multi-id writer takes every id it already knows in one call before it
// writes any of them (newCreateBatchCache, ApplyBatchInTx, a retaining sweep);
// what remains is a generated id, minted mid-transaction, taking its own cell
// afterwards. Postgres breaks such a cycle with 40P01 after deadlock_timeout,
// which the SERIALIZABLE envelopes and the importer retry; Dolt has no lock
// wait at all, only commit-time conflicts.
func CoordinateIDCreatesInTx(ctx context.Context, tx DBTX, ids []string) error {
	shards := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			shards[issueCreateCoordinationKey(id)] = true
		}
	}
	if len(shards) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(shards))
	stamp := strconv.FormatInt(FreshRowLock(), 10)
	for start := 0; start < len(keys); start += queryBatchSize {
		chunk := keys[start:min(start+queryBatchSize, len(keys))]
		args := make([]any, 0, 2*len(chunk))
		for _, key := range chunk {
			args = append(args, key, stamp)
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?), ", len(chunk)), ", ")
		if _, err := tx.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES "+values, args...); err != nil {
			return fmt.Errorf("coordinate issue create: %w", err)
		}
	}
	return nil
}

func issueCreateCoordinationKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	shard := uint16(sum[0])<<4 | uint16(sum[1])>>4
	return fmt.Sprintf("issue-create/v1/%03x", shard)
}
