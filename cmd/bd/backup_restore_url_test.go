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

// TestBackupRestoreCommandReportsTheJSONSourceRedacted pins the success-path
// echo. JSON output is routinely retained in scripts and CI logs, so remote
// URL credentials must be handled by the same canonical redactor as prose.
func TestBackupRestoreCommandReportsTheJSONSourceRedacted(t *testing.T) {
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

	const source = "s3://bucket/db?region=auto"
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
	want := versioncontrolops.RedactBackupURL(source)
	if payload.Source != want {
		t.Errorf("--json source = %q, want redacted source %q", payload.Source, want)
	}
	if fake.restoreSource != source {
		t.Errorf("RestoreDatabase source = %q, want %q", fake.restoreSource, source)
	}
}

// TestBackupRestoreCommandProxiedRefusesBackupURL pins the E-layer topology
// boundary. #5950 left the proxied resolver capable of carrying a URL, but the
// route executes it through the installed Dolt binary rather than the embedded
// engine upgraded here. The command therefore refuses before reaching either
// the resolver's directory guard or a live provider.
func TestBackupRestoreCommandProxiedRefusesBackupURL(t *testing.T) {
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
	if !strings.Contains(stderr, "not supported in proxied-server mode") {
		t.Fatalf("backup restore %q said %q, want the topology refusal", source, stderr)
	}
	if strings.Contains(stderr, "not a directory") || strings.Contains(stderr, "provider") {
		t.Fatalf("backup restore %q said %q, want refusal before resolution or provider startup", source, stderr)
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0: the proxied route runs DOLT_BACKUP itself", fake.restoreCalls)
	}
}

