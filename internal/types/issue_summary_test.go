package types

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestIssueSummaryJSONTagsMatchIssue pins IssueSummary's wire shape to Issue's,
// field by field. IssueSummary is a narrow projection of Issue, so any field it
// carries must serialize under the same key with the same omitempty behavior —
// otherwise a summary-backed `bd list --json` and a full-Issue-backed one emit
// different documents for the same bead. Comparing against Issue's own tags
// rather than a hand-copied literal means the two cannot drift when a tag on
// Issue changes.
func TestIssueSummaryJSONTagsMatchIssue(t *testing.T) {
	issueTags := make(map[string]string)
	issueType := reflect.TypeOf(Issue{})
	for i := 0; i < issueType.NumField(); i++ {
		f := issueType.Field(i)
		issueTags[f.Name] = f.Tag.Get("json")
	}

	summaryType := reflect.TypeOf(IssueSummary{})
	for i := 0; i < summaryType.NumField(); i++ {
		f := summaryType.Field(i)
		want, ok := issueTags[f.Name]
		if !ok {
			t.Errorf("IssueSummary.%s has no same-named field on Issue; choose its json tag deliberately and update this test", f.Name)
			continue
		}
		if got := f.Tag.Get("json"); got != want {
			t.Errorf("IssueSummary.%s json tag = %q, want %q (must match Issue.%s)", f.Name, got, want, f.Name)
		}
	}
}

// TestIssueSummaryMarshalsIssueWireKeys is the concrete counterpart to the
// reflection test above: it marshals a fully-populated DURABLE summary and
// asserts the exact key set. An untagged struct would emit Go's default
// capitalized field names ("ID", "Title", …) and silently break every consumer
// parsing bd output, which is the regression this pins.
//
// The exact-count assertion carries a second claim now that IssueSummary also
// holds the wisp-plane markers: a durable bead must not sprout ephemeral,
// no_history, wisp_type or storage_class keys. The wisp direction is pinned by
// TestIssueSummaryWispKeysMatchIssue below.
func TestIssueSummaryMarshalsIssueWireKeys(t *testing.T) {
	closedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	summary := IssueSummary{
		ID:        "bd-1",
		Title:     "narrow projection",
		Status:    StatusClosed,
		Priority:  1,
		IssueType: TypeTask,
		Assignee:  "someone",
		Pinned:    true,
		Labels:    []string{"alpha"},
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		ClosedAt:  &closedAt,
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal IssueSummary: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal IssueSummary: %v", err)
	}

	want := []string{
		"id", "title", "status", "priority", "issue_type",
		"assignee", "pinned", "labels", "created_at", "updated_at", "closed_at",
	}
	for _, key := range want {
		if _, ok := decoded[key]; !ok {
			t.Errorf("marshaled IssueSummary missing key %q; got %s", key, encoded)
		}
	}
	if len(decoded) != len(want) {
		t.Errorf("marshaled IssueSummary has %d keys, want %d; got %s", len(decoded), len(want), encoded)
	}
}

// TestIssueSummaryOmitsEmptyLikeIssue pins the omitempty half of the contract:
// Priority carries no omitempty because 0 is a valid priority (P0/critical),
// while the optional fields drop out of the document entirely when unset. A
// summary that emitted "priority" only for non-zero values would make P0 beads
// indistinguishable from unset ones in `bd list --json`.
func TestIssueSummaryOmitsEmptyLikeIssue(t *testing.T) {
	encoded, err := json.Marshal(IssueSummary{ID: "bd-1", Title: "t"})
	if err != nil {
		t.Fatalf("marshal zero-value IssueSummary: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal IssueSummary: %v", err)
	}

	for _, key := range []string{"id", "title", "priority", "created_at", "updated_at"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("zero-value IssueSummary should still emit %q; got %s", key, encoded)
		}
	}
	for _, key := range []string{"status", "issue_type", "assignee", "pinned", "labels", "closed_at"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("zero-value IssueSummary should omit %q; got %s", key, encoded)
		}
	}
}

// wispMarkerFields are the Issue fields that identify a row as living on the
// wisp plane. issueops.searchInTx merges wisp rows into every SearchIssueSummaries
// result that does not set SkipWisps, so these travel the summary path too.
var wispMarkerFields = []string{"Ephemeral", "NoHistory", "WispType", "StorageClass"}

