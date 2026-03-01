package dolt

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
)

// Credential storage and encryption for federation peers.
// Enables SQL user authentication when syncing with peer Gas Towns.

// credentialKeyFile is the filename for the random encryption key stored alongside the database.
const credentialKeyFile = ".beads-credential-key"

// federationEnvMutex protects DOLT_REMOTE_USER/PASSWORD env vars from concurrent access.
// Environment variables are process-global, so we need to serialize federation operations.
var federationEnvMutex sync.Mutex

// validPeerNameRegex matches valid peer names (alphanumeric, hyphens, underscores)
var validPeerNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// validatePeerName checks that a peer name is safe for use as a Dolt remote name
func validatePeerName(name string) error {
	if name == "" {
		return fmt.Errorf("peer name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("peer name too long (max 64 characters)")
	}
	if !validPeerNameRegex.MatchString(name) {
		return fmt.Errorf("peer name must start with a letter and contain only alphanumeric characters, hyphens, and underscores")
	}
	return nil
}

// initCredentialKey loads or generates the credential encryption key.
// If a key file exists at <dbPath>/.beads-credential-key, it is loaded.
// Otherwise, a new random key is generated, any existing credentials are
// migrated from the old dbPath-derived key, and the new key is saved.
func (s *DoltStore) initCredentialKey(ctx context.Context) error {
	if s.dbPath == "" {
		return nil // No filesystem path — credential encryption unavailable
	}

	keyPath := filepath.Join(s.dbPath, credentialKeyFile)

	// Try to load existing key file
	key, err := os.ReadFile(keyPath)
	if err == nil && len(key) == 32 {
		s.credentialKey = key
		return nil
	}

	// Generate new random 32-byte key (AES-256)
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("failed to generate credential encryption key: %w", err)
	}

	// Migrate existing credentials from old dbPath-derived key to new random key
	if err := s.migrateCredentialKeys(ctx, key); err != nil {
		return fmt.Errorf("failed to migrate credential keys: %w", err)
	}

	// Ensure the directory exists before writing the key file
	if err := os.MkdirAll(s.dbPath, 0700); err != nil {
		return fmt.Errorf("failed to create directory for credential key: %w", err)
	}

	// Write key file with owner-only permissions (0600)
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return fmt.Errorf("failed to write credential key file: %w", err)
	}

	s.credentialKey = key
	return nil
}

// legacyEncryptionKey derives the old predictable key from dbPath.
// Used only during migration from the old key derivation scheme.
func (s *DoltStore) legacyEncryptionKey() []byte {
	h := sha256.New()
	h.Write([]byte(s.dbPath + "beads-federation-key-v1"))
	return h.Sum(nil)
}

// migrateCredentialKeys re-encrypts all stored federation passwords from the
// old dbPath-derived key to the new random key.
func (s *DoltStore) migrateCredentialKeys(ctx context.Context, newKey []byte) error {
	if s.db == nil {
		return nil // No database connection — nothing to migrate
	}

	oldKey := s.legacyEncryptionKey()

	rows, err := s.queryContext(ctx, `
		SELECT name, password_encrypted FROM federation_peers
		WHERE password_encrypted IS NOT NULL AND LENGTH(password_encrypted) > 0
	`)
	if err != nil {
		// Table may not exist yet (fresh install) — not an error
		return nil
	}
	defer rows.Close()

	type migrationEntry struct {
		name      string
		plaintext string
	}

	var toMigrate []migrationEntry
	for rows.Next() {
		var name string
		var encrypted []byte
		if err := rows.Scan(&name, &encrypted); err != nil {
			return fmt.Errorf("failed to scan peer for migration: %w", err)
		}

		// Decrypt with old key
		plaintext, err := decryptWithKey(encrypted, oldKey)
		if err != nil {
			// Can't decrypt with old key — skip (may already use a different scheme)
			continue
		}
		toMigrate = append(toMigrate, migrationEntry{name: name, plaintext: plaintext})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate peers for migration: %w", err)
	}

	// Re-encrypt each password with the new key
	for _, entry := range toMigrate {
		encrypted, err := encryptWithKey(entry.plaintext, newKey)
		if err != nil {
			return fmt.Errorf("failed to re-encrypt password for peer %s: %w", entry.name, err)
		}
		if _, err := s.execContext(ctx, `
			UPDATE federation_peers SET password_encrypted = ? WHERE name = ?
		`, encrypted, entry.name); err != nil {
			return fmt.Errorf("failed to update encrypted password for peer %s: %w", entry.name, err)
		}
	}

	return nil
}

