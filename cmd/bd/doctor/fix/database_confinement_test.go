package fix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// writeRawMetadataForTest writes workspace metadata verbatim, for values
// Config.Save would strip (an absolute dolt_data_dir or database).
func writeRawMetadataForTest(t *testing.T, beadsDir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

// assertNoCorruptBackups fails when any *.corrupt.backup entry exists
// anywhere under root.
func assertNoCorruptBackups(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(filepath.Base(path), ".corrupt.backup") {
			t.Errorf("corrupt backup artifact left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// sentinelForTest plants a sentinel file and returns a function asserting its
// content survived unchanged.
func sentinelForTest(t *testing.T, path, content string) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir sentinel parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("plant sentinel: %v", err)
	}
	return func() {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("sentinel %s disappeared: %v", path, err)
		}
		if string(got) != content {
			t.Errorf("sentinel %s modified: %q", path, string(got))
		}
	}
}

// TestDatabaseIntegrityRefusesUnsafeDatabaseSelections covers AC-5: metadata
// selecting a database outside the effective workspace, the workspace root
// itself, a symlinked database leaf (dangling or not), or a path escaping
// through a symlinked ancestor is refused before any backup or deletion, with
// every sentinel and original entry preserved and the refusal preceding the
// test-binary guard.
func TestDatabaseIntegrityRefusesUnsafeDatabaseSelections(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")

	type fixture struct {
		name     string
		metadata func(t *testing.T, dir, beadsDir string)
		sentinel string
	}

	fixtures := []fixture{
		{
			name: "relative dolt_data_dir escapes workspace",
			metadata: func(t *testing.T, dir, beadsDir string) {
				if err := (&configfile.Config{Backend: configfile.BackendDolt, DoltDataDir: "../outside-dolt"}).Save(beadsDir); err != nil {
					t.Fatalf("save config: %v", err)
				}
			},
			sentinel: "outside-dolt/noms.db",
		},
		{
			name: "absolute dolt_data_dir outside workspace",
			metadata: func(t *testing.T, dir, beadsDir string) {
				writeRawMetadataForTest(t, beadsDir, fmt.Sprintf(`{"backend":"dolt","dolt_data_dir":%q}`, filepath.Join(dir, "outside-dolt")))
			},
			sentinel: "outside-dolt/noms.db",
		},
		{
			name: "dolt_data_dir selects the workspace root",
			metadata: func(t *testing.T, dir, beadsDir string) {
				if err := (&configfile.Config{Backend: configfile.BackendDolt, DoltDataDir: "."}).Save(beadsDir); err != nil {
					t.Fatalf("save config: %v", err)
				}
			},
			// A sentinel beside the metadata file: at base the repair renames
			// the whole .beads directory away as its "database" before the
			// binary lookup fails; the refusal must never touch it.
			sentinel: ".beads/workspace-keep.db",
		},
		{
			name: "absolute database outside workspace",
			metadata: func(t *testing.T, dir, beadsDir string) {
				writeRawMetadataForTest(t, beadsDir, fmt.Sprintf(`{"backend":"dolt","database":%q}`, filepath.Join(dir, "outside.db")))
			},
			sentinel: "outside.db",
		},
		{
			name: "symlinked ancestor escapes workspace",
			metadata: func(t *testing.T, dir, beadsDir string) {
				if err := (&configfile.Config{Backend: configfile.BackendDolt, DoltDataDir: "linked/dolt"}).Save(beadsDir); err != nil {
					t.Fatalf("save config: %v", err)
				}
				if err := os.Symlink(filepath.Join(dir, "real-dolt"), filepath.Join(beadsDir, "linked")); err != nil {
					t.Fatalf("symlink ancestor: %v", err)
				}
			},
			sentinel: "real-dolt/noms.db",
		},
		{
			name: "symlinked database leaf points outside",
			metadata: func(t *testing.T, dir, beadsDir string) {
				if err := (&configfile.Config{Backend: configfile.BackendDolt}).Save(beadsDir); err != nil {
					t.Fatalf("save config: %v", err)
				}
				if err := os.Symlink(filepath.Join(dir, "real-dolt"), filepath.Join(beadsDir, "dolt")); err != nil {
					t.Fatalf("symlink database: %v", err)
				}
			},
			sentinel: "real-dolt/noms.db",
		},
		{
			name: "dangling symlinked database leaf",
			metadata: func(t *testing.T, dir, beadsDir string) {
				if err := (&configfile.Config{Backend: configfile.BackendDolt}).Save(beadsDir); err != nil {
					t.Fatalf("save config: %v", err)
				}
				if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(beadsDir, "dolt")); err != nil {
					t.Fatalf("symlink database: %v", err)
				}
			},
			sentinel: ".beads/metadata.json",
		},
		{
			name: "symlinked database leaf points inside workspace",
			metadata: func(t *testing.T, dir, beadsDir string) {
				if err := (&configfile.Config{Backend: configfile.BackendDolt}).Save(beadsDir); err != nil {
					t.Fatalf("save config: %v", err)
				}
				if err := os.Symlink(filepath.Join(beadsDir, "other-dolt"), filepath.Join(beadsDir, "dolt")); err != nil {
					t.Fatalf("symlink database: %v", err)
				}
			},
			sentinel: ".beads/other-dolt/noms.db",
		},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			dir := setupTestWorkspace(t)
			beadsDir := filepath.Join(dir, ".beads")
			fx.metadata(t, dir, beadsDir)
			checkSentinel := sentinelForTest(t, filepath.Join(dir, filepath.FromSlash(fx.sentinel)), "sentinel-data\n")

			err := DatabaseIntegrity(dir)
			if err == nil {
				t.Fatal("expected integrity recovery to refuse the unsafe database selection")
			}
			if errors.Is(err, ErrTestBinary) || strings.Contains(err.Error(), ErrTestBinary.Error()) {
				t.Fatalf("expected the refusal to precede the bd binary lookup, got %v", err)
			}
			if !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("expected a refusal error, got %v", err)
			}
			checkSentinel()
			assertNoCorruptBackups(t, dir)
		})
	}
}