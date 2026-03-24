//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
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
			return nil, fmt.Errorf("decrypt password: %w", err)
		}
	}
	return &row.Peer, nil
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

		if err := s.Commit(ctx, fmt.Sprintf("Resolve conflicts from %s using %s strategy", peer, strategy)); err != nil {
			result.Error = fmt.Errorf("commit conflict resolution: %w", err)
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
