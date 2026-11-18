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

	// The admission edge: a changed title of exactly 500 bytes, ASCII or
	// multibyte, is stored byte-identically and passes the validation create
	// and import run, so an export of it imports back. One byte more is
	// refused with the stored title untouched.
	for _, tc := range []struct {
		name  string
		id    string
		title string
	}{
		{"500 ASCII bytes are stored byte-identically", "tl-at-limit-ascii", strings.Repeat("t", 500)},
		{"500-byte multibyte title is stored byte-identically", "tl-at-limit", strings.Repeat("é", 250)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create(t, tc.id, false)
			if err := store.UpdateIssue(ctx, tc.id, map[string]interface{}{"title": tc.title}, "tester"); err != nil {
				t.Fatalf("UpdateIssue with a 500-byte title: %v", err)
			}
			got, err := store.GetIssue(ctx, tc.id)
			if err != nil || got == nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if got.Title != tc.title {
				t.Fatalf("stored title is %d bytes, want the 500-byte title byte-identical", len(got.Title))
			}
			if err := got.ValidateWithCustom(nil, nil); err != nil {
				t.Fatalf("stored issue fails create/import validation: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		id    string
		title string
	}{
		{"501 ASCII bytes are refused", "tl-over-ascii", strings.Repeat("t", 501)},
		{"502 multibyte bytes are refused", "tl-over-multibyte", strings.Repeat("é", 251)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := create(t, tc.id, false)
			if err := store.UpdateIssue(ctx, tc.id, map[string]interface{}{"title": tc.title}, "tester"); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("UpdateIssue with a %d-byte title: err = %v, want errors.Is(ErrValidation)", len(tc.title), err)
			}
			got, err := store.GetIssue(ctx, tc.id)
			if err != nil || got == nil {
				t.Fatalf("GetIssue after refused update: %v", err)
			}
			if got.Title != before.Title {
				t.Errorf("refused update changed the title to %d bytes, want %q", len(got.Title), before.Title)
			}
		})
	}

	t.Run("legacy oversized title stays editable when not changed", func(t *testing.T) {
		// Seed the row an older bd could write: the store refuses this title
		// now, so it goes in by raw SQL, in this test's isolated database only.
		create(t, "tl-legacy", false)
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", reporter, "tl-legacy"); err != nil {
			t.Fatalf("seed legacy title: %v", err)
		}
		for _, step := range []struct {
			name    string
			updates map[string]interface{}
		}{
			{"title omitted", map[string]interface{}{"description": "edited with title omitted"}},
			{"title restated", map[string]interface{}{"title": reporter, "description": "edited with title restated"}},
		} {
			if err := store.UpdateIssue(ctx, "tl-legacy", step.updates, "tester"); err != nil {
				t.Fatalf("%s: UpdateIssue on a legacy row: %v", step.name, err)
			}
			got, err := store.GetIssue(ctx, "tl-legacy")
			if err != nil || got == nil {
				t.Fatalf("%s: GetIssue: %v", step.name, err)
			}
			if got.Title != reporter || got.Description != step.updates["description"] {
				t.Fatalf("%s: got title %d bytes / description %q, want the legacy title kept and description %q",
					step.name, len(got.Title), got.Description, step.updates["description"])
			}
		}
		// A changed title still answers to the rule, and a valid one repairs the row.
		if err := store.UpdateIssue(ctx, "tl-legacy", map[string]interface{}{"title": reporter + "x"}, "tester"); !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("changing a legacy title to another oversized title: err = %v, want errors.Is(ErrValidation)", err)
		}
		if got, err := store.GetIssue(ctx, "tl-legacy"); err != nil || got == nil || got.Title != reporter {
			t.Fatalf("after the refused replacement: got %+v, err %v; want the legacy title kept", got, err)
		}
		if err := store.UpdateIssue(ctx, "tl-legacy", map[string]interface{}{"title": "repaired title"}, "tester"); err != nil {
			t.Fatalf("repairing the legacy title: %v", err)
		}
		got, err := store.GetIssue(ctx, "tl-legacy")
		if err != nil || got == nil || got.Title != "repaired title" {
			t.Fatalf("after repair: got %+v, err %v; want title %q", got, err, "repaired title")
		}
		if err := got.ValidateWithCustom(nil, nil); err != nil {
			t.Fatalf("repaired row fails create/import validation: %v", err)
		}
	})
}
