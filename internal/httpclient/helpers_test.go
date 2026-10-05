// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore test helpers@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import "testing"

// ptr returns a pointer to v (test helper lifted alongside credential_test.go).
func ptr[T any](v T) *T { return &v }

// testTarget builds a minimal, valid Target for tests that only care about
// save/load/remove round-tripping the sidecar and not about any particular
// server identity. It carries no CAFile and no ExpectProjectID, matching the
// "connect with nothing pinned yet" shape — callers that need a specific
// field set (e.g. an absolute CAFile) build their own Target literal instead
// of asking this helper to grow a parameter for every case.
func testTarget(t *testing.T) Target {
	t.Helper()
	return Target{BaseURL: mustParseURL(t, "https://example.com")}
}
