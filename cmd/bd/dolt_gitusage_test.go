//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/gittraceenv"
	"github.com/steveyegge/beads/internal/storage"
)

// sqlGitUsageHints is the failure GH#6472 reported: Dolt's git transport
// surfaces git's usage banner — here for the ls-remote separator an old git
// rejects — wearing the credential hints its auth wrapper appends to every
// git failure.
const sqlGitUsageHints = "usage: git ls-remote [--heads] [--tags] [-u <exec> | --upload-pack <exec>] " +
	"[<repository> [<refs>...]]\n" +
	"hint: dolt does not support interactive credential prompts\n" +
	"hint: configure git credentials (credential helper, token) for HTTPS remotes\n" +
	"hint: run `ssh-add <key>` to pre-load your key for SSH remotes\n" +
	"hint: ensure non-interactive auth is configured for GCM"

// cliGitUsageLease is the same refusal on the push side, where an old git
// rejects Dolt's --force-with-lease=<dst>:<expected> argument with git push's
// banner.
const cliGitUsageLease = "cannot parse expected object name ''\n" +
	"usage: git push [--all | --mirror | --tags] [--receive-pack=<git-receive-pack>] [<repository> [<refspec>...]]"

// gitUsageGuidancePhrases are what the transport guidance pins: that git
// refused the arguments of a command Dolt ran, that the appended credential
// hints are Dolt's wrapper and not an authentication finding, the executable
// checks for old-git-on-PATH, the server-mode PATH and restart note, and the
// file-target trace capture.
var gitUsageGuidancePhrases = []string{
	"Git rejected the arguments of a git command Dolt's remote transport ran",
	"they do not establish an\nauthentication problem.",
	"git --version",
	"which -a git",
	"the Dolt server runs git with the PATH it started with",
	"bd dolt stop; the next bd command starts it again",
	"bd removes GIT_TRACE=1 before Dolt runs git",
	"sync-setup.md#usage-git-banner-on-push-pull-or-bootstrap",
}

// gitTraceRecipeLine is the guidance's Unix trace recipe: GIT_TRACE's value,
// then the bd command it re-runs.
var gitTraceRecipeLine = regexp.MustCompile(`(?m)^  GIT_TRACE=(\S+) (.+)$`)

// requireGitTraceRecipe checks that the guidance re-runs the command that
// failed (rerun) with GIT_TRACE set to a value bd hands to the git that
// failed: gittraceenv removes the stderr forms (GIT_TRACE=1) around every
// remote call Dolt makes, so a recipe relying on them traces nothing.
func requireGitTraceRecipe(t *testing.T, stderr, rerun string) {
	t.Helper()
	m := gitTraceRecipeLine.FindStringSubmatch(stderr)
	if m == nil {
		t.Errorf("stderr has no GIT_TRACE recipe line:\n%s", stderr)
		return
	}
	if m[2] != rerun {
		t.Errorf("trace recipe re-runs %q, want %q:\n%s", m[2], rerun, stderr)
	}
	if kept := gittraceenv.ScrubEnv([]string{"GIT_TRACE=" + m[1]}); len(kept) != 1 {
		t.Errorf("trace recipe sets GIT_TRACE=%s, which bd removes before git runs:\n%s", m[1], stderr)
	}
	if want := `PowerShell: $env:GIT_TRACE="$env:TEMP\bd-git-trace.log"; ` + rerun + "\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks the PowerShell recipe %q:\n%s", want, stderr)
	}
}

// requireGitUsageGuidance checks that stderr carries the transport guidance
// exactly once, with the unchanged Error/warning line in front of it, a trace
// recipe that re-runs rerun, and none of the other recovery guidance (which
// names remedies this failure does not have).
func requireGitUsageGuidance(t *testing.T, stderr, lead, rerun string) {
	t.Helper()
	for _, want := range gitUsageGuidancePhrases {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if got := strings.Count(stderr, "Git rejected the arguments"); got != 1 {
		t.Errorf("guidance appears %d times, want exactly once:\n%s", got, stderr)
	}
	if !strings.HasPrefix(stderr, lead) {
		t.Errorf("stderr does not start with the unchanged %q:\n%s", lead, stderr)
	}
	requireGitTraceRecipe(t, stderr, rerun)
	for _, unwanted := range []string{
		"histories have diverged",
		"different primary keys",
		"Dolt refused to update the remote",
		"credentials, network, VPN",
	} {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("stderr has %q:\n%s", unwanted, stderr)
		}
	}
}

