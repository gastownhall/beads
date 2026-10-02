package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

var errBackupRestoreReachedStorage = errors.New("backup restore reached storage")

type backupRestoreRecordingStore struct {
	storage.DoltStorage
	restoreErr    error
	restoreCalls  int
	restoreSource string
	backupAddURL  string
	backupAddErr  error
}

func (s *backupRestoreRecordingStore) BackupAdd(_ context.Context, _ string, url string) error {
	s.backupAddURL = url
	return s.backupAddErr
}
func (s *backupRestoreRecordingStore) BackupSync(context.Context, string) error     { return nil }
func (s *backupRestoreRecordingStore) BackupRemove(context.Context, string) error   { return nil }
func (s *backupRestoreRecordingStore) BackupDatabase(context.Context, string) error { return nil }
func (s *backupRestoreRecordingStore) RestoreDatabase(_ context.Context, source string, _ bool) error {
	s.restoreCalls++
	s.restoreSource = source
	return s.restoreErr
}
func (s *backupRestoreRecordingStore) Commit(context.Context, string) error { return nil }

var _ storage.BackupStore = (*backupRestoreRecordingStore)(nil)

func TestBackupRestoreCommandRoutesBackupURLToStorage(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	fake := &backupRestoreRecordingStore{restoreErr: errBackupRestoreReachedStorage}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	const source = "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true"
	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	if !errors.Is(err, errBackupRestoreReachedStorage) {
		t.Fatalf("backup restore error = %v, want storage error", err)
	}
	if fake.restoreCalls != 1 {
		t.Fatalf("RestoreDatabase calls = %d, want 1", fake.restoreCalls)
	}
	if fake.restoreSource != source {
		t.Fatalf("RestoreDatabase source = %q, want %q", fake.restoreSource, source)
	}
}

func TestBackupRestoreCommandKeepsDirectoryValidation(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	fake := &backupRestoreRecordingStore{restoreErr: errBackupRestoreReachedStorage}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	source := filepath.Join(t.TempDir(), "missing")
	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	want := fmt.Sprintf("backup directory not found: %s\nRun 'bd backup' first to create a backup", source)
	if err == nil || err.Error() != want {
		t.Fatalf("backup restore error = %q, want %q", err, want)
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0", fake.restoreCalls)
	}

	// IsBackupURL is case-sensitive by design, so an unrecognized scheme is not
	// exempted from the gate — it is stat'ed as a path and quoted back by this
	// same refusal. That makes this the one message a credentialed source can
	// still reach, so it redacts like every other echo of the source. A plain
	// directory path, above, has no "://" and comes back unchanged.
	const credentialed = "S3://AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG@bucket/beads"
	err = backupRestoreCmd.RunE(backupRestoreCmd, []string{credentialed})
	if err == nil {
		t.Fatalf("backup restore %q = nil, want the missing-directory refusal", credentialed)
	}
	if !strings.Contains(err.Error(), "backup directory not found") {
		t.Fatalf("backup restore %q = %q, want the 'backup directory not found' wording", credentialed, err)
	}
	for _, leaked := range []string{"AKIAEXAMPLE", "wJalrXUtnFEMI", "K7MDENG"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("backup restore %q = %q, which echoes %q", credentialed, err, leaked)
		}
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0", fake.restoreCalls)
	}
}

