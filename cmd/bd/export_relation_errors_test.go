package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// failingExportRelationStore is a classic DoltStorage stand-in that serves
// one in-scope issue (bd-export1, labelled "keep") and fails exactly the bulk
// relation loader named by fail with that loader's own sentinel (GH#6870).
type failingExportRelationStore struct {
	storage.DoltStorage
	fail string
}

// exportRelationLoaders lists the five classic bulk relation loaders, the
// sentinel each one fails with, and the wrap prefix every export path must
// put in front of it (the same prefixes uowExportSource uses).
var exportRelationLoaders = []struct {
	name       string
	sentinel   error
	wantPrefix string
}{
	{"labels", errors.New("injected labels failure"), "load labels"},
	{"dependency records", errors.New("injected dependency records failure"), "load dependency records"},
	{"comments", errors.New("injected comments failure"), "load comments"},
	{"comment counts", errors.New("injected comment counts failure"), "load comment counts"},
	{"dependency counts", errors.New("injected dependency counts failure"), "load dependency counts"},
}

func (s *failingExportRelationStore) relationErr(name string) error {
	if s.fail != name {
		return nil
	}
	for _, l := range exportRelationLoaders {
		if l.name == name {
			return l.sentinel
		}
	}
	panic("unknown relation loader " + name)
}

func (s *failingExportRelationStore) GetInfraTypes(context.Context) map[string]bool { return nil }

func (s *failingExportRelationStore) GetConfig(context.Context, string) (string, error) {
	return "", nil
}

func (s *failingExportRelationStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return []*types.Issue{{ID: "bd-export1", Title: "export me", Status: types.StatusOpen, IssueType: types.TypeTask}}, nil
}

func (s *failingExportRelationStore) GetLabelsForIssues(context.Context, []string) (map[string][]string, error) {
	if err := s.relationErr("labels"); err != nil {
		return nil, err
	}
	return map[string][]string{"bd-export1": {"keep"}}, nil
}

func (s *failingExportRelationStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	if err := s.relationErr("dependency records"); err != nil {
		return nil, err
	}
	return map[string][]*types.Dependency{}, nil
}

func (s *failingExportRelationStore) GetCommentsForIssues(context.Context, []string) (map[string][]*types.Comment, error) {
	if err := s.relationErr("comments"); err != nil {
		return nil, err
	}
	return map[string][]*types.Comment{}, nil
}

func (s *failingExportRelationStore) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	if err := s.relationErr("comment counts"); err != nil {
		return nil, err
	}
	return map[string]int{}, nil
}

func (s *failingExportRelationStore) GetDependencyCounts(context.Context, []string) (map[string]*types.DependencyCounts, error) {
	if err := s.relationErr("dependency counts"); err != nil {
		return nil, err
	}
	return map[string]*types.DependencyCounts{}, nil
}

// exportRelationSeed is a previous export of the stub's one issue, with its
// label: an in-scope record every auto-export guard lets through, so a
// failing write gets as far as the relation loaders.
const exportRelationSeed = `{"id":"bd-export1","title":"export me","status":"open","issue_type":"task","labels":["keep"]}` + "\n"

// assertFileUnchanged checks that path still holds exactly want.
func assertFileUnchanged(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s changed: got %q (err %v), want %q", path, got, err, want)
	}
}

// assertRelationLoaderErr checks that err wraps the failing loader's sentinel
// behind its uow-aligned prefix, not merely that it mentions the loader.
func assertRelationLoaderErr(t *testing.T, path string, err, sentinel error, wantPrefix string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("%s: err = %v, want errors.Is %v", path, err, sentinel)
	}
	if !strings.HasPrefix(err.Error(), wantPrefix+": ") {
		t.Fatalf("%s: err = %q, want prefix %q", path, err.Error(), wantPrefix+": ")
	}
}

