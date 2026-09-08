package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestEncryptDecryptWithKey(t *testing.T) {
	// Generate a random key
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	password := "super-secret-federation-password"

	encrypted, err := encryptWithKey(password, key)
	if err != nil {
		t.Fatalf("encryptWithKey failed: %v", err)
	}
	if len(encrypted) == 0 {
		t.Fatal("encrypted result is empty")
	}

	decrypted, err := decryptWithKey(encrypted, key)
	if err != nil {
		t.Fatalf("decryptWithKey failed: %v", err)
	}
	if decrypted != password {
		t.Errorf("decrypted = %q, want %q", decrypted, password)
	}
}

func TestApplyS3ChecksumEnvToCmd(t *testing.T) {
	t.Setenv(awsResponseChecksumValidationEnv, "when_supported")

	cmd := exec.Command("dolt", "push") // #nosec G204 -- test command is not executed
	(&remoteCredentials{username: "user", password: "pass"}).applyToCmd(cmd)
	applyS3ChecksumEnvToCmd(cmd)

	var gotChecksum, gotUser, gotPassword string
	for _, e := range cmd.Env {
		switch {
		case strings.HasPrefix(e, awsResponseChecksumValidationEnv+"="):
			gotChecksum = strings.TrimPrefix(e, awsResponseChecksumValidationEnv+"=")
		case strings.HasPrefix(e, "DOLT_REMOTE_USER="):
			gotUser = strings.TrimPrefix(e, "DOLT_REMOTE_USER=")
		case strings.HasPrefix(e, "DOLT_REMOTE_PASSWORD="):
			gotPassword = strings.TrimPrefix(e, "DOLT_REMOTE_PASSWORD=")
		}
	}

	if gotChecksum != "when_required" {
		t.Fatalf("%s = %q, want when_required", awsResponseChecksumValidationEnv, gotChecksum)
	}
	if gotUser != "user" || gotPassword != "pass" {
		t.Fatalf("credential env = user:%q password:%q", gotUser, gotPassword)
	}
}

func TestPrepareDoltCLITransferCommandAppliesCredentialsAndS3Env(t *testing.T) {
	t.Setenv(awsResponseChecksumValidationEnv, "when_supported")
	creds := &remoteCredentials{username: "user", password: "pass"}

	cmd, _, cancel := prepareDoltCLITransferCommand(context.Background(), "/tmp/beads-cli", creds, true, "fetch", "peer")
	defer cancel()

	if cmd.Dir != "/tmp/beads-cli" {
		t.Fatalf("command dir = %q, want /tmp/beads-cli", cmd.Dir)
	}

	var gotChecksum, gotUser, gotPassword string
	for _, e := range cmd.Env {
		switch {
		case strings.HasPrefix(e, awsResponseChecksumValidationEnv+"="):
			gotChecksum = strings.TrimPrefix(e, awsResponseChecksumValidationEnv+"=")
		case strings.HasPrefix(e, "DOLT_REMOTE_USER="):
			gotUser = strings.TrimPrefix(e, "DOLT_REMOTE_USER=")
		case strings.HasPrefix(e, "DOLT_REMOTE_PASSWORD="):
			gotPassword = strings.TrimPrefix(e, "DOLT_REMOTE_PASSWORD=")
		}
	}

	if gotChecksum != "when_required" {
		t.Fatalf("%s = %q, want when_required", awsResponseChecksumValidationEnv, gotChecksum)
	}
	if gotUser != "user" || gotPassword != "pass" {
		t.Fatalf("credential env = user:%q password:%q", gotUser, gotPassword)
	}
}

func TestApplyNoGitHooksToCmd(t *testing.T) {
	cmd := exec.Command("dolt", "push") // #nosec G204 -- test command is not executed
	applyNoGitHooksToCmd(cmd)

	var got string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS=") {
			got = strings.TrimPrefix(e, "GIT_CONFIG_PARAMETERS=")
			break
		}
	}
	if want := "'core.hooksPath=/dev/null'"; got != want {
		t.Fatalf("GIT_CONFIG_PARAMETERS = %q, want %q", got, want)
	}
}

// TestApplyNoGitHooksToCmdComposesWithCredentials verifies the helper layers
// cleanly on top of credential env vars, so callers that already populated
// cmd.Env via remoteCredentials.applyToCmd keep their values.
func TestApplyNoGitHooksToCmdComposesWithCredentials(t *testing.T) {
	cmd := exec.Command("dolt", "push") // #nosec G204 -- test command is not executed
	(&remoteCredentials{username: "user", password: "pass"}).applyToCmd(cmd)
	applyNoGitHooksToCmd(cmd)

	var gotHooks, gotUser, gotPassword string
	for _, e := range cmd.Env {
		switch {
		case strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS="):
			gotHooks = strings.TrimPrefix(e, "GIT_CONFIG_PARAMETERS=")
		case strings.HasPrefix(e, "DOLT_REMOTE_USER="):
			gotUser = strings.TrimPrefix(e, "DOLT_REMOTE_USER=")
		case strings.HasPrefix(e, "DOLT_REMOTE_PASSWORD="):
			gotPassword = strings.TrimPrefix(e, "DOLT_REMOTE_PASSWORD=")
		}
	}
	if gotHooks != "'core.hooksPath=/dev/null'" {
		t.Fatalf("GIT_CONFIG_PARAMETERS = %q, want hook-disabling override", gotHooks)
	}
	if gotUser != "user" || gotPassword != "pass" {
		t.Fatalf("credential env = user:%q password:%q (creds clobbered by applyNoGitHooksToCmd)", gotUser, gotPassword)
	}
}

