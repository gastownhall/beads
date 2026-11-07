package main

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// replayCreateState drives one proxied create through a lost commit-time
// conflict. Its create mints the next ID into the caller's issue when the
// issue carries none, as the domain create does, and records the ID each
// attempt was handed. Its first commit fails with the serialization error
// Dolt's commit-time merge raises, so uow.RunTxResult rolls that attempt back
// and replays the body.
type replayCreateState struct {
	conflicts int
	minted    int
	handed    []string
}

type replayCreateIssues struct {
	domain.IssueUseCase // any other method is a nil call: these routes reach only CreateIssue
	s                   *replayCreateState
}

func (u replayCreateIssues) CreateIssue(_ context.Context, params domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	u.s.handed = append(u.s.handed, params.Issue.ID)
	if params.Issue.ID == "" {
		u.s.minted++
		params.Issue.ID = fmt.Sprintf("bd-%d", u.s.minted)
	}
	return domain.CreateIssueResult{Issue: params.Issue}, nil
}

type replayCreateUOW struct {
	uow.UnitOfWork
	s *replayCreateState
}

func (u replayCreateUOW) Close(context.Context)             {}
func (u replayCreateUOW) IssueUseCase() domain.IssueUseCase { return replayCreateIssues{s: u.s} }
func (u replayCreateUOW) Commit(context.Context, string) error {
	if u.s.conflicts > 0 {
		u.s.conflicts--
		return &mysql.MySQLError{Number: 1213, Message: "serialization failure"}
	}
	return nil
}

type replayCreateProvider struct{ s *replayCreateState }

func (p replayCreateProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return replayCreateUOW{s: p.s}, nil
}

func (p replayCreateProvider) Close(context.Context) error { return nil }

// withReplayCreateProvider points the proxied routes, for one test, at a
// provider whose first commit loses a write conflict.
func withReplayCreateProvider(t *testing.T) *replayCreateState {
	t.Helper()
	state := &replayCreateState{conflicts: 1}
	oldProvider, oldJSON, oldWrote := uowProvider, jsonOutput, commandDidWrite.Load()
	uowProvider = replayCreateProvider{s: state}
	jsonOutput = false
	// The actor and the owner resolve from these before they would run git.
	t.Setenv("BEADS_ACTOR", "replay-tester")
	t.Setenv("GIT_AUTHOR_EMAIL", "replay-tester@example.test")
	t.Cleanup(func() {
		uowProvider = oldProvider
		jsonOutput = oldJSON
		commandDidWrite.Store(oldWrote)
	})
	return state
}

// assertReplayStartedFromTheRequest: both attempts were handed the issue
// without an ID. A replay handed the rolled-back attempt's bd-1 is the bug: the
// domain create reads a leftover ID, absent a parent, as one the caller named
// and, in counter mode, upserts over the issue a concurrent writer committed
// under that number.
func assertReplayStartedFromTheRequest(t *testing.T, state *replayCreateState) {
	t.Helper()
	if want := []string{"", ""}; !reflect.DeepEqual(state.handed, want) {
		t.Fatalf("the create was handed IDs %q, want %q: the replay re-used the rolled-back attempt's ID", state.handed, want)
	}
}

func TestTodoAddProxiedReplayStartsFromTheRequest(t *testing.T) {
	state := withReplayCreateProvider(t)
	cmd := &cobra.Command{}
	cmd.Flags().Int("priority", 2, "")
	cmd.Flags().String("description", "", "")
	if err := runTodoAddProxiedServer(cmd, context.Background(), []string{"write", "the", "tests"}); err != nil {
		t.Fatalf("todo add: %v", err)
	}
	assertReplayStartedFromTheRequest(t, state)
}

func TestQuickProxiedReplayStartsFromTheRequest(t *testing.T) {
	state := withReplayCreateProvider(t)
	cmd := &cobra.Command{}
	cmd.Flags().String("priority", "2", "")
	cmd.Flags().String("type", "task", "")
	cmd.Flags().StringSlice("labels", nil, "")
	cmd.Flags().String("parent", "", "")
	if err := runQuickProxiedServer(cmd, context.Background(), []string{"quick", "capture"}); err != nil {
		t.Fatalf("q: %v", err)
	}
	assertReplayStartedFromTheRequest(t, state)
}
