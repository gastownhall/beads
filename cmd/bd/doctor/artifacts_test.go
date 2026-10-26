package doctor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backendnames"
)

func TestCheckClassicArtifacts_NoArtifacts(t *testing.T) {
	dir := t.TempDir()

	// Create a basic .beads directory with no artifacts
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckClassicArtifacts(dir)
	if check.Status != StatusOK {
		t.Errorf("expected StatusOK, got %s: %s", check.Status, check.Message)
	}
}

// TestScanForArtifacts_JSONLInDoltDir — JSONL artifact scanning removed (bd-9ni.2)
func TestScanForArtifacts_JSONLInDoltDir(t *testing.T) {
	t.Skip("JSONL artifact scanning removed as part of JSONL removal (bd-9ni.2)")
}

func TestScanForArtifacts_SQLiteFiles(t *testing.T) {
	dir := t.TempDir()

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create dolt/ directory so isDoltNative returns true (SQLite files
	// are only flagged as artifacts when Dolt is the active backend)
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create SQLite artifacts
	for _, name := range []string{"beads.db", "beads.db-shm", "beads.db-wal", "beads.backup-20260204.db"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.SQLiteArtifacts) != 4 {
		t.Errorf("expected 4 SQLite artifacts, got %d", len(report.SQLiteArtifacts))
	}

	// None is safe to delete: beads.db and the backup are databases, and the
	// WAL and SHM belong to the beads.db beside them.
	for _, f := range report.SQLiteArtifacts {
		if f.SafeDelete {
			t.Errorf("%s should NOT be safe to delete", filepath.Base(f.Path))
		}
	}
}

func TestScanForArtifacts_CruftBeadsDir(t *testing.T) {
	// Create a structure like: beads/polecats/testpolecat/.beads/ with extra files
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "testpolecat")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Add redirect file (expected)
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}

	// Add a runtime leftover, which is safe to delete, beside files that
	// are not (a database would make it a workspace)
	writeArtifactTree(t, beadsDir, map[string]string{"daemon.log": "log", "config.yaml": "data", "issues.jsonl": "data"})

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	// The directory keeps files that must stay, so it is reported for
	// review, not as safe to delete; the leftover in it is a finding of its
	// own, safe to delete.
	want := []ArtifactFinding{
		{Path: beadsDir, Type: fix.ArtifactCruft, Description: "should be redirect-only but contains files the stub cleanup does not remove: config.yaml, issues.jsonl; review manually"},
		{Path: filepath.Join(beadsDir, "daemon.log"), Type: fix.ArtifactCruft, Description: "bd runtime leftover in a redirect-only .beads", SafeDelete: true},
	}
	if !reflect.DeepEqual(report.CruftBeadsDirs, want) {
		t.Errorf("cruft findings = %+v, want %+v", report.CruftBeadsDirs, want)
	}
	if report.SafeDeleteCount != 1 {
		t.Errorf("SafeDeleteCount = %d, want 1", report.SafeDeleteCount)
	}
}

func TestScanForArtifacts_CruftBeadsDirNoRedirect(t *testing.T) {
	// Cruft dir without redirect should be detected, but its config.yaml is
	// not a bd runtime leftover (it can be a server-mode workspace's only
	// configuration, or a file the checkout tracks), so it is kept for review.
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "testpolecat")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No redirect file, just extra files
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Should be detected as cruft (it's in a polecat location), for review
	if len(report.CruftBeadsDirs) != 1 {
		t.Errorf("expected 1 cruft beads dir, got %d", len(report.CruftBeadsDirs))
	}
	if len(report.CruftBeadsDirs) > 0 && report.CruftBeadsDirs[0].SafeDelete {
		t.Error("a config.yaml in a redirect-expected location should not be safe to delete")
	}
}

func TestScanForArtifacts_CrewDir(t *testing.T) {
	dir := t.TempDir()
	crewDir := filepath.Join(dir, "crew", "testcrew")
	beadsDir := filepath.Join(crewDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Add redirect and cruft
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "extra.txt"), []byte("cruft"), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.CruftBeadsDirs) != 1 {
		t.Errorf("expected 1 cruft beads dir for crew, got %d", len(report.CruftBeadsDirs))
	}
}

