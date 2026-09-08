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

// TestShowJSONDetailsCarryTheRevisionToken pins what `bd show --json` puts on
// the wire, from the type the command now marshals directly.
//
// The CLI-only projection wrapper this used to test is gone: the token is a
// field of types.IssueDetails, set by types.NewIssueDetails, and both front
// doors get it from the one detail seam. What is still worth asserting HERE is
// the CLI's half of the deal — that the command marshals a detail view built
// through that constructor and that no storage spelling rides along with it.
func TestShowJSONDetailsCarryTheRevisionToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token int64
		want  string
	}{
		{"a mutated row", 123456789, `"revision":123456789`},
		// 0 is the migration-0054 backfill token, a legitimate CAS value a
		// guarded client must be able to read. No omitempty, so it is emitted
		// rather than standing in for "this producer has no token".
		{"a legacy un-mutated row", 0, `"revision":0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			details := types.NewIssueDetails(types.Issue{
				ID:         "be-revision",
				Title:      "Versioned issue",
				RowVersion: tc.token,
			})

			data, err := json.Marshal(details)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			js := string(data)
			if !strings.Contains(js, tc.want) {
				t.Errorf("expected %s in show JSON, got: %s", tc.want, js)
			}
			for _, forbidden := range []string{"row_version", "RowVersion", "row_lock"} {
				if strings.Contains(js, forbidden) {
					t.Errorf("show JSON leaked storage field %q: %s", forbidden, js)
				}
			}
		})
	}
}
