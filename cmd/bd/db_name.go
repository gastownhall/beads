package main

import "strings"

// sanitizeDBName replaces hyphens and dots with underscores for
// SQL-idiomatic embedded Dolt database names (GH#2142, GH#3231).
//
// Defined here without a build tag so the pure-string helper is visible
// to non-cgo callers (e.g. cmd/bd/init.go) and to lint typechecking passes
// that run with CGO_ENABLED=0. The consumers that actually use embedded
// Dolt (in store_factory.go, //go:build cgo) still link to this same
// implementation.
func sanitizeDBName(name string) string {
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}
