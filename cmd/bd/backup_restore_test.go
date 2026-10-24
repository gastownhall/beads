//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestValidateBackupRestoreDir pins GH#7098/GH#5972: an existing-but-empty
// directory (or any directory without a Dolt manifest) must be rejected
// before a restore ever reaches CALL DOLT_BACKUP('restore', ...), which
// drops the target database before discovering the source is invalid. Pure
// filesystem logic, no Dolt server needed.
func TestValidateBackupRestoreDir(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		err := validateBackupRestoreDir(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatal("expected error for nonexistent backup dir")
		}
	})

	t.Run("existing but empty directory is rejected, not silently accepted", func(t *testing.T) {
		dir := t.TempDir()
		err := validateBackupRestoreDir(dir)
		if err == nil {
			t.Fatal("expected error for a directory with no Dolt manifest")
		}
		if !strings.Contains(err.Error(), "does not look like a Dolt backup") {
			t.Fatalf("validateBackupRestoreDir error = %q, want manifest-missing wording", err)
		}
	})

	t.Run("path is a file, not a directory", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		err := validateBackupRestoreDir(file)
		if err == nil {
			t.Fatal("expected error for a file path")
		}
	})

	t.Run("directory with a manifest is accepted", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte("fake"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateBackupRestoreDir(dir); err != nil {
			t.Fatalf("validateBackupRestoreDir on a real-looking backup dir: %v", err)
		}
	})
}

func TestBackupRestoreMissingDir(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStoreWithPrefix(t, testDBPath, "dn")
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	err := runBackupRestore(ctx, s, "/nonexistent/path", false)
	if err == nil {
		t.Error("expected error for nonexistent backup dir")
	}
}

func TestSyncProjectIDFromDB_NoWorkspaceUsesActiveWorkspaceError(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)

	ctx := context.Background()
	repoDir := t.TempDir()
	testDBPath := filepath.Join(repoDir, ".beads", "beads.db")
	s := newTestStoreWithPrefix(t, testDBPath, "bp")
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetMetadata(ctx, "_project_id", "project-123"); err != nil {
		t.Fatalf("SetMetadata(_project_id): %v", err)
	}

	noWorkspaceDir := t.TempDir()
	t.Chdir(noWorkspaceDir)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")

	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	err := syncProjectIDFromDB(ctx, s)
	if err == nil {
		t.Fatal("expected syncProjectIDFromDB to fail without an active workspace")
	}
	if !strings.Contains(err.Error(), activeWorkspaceNotFoundError()) {
		t.Fatalf("syncProjectIDFromDB error = %q, want active workspace wording", err)
	}
	if !strings.Contains(err.Error(), diagHint()) {
		t.Fatalf("syncProjectIDFromDB error = %q, want diag hint", err)
	}
	if _, statErr := os.Stat(filepath.Join(noWorkspaceDir, ".beads")); !os.IsNotExist(statErr) {
		t.Fatalf("syncProjectIDFromDB should not create local .beads, stat err = %v", statErr)
	}
}

// strictReadonlyRestoreRefusal is the line CheckReadonly prints when a write
// command is run under strict --readonly.
const strictReadonlyRestoreRefusal = "Error: operation 'backup restore' is not allowed in read-only mode\n"

// assertStrictReadonlyRestoreRefused checks one refused `bd --readonly backup
// restore` run: the standard CheckReadonly refusal (text and exit code), nothing
// on stdout, and none of the storage-layer message that embedded mode gives when
// the command gets past CheckReadonly and opens the store.
func assertStrictReadonlyRestoreRefused(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, strictReadonlyRestoreRefusal) {
		t.Errorf("stderr is missing the standard refusal %q:\n%s", strictReadonlyRestoreRefusal, stderr)
	}
	if strings.Contains(stderr, "embeddeddolt: store is read-only") {
		t.Errorf("stderr carries the storage-layer refusal, so the command opened the store before being refused:\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when the restore is refused, got:\n%s", stdout)
	}
}

// workspaceSnapshot is what a refused command must leave alone: the entries of
// the project directory (the workspace gate file and its holder sidecar sit
// beside .beads, not in it) and everything below .beads, content included.
type workspaceSnapshot struct {
	projectEntries []string
	beads          readonlyTreeSnapshot
}

func snapshotWorkspace(t *testing.T, dir string) workspaceSnapshot {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return workspaceSnapshot{
		projectEntries: names,
		beads:          snapshotReadonlyTree(t, filepath.Join(dir, ".beads")),
	}
}

// assertWorkspaceUnchanged fails, naming each path, when the workspace under dir
// no longer matches a snapshot taken before a refused command.
func assertWorkspaceUnchanged(t *testing.T, dir string, before workspaceSnapshot) {
	t.Helper()
	after := snapshotWorkspace(t, dir)
	var diffs []string
	for _, name := range before.projectEntries {
		if !slices.Contains(after.projectEntries, name) {
			diffs = append(diffs, "removed: "+name)
		}
	}
	for _, name := range after.projectEntries {
		if !slices.Contains(before.projectEntries, name) {
			diffs = append(diffs, "created: "+name)
		}
	}
	for path, want := range before.beads.Entries {
		switch got, ok := after.beads.Entries[path]; {
		case !ok:
			diffs = append(diffs, "removed: .beads/"+path)
		case got != want:
			diffs = append(diffs, "changed: .beads/"+path)
		}
	}
	for path := range after.beads.Entries {
		if _, ok := before.beads.Entries[path]; !ok {
			diffs = append(diffs, "created: .beads/"+path)
		}
	}
	if len(diffs) == 0 {
		return
	}
	sort.Strings(diffs)
	const show = 20
	if len(diffs) > show {
		diffs = append(diffs[:show], fmt.Sprintf("... and %d more", len(diffs)-show))
	}
	t.Errorf("the refused restore changed the workspace:\n%s", strings.Join(diffs, "\n"))
}

