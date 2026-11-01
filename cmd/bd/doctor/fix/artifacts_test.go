package fix

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backendnames"
	"github.com/steveyegge/beads/internal/testutil"
)

// doltMetadata is metadata with positive Dolt evidence.
const doltMetadata = `{"database":"dolt","backend":"dolt"}`

func TestClassicArtifacts_NoArtifacts(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir, io.Discard)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestClassicArtifacts_RemovesSQLiteWAL(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	// Positive Dolt evidence: an orphaned WAL/SHM is removed only there.
	writeBeadsTree(t, beadsDir, map[string]string{configfile.ConfigFileName: doltMetadata})

	// Create WAL/SHM files
	for _, name := range []string{"beads.db-shm", "beads.db-wal"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	err := ClassicArtifacts(dir, io.Discard)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Verify WAL/SHM files were removed
	for _, name := range []string{"beads.db-shm", "beads.db-wal"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", name)
		}
	}
}

func TestClassicArtifacts_SkipsBeadsDB(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create beads.db (should be skipped)
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir, io.Discard)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// beads.db should still exist (not safe to delete automatically)
	if _, err := os.Stat(filepath.Join(beadsDir, "beads.db")); os.IsNotExist(err) {
		t.Error("beads.db should NOT have been removed")
	}
}

// TestClassicArtifacts_KeepsBackupDBs pins that a pre-migration backup, a
// whole SQLite database, is never removed, even in a workspace on Dolt.
func TestClassicArtifacts_KeepsBackupDBs(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	backupName := "beads.backup-20260204.db"
	writeBeadsTree(t, beadsDir, map[string]string{configfile.ConfigFileName: doltMetadata, backupName: "data"})

	err := ClassicArtifacts(dir, io.Discard)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(beadsDir, backupName)); err != nil {
		t.Errorf("backup db should have been kept: %v", err)
	}
}

// TestClassicArtifacts_LeavesJSONLFiles pins that the cleanup no longer
// removes JSONL files: the scan stopped reporting them when JSONL artifact
// scanning was removed (bd-9ni.2), so removing them deleted files no scan
// listed.
func TestClassicArtifacts_LeavesJSONLFiles(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	writeBeadsTree(t, beadsDir, map[string]string{
		configfile.ConfigFileName: doltMetadata,
		"dolt/":                   "",
		"issues.jsonl.new":        `{"id":"test"}`,
		"interactions.jsonl":      "",
		"issues.jsonl":            `{"id":"real"}`,
	})
	want := snapshotBeadsTree(t, beadsDir)

	if err := ClassicArtifacts(dir, io.Discard); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if got := snapshotBeadsTree(t, beadsDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("ClassicArtifacts changed %s:\n got %q\nwant %q", beadsDir, got, want)
	}
}

func TestClassicArtifacts_CleansCruftBeadsDir(t *testing.T) {
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "test")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Add redirect (expected), to a workspace bd follows it to
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	writeBeadsTree(t, filepath.Join(dir, "mayor", "rig", ".beads"), map[string]string{"metadata.json": doltMetadata})

	// Add .gitkeep (should be preserved)
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitkeep"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Add bd's runtime leftovers, which the stub rule removes, and files
	// that are not, which it keeps for review.
	writeBeadsTree(t, beadsDir, map[string]string{".local_version": "0.62.0\n", "daemon.log": "log", "extra.txt": "mine", "cruft-subdir/": ""})

	err := ClassicArtifacts(dir, io.Discard)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// redirect, .gitkeep and what is not a runtime leftover stay
	for _, name := range []string{"redirect", ".gitkeep", "extra.txt", "cruft-subdir"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); err != nil {
			t.Errorf("%s should NOT have been removed: %v", name, err)
		}
	}

	// runtime leftovers are removed
	for _, name := range []string{".local_version", "daemon.log"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", name)
		}
	}
}

