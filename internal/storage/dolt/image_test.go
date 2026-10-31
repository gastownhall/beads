package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// imageOf reads one image through issueops.ImageInTx in its own transaction.
func imageOf(t *testing.T, ctx context.Context, store *DoltStore, id string) publicops.Image {
	t.Helper()
	images, err := imagesOf(ctx, store, id)
	if err != nil {
		t.Fatalf("image %s: %v", id, err)
	}
	if len(images) != 1 {
		t.Fatalf("image %s: got %d images", id, len(images))
	}
	return images[0]
}

func imagesOf(ctx context.Context, store *DoltStore, ids ...string) ([]publicops.Image, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return issueops.ImageInTx(ctx, tx, ids)
}

func newImageIssue(t *testing.T, ctx context.Context, store *DoltStore, title string, ephemeral bool) *types.Issue {
	t.Helper()
	issue := &types.Issue{
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return issue
}

// TestImageRevisionChangesOnEveryWrite walks one bead through each kind of
// write the archive guard must notice and checks that each moves the revision,
// while two reads with nothing between them agree.
func TestImageRevisionChangesOnEveryWrite(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	bead := newImageIssue(t, ctx, store, "imaged bead", false)
	other := newImageIssue(t, ctx, store, "depends on the imaged bead", false)

	first := imageOf(t, ctx, store, bead.ID)
	if again := imageOf(t, ctx, store, bead.ID); again.Revision != first.Revision {
		t.Fatalf("two reads differ: %s vs %s", first.Revision, again.Revision)
	}
	if first.Table != "issues" || first.Row["id"] != bead.ID {
		t.Fatalf("image header = %q, row id %v", first.Table, first.Row["id"])
	}

	prev := first
	step := func(name string, write func() error, check func(publicops.Image) bool) {
		t.Helper()
		if err := write(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img := imageOf(t, ctx, store, bead.ID)
		if img.Revision == prev.Revision {
			t.Fatalf("%s did not change the revision %s", name, img.Revision)
		}
		if check != nil && !check(img) {
			b, _ := issueops.CanonicalImageJSON(img)
			t.Fatalf("%s: image does not show the write: %s", name, b)
		}
		prev = img
	}
	hasLabel := func(img publicops.Image, label string) bool {
		for _, row := range img.Labels {
			if row["label"] == label {
				return true
			}
		}
		return false
	}

	step("body edit", func() error {
		return store.UpdateIssue(ctx, bead.ID, map[string]interface{}{"description": "new body"}, "tester")
	}, func(img publicops.Image) bool { return img.Row["description"] == "new body" })
	step("label add", func() error { return store.AddLabel(ctx, bead.ID, "archive-me", "tester") },
		func(img publicops.Image) bool { return hasLabel(img, "archive-me") })
	step("label remove", func() error { return store.RemoveLabel(ctx, bead.ID, "archive-me", "tester") },
		func(img publicops.Image) bool { return !hasLabel(img, "archive-me") })
	step("comment", func() error {
		_, err := store.AddIssueComment(ctx, bead.ID, "tester", "a comment")
		return err
	}, func(img publicops.Image) bool {
		return len(img.Comments) == 1 && img.Comments[0]["text"] == "a comment"
	})
	events := len(prev.Events)
	step("event", func() error {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := issueops.RecordEventInTable(ctx, tx, "events", bead.ID, types.EventCommented, "tester", "x"); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}, func(img publicops.Image) bool { return len(img.Events) == events+1 })
	step("inbound dependency", func() error {
		return store.AddDependency(ctx, &types.Dependency{IssueID: other.ID, DependsOnID: bead.ID, Type: types.DepBlocks}, "tester")
	}, func(img publicops.Image) bool {
		return len(img.Dependencies) == 1 && img.Dependencies[0]["issue_id"] == other.ID
	})
	step("close", func() error { return store.CloseIssue(ctx, bead.ID, "done", "tester", "") },
		func(img publicops.Image) bool { return img.Row["status"] == string(types.StatusClosed) })
	step("reopen", func() error { return store.ReopenIssue(ctx, bead.ID, "again", "tester") },
		func(img publicops.Image) bool { return img.Row["status"] == string(types.StatusOpen) })

	if again := imageOf(t, ctx, store, bead.ID); again.Revision != prev.Revision {
		t.Fatalf("two reads after the writes differ: %s vs %s", prev.Revision, again.Revision)
	}
}

// TestImageWispReadsWispTables images a wisp from the wisp plane and shows a
// durable bead's image picking up the wisp's edge onto it from
// wisp_dependencies.
func TestImageWispReadsWispTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	durable := newImageIssue(t, ctx, store, "durable target", false)
	wisp := newImageIssue(t, ctx, store, "imaged wisp", true)
	if err := store.AddLabel(ctx, wisp.ID, "wisp-label", "tester"); err != nil {
		t.Fatalf("label wisp: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: wisp.ID, DependsOnID: durable.ID, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("wisp dependency: %v", err)
	}

	images, err := imagesOf(ctx, store, wisp.ID, durable.ID, wisp.ID)
	if err != nil {
		t.Fatalf("image: %v", err)
	}
	if len(images) != 2 || images[0].ID != wisp.ID || images[1].ID != durable.ID {
		t.Fatalf("images not in first-mention order with duplicates collapsed: %d", len(images))
	}
	w := images[0]
	if w.Table != "wisps" || w.Row["id"] != wisp.ID {
		t.Fatalf("wisp image table = %q, row id %v", w.Table, w.Row["id"])
	}
	if len(w.Labels) != 1 || w.Labels[0]["label"] != "wisp-label" {
		t.Fatalf("wisp labels = %v", w.Labels)
	}
	if len(w.Events) == 0 {
		t.Fatal("wisp image has no wisp_events rows")
	}
	if len(w.Dependencies) != 1 || w.Dependencies[0]["depends_on_issue_id"] != durable.ID {
		t.Fatalf("wisp dependencies = %v", w.Dependencies)
	}
	d := images[1]
	if d.Table != "issues" || len(d.Dependencies) != 1 || d.Dependencies[0]["issue_id"] != wisp.ID {
		t.Fatalf("durable image misses the inbound wisp edge: table %q deps %v", d.Table, d.Dependencies)
	}
}

func TestImageMissingIDReturnsNothing(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	bead := newImageIssue(t, ctx, store, "present", false)
	images, err := imagesOf(ctx, store, bead.ID, "bd-image-missing")
	if images != nil {
		t.Fatalf("partial result: %v", images)
	}
	if !errors.Is(err, publicops.ErrNotFound) || !strings.Contains(err.Error(), "bd-image-missing") {
		t.Fatalf("err = %v, want ErrNotFound naming bd-image-missing", err)
	}
	if strings.Contains(err.Error(), bead.ID) {
		t.Fatalf("err names the present id: %v", err)
	}
}

// TestImageExcludesGeneratedColumn adds a stored and a virtual generated
// column to issues on the test's own branch: SHOW COLUMNS must mark both
// generated, and neither may be imaged while the column it derives from is.
func TestImageExcludesGeneratedColumn(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, ddl := range []string{
		"ALTER TABLE issues ADD COLUMN image_team varchar(255) GENERATED ALWAYS AS " +
			"(LEFT(json_unquote(json_extract(metadata, '$.owner.team')), 255)) STORED",
		"ALTER TABLE issues ADD COLUMN image_title_len int GENERATED ALWAYS AS (CHAR_LENGTH(title)) VIRTUAL",
	} {
		if _, err := store.db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	rows, err := store.db.QueryContext(ctx, "SHOW COLUMNS FROM issues")
	if err != nil {
		t.Fatal(err)
	}
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	extra := map[string]string{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, col := range cols {
			switch v := vals[i].(type) {
			case []byte:
				row[strings.ToLower(col)] = string(v)
			case string:
				row[strings.ToLower(col)] = v
			}
		}
		extra[row["field"]] = row["extra"]
	}
	_ = rows.Close()
	for _, col := range []string{"image_team", "image_title_len"} {
		if !strings.Contains(strings.ToUpper(extra[col]), " GENERATED") {
			t.Fatalf("SHOW COLUMNS Extra for %s = %q, want it marked generated", col, extra[col])
		}
	}

	issue := &types.Issue{
		Title:     "with team",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Metadata:  json.RawMessage(`{"owner":{"team":"storage"}}`),
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatal(err)
	}
	var team string
	if err := store.db.QueryRowContext(ctx, "SELECT image_team FROM issues WHERE id = ?", issue.ID).Scan(&team); err != nil || team != "storage" {
		t.Fatalf("image_team = %q, %v", team, err)
	}
	img := imageOf(t, ctx, store, issue.ID)
	for _, col := range []string{"image_team", "image_title_len"} {
		if _, ok := img.Row[col]; ok {
			t.Fatalf("generated column %s imaged: %v", col, img.Row)
		}
	}
	meta, ok := img.Row["metadata"].(string)
	if !ok || !strings.Contains(meta, `"storage"`) {
		t.Fatalf("metadata = %#v, want the stored JSON text", img.Row["metadata"])
	}
}
