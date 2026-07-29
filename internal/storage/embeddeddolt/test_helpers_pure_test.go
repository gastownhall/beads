// Pure-Go test helper landing zone for embeddeddolt tests.
//
// This file MUST NOT carry a `//go:build cgo` tag. Keep helpers here
// stdlib-only so future pure-Go tests in this package compile under
// CGO_ENABLED=0 with the gms_pure_go build tag. Helpers that touch
// EmbeddedDoltStore internals, sql.DB, or embedded Dolt belong in cgo-tagged
// test files.

package embeddeddolt
