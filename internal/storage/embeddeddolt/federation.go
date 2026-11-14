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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/remedy"
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
		return "", storage.CredentialKeyMismatchError(filepath.Join(s.beadsDir, credentialKeyFile), fmt.Errorf("ciphertext too short"))
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
		return "", storage.CredentialKeyMismatchError(filepath.Join(s.beadsDir, credentialKeyFile), err)
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

	warnStoredPeerSuppressesAmbientPassword(peer, p)

	restoreUser := overrideEnv("DOLT_REMOTE_USER", p.Username)
	restorePassword := overrideEnv("DOLT_REMOTE_PASSWORD", p.Password)
	defer func() {
		restorePassword()
		restoreUser()
	}()

	return fn(p.Username)
}

// federationWarnWriter receives the peer-credential diagnostics. Defaults to
// os.Stderr, matching the unconditional warnings this package writes from
// store.go; overridable in tests.
var federationWarnWriter io.Writer = os.Stderr

// warnStoredPeerSuppressesAmbientPassword emits one line when a peer stores a
// username but no password while DOLT_REMOTE_PASSWORD is set. Suppressing the
// ambient password is the intended security default, but a setup that used to
// authenticate off the environment then fails with no hint about why (GH#5085
// review). Call with federationEnvMutex held and before overrideEnv rewrites
// the pair, so the value read is the ambient one and not another peer's
// installed password.
func warnStoredPeerSuppressesAmbientPassword(peer string, p *storage.FederationPeer) {
	// Defensive: withPeerAuth returns before this guard when both fields are empty.
	if p.Username == "" || p.Password != "" {
		return
	}
	// An ambient empty value is treated as no ambient password: unsetting it
	// changes nothing the user could be puzzled by.
	if os.Getenv("DOLT_REMOTE_PASSWORD") == "" {
		return
	}
	// add-peer upserts the whole row, so a re-run without --sovereignty
	// clears a stored tier (GH#5213): carry the stored tier into the
	// suggested command. The password is left to add-peer's no-echo prompt
	// rather than suggested as an argument that lands in shell history.
	//
	// The peer name, username and tier are fields of a federation_peers row,
	// which travels with a pull, and this line is written straight to stderr,
	// past bd's error printers. So the prose spells the name with %q, and
	// the command takes each value through CommandArg: a value that is not
	// one shell word becomes a placeholder, named escaped after the command.
	peerArg, peerNote := remedy.CommandArg(runtime.GOOS, peer, "<peer>")
	userArg, userNote := remedy.CommandArg(runtime.GOOS, p.Username, "<user>")
	sovereignty, tierNote := "", ""
	if p.Sovereignty != "" {
		var tierArg string
		tierArg, tierNote = remedy.CommandArg(runtime.GOOS, p.Sovereignty, "<tier>")
		sovereignty = " --sovereignty " + tierArg
	}
	fmt.Fprintf(federationWarnWriter,
		"Warning: peer %q stores a username with an empty password, "+
			"which overrides the ambient DOLT_REMOTE_PASSWORD for this operation; "+
			"store a password by re-running bd federation add-peer %s <url> "+
			"--user %s%s%s and entering it at the prompt.\n",
		peer, peerArg, userArg, sovereignty, peerNote+userNote+tierNote)
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
		// The peer name is a row of the database, which travels with a
		// pull, so it is named and pasted through the remedy printer.
		arg, note := remedy.CommandArg(runtime.GOOS, peer, "<peer>")
		return fmt.Errorf("federation peer %s has stored credentials but no remote named %s exists; re-run bd federation add-peer %s <url> --user <user>%s to restore it", remedy.PrintedValue(peer), remedy.PrintedValue(peer), arg, note)
	}
	if err != nil {
		return fmt.Errorf("resolve remote URL for peer %s: %w", remedy.PrintedValue(peer), err)
	}
	if liveURL != storedURL {
		return fmt.Errorf("remote %s points at %q but federation peer %s stored its credentials for %q; refusing to send stored credentials to a diverged URL. Remove and re-add the peer to rebind it", remedy.PrintedValue(peer), liveURL, remedy.PrintedValue(peer), storedURL)
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
// 2. Merge peer's changes — an explicit strategy via MergeWithStrategy, the
//    pinned-session merge/resolve/repair/commit sequence that can resolve a
//    real conflict (GH#5564, bd-sesknw); the bare Merge's CALL DOLT_MERGE
//    runs on a pool connection and is rejected on a genuine conflict before
//    any strategy could apply
// 3. Push local changes to peer
//
// A strategy merge is concluded (committed, full result, config included)
// before Sync pushes, so the peer receives the resolution.
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

	// Step 3: Merge peer's branch. GH#5564 (bd-sesknw): an explicit strategy
	// must reach MergeWithStrategy — the pinned-session sequence that sets the
	// conflict-tolerance flags, resolves every conflicted table with the
	// operator's strategy, repairs FK cascade violations, and concludes the
	// merge — because the bare Merge's CALL DOLT_MERGE runs on a pool
	// connection under autocommit and is rejected on a real conflict (Error
	// 1105) before any strategy could apply. The resolve/commit tail this
	// function used to run after a bare conflicted merge was therefore dead
	// code on genuine conflicts. MergeWithStrategy also runs the is_blocked
	// recompute itself on success.
	remoteBranch := fmt.Sprintf("%s/%s", peer, s.branch)
	var conflicts []storage.Conflict
	var err error
	if strategy != "" {
		conflicts, err = s.MergeWithStrategy(ctx, remoteBranch, strategy)
	} else {
		conflicts, err = s.Merge(ctx, remoteBranch)
	}
	if err != nil {
		result.Error = fmt.Errorf("merge failed: %w", err)
		return result, result.Error
	}

	// Step 4: Report conflicts. With a strategy they were resolved and the
	// merge concluded inside MergeWithStrategy; without one a genuine
	// conflict stays the operator's to resolve on a retry.
	if len(conflicts) > 0 {
		result.Conflicts = conflicts

		if strategy == "" {
			result.Error = fmt.Errorf("merge conflicts require resolution (use --strategy ours|theirs)")
			return result, result.Error
		}
		result.ConflictsResolved = true
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
