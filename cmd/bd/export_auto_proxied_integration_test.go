//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestProxiedServerAutoExport pins GH#8490: export.auto must work on the
// proxied write path. Before the fix the proxied arm of PersistentPostRunE
// never ran auto-export (and maybeAutoExport bailed on its store==nil guard),
// so .beads/issues.jsonl silently stopped tracking the store while writes
// landed in Dolt fine.
//
// Every subtest owns a fresh proxied project: auto-export state (the JSONL,
// export-state.json and the throttle timestamp) is per-workspace and must not
// leak between scenarios.
func TestProxiedServerAutoExport(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	t.Run("create_exports", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "paxc")
		paxEnableAutoExport(t, bd, p, "1ms")

		id := bdProxiedCreateSilent(t, bd, p.dir, "Auto-export seed", "--type", "task", "--priority", "2")

		data, err := os.ReadFile(paxJSONL(p))
		if err != nil {
			t.Fatalf("issues.jsonl after bd create: %v", err)
		}
		if rec := exportIssueRecordByID(exportLines(t, string(data)), id); rec == nil {
			t.Fatalf("issues.jsonl does not carry the created issue %q:\n%s", id, data)
		}
		state := paxReadExportState(t, p)
		if state.LastDoltCommit == "" {
			t.Fatalf("export-state.json has an empty last_dolt_commit: %+v", state)
		}
		if state.Issues != 1 {
			t.Errorf("export-state.json issues = %d, want 1", state.Issues)
		}
	})

	// AC-2: with a stored issue not yet in the JSONL, every skip shape must
	// leave both files alone — and the trailing canary write proves the skip
	// assertions were meaningful: the same workspace CAN export.
	t.Run("read_only_and_preview_skip", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedRootProject(t, bd, "paxr")
		bdProxiedCreateSilent(t, bd, p.dir, "Pre-enable issue", "--type", "task")
		// Enable via config.yaml, not `bd config set`: the enabling command's
		// own post-run would export, and this subtest needs the JSONL absent
		// until its canary write.
		paxEnableAutoExportQuiet(t, p, "1ms")

		jsonlAbsent := func() {
			t.Helper()
			if _, err := os.Stat(paxJSONL(p)); err == nil {
				data, _ := os.ReadFile(paxJSONL(p))
				t.Fatalf("issues.jsonl must not exist yet, got:\n%s", data)
			}
		}
		stateAbsent := func() {
			t.Helper()
			if _, err := os.Stat(paxState(p)); err == nil {
				t.Fatalf("export-state.json must not exist yet")
			}
		}

		// bd list — a read-only command.
		if out, err := bdProxiedRun(t, bd, p.dir, "list"); err != nil {
			t.Fatalf("bd list: %v\n%s", err, out)
		}
		jsonlAbsent()

		// A --dry-run preview.
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--dry-run", "Preview only", "--type", "task"); err != nil {
			t.Fatalf("bd create --dry-run: %v\n%s", err, out)
		}
		jsonlAbsent()

		// Strict --readonly: proxied mode refuses the open itself (it cannot
		// guarantee mutation-free access), so nothing can export; the
		// successful-readonly skip shape is pinned at unit level instead.
		if _, _, err := bdProxiedRunBuffers(t, bd, p.dir, "--readonly", "list"); err == nil {
			t.Fatalf("bd --readonly list unexpectedly succeeded on a proxied workspace")
		}
		jsonlAbsent()
		stateAbsent()

		// BD_GIT_HOOK=1 — a git hook's bd must not re-export.
		if _, _, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BD_GIT_HOOK=1"}, "create", "--silent", "From a hook", "--type", "task"); err != nil {
			t.Fatalf("bd create under BD_GIT_HOOK=1: %v", err)
		}
		jsonlAbsent()
		stateAbsent()

		// A --database override: the JSONL belongs to the workspace's
		// configured database, so an override command must not export it.
		// The override target is a second real beads database on the same
		// server, bootstrapped from its own directory with root credentials
		// (the TestProxiedServerSQLDatabaseFlag shape; this project connects
		// as root for the same reason): the create must SUCCEED — the
		// post-run arm runs — and still leave this workspace's JSONL
		// untouched.
		other := p.database + "_other"
		port := requireSharedProxiedServer(t)
		bdProxiedInit(t, bd, "paxs", "--database", other,
			"--proxied-server-external-host", "127.0.0.1",
			"--proxied-server-external-port", strconv.Itoa(port))
		if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, nil, "--database", other, "create", "--silent", "Other database", "--type", "task"); err != nil {
			t.Fatalf("bd --database %s create: %v\n%s", other, err, stderr)
		}
		jsonlAbsent()
		stateAbsent()

		// export.auto=false.
		paxWriteExportConfig(t, p, false, "1ms")
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--silent", "While disabled", "--type", "task"); err != nil {
			t.Fatalf("bd create with export.auto=false: %v\n%s", err, out)
		}
		jsonlAbsent()
		stateAbsent()

		// Canary: the same workspace, export re-enabled, a write DOES export.
		paxWriteExportConfig(t, p, true, "1ms")
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--silent", "Canary write", "--type", "task"); err != nil {
			t.Fatalf("canary bd create: %v\n%s", err, out)
		}
		if _, err := os.Stat(paxJSONL(p)); err != nil {
			t.Fatalf("canary write did not produce issues.jsonl: %v", err)
		}
	})

	// AC-3, throttle half: within export.interval nothing is rewritten even
	// though the hash moved.
	t.Run("throttle_window", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "paxt")
		paxEnableAutoExport(t, bd, p, "1h")

		bdProxiedCreateSilent(t, bd, p.dir, "Throttled one", "--type", "task")
		before, err := os.ReadFile(paxJSONL(p))
		if err != nil {
			t.Fatalf("first export missing: %v", err)
		}
		stateBefore := paxReadExportState(t, p)

		// The second write changes the hash but lands inside the window.
		bdProxiedCreateSilent(t, bd, p.dir, "Throttled two", "--type", "task")

		after, err := os.ReadFile(paxJSONL(p))
		if err != nil {
			t.Fatalf("issues.jsonl vanished inside the throttle window: %v", err)
		}
		if string(after) != string(before) {
			t.Fatalf("issues.jsonl rewritten inside export.interval:\n%s", firstStreamDiff(string(before), string(after)))
		}
		stateAfter := paxReadExportState(t, p)
		if stateAfter.LastDoltCommit != stateBefore.LastDoltCommit || stateAfter.Issues != stateBefore.Issues {
			t.Fatalf("export-state.json changed inside the window: before %+v after %+v", stateBefore, stateAfter)
		}
	})

	// AC-3, batch half: under dolt.auto-commit=batch HEAD does not move, so
	// only a working-set-aware state hash sees the update.
	t.Run("batch_working_set", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "paxb")
		paxConfigSet(t, bd, p, "set", "dolt.auto-commit", "batch")
		paxEnableAutoExport(t, bd, p, "1ms")

		id := bdProxiedCreateSilent(t, bd, p.dir, "Batch original title", "--type", "task")
		first := paxReadExportState(t, p)
		if first.LastDoltCommit == "" {
			t.Fatalf("no export ran for the batch create: %+v", first)
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "update", id, "--title", "Batch updated title"); err != nil {
			t.Fatalf("bd update under batch: %v\n%s", err, out)
		}

		data, err := os.ReadFile(paxJSONL(p))
		if err != nil {
			t.Fatalf("issues.jsonl after batch update: %v", err)
		}
		if !strings.Contains(string(data), "Batch updated title") {
			t.Fatalf("issues.jsonl still carries the old title under dolt.auto-commit=batch (HEAD did not move and change detection went quiet):\n%s", data)
		}
		second := paxReadExportState(t, p)
		if second.LastDoltCommit == first.LastDoltCommit {
			t.Fatalf("state hash did not move under batch (was %q)", first.LastDoltCommit)
		}
	})

	// AC-4: a proxied delete reconciles the JSONL — the removed line goes,
	// the note says so, and the JSONL-only refusal never fires — under both
	// auto-commit modes.
	t.Run("delete_reconciled", func(t *testing.T) {
		// dolt.auto-commit=batch exercises the P1 in-process delete handoff
		// (bd-todbq7 AC-4): under batch the delete never reaches committed
		// history in this run, so only the handoff can prove the id —
		// without it the orphan guard refuses instead of reconciling.
		for _, mode := range []string{"on", "batch"} {
			t.Run("auto_commit_"+mode, func(t *testing.T) {
				t.Parallel()
				p := newSharedProxiedProject(t, bd, "paxd")
				paxConfigSet(t, bd, p, "set", "dolt.auto-commit", mode)
				paxEnableAutoExport(t, bd, p, "1ms")

				a := bdProxiedCreateSilent(t, bd, p.dir, "Delete me", "--type", "task")
				bdProxiedCreateSilent(t, bd, p.dir, "Keep me", "--type", "task")

				_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "delete", a, "--force")
				if err != nil {
					t.Fatalf("bd delete %s: %v\n%s", a, err, stderr)
				}
				if !strings.Contains(stderr, "auto-export: reconciled 1 deleted issue(s)") {
					t.Fatalf("stderr lacks the reconcile note (GH#5896 wedge check):\n%s", stderr)
				}
				if strings.Contains(stderr, "refusing to overwrite") {
					t.Fatalf("proxied delete hit the JSONL-only refusal instead of reconciling:\n%s", stderr)
				}

				data, err := os.ReadFile(paxJSONL(p))
				if err != nil {
					t.Fatalf("issues.jsonl after delete: %v", err)
				}
				idsAfter := exportIssueIDs(exportLines(t, string(data)))
				if idsAfter[a] {
					t.Fatalf("deleted issue %s still in issues.jsonl:\n%s", a, data)
				}
				if !strings.Contains(string(data), "Keep me") {
					t.Fatalf("surviving issue lost from issues.jsonl:\n%s", data)
				}
			})
		}

		// The --cascade closure rides the same handoff: every id the delete
		// actually removed is proven, not just the requested one.
		t.Run("cascade", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "paxx")
			paxEnableAutoExport(t, bd, p, "1ms")

			parent := bdProxiedCreateSilent(t, bd, p.dir, "Cascade parent", "--type", "task")
			child := bdProxiedCreateSilent(t, bd, p.dir, "Cascade child", "--type", "task", "--parent", parent)
			bdProxiedCreateSilent(t, bd, p.dir, "Cascade bystander", "--type", "task")

			_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "delete", parent, "--cascade", "--force")
			if err != nil {
				t.Fatalf("bd delete --cascade: %v\n%s", err, stderr)
			}
			if !strings.Contains(stderr, "auto-export: reconciled 2 deleted issue(s)") {
				t.Fatalf("stderr lacks the cascade reconcile note:\n%s", stderr)
			}
			if strings.Contains(stderr, "refusing to overwrite") {
				t.Fatalf("cascade delete hit the JSONL-only refusal:\n%s", stderr)
			}
			data, err := os.ReadFile(paxJSONL(p))
			if err != nil {
				t.Fatalf("issues.jsonl after cascade delete: %v", err)
			}
			idsAfter := exportIssueIDs(exportLines(t, string(data)))
			if idsAfter[parent] || idsAfter[child] {
				t.Fatalf("cascade-closed ids still in issues.jsonl:\n%s", data)
			}
			if !strings.Contains(string(data), "Cascade bystander") {
				t.Fatalf("bystander lost from issues.jsonl:\n%s", data)
			}
		})
	})
}

