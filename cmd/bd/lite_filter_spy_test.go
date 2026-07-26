//go:build cgo

package main

import (
	"context"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// filterCapturingStore is the shared spy used by the round-trip /
// opt-out filter gate tests:
//
//   - TestExport_FilterHasZeroMaxRows       (cmd/bd/export_max_rows_gate_test.go)        — be-x42v.4
//   - TestExportAuto_FilterHasZeroMaxRows   (cmd/bd/export_auto_max_rows_gate_test.go)   — be-x42v.4
//   - TestMigrateIssues_FilterHasZeroMaxRows (cmd/bd/migrate_issues_max_rows_gate_test.go) — be-x42v.4
//   - TestJiraSync_FilterHasZeroMaxRows     (cmd/bd/jira_max_rows_gate_test.go)          — be-x42v.4
//
// The same spy is intended to be reused by be-uwvs.5's parallel
// Lite=false gate tests (TestExportFilterIsAlwaysFull and friends);
// whichever bead lands first creates the spy, the other reuses it.
//
// It embeds *dolt.DoltStore (concrete) rather than the narrow
// storage.Storage interface so that cmd/bd's capability assertions
// (mustAnnot, mustDeps, mustConfig — see storage_caps.go) continue to
// resolve to the underlying Dolt-backed capabilities. Wrapping with the
// bare interface would satisfy storage.Storage at compile time but break
// the runtime AnnotationStore / DependencyQueryStore type assertions the
// commands under test rely on.
//
// SearchIssues records every filter received and then delegates to the
// embedded store so the caller sees real query results. Tests call
// LastFilter() / AllFilters() to assert on the gate invariants:
//
//   - filter.MaxRows MUST be 0 on every call (env-bypass guarantee).
//   - filter.MaxRowsSource MUST be "" on every call.
//   - filter.Lite MUST be false on every call (be-uwvs.5 follow-up;
//     round-trip integrity).
//   - filter.Limit MUST be 0 on the main round-trip call (unlimited).
//
// Recording every filter (not just the last) gives multi-call paths
// like migrate-issues — which issues 2 probe + 1 candidate-scan + 1
// dedup SearchIssues calls — free coverage without re-touching the
// spy.
type filterCapturingStore struct {
	*dolt.DoltStore

	mu       sync.Mutex
	captured []types.IssueFilter
}

// newFilterCapturingStore wraps an existing *dolt.DoltStore in a spy.
// Callers reach the inner store via the embedded field if they need to
// seed data outside the spy's SearchIssues capture (e.g., via DB().Exec).
func newFilterCapturingStore(inner *dolt.DoltStore) *filterCapturingStore {
	return &filterCapturingStore{DoltStore: inner}
}

// SearchIssues records the filter then delegates to the embedded Dolt
// store. The filter is captured before delegation so a panicking
// underlying call (which shouldn't happen with Dolt) still leaves the
// captured slice consistent for the test's diagnostic message.
func (s *filterCapturingStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.mu.Lock()
	s.captured = append(s.captured, filter)
	s.mu.Unlock()
	return s.DoltStore.SearchIssues(ctx, query, filter)
}

// LastFilter returns the most recent filter recorded by SearchIssues.
// Returns the zero-value types.IssueFilter{} (and len(s.captured)==0
// observable via Calls()) if SearchIssues was never invoked — callers
// should assert on Calls() > 0 before reading the filter.
func (s *filterCapturingStore) LastFilter() types.IssueFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.captured) == 0 {
		return types.IssueFilter{}
	}
	return s.captured[len(s.captured)-1]
}

// AllFilters returns a snapshot of every filter SearchIssues received.
// The returned slice is a copy so callers can iterate without holding
// the spy lock.
func (s *filterCapturingStore) AllFilters() []types.IssueFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.IssueFilter, len(s.captured))
	copy(out, s.captured)
	return out
}

// Calls returns the number of times SearchIssues has been invoked on
// this spy. Tests use it to gate that the spy was actually exercised
// before asserting on the captured filter — a test that never reaches
// SearchIssues would otherwise pass vacuously.
func (s *filterCapturingStore) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.captured)
}

// Compile-time assurances that filterCapturingStore satisfies both the
// narrow storage.Storage interface and the wider storage.DoltStorage
// interface that cmd/bd's `store` global is typed as. The embedded
// *dolt.DoltStore provides every required method; these assertions fail
// at build time if either the embed is changed or the interface adds a
// method DoltStore doesn't satisfy.
var (
	_ storage.Storage     = (*filterCapturingStore)(nil)
	_ storage.DoltStorage = (*filterCapturingStore)(nil)
)
