//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestGetRoutingConfigValue_DBFallback(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode in DB: %v", err)
	}

	got := getRoutingConfigValue(ctx, s, "routing.mode")
	if got != "auto" {
		t.Fatalf("getRoutingConfigValue() = %q, want %q", got, "auto")
	}
}

func TestGetRoutingConfigValue_YAMLPrecedence(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode in DB: %v", err)
	}
	config.Set("routing.mode", "maintainer")

	got := getRoutingConfigValue(ctx, s, "routing.mode")
	if got != "maintainer" {
		t.Fatalf("getRoutingConfigValue() = %q, want %q", got, "maintainer")
	}
}

func TestFindTownBeadsDir_PrefersCurrentBeadsDirOverNestedTownCWD(t *testing.T) {
	outerTownDir := t.TempDir()

	outerMayorDir := filepath.Join(outerTownDir, "mayor")
	if err := os.MkdirAll(outerMayorDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerMayorDir, "town.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	outerBeadsDir := filepath.Join(outerTownDir, ".beads")
	if err := os.MkdirAll(outerBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rigs/outer"}
`), 0600); err != nil {
		t.Fatal(err)
	}

	outerRigBeadsDir := filepath.Join(outerTownDir, "outer-worktree", ".beads")
	if err := os.MkdirAll(outerRigBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerRigBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"outer"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerRigBeadsDir, "config.yaml"), []byte("backend: dolt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerRigBeadsDir, "dolt.db"), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(outerBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rigs/outer"}
`), 0600); err != nil {
		t.Fatal(err)
	}

	innerTownDir := filepath.Join(outerTownDir, "nested-town")
	innerMayorDir := filepath.Join(innerTownDir, "mayor")
	if err := os.MkdirAll(innerMayorDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerMayorDir, "town.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	innerBeadsDir := filepath.Join(innerTownDir, ".beads")
	if err := os.MkdirAll(innerBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rigs/inner"}
`), 0600); err != nil {
		t.Fatal(err)
	}

	workerDir := filepath.Join(innerTownDir, "crew", "worker")
	if err := os.MkdirAll(workerDir, 0750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workerDir)
	t.Setenv("BEADS_DIR", outerRigBeadsDir)

	oldDBPath := dbPath
	dbPath = ""
	t.Cleanup(func() { dbPath = oldDBPath })

	got, err := findTownBeadsDir()
	if err != nil {
		t.Fatalf("findTownBeadsDir() error = %v", err)
	}
	if got != outerBeadsDir {
		t.Fatalf("findTownBeadsDir() = %s, want %s", got, outerBeadsDir)
	}
}

func TestFindTownBeadsDir_PrefersExplicitDBPathOverBEADSDir(t *testing.T) {
	outerTownDir := t.TempDir()

	outerMayorDir := filepath.Join(outerTownDir, "mayor")
	if err := os.MkdirAll(outerMayorDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerMayorDir, "town.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	outerBeadsDir := filepath.Join(outerTownDir, ".beads")
	if err := os.MkdirAll(outerBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rigs/outer"}
`), 0600); err != nil {
		t.Fatal(err)
	}

	outerRigBeadsDir := filepath.Join(outerTownDir, "outer-worktree", ".beads")
	outerDBPath := filepath.Join(outerRigBeadsDir, "dolt")
	writeTestMetadata(t, outerDBPath, "outer_db")

	innerTownDir := filepath.Join(outerTownDir, "nested-town")
	innerMayorDir := filepath.Join(innerTownDir, "mayor")
	if err := os.MkdirAll(innerMayorDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerMayorDir, "town.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	innerBeadsDir := filepath.Join(innerTownDir, ".beads")
	if err := os.MkdirAll(innerBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rigs/inner"}
`), 0600); err != nil {
		t.Fatal(err)
	}

	workerDir := filepath.Join(innerTownDir, "crew", "worker")
	if err := os.MkdirAll(workerDir, 0750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workerDir)
	t.Setenv("BEADS_DIR", innerBeadsDir)

	oldDBPath := dbPath
	dbPath = outerDBPath
	t.Cleanup(func() { dbPath = oldDBPath })

	got, err := findTownBeadsDir()
	if err != nil {
		t.Fatalf("findTownBeadsDir() error = %v", err)
	}
	if got != outerBeadsDir {
		t.Fatalf("findTownBeadsDir() = %s, want %s", got, outerBeadsDir)
	}
}
