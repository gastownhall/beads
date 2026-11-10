package issueops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDependencyCoordinationKeyShardProperties(t *testing.T) {
	t.Parallel()

	const parent = "bd-sensitive-parent-id"
	durable := dependencyCoordinationKey(parent, dependencyCoordinationDurableTier)
	if got := dependencyCoordinationKey(parent, dependencyCoordinationDurableTier); got != durable {
		t.Fatalf("durable key is not deterministic: first %q, second %q", durable, got)
	}
	wisp := dependencyCoordinationKey(parent, dependencyCoordinationEphemeralTier)
	if durable == wisp {
		t.Fatalf("tier keys collide: %q", durable)
	}
	for _, key := range []string{durable, wisp} {
		if len(key) >= 255 {
			t.Fatalf("key length = %d, want < 255: %q", len(key), key)
		}
		if strings.Contains(key, parent) {
			t.Fatalf("key exposes parent ID: %q", key)
		}
	}
	if dependencyCoordinationShardCount != 4096 {
		t.Fatalf("shard count = %d, want 4096", dependencyCoordinationShardCount)
	}
	seen := make(map[uint16]struct{})
	for i := 0; i < 8192; i++ {
		shard := dependencyCoordinationShard(fmt.Sprintf("parent-%d", i))
		if shard >= dependencyCoordinationShardCount {
			t.Fatalf("shard = %d, want < %d", shard, dependencyCoordinationShardCount)
		}
		seen[shard] = struct{}{}
	}
	if len(seen) > dependencyCoordinationShardCount {
		t.Fatalf("distinct shards = %d, want <= %d", len(seen), dependencyCoordinationShardCount)
	}
}

func TestTouchDependencyCoordinationInTxTouchesTiersInOrder(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const parent = "coordination-parent"
	query := regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)")
	mock.ExpectExec(query).
		WithArgs(dependencyCoordinationKey(parent, dependencyCoordinationDurableTier), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(query).
		WithArgs(dependencyCoordinationKey(parent, dependencyCoordinationEphemeralTier), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := touchDependencyCoordinationInTx(context.Background(), tx, parent); err != nil {
		t.Fatalf("touchDependencyCoordinationInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("coordination touch expectations: %v", err)
	}
}
