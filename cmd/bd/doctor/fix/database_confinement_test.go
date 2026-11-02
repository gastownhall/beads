package fix

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// confineSymlink creates link -> target, skipping where symlinks are unavailable.
func confineSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
}

// confineWriteFile creates path's parents and writes content.
func confineWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// confineMetadata writes raw metadata.json: Config.Save drops absolute
// dolt_data_dir values, which these fixtures need.
func confineMetadata(t *testing.T, beadsDir string, fields map[string]string) {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	confineWriteFile(t, filepath.Join(beadsDir, "metadata.json"), string(data))
}

// confineTree records every entry under root without following symlinks:
// file content, "<dir>", or "<link> target".
func confineTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			tree[rel] = "<link> " + target
		case d.IsDir():
			tree[rel] = "<dir>"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func confineTreesEqual(t *testing.T, want, got map[string]string, label string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s changed: before %v, after %v", label, want, got)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s changed at %q: before %q, after %q", label, k, v, got[k])
		}
	}
}

// unsafeDatabaseCases are metadata selections doctor must not back up or
// delete: each arranges dir (the workspace root) and outside (unrelated data
// holding a sentinel at outside/data/sentinel).
var unsafeDatabaseCases = []struct {
	name  string
	setup func(t *testing.T, beadsDir, outside string)
}{
	{"relative dolt_data_dir escaping .beads", func(t *testing.T, beadsDir, _ string) {
		confineWriteFile(t, filepath.Join(filepath.Dir(beadsDir), "escaped", "sentinel"), "keep")
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "../escaped"})
	}},
	{"absolute dolt_data_dir outside", func(t *testing.T, beadsDir, outside string) {
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": filepath.Join(outside, "data")})
	}},
	{"absolute database outside", func(t *testing.T, beadsDir, outside string) {
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "database": filepath.Join(outside, "data")})
	}},
	{"sqlite database outside with sidecars", func(t *testing.T, beadsDir, outside string) {
		db := filepath.Join(outside, "data", "beads.db")
		confineWriteFile(t, db, "db")
		confineWriteFile(t, db+"-wal", "wal")
		confineWriteFile(t, db+"-shm", "shm")
		confineMetadata(t, beadsDir, map[string]string{"backend": "sqlite", "database": db})
	}},
	{"workspace root itself", func(t *testing.T, beadsDir, _ string) {
		confineWriteFile(t, filepath.Join(beadsDir, "sentinel"), "keep")
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "."})
	}},
	{"symlinked ancestor escaping .beads", func(t *testing.T, beadsDir, outside string) {
		confineSymlink(t, outside, filepath.Join(beadsDir, "linked"))
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "linked/data"})
	}},
	{"database leaf linked outside", func(t *testing.T, beadsDir, outside string) {
		confineSymlink(t, filepath.Join(outside, "data"), filepath.Join(beadsDir, "dolt"))
	}},
	{"dangling database leaf link", func(t *testing.T, beadsDir, outside string) {
		confineSymlink(t, filepath.Join(outside, "missing"), filepath.Join(beadsDir, "dolt"))
	}},
	{"database leaf linked inside .beads", func(t *testing.T, beadsDir, _ string) {
		confineWriteFile(t, filepath.Join(beadsDir, "real-dolt", "sentinel"), "keep")
		confineSymlink(t, filepath.Join(beadsDir, "real-dolt"), filepath.Join(beadsDir, "dolt"))
	}},
}

// setupUnsafeDatabase returns a workspace and an outside directory arranged
// by setup, with BEADS_DOLT_DATA_DIR cleared so only metadata selects.
func setupUnsafeDatabase(t *testing.T, setup func(t *testing.T, beadsDir, outside string)) (dir, outside string) {
	t.Helper()
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	dir = setupTestWorkspace(t)
	outside = t.TempDir()
	confineWriteFile(t, filepath.Join(outside, "data", "sentinel"), "keep")
	setup(t, filepath.Join(dir, ".beads"), outside)
	return dir, outside
}

func TestDatabaseIntegrity_RefusesUnconfinedDatabasePath(t *testing.T) {
	for _, tc := range unsafeDatabaseCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, outside := setupUnsafeDatabase(t, tc.setup)
			dirBefore, outsideBefore := confineTree(t, dir), confineTree(t, outside)

			err := DatabaseIntegrity(dir)
			if err == nil || errors.Is(err, ErrTestBinary) || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("expected a confinement refusal before backup, got %v", err)
			}
			confineTreesEqual(t, dirBefore, confineTree(t, dir), "workspace")
			confineTreesEqual(t, outsideBefore, confineTree(t, outside), "outside data")
		})
	}
}
