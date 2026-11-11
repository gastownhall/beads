package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestScreenImportIssuesVocabularyError pins the screen's handling of a
// vocabulary it cannot read (#4492): the row that needs it is not rejected,
// the error names its line and the read failure, and no row comes back to
// write. The vocabulary is read lazily (never for built-in values) and once.
func TestScreenImportIssuesVocabularyError(t *testing.T) {
	recs, err := scanImportRecords(strings.NewReader(
		`{"id":"vs-1","title":"Built in","status":"open","issue_type":"task"}` + "\n" +
			"\n" +
			`{"id":"vs-2","title":"Custom","status":"verify","issue_type":"task"}` + "\n" +
			`{"id":"vs-3","title":"Custom too","status":"triage","issue_type":"task"}` + "\n"))
	if err != nil {
		t.Fatalf("scanImportRecords: %v", err)
	}

	t.Run("read error stops the screen", func(t *testing.T) {
		reads := 0
		readErr := errors.New("custom_statuses: table is corrupt")
		kept, rejected, err := screenImportIssues(context.Background(), recs.issues, recs.sources, func(context.Context) ([]string, []string, error) {
			reads++
			return nil, nil, readErr
		})
		if err == nil {
			t.Fatalf("screenImportIssues returned no error (kept %d, rejected %d)", len(kept), len(rejected))
		}
		if !errors.Is(err, readErr) {
			t.Fatalf("error %v does not wrap the read failure", err)
		}
		if !strings.Contains(err.Error(), "line 3 (issue vs-2)") {
			t.Fatalf("error %q does not name line 3 (issue vs-2)", err)
		}
		if kept != nil || rejected != nil {
			t.Fatalf("a failed screen returned rows: kept %v, rejected %v", kept, rejected)
		}
		if reads != 1 {
			t.Fatalf("vocabulary read %d times, want 1", reads)
		}
	})

	t.Run("built-in rows never read it", func(t *testing.T) {
		kept, rejected, err := screenImportIssues(context.Background(), recs.issues[:1], recs.sources, func(context.Context) ([]string, []string, error) {
			t.Fatal("vocabulary read for a row with a built-in status and type")
			return nil, nil, nil
		})
		if err != nil || len(kept) != 1 || len(rejected) != 0 {
			t.Fatalf("screenImportIssues = kept %d, rejected %d, err %v; want 1, 0, nil", len(kept), len(rejected), err)
		}
	})

	t.Run("one read serves every row", func(t *testing.T) {
		reads := 0
		kept, rejected, err := screenImportIssues(context.Background(), recs.issues, recs.sources, func(context.Context) ([]string, []string, error) {
			reads++
			return []string{"verify"}, nil, nil
		})
		if err != nil {
			t.Fatalf("screenImportIssues: %v", err)
		}
		if reads != 1 {
			t.Fatalf("vocabulary read %d times, want 1", reads)
		}
		if len(kept) != 2 || len(rejected) != 1 || rejected[0].Line != 4 || rejected[0].ID != "vs-3" {
			t.Fatalf("kept %d, rejected %+v; want vs-1 and vs-2 kept, vs-3 (line 4) rejected", len(kept), rejected)
		}
	})

	t.Run("a store without the capability says so", func(t *testing.T) {
		_, _, err := storeImportVocabulary(nil)(context.Background())
		if err == nil || !strings.Contains(err.Error(), "cannot read the custom status/type vocabulary") {
			t.Fatalf("storeImportVocabulary(nil) = %v, want a missing-capability error", err)
		}
	})

	if got := recs.issues[1].Status; got != types.Status("verify") {
		t.Fatalf("screening changed a row's status to %q", got)
	}
}
