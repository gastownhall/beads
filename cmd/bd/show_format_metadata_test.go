package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestFormatIssueCustomMetadata_Nil(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{}
	result := formatIssueCustomMetadata(issue)
	if result != "" {
		t.Errorf("expected empty string for nil metadata, got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_EmptyObject(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{}`)}
	result := formatIssueCustomMetadata(issue)
	if result != "" {
		t.Errorf("expected empty string for {} metadata, got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_NullLiteral(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`null`)}
	result := formatIssueCustomMetadata(issue)
	if result != "" {
		t.Errorf("expected empty string for null metadata, got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_SingleScalar(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"team":"platform"}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, "METADATA") {
		t.Errorf("expected METADATA header, got: %q", result)
	}
	if !strings.Contains(result, "team: platform") {
		t.Errorf("expected 'team: platform', got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_MultipleKeys(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"sprint":"Q1","team":"platform","version":"1.0"}`)}
	result := formatIssueCustomMetadata(issue)
	// Keys should be sorted alphabetically
	sprintIdx := strings.Index(result, "sprint:")
	teamIdx := strings.Index(result, "team:")
	versionIdx := strings.Index(result, "version:")
	if sprintIdx < 0 || teamIdx < 0 || versionIdx < 0 {
		t.Fatalf("expected all three keys in output, got: %q", result)
	}
	if !(sprintIdx < teamIdx && teamIdx < versionIdx) {
		t.Errorf("expected keys in sorted order (sprint < team < version), got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_NumberValue(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"story_points":5}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, "story_points: 5") {
		t.Errorf("expected 'story_points: 5', got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_FloatValue(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"score":3.14}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, "score: 3.14") {
		t.Errorf("expected 'score: 3.14', got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_BoolValue(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"reviewed":true}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, "reviewed: true") {
		t.Errorf("expected 'reviewed: true', got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_NullValue(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"optional":null}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, "optional: null") {
		t.Errorf("expected 'optional: null', got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_ArrayValue(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"files":["a.go","b.go"]}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, `files: ["a.go","b.go"]`) {
		t.Errorf("expected compact JSON array, got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_NestedObject(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`{"jira":{"sprint":"Q1","points":3}}`)}
	result := formatIssueCustomMetadata(issue)
	if !strings.Contains(result, "jira:") {
		t.Errorf("expected 'jira:' key, got: %q", result)
	}
	// Nested object rendered as compact JSON
	if !strings.Contains(result, "sprint") && !strings.Contains(result, "points") {
		t.Errorf("expected nested object content, got: %q", result)
	}
}

func TestFormatIssueCustomMetadata_InvalidJSON(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{Metadata: json.RawMessage(`not-json`)}
	result := formatIssueCustomMetadata(issue)
	// Should still render something (raw fallback), not panic
	if !strings.Contains(result, "METADATA") {
		t.Errorf("expected METADATA header for raw fallback, got: %q", result)
	}
}

func TestHasCustomMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		metadata json.RawMessage
		want     bool
	}{
		{"nil", nil, false},
		{"empty bytes", json.RawMessage{}, false},
		{"empty object", json.RawMessage(`{}`), false},
		{"null", json.RawMessage(`null`), false},
		{"has data", json.RawMessage(`{"key":"val"}`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{Metadata: tt.metadata}
			if got := hasCustomMetadata(issue); got != tt.want {
				t.Errorf("hasCustomMetadata() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountMetadataKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		metadata json.RawMessage
		want     int
	}{
		{"nil", nil, 0},
		{"empty object", json.RawMessage(`{}`), 0},
		{"one key", json.RawMessage(`{"a":"b"}`), 1},
		{"three keys", json.RawMessage(`{"a":1,"b":2,"c":3}`), 3},
		{"invalid json", json.RawMessage(`not-json`), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{Metadata: tt.metadata}
			if got := countMetadataKeys(issue); got != tt.want {
				t.Errorf("countMetadataKeys() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFormatIssueLong_WithMetadata(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{
		ID:        "test-meta1",
		Title:     "Issue With Metadata",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{"team":"platform","sprint":"Q1"}`),
	}
	var buf strings.Builder
	formatIssueLong(&buf, issue, nil)
	result := buf.String()
	if !strings.Contains(result, "Metadata: 2 keys") {
		t.Errorf("expected 'Metadata: 2 keys' in long format, got: %q", result)
	}
}

func TestFormatIssueLong_WithoutMetadata(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{
		ID:        "test-nometa",
		Title:     "Issue Without Metadata",
		Priority:  1,
		IssueType: types.TypeBug,
		Status:    types.StatusOpen,
	}
	var buf strings.Builder
	formatIssueLong(&buf, issue, nil)
	result := buf.String()
	if strings.Contains(result, "Metadata:") {
		t.Errorf("expected no Metadata line for issue without metadata, got: %q", result)
	}
}

func TestFormatIssueLong_NonObjectMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"array", json.RawMessage(`[1,2,3]`)},
		{"string", json.RawMessage(`"hello"`)},
		{"number", json.RawMessage(`42`)},
		{"bool", json.RawMessage(`true`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{
				ID:        "test-nonobj",
				Title:     "Non-object metadata",
				Priority:  2,
				IssueType: types.TypeTask,
				Status:    types.StatusOpen,
				Metadata:  tt.metadata,
			}
			var buf strings.Builder
			formatIssueLong(&buf, issue, nil)
			result := buf.String()
			if !strings.Contains(result, "Metadata: set") {
				t.Errorf("expected 'Metadata: set' for %s metadata, got: %q", tt.name, result)
			}
			if strings.Contains(result, "0 keys") {
				t.Errorf("should not show '0 keys' for %s metadata, got: %q", tt.name, result)
			}
		})
	}
}

func TestFormatIssueLong_EmptyMetadata(t *testing.T) {
	t.Parallel()
	issue := &types.Issue{
		ID:        "test-emptymeta",
		Title:     "Issue With Empty Metadata",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Metadata:  json.RawMessage(`{}`),
	}
	var buf strings.Builder
	formatIssueLong(&buf, issue, nil)
	result := buf.String()
	if strings.Contains(result, "Metadata:") {
		t.Errorf("expected no Metadata line for empty metadata, got: %q", result)
	}
}
