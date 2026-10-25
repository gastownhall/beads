package main

import bdhttp "github.com/steveyegge/beads/backend/http"

// Registering the http backend is a property of the bd distribution being
// built, not of the import graph (see bdhttp.Register's own doc comment): OSS
// ships `bd serve`, so OSS `bd` should be able to talk to it too, and this is
// the one production call site. A workspace whose metadata.json names any
// OTHER unregistered backend still hard-fails with the registry's
// UnknownBackendError, exactly as before this file existed — only "http"
// becomes selectable, and only because this file registers it.
func init() {
	bdhttp.Register(bdhttp.Options{
		UserAgent: "bd/" + Version + " " + bdhttp.UserAgentSuffix,
	})
}
