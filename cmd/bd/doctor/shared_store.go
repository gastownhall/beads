package doctor

import (
	"database/sql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// SharedStore holds a single read-only storage.DoltStorage open for the
// duration of a doctor run, preventing the infinite Dolt server restart loop
// that occurs when each check opens and closes its own store (GH#2636).
//
// In server mode, the underlying store is a *dolt.DoltStore. In embedded mode
// (the default), it is a *embeddeddolt.EmbeddedDoltStore (bd-ffe). Both
// satisfy storage.DoltStorage.
//
// Usage:
//
//	ss := NewSharedStore(path)
//	defer ss.Close()
//	store := ss.Store() // may be nil if DB doesn't exist or can't open
//
// Check functions that accept a storage.DoltStorage parameter should use the
// shared store when available, falling back to opening their own store when
// called standalone (e.g., from tests or one-off checks).
type SharedStore struct {
	store    storage.DoltStorage
	beadsDir string
	// rawDB is a long-lived raw *sql.DB handle for checks that need direct
	// SQL access (e.g., validation.go, integrity.go, multirepo.go). In server
	// mode this is the dolt.DoltStore's UnderlyingDB. In embedded mode it is
	// a dedicated sql.DB opened via embeddeddolt.OpenSQL (may be nil when the
	// store failed to open).
	rawDB        *sql.DB
	rawCleanup   func() error
	isEmbedded   bool
}

// Store returns the shared DoltStorage, or nil if the database couldn't be opened.
func (ss *SharedStore) Store() storage.DoltStorage {
	if ss == nil {
		return nil
	}
	return ss.store
}

// BeadsDir returns the resolved .beads directory path.
func (ss *SharedStore) BeadsDir() string {
	if ss == nil {
		return ""
	}
	return ss.beadsDir
}

// RawDB returns a raw *sql.DB handle for direct SQL queries, or nil if no
// database is available. Callers must NOT close the returned DB — SharedStore
// manages its lifetime.
//
// Prefer the typed methods on Store() when possible. Use RawDB only for
// diagnostic queries that cannot be expressed through the DoltStorage interface.
func (ss *SharedStore) RawDB() *sql.DB {
	if ss == nil {
		return nil
	}
	return ss.rawDB
}

// IsEmbedded reports whether the shared store is backed by an embedded Dolt engine.
// Checks that require server-mode connectivity can gate on this (AC bullet 2 of bd-ffe).
func (ss *SharedStore) IsEmbedded() bool {
	return ss != nil && ss.isEmbedded
}

// Close closes the underlying store and raw DB. Safe to call multiple times.
func (ss *SharedStore) Close() {
	if ss == nil {
		return
	}
	if ss.rawCleanup != nil {
		_ = ss.rawCleanup()
		ss.rawCleanup = nil
	}
	ss.rawDB = nil
	if ss.store != nil {
		_ = ss.store.Close()
		ss.store = nil
	}
}

func beadsDirFromSharedStore(path string, ss *SharedStore) string {
	if beadsDir := sharedStoreBeadsDir(ss); beadsDir != "" {
		return beadsDir
	}
	return ResolveBeadsDirForRepo(path)
}

func sharedStoreBeadsDir(ss *SharedStore) string {
	if ss == nil {
		return ""
	}
	return ss.BeadsDir()
}

func sharedStoreNeedsLocalDoltDir(beadsDir string) bool {
	cfg, err := configfile.Load(beadsDir)
	return err != nil || cfg == nil || !cfg.IsDoltServerMode()
}

// sharedStoreIsEmbeddedConfig reports whether the persisted config selects
// embedded mode (either explicitly via dolt_mode=embedded, or implicitly by
// the absence of server-mode settings).
func sharedStoreIsEmbeddedConfig(beadsDir string) bool {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return true // default: embedded
	}
	return !cfg.IsDoltServerMode()
}

// embeddedSkippedCheck builds a standard "skipped in embedded mode" DoctorCheck.
// Used by DB-backed checks that cannot run without an open store and whose
// open path would deadlock subprocess-spawning checks (bd-ffe).
func embeddedSkippedCheck(name, detail string) DoctorCheck {
	return DoctorCheck{
		Name:    name,
		Status:  StatusOK,
		Message: "Skipped in embedded mode",
		Detail:  detail,
		Fix:     "Run targeted checks with 'bd doctor --check=validate' (or pollution/artifacts/conventions) for DB-backed diagnostics",
	}
}
