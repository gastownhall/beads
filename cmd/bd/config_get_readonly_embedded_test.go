//go:build cgo

package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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

// TestEmbeddedConfigGetYamlKeyWithServerDown pins the outage half of GH#5108:
// a config.yaml key is answered from config.yaml without opening the store,
// so `bd config get` still reads it while the configured Dolt server is down,
// and still writes nothing.
func TestEmbeddedConfigGetYamlKeyWithServerDown(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "test")

	if stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "set", "dolt.max-conns", "7"); err != nil {
		t.Fatalf("config set dolt.max-conns failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	// Point the workspace at a server-mode port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata: cfg=%v err=%v", cfg, err)
	}
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	before := snapshotConfigGetFiles(t, beadsDir)

	stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "get", "dolt.max-conns")
	if err != nil {
		t.Fatalf("config get dolt.max-conns with the server down failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "7" {
		t.Fatalf("config get dolt.max-conns = %q, want %q\nstderr:\n%s", got, "7", stderr)
	}
	assertConfigGetFilesUnchanged(t, beadsDir, "config get dolt.max-conns", before)

	stdout, stderr, err = runConfigCommandWithStreams(t, bd, dir, "get", "--json", "dolt.max-conns")
	if err != nil {
		t.Fatalf("config get --json dolt.max-conns with the server down failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode config get --json output: %v\nstdout:\n%s", err, stdout)
	}
	want := map[string]string{"key": "dolt.max-conns", "value": "7", "location": "config.yaml"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("config get --json %s = %v, want %q (full: %s)", k, got[k], v, stdout)
		}
	}
	assertConfigGetFilesUnchanged(t, beadsDir, "config get --json dolt.max-conns", before)
}