// TestIssueSummaryCarriesWispMarkers pins the wisp half of the wire-shape
// contract the doc comment on IssueSummary states.
//
// The durable-row half is already covered: TestIssueSummaryJSONTagsMatchIssue
// checks that every field IssueSummary *has* serializes like Issue's. That
// leaves the fields it does *not* have unchecked, and one group of those is
// not optional — issueops.searchInTx merges the wisps table into every
// non-SkipWisps result, so a summary-backed `bd list --json` renders wisp rows
// alongside durable ones. A marker Issue carries and IssueSummary drops
// disappears from that document with no other signal, which is exactly the
// "every consumer parsing bd output breaks silently" failure the doc comment
// promises against.
//
// Type equality is asserted as well as the tag: WispType and StorageClass are
// named string types whose zero values marshal identically to a plain string,
// so a field declared as string here would satisfy a tag-only check while
// still dropping the type's own validation surface.
func TestIssueSummaryCarriesWispMarkers(t *testing.T) {
	issueType := reflect.TypeOf(Issue{})
	summaryType := reflect.TypeOf(IssueSummary{})

	for _, name := range wispMarkerFields {
		issueField, ok := issueType.FieldByName(name)
		if !ok {
			t.Fatalf("Issue has no %s field; update wispMarkerFields to match the wisp plane's markers", name)
		}

		summaryField, ok := summaryType.FieldByName(name)
		if !ok {
			t.Errorf("IssueSummary has no %s field: searchInTx merges wisp rows into every non-SkipWisps "+
				"SearchIssueSummaries result, so a summary-backed `bd list --json` would drop %q for those rows",
				name, issueField.Tag.Get("json"))
			continue
		}
		if got, want := summaryField.Tag.Get("json"), issueField.Tag.Get("json"); got != want {
			t.Errorf("IssueSummary.%s json tag = %q, want %q (must match Issue.%s)", name, got, want, name)
		}
		if got, want := summaryField.Type, issueField.Type; got != want {
			t.Errorf("IssueSummary.%s type = %v, want %v (must match Issue.%s)", name, got, want, name)
		}
	}
}

// TestIssueSummaryWispKeysMatchIssue is the concrete counterpart to
// TestIssueSummaryCarriesWispMarkers: the reflection test proves the fields
// exist with matching tags, this one proves a wisp row actually serializes the
// same through both types. It marshals the same marker values as an Issue and
// as an IssueSummary and compares the four keys byte for byte, so a divergence
// in omitempty behavior, key name, or the named types' own marshaling shows up
// as a value mismatch rather than passing on field presence alone.
//
// The durable case is included deliberately: parity means a durable bead emits
// no marker keys through either type, not merely that a wisp emits some.
func TestIssueSummaryWispKeysMatchIssue(t *testing.T) {
	// Ephemeral and NoHistory are mutually exclusive on the wisp plane
	// (Issue.Validate rejects both set), so they get one row each rather than
	// one row setting both.
	cases := []struct {
		name         string
		ephemeral    bool
		noHistory    bool
		wispType     WispType
		storageClass StorageClass
	}{
		{
			name:         "ephemeral heartbeat wisp",
			ephemeral:    true,
			wispType:     WispTypeHeartbeat,
			storageClass: StorageClassEphemeral,
		},
		{
			name:         "no-history escalation wisp",
			noHistory:    true,
			wispType:     WispTypeEscalation,
			storageClass: StorageClassEphemeral,
		},
		{
			name: "durable bead emits no marker keys",
		},
	}

	markerKeys := []string{"ephemeral", "no_history", "wisp_type", "storage_class"}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			issue := Issue{
				ID:           "bd-1",
				Title:        "narrow projection",
				Status:       StatusOpen,
				Priority:     2,
				IssueType:    TypeTask,
				Ephemeral:    tc.ephemeral,
				NoHistory:    tc.noHistory,
				WispType:     tc.wispType,
				StorageClass: tc.storageClass,
			}
			summary := IssueSummary{
				ID:           issue.ID,
				Title:        issue.Title,
				Status:       issue.Status,
				Priority:     issue.Priority,
				IssueType:    issue.IssueType,
				Ephemeral:    tc.ephemeral,
				NoHistory:    tc.noHistory,
				WispType:     tc.wispType,
				StorageClass: tc.storageClass,
			}

			fromIssue := marshalToKeys(t, issue)
			fromSummary := marshalToKeys(t, summary)

			for _, key := range markerKeys {
				want, inIssue := fromIssue[key]
				got, inSummary := fromSummary[key]
				if inIssue != inSummary {
					t.Errorf("key %q present in Issue=%v, in IssueSummary=%v; a summary-backed "+
						"`bd list --json` must emit the same marker keys a full-Issue-backed one does",
						key, inIssue, inSummary)
					continue
				}
				if inIssue && string(got) != string(want) {
					t.Errorf("key %q = %s through IssueSummary, want %s (Issue's value)", key, got, want)
				}
			}
		})
	}
}

// marshalToKeys marshals v and returns its top-level keys, so two documents
// can be compared key by key without depending on field order.
func marshalToKeys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal %T: %v", v, err)
	}
	return decoded
}
