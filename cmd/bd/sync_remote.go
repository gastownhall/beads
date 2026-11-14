package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/remotecache"
)

// resolveSyncRemote returns the effective sync remote URL.
// Resolution order:
//  1. sync.remote (primary — any Dolt-compatible remote URL)
//  2. sync.git-remote (deprecated fallback)
//  3. "" (not configured)
func resolveSyncRemote() string {
	if v := config.GetString("sync.remote"); v != "" {
		return v
	}
	return config.GetString("sync.git-remote")
}

// resolveSyncRemoteChecked is resolveSyncRemote for paths that hand the value
// to git or dolt: a committed config.yaml can set it, so a value git would
// parse as an option is refused, naming the key that set it.
func resolveSyncRemoteChecked() (string, error) {
	key := "sync.remote"
	v := config.GetString(key)
	if v == "" {
		key = "sync.git-remote"
		v = config.GetString(key)
	}
	if err := checkRemoteNotOption(key, v); err != nil {
		return "", err
	}
	// GH#6233: a sync remote the allowlist rejects is never cloned from or
	// wired in; the refusal names the key, not the URL.
	if v != "" {
		if err := remotecache.CheckRemotePolicy(key, v, config.AllowedRemotePatterns()); err != nil {
			return "", err
		}
	}
	return v, nil
}

// checkRemoteNotOption rejects a remote URL that a program it reaches would
// read as an option (remotecache.OptionShapedPart): one that begins with
// "-", including after a "git+" scheme prefix, which git and dolt would read
// as an option (e.g. --upload-pack=<command> or -u<command>), and an ssh://
// URL whose user name or host begins with "-", which Dolt's ssh transport
// hands to ssh as an option (e.g. -oProxyCommand=<command>).
func checkRemoteNotOption(source, remote string) error {
	switch part := remotecache.OptionShapedPart(remote); part {
	case "":
		return nil
	case remotecache.OptionShapedURL:
		return fmt.Errorf("%s %q is not a valid remote URL: it must not start with '-'", source, redactRemoteURL(remote))
	default:
		return fmt.Errorf("%s %q is not a valid remote URL: its %s must not start with '-'", source, redactRemoteURL(remote), part)
	}
}

// resolveSyncRemoteFromDir is like resolveSyncRemote but reads from a
// specific beads directory's config.yaml. Used by context_cmd, doctor,
// and other paths that operate on a resolved beads dir rather than CWD.
func resolveSyncRemoteFromDir(beadsDir string) string {
	if v := config.GetStringFromDir(beadsDir, "sync.remote"); v != "" {
		return v
	}
	return config.GetStringFromDir(beadsDir, "sync.git-remote")
}

// commitBeadsConfig stages .beads/config.yaml and commits it.
// Silently no-ops if the file is clean or the commit fails (e.g. hooks,
// nothing to commit). Used by bd dolt remote add/remove to keep the
// working tree clean after persisting sync.remote.
func commitBeadsConfig(msg string) {
	commitBeadsConfigForActiveRepo(context.Background(), msg)
}

func commitBeadsConfigForActiveRepo(ctx context.Context, msg string) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return
	}
	addCmd := rc.GitCmd(ctx, "add", ".beads/config.yaml")
	if err := addCmd.Run(); err != nil {
		return
	}
	commitCmd := rc.GitCmd(ctx, "commit", "-m", msg)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "nothing to commit") {
			fmt.Fprintf(os.Stderr, "Warning: failed to commit config change: %v\n", err)
		}
	}
}

// normalizeRemoteURL converts a remote URL to a Dolt-compatible format.
// See doltremote.Normalize for the scheme rules - this wrapper adds nothing and
// exists only so call sites in this package read against one local name.
// Restating those rules here drifts: this comment still omitted s3:// after the
// source gained it, and named gitURLToDoltRemote as the converter when the call
// below goes to doltremote.Normalize directly.
func normalizeRemoteURL(url string) string {
	return doltremote.Normalize(url)
}

// doltRemoteURL returns the form of remote that bd hands to Dolt — for
// DOLT_CLONE, for DOLT_REMOTE('add', ...), and therefore for DOLT_PUSH.
//
// A git-forge URL must reach Dolt in its git+ form. Dolt's dbfactory routes by
// scheme: raw http(s):// goes to the remotesapi client, which speaks Dolt's
// wire protocol at github.com and retries indefinitely (#4421), while
// git+https/git+ssh goes to the git remote factory, which shells out to git
// and fails cleanly.
//
// Everything else is returned byte-identical. That preserves GH#3339: a
// user-configured Dolt remotesapi endpoint (http://myserver:7007/mydb) must
// never be rewritten to git+http://, and it never classifies as a forge URL.
//
// This is the single owner of that routing rule. bd init and bd bootstrap must
// both call it: if they disagree about the URL derived from one committed
// sync.remote, that is the #5743 class of skew all over again.
func doltRemoteURL(remote string) string {
	if isGitCodeRepoURL(remote) {
		return normalizeRemoteURL(remote)
	}
	return remote
}

// redactRemoteURL strips credentials from a remote URL so it is safe to echo
// into errors, hints, logs and JSON. CI commonly configures sync.remote as
// https://x-access-token:<token>@github.com/org/repo.git, and the clone funnel
// already scrubs userinfo before reporting (versioncontrolops.sanitizeURL);
// diagnostics must not be the hole in that convention.
//
// See doltremote.Redact for the rule (HTTP(S) userinfo is dropped whole, an SSH
// account is kept and only its password dropped). Like normalizeRemoteURL, this
// wrapper adds nothing: the storage layer redacts Dolt's transfer errors with
// the same function, so the rule has one copy to fix.
func redactRemoteURL(raw string) string {
	return doltremote.Redact(raw)
}

// scrubURLCredentials redacts credential-bearing URLs embedded anywhere in s,
// for example the git stderr line bootstrap folds into a probe error, which can
// echo the very remote (credentials and all) that git failed to reach. Each URL
// gets redactRemoteURL's rule, and text with no "userinfo@" URL is returned
// unchanged, so credential-free git diagnostics keep their full detail. See
// doltremote.RedactText.
func scrubURLCredentials(s string) string {
	return doltremote.RedactText(s)
}
