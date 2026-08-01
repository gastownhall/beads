package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/steveyegge/beads/internal/githooksenv"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Ref names anchoring a git-backed Dolt remote's data plane, as published by
// dolt's git blobstore (store/blobstore/git_refs.go: DoltDataRef and
// DefaultInfoBranch). Pinned locally so the binary does not import that
// package; TestResetDataRefNamesMatchDolt keeps the pin honest.
const (
	gitDoltDataRef = "refs/dolt/data"
	gitDoltInfoRef = "refs/heads/__dolt_remote_info__"
)

// resetDataKind classifies how reset-data can replace the data plane behind
// a remote URL.
type resetDataKind int

const (
	// resetDataGitBacked: delete the Dolt data refs on the git remote, then
	// force-push to rebuild a fresh store holding only live chunks.
	resetDataGitBacked resetDataKind = iota
	// resetDataFileStore: clear the native Dolt file-store directory, then
	// force-push to rebuild it.
	resetDataFileStore
	// resetDataFileAbsent: the file target is missing or empty — nothing
	// anchors old data; force-push alone rebuilds it.
	resetDataFileAbsent
	// resetDataUnsupported: a cloud or hosted store whose contents bd cannot
	// safely clear — replace the remote with a fresh URL/prefix instead.
	resetDataUnsupported
)

// classifyResetDataRemote decides which reset-data mechanism applies to url.
// File URLs are classified by what is actually on disk, because a file://
// remote can be either a bare git repository (Dolt normalizes those to
// git+file:// at push time) or a native Dolt file store: a bare git repo
// takes the git-backed path, a directory with an nbs manifest is a file
// store, and a missing or empty target has nothing to reset. A non-empty
// target that is neither is an error — reset-data must not clear a
// directory it cannot identify.
func classifyResetDataRemote(url string) (resetDataKind, error) {
	if doltutil.IsGitProtocolURL(url) {
		return resetDataGitBacked, nil
	}
	path, isFile := resetDataFilePath(url)
	if !isFile {
		return resetDataUnsupported, nil
	}
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return resetDataFileAbsent, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspecting remote target %s: %w", path, err)
	}
	if len(entries) == 0 {
		return resetDataFileAbsent, nil
	}
	if isBareGitRepoDir(path) {
		return resetDataGitBacked, nil
	}
	if _, err := os.Stat(filepath.Join(path, "manifest")); err == nil {
		return resetDataFileStore, nil
	}
	return 0, fmt.Errorf("remote target %s is non-empty but is neither a bare git repository nor a Dolt file store (no manifest); refusing to clear it", path)
}

// resetDataFilePath extracts the local filesystem path from a file:// URL or
// a bare absolute path, reporting whether url is file-backed at all.
func resetDataFilePath(url string) (string, bool) {
	if strings.HasPrefix(url, "file://") {
		return strings.TrimPrefix(url, "file://"), true
	}
	if filepath.IsAbs(url) {
		return url, true
	}
	return "", false
}

// isBareGitRepoDir reports whether dir looks like a bare git repository
// (HEAD file plus an objects directory).
func isBareGitRepoDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || fi.IsDir() {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, "objects"))
	return err == nil && fi.IsDir()
}

// resetDataGitURL converts a Dolt git-protocol remote URL to the form the
// git CLI accepts (strip the git+ prefix; file paths gain file://).
func resetDataGitURL(url string) string {
	if trimmed := strings.TrimPrefix(url, "git+"); trimmed != url {
		return trimmed
	}
	if path, isFile := resetDataFilePath(url); isFile && !strings.HasPrefix(url, "file://") {
		return "file://" + path
	}
	return url
}

// lsRemoteDoltDataRefs returns which of the Dolt data-plane refs currently
// exist on the git remote at gitURL.
func lsRemoteDoltDataRefs(ctx context.Context, gitURL string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", gitURL, gitDoltDataRef, gitDoltInfoRef) // #nosec G204 -- URL from configured remote
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s failed: %s: %w", gitURL, strings.TrimSpace(string(out)), err)
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			refs = append(refs, fields[1])
		}
	}
	return refs, nil
}

