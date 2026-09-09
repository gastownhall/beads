//go:build cgo

package embeddeddolt

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// credentialKeyFile is the filename for the random encryption key.
const credentialKeyFile = ".beads-credential-key" //nolint:gosec // G101: filename, not a credential

// ensureCredentialKey lazily initializes the credential encryption key.
func (s *EmbeddedDoltStore) ensureCredentialKey() error {
	if s.credentialKey != nil {
		return nil
	}
	if s.beadsDir == "" {
		return fmt.Errorf("beads directory not set; credential encryption unavailable")
	}

	keyPath := filepath.Join(s.beadsDir, credentialKeyFile)

	// Try to load existing key.
	key, err := os.ReadFile(keyPath) //nolint:gosec // G304: keyPath derived from trusted beadsDir
	if err == nil && len(key) == 32 {
		s.credentialKey = key
		return nil
	}

	// Generate new random 32-byte key (AES-256).
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("generate credential key: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return fmt.Errorf("write credential key: %w", err)
	}

	s.credentialKey = key
	return nil
}

func (s *EmbeddedDoltStore) encryptPassword(password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	if err := s.ensureCredentialKey(); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.credentialKey)
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
	return gcm.Seal(nonce, nonce, []byte(password), nil), nil
}

func (s *EmbeddedDoltStore) decryptPassword(encrypted []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", nil
	}
	if err := s.ensureCredentialKey(); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.credentialKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		// A blob too short to carry its nonce is the same local-corruption
		// class as the failed GCM open below: the stored credential cannot be
		// decrypted on this machine, and re-adding the peer is the fix.
		// Classify it through the same sentinel so federation status reports
		// the credential problem rather than an unreachable peer.
		return "", storage.CredentialKeyMismatchError(credentialKeyFile, fmt.Errorf("ciphertext too short"))
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// The common shape: the peer row replicated with the database but the key
		// file did not, so machine B pulls the row, ensureCredentialKey generates
		// a fresh key, and every decrypt fails authentication. Enrich at the
		// single decrypt funnel so every reader reports it: peer resolution for
		// the remote verbs, GetFederationPeer, ListFederationPeers. The wording
		// is shared with package dolt, which enriches its own two read paths.
		return "", storage.CredentialKeyMismatchError(credentialKeyFile, err)
	}
	return string(plaintext), nil
}

// ---------------------------------------------------------------------------
// FederationStore implementation
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) AddFederationPeer(ctx context.Context, peer *storage.FederationPeer) error {
	encryptedPwd, err := s.encryptPassword(peer.Password)
	if err != nil {
		return fmt.Errorf("encrypt password: %w", err)
	}

	if err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		if err := issueops.AddFederationPeerInTx(ctx, tx, peer, encryptedPwd); err != nil {
			return err
		}
		// Also add the Dolt remote.
		return issueops.AddRemoteIfNotExists(ctx, tx, peer.Name, peer.RemoteURL)
	}); err != nil {
		return err
	}
	return nil
}

func (s *EmbeddedDoltStore) GetFederationPeer(ctx context.Context, name string) (*storage.FederationPeer, error) {
	var row *issueops.FederationPeerRow
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		row, err = issueops.GetFederationPeerInTx(ctx, tx, name)
		return err
	})
	if err != nil {
		return nil, err
	}

	if len(row.EncryptedPwd) > 0 {
		row.Peer.Password, err = s.decryptPassword(row.EncryptedPwd)
		if err != nil {
			return nil, fmt.Errorf("decrypt password for peer %s: %w", name, err)
		}
	}
	return &row.Peer, nil
}

// federationEnvMutex serializes mutation of the process-wide
// DOLT_REMOTE_USER/DOLT_REMOTE_PASSWORD pair: the in-process Dolt engine
// reads them from the process environment, so concurrent peer operations
// would otherwise observe each other's credentials. This is a separate
// lock from package dolt's federationEnvMutex, not the same lock; the two
// stores never run remote operations concurrently, so serializing within
// each package suffices.
var federationEnvMutex sync.Mutex

