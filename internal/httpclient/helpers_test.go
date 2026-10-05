// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore test helpers@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

// ptr returns a pointer to v (test helper lifted alongside credential_test.go).
func ptr[T any](v T) *T { return &v }
