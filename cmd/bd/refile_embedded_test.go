//go:build embeddeddolt

package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestEmbeddedRefile(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rf")

	// NOTE: refile requires a multi-rig town layout (findTownBeadsDir,
	// routing.ResolveBeadsDirForRig, dolt.NewFromConfig for target).
	// Tests here cover error paths; cross-rig functionality needs a
	// dedicated multi-rig test harness.

	t.Run("refile_requires_two_args", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Refile one arg", "--type", "task")
		cmd := exec.Command(bd, "refile", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected refile with one arg to fail, got: %s", out)
		}
	})

	t.Run("refile_nonexistent_source", func(t *testing.T) {
		cmd := exec.Command(bd, "refile", "rf-nonexistent999", "other")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected refile of nonexistent to fail, got: %s", out)
		}
		_ = out
	})

	t.Run("refile_same_rig_fails", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Refile same rig", "--type", "task")
		cmd := exec.Command(bd, "refile", issue.ID, "rf")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected refile to same rig to fail, got: %s", out)
		}
		_ = out
	})

	t.Run("refile_cross_rig_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout")
	})

	t.Run("refile_keep_open_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout")
	})

	t.Run("refile_json_skipped", func(t *testing.T) {
		t.Skip("requires multi-rig town layout")
	})
}

func TestEmbeddedRefileConcurrent(t *testing.T) {
	t.Skip("requires multi-rig town layout for concurrent refile testing")
}
