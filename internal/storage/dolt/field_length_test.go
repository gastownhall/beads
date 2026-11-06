package dolt

import (
	"errors"
	"strings"
	"testing"

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

// reporterTitle is the GH#8150 fixture: 499 characters, 514 bytes — one em
// dash, two en dashes, five middle dots and two primes. It is under 500
// characters, so it fits the issues.title VARCHAR(500) column (which counts
// characters) and only beads' own byte-based validation can refuse it.
func reporterTitle() string {
	return strings.Repeat("t", 489) + "—––·····′′"
}

func titleTestIssue(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "short title " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

// TestMapUpdateTitleValidation drives the canonical title rules through the
// map-based UpdateIssue path that bd edit, bd batch and bd rename take
// (GH#8150). Create and import refuse an empty or over-500-byte title, so the
// update path must refuse the same values or the store holds a row its own
// export/import later rejects. A refused update aborts whole: the companion
// description edit and the row version must be untouched too.
func TestMapUpdateTitleValidation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	t.Run("over-length multibyte title with a companion edit is refused whole", func(t *testing.T) {
		iss := titleTestIssue("tl-refuse")
		iss.Description = "original description"
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		before, err := store.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}

		err = store.UpdateIssue(ctx, iss.ID, map[string]interface{}{
			"title":       reporterTitle(),
			"description": "changed description",
		}, "tester")
		if !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("UpdateIssue with a 514-byte title: err = %v, want ErrValidation", err)
		}

		got, err := store.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatalf("GetIssue after refusal: %v", err)
		}
		if got.Title != before.Title {
			t.Errorf("title changed after a refused update: %q -> %q", before.Title, got.Title)
		}
		if got.Description != before.Description {
			t.Errorf("companion description changed after a refused update: %q -> %q", before.Description, got.Description)
		}
		if got.RowVersion != before.RowVersion {
			t.Errorf("RowVersion advanced after a refused update: %d -> %d", before.RowVersion, got.RowVersion)
		}
	})

	t.Run("empty title is refused", func(t *testing.T) {
		iss := titleTestIssue("tl-empty")
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := store.UpdateIssue(ctx, iss.ID, map[string]interface{}{"title": ""}, "tester"); !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("UpdateIssue with an empty title: err = %v, want ErrValidation", err)
		}
		got, err := store.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Title != iss.Title {
			t.Errorf("title changed after a refused empty-title update, got %+v", got)
		}
	})

	t.Run("non-string title is refused", func(t *testing.T) {
		iss := titleTestIssue("tl-nonstring")
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := store.UpdateIssue(ctx, iss.ID, map[string]interface{}{"title": 7}, "tester"); !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("UpdateIssue with an int title: err = %v, want ErrValidation", err)
		}
	})

	t.Run("wisp refuses the same invalid title", func(t *testing.T) {
		iss := titleTestIssue("tl-wisp")
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := store.UpdateIssue(ctx, iss.ID, map[string]interface{}{"wisp": true}, "tester"); err != nil {
			t.Fatalf("demote to wisp: %v", err)
		}
		err := store.UpdateIssue(ctx, iss.ID, map[string]interface{}{"title": reporterTitle()}, "tester")
		if !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("UpdateIssue with a 514-byte title on a wisp: err = %v, want ErrValidation", err)
		}
		got, err := store.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Title != iss.Title {
			t.Errorf("wisp title changed after a refused update, got %+v", got)
		}
	})
}

