package main

import "fmt"

// initializeNoDbMode previously set up in-memory storage from JSONL.
// The memory backend has been removed; only Dolt is supported.
func initializeNoDbMode() error {
	return fmt.Errorf("--no-db mode has been removed; beads now requires Dolt (run 'bd init --backend=dolt' to create a database)")
}
