//go:build cgo && unix

package main

import (
	"encoding/json"
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
	epic := bdProxiedCreate(t, bd, p.dir, "refusal epic", "--type", "epic")
	formula := filepath.Join(t.TempDir(), "refusal.toml")
	if err := os.WriteFile(formula, []byte("name = \"refusal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(p.proxyRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("read config before refusal: %v", err)
	}
	commands := [][]string{{"cook", formula, "--persist"}, {"ship", "refusal"}, {"swarm", "create", epic.ID}, {"swarm", "list"}, {"merge-slot", "create"}, {"merge-slot", "check"}, {"merge-slot", "acquire"}, {"merge-slot", "release"}}
	for _, args := range commands {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			for _, jsonMode := range []bool{false, true} {
				invoke := append([]string(nil), args...)
				if jsonMode {
					invoke = append(invoke, "--json")
				}
				stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, invoke...)
				if err == nil {
					t.Fatalf("%v unexpectedly succeeded: %s", invoke, stdout)
				}
				out := strings.ToLower(stdout + stderr)
				if !strings.Contains(out, "proxy") || !strings.Contains(out, "unsupported") {
					t.Fatalf("%v refusal lacks stable proxy/unsupported shape: %s", invoke, out)
				}
				if jsonMode {
					var payload struct {
						Code    string `json:"code"`
						Mutates bool   `json:"mutates"`
					}
					if err := json.Unmarshal([]byte(stdout), &payload); err == nil {
						if !strings.HasPrefix(payload.Code, "proxy.") || payload.Mutates {
							t.Fatalf("typed refusal = %+v", payload)
						}
					}
				}
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
