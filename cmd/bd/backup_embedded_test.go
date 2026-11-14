//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// bdBackup runs "bd backup" with extra args. Returns combined output.
func bdBackup(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"backup"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd backup %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdBackupFail runs "bd backup" expecting failure. Returns combined output.
func bdBackupFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"backup"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd backup %s should have failed, got: %s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedBackup(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt backup tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("status_no_backup", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bkstat0")

		out := bdBackup(t, bd, dir, "status")
		if !strings.Contains(out, "No backup has been performed") {
			t.Errorf("expected 'No backup has been performed', got: %s", out)
		}
	})

	t.Run("status_after_init_sync", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bkstat1")
		bdCreateSilent(t, bd, dir, "status test issue")

		backupDest := filepath.Join(t.TempDir(), "dolt-backup-status")
		backupURL := "file://" + backupDest
		bdBackup(t, bd, dir, "init", backupURL)
		bdBackup(t, bd, dir, "sync")

		out := bdBackup(t, bd, dir, "status")
		if out == "" {
			t.Error("expected non-empty status output")
		}
		if strings.Contains(out, "Database size: 0 B") {
			t.Errorf("embedded backup status reported an empty database: %s", out)
		}

		jsonOut := bdBackup(t, bd, dir, "status", "--json")
		var status struct {
			DatabaseSize struct {
				Bytes int64 `json:"bytes"`
			} `json:"database_size"`
		}
		if err := json.Unmarshal([]byte(jsonOut), &status); err != nil {
			t.Fatalf("parse backup status JSON: %v\noutput: %s", err, jsonOut)
		}
		if status.DatabaseSize.Bytes <= 0 {
			t.Errorf("database_size.bytes = %d, want > 0; output: %s", status.DatabaseSize.Bytes, jsonOut)
		}
	})

	t.Run("init_and_sync", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bkinit")
		bdCreateSilent(t, bd, dir, "init sync test issue")

		// Create a backup destination directory
		backupDest := filepath.Join(t.TempDir(), "dolt-backup")
		backupURL := "file://" + backupDest

		// Init backup destination
		out := bdBackup(t, bd, dir, "init", backupURL)
		if !strings.Contains(out, "Backup destination configured") {
			t.Errorf("expected 'Backup destination configured', got: %s", out)
		}

		// Sync to backup
		out = bdBackup(t, bd, dir, "sync")
		if !strings.Contains(out, "Backup synced") {
			t.Errorf("expected 'Backup synced', got: %s", out)
		}

		// Verify backup destination has data
		if _, err := os.Stat(backupDest); os.IsNotExist(err) {
			t.Error("backup destination directory should exist after sync")
		}
	})

	t.Run("sync_without_init", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bksync0")

		out := bdBackupFail(t, bd, dir, "sync")
		if !strings.Contains(out, "no backup") && !strings.Contains(out, "not found") &&
			!strings.Contains(out, "No backup") {
			t.Errorf("expected backup-not-configured error, got: %s", out)
		}
	})

	t.Run("restore_from_backup", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bksrc")
		bdCreateSilent(t, bd, dir, "restore test issue A")
		bdCreateSilent(t, bd, dir, "restore test issue B")

		// Create a Dolt backup via init + sync
		backupDest := filepath.Join(t.TempDir(), "dolt-backup-restore")
		if err := os.MkdirAll(backupDest, 0750); err != nil {
			t.Fatal(err)
		}
		backupURL := "file://" + backupDest
		bdBackup(t, bd, dir, "init", backupURL)
		bdBackup(t, bd, dir, "sync")

		// Restore into a fresh environment. Use a prefix that differs
		// from the source ("bksrc") so the restored database name
		// doesn't collide with the target's own database.
		dir2, _, _ := bdInit(t, bd, "--prefix", "bkdst")

		// The restore will create a database named "bkdst" from the
		// backup, but "bkdst" already exists from bd init. Use
		// bdBackupFail to confirm the expected "already exists" error.
		out := bdBackupFail(t, bd, dir2, "restore", backupDest)
		if !strings.Contains(out, "already exists") {
			t.Errorf("expected 'already exists' error, got: %s", out)
		}
	})
}

func TestEmbeddedBackupConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt backup tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bkconc")

	// Seed some data
	for i := 0; i < 5; i++ {
		bdCreateSilent(t, bd, dir, fmt.Sprintf("concurrent backup issue %d", i))
	}

	// Set up a backup destination for sync
	backupDest := filepath.Join(t.TempDir(), "dolt-backup-concurrent")
	backupURL := "file://" + backupDest
	bdBackup(t, bd, dir, "init", backupURL)

	const numWorkers = 5

	type result struct {
		worker int
		out    string
		err    error
	}

	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			cmd := exec.Command(bd, "backup", "sync")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			results[worker] = result{worker: worker, out: string(out), err: err}
		}(w)
	}
	wg.Wait()

	successes := 0
	for _, r := range results {
		if strings.Contains(r.out, "panic") {
			t.Errorf("worker %d panicked:\n%s", r.worker, r.out)
		}
		if r.err == nil {
			successes++
		} else if !strings.Contains(r.out, "one writer at a time") {
			t.Errorf("worker %d failed with unexpected error: %v\n%s", r.worker, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 successful backup, got %d", successes)
	}
	t.Logf("%d/%d backup workers succeeded", successes, numWorkers)

	if _, err := os.Stat(backupDest); os.IsNotExist(err) {
		t.Error("backup destination should exist after concurrent syncs")
	}
}

// TestEmbeddedBackupRestoreStrictReadonly pins the refusal of a strict
// --readonly restore. A restore replaces the store, so CheckReadonly must stop
// it with the standard message before the command takes the exclusive workspace
// gate. Embedded mode used to be stopped only by the storage layer
// ("embeddeddolt: store is read-only"), after the gate had already left its
// holder files in the workspace. The control proves the fixture really does
// restore when --readonly is not asked for, so "the store is unchanged" below
// cannot be vacuous.
func TestEmbeddedBackupRestoreStrictReadonly(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt backup tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// newWorkspace returns a workspace whose live store holds one issue its
	// backup does not (three against two), so a restore that really ran shows up
	// as a changed issue count. Each case gets its own: a restore that runs
	// replaces the store and would hide a leak from the next.
	newWorkspace := func(t *testing.T, prefix string) (dir, backupDir string) {
		t.Helper()
		dir, _, _ = bdInit(t, bd, "--prefix", prefix)
		bdCreateSilent(t, bd, dir, "issue in the backup A")
		bdCreateSilent(t, bd, dir, "issue in the backup B")

		backupDir = filepath.Join(t.TempDir(), "dolt-backup")
		if err := os.MkdirAll(backupDir, 0o750); err != nil {
			t.Fatal(err)
		}
		bdBackup(t, bd, dir, "init", "file://"+backupDir)
		bdBackup(t, bd, dir, "sync")

		bdCreateSilent(t, bd, dir, "issue created after the backup")
		return dir, backupDir
	}

	t.Run("control_restore_without_readonly", func(t *testing.T) {
		dir, backupDir := newWorkspace(t, "rrctl")
		// The restore preflight (bd-jwaljq) accepts only a source whose top
		// level holds Dolt's manifest; `bd backup sync` must have written one.
		if info, err := os.Stat(filepath.Join(backupDir, "manifest")); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("bd backup sync left no regular manifest in %s: %v", backupDir, err)
		}
		stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "backup", "restore", "--force", backupDir)
		if code != 0 {
			t.Fatalf("restore without --readonly exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Restore complete") {
			t.Errorf("stdout is missing %q:\n%s", "Restore complete", stdout)
		}
		var titles []string
		for _, issue := range bdListJSON(t, bd, dir) {
			titles = append(titles, issue.Title)
		}
		sort.Strings(titles)
		if want := []string{"issue in the backup A", "issue in the backup B"}; !slices.Equal(titles, want) {
			t.Errorf("issues after a real restore = %q, want the backup's %q: the live store also held a third", titles, want)
		}
	})

	// bd-jwaljq (GH#7098): a source that holds no Dolt backup is refused before
	// the restore runs, so the live store keeps exactly the issues it had. At
	// base a forced restore dropped the database first and failed afterwards.
	// The default-path case matters on its own: backupDir creates .beads/backup
	// when it is missing, so a bare `bd backup restore --force` used to hand Dolt
	// an empty directory.
	issueSet := func(t *testing.T, dir string) []string {
		t.Helper()
		var got []string
		for _, issue := range bdListJSON(t, bd, dir) {
			got = append(got, fmt.Sprintf("%s|%s|%s", issue.ID, issue.Title, issue.Status))
		}
		sort.Strings(got)
		return got
	}
	for _, tc := range []struct {
		name   string
		prefix string
		files  []string // created in an explicit source; nil means the default path
		force  bool
	}{
		{"refuses_empty_dir", "rrempty", []string{}, true},
		{"refuses_empty_dir_without_force", "rrnoforce", []string{}, false},
		{"refuses_default_dir", "rrdefault", nil, true},
		{"refuses_exports_only", "rrexport", []string{"issues.jsonl", "backup_state.json"}, true},
		{"refuses_restore_debris", "rrdebris", []string{"LOCK", "oldgen/"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := newWorkspace(t, tc.prefix)
			before := issueSet(t, dir)
			if len(before) != 3 {
				t.Fatalf("fixture holds %d issues, want 3: %v", len(before), before)
			}

			args := []string{"backup", "restore"}
			if tc.force {
				args = append(args, "--force")
			}
			source := filepath.Join(".beads", "backup")
			if tc.files != nil {
				source = t.TempDir()
				for _, f := range tc.files {
					path := filepath.Join(source, f)
					var err error
					if strings.HasSuffix(f, "/") {
						err = os.MkdirAll(path, 0o750)
					} else {
						err = os.WriteFile(path, []byte("x"), 0o600)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, source)
			}
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, args...)

			if code == 0 {
				t.Errorf("bd %s exited 0 from a source with no backup\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout, stderr)
			}
			for _, want := range []string{"holds no Dolt backup", source, "Restore was not attempted"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr is missing %q:\n%s", want, stderr)
				}
			}
			if strings.Contains(stdout, "Restore complete") {
				t.Errorf("stdout reports a restore:\n%s", stdout)
			}
			if after := issueSet(t, dir); !slices.Equal(after, before) {
				t.Errorf("issues after the refused restore = %v, want the live store's %v", after, before)
			}
		})
	}

	// A real backup at a path Dolt reads as another directory is refused too.
	// Dolt parses the source as a file:// URL, so a backup copied to "proj#2"
	// was restored from "proj": Dolt created that directory empty, dropped
	// the live database, and failed, as with an empty source. Windows paths
	// skip that parse, but '%' is unescaped there.
	t.Run("refuses_backup_at_a_path_dolt_misreads", func(t *testing.T) {
		dir, backupDir := newWorkspace(t, "rrmisread")
		before := issueSet(t, dir)
		if len(before) != 3 {
			t.Fatalf("fixture holds %d issues, want 3: %v", len(before), before)
		}
		name := "proj#2"
		if runtime.GOOS == "windows" {
			name = "proj%232"
		}
		parent := t.TempDir()
		source := filepath.Join(parent, name)
		if err := copyTree(backupDir, source); err != nil {
			t.Fatalf("copy the backup to %s: %v", source, err)
		}

		stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "backup", "restore", "--force", source)

		if code == 0 {
			t.Errorf("bd backup restore --force %s exited 0\nstdout:\n%s\nstderr:\n%s", source, stdout, stderr)
		}
		for _, want := range []string{"cannot be restored where it is", source, "Restore was not attempted"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr is missing %q:\n%s", want, stderr)
			}
		}
		if after := issueSet(t, dir); !slices.Equal(after, before) {
			t.Errorf("issues after the refused restore = %v, want the live store's %v", after, before)
		}
		if _, err := os.Stat(filepath.Join(parent, "proj")); !os.IsNotExist(err) {
			t.Errorf("the refused restore reached Dolt, which created %s: %v", filepath.Join(parent, "proj"), err)
		}
	})

	for _, tc := range []struct {
		name   string
		prefix string
		flags  []string
	}{
		{"readonly", "rrro", []string{"--readonly"}},
		{"readonly_sandbox", "rrrosb", []string{"--readonly", "--sandbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, backupDir := newWorkspace(t, tc.prefix)
			before := snapshotWorkspace(t, dir)

			args := append(append([]string{}, tc.flags...), "backup", "restore", "--force", backupDir)
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, args...)

			assertStrictReadonlyRestoreRefused(t, stdout, stderr, code)
			assertWorkspaceUnchanged(t, dir, before)
			if got := len(bdListJSON(t, bd, dir)); got != 3 {
				t.Errorf("issue count after the refused restore = %d, want 3: the restore replaced the store", got)
			}
		})
	}
}
