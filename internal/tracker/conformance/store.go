package conformance

import (
	"context"
	"sort"
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
	mu       sync.Mutex
	Issues   map[string]*types.Issue
	Config   map[string]string
	Metadata map[string]string

	// Wisps is the second storage plane. It exists so the shared suite can
	// state the external_ref resolution contract — issues win over wisps —
	// which is unrepresentable in a single-plane fixture and which a merged
	// search silently gets wrong. Empty unless a test seeds it, so every
	// other assertion is unaffected.
	Wisps map[string]*types.Issue
	Deps  map[string]map[string]struct{}

	LastSync  string
	Mutations int
}

// Snapshot returns a copy of the normalized state.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{Issues: make(map[string]types.Issue, len(s.Issues)), Dependencies: make(map[string][]string, len(s.Deps)), Config: cloneMap(s.Config), Metadata: cloneMap(s.Metadata), LastSync: s.LastSync}
	for id, issue := range s.Issues {
		out.Issues[id] = *cloneIssue(issue)
	}
	for id, targets := range s.Deps {
		for target := range targets {
			out.Dependencies[id] = append(out.Dependencies[id], target)
		}
		sort.Strings(out.Dependencies[id])
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
		issue.Labels = normalizedLabels(labels)
	}
	return nil
}

// normalizedLabels mirrors the engine's normalizedStringSlice: trim, drop
// empty, dedupe, sort. The sort is not cosmetic — the fake is the oracle
// adapters are judged against, and both real backends read labels back
// ORDER BY label. Without it the first order-sensitive multi-label assertion
// would pass on the real legs and fail here, or vice versa.
func normalizedLabels(labels []string) []string {
	seen := make(map[string]struct{}, len(labels))
	result := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		result = append(result, label)
	}
	sort.Strings(result)
	return result
}

// NewStore returns an empty fake tracker store.
func NewStore() *Store {
	return &Store{Issues: map[string]*types.Issue{}, Wisps: map[string]*types.Issue{}, Deps: map[string]map[string]struct{}{}, Config: map[string]string{}, Metadata: map[string]string{}}
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

// GetIssueByExternalRef finds one local issue by external reference, issues
// plane first and wisps only as a fallback. The order is the contract, not an
// implementation detail: the direct backend resolves `issues` before `wisps`
// so pull dedup updates the durable bead rather than a pushed ephemeral one,
// and Run asserts it (see external_ref_resolution_prefers_issue_plane).
func (s *Store) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, plane := range []map[string]*types.Issue{s.Issues, s.Wisps} {
		if issue := matchExternalRef(plane, ref); issue != nil {
			return cloneIssue(issue), nil
		}
	}
	return nil, storage.ErrNotFound
}

// matchExternalRef returns the lowest-ID row in one plane carrying ref, or nil.
// Go map iteration is randomized, so picking by ID keeps a plane with several
// matching rows from making the harness flaky.
func matchExternalRef(plane map[string]*types.Issue, ref string) *types.Issue {
	var found *types.Issue
	for _, issue := range plane {
		if issue.ExternalRef == nil || *issue.ExternalRef != ref {
			continue
		}
		if found == nil || issue.ID < found.ID {
			found = issue
		}
	}
	return found
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
	if v, ok := updates["status"].(string); ok {
		issue.Status = types.Status(v)
	}
	s.Mutations++
	return nil
}

// AddDependency stores the dependency relation in the minimal fixture.
func (s *Store) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	s.mu.Lock()
	if s.Deps[dep.IssueID] == nil {
		s.Deps[dep.IssueID] = map[string]struct{}{}
	}
	s.Deps[dep.IssueID][dep.DependsOnID] = struct{}{}
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