// TestClassicExportPathsRejectRelationLoaderFailures pins GH#6870 on every
// classic export path: a failing bulk relation loader must fail the export
// instead of publishing records with that relation silently empty.
func TestClassicExportPathsRejectRelationLoaderFailures(t *testing.T) {
	for _, loader := range exportRelationLoaders {
		t.Run(loader.name, func(t *testing.T) {
			saveAndRestoreGlobals(t)
			store = &failingExportRelationStore{fail: loader.name}
			issues := []*types.Issue{{ID: "bd-export1", Title: "export me"}}

			_, err := storeExportSource{}.LoadExportRelations(t.Context(), issues)
			assertRelationLoaderErr(t, "LoadExportRelations", err, loader.sentinel, loader.wantPrefix)

			// Incremental auto-export encoder: no records, so the patch path
			// never reaches rewriteExportFile.
			records, err := encodeIssueRecords(t.Context(), []*types.Issue{{ID: "bd-export1", Title: "export me"}})
			assertRelationLoaderErr(t, "encodeIssueRecords", err, loader.sentinel, loader.wantPrefix)
			if records != nil {
				t.Fatalf("encodeIssueRecords records = %v, want nil", records)
			}

			// Full auto-export writer over a previous export: the atomic
			// temp file is aborted, so the old JSONL survives byte for byte
			// and nothing else is left beside it.
			dir := t.TempDir()
			out := filepath.Join(dir, "issues.jsonl")
			if err := os.WriteFile(out, []byte(exportRelationSeed), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err = exportToFile(t.Context(), out, false)
			assertRelationLoaderErr(t, "exportToFile", err, loader.sentinel, loader.wantPrefix)
			assertFileUnchanged(t, out, exportRelationSeed)
			if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) != 1 {
				t.Fatalf("export dir entries = %v (err %v), want only issues.jsonl", entries, readErr)
			}

			// ...and with no previous export, none is published.
			absent := filepath.Join(t.TempDir(), "issues.jsonl")
			_, _, err = exportToFile(t.Context(), absent, false)
			assertRelationLoaderErr(t, "exportToFile (absent)", err, loader.sentinel, loader.wantPrefix)
			if _, statErr := os.Stat(absent); !os.IsNotExist(statErr) {
				t.Fatalf("failed export published %q: stat err = %v", absent, statErr)
			}
		})
	}
}

// useClassicExportStore installs s as the classic store for runExport and
// pins the globals that would route it to the proxied or JSON paths.
func useClassicExportStore(t *testing.T, s storage.DoltStorage) {
	t.Helper()
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	oldCtx, oldProxied, oldJSON := rootCtx, proxiedServerMode, jsonOutput
	t.Cleanup(func() {
		rootCtx, proxiedServerMode, jsonOutput = oldCtx, oldProxied, oldJSON
		resetCommandContext()
	})
	rootCtx, proxiedServerMode, jsonOutput = t.Context(), false, false
	exportAll, exportIncludeInfra, exportScrub = false, false, false
	exportNoMemories, exportIncludeMemories = false, false
	store = s
	if usesProxiedServer() || storeIsRemoteBackend(store) {
		t.Fatal("precondition: runExport must take the classic storeExportSource path")
	}
}

// TestRunExportFailsWhenRelationLoaderFails pins GH#6870 end to end on
// `bd export`: a failing relation loader exits non-zero before any record is
// written, prints no success summary, and leaves the -o target untouched.
func TestRunExportFailsWhenRelationLoaderFails(t *testing.T) {
	for _, loader := range exportRelationLoaders {
		t.Run(loader.name, func(t *testing.T) {
			useClassicExportStore(t, &failingExportRelationStore{fail: loader.name})
			exportOutput = filepath.Join(t.TempDir(), "issues.jsonl")
			if err := os.WriteFile(exportOutput, []byte(exportRelationSeed), 0o600); err != nil {
				t.Fatal(err)
			}

			var err error
			stderr := captureStderr(t, func() { err = runExport(exportCmd, nil) })
			if err == nil {
				t.Fatalf("runExport err = nil, want failure; stderr:\n%s", stderr)
			}
			if want := "failed to load relational data: " + loader.wantPrefix + ": "; !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
			}
			if strings.Contains(stderr, "Exported") {
				t.Fatalf("stderr = %q, want no export summary", stderr)
			}
			assertFileUnchanged(t, exportOutput, exportRelationSeed)
		})
	}

	t.Run("absent output stays absent", func(t *testing.T) {
		useClassicExportStore(t, &failingExportRelationStore{fail: "labels"})
		exportOutput = filepath.Join(t.TempDir(), "issues.jsonl")

		var err error
		_ = captureStderr(t, func() { err = runExport(exportCmd, nil) })
		if err == nil {
			t.Fatal("runExport err = nil, want failure")
		}
		if _, statErr := os.Stat(exportOutput); !os.IsNotExist(statErr) {
			t.Fatalf("failed export published %q: stat err = %v", exportOutput, statErr)
		}
		entries, readErr := os.ReadDir(filepath.Dir(exportOutput))
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("output dir entries = %v (err %v), want none", entries, readErr)
		}
	})

	t.Run("stdout writes no records", func(t *testing.T) {
		useClassicExportStore(t, &failingExportRelationStore{fail: "labels"})
		exportOutput = ""

		var err error
		stdout := captureStdout(t, func() error { err = runExport(exportCmd, nil); return nil })
		if err == nil {
			t.Fatal("runExport err = nil, want failure")
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want no records", stdout)
		}
	})
}