// deleteGitDoltDataRefs deletes refs on the git remote at gitURL. Git
// client-side hooks are disabled for the push, same as bd's other internal
// git invocations (GH#3724 class: a user's templated pre-push hook must not
// break — or observe — bd's data-plane plumbing).
func deleteGitDoltDataRefs(ctx context.Context, gitURL string, refs []string) error {
	args := []string{"push", gitURL}
	for _, ref := range refs {
		args = append(args, ":"+ref)
	}
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- URL from configured remote, fixed refspecs
	cmd.Env = envWithNoGitHooks()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push (delete %s) failed: %s: %w", strings.Join(refs, ", "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// envWithNoGitHooks returns the current environment with git client-side
// hooks disabled via GIT_CONFIG_PARAMETERS, preserving any parameters the
// caller already set (mirrors applyNoGitHooksToCmd in internal/storage/dolt).
func envWithNoGitHooks() []string {
	base := os.Environ()
	merged := githooksenv.AppendParameter(githooksenv.Extract(base), githooksenv.NoHooksParam)
	env := make([]string, 0, len(base)+1)
	prefix := githooksenv.ParametersEnv + "="
	for _, e := range base {
		if !strings.HasPrefix(e, prefix) {
			env = append(env, e)
		}
	}
	return append(env, prefix+merged)
}

// clearDoltFileStore removes the contents of a native Dolt file-store
// directory, keeping the directory itself so the follow-up push can rebuild
// the store in place. The caller has already verified a manifest is present.
func clearDoltFileStore(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading remote store %s: %w", dir, err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("clearing remote store %s: %w", dir, err)
		}
	}
	return nil
}

var doltRemoteResetDataYes bool

