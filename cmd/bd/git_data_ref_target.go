package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitBackedRemoteShapes names the URL shapes dolt treats as git-backed, for
// help and error text.
const gitBackedRemoteShapes = "a git+https://, git+ssh://, git+file://, or git+http:// URL, or a URL ending in .git that dolt rewrites to one of those: https://, http://, ssh://, file://, user@host:path, host/path, or a local path"

// gitDataRefTarget is what a git repository holds at a candidate data ref.
type gitDataRefTarget struct {
	Exists bool // the ref exists on the remote
	IsHead bool // the ref is the remote's default branch, the target of HEAD
}

// probeGitDataRefTarget asks the git repository at url (a git+ Dolt URL or a
// git URL) what ref holds, with one `git ls-remote --symref`.
func probeGitDataRefTarget(url, ref string) (gitDataRefTarget, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", gitRemoteURLForLsRemote(url), "HEAD", ref) // #nosec G204 -- url and ref are validated configuration
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return gitDataRefTarget{}, fmt.Errorf("probe %s on %s: %w", ref, url, err)
	}
	var target gitDataRefTarget
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 3 && fields[0] == "ref:" && fields[2] == "HEAD":
			target.IsHead = fields[1] == ref
		case len(fields) == 2 && fields[1] == ref:
			target.Exists = true
		}
	}
	return target, nil
}

// gitDataRefNeedsTargetCheck: a ref a host shows as a branch or a tag can
// collide with source history; a ref elsewhere (refs/dolt/...) cannot, and a
// URL that is not git-backed carries no ref at all.
func gitDataRefNeedsTargetCheck(url, ref string) bool {
	return ref != "" && isGitBackedDoltRemoteURL(url) &&
		(strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/tags/"))
}

// guardGitDataRefTarget runs before a remote is created or replaced on ref.
// Dolt's first push replaces the ref's tip with Dolt storage, so the
// remote's default branch is refused outright, and a ref that already exists
// is taken only on an explicit answer: yes, or a confirmed prompt; without a
// terminal and without yes it is refused, naming the ref. A probe that fails
// is reported and does not block, since it says nothing about the ref.
func guardGitDataRefTarget(url, ref string, yes bool, confirm func(question string) bool) (canceled bool, err error) {
	if !gitDataRefNeedsTargetCheck(url, ref) {
		return false, nil
	}
	target, err := probeGitDataRefTarget(url, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not check what %s holds on %s (%v); the first push replaces it with Dolt storage.\n", ref, url, err)
		return false, nil
	}
	if target.IsHead {
		return false, fmt.Errorf("%s is the default branch of %s; a push would replace its tip with Dolt storage. Name a branch that does not exist yet", ref, url)
	}
	if !target.Exists || yes {
		return false, nil
	}
	if !remoteAddStdinIsTerminal() {
		return false, fmt.Errorf("%s already exists on %s; the first push replaces its tip with Dolt storage. Re-run with --yes if that ref already holds this workspace's Dolt data, or name a ref that does not exist yet", ref, url)
	}
	if !confirm(fmt.Sprintf("%s already exists on %s. Replace its tip with Dolt storage on the next push?", ref, url)) {
		return true, nil
	}
	return false, nil
}

// confirmGitDataRefTarget asks question on the terminal; anything but yes
// declines.
func confirmGitDataRefTarget(question string) bool {
	fmt.Printf("  %s (y/N): ", question)
	response, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// gitDataRefTargetGuard runs right before a remote is created or replaced
// (never for a re-add that changes nothing) and may refuse or cancel it.
type gitDataRefTargetGuard func() (canceled bool, err error)
