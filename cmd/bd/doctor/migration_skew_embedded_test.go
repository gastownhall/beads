//go:build cgo

package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bd-578h9.13: SharedStore only ever opens a server-mode store, so in an
// embedded workspace (the mode #4259 was reported against) the skew check
// used to short-circuit to "N/A (no database)" — an OK status — while a real
// embedded database sat on disk. The check must fall back to opening the
// embedded database and actually run the comparison.
func TestCheckMigrationContentSkew_EmbeddedFallback(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	store, err := embeddeddolt.Open(ctx, beadsDir, "beads", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	got := CheckMigrationContentSkew(&SharedStore{beadsDir: beadsDir})
	if got.Message == "N/A (no database)" {
		t.Fatalf("check short-circuited to %q despite an embedded database on disk", got.Message)
	}
	// Fresh embedded DB: no sync remote, so the check runs and skips cleanly.
	if got.Status != StatusOK || !strings.Contains(got.Message, "not configured") {
		t.Fatalf("check = %q (%s), want OK with a 'not configured' skip", got.Status, got.Message)
	}
}
