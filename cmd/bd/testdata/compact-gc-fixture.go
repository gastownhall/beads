// Command compact-gc-fixture stands in for an external `dolt` binary in
// TestRunCompactDoltTargetsOnlyAuthorizedActiveDatabase
// (cmd/bd/compact_gc_target_test.go). It is entirely env-driven at run time
// (BEADS_COMPACT_GC_FIXTURE_LOG, BEADS_COMPACT_GC_FIXTURE_MODE), so one
// prebuilt copy serves every test case and every mode: the test never needs
// to invoke a Go toolchain to produce it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	log, err := os.OpenFile(os.Getenv("BEADS_COMPACT_GC_FIXTURE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(struct {
		Dir  string
		Args []string
	}{dir, os.Args[1:]}); err != nil {
		panic(err)
	}
	if err := log.Close(); err != nil {
		panic(err)
	}
	mode := os.Getenv("BEADS_COMPACT_GC_FIXTURE_MODE")
	if mode == "fallback" && len(os.Args) == 4 {
		fmt.Fprintln(os.Stderr, "unknown flag: --archive-level")
		os.Exit(23)
	}
	if mode == "failure" {
		fmt.Fprintln(os.Stderr, "genuine GC failure")
		os.Exit(23)
	}
	if err := os.WriteFile("gc-ran", []byte("collected"), 0600); err != nil {
		panic(err)
	}
}
