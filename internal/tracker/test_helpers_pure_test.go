// Pure-Go test helpers shared across tracker test files.
//
// This file MUST NOT carry a `//go:build cgo` tag. Helpers here avoid sql.DB,
// internal/storage/dolt, and embedded Dolt so pure-Go tests in this package
// compile under CGO_ENABLED=0 with the gms_pure_go build tag. Cgo-only helpers
// live in tagged test files.

package tracker

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// mockTracker implements IssueTracker for testing.
type mockTracker struct {
	name            string
	issues          []TrackerIssue
	created         []*types.Issue
	updated         map[string]*types.Issue
	fetchErr        error
	fetchIssueErr   error
	createErr       error
	createFailAfter int // fail after this many successful creates (0 = fail immediately)
	updateErr       error
	fieldMapper     FieldMapper
	fetchIssues     func(context.Context, FetchOptions) ([]TrackerIssue, error)
}

type mockExternalRefTracker struct {
	*mockTracker
	buildRef func(*TrackerIssue) string
	extract  func(string) string
	isRef    func(string) bool
}

type mockBatchTracker struct {
	*mockTracker
	batchResult   *BatchPushResult
	batchDryRun   *BatchPushResult
	batchErr      error
	batchCalls    int
	batchDryCalls int
	batchIssues   []*types.Issue
	batchForceIDs map[string]bool
}

func newMockTracker(name string) *mockTracker {
	return &mockTracker{
		name:        name,
		updated:     make(map[string]*types.Issue),
		fieldMapper: &mockMapper{},
	}
}

func (m *mockExternalRefTracker) IsExternalRef(ref string) bool {
	if m.isRef != nil {
		return m.isRef(ref)
	}
	return m.mockTracker.IsExternalRef(ref)
}

func (m *mockExternalRefTracker) ExtractIdentifier(ref string) string {
	if m.extract != nil {
		return m.extract(ref)
	}
	return m.mockTracker.ExtractIdentifier(ref)
}

func (m *mockExternalRefTracker) BuildExternalRef(issue *TrackerIssue) string {
	if m.buildRef != nil {
		return m.buildRef(issue)
	}
	return m.mockTracker.BuildExternalRef(issue)
}

func (m *mockTracker) Name() string                                    { return m.name }
func (m *mockTracker) DisplayName() string                             { return m.name }
func (m *mockTracker) ConfigPrefix() string                            { return m.name }
func (m *mockTracker) Init(_ context.Context, _ storage.Storage) error { return nil }
func (m *mockTracker) Validate() error                                 { return nil }
func (m *mockTracker) Close() error                                    { return nil }
func (m *mockTracker) FieldMapper() FieldMapper                        { return m.fieldMapper }
func (m *mockTracker) IsExternalRef(ref string) bool                   { return len(ref) > 0 }
func (m *mockTracker) ExtractIdentifier(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}
func (m *mockTracker) BuildExternalRef(issue *TrackerIssue) string {
	// Mirror the real trackers (notion/linear/github), which all prefer the
	// issue's canonical URL over a synthesized ref.
	if issue.URL != "" {
		return issue.URL
	}
	return fmt.Sprintf("https://%s.test/%s", m.name, issue.Identifier)
}

func (m *mockBatchTracker) BatchPush(_ context.Context, issues []*types.Issue, forceIDs map[string]bool) (*BatchPushResult, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	m.batchCalls++
	m.batchIssues = append(m.batchIssues, issues...)
	m.batchForceIDs = forceIDs
	if m.batchResult != nil {
		return m.batchResult, nil
	}
	return &BatchPushResult{}, nil
}

func (m *mockBatchTracker) BatchPushDryRun(_ context.Context, issues []*types.Issue, forceIDs map[string]bool) (*BatchPushResult, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	m.batchDryCalls++
	m.batchIssues = append(m.batchIssues, issues...)
	m.batchForceIDs = forceIDs
	if m.batchDryRun != nil {
		return m.batchDryRun, nil
	}
	return &BatchPushResult{}, nil
}

func (m *mockTracker) FetchIssues(ctx context.Context, opts FetchOptions) ([]TrackerIssue, error) {
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	if m.fetchIssues != nil {
		return m.fetchIssues(ctx, opts)
	}
	return m.issues, nil
}

func (m *mockTracker) FetchIssue(_ context.Context, identifier string) (*TrackerIssue, error) {
	if m.fetchIssueErr != nil {
		return nil, m.fetchIssueErr
	}
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	for i := range m.issues {
		if m.issues[i].Identifier == identifier {
			return &m.issues[i], nil
		}
	}
	return nil, nil
}

func (m *mockTracker) CreateIssue(_ context.Context, issue *types.Issue) (*TrackerIssue, error) {
	if m.createErr != nil {
		if m.createFailAfter > 0 && len(m.created) < m.createFailAfter {
			// Allow first N creates to succeed.
		} else {
			return nil, m.createErr
		}
	}
	m.created = append(m.created, issue)
	return &TrackerIssue{
		ID:         "ext-" + issue.ID,
		Identifier: "EXT-" + issue.ID,
		URL:        fmt.Sprintf("https://%s.test/EXT-%s", m.name, issue.ID),
		Title:      issue.Title,
	}, nil
}

func (m *mockTracker) UpdateIssue(_ context.Context, externalID string, issue *types.Issue) (*TrackerIssue, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	m.updated[externalID] = issue
	return &TrackerIssue{
		ID:         externalID,
		Identifier: externalID,
		Title:      issue.Title,
	}, nil
}

// mockMapper implements FieldMapper for testing.
type mockMapper struct {
	issueToBeads func(*TrackerIssue) *IssueConversion
}

func (m *mockMapper) PriorityToBeads(p interface{}) int {
	if v, ok := p.(int); ok {
		return v
	}
	return 2
}
func (m *mockMapper) PriorityToTracker(p int) interface{}         { return p }
func (m *mockMapper) StatusToBeads(_ interface{}) types.Status    { return types.StatusOpen }
func (m *mockMapper) StatusToTracker(s types.Status) interface{}  { return string(s) }
func (m *mockMapper) TypeToBeads(_ interface{}) types.IssueType   { return types.TypeTask }
func (m *mockMapper) TypeToTracker(t types.IssueType) interface{} { return string(t) }
func (m *mockMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return map[string]interface{}{
		"title":       issue.Title,
		"description": issue.Description,
	}
}

func (m *mockMapper) IssueToBeads(ti *TrackerIssue) *IssueConversion {
	if m.issueToBeads != nil {
		return m.issueToBeads(ti)
	}
	return &IssueConversion{
		Issue: &types.Issue{
			Title:       ti.Title,
			Description: ti.Description,
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
		},
	}
}

type pureTestStore struct {
	storage.Storage
	issues        []*types.Issue
	localMetadata map[string]string
}

func newPureTestStore(issues ...*types.Issue) *pureTestStore {
	return &pureTestStore{
		issues:        issues,
		localMetadata: make(map[string]string),
	}
}

func (s *pureTestStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return append([]*types.Issue(nil), s.issues...), nil
}

func (s *pureTestStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	for _, issue := range s.issues {
		if issue.ID != id {
			continue
		}
		if raw, ok := updates["external_ref"]; ok {
			if ref, ok := raw.(string); ok {
				issue.ExternalRef = &ref
			}
		}
		return nil
	}
	return storage.ErrNotFound
}

func (s *pureTestStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	return s.localMetadata[key], nil
}

func (s *pureTestStore) SetLocalMetadata(_ context.Context, key, value string) error {
	s.localMetadata[key] = value
	return nil
}
