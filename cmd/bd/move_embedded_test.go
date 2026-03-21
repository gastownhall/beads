//go:build embeddeddolt

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestEmbeddedMove(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mv")

	// NOTE: move and refile require a multi-rig town layout (findTownBeadsDir,
	// routing.ResolveBeadsDirForRig, dolt.NewFromConfig for target). The single-rig
	// embedded test harness cannot set this up. Tests here cover error paths and
	// flag validation; cross-rig functionality needs a dedicated multi-rig test harness.

	t.Run("move_requires_to_flag", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Move no-to", "--type", "task")
		cmd := exec.Command(bd, "move", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected move without --to to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "--to") {
			t.Errorf("expected error about --to flag: %s", out)
		}
	})

	t.Run("move_nonexistent_source", func(t *testing.T) {
		cmd := exec.Command(bd, "move", "mv-nonexistent999", "--to", "other")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected move of nonexistent issue to fail, got: %s", out)
		}
		_ = out
	})

	t.Run("move_same_rig_fails", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Move same rig", "--type", "task")
		cmd := exec.Command(bd, "move", issue.ID, "--to", "mv")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected move to same rig to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "already in rig") {
			t.Logf("same-rig error: %s", out)
		}
	})

	t.Run("move_cross_rig_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout (findTownBeadsDir + routing.ResolveBeadsDirForRig)")
	})

	t.Run("move_keep_open_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout")
	})

	t.Run("move_skip_deps_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout")
	})

	t.Run("move_json_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout")
	})
}

// TestEmbeddedMoveConcurrent is skipped — move requires multi-rig setup.
func TestEmbeddedMoveConcurrent(t *testing.T) {
	t.Skip("requires multi-rig town layout for concurrent move testing")
}