func TestScanForArtifacts_RedirectValidation(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create redirect pointing to nonexistent target
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("/nonexistent/path"), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.RedirectIssues) != 1 {
		t.Errorf("expected 1 redirect issue, got %d", len(report.RedirectIssues))
	}
}

func TestScanForArtifacts_EmptyRedirect(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create empty redirect
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.RedirectIssues) != 1 {
		t.Errorf("expected 1 redirect issue (empty), got %d", len(report.RedirectIssues))
	}
}

func TestScanForArtifacts_ValidRedirect(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	targetDir := filepath.Join(dir, "target-beads")

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A valid redirect target must have a metadata.json or database
	// (gastownhall/beads#4692 guard).
	if err := os.WriteFile(filepath.Join(targetDir, "metadata.json"), []byte(`{"database":"beads.db"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create redirect pointing to valid target
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(targetDir), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.RedirectIssues) != 0 {
		t.Errorf("expected 0 redirect issues for valid target, got %d", len(report.RedirectIssues))
	}
}

// TestScanForArtifacts_RedirectTargetHasNoDatabase is a regression test for
// gastownhall/beads#4692: a redirect target directory that exists but has no
// metadata.json and no recognizable database is flagged as an actionable
// warning (not SafeDelete cruft), matching FollowRedirect's own target-
// validity guard (internal/beads).
func TestScanForArtifacts_RedirectTargetHasNoDatabase(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	targetDir := filepath.Join(dir, "target-beads")

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	// targetDir exists but is empty: no metadata.json, no database.

	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(targetDir), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.RedirectIssues) != 1 {
		t.Fatalf("expected 1 redirect issue (target has no database), got %d", len(report.RedirectIssues))
	}
	if report.RedirectIssues[0].SafeDelete {
		t.Errorf("expected SafeDelete=false: an invalid-target redirect is an actionable warning, not cruft")
	}
}

func TestScanForArtifacts_SkipsGitkeep(t *testing.T) {
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "test")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// redirect + .gitkeep only = no cruft
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitkeep"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.CruftBeadsDirs) != 0 {
		t.Errorf("expected 0 cruft dirs (redirect + .gitkeep only), got %d", len(report.CruftBeadsDirs))
	}
}

func TestScanForArtifacts_SkipsGitInternalsButScansBeadsWorktrees(t *testing.T) {
	dir := t.TempDir()

	// A bogus .beads under .git internals should be ignored entirely.
	ignoredBeads := filepath.Join(dir, ".git", "objects", "pack", ".beads")
	if err := os.MkdirAll(ignoredBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignoredBeads, "beads.db"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	// A worktree .beads under .git/beads-worktrees must still be scanned.
	worktreeBeads := filepath.Join(dir, ".git", "beads-worktrees", "test", ".beads")
	if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeBeads, "extra.txt"), []byte("cruft"), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := ScanForArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.SQLiteArtifacts) != 0 {
		t.Fatalf("expected .git internals to be skipped, got %d sqlite findings", len(report.SQLiteArtifacts))
	}
	if len(report.CruftBeadsDirs) != 1 {
		t.Fatalf("expected 1 worktree cruft finding, got %d", len(report.CruftBeadsDirs))
	}
	if got := report.CruftBeadsDirs[0].Path; got != worktreeBeads {
		t.Fatalf("cruft finding path = %q, want %q", got, worktreeBeads)
	}
}

// TestScanForArtifacts_NonEmptyInteractionsJSONL — JSONL artifact scanning removed (bd-9ni.2)
func TestScanForArtifacts_NonEmptyInteractionsJSONL(t *testing.T) {
	t.Skip("JSONL artifact scanning removed as part of JSONL removal (bd-9ni.2)")
}

func TestCheckClassicArtifacts_WithArtifacts(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create dolt/ directory so isDoltNative returns true
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a SQLite artifact
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db-wal"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckClassicArtifacts(dir)
	if check.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %s: %s", check.Status, check.Message)
	}
	if check.Name != "Classic Artifacts" {
		t.Errorf("expected name 'Classic Artifacts', got %q", check.Name)
	}
}

// writeArtifactTree creates each entry under dir: a name ending in "/" is an
// empty directory, anything else a file holding its value.
func writeArtifactTree(t *testing.T, dir string, entries map[string]string) {
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

// TestScanForArtifacts_SkipsUnsupportedWorkspaceFormat pins that the Classic
// Artifacts scan, which plain bd doctor and bd doctor --check=artifacts run,
// leaves a .beads directory in a format this build cannot open byte-identical
// and reports nothing in it. The scan walks every .beads under the path,
// including those of nested repositories and worktrees, which command
// admission never checks, and IsDoltBackend classified each one with the
// migrating configfile.Load: a graph workspace's legacy config.json became a
// metadata.json without the graph_* keys this build does not know. Each
// layout also holds artifacts, and the redirect-expected ones cruft, that the
// scan would otherwise report; the admitted workspace at the root is still
// scanned.
func TestScanForArtifacts_SkipsUnsupportedWorkspaceFormat(t *testing.T) {
	const graphMetadata = `{"database":"beads.db","backend":"dolt","graph_mode":"%s","graph_future_binding":"keep-me"}`
	const strippedMetadata = `{"database":"beads.db","backend":"dolt"}`
	layouts := []struct {
		name  string
		dir   string // the workspace's .beads, relative to the scanned root
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
		for _, entry := range []string{"ScanForArtifacts", "CheckClassicArtifacts"} {
			t.Run(layout.name+"/"+entry, func(t *testing.T) {
				root := t.TempDir()
				rootWAL := filepath.Join(root, ".beads", "beads.db-wal")
				writeArtifactTree(t, filepath.Dir(rootWAL), map[string]string{"beads.db-wal": "wal", configfile.ConfigFileName: `{"backend":"dolt"}`})
				beadsDir := filepath.Join(root, filepath.FromSlash(layout.dir))
				entries := map[string]string{
					"dolt/":                    "",
					"beads.db-wal":             "wal",
					"beads.backup-20260204.db": "backup",
				}
				for name, content := range layout.files {
					entries[name] = content
				}
				writeArtifactTree(t, beadsDir, entries)
				want := snapshotBeadsDir(t, beadsDir)

				if entry == "ScanForArtifacts" {
					report, err := ScanForArtifacts(root)
					if err != nil {
						t.Fatalf("ScanForArtifacts: %v", err)
					}
					var paths []string
					for _, findings := range [][]ArtifactFinding{report.SQLiteArtifacts, report.CruftBeadsDirs, report.RedirectIssues} {
						for _, f := range findings {
							paths = append(paths, f.Path)
						}
					}
					if !reflect.DeepEqual(paths, []string{rootWAL}) {
						t.Fatalf("ScanForArtifacts findings = %q, want only the root workspace's %s", paths, rootWAL)
					}
				} else {
					check := CheckClassicArtifacts(root)
					if check.Status != StatusWarning || check.Message != "1 SQLite artifact(s)" {
						t.Fatalf("CheckClassicArtifacts = %s %q, want warning %q for the root workspace only", check.Status, check.Message, "1 SQLite artifact(s)")
					}
				}
				if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s changed %s:\n got %q\nwant %q", entry, layout.dir, got, want)
				}
			})
		}
	}
}

// TestScanForArtifacts_DoesNotMigrateLegacyConfig pins that the scan reads a
// nested workspace's legacy config.json without migrating it into
// metadata.json, and still classifies that workspace by what it says: SQLite
// files are artifacts only when its backend is Dolt.
func TestScanForArtifacts_DoesNotMigrateLegacyConfig(t *testing.T) {
	cases := []struct {
		name   string
		legacy string
		want   []string // reported artifacts, relative to the nested .beads
	}{
		{name: "dolt backend", legacy: `{"database":"beads.db","backend":"dolt","future_key":"keep-me"}`, want: []string{"beads.db-wal"}},
		{name: "sqlite backend", legacy: `{"database":"beads.db","backend":"sqlite","future_key":"keep-me"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, "sub", ".beads")
			writeArtifactTree(t, beadsDir, map[string]string{
				"config.json":  tc.legacy,
				"beads.db-wal": "wal",
			})

			report, err := ScanForArtifacts(root)
			if err != nil {
				t.Fatalf("ScanForArtifacts: %v", err)
			}
			var got []string
			for _, f := range report.SQLiteArtifacts {
				rel, err := filepath.Rel(beadsDir, f.Path)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, filepath.ToSlash(rel))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SQLite artifacts = %q, want %q", got, tc.want)
			}
			if data, err := os.ReadFile(filepath.Join(beadsDir, "config.json")); err != nil || string(data) != tc.legacy {
				t.Fatalf("config.json = %q (read err %v), want it unchanged: %q", data, err, tc.legacy)
			}
			if _, err := os.Stat(filepath.Join(beadsDir, configfile.ConfigFileName)); !os.IsNotExist(err) {
				t.Fatalf("ScanForArtifacts wrote %s (stat err %v)", configfile.ConfigFileName, err)
			}
		})
	}
}