// paxConfigSet is bd config set for a proxied project.
func paxConfigSet(t *testing.T, bd string, p proxiedProject, args ...string) {
	t.Helper()
	bdProxiedConfig(t, bd, p.dir, args...)
}

// paxEnableAutoExport turns auto-export on with the given throttle interval.
func paxEnableAutoExport(t *testing.T, bd string, p proxiedProject, interval string) {
	t.Helper()
	paxConfigSet(t, bd, p, "set", "export.auto", "true")
	paxConfigSet(t, bd, p, "set", "export.interval", interval)
}

// paxEnableAutoExportQuiet is paxEnableAutoExport without running bd: the
// enabling command's own post-run would export, and some scenarios need the
// JSONL absent until a later, chosen command. Writes the same config.yaml
// keys `bd config set` would (export.* are YAML-only keys); init's template
// carries no active settings, so appending one block is a merge.
func paxEnableAutoExportQuiet(t *testing.T, p proxiedProject, interval string) {
	t.Helper()
	paxWriteExportConfig(t, p, true, interval)
}

// paxWriteExportConfig sets the export section in config.yaml. The section
// this file's helpers append is always at EOF and only ever holds auto and
// interval, so an existing one is rewritten wholesale rather than merged —
// the surrounding file stays the init template's commented-out defaults.
func paxWriteExportConfig(t *testing.T, p proxiedProject, auto bool, interval string) {
	t.Helper()
	path := filepath.Join(p.beadsDir, "config.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	on := "false"
	if auto {
		on = "true"
	}
	block := []byte(fmt.Sprintf("\nexport:\n  auto: %s\n  interval: %s\n", on, interval))
	trimmed := body
	if idx := bytes.Index(trimmed, []byte("\nexport:")); idx >= 0 {
		trimmed = trimmed[:idx:idx]
	}
	if err := os.WriteFile(path, append(trimmed, block...), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

func paxJSONL(p proxiedProject) string { return filepath.Join(p.beadsDir, "issues.jsonl") }
func paxState(p proxiedProject) string { return filepath.Join(p.beadsDir, "export-state.json") }

func paxReadExportState(t *testing.T, p proxiedProject) exportAutoState {
	t.Helper()
	data, err := os.ReadFile(paxState(p))
	if err != nil {
		t.Fatalf("read export-state.json: %v", err)
	}
	var state exportAutoState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse export-state.json %s: %v", data, err)
	}
	return state
}

func TestProxiedServerAutoExportRefusalGuards(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	// AC-5: the fail-safe guards keep their direct-mode shape over the
	// proxied read surface — a JSONL record this database cannot prove
	// deleted, or an export that would empty a non-empty JSONL, refuses with
	// the same warning, byte-for-byte unchanged file, and no state saved.
	t.Run("foreign_record_refused", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "paxf")
		paxEnableAutoExport(t, bd, p, "1ms")

		bdProxiedCreateSilent(t, bd, p.dir, "Foreign guard keeper", "--type", "task")
		bdProxiedCreateSilent(t, bd, p.dir, "Foreign guard second", "--type", "task")

		foreign := `{"_type": "issue", "id": "pax-foreign-1", "title": "Record this store never had", "status": "open", "issue_type": "task"}` + "\n"
		f, err := os.OpenFile(paxJSONL(p), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(foreign); err != nil {
			t.Fatal(err)
		}
		f.Close()

		before, _ := os.ReadFile(paxJSONL(p))
		stateBefore, _ := os.ReadFile(paxState(p))

		_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "create", "--silent", "Trigger write", "--type", "task")
		if err != nil {
			t.Fatalf("trigger create: %v\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "Warning: auto-export skipped:") || !strings.Contains(stderr, "refusing to overwrite") {
			t.Fatalf("stderr lacks the direct-mode refusal warning:\n%s", stderr)
		}

		after, _ := os.ReadFile(paxJSONL(p))
		if string(after) != string(before) {
			t.Fatalf("refused export still rewrote issues.jsonl:\n%s", firstStreamDiff(string(before), string(after)))
		}
		stateAfter, _ := os.ReadFile(paxState(p))
		if string(stateAfter) != string(stateBefore) {
			t.Fatalf("refused export still saved state:\n%s\n%s", stateBefore, stateAfter)
		}
	})

	t.Run("empty_overwrite_refused", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "paxe")
		paxEnableAutoExport(t, bd, p, "1ms")

		a := bdProxiedCreateSilent(t, bd, p.dir, "Will look infra", "--type", "task")

		// Flip the only issue to an infra type in place: the JSONL keeps its
		// record while the filtered export set becomes empty — the
		// empty-overwrite guard's exact wedge shape. The trigger write is a
		// comment, not a create: a new issue would re-populate the
		// in-scope set and defuse the guard.
		db := openProxiedDB(t, p)
		if _, err := db.Exec("UPDATE issues SET issue_type = 'message' WHERE id = ?", a); err != nil {
			t.Fatalf("flip issue_type: %v", err)
		}
		db.Close()

		before, _ := os.ReadFile(paxJSONL(p))
		stateBefore, _ := os.ReadFile(paxState(p))

		_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comment", a, "Trigger write")
		if err != nil {
			t.Fatalf("trigger comment: %v\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "Warning: auto-export skipped:") || !strings.Contains(stderr, "refusing to overwrite") {
			t.Fatalf("stderr lacks the empty-overwrite refusal:\n%s", stderr)
		}

		after, _ := os.ReadFile(paxJSONL(p))
		if string(after) != string(before) {
			t.Fatalf("refused export still rewrote issues.jsonl:\n%s", firstStreamDiff(string(before), string(after)))
		}
		stateAfter, _ := os.ReadFile(paxState(p))
		if string(stateAfter) != string(stateBefore) {
			t.Fatalf("refused export still saved state")
		}
	})
}

