package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/peercred"
)

// PeerCredentialObservationsInTx returns the (ciphertext blob, peer identity)
// pairs of the current federation_peers rows. Blob identity is raw stored bytes:
// the migration planner compares provenance by ciphertext, not by decryption.
func PeerCredentialObservationsInTx(ctx context.Context, tx *sql.Tx) ([]peercred.ObservedBinding, error) {
	rows, err := ListFederationPeersInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]peercred.ObservedBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, peercred.ObservedBinding{
			Blob:    row.EncryptedPwd,
			Binding: peercred.Binding{Name: row.Peer.Name, URL: row.Peer.RemoteURL, Username: row.Peer.Username},
		})
	}
	return out, nil
}

// FederationPeerHistoryObservationsInTx returns the (ciphertext blob, peer
// identity) pairs observed at every commit reachable from the checked-out
// branch's head, including merge parents, for superseded as well as surviving
// federation_peers rows. The one-time credential migration needs this as
// provenance evidence: a legacy blob that ever appeared under a second peer
// identity must not be re-sealed for whichever row holds it now (GH#7890).
//
// Commits that predate the federation_peers table carry no rows: their AS OF
// read fails with the engine's not-found error, which is expected history and
// yields no observations. Any other error fails the walk.
func FederationPeerHistoryObservationsInTx(ctx context.Context, tx *sql.Tx) ([]peercred.ObservedBinding, error) {
	rows, err := tx.QueryContext(ctx, "SELECT commit_hash FROM dolt_log")
	if err != nil {
		return nil, fmt.Errorf("read dolt log: %w", err)
	}
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan dolt log: %w", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate dolt log: %w", err)
	}
	_ = rows.Close()

	var out []peercred.ObservedBinding
	for _, hash := range hashes {
		obs, err := peerCredentialRowsAsOf(ctx, tx, hash)
		if err != nil {
			return nil, err
		}
		out = append(out, obs...)
	}
	return out, nil
}

// peerCredentialRowsAsOf reads the federation_peers rows at one commit. ref
// must be a validated dolt ref; AS OF takes it as a SQL literal, so the
// validation is the only thing standing between the raw string and the query.
func peerCredentialRowsAsOf(ctx context.Context, tx *sql.Tx, ref string) ([]peercred.ObservedBinding, error) {
	if err := ValidateRef(ref); err != nil {
		return nil, fmt.Errorf("history ref: %w", err)
	}
	//nolint:gosec // G201: ref validated by ValidateRef above; AS OF requires a literal
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT name, remote_url, username, password_encrypted
		FROM federation_peers AS OF '%s'
	`, ref))
	if err != nil {
		if isTableNotFoundErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read federation peers as of %s: %w", ref, err)
	}
	defer rows.Close()

	var out []peercred.ObservedBinding
	for rows.Next() {
		var name, url string
		var username sql.NullString
		var blob []byte
		if err := rows.Scan(&name, &url, &username, &blob); err != nil {
			return nil, fmt.Errorf("scan federation peer history at %s: %w", ref, err)
		}
		out = append(out, peercred.ObservedBinding{
			Blob:    blob,
			Binding: peercred.Binding{Name: name, URL: url, Username: username.String},
		})
	}
	return out, rows.Err()
}

// isTableNotFoundErr reports whether err is the engine's "table not found"
// class, raised by an AS OF read at a commit that predates the table.
func isTableNotFoundErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "unknown table")
}

// RebindFederationPeerCredentialInTx replaces one federation_peers row's
// stored ciphertext with newBlob by compare-and-swap: the update matches the
// row's full identity (name, raw remote URL, username with SQL NULL
// normalized) and the exact legacy blob the migration plan was built from. A
// row that changed between the plan and the update does not match, and the
// error fails the migration without touching any other row.
func RebindFederationPeerCredentialInTx(ctx context.Context, tx *sql.Tx, binding peercred.Binding, oldBlob, newBlob []byte) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE federation_peers
		SET password_encrypted = ?, updated_at = CURRENT_TIMESTAMP
		WHERE name = ? AND remote_url = ? AND IFNULL(username, '') = ? AND password_encrypted = ?
	`, newBlob, binding.Name, binding.URL, binding.Username, oldBlob)
	if err != nil {
		return fmt.Errorf("rebind federation peer credential for %s: %w", binding.Name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rebind federation peer credential for %s: %w", binding.Name, err)
	}
	if n != 1 {
		return fmt.Errorf("rebind federation peer credential for %s: row changed since the migration plan was built (%d rows matched)", binding.Name, n)
	}
	return nil
}