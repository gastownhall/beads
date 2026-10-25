package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestDoctorCleanupEntryPointsDeleteOnlyWhatTheyOwn drives each Classic
// Artifacts and Legacy MQ cleanup path -- --check=artifacts --clean (prompt
// answered by --yes, and the --json form that never prompts) and --fix's
// Classic Artifacts and Legacy MQ Files repairs -- over workspaces doctor
// cannot positively claim: a nested .beads whose metadata does not load, a
// metadata-less layout the historical classifier does not recognize, Beads
// Classic and Dolt workspaces at paths that should hold only a redirect (a
// server-mode one, with no local store and no redirect, included),
// workspaces on other backends there whatever their database is named, stubs
// holding a database under any name (a Dolt chunk-store backup included),
// stubs whose metadata or symlink points at a Dolt store, a Dolt workspace
// under .git outside beads-worktrees, and rig roots (with a local store or
// server-mode) that mayor/rig/.beads redirects to. Each must stay
// byte-identical, while what the scan reports as safe to delete beside them
// (an orphaned WAL in a workspace on Dolt) is still removed.
func TestDoctorCleanupEntryPointsDeleteOnlyWhatTheyOwn(t *testing.T) {
	const sqliteDB = "SQLite format 3\x00rest"
	protected := map[string]map[string]string{
		"sub/.beads":         {"metadata.json": "{\"database\":\"beads.db\",\n<<<<<<< HEAD\n", "beads.db": sqliteDB, "beads.db-wal": "uncheckpointed", "beads.db-shm": "s", "beads.backup-20260204.db": "b"},
		"amb/.beads":         {".gitignore": "*.db\n", "beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s"},
		"crew/alice/.beads":  {"metadata.json": `{"database":"beads.db","jsonl_export":"issues.jsonl"}`, "beads.db": sqliteDB, "beads.db-wal": "w", "beads.db-shm": "s"},
		"polecats/p1/.beads": {"config.json": `{"database":"beads.db"}`, "beads.db": sqliteDB, "beads.db-wal": "w"},
		"crew/bob/.beads":    {"metadata.json": `{"backend":"dolt","dolt_mode":"embedded"}`, "embeddeddolt/bob/.dolt/noms/manifest": "m", "config.yaml": "c"},
		"rig/.beads":         {"metadata.json": `{"backend":"dolt"}`, "dolt/rig/.dolt/noms/manifest": "m"},
		// Another backend at a redirect-only path, whatever its database is
		// named, and a stub holding a database under any name.
		"crew/carol/.beads":   {"metadata.json": `{"backend":"sqlite","sqlite_path":"issues.sqlite3"}`, "issues.sqlite3": sqliteDB},
		"polecats/p3/.beads":  {"metadata.json": `{"backend":"sqlite","sqlite_path":"data/tracker.sqlite3"}`, "data/tracker.sqlite3": sqliteDB},
		"crew/erin/.beads":    {"metadata.json": `{"database":"tracker.sqlite"}`, "tracker.sqlite": sqliteDB},
		"crew/dave/.beads":    {"metadata.json": `{"backend":"http"}`, "config.yaml": "c"},
		"crew/frank/.beads":   {"metadata.json": `{"backend":"postgres"}`, "config.yaml": "c"},
		"refinery/rig/.beads": {"metadata.json": `{"backend":"future-x"}`, "store/x.bin": "opaque"},
		"polecats/p4/.beads":  {"redirect": "../../mayor/rig/.beads\n", "beads.db.bak": sqliteDB},
		"polecats/p5/.beads":  {"redirect": "../../mayor/rig/.beads\n", "old.db-wal/inner.db": sqliteDB},
		// A server-mode rig root that mayor/rig/.beads redirects to: no
		// local store, but every checkout routes through it.
		"town/rig/.beads": {"metadata.json": `{"backend":"dolt","dolt_mode":"server","dolt_database":"my_rig"}`, "config.yaml": "c", "routes.jsonl": "{}"},
		// A server-mode workspace with no redirect: its metadata is its only
		// record of the server database it opens.
		"crew/hal/.beads": {"metadata.json": `{"backend":"dolt","dolt_mode":"server","dolt_database":"hal"}`, "config.yaml": "c", "routes.jsonl": "{}"},
		// A Dolt database in the raw chunk-store layout of a file:// Dolt
		// backup or remote (manifest, *.darc, LOCK, oldgen/, no .dolt), under
		// a name that says nothing, in a stub and in a server-mode stub.
		"polecats/p6/.beads": {"redirect": "../../mayor/rig/.beads\n", "snap/manifest": doltManifestForDoctorTest, "snap/defbr.darc": "a", "snap/LOCK": "", "snap/oldgen/LOCK": ""},
		"crew/gil/.beads":    {"metadata.json": `{"backend":"dolt","dolt_mode":"server"}`, "dolt-backups/manifest": doltManifestForDoctorTest, "dolt-backups/defbr.darc": "a", "dolt-backups/LOCK": ""},
		// A header-less SQLite name, metadata pointing at an existing local
		// Dolt store, and a link to a Dolt store (followed only to classify).
		"crew/zed/.beads":    {"vault.sqlite": "\x8f\x12opaque"},
		"crew/yan/.beads":    {"metadata.json": `{"backend":"dolt","dolt_data_dir":"../../../store"}`, "config.yaml": "c"},
		"polecats/p9/.beads": {"redirect": "../../mayor/rig/.beads\n"},
		// .git is entered only at .git/beads-worktrees: a Dolt workspace
		// elsewhere under .git keeps even an orphaned WAL.
		".git/stash-area/.beads": {"metadata.json": `{"backend":"dolt"}`, "beads.db-wal": "w"},
	}
	entryPoints := []struct {
		name string
		run  func(t *testing.T, root string)
	}{
		{"check=artifacts --clean --yes", func(t *testing.T, root string) {
			if err := runArtifactsCheck(root, true, true); err != nil {
				t.Fatalf("runArtifactsCheck: %v", err)
			}
		}},
		{"check=artifacts --clean --json", func(t *testing.T, root string) {
			old := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = old })
			if err := runArtifactsCheck(root, true, false); err != nil {
				t.Fatalf("runArtifactsCheck --json: %v", err)
			}
		}},
		{"fix Classic Artifacts", func(t *testing.T, root string) {
			applyFixList(root, []doctorCheck{{Name: "Classic Artifacts", Status: statusWarning, Fix: "bd doctor --fix"}})
		}},
	}
	for _, entry := range entryPoints {
		t.Run(entry.name, func(t *testing.T) {
			isolateBeadsDirForTest(t)
			root := t.TempDir()
			rootWAL := filepath.Join(root, ".beads", "beads.db-wal")
			writeTreeForDoctorTest(t, filepath.Join(root, ".beads"), map[string]string{"metadata.json": `{"backend":"dolt"}`, "beads.db-wal": "w"})
			for dir, files := range protected {
				writeTreeForDoctorTest(t, filepath.Join(root, dir), files)
			}
			writeTreeForDoctorTest(t, filepath.Join(root, "rig", "mayor", "rig", ".beads"), map[string]string{"redirect": "../../.beads\n"})
			writeTreeForDoctorTest(t, filepath.Join(root, "town", "rig"), map[string]string{"mayor/rig/.beads/redirect": "../../.beads\n", "polecats/": ""})
			writeTreeForDoctorTest(t, root, map[string]string{"store/yan/.dolt/noms/manifest": doltManifestForDoctorTest, "x/data/db/.dolt/noms/manifest": doltManifestForDoctorTest})
			if runtime.GOOS != "windows" { // symlinks need privileges there
				if err := os.Symlink(filepath.Join(root, "x", "data"), filepath.Join(root, "polecats", "p9", ".beads", "data")); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]map[string]string{}
			for dir := range protected {
				before[dir] = snapshotTreeForDoctorTest(t, filepath.Join(root, dir))
			}

			entry.run(t, root)

			for dir := range protected {
				if got := snapshotTreeForDoctorTest(t, filepath.Join(root, dir)); !reflect.DeepEqual(got, before[dir]) {
					t.Errorf("%s changed %s:\n got %q\nwant %q", entry.name, dir, got, before[dir])
				}
			}
			if _, err := os.Stat(rootWAL); !os.IsNotExist(err) {
				t.Errorf("%s left the orphaned WAL it reports as safe to delete (stat err %v)", entry.name, err)
			}
		})
	}

	t.Run("fix Legacy MQ Files, mq a symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		isolateBeadsDirForTest(t)
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		writeTreeForDoctorTest(t, beadsDir, map[string]string{"metadata.json": `{"backend":"dolt"}`})
		writeTreeForDoctorTest(t, filepath.Join(root, "elsewhere"), map[string]string{"a.json": "{}"})
		if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(beadsDir, "mq")); err != nil {
			t.Fatal(err)
		}
		before := snapshotTreeForDoctorTest(t, root)
		applyFixList(root, []doctorCheck{{Name: "Legacy MQ Files", Status: statusWarning, Fix: "bd doctor --fix"}})
		if got := snapshotTreeForDoctorTest(t, root); !reflect.DeepEqual(got, before) {
			t.Fatalf("Legacy MQ Files fix removed or followed a symlinked .beads/mq:\n got %q\nwant %q", got, before)
		}
	})

	t.Run("fix Legacy MQ Files", func(t *testing.T) {
		isolateBeadsDirForTest(t)
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		writeTreeForDoctorTest(t, beadsDir, map[string]string{"metadata.json": `{"backend":"dolt"}`, "mq/a.json": "{}", "mq/notes.txt": "mine", "mq/sub/keep.dat": "d"})
		applyFixList(root, []doctorCheck{{Name: "Legacy MQ Files", Status: statusWarning, Fix: "bd doctor --fix"}})
		want := map[string]string{".": "/", "notes.txt": "mine", "sub": "/", "sub/keep.dat": "d"}
		if got := snapshotTreeForDoctorTest(t, filepath.Join(beadsDir, "mq")); !reflect.DeepEqual(got, want) {
			t.Fatalf("Legacy MQ Files fix left .beads/mq as\n %q\nwant only the reported *.json removed: %q", got, want)
		}
	})
}

// doltManifestForDoctorTest starts like the manifest of a Dolt chunk store.
const doltManifestForDoctorTest = "5:__DOLT__:nqhvd2c4qe4m0hm9s3cqc5f6ilm1qv7a:abc"

func writeTreeForDoctorTest(t *testing.T, dir string, entries map[string]string) {
	t.Helper()
	for name, content := range entries {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotTreeForDoctorTest(t *testing.T, dir string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			snapshot[filepath.ToSlash(rel)] = "/"
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[filepath.ToSlash(rel)] = "-> " + target
			return nil
		}
		data, err := os.ReadFile(path) // #nosec G304 -- test-owned temp tree
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
