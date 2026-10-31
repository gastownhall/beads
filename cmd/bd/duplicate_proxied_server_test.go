package main

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// markWrites counts what a proxied duplicate or supersede wrote.
type markWrites struct{ edges, closes, commits int }

// markIssueUC resolves every id to itself and finds every target.
type markIssueUC struct {
	domain.IssueUseCase // any other method is a nil call
	w                   *markWrites
}

func (s markIssueUC) SearchIssues(_ context.Context, _ string, f types.IssueFilter) (domain.SearchPage, error) {
	return domain.SearchPage{Items: []*types.Issue{{ID: f.IDs[0]}}}, nil
}

func (s markIssueUC) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return &types.Issue{ID: id}, nil
}

func (s markIssueUC) CloseIssue(context.Context, string, domain.CloseIssueParams, string) (domain.CloseIssueResult, error) {
	s.w.closes++
	return domain.CloseIssueResult{}, nil
}

type markDepUC struct {
	domain.DependencyUseCase
	w *markWrites
}

func (s markDepUC) AddDependency(context.Context, *types.Dependency, string) error {
	s.w.edges++
	return nil
}

type markUOW struct {
	uow.UnitOfWork
	w *markWrites
}

func (u markUOW) Close(context.Context)                       {}
func (u markUOW) Commit(context.Context, string) error        { u.w.commits++; return nil }
func (u markUOW) IssueUseCase() domain.IssueUseCase           { return markIssueUC{w: u.w} }
func (u markUOW) DependencyUseCase() domain.DependencyUseCase { return markDepUC{w: u.w} }

type markProvider struct{ w *markWrites }

func (p markProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return markUOW{w: p.w}, nil }
func (p markProvider) Close(context.Context) error                    { return nil }

// TestProxiedMarkRefusesSelfBeforeWriting pins the self check in the proxied
// duplicate and supersede: an issue marked against itself is refused before
// the edge or the close is written. The distinct-ids case shows the stub path
// writes when the check does not refuse.
func TestProxiedMarkRefusesSelfBeforeWriting(t *testing.T) {
	cases := []struct {
		name    string
		target  *string
		run     func(*cobra.Command, []string) error
		wantErr string
	}{
		{"duplicate", &duplicateOf, runDuplicateProxiedServer, "cannot mark an issue as duplicate of itself"},
		{"supersede", &supersededWith, runSupersedeProxiedServer, "cannot mark an issue as superseded by itself"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldProvider, oldTarget, oldCtx, oldJSON, oldWrote := uowProvider, *tc.target, rootCtx, jsonOutput, commandDidWrite.Load()
			t.Cleanup(func() {
				uowProvider, *tc.target, rootCtx, jsonOutput = oldProvider, oldTarget, oldCtx, oldJSON
				commandDidWrite.Store(oldWrote)
			})
			rootCtx, jsonOutput = context.Background(), false

			var w markWrites
			uowProvider = markProvider{w: &w}
			*tc.target = "bd-b"
			captureStdout(t, func() error { return tc.run(&cobra.Command{}, []string{"bd-a"}) })
			if w != (markWrites{edges: 1, closes: 1, commits: 1}) {
				t.Fatalf("distinct ids: writes = %+v, want one edge, one close, one commit", w)
			}

			w = markWrites{}
			*tc.target = "bd-a"
			var err error
			stderr := captureStderr(t, func() { err = tc.run(&cobra.Command{}, []string{"bd-a"}) })
			if err == nil || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("self: err = %v, stderr = %q, want a refusal saying %q", err, stderr, tc.wantErr)
			}
			if w != (markWrites{}) {
				t.Fatalf("self: writes = %+v, want none", w)
			}
		})
	}
}