// encryptWithKey encrypts plaintext using AES-GCM with the given key.
func encryptWithKey(plaintext string, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// decryptWithKey decrypts ciphertext using AES-GCM with the given key.
func decryptWithKey(encrypted []byte, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// encryptPassword encrypts a password using AES-GCM with the store's credential key.
func (s *DoltStore) encryptPassword(password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	key := s.credentialKey
	if key == nil {
		return nil, fmt.Errorf("credential encryption key not initialized")
	}
	return encryptWithKey(password, key)
}

// decryptPassword decrypts a password using AES-GCM with the store's credential key.
func (s *DoltStore) decryptPassword(encrypted []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", nil
	}
	key := s.credentialKey
	if key == nil {
		return "", fmt.Errorf("credential encryption key not initialized")
	}
	return decryptWithKey(encrypted, key)
}

// AddFederationPeer adds or updates a federation peer with credentials.
// This stores credentials in the database and also adds the Dolt remote.
func (s *DoltStore) AddFederationPeer(ctx context.Context, peer *storage.FederationPeer) error {
	// Validate peer name
	if err := validatePeerName(peer.Name); err != nil {
		return fmt.Errorf("invalid peer name: %w", err)
	}

	// Encrypt password before storing
	var encryptedPwd []byte
	var err error
	if peer.Password != "" {
		encryptedPwd, err = s.encryptPassword(peer.Password)
		if err != nil {
			return fmt.Errorf("failed to encrypt password: %w", err)
		}
	}

	// Upsert the peer credentials
	_, err = s.execContext(ctx, `
		INSERT INTO federation_peers (name, remote_url, username, password_encrypted, sovereignty)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			remote_url = VALUES(remote_url),
			username = VALUES(username),
			password_encrypted = VALUES(password_encrypted),
			sovereignty = VALUES(sovereignty),
			updated_at = CURRENT_TIMESTAMP
	`, peer.Name, peer.RemoteURL, peer.Username, encryptedPwd, peer.Sovereignty)

	if err != nil {
		return fmt.Errorf("failed to add federation peer: %w", err)
	}

	// Also add the Dolt remote
	if err := s.AddRemote(ctx, peer.Name, peer.RemoteURL); err != nil {
		// Ignore "remote already exists" errors
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("failed to add dolt remote: %w", err)
		}
	}

	return nil
}

// GetFederationPeer retrieves a federation peer by name.
// Returns storage.ErrNotFound (wrapped) if the peer does not exist.
func (s *DoltStore) GetFederationPeer(ctx context.Context, name string) (*storage.FederationPeer, error) {
	var peer storage.FederationPeer
	var encryptedPwd []byte
	var lastSync sql.NullTime
	var username sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT name, remote_url, username, password_encrypted, sovereignty, last_sync, created_at, updated_at
		FROM federation_peers WHERE name = ?
	`, name).Scan(&peer.Name, &peer.RemoteURL, &username, &encryptedPwd, &peer.Sovereignty, &lastSync, &peer.CreatedAt, &peer.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: federation peer %s", storage.ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get federation peer: %w", err)
	}

	if username.Valid {
		peer.Username = username.String
	}
	if lastSync.Valid {
		peer.LastSync = &lastSync.Time
	}

	// Decrypt password
	if len(encryptedPwd) > 0 {
		peer.Password, err = s.decryptPassword(encryptedPwd)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt password: %w", err)
		}
	}

	return &peer, nil
}

// ListFederationPeers returns all configured federation peers.
func (s *DoltStore) ListFederationPeers(ctx context.Context) ([]*storage.FederationPeer, error) {
	rows, err := s.queryContext(ctx, `
		SELECT name, remote_url, username, password_encrypted, sovereignty, last_sync, created_at, updated_at
		FROM federation_peers ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list federation peers: %w", err)
	}
	defer rows.Close()

	var peers []*storage.FederationPeer
	for rows.Next() {
		var peer storage.FederationPeer
		var encryptedPwd []byte
		var lastSync sql.NullTime
		var username sql.NullString

		if err := rows.Scan(&peer.Name, &peer.RemoteURL, &username, &encryptedPwd, &peer.Sovereignty, &lastSync, &peer.CreatedAt, &peer.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan federation peer: %w", err)
		}

		if username.Valid {
			peer.Username = username.String
		}
		if lastSync.Valid {
			peer.LastSync = &lastSync.Time
		}

		// Decrypt password
		if len(encryptedPwd) > 0 {
			peer.Password, err = s.decryptPassword(encryptedPwd)
			if err != nil {
				return nil, fmt.Errorf("failed to decrypt password: %w", err)
			}
		}

		peers = append(peers, &peer)
	}

	return peers, rows.Err()
}

