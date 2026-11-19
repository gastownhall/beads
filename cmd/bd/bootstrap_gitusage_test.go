//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// cmdBdOldGitUsageBanner is git's argument-parse rejection of the
// separator-bearing ls-remote bd's probe and Dolt's remote transport run.
const cmdBdOldGitUsageBanner = "usage: git ls-remote [--heads] [--tags] [-u <exec> | --upload-pack <exec>] [<repository> [<refs>...]]"

// gitProbeUsageGuidancePhrases are what the probe guidance pins: that git
// refused the arguments of bd's own ls-remote, the old-git-on-PATH checks and
// the troubleshooting entry.
var gitProbeUsageGuidancePhrases = []string{
	"Git rejected the arguments of the git ls-remote bd ran to check the\nremote",
	"git --version",
	"which -a git",
	"sync-setup.md#usage-git-banner-on-push-pull-or-bootstrap",
}

// gitTransportOnlyPhrases belong to the guidance for Dolt's transport alone:
// bd's own probe carries no Dolt credential hints and runs no server's git.
var gitTransportOnlyPhrases = []string{"Dolt's wrapper", "Dolt's remote transport", "server mode", "bd dolt stop"}

// writeProbeOldGitShim installs a `git` first on PATH that rejects the
// separator-bearing ls-remote bd's probe runs (git 2.6.1 rejected the
// separator with usage(), GH#6472), printing a trace line ahead of the banner,
// and delegates every other invocation to the real git binary it returns.
func writeProbeOldGitShim(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	shim := `#!/bin/sh
# fake old git: reject the separator-bearing ls-remote bd's probe runs
# (git 2.6.1 rejected the -- separator with usage()), pass everything else
# through.
seen=
for a in "$@"; do
  if [ "$a" = ls-remote ]; then
    seen=1
  fi
  if [ "$a" = "--" ] && [ "$seen" = 1 ]; then
    echo "trace: built-in: git ls-remote" >&2
    echo "` + cmdBdOldGitUsageBanner + `" >&2
    exit 129
  fi
done
exec "` + realGit + `" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGitLsRemoteProbeOldGitRefusal: the probe's own error, when an old git on
// PATH rejects the separator-bearing ls-remote, keeps git's usage banner —
// even past trace lines ahead of it — and carries the one-line hint that says
// what failed, while other failures keep their first-line diagnosis with
// credentials scrubbed and no hint.
func TestGitLsRemoteProbeOldGitRefusal(t *testing.T) {
	t.Run("usage refusal", func(t *testing.T) {
		writeProbeOldGitShim(t, t.TempDir())

		_, err := gitRemoteHasDoltDataRefStatus("git+ssh://git@github.example/org/repo.git")
		if err == nil {
			t.Fatal("probe with an old git on PATH returned no error")
		}
		if !isGitUsageRefusalErr(err) {
			t.Fatalf("probe error does not classify as a Git usage refusal:\n%v", err)
		}
		if !strings.Contains(err.Error(), cmdBdOldGitUsageBanner) {
			t.Errorf("probe error does not carry the usage banner:\n%v", err)
		}
		if strings.Contains(err.Error(), "trace: built-in") {
			t.Errorf("probe error keeps the trace line ahead of the banner instead of the banner:\n%v", err)
		}
		if !strings.Contains(err.Error(), "old or wrapped git on PATH") {
			t.Errorf("probe error carries no hint at what failed:\n%v", err)
		}
	})

	t.Run("auth failure keeps its diagnosis, scrubbed, without the hint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell fixture")
		}
		dir := t.TempDir()
		shim := `#!/bin/sh
echo "fatal: unable to access 'https://user:secret-token@github.example/x': Couldn't connect" >&2
exit 128
`
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := gitRemoteHasDoltDataRefStatus("https://github.example/org/repo.git")
		if err == nil {
			t.Fatal("probe returned no error")
		}
		if isGitUsageRefusalErr(err) {
			t.Errorf("auth failure misclassifies as a Git usage refusal:\n%v", err)
		}
		if !strings.Contains(err.Error(), "fatal: unable to access") {
			t.Errorf("probe error lost git's diagnosis:\n%v", err)
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Errorf("probe error leaks credentials:\n%v", err)
		}
		if strings.Contains(err.Error(), "old or wrapped git on PATH") {
			t.Errorf("probe error carries the usage hint on a non-usage failure:\n%v", err)
		}
	})
}

// TestBootstrapPlanOldGitRefusal: a configured sync.remote whose probe an old
// git on PATH refuses blocks the plan (never clones), names the invocation
// problem, reproduces with the separator the actual probe passes, and prints
// the probe's argument-refusal guidance (bd's own git, so no Dolt hints or
// server PATH) instead of the credentials/network advice.
func TestBootstrapPlanOldGitRefusal(t *testing.T) {
	writeProbeOldGitShim(t, t.TempDir())
	beadsDir := newForgeSyncRemoteWorkspace(t, "https://user:secret-token@github.com/org/repo.git")

	plan := detectBootstrapAction(beadsDir, configfile.DefaultConfig())

	if plan.Action != "none" {
		t.Fatalf("action = %q, want %q (an unverifiable remote must fail closed)", plan.Action, "none")
	}
	if !plan.Blocked {
		t.Error("Blocked=false; an unverifiable remote must not report success")
	}
	if plan.SyncRemote != "" {
		t.Errorf("SyncRemote=%q, want empty on a blocked plan", plan.SyncRemote)
	}
	if plan.BlockedRemote == "" {
		t.Fatal("BlockedRemote empty; the diagnostic needs the remote it could not verify")
	}
	if !isGitUsageRefusalErr(errors.New(plan.Reason)) {
		t.Fatalf("Reason does not classify as a Git usage refusal:\n%s", plan.Reason)
	}
	if strings.Contains(plan.Reason, "secret-token") {
		t.Errorf("Reason leaks credentials:\n%s", plan.Reason)
	}
	if err := bootstrapPlanOutcome(plan, false); err == nil {
		t.Error("bootstrapPlanOutcome = nil; a blocked plan must exit non-zero")
	}

	_, stderr := captureStdio(t, func() { printBootstrapPlan(plan) })

	if !strings.Contains(stderr, "Bootstrap did not set up a database:") {
		t.Errorf("stderr lacks the blocked verdict:\n%s", stderr)
	}
	if want := "Reproduce: git ls-remote -- "; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks the separator-bearing reproduce command %q — without the separator an old git accepts the probe and the reproduction \"succeeds\":\n%s", want, stderr)
	}
	if unwanted := "credentials, network, VPN"; strings.Contains(stderr, unwanted) {
		t.Errorf("stderr recommends %q for a git invocation refusal:\n%s", unwanted, stderr)
	}
	if got := strings.Count(stderr, "Git rejected the arguments"); got != 1 {
		t.Errorf("guidance appears %d times, want exactly once:\n%s", got, stderr)
	}
	for _, want := range gitProbeUsageGuidancePhrases {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	for _, unwanted := range gitTransportOnlyPhrases {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("probe guidance has the transport-only %q:\n%s", unwanted, stderr)
		}
	}
	requireGitTraceRecipe(t, stderr, "bd bootstrap")
	if strings.Contains(stderr, "secret-token") {
		t.Errorf("stderr leaks credentials:\n%s", stderr)
	}
}

// TestBootstrapCloneGitUsageGuidance drives `bd bootstrap` end to end through
// the real calling path: a local-path sync.remote plans a clone, the clone
// fails with Dolt's wrapped usage refusal, and the reported "Bootstrap failed"
// error is followed by the argument-refusal guidance exactly once — while a
// clone failure without the banner keeps its bare error.
func TestBootstrapCloneGitUsageGuidance(t *testing.T) {
	runCloneCase := func(t *testing.T, cloneErr error, wantGuidance bool) string {
		t.Helper()
		// A local path without the .git suffix is a trusted non-forge
		// sync.remote (isGitCodeRepoURL): detection plans the clone without
		// probing it, so the stubbed clone below is the first remote touch.
		repo := filepath.Join(t.TempDir(), "repo")
		newForgeSyncRemoteWorkspace(t, repo)

		oldClone := remoteCloneViaEmbedded
		remoteCloneViaEmbedded = func(context.Context, string, string, string) error { return cloneErr }
		t.Cleanup(func() { remoteCloneViaEmbedded = oldClone })

		saveAndRestoreGlobals(t)
		resetCommandContext()
		oldJSON := jsonOutput
		t.Cleanup(func() { jsonOutput = oldJSON })
		jsonOutput = false
		for name, value := range map[string]string{"dry-run": "false", "yes": "true", "non-interactive": "false"} {
			if err := bootstrapCmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set --%s: %v", name, err)
			}
		}
		// bootstrapCmd is shared: put --yes back, as the sibling bootstrap
		// tests do, so no later test inherits the confirmation.
		t.Cleanup(func() { _ = bootstrapCmd.Flags().Set("yes", "false") })

		var runErr error
		_, stderr := captureStdio(t, func() { runErr = bootstrapCmd.RunE(bootstrapCmd, nil) })
		var exit *exitError
		if !errors.As(runErr, &exit) || exit.Code != 1 {
			t.Fatalf("RunE = %v, want exit 1\n%s", runErr, stderr)
		}
		if want := "Error: Bootstrap failed: " + scrubURLCredentials(cloneErr.Error()) + "\n"; !strings.HasPrefix(stderr, want) {
			t.Errorf("stderr does not start with the unchanged error %q:\n%s", want, stderr)
		}
		return stderr
	}

	t.Run("usage refusal", func(t *testing.T) {
		stderr := runCloneCase(t, errors.New(sqlGitUsageHints), true)
		if got := strings.Count(stderr, "Git rejected the arguments"); got != 1 {
			t.Errorf("guidance appears %d times, want exactly once:\n%s", got, stderr)
		}
		for _, want := range gitUsageGuidancePhrases {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		requireGitTraceRecipe(t, stderr, "bd bootstrap")
	})

	t.Run("other clone failure keeps its bare error", func(t *testing.T) {
		stderr := runCloneCase(t, errors.New("clone from remote: database exists"), false)
		if strings.Contains(stderr, "Git rejected the arguments") {
			t.Errorf("a non-usage clone failure prints Git usage guidance:\n%s", stderr)
		}
	})
}
