package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	if perm := info.Mode().Perm(); perm != 0600 {
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

// setupCredentialTestStore creates a DoltStore with a dolt-initialized CLI directory
// and "origin" remote for credential routing tests. Requires dolt CLI.
func setupCredentialTestStore(t *testing.T, remoteUser, remotePassword string, serverMode, setupRemote bool) *DoltStore {
	t.Helper()
	tmpDir := t.TempDir()
	dbName := "testdb"
	dbDir := filepath.Join(tmpDir, dbName)

	if setupRemote {
		if err := os.MkdirAll(dbDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("dolt", "init")
		cmd.Dir = dbDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt init failed: %s: %v", out, err)
		}
		cmd = exec.Command("dolt", "remote", "add", "origin", "https://example.com/repo")
		cmd.Dir = dbDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt remote add failed: %s: %v", out, err)
		}
	}

	return &DoltStore{
		remoteUser:     remoteUser,
		remotePassword: remotePassword,
		serverMode:     serverMode,
		dbPath:         tmpDir,
		database:       dbName,
		remote:         "origin",
	}
}

// TestCredentialCLIRouting verifies the shouldUseCLIForCredentials guard that controls
// CLI subprocess routing for Push, ForcePush, and Pull when credentials are set.
// The guard is shared across all three operations (same insertion pattern in store.go).
func TestCredentialCLIRouting(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping credential routing test")
	}

	tests := []struct {
		name           string
		remoteUser     string
		remotePassword string
		serverMode     bool
		setupRemote    bool // if true, init dolt dir and add "origin" remote
		wantCLI        bool
	}{
		// Positive cases: guard returns true → CLI routing for Push/ForcePush/Pull
		{"credentials+serverMode+remote", "user", "pass", true, true, true},
		{"password only", "", "pass", true, true, true},
		// Negative cases: guard returns false → SQL fallback
		{"no credentials", "", "", true, true, false},
		{"no server mode (embedded)", "user", "pass", false, true, false},
		{"no CLI remote", "user", "pass", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := setupCredentialTestStore(t, tt.remoteUser, tt.remotePassword, tt.serverMode, tt.setupRemote)
			got := store.shouldUseCLIForCredentials(context.Background())
			if got != tt.wantCLI {
				t.Errorf("shouldUseCLIForCredentials() = %v, want %v", got, tt.wantCLI)
			}
		})
	}
}

func TestCredentialCLIRoutingExternalServer(t *testing.T) {
	// External server mode: credentials set but CLIDir points to a directory
	// without the remote configured. Guard should return false (SQL fallback).
	store := &DoltStore{
		remoteUser:     "user",
		remotePassword: "pass",
		serverMode:     true,
		dbPath:         t.TempDir(), // empty dir, no .dolt/
		database:       "testdb",
		remote:         "origin",
	}
	if store.shouldUseCLIForCredentials(context.Background()) {
		t.Error("expected false for external server mode (no CLI remote in CLIDir)")
	}
}

func TestCredentialCLIRoutingNoRemote(t *testing.T) {
	// When credentials are set but no CLI remote exists,
	// shouldUseCLIForCredentials returns false, allowing
	// fallthrough to the SQL path (withEnvCredentials).
	store := &DoltStore{
		remoteUser:     "user",
		remotePassword: "pass",
		serverMode:     true,
		dbPath:         t.TempDir(),
		database:       "nodb",
		remote:         "origin",
	}
	if store.shouldUseCLIForCredentials(context.Background()) {
		t.Error("expected false when CLI remote does not exist")
	}
}

// TestFederationCredentialCLIRouting verifies the shouldUseCLIForPeerCredentials guard
// that controls CLI subprocess routing for federation PushTo, PullFrom, and Fetch
// when peer credentials are resolved from the federation_peers table.
func TestFederationCredentialCLIRouting(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping credential routing test")
	}

	tests := []struct {
		name        string
		serverMode  bool
		setupRemote bool
		credsEmpty  bool
		wantCLI     bool
	}{
		{"peer credentials+serverMode+remote", true, true, false, true},
		{"no peer credentials", true, true, true, false},
		{"no server mode (embedded)", false, true, false, false},
		{"no CLI remote for peer", true, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := setupCredentialTestStore(t, "", "", tt.serverMode, tt.setupRemote)

			var creds *remoteCredentials
			if !tt.credsEmpty {
				creds = &remoteCredentials{username: "peeruser", password: "peerpass"}
			} else {
				creds = &remoteCredentials{}
			}

			got := store.shouldUseCLIForPeerCredentials(context.Background(), "origin", creds)
			if got != tt.wantCLI {
				t.Errorf("shouldUseCLIForPeerCredentials() = %v, want %v", got, tt.wantCLI)
			}
		})
	}
}

func TestCloudAuthCLIRouting(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed")
	}

	tests := []struct {
		name        string
		serverMode  bool
		setupRemote bool
		envKey      string // env var to set (empty = none)
		envValue    string
		wantCLI     bool
	}{
		// Positive: cloud env + server mode + remote configured → CLI
		{"azure storage account", true, true, "AZURE_STORAGE_ACCOUNT", "myaccount", true},
		{"azure storage key", true, true, "AZURE_STORAGE_KEY", "mykey", true},
		{"aws access key", true, true, "AWS_ACCESS_KEY_ID", "AKID", true},
		{"aws secret key", true, true, "AWS_SECRET_ACCESS_KEY", "secret", true},
		{"google creds", true, true, "GOOGLE_APPLICATION_CREDENTIALS", "/path/to/creds.json", true},
		{"gcs creds file", true, true, "GCS_CREDENTIALS_FILE", "/path/to/creds.json", true},
		{"oci var", true, true, "OCI_TENANCY", "ocid1.tenancy", true},
		{"dolt remote user", true, true, "DOLT_REMOTE_USER", "admin", true},
		// Negative: missing conditions → SQL fallback
		{"no cloud env", true, true, "", "", false},
		{"embedded mode", false, true, "AZURE_STORAGE_ACCOUNT", "myaccount", false},
		{"no CLI remote", true, false, "AZURE_STORAGE_ACCOUNT", "myaccount", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a clean store (no remoteUser/remotePassword — cloud auth uses env vars)
			store := setupCredentialTestStore(t, "", "", tt.serverMode, tt.setupRemote)
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envValue)
			}
			got := store.shouldUseCLIForCloudAuth()
			if got != tt.wantCLI {
				t.Errorf("shouldUseCLIForCloudAuth() = %v, want %v", got, tt.wantCLI)
			}
		})
	}
}
