package main

import "strings"

// sanitizeDBName replaces characters that are awkward for embedded Dolt
// database names with underscores so both cgo and no-cgo builds share the
// same normalization logic.
func sanitizeDBName(name string) string {
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}
