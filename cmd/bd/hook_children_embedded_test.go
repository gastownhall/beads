//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestEmbeddedHookChildrenRunTheHookBinary pins GH#5922's live gap: the
// git-hook lanes shell out to `bd export` (pre-commit) and `bd import`
// (post-merge, post-checkout), and those children must be the bd running the
// hook, not whichever bd PATH selects. Each case starts the real bd by
// absolute path with a decoy bd first on PATH; the decoy records its argv and
// fails, as a stale install that cannot open the database would. Before the
// fix the decoy ran the child, the hook warned, and the index or store stayed
// stale; the hook's own exit status is 0 either way, so it proves nothing.
func TestEmbeddedHookChildrenRunTheHookBinary(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell-script decoy bd is POSIX-only")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	if !filepath.IsAbs(bd) {
		t.Fatalf("hook bd must be started by absolute path, got %q", bd)
	}

	t.Run("pre-commit export", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd, "--prefix", "hx")
		bdCreate(t, bd, dir, "already exported", "--type", "task")
		bdExportTo(t, bd, dir, filepath.Join(dir, ".beads", "issues.jsonl"))
		gitInHookTest(t, dir, "add", "-A")
		gitInHookTest(t, dir, "commit", "-q", "--no-verify", "-m", "track the export")

		// export.auto is off by default, so creating the issue does not
		// export it: only the hook can bring it into the index.
		pending := bdCreate(t, bd, dir, "pending export", "--type", "task")
		appendFileForHookTest(t, filepath.Join(dir, ".beads", "config.yaml"), "# staged change\n")
		gitInHookTest(t, dir, "add", ".beads/config.yaml")
		wantRow := `"id":"` + pending.ID + `"`
		if got := gitInHookTest(t, dir, "show", ":.beads/issues.jsonl"); strings.Contains(got, wantRow) {
			t.Fatalf("precondition: indexed JSONL already holds %s:\n%s", pending.ID, got)
		}

		marker, env := decoyBDEnv(t, dir)
		env = append(env, "BD_EXPORT_AUTO=true", "BD_EXPORT_GIT_ADD=true")
		_, stderr := runHookForTest(t, bd, dir, env, "pre-commit")

		assertDecoyNeverRan(t, marker)
		if strings.Contains(stderr, "pre-commit export warning") {
			t.Errorf("pre-commit warned about its export:\n%s", stderr)
		}
		if got := gitInHookTest(t, dir, "show", ":.beads/issues.jsonl"); !strings.Contains(got, wantRow) {
			t.Errorf("indexed JSONL lacks %s after pre-commit:\n%s\nhook stderr:\n%s", pending.ID, got, stderr)
		}
	})
}

// decoyBDEnv returns the hook environment for dir with a decoy bd first on
// PATH (the rest of PATH is kept, so git and sh still resolve) and the file
// the decoy appends its argv to whenever it runs.
func decoyBDEnv(t *testing.T, dir string) (marker string, env []string) {
	t.Helper()
	decoyDir := t.TempDir()
	marker = filepath.Join(decoyDir, "decoy-ran")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(decoyDir, "bd"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	env = envWithout(bdEnv(dir), "PATH")
	env = append(env, "PATH="+decoyDir+string(os.PathListSeparator)+os.Getenv("PATH"), "BD_GIT_HOOK=1")
	return marker, env
}

func assertDecoyNeverRan(t *testing.T, marker string) {
	t.Helper()
	if data, err := os.ReadFile(marker); err == nil {
		t.Errorf("decoy bd on PATH ran instead of the hook's bd: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// runHookForTest runs `bd hooks run <hook> [args...]` with the real bd,
// started by absolute path, and returns its stdout and stderr.
func runHookForTest(t *testing.T, bd, dir string, env []string, hook string, args ...string) (string, string) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"hooks", "run", hook}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd hooks run %s: %v\nstdout:\n%s\nstderr:\n%s", hook, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func bdExportTo(t *testing.T, bd, dir, path string) {
	t.Helper()
	cmd := exec.Command(bd, "export", "-o", path)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd export -o %s: %v\n%s", path, err, out)
	}
}

func gitInHookTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func appendFileForHookTest(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
