package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func clausesContain(clauses []string, want string) bool {
	for _, c := range clauses {
		if c == want {
			return true
		}
	}
	return false
}

func argsContain(args []interface{}, want interface{}) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// Without IndexedMetadataKeys, a metadata-field equality filter falls back to a
// JSON_EXTRACT scan (preserving legacy behavior).
func TestBuildIssueFilterClauses_MetadataField_JSONFallback(t *testing.T) {
	filter := types.IssueFilter{
		MetadataFields: map[string]string{"alias": "gastown.boot"},
	}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	if !clausesContain(clauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?") {
		t.Fatalf("expected JSON fallback clause, got: %v", clauses)
	}
	if !argsContain(args, "$.alias") || !argsContain(args, "gastown.boot") {
		t.Fatalf("expected JSON path + value in args, got: %v", args)
	}
}

// With the key marked indexed, the filter targets the generated column so Dolt
// uses the index instead of scanning every row's JSON (Dolt does not rewrite
// the JSON_EXTRACT form to the index — verified empirically).
func TestBuildIssueFilterClauses_MetadataField_IndexedColumn(t *testing.T) {
	filter := types.IssueFilter{
		MetadataFields:      map[string]string{"alias": "gastown.boot"},
		IndexedMetadataKeys: map[string]bool{"alias": true},
	}
	clauses, args, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	if !clausesContain(clauses, "bd_md_alias = ?") {
		t.Fatalf("expected indexed-column clause, got: %v", clauses)
	}
	if !argsContain(args, "gastown.boot") {
		t.Fatalf("expected value in args, got: %v", args)
	}
	if argsContain(args, "$.alias") {
		t.Fatalf("indexed path must not bind a JSON path arg, got: %v", args)
	}
}

// HasMetadataKey uses the indexed column for an IS NOT NULL existence check.
func TestBuildIssueFilterClauses_HasMetadataKey_IndexedColumn(t *testing.T) {
	filter := types.IssueFilter{
		HasMetadataKey:      "alias",
		IndexedMetadataKeys: map[string]bool{"alias": true},
	}
	clauses, _, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	if !clausesContain(clauses, "bd_md_alias IS NOT NULL") {
		t.Fatalf("expected indexed existence clause, got: %v", clauses)
	}
}

// A non-indexed key still uses JSON even when other keys are indexed.
func TestBuildIssueFilterClauses_MixedIndexedAndFallback(t *testing.T) {
	filter := types.IssueFilter{
		MetadataFields:      map[string]string{"alias": "x", "note": "y"},
		IndexedMetadataKeys: map[string]bool{"alias": true},
	}
	clauses, _, err := BuildIssueFilterClauses("", filter, IssuesFilterTables)
	if err != nil {
		t.Fatalf("BuildIssueFilterClauses: %v", err)
	}
	if !clausesContain(clauses, "bd_md_alias = ?") {
		t.Fatalf("expected indexed clause for alias, got: %v", clauses)
	}
	if !clausesContain(clauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?") {
		t.Fatalf("expected JSON fallback clause for note, got: %v", clauses)
	}
}
