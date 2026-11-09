package issueops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// TestValidateIssueForInsertParityAndImmutability pins the contract the import
// pre-screen (GH#4492) depends on: ValidateIssueForInsert reports EXACTLY the
// rejection PrepareIssueForInsert would return at write time — same verdict,
// same error text — while mutating neither the row nor any of its pointees.
// The writer's validation remains the backstop; if the two ever disagree the
// screen starts rejecting records the writer accepts or waving through ones
// it refuses, and this test turns that drift into a failure.
func TestValidateIssueForInsertParityAndImmutability(t *testing.T) {
	// The screen runs before the writer; warn-mode metadata warnings must not
	// fire twice (nor fail the screen), so keep the default "none" mode.
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	defer config.ResetForTesting()

	now := time.Now().UTC()
	tz := time.FixedZone("UTC+2", 2*60*60)
	offsetTime := now.In(tz)

	base := func() types.Issue {
		return types.Issue{
			ID: "ivl-aaa", Title: "Valid row", Status: types.StatusOpen, IssueType: types.TypeTask,
			Priority: 2, CreatedAt: now, UpdatedAt: now,
		}
	}
	withCustomStatus := base()
	withCustomStatus.Status = types.Status("verify")
	withCustomType := base()
	withCustomType.IssueType = types.IssueType("epic-quest")
	invalidStatus := base()
	invalidStatus.Status = types.Status("deferred-forever")
	invalidType := base()
	invalidType.IssueType = types.IssueType("not-a-type")
	invalidTitle := base()
	invalidTitle.Title = ""
	invalidPriority := base()
	invalidPriority.Priority = 99
	closedNoClosedAt := base()
	closedNoClosedAt.Status = types.StatusClosed
	nonClosedWithClosedAt := base()
	nonClosedWithClosedAt.ClosedAt = &now
	ephemeralNoHistory := base()
	ephemeralNoHistory.Ephemeral = true
	ephemeralNoHistory.NoHistory = true
	badMetadataJSON := base()
	badMetadataJSON.Metadata = json.RawMessage(`{bad`)
	carriedArrivalSeq := base()
	carriedArrivalSeq.ArrivalSeq = 42
	offsetTimestamps := base()
	offsetTimestamps.CreatedAt = offsetTime
	offsetTimestamps.UpdatedAt = offsetTime
	offsetTimestamps.DueAt = &offsetTime

	cases := []struct {
		name         string
		issue        types.Issue
		customStatus []string
		customTypes  []string
	}{
		{"valid open row", base(), nil, nil},
		{"custom status from vocabulary", withCustomStatus, []string{"verify"}, nil},
		{"custom status not in vocabulary", invalidStatus, []string{"verify"}, nil},
		{"custom type from vocabulary", withCustomType, nil, []string{"epic-quest"}},
		{"custom type not in vocabulary", invalidType, nil, []string{"epic-quest"}},
		{"empty title", invalidTitle, nil, nil},
		{"invalid priority", invalidPriority, nil, nil},
		{"closed row filled closed_at by normalization", closedNoClosedAt, nil, nil},
		{"non-closed row with closed_at", nonClosedWithClosedAt, nil, nil},
		{"ephemeral plus no_history", ephemeralNoHistory, nil, nil},
		{"malformed metadata JSON", badMetadataJSON, nil, nil},
		{"carried arrival_seq is not an insert error", carriedArrivalSeq, nil, nil},
		{"non-UTC timestamps", offsetTimestamps, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The frozen pre-screen input: ValidateIssueForInsert must leave it
			// byte-identical. The snapshot marshals the row with its metadata
			// lifted out (a malformed-metadata row cannot marshal — that is the
			// very defect under test) and carries the metadata bytes alongside,
			// so mutations through either the row or its pointees still show.
			screened := tc.issue
			snapshot := func(i types.Issue) (string, string) {
				meta := append([]byte(nil), i.Metadata...)
				i.Metadata = nil
				b, err := json.Marshal(i)
				if err != nil {
					t.Fatalf("marshal snapshot: %v", err)
				}
				return string(b), string(meta)
			}
			before, metaBefore := snapshot(screened)

			screenErr := ValidateIssueForInsert(&screened, tc.customStatus, tc.customTypes)

			after, metaAfter := snapshot(screened)
			if before != after || metaBefore != metaAfter {
				t.Errorf("ValidateIssueForInsert mutated the row:\nbefore: %s %s\nafter:  %s %s", before, metaBefore, after, metaAfter)
			}
			if tc.issue.Status == types.StatusClosed && tc.issue.ClosedAt == nil && screened.ClosedAt != nil {
				t.Errorf("ValidateIssueForInsert filled closed_at on the screened row")
			}
			if tc.issue.ArrivalSeq == 0 && screened.ArrivalSeq != 0 {
				t.Errorf("ValidateIssueForInsert stamped arrival_seq on the screened row")
			}

			// Parity: the writer's own verdict on an identical row, same text.
			writerCopy := tc.issue
			prepareErr := PrepareIssueForInsert(&writerCopy, tc.customStatus, tc.customTypes)
			switch {
			case screenErr == nil && prepareErr == nil:
			case screenErr == nil && prepareErr != nil:
				t.Errorf("screen accepted a row the writer rejects (%v)", prepareErr)
			case screenErr != nil && prepareErr == nil:
				t.Errorf("screen rejected a row the writer accepts (%v)", screenErr)
			case screenErr.Error() != prepareErr.Error():
				t.Errorf("error text diverged:\nscreen:  %v\nwriter: %v", screenErr, prepareErr)
			}
		})
	}
}

// TestValidateIssueForInsertMetadataModes pins how the screen treats the
// metadata schema check in warn and error mode: warn mode stays the writer's
// to report (the screen skips it, or every import would print each warning
// twice), while error mode rejects up front with the writer's own text.
func TestValidateIssueForInsertMetadataModes(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	defer config.ResetForTesting()
	config.Set("validation.metadata.fields", map[string]interface{}{
		"required_field": map[string]interface{}{"type": "string", "required": true},
	})

	now := time.Now().UTC()
	lacking := types.Issue{
		ID: "ivl-md", Title: "Metadata row", Status: types.StatusOpen, IssueType: types.TypeTask,
		Priority: 2, CreatedAt: now, UpdatedAt: now,
		Metadata: json.RawMessage(`{"other":"value"}`),
	}

	t.Run("warn mode is left to the writer", func(t *testing.T) {
		config.Set("validation.metadata.mode", "warn")
		screened := lacking
		if err := ValidateIssueForInsert(&screened, nil, nil); err != nil {
			t.Fatalf("screen must skip warn-mode metadata validation, got: %v", err)
		}
		writerCopy := lacking
		if err := PrepareIssueForInsert(&writerCopy, nil, nil); err != nil {
			t.Fatalf("writer accepts warn-mode violations (warns only), got: %v", err)
		}
	})

	t.Run("error mode rejects with the writer's text", func(t *testing.T) {
		config.Set("validation.metadata.mode", "error")
		screened := lacking
		screenErr := ValidateIssueForInsert(&screened, nil, nil)
		if screenErr == nil || !strings.Contains(screenErr.Error(), "metadata validation failed") {
			t.Fatalf("want a metadata validation failure in error mode, got: %v", screenErr)
		}
		writerCopy := lacking
		prepareErr := PrepareIssueForInsert(&writerCopy, nil, nil)
		if prepareErr == nil || prepareErr.Error() != screenErr.Error() {
			t.Fatalf("error-mode text diverged:\nscreen:  %v\nwriter: %v", screenErr, prepareErr)
		}
	})
}
