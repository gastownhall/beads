package conformance

import (
	"context"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// Store is a small tracker.Store implementation backed by maps. It is useful
// for adapter contract tests that need to observe local mutations without
// opening a real Dolt server.
type Store struct {
	mu        sync.Mutex
	Issues    map[string]*types.Issue
	Config    map[string]string
	Metadata  map[string]string
	LastSync  string
	Mutations int
}

// Snapshot returns a copy of the normalized state.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{Issues: make(map[string]types.Issue, len(s.Issues)), Config: cloneMap(s.Config), Metadata: cloneMap(s.Metadata), LastSync: s.LastSync}
	for id, issue := range s.Issues {
		out.Issues[id] = *cloneIssue(issue)
	}
	return out
}

// MutationCount reports committed local writes.
func (s *Store) MutationCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.Mutations }

var _ tracker.Store = (*Store)(nil)

// ApplyIssueUpdate applies an issue update and replaces labels atomically.
func (s *Store) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, labels []string, actor string) error {
	if err := s.UpdateIssue(ctx, id, updates, actor); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue := s.Issues[id]; issue != nil && labels != nil {
		issue.Labels = append([]string(nil), labels...)
	}
	return nil
}

// NewStore returns an empty fake tracker store.
func NewStore() *Store {
	return &Store{Issues: map[string]*types.Issue{}, Config: map[string]string{}, Metadata: map[string]string{}}
}

// Open returns the store as a unit-of-work target. The call is intentionally
// side-effect free; callers count UOW opens in their factory when needed.
func (s *Store) Open() tracker.Store { return s }

// GetConfig reads one config value.
func (s *Store) GetConfig(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Config[key], nil
}

// GetAllConfig returns a copy of config.
func (s *Store) GetAllConfig(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMap(s.Config), nil
}

// GetLocalMetadata reads one local metadata value.
func (s *Store) GetLocalMetadata(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Metadata[key], nil
}

// SetLocalMetadata stores one local metadata value.
func (s *Store) SetLocalMetadata(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Metadata[key] = value
	if strings.HasSuffix(key, ".last_sync") {
		s.LastSync = value
	}
	s.Mutations++
	return nil
}

// SearchIssues returns a copy of all local issues.
func (s *Store) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*types.Issue, 0, len(s.Issues))
	for _, issue := range s.Issues {
		out = append(out, cloneIssue(issue))
	}
	return out, nil
}

// GetIssueByExternalRef finds one local issue by external reference.
func (s *Store) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, issue := range s.Issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return cloneIssue(issue), nil
		}
	}
	return nil, storage.ErrNotFound
}

// GetDependentsWithMetadata returns no dependents in the minimal fixture.
func (s *Store) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

// GetDependenciesWithMetadata returns no dependencies in the minimal fixture.
func (s *Store) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

// CreateIssue inserts one local issue.
func (s *Store) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Issues[issue.ID] = cloneIssue(issue)
	s.Mutations++
	return nil
}

// UpdateIssue applies the common tracker fields.
func (s *Store) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	issue := s.Issues[id]
	if issue == nil {
		return storage.ErrNotFound
	}
	if v, ok := updates["title"].(string); ok {
		issue.Title = v
	}
	if v, ok := updates["description"].(string); ok {
		issue.Description = v
	}
	if v, ok := updates["external_ref"].(string); ok {
		issue.ExternalRef = &v
	}
	s.Mutations++
	return nil
}

// AddDependency is a no-op in the minimal fixture.
func (s *Store) AddDependency(context.Context, *types.Dependency, string) error {
	s.mu.Lock()
	s.Mutations++
	s.mu.Unlock()
	return nil
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneIssue(in *types.Issue) *types.Issue {
	if in == nil {
		return nil
	}
	out := *in
	out.Labels = append([]string(nil), in.Labels...)
	if in.ExternalRef != nil {
		ref := *in.ExternalRef
		out.ExternalRef = &ref
	}
	return &out
}
