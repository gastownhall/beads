package dberrors

import (
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// sqlGitUsageRefusal is the refusal GH#6472 captured over SQL: Dolt's embedded
// git transport spawned `git --git-dir <cache> ls-remote --heads -- <url>`,
// the git on PATH rejected the `--` separator with its usage banner, and
// Dolt's gitauth wrapper appended credential hints to the banner.
const sqlGitUsageRefusal = "usage: git ls-remote [--heads] [--tags] [-u <exec> | --upload-pack <exec>] " +
	"[<repository> [<refs>...]]\n" +
	"hint: dolt does not support interactive credential prompts\n" +
	"hint: configure git credentials (credential helper, token) for HTTPS remotes\n" +
	"hint: run `ssh-add <key>` to pre-load your key for SSH remotes\n" +
	"hint: ensure non-interactive auth is configured for GCM"

// cliGitUsageRefusal is the same refusal through the server-mode CLI route,
// where an old git rejects Dolt's --force-with-lease=<dst>:<expected> push
// argument with git push's banner instead of ls-remote's.
const cliGitUsageRefusal = "dolt push failed: cannot parse expected object name ''\n" +
	"usage: git push [--all | --mirror | --tags] [--receive-pack=<git-receive-pack>]" +
	" [<repository> [<refspec>...]]: exit status 129"

// probeGitUsageRefusal is bd's own ls-remote probe surfacing the banner.
const probeGitUsageRefusal = "git ls-remote: exit status 129: " +
	"usage: git ls-remote [--heads] [--tags] [-u <exec> | --upload-pack <exec>] [<repository> [<refs>...]]"

// TestIsGitUsageRefusal: git's usage banner — with or without Dolt's appended
// credential hints, through SQL wrapping or bd's probe — is an invocation
// refusal, while authentication and the existing storage refusals, which
// never carry the banner, stay unclassified.
func TestIsGitUsageRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "dolt-wrapped ls-remote refusal over sql",
			err: &mysql.MySQLError{
				Number:   1105,
				SQLState: [5]byte{'H', 'Y', '0', '0', '0'},
				Message:  sqlGitUsageRefusal,
			},
			want: true,
		},
		{
			name: "dolt-wrapped ls-remote refusal through cli",
			err:  errors.New(sqlGitUsageRefusal),
			want: true,
		},
		{
			name: "push lease refusal through cli",
			err:  errors.New(cliGitUsageRefusal),
			want: true,
		},
		{name: "bd probe error", err: errors.New(probeGitUsageRefusal), want: true},
		{name: "upper case banner", err: errors.New("Usage: git ls-remote [--heads]"), want: true},
		{
			name: "wrapped through sync step",
			err:  fmt.Errorf("sync failed: push: %w", errors.New(sqlGitUsageRefusal)),
			want: true,
		},
		{
			// The hints alone do not make a refusal: an authentication failure
			// Dolt wrapped in the same credential hints must not read as an
			// invocation problem.
			name: "auth failure with credential hints",
			err: errors.New("fatal: Could not read from remote repository.\n" +
				"hint: dolt does not support interactive credential prompts\n" +
				"hint: run `ssh-add <key>` to pre-load your key for SSH remotes"),
			want: false,
		},
		{
			name: "remote dangling reference refusal",
			err:  errors.New("unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}"),
			want: false,
		},
		{name: "diverged history", err: errors.New("failed to push to origin/main: no common ancestor"), want: false},
		{
			name: "ancestor pk mismatch",
			err:  errors.New("error: cannot merge because table dependencies has different primary keys in its common ancestor"),
			want: false,
		},
		{name: "another tool's usage banner", err: errors.New("usage: bd doctor [--fix]"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsGitUsageRefusal(tc.err); got != tc.want {
				t.Fatalf("IsGitUsageRefusal(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
