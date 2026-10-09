// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/servercontext.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

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
