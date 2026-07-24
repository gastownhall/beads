package configfile

import "fmt"

// This file is the single source of truth for the user-facing removed-backend
// and unknown-backend messages. cmd/bd, the public beads package, and the Dolt
// store must all fail closed with the same text, so the wording is deliberately
// centralized here. The framing of these messages is pending a separate
// maintainer decision — reword them here, in one place, once that lands.

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

// RemovedBackendDetail returns the shared body of every removed-backend error:
// the rationale, the untouched-data guarantee, and the migration path. Callers
// prepend a site-specific lead-in; RemovedBackendError carries the standard one.
func RemovedBackendDetail(backend string) string {
	return fmt.Sprintf("%s; the configured %s database was not opened or modified; export it with a bd version that supports %s, then follow bd help init-safety to reinitialize with Dolt and import the exported data", removedBackendRationale(backend), backend, backend)
}

// RemovedBackendError is the standard fail-closed error for metadata that
// selects a backend whose direct support was removed.
func RemovedBackendError(backend string) error {
	return fmt.Errorf("storage backend %q is no longer supported: %s", backend, RemovedBackendDetail(backend))
}

// UnknownBackendError is the standard fail-closed error for metadata that
// names a backend this build does not recognize.
func UnknownBackendError(backend string) error {
	return fmt.Errorf("storage backend %q in metadata.json is not recognized or supported; %s; the supported backend is %q; fix or restore metadata.json and retry", backend, BackendNotOpenedGuarantee, BackendDolt)
}
