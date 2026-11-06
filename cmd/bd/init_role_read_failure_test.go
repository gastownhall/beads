package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// corruptLocalGitConfig replaces the repository-local config with one git
// cannot parse, so every beads.role read through the role-authority boundary
// fails with git's own diagnostic instead of answering absence.
func corruptLocalGitConfig(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[beads\n\trole = maintainer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPromptContributorModeFailsBeforePromptingOnRoleReadFailure pins
// bd-a2qnf4 (GH#6765) for the interactive init prompt: when the beads.role
// read fails, promptContributorMode must surface that failure before asking
// the user anything, instead of collapsing the failed read into "no role",
// prompting, and then failing on the write the answer triggers.
func TestPromptContributorModeFailsBeforePromptingOnRoleReadFailure(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	target := newGitRepo(t)
	corruptLocalGitConfig(t, target)
	t.Chdir(target)

	// The prompt reads stdin; leave a pending "y" so the unfixed behavior
	// flows all the way through the prompt instead of dying on EOF. The
	// fixed path returns the read failure before touching stdin.
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	if _, err := w.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var promptErr error
	stdout := captureStdout(t, func() error {
		_, promptErr = promptContributorMode()
		return nil
	})
	if promptErr == nil {
		t.Fatal("promptContributorMode must fail when the role read fails")
	}
	if !strings.Contains(promptErr.Error(), "bad config") {
		t.Errorf("error %q must carry git's own diagnostic for the unreadable config", promptErr)
	}
	if strings.Contains(stdout, "Contributing to someone else's repo") {
		t.Errorf("stdout %q prompted for a role before reporting the failed read", stdout)
	}
}

// proxiedRoleReadFailureUseCase fakes the selected-directory repository for
// the proxied init tail: every beads.role read fails, and role writes are
// recorded rather than reaching git.
type proxiedRoleReadFailureUseCase struct {
	domain.GitUseCase
	readErr  error
	roleSets []string
}

func (u *proxiedRoleReadFailureUseCase) IsGitRepo(context.Context) bool { return true }

func (u *proxiedRoleReadFailureUseCase) HasAnyRemotes(context.Context) bool { return false }

func (u *proxiedRoleReadFailureUseCase) BeadsRole(context.Context) (string, bool, error) {
	return "", false, u.readErr
}

func (u *proxiedRoleReadFailureUseCase) SetBeadsRole(_ context.Context, role string) error {
	u.roleSets = append(u.roleSets, role)
	return nil
}

// TestProxiedInitTailRoleReadFailure pins bd-a2qnf4 (GH#6765) for the
// proxied init tail: a failed beads.role read must warn and keep any
// existing role untouched rather than collapsing into absence and writing
// the default, while an explicit --role flag still carries user intent.
func TestProxiedInitTailRoleReadFailure(t *testing.T) {
	readErr := errors.New("git: GetConfig beads.role: exit status 128: fatal: bad config line 3 in file .git/config")
	for _, tc := range []struct {
		name     string
		flag     string
		wantSets []string
	}{
		{"no_flag_keeps_existing_role", "", nil},
		{"explicit_flag_still_writes", "contributor", []string{"contributor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initRoleReadFailureWarned = false
			gitUC := &proxiedRoleReadFailureUseCase{readErr: readErr}
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", false, "")
			in := initProxiedServerInput{roleFlag: tc.flag, stealth: true, skipHooks: true, skipAgents: true}
			stderr := captureStderr(t, func() {
				if err := runInitProxiedServerTail(cmd, t.Context(), in, runInitTailContext{gitUC: gitUC}); err != nil {
					t.Errorf("runInitProxiedServerTail: %v", err)
				}
			})
			if len(gitUC.roleSets) != len(tc.wantSets) {
				t.Errorf("role writes = %v; want %v: a failed read must not collapse into absence and trigger the default write", gitUC.roleSets, tc.wantSets)
			} else {
				for i, set := range gitUC.roleSets {
					if set != tc.wantSets[i] {
						t.Errorf("role writes = %v; want %v", gitUC.roleSets, tc.wantSets)
						break
					}
				}
			}
			if !strings.Contains(stderr, "could not read beads.role") {
				t.Errorf("stderr %q lacks the read-failure warning", stderr)
			}
			if !strings.Contains(stderr, "bad config") {
				t.Errorf("stderr %q lacks git's diagnostic for the unreadable config", stderr)
			}
		})
	}
}

// TestInitRoleHelpersOnReadFailure pins bd-a2qnf4 (GH#6765) for init's
// direct role writes: over a config git cannot parse, the default-role
// branch warns and writes nothing unless --role was explicit, and the
// GH#2950 safety net never writes at all.
func TestInitRoleHelpersOnReadFailure(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	target := newGitRepo(t)
	corruptLocalGitConfig(t, target)
	t.Chdir(target)

	if _, _, err := getBeadsRole(); err == nil {
		t.Fatal("fixture precondition: getBeadsRole must fail on the malformed config")
	}

	t.Run("getBeadsRole carries git's diagnostic", func(t *testing.T) {
		role, hasRole, err := getBeadsRole()
		if err == nil {
			t.Fatal("getBeadsRole must fail on a config git cannot parse")
		}
		if !strings.Contains(err.Error(), "bad config") {
			t.Errorf("error %q must carry git's own diagnostic", err)
		}
		if hasRole || role != "" {
			t.Errorf("failed read = %q, %t; want empty absence-shaped values", role, hasRole)
		}
	})
	t.Run("default branch writes nothing without an explicit role", func(t *testing.T) {
		initRoleReadFailureWarned = false
		stderr := captureStderr(t, func() { ensureDefaultBeadsRole("", false) })
		if !strings.Contains(stderr, "could not read beads.role") {
			t.Errorf("stderr %q lacks the read-failure warning", stderr)
		}
		if strings.Contains(stderr, "failed to set") {
			t.Errorf("stderr %q shows a write over a failed read", stderr)
		}
	})
	t.Run("default branch honors an explicit role", func(t *testing.T) {
		initRoleReadFailureWarned = false
		stderr := captureStderr(t, func() { ensureDefaultBeadsRole("contributor", false) })
		if !strings.Contains(stderr, "could not read beads.role") {
			t.Errorf("stderr %q lacks the read-failure warning", stderr)
		}
		// The write is attempted and honestly fails on the same unreadable
		// config; only its warning proves it happened.
		if !strings.Contains(stderr, "failed to set") {
			t.Errorf("stderr %q lacks the explicit-role write attempt", stderr)
		}
	})
	t.Run("safety net never writes on a failed read", func(t *testing.T) {
		initRoleReadFailureWarned = false
		stderr := captureStderr(t, func() { ensureFallbackBeadsRole("contributor", false) })
		if !strings.Contains(stderr, "could not read beads.role") {
			t.Errorf("stderr %q lacks the read-failure warning", stderr)
		}
		if strings.Contains(stderr, "failed to set") {
			t.Errorf("stderr %q shows the safety net writing over a failed read", stderr)
		}
	})
	t.Run("warnings dedup within one init run", func(t *testing.T) {
		initRoleReadFailureWarned = false
		stderr := captureStderr(t, func() {
			ensureDefaultBeadsRole("", false)
			ensureFallbackBeadsRole("", false)
		})
		if got := strings.Count(stderr, "could not read beads.role"); got != 1 {
			t.Errorf("stderr %q warns %d times; want exactly one", stderr, got)
		}
	})
}