// TestMapUpdateTitleBoundary pins the exact admission edge on the map-update
// path: a changed title of exactly 500 bytes — ASCII or multibyte — is stored
// byte-identically and satisfies the same validation create and import apply,
// while one byte more is refused.
func TestMapUpdateTitleBoundary(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, tc := range []struct {
		name  string
		title string
	}{
		{"exactly 500 ASCII bytes", strings.Repeat("a", 500)},
		{"exactly 500 multibyte bytes, 250 × é", strings.Repeat("é", 250)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			iss := titleTestIssue("tl-boundary")
			if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			if err := store.UpdateIssue(ctx, iss.ID, map[string]interface{}{"title": tc.title}, "tester"); err != nil {
				t.Fatalf("UpdateIssue with a 500-byte title: %v", err)
			}
			got, err := store.GetIssue(ctx, iss.ID)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if got == nil || got.Title != tc.title {
				t.Fatalf("stored title is not byte-identical, got %+v", got)
			}
			// The stored row must satisfy the canonical rule the create and
			// import paths apply to the same value.
			if err := got.ValidateWithCustom(nil, nil); err != nil {
				t.Errorf("stored issue fails canonical validation: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		title string
	}{
		{"501 ASCII bytes", strings.Repeat("a", 501)},
		{"502 multibyte bytes, 251 × é", strings.Repeat("é", 251)},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			iss := titleTestIssue("tl-over")
			if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			if err := store.UpdateIssue(ctx, iss.ID, map[string]interface{}{"title": tc.title}, "tester"); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("UpdateIssue with an over-limit title: err = %v, want ErrValidation", err)
			}
			got, err := store.GetIssue(ctx, iss.ID)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if got == nil || got.Title != iss.Title {
				t.Errorf("title changed after a refused update, got %+v", got)
			}
		})
	}
}

// TestMapUpdateTitleLegacyRow covers rows whose stored title predates update
// validation (GH#8150): a map update may still change other fields while the
// invalid title is omitted or restated verbatim — the no-op filter drops an
// unchanged title before validation runs — while any newly applied invalid
// title stays refused and a changed valid title repairs the row.
func TestMapUpdateTitleLegacyRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seed := func(t *testing.T) string {
		t.Helper()
		iss := titleTestIssue("tl-legacy")
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		// Seed the legacy invalid title directly in the database, the way
		// rows written by bd 1.1.x update paths look: no beads validation
		// runs on a raw column write.
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", reporterTitle(), iss.ID); err != nil {
			t.Fatalf("seed legacy title: %v", err)
		}
		got, err := store.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Title != reporterTitle() {
			t.Fatalf("legacy seed failed: title = %+v", got)
		}
		return iss.ID
	}

	t.Run("description update with the title omitted succeeds", func(t *testing.T) {
		id := seed(t)
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"description": "legacy-safe edit"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue changing only the description of a legacy row: %v", err)
		}
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Title != reporterTitle() {
			t.Errorf("legacy title changed by a description-only update: got %d bytes, want unchanged", len(got.Title))
		}
		if got.Description != "legacy-safe edit" {
			t.Errorf("description not applied: %q", got.Description)
		}
	})

	t.Run("verbatim title restatement with another edit succeeds", func(t *testing.T) {
		id := seed(t)
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{
			"title":       reporterTitle(),
			"description": "restated edit",
		}, "tester"); err != nil {
			t.Fatalf("UpdateIssue restating the legacy title verbatim: %v", err)
		}
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Title != reporterTitle() {
			t.Errorf("restated legacy title changed: got %d bytes, want unchanged", len(got.Title))
		}
		if got.Description != "restated edit" {
			t.Errorf("companion edit not applied: %q", got.Description)
		}
	})

	t.Run("a newly applied invalid title stays refused", func(t *testing.T) {
		id := seed(t)
		changed := reporterTitle() + "x"
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"title": changed}, "tester"); !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("UpdateIssue applying a different 515-byte title to a legacy row: err = %v, want ErrValidation", err)
		}
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Title != reporterTitle() {
			t.Errorf("legacy title changed by a refused update, got %+v", got)
		}
	})

	t.Run("a changed valid title repairs the row", func(t *testing.T) {
		id := seed(t)
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"title": "repaired title"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue repairing the legacy title: %v", err)
		}
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got == nil || got.Title != "repaired title" {
			t.Fatalf("repaired title not stored, got %+v", got)
		}
		if err := got.ValidateWithCustom(nil, nil); err != nil {
			t.Errorf("repaired row fails canonical validation: %v", err)
		}
	})
}
