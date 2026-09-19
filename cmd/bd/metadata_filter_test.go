//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestMetadataFilterSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	// Create all test data up front — one DB for all subtests.
	// Use unique metadata keys per subtest group to avoid interference.

	// --- MetadataFieldMatch data ---
	mfm1 := &types.Issue{
		ID: "mfm-1", Title: "Platform issue (mfm)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"mfm_team":"platform","mfm_sprint":"Q1"}`),
	}
	mfm2 := &types.Issue{
		ID: "mfm-2", Title: "Frontend issue (mfm)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"mfm_team":"frontend","mfm_sprint":"Q1"}`),
	}

	// --- HasMetadataKey data ---
	hmk1 := &types.Issue{
		ID: "hmk-1", Title: "Has team key (hmk)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"hmk_team":"platform"}`),
	}
	hmk2 := &types.Issue{
		ID: "hmk-2", Title: "No metadata (hmk)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
	}

	// --- MultipleMetadataFieldsANDed data ---
	and1 := &types.Issue{
		ID: "and-1", Title: "Both match (and)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"and_team":"platform","and_sprint":"Q1"}`),
	}
	and2 := &types.Issue{
		ID: "and-2", Title: "Partial match (and)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"and_team":"platform","and_sprint":"Q2"}`),
	}

	// --- NoMetadataDoesNotMatch data ---
	nometa := &types.Issue{
		ID: "nometa-1", Title: "No metadata (nometa)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
	}

	// --- CreateIssue_WithMetadata data ---
	withmeta := &types.Issue{
		ID: "withmeta-1", Title: "Issue with metadata (withmeta)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"wm_team":"platform","wm_sprint":"Q1","wm_points":5}`),
	}

	// --- CreateIssue_WithMetadata_Queryable data ---
	queryable := &types.Issue{
		ID: "queryable-1", Title: "Queryable metadata (queryable)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"qm_team":"backend"}`),
	}

	// --- SlashKey data ---
	// Regression coverage for the slash-in-metadata-key bug: an unquoted
	// JSON path treats "/" as invalid path syntax, so this key must go
	// through JSONMetadataPath's always-quoted form and round-trip through
	// the real Dolt/go-mysql-server JSON path parser, not just a unit test
	// of the path string.
	slash1 := &types.Issue{
		ID: "slash-1", Title: "Slash key match (slash)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"jira/sprint":"Q1"}`),
	}
	slash2 := &types.Issue{
		ID: "slash-2", Title: "Slash key no match (slash)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"jira/sprint":"Q2"}`),
	}

	// --- MixedCaseKey data ---
	// Regression coverage for case-sensitive metadata keys: two issues with
	// keys differing only by case must not collide, proving the query path
	// preserves case end-to-end (parser, JSONMetadataPath, and the JSON
	// engine's own key comparison) rather than only at the parser layer.
	mixedcase1 := &types.Issue{
		ID: "mixedcase-1", Title: "Mixed-case key match (mixedcase)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"McTeam":"platform"}`),
	}
	mixedcase2 := &types.Issue{
		ID: "mixedcase-2", Title: "Different-case key no match (mixedcase)", Priority: 2,
		IssueType: types.TypeTask, Status: types.StatusOpen,
		Metadata: json.RawMessage(`{"mcteam":"platform"}`),
	}

	// Bulk create all issues
	allIssues := []*types.Issue{
		mfm1, mfm2, hmk1, hmk2, and1, and2, nometa, withmeta, queryable,
		slash1, slash2, mixedcase1, mixedcase2,
	}
	for _, issue := range allIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}

	t.Run("MetadataFieldMatch", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"mfm_team": "platform"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != mfm1.ID {
			t.Errorf("expected issue %s, got %s", mfm1.ID, results[0].ID)
		}
	})

	t.Run("MetadataFieldNoMatch", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"mfm_team": "backend"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("HasMetadataKey", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			HasMetadataKey: "hmk_team",
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != hmk1.ID {
			t.Errorf("expected issue %s, got %s", hmk1.ID, results[0].ID)
		}
	})

	t.Run("MultipleMetadataFieldsANDed", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{
				"and_team":   "platform",
				"and_sprint": "Q1",
			},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != and1.ID {
			t.Errorf("expected issue %s, got %s", and1.ID, results[0].ID)
		}
	})

	t.Run("MetadataFieldInvalidKey", func(t *testing.T) {
		_, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"'; DROP TABLE issues; --": "val"},
		})
		if err == nil {
			t.Fatal("expected error for invalid metadata key, got nil")
		}
	})

	t.Run("HasMetadataKeyInvalidKey", func(t *testing.T) {
		_, err := store.SearchIssues(ctx, "", types.IssueFilter{
			HasMetadataKey: "bad key!",
		})
		if err == nil {
			t.Fatal("expected error for invalid metadata key, got nil")
		}
	})

	t.Run("NoMetadataDoesNotMatch", func(t *testing.T) {
		// Search for a key that no issue has (unique to this test)
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"nometa_team": "platform"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for nonexistent metadata key, got %d", len(results))
		}
	})

	t.Run("CreateIssue_WithMetadata_Roundtrip", func(t *testing.T) {
		got, err := store.GetIssue(ctx, withmeta.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Metadata == nil {
			t.Fatal("expected metadata to be set, got nil")
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(got.Metadata, &parsed); err != nil {
			t.Fatalf("failed to parse metadata: %v", err)
		}
		if parsed["wm_team"] != "platform" {
			t.Errorf("expected wm_team=platform, got %v", parsed["wm_team"])
		}
		if parsed["wm_sprint"] != "Q1" {
			t.Errorf("expected wm_sprint=Q1, got %v", parsed["wm_sprint"])
		}
		// JSON numbers unmarshal as float64
		if parsed["wm_points"] != float64(5) {
			t.Errorf("expected wm_points=5, got %v", parsed["wm_points"])
		}
	})

	t.Run("CreateIssue_WithMetadata_Queryable", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"qm_team": "backend"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != queryable.ID {
			t.Errorf("expected issue %s, got %s", queryable.ID, results[0].ID)
		}
	})

	t.Run("MetadataFieldMatchSlashKey", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"jira/sprint": "Q1"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != slash1.ID {
			t.Errorf("expected issue %s, got %s", slash1.ID, results[0].ID)
		}
	})

	t.Run("MetadataFieldMatchMixedCaseKey", func(t *testing.T) {
		results, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"McTeam": "platform"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != mixedcase1.ID {
			t.Errorf("expected issue %s (key %q), got %s", mixedcase1.ID, "McTeam", results[0].ID)
		}

		// A differently-cased key ("mcteam") must not collide with "McTeam":
		// JSON object keys are case-sensitive, so querying the lowercase
		// variant must match only mixedcase2.
		lower, err := store.SearchIssues(ctx, "", types.IssueFilter{
			MetadataFields: map[string]string{"mcteam": "platform"},
		})
		if err != nil {
			t.Fatalf("SearchIssues: %v", err)
		}
		if len(lower) != 1 {
			t.Fatalf("expected 1 result, got %d", len(lower))
		}
		if lower[0].ID != mixedcase2.ID {
			t.Errorf("expected issue %s (key %q), got %s", mixedcase2.ID, "mcteam", lower[0].ID)
		}
	})
}

// Key validation unit tests (don't need a store)

func TestValidateMetadataKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key     string
		wantErr bool
	}{
		{"team", false},
		{"story_points", false},
		{"jira.sprint", false},
		{"jira/sprint", false},
		{"a/b/c", false},
		{"_private", false},
		{"CamelCase", false},
		{"a1b2c3", false},
		{"", true},
		{"bad key", true},
		{"bad-key", true},       // hyphens not allowed
		{"123start", true},      // must start with letter/underscore
		{"key=value", true},     // equals not allowed
		{"'; DROP TABLE", true}, // SQL injection
		{"$.path", true},        // JSON path chars not allowed
		{"key\nvalue", true},    // newlines not allowed
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			err := storage.ValidateMetadataKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMetadataKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}
