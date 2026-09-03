//go:build cgo && unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProxiedWorkflowRefusalsFrontDoor executes the real bd binary for every
// workflow family rejected by the proxy capability contract. Refusals must
// happen before provider startup and leave the workspace untouched.
func TestProxiedWorkflowRefusalsFrontDoor(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "refusal", 5*time.Minute)
	before, err := os.ReadFile(filepath.Join(p.proxyRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("read config before refusal: %v", err)
	}
	commands := [][]string{{"cook", "--persist"}, {"ship"}, {"swarm", "create"}, {"swarm", "list"}, {"merge-slot", "create"}, {"merge-slot", "check"}, {"merge-slot", "acquire"}, {"merge-slot", "release"}}
	for _, args := range commands {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, append(args, "--json")...)
			if err == nil {
				t.Fatalf("%v unexpectedly succeeded: %s", args, stdout)
			}
			out := strings.ToLower(stdout + stderr)
			if !strings.Contains(out, "proxy") || !strings.Contains(out, "unsupported") {
				t.Fatalf("%v refusal lacks stable proxy/unsupported shape: %s", args, out)
			}
		})
	}
	after, err := os.ReadFile(filepath.Join(p.proxyRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("read config after refusal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("config changed during refused workflow commands")
	}
}
