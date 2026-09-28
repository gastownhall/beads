package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type failingExportRelationStore struct {
	storage.DoltStorage
	fail string
}

func (s *failingExportRelationStore) GetInfraTypes(context.Context) map[string]bool { return nil }
func (s *failingExportRelationStore) GetConfig(context.Context, string) (string, error) {
	return "", nil
}
func (s *failingExportRelationStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return []*types.Issue{{ID: "bd-export1", Title: "export me"}}, nil
}
func (s *failingExportRelationStore) relationResult(name string) error {
	if s.fail == name {
		return errors.New("injected " + name + " failure")
	}
	return nil
}
func (s *failingExportRelationStore) GetLabelsForIssues(context.Context, []string) (map[string][]string, error) {
	return nil, s.relationResult("labels")
}
func (s *failingExportRelationStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, s.relationResult("dependencies")
}
func (s *failingExportRelationStore) GetCommentsForIssues(context.Context, []string) (map[string][]*types.Comment, error) {
	return nil, s.relationResult("comments")
}
func (s *failingExportRelationStore) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	return nil, s.relationResult("comment counts")
}
func (s *failingExportRelationStore) GetDependencyCounts(context.Context, []string) (map[string]*types.DependencyCounts, error) {
	return nil, s.relationResult("dependency counts")
}

func TestClassicExportPathsRejectRelationLoaderFailures(t *testing.T) {
	loaders := []string{"labels", "dependencies", "comments", "comment counts", "dependency counts"}
	for _, loader := range loaders {
		loader := loader
		t.Run(loader, func(t *testing.T) {
			oldStore := store
			store = &failingExportRelationStore{fail: loader}
			t.Cleanup(func() { store = oldStore })

			issues := func() []*types.Issue {
				return []*types.Issue{{ID: "bd-export1", Title: "export me"}}
			}
			if _, err := (storeExportSource{}).LoadExportRelations(t.Context(), issues()); err == nil || !strings.Contains(err.Error(), loader) {
				t.Fatalf("store source error = %v, want %q loader failure", err, loader)
			}
			if _, err := encodeIssueRecords(t.Context(), issues()); err == nil || !strings.Contains(err.Error(), loader) {
				t.Fatalf("incremental encode error = %v, want %q loader failure", err, loader)
			}
			out := filepath.Join(t.TempDir(), "issues.jsonl")
			if _, _, err := exportToFile(t.Context(), out, false); err == nil || !strings.Contains(err.Error(), loader) {
				t.Fatalf("full auto-export error = %v, want %q loader failure", err, loader)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("failed export published %q: stat error = %v", out, err)
			}
		})
	}
}
