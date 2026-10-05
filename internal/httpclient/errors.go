// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/errors.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
)

// ErrHTTPUnsupported is returned by every refused method of the http store. It
// unwraps to *storage.ErrUnsupported{Op: <method>, Backend: "http"} so that
// errors.Is/errors.As classification and backend/conformance's
// RunUnsupportedContract hold unchanged, and it carries what the refusal
// taxonomy needs to say about the server that refused.
//
// The taxonomy (design D7) renders these fields at the standard error path
// rather than letting a Go method name escape to a user; the shape here is the
// one D8 ("The sentinel") specifies.
type ErrHTTPUnsupported struct {
	// Unsup is the portable sentinel: Op is the method name, Backend is "http".
	Unsup *storage.ErrUnsupported
	// ServerURL is the target this store speaks to. Empty for a backstop
	// refusal raised before a target was resolved (see errUnsupported).
	ServerURL string
	// BdVersion is the serving binary's release, from the cached
	// ContextResponse; empty before the handshake has run (D6).
	BdVersion string
	// Capabilities are the tokens the server advertises, sorted. Nil before the
	// handshake.
	Capabilities []string
}

func (e *ErrHTTPUnsupported) Error() string {
	// D8 pins this shape for a refusal that knows its server. A backstop
	// refusal has no URL to name, so it falls back to the portable sentinel's
	// own rendering rather than printing "bd serve at " with nothing after it.
	if e.ServerURL == "" {
		return e.Unsup.Error()
	}
	return fmt.Sprintf("operation %q not supported by the http backend (bd serve at %s)",
		e.Unsup.Op, e.ServerURL)
}

func (e *ErrHTTPUnsupported) Unwrap() error { return e.Unsup }

// errUnsupported is the constructor every generated stub in unsupported_gen.go
// calls. Those stubs hang off the generated empty value receiver and therefore
// cannot reach a store, so they are the backstop arm: Op and Backend only.
//
// Every HAND-WRITTEN refusal in this package goes through (*Store).unsupported
// instead, which fills in the server context D7 renders. That split is the
// reason the taxonomy's user-facing texts always have a URL: whole-command and
// flag-mode refusals are raised before RunE from the store, never from a
// promoted shell stub.
func errUnsupported(op string) error {
	return &ErrHTTPUnsupported{Unsup: &storage.ErrUnsupported{Op: op, Backend: Backend}}
}

// unsupported is the store-bound refusal: the same typed sentinel, enriched
// with the target and the handshake snapshot so the D7 taxonomy can name the
// server, its version and what it does advertise.
func (s *Store) unsupported(op string) error {
	e := &ErrHTTPUnsupported{Unsup: &storage.ErrUnsupported{Op: op, Backend: Backend}}
	if s == nil {
		return e
	}
	e.ServerURL = s.target.String()
	if snap := s.cachedSnapshot(); snap != nil {
		e.BdVersion = snap.BdVersion
		e.Capabilities = append([]string(nil), snap.Capabilities...)
		sort.Strings(e.Capabilities)
	}
	return e
}
