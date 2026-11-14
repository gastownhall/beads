//go:build cgo

package embeddeddolt_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedWritableStoreRetainsBehavior is the writable-store control for
// the GH#7445 write-access refusal (AC-4): on a store the process can write,
// the strict read open, the preview open, the classified-read open and the
// ordinary write open all keep their current behavior. The refusal must
// classify only a real permission failure at the engine open, never turn a
// successful open into a refusal. (The CLI-level control — a normal `bd
// create` write on a writable workspace — is in
// TestEmbeddedReadonlyWriteAccessDiagnostic's writable phase.)
func TestEmbeddedWritableStoreRetainsBehavior(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	tmpl := initWriteAccessFixture(t)

	read := func(t *testing.T, name string, open func(ctx context.Context, beadsDir string) (*embeddeddolt.EmbeddedDoltStore, error)) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			beadsDir := copyWriteAccessFixture(t, tmpl)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			store, err := open(ctx, beadsDir)
			if err != nil {
				t.Fatalf("%s on a writable store failed (current behavior is success): %v", name, err)
			}
			defer store.Close()
			stats, err := store.GetStatistics(ctx)
			if err != nil {
				t.Fatalf("%s read (GetStatistics) on a writable store: %v", name, err)
			}
			if stats == nil {
				t.Errorf("%s GetStatistics returned nil statistics", name)
			}
		})
	}

	read(t, "OpenReadOnly", func(ctx context.Context, beadsDir string) (*embeddeddolt.EmbeddedDoltStore, error) {
		return embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	})
	read(t, "OpenForPreviewCommand", func(ctx context.Context, beadsDir string) (*embeddeddolt.EmbeddedDoltStore, error) {
		return embeddeddolt.OpenForPreviewCommand(ctx, beadsDir, "testdb", "main")
	})
	read(t, "OpenForReadOnlyCommand", func(ctx context.Context, beadsDir string) (*embeddeddolt.EmbeddedDoltStore, error) {
		return embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main")
	})

	// The ordinary write open, on a fresh empty store where the open itself
	// is the write (data-directory creation, CREATE DATABASE, schema
	// migration) — the open `bd` itself uses for every non-read command.
	t.Run("Open", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		fresh := filepath.Join(t.TempDir(), ".beads")
		store, err := embeddeddolt.Open(ctx, fresh, "testdb", "main")
		if err != nil {
			t.Fatalf("Open (schema-creating write open) on a fresh writable store: %v", err)
		}
		defer store.Close()
	})
}
