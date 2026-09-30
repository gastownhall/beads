package fix

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// The clone-local FK spec, scan, and re-link core live in
// internal/storage/schema (schema.CloneLocalFKs) so the storage code that
// performs hard resets can re-link through the same code the doctor uses
// (bd-7bpkd, ga-28co77). This file keeps only the doctor's workspace plumbing
// and output.

// ScanSeveredCloneLocalFKs reports which clone-local FKs are missing from the
// live schema. Used by CheckCloneLocalFKs in the doctor package.
func ScanSeveredCloneLocalFKs(path string) ([]schema.SeveredCloneLocalFK, error) {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return nil, err
	}

	db, _, err := openDoltDB(beadsDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	return scanSeveredCloneLocalFKs(db)
}

func scanSeveredCloneLocalFKs(db *sql.DB) ([]schema.SeveredCloneLocalFK, error) {
	return schema.ScanSeveredCloneLocalFKs(context.Background(), db)
}

// CloneLocalFKEnforcement re-links severed clone-local FKs: for each missing
// constraint it deletes the orphaned rows that accumulated while enforcement
// was off (ADD CONSTRAINT validates existing rows, so they must go first),
// then re-adds the constraint in place. Verified on dolt 2.2.2: the re-added
// FK resolves against the current tracked root and enforces again.
func CloneLocalFKEnforcement(path string, verbose bool) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	db, cfg, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Clone-local FK fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	if skip, err := guardFixTarget("Clone-local FK fix", db, beadsDir, cfg); skip {
		return err
	}

	return relinkSeveredCloneLocalFKs(db, verbose)
}

// relinkSeveredCloneLocalFKs is the core of CloneLocalFKEnforcement, split out
// so tests can drive it against a database handle directly.
func relinkSeveredCloneLocalFKs(db *sql.DB, verbose bool) error {
	severed, err := scanSeveredCloneLocalFKs(db)
	if err != nil {
		return err
	}
	if len(severed) == 0 {
		fmt.Println("  ✓ All clone-local FKs present")
		return nil
	}

	for _, fk := range severed {
		removed, err := schema.RelinkCloneLocalFK(context.Background(), db, fk.CloneLocalFK)
		if err != nil {
			return err
		}
		if verbose && removed > 0 {
			fmt.Printf("  Removed %d orphaned row(s) from %s\n", removed, fk.Table)
		}
		fmt.Printf("  ✓ Re-linked %s.%s (%d orphaned row(s) removed)\n", fk.Table, fk.Constraint, removed)
	}
	return nil
}
