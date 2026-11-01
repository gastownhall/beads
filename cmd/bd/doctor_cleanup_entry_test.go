package main

import (
	"encoding/json"
	"io"
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
// server-mode) that mayor/rig/.beads redirects to, and redirect-only stubs
// holding what is not a bd runtime leftover: an orchestrator worktree's
// tracked formulas/, config.yaml, README.md and .gitignore beside its
// redirect, the audit log, routes and an encrypted database, a server-mode
// config.yaml with no redirect beside the .local_version witness of a pre-1.0
// server workspace, and bd's state files beside a redirect bd ignores. Each
// must stay byte-identical, and bd must still refuse to open that legacy
// server workspace, while what the scan reports as safe to delete beside
// them (an orphaned WAL in a workspace on Dolt, a stub's runtime leftovers)
// is still removed.
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
		// An orchestrator worktree of a repository that tracks .beads files
		// keeps them beside the redirect it writes, and a stub with no
		// redirect may be a server-mode workspace configured by config.yaml
		// alone, whose .local_version is the witness that makes bd refuse a
		// pre-1.0 server workspace. bd opens a stub whose redirect it
		// ignores, so its state files are not leftovers either. None of it
		// is a bd runtime leftover.
		"crew/ivy/.beads":     {"redirect": "../../mayor/rig/.beads\n", ".gitignore": "redirect\n", "config.yaml": "issue-prefix: rig\n", "README.md": "# Beads\n", "formulas/beads-release.formula.toml": "formula = \"beads-release\"\n", "interactions.jsonl": "{}\n", "routes.jsonl": "{}\n", "beads.db.enc": "opaque", "mq/a.json": "{}"},
		"crew/kim/.beads":     {"config.yaml": "dolt:\n  mode: server\n  host: db.example.com\n", "interactions.jsonl": "{}\n", ".local_version": "0.62.0\n"},
		"polecats/p8/.beads":  {"redirect": "../../mayor/rig/.beads\n", "last-touched/notes.txt": "mine"},
		"polecats/p11/.beads": {"redirect": "../../nowhere/.beads\n", ".local_version": "0.62.0\n", "last-touched": "rig-1\n"},
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
			// A stub holding only bd's runtime leftovers, beside a redirect
			// bd follows, loses them.
			leftovers := filepath.Join(root, "polecats", "p10", ".beads")
			writeTreeForDoctorTest(t, leftovers, map[string]string{"redirect": "../../rig/.beads\n", ".local_version": "0.62.0\n", "last-touched": "rig-1\n", "daemon.pid": "1"})
			before := map[string]map[string]string{}
			for dir := range protected {
				before[dir] = snapshotTreeForDoctorTest(t, filepath.Join(root, dir))
			}
			legacyServer := filepath.Join(root, "crew", "kim", ".beads")
			requireLegacyServerRefusal(t, legacyServer, "before the cleanup")

			entry.run(t, root)

			for dir := range protected {
				if got := snapshotTreeForDoctorTest(t, filepath.Join(root, dir)); !reflect.DeepEqual(got, before[dir]) {
					t.Errorf("%s changed %s:\n got %q\nwant %q", entry.name, dir, got, before[dir])
				}
			}
			if _, err := os.Stat(rootWAL); !os.IsNotExist(err) {
				t.Errorf("%s left the orphaned WAL it reports as safe to delete (stat err %v)", entry.name, err)
			}
			if got, want := snapshotTreeForDoctorTest(t, leftovers), map[string]string{".": "/", "redirect": "../../rig/.beads\n"}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s left the stub's runtime leftovers it reports as safe to delete: %q, want %q", entry.name, got, want)
			}
			requireLegacyServerRefusal(t, legacyServer, "after the cleanup")
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

// requireLegacyServerRefusal checks that guardLegacyUpgradeWorkspace refuses
// beadsDir as a legacy Dolt server workspace. It runs in a subtest, so the
// workspace config and environment it binds do not outlive the check.
func requireLegacyServerRefusal(t *testing.T, beadsDir, when string) {
	t.Helper()
	t.Run("legacy server refused "+when, func(t *testing.T) {
		captureLegacyUpgradeWarnings(t)
		bindSelectedWorkspaceConfig(t, beadsDir)
		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Errorf("guardLegacyUpgradeWorkspace(%s) = %v, want the legacy server workspace refusal", beadsDir, err)
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

// TestDoctorArtifactsCleanJSONPrintsOnlyJSON pins that bd doctor
// --check=artifacts --clean --json prints exactly one JSON document on
// stdout: the cleanup's progress lines (each removal, each finding kept for
// review, the summary) go to stderr. They were printed on stdout ahead of
// the document, so stdout did not parse as JSON. Without --json, --clean and
// --fix still print them on stdout.
func TestDoctorArtifactsCleanJSONPrintsOnlyJSON(t *testing.T) {
	// An orphaned WAL on Dolt, which the cleanup removes, and a database it
	// keeps for review.
	setup := func(t *testing.T) (root, wal string) {
		isolateBeadsDirForTest(t)
		root = t.TempDir()
		writeTreeForDoctorTest(t, filepath.Join(root, "sub", ".beads"), map[string]string{"metadata.json": `{"backend":"dolt"}`, "beads.db-wal": "w"})
		writeTreeForDoctorTest(t, filepath.Join(root, "keep", ".beads"), map[string]string{"metadata.json": `{"backend":"dolt"}`, "beads.db": "SQLite format 3\x00rest"})
		return root, filepath.Join(root, "sub", ".beads", "beads.db-wal")
	}
	const summary = "Artifact cleanup: 1 removed, 1 skipped, 0 errors"

	t.Run("--clean --json", func(t *testing.T) {
		root, wal := setup(t)
		old := jsonOutput
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = old })
		var err error
		stdout, stderr := captureStdio(t, func() { err = runArtifactsCheck(root, true, false) })
		if err != nil {
			t.Fatalf("runArtifactsCheck --clean --json: %v", err)
		}
		dec := json.NewDecoder(strings.NewReader(stdout))
		var doc map[string]interface{}
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("stdout does not parse as JSON (%v):\n%s", err, stdout)
		}
		if _, err := dec.Token(); err != io.EOF {
			t.Fatalf("stdout holds more than one JSON document:\n%s", stdout)
		}
		if doc["total_count"] != float64(1) || doc["safe_delete_count"] != float64(0) {
			t.Errorf("JSON after the cleanup = %v, want total_count 1 (the database kept for review) and safe_delete_count 0", doc)
		}
		for _, want := range []string{"Removed: " + wal, "Skip (needs review): ", summary} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		if _, err := os.Stat(wal); !os.IsNotExist(err) {
			t.Errorf("the cleanup left %s (stat err %v)", wal, err)
		}
	})
	for _, entry := range []struct {
		name string
		run  func(root string)
	}{
		{"--clean --yes", func(root string) {
			if err := runArtifactsCheck(root, true, true); err != nil {
				t.Errorf("runArtifactsCheck --clean --yes: %v", err)
			}
		}},
		{"--fix", func(root string) {
			applyFixList(root, []doctorCheck{{Name: "Classic Artifacts", Status: statusWarning, Fix: "bd doctor --fix"}})
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			root, wal := setup(t)
			stdout, stderr := captureStdio(t, func() { entry.run(root) })
			for _, want := range []string{"Removed: " + wal, summary} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if strings.Contains(stderr, "Removed: ") {
				t.Errorf("progress went to stderr:\n%s", stderr)
			}
		})
	}
}
