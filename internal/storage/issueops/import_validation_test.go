package issueops

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestValidateIssueForInsertMatchesTheWriter pins the import screen to the
// writer: every record PrepareIssueForInsert refuses, ValidateIssueForInsert
// refuses with the same error, and every record it accepts is accepted.
func TestValidateIssueForInsertMatchesTheWriter(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	closed := created.Add(time.Hour)
	estimate := -1
	valid := func() *types.Issue {
		return &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: created, UpdatedAt: created}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*types.Issue)
		custom []string
		types  []string
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "closed without closed_at gets one", mutate: func(i *types.Issue) { i.Status = types.StatusClosed }, ok: true},
		{name: "optional timestamp", mutate: func(i *types.Issue) { i.Status = types.StatusClosed; i.ClosedAt = &closed }, ok: true},
		{name: "unknown status", mutate: func(i *types.Issue) { i.Status = "verify" }},
		{name: "custom status", mutate: func(i *types.Issue) { i.Status = "verify" }, custom: []string{"verify"}, ok: true},
		{name: "unknown type", mutate: func(i *types.Issue) { i.IssueType = "saga" }},
		{name: "custom type", mutate: func(i *types.Issue) { i.IssueType = "saga" }, types: []string{"saga"}, ok: true},
		{name: "empty title", mutate: func(i *types.Issue) { i.Title = "" }},
		{name: "priority out of range", mutate: func(i *types.Issue) { i.Priority = 9 }},
		{name: "negative estimate", mutate: func(i *types.Issue) { i.EstimatedMinutes = &estimate }},
		{name: "closed_at on open issue", mutate: func(i *types.Issue) { i.ClosedAt = &closed }},
		{name: "malformed metadata", mutate: func(i *types.Issue) { i.Metadata = []byte("{") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := valid()
			if tc.mutate != nil {
				tc.mutate(issue)
			}
			before := *issue
			got := ValidateIssueForInsert(issue, tc.custom, tc.types)
			if !reflect.DeepEqual(*issue, before) {
				t.Fatalf("ValidateIssueForInsert changed its input:\n got %+v\nwant %+v", *issue, before)
			}
			writerCopy := before
			want := PrepareIssueForInsert(&writerCopy, tc.custom, tc.types)
			if (got == nil) != tc.ok {
				t.Fatalf("ValidateIssueForInsert() = %v, want ok=%v", got, tc.ok)
			}
			if (got == nil) != (want == nil) || (got != nil && got.Error() != want.Error()) {
				t.Fatalf("ValidateIssueForInsert() = %v, writer said %v", got, want)
			}
		})
	}
}

// TestValidateIssueForInsertRefusesAnOverLengthLabel covers the label check the
// writer applies per label (AddLabelInTx), which PrepareIssueForInsert does
// not: the screen must refuse the record too.
func TestValidateIssueForInsertRefusesAnOverLengthLabel(t *testing.T) {
	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"ok", strings.Repeat("l", 300)}}
	err := ValidateIssueForInsert(issue, nil, nil)
	if !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("ValidateIssueForInsert() = %v, want ErrFieldTooLong", err)
	}
	if !strings.Contains(err.Error(), "bd-1") {
		t.Fatalf("error %q does not name the issue", err)
	}
}
