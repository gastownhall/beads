//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProxiedServerAutoExport is GH#8490 end to end: export.auto=true on a
// proxied-server workspace keeps .beads/issues.jsonl in step with the store,
// through the same pipeline (guards, deletion proofs, writer, state file) a
// direct-mode workspace runs.
func TestProxiedServerAutoExport(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("create_exports", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxa")
		id := bdProxiedCreateSilent(t, bd, p.dir, "Auto exported issue")

		ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p)))
		if !ids[id] {
			t.Fatalf("issues.jsonl after a proxied `bd create` lacks %s; export.auto is a silent no-op (GH#8490)", id)
		}
		if state := readProxiedAutoExportState(t, p); state.LastDoltCommit == "" {
			t.Fatalf("export-state.json has an empty last_dolt_commit: %+v", state)
		}
	})

	// Commands that must not export: reads, previews, a git-hook invocation,
	// and export.auto=false. (Strict --readonly, bd serve, a migration freeze
	// and --database <other> are pinned by the unit tests: a proxied-server
	// workspace refuses strict --readonly before any command runs.)
	t.Run("read_only_and_preview_skip", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxs")
		id := bdProxiedCreateSilent(t, bd, p.dir, "Seed issue")
		jsonl, state := readProxiedAutoExportFiles(t, p)
		if !strings.Contains(jsonl, id) {
			t.Fatalf("seed create did not export %s:\n%s", id, jsonl)
		}

		for _, run := range []struct {
			what string
			env  []string
			args []string
		}{
			{what: "bd list", args: []string{"list"}},
			{what: "bd show", args: []string{"show", id}},
			{what: "bd create --dry-run", args: []string{"create", "--dry-run", "Preview only"}},
			{what: "bd create under BD_GIT_HOOK=1", env: []string{"BD_GIT_HOOK=1"}, args: []string{"create", "--silent", "Hook-time create"}},
		} {
			if stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, run.env, run.args...); err != nil {
				t.Fatalf("%s: %v\nstdout:\n%s\nstderr:\n%s", run.what, err, stdout, stderr)
			}
			requireProxiedAutoExportUnchanged(t, p, run.what, jsonl, state)
		}

		setProxiedAutoExportConfig(t, bd, p, "export.auto", "false")
		jsonl, state = readProxiedAutoExportFiles(t, p)
		bdProxiedCreateSilent(t, bd, p.dir, "Created with export.auto=false")
		requireProxiedAutoExportUnchanged(t, p, "bd create with export.auto=false", jsonl, state)
	})
}

// newProxiedAutoExportProject is a shared-server proxied project with
// export.auto on and a throttle short enough that every write exports.
func newProxiedAutoExportProject(t *testing.T, bd, prefix string, settings ...string) proxiedProject {
	t.Helper()
	p := newSharedProxiedProject(t, bd, prefix)
	settings = append([]string{"export.auto", "true", "export.interval", "1ms"}, settings...)
	for i := 0; i+1 < len(settings); i += 2 {
		setProxiedAutoExportConfig(t, bd, p, settings[i], settings[i+1])
	}
	return p
}

func setProxiedAutoExportConfig(t *testing.T, bd string, p proxiedProject, key, value string) {
	t.Helper()
	if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", key, value); err != nil {
		t.Fatalf("bd config set %s %s: %v\n%s", key, value, err, out)
	}
}

func proxiedAutoExportJSONLPath(p proxiedProject) string {
	return filepath.Join(p.beadsDir, "issues.jsonl")
}

// readProxiedAutoExportJSONL returns the auto-exported JSONL, failing when the
// file was never written.
func readProxiedAutoExportJSONL(t *testing.T, p proxiedProject) string {
	t.Helper()
	data, err := os.ReadFile(proxiedAutoExportJSONLPath(p))
	if err != nil {
		t.Fatalf("read auto-exported JSONL: %v", err)
	}
	return string(data)
}

// readProxiedAutoExportFiles returns the raw JSONL and export-state.json
// bytes ("" for an absent file), for unchanged-file assertions.
func readProxiedAutoExportFiles(t *testing.T, p proxiedProject) (jsonl, state string) {
	t.Helper()
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return ""
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}
	return read(proxiedAutoExportJSONLPath(p)), read(filepath.Join(p.beadsDir, exportAutoStateFile))
}

func readProxiedAutoExportState(t *testing.T, p proxiedProject) exportAutoState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.beadsDir, exportAutoStateFile))
	if err != nil {
		t.Fatalf("read export-state.json: %v", err)
	}
	var state exportAutoState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse export-state.json: %v\n%s", err, data)
	}
	return state
}

// requireProxiedAutoExportUnchanged fails when the JSONL or the export state
// moved from the snapshot taken before a command that must not export.
func requireProxiedAutoExportUnchanged(t *testing.T, p proxiedProject, what, wantJSONL, wantState string) {
	t.Helper()
	gotJSONL, gotState := readProxiedAutoExportFiles(t, p)
	if gotJSONL != wantJSONL {
		t.Errorf("%s rewrote issues.jsonl:\n%s", what, firstStreamDiff(wantJSONL, gotJSONL))
	}
	if gotState != wantState {
		t.Errorf("%s rewrote export-state.json:\nbefore: %s\nafter:  %s", what, wantState, gotState)
	}
}
