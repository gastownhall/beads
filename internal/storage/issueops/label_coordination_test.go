package issueops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLabelCoordinationKeyShape(t *testing.T) {
	t.Parallel()

	const issue = "bd-label-coordination"
	durable := labelCoordinationKey(issue, labelCoordinationDurableTier)
	wisp := labelCoordinationKey(issue, labelCoordinationEphemeralTier)
	if durable == wisp {
		t.Fatalf("tier keys collide: %q", durable)
	}
	if got := labelCoordinationKey(issue, labelCoordinationDurableTier); got != durable {
		t.Fatalf("key is not deterministic: %q then %q", durable, got)
	}
	if !strings.HasPrefix(durable, "label-coordination/v1/labels/") || !strings.HasPrefix(wisp, "label-coordination/v1/wisp_labels/") {
		t.Fatalf("keys = %q, %q, want the label-coordination/v1/<table>/ namespace", durable, wisp)
	}
	if strings.Contains(durable, issue) {
		t.Fatalf("key exposes the issue ID: %q", durable)
	}
	if labelCoordinationShardCount != 4096 {
		t.Fatalf("shard count = %d, want 4096", labelCoordinationShardCount)
	}
	seen := make(map[uint16]struct{})
	for i := 0; i < 3*labelCoordinationShardCount; i++ {
		shard := labelCoordinationShard(fmt.Sprintf("issue-%d", i))
		if shard >= labelCoordinationShardCount {
			t.Fatalf("shard = %d, want < %d", shard, labelCoordinationShardCount)
		}
		seen[shard] = struct{}{}
	}
	if len(seen) > labelCoordinationShardCount {
		t.Fatalf("distinct shards = %d, want <= %d", len(seen), labelCoordinationShardCount)
	}
}

func TestTouchLabelCoordinationInTxRoutesByTableAndRejectsBadInput(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const issue = "coordination-issue"
	query := regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)")
	mock.ExpectExec(query).
		WithArgs(labelCoordinationKey(issue, "wisp_labels"), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	ctx := context.Background()
	if err := TouchLabelCoordinationInTx(ctx, tx, issue, "wisp_labels"); err != nil {
		t.Fatalf("TouchLabelCoordinationInTx(wisp_labels): %v", err)
	}
	if err := TouchLabelCoordinationInTx(ctx, tx, "", "labels"); err == nil {
		t.Fatal("empty issue ID accepted")
	}
	if err := TouchLabelCoordinationInTx(ctx, tx, issue, "dependencies"); err == nil {
		t.Fatal("unsupported table accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("coordination touch expectations: %v", err)
	}
}