func TestProxiedServerAutoExportByteIdentical(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	// AC-6: the acceptance oracle — the same corpus auto-exported from a
	// proxied workspace and from a classic (embedded) workspace holding the
	// same data must be byte-identical, and neither may carry infra,
	// template, ephemeral, excluded-owner, wisp_plane or memory records.
	t.Run("cross_mode_byte_identical", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "paxm")
		fx := seedProxiedExportFixture(t, bd, p)

		// An excluded owner: config-excluded owners must not reach the
		// git-committed JSONL from auto-export either.
		paxConfigSet(t, bd, p, "set", "export.exclude_owners", "pax-owner")
		_, _, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_ACTOR=pax-owner"},
			"create", "--silent", "Owner-excluded issue", "--type", "task")
		if err != nil {
			t.Fatalf("create as excluded owner: %v", err)
		}

		paxEnableAutoExport(t, bd, p, "1ms")
		bdProxiedCreateSilent(t, bd, p.dir, "Trigger write", "--type", "task")

		proxiedBytes, err := os.ReadFile(paxJSONL(p))
		if err != nil {
			t.Fatalf("proxied auto-export missing: %v", err)
		}
		proxiedRecords := exportLines(t, string(proxiedBytes))
		ids := exportIssueIDs(proxiedRecords)
		if ids[fx.infraMsg] {
			t.Errorf("auto-export carried the infra bead %s", fx.infraMsg)
		}
		if ids[fx.wisp] {
			t.Errorf("auto-export carried the ephemeral wisp %s", fx.wisp)
		}
		if keys := exportMemoryKeys(proxiedRecords); len(keys) != 0 {
			t.Errorf("auto-export carried memories %v (GH#3650)", keys)
		}
		for _, rec := range proxiedRecords {
			if _, marked := rec["wisp_plane"]; marked {
				t.Errorf("auto-export record carries a wisp_plane marker: %v", rec)
			}
			if tpl, _ := rec["is_template"].(bool); tpl {
				t.Errorf("auto-export carried a template record: %v", rec)
			}
			if rec["_type"] == "issue" && rec["title"] == "Owner-excluded issue" {
				t.Errorf("auto-export carried the excluded owner's issue")
			}
		}

		// Classic side: import the proxied JSONL into a fresh embedded
		// workspace with auto-export on; the import's own post-run
		// re-exports it there.
		classicDir, _, _ := bdInit(t, bd, "--prefix", "cls")
		runClassic(t, bd, classicDir, "config", "set", "export.auto", "true")
		runClassic(t, bd, classicDir, "config", "set", "export.interval", "1ms")
		incoming := filepath.Join(classicDir, "incoming.jsonl")
		if err := os.WriteFile(incoming, proxiedBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		runClassic(t, bd, classicDir, "import", "-i", incoming)

		classicBytes, err := os.ReadFile(filepath.Join(classicDir, ".beads", "issues.jsonl"))
		if err != nil {
			t.Fatalf("classic auto-export missing after import: %v", err)
		}
		if string(proxiedBytes) != string(classicBytes) {
			t.Fatalf("proxied and classic auto-exports differ:\n%s",
				firstStreamDiff(string(proxiedBytes), string(classicBytes)))
		}
	})
}
