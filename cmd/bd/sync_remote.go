package main

import (
	"context"
	"fmt"
	"os"
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

// resolveSyncRemoteRef returns the configured sync.remote-ref, or "" when it
// is unset or names Dolt's default ref. A committed value that fails the ref
// rules (bd config set refuses them, but a hand-edited file does not) is
// ignored with a warning rather than handed to dolt or git.
func resolveSyncRemoteRef() string {
	return usableSyncRemoteRef(config.GetString(syncRemoteRefKey))
}

// resolveSyncRemoteRefFromDir is resolveSyncRemoteRef against a specific
// beads directory's config.yaml, in either spelling of the key (a flat
// `sync.remote-ref:` line or a nested `sync:` block).
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
// (absolute, or relative with ./ or ../), or a scheme-less host/path, all
// of which dolt rewrites to the matching git+ scheme.
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
	if colon := strings.IndexByte(url, ':'); colon > 0 {
		host := url[:colon]
		// scp-style: dolt takes the host only with a dot or an @ in it, so a
		// Windows drive letter is not mistaken for a host.
		return !strings.Contains(host, "/") && (strings.Contains(host, ".") || strings.Contains(host, "@")) && colon < len(url)-1
	}
	// A local path (absolute, ./ or ../) becomes git+file; anything else
	// scheme-less is host/path and becomes git+https.
	return true
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

// ensureSyncRemoteRefCleared finishes a `bd config unset sync.remote-ref`:
// unset comments out a flat key line only, so a key stored in a nested
// sync: block survives it. When it does, the empty value is written, which
// means the default (#5760 owns the general nested unset).
func ensureSyncRemoteRefCleared() error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil
	}
	if value, _ := config.WorkspaceYamlValue(beadsDir, syncRemoteRefKey); strings.TrimSpace(value) == "" {
		return nil
	}
	return config.SetYamlConfigInDir(beadsDir, syncRemoteRefKey, "")
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
	if err := config.UnsetYamlConfig("sync.remote"); err != nil {
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
// Dolt-native URLs (dolthub://, file://, aws://, gs://, git+...) are
// returned as-is. Git URLs (https://, ssh://, git@...) are converted
// via gitURLToDoltRemote. Unknown schemes are returned as-is and let
// dolt clone decide.
func normalizeRemoteURL(url string) string {
	return doltremote.Normalize(url)
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
