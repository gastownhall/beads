//go:build !cgo

package main

// listMigrations returns an empty list (no Dolt without CGO).
func listMigrations() []string {
	return nil
}
