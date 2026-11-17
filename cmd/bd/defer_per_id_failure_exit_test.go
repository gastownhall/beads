package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// deferPerIDFailureExitCode is the exit code bd defer and bd undefer give
// TODAY on the direct route when one id's write fails and the other ids still
// run. Native fix C1 (bdp-cli-conflict-20261107-fixes-c-exit-flags) makes it
// 1; until that ruling it is a pin, and the PR that lands C1 changes this
// line. The proxied route's twin is proxied_lookup_failure_test.go's exitsZero.
const deferPerIDFailureExitCode = 0

// deferPerIDFailureStore fails the write for one id, which no real store does
// on demand. The embedded nil DoltStorage makes any other call panic, so a
// command that wanders off the path under test fails loudly.
type deferPerIDFailureStore struct {
	storage.DoltStorage
	issues  map[string]*types.Issue
	failID  string
	written []string
}

func (s *deferPerIDFailureStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range filter.IDs {
		if issue, ok := s.issues[id]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (s *deferPerIDFailureStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return s.issues[id], nil
}

func (s *deferPerIDFailureStore) UpdateIssue(_ context.Context, id string, _ map[string]interface{}, _ string) error {
	if id == s.failID {
		return errors.New("the write failed")
	}
	s.written = append(s.written, id)
	return nil
}

// IssueLifecycle answers for the same write, so the pin holds whichever way
// the commands reach the store: native fix B8 moves them onto the role.
func (s *deferPerIDFailureStore) IssueLifecycle() (issueops.Lifecycle, error) {
	return deferPerIDFailureLifecycle{store: s}, nil
}

type deferPerIDFailureLifecycle struct {
	issueops.Lifecycle
	store *deferPerIDFailureStore
}

func (l deferPerIDFailureLifecycle) Update(ctx context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	if err := l.store.UpdateIssue(ctx, req.IssueID, nil, req.Actor); err != nil {
		return issueops.UpdateResult{}, err
	}
	return issueops.UpdateResult{Issue: l.store.issues[req.IssueID]}, nil
}

// TestDeferAndUndeferPerIDWriteFailureExitCode pins the direct route at the
// command's seam: one id's write fails, it is reported, the other id still
// runs, and the command exits deferPerIDFailureExitCode.
func TestDeferAndUndeferPerIDWriteFailureExitCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    func() error
		status types.Status
		says   string
	}{
		{
			name:   "defer",
			run:    func() error { return deferCmd.RunE(deferCmd, []string{"t-bad", "t-good"}) },
			status: types.StatusOpen,
			says:   "Error deferring t-bad: the write failed",
		},
		{
			name:   "undefer",
			run:    func() error { return undeferCmd.RunE(undeferCmd, []string{"t-bad", "t-good"}) },
			status: types.StatusDeferred,
			says:   "Error undeferring t-bad: the write failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &deferPerIDFailureStore{failID: "t-bad", issues: map[string]*types.Issue{
				"t-bad":  {ID: "t-bad", Status: tc.status},
				"t-good": {ID: "t-good", Status: tc.status},
			}}
			oldStore, oldDidWrite := store, commandDidWrite.Load()
			store = stub
			t.Cleanup(func() {
				store = oldStore
				commandDidWrite.Store(oldDidWrite)
			})

			var err error
			stderr := captureStderrDuring(t, func() { err = tc.run() })

			code := 0
			if err != nil {
				code = 1 // what main does with an error that names no code
				if c, ok := exitCodeFromError(err); ok {
					code = c
				}
			}
			if code != deferPerIDFailureExitCode {
				t.Errorf("bd %s with one id's write failing: exit %d (err %v), want %d", tc.name, code, err, deferPerIDFailureExitCode)
			}
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("stderr = %q, want %q", stderr, tc.says)
			}
			if !slices.Equal(stub.written, []string{"t-good"}) {
				t.Errorf("wrote %v, want the other id to have run", stub.written)
			}
		})
	}
}
