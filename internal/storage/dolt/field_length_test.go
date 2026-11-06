package dolt

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestFieldLengthValidation drives the assignee/owner/label VARCHAR(255) bounds
// through the real DoltStore. Over-length values are rejected up front with a
// typed types.ErrFieldTooLong — instead of a raw backend "data too long" error
// for assignee/owner, or (critically) a SILENT INSERT IGNORE truncation for
// labels — and nothing is persisted. Values at exactly the 255-character limit
// still succeed.
func TestFieldLengthValidation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	over := strings.Repeat("x", 300)                  // 300 runes > MaxFieldLen (255)
	atLimit := strings.Repeat("y", types.MaxFieldLen) // exactly 255 runes

	baseIssue := func(id string) *types.Issue {
		return &types.Issue{
			ID:        id,
			Title:     "field-length " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
	}

	t.Run("create with over-length label is rejected and not stored", func(t *testing.T) {
		iss := baseIssue("fl-label-over")
		iss.Labels = []string{over}
		err := store.CreateIssue(ctx, iss, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssue with 300-char label: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
		// The whole create ran in one transaction, so it rolled back: no label
		// row exists (not a silently truncated one) and the issue is absent.
		labels, err := store.GetLabels(ctx, "fl-label-over")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(labels) != 0 {
			t.Errorf("expected no labels stored after failed create, got %v", labels)
		}
		if got, _ := store.GetIssue(ctx, "fl-label-over"); got != nil {
			t.Errorf("expected issue to be absent after rejected create, got %+v", got)
		}
	})

	t.Run("create with over-length assignee is rejected and not stored", func(t *testing.T) {
		iss := baseIssue("fl-assignee-over")
		iss.Assignee = over
		err := store.CreateIssue(ctx, iss, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssue with 300-char assignee: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
		if got, _ := store.GetIssue(ctx, "fl-assignee-over"); got != nil {
			t.Errorf("expected issue to be absent after rejected create, got %+v", got)
		}
	})

	t.Run("create with over-length owner is rejected and not stored", func(t *testing.T) {
		iss := baseIssue("fl-owner-over")
		iss.Owner = over
		err := store.CreateIssue(ctx, iss, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssue with 300-char owner: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
		if got, _ := store.GetIssue(ctx, "fl-owner-over"); got != nil {
			t.Errorf("expected issue to be absent after rejected create, got %+v", got)
		}
	})

	t.Run("255-rune multibyte assignee round-trips (rune-count, not byte-count)", func(t *testing.T) {
		// "é" is 2 bytes, so 255 of them is 255 runes / 510 bytes: fits the
		// VARCHAR(255) column by character count and must survive unchanged.
		multibyte := strings.Repeat("é", types.MaxFieldLen)
		iss := baseIssue("fl-multibyte-ok")
		iss.Assignee = multibyte
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue with 255-rune multibyte assignee (%d bytes): %v", len(multibyte), err)
		}
		got, err := store.GetIssue(ctx, "fl-multibyte-ok")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Assignee != multibyte {
			t.Errorf("expected the 255-rune multibyte assignee stored intact, got %+v", got)
		}
	})

	t.Run("256-rune multibyte assignee is rejected", func(t *testing.T) {
		iss := baseIssue("fl-multibyte-over")
		iss.Assignee = strings.Repeat("é", types.MaxFieldLen+1)
		err := store.CreateIssue(ctx, iss, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssue with 256-rune multibyte assignee: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
	})

	t.Run("bulk create with one over-length field is rejected and nothing persisted", func(t *testing.T) {
		// The bulk path enforces the configured "test" ID prefix, so use it here.
		good := baseIssue("test-fl-bulk-good")
		bad := baseIssue("test-fl-bulk-bad")
		bad.Assignee = over
		err := store.CreateIssues(ctx, []*types.Issue{good, bad}, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssues with a 300-char assignee: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
		// The batch runs in one transaction, so a mid-batch rejection rolls back
		// the whole batch — neither issue is persisted.
		for _, id := range []string{"test-fl-bulk-good", "test-fl-bulk-bad"} {
			if got, _ := store.GetIssue(ctx, id); got != nil {
				t.Errorf("expected %s absent after rejected bulk create, got %+v", id, got)
			}
		}
	})

	t.Run("update assignee to over-length is rejected", func(t *testing.T) {
		iss := baseIssue("fl-update-assignee")
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		err := store.UpdateIssue(ctx, "fl-update-assignee", map[string]interface{}{"assignee": over}, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("UpdateIssue assignee=300 chars: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
	})

	t.Run("add-label with over-length label is rejected", func(t *testing.T) {
		iss := baseIssue("fl-addlabel")
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		err := store.AddLabel(ctx, "fl-addlabel", over, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("AddLabel with 300-char label: err = %v, want errors.Is(ErrFieldTooLong)", err)
		}
		labels, err := store.GetLabels(ctx, "fl-addlabel")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(labels) != 0 {
			t.Errorf("expected no labels stored after rejected AddLabel, got %v", labels)
		}
	})

	t.Run("values at the 255-char limit succeed", func(t *testing.T) {
		iss := baseIssue("fl-atlimit")
		iss.Assignee = atLimit
		iss.Labels = []string{atLimit}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue with 255-char assignee+label: %v", err)
		}
		labels, err := store.GetLabels(ctx, "fl-atlimit")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(labels) != 1 || labels[0] != atLimit {
			t.Errorf("expected the 255-char label stored intact, got %v", labels)
		}
		got, err := store.GetIssue(ctx, "fl-atlimit")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Assignee != atLimit {
			t.Errorf("expected the 255-char assignee stored intact, got %+v", got)
		}
	})
}

// TestMapUpdateTitleValidation drives the GH#8150 title rule through the real
// DoltStore's map-update path, the one bd edit, bd batch and bd rename take. A
// newly applied title must pass the same canonical check create and import
// run, or the store holds a row that its own export cannot import. The
// reporter's title (499 runes, 514 bytes) fits the VARCHAR(500) column, so
// only that check can refuse it.
func TestMapUpdateTitleValidation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	reporter := strings.Repeat("t", 489) + "—––·····′′"
	if runes, size := utf8.RuneCountInString(reporter), len(reporter); runes != 499 || size != 514 {
		t.Fatalf("reporter fixture is %d runes / %d bytes, want 499 / 514", runes, size)
	}

	create := func(t *testing.T, id string, ephemeral bool) *types.Issue {
		t.Helper()
		iss := &types.Issue{
			ID:          id,
			Title:       "title " + id,
			Description: "original description",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			Ephemeral:   ephemeral,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
		got, err := store.GetIssue(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("GetIssue %s: %v", id, err)
		}
		return got
	}

	for _, tc := range []struct {
		name      string
		title     interface{}
		ephemeral bool
	}{
		{"reporter title is refused", reporter, false},
		{"reporter title is refused on a wisp", reporter, true},
		{"empty title is refused", "", false},
		{"non-string title is refused", 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "tl-" + strings.ReplaceAll(tc.name, " ", "-")
			before := create(t, id, tc.ephemeral)
			err := store.UpdateIssue(ctx, id, map[string]interface{}{
				"title":       tc.title,
				"description": "companion description",
			}, "tester")
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("UpdateIssue title=%T: err = %v, want errors.Is(ErrValidation)", tc.title, err)
			}
			after, err := store.GetIssue(ctx, id)
			if err != nil || after == nil {
				t.Fatalf("GetIssue after refused update: %v", err)
			}
			if after.Title != before.Title || after.Description != before.Description || after.RowVersion != before.RowVersion {
				t.Errorf("refused update persisted: title %q->%q, description %q->%q, RowVersion %d->%d",
					before.Title, after.Title, before.Description, after.Description, before.RowVersion, after.RowVersion)
			}
		})
	}
}
