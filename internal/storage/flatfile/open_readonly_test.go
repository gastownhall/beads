package flatfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TASKS-nvzk / GH#3231: a read-only cross-repo open must not mutate the
// foreign project. A fresh clone of a flatfile repo is missing its empty
// subdirectories (git does not track empty dirs); OpenStoreReadOnly must
// serve reads without creating them.
func TestOpenStoreReadOnlyDoesNotCreateDirs(t *testing.T) {
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	// Fresh-clone shape: metadata.json plus one tracked issue file; the
	// empty comments/, memories/, events/ dirs do not exist.
	if err := os.MkdirAll(filepath.Join(beadsDir, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"flatfile","project_id":"test"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	issue := `{"id":"ro-1","title":"read me","status":"open","issue_type":"task","priority":2}`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues", "ro-1.json"), []byte(issue), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := OpenStoreReadOnly(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenStoreReadOnly: %v", err)
	}
	defer st.Close()

	// Reads must work against the fresh-clone layout.
	got, err := st.GetIssue(ctx, "ro-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "read me" {
		t.Errorf("GetIssue title = %q, want %q", got.Title, "read me")
	}
	if comments, err := st.GetIssueComments(ctx, "ro-1"); err != nil {
		t.Errorf("GetIssueComments with missing comments/: %v", err)
	} else if len(comments) != 0 {
		t.Errorf("GetIssueComments = %d comments, want 0", len(comments))
	}

	// The open and the reads must not have created any directory.
	for _, d := range []string{"comments", "memories", "events"} {
		if _, err := os.Stat(filepath.Join(beadsDir, d)); !os.IsNotExist(err) {
			t.Errorf("read-only open created %s/ in the foreign checkout (stat err=%v)", d, err)
		}
	}

	// A missing issue still maps to ErrNotFound, not a directory error.
	if _, err := st.GetIssue(ctx, "ro-2"); err != storage.ErrNotFound {
		t.Errorf("GetIssue(missing) error = %v, want storage.ErrNotFound", err)
	}
}

// Contrast: the read-write open keeps creating the required directories.
func TestOpenStoreCreatesDirs(t *testing.T) {
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"flatfile","project_id":"test"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := OpenStore(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	for _, d := range []string{"issues", "comments", "memories", "events"} {
		if _, err := os.Stat(filepath.Join(beadsDir, d)); err != nil {
			t.Errorf("read-write open did not create %s/: %v", d, err)
		}
	}
}
