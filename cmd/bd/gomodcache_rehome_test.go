package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGOMODCACHENotUnderTestHome guards the testMainInner fix that pins
// GOMODCACHE before re-homing HOME to a temp dir. The default module cache
// is $HOME/go/pkg/mod; if it is left to follow the fake test HOME, every
// in-test `go build` (buildEmbeddedBD) re-downloads the full module set —
// slow, and a hard failure when offline.
//
// By the time any test runs, TestMain -> testMainInner has already:
//   - set HOME to a temp dir (testTempRoot), and
//   - pinned GOMODCACHE to the real module cache.
//
// So GOMODCACHE must be set and must NOT resolve under the current HOME.
func TestGOMODCACHENotUnderTestHome(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set; testMainInner did not run")
	}

	modcache := os.Getenv("GOMODCACHE")
	if modcache == "" {
		t.Fatalf("GOMODCACHE is empty after test setup: the module cache will default to $HOME/go/pkg/mod (%s), forcing buildEmbeddedBD to re-download every dependency into the temp HOME",
			filepath.Join(home, "go", "pkg", "mod"))
	}

	absHome, _ := filepath.Abs(home)
	absCache, _ := filepath.Abs(modcache)
	rel, err := filepath.Rel(absHome, absCache)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
		t.Fatalf("GOMODCACHE (%s) resolves under the test HOME (%s): the module cache followed the fake HOME, so buildEmbeddedBD must re-download all modules",
			absCache, absHome)
	}
}
