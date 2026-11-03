package peercred

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
)

// OpenLegacy decrypts the pre-v2 unbound format (nonce || AES-GCM, no
// associated data). It exists only for migration: an unbound blob may have
// been copied from another peer row, so its plaintext must never be used for
// a remote operation without the provenance check Plan performs.
func OpenLegacy(key, blob []byte) (string, error) {
	if len(key) != KeySize {
		return "", fmt.Errorf("credential key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// Row is a stored federation_peers credential: its binding, its sovereignty
// (carried only into re-add advice) and its ciphertext.
type Row struct {
	Binding     Binding
	Sovereignty string
	Blob        []byte
}

// Candidate is a current row whose blob authenticated in the legacy format
// under one of this machine's keys.
type Candidate struct {
	Row       Row
	Plaintext string
}

// Occurrence is one binding a ciphertext was stored under, in the current
// table or in any committed version reachable from the checked-out branch.
type Occurrence struct {
	Binding Binding
	Blob    []byte
}

// Plan splits legacy candidates into the ones safe to rebind to their current
// row and the ones that are not. A legacy blob is unbound, so it can be
// rebound only when every place the same ciphertext was ever stored carries
// the current row's binding. A blob seen under any other binding was copied
// or moved between peer rows (perhaps with its original deleted or
// overwritten since), and binding it to whichever row holds it now could
// hand one peer's password to another peer's URL.
func Plan(candidates []Candidate, occurrences []Occurrence) (eligible, skipped []Candidate) {
	for _, c := range candidates {
		ok := true
		for _, o := range occurrences {
			if bytes.Equal(o.Blob, c.Row.Blob) && o.Binding != c.Row.Binding {
				ok = false
				break
			}
		}
		if ok {
			eligible = append(eligible, c)
		} else {
			skipped = append(skipped, c)
		}
	}
	return eligible, skipped
}

// Conn is the slice of a SQL connection Migrate needs. *sql.DB, *sql.Conn and
// versioncontrolops.DBConn all satisfy it.
type Conn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MigrationResult reports what Migrate changed and what it refused.
type MigrationResult struct {
	// Migrated names the peers whose rows were rewritten as bound v2.
	Migrated []string
	// Skipped holds the legacy rows left unusable because their ciphertext
	// also appears under another binding.
	Skipped []Row
}

const currentRowsQuery = `
	SELECT name, remote_url, username, password_encrypted, sovereignty
	FROM federation_peers
	WHERE password_encrypted IS NOT NULL AND LENGTH(password_encrypted) > 0`

// historyRowsQuery lists every binding each ciphertext was committed under on
// the checked-out branch. dolt_history_<table> walks every commit reachable
// from HEAD, merge parents included, so a blob that was copied and then
// deleted or overwritten still shows its other binding here.
const historyRowsQuery = `
	SELECT DISTINCT name, remote_url, username, password_encrypted
	FROM dolt_history_federation_peers
	WHERE password_encrypted IS NOT NULL AND LENGTH(password_encrypted) > 0`

// Migrate rewrites this machine's legacy federation_peers credentials as v2
// sealed under key and bound to their current row, when Plan finds their
// provenance unambiguous. legacyKeys are the keys a legacy blob may have
// been sealed under (the current key first). Rows already in v2, and rows
// no local key opens, are left untouched. Each rewrite is a compare-and-swap
// on the observed row, so a row changed concurrently fails the migration
// rather than being overwritten. The caller commits federation_peers and,
// only after that, writes the marker.
func Migrate(ctx context.Context, conn Conn, key []byte, legacyKeys [][]byte) (MigrationResult, error) {
	var res MigrationResult
	current, err := scanRows(ctx, conn, currentRowsQuery, true)
	if err != nil {
		return res, fmt.Errorf("read federation peer credentials: %w", err)
	}

	var candidates []Candidate
	for _, r := range current {
		if _, err := Open(key, r.Binding, r.Blob); err == nil {
			continue
		}
		for _, k := range legacyKeys {
			if plain, err := OpenLegacy(k, r.Blob); err == nil {
				candidates = append(candidates, Candidate{Row: r, Plaintext: plain})
				break
			}
		}
	}
	if len(candidates) == 0 {
		return res, nil
	}

	history, err := scanRows(ctx, conn, historyRowsQuery, false)
	if err != nil {
		return res, fmt.Errorf("read federation peer credential history: %w", err)
	}
	occurrences := make([]Occurrence, 0, len(current)+len(history))
	for _, rs := range [][]Row{current, history} {
		for _, r := range rs {
			occurrences = append(occurrences, Occurrence{Binding: r.Binding, Blob: r.Blob})
		}
	}

	eligible, skipped := Plan(candidates, occurrences)
	for _, c := range skipped {
		res.Skipped = append(res.Skipped, c.Row)
	}
	for _, c := range eligible {
		sealed, err := Seal(key, c.Row.Binding, c.Plaintext)
		if err != nil {
			return res, fmt.Errorf("seal credential for peer %s: %w", remedy.PrintedValue(c.Row.Binding.Name), err)
		}
		r, err := conn.ExecContext(ctx, `
			UPDATE federation_peers SET password_encrypted = ?
			WHERE name = ? AND remote_url = ? AND COALESCE(username, '') = ? AND password_encrypted = ?`,
			sealed, c.Row.Binding.Name, c.Row.Binding.URL, c.Row.Binding.Username, c.Row.Blob)
		if err != nil {
			return res, fmt.Errorf("migrate credential for peer %s: %w", remedy.PrintedValue(c.Row.Binding.Name), err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return res, fmt.Errorf("migrate credential for peer %s: %w", remedy.PrintedValue(c.Row.Binding.Name), err)
		}
		if n != 1 {
			return res, fmt.Errorf("migrate credential for peer %s: the row changed during migration; retry the command", remedy.PrintedValue(c.Row.Binding.Name))
		}
		res.Migrated = append(res.Migrated, c.Row.Binding.Name)
	}
	return res, nil
}

func scanRows(ctx context.Context, conn Conn, query string, withSovereignty bool) ([]Row, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var username, sovereignty sql.NullString
		dest := []any{&r.Binding.Name, &r.Binding.URL, &username, &r.Blob}
		if withSovereignty {
			dest = append(dest, &sovereignty)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		r.Binding.Username = username.String
		r.Sovereignty = sovereignty.String
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SkipWarning is the line an operator sees for a legacy credential Migrate
// refused to rebind. Every value comes from a replicated row, so names are
// escaped and the command takes each through remedy.CommandArg. It never
// prints the password, the ciphertext or the URL.
func SkipWarning(goos string, r Row) string {
	peerArg, peerNote := remedy.CommandArg(goos, r.Binding.Name, "<peer>")
	user, userNote := "", ""
	if r.Binding.Username != "" {
		var userArg string
		userArg, userNote = remedy.CommandArg(goos, r.Binding.Username, "<user>")
		user = " --user " + userArg
	}
	sovereignty, tierNote := "", ""
	if r.Sovereignty != "" {
		var tierArg string
		tierArg, tierNote = remedy.CommandArg(goos, r.Sovereignty, "<tier>")
		sovereignty = " --sovereignty " + tierArg
	}
	return fmt.Sprintf("Warning: the stored password for federation peer %s was not upgraded: "+
		"the same encrypted value appears under another peer name, URL or username in this database or its history, "+
		"so it cannot be bound to this peer safely and will not be used. "+
		"Re-add the peer with bd federation add-peer %s <url>%s%s and enter the password at the prompt; "+
		"rotate that password if you did not copy it between peers yourself.%s\n",
		remedy.PrintedValue(r.Binding.Name), peerArg, user, sovereignty, peerNote+userNote+tierNote)
}

// markerDomain separates the marker MAC from every other use of the key.
const markerDomain = "beads federation peer credential v2 migration complete\x00"

// MarkerPath is the completion marker beside the credential key file.
func MarkerPath(keyPath string) string {
	return keyPath + ".v2-migrated"
}

func markerMAC(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(markerDomain))
	return hex.EncodeToString(m.Sum(nil))
}

// MarkerValid reports whether the marker beside keyPath records completed
// migration under key. A marker written under another key is not completion
// evidence for this one.
func MarkerValid(keyPath string, key []byte) (bool, error) {
	data, err := os.ReadFile(MarkerPath(keyPath)) //nolint:gosec // G304: derived from the trusted key path
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read credential migration marker: %w", err)
	}
	return hmac.Equal([]byte(strings.TrimSpace(string(data))), []byte(markerMAC(key))), nil
}

// WriteMarker records completed migration under key, mode 0600, atomically.
// Write it only after the migrated rows are committed.
func WriteMarker(keyPath string, key []byte) error {
	path := MarkerPath(keyPath)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write credential migration marker: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write credential migration marker: %w", err)
	}
	if _, err := tmp.WriteString(markerMAC(key) + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write credential migration marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write credential migration marker: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write credential migration marker: %w", err)
	}
	return nil
}
