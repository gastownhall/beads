//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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

// TestValidateBackupRestoreDir pins the restore preflight (bd-jwaljq, GH#7098):
// an existing directory with no Dolt backup, or a backup at a path Dolt would
// read as another directory, is refused before either restore path starts,
// with the source named and a way forward, while a missing path keeps its
// "not found" wording and a directory holding a manifest passes. The empty,
// regular-file, missing and manifest cases are @rjgeng's from PR #7192.
func TestValidateBackupRestoreDir(t *testing.T) {
	root := t.TempDir()
	mkdir := func(name string, files ...string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "issues.jsonl")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	debris := mkdir("debris", "LOCK")
	mkdir(filepath.Join("debris", "oldgen"))
	manifestDir := mkdir("manifest_dir")
	mkdir(filepath.Join("manifest_dir", "manifest"))
	brokenLink := mkdir("broken_link")
	link(filepath.Join(brokenLink, "absent"), filepath.Join(brokenLink, "manifest"))
	linked := mkdir("linked", "manifest.real")
	link(filepath.Join(linked, "manifest.real"), filepath.Join(linked, "manifest"))
	// A backup at a path Dolt reads as another directory: it cuts a file://
	// path at '#'. Windows paths skip that parse, but '%' is unescaped there.
	misread := "proj#2"
	if runtime.GOOS == "windows" {
		misread = "proj%232"
	}

	for _, tc := range []struct {
		name, dir string
		want      []string
	}{
		{name: "missing", dir: filepath.Join(root, "absent"), want: []string{"backup directory not found", "bd backup sync"}},
		{name: "regular_file", dir: file, want: []string{"is not a directory", "Restore was not attempted"}},
		{name: "empty_dir", dir: mkdir("empty"), want: []string{"holds no Dolt backup", "Restore was not attempted", "bd backup init"}},
		{name: "exports_only", dir: mkdir("exports", "issues.jsonl", "backup_state.json"), want: []string{"holds no Dolt backup"}},
		{name: "lock_and_oldgen_debris", dir: debris, want: []string{"holds no Dolt backup"}},
		{name: "manifest_is_a_directory", dir: manifestDir, want: []string{"holds no Dolt backup", "Restore was not attempted"}},
		{name: "broken_manifest_symlink", dir: brokenLink, want: []string{"holds no Dolt backup"}},
		{name: "backup_at_a_misread_path", dir: mkdir(misread, "manifest"), want: []string{"cannot be restored where it is", "move or rename the backup", "Restore was not attempted"}},
		{name: "backup", dir: mkdir("backup", "manifest")},
		{name: "manifest_symlink_to_regular_file", dir: linked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBackupRestoreDir(tc.dir, restoreSourceReadByBd)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("validateBackupRestoreDir(%q) = %v, want nil", tc.dir, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateBackupRestoreDir(%q) = nil, want a refusal", tc.dir)
			}
			for _, want := range append(tc.want, tc.dir) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("validateBackupRestoreDir(%q) error = %q, want it to contain %q", tc.dir, err, want)
				}
			}
		})
	}
}

// TestValidateBackupRestoreDirByReader pins what the preflight says for each
// reader of the source (PR #7192 post-merge review). A Dolt SQL server at a
// loopback host gets the same checks, and its refusals add that bd looked on
// this machine while the server opens the path on its own filesystem. A
// server at another host, whose filesystem bd cannot check, is refused
// whatever the directory holds here, a backup included.
func TestValidateBackupRestoreDirByReader(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	backup := filepath.Join(root, "backup")
	for _, dir := range []string{empty, backup} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(backup, "manifest"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The other-host refusal names the server; with the variable set, the
	// host comes from it rather than from any workspace configuration.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "db.example.com")

	serverNote := []string{"bd checked the path on this machine", "opens it on its own filesystem", "mounted at the same path in both"}
	otherHost := []string{"is read by the Dolt server at db.example.com", "which bd cannot check", "Restore was not attempted", "on the server's machine"}
	for _, tc := range []struct {
		name    string
		dir     string
		reader  restoreSourceReader
		want    []string // nil: the source passes
		notWant []string
	}{
		{name: "bd_passes_a_backup", dir: backup, reader: restoreSourceReadByBd},
		{name: "bd_refusal_has_no_server_note", dir: empty, reader: restoreSourceReadByBd,
			want: []string{"holds no Dolt backup", "Restore was not attempted"}, notWant: serverNote},
		{name: "local_server_passes_a_backup", dir: backup, reader: restoreSourceReadByLocalServer},
		{name: "local_server_refusal_says_which_side_was_checked", dir: empty, reader: restoreSourceReadByLocalServer,
			want: append([]string{"holds no Dolt backup", "Restore was not attempted"}, serverNote...)},
		{name: "local_server_missing_dir_says_which_side_was_checked", dir: filepath.Join(root, "absent"), reader: restoreSourceReadByLocalServer,
			want: append([]string{"backup directory not found"}, serverNote...)},
		// Nothing here was checked, so neither the local refusal nor its
		// remedy, which writes a backup bd could not check either, appears.
		{name: "other_host_refuses_a_backup", dir: backup, reader: restoreSourceReadByRemoteServer,
			want: otherHost, notWant: []string{`no regular "manifest" file`, "bd backup init"}},
		{name: "other_host_refuses_an_empty_dir", dir: empty, reader: restoreSourceReadByRemoteServer,
			want: otherHost, notWant: []string{`no regular "manifest" file`, "bd backup init"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBackupRestoreDir(tc.dir, tc.reader)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("validateBackupRestoreDir(%q, %d) = %v, want nil", tc.dir, tc.reader, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateBackupRestoreDir(%q, %d) = nil, want a refusal", tc.dir, tc.reader)
			}
			for _, want := range append(slices.Clone(tc.want), tc.dir) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("validateBackupRestoreDir(%q, %d) error = %q, want it to contain %q", tc.dir, tc.reader, err, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Errorf("validateBackupRestoreDir(%q, %d) error = %q, want it without %q", tc.dir, tc.reader, err, notWant)
				}
			}
		})
	}
}

// TestRestoreSourceReaderOf pins who bd takes to open a restore source: the
// engine in this process for an embedded store, whatever host is configured,
// and otherwise the Dolt SQL server, which shares this machine's filesystem
// only at a host configfile.IsLocalHostString accepts. The stores are zero
// values: the choice reads their type, never their state.
func TestRestoreSourceReaderOf(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store storage.DoltStorage
		host  string
		want  restoreSourceReader
	}{
		{"embedded_ignores_another_host", &embeddeddolt.EmbeddedDoltStore{}, "db.example.com", restoreSourceReadByBd},
		{"server_at_127.0.0.1", &dolt.DoltStore{}, "127.0.0.1", restoreSourceReadByLocalServer},
		{"server_at_localhost", &dolt.DoltStore{}, "localhost", restoreSourceReadByLocalServer},
		{"server_at_ipv6_loopback", &dolt.DoltStore{}, "::1", restoreSourceReadByLocalServer},
		{"server_at_another_host", &dolt.DoltStore{}, "db.example.com", restoreSourceReadByRemoteServer},
		{"server_at_another_loopback_address", &dolt.DoltStore{}, "127.0.0.2", restoreSourceReadByRemoteServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
			if got := restoreSourceReaderOf(tc.store); got != tc.want {
				t.Errorf("restoreSourceReaderOf(%T) with host %q = %d, want %d", tc.store, tc.host, got, tc.want)
			}
		})
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
// and a containerized test server both serve. bd checks the restore source on
// this machine first, so the control's outcome depends on which: see there.
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

	issueIDs := func(t *testing.T, dir string, env []string) []string {
		t.Helper()
		stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, "list", "--json")
		if code != 0 {
			t.Fatalf("bd list --json exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in bd list --json output:\n%s", stdout)
		}
		var issues []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
			t.Fatalf("parse bd list --json output: %v\n%s", err, stdout)
		}
		ids := make([]string, 0, len(issues))
		for _, issue := range issues {
			ids = append(ids, issue.ID)
		}
		sort.Strings(ids)
		return ids
	}
	issueCount := func(t *testing.T, dir string, env []string) int {
		t.Helper()
		return len(issueIDs(t, dir, env))
	}

	// The control proves the fixture really does restore when --readonly is not
	// asked for, so "the store is unchanged" below cannot be vacuous.
	t.Run("control_restore_without_readonly", func(t *testing.T) {
		dir, env, backupDir := newWorkspace(t)
		// bd backup restore checks its source on this machine before the server
		// reads it on its own filesystem (bd-jwaljq), so a real restore needs
		// the backup the server wrote to be visible here too. The local backend
		// shares this filesystem. A containerized server, which bd at
		// 127.0.0.1 cannot tell from a local one, wrote it inside its
		// container: there the restore is refused, the refusal has to say
		// that bd looked on this machine, and the store has to stay as it was.
		if _, err := os.Stat(filepath.Join(backupDir, "manifest")); err != nil {
			if os.Getenv(testutil.EnvDoltServerBackend) == "local" {
				t.Fatalf("the local test server's backup left no manifest visible at %s: %v", backupDir, err)
			}
			t.Logf("the test server's backup is not visible on this machine (%v): checking that the restore is refused, says why, and keeps the store", err)
			stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, "backup", "restore", "--force", backupDir)
			if code == 0 {
				t.Fatalf("restore --force from a backup only the server's filesystem holds exited 0\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			for _, want := range []string{"holds no Dolt backup", backupDir, "Restore was not attempted", "bd checked the path on this machine", "opens it on its own filesystem"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr is missing %q:\n%s", want, stderr)
				}
			}
			if got := issueCount(t, dir, env); got != 3 {
				t.Errorf("issue count after the refused restore = %d, want 3", got)
			}
			return
		}
		stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, "backup", "restore", "--force", backupDir)
		if code != 0 {
			t.Fatalf("restore without --readonly exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Restore complete") {
			t.Errorf("stdout is missing %q:\n%s", "Restore complete", stdout)
		}
		if got, want := issueIDs(t, dir, env), []string{"rr-1", "rr-2"}; !slices.Equal(got, want) {
			t.Errorf("issues after a real restore = %v, want the backup's %v: the live store also held rr-3", got, want)
		}
	})

	// bd-jwaljq (GH#7098): an existing directory that holds no Dolt backup must
	// be refused before the restore runs, leaving the live store intact.
	t.Run("force_restore_from_empty_dir_keeps_store", func(t *testing.T) {
		dir, env, _ := newWorkspace(t)
		emptyDir := t.TempDir()

		stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, "backup", "restore", "--force", emptyDir)
		if code == 0 {
			t.Errorf("restore --force from an empty directory exited 0\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		// The workspace reaches the server at 127.0.0.1, so bd takes the server
		// to share this machine's filesystem, checks the path here, and says so.
		for _, want := range []string{"holds no Dolt backup", emptyDir, "Restore was not attempted", "bd checked the path on this machine", "opens it on its own filesystem"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr is missing %q:\n%s", want, stderr)
			}
		}
		stdout, stderr, code = runBDMigrationFreezeWithEnv(t, bd, dir, env, "list", "--json")
		if code != 0 {
			t.Fatalf("the live store is gone after restore --force from an empty directory: bd list --json exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if got := issueCount(t, dir, env); got != 3 {
			t.Errorf("issue count after restore --force from an empty directory = %d, want 3", got)
		}
	})

	// A server at a host other than a loopback address opens the source on a
	// filesystem bd cannot check, so the restore is refused whatever the path
	// holds here: a real backup, the one the control restores, and with or
	// without --force. The workspace reaches the test server through
	// 127.0.0.2, an address configfile.IsLocalHostString does not count as
	// this machine's, so the bd list calls show the store is reachable and
	// unchanged.
	t.Run("server_at_another_host_refuses_restore_keeps_store", func(t *testing.T) {
		dir, env, backupDir := newWorkspace(t)
		port := forwardFromOtherLoopback(t, testDoltServerPort)
		beadsDir := filepath.Join(dir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
			t.Fatalf("load workspace metadata: %v", err)
		}
		cfg.DoltServerHost = "127.0.0.2"
		cfg.DoltServerPort = port
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save workspace metadata: %v", err)
		}
		if got := issueCount(t, dir, env); got != 3 {
			t.Fatalf("issue count through 127.0.0.2 = %d, want 3", got)
		}

		for _, flags := range [][]string{{"--force"}, nil} {
			args := append(append([]string{"backup", "restore"}, flags...), backupDir)
			stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, env, args...)
			if code == 0 {
				t.Errorf("bd %v against a server at another host exited 0\nstdout:\n%s\nstderr:\n%s", args, stdout, stderr)
			}
			for _, want := range []string{backupDir, "is read by the Dolt server at 127.0.0.2", "which bd cannot check", "Restore was not attempted", "on the server's machine"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("bd %v: stderr is missing %q:\n%s", args, want, stderr)
				}
			}
			if got := issueCount(t, dir, env); got != 3 {
				t.Errorf("issue count after bd %v = %d, want 3: the restore ran", args, got)
			}
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

// forwardFromOtherLoopback listens on 127.0.0.2 and forwards each connection
// to the test Dolt server on 127.0.0.1:port, returning the port it listens on.
// A workspace pointed at it reaches the same server at an address bd does not
// count as this machine's (configfile.IsLocalHostString), which stands in for
// a server on another host. Linux routes all of 127.0.0.0/8 to the loopback
// interface; where 127.0.0.2 is not configured (macOS) the test skips.
func forwardFromOtherLoopback(t *testing.T, port int) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot listen on 127.0.0.2 (%v): this case needs a second loopback address", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = client.Close() }()
				server, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
				go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}
