//go:build cgo

package embeddeddolt_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedOpenReadOnly_ReadOnlyFiles reproduces GH#7445: the read-only
// embedded open (bd --readonly) must work against a store whose files the
// process cannot write, or fail with an error that says the store needs write
// access, never a bare permission-denied on the chunk journal.
func TestEmbeddedOpenReadOnly_ReadOnlyFiles(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	// Mark every file under .beads read-only, as in the report.
	var files []string
	if err := filepath.WalkDir(beadsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
			return os.Chmod(p, 0o444)
		}
		return nil
	}); err != nil {
		t.Fatalf("chmod files read-only: %v", err)
	}
	t.Cleanup(func() {
		for _, p := range files {
			_ = os.Chmod(p, 0o644)
		}
	})

	ro, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err != nil {
		if strings.Contains(err.Error(), "write access") {
			return // the documented alternative: a clear, non-retryable refusal
		}
		t.Fatalf("OpenReadOnly on a store with read-only files: %v\nwant a successful read-only open, or an error saying the store needs write access", err)
	}
	defer ro.Close()
	if _, err := ro.GetStatistics(ctx); err != nil {
		t.Fatalf("read on read-only store: %v", err)
	}
}
