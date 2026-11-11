package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// doltRemoteMatchesGitOrigin reports whether doltURL refers to the same
// repository as the git origin. Handles git+https://, git+ssh://, SCP-style,
// and plain https:// / ssh:// forms by normalizing both sides with
// doltremote.CanonicalForComparison before comparing.
// Returns false when there is no git origin.
func doltRemoteMatchesGitOrigin(doltURL string) bool {
	originURL, err := gitOriginGetURL()
	if err != nil {
		return false
	}
	return doltremote.CanonicalForComparison(doltURL) == doltremote.CanonicalForComparison(originURL)
}

// refuseGitOriginDoltRemote is bd dolt remote add's guard against a Dolt
// remote whose URL matches the git origin. It reports whether the add must
// stop, having said why on stderr; with allow (--allow-git-origin) it only
// warns.
//
// origin that Dolt stores git-backed is the sync remote bd wires up: its data
// lives under refs/dolt/data, apart from the source branches, so it does not
// collide with the git origin and passes (GH#6150). Dolt stores a .git URL in
// its git+ form, so url is judged as Dolt will store it, the form doctor reads
// back. origin over any other transport is refused with the git transport
// form of url, not with advice to give up sync.
func refuseGitOriginDoltRemote(name, url string, allow bool) bool {
	if !doltRemoteMatchesGitOrigin(url) || (name == "origin" && doltutil.AddsAsGitBackedRemote(url)) {
		return false
	}
	if allow {
		fmt.Fprintf(os.Stderr, "Warning: %q matches the git origin — proceeding because --allow-git-origin is set.\n", url)
		return false
	}
	if name == "origin" {
		fmt.Fprintf(os.Stderr, "Error: refusing to add %q as Dolt remote origin — this URL matches the git origin but does not use a git transport.\n", url)
		if gitURL, ok := gitTransportURL(url); ok {
			arg, note := remedy.CommandArg(runtime.GOOS, gitURL, "<url>")
			fmt.Fprintf(os.Stderr, "  Hint: add origin over the git transport: bd dolt remote add origin %s%s\n", arg, note)
		} else {
			fmt.Fprintln(os.Stderr, "  Hint: add origin over a git transport (git+https://, git+ssh:// or git+file://) to the same repository.")
		}
		return true
	}
	fmt.Fprintf(os.Stderr, "Error: refusing to add %q as a Dolt remote — this URL matches the git origin.\n", url)
	fmt.Fprintln(os.Stderr, "  Hint: use --allow-git-origin to proceed anyway (e.g. monorepo layout).")
	fmt.Fprintln(os.Stderr, "  Hint: or set dolt.local-only=true to disable remote sync entirely.")
	return true
}

// gitTransportURL returns url in a form Dolt stores git-backed, for a hint:
// the git+ form doltremote.Normalize gives a forge URL, or git+file:// for a
// file:// URL or an absolute path. It reports false when url has none, as
// for git://, a transport Dolt does not have.
func gitTransportURL(url string) (string, bool) {
	if gitURL := normalizeRemoteURL(url); doltutil.IsGitBackedRemoteURL(gitURL) {
		return gitURL, true
	}
	if strings.HasPrefix(url, "file://") {
		return "git+" + url, true
	}
	if filepath.IsAbs(url) {
		return "git+file://" + filepath.ToSlash(url), true
	}
	return "", false
}
