//go:build !cgo

package main

// cgoAvailable reports whether this binary was built with CGO support.
// CGO is required for the embedded Dolt database backend.
func cgoAvailable() bool {
	return false
}