// TestClassicArtifacts_PreservesMetadataJSONAlongsideRedirect is a
// regression test for gastownhall/beads#4692: a metadata.json co-located
// with a redirect file (the fb51196f7 / docs "Database Redirects" topology,
// where a server-mode source rig's own metadata.json supplies its
// dolt_database while a redirect points at the shared Gas Town root) must
// never be deleted as cruft while the redirect is preserved.
func TestClassicArtifacts_PreservesMetadataJSONAlongsideRedirect(t *testing.T) {
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "test")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"server","dolt_database":"lola"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Still remove a runtime leftover in the same pass. bd's own state
	// files stay: the stub's metadata makes it a workspace bd opens once
	// the redirect stops resolving.
	writeBeadsTree(t, beadsDir, map[string]string{"daemon.pid": "1", "last-touched": "rig-1\n"})

	if err := ClassicArtifacts(dir, io.Discard); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(beadsDir, "redirect")); os.IsNotExist(err) {
		t.Error("redirect should NOT have been removed")
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); os.IsNotExist(err) {
		t.Error("metadata.json should NOT have been removed while a redirect is present")
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "last-touched")); err != nil {
		t.Errorf("last-touched should NOT have been removed beside the stub's own metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Error("daemon.pid should have been removed")
	}
}

func TestClassicArtifacts_CleansCruftWithoutRedirectFile(t *testing.T) {
	// Regression test: when a redirect-expected location has runtime
	// leftovers but NO redirect file, the fix should still remove them.
	// Previously, the fix required both isRedirectExpected AND hasRedirect.
	// The other files (config.yaml, metadata.json, README.md, issues.jsonl,
	// .gitignore) are what a checkout tracks there, not bd's leftovers, and
	// stay. So does .local_version: with no redirect bd opens this .beads
	// itself, and it is the witness that makes bd refuse a pre-1.0 Dolt
	// server workspace.
	dir := t.TempDir()

	// Simulate .git/beads-worktrees/master/.beads/ (redirect-expected location)
	worktreeBeads := filepath.Join(dir, ".git", "beads-worktrees", "master", ".beads")
	if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
		t.Fatal(err)
	}

	// Add typical stale files (NO redirect file present)
	cruftFiles := []string{"config.yaml", "metadata.json", "README.md", "issues.jsonl", ".gitignore", ".local_version", "daemon.log"}
	for _, name := range cruftFiles {
		content := "stale"
		if name == "metadata.json" {
			// Pre-Dolt metadata that configures nothing: metadata that does
			// not load, or that configures Dolt with no redirect beside it,
			// leaves the directory for review.
			content = `{"database":"beads.db","jsonl_export":"issues.jsonl"}`
		}
		if err := os.WriteFile(filepath.Join(worktreeBeads, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	err := ClassicArtifacts(dir, io.Discard)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Only the former daemon's leftover is removed
	for _, name := range cruftFiles {
		_, err := os.Stat(filepath.Join(worktreeBeads, name))
		if name == "daemon.log" {
			if !os.IsNotExist(err) {
				t.Errorf("%s should have been removed (runtime leftover in redirect-expected dir without redirect file)", name)
			}
		} else if err != nil {
			t.Errorf("%s should NOT have been removed: %v", name, err)
		}
	}
}

func TestIsRedirectExpectedLocation(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"polecat worktree", "/foo/polecats/obsidian/.beads", true},
		{"crew workspace", "/foo/crew/mel/.beads", true},
		{"refinery rig", "/foo/refinery/rig/.beads", true},
		{"beads-worktrees", "/foo/.git/beads-worktrees/abc/.beads", true},
		{"regular beads dir", "/foo/.beads", false},
		{"nested project", "/foo/bar/.beads", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRedirectExpectedLocation(tt.path)
			if got != tt.expected {
				t.Errorf("isRedirectExpectedLocation(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

// writeBeadsTree creates each entry under dir: a name ending in "/" is an empty
// directory, anything else a file holding its value.
func writeBeadsTree(t *testing.T, dir string, entries map[string]string) {
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

// snapshotBeadsTree returns every entry under dir, keyed by its slash-separated
// relative path: a file maps to its bytes, a directory to "/".
func snapshotBeadsTree(t *testing.T, dir string) map[string]string {
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

// TestClassicArtifacts_SkipsUnsupportedWorkspaceFormat pins that the Classic
// Artifacts fix leaves a .beads directory in a format this build cannot open
// byte-identical. The fix walks every .beads under the path, including those
// of nested repositories and worktrees, which command admission never checks,
// and it classified each one with the migrating configfile.Load: a graph
// workspace's legacy config.json became a metadata.json without the graph_*
// keys this build does not know. Each layout also holds artifacts the fix
// would otherwise remove, and the redirect-expected ones cruft, so nothing
// else in the workspace may change either; the admitted workspace at the root
// is still cleaned.
func TestClassicArtifacts_SkipsUnsupportedWorkspaceFormat(t *testing.T) {
	const graphMetadata = `{"database":"beads.db","backend":"dolt","graph_mode":"%s","graph_future_binding":"keep-me"}`
	const strippedMetadata = `{"database":"beads.db","backend":"dolt"}`
	layouts := []struct {
		name  string
		dir   string // the workspace's .beads, relative to the walked root
		files map[string]string
	}{
		{name: "graph_mode in legacy config.json", dir: "sub/.beads", files: map[string]string{"config.json": fmt.Sprintf(graphMetadata, configfile.GraphModeLink)}},
		{name: "future graph_mode in legacy config.json", dir: "sub/.beads", files: map[string]string{"config.json": fmt.Sprintf(graphMetadata, "future-format")}},
		{name: "marker beside legacy config.json", dir: "sub/.beads", files: map[string]string{"config.json": strippedMetadata, configfile.GraphFormatMarkerFile: "link-preview-v9\n"}},
		{name: "graph_mode in metadata.json", dir: "sub/.beads", files: map[string]string{configfile.ConfigFileName: fmt.Sprintf(graphMetadata, configfile.GraphModeLink)}},
		{name: "marker in a polecat worktree", dir: "polecats/p1/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", configfile.GraphFormatMarkerFile: "link-preview-v9\n", "extra.txt": "cruft"}},
		{name: "graph_mode in a beads worktree", dir: ".git/beads-worktrees/wt/.beads", files: map[string]string{"config.json": fmt.Sprintf(graphMetadata, configfile.GraphModeLink), "extra.txt": "cruft"}},
	}
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			root := t.TempDir()
			rootWAL := filepath.Join(root, ".beads", "beads.db-wal")
			writeBeadsTree(t, filepath.Dir(rootWAL), map[string]string{"beads.db-wal": "wal", configfile.ConfigFileName: doltMetadata})
			beadsDir := filepath.Join(root, filepath.FromSlash(layout.dir))
			entries := map[string]string{
				"dolt/":                    "",
				"beads.db-wal":             "wal",
				"beads.backup-20260204.db": "backup",
				"issues.jsonl.new":         `{"id":"x"}`,
			}
			for name, content := range layout.files {
				entries[name] = content
			}
			writeBeadsTree(t, beadsDir, entries)
			want := snapshotBeadsTree(t, beadsDir)

			if err := ClassicArtifacts(root, io.Discard); err != nil {
				t.Fatalf("ClassicArtifacts: %v", err)
			}
			if got := snapshotBeadsTree(t, beadsDir); !reflect.DeepEqual(got, want) {
				t.Fatalf("ClassicArtifacts changed %s:\n got %q\nwant %q", layout.dir, got, want)
			}
			if _, err := os.Stat(rootWAL); !os.IsNotExist(err) {
				t.Fatalf("ClassicArtifacts left the root workspace's %s (stat err %v)", rootWAL, err)
			}
		})
	}
}

// TestClassicArtifacts_DoesNotMigrateLegacyConfig pins that the fix reads a
// nested workspace's legacy config.json without migrating it into
// metadata.json, and still cleans that workspace by what it says: its
// backend dolt is the positive evidence that makes the orphaned WAL
// removable. A JSONL file beside it, which no scan reports, stays.
func TestClassicArtifacts_DoesNotMigrateLegacyConfig(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, "sub", ".beads")
	const legacy = `{"database":"beads.db","backend":"dolt","future_key":"keep-me"}`
	writeBeadsTree(t, beadsDir, map[string]string{
		"config.json":      legacy,
		"beads.db-wal":     "wal",
		"issues.jsonl.new": `{"id":"x"}`,
	})

	if err := ClassicArtifacts(root, io.Discard); err != nil {
		t.Fatalf("ClassicArtifacts: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(beadsDir, "config.json")); err != nil || string(got) != legacy {
		t.Fatalf("config.json = %q (read err %v), want it unchanged: %q", got, err, legacy)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, configfile.ConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("ClassicArtifacts wrote %s (stat err %v)", configfile.ConfigFileName, err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "beads.db-wal")); !os.IsNotExist(err) {
		t.Errorf("beads.db-wal should have been removed (stat err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "issues.jsonl.new")); err != nil {
		t.Errorf("issues.jsonl.new, which no scan reports, should have been left in place: %v", err)
	}
}

// TestClassicArtifacts_PreservesLegacyConfigJSONAlongsideRedirect extends
// TestClassicArtifacts_PreservesMetadataJSONAlongsideRedirect to a legacy
// config.json, the form the source rig's metadata takes before its migration.
// The walk no longer migrates it into a metadata.json the cleanup keeps, so
// the cleanup keeps the config.json itself, byte-identical with its
// dolt_database and keys bd does not know, while a runtime leftover beside it
// is still removed. Without a redirect, a config.json that configures Dolt is the
// workspace's own, and the cleanup leaves the directory as it is.
func TestClassicArtifacts_PreservesLegacyConfigJSONAlongsideRedirect(t *testing.T) {
	const legacy = `{"dolt_mode":"server","dolt_database":"lola","future_key":"keep-me"}`
	cases := []struct {
		name  string
		files map[string]string
		want  map[string]string // the .beads after the cleanup
	}{
		{
			name:  "with redirect",
			files: map[string]string{"redirect": "../../mayor/rig/.beads", "config.json": legacy, "daemon.pid": "1", "extra.txt": "mine"},
			want:  map[string]string{".": "/", "redirect": "../../mayor/rig/.beads", "config.json": legacy, "extra.txt": "mine"},
		},
		{
			name:  "without redirect",
			files: map[string]string{"config.json": legacy, "extra.txt": "cruft"},
			want:  map[string]string{".": "/", "config.json": legacy, "extra.txt": "cruft"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, "polecats", "p1", ".beads")
			writeBeadsTree(t, beadsDir, tc.files)

			if err := ClassicArtifacts(root, io.Discard); err != nil {
				t.Fatalf("ClassicArtifacts: %v", err)
			}
			if got := snapshotBeadsTree(t, beadsDir); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ClassicArtifacts left %s as\n %q\nwant %q", beadsDir, got, tc.want)
			}
		})
	}
}

// TestClassicArtifacts_LeavesOtherBackendsSQLiteFiles pins that the Classic
// Artifacts fix removes SQLite files only in a .beads classifyBeadsDir finds
// positively on Dolt, where PlanArtifacts marks them safe to delete. The walk
// reaches nested workspaces that command admission never checks, and the fix
// removed beads.db-wal, beads.db-shm and every beads.backup-*.db in each of
// them whatever its backend. In a workspace on a removed backend (sqlite,
// postgres, mysql), on a registered one (such as http in the shipped bd; a
// test-only name stands in for it here), on one this build does not
// recognize, or in a Beads Classic SQLite workspace, whose metadata predates
// the backend key and so reads as Dolt to GetBackend, those are that
// backend's files: a SQLite workspace's WAL can hold committed transactions
// not yet checkpointed into beads.db, which only a build that opens it can
// recover. The root workspace's WAL, which the scan reports, is still removed.
func TestClassicArtifacts_LeavesOtherBackendsSQLiteFiles(t *testing.T) {
	// GetBackend returns a registered name unchanged, so this is the case
	// only classifyBeadsDir's GetBackend check guards: IsSupportedBackend
	// accepts it.
	const registered = "acme-artifacts-fix"
	backendnames.Add(registered)
	t.Cleanup(func() { backendnames.Remove(registered) })

	const sqliteDB = "SQLite format 3\x00"
	// workspace is a nested .beads directory holding metadata, in
	// metadataFile, and every SQLite file the fix removes on Dolt.
	workspace := func(metadataFile, metadata string) map[string]string {
		return map[string]string{
			metadataFile:               metadata,
			"beads.db":                 sqliteDB,
			"beads.db-wal":             "wal",
			"beads.db-shm":             "shm",
			"beads.backup-20260204.db": "backup",
		}
	}
	type nested struct {
		name  string
		files map[string]string // the nested .beads directory's entries
	}
	var workspaces []nested
	for _, backend := range []string{configfile.BackendSQLite, configfile.BackendPostgres, configfile.BackendMySQL, registered, "future-backend"} {
		for _, metadataFile := range []string{configfile.ConfigFileName, "config.json"} {
			workspaces = append(workspaces, nested{backend + "/" + metadataFile, workspace(metadataFile, fmt.Sprintf(`{"database":"beads.db","backend":%q}`, backend))})
		}
	}
	workspaces = append(workspaces,
		// Beads Classic metadata, with no backend key.
		nested{"classic/" + configfile.ConfigFileName, workspace(configfile.ConfigFileName, `{"database":"beads.db","jsonl_export":"issues.jsonl"}`)},
		nested{"classic/config.json", workspace("config.json", `{"database":"beads.db"}`)},
		// Without metadata, admission recognizes the workspace by its layout,
		// which allows one .db file only, so this one has no backup.
		nested{"classic/no metadata", map[string]string{"beads.db": sqliteDB, "beads.db-wal": "wal", "beads.db-shm": "shm"}},
	)
	for _, ws := range workspaces {
		t.Run(ws.name, func(t *testing.T) {
			root := t.TempDir()
			rootWAL := filepath.Join(root, ".beads", "beads.db-wal")
			writeBeadsTree(t, filepath.Dir(rootWAL), map[string]string{"beads.db-wal": "wal", configfile.ConfigFileName: doltMetadata})
			beadsDir := filepath.Join(root, "sub", ".beads")
			writeBeadsTree(t, beadsDir, ws.files)
			want := snapshotBeadsTree(t, beadsDir)

			if err := ClassicArtifacts(root, io.Discard); err != nil {
				t.Fatalf("ClassicArtifacts: %v", err)
			}
			if got := snapshotBeadsTree(t, beadsDir); !reflect.DeepEqual(got, want) {
				t.Fatalf("ClassicArtifacts changed the %s workspace %s:\n got %q\nwant %q", ws.name, beadsDir, got, want)
			}
			if _, err := os.Stat(rootWAL); !os.IsNotExist(err) {
				t.Fatalf("ClassicArtifacts left the root workspace's %s (stat err %v)", rootWAL, err)
			}
		})
	}
}

// TestClassicArtifacts_KeepsWhatGitTracksInAStub pins, against a real clone,
// that the stub rule never deletes a file git tracks. An orchestrator's crew
// worktree of a repository that tracks .beads files holds them beside the
// redirect it writes, and the cleanup deleted every one of them (and an
// uncommitted edit with it), so git status showed the clone's own files
// deleted. Now it removes only runtime leftovers bd's .gitignore keeps out
// of git (here the former daemon's pid file: beside the clone's own
// config.yaml bd's state files stay too), and the worktree's status is what
// it was.
func TestClassicArtifacts_KeepsWhatGitTracksInAStub(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "crew", "alice")
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, clone); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = clone
		cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	beadsDir := filepath.Join(clone, ".beads")
	writeBeadsTree(t, beadsDir, map[string]string{
		".gitignore":                          "redirect\n.local_version\nlast-touched\ndaemon.pid\n",
		"config.yaml":                         "issue-prefix: rig\n",
		"README.md":                           "# Beads\n",
		"formulas/beads-release.formula.toml": "formula = \"beads-release\"\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "track .beads")
	writeBeadsTree(t, beadsDir, map[string]string{
		"redirect":       "../../mayor/rig/.beads\n",
		".local_version": "0.62.0\n",
		"last-touched":   "rig-1\n",
		"daemon.pid":     "1",
		"config.yaml":    "issue-prefix: rig\n# uncommitted edit\n",
	})
	status := git("status", "--porcelain")
	kept := map[string]string{}
	for rel, content := range snapshotBeadsTree(t, beadsDir) {
		if rel != "daemon.pid" {
			kept[rel] = content
		}
	}

	if err := ClassicArtifacts(root, io.Discard); err != nil {
		t.Fatalf("ClassicArtifacts: %v", err)
	}
	if got := git("status", "--porcelain"); got != status {
		t.Fatalf("git status after the cleanup:\n%s\nwant it unchanged:\n%s", got, status)
	}
	if got := snapshotBeadsTree(t, beadsDir); !reflect.DeepEqual(got, kept) {
		t.Fatalf("ClassicArtifacts left %s as\n %q\nwant everything but the daemon's leftover:\n %q", beadsDir, got, kept)
	}
}