// TestScanForArtifacts_MetadataAlongsideRedirectIsNotCruft pins that the scan
// reports in a redirect-only .beads exactly what the cleanup removes there.
// Beside a redirect the cleanup keeps the directory's own metadata, a
// metadata.json or a legacy config.json not yet migrated into one
// (gastownhall/beads#4692), so the scan lists only the unrelated file, the
// cleanup leaves the metadata byte-identical, and a second scan has nothing
// left to report. Without a redirect, metadata that configures Dolt is the
// workspace's own record of its database (a server-mode workspace keeps no
// local store), so the directory is reported for review and left
// byte-identical; so is stale pre-Dolt metadata, which is not a bd runtime
// leftover either.
func TestScanForArtifacts_MetadataAlongsideRedirectIsNotCruft(t *testing.T) {
	const metadata = `{"dolt_mode":"server","dolt_database":"lola","future_key":"keep-me"}`
	const preDolt = `{"database":"beads.db","jsonl_export":"issues.jsonl"}`
	const redirect = "../../mayor/rig/.beads\n"
	const review = "should be redirect-only but its metadata configures Dolt and it has no redirect; review manually"
	cases := []struct {
		name  string
		files map[string]string // the polecat's .beads
		want  ArtifactFinding   // the scan's one finding, Path and Type aside
		after map[string]string // the .beads after the cleanup; nil if unchanged
	}{
		{
			name:  "metadata.json with redirect",
			files: map[string]string{"redirect": redirect, configfile.ConfigFileName: metadata, "daemon.pid": "1"},
			want:  ArtifactFinding{Description: "should be redirect-only but contains: daemon.pid", SafeDelete: true},
			after: map[string]string{".": "/", "redirect": redirect, configfile.ConfigFileName: metadata},
		},
		{
			name:  "legacy config.json with redirect",
			files: map[string]string{"redirect": redirect, "config.json": metadata, "daemon.pid": "1"},
			want:  ArtifactFinding{Description: "should be redirect-only but contains: daemon.pid", SafeDelete: true},
			after: map[string]string{".": "/", "redirect": redirect, "config.json": metadata},
		},
		{
			name:  "metadata.json configuring Dolt without redirect",
			files: map[string]string{configfile.ConfigFileName: metadata, "extra.txt": "cruft"},
			want:  ArtifactFinding{Description: review},
		},
		{
			name:  "legacy config.json configuring Dolt without redirect",
			files: map[string]string{"config.json": metadata, "extra.txt": "cruft"},
			want:  ArtifactFinding{Description: review},
		},
		{
			name:  "pre-Dolt metadata.json without redirect",
			files: map[string]string{configfile.ConfigFileName: preDolt, "extra.txt": "cruft"},
			want:  ArtifactFinding{Description: "should be redirect-only but contains files the stub cleanup does not remove: extra.txt, metadata.json; review manually"},
		},
		{
			name:  "pre-Dolt legacy config.json without redirect",
			files: map[string]string{"config.json": preDolt, "extra.txt": "cruft"},
			want:  ArtifactFinding{Description: "should be redirect-only but contains files the stub cleanup does not remove: config.json, extra.txt; review manually"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeArtifactTree(t, filepath.Join(root, "mayor", "rig", ".beads"), map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`})
			beadsDir := filepath.Join(root, "polecats", "p1", ".beads")
			writeArtifactTree(t, beadsDir, tc.files)
			after := tc.after
			if after == nil {
				after = snapshotBeadsDir(t, beadsDir)
			}

			report, err := ScanForArtifacts(root)
			if err != nil {
				t.Fatalf("ScanForArtifacts: %v", err)
			}
			want := tc.want
			want.Path, want.Type = beadsDir, "cruft-beads"
			if report.TotalCount != 1 || !reflect.DeepEqual(report.CruftBeadsDirs, []ArtifactFinding{want}) {
				t.Fatalf("ScanForArtifacts = %+v, want only the cruft finding %+v", report, want)
			}

			if err := fix.ClassicArtifacts(root, io.Discard); err != nil {
				t.Fatalf("fix.ClassicArtifacts: %v", err)
			}
			if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, after) {
				t.Fatalf("fix.ClassicArtifacts left %s as\n %q\nwant %q", beadsDir, got, after)
			}
			// A removed finding leaves nothing to report; one kept for review
			// is reported again.
			left := 1
			if want.SafeDelete {
				left = 0
			}
			if report, err = ScanForArtifacts(root); err != nil || report.TotalCount != left {
				t.Fatalf("ScanForArtifacts after the cleanup = %+v (err %v), want %d finding(s)", report, err, left)
			}
		})
	}
}

// TestScanForArtifacts_LeavesWorkspaceWithUnloadableMetadataForReview pins
// that a .beads whose metadata does not load is scanned (it is not a format
// refusal) but nothing in it is safe to delete: doctor cannot establish its
// backend, so its SQLite files and, in a redirect-only location, the
// directory are reported for manual review, and the cleanup leaves it
// byte-identical.
func TestScanForArtifacts_LeavesWorkspaceWithUnloadableMetadataForReview(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, "polecats", "p1", ".beads")
	writeArtifactTree(t, beadsDir, map[string]string{
		configfile.ConfigFileName: "stale",
		"beads.db-wal":            "wal",
		"extra.txt":               "cruft",
	})
	before := snapshotBeadsDir(t, beadsDir)

	report, err := ScanForArtifacts(root)
	if err != nil {
		t.Fatalf("ScanForArtifacts: %v", err)
	}
	wantSQLite := []ArtifactFinding{{Path: filepath.Join(beadsDir, "beads.db-wal"), Type: "sqlite", Description: "SQLite WAL/SHM without its database (backend unknown: metadata.json does not load)"}}
	if !reflect.DeepEqual(report.SQLiteArtifacts, wantSQLite) {
		t.Errorf("SQLite artifacts = %+v, want %+v", report.SQLiteArtifacts, wantSQLite)
	}
	wantCruft := []ArtifactFinding{{Path: beadsDir, Type: "cruft-beads", Description: "should be redirect-only but metadata.json does not load; review manually"}}
	if !reflect.DeepEqual(report.CruftBeadsDirs, wantCruft) {
		t.Errorf("cruft .beads dirs = %+v, want %+v", report.CruftBeadsDirs, wantCruft)
	}
	if report.SafeDeleteCount != 0 {
		t.Errorf("SafeDeleteCount = %d, want 0", report.SafeDeleteCount)
	}
	if err := fix.ClassicArtifacts(root, io.Discard); err != nil {
		t.Fatalf("fix.ClassicArtifacts: %v", err)
	}
	if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, before) {
		t.Fatalf("fix.ClassicArtifacts changed %s:\n got %q\nwant %q", beadsDir, got, before)
	}
}

// TestScanForArtifacts_CleanupRemovesOnlyReportedSQLiteFiles pins that the
// scan and the cleanup (fix.ClassicArtifacts) agree on a nested workspace's
// SQLite files, which the walk reaches though command admission never checks
// that workspace: the scan reports them only where the workspace is on Dolt
// (fix.PlanArtifacts, through classifyBeadsDir and isDatabaseState), the
// cleanup removes exactly the ones it marks safe to delete, and a second scan
// finds only what is left for manual review. The
// cleanup used to remove beads.db-wal, beads.db-shm and every
// beads.backup-*.db whatever the backend, files the scan never reported, as
// soon as the scan found anything at all, here the root workspace's WAL. A
// registered backend (such as http in the shipped bd; a test-only name stands
// in for it here) is not Dolt, and a backend this build does not recognize,
// which configfile.Config.GetBackend reads as Dolt, is not Dolt to either
// half. Nor is a Beads Classic SQLite workspace, whose metadata predates the
// backend key and so reads as Dolt to GetBackend too: command admission
// refuses it as historical SQLite (configfile.IsHistoricalSQLiteWorkspace), and its WAL
// can hold transactions not yet checkpointed into beads.db.
func TestScanForArtifacts_CleanupRemovesOnlyReportedSQLiteFiles(t *testing.T) {
	const registered = "acme-artifacts-doctor"
	backendnames.Add(registered)
	t.Cleanup(func() { backendnames.Remove(registered) })

	const sqliteDB = "SQLite format 3\x00"
	// workspace is a nested .beads directory holding metadata, in
	// metadataFile, and every SQLite file the scan can report.
	workspace := func(metadataFile, metadata string) map[string]string {
		return map[string]string{
			metadataFile:               metadata,
			"beads.db":                 sqliteDB,
			"beads.db-wal":             "wal",
			"beads.db-shm":             "shm",
			"beads.backup-20260204.db": "backup",
		}
	}
	named := func(backend string) map[string]string {
		return workspace(configfile.ConfigFileName, fmt.Sprintf(`{"database":"dolt","backend":%q}`, backend))
	}
	cases := []struct {
		name     string
		files    map[string]string // the nested .beads directory's entries
		reported map[string]bool   // the nested files the scan reports, and whether each is safe to delete
	}{
		// On Dolt nothing beside a beads.db is safe: the WAL and SHM are part of
		// it, and the backup is a whole database.
		{name: configfile.BackendDolt, files: named(configfile.BackendDolt), reported: map[string]bool{"beads.db": false, "beads.db-wal": false, "beads.db-shm": false, "beads.backup-20260204.db": false}},
		// Without its beads.db, an orphaned WAL and SHM on Dolt are.
		{name: "dolt without beads.db", files: map[string]string{configfile.ConfigFileName: `{"database":"dolt","backend":"dolt"}`, "beads.db-wal": "wal", "beads.db-shm": "shm", "beads.backup-20260204.db": "backup"}, reported: map[string]bool{"beads.db-wal": true, "beads.db-shm": true, "beads.backup-20260204.db": false}},
		{name: configfile.BackendSQLite, files: named(configfile.BackendSQLite)},
		{name: configfile.BackendPostgres, files: named(configfile.BackendPostgres)},
		{name: configfile.BackendMySQL, files: named(configfile.BackendMySQL)},
		{name: registered, files: named(registered)},
		{name: "future-backend", files: named("future-backend")},
		// Beads Classic metadata, with no backend key.
		{name: "classic metadata.json", files: workspace(configfile.ConfigFileName, `{"database":"beads.db","jsonl_export":"issues.jsonl"}`)},
		{name: "classic config.json", files: workspace("config.json", `{"database":"beads.db"}`)},
		// Without metadata, admission recognizes the workspace by its layout,
		// which allows one .db file only, so this one has no backup.
		{name: "classic without metadata", files: map[string]string{"beads.db": sqliteDB, "beads.db-wal": "wal", "beads.db-shm": "shm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rootWAL := filepath.Join(root, ".beads", "beads.db-wal")
			writeArtifactTree(t, filepath.Dir(rootWAL), map[string]string{"beads.db-wal": "wal", configfile.ConfigFileName: `{"backend":"dolt"}`})
			beadsDir := filepath.Join(root, "sub", ".beads")
			writeArtifactTree(t, beadsDir, tc.files)
			before := snapshotBeadsDir(t, beadsDir)

			report, err := ScanForArtifacts(root)
			if err != nil {
				t.Fatalf("ScanForArtifacts: %v", err)
			}
			want := map[string]bool{rootWAL: true}
			for name, safe := range tc.reported {
				want[filepath.Join(beadsDir, name)] = safe
			}
			got := map[string]bool{}
			for _, f := range report.SQLiteArtifacts {
				got[f.Path] = f.SafeDelete
			}
			if report.TotalCount != len(report.SQLiteArtifacts) || !reflect.DeepEqual(got, want) {
				t.Fatalf("ScanForArtifacts = %+v\nwant only the SQLite findings (path: safe to delete) %v", report, want)
			}

			if err := fix.ClassicArtifacts(root, io.Discard); err != nil {
				t.Fatalf("fix.ClassicArtifacts: %v", err)
			}
			after := map[string]string{}
			for name, content := range before {
				if !tc.reported[name] {
					after[name] = content
				}
			}
			if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, after) {
				t.Fatalf("fix.ClassicArtifacts left %s as\n %q\nwant only the files the scan reported as safe to delete removed: %q", beadsDir, got, after)
			}
			if _, err := os.Stat(rootWAL); !os.IsNotExist(err) {
				t.Fatalf("fix.ClassicArtifacts left the root workspace's %s (stat err %v)", rootWAL, err)
			}

			report, err = ScanForArtifacts(root)
			if err != nil {
				t.Fatalf("ScanForArtifacts after the cleanup: %v", err)
			}
			left := map[string]bool{}
			for _, f := range report.SQLiteArtifacts {
				left[f.Path] = f.SafeDelete
			}
			wantLeft := map[string]bool{}
			for name, safe := range tc.reported {
				if !safe {
					wantLeft[filepath.Join(beadsDir, name)] = false
				}
			}
			if report.TotalCount != len(report.SQLiteArtifacts) || !reflect.DeepEqual(left, wantLeft) {
				t.Fatalf("ScanForArtifacts after the cleanup = %+v\nwant only the findings left for manual review %v", report, wantLeft)
			}
		})
	}
}

// TestCheckClassicArtifacts_OffersFixOnlyWhenSomethingIsSafe pins that the
// check sends bd doctor --fix at the Classic Artifacts cleanup only when that
// cleanup would remove something. Findings that need manual review (a
// database, or a .beads whose backend doctor cannot establish) still warn,
// but a fix that removes nothing would otherwise be reported as fixed after
// every run.
func TestCheckClassicArtifacts_OffersFixOnlyWhenSomethingIsSafe(t *testing.T) {
	cases := []struct {
		name    string
		dir     string            // the .beads, relative to the root; "" is the root's
		files   map[string]string // its entries
		wantFix bool
	}{
		{name: "orphaned WAL on Dolt", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "beads.db-wal": "w"}, wantFix: true},
		{name: "database on Dolt", files: map[string]string{configfile.ConfigFileName: `{"backend":"dolt"}`, "beads.db": "d", "beads.db-wal": "w"}},
		{name: "backend undetermined", files: map[string]string{"beads.db-wal": "w"}},
		{name: "stub holding a runtime leftover", dir: "crew/alice/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "daemon.pid": "1", "README.md": "r"}, wantFix: true},
		{name: "stub holding only what is not a runtime leftover", dir: "crew/alice/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", "config.yaml": "c", "formulas/x.toml": "f"}},
		// bd opens a stub whose redirect it ignores, so its state files are
		// not leftovers.
		{name: "stub holding bd's state files beside a redirect bd ignores", dir: "crew/alice/.beads", files: map[string]string{"redirect": "../../mayor/rig/.beads\n", ".local_version": "0.62.0\n", "last-touched": "rig-1\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := tc.dir
			if dir == "" {
				dir = ".beads"
			}
			writeArtifactTree(t, filepath.Join(root, filepath.FromSlash(dir)), tc.files)
			check := CheckClassicArtifacts(root)
			if check.Status != StatusWarning {
				t.Fatalf("CheckClassicArtifacts = %s %q, want a warning", check.Status, check.Message)
			}
			if gotFix := check.Fix != ""; gotFix != tc.wantFix {
				t.Fatalf("CheckClassicArtifacts Fix = %q, want offered = %v", check.Fix, tc.wantFix)
			}
		})
	}
}
