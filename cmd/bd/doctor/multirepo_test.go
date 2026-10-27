package doctor

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

func TestReadTypesFromYAML_FlowSequence(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "types:\n  custom: [bug-report, feature-request, hotfix]\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	types, err := readTypesFromYAML(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"bug-report", "feature-request", "hotfix"}
	if len(types) != len(want) {
		t.Fatalf("got %d types, want %d", len(types), len(want))
	}
	for i, got := range types {
		if got != want[i] {
			t.Errorf("types[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestReadTypesFromYAML_BlockSequence(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "types:\n  custom:\n    - mail\n    - molecule\n    - wisp\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	types, err := readTypesFromYAML(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"mail", "molecule", "wisp"}
	if len(types) != len(want) {
		t.Fatalf("got %d types, want %d", len(types), len(want))
	}
	for i, got := range types {
		if got != want[i] {
			t.Errorf("types[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestReadTypesFromYAML_Empty(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "types:\n  custom: []\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	types, err := readTypesFromYAML(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if types != nil {
		t.Errorf("expected nil, got %v", types)
	}
}

func TestReadTypesFromYAML_NoTypesSection(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "backend: dolt\nsync-branch: main\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	types, err := readTypesFromYAML(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if types != nil {
		t.Errorf("expected nil, got %v", types)
	}
}

func TestReadTypesFromYAML_MissingFile(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := readTypesFromYAML(beadsDir)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadTypesFromYAML_OtherKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "backend: dolt\ntypes:\n  custom:\n    - mr\n  builtin:\n    - bug\nother: value\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	types, err := readTypesFromYAML(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(types) != 1 || types[0] != "mr" {
		t.Errorf("got %v, want [mr]", types)
	}
}

// snapshotBeadsDir returns every entry under beadsDir, keyed by its
// slash-separated relative path: a file maps to its bytes, a directory to "/".
func snapshotBeadsDir(t *testing.T, beadsDir string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(beadsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(beadsDir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			snapshot[filepath.ToSlash(rel)] = "/"
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

// TestDiscoverChildTypesLeavesGraphWorkspaceUnchanged pins that bd doctor's
// multi-repo type discovery refuses an additional repo in a format this build
// cannot open before anything migrates its metadata. Command admission only
// checks the workspace doctor runs in, never the repos listed under
// repos.additional, so the migrating configfile.Load in readTypesFromDB used
// to turn a graph workspace's legacy config.json into metadata.json (dropping
// graph_* keys this build does not know) before the Dolt constructor refused
// it. Each layout runs with a .beads/dolt directory, so that discovery would
// reach that constructor, and without one, so that discovery's own refusal is
// what the test sees; each carries a config.yaml whose types the YAML
// fallback still reports.
func TestDiscoverChildTypesLeavesGraphWorkspaceUnchanged(t *testing.T) {
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	const graphMetadata = `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0123","graph_mode":"%s","graph_future_binding":"keep-me"}`
	const strippedMetadata = `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0123"}`
	layouts := []struct {
		name      string
		files     map[string]string
		graphMode string // the refused graph_mode; "" for the marker refusal
	}{
		{name: "graph_mode in legacy config.json", files: map[string]string{"config.json": fmt.Sprintf(graphMetadata, configfile.GraphModeLink)}, graphMode: configfile.GraphModeLink},
		{name: "future graph_mode in legacy config.json", files: map[string]string{"config.json": fmt.Sprintf(graphMetadata, "future-format")}, graphMode: "future-format"},
		{name: "marker beside legacy config.json", files: map[string]string{"config.json": strippedMetadata, configfile.GraphFormatMarkerFile: "link-preview-v9\n"}},
		{name: "graph_mode in metadata.json", files: map[string]string{configfile.ConfigFileName: fmt.Sprintf(graphMetadata, configfile.GraphModeLink)}, graphMode: configfile.GraphModeLink},
		{name: "marker beside stripped metadata.json", files: map[string]string{configfile.ConfigFileName: strippedMetadata, configfile.GraphFormatMarkerFile: "link-preview-v9\n"}},
	}
	for _, layout := range layouts {
		for _, doltRoot := range []bool{true, false} {
			subtest := layout.name + "/no dolt root"
			if doltRoot {
				subtest = layout.name + "/dolt root"
			}
			t.Run(subtest, func(t *testing.T) {
				repo := t.TempDir()
				beadsDir := filepath.Join(repo, ".beads")
				if err := os.MkdirAll(beadsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if doltRoot {
					if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				files := map[string]string{"config.yaml": "types:\n  custom: [graph-node]\n"}
				for name, content := range layout.files {
					files[name] = content
				}
				for name, content := range files {
					if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				want := snapshotBeadsDir(t, beadsDir)

				_, err := readTypesFromDB(beadsDir)
				var formatErr *configfile.UnsupportedWorkspaceFormatError
				if !errors.As(err, &formatErr) || formatErr.BeadsDir != beadsDir ||
					formatErr.GraphMode != layout.graphMode || formatErr.FormatMarker != (layout.graphMode == "") {
					t.Fatalf("readTypesFromDB error = %v, want UnsupportedWorkspaceFormatError for %s", err, beadsDir)
				}
				if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, want) {
					t.Fatalf("readTypesFromDB changed the workspace:\n got %q\nwant %q", got, want)
				}

				got, refused := discoverChildTypes(repo)
				if !reflect.DeepEqual(got, []string{"graph-node"}) {
					t.Fatalf("discoverChildTypes = %q, want the config.yaml fallback [graph-node]", got)
				}
				var refusedFormat *configfile.UnsupportedWorkspaceFormatError
				if !errors.As(refused, &refusedFormat) || refusedFormat.BeadsDir != ResolveBeadsDirForRepo(repo) ||
					refusedFormat.GraphMode != layout.graphMode || refusedFormat.FormatMarker != (layout.graphMode == "") {
					t.Fatalf("discoverChildTypes refusal = %v, want the UnsupportedWorkspaceFormatError readTypesFromDB returned", refused)
				}
				if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, want) {
					t.Fatalf("discoverChildTypes changed the workspace:\n got %q\nwant %q", got, want)
				}
			})
		}
	}
}

// TestDiscoverChildTypesLeavesLegacyConfigUnopenedUnchanged pins that type
// discovery reads an additional repo's legacy config.json without migrating
// it: only the store open itself may migrate it, so a child with no Dolt
// database for discovery to open keeps its config.json. A child whose
// database is present is opened, and reading its types needs a Dolt server;
// that path is outside this unit pin.
func TestDiscoverChildTypesLeavesLegacyConfigUnopenedUnchanged(t *testing.T) {
	// An ambient BEADS_DOLT_DATA_DIR naming an existing directory would make
	// the database look present, and the open would migrate config.json.
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	// If discovery reached the open anyway, it must fail fast rather than
	// reach the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config.json": `{"database":"beads.db","backend":"dolt","dolt_mode":"server","dolt_database":"beads_legacy_0123"}`,
		"config.yaml": "types:\n  custom: [legacy-node]\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := snapshotBeadsDir(t, beadsDir)

	if _, err := readTypesFromDB(beadsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readTypesFromDB error = %v, want the missing Dolt database", err)
	}
	if got, refused := discoverChildTypes(repo); !reflect.DeepEqual(got, []string{"legacy-node"}) || refused != nil {
		t.Fatalf("discoverChildTypes = %q, %v, want the config.yaml fallback [legacy-node] and no refusal", got, refused)
	}
	if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("type discovery changed the workspace:\n got %q\nwant %q", got, want)
	}
}

// TestDiscoverChildTypesLeavesUnrecognizedBackendUnchanged pins that type
// discovery refuses an additional repo whose metadata names a backend this
// build does not recognize before anything opens it. GetBackend reads such a
// backend as Dolt, and the store constructor's configfile.Load migrated a
// legacy config.json into metadata.json, dropping keys it does not know,
// before the constructor refused the backend, whenever a Dolt database
// directory was there for discovery to find.
func TestDiscoverChildTypesLeavesUnrecognizedBackendUnchanged(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "") // would override DatabasePath
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config.json": `{"database":"beads.db","backend":"future-backend","dolt_mode":"server","dolt_database":"beads_future_0123","future_binding":"keep-me"}`,
		"config.yaml": "types:\n  custom: [future-node]\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := snapshotBeadsDir(t, beadsDir)

	if _, err := readTypesFromDB(beadsDir); err == nil {
		t.Fatal("readTypesFromDB succeeded, want the unrecognized backend refused")
	}
	if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("readTypesFromDB changed the workspace:\n got %q\nwant %q", got, want)
	}
	if got, refused := discoverChildTypes(repo); !reflect.DeepEqual(got, []string{"future-node"}) || refused != nil {
		t.Fatalf("discoverChildTypes = %q, %v, want the config.yaml fallback [future-node] and no format refusal", got, refused)
	}
	if got := snapshotBeadsDir(t, beadsDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverChildTypes changed the workspace:\n got %q\nwant %q", got, want)
	}
}

// TestCheckMultiRepoTypesNamesRefusedWorkspace pins that the Multi-Repo Types
// check says so when it lists only an additional repo's config.yaml types
// because it refused that repo's workspace format, and why, instead of
// presenting them as the repo's types without a word. A repo whose database
// discovery simply does not find gets no such line.
func TestCheckMultiRepoTypesNamesRefusedWorkspace(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "") // would override DatabasePath
	// If a refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	newRepo := func(files map[string]string) string {
		t.Helper()
		repo := t.TempDir()
		beadsDir := filepath.Join(repo, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return repo
	}
	graphRepo := newRepo(map[string]string{
		configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0123","graph_mode":"link"}`,
		"config.yaml":             "types:\n  custom: [graph-node]\n",
	})
	markerRepo := newRepo(map[string]string{
		configfile.ConfigFileName:        `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0456"}`,
		configfile.GraphFormatMarkerFile: "link-preview-v9\n",
	})
	plainRepo := newRepo(map[string]string{
		configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_plain_0123"}`,
		"config.yaml":             "types:\n  custom: [plain-node]\n",
	})
	parent := newRepo(nil)
	config.Set("repos.primary", parent)
	config.Set("repos.additional", []string{graphRepo, markerRepo, plainRepo})

	check := CheckMultiRepoTypes(parent)
	const skipped = "    database not read, so only config.yaml types are listed: "
	want := strings.Join([]string{
		"  " + graphRepo + ": graph-node",
		skipped + (&configfile.UnsupportedWorkspaceFormatError{BeadsDir: ResolveBeadsDirForRepo(graphRepo), GraphMode: configfile.GraphModeLink}).Error(),
		"  " + markerRepo + ": (no custom types)",
		skipped + (&configfile.UnsupportedWorkspaceFormatError{BeadsDir: ResolveBeadsDirForRepo(markerRepo), FormatMarker: true}).Error(),
		"  " + plainRepo + ": plain-node",
	}, "\n")
	if check.Status != StatusOK || check.Detail != want {
		t.Fatalf("CheckMultiRepoTypes = %s %q, detail:\n%s\nwant %s, detail:\n%s", check.Status, check.Message, check.Detail, StatusOK, want)
	}
}

// TestCheckMultiRepoTypesNamesHistoricalSQLiteRepo pins that the Multi-Repo
// Types check reads a repos.additional repo that is a historical SQLite
// workspace without changing it, and says it did not read its database
// because admission refuses that workspace, with admission's text, as it does
// for a workspace format this build cannot open.
func TestCheckMultiRepoTypesNamesHistoricalSQLiteRepo(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "") // would override DatabasePath
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		t.Run(layout.Name, func(t *testing.T) {
			config.ResetForTesting()
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			t.Cleanup(config.ResetForTesting)
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			layout.Write(t, beadsDir)
			// A config.yaml beside a metadata-less SQLite database is not the
			// layout a pre-Dolt bd left, and admission does not refuse it.
			types := "(no custom types)"
			if _, metadataLess := layout.Files["vc.db"]; !metadataLess {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("types:\n  custom: [legacy-node]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				types = "legacy-node"
			}
			before := legacyworkspace.TreeDigest(t, beadsDir)
			parent := t.TempDir()
			config.Set("repos.primary", parent)
			config.Set("repos.additional", []string{repo})

			check := CheckMultiRepoTypes(parent)
			if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
				t.Fatalf("CheckMultiRepoTypes changed the additional repo: before %v, after %v", before, after)
			}
			want := strings.Join([]string{
				"  " + repo + ": " + types,
				"    database not read, so only config.yaml types are listed: " + legacyworkspace.HistoricalSQLiteRefusal,
			}, "\n")
			if check.Detail != want {
				t.Fatalf("CheckMultiRepoTypes detail:\n%s\nwant:\n%s", check.Detail, want)
			}
		})
	}
}

// TestReadTypesFromDBAsksAdmissionBeforeTheOpen pins that readTypesFromDB
// consults AdmitAdditionalRepo before it opens an additional repository's
// database, and that discoverChildTypes reports the legacy refusal it returns
// beside the repository's config.yaml types. bd sets the hook to its own
// foreign-workspace admission, which classifies the legacy Dolt layouts.
func TestReadTypesFromDBAsksAdmissionBeforeTheOpen(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	// If the hook were skipped, the open must fail fast.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	legacyworkspace.Layout{Files: map[string]string{
		configfile.ConfigFileName:         `{"backend":"dolt","dolt_mode":"server","dolt_database":"herald"}`,
		"config.yaml":                     "types:\n  custom: [legacy-node]\n",
		"dolt/herald/.dolt/noms/manifest": "legacy manifest",
	}}.Write(t, beadsDir)
	refusal := &configfile.LegacyWorkspaceError{Reason: "legacy Dolt server workspace"}
	var asked string
	previous := AdmitAdditionalRepo
	AdmitAdditionalRepo = func(dir string) error {
		asked = dir
		return refusal
	}
	t.Cleanup(func() { AdmitAdditionalRepo = previous })

	if _, err := readTypesFromDB(beadsDir); !errors.Is(err, refusal) {
		t.Fatalf("readTypesFromDB = %v, want the admission refusal", err)
	}
	if asked != beadsDir {
		t.Fatalf("AdmitAdditionalRepo asked about %q, want %q", asked, beadsDir)
	}
	if got, refused := discoverChildTypes(repo); !reflect.DeepEqual(got, []string{"legacy-node"}) || !errors.Is(refused, refusal) {
		t.Fatalf("discoverChildTypes = %v, %v; want the config.yaml types and the refusal", got, refused)
	}
}