// GetCurrentCommit doubles as the auto-export state hash (the stub is no
// storage.StateHasher) and the diff anchor.
func (s *failingExportRelationStore) GetCurrentCommit(context.Context) (string, error) {
	return "hash-after-write", nil
}

// failingDiffExportRelationStore adds storage.DiffStore so auto-export takes
// the incremental path (encodeIssueRecords) before its full fallback.
type failingDiffExportRelationStore struct {
	*failingExportRelationStore
}

func (s *failingDiffExportRelationStore) ChangedIssueIDs(context.Context, string, string) (storage.ChangedIssueIDs, error) {
	return storage.ChangedIssueIDs{Upserted: []string{"bd-export1"}}, nil
}

func (s *failingDiffExportRelationStore) GetIssuesByIDs(ctx context.Context, _ []string) ([]*types.Issue, error) {
	return s.SearchIssues(ctx, "", types.IssueFilter{})
}

// TestMaybeAutoExportKeepsJSONLWhenRelationLoaderFails pins GH#6870 on
// auto-export: a failing relation loader leaves the triggering command
// successful, warns, keeps the previous JSONL byte for byte, and does not
// advance export-state.json, so the next write command retries.
func TestMaybeAutoExportKeepsJSONLWhenRelationLoaderFails(t *testing.T) {
	cases := []struct {
		name       string
		store      func() storage.DoltStorage
		diffAnchor string
	}{
		{
			name:  "full",
			store: func() storage.DoltStorage { return &failingExportRelationStore{fail: "labels"} },
		},
		{
			name: "incremental then full",
			store: func() storage.DoltStorage {
				return &failingDiffExportRelationStore{&failingExportRelationStore{fail: "labels"}}
			},
			diffAnchor: "anchor-before-write",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			store = tc.store()

			dir := t.TempDir()
			beadsDir := filepath.Join(dir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads","backend":"dolt"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)
			t.Setenv("BEADS_DIR", beadsDir)
			jsonl := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(jsonl, []byte(exportRelationSeed), 0o600); err != nil {
				t.Fatal(err)
			}
			saveExportAutoState(beadsDir, &exportAutoState{LastDoltCommit: "hash-before-write", LastDiffAnchor: tc.diffAnchor})

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(t.Context(), false) })
			if err != nil {
				t.Fatalf("maybeAutoExport err = %v, want nil (the triggering command must still succeed)", err)
			}
			if n := strings.Count(stderr, "Warning: auto-export failed: load labels: "); n != 1 {
				t.Fatalf("stderr = %q, want exactly one auto-export failure warning naming load labels", stderr)
			}
			assertFileUnchanged(t, jsonl, exportRelationSeed)
			if got := loadExportAutoState(beadsDir); got.LastDoltCommit != "hash-before-write" || got.LastDiffAnchor != tc.diffAnchor {
				t.Fatalf("export state advanced to commit %q anchor %q, want %q / %q", got.LastDoltCommit, got.LastDiffAnchor, "hash-before-write", tc.diffAnchor)
			}
		})
	}
}
