package issueops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func preimageFixtureIssue() *types.Issue {
	return &types.Issue{
		ID:          "bd-pre1",
		Title:       "a title",
		Description: strings.Repeat("d", 4096),
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
}

// A claim's pre-image names exactly the two fields a claim rewrites: the old
// status, and null for the assignee the open issue did not carry.
func TestChangedFieldsPreImageClaimRecordsOnlyAssigneeAndStatus(t *testing.T) {
	got := ChangedFieldsPreImage(preimageFixtureIssue(), map[string]interface{}{
		"assignee": "worker", "status": "in_progress",
	})
	if want := `{"assignee":null,"status":"open"}`; got != want {
		t.Fatalf("pre-image = %s, want %s", got, want)
	}
}

// The projection drops everything the update does not touch: the 4 KB
// description never reaches old_value for a status change.
func TestChangedFieldsPreImageOmitsUntouchedFields(t *testing.T) {
	issue := preimageFixtureIssue()
	got := ChangedFieldsPreImage(issue, map[string]interface{}{"status": types.StatusClosed})
	if want := `{"status":"open"}`; got != want {
		t.Fatalf("pre-image = %s, want %s", got, want)
	}
	full, _ := json.Marshal(issue)
	if len(got) >= len(full)/50 {
		t.Fatalf("pre-image is %d bytes against a %d-byte issue; expected it to drop the description", len(got), len(full))
	}
}

// A changed field keeps the Issue's own JSON encoding, and keys sort, so the
// value is deterministic for the derived event id whatever the map order.
func TestChangedFieldsPreImageUsesIssueEncodingAndSortedKeys(t *testing.T) {
	issue := preimageFixtureIssue()
	issue.Assignee = "holder"
	got := ChangedFieldsPreImage(issue, map[string]interface{}{
		"title": "new", "priority": 0, "assignee": "", "created_at": time.Now(),
	})
	want := `{"assignee":"holder","created_at":"2026-10-30T12:00:00Z","priority":2,"title":"a title"}`
	if got != want {
		t.Fatalf("pre-image = %s, want %s", got, want)
	}
}

// The update-map aliases applyUpdateField accepts land under the Issue's field
// name, and a key that is no Issue field is recorded as null rather than
// dropped, so the pre-image still names what the update touched.
func TestChangedFieldsPreImageAliasesAndUnknownKeys(t *testing.T) {
	issue := preimageFixtureIssue()
	issue.Actor = "rhialto"
	got := ChangedFieldsPreImage(issue, map[string]interface{}{
		"event_actor": "someone", "not_a_field": 1,
	})
	if want := `{"actor":"rhialto","not_a_field":null}`; got != want {
		t.Fatalf("pre-image = %s, want %s", got, want)
	}
}

func TestChangedFieldsPreImageDegenerateInputs(t *testing.T) {
	if got := ChangedFieldsPreImage(nil, map[string]interface{}{"status": "x"}); got != "{}" {
		t.Fatalf("nil issue pre-image = %s, want {}", got)
	}
	if got := ChangedFieldsPreImage(preimageFixtureIssue(), nil); got != "{}" {
		t.Fatalf("empty updates pre-image = %s, want {}", got)
	}
}