// TestDoltPushGitUsageGuidance: a Git usage refusal during bd dolt push — the
// default remote's SQL route and a named remote's CLI route — keeps its Error:
// line and exit, and is followed once by the shared argument-refusal guidance
// instead of silent exit or a misleading auth remedy.
func TestDoltPushGitUsageGuidance(t *testing.T) {
	t.Run("default remote, sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: sqlGitUsageHints}
		pushErr := fmt.Errorf("failed to push to origin/main: %w", sqlErr)
		stderr, err := runFailingDoltPush(t, pushErr, "")
		requireSilentExit(t, err, stderr)
		requireGitUsageGuidance(t, stderr, "Error: "+pushErr.Error()+"\n", "bd dolt push")
	})

	t.Run("named remote, cli route", func(t *testing.T) {
		pushErr := fmt.Errorf("dolt push failed: %s: %w", cliGitUsageLease, errors.New("exit status 129"))
		stderr, err := runFailingDoltPush(t, pushErr, "backup")
		requireSilentExit(t, err, stderr)
		requireGitUsageGuidance(t, stderr, "Error: "+pushErr.Error()+"\n", "bd dolt push --remote backup")
		if unwanted := `Remote "backup" is not configured.`; strings.Contains(stderr, unwanted) {
			t.Errorf("stderr has %q:\n%s", unwanted, stderr)
		}
	})
}

// failingPullStore is a store with an origin remote whose pulls fail with
// pullErr.
type failingPullStore struct {
	storage.DoltStorage
	pullErr error
}

func (s *failingPullStore) Pull(context.Context) error { return s.pullErr }
func (s *failingPullStore) PullRemote(context.Context, string) error {
	return s.pullErr
}
func (s *failingPullStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return []storage.RemoteInfo{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/o/r"}}, nil
}

// runDoltPull runs `bd dolt pull` (with --remote when remote is set) in
// embedded mode against st, returning stderr and RunE's error.
func runDoltPull(t *testing.T, st storage.DoltStorage, remote string) (string, error) {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	pinStorageMode(t, false)
	store = st
	if err := doltPullCmd.Flags().Set("remote", remote); err != nil {
		t.Fatalf("set --remote: %v", err)
	}
	t.Cleanup(func() { _ = doltPullCmd.Flags().Set("remote", "") })
	var runErr error
	_, stderr := captureStdio(t, func() { runErr = doltPullCmd.RunE(doltPullCmd, nil) })
	return stderr, runErr
}

// TestDoltPullGitUsageGuidance: the same refusal during bd dolt pull, on both
// the named-remote and default-remote arms.
func TestDoltPullGitUsageGuidance(t *testing.T) {
	t.Run("named remote, sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: sqlGitUsageHints}
		pullErr := fmt.Errorf("failed to pull from origin/main: %w", sqlErr)
		stderr, err := runDoltPull(t, &failingPullStore{pullErr: pullErr}, "backup")
		requireSilentExit(t, err, stderr)
		requireGitUsageGuidance(t, stderr, "Error: "+pullErr.Error()+"\n", "bd dolt pull --remote backup")
	})

	t.Run("default remote", func(t *testing.T) {
		pullErr := errors.New("fetch failed: " + sqlGitUsageHints)
		stderr, err := runDoltPull(t, &failingPullStore{pullErr: pullErr}, "")
		requireSilentExit(t, err, stderr)
		requireGitUsageGuidance(t, stderr, "Error: "+pullErr.Error()+"\n", "bd dolt pull")
	})
}