// TestBackupRestoreStrictReadonlyServerMode is the server-mode counterpart of
// TestEmbeddedBackupRestoreStrictReadonly. Server mode is where the missing
// refusal mattered: no storage layer stops a server connection, so a
// `bd --readonly backup restore --force` ran to the end and replaced the store.
// It drives the built bd as a subprocess because CheckReadonly exits the
// process. Both the fixture's backup and the restore run inside the Dolt
// server, against the same path on the server's own filesystem, so a local
// and a containerized test server both serve.
func TestBackupRestoreStrictReadonlyServerMode(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	bd := buildBDForInitTests(t)

	// newWorkspace builds a server-mode workspace on the test server whose live
	// store holds one issue its backup does not (three against two), so a restore
	// that really ran shows up as a changed issue count. Each case gets its own: a
	// restore that runs replaces the store and would hide a leak from the next.
	newWorkspace := func(t *testing.T) (dir string, env []string, backupDir string) {
		t.Helper()
		dir = t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		database := uniqueTestDBName(t)
		if err := (&configfile.Config{
			Backend:        configfile.BackendDolt,
			DoltMode:       configfile.DoltModeServer,
			DoltServerHost: "127.0.0.1",
			DoltServerPort: testDoltServerPort,
			DoltDatabase:   database,
		}).Save(beadsDir); err != nil {
			t.Fatalf("save workspace metadata: %v", err)
		}

		ctx := context.Background()
		seedStore, err := dolt.New(ctx, &dolt.Config{
			Path:            filepath.Join(beadsDir, "dolt"),
			BeadsDir:        beadsDir,
			ServerHost:      "127.0.0.1",
			ServerPort:      testDoltServerPort,
			Database:        database,
			CreateIfMissing: true,
		})
		if err != nil {
			t.Fatalf("create the server-mode database: %v", err)
		}
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
		defer func() { _ = seedStore.Close() }()

		if err := seedStore.SetConfig(ctx, "issue_prefix", "rr"); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}
		seed := func(id, title string) {
			t.Helper()
			now := time.Now()
			issue := &types.Issue{
				ID:        id,
				Title:     title,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := seedStore.CreateIssue(ctx, issue, "test-user"); err != nil {
				t.Fatalf("create issue %s: %v", id, err)
			}
		}
		commit := func(message string) {
			t.Helper()
			if err := seedStore.Commit(ctx, message); err != nil && !strings.Contains(err.Error(), "nothing to commit") {
				t.Fatalf("commit %q: %v", message, err)
			}
		}

		seed("rr-1", "issue in the backup A")
		seed("rr-2", "issue in the backup B")
		commit("issues the backup holds")

		backupDir = filepath.Join(t.TempDir(), "dolt-backup")
		if err := os.MkdirAll(backupDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := seedStore.BackupDatabase(ctx, backupDir); err != nil {
			t.Fatalf("back up the database (the Dolt server must be able to write %s): %v", backupDir, err)
		}

		seed("rr-3", "issue created after the backup")
		commit("issue the backup lacks")

		env = []string{
			"BEADS_TEST_MODE=1",
			// The subprocess connects to a testdb_* database on the test server;
			// this is the opt-in the test-database firewall asks for.
			"BEADS_TEST_SERVER=1",
			"BEADS_DIR=" + beadsDir,
			"XDG_CONFIG_HOME=" + t.TempDir(),
			// Keep the subprocess's circuit-breaker state in the suite's own
			// directory rather than the host's temp dir.
			"BEADS_TEST_CIRCUIT_DIR=" + os.Getenv("BEADS_TEST_CIRCUIT_DIR"),
		}
		return dir, env, backupDir
	}

	issueCount := func(t *testing.T, dir string, env []string) int {
		t.Helper()
		stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, "list", "--json")
		if code != 0 {
			t.Fatalf("bd list --json exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in bd list --json output:\n%s", stdout)
		}
		var issues []json.RawMessage
		if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
			t.Fatalf("parse bd list --json output: %v\n%s", err, stdout)
		}
		return len(issues)
	}

	// The control proves the fixture really does restore when --readonly is not
	// asked for, so "the store is unchanged" below cannot be vacuous.
	t.Run("control_restore_without_readonly", func(t *testing.T) {
		dir, env, backupDir := newWorkspace(t)
		stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, "backup", "restore", "--force", backupDir)
		if code != 0 {
			t.Fatalf("restore without --readonly exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Restore complete") {
			t.Errorf("stdout is missing %q:\n%s", "Restore complete", stdout)
		}
		if got := issueCount(t, dir, env); got != 2 {
			t.Errorf("issue count after a real restore = %d, want 2: the backup holds two issues and the live store held three", got)
		}
	})

	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"readonly", []string{"--readonly"}},
		{"readonly_sandbox", []string{"--readonly", "--sandbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, env, backupDir := newWorkspace(t)
			before := snapshotWorkspace(t, dir)

			args := append(append([]string{}, tc.flags...), "backup", "restore", "--force", backupDir)
			stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, args...)

			assertStrictReadonlyRestoreRefused(t, stdout, stderr, code)
			assertWorkspaceUnchanged(t, dir, before)
			if got := issueCount(t, dir, env); got != 3 {
				t.Errorf("issue count after the refused restore = %d, want 3: the restore replaced the store", got)
			}
		})
	}
}
