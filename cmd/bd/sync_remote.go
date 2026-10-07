package main

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/storage"
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

// resolveSyncRemoteFromDir is like resolveSyncRemote but reads from a
// specific beads directory's config.yaml. Used by context_cmd, doctor,
// and other paths that operate on a resolved beads dir rather than CWD.
func resolveSyncRemoteFromDir(beadsDir string) string {
	if v := config.GetStringFromDir(beadsDir, "sync.remote"); v != "" {
		return v
	}
	return config.GetStringFromDir(beadsDir, "sync.git-remote")
}

// syncRemoteRefKey names, in .beads/config.yaml, the git ref a git-backed
// sync remote keeps its Dolt data on. Unset means Dolt's default,
// refs/dolt/data. It applies to the remote named origin: bd dolt remote add
// origin --ref writes it, and bd bootstrap, bd init, and the origin probe
// read it.
const syncRemoteRefKey = "sync.remote-ref"

// syncRemoteRefEnv is the environment override of sync.remote-ref, the name
// the merged configuration derives from the key (the BD prefix, dots and
// dashes as underscores).
const syncRemoteRefEnv = "BD_SYNC_REMOTE_REF"

// resolveSyncRemoteRef returns the sync.remote-ref that applies to the
// current workspace, or "" when it is unset or names Dolt's default ref. A
// committed value that fails the ref rules (bd config set refuses them, but
// a hand-edited file does not) is ignored with a warning rather than handed
// to dolt or git.
func resolveSyncRemoteRef() string {
	return resolveSyncRemoteRefIn(beads.FindBeadsDir())
}

// resolveSyncRemoteRefIn resolves the ref for the workspace at beadsDir in
// the order the configuration layers are merged: the environment
// (BD_SYNC_REMOTE_REF, an empty value counting as unset, as the merged
// reader has it), config.local.yaml, the workspace's config.yaml, and then
// the merged configuration, which at that point can only hold the
// user-global file's value. The two workspace files are read directly, in
// either spelling of the key, rather than through the merged reader: the
// key is the workspace's statement of where its data lives, so a committed
// empty value must pin the default for every clone even when the
// user-global config.yaml names a ref, and the merged reader would let a
// flat `sync.remote-ref:` line in that file shadow a nested workspace value,
// since its lookup tries the joined path before the nested one. A workspace
// file the strict reader cannot parse falls through to the merged reader,
// which Initialize has already failed on in that case.
func resolveSyncRemoteRefIn(beadsDir string) string {
	if value := os.Getenv(syncRemoteRefEnv); value != "" {
		return usableSyncRemoteRef(value)
	}
	if beadsDir != "" {
		if value, present, err := config.WorkspaceYamlValueStrictWithLocal(beadsDir, syncRemoteRefKey); err == nil && present {
			return usableSyncRemoteRef(value)
		}
	}
	return usableSyncRemoteRef(config.GetString(syncRemoteRefKey))
}

// resolveSyncRemoteRefFromDir reads the sync.remote-ref recorded in one
// config.yaml, in either spelling of the key, and nothing else: no
// environment, no local layer, no user-global fallback. It answers "what
// does this file say", for the bootstrap paths that persist or clear the
// key in a workspace they are creating; the ref that applies to a workspace
// is resolveSyncRemoteRefIn.
func resolveSyncRemoteRefFromDir(beadsDir string) string {
	value, _ := config.WorkspaceYamlValue(beadsDir, syncRemoteRefKey)
	return usableSyncRemoteRef(value)
}

var warnedBadSyncRemoteRef sync.Once

func usableSyncRemoteRef(value string) string {
	if err := config.ValidateGitDataRef(value); err != nil {
		warnedBadSyncRemoteRef.Do(func() {
			fmt.Fprintf(os.Stderr, "Warning: ignoring %s: %v\n", syncRemoteRefKey, err)
		})
		return ""
	}
	return canonicalGitDataRef(value)
}