// TestBackupRestoreCommandProxiedRefusesFileURL keeps the command's local-only
// contract literal: proxied callers pass a directory, not a file:// spelling
// of one. The lower-level resolver still validates file URLs for storage paths
// that call it directly.
func TestBackupRestoreCommandProxiedRefusesFileURL(t *testing.T) {
	workspace, fake := useProxiedRestoreWorkspace(t)

	source := "file://" + filepath.Join(workspace, "no-such-backup")
	var refusal error
	stderr := captureStderr(t, func() {
		refusal = backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	})
	if refusal == nil {
		t.Fatalf("backup restore %q on a proxied workspace = nil, want a refusal", source)
	}
	if !strings.Contains(stderr, "not supported in proxied-server mode") {
		t.Fatalf("backup restore %q said %q, want the topology refusal", source, stderr)
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

func TestRegisterBackupRemotePreservesStorageRedactedBackupAddError(t *testing.T) {
	const storageErr = `add backup default: Dolt rejected "aws://bucket/db"; contact ops@example.com? retry #1`
	fake := &backupRestoreRecordingStore{
		backupAddErr: errors.New(storageErr),
	}

	stderr := captureStderr(t, func() {
		registerBackupRemote(context.Background(), fake, "aws://bucket/db")
	})
	if want := "Warning: failed to register backup remote: " + storageErr + "\n"; stderr != want {
		t.Errorf("warning = %q, want the storage-sanitized diagnostic unchanged, %q", stderr, want)
	}
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

// TestReconcileRestoredProxiedWorkspacePreservesStorageRedactedBackupAddError
// exercises the real versioncontrolops.BackupAdd wrapper and verifies that the
// command does not run its URL-only redactor over the already-sanitized error.
// The storage-level credential-redaction invariant itself is pinned by
// versioncontrolops.TestBackupAddScrubsEchoedURLAndPreservesCause.
func TestReconcileRestoredProxiedWorkspacePreservesStorageRedactedBackupAddError(t *testing.T) {
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
		WillReturnError(fmt.Errorf("Dolt rejected %q; contact ops@example.com? retry #1", source))

	stderr := captureStderr(t, func() {
		if err := reconcileRestoredProxiedWorkspace(context.Background(), conn, beadsDir, source, false); err != nil {
			t.Fatalf("reconcileRestoredProxiedWorkspace() = %v", err)
		}
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("proxied backup registration: %v", err)
	}
	assertBackupWarningRedacted(t, stderr)
	if !strings.Contains(stderr, "contact ops@example.com? retry #1") {
		t.Errorf("warning %q mangles safe prose from the storage-sanitized error", stderr)
	}
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

func writeDoltBackupConfig(t *testing.T, beadsDir, backupURL string) {
	t.Helper()
	data, err := json.Marshal(doltBackupConfig{BackupURL: backupURL, BackupName: defaultDoltBackupName})
	if err != nil {
		t.Fatalf("marshal backup config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-backup.json"), data, 0o600); err != nil {
		t.Fatalf("write backup config: %v", err)
	}
}

// TestBackupRestoreCommandRefusesURLInProxiedServerMode covers the branch every
// E-only test below otherwise pins OFF. #5950's lower route can resolve a URL,
// but proxied restore executes through the installed Dolt server rather than
// the embedded engine upgraded by this PR, so the command refuses before
// provider startup.
func TestBackupRestoreCommandRefusesURLInProxiedServerMode(t *testing.T) {
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
	proxiedServerMode = true

	// The signed query doubles as a leak check on the new message.
	const source = "s3://bucket/path?X-Amz-Signature=deadbeefsignature"
	const wantNamed = "s3://bucket/path"

	var err error
	stderr := captureStderr(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, []string{source})
	})
	if err == nil {
		t.Fatalf("backup restore error = nil, want a refusal")
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0", fake.restoreCalls)
	}
	if !strings.Contains(stderr, "not supported in proxied-server mode") {
		t.Fatalf("refusal %q does not explain the proxied-mode limit", stderr)
	}
	if !strings.Contains(stderr, wantNamed) {
		t.Fatalf("refusal %q does not name the source %q", stderr, wantNamed)
	}
	if strings.Contains(stderr, "not a directory") {
		t.Fatalf("refusal fell through to the DirToFileURL guard: %q", stderr)
	}
	if strings.Contains(stderr, "deadbeefsignature") {
		t.Fatalf("refusal echoed the signed query: %q", stderr)
	}
}

// TestBackupRestoreCommandRefusesNoArgWhenDestinationIsRemote pins the other
// half of the widened surface: after `bd backup init s3://…` a bare
// `bd backup restore` used to fall through to backupDir(), which CREATES the
// directory it returns, so the existence check passed on a fresh empty dir and
// the failure surfaced as an opaque Dolt error over a file:// source. Its twin,
// ...RestoresRecordedBackupWhenDestinationIsRemote, differs by one setup line.
func TestBackupRestoreCommandRefusesNoArgWhenDestinationIsRemote(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	beadsDir := backupConfigTestDir(t)
	// A "/" in the secret is the case the redaction helper used to miss.
	writeDoltBackupConfig(t, beadsDir, "aws://AKIAEXAMPLE:se/cret@bucket/db")

	fake := &backupRestoreRecordingStore{restoreErr: errBackupRestoreReachedStorage}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	var err error
	stderr := captureStderr(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, nil)
	})
	if err == nil {
		t.Fatalf("backup restore error = nil, want a refusal")
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0", fake.restoreCalls)
	}
	if !strings.Contains(stderr, "configured backup destination is a remote URL") {
		t.Fatalf("refusal %q does not explain the configured destination", stderr)
	}
	if !strings.Contains(stderr, "aws://bucket/db") {
		t.Fatalf("refusal %q does not name the destination", stderr)
	}
	if !strings.Contains(stderr, "no local backup is recorded") {
		t.Fatalf("refusal %q does not say why the backup directory was not used", stderr)
	}
	if strings.Contains(stderr, "se/cret") {
		t.Fatalf("refusal echoed the secret: %q", stderr)
	}
}

// TestBackupRestoreCommandNoArgInProxiedModeDoesNotSuggestURL covers the
// managed-local route where a URL restore is rejected. The no-argument
// refusal must not direct the operator to retry with an explicit URL that the
// same command will refuse one branch later.
func TestBackupRestoreCommandNoArgInProxiedModeDoesNotSuggestURL(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	beadsDir := backupConfigTestDir(t)
	writeDoltBackupConfig(t, beadsDir, "s3://bucket/db")

	fake := &backupRestoreRecordingStore{restoreErr: errBackupRestoreReachedStorage}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = true

	var err error
	stderr := captureStderr(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, nil)
	})
	if err == nil {
		t.Fatalf("backup restore error = nil, want a refusal")
	}
	if fake.restoreCalls != 0 {
		t.Fatalf("RestoreDatabase calls = %d, want 0", fake.restoreCalls)
	}
	if !strings.Contains(stderr, "Pass a directory that holds a Dolt backup") {
		t.Fatalf("refusal %q does not give the supported local-directory remedy", stderr)
	}
	if strings.Contains(stderr, "Pass the backup URL explicitly") {
		t.Fatalf("refusal %q suggests a URL that proxied mode does not support", stderr)
	}
}

