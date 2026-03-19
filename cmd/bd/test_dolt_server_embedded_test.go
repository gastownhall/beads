//go:build embeddeddolt

package main

import "database/sql"

// Stubs for variables defined in test_dolt_server_cgo_test.go which is
// excluded from embedded builds (cgo && !embeddeddolt). The test helpers
// in test_helpers_test.go reference these, so they must exist.
var testSharedDB string
var testSharedConn *sql.DB
