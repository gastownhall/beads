//go:build cgo

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runBDWithEnv runs the bd binary with args in workDir plus extraEnv and
// returns exit code and combined output. Unlike runBDStdout it does not
// fail the test on a non-zero exit — guard tests assert on refusals.
func runBDWithEnv(t *testing.T, binPath, workDir string, extraEnv []string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"LINEAR_API_KEY=",
		// Neutralize CI/agent env so each case controls the guard inputs.
		"CI=",
		"BD_NON_INTERACTIVE=",
		"BD_LAST_TOUCHED_FALLBACK=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("bd %v in %s: %v\noutput: %s", args, workDir, err, buf.String())
		}
	}
	return code, buf.String()
}

// TestNoIDFallbackGuard verifies the last-touched fallback on mutating
// commands is refused in non-interactive sessions and honored only with an
// explicit BD_LAST_TOUCHED_FALLBACK=1 opt-in (bd-m00pb). The bd subprocess
// runs without a TTY on stdin, which is exactly the incident scenario: a
// scripted `bd update $ID ...` where $ID expanded to nothing.
func TestNoIDFallbackGuard(t *testing.T) {
	binPath := buildBDUnderTest(t)
	workDir := t.TempDir()
	initBeadsWorkspace(t, binPath, workDir)

	id := runBDStdout(t, binPath, workDir, "q", "Guard test issue")
	if id == "" {
		t.Fatal("bd q returned empty issue ID")
	}

	t.Run("update without ID refused non-interactively", func(t *testing.T) {
		code, out := runBDWithEnv(t, binPath, workDir, nil, "update", "--priority", "3")
		if code == 0 {
			t.Fatalf("bd update with no ID succeeded non-interactively; output: %s", out)
		}
		if !strings.Contains(out, "BD_LAST_TOUCHED_FALLBACK") {
			t.Errorf("refusal should mention the opt-in env var; output: %s", out)
		}
	})

	t.Run("close without ID refused non-interactively", func(t *testing.T) {
		code, out := runBDWithEnv(t, binPath, workDir, nil, "close")
		if code == 0 {
			t.Fatalf("bd close with no ID succeeded non-interactively; output: %s", out)
		}
		if !strings.Contains(out, "BD_LAST_TOUCHED_FALLBACK") {
			t.Errorf("refusal should mention the opt-in env var; output: %s", out)
		}
	})

	t.Run("BD_NON_INTERACTIVE=1 also refuses", func(t *testing.T) {
		code, out := runBDWithEnv(t, binPath, workDir,
			[]string{"BD_NON_INTERACTIVE=1"}, "update", "--priority", "3")
		if code == 0 {
			t.Fatalf("bd update with no ID succeeded under BD_NON_INTERACTIVE=1; output: %s", out)
		}
	})

	t.Run("explicit opt-in restores the fallback", func(t *testing.T) {
		// Seed the marker directly: `bd q` does not record last-touched
		// (only create/update/show/close do), and the subtest should not
		// depend on which earlier command happened to write it.
		lastTouchedPath := filepath.Join(workDir, ".beads", lastTouchedFile)
		if err := os.WriteFile(lastTouchedPath, []byte(id+"\n"), 0600); err != nil {
			t.Fatalf("seed last-touched: %v", err)
		}
		code, out := runBDWithEnv(t, binPath, workDir,
			[]string{"BD_LAST_TOUCHED_FALLBACK=1"}, "update", "--priority", "1")
		if code != 0 {
			t.Fatalf("bd update with opt-in failed (exit %d): %s", code, out)
		}
		if !strings.Contains(out, id) {
			t.Errorf("opt-in fallback should have updated last-touched issue %s; output: %s", id, out)
		}
	})

	t.Run("refusal fires before store open", func(t *testing.T) {
		// A bare directory that is not a beads workspace: if the no-ID
		// refusal ran after root's pre-run hooks, this would die on a
		// store/workspace error (or, in a real workspace, run a migration
		// or JSONL auto-import first). Argument validation runs before
		// PersistentPreRunE, so the advertised message must appear and the
		// directory must stay untouched.
		bareDir := t.TempDir()
		code, out := runBDWithEnv(t, binPath, bareDir, nil, "update", "--priority", "3")
		if code == 0 {
			t.Fatalf("bd update with no ID succeeded outside a workspace; output: %s", out)
		}
		if !strings.Contains(out, "BD_LAST_TOUCHED_FALLBACK") {
			t.Errorf("refusal should fail fast with the advertised message, not a store error; output: %s", out)
		}
		entries, err := os.ReadDir(bareDir)
		if err != nil {
			t.Fatalf("read bare dir: %v", err)
		}
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("no-ID refusal must have no side effects; created: %v", names)
		}
	})

	t.Run("explicit ID always works", func(t *testing.T) {
		code, out := runBDWithEnv(t, binPath, workDir, nil, "update", id, "--priority", "2")
		if code != 0 {
			t.Fatalf("bd update with explicit ID failed (exit %d): %s", code, out)
		}
	})
}
