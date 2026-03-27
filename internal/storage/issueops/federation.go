package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// validPeerNameRegex matches valid peer names (alphanumeric, hyphens, underscores).
var validPeerNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// ValidatePeerName checks that a peer name is safe for use as a Dolt remote name.
func ValidatePeerName(name string) error {
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

// AddFederationPeerInTx upserts a federation peer record. The encryptedPwd
// should already be encrypted by the caller; pass nil for no password.
func AddFederationPeerInTx(ctx context.Context, tx *sql.Tx, peer *storage.FederationPeer, encryptedPwd []byte) error {
	if err := ValidatePeerName(peer.Name); err != nil {
		return fmt.Errorf("invalid peer name: %w", err)
	}

	_, err := tx.ExecContext(ctx, `
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
		return fmt.Errorf("add federation peer: %w", err)
	}
	return nil
}

// FederationPeerRow holds raw database fields for a federation peer.
// The caller is responsible for decrypting EncryptedPwd.
type FederationPeerRow struct {
	Peer         storage.FederationPeer
	EncryptedPwd []byte
}

// GetFederationPeerInTx retrieves a federation peer by name.
// Returns storage.ErrNotFound (wrapped) if the peer does not exist.
func GetFederationPeerInTx(ctx context.Context, tx *sql.Tx, name string) (*FederationPeerRow, error) {
	var row FederationPeerRow
	var lastSync sql.NullTime
	var username sql.NullString

	err := tx.QueryRowContext(ctx, `
		SELECT name, remote_url, username, password_encrypted, sovereignty, last_sync, created_at, updated_at
		FROM federation_peers WHERE name = ?
	`, name).Scan(
		&row.Peer.Name, &row.Peer.RemoteURL, &username, &row.EncryptedPwd,
		&row.Peer.Sovereignty, &lastSync, &row.Peer.CreatedAt, &row.Peer.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: federation peer %s", storage.ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get federation peer: %w", err)
	}

	if username.Valid {
		row.Peer.Username = username.String
	}
	if lastSync.Valid {
		row.Peer.LastSync = &lastSync.Time
	}

	return &row, nil
}

// ListFederationPeersInTx returns all configured federation peer rows.
func ListFederationPeersInTx(ctx context.Context, tx *sql.Tx) ([]*FederationPeerRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, remote_url, username, password_encrypted, sovereignty, last_sync, created_at, updated_at
		FROM federation_peers ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list federation peers: %w", err)
	}
	defer rows.Close()

	var peers []*FederationPeerRow
	for rows.Next() {
		var row FederationPeerRow
		var lastSync sql.NullTime
		var username sql.NullString

		if err := rows.Scan(
			&row.Peer.Name, &row.Peer.RemoteURL, &username, &row.EncryptedPwd,
			&row.Peer.Sovereignty, &lastSync, &row.Peer.CreatedAt, &row.Peer.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan federation peer: %w", err)
		}

		if username.Valid {
			row.Peer.Username = username.String
		}
		if lastSync.Valid {
			row.Peer.LastSync = &lastSync.Time
		}

		peers = append(peers, &row)
	}
	return peers, rows.Err()
}

// RemoveFederationPeerInTx deletes a federation peer by name.
func RemoveFederationPeerInTx(ctx context.Context, tx *sql.Tx, name string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM federation_peers WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("remove federation peer: %w", err)
	}
	return nil
}

// AddRemoteIfNotExists adds a Dolt remote, ignoring "already exists" errors.
// This is a helper used when adding federation peers that also need a Dolt remote.
func AddRemoteIfNotExists(ctx context.Context, tx *sql.Tx, name, url string) error {
	_, err := tx.ExecContext(ctx, "CALL DOLT_REMOTE('add', ?, ?)", name, url)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("add remote %s: %w", name, err)
	}
	return nil
}