// isGitBackedDoltRemoteURL reports whether dolt treats url as a git-backed
// remote, the only kind that can carry a git data ref. It mirrors dolt's
// NormalizeGitRemoteUrl: an explicit git+ scheme (file, http, https, ssh),
// or, when the URL ends in .git, a file/http/https/ssh URL, an scp-style
// [user@]host:path whose host carries a dot or an @, a local path
// (absolute on this platform, or relative with ./ or ../), or a scheme-less
// host/path, all of which dolt rewrites to the matching git+ scheme.
func isGitBackedDoltRemoteURL(url string) bool {
	url = strings.TrimSpace(url)
	lower := strings.ToLower(url)
	for _, prefix := range []string{"git+https://", "git+http://", "git+ssh://", "git+file://"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	base := url
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if !strings.HasSuffix(base, ".git") {
		return false
	}
	if i := strings.Index(lower, "://"); i >= 0 {
		switch lower[:i] {
		case "file", "http", "https", "ssh":
			return true
		}
		return false
	}
	if strings.IndexByte(url, ':') > 0 {
		// scp-style: dolt takes the host only with a dot or an @ in it, so a
		// Windows drive letter is not mistaken for a host. Anything else with
		// a colon falls through to the path rules, as it does in dolt.
		if isScpLikeForDolt(url) || looksLikeLocalPathForDolt(url) {
			return true
		}
		// Scheme-less host/path becomes git+https when it parses as a URL; a
		// colon that is not a port (host:org/repo.git) does not.
		u, err := neturl.Parse("git+https://" + url)
		return err == nil && u.Host != ""
	}
	// A local path (absolute, ./ or ../) becomes git+file; anything else
	// scheme-less is host/path and becomes git+https.
	return true
}

// isScpLikeForDolt mirrors dolt's isScpLikeGitRemote: no scheme, a host
// before the first colon with no slash in it and a dot or an @, and a path
// after it.
func isScpLikeForDolt(url string) bool {
	if strings.Contains(url, "://") {
		return false
	}
	colon := strings.IndexByte(url, ':')
	if colon <= 0 || colon == len(url)-1 {
		return false
	}
	host := url[:colon]
	return !strings.Contains(host, "/") && (strings.Contains(host, ".") || strings.Contains(host, "@"))
}

// looksLikeLocalPathForDolt mirrors dolt's looksLikeLocalPath: absolute on
// this platform (a drive-letter or UNC path on Windows), or relative with ./
// or ../.
func looksLikeLocalPathForDolt(url string) bool {
	return filepath.IsAbs(url) || strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../")
}

var warnedRefOnNonGitRemote sync.Once

// dataRefForRemoteURL is the ref a remote at url is worked on: configured when
// dolt treats url as git-backed, else the default, with one warning naming
// sync.remote-ref, since dolt's own refusal of a ref on a non-git remote
// never names the key.
func dataRefForRemoteURL(url, configured string) string {
	ref, ok := refForAdoptedRemote(url, configured)
	if !ok {
		warnedRefOnNonGitRemote.Do(func() {
			fmt.Fprintf(os.Stderr, "Warning: ignoring %s %q: %s is not a git-backed Dolt remote, so its data has no git ref. Clear the key with: bd config set %s \"\"\n", syncRemoteRefKey, configured, url, syncRemoteRefKey)
		})
	}
	return ref
}

// syncRemoteRefForURL is the configured sync.remote-ref as it applies to the
// remote at url (see dataRefForRemoteURL).
func syncRemoteRefForURL(url string) string {
	return dataRefForRemoteURL(url, resolveSyncRemoteRef())
}

// validateRefFlagArg runs --ref validation in cobra's argument phase, before
// the root command's persistent pre-run opens storage, so a malformed ref
// is refused without touching a database.
func validateRefFlagArg(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("ref") {
		return nil
	}
	refFlag, _ := cmd.Flags().GetString("ref")
	_, err := validateGitDataRef(refFlag)
	return err
}

// canonicalGitDataRef trims ref and maps Dolt's default to "", so that an
// explicit refs/dolt/data and an unset value behave the same everywhere.
func canonicalGitDataRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == storage.DefaultGitDataRef {
		return ""
	}
	return ref
}

// validateGitDataRef checks a user-supplied --ref value with the same rules
// bd config set applies to sync.remote-ref (config.ValidateGitDataRef); an
// empty flag is an error here, since the flag was given. The result is
// canonical: an explicit refs/dolt/data becomes "".
func validateGitDataRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", fmt.Errorf("--ref cannot be empty")
	}
	if err := config.ValidateGitDataRef(trimmed); err != nil {
		return "", fmt.Errorf("--ref %w", err)
	}
	return canonicalGitDataRef(trimmed), nil
}

