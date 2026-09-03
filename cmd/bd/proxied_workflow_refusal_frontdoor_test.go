//go:build cgo && unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectWorkflowRepresentativeBehavior(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "directwf", "--skip-hooks", "--skip-agents")
	cmd := exec.Command(bd, "ship", "refusal")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err == nil {
		t.Fatal("direct ship unexpectedly succeeded")
	}
	if strings.Contains(strings.ToLower(out.String()), "proxied-server mode") {
		t.Fatalf("direct mode used proxy refusal: %s", out.String())
	}
}

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
	issuesBefore := bdProxiedListJSON(t, bd, p, "--all")
	artifactBefore := map[string][]byte{}
	for _, name := range []string{".local_version", "events.jsonl", "metadata.json", "proxy.pid", "proxy-child.pid", "proxy.lock", "proxy-child.lock"} {
		b, _ := os.ReadFile(filepath.Join(p.beadsDir, name))
		artifactBefore[name] = b
	}
	commands := [][]string{{"cook", formula, "--persist"}, {"ship", "refusal"}, {"swarm", "create", epic.ID}, {"swarm", "list"}, {"merge-slot", "create"}, {"merge-slot", "check"}, {"merge-slot", "acquire"}, {"merge-slot", "release"}}
	expectedCode := func(args []string) string {
		if args[0] == "swarm" {
			return "proxy.swarm.unsupported"
		}
		if args[0] == "merge-slot" {
			return "proxy.merge_slot.unsupported"
		}
		return "proxy.formula.unsupported"
	}
	expectedMessage := func(args []string) string { return args[0] + " is not supported in proxied-server mode" }
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
				if !jsonMode && !strings.Contains(stderr, expectedMessage(args)) {
					t.Fatalf("%v refusal lacks stable proxy/unsupported shape: %s", invoke, out)
				}
				if jsonMode {
					var payload struct {
						Code    string `json:"code"`
						Mutates bool   `json:"mutates"`
					}
					if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
						t.Fatalf("invalid refusal JSON: %v", err)
					}
					if payload.Code != expectedCode(args) || payload.Mutates {
						t.Fatalf("typed refusal = %+v", payload)
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
	issuesAfter := bdProxiedListJSON(t, bd, p, "--all")
	if len(issuesBefore) != len(issuesAfter) {
		t.Fatalf("issue rows changed during refusals: before=%d after=%d", len(issuesBefore), len(issuesAfter))
	}
	for name, want := range artifactBefore {
		got, _ := os.ReadFile(filepath.Join(p.beadsDir, name))
		if string(got) != string(want) {
			t.Fatalf("artifact %s changed", name)
		}
	}
}
