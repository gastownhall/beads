package issueops

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
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

// TestValidateIssueForInsertLeavesTheMetadataWarningToTheWriter pins the
// screen's metadata check to the writer's verdict without the writer's output.
// Under validation.metadata.mode warn a record that breaks the schema passes
// both, and only the writer prints the warning, so an import that screens a
// record and then writes it shows the warning once, not twice. Under error
// mode both refuse the record with the same error.
func TestValidateIssueForInsertLeavesTheMetadataWarningToTheWriter(t *testing.T) {
	// t.Setenv forces non-parallel; ignore the repo's tracked config so only the
	// schema set below is in effect.
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	defer config.ResetForTesting()
	config.Set("validation.metadata.fields", map[string]interface{}{
		"required_field": map[string]interface{}{"type": "string", "required": true},
	})
	for _, tc := range []struct {
		mode        string
		writerWarns int
		refused     bool
	}{
		{mode: "warn", writerWarns: 1},
		{mode: "error", refused: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			config.Set("validation.metadata.mode", tc.mode)
			issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{}`)}
			var screenErr, writerErr error
			screenOut := captureStderr(t, func() { screenErr = ValidateIssueForInsert(issue, nil, nil) })
			writerCopy := *issue
			writerOut := captureStderr(t, func() { writerErr = PrepareIssueForInsert(&writerCopy, nil, nil) })
			if screenOut != "" {
				t.Errorf("ValidateIssueForInsert printed %q, want nothing: the writer prints the warning", screenOut)
			}
			if got := strings.Count(writerOut, "warning: "); got != tc.writerWarns {
				t.Errorf("PrepareIssueForInsert printed %d warnings (%q), want %d", got, writerOut, tc.writerWarns)
			}
			if (screenErr != nil) != tc.refused || (writerErr != nil) != tc.refused {
				t.Fatalf("ValidateIssueForInsert() = %v, writer said %v; want refused=%v", screenErr, writerErr, tc.refused)
			}
			if tc.refused && screenErr.Error() != writerErr.Error() {
				t.Errorf("ValidateIssueForInsert() = %v, writer said %v", screenErr, writerErr)
			}
		})
	}
}

// captureStderr returns what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	func() {
		defer func() { os.Stderr = orig }()
		fn()
	}()
	_ = w.Close()
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
