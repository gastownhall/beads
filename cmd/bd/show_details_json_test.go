package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIssueDetailsCountOnlyJSON is the regression guard for be-ijck6q:
// the default bd show --json output must emit dependent_count / comment_count
// as count-only fields and must NOT include a "dependents" or "comments" key
// when --include-dependents / --include-comments are not given.
func TestIssueDetailsCountOnlyJSON(t *testing.T) {
	depCount := int64(42)
	depnCount := int64(3)
	cmtCount := int64(7)
	details := &types.IssueDetails{
		Issue: types.Issue{
			ID:    "be-abc",
			Title: "Test issue",
		},
		DependentCount:  &depCount,
		DependencyCount: &depnCount,
		CommentCount:    &cmtCount,
		// Dependents and Comments intentionally nil (count-only mode)
	}

	data, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(data)

	// Count fields must be present.
	if !strings.Contains(js, `"dependent_count":42`) {
		t.Errorf("expected dependent_count:42 in JSON, got: %s", js)
	}
	if !strings.Contains(js, `"dependency_count":3`) {
		t.Errorf("expected dependency_count:3 in JSON, got: %s", js)
	}
	if !strings.Contains(js, `"comment_count":7`) {
		t.Errorf("expected comment_count:7 in JSON, got: %s", js)
	}

	// Slice fields must be absent (omitempty, nil → omitted).
	if strings.Contains(js, `"dependents"`) {
		t.Errorf("expected no dependents key in count-only output, got: %s", js)
	}
	if strings.Contains(js, `"comments"`) {
		t.Errorf("expected no comments key in count-only output, got: %s", js)
	}
}
