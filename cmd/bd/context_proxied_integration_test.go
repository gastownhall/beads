//go:build cgo

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestProxiedServerContext(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ctx")

	t.Run("default_text", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "context")
		if err != nil {
			t.Fatalf("bd context failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, configfile.DoltModeProxiedServer) {
			t.Errorf("expected mode %q in context output:\n%s", configfile.DoltModeProxiedServer, stdout)
		}
		if !strings.Contains(stdout, ".beads") {
			t.Errorf("expected .beads in context output:\n%s", stdout)
		}
	})

	t.Run("json_fields", func(t *testing.T) {
		t.Parallel()
		info := proxiedContextJSON(t, bd, p)

		if info.DoltMode != configfile.DoltModeProxiedServer {
			t.Errorf("dolt_mode = %q, want %q", info.DoltMode, configfile.DoltModeProxiedServer)
		}
		if info.ProxiedDir == "" {
			t.Error("proxied_dir should be populated in proxied-server mode")
		}
		if info.Database != p.database {
			t.Errorf("database = %q, want %q", info.Database, p.database)
		}
		if filepath.Base(info.BeadsDir) != ".beads" {
			t.Errorf("beads_dir = %q, want a path ending in .beads", info.BeadsDir)
		}
		if info.Backend != configfile.BackendDolt {
			t.Errorf("backend = %q, want %q", info.Backend, configfile.BackendDolt)
		}
		if info.BdVersion == "" {
			t.Error("bd_version should be populated")
		}
		if info.ServerHost != "" || info.ServerPort != 0 {
			t.Errorf("server host/port must be empty in proxied-server mode: host=%q port=%d", info.ServerHost, info.ServerPort)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		t.Parallel()
		const numWorkers = 8
		errs := make([]error, numWorkers)
		modes := make([]string, numWorkers)

		var wg sync.WaitGroup
		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "context", "--json")
				if err != nil {
					errs[idx] = err
					return
				}
				var info ContextInfo
				if jerr := json.Unmarshal([]byte(stdout), &info); jerr != nil {
					errs[idx] = jerr
					return
				}
				modes[idx] = info.DoltMode
			}(i)
		}
		wg.Wait()

		for i := 0; i < numWorkers; i++ {
			if errs[i] != nil {
				t.Errorf("worker %d: bd context failed: %v", i, errs[i])
				continue
			}
			if modes[i] != configfile.DoltModeProxiedServer {
				t.Errorf("worker %d: dolt_mode = %q, want %q", i, modes[i], configfile.DoltModeProxiedServer)
			}
		}
	})
}

func proxiedContextJSON(t *testing.T, bd string, p proxiedProject) ContextInfo {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "context", "--json")
	if err != nil {
		t.Fatalf("bd context --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var info ContextInfo
	if jerr := json.Unmarshal([]byte(stdout), &info); jerr != nil {
		t.Fatalf("decoding context JSON: %v\noutput:\n%s", jerr, stdout)
	}
	return info
}