// TestBackupRestoreCommandNoArgRestoresRecordedBackupWhenDestinationIsRemote is
// the twin of the refusal above, one setup line apart. Auto-backup writes to
// the backup directory whatever destination `bd backup init` recorded, and the
// remote schemes were persisted verbatim long before s3:// was accepted — so a
// recorded local backup must still restore with no argument, as it always did.
func TestBackupRestoreCommandNoArgRestoresRecordedBackupWhenDestinationIsRemote(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	beadsDir := backupConfigTestDir(t)
	writeDoltBackupConfig(t, beadsDir, "aws://AKIAEXAMPLE:se/cret@bucket/db")
	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	if err := saveBackupState(dir, &backupState{LastDoltCommit: "0123456789abcdef"}); err != nil {
		t.Fatalf("record a local backup: %v", err)
	}

	fake := &backupRestoreRecordingStore{}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	stderr := captureStderr(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, nil)
	})
	if err != nil {
		t.Fatalf("backup restore error = %v, want nil (stderr %q)", err, stderr)
	}
	if fake.restoreCalls != 1 {
		t.Fatalf("RestoreDatabase calls = %d, want 1", fake.restoreCalls)
	}
	if fake.restoreSource != dir {
		t.Fatalf("RestoreDatabase source = %q, want the backup directory %q", fake.restoreSource, dir)
	}
	if !strings.Contains(stderr, "aws://bucket/db") {
		t.Fatalf("note %q does not name the destination it did not use", stderr)
	}
	if strings.Contains(stderr, "se/cret") {
		t.Fatalf("note echoed the secret: %q", stderr)
	}
}

// TestBackupRestoreCommandJSONReportRedactsRemoteSource pins the success-path
// echo: every error redacts the source, and the --json report is the output
// scripts and CI logs keep. Only remote URLs are rewritten; #5949's canonical
// helper keeps file URL paths containing "@" intact, and local directory data
// remains byte-identical too.
func TestBackupRestoreCommandJSONReportRedactsRemoteSource(t *testing.T) {
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
	t.Setenv("BD_JSON_ENVELOPE", "0")

	localDir := filepath.Join(t.TempDir(), "beads@2024-01")
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatalf("create backup directory: %v", err)
	}

	for _, tc := range []struct {
		name, source, want, secret string
	}{
		{"signed s3 query", "s3://bucket/db?X-Amz-Signature=deadbeefsignature", "s3://bucket/db", "deadbeefsignature"},
		{"aws userinfo", "aws://AKIAEXAMPLE:se/cret@bucket/db", "aws://bucket/db", "se/cret"},
		{"file url with at sign", "file:///var/backups/beads@2024-01/db", "file:///var/backups/beads@2024-01/db", ""},
		{"directory with at sign", localDir, localDir, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupConfigTestDir(t)
			fake := &backupRestoreRecordingStore{}
			store = fake
			rootCtx = context.Background()
			proxiedServerMode = false
			jsonOutput = true

			out := captureStdout(t, func() error {
				return backupRestoreCmd.RunE(backupRestoreCmd, []string{tc.source})
			})
			if fake.restoreSource != tc.source {
				t.Fatalf("RestoreDatabase source = %q, want the unredacted %q", fake.restoreSource, tc.source)
			}
			var report struct {
				Restored bool   `json:"restored"`
				Source   string `json:"source"`
			}
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("parse success report %q: %v", out, err)
			}
			if !report.Restored || report.Source != tc.want {
				t.Fatalf("success report = %+v, want restored with source %q", report, tc.want)
			}
			if tc.secret != "" && strings.Contains(out, tc.secret) {
				t.Fatalf("success report echoed the secret: %q", out)
			}
		})
	}
}

// TestBackupRestoreCommandNoArgKeepsBackupDirForFileDestination is the control
// for the guard above. resolveDoltBackupURL writes file://<abs> for every local
// `bd backup init`, and those satisfy IsBackupURL too — so the guard is scoped
// to REMOTE schemes and the long-standing directory default is untouched.
func TestBackupRestoreCommandNoArgKeepsBackupDirForFileDestination(t *testing.T) {
	oldStore := store
	oldRootCtx := rootCtx
	oldProxiedServerMode := proxiedServerMode
	t.Cleanup(func() {
		store = oldStore
		rootCtx = oldRootCtx
		proxiedServerMode = oldProxiedServerMode
	})

	beadsDir := backupConfigTestDir(t)
	writeDoltBackupConfig(t, beadsDir, "file:///somewhere/else")

	fake := &backupRestoreRecordingStore{}
	store = fake
	rootCtx = context.Background()
	proxiedServerMode = false

	want, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	if err := backupRestoreCmd.RunE(backupRestoreCmd, nil); err != nil {
		t.Fatalf("backup restore error = %v, want nil", err)
	}
	if fake.restoreCalls != 1 {
		t.Fatalf("RestoreDatabase calls = %d, want 1", fake.restoreCalls)
	}
	if fake.restoreSource != want {
		t.Fatalf("RestoreDatabase source = %q, want the backup directory %q", fake.restoreSource, want)
	}
}
