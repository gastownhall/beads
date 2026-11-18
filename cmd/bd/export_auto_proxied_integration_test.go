//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

	// Commands that must not export: reads, previews, a git-hook invocation,
	// and export.auto=false. (Strict --readonly, bd serve, a migration freeze
	// and --database <other> are pinned by the unit tests: a proxied-server
	// workspace refuses strict --readonly before any command runs.)
	// AC-2: with a stored issue not yet in the JSONL, every skip shape must
	// leave both files alone, and the trailing canary write proves the skip
	// assertions were meaningful: the same workspace CAN export.
	t.Run("read_only_and_preview_skip", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedRootProject(t, bd, "paxr")
		bdProxiedCreateSilent(t, bd, p.dir, "Pre-enable issue", "--type", "task")
		// Enable via config.yaml, not `bd config set`: the enabling command's
		// own post-run would export, and this subtest needs the JSONL absent
		// until its canary write.
		writeProxiedAutoExportConfigYAML(t, p, true, "1ms")
		requireNoExport := func(what string) {
			t.Helper()
			requireProxiedAutoExportUnchanged(t, p, what, "", "")
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "list"); err != nil {
			t.Fatalf("bd list: %v\n%s", err, out)
		}
		requireNoExport("bd list")

		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--dry-run", "Preview only", "--type", "task"); err != nil {
			t.Fatalf("bd create --dry-run: %v\n%s", err, out)
		}
		requireNoExport("bd create --dry-run")

		// Strict --readonly: proxied mode refuses the open itself (it cannot
		// guarantee mutation-free access), so nothing can export; the
		// successful-readonly skip shape is pinned at unit level instead.
		if _, _, err := bdProxiedRunBuffers(t, bd, p.dir, "--readonly", "list"); err == nil {
			t.Fatalf("bd --readonly list unexpectedly succeeded on a proxied workspace")
		}
		requireNoExport("bd --readonly list")

		// BD_GIT_HOOK=1: a git hook's bd must not re-export.
		if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BD_GIT_HOOK=1"}, "create", "--silent", "From a hook", "--type", "task"); err != nil {
			t.Fatalf("bd create under BD_GIT_HOOK=1: %v\n%s", err, stderr)
		}
		requireNoExport("bd create under BD_GIT_HOOK=1")

		// A --database override: the JSONL belongs to the workspace's
		// configured database, so an override command must not export it.
		// The override target is a second real beads database on the same
		// server, bootstrapped from its own directory with root credentials
		// (the TestProxiedServerSQLDatabaseFlag shape; this project connects
		// as root for the same reason): the create must SUCCEED, so the
		// post-run arm runs, and still leave this workspace's JSONL untouched.
		other := p.database + "_other"
		port := requireSharedProxiedServer(t)
		bdProxiedInit(t, bd, "paxs", "--database", other,
			"--proxied-server-external-host", "127.0.0.1",
			"--proxied-server-external-port", strconv.Itoa(port))
		if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, nil, "--database", other, "create", "--silent", "Other database", "--type", "task"); err != nil {
			t.Fatalf("bd --database %s create: %v\n%s", other, err, stderr)
		}
		requireNoExport("bd --database <other> create")

		writeProxiedAutoExportConfigYAML(t, p, false, "1ms")
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--silent", "While disabled", "--type", "task"); err != nil {
			t.Fatalf("bd create with export.auto=false: %v\n%s", err, out)
		}
		requireNoExport("bd create with export.auto=false")

		// Canary: the same workspace, export re-enabled, a write DOES export.
		writeProxiedAutoExportConfigYAML(t, p, true, "1ms")
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--silent", "Canary write", "--type", "task"); err != nil {
			t.Fatalf("canary bd create: %v\n%s", err, out)
		}
		if _, err := os.Stat(proxiedAutoExportJSONLPath(p)); err != nil {
			t.Fatalf("canary write did not produce issues.jsonl: %v", err)
		}
	})

	// Inside export.interval nothing is rewritten even though the hash moved.
	t.Run("throttle_window", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxt", "export.interval", "1h")
		bdProxiedCreateSilent(t, bd, p.dir, "Throttled one")
		jsonl, state := readProxiedAutoExportFiles(t, p)
		if jsonl == "" || state == "" {
			t.Fatalf("first proxied write did not export: jsonl=%q state=%q", jsonl, state)
		}
		// The second write changes the hash but lands inside the window.
		bdProxiedCreateSilent(t, bd, p.dir, "Throttled two")
		requireProxiedAutoExportUnchanged(t, p, "bd create inside export.interval", jsonl, state)
	})

	// Change detection follows the working set, not HEAD: under
	// dolt.auto-commit=batch nothing commits, yet an update past the
	// throttle still reaches the JSONL. Inside the interval it does not.
	t.Run("batch_working_set", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxb", "dolt.auto-commit", "batch")
		id := bdProxiedCreateSilent(t, bd, p.dir, "Original title")
		if rec := exportIssueRecordByID(exportLines(t, readProxiedAutoExportJSONL(t, p)), id); rec == nil {
			t.Fatalf("batch-mode create did not export %s", id)
		}
		first := readProxiedAutoExportState(t, p)

		setProxiedAutoExportConfig(t, bd, p, "export.interval", "1h")
		jsonl, state := readProxiedAutoExportFiles(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, id, "--title", "Throttled title")
		requireProxiedAutoExportUnchanged(t, p, "bd update inside export.interval", jsonl, state)

		setProxiedAutoExportConfig(t, bd, p, "export.interval", "1ms")
		bdProxiedUpdateOne(t, bd, p.dir, id, "--title", "Working-set title")
		rec := exportIssueRecordByID(exportLines(t, readProxiedAutoExportJSONL(t, p)), id)
		if rec == nil || rec["title"] != "Working-set title" {
			t.Fatalf("after an uncommitted (batch) update past the throttle, JSONL record = %v; want title %q", rec, "Working-set title")
		}
		if second := readProxiedAutoExportState(t, p); second.LastDoltCommit == first.LastDoltCommit {
			t.Fatalf("export-state last_dolt_commit did not move under dolt.auto-commit=batch (still %q)", first.LastDoltCommit)
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

// writeProxiedAutoExportConfigYAML sets the export section in config.yaml
// without running bd: the enabling command's own post-run would export, and
// some scenarios need the JSONL absent until a later, chosen command. It
// writes the keys `bd config set` would (export.* are YAML-only keys). The
// section this helper appends is always at EOF and only ever holds auto and
// interval, so an existing one is rewritten wholesale; the rest of the file
// stays the init template's commented-out defaults.
func writeProxiedAutoExportConfigYAML(t *testing.T, p proxiedProject, auto bool, interval string) {
	t.Helper()
	path := filepath.Join(p.beadsDir, "config.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	block := []byte(fmt.Sprintf("\nexport:\n  auto: %t\n  interval: %s\n", auto, interval))
	if idx := bytes.Index(body, []byte("\nexport:")); idx >= 0 {
		body = body[:idx:idx]
	}
	if err := os.WriteFile(path, append(body, block...), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
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