// RemoveFederationPeer removes a federation peer and its credentials.
func (s *DoltStore) RemoveFederationPeer(ctx context.Context, name string) error {
	result, err := s.execContext(ctx, "DELETE FROM federation_peers WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("failed to remove federation peer: %w", err)
	}

	rows, _ := result.RowsAffected() // Best effort: rows affected is used only for logging
	if rows == 0 {
		// Peer not in credentials table, but might still be a Dolt remote
		// Continue to try removing the remote
	}

	// Also remove the Dolt remote (best-effort)
	_ = s.RemoveRemote(ctx, name) // Best effort cleanup before re-adding remote

	return nil
}

// updatePeerLastSync updates the last sync time for a peer.
func (s *DoltStore) updatePeerLastSync(ctx context.Context, name string) error {
	_, err := s.execContext(ctx, "UPDATE federation_peers SET last_sync = CURRENT_TIMESTAMP WHERE name = ?", name)
	return wrapExecError("update peer last sync", err)
}

// remoteCredentials holds authentication credentials for a Dolt remote.
// Used to pass credentials to CLI subprocesses via cmd.Env (isolated) or to
// the SQL path via process env vars under mutex protection.
type remoteCredentials struct {
	Username string
	Password string
}

// empty returns true if no credentials are set.
func (c *remoteCredentials) empty() bool {
	return c == nil || (c.Username == "" && c.Password == "")
}

// applyToCmd sets DOLT_REMOTE_USER/PASSWORD on the subprocess environment,
// isolating credentials to this specific exec.Cmd. This avoids setting
// process-wide env vars that could leak to concurrent goroutines.
func (c *remoteCredentials) applyToCmd(cmd *exec.Cmd) {
	if c.empty() {
		return
	}
	// Start with current process env, filtering out any existing credential vars
	// to prevent stale values from leaking into the subprocess.
	env := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "DOLT_REMOTE_USER=") && !strings.HasPrefix(e, "DOLT_REMOTE_PASSWORD=") {
			env = append(env, e)
		}
	}
	if c.Username != "" {
		env = append(env, "DOLT_REMOTE_USER="+c.Username)
	}
	if c.Password != "" {
		env = append(env, "DOLT_REMOTE_PASSWORD="+c.Password)
	}
	cmd.Env = env
}

// setFederationCredentials sets DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD env vars.
// Returns a cleanup function that must be called (typically via defer) to unset them.
// The caller must hold federationEnvMutex.
// Only used for SQL-path operations where the in-process Dolt server reads from
// the process environment. CLI operations should use remoteCredentials.applyToCmd instead.
func setFederationCredentials(username, password string) func() {
	if username != "" {
		_ = os.Setenv("DOLT_REMOTE_USER", username) // Best effort: Setenv failure is extremely rare in practice
	}
	if password != "" {
		_ = os.Setenv("DOLT_REMOTE_PASSWORD", password) // Best effort: Setenv failure is extremely rare in practice
	}
	return func() {
		_ = os.Unsetenv("DOLT_REMOTE_USER")     // Best effort cleanup of auth env vars
		_ = os.Unsetenv("DOLT_REMOTE_PASSWORD") // Best effort cleanup of auth env vars
	}
}

// withEnvCredentials executes fn with credentials set as process-wide env vars,
// protected by federationEnvMutex. This is required for SQL-path operations
// (CALL DOLT_PUSH/PULL) where the in-process Dolt server reads credentials
// from the process environment. CLI operations should NOT use this — use
// remoteCredentials.applyToCmd instead for race-free subprocess isolation.
func withEnvCredentials(creds *remoteCredentials, fn func() error) error {
	if creds.empty() {
		return fn()
	}
	federationEnvMutex.Lock()
	defer federationEnvMutex.Unlock()
	cleanup := setFederationCredentials(creds.Username, creds.Password)
	defer cleanup()
	return fn()
}

// withPeerCredentials looks up credentials for a federation peer and passes
// them to fn. The callback receives the credentials and is responsible for
// applying them appropriately: CLI operations use creds.applyToCmd for
// subprocess isolation; SQL operations use withEnvCredentials for mutex-protected
// process env access.
func (s *DoltStore) withPeerCredentials(ctx context.Context, peerName string, fn func(creds *remoteCredentials) error) error {
	peer, err := s.GetFederationPeer(ctx, peerName)
	if err != nil {
		return fmt.Errorf("failed to get peer credentials: %w", err)
	}

	var creds *remoteCredentials
	if peer != nil && (peer.Username != "" || peer.Password != "") {
		creds = &remoteCredentials{Username: peer.Username, Password: peer.Password}
	}

	err = fn(creds)

	// Update last sync time on success
	if err == nil && peer != nil {
		_ = s.updatePeerLastSync(ctx, peerName) // Best effort: peer sync timestamp is advisory
	}

	return err
}

// FederationPeer is an alias for storage.FederationPeer for convenience.
type FederationPeer = storage.FederationPeer
