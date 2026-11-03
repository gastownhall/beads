package peercred

import (
	"fmt"

	"github.com/steveyegge/beads/internal/remedy"
)

// ObservedBinding is one (ciphertext blob, peer identity) pair seen while
// scanning the federation_peers table — either a current row or a row at some
// commit reachable from the checked-out branch. The planner compares raw blob
// bytes, so observations from any key vintage are usable as provenance.
type ObservedBinding struct {
	Blob    []byte
	Binding Binding
}

// Rebind is one legacy credential the migration may re-seal: its plaintext
// decrypted under a key this machine holds, and provenance evidence that the
// blob has only ever belonged to its current row's identity.
type Rebind struct {
	Binding Binding
	// Password is the decrypted legacy credential, held only in memory between
	// the planner and the re-seal. It never reaches a log, an error, or disk.
	Password string //nolint:gosec // G117: transient plaintext, re-sealed by the caller, never logged
	// OldBlob is the legacy ciphertext the compare-and-swap update must match,
	// so a row changed between the scan and the update is never overwritten.
	OldBlob []byte
}

// MigrationPlan classifies the legacy (non-v2) blobs found in current
// federation_peers rows.
type MigrationPlan struct {
	// Rebind lists eligible legacy credentials to re-seal as v2 under their
	// current row's identity.
	Rebind []Rebind
	// Skipped lists legacy credentials whose blob appears under more than one
	// binding in the current rows or reachable history. They stay encrypted
	// and unusable; each needs a per-peer re-add warning, and none may be
	// rebound.
	Skipped []Binding
	// Unopenable lists legacy rows no supplied key can decrypt (another
	// machine's key, or corruption). They are left untouched and fail later
	// use with the existing key-mismatch classification; they are not the
	// migration's to warn about.
	Unopenable []Binding
}

// PlanMigration classifies every legacy blob in current against the local
// credential key and, when non-nil, an additional legacy key (the server
// store's historical dbPath-derived key). history is the set of
// (blob, binding) observations from commits reachable from the checked-out
// branch, including merge parents and superseded rows.
//
// A blob is eligible only when it authenticates under a supplied key AND
// appears under exactly one distinct binding across current rows and history.
// One occurrence under a second binding — a copy in another row, or a move
// whose original row was deleted or overwritten — is ambiguous provenance:
// rebinding it would hand the password to whichever row happens to hold it
// now, so it is skipped and left unusable instead.
//
// The planner is pure: it makes no I/O and no judgment the caller can undo.
// v2 blobs in current are already bound and are ignored; they still
// participate as provenance observations for other blobs only through the
// raw bytes they no longer share (re-sealing mints fresh ciphertext), so
// there is nothing to record.
func PlanMigration(key, legacyKey []byte, current, history []ObservedBinding) MigrationPlan {
	var plan MigrationPlan

	// blobKey → distinct bindings that have ever held this blob.
	seen := make(map[string]map[Binding]struct{})
	record := func(observations []ObservedBinding) {
		for _, o := range observations {
			if len(o.Blob) == 0 {
				continue
			}
			k := string(o.Blob)
			if seen[k] == nil {
				seen[k] = make(map[Binding]struct{})
			}
			seen[k][o.Binding] = struct{}{}
		}
	}
	record(current)
	record(history)

	for _, row := range current {
		if len(row.Blob) == 0 || IsV2(row.Blob) {
			continue
		}
		password, err := OpenLegacy(row.Blob, key)
		if err != nil && legacyKey != nil {
			password, err = OpenLegacy(row.Blob, legacyKey)
		}
		if err != nil {
			plan.Unopenable = append(plan.Unopenable, row.Binding)
			continue
		}
		if len(seen[string(row.Blob)]) > 1 {
			plan.Skipped = append(plan.Skipped, row.Binding)
			continue
		}
		plan.Rebind = append(plan.Rebind, Rebind{
			Binding:  row.Binding,
			Password: password,
			OldBlob:  row.Blob,
		})
	}
	return plan
}

// HasOpenableLegacyBlob reports whether observations contain a legacy
// (non-v2) blob that decrypts under key or, when non-nil, the additional
// legacyKey (the server store's historical dbPath-derived key). It gates the
// caller's history walk: only a re-sealable blob can make provenance evidence
// from history matter, so no openable legacy blob means no walk.
func HasOpenableLegacyBlob(observations []ObservedBinding, key, legacyKey []byte) bool {
	for _, o := range observations {
		if len(o.Blob) == 0 || IsV2(o.Blob) {
			continue
		}
		if _, err := OpenLegacy(o.Blob, key); err == nil {
			return true
		}
		if legacyKey != nil {
			if _, err := OpenLegacy(o.Blob, legacyKey); err == nil {
				return true
			}
		}
	}
	return false
}

// RebindWarnings renders one per-peer re-add line for each skipped
// credential. goos selects the shell quoting of the peer name
// (remedy.CommandArg); the URL stays a placeholder because it is typed by the
// operator. The migration prints these lines and nothing else about the
// affected rows: no passwords, ciphertext, keys, or credential-bearing URLs
// ever reach the log.
func (p MigrationPlan) RebindWarnings(goos string) []string {
	if len(p.Skipped) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Skipped))
	for _, b := range p.Skipped {
		arg, note := remedy.CommandArg(goos, b.Name, "<peer>")
		out = append(out, fmt.Sprintf(
			"Warning: bd will not rebind peer %q's stored password automatically: its ciphertext appears under more than one peer identity in the current rows or the database's reachable history, so its provenance is ambiguous. "+
				"It remains encrypted and unusable. If this was unexpected, rotate the peer's password on the remote, then re-run: bd federation add-peer %s <url>%s",
			b.Name, arg, note))
	}
	return out
}