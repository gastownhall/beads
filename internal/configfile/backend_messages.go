package configfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// This file is the single source of truth for the user-facing removed-backend
// and unknown-backend messages. cmd/bd, the public beads package, and the Dolt
// store must all fail closed with the same text, so the wording is deliberately
// centralized here.
//
// Rejection is fail-closed and stays that way: bd never rewrites metadata.json
// on an open path. But a workspace whose metadata names a removed backend often
// holds a live Dolt database — bd v1.2.x opened these workspaces as Dolt no
// matter what the "backend" field said, so the field is the only stale thing
// about them. Sending those users down the export-and-reinitialize path would
// destroy a database that is one JSON edit from healthy, so the messages below
// branch on whether Dolt data is actually present and, when it is, name the
// exact edit instead (D-8).

// RemovedBackendRationale explains why direct PostgreSQL/MySQL support was
// removed. Shortened site-specific messages (e.g. bd init flag guidance)
// compose with this clause directly.
const RemovedBackendRationale = "direct support for general-purpose server databases was rolled back to keep Beads simple and resource-light"

// RemovedSQLiteRationale explains why the SQLite backend was removed. SQLite is
// not a server database, so it carries its own rationale instead of reusing
// RemovedBackendRationale.
const RemovedSQLiteRationale = "the SQLite backend was rolled back to consolidate storage on a single engine and dialect and keep Beads simple and robust"

// BackendNotOpenedGuarantee is the fail-closed data-safety guarantee included
// in backend rejection errors: refusing a workspace never opens, creates, or
// modifies its storage.
const BackendNotOpenedGuarantee = "no storage database was opened or modified"

// removedBackendRationale picks the rationale clause for a removed backend.
func removedBackendRationale(backend string) string {
	if backend == BackendSQLite {
		return RemovedSQLiteRationale
	}
	return RemovedBackendRationale
}

// ExistingDoltDataDir returns the path of a Dolt database directory already
// present in the workspace beneath beadsDir, or "" when there is none. It
// checks the embedded layout (.beads/embeddeddolt) and the server-mode data dir
// (.beads/dolt by default, or the dolt_data_dir / BEADS_DOLT_DATA_DIR override,
// absolute or relative to beadsDir); under either root a database lives
// directly at <root>/.dolt or one level down at <root>/<db>/.dolt.
//
// It stats and reads directories only — never opens or writes storage — because
// every caller is on a rejection path that has promised not to touch the data.
// An empty beadsDir returns "", so callers without a workspace directory fall
// back to the generic message.
//
// The detection approach is ported from PR #4740 by Steve Yegge.
func ExistingDoltDataDir(beadsDir string, cfg *Config) string {
	if beadsDir == "" {
		return ""
	}

	doltDir := filepath.Join(beadsDir, "dolt")
	if custom := cfg.GetDoltDataDir(); custom != "" {
		if filepath.IsAbs(custom) {
			doltDir = custom
		} else {
			doltDir = filepath.Join(beadsDir, custom)
		}
	}

	for _, root := range []string{filepath.Join(beadsDir, "embeddeddolt"), doltDir} {
		if isDoltRepository(root) {
			return root
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if db := filepath.Join(root, entry.Name()); isDoltRepository(db) {
				return db
			}
		}
	}
	return ""
}

// isDoltRepository reports whether dir is a Dolt database root, i.e. holds a
// .dolt directory.
func isDoltRepository(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".dolt"))
	return err == nil && info.IsDir()
}

// RemovedBackendDetail returns the shared body of a removed-backend error for a
// caller with no workspace directory to inspect: the rationale, the
// untouched-data guarantee, and the export/reinitialize recovery path. Callers
// that know the workspace should use RemovedBackendDetailAt, which can offer the
// metadata heal when the workspace already holds Dolt data.
func RemovedBackendDetail(backend string) string {
	return removedBackendRecoveryDetail(backend, "")
}

// RemovedBackendDetailAt returns the body of a removed-backend error for a known
// workspace: the metadata heal when beadsDir already holds a Dolt database, and
// the export/reinitialize recovery path otherwise. Callers prepend a
// site-specific lead-in; RemovedBackendErrorAt carries the standard one.
func RemovedBackendDetailAt(backend, beadsDir string, cfg *Config) string {
	if doltDir := ExistingDoltDataDir(beadsDir, cfg); doltDir != "" {
		return removedBackendHealDetail(backend, beadsDir, doltDir)
	}
	return removedBackendRecoveryDetail(backend, beadsDir)
}

// removedBackendHealDetail is the branch for a workspace that already has Dolt
// data: the stale metadata value is the whole problem, so the message names the
// exact edit that fixes it rather than a destructive rebuild.
func removedBackendHealDetail(backend, beadsDir, doltDir string) string {
	return fmt.Sprintf("%s; however, this workspace already has a Dolt database at %s, and bd v1.2.x opened workspaces like this as Dolt — the stale \"backend\" value is the only problem; to heal, edit %s and change \"backend\": %q to \"backend\": %q (or delete the \"backend\" field entirely; Dolt is the default), then rerun the command; %s",
		removedBackendRationale(backend), doltDir, ConfigPath(beadsDir),
		backend, BackendDolt, BackendNotOpenedGuarantee)
}

// removedBackendRecoveryDetail is the branch for a workspace with no Dolt data:
// the configured database really is the only copy, so recovery runs through an
// export from a bd release that still has the backend.
func removedBackendRecoveryDetail(backend, beadsDir string) string {
	absent := ""
	if beadsDir != "" {
		absent = fmt.Sprintf(", and no Dolt database was found under %s", beadsDir)
	}
	return fmt.Sprintf("%s; the configured %s database was not opened or modified%s; to recover the data, export it to JSONL with a bd release that still includes the %s backend, then follow 'bd help init-safety' to reinitialize this workspace with Dolt and import the exported data",
		removedBackendRationale(backend), backend, absent, backend)
}

// RemovedBackendError is the standard fail-closed error for metadata that
// selects a backend whose direct support was removed, for callers with no
// workspace directory to inspect. Prefer RemovedBackendErrorAt.
func RemovedBackendError(backend string) error {
	return fmt.Errorf("storage backend %q is no longer supported: %s", backend, RemovedBackendDetail(backend))
}

// RemovedBackendErrorAt is RemovedBackendError for a known workspace: when
// beadsDir already holds a Dolt database the error names metadata.json as the
// one thing to fix, because that is the truth for every workspace bd v1.2.x
// opened as Dolt despite the stale "backend" value.
func RemovedBackendErrorAt(backend, beadsDir string, cfg *Config) error {
	if doltDir := ExistingDoltDataDir(beadsDir, cfg); doltDir != "" {
		return fmt.Errorf("storage backend %q in %s is no longer supported: %s",
			backend, ConfigPath(beadsDir), removedBackendHealDetail(backend, beadsDir, doltDir))
	}
	return fmt.Errorf("storage backend %q is no longer supported: %s", backend, removedBackendRecoveryDetail(backend, beadsDir))
}

// UnknownBackendError is the standard fail-closed error for metadata that
// names a backend this build does not recognize.
func UnknownBackendError(backend string) error {
	return UnknownBackendErrorAt(backend, "", nil)
}

// UnknownBackendErrorAt is UnknownBackendError enriched with the metadata heal
// when the workspace already holds a Dolt database — the same truth the
// removed-backend branch tells, for a value bd never recognized at all.
func UnknownBackendErrorAt(backend, beadsDir string, cfg *Config) error {
	remedy := "fix or restore metadata.json and retry"
	if doltDir := ExistingDoltDataDir(beadsDir, cfg); doltDir != "" {
		remedy = fmt.Sprintf("a Dolt database exists at %s — edit %s and set \"backend\": %q (or delete the \"backend\" field), then rerun",
			doltDir, ConfigPath(beadsDir), BackendDolt)
	}
	return fmt.Errorf("storage backend %q in metadata.json is not recognized or supported; %s; the supported backend is %q; %s", backend, BackendNotOpenedGuarantee, BackendDolt, remedy)
}
