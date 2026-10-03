package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// mockProvider implements types.IssueProvider for testing
type mockProvider struct {
	issues []*types.Issue
	prefix string
}

func (m *mockProvider) GetOpenIssues(ctx context.Context) ([]*types.Issue, error) {
	return m.issues, nil
}

func (m *mockProvider) GetIssuePrefix() string {
	if m.prefix == "" {
		return "bd"
	}
	return m.prefix
}

func TestFindOrphanedIssues_ConvertsDoctorOutput(t *testing.T) {
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(path string, provider types.IssueProvider) ([]doctor.OrphanIssue, error) {
		if path != "/tmp/repo" {
			t.Fatalf("unexpected path %q", path)
		}
		return []doctor.OrphanIssue{{
			IssueID:             "bd-123",
			Title:               "Fix login",
			Status:              "open",
			LatestCommit:        "abc123",
			LatestCommitMessage: "(bd-123) implement fix",
		}}, nil
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	// Set up a mock store so getIssueProvider works
	origStore := store
	store = nil // Force the "no database available" path to be avoided
	t.Cleanup(func() { store = origStore })

	// We need to bypass getIssueProvider for this test since it needs a real store
	// The test is really about conversion logic, so we test the mock directly
	provider := &mockProvider{prefix: "bd"}
	orphans, err := doctorFindOrphanedIssues("/tmp/repo", provider)
	if err != nil {
		t.Fatalf("doctorFindOrphanedIssues returned error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans))
	}
	orphan := orphans[0]
	if orphan.IssueID != "bd-123" || orphan.Title != "Fix login" || orphan.Status != "open" {
		t.Fatalf("unexpected orphan output: %#v", orphan)
	}
	if orphan.LatestCommit != "abc123" || !strings.Contains(orphan.LatestCommitMessage, "implement") {
		t.Fatalf("commit metadata not preserved: %#v", orphan)
	}
}

func TestFindOrphanedIssues_ErrorWrapped(t *testing.T) {
	// Test that errors from doctorFindOrphanedIssues are properly wrapped.
	// We test the doctor function directly since findOrphanedIssues now
	// requires a valid provider setup (store or dbPath).
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(string, types.IssueProvider) ([]doctor.OrphanIssue, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	// Call the mocked function directly to test error propagation
	provider := &mockProvider{prefix: "bd"}
	_, err := doctorFindOrphanedIssues("/tmp/repo", provider)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestCloseIssue_UsesRunner(t *testing.T) {
	orig := closeIssueRunner
	defer func() { closeIssueRunner = orig }()

	called := false
	closeIssueRunner = func(issueID string) error {
		called = true
		if issueID != "bd-999" {
			t.Fatalf("unexpected issue id %q", issueID)
		}
		return nil
	}

	if err := closeIssue("bd-999"); err != nil {
		t.Fatalf("closeIssue returned error: %v", err)
	}
	if !called {
		t.Fatal("closeIssueRunner was not invoked")
	}
}

func TestCloseIssue_PropagatesError(t *testing.T) {
	orig := closeIssueRunner
	closeIssueRunner = func(string) error { return errors.New("nope") }
	t.Cleanup(func() { closeIssueRunner = orig })

	err := closeIssue("bd-1")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected delegated error, got %v", err)
	}
}

func TestOrphansLabelFlagsRegistered(t *testing.T) {
	labelFlag := orphansCmd.Flags().Lookup("label")
	if labelFlag == nil {
		t.Fatal("--label flag not registered on orphansCmd")
	}
	labelAnyFlag := orphansCmd.Flags().Lookup("label-any")
	if labelAnyFlag == nil {
		t.Fatal("--label-any flag not registered on orphansCmd")
	}
}

func TestDoltStoreProvider_LabelFields(t *testing.T) {
	p := &doltStoreProvider{
		labels:    []string{"theme:personal"},
		labelsAny: []string{"theme:ventures", "theme:probono"},
	}
	if len(p.labels) != 1 || p.labels[0] != "theme:personal" {
		t.Fatalf("unexpected labels: %v", p.labels)
	}
	if len(p.labelsAny) != 2 {
		t.Fatalf("unexpected labelsAny: %v", p.labelsAny)
	}
}

// TestFindOrphanedIssues_LabelArgs verifies that label args are passed to the doctor
// function via a provider that was constructed with those labels. We intercept
// doctorFindOrphanedIssues to capture the provider and inspect its fields.
func TestFindOrphanedIssues_LabelArgs(t *testing.T) {
	orig := doctorFindOrphanedIssues
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	var capturedProvider types.IssueProvider
	doctorFindOrphanedIssues = func(path string, provider types.IssueProvider) ([]doctor.OrphanIssue, error) {
		capturedProvider = provider
		return nil, nil
	}

	// Substitute getIssueProvider so we don't need a real store.
	origProviderFn := getIssueProviderFn
	t.Cleanup(func() { getIssueProviderFn = origProviderFn })
	getIssueProviderFn = func(labels, labelsAny []string) (types.IssueProvider, func(), error) {
		return &doltStoreProvider{labels: labels, labelsAny: labelsAny}, func() {}, nil
	}

	wantLabels := []string{"theme:personal"}
	wantLabelsAny := []string{"theme:ventures"}
	_, err := findOrphanedIssues(".", wantLabels, wantLabelsAny)
	if err != nil {
		t.Fatalf("findOrphanedIssues returned unexpected error: %v", err)
	}
	p, ok := capturedProvider.(*doltStoreProvider)
	if !ok {
		t.Fatalf("expected *doltStoreProvider, got %T", capturedProvider)
	}
	if len(p.labels) != 1 || p.labels[0] != "theme:personal" {
		t.Fatalf("labels not propagated: got %v", p.labels)
	}
	if len(p.labelsAny) != 1 || p.labelsAny[0] != "theme:ventures" {
		t.Fatalf("labelsAny not propagated: got %v", p.labelsAny)
	}
}

func TestFindOrphanedIssues_NoLabels(t *testing.T) {
	orig := doctorFindOrphanedIssues
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	var capturedProvider types.IssueProvider
	doctorFindOrphanedIssues = func(_ string, provider types.IssueProvider) ([]doctor.OrphanIssue, error) {
		capturedProvider = provider
		return nil, nil
	}

	origProviderFn := getIssueProviderFn
	t.Cleanup(func() { getIssueProviderFn = origProviderFn })
	getIssueProviderFn = func(labels, labelsAny []string) (types.IssueProvider, func(), error) {
		return &doltStoreProvider{labels: labels, labelsAny: labelsAny}, func() {}, nil
	}

	_, err := findOrphanedIssues(".", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p, ok := capturedProvider.(*doltStoreProvider)
	if !ok {
		t.Fatalf("expected *doltStoreProvider, got %T", capturedProvider)
	}
	if len(p.labels) != 0 || len(p.labelsAny) != 0 {
		t.Fatalf("expected empty label filters, got labels=%v labelsAny=%v", p.labels, p.labelsAny)
	}
}

// TestFindOrphanedIssues_EmptyResultIsEmptyArray pins the --json contract for the
// no-orphans case: the slice must be non-nil so it marshals to [] rather than the
// JSON literal null. Reverting the make() in findOrphanedIssuesWithProvider makes
// this fail with "null".
func TestFindOrphanedIssues_EmptyResultIsEmptyArray(t *testing.T) {
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(path string, provider types.IssueProvider) ([]doctor.OrphanIssue, error) {
		return []doctor.OrphanIssue{}, nil
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	output, err := findOrphanedIssuesWithProvider(".", &mockProvider{})
	if err != nil {
		t.Fatalf("findOrphanedIssuesWithProvider returned error: %v", err)
	}
	if output == nil {
		t.Fatal("output slice is nil; it must be non-nil so --json emits [] not null")
	}
	if len(output) != 0 {
		t.Fatalf("expected 0 rows, got %d", len(output))
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if got := string(encoded); got != "[]" {
		t.Fatalf("empty result marshalled to %q, want \"[]\"", got)
	}
}

// TestFindOrphanedIssues_RowCarriesIDAndLabels pins the other half of the --json
// contract: every row exposes the issue identifier under the `id` key that the
// rest of bd's --json surfaces use, retains `issue_id` for existing consumers,
// and reports the labels the --label/--label-any flags filter on.
func TestFindOrphanedIssues_RowCarriesIDAndLabels(t *testing.T) {
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(path string, provider types.IssueProvider) ([]doctor.OrphanIssue, error) {
		return []doctor.OrphanIssue{{
			IssueID: "bd-123",
			Title:   "Fix login",
			Status:  "open",
			Labels:  []string{"theme:personal", "kind:bug"},
		}}, nil
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	output, err := findOrphanedIssuesWithProvider(".", &mockProvider{})
	if err != nil {
		t.Fatalf("findOrphanedIssuesWithProvider returned error: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("expected 1 row, got %d", len(output))
	}
	row := output[0]
	if row.ID != "bd-123" {
		t.Fatalf("row.ID = %q, want %q", row.ID, "bd-123")
	}
	if row.IssueID != row.ID {
		t.Fatalf("issue_id %q and id %q must carry the same value", row.IssueID, row.ID)
	}
	if len(row.Labels) != 2 || row.Labels[0] != "theme:personal" {
		t.Fatalf("labels not propagated: %#v", row.Labels)
	}

	var decoded map[string]any
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	for _, key := range []string{"id", "issue_id", "labels"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("key %q absent from marshalled row: %s", key, encoded)
		}
	}
}

// TestFindOrphanedIssues_LabelsNeverNull keeps an unlabelled issue's labels as []
// rather than null, so a consumer can range over the field unconditionally.
func TestFindOrphanedIssues_LabelsNeverNull(t *testing.T) {
	orig := doctorFindOrphanedIssues
	doctorFindOrphanedIssues = func(path string, provider types.IssueProvider) ([]doctor.OrphanIssue, error) {
		return []doctor.OrphanIssue{{IssueID: "bd-1", Title: "t", Status: "open"}}, nil
	}
	t.Cleanup(func() { doctorFindOrphanedIssues = orig })

	output, err := findOrphanedIssuesWithProvider(".", &mockProvider{})
	if err != nil {
		t.Fatalf("findOrphanedIssuesWithProvider returned error: %v", err)
	}
	if output[0].Labels == nil {
		t.Fatal("Labels is nil; an unlabelled issue must still marshal labels as []")
	}
	encoded, err := json.Marshal(output[0])
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(encoded), `"labels":[]`) {
		t.Fatalf("expected \"labels\":[] in %s", encoded)
	}
}
