//go:build cgo

package embeddeddolt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestEmbeddedDoltManifestIsGroupWritable(t *testing.T) {
	dataDir := t.TempDir()
	db, cleanup, err := OpenSQL(t.Context(), dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE DATABASE permission_test"); err != nil {
		_ = cleanup()
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}

	manifest := filepath.Join(dataDir, "permission_test", ".dolt", "noms", "manifest")
	info, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != config.BeadsFilePerm {
		t.Errorf("manifest permissions = %04o, want %04o", got, config.BeadsFilePerm)
	}
}
