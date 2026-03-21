//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsOrchestratorRoot_TrueWhenMarkersPresent(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte(`{"name":"gt"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".beads", "routes.jsonl"), []byte(`{"prefix":"hq-","path":"."}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if !isOrchestratorRoot(root) {
		t.Fatalf("expected %s to be detected as orchestrator root", root)
	}
}

func TestIsOrchestratorRoot_FalseWhenMarkersMissing(t *testing.T) {
	tests := []struct {
		name       string
		setupTown  bool
		setupRoute bool
	}{
		{name: "missing both", setupTown: false, setupRoute: false},
		{name: "missing routes", setupTown: true, setupRoute: false},
		{name: "missing town", setupTown: false, setupRoute: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "mayor"), 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, ".beads"), 0750); err != nil {
				t.Fatal(err)
			}

			if tc.setupTown {
				if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte(`{"name":"gt"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.setupRoute {
				if err := os.WriteFile(filepath.Join(root, ".beads", "routes.jsonl"), []byte(`{"prefix":"hq-","path":"."}`+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			if isOrchestratorRoot(root) {
				t.Fatalf("expected %s to NOT be detected as orchestrator root", root)
			}
		})
	}
}
