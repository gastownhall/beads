//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// exportCheckFixture points the global store at a fake holding ids and
// returns the workspace's .beads directory.
func exportCheckFixture(t *testing.T, autoExport bool, ids ...string) string {
	t.Helper()
	initConfigForTest(t)
	config.Set("export.auto", autoExport)
	saveAndRestoreGlobals(t)
	fake := &fakeHeadOnlyStore{}
	for _, id := range ids {
		fake.issues = append(fake.issues, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	}
	store = fake
	return filepath.Join(autoExportTestDir(t), ".beads")
}

func issueLines(ids ...string) []map[string]any {
	recs := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		recs = append(recs, map[string]any{"id": id, "title": id, "issue_type": "task"})
	}
	return recs
}

// TestCollectExportCoverageReadsStoreAndFile pins the store- and file-reading
// half of `bd doctor --check=export` (GH#6955): IDs are compared as sets, not
// counts; the file compared is the one auto-export writes, including a custom
// export.path; and a malformed file is an error, never a clean result.
func TestCollectExportCoverageReadsStoreAndFile(t *testing.T) {
	t.Run("equal counts still find the missing id", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1", "ex-2")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-1", "gone-1")...)

		result, err := collectExportCoverage(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != statusWarning || result.InScope != 2 || result.InFile != 2 ||
			result.Missing != 1 || !reflect.DeepEqual(result.MissingIDs, []string{"ex-2"}) {
			t.Fatalf("result = %+v, want a warning with ex-2 missing", result)
		}
		if !strings.Contains(result.Message, "bd export -o ") {
			t.Errorf("message %q does not carry the refresh action", result.Message)
		}
	})

	t.Run("custom export.path is the file compared", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")
		config.Set("export.path", "custom.jsonl")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-1")...)

		result, err := collectExportCoverage(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if result.Path != filepath.Join(beadsDir, "custom.jsonl") || result.Missing != 1 {
			t.Fatalf("result = %+v, want custom.jsonl compared and ex-1 missing", result)
		}
	})

	t.Run("malformed file is an error, not a clean result", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{not json\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := collectExportCoverage(context.Background(), beadsDir); err == nil {
			t.Fatal("collectExportCoverage accepted a malformed export")
		}
	})
}

// runDoctorExportCheckInProcess runs `bd doctor --check=export --json` with
// args through runInUserGlobalHome. It restores the maintenance opt-out the
// check sets, which a later test calling PersistentPostRunE directly would
// otherwise inherit.
func runDoctorExportCheckInProcess(t *testing.T, home, dir string, env map[string]string, args ...string) (exit int, stdout, stderr string) {
	t.Helper()
	original := commandSkipsPostRunMaintenance.Load()
	t.Cleanup(func() { commandSkipsPostRunMaintenance.Store(original) })
	return runInUserGlobalHome(t, home, dir, env, append([]string{"doctor", "--check=export", "--json"}, args...)...)
}

// seedExportCheckDatabase creates the embedded database beadsDir's metadata
// names, as bd init does: the check reads a database and refuses a
// workspace that holds none.
func seedExportCheckDatabase(t *testing.T, beadsDir string) {
	t.Helper()
	s, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("create the database in %s: %v", beadsDir, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close the database in %s: %v", beadsDir, err)
	}
}