// unsetEnvForTest removes key for the duration of the test, restoring the
// ambient value on cleanup.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

func TestWithRemoteOperationEnvRestoresS3ChecksumEnv(t *testing.T) {
	t.Setenv(awsResponseChecksumValidationEnv, "when_supported")

	err := withRemoteOperationEnv(nil, true, func() error {
		if got := os.Getenv(awsResponseChecksumValidationEnv); got != "when_required" {
			t.Fatalf("%s during operation = %q, want when_required", awsResponseChecksumValidationEnv, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRemoteOperationEnv returned error: %v", err)
	}
	if got := os.Getenv(awsResponseChecksumValidationEnv); got != "when_supported" {
		t.Fatalf("%s after operation = %q, want restored when_supported", awsResponseChecksumValidationEnv, got)
	}
}

func TestWithRemoteOperationEnvUnsetsS3ChecksumEnv(t *testing.T) {
	unsetEnvForTest(t, awsResponseChecksumValidationEnv)

	err := withRemoteOperationEnv(nil, true, func() error {
		if got := os.Getenv(awsResponseChecksumValidationEnv); got != "when_required" {
			t.Fatalf("%s during operation = %q, want when_required", awsResponseChecksumValidationEnv, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRemoteOperationEnv returned error: %v", err)
	}
	if _, ok := os.LookupEnv(awsResponseChecksumValidationEnv); ok {
		t.Fatalf("%s should be unset after operation", awsResponseChecksumValidationEnv)
	}
}

// TestWithRemoteOperationEnvRestoresAmbientCredentials verifies that stored
// credentials override an ambient DOLT_REMOTE_USER/DOLT_REMOTE_PASSWORD pair
// for the operation and hand it back afterwards. Unsetting on cleanup would
// destroy credentials the remote operation never owned.
func TestWithRemoteOperationEnvRestoresAmbientCredentials(t *testing.T) {
	t.Setenv("DOLT_REMOTE_USER", "ambient-user")
	t.Setenv("DOLT_REMOTE_PASSWORD", "ambient-pass")

	creds := &remoteCredentials{username: "peer-user", password: "peer-pass"}
	err := withRemoteOperationEnv(creds, false, func() error {
		if got := os.Getenv("DOLT_REMOTE_USER"); got != "peer-user" {
			t.Fatalf("DOLT_REMOTE_USER during operation = %q, want peer-user", got)
		}
		if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "peer-pass" {
			t.Fatalf("DOLT_REMOTE_PASSWORD during operation = %q, want peer-pass", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRemoteOperationEnv returned error: %v", err)
	}

	if got := os.Getenv("DOLT_REMOTE_USER"); got != "ambient-user" {
		t.Fatalf("DOLT_REMOTE_USER after operation = %q, want restored ambient-user", got)
	}
	if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "ambient-pass" {
		t.Fatalf("DOLT_REMOTE_PASSWORD after operation = %q, want restored ambient-pass", got)
	}
}

// TestWithRemoteOperationEnvRestoresPartialAmbientCredentials covers the mixed
// case: a var that was set comes back, a var that was unset stays unset.
func TestWithRemoteOperationEnvRestoresPartialAmbientCredentials(t *testing.T) {
	t.Setenv("DOLT_REMOTE_USER", "ambient-user")
	unsetEnvForTest(t, "DOLT_REMOTE_PASSWORD")

	creds := &remoteCredentials{username: "peer-user", password: "peer-pass"}
	err := withRemoteOperationEnv(creds, false, func() error {
		if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "peer-pass" {
			t.Fatalf("DOLT_REMOTE_PASSWORD during operation = %q, want peer-pass", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRemoteOperationEnv returned error: %v", err)
	}

	if got := os.Getenv("DOLT_REMOTE_USER"); got != "ambient-user" {
		t.Fatalf("DOLT_REMOTE_USER after operation = %q, want restored ambient-user", got)
	}
	if _, ok := os.LookupEnv("DOLT_REMOTE_PASSWORD"); ok {
		t.Fatal("DOLT_REMOTE_PASSWORD should be unset after operation (it was unset before)")
	}
}

// TestWithRemoteOperationEnvUnsetsCredentialsWithNoAmbientPair verifies stored
// credentials do not linger in the process environment when there was nothing
// ambient to restore.
func TestWithRemoteOperationEnvUnsetsCredentialsWithNoAmbientPair(t *testing.T) {
	unsetEnvForTest(t, "DOLT_REMOTE_USER")
	unsetEnvForTest(t, "DOLT_REMOTE_PASSWORD")

	creds := &remoteCredentials{username: "peer-user", password: "peer-pass"}
	err := withRemoteOperationEnv(creds, false, func() error {
		if got := os.Getenv("DOLT_REMOTE_USER"); got != "peer-user" {
			t.Fatalf("DOLT_REMOTE_USER during operation = %q, want peer-user", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRemoteOperationEnv returned error: %v", err)
	}

	if _, ok := os.LookupEnv("DOLT_REMOTE_USER"); ok {
		t.Fatal("DOLT_REMOTE_USER should be unset after operation")
	}
	if _, ok := os.LookupEnv("DOLT_REMOTE_PASSWORD"); ok {
		t.Fatal("DOLT_REMOTE_PASSWORD should be unset after operation")
	}
}

// TestWithRemoteOperationEnvRestoresAmbientEnvWithBothCleanups exercises the
// two-cleanup path, the shape store.go uses for push and pull against an S3
// remote: credentials plus the checksum override are registered together, so
// both restores run from the same defer. All three ambient values must come
// back after the operation.
func TestWithRemoteOperationEnvRestoresAmbientEnvWithBothCleanups(t *testing.T) {
	t.Setenv("DOLT_REMOTE_USER", "ambient-user")
	t.Setenv("DOLT_REMOTE_PASSWORD", "ambient-pass")
	t.Setenv(awsResponseChecksumValidationEnv, "when_supported")

	creds := &remoteCredentials{username: "peer-user", password: "peer-pass"}
	err := withRemoteOperationEnv(creds, true, func() error {
		if got := os.Getenv("DOLT_REMOTE_USER"); got != "peer-user" {
			t.Fatalf("DOLT_REMOTE_USER during operation = %q, want peer-user", got)
		}
		if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "peer-pass" {
			t.Fatalf("DOLT_REMOTE_PASSWORD during operation = %q, want peer-pass", got)
		}
		if got := os.Getenv(awsResponseChecksumValidationEnv); got != "when_required" {
			t.Fatalf("%s during operation = %q, want when_required", awsResponseChecksumValidationEnv, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRemoteOperationEnv returned error: %v", err)
	}

	if got := os.Getenv("DOLT_REMOTE_USER"); got != "ambient-user" {
		t.Fatalf("DOLT_REMOTE_USER after operation = %q, want restored ambient-user", got)
	}
	if got := os.Getenv("DOLT_REMOTE_PASSWORD"); got != "ambient-pass" {
		t.Fatalf("DOLT_REMOTE_PASSWORD after operation = %q, want restored ambient-pass", got)
	}
	if got := os.Getenv(awsResponseChecksumValidationEnv); got != "when_supported" {
		t.Fatalf("%s after operation = %q, want restored when_supported", awsResponseChecksumValidationEnv, got)
	}
}

func TestIsS3RemoteURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "dolt aws", url: "aws://[table:bucket]/db", want: true},
		{name: "s3", url: "s3://bucket/path", want: true},
		{name: "gcs", url: "gs://bucket/path", want: false},
		{name: "azure", url: "az://account.blob.core.windows.net/container", want: false},
		{name: "empty", url: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isS3RemoteURL(tt.url); got != tt.want {
				t.Fatalf("isS3RemoteURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestEncryptDecryptWithWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	for i := range key1 {
		key1[i] = byte(i)
		key2[i] = byte(i + 1)
	}

	encrypted, err := encryptWithKey("password", key1)
	if err != nil {
		t.Fatalf("encryptWithKey failed: %v", err)
	}

	_, err = decryptWithKey(encrypted, key2)
	if err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestCredentialKeyFileGeneration(t *testing.T) {
	tmpDir := t.TempDir()

	store := &DoltStore{dbPath: tmpDir, beadsDir: tmpDir}

	// Key file should not exist yet
	keyPath := filepath.Join(tmpDir, credentialKeyFile)
	if _, err := os.Stat(keyPath); err == nil {
		t.Fatal("key file should not exist before initCredentialKey")
	}

	// initCredentialKey should generate and save a key
	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey failed: %v", err)
	}

	// Key should be set on the store
	if len(store.credentialKey) != 32 {
		t.Fatalf("credentialKey length = %d, want 32", len(store.credentialKey))
	}

	// Key file should exist with restrictive permissions
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file should exist after initCredentialKey: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Log("skipping POSIX mode-bit check on Windows")
	} else if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("key file permissions = %o, want 0600", perm)
	}

	// Reading the key file should return the same key
	savedKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read key file: %v", err)
	}
	if string(savedKey) != string(store.credentialKey) {
		t.Error("saved key does not match store key")
	}
}

func TestCredentialKeyFileReload(t *testing.T) {
	tmpDir := t.TempDir()

	// First store generates the key
	store1 := &DoltStore{dbPath: tmpDir, beadsDir: tmpDir}
	if err := store1.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey (store1) failed: %v", err)
	}

	// Second store should load the same key from file
	store2 := &DoltStore{dbPath: tmpDir, beadsDir: tmpDir}
	if err := store2.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey (store2) failed: %v", err)
	}

	if string(store1.credentialKey) != string(store2.credentialKey) {
		t.Error("second store loaded different key than first store generated")
	}
}

func TestCredentialKeyNotPredictable(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	store1 := &DoltStore{dbPath: tmpDir1, beadsDir: tmpDir1}
	if err := store1.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey (store1) failed: %v", err)
	}

	store2 := &DoltStore{dbPath: tmpDir2, beadsDir: tmpDir2}
	if err := store2.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey (store2) failed: %v", err)
	}

	// Two different installations should generate different keys
	if string(store1.credentialKey) == string(store2.credentialKey) {
		t.Error("different installations generated identical keys — key is not random")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	store := &DoltStore{dbPath: tmpDir, beadsDir: tmpDir}
	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey failed: %v", err)
	}

	password := "my-federation-password-123!"

	encrypted, err := store.encryptPassword(password)
	if err != nil {
		t.Fatalf("encryptPassword failed: %v", err)
	}

	decrypted, err := store.decryptPassword(encrypted)
	if err != nil {
		t.Fatalf("decryptPassword failed: %v", err)
	}

	if decrypted != password {
		t.Errorf("round-trip failed: got %q, want %q", decrypted, password)
	}
}

func TestEncryptPasswordEmpty(t *testing.T) {
	store := &DoltStore{}

	encrypted, err := store.encryptPassword("")
	if err != nil {
		t.Fatalf("encryptPassword with empty string failed: %v", err)
	}
	if encrypted != nil {
		t.Error("expected nil for empty password encryption")
	}
}

func TestDecryptPasswordEmpty(t *testing.T) {
	store := &DoltStore{}

	decrypted, err := store.decryptPassword(nil)
	if err != nil {
		t.Fatalf("decryptPassword with nil failed: %v", err)
	}
	if decrypted != "" {
		t.Errorf("expected empty string, got %q", decrypted)
	}
}

func TestEncryptPasswordNoKey(t *testing.T) {
	store := &DoltStore{}

	_, err := store.encryptPassword("password")
	if err == nil {
		t.Fatal("expected error when key is not initialized")
	}
}

func TestInitCredentialKeyEmptyDbPath(t *testing.T) {
	store := &DoltStore{dbPath: ""}
	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey with empty dbPath should not fail: %v", err)
	}
	if store.credentialKey != nil {
		t.Error("expected nil key when dbPath is empty")
	}
}

func TestEnsureCredentialKeyAlreadyInitialized(t *testing.T) {
	tmpDir := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	store := &DoltStore{
		dbPath:        filepath.Join(tmpDir, "dolt"),
		beadsDir:      tmpDir,
		credentialKey: append([]byte(nil), key...),
	}

	if err := store.ensureCredentialKey(t.Context()); err != nil {
		t.Fatalf("ensureCredentialKey() error = %v", err)
	}
	if string(store.credentialKey) != string(key) {
		t.Fatalf("credentialKey changed unexpectedly: got %q want %q", string(store.credentialKey), string(key))
	}
	if _, err := os.Stat(filepath.Join(tmpDir, credentialKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no key file write when key already initialized, got err=%v", err)
	}
}

func TestEnsureCredentialKeyInitializesWhenMissing(t *testing.T) {
	tmpDir := t.TempDir()
	store := &DoltStore{
		dbPath:   filepath.Join(tmpDir, "dolt"),
		beadsDir: tmpDir,
	}

	if err := store.ensureCredentialKey(t.Context()); err != nil {
		t.Fatalf("ensureCredentialKey() error = %v", err)
	}
	if len(store.credentialKey) != 32 {
		t.Fatalf("credentialKey length = %d, want 32", len(store.credentialKey))
	}
	if _, err := os.Stat(filepath.Join(tmpDir, credentialKeyFile)); err != nil {
		t.Fatalf("expected key file after lazy init: %v", err)
	}
}

func TestAddFederationPeerReturnsCredentialKeyInitError(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create parent file: %v", err)
	}

	store := &DoltStore{
		dbPath:   filepath.Join(parentFile, "dolt"),
		beadsDir: filepath.Join(parentFile, ".beads"),
	}

	err := store.AddFederationPeer(t.Context(), &storage.FederationPeer{
		Name:        "peerone",
		RemoteURL:   "file:///tmp/nonexistent-peer",
		Password:    "secret",
		Sovereignty: "T2",
	})
	if err == nil {
		t.Fatal("expected credential key initialization error")
	}
	if !strings.Contains(err.Error(), "failed to initialize credential key") {
		t.Fatalf("expected credential key initialization error, got: %v", err)
	}
}

func TestDecryptWithKeyShortCiphertext(t *testing.T) {
	key := make([]byte, 32)
	if _, err := decryptWithKey([]byte("short"), key); err == nil || err.Error() != "ciphertext too short" {
		t.Fatalf("decryptWithKey(short) error = %v, want ciphertext too short", err)
	}
}

func TestValidatePeerName(t *testing.T) {
	tests := []struct {
		name    string
		peer    string
		wantErr string
	}{
		{name: "valid", peer: "peer_one-2"},
		{name: "empty", peer: "", wantErr: "peer name cannot be empty"},
		{name: "must start with letter", peer: "1peer", wantErr: "peer name must start with a letter and contain only alphanumeric characters, hyphens, and underscores"},
		{name: "invalid character", peer: "peer.one", wantErr: "peer name must start with a letter and contain only alphanumeric characters, hyphens, and underscores"},
		{name: "too long", peer: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_extra", wantErr: "peer name too long (max 64 characters)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePeerName(tt.peer)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePeerName(%q) unexpected error: %v", tt.peer, err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("validatePeerName(%q) error = %v, want %q", tt.peer, err, tt.wantErr)
			}
		})
	}
}

func TestCredentialKeyMigrationFromDbPath(t *testing.T) {
	// Simulate old layout: key file in .beads/dolt/ (dbPath)
	beadsDir := t.TempDir()
	dbPath := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(dbPath, 0700); err != nil {
		t.Fatal(err)
	}

	// Write key to old location
	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = byte(i)
	}
	oldKeyPath := filepath.Join(dbPath, credentialKeyFile)
	if err := os.WriteFile(oldKeyPath, oldKey, 0600); err != nil {
		t.Fatal(err)
	}

	store := &DoltStore{dbPath: dbPath, beadsDir: beadsDir}
	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey failed: %v", err)
	}

	// Key should be loaded from old location
	if string(store.credentialKey) != string(oldKey) {
		t.Error("migrated key does not match original")
	}

	// New location should now have the key
	newKeyPath := filepath.Join(beadsDir, credentialKeyFile)
	newKey, err := os.ReadFile(newKeyPath)
	if err != nil {
		t.Fatalf("key file should exist at new location: %v", err)
	}
	if string(newKey) != string(oldKey) {
		t.Error("key at new location does not match original")
	}

	// Old location should be cleaned up
	if _, err := os.Stat(oldKeyPath); err == nil {
		t.Error("old key file should have been removed after migration")
	}
}

func TestCredentialKeyNoGhostDir(t *testing.T) {
	// In shared-server mode, dbPath (.beads/dolt/) should NOT be created
	beadsDir := t.TempDir()
	dbPath := filepath.Join(beadsDir, "dolt") // does not exist

	store := &DoltStore{dbPath: dbPath, beadsDir: beadsDir}
	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey failed: %v", err)
	}

	// The key should be written to beadsDir, not dbPath
	if _, err := os.Stat(filepath.Join(beadsDir, credentialKeyFile)); err != nil {
		t.Error("key file should exist in beadsDir")
	}

	// dbPath directory should NOT have been created
	if _, err := os.Stat(dbPath); err == nil {
		t.Error("dbPath directory should not be created in shared-server mode — ghost directory bug")
	}
}

// TestCredentialKeyCreatesBeadsDir verifies that initCredentialKey creates the
// .beads/ directory if it doesn't exist. This is needed for external server
// setups where bd connects to a pre-existing dolt server without bd init (GH#2641).
func TestCredentialKeyCreatesBeadsDir(t *testing.T) {
	parentDir := t.TempDir()
	beadsDir := filepath.Join(parentDir, ".beads") // does not exist yet

	store := &DoltStore{dbPath: "", beadsDir: beadsDir}
	if err := store.initCredentialKey(t.Context()); err != nil {
		t.Fatalf("initCredentialKey failed when beadsDir doesn't exist: %v", err)
	}

	// Key should be generated
	if len(store.credentialKey) != 32 {
		t.Fatalf("credentialKey length = %d, want 32", len(store.credentialKey))
	}

	// .beads/ directory should have been created
	info, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatalf(".beads/ directory should have been created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal(".beads/ should be a directory")
	}

	// Key file should exist in the newly created directory
	keyPath := filepath.Join(beadsDir, credentialKeyFile)
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key file should exist in newly created .beads/: %v", err)
	}
}

func TestFederationPeerCredentialLifecycleLazyKeyInit(t *testing.T) {
	skipIfNoServer(t)

	ctx := context.Background()
	baseDir := t.TempDir()
	beadsDir := filepath.Join(baseDir, ".beads")
	dbName := fmt.Sprintf("test_federation_credentials_%d", testServerPort)

	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	store, err := New(ctx, &Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	peer := &storage.FederationPeer{
		Name:        "peerone",
		RemoteURL:   "file:///tmp/nonexistent-peer",
		Username:    "alice",
		Password:    "s3cret",
		Sovereignty: "T2",
	}

	if err := store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, credentialKeyFile)); err != nil {
		t.Fatalf("expected credential key file after adding peer with password: %v", err)
	}

	store.credentialKey = nil
	got, err := store.GetFederationPeer(ctx, peer.Name)
	if err != nil {
		t.Fatalf("GetFederationPeer() error = %v", err)
	}
	if got.Password != peer.Password {
		t.Fatalf("GetFederationPeer().Password = %q, want %q", got.Password, peer.Password)
	}

	store.credentialKey = nil
	peers, err := store.ListFederationPeers(ctx)
	if err != nil {
		t.Fatalf("ListFederationPeers() error = %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("ListFederationPeers() length = %d, want 1", len(peers))
	}
	if peers[0].Password != peer.Password {
		t.Fatalf("ListFederationPeers()[0].Password = %q, want %q", peers[0].Password, peer.Password)
	}
}

// A federation_peers row travels with the database; the credential key file
// does not. A database opened on a second machine therefore holds a peer
// password this machine's key cannot read, and both server-mode read paths
// must say so with the remediation instead of surfacing a bare
// "cipher: message authentication failed" (GH#5085 review). sqlmock stands in
// for the server, following draincall_regression_test.go, so the branch is
// pinned without a live Dolt.
func TestFederationPeerDecryptKeyMismatchNamesTheLocalKey(t *testing.T) {
	writerKey := make([]byte, 32)
	for i := range writerKey {
		writerKey[i] = byte(i)
	}
	localKey := make([]byte, 32)
	for i := range localKey {
		localKey[i] = byte(i + 1)
	}

	encrypted, err := encryptWithKey("peerpass", writerKey)
	if err != nil {
		t.Fatalf("encryptWithKey() error = %v", err)
	}

	// credentialKey is preset, so ensureCredentialKey is a no-op and no test
	// writes a key file.
	newStore := func(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
		t.Helper()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return &DoltStore{db: db, credentialKey: localKey}, mock
	}

	peerRows := func() *sqlmock.Rows {
		now := time.Now()
		return sqlmock.NewRows([]string{
			"name", "remote_url", "username", "password_encrypted",
			"sovereignty", "last_sync", "created_at", "updated_at",
		}).AddRow("team", "https://peer.example/peerdb", "peeruser", encrypted, "T2", nil, now, now)
	}

	wantFragments := []string{
		// Both paths name the peer, so the operator knows which one to re-add.
		"failed to decrypt password for peer team",
		// The shared enrichment, pinned verbatim. The machine-local key file is
		// context in a parenthetical, not an asserted cause: an AES-GCM open also
		// fails on a tampered blob and on a row still under the legacy key, and
		// re-adding the peer is the fix in all three cases.
		"stored peer credentials cannot be decrypted with this machine's credential key " +
			"(the key file " + credentialKeyFile + " is machine-local and does not replicate with the database); " +
			"re-run 'bd federation add-peer <name> <url> --user <user>' on this machine",
		// The cipher error stays wrapped, so the raw cause is still readable.
		"cipher: message authentication failed",
	}

	assertMismatch := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("read succeeded, want decrypt failure")
		}
		if !errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want errors.Is storage.ErrCredentialKeyMismatch", err)
		}
		for _, want := range wantFragments {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	}

	t.Run("GetFederationPeer", func(t *testing.T) {
		store, mock := newStore(t)
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
			WithArgs("team").
			WillReturnRows(peerRows())

		_, err := store.GetFederationPeer(t.Context(), "team")
		assertMismatch(t, err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("ListFederationPeers", func(t *testing.T) {
		store, mock := newStore(t)
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers ORDER BY name")).
			WillReturnRows(peerRows())

		_, err := store.ListFederationPeers(t.Context())
		assertMismatch(t, err)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}

// openCloudAuthTestStore opens a DoltStore against the shared test Dolt server
// for cloud-auth routing tests. The returned store has serverMode=true and a
// fresh empty database; callers should AddRemote to seed the SQL surface.
func openCloudAuthTestStore(t *testing.T, dbSuffix string) *DoltStore {
	t.Helper()
	ctx := context.Background()
	dbName := fmt.Sprintf("test_cloud_auth_%s_%d", dbSuffix, testServerPort)
	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	store, err := New(ctx, &Config{
		Path:            filepath.Join(t.TempDir(), "dolt"),
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ensureCloudAuthCLIDatabase(t *testing.T, store *DoltStore) {
	t.Helper()
	testutil.RequireDoltBinary(t)

	cliDir := store.CLIDir()
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatalf("create CLI dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, ".dolt")); os.IsNotExist(err) {
		c := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com") // #nosec G204 -- fixed test command
		c.Dir = cliDir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("dolt init failed: %v\n%s", err, out)
		}
	}
}

func addCloudAuthCLIRemote(t *testing.T, store *DoltStore, name, url string) {
	t.Helper()
	ensureCloudAuthCLIDatabase(t, store)

	cliDir := store.CLIDir()
	c := exec.Command("dolt", "remote", "add", name, url) // #nosec G204 -- fixed test command
	c.Dir = cliDir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt remote add %q failed: %v\n%s", name, err, out)
	}
}

// clearCloudAuthEnv unsets all process env vars matching the cloud-auth scheme
// prefixes so shouldUseCLIForCloudAuth tests are deterministic regardless of
// the runner's ambient AWS_*/AZURE_*/etc. environment. Originals restored on
// test cleanup.
func clearCloudAuthEnv(t *testing.T) {
	t.Helper()
	prefixes := []string{"AZURE_STORAGE_", "AWS_", "GOOGLE_", "GCS_", "OCI_", "DOLT_REMOTE_"}
	for _, e := range os.Environ() {
		eq := strings.IndexByte(e, '=')
		if eq < 0 {
			continue
		}
		key := e[:eq]
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				k, v := key, os.Getenv(key)
				_ = os.Unsetenv(k)
				t.Cleanup(func() { _ = os.Setenv(k, v) })
				break
			}
		}
	}
}

func TestCloudAuthCLIRouting(t *testing.T) {
	skipIfNoServer(t)
	clearCloudAuthEnv(t)
	start := time.Now()

	tests := []struct {
		name      string
		remoteURL string // URL scheme determines which env vars are relevant
		envKey    string // env var to set (empty = none)
		envValue  string
		wantCLI   bool
	}{
		// Per-scheme positive: env var matches remote's scheme → CLI routing
		{"azure env + az:// remote", "az://account.blob.core.windows.net/container", "AZURE_STORAGE_ACCOUNT", "myaccount", true},
		{"azure key + az:// remote", "az://account.blob.core.windows.net/container", "AZURE_STORAGE_KEY", "mykey", true},
		{"aws env + s3:// remote", "s3://my-bucket/path", "AWS_ACCESS_KEY_ID", "AKID", true},
		{"aws secret + s3:// remote", "s3://my-bucket/path", "AWS_SECRET_ACCESS_KEY", "secret", true},
		{"google creds + gs:// remote", "gs://my-bucket/path", "GOOGLE_APPLICATION_CREDENTIALS", "/path/to/creds.json", true},
		{"gcs creds + gs:// remote", "gs://my-bucket/path", "GCS_CREDENTIALS_FILE", "/path/to/creds.json", true},
		{"oci env + oci:// remote", "oci://my-namespace/my-bucket/path", "OCI_TENANCY", "ocid1.tenancy", true},
		{"dolt env + dolthub:// remote", "dolthub://org/beads", "DOLT_REMOTE_USER", "admin", true},
		{"dolt env + https:// remote", "https://example.com/repo", "DOLT_REMOTE_USER", "admin", true},
		{"dolt env + http:// remote", "http://example.com/repo", "DOLT_REMOTE_USER", "admin", true},

		// Per-scheme negative: env var does NOT match remote's scheme → SQL fallback
		{"azure env + dolthub:// remote", "dolthub://org/beads", "AZURE_STORAGE_ACCOUNT", "myaccount", false},
		{"azure env + https:// remote", "https://example.com/repo", "AZURE_STORAGE_ACCOUNT", "myaccount", false},
		{"azure env + s3:// remote", "s3://my-bucket/path", "AZURE_STORAGE_ACCOUNT", "myaccount", false},
		{"aws env + az:// remote", "az://account.blob.core.windows.net/container", "AWS_ACCESS_KEY_ID", "AKID", false},
		{"dolt env + az:// remote", "az://account.blob.core.windows.net/container", "DOLT_REMOTE_USER", "admin", false},

		// Structural negative: missing conditions → SQL fallback
		{"no cloud env", "az://account.blob.core.windows.net/container", "", "", false},
	}
	// Shared store: creating one Dolt database per case (16 total) is what
	// made this test slow (see the regression guard below). Each case gets
	// its own remote name (origin_0..origin_15) against a single store,
	// since shouldUseCLIForCloudAuth's routing decision is keyed purely by
	// remote name — distinct names are enough to keep cases isolated.
	store := openCloudAuthTestStore(t, "route")
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			remote := fmt.Sprintf("origin_%d", i)
			if err := store.AddRemote(ctx, remote, tt.remoteURL); err != nil {
				t.Fatalf("AddRemote: %v", err)
			}
			addCloudAuthCLIRemote(t, store, remote, tt.remoteURL)
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envValue)
			}
			got := store.shouldUseCLIForCloudAuth(ctx, remote)
			if got != tt.wantCLI {
				t.Errorf("shouldUseCLIForCloudAuth() = %v, want %v", got, tt.wantCLI)
			}
		})
	}

	// Regression guard: this test previously created one Dolt database per
	// case (16 total), each slower than the last as server load grew,
	// pushing wall time into the hundreds of seconds for a test that only
	// exercises a pure routing predicate. 90s sits below every measured
	// unfixed run and comfortably above the shared-store fix's expected
	// time, so it fails on the old per-case-store shape without flaking
	// under normal CI load.
	if elapsed := time.Since(start); elapsed > 90*time.Second {
		t.Fatalf("TestCloudAuthCLIRouting took %s, want < 90s (regression: are per-case Dolt databases being created again instead of a shared store?)", elapsed)
	}
}

func TestCloudAuthCLIRoutingStructural(t *testing.T) {
	clearCloudAuthEnv(t)
	t.Run("embedded mode", func(t *testing.T) {
		// serverMode=false short-circuits before any DB access, so no real
		// store is required for this case.
		store := &DoltStore{serverMode: false, remote: "origin"}
		t.Setenv("AZURE_STORAGE_ACCOUNT", "myaccount")
		if store.shouldUseCLIForCloudAuth(context.Background(), store.remote) {
			t.Error("expected false in embedded mode")
		}
	})
	t.Run("no remote configured", func(t *testing.T) {
		skipIfNoServer(t)
		// Needs its own fresh store: the precondition under test is the
		// ABSENCE of any configured remote, which TestCloudAuthCLIRouting's
		// shared store (populated with origin_0..origin_15) can't provide.
		store := openCloudAuthTestStore(t, "structural_no_remote")
		t.Setenv("AZURE_STORAGE_ACCOUNT", "myaccount")
		if store.shouldUseCLIForCloudAuth(context.Background(), "origin") {
			t.Error("expected false when remote not configured")
		}
	})
	t.Run("sql remote materializes local CLI remote", func(t *testing.T) {
		skipIfNoServer(t)
		// Needs its own fresh store: the precondition under test is a SQL
		// remote with NO local CLI remote materialized yet (checked
		// explicitly below), which a store shared across other cases —
		// which pre-populate CLI remotes — can't provide.
		store := openCloudAuthTestStore(t, "structural_sql_only")
		remoteURL := "az://account.blob.core.windows.net/container"
		if err := store.AddRemote(context.Background(), "origin", remoteURL); err != nil {
			t.Fatalf("AddRemote: %v", err)
		}
		ensureCloudAuthCLIDatabase(t, store)
		if got := doltutil.FindCLIRemote(store.CLIDir(), "origin"); got != "" {
			t.Fatalf("precondition: local CLI remote = %q, want absent", got)
		}
		t.Setenv("AZURE_STORAGE_ACCOUNT", "myaccount")
		if !store.shouldUseCLIForCloudAuth(context.Background(), "origin") {
			t.Error("expected true when SQL remote can be materialized into local CLI directory")
		}
		if got := doltutil.FindCLIRemote(store.CLIDir(), "origin"); !doltutil.RemoteURLsMatch(got, remoteURL) {
			t.Errorf("local CLI remote = %q, want %q", got, remoteURL)
		}
	})
}

// TestPerRemoteCloudAuthHybrid verifies the core use case: a hybrid setup with
// DoltHub (primary) + Azure (backup) remotes. AZURE_STORAGE_ACCOUNT should
// trigger CLI routing ONLY for the Azure remote, not the DoltHub remote.
func TestPerRemoteCloudAuthHybrid(t *testing.T) {
	skipIfNoServer(t)
	clearCloudAuthEnv(t)

	ctx := context.Background()
	store := openCloudAuthTestStore(t, "hybrid")
	if err := store.AddRemote(ctx, "primary", "dolthub://org/beads"); err != nil {
		t.Fatalf("AddRemote primary: %v", err)
	}
	if err := store.AddRemote(ctx, "backup", "az://account.blob.core.windows.net/dolt/beads"); err != nil {
		t.Fatalf("AddRemote backup: %v", err)
	}
	addCloudAuthCLIRemote(t, store, "primary", "dolthub://org/beads")
	addCloudAuthCLIRemote(t, store, "backup", "az://account.blob.core.windows.net/dolt/beads")

	t.Setenv("AZURE_STORAGE_ACCOUNT", "myaccount")

	if !store.shouldUseCLIForCloudAuth(ctx, "backup") {
		t.Error("expected CLI routing for az:// remote when AZURE_STORAGE_ACCOUNT is set")
	}
	if store.shouldUseCLIForCloudAuth(ctx, "primary") {
		t.Error("expected SQL routing for dolthub:// remote when AZURE_STORAGE_ACCOUNT is set — per-remote resolution should prevent misrouting")
	}
}

// TestEnvPrefixesForRemoteURL verifies the scheme-to-env-prefix mapping.
func TestEnvPrefixesForRemoteURL(t *testing.T) {
	tests := []struct {
		url     string
		wantNil bool
		wantHas string // one prefix we expect to find
	}{
		{"az://account.blob.core.windows.net/container", false, "AZURE_STORAGE_"},
		{"s3://my-bucket/path", false, "AWS_"},
		{"gs://my-bucket/path", false, "GOOGLE_"},
		{"oci://namespace/bucket/path", false, "OCI_"},
		{"dolthub://org/repo", false, "DOLT_REMOTE_"},
		{"https://dolthub.com/org/repo", false, "DOLT_REMOTE_"},
		{"http://localhost:8080/repo", false, "DOLT_REMOTE_"},
		{"git+ssh://host/repo", true, ""},  // git protocol — handled elsewhere
		{"ssh://host/repo", true, ""},      // git protocol — handled elsewhere
		{"file:///path/to/repo", true, ""}, // local filesystem — no cloud auth
		{"git@host:repo.git", true, ""},    // SCP-style — handled elsewhere
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := envPrefixesForRemoteURL(tt.url)
			if tt.wantNil && got != nil {
				t.Errorf("envPrefixesForRemoteURL(%q) = %v, want nil", tt.url, got)
			}
			if !tt.wantNil {
				if got == nil {
					t.Fatalf("envPrefixesForRemoteURL(%q) = nil, want non-nil", tt.url)
				}
				found := false
				for _, p := range got {
					if p == tt.wantHas {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("envPrefixesForRemoteURL(%q) = %v, missing %q", tt.url, got, tt.wantHas)
				}
			}
		})
	}
}

