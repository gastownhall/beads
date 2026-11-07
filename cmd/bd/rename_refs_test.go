package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestRenameIDReferences(t *testing.T) {
	t.Parallel()
	// GH#7040: a mention is the old ID standing alone. A longer ID that
	// starts or ends with it names a different issue and is left alone.
	for _, tt := range []struct {
		name, text, oldID, newID, want string
	}{
		{"alone", "test-a", "test-a", "test-z", "test-z"},
		{"sentence period", "see test-a.", "test-a", "test-z", "see test-z."},
		{"ellipsis", "maybe test-a...", "test-a", "test-z", "maybe test-z..."},
		{"comma repeat", "test-a,test-a", "test-a", "test-z", "test-z,test-z"},
		{"repeated", "test-a test-a; test-a!", "test-a", "test-z", "test-z test-z; test-z!"},
		{"brackets", "(test-a) [test-a] `test-a`", "test-a", "test-z", "(test-z) [test-z] `test-z`"},
		{"possessive", "test-a's parent", "test-a", "test-z", "test-z's parent"},
		{"rig qualified", "waits on rig:test-a", "test-a", "test-z", "waits on rig:test-z"},
		{"url path", "https://example.com/issues/test-a/edit", "test-a", "test-z", "https://example.com/issues/test-z/edit"},
		{"lines", "test-a\ntest-a\n", "test-a", "test-z", "test-z\ntest-z\n"},
		{"non-ascii neighbours", "ab—test-a—cd", "test-a", "test-z", "ab—test-z—cd"},
		{"dotted child", "test-a tracks test-a.1 and test-a.1.2", "test-a", "test-z", "test-z tracks test-a.1 and test-a.1.2"},
		{"child period", "see test-a.1.", "test-a", "test-z", "see test-a.1."},
		{"hyphenated extension", "bd-auth-v2 replaces bd-auth", "bd-auth", "bd-login", "bd-auth-v2 replaces bd-login"},
		{"leading prefix", "beads-vscode-1 is not vscode-1", "vscode-1", "vscode-9", "beads-vscode-1 is not vscode-9"},
		{"pre prefix", "pre-test-a", "test-a", "test-z", "pre-test-a"},
		{"underscore", "test-a_x x_test-a", "test-a", "test-z", "test-a_x x_test-a"},
		{"trailing hyphen", "test-a- is open", "test-a", "test-z", "test-a- is open"},
		{"trailing hyphen id", "test-a- and test-a", "test-a-", "test-z", "test-z and test-a"},
		{"case", "Test-A TEST-A", "test-a", "test-z", "Test-A TEST-A"},
		{"no match", "nothing here", "test-a", "test-z", "nothing here"},
		{"empty", "", "test-a", "test-z", ""},
		{"new ID contains old", "test-a and test-a.", "test-a", "test-a-2", "test-a-2 and test-a-2."},
		{"contributor case", "test-a tracks test-a.1; test-a,test-a; see test-a.", "test-a", "test-z", "test-z tracks test-a.1; test-z,test-z; see test-z."},
	} {
		if got := replaceIDRefs(tt.text, tt.oldID, tt.newID); got != tt.want {
			t.Errorf("%s: replaceIDRefs(%q, %q, %q) = %q, want %q", tt.name, tt.text, tt.oldID, tt.newID, got, tt.want)
		}
	}
}

// fakeRenameReferenceStore is a renameReferenceStore over issues held in
// memory. UpdateIssue records each update.
type fakeRenameReferenceStore struct {
	issues  []*types.Issue
	updates map[string]map[string]interface{}
}

func (s *fakeRenameReferenceStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s *fakeRenameReferenceStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	if s.updates == nil {
		s.updates = map[string]map[string]interface{}{}
	}
	s.updates[id] = updates
	return nil
}

// renameReportText renders report as bd rename prints it after its Renamed
// line.
func renameReportText(report renameReport, oldID, newID string) string {
	var buf bytes.Buffer
	report.write(&buf, oldID, newID)
	return buf.String()
}

func TestUpdateReferencesInAllIssues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("rewrites and lists each issue", func(t *testing.T) {
		t.Parallel()
		st := &fakeRenameReferenceStore{issues: []*types.Issue{
			{ID: "test-10", Title: "T", CloseReason: "duplicate of test-a"},
			{ID: "test-z", Title: "renamed test-a", Description: "test-a, test-a and test-a.", Notes: "nothing"},
			{ID: "test-9", AcceptanceCriteria: "test-a done", Design: "after test-a"},
			{ID: "test-c", Title: "C", Description: "about test-a.1"},
		}}
		report, err := updateReferencesInAllIssues(ctx, st, "test-a", "test-z", "test")
		if err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		wantUpdates := map[string]map[string]interface{}{
			"test-10": {"close_reason": "duplicate of test-z"},
			"test-z":  {"title": "renamed test-z", "description": "test-z, test-z and test-z."},
			"test-9":  {"acceptance_criteria": "test-z done", "design": "after test-z"},
		}
		if fmt.Sprint(st.updates) != fmt.Sprint(wantUpdates) {
			t.Errorf("updates = %v, want %v", st.updates, wantUpdates)
		}
		want := "Rewrote test-a to test-z in:\n" +
			"  test-9: design, acceptance_criteria\n" +
			"  test-10: close_reason\n" +
			"  test-z: title, description\n"
		if got := renameReportText(report, "test-a", "test-z"); got != want {
			t.Errorf("report:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("nothing to report", func(t *testing.T) {
		t.Parallel()
		st := &fakeRenameReferenceStore{issues: []*types.Issue{{ID: "test-z", Title: "renamed"}, {ID: "test-b", Notes: "test-a.1"}}}
		report, err := updateReferencesInAllIssues(ctx, st, "test-a", "test-z", "test")
		if err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		if len(st.updates) != 0 {
			t.Errorf("updates = %v, want none", st.updates)
		}
		if got := renameReportText(report, "test-a", "test-z"); got != "" {
			t.Errorf("report = %q, want nothing", got)
		}
	})

	t.Run("lists every issue", func(t *testing.T) {
		t.Parallel()
		st := &fakeRenameReferenceStore{}
		for i := 1; i <= 25; i++ {
			st.issues = append(st.issues, &types.Issue{ID: fmt.Sprintf("test-%d", i), Notes: "see test-a"})
		}
		report, err := updateReferencesInAllIssues(ctx, st, "test-a", "test-z", "test")
		if err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		got := renameReportText(report, "test-a", "test-z")
		for i := 1; i <= 25; i++ {
			if line := fmt.Sprintf("  test-%d: notes\n", i); !strings.Contains(got, line) {
				t.Errorf("report lacks %q:\n%s", line, got)
			}
		}
	})
}
