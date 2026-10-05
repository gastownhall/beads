// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/register.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import "github.com/steveyegge/beads/internal/storage/backends"

// Register adds the http client store to the backend registry under the "http"
// name, so workspace validation, discovery and store dispatch recognize
// metadata.json's `backend: "http"`.
//
// Deliberately not an init, for the reason postgres.Register documents:
// registration is a property of the distribution being built, not of the import
// graph. An OSS bd that reached this package transitively would gain a
// selectable "http" backend it must instead hard-fail on with
// UnknownBackendError. Only the enterprise distribution calls this, from its
// build-tagged wiring in cmd/bd.
//
// WorkspaceIsBeadsDir is true: there is no local database to discover, and
// metadata.json plus the activation sidecar are the whole workspace.
//
// Register panics on a duplicate registration, like any process-start wiring
// error, so it must have exactly one production call site.
func Register() {
	backends.Register(Backend, backends.Backend{
		Open:                NewFromConfig,
		OpenReadOnly:        NewReadOnlyFromConfig,
		WorkspaceIsBeadsDir: true,
		Remote:              true,
	})
}
