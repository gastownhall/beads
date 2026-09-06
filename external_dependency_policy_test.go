package beads_test

import (
	"context"
	"errors"
	"testing"

	beads "github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
)

type externalPolicyStore struct {
	storage.DoltStorage
	ready  []*beads.Issue
	deps   map[string][]*beads.Dependency
	labels map[string][]*beads.Issue
	closed bool
}

func (s *externalPolicyStore) GetExternalBlockingDependencyRecords(context.Context) (map[string][]*beads.Dependency, error) {
	return s.deps, nil
}

func (s *externalPolicyStore) GetReadyWork(_ context.Context, filter beads.WorkFilter) ([]*beads.Issue, error) {
	excluded := make(map[string]struct{}, len(filter.ExcludeIDs))
	for _, id := range filter.ExcludeIDs {
		excluded[id] = struct{}{}
	}
	result := make([]*beads.Issue, 0, len(s.ready))
	for _, issue := range s.ready {
		if _, skip := excluded[issue.ID]; !skip {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (s *externalPolicyStore) GetIssuesByLabel(_ context.Context, label string) ([]*beads.Issue, error) {
	return s.labels[label], nil
}

func (s *externalPolicyStore) Close() error {
	s.closed = true
	return nil
}

type baseOnlyStore struct {
	beads.Storage
}

func TestWithExternalDependencyPolicyResolvesCapabilitiesThroughPublicSDK(t *testing.T) {
	local := &externalPolicyStore{
		ready: []*beads.Issue{
			{ID: "local-blocked", Status: beads.StatusOpen, IssueType: beads.TypeTask},
			{ID: "local-ready", Status: beads.StatusOpen, IssueType: beads.TypeTask},
		},
		deps: map[string][]*beads.Dependency{
			"local-blocked": {{IssueID: "local-blocked", DependsOnID: "external:payments:api", Type: beads.DepBlocks}},
		},
	}
	foreign := &externalPolicyStore{
		labels: map[string][]*beads.Issue{
			"provides:api": {{ID: "provider", Status: beads.StatusOpen, IssueType: beads.TypeTask}},
		},
	}

	wrapped, err := beads.WithExternalDependencyPolicy(
		local,
		func(project string) (string, bool) {
			return "/projects/" + project, project == "payments"
		},
		func(_ context.Context, projectRoot string) (beads.Storage, error) {
			if projectRoot != "/projects/payments" {
				t.Fatalf("project root = %q, want /projects/payments", projectRoot)
			}
			return foreign, nil
		},
	)
	if err != nil {
		t.Fatalf("WithExternalDependencyPolicy: %v", err)
	}

	ready, err := wrapped.GetReadyWork(t.Context(), beads.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork with open provider: %v", err)
	}
	if got := issueIDs(ready); len(got) != 1 || got[0] != "local-ready" {
		t.Fatalf("ready with open provider = %v, want [local-ready]", got)
	}
	if !foreign.closed {
		t.Fatal("foreign store was not closed after capability resolution")
	}

	foreign.closed = false
	foreign.labels["provides:api"][0].Status = beads.StatusClosed
	ready, err = wrapped.GetReadyWork(t.Context(), beads.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork with closed provider: %v", err)
	}
	if got := issueIDs(ready); len(got) != 2 || got[0] != "local-blocked" || got[1] != "local-ready" {
		t.Fatalf("ready with closed provider = %v, want [local-blocked local-ready]", got)
	}
}

func TestWithExternalDependencyPolicyRejectsNonDoltStorage(t *testing.T) {
	_, err := beads.WithExternalDependencyPolicy(&baseOnlyStore{}, nil, nil)
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want *beads.ErrUnsupported", err)
	}
}

func issueIDs(issues []*beads.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}
