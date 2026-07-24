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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
)

// Credential storage and encryption for federation peers.
// Enables SQL user authentication when syncing with peer workspaces.

// credentialKeyFile is the filename for the random encryption key stored alongside the database.
const credentialKeyFile = ".beads-credential-key" //nolint:gosec // G101: not a credential, just a filename

const awsResponseChecksumValidationEnv = "AWS_RESPONSE_CHECKSUM_VALIDATION"

// federationEnvMutex protects process-wide env vars from concurrent access.
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
// The key file is stored in .beads/ (beadsDir), NOT in .beads/dolt/ (dbPath),
// to avoid creating ghost directories in shared-server mode (GH bd-cby).
// Falls back to the old dbPath location for transparent migration.
func (s *DoltStore) initCredentialKey(ctx context.Context) error {
	if s.beadsDir == "" {
		return nil // No filesystem path — credential encryption unavailable
	}

	keyPath := filepath.Join(s.beadsDir, credentialKeyFile)

	// Try to load from new location (.beads/)
	key, err := os.ReadFile(keyPath) //nolint:gosec // G304: keyPath is derived from trusted beadsDir, not user input
	if err == nil && len(key) == 32 {
		s.credentialKey = key
		return nil
	}

	// Migration: try old location (.beads/dolt/) and move to new location
	if s.dbPath != "" {
		oldKeyPath := filepath.Join(s.dbPath, credentialKeyFile)
		oldKey, oldErr := os.ReadFile(oldKeyPath) //nolint:gosec // G304: oldKeyPath is derived from trusted dbPath
		if oldErr == nil && len(oldKey) == 32 {
			// Write to new location, then remove old file
			if writeErr := os.WriteFile(keyPath, oldKey, 0600); writeErr == nil {
				_ = os.Remove(oldKeyPath)
			}
			s.credentialKey = oldKey
			return nil
		}
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

	// Write key file with owner-only permissions (0600).
	// Ensure the directory exists first — when connecting to an external
	// server without having run `bd init`, .beads/ may not exist yet (GH#2641).
	if err := os.MkdirAll(s.beadsDir, 0700); err != nil {
		return fmt.Errorf("failed to create beads directory %s: %w", s.beadsDir, err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return fmt.Errorf("failed to write credential key file: %w", err)
	}

	s.credentialKey = key
	return nil
}

// ensureCredentialKey lazily initializes the credential key when federation
// operations actually need password encryption or decryption.
func (s *DoltStore) ensureCredentialKey(ctx context.Context) error {
	s.mu.RLock()
	if s.credentialKey != nil {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.credentialKey != nil {
		return nil
	}
	return s.initCredentialKey(ctx)
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
	s.mu.RLock()
	key := s.credentialKey
	s.mu.RUnlock()
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
	s.mu.RLock()
	key := s.credentialKey
	s.mu.RUnlock()
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
		if err := s.ensureCredentialKey(ctx); err != nil {
			return fmt.Errorf("failed to initialize credential key: %w", err)
		}
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

	// Also add the Dolt remote.
	if err := s.AddRemote(ctx, peer.Name, peer.RemoteURL); err != nil {
		// Ignore "remote already exists" errors
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("failed to add dolt remote: %w", err)
		}
	}

	if err := s.doltAddAndCommit(ctx, []string{"federation_peers"}, "federation: add peer "+peer.Name); err != nil {
		return fmt.Errorf("failed to commit federation peer: %w", err)
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
		if err := s.ensureCredentialKey(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize credential key: %w", err)
		}
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
			if err := s.ensureCredentialKey(ctx); err != nil {
				return nil, fmt.Errorf("failed to initialize credential key: %w", err)
			}
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
	username string
	password string
}

// empty returns true if no credentials are set.
func (c *remoteCredentials) empty() bool {
	return c == nil || (c.username == "" && c.password == "")
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
	if c.username != "" {
		env = append(env, "DOLT_REMOTE_USER="+c.username)
	}
	if c.password != "" {
		env = append(env, "DOLT_REMOTE_PASSWORD="+c.password)
	}
	cmd.Env = env
}

func setCmdEnv(cmd *exec.Cmd, key, value string) {
	prefix := key + "="
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	for _, e := range base {
		if !strings.HasPrefix(e, prefix) {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, prefix+value)
}

func applyS3ChecksumEnvToCmd(cmd *exec.Cmd) {
	setCmdEnv(cmd, awsResponseChecksumValidationEnv, "when_required")
}

// applyNoGitHooksToCmd disables git client-side hooks (notably pre-push) for
// any git invocation made by the subprocess. bd-internal git ops — in
// particular the `git push --porcelain --force-with-lease=… refs/dolt/data`
// that Dolt runs against its embedded `git-remote-cache/<hash>/repo.git/`
// mirror during `dolt push` — must not run user-installed hooks.
//
// The cache-mirror is a bare-style repo with no work tree, but `git init`
// still honors the user's `init.templateDir` and copies template hooks
// into its `hooks/` dir. When the user's templated `pre-push` hook calls
// `git diff` / `git status` (e.g. via the pre-commit framework's
// staged_files_only setup) it fails with `fatal: this operation must be
// run in a work tree` and bd's push fails with it.
//
// `GIT_CONFIG_PARAMETERS='core.hooksPath=/dev/null'` tells every git
// invocation in the subprocess to look for hooks in `/dev/null` — i.e. to
// skip them. Same intent as the `--no-verify` fix on the commit side
// (GH#3340 / GH#3598 / PR #3626), applied at the push site (GH#3724).
func applyNoGitHooksToCmd(cmd *exec.Cmd) {
	setCmdEnv(cmd, "GIT_CONFIG_PARAMETERS", "'core.hooksPath=/dev/null'")
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

func setS3ChecksumEnv() func() {
	prev, hadPrev := os.LookupEnv(awsResponseChecksumValidationEnv)
	_ = os.Setenv(awsResponseChecksumValidationEnv, "when_required")
	return func() {
		if hadPrev {
			_ = os.Setenv(awsResponseChecksumValidationEnv, prev)
		} else {
			_ = os.Unsetenv(awsResponseChecksumValidationEnv)
		}
	}
}

func withRemoteOperationEnv(creds *remoteCredentials, s3Checksum bool, fn func() error) error {
	if creds.empty() && !s3Checksum {
		return fn()
	}
	federationEnvMutex.Lock()
	defer federationEnvMutex.Unlock()

	var cleanups []func()
	if !creds.empty() {
		cleanups = append(cleanups, setFederationCredentials(creds.username, creds.password))
	}
	if s3Checksum {
		cleanups = append(cleanups, setS3ChecksumEnv())
	}
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	return fn()
}

// withEnvCredentials executes fn with credentials set as process-wide env vars,
// protected by federationEnvMutex. This is required for SQL-path operations
// (CALL DOLT_PUSH/PULL) where the in-process Dolt server reads credentials
// from the process environment. CLI operations should NOT use this — use
// remoteCredentials.applyToCmd instead for race-free subprocess isolation.
func withEnvCredentials(creds *remoteCredentials, fn func() error) error {
	return withRemoteOperationEnv(creds, false, fn)
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
		creds = &remoteCredentials{username: peer.Username, password: peer.Password}
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

func (s *DoltStore) prepareCLIRouteForPeerCredentials(ctx context.Context, peer string, creds *remoteCredentials) (bool, error) {
	if creds.empty() {
		return false, nil // no credentials to pass
	}
	if !s.serverMode {
		return false, nil // embedded mode: withEnvCredentials works in-process
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before credential routing for peer %q: %w", peer, err)
	}
	for _, r := range remotes {
		if r.Name == peer {
			if err := s.ensureMatchingCLIRemote(peer, r.URL); err != nil {
				return false, fmt.Errorf("peer remote %q has credentials and requires CLI routing: %w", peer, err)
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *DoltStore) shouldUseCLIForPeerCredentialsWithError(ctx context.Context, peer string, creds *remoteCredentials) (bool, error) {
	return s.prepareCLIRouteForPeerCredentials(ctx, peer, creds)
}

// shouldUseCLIForCredentials returns true when CLI subprocess routing should
// be used instead of SQL path for credential-bearing push/pull operations.
//
// When true, callers should route through doltCLIPush/Pull instead of
// CALL DOLT_PUSH/PULL, because withEnvCredentials() sets env vars on the
// bd client process — the external server process cannot see them.
//
// Returns true when ALL conditions are met:
//  1. Credentials exist (remoteUser or remotePassword non-empty)
//  2. Server is in server mode (not embedded)
//  3. The remote exists as a SQL-visible remote
//  4. The same remote URL can be materialized in the local CLI directory
func (s *DoltStore) shouldUseCLIForCredentials(ctx context.Context, remote string, creds *remoteCredentials) bool {
	ok, err := s.prepareCLIRouteForCredentials(ctx, remote, creds)
	if err != nil {
		log.Printf("warning: %v", err)
		return false
	}
	return ok
}

func (s *DoltStore) prepareCLIRouteForCredentials(ctx context.Context, remote string, creds *remoteCredentials) (bool, error) {
	if creds.empty() {
		return false, nil // no credentials to pass
	}
	if !s.serverMode {
		return false, nil // embedded mode: withEnvCredentials works in-process
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before credential routing for remote %q: %w", remote, err)
	}
	for _, r := range remotes {
		if r.Name == remote {
			if err := s.ensureMatchingCLIRemote(remote, r.URL); err != nil {
				return false, fmt.Errorf("remote %q has credentials and requires CLI routing: %w", remote, err)
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *DoltStore) shouldUseCLIForCredentialsWithError(ctx context.Context, remote string, creds *remoteCredentials) (bool, error) {
	return s.prepareCLIRouteForCredentials(ctx, remote, creds)
}

func (s *DoltStore) shouldUseCLIForLocalRemoteWithError(ctx context.Context, remote string) (bool, error) {
	if !s.serverMode {
		return false, nil
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	sqlRemotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before local CLI routing for remote %q: %w", remote, err)
	}
	for _, r := range sqlRemotes {
		if r.Name == remote {
			return s.hasMatchingCLIRemote(remote, r.URL), nil
		}
	}
	return false, nil
}

// cloudAuthSchemeMap maps remote URL scheme prefixes to the environment
// variable prefixes that provide credentials for that scheme. Only env vars
// relevant to the remote's scheme are checked, preventing misrouting when
// multiple remotes use different cloud providers (e.g., DoltHub + Azure).
//
// The CLI subprocess inherits the current process env, so these env vars
// reach the dolt binary. The SQL server process may not have them if it was
// started in a different context (GH#6).
var cloudAuthSchemeMap = map[string][]string{
	"az://":      {"AZURE_STORAGE_"},  // Azure Blob Storage
	"aws://":     {"AWS_"},            // Dolt AWS remotes (S3 + DynamoDB)
	"s3://":      {"AWS_"},            // AWS S3
	"gs://":      {"GOOGLE_", "GCS_"}, // Google Cloud Storage
	"oci://":     {"OCI_"},            // Oracle Cloud Infrastructure
	"dolthub://": {"DOLT_REMOTE_"},    // DoltHub
	"https://":   {"DOLT_REMOTE_"},    // Hosted Dolt / DoltHub HTTPS
	"http://":    {"DOLT_REMOTE_"},    // Hosted Dolt HTTP
}

func isS3RemoteURL(url string) bool {
	return strings.HasPrefix(url, "aws://") || strings.HasPrefix(url, "s3://")
}

func (s *DoltStore) isS3Remote(ctx context.Context, remote string) bool {
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false
	}
	for _, r := range remotes {
		if r.Name == remote {
			return isS3RemoteURL(r.URL)
		}
	}
	return false
}

// envPrefixesForRemoteURL returns the env var prefixes relevant to the
// given remote URL based on its scheme. Returns nil for unrecognized schemes
// (git-protocol remotes are handled by isGitProtocolRemote, not here).
func envPrefixesForRemoteURL(url string) []string {
	for scheme, prefixes := range cloudAuthSchemeMap {
		if strings.HasPrefix(url, scheme) {
			return prefixes
		}
	}
	return nil
}

// shouldUseCLIForCloudAuth returns true when CLI subprocess routing should
// be used for push/pull because cloud storage credentials relevant to this
// specific remote are present in the environment and the store is using an
// external dolt-sql-server.
//
// Unlike a global heuristic, this checks only the env var prefixes that
// match the remote's URL scheme. An Azure env var (AZURE_STORAGE_ACCOUNT)
// will trigger CLI routing for an az:// remote but NOT for a dolthub:// remote.
//
// Scheme detection uses the SQL remote URL. If matching cloud credentials are
// present, bd materializes the same remote into the local CLI directory before
// routing to the CLI subprocess.
//
// When bd connects to an external dolt-sql-server (server mode), CALL
// DOLT_PUSH/PULL executes inside the server process. That process only has
// the env vars it inherited at startup. If cloud credentials were set (or
// changed) after the server started, the SQL path silently fails to
// authenticate. Routing through a CLI subprocess (dolt push/pull) ensures
// the child process inherits the current environment (GH#6).
func (s *DoltStore) shouldUseCLIForCloudAuth(ctx context.Context, remote string) bool {
	ok, err := s.prepareCLIRouteForCloudAuth(ctx, remote)
	if err != nil {
		log.Printf("warning: %v", err)
		return false
	}
	return ok
}

func (s *DoltStore) prepareCLIRouteForCloudAuth(ctx context.Context, remote string) (bool, error) {
	if !s.serverMode {
		return false, nil // embedded mode: env vars are in-process
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before cloud-auth routing for remote %q: %w", remote, err)
	}
	var remoteURL string
	for _, r := range remotes {
		if r.Name == remote {
			remoteURL = r.URL
			break
		}
	}
	if remoteURL == "" {
		return false, nil
	}
	prefixes := envPrefixesForRemoteURL(remoteURL)
	if len(prefixes) == 0 {
		return false, nil // unknown scheme — not a cloud remote
	}
	for _, e := range os.Environ() {
		for _, prefix := range prefixes {
			if strings.HasPrefix(e, prefix) {
				if err := s.ensureMatchingCLIRemote(remote, remoteURL); err != nil {
					return false, fmt.Errorf("remote %q has cloud credentials and requires CLI routing: %w", remote, err)
				}
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *DoltStore) shouldUseCLIForCloudAuthWithError(ctx context.Context, remote string) (bool, error) {
	return s.prepareCLIRouteForCloudAuth(ctx, remote)
}
