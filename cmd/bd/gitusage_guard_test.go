//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// guardSqlUsageRefusal is a Git usage refusal as Dolt's remote transport
// surfaces it (GH#6472): git's own usage banner wearing the credential hints
// Dolt's wrapper appends to every git failure.
const guardSqlUsageRefusal = "usage: git push [--all | --mirror | --tags] [--receive-pack=<git-receive-pack>] [<repository> [<refspec>...]]\n" +
	"hint: dolt does not support interactive credential prompts"

// guardGuidanceLead is the shared argument-refusal guidance's first line; the
// guard counts it rather than the whole block so wrapping cannot fool it.
const guardGuidanceLead = "Git rejected the arguments"

// succeedingPushStore is failingPushStore with a push that succeeds.
type succeedingPushStore struct {
	failingPushStore
}

func (s *succeedingPushStore) Push(context.Context) error      { return nil }
func (s *succeedingPushStore) ForcePush(context.Context) error { return nil }
func (s *succeedingPushStore) PushRemote(context.Context, string, bool) error {
	return nil
}

// countingPushStore is failingPushStore that counts the transfer attempts.
type countingPushStore struct {
	failingPushStore
	transfers int
}

func (s *countingPushStore) Push(context.Context) error {
	s.transfers++
	return s.pushErr
}
func (s *countingPushStore) ForcePush(context.Context) error {
	s.transfers++
	return s.pushErr
}
func (s *countingPushStore) PushRemote(_ context.Context, _ string, _ bool) error {
	s.transfers++
	return s.pushErr
}

// TestGitUsageRefusalRegressionGuard pins what the argument-refusal guidance
// must NOT change: a successful push keeps its clean output, a diverged
// push keeps its own (correct) guidance, and a usage refusal gets the shared
// guidance exactly once — from one transfer attempt, with bd probing no Git
// version itself (the guidance tells the user to run git --version; bd
// running it would report the wrong environment: server mode runs the git on
// the server's PATH).
func TestGitUsageRefusalRegressionGuard(t *testing.T) {
	t.Run("successful push keeps its clean output", func(t *testing.T) {
		st := &succeedingPushStore{failingPushStore: failingPushStore{pushErr: nil}}
		stderr, err := runDoltPush(t, st, "", false)
		if err != nil {
			t.Fatalf("RunE = %v, want a clean success\n%s", err, stderr)
		}
		if strings.Contains(stderr, guardGuidanceLead) {
			t.Errorf("a successful push printed failure guidance:\n%s", stderr)
		}
	})

	t.Run("diverged history keeps its own guidance", func(t *testing.T) {
		pushErr := errors.New("failed to push to origin/main: no common ancestor")
		stderr, err := runFailingDoltPush(t, pushErr, "")
		requireSilentExit(t, err, stderr)
		if !strings.HasPrefix(stderr, "Error: "+pushErr.Error()+"\n") {
			t.Errorf("stderr does not start with the unchanged Error: line:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Local and remote Dolt histories have diverged.") {
			t.Errorf("stderr lost the diverged-history guidance:\n%s", stderr)
		}
		if strings.Contains(stderr, guardGuidanceLead) {
			t.Errorf("a non-usage failure printed the argument-refusal guidance:\n%s", stderr)
		}
	})

	t.Run("usage refusal: guidance once, one transfer, no git version probe", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell fixture")
		}
		// Record every git invocation a shim-first PATH sees, delegating each
		// to the real git, so the guard can assert bd never ran one itself.
		log := filepath.Join(t.TempDir(), "git-invocations")
		dir := t.TempDir()
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git not on PATH")
		}
		shim := `#!/bin/sh
printf '%s\n' "$*" >> "` + log + `"
exec "` + realGit + `" "$@"
`
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

		st := &countingPushStore{failingPushStore: failingPushStore{pushErr: fmt.Errorf("dolt push failed: %s", guardSqlUsageRefusal)}}
		stderr, err := runDoltPush(t, st, "", false)
		requireSilentExit(t, err, stderr)
		if got := strings.Count(stderr, guardGuidanceLead); got != 1 {
			t.Errorf("guidance appears %d times, want exactly once:\n%s", got, stderr)
		}
		if st.transfers != 1 {
			t.Errorf("push attempted %d transfers, want 1 — a refused invocation must not be retried", st.transfers)
		}
		invocations, readErr := os.ReadFile(log)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if strings.Contains(string(invocations), "--version") {
			t.Errorf("bd probed the git version itself:\n%s", invocations)
		}
	})
}
