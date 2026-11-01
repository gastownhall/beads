// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/servercontext.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
)

// ServerContext reports the three facts design D7's refusal taxonomy needs to
// name the server: the URL it speaks to, the release it is running, and the
// capability tokens it advertises.
//
// The typed sentinel carries the same three fields, so a refusal raised BY the
// store needs nothing from here. This exists for the refusals that are not
// sentinels — a 400 `unknown_parameter` comes back as a problem document, which
// carries the parameter and the URL but has no way to know the server's
// version — and for cmd/bd's pre-run checks, which have a store but no error.
//
// The version and capabilities are empty until the lazy handshake has run (D6);
// callers render the shorter half of their text rather than dialing to fill it,
// because a refusal that opens a connection to explain itself is worse than a
// refusal that says less.
func (s *Store) ServerContext() (serverURL, bdVersion string, capabilities []string) {
	if s == nil {
		return "", "", nil
	}
	serverURL = s.target.String()
	snap := s.cachedSnapshot()
	if snap == nil {
		return serverURL, "", nil
	}
	return serverURL, snap.BdVersion, append([]string(nil), snap.Capabilities...)
}

// Ping satisfies storage.Pinger: one authenticated round trip to the server.
//
// The first call in a process IS the handshake (identity-checked against the
// workspace's pin, and cached for every later dispatch). Once a handshake is
// cached, a later Ping re-reads the context uncached, so it never reports a
// live server from a stale answer.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.wire == nil {
		return fmt.Errorf("http backend: no transport to ping")
	}
	if s.cachedSnapshot() == nil {
		_, err := s.snapshot(ctx)
		return err
	}
	var out apigen.ContextResponse
	return s.wire.Do(ctx, wire.Request{Op: wire.OpGetContext, Method: http.MethodGet, Path: wire.PathContext}, &out)
}

var _ storage.Pinger = (*Store)(nil)

// CallerAttributionLimited satisfies storage.CallerAttributionLimitedStore:
// the v0 wire publishes no provenance member on updateIssue or reopenIssue
// (the server writes its own history label) and no closed_by_session member
// on either patch document (W-UpdateRequest.Provenance,
// W-ReopenRequest.Provenance, W-IssuePatch.ClosedBySession). It is
// unconditional because the gap is in the wire shape; a wire revision that
// publishes either member would turn this into a handshake capability check.
func (s *Store) CallerAttributionLimited() bool { return true }

var _ storage.CallerAttributionLimitedStore = (*Store)(nil)