// TestBackupRestoreCommandReportsTheJSONSourceVerbatim pins the success-path
// echo. "source" is data: a caller compares it with the argument it passed, so
// it is the argument byte-for-byte and not a redacted copy. A credentialed
// remote makes the distinction observable: the success payload stays verbatim,
// while errors that quote the same source must use RedactBackupURL.
func TestBackupRestoreCommandReportsTheJSONSourceVerbatim(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	oldJSONOutput := jsonOutput
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
		jsonOutput = oldJSONOutput
	})

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
		t.Fatalf("create workspace marker: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	fake := &backupRestoreRecordingStore{}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false
	jsonOutput = true

	const source = "https://user:hunter2pass@doltremoteapi.dolthub.com/org/db"
	if redacted := versioncontrolops.RedactBackupURL(source); redacted == source {
		t.Fatalf("fixture %q survives RedactBackupURL, so it cannot tell a verbatim source from a redacted one", source)
	}

	out := captureStdout(t, func() error {
		return backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	})

	var payload struct {
		Restored bool   `json:"restored"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal --json payload %q: %v", out, err)
	}
	if !payload.Restored {
		t.Fatalf("--json payload %q does not report the restore", out)
	}
	if payload.Source != source {
		t.Errorf("--json source = %q, want the argument verbatim, %q", payload.Source, source)
	}
	if fake.restoreSource != source {
		t.Errorf("RestoreDatabase source = %q, want %q", fake.restoreSource, source)
	}
}

// TestBackupRestoreCommandProxiedAcceptsBackupURL is the proxiedServerMode=true
// case the other tests in this file do not cover, and it is the topology where
// the URL path was still broken: validateBackupRestoreDir is exempted for a
// backup URL before the route split, but runBackupRestoreProxied then built its
// URL with DirToFileURL, which hard-rejects any input containing "://". So
// `bd backup restore s3://…` failed on every proxied-server workspace with
// `"s3://bucket/beads" is a s3 URL, not a directory` — later than before, but
// just as unreachable — while the command's help text advertises the feature
// with no topology caveat.
//
// The discriminator is that refusal string: only DirToFileURL produces it.
// ResolveBackupSource passes a remote backup URL through un-stat'ed, so the
// route runs on to the live provider and dies there instead. That is also what
// keeps this test serverless — it stops at the first step that needs a running
// topology, which is two steps past the URL build.
func TestBackupRestoreCommandProxiedAcceptsBackupURL(t *testing.T) {
	_, fake := useProxiedRestoreWorkspace(t)

	const source = "s3://bucket/beads"
	var refusal error
	// HandleErrorRespectJSON renders the message and returns a bare exit code,
	// so the wording is on stderr, not in the error value.
	stderr := captureStderr(t, func() {
		refusal = backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	})
	if refusal == nil {
		t.Fatalf("backup restore %q on a proxied workspace = nil; this fixture has no live topology to restore into", source)
	}
	if strings.Contains(stderr, "not a directory") {
		t.Fatalf("backup restore %q said %q: that refusal can only come from DirToFileURL, "+
			"so the proxied route still rejects every backup URL", source, stderr)
	}
	if !strings.Contains(stderr, "provider") {
		t.Fatalf("backup restore %q said %q, want the live-provider failure that follows a resolved URL", source, stderr)
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0: the proxied route runs DOLT_BACKUP itself", fake.restoreCalls)
	}
}

// TestBackupRestoreCommandProxiedRefusesMissingFileURL covers the one backup
// URL the proxied route has to stat. The command exempts every recognized URL
// from validateBackupRestoreDir, and file:// is recognized, but it names a
// local directory: handed a missing one, DOLT_BACKUP creates it, opens it as an
// empty backup and, under --force, drops the live database before the restore
// fails. On this route ResolveBackupSource is the only check before the
// topology is taken down, so its refusal has to arrive before the live-provider
// step the s3:// case above dies at.
func TestBackupRestoreCommandProxiedRefusesMissingFileURL(t *testing.T) {
	workspace, fake := useProxiedRestoreWorkspace(t)

	source := "file://" + filepath.Join(workspace, "no-such-backup")
	var refusal error
	stderr := captureStderr(t, func() {
		refusal = backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	})
	if refusal == nil {
		t.Fatalf("backup restore %q on a proxied workspace = nil, want a refusal", source)
	}
	if !strings.Contains(stderr, "backup source does not exist") {
		t.Fatalf("backup restore %q said %q, want ResolveBackupSource's missing-source refusal", source, stderr)
	}
	if strings.Contains(stderr, "provider") {
		t.Fatalf("backup restore %q said %q: it got past the URL build to the live provider", source, stderr)
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0: the proxied route runs DOLT_BACKUP itself", fake.restoreCalls)
	}
}

// useProxiedRestoreWorkspace points the command at a proxied-server workspace
// with no live topology behind it, so a restore runs until the first step that
// needs a running server. It returns the workspace root and the recording store
// installed as the command's store.
func useProxiedRestoreWorkspace(t *testing.T) (string, *backupRestoreRecordingStore) {
	t.Helper()
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
		beads.ResetCaches()
		git.ResetCaches()
	})

	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg, err := json.Marshal(configfile.Config{
		Backend:  configfile.BackendDolt,
		DoltMode: configfile.DoltModeProxiedServer,
	})
	if err != nil {
		t.Fatalf("marshal workspace config: %v", err)
	}
	if err := os.WriteFile(configfile.ConfigPath(beadsDir), cfg, 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(workspace)
	beads.ResetCaches()
	git.ResetCaches()

	// No proxied-server sidecar, so the topology is managed-local and the
	// backup capability row is honored. Asserted rather than assumed: if this
	// drifts, the route refuses before the URL build and a test that expects
	// to get past it would pass for the wrong reason.
	if got := resolveProxiedTopology(beadsDir); got != ProxyTopologyManagedLocal {
		t.Fatalf("fixture topology = %q, want %q; the capability gate would refuse before the URL build",
			got, ProxyTopologyManagedLocal)
	}

	fake := &backupRestoreRecordingStore{}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = true
	return workspace, fake
}

func TestBackupRestoreCommandRegistersBackupURLAfterRestore(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	// The URL is persisted byte-for-byte on purpose, signed query parameters
	// and any userinfo included, and that is the one echo of the source this
	// change does NOT redact: dolt-backup.json is what `bd backup sync` reads,
	// so a redacted backup_url would name a destination that cannot be synced
	// to. The trade is that a credential handed to `bd backup restore` lands in
	// a file under .beads/, which the doctor gitignore template does not
	// exclude. That persistence predates this change — `bd backup init <url>`
	// has always written the same field through the same helper — and this only
	// adds the restore path as a second entry point to it, so the boundary
	// question (gitignore the file, or persist credential-free URLs and require
	// ambient credentials for sync) is deliberately left where it was rather
	// than settled here.
	//
	// A successful restore re-registers the source as the backup remote and
	// saves .beads/dolt-backup.json via beads.FindBeadsDir, so point BEADS_DIR
	// at a throwaway workspace (embeddeddolt/ is the marker FindBeadsDir
	// accepts) rather than whatever .beads is ambient.
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
		t.Fatalf("create workspace marker: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	fake := &backupRestoreRecordingStore{}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	const source = "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true"
	if err := backupRestoreCmd.RunE(backupRestoreCmd, []string{source}); err != nil {
		t.Fatalf("backup restore error = %v, want nil", err)
	}
	if fake.backupAddURL != source {
		t.Fatalf("BackupAdd url = %q, want %q", fake.backupAddURL, source)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-backup.json"))
	if err != nil {
		t.Fatalf("read saved backup config: %v", err)
	}
	var cfg doltBackupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal saved backup config: %v", err)
	}
	if cfg.BackupURL != source {
		t.Fatalf("saved backup_url = %q, want %q", cfg.BackupURL, source)
	}
}

func TestRegisterBackupRemoteRedactsBackupAddFailure(t *testing.T) {
	const source = "aws://AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG@bucket/db"
	fake := &backupRestoreRecordingStore{
		backupAddErr: fmt.Errorf("Dolt rejected %q", source),
	}

	stderr := captureStderr(t, func() {
		registerBackupRemote(context.Background(), fake, source)
	})
	assertBackupWarningRedacted(t, stderr)
}

func TestReconcileRestoredProxiedWorkspaceRegistersBackupURL(t *testing.T) {
	beadsDir := backupConfigTestDir(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const source = "s3://bucket/path?endpoint=https://minio.example&region=auto&path-style=true"
	mock.ExpectExec("CALL DOLT_BACKUP('rm', ?)").
		WithArgs(proxiedBackupTargetName).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CALL DOLT_BACKUP('add', ?, ?)").
		WithArgs(proxiedBackupTargetName, source).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := reconcileRestoredProxiedWorkspace(context.Background(), conn, beadsDir, source, false); err != nil {
		t.Fatalf("reconcileRestoredProxiedWorkspace() = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("proxied backup registration: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-backup.json"))
	if err != nil {
		t.Fatalf("read saved backup config: %v", err)
	}
	var cfg doltBackupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal saved backup config: %v", err)
	}
	if cfg.BackupURL != source {
		t.Fatalf("saved backup_url = %q, want %q", cfg.BackupURL, source)
	}
}

func TestReconcileRestoredProxiedWorkspaceRedactsBackupAddFailure(t *testing.T) {
	beadsDir := backupConfigTestDir(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Keep the credentialed URL parse-valid. B1 deliberately hides the entire
	// Dolt cause for parse-invalid credentialed URLs because net/url can echo a
	// credential fragment outside the URL; this case exercises the ordinary
	// redacted-error path and retains the safe backup location.
	const source = "aws://AKIAEXAMPLE:wJalrXUtnFEMIK7MDENG@bucket/db"
	mock.ExpectExec("CALL DOLT_BACKUP('rm', ?)").
		WithArgs(proxiedBackupTargetName).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CALL DOLT_BACKUP('add', ?, ?)").
		WithArgs(proxiedBackupTargetName, source).
		WillReturnError(fmt.Errorf("Dolt rejected %q", source))

	stderr := captureStderr(t, func() {
		if err := reconcileRestoredProxiedWorkspace(context.Background(), conn, beadsDir, source, false); err != nil {
			t.Fatalf("reconcileRestoredProxiedWorkspace() = %v", err)
		}
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("proxied backup registration: %v", err)
	}
	assertBackupWarningRedacted(t, stderr)
}

func backupConfigTestDir(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
		t.Fatalf("create workspace marker: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)
	return beadsDir
}

func assertBackupWarningRedacted(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "aws://bucket/db") {
		t.Errorf("warning %q does not retain the redacted backup location", stderr)
	}
	for _, leaked := range []string{"AKIAEXAMPLE", "wJalrXUtnFEMI", "K7MDENG"} {
		if strings.Contains(stderr, leaked) {
			t.Errorf("warning %q echoes %q", stderr, leaked)
		}
	}
}
