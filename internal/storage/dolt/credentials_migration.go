package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/peercred"
)

// ensurePeerCredentialMigration runs the one-time migration of legacy
// (pre-v2, identity-unbound) federation peer credentials before the store's
// first credential operation reads or writes one.
//
// A legacy blob carries no binding, so it decrypts for any peer row that holds
// it — the exact defect the v2 format closes (GH#7890 findings 32/38). The
// migration re-seals each eligible legacy credential as v2 under its row's own
// identity, then writes a key-bound completion marker beside the credential
// key. Once the marker is in place, migration never runs again: a legacy blob
// injected later (a row replicated from a peer that has not upgraded) is
// rejected by the v2-only decrypt path, never re-sealed.
//
// Eligibility covers both key vintages this store can hold: rows sealed under
// the current random key file, and rows still under the historical
// dbPath-derived key (legacyEncryptionKey), which the pre-v2 key rotation
// re-encrypted in place. A store with no credential key at all (no beadsDir)
// skips the migration and reports the missing key through its normal decrypt
// path. A read-only store never migrates, never writes the marker, and never
// re-seals.
func (s *DoltStore) ensurePeerCredentialMigration(ctx context.Context) error {
	s.credMigrationMu.Lock()
	defer s.credMigrationMu.Unlock()
	if s.credMigrationDone {
		return nil
	}
	if s.readOnly || s.db == nil {
		return nil
	}
	if err := s.ensureCredentialKey(); err != nil {
		return err
	}
	if s.credentialKey == nil {
		// No filesystem key location: no v2 seal is possible on this store,
		// and decryptPassword already reports the missing key. Nothing to
		// migrate and no marker to write.
		return nil
	}
	done, err := s.runPeerCredentialMigration(ctx)
	if err != nil {
		return err
	}
	s.credMigrationDone = done
	return nil
}

// runPeerCredentialMigration performs one migration attempt and reports
// whether it completed. It is called only under credMigrationMu.
func (s *DoltStore) runPeerCredentialMigration(ctx context.Context) (bool, error) {
	// The marker is completion evidence only for the key it authenticates: a
	// marker written under another key (a fresh key file beside a replicated
	// database) is not evidence that THIS machine's migration finished, and
	// an unreadable marker is no evidence at all. Both fall through to a real
	// migration attempt.
	markerPath := filepath.Join(s.beadsDir, peercred.MarkerFile)
	//nolint:gosec // G304: markerPath is derived from the trusted beadsDir
	content, markerErr := os.ReadFile(markerPath)
	if markerErr == nil && peercred.MarkerMatches(content, s.credentialKey) {
		return true, nil
	}
	legacyKey := s.legacyEncryptionKey()

	var plan peercred.MigrationPlan
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		current, err := issueops.PeerCredentialObservationsInTx(ctx, tx)
		if err != nil {
			return err
		}

		// The history walk costs one AS OF read per commit in the database's
		// reachable history; it runs only when there is a legacy blob this
		// machine could re-seal. Without one, the plan is empty no matter
		// what the history holds.
		var history []peercred.ObservedBinding
		if peercred.HasOpenableLegacyBlob(current, s.credentialKey, legacyKey) {
			history, err = issueops.FederationPeerHistoryObservationsInTx(ctx, tx)
			if err != nil {
				return err
			}
		}
		plan = peercred.PlanMigration(s.credentialKey, legacyKey, current, history)

		for _, r := range plan.Rebind {
			sealed, err := peercred.Seal(r.Password, s.credentialKey, r.Binding)
			if err != nil {
				return fmt.Errorf("re-seal legacy credential for peer %s: %w", r.Binding.Name, err)
			}
			if err := issueops.RebindFederationPeerCredentialInTx(ctx, tx, r.Binding, r.OldBlob, sealed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	// The re-sealed rows are durable only as a Dolt commit; the SQL
	// transaction above merely landed them in the working set.
	if len(plan.Rebind) > 0 {
		if err := s.doltAddAndCommit(ctx, []string{"federation_peers"}, "federation: re-seal legacy peer credentials as identity-bound"); err != nil {
			return false, fmt.Errorf("commit re-sealed peer credentials: %w", err)
		}
	}

	// The marker is written only after the rebinds have committed. A crash
	// before this point leaves the marker absent, so the next credential
	// operation replans against the committed rows: already-re-sealed rows
	// are v2 and the planner ignores them. A marker that cannot be written
	// fails the credential operation rather than leaving a half-finished
	// migration unmarked.
	if err := os.WriteFile(markerPath, peercred.MarkerValue(s.credentialKey), 0600); err != nil {
		return false, fmt.Errorf("write credential migration marker: %w", err)
	}

	// Skipped credentials stay encrypted and unusable by design; the operator
	// learns which peers need a manual re-add from the migration itself,
	// once, because the marker above keeps this path from running again.
	for _, line := range plan.RebindWarnings(runtime.GOOS) {
		fmt.Fprintf(credentialWarnWriter, "%s\n", line)
	}
	return true, nil
}

// credentialWarnWriter receives the credential-migration diagnostics. Defaults
// to os.Stderr, matching the unconditional warnings this package writes from
// federation.go; overridable in tests, as embeddeddolt's federationWarnWriter
// is.
var credentialWarnWriter io.Writer = os.Stderr