// withPeerAuth runs fn with the remote-auth username for peer. Credentials
// stored by add-peer win and override the environment pair as a unit, so an
// ambient DOLT_REMOTE_PASSWORD never mixes with a stored username (or vice
// versa); remotes without a stored peer keep the environment fallback.
//
// Every callback path holds federationEnvMutex, including the environment
// fallbacks: the in-process Dolt engine reads the pair from the process
// environment, so an unserialized plain-remote operation could observe
// another peer operation's temporarily installed credentials.
//
// Stored credentials are bound to the peer's canonical remote URL, not only
// to its name (see verifyPeerRemoteURL).
func (s *EmbeddedDoltStore) withPeerAuth(ctx context.Context, peer string, fn func(user string) error) error {
	p, err := s.GetFederationPeer(ctx, peer)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("resolve peer credentials: %w", err)
	}
	if err != nil || (p.Username == "" && p.Password == "") {
		federationEnvMutex.Lock()
		defer federationEnvMutex.Unlock()
		return fn(remoteAuthUser())
	}

	if err := s.verifyPeerRemoteURL(ctx, peer, p.RemoteURL); err != nil {
		return err
	}

	federationEnvMutex.Lock()
	defer federationEnvMutex.Unlock()

	restoreUser := overrideEnv("DOLT_REMOTE_USER", p.Username)
	restorePassword := overrideEnv("DOLT_REMOTE_PASSWORD", p.Password)
	defer func() {
		restorePassword()
		restoreUser()
	}()

	return fn(p.Username)
}

// verifyPeerRemoteURL fails closed when the live remote named peer does not
// carry the URL stored on the federation peer row. AddRemoteIfNotExists
// preserves an existing same-name remote regardless of its URL, so the name
// alone does not prove the destination; installing the stored password for a
// diverged URL would disclose it to an unrelated host. A missing remote fails
// the same way: there is no verified destination to authenticate against.
// Runs before any credential is installed, outside federationEnvMutex.
func (s *EmbeddedDoltStore) verifyPeerRemoteURL(ctx context.Context, peer, storedURL string) error {
	var liveURL string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT url FROM dolt_remotes WHERE name = ?", peer).Scan(&liveURL)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("federation peer %s has stored credentials but no remote named %s exists; re-run 'bd federation add-peer %s <url> --user <user>' to restore it", peer, peer, peer)
	}
	if err != nil {
		return fmt.Errorf("resolve remote URL for peer %s: %w", peer, err)
	}
	if liveURL != storedURL {
		return fmt.Errorf("remote %s points at %q but federation peer %s stored its credentials for %q; refusing to send stored credentials to a diverged URL. Remove and re-add the peer to rebind it", peer, liveURL, peer, storedURL)
	}
	return nil
}

// overrideEnv sets key to value (unsetting it when value is empty, so an
// ambient value cannot leak into an operation that stored an empty field)
// and returns a function restoring the prior state.
func overrideEnv(key, value string) func() {
	prev, had := os.LookupEnv(key)
	if value == "" {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, value)
	}
	return func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

func (s *EmbeddedDoltStore) ListFederationPeers(ctx context.Context) ([]*storage.FederationPeer, error) {
	var rows []*issueops.FederationPeerRow
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		rows, err = issueops.ListFederationPeersInTx(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}

	peers := make([]*storage.FederationPeer, 0, len(rows))
	for _, row := range rows {
		if len(row.EncryptedPwd) > 0 {
			pwd, err := s.decryptPassword(row.EncryptedPwd)
			if err != nil {
				return nil, fmt.Errorf("decrypt password for peer %s: %w", row.Peer.Name, err)
			}
			row.Peer.Password = pwd
		}
		peers = append(peers, &row.Peer)
	}
	return peers, nil
}