// exportCheckScratch returns an empty directory outside any repository, with
// its symlinks resolved so paths compare equal to what discovery returns.
func exportCheckScratch(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestDoctorExportCheckTargetsTheSelectedWorkspace pins which workspace
// `bd doctor --check=export` inspects. Without a positional path it is the
// one bd selected, here through BEADS_DIR, including the two that
// rediscovery from the parent directory misses: the account home's .beads
// (GH#4402 passes over it) and a directory not named .beads. Each holds a
// database, as the check requires. A positional path naming another
// workspace is refused before any store opens.
func TestDoctorExportCheckTargetsTheSelectedWorkspace(t *testing.T) {
	wantInspected := func(t *testing.T, beadsDir string, exit int, stdout, stderr string) {
		t.Helper()
		if exit != 0 {
			t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
		}
		var result exportCoverageResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("stdout is not the check's JSON: %v\n%s", err, stdout)
		}
		if want := filepath.Join(beadsDir, "issues.jsonl"); result.Check != "export" || result.Path != want {
			t.Fatalf("result = %+v, want the export check of %s", result, want)
		}
	}

	t.Run("BEADS_DIR is the account home's .beads", func(t *testing.T) {
		f := newUserGlobalFixture(t)
		seedExportCheckDatabase(t, f.beadsDir)
		exit, stdout, stderr := runDoctorExportCheckInProcess(t, f.home, f.notes, map[string]string{"BEADS_DIR": f.beadsDir})
		wantInspected(t, f.beadsDir, exit, stdout, stderr)
	})

	t.Run("BEADS_DIR is not named .beads", func(t *testing.T) {
		root := exportCheckScratch(t)
		selected := filepath.Join(root, "selected-store")
		newFixtureWorkspace(t, selected)
		seedExportCheckDatabase(t, selected)
		exit, stdout, stderr := runDoctorExportCheckInProcess(t, filepath.Join(root, "home"), root, map[string]string{"BEADS_DIR": selected})
		wantInspected(t, selected, exit, stdout, stderr)
	})

	t.Run("a positional path naming another workspace is refused", func(t *testing.T) {
		root := exportCheckScratch(t)
		mine, other := filepath.Join(root, "mine"), filepath.Join(root, "other")
		newFixtureWorkspace(t, filepath.Join(mine, ".beads"))
		newFixtureWorkspace(t, filepath.Join(other, ".beads"))
		exit, stdout, stderr := runDoctorExportCheckInProcess(t, filepath.Join(root, "home"), mine, nil, other)
		if exit != 1 || !strings.Contains(stdout+stderr, "refusing to compare workspaces") {
			t.Fatalf("exit %d, want 1 and a refusal to compare workspaces\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
		}
		assertFixtureWorkspaceUntouched(t, filepath.Join(mine, ".beads"))
		assertFixtureWorkspaceUntouched(t, filepath.Join(other, ".beads"))
	})
}

// TestDoctorExportCheckRefusesAWorkspaceWithNoDatabase pins that the check
// never creates the database it reads. A fresh clone carries metadata.json
// and the committed export but no database (embeddeddolt/ is gitignored).
// The check used to open it writable, which created an empty database,
// reported its zero in-scope IDs all present, and left bd bootstrap taking
// the empty database for the real one. It now refuses, names bd bootstrap,
// and leaves the workspace without a database, whether embeddeddolt/ is
// absent or empty.
func TestDoctorExportCheckRefusesAWorkspaceWithNoDatabase(t *testing.T) {
	wantRefused := func(t *testing.T, exit int, stdout, stderr string) {
		t.Helper()
		if exit != 1 || stdout != "" ||
			!strings.Contains(stderr, "no embedded database at") || !strings.Contains(stderr, "Run bd bootstrap") {
			t.Fatalf("exit %d, want 1, no result, and a refusal naming bd bootstrap\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
		}
	}

	t.Run("fresh clone without embeddeddolt", func(t *testing.T) {
		root := exportCheckScratch(t)
		clone := filepath.Join(root, "clone")
		beadsDir := filepath.Join(clone, ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeMetadataConfig(t, beadsDir, configfile.DoltModeEmbedded, "fc")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("fc-1", "fc-2")...)
		// files maps each entry of .beads to its content ("" for a directory).
		files := func() map[string]string {
			entries, err := os.ReadDir(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[string]string, len(entries))
			for _, e := range entries {
				data, _ := os.ReadFile(filepath.Join(beadsDir, e.Name()))
				got[e.Name()] = string(data)
			}
			return got
		}
		before := files()

		exit, stdout, stderr := runDoctorExportCheckInProcess(t, filepath.Join(root, "home"), clone, nil)
		wantRefused(t, exit, stdout, stderr)
		if after := files(); !reflect.DeepEqual(after, before) {
			t.Errorf("the check changed %s: it must not create a database or touch the workspace\nbefore: %v\nafter:  %v", beadsDir, before, after)
		}
	})

	t.Run("empty embeddeddolt", func(t *testing.T) {
		root := exportCheckScratch(t)
		workspace := filepath.Join(root, "workspace")
		newFixtureWorkspace(t, filepath.Join(workspace, ".beads"))
		exit, stdout, stderr := runDoctorExportCheckInProcess(t, filepath.Join(root, "home"), workspace, nil)
		wantRefused(t, exit, stdout, stderr)
		assertFixtureWorkspaceUntouched(t, filepath.Join(workspace, ".beads"))
	})
}