// An uninitialized credential key is not a key mismatch, and the distinction
// is reachable from the peer readers: initCredentialKey returns nil without
// setting a key when beadsDir is empty, so ensureCredentialKey succeeds and
// decryptPassword still finds no key. Wrapping at the callers labeled that
// error a mismatch and told the operator to re-add the peer, which does
// nothing when the key was never created; the fix there is init or file
// permissions. The wrap therefore lives at the decrypt funnel instead.
func TestFederationPeerUninitializedKeyIsNotAMismatch(t *testing.T) {
	// beadsDir empty and credentialKey nil: ensureCredentialKey is a no-op
	// that reports success, which is the edge the caller-side wrap mislabeled.
	newStore := func(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
		t.Helper()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return &DoltStore{db: db}, mock
	}

	peerRows := func() *sqlmock.Rows {
		now := time.Now()
		return sqlmock.NewRows([]string{
			"name", "remote_url", "username", "password_encrypted",
			"sovereignty", "last_sync", "created_at", "updated_at",
		}).AddRow("team", "https://peer.example/peerdb", "peeruser",
			[]byte("stored ciphertext"), "T2", nil, now, now)
	}

	assertNotMismatch := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("read succeeded, want a decrypt failure")
		}
		if errors.Is(err, storage.ErrCredentialKeyMismatch) {
			t.Errorf("error = %v, want NOT errors.Is storage.ErrCredentialKeyMismatch", err)
		}
		if !strings.Contains(err.Error(), "credential encryption key not initialized") {
			t.Errorf("error = %v, want it to name the uninitialized key", err)
		}
		if strings.Contains(err.Error(), "re-run 'bd federation add-peer") {
			t.Errorf("error = %v, must not prescribe re-adding the peer for an uninitialized key", err)
		}
	}

	t.Run("GetFederationPeer", func(t *testing.T) {
		store, mock := newStore(t)
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers WHERE name = ?")).
			WithArgs("team").
			WillReturnRows(peerRows())

		_, err := store.GetFederationPeer(t.Context(), "team")
		assertNotMismatch(t, err)
	})

	t.Run("ListFederationPeers", func(t *testing.T) {
		store, mock := newStore(t)
		mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers ORDER BY name")).
			WillReturnRows(peerRows())

		_, err := store.ListFederationPeers(t.Context())
		assertNotMismatch(t, err)
	})
}

// The key-rotation reader passes an old key to decryptWithKey on purpose, so
// that helper must stay unwrapped: a rotation miss is not a mismatch.
func TestDecryptWithKeyStaysUnwrappedForRotation(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(255 - i)
	}
	encrypted, err := encryptWithKey("secret", key)
	if err != nil {
		t.Fatalf("encryptWithKey() error = %v", err)
	}

	_, err = decryptWithKey(encrypted, other)
	if err == nil {
		t.Fatal("decryptWithKey with the wrong key succeeded, want an error")
	}
	if errors.Is(err, storage.ErrCredentialKeyMismatch) {
		t.Errorf("error = %v, want the bare cipher error; classification belongs to decryptPassword", err)
	}
}
