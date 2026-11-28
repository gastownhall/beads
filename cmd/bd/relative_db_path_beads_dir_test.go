package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A RELATIVE --db path must resolve to the .beads directory in the current
// directory. The upward walk's guard `dir != filepath.Dir(dir)` is false at
// ".", because filepath.Dir(".") == ".", so the loop body never runs for the
// current directory and ./.beads is never tested.
//
// Consequence measured on bd v1.2.2 AND v1.3.1: with --db
// .beads/embeddeddolt/<name> the resolver returns ".beads/embeddeddolt",
// no config is found there, bd falls back to database name "beads", opens a
// database that is not the one named on the command line, and prints
// "No issues found." at rc=0.
func TestResolveCommandBeadsDirFindsDotBeadsForARelativeDBPath(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	// The real shape: .beads/ holds the config, the db lives under it.
	if err := os.MkdirAll(filepath.Join(".beads", "embeddeddolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolveCommandBeadsDir(filepath.Join(".beads", "embeddeddolt", "proj"))

	if got != ".beads" {
		t.Errorf("resolveCommandBeadsDir(%q) = %q, want %q — the walk never tested ./.beads, so bd opens a database that is not the one named",
			".beads/embeddeddolt/proj", got, ".beads")
	}
}

// Absolute paths already worked; this is the control that the fix must not break.
func TestResolveCommandBeadsDirStillFindsDotBeadsForAnAbsoluteDBPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads", "embeddeddolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".beads")
	if got := resolveCommandBeadsDir(filepath.Join(dir, ".beads", "embeddeddolt", "proj")); got != want {
		t.Errorf("absolute path regressed: got %q, want %q", got, want)
	}
}
