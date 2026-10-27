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

// TestEmbeddedConfigGetYamlKeyWithServerDown pins the outage half of GH#5108:
// with the workspace configured for a Dolt server that is not listening,
// `bd config get <config.yaml key>` must still answer from config.yaml,
// exit 0, and touch nothing in .beads. At base the command tried to open a
// store before its RunE ever read the key, so during an outage it failed
// without printing the on-disk value, and the attempt wrote
// .beads/.local_version on the way.
func TestEmbeddedConfigGetYamlKeyWithServerDown(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "test")

	// A port that is verifiably closed: reserve it, then stop listening.
	// bdEnv already pins BEADS_DOLT_AUTO_START=0 and strips every inherited
	// BEADS_*/BD_* variable (including an ambient server endpoint) from the
	// subprocess, so the metadata below is the whole server configuration.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	serverPort := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close the reserved port: %v", err)
	}

	// Flip the initialized workspace to server mode pointing at that closed
	// port, rewriting metadata.json in place so only the mode and endpoint
	// change.
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata.json: %v", err)
	}
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = serverPort
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	// The value under test lives in config.yaml, which bd init may or may
	// not have created: append the key either way.
	configYAML := filepath.Join(beadsDir, "config.yaml")
	baseYAML, err := os.ReadFile(configYAML)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read config.yaml: %v", err)
	}
	if len(baseYAML) > 0 && baseYAML[len(baseYAML)-1] != '\n' {
		baseYAML = append(baseYAML, '\n')
	}
	if err := os.WriteFile(configYAML, append(baseYAML, []byte("dolt.max-conns: 7\n")...), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	// .local_version joins the set because bd init itself writes it
	// (init.go): the get must leave it byte-identical, and the store-opening
	// path is what would rewrite it on a version change.
	files := []string{"config.yaml", "metadata.json", "dolt-config.log", localVersionFile}
	snapshot := func() map[string]string {
		m := map[string]string{}
		for _, f := range files {
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

	before := snapshot()
	stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "get", "dolt.max-conns")
	if err != nil {
		t.Fatalf("config get dolt.max-conns with the server down failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "7" {
		t.Fatalf("config get dolt.max-conns printed %q, want 7", got)
	}

	stdout, stderr, err = runConfigCommandWithStreams(t, bd, dir, "get", "dolt.max-conns", "--json")
	if err != nil {
		t.Fatalf("config get dolt.max-conns --json with the server down failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var payload struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse config get --json output: %v\nstdout:\n%s", err, stdout)
	}
	if payload.Key != "dolt.max-conns" || payload.Value != "7" || payload.Location != "config.yaml" {
		t.Fatalf("config get --json = {%q %q %q}, want {dolt.max-conns 7 config.yaml}", payload.Key, payload.Value, payload.Location)
	}

	after := snapshot()
	for _, f := range files {
		if before[f] != after[f] {
			t.Errorf("config get with the server down modified .beads/%s:\nbefore:\n%s\nafter:\n%s", f, before[f], after[f])
		}
	}
}
