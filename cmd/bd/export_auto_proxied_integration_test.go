//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		state := readProxiedAutoExportState(t, p)
		if state.LastDoltCommit == "" {
			t.Fatalf("export-state.json has an empty last_dolt_commit: %+v", state)
		}
		if state.Issues != 1 {
			t.Errorf("export-state.json issues = %d, want 1", state.Issues)
		}
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