func (s *EmbeddedDoltStore) RemoveFederationPeer(ctx context.Context, name string) error {
	if err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.RemoveFederationPeerInTx(ctx, tx, name)
	}); err != nil {
		return err
	}

	// Also remove the Dolt remote (best-effort).
	if rmErr := s.RemoveRemote(ctx, name); rmErr != nil {
		if !strings.Contains(rmErr.Error(), "not found") {
			// Silently ignore "not found" — the remote may not exist.
			_ = rmErr
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SyncStore implementation
// ---------------------------------------------------------------------------

// Sync performs a full bidirectional sync with a peer:
// 1. Fetch from peer
// 2. Merge peer's changes (handling conflicts per strategy)
// 3. Push local changes to peer
func (s *EmbeddedDoltStore) Sync(ctx context.Context, peer string, strategy string) (*storage.SyncResult, error) {
	result := &storage.SyncResult{
		Peer:      peer,
		StartTime: time.Now(),
	}

	// GH#2474 / bd-578h9.2: commit pending changes before the merge, matching
	// embedded Pull/PullRemote/PullFrom and server-mode Sync. Embedded Commit is
	// DOLT_COMMIT('-Am'), so it stages config — where kv.memory.* memories live —
	// and a leftover dirty working set (e.g. a `bd remember` write) would
	// otherwise make DOLT_MERGE refuse to start ("cannot merge with uncommitted
	// changes"). CommitPending is a no-op when the working set is already clean.
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		result.Error = fmt.Errorf("commit pending before sync: %w", err)
		return result, result.Error
	}

	// Step 1: Fetch
	if err := s.Fetch(ctx, peer); err != nil {
		result.Error = fmt.Errorf("fetch failed: %w", err)
		return result, result.Error
	}
	result.Fetched = true

	// Step 2: Get commit before merge for change detection
	beforeCommit, _ := s.GetCurrentCommit(ctx)

	// Step 3: Merge peer's branch
	remoteBranch := fmt.Sprintf("%s/%s", peer, s.branch)
	conflicts, err := s.Merge(ctx, remoteBranch)
	if err != nil {
		result.Error = fmt.Errorf("merge failed: %w", err)
		return result, result.Error
	}

	// Step 4: Handle conflicts
	if len(conflicts) > 0 {
		result.Conflicts = conflicts

		if strategy == "" {
			result.Error = fmt.Errorf("merge conflicts require resolution (use --strategy ours|theirs)")
			return result, result.Error
		}

		for _, c := range conflicts {
			if err := s.ResolveConflicts(ctx, c.Field, strategy); err != nil {
				result.Error = fmt.Errorf("conflict resolution failed for %s: %w", c.Field, err)
				return result, result.Error
			}
		}
		result.ConflictsResolved = true

		// CommitMergeResolution, not Commit: Commit's GH#3886 nothing-to-commit
		// tolerance would swallow the --ours case (resolution dirties nothing)
		// as a silent no-op here, leaving dolt_merge_status.is_merging true while
		// this function reports result.Merged = true and pushes — the exact
		// re-wedge CommitMergeResolution's doc comment describes. See the
		// server-mode twin, dolt/federation.go's Sync.
		if err := s.CommitMergeResolution(ctx, fmt.Sprintf("Resolve conflicts from %s using %s strategy", peer, strategy)); err != nil {
			result.Error = fmt.Errorf("commit conflict resolution: %w", err)
			return result, result.Error
		}

		// bd-578h9.11: the conflicted merge skipped the automatic is_blocked
		// recompute (unresolved rows would have fed it garbage); now that the
		// resolution is committed, cover the whole merge+resolution window.
		if err := s.RecomputeBlockedAfterMerge(ctx, beforeCommit); err != nil {
			result.Error = fmt.Errorf("conflicts resolved but is_blocked recompute failed: %w", err)
			return result, result.Error
		}
	}
	result.Merged = true

	afterCommit, _ := s.GetCurrentCommit(ctx)
	if beforeCommit != afterCommit {
		result.PulledCommits = 1
	}

	// Step 5: Push
	if err := s.PushTo(ctx, peer); err != nil {
		result.PushError = err
	} else {
		result.Pushed = true
	}

	// Record last sync time in metadata.
	_ = s.setLastSyncTime(ctx, peer)

	result.EndTime = time.Now()
	return result, nil
}

// SyncStatus returns the synchronization status with a peer.
func (s *EmbeddedDoltStore) SyncStatus(ctx context.Context, peer string) (*storage.SyncStatus, error) {
	status := &storage.SyncStatus{
		Peer: peer,
	}

	// Get ahead/behind counts by comparing refs.
	// Dolt's AS OF requires a literal ref, not a parameterized expression.
	remoteRef := peer + "/" + s.branch
	if err := issueops.ValidateRef(remoteRef); err != nil {
		status.LocalAhead = -1
		status.LocalBehind = -1
	} else if err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		query := fmt.Sprintf(`
			SELECT
				(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
					(SELECT commit_hash FROM dolt_log AS OF '%s')) as ahead,
				(SELECT COUNT(*) FROM dolt_log AS OF '%s' WHERE commit_hash NOT IN
					(SELECT commit_hash FROM dolt_log)) as behind
		`, remoteRef, remoteRef)
		if err := db.QueryRowContext(ctx, query).
			Scan(&status.LocalAhead, &status.LocalBehind); err != nil {
			// Remote branch may not exist locally yet.
			status.LocalAhead = -1
			status.LocalBehind = -1
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Check for conflicts.
	conflicts, err := s.GetConflicts(ctx)
	if err == nil && len(conflicts) > 0 {
		status.HasConflicts = true
	}

	// Get last sync time.
	status.LastSync = s.getLastSyncTime(ctx, peer)

	return status, nil
}

// setLastSyncTime records the last sync time for a peer in metadata.
func (s *EmbeddedDoltStore) setLastSyncTime(ctx context.Context, peer string) error {
	key := "last_sync_" + peer
	value := time.Now().Format(time.RFC3339)
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO metadata (`key`, value) VALUES (?, ?)", key, value)
		return err
	})
}

// getLastSyncTime retrieves the last sync time for a peer from metadata.
func (s *EmbeddedDoltStore) getLastSyncTime(ctx context.Context, peer string) time.Time {
	key := "last_sync_" + peer
	var value string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	})
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
