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
		// That database is another beads project, which the --database
		// identity guard refuses (#5572); BEADS_SKIP_IDENTITY_CHECK=1 is its
		// deliberate cross-project hatch, as in
		// TestProxiedServerDatabaseFlagForeignProjectIdentity.
		other := p.database + "_other"
		port := requireSharedProxiedServer(t)
		bdProxiedInit(t, bd, "paxs", "--database", other,
			"--proxied-server-external-host", "127.0.0.1",
			"--proxied-server-external-port", strconv.Itoa(port))
		if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "--database", other, "create", "--silent", "Other database", "--type", "task"); err != nil {
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

	// A delete reconciles its line out of the JSONL and says so, under
	// auto-commit on (history proves it) and batch (only the in-process
	// handoff from the delete route can): never the JSONL-only refusal.
	for _, mode := range []string{"on", "batch"} {
		t.Run("delete_reconciled_autocommit_"+mode, func(t *testing.T) {
			t.Parallel()
			p := newProxiedAutoExportProject(t, bd, "pxd", "dolt.auto-commit", mode)
			keep := bdProxiedCreateSilent(t, bd, p.dir, "Survivor")
			gone := bdProxiedCreateSilent(t, bd, p.dir, "Doomed")
			if ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p))); !ids[keep] || !ids[gone] {
				t.Fatalf("seed creates not exported: %v", ids)
			}

			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "delete", gone, "--force")
			if err != nil {
				t.Fatalf("bd delete: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if !strings.Contains(stderr, "auto-export: reconciled 1 deleted issue(s)") {
				t.Errorf("stderr lacks the reconciliation notice:\n%s", stderr)
			}
			if strings.Contains(stderr, "JSONL-only") {
				t.Errorf("delete hit the JSONL-only refusal:\n%s", stderr)
			}
			ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p)))
			if ids[gone] || !ids[keep] {
				t.Fatalf("after deleting %s, JSONL ids = %v; want only %s", gone, ids, keep)
			}
		})
	}

	// The --cascade closure rides the same handoff: every id the delete
	// actually removed is proven, not just the requested one. Under batch
	// nothing commits, so the handoff is the only proof for the child.
	t.Run("delete_reconciled_cascade", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxx", "dolt.auto-commit", "batch")
		parent := bdProxiedCreateSilent(t, bd, p.dir, "Cascade parent", "--type", "task")
		child := bdProxiedCreateSilent(t, bd, p.dir, "Cascade child", "--type", "task", "--parent", parent)
		bystander := bdProxiedCreateSilent(t, bd, p.dir, "Cascade bystander", "--type", "task")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "delete", parent, "--cascade", "--force")
		if err != nil {
			t.Fatalf("bd delete --cascade: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stderr, "auto-export: reconciled 2 deleted issue(s)") {
			t.Errorf("stderr lacks the cascade reconciliation notice:\n%s", stderr)
		}
		if strings.Contains(stderr, "refusing to overwrite") {
			t.Errorf("cascade delete hit the JSONL-only refusal:\n%s", stderr)
		}
		ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p)))
		if ids[parent] || ids[child] || !ids[bystander] {
			t.Fatalf("after cascade-deleting %s, JSONL ids = %v; want only %s", parent, ids, bystander)
		}
	})

	// `bd mol burn` deletes through its own route, not `bd delete`'s, and
	// must hand off the same proof: under batch the burned molecule never
	// reaches history, and without the handoff this export and every later
	// one would refuse.
	for _, mode := range []string{"on", "batch"} {
		t.Run("mol_burn_reconciled_autocommit_"+mode, func(t *testing.T) {
			t.Parallel()
			p := newProxiedAutoExportProject(t, bd, "pxm", "dolt.auto-commit", mode)
			keep := bdProxiedCreateSilent(t, bd, p.dir, "Survivor", "--type", "task")
			mol := bdProxiedCreateSilent(t, bd, p.dir, "Burned molecule", "--type", "epic")
			step := bdProxiedCreateSilent(t, bd, p.dir, "Burned step", "--type", "task", "--parent", mol)
			if ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p))); !ids[keep] || !ids[mol] || !ids[step] {
				t.Fatalf("seed creates not exported: %v", ids)
			}

			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "burn", mol, "--force")
			if err != nil {
				t.Fatalf("bd mol burn: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if !strings.Contains(stderr, "auto-export: reconciled 2 deleted issue(s)") {
				t.Errorf("stderr lacks the reconciliation notice:\n%s", stderr)
			}
			if strings.Contains(stderr, "refusing to overwrite") {
				t.Errorf("mol burn hit the JSONL-only refusal:\n%s", stderr)
			}
			ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p)))
			if ids[mol] || ids[step] || !ids[keep] {
				t.Fatalf("after burning %s, JSONL ids = %v; want only %s", mol, ids, keep)
			}
		})
	}

	// `bd mol wisp gc` deletes through a route of its own as well. Its sweep
	// takes an abandoned wisp's --no-history dependents, which auto-export
	// writes to the JSONL but which live in the dolt_ignore'd wisps table: no
	// history proves them gone in any auto-commit mode, the default `on`
	// included, so the route's handoff is the only proof.
	t.Run("mol_wisp_gc_reconciled", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxw")
		keep := bdProxiedCreateSilent(t, bd, p.dir, "Survivor")
		wisp := bdProxiedCreateSilent(t, bd, p.dir, "Abandoned wisp", "--ephemeral")
		child := bdProxiedCreateSilent(t, bd, p.dir, "No-history dependent", "--no-history", "--parent", wisp)
		if ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p))); !ids[keep] || !ids[child] || ids[wisp] {
			t.Fatalf("seed creates: JSONL ids = %v; want %s and %s, and not the ephemeral %s", ids, keep, child, wisp)
		}

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "wisp", "gc", "--age", "0s")
		if err != nil {
			t.Fatalf("bd mol wisp gc: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, "Deleted 2 issue(s)") {
			t.Fatalf("gc did not sweep the wisp and its no-history dependent:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "auto-export: reconciled 1 deleted issue(s)") {
			t.Errorf("stderr lacks the reconciliation notice:\n%s", stderr)
		}
		if strings.Contains(stderr, "refusing to overwrite") {
			t.Errorf("mol wisp gc hit the JSONL-only refusal:\n%s", stderr)
		}
		ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p)))
		if ids[child] || !ids[keep] {
			t.Fatalf("after gc swept %s, JSONL ids = %v; want only %s", child, ids, keep)
		}
	})

	// The overwrite guards run on proxied workspaces too: a JSONL record the
	// store cannot account for, or a store that would export 0 issues over a
	// non-empty JSONL, refuses with the direct-mode warning and leaves the
	// file and the export state alone. Only the remedy the warning names
	// differs: `bd import`, since `bd init --from-jsonl` does not run on a
	// proxied-server workspace.
	t.Run("foreign_record_refused", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxf")
		local := bdProxiedCreateSilent(t, bd, p.dir, "Local issue")
		path := proxiedAutoExportJSONLPath(p)
		foreign := `{"_type":"issue","id":"zz-foreign1","title":"From another clone"}` + "\n"
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(foreign); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		jsonl, state := readProxiedAutoExportFiles(t, p)

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "create", "--silent", "Another local issue")
		if err != nil {
			t.Fatalf("bd create: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stderr, "Warning: auto-export skipped:") || !strings.Contains(stderr, "refusing to overwrite") || !strings.Contains(stderr, "zz-foreign1") {
			t.Errorf("stderr lacks the JSONL-only refusal naming zz-foreign1:\n%s", stderr)
		}
		requireProxiedImportRemedy(t, stderr)
		requireProxiedAutoExportUnchanged(t, p, "a create over an unprovable JSONL record", jsonl, state)
		another := strings.TrimSpace(stdout)

		// The named remedy runs here and heals the refusal: the import brings
		// the foreign record into the store, so its own post-run export
		// writes every record.
		stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "import")
		if err != nil {
			t.Fatalf("bd import (the named remedy): %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if strings.Contains(stderr, "refusing to overwrite") {
			t.Errorf("the export after `bd import` still refused:\n%s", stderr)
		}
		ids := exportIssueIDs(exportLines(t, readProxiedAutoExportJSONL(t, p)))
		if !ids["zz-foreign1"] || !ids[local] || !ids[another] {
			t.Fatalf("after `bd import`, JSONL ids = %v; want zz-foreign1, %s and %s", ids, local, another)
		}
	})

	t.Run("empty_overwrite_refused", func(t *testing.T) {
		t.Parallel()
		p := newProxiedAutoExportProject(t, bd, "pxz")
		id := bdProxiedCreateSilent(t, bd, p.dir, "Will look infra", "--type", "task")

		// Flip the only issue to an infra type in place: the JSONL keeps its
		// record while the filtered export set becomes empty, the
		// empty-overwrite guard's exact wedge shape. The trigger write is a
		// comment, not a create: a new issue would re-populate the in-scope
		// set and defuse the guard.
		db := openProxiedDB(t, p)
		if _, err := db.Exec("UPDATE issues SET issue_type = 'message' WHERE id = ?", id); err != nil {
			t.Fatalf("flip issue_type: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close proxied db: %v", err)
		}
		jsonl, state := readProxiedAutoExportFiles(t, p)
		if !strings.Contains(jsonl, id) {
			t.Fatalf("seed create did not export %s:\n%s", id, jsonl)
		}

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "comment", id, "Trigger write")
		if err != nil {
			t.Fatalf("trigger comment: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stderr, "Warning: auto-export skipped: current database would export 0 issues") || !strings.Contains(stderr, "refusing to overwrite") {
			t.Errorf("stderr lacks the empty-overwrite refusal:\n%s", stderr)
		}
		requireProxiedImportRemedy(t, stderr)
		requireProxiedAutoExportUnchanged(t, p, "an empty-store export over a non-empty JSONL", jsonl, state)
	})
}

