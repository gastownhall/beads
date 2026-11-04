package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureContextText returns what printContextText writes to stdout.
func captureContextText(t *testing.T, info ContextInfo) string {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	path := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}

	oldStdout := os.Stdout
	func() {
		defer func() { os.Stdout = oldStdout }()
		os.Stdout = f
		printContextText(info)
	}()

	if err := f.Close(); err != nil {
		t.Fatalf("close stdout capture: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	return string(out)
}

// The degraded state this command exists to report must survive into the TEXT
// output, not only into --json's omitted field.
//
// `cwd repo` printed only when it was non-empty AND different from the repo
// root, so an empty one — git answered nothing for the working directory —
// vanished from the human-readable output entirely. A JSON consumer could
// still infer it from the absent key; a person reading the output could not
// see that anything was unusual.
func TestPrintContextText_SaysWhenGitCouldNotAnswer(t *testing.T) {
	out := captureContextText(t, ContextInfo{
		BdVersion: "test",
		BeadsDir:  "/ws/.beads",
		RepoRoot:  "/ws",
		// Empty: no git root for the working directory.
		CWDRepoRoot: "",
	})

	if !strings.Contains(out, "cwd repo:") {
		t.Fatalf("text output says nothing about the working directory's repo:\n%s", out)
	}
	if !strings.Contains(out, "git: unavailable") {
		t.Errorf("empty cwd repo root is not reported as git being unavailable:\n%s", out)
	}
}

// The two cases that must NOT gain the line: a CWD inside the same repository
// as the workspace (nothing worth saying), and one inside a different repo
// (the existing disclosure, which is a path and not a diagnosis).
func TestPrintContextText_CWDRepoRootCases(t *testing.T) {
	t.Run("same repo prints no cwd repo line", func(t *testing.T) {
		out := captureContextText(t, ContextInfo{
			BdVersion:   "test",
			BeadsDir:    "/ws/.beads",
			RepoRoot:    "/ws",
			CWDRepoRoot: "/ws",
		})
		if strings.Contains(out, "cwd repo:") {
			t.Errorf("cwd repo line printed when it matches the repo root:\n%s", out)
		}
	})

	t.Run("different repo prints the path", func(t *testing.T) {
		out := captureContextText(t, ContextInfo{
			BdVersion:   "test",
			BeadsDir:    "/ws/.beads",
			RepoRoot:    "/ws",
			CWDRepoRoot: "/other",
		})
		if !strings.Contains(out, "cwd repo:     /other") {
			t.Errorf("cwd repo line missing the working directory's own repo root:\n%s", out)
		}
		if strings.Contains(out, "git: unavailable") {
			t.Errorf("a resolvable cwd repo root must not be reported as unavailable:\n%s", out)
		}
	})
}
