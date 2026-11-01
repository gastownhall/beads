package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// relationLoaderFailStore is a DoltStorage stand-in whose bulk relation
// loaders succeed except the one named by fail, which returns errInjected.
type relationLoaderFailStore struct {
	storage.DoltStorage
	fail string
}

var errRelationLoaderInjected = errors.New("injected loader failure")

func (s *relationLoaderFailStore) GetLabelsForIssues(context.Context, []string) (map[string][]string, error) {
	if s.fail == "labels" {
		return nil, errRelationLoaderInjected
	}
	return map[string][]string{}, nil
}

func (s *relationLoaderFailStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	if s.fail == "deps" {
		return nil, errRelationLoaderInjected
	}
	return map[string][]*types.Dependency{}, nil
}

func (s *relationLoaderFailStore) GetCommentsForIssues(context.Context, []string) (map[string][]*types.Comment, error) {
	if s.fail == "comments" {
		return nil, errRelationLoaderInjected
	}
	return map[string][]*types.Comment{}, nil
}

func (s *relationLoaderFailStore) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	if s.fail == "commentCounts" {
		return nil, errRelationLoaderInjected
	}
	return map[string]int{}, nil
}

func (s *relationLoaderFailStore) GetDependencyCounts(context.Context, []string) (map[string]*types.DependencyCounts, error) {
	if s.fail == "depCounts" {
		return nil, errRelationLoaderInjected
	}
	return map[string]*types.DependencyCounts{}, nil
}

// TestStoreExportSourceLoadRelationsPropagatesLoaderError is the bd-8kjh5f
// (GH #6870) repro: a failing bulk relation loader in the classic export
// path must fail the export instead of publishing records with that
// relation silently empty.
func TestStoreExportSourceLoadRelationsPropagatesLoaderError(t *testing.T) {
	saved := store
	t.Cleanup(func() { store = saved })

	issues := []*types.Issue{{ID: "bd-1"}}
	for _, loader := range []string{"labels", "deps", "comments", "commentCounts", "depCounts"} {
		t.Run(loader, func(t *testing.T) {
			store = &relationLoaderFailStore{fail: loader}
			_, err := storeExportSource{}.LoadExportRelations(context.Background(), issues)
			if !errors.Is(err, errRelationLoaderInjected) {
				t.Fatalf("LoadExportRelations with failing %s loader: err = %v, want %v", loader, err, errRelationLoaderInjected)
			}
		})
	}
}
