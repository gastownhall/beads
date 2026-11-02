//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

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

// TestBackupRestoreRefusesSourceWithoutBackup pins the sql-server half of
// GH#7098: RestoreDatabase must refuse a local source directory that holds no
// Dolt backup before it issues any restore SQL, because
// DOLT_BACKUP('restore', '--force') drops the target database on the server
// before it discovers the source is empty. Every case asserts the
// postcondition — the database still answers with its issue, configuration
// and current commit — so a regression demonstrates data loss, not merely an
// error-text mismatch. Each subtest gets its own isolated database: a restore
// that runs replaces it, and two subtests must not read each other's damage.
func TestBackupRestoreRefusesSourceWithoutBackup(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	const seedTitle = "issue the failed restore must leave in place"

	// newWorkspace returns a store on its own database holding one committed
	// issue, plus that issue's id and the database's current commit.
	newWorkspace := func(t *testing.T) (ctx context.Context, s *dolt.DoltStore, id, commit string) {
		t.Helper()
		ctx = context.Background()
		dbPath := filepath.Join(t.TempDir(), "dolt")
		s = newTestStoreIsolatedDB(t, dbPath, "bk")

		now := time.Now()
		issue := &types.Issue{
			ID:        "bk-1",
			Title:     seedTitle,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("create issue: %v", err)
		}
		if err := s.Commit(ctx, "seed issue"); err != nil && !strings.Contains(err.Error(), "nothing to commit") {
			t.Fatalf("commit seed issue: %v", err)
		}
		commit, err := s.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("read current commit: %v", err)
		}
		return ctx, s, issue.ID, commit
	}

	assertIntact := func(t *testing.T, ctx context.Context, s *dolt.DoltStore, id, commit string) {
		t.Helper()
		got, err := s.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("issue %s is missing after the refused restore (the database was replaced): %v", id, err)
		}
		if got.Title != seedTitle {
			t.Fatalf("issue %s came back as %q, want %q", id, got.Title, seedTitle)
		}
		prefix, err := s.GetConfig(ctx, "issue_prefix")
		if err != nil || prefix != "bk" {
			t.Errorf("issue_prefix after the refused restore = %q, %v; want %q", prefix, err, "bk")
		}
		after, err := s.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("read current commit after the refused restore (the database was replaced): %v", err)
		}
		if after != commit {
			t.Errorf("current commit moved across the refused restore: %s -> %s", commit, after)
		}
	}

	// emptySource returns a directory that exists but holds no backup.
	emptySource := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if _, err := os.Stat(filepath.Join(dir, "manifest")); !os.IsNotExist(err) {
			t.Fatalf("precondition: %s must hold no backup manifest, stat err = %v", dir, err)
		}
		return dir
	}

	t.Run("store_method", func(t *testing.T) {
		ctx, s, id, commit := newWorkspace(t)
		source := emptySource(t)

		err := s.RestoreDatabase(ctx, source, true)
		if !errors.Is(err, versioncontrolops.ErrNoBackup) {
			t.Fatalf("RestoreDatabase error = %v, want one wrapping ErrNoBackup", err)
		}
		if !strings.Contains(err.Error(), "no Dolt backup found") || !strings.Contains(err.Error(), source) {
			t.Errorf("RestoreDatabase error should name the source and the missing backup, got: %v", err)
		}
		assertIntact(t, ctx, s, id, commit)
	})

	// runBackupRestore is the path the CLI and SDK callers above the store go
	// through; the refusal must survive its wrapping.
	t.Run("run_backup_restore", func(t *testing.T) {
		ctx, s, id, commit := newWorkspace(t)
		source := emptySource(t)

		err := runBackupRestore(ctx, s, source, true)
		if !errors.Is(err, versioncontrolops.ErrNoBackup) {
			t.Fatalf("runBackupRestore error = %v, want one wrapping ErrNoBackup", err)
		}
		assertIntact(t, ctx, s, id, commit)
	})
}

// TestBackupRestoreValidSourceRoundTrip is the positive control for the
// no-backup refusal (GH#7098): a real backup — a directory BackupDatabase
// produced, manifest and all — must still restore through the same
// prerequisite, and the force restore must replace the live database with the
// backup's contents: the issues the backup holds come back, the issue created
// after it does not. It runs on its own isolated database; a restore that
// runs replaces it.
func TestBackupRestoreValidSourceRoundTrip(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dolt")
	s := newTestStoreIsolatedDB(t, dbPath, "bkv")

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
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("create issue %s: %v", id, err)
		}
	}
	commit := func(message string) {
		t.Helper()
		if err := s.Commit(ctx, message); err != nil && !strings.Contains(err.Error(), "nothing to commit") {
			t.Fatalf("commit %q: %v", message, err)
		}
	}

	seed("bkv-1", "issue the backup holds A")
	seed("bkv-2", "issue the backup holds B")
	commit("issues the backup holds")

	backupDir := filepath.Join(t.TempDir(), "dolt-backup")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := s.BackupDatabase(ctx, backupDir); err != nil {
		t.Fatalf("back up the database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "manifest")); err != nil {
		t.Fatalf("backup produced no manifest at %s: %v", backupDir, err)
	}

	seed("bkv-3", "issue created after the backup")
	commit("issue the backup lacks")

	if err := s.RestoreDatabase(ctx, backupDir, true); err != nil {
		t.Fatalf("force restore from a real backup: %v", err)
	}

	// Verify through a store opened after the replace: the first store's
	// pooled connections watched the database be dropped and recreated.
	after, err := dolt.NewFromConfig(ctx, filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("reopen the restored database: %v", err)
	}
	t.Cleanup(func() { _ = after.Close() })

	for _, id := range []string{"bkv-1", "bkv-2"} {
		got, err := after.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("issue %s missing after the restore: %v", id, err)
			continue
		}
		if !strings.HasPrefix(got.Title, "issue the backup holds") {
			t.Errorf("issue %s came back as %q", id, got.Title)
		}
	}
	if got, err := after.GetIssue(ctx, "bkv-3"); err == nil {
		t.Errorf("restore did not replace the database: %s was created after the backup and is still here (%q)", "bkv-3", got.Title)
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
