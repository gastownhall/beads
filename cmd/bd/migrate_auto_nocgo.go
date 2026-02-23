//go:build !cgo

package main

// autoMigrateSQLiteToDolt is a no-op in non-CGO builds.
// SQLite reading requires CGO; users on non-CGO builds must migrate manually.
func autoMigrateSQLiteToDolt() {}
