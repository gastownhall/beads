//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

var configGetReadOnlyFiles = []string{"config.yaml", "metadata.json", "dolt-config.log"}

// snapshotConfigGetFiles returns the contents of the files GH#5108 reported
// as rewritten by `bd config get`, with "<absent>" for a missing file.
func snapshotConfigGetFiles(t *testing.T, beadsDir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, f := range configGetReadOnlyFiles {
		data, err := os.ReadFile(filepath.Join(beadsDir, f))
		if os.IsNotExist(err) {
			m[f] = "<absent>"
			continue
		}
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		m[f] = string(data)
	}
	return m
}

func assertConfigGetFilesUnchanged(t *testing.T, beadsDir, what string, before map[string]string) {
	t.Helper()
	after := snapshotConfigGetFiles(t, beadsDir)
	for _, f := range configGetReadOnlyFiles {
		if before[f] != after[f] {
			t.Errorf("%s modified .beads/%s:\nbefore:\n%s\nafter:\n%s", what, f, before[f], after[f])
		}
	}
}

// TestEmbeddedConfigGetIsReadOnly pins GH#5108: `bd config get` must not
// rewrite .beads/config.yaml or .beads/metadata.json, nor append to
// .beads/dolt-config.log.
func TestEmbeddedConfigGetIsReadOnly(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "test")

	before := snapshotConfigGetFiles(t, beadsDir)
	for _, key := range []string{"storage.backend", "dolt.server", "dolt.database"} {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "get", key)
		if err != nil {
			t.Fatalf("config get %s failed: %v\nstdout:\n%s\nstderr:\n%s", key, err, stdout, stderr)
		}
		assertConfigGetFilesUnchanged(t, beadsDir, "config get "+key, before)
	}
}