// requireProxiedImportRemedy fails unless a proxied overwrite refusal names
// `bd import`, the import that runs on a proxied-server workspace, rather
// than `bd init --from-jsonl`, which bd refuses there.
func requireProxiedImportRemedy(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "Run `bd import` to import the JSONL file") || strings.Contains(stderr, "bd init --from-jsonl") {
		t.Errorf("the proxied refusal does not name `bd import` as its remedy:\n%s", stderr)
	}
}

// TestProxiedServerAutoExportByteIdentical is the GH#8490 cross-mode oracle:
// the same corpus auto-exported from a proxied workspace and from a classic
// (embedded) workspace holding the same data must be byte-identical. It is its
// own entry point because it also builds a classic workspace.
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
		setProxiedAutoExportConfig(t, bd, p, "export.exclude_owners", "pax-owner")
		_, _, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_ACTOR=pax-owner"},
			"create", "--silent", "Owner-excluded issue", "--type", "task")
		if err != nil {
			t.Fatalf("create as excluded owner: %v", err)
		}

		setProxiedAutoExportConfig(t, bd, p, "export.auto", "true")
		setProxiedAutoExportConfig(t, bd, p, "export.interval", "1ms")
		bdProxiedCreateSilent(t, bd, p.dir, "Trigger write", "--type", "task")

		proxiedBytes, err := os.ReadFile(proxiedAutoExportJSONLPath(p))
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
		// The positive half. The classic side imports this very file, so a
		// record or relation the proxied reader dropped would be missing
		// from both exports and the bytes would still match: pin the
		// fixture's durable issues and relations here.
		for _, id := range []string{fx.critical, fx.epic, fx.child, fx.blocker, fx.done, fx.promoted, fx.promotedFriend} {
			if !ids[id] {
				t.Errorf("auto-export missing durable issue %s", id)
			}
		}
		requireExportFixtureRelations(t, proxiedRecords, fx)

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