var doltRemoteResetDataCmd = &cobra.Command{
	Use:   "reset-data <name>",
	Short: "Replace a remote's data plane in place after a history squash",
	Long: `Replace a Dolt remote's stored data with a fresh copy of local HEAD.

After a history squash (see the History Bloat recovery runbook), a plain
'bd dolt push --force' re-points the remote's refs but deletes nothing:
Dolt remotes accumulate chunks monotonically, so the remote keeps the full
pre-squash store. This command rebuilds the remote's data plane so it holds
only live chunks:

  - Git-backed remotes (issue data riding a git remote under refs/dolt/data):
    deletes the Dolt data refs on the git remote, then force-pushes to
    rebuild a fresh store. Code branches are untouched.
  - Native file remotes (file:// paths): clears the store directory, then
    force-pushes to rebuild it.
  - Cloud/hosted remotes (aws://, gs://, dolthub://, ...): bd cannot clear
    the stored data safely — replace the remote with a fresh URL or prefix:
      bd dolt remote remove <name>
      bd dolt remote add <name> <fresh-url>
      bd dolt push --force

This rewrites the remote's data plane. Every other clone must re-clone from
the reset remote (that is already true after the squash itself). Refuses to
run with uncommitted working-set changes: the rebuilt remote holds exactly
HEAD, and anything uncommitted would not be part of it.

Examples:
  bd dolt remote reset-data origin          # prompts for confirmation
  bd dolt remote reset-data origin --yes    # no prompt (scripts, agents)`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("dolt remote reset-data")
		name := args[0]

		if usesProxiedServer() {
			return HandleErrorRespectJSON("bd dolt remote reset-data is not supported over a proxied bd server; run it from the workspace that hosts the Dolt database")
		}
		if isDoltLocalOnly() {
			fmt.Fprintln(os.Stderr, "Error: cannot reset remote data: remote sync is disabled (dolt.local-only=true).")
			fmt.Fprintln(os.Stderr, "To re-enable remote sync: bd config unset dolt.local-only")
			return SilentExit()
		}

		ctx := rootCtx
		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}

		remotes, err := st.ListRemotes(ctx)
		if err != nil {
			return HandleError("listing remotes: %v", err)
		}
		var url string
		for _, r := range remotes {
			if r.Name == name {
				url = r.URL
				break
			}
		}
		if url == "" {
			return HandleErrorWithHint(
				fmt.Sprintf("remote %q is not configured", name),
				"Use 'bd dolt remote list' to see configured remotes.")
		}

		// The rebuilt remote holds exactly HEAD; refuse while anything
		// uncommitted would be silently left out of it.
		if det, ok := storage.UnwrapStore(st).(storage.PendingChangeDetector); ok {
			dirty, derr := det.HasCommittablePending(ctx)
			if derr != nil {
				return HandleError("checking working set: %v", derr)
			}
			if dirty {
				return HandleErrorWithHint(
					"working set has uncommitted changes; refusing to reset remote data",
					"Run 'bd dolt commit' first — the rebuilt remote holds exactly HEAD.")
			}
		}

		kind, err := classifyResetDataRemote(url)
		if err != nil {
			return HandleError("%v", err)
		}
		if kind == resetDataUnsupported {
			return HandleErrorWithHint(
				fmt.Sprintf("cannot clear stored data on remote %q (%s)", name, url),
				fmt.Sprintf("Replace the remote with a fresh URL or prefix instead:\n"+
					"  bd dolt remote remove %s\n"+
					"  bd dolt remote add %s <fresh-url>\n"+
					"  bd dolt push --force", name, name))
		}

		if !doltRemoteResetDataYes {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return HandleErrorWithHint(
					fmt.Sprintf("reset-data replaces all Dolt data stored on remote %q (%s)", name, url),
					"Re-run with --yes to confirm.")
			}
			fmt.Printf("This replaces all Dolt data stored on remote %q:\n", name)
			fmt.Printf("  %s\n", url)
			fmt.Println("The remote is rebuilt from local HEAD; other clones must re-clone.")
			fmt.Print("Proceed? (y/N): ")
			reader := bufio.NewReader(os.Stdin)
			response, rerr := reader.ReadString('\n')
			if rerr != nil {
				return HandleError("reading confirmation: %v", rerr)
			}
			response = strings.TrimSpace(strings.ToLower(response))
			if response != "y" && response != "yes" {
				fmt.Println("Canceled.")
				return nil
			}
		}

		var deletedRefs []string
		var clearedStore bool
		switch kind {
		case resetDataGitBacked:
			gitURL := resetDataGitURL(url)
			refs, lerr := lsRemoteDoltDataRefs(ctx, gitURL)
			if lerr != nil {
				return HandleError("%v", lerr)
			}
			if len(refs) == 0 {
				fmt.Println("No Dolt data refs found on the remote (already reset?); pushing fresh store.")
			} else {
				if derr := deleteGitDoltDataRefs(ctx, gitURL, refs); derr != nil {
					return HandleError("%v", derr)
				}
				deletedRefs = refs
				fmt.Printf("Deleted %s on %s\n", strings.Join(refs, ", "), gitURL)
			}
		case resetDataFileStore:
			path, _ := resetDataFilePath(url)
			if cerr := clearDoltFileStore(path); cerr != nil {
				return HandleError("%v", cerr)
			}
			clearedStore = true
			fmt.Printf("Cleared remote store %s\n", path)
		case resetDataFileAbsent:
			fmt.Println("Remote store is empty or absent; pushing fresh store.")
		}

		fmt.Printf("Force-pushing to remote %q...\n", name)
		if perr := st.PushRemote(ctx, name, true); perr != nil {
			return HandleError("push after reset failed (the remote's data refs were already removed — re-run 'bd dolt push --force --remote %s' once the cause is fixed): %v", name, perr)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"remote":        name,
				"url":           url,
				"deleted_refs":  deletedRefs,
				"cleared_store": clearedStore,
				"pushed":        true,
			})
		}
		fmt.Printf("✓ Remote %q data plane reset; store rebuilt from HEAD.\n", name)
		fmt.Println("  Other clones of this database must re-clone from the remote.")
		return nil
	},
}
