package dolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Deleter and the Sweeper commit their SQL transaction with the CALL
// DOLT_COMMIT inside their transaction body: that statement is their commit
// point. A response lost there may mean the deletion landed, so it must surface
// as ErrCommitIndeterminate, and withRetryTx must not replay a deletion that
// may already be durable.

func isDoltCommitCall(query string) bool {
	return strings.HasPrefix(strings.ToUpper(query), "CALL DOLT_COMMIT")
}

func isTransactionBegin(query string) bool {
	return strings.HasPrefix(strings.ToUpper(query), "START TRANSACTION")
}

func TestDeleterLostDoltCommitResponseIsIndeterminateAndNotReplayed(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-doomed", Title: "doomed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	proxy := startSQLWireProxy(t)
	via := openStoreOnPort(t, ctx, base, proxy.port())
	deleter, err := via.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}

	mark := proxy.mark()
	sprung := proxy.dropResponseToNext(isDoltCommitCall)
	_, err = deleter.Delete(ctx, issueops.DeleteRequest{Actor: "deleter", IDs: []string{"test-doomed"}, Force: true})
	select {
	case <-sprung:
	default:
		t.Fatalf("the delete never reached CALL DOLT_COMMIT (err = %v)", err)
	}
	if _, gerr := base.GetIssue(ctx, "test-doomed"); !errors.Is(gerr, storage.ErrNotFound) {
		t.Fatalf("read test-doomed after the lost response = %v, want ErrNotFound: the delete did not land, so the test exercised no lost response", gerr)
	}
	if err == nil {
		t.Fatal("a lost DOLT_COMMIT response reported success")
	}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("delete error = %v, want ErrCommitIndeterminate: the deletion may have landed", err)
	}
	if n := proxy.statementsSince(mark, isTransactionBegin); n != 1 {
		t.Fatalf("the delete began %d transactions, want 1: a deletion that may have landed was replayed", n)
	}
}

func TestSweeperLostDoltCommitResponseIsIndeterminate(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-swept", Title: "swept", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	proxy := startSQLWireProxy(t)
	via := openStoreOnPort(t, ctx, base, proxy.port())
	sweeper, err := via.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}

	sprung := proxy.dropResponseToNext(isDoltCommitCall)
	_, err = sweeper.Sweep(ctx, issueops.SweepRequest{Actor: "sweeper", Tier: issueops.SweepDurable, IDPattern: "test-swept"})
	select {
	case <-sprung:
	default:
		t.Fatalf("the sweep never reached CALL DOLT_COMMIT (err = %v)", err)
	}
	if _, gerr := base.GetIssue(ctx, "test-swept"); !errors.Is(gerr, storage.ErrNotFound) {
		t.Fatalf("read test-swept after the lost response = %v, want ErrNotFound: the sweep did not land, so the test exercised no lost response", gerr)
	}
	if err == nil {
		t.Fatal("a lost DOLT_COMMIT response reported success")
	}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("sweep error = %v, want ErrCommitIndeterminate: the sweep may have landed", err)
	}
}