// clearOriginSyncConfig removes the sync.remote that `bd dolt remote add
// origin` wrote to config.yaml. sync.remote-ref stays: it is the workspace's
// statement of where its data lives, read by bootstrap, init, and the next
// add of origin, so `remove origin` followed by `add origin <url>` lands on
// the same ref instead of moving the data to the default silently. Only an
// explicit --ref refs/dolt/data clears it.
func clearOriginSyncConfig() {
	if current := config.GetYamlConfig("sync.remote"); current == "" {
		return
	}
	if _, err := config.UnsetYamlConfig("sync.remote"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear sync.remote from config.yaml: %v\n", err)
	}
	if isGitRepo() {
		commitBeadsConfig("bd: clear sync.remote")
	}
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
// HTTP(S) userinfo is transport credentials and is dropped whole. SSH userinfo
// selects the remote account (git@host is not a secret and is needed for the
// hint to be runnable), so only a password component is dropped. A
// scheme-less host/path is https for dolt (NormalizeGitRemoteUrl), so its
// userinfo is dropped whole too, and the result is spelled https://: the
// userinfo was what kept a dotted host from reading as an scp address
// (x-access-token:tok@host.example:443/repo.git is https for dolt and git
// both; host.example:443/repo.git would be ssh for both), and the result is
// persisted and pasted as a URL, not only shown. An scp form and a local
// path have no place for a password.
func redactRemoteURL(raw string) string {
	sep := strings.Index(raw, "://")
	if sep < 0 {
		if isScpLikeForDolt(raw) || looksLikeLocalPathForDolt(raw) {
			return raw
		}
		authority, tail := raw, ""
		if slash := strings.Index(raw, "/"); slash >= 0 {
			authority, tail = raw[:slash], raw[slash:]
		}
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			return "https://" + authority[at+1:] + tail
		}
		return raw
	}
	scheme, rest := raw[:sep], raw[sep+3:]
	authority, tail := rest, ""
	if slash := strings.Index(rest, "/"); slash >= 0 {
		authority, tail = rest[:slash], rest[slash:]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw
	}
	userinfo, host := authority[:at], authority[at+1:]
	if isDoltSSHScheme(scheme) {
		if colon := strings.Index(userinfo, ":"); colon >= 0 {
			userinfo = userinfo[:colon]
		}
		return scheme + "://" + userinfo + "@" + host + tail
	}
	return scheme + "://" + host + tail
}

func isDoltSSHScheme(scheme string) bool {
	return scheme == "ssh" || scheme == "git+ssh"
}

// urlWithUserinfoRe matches the "scheme://userinfo@host" span of a URL embedded
// anywhere in free-form text — for example the git stderr line bootstrap folds
// into a probe error, which can echo the very remote (credentials and all) that
// git failed to reach. The userinfo run stops at the first "@" and the host run
// at the first "/" so a trailing path is left for redactRemoteURL to keep.
var urlWithUserinfoRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^@\s'"]*@[^/\s'"]*`)

// scrubURLCredentials redacts credential-bearing URLs embedded anywhere in s by
// running each match through redactRemoteURL, so the same rule applied to the
// plan's own URL fields (http(s) drops userinfo, ssh drops only a password) also
// covers URLs that arrive inside arbitrary text. Text with no "userinfo@" URL is
// returned unchanged, so credential-free git diagnostics keep their full detail.
func scrubURLCredentials(s string) string {
	return urlWithUserinfoRe.ReplaceAllStringFunc(s, redactRemoteURL)
}

// gitOriginDoltURL spells the git origin as the Dolt remote it is cloned or
// adopted from. Every URL shape but one normalizes to a git+ scheme; a local
// path without a .git suffix stays a path, which dolt reads as a DoltHub
// remote (a scheme-less, host-less name goes to the remotesapi host), not as
// a git repository. When a ref is in play the origin has just been probed as
// a git repository holding Dolt data on that ref, so it is spelled
// git+file:// to say so.
func gitOriginDoltURL(originURL, dataRef string) string {
	url := normalizeRemoteURL(originURL)
	if dataRef != "" && strings.HasPrefix(url, "/") && !isGitBackedDoltRemoteURL(url) {
		return "git+file://" + url
	}
	return url
}
