package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
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
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", "--", gitRemoteURLForLsRemote(url), "HEAD", ref) // #nosec G204 -- url and ref are validated configuration; -- keeps a URL from reading as an option
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		// git's first stderr line, credentials scrubbed, says what failed;
		// the callers name the ref and the URL.
		return gitDataRefTarget{}, gitLsRemoteProbeError(ctx, err)
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
// says nothing about the ref, the default branch included, so with a terminal
// it is reported and does not block, and without one it is a refusal, since
// nobody reads the warning on an unattended run and yes cannot vouch for a
// default branch.
func guardGitDataRefTarget(url, ref string, yes bool, confirm func(question string) bool) (canceled bool, err error) {
	if !gitDataRefNeedsTargetCheck(url, ref) {
		return false, nil
	}
	target, err := probeGitDataRefTarget(url, ref)
	if err != nil {
		if !remoteAddStdinIsTerminal() {
			return false, fmt.Errorf("could not check what %s holds on %s (%v); the first push would replace it with Dolt storage, and without a terminal nobody reads that warning. Make the repository reachable and re-run, or name a ref outside refs/heads and refs/tags, which needs no check", ref, redactRemoteURL(url), err)
		}
		fmt.Fprintf(os.Stderr, "Warning: could not check what %s holds on %s (%v); the first push replaces it with Dolt storage.\n", ref, redactRemoteURL(url), err)
		return false, nil
	}
	if target.IsHead {
		return false, fmt.Errorf("%s is the default branch of %s; a push would replace its tip with Dolt storage. Name a branch that does not exist yet", ref, redactRemoteURL(url))
	}
	if !target.Exists || yes {
		return false, nil
	}
	if !remoteAddStdinIsTerminal() {
		return false, fmt.Errorf("%s already exists on %s; the first push replaces its tip with Dolt storage. Re-run with --yes if that ref already holds this workspace's Dolt data, or name a ref that does not exist yet", ref, redactRemoteURL(url))
	}
	if !confirm(fmt.Sprintf("%s already exists on %s. Replace its tip with Dolt storage on the next push?", ref, redactRemoteURL(url))) {
		return true, nil
	}
	return false, nil
}

// gitDataRefRemedy is what a refusal tells the user to do: take, how the
// calling path takes an existing ref on purpose (offered too when the probe
// could not read the ref); move, how it names a ref that does not exist
// yet, the only way past the default branch.
type gitDataRefRemedy struct {
	take string
	move string
}

// refuseGitDataRefTarget is the guard for a path that cannot ask: bd config
// apply, proxied bd init, and bd init without a terminal. The remote's
// default branch, a ref that already exists, and a ref the probe cannot read
// are all refusals, the last because nobody reads a warning on an unattended
// run and the first push would replace whatever the ref holds. remedies is
// called on a refusal only: building one reads the git origin.
func refuseGitDataRefTarget(url, ref string, remedies func(url, ref string) gitDataRefRemedy) error {
	return refuseGitDataRefTargetUnattended(url, ref, false, remedies)
}

// refuseGitDataRefTargetUnattended is refuseGitDataRefTarget with one
// exception: takeExisting, a consent the user gave already, admits a ref that
// exists. The default branch and a ref the probe cannot read are refused
// regardless, since consent cannot vouch for what was never read.
func refuseGitDataRefTargetUnattended(url, ref string, takeExisting bool, remedies func(url, ref string) gitDataRefRemedy) error {
	if !gitDataRefNeedsTargetCheck(url, ref) {
		return nil
	}
	target, err := probeGitDataRefTarget(url, ref)
	if err != nil {
		return fmt.Errorf("could not check what %s holds on %s (%v); the first push would replace it with Dolt storage. %s", ref, redactRemoteURL(url), err, remedies(url, ref).take)
	}
	if target.IsHead {
		return fmt.Errorf("%s is the default branch of %s; a push would replace its tip with Dolt storage. %s", ref, redactRemoteURL(url), remedies(url, ref).move)
	}
	if target.Exists && !takeExisting {
		return fmt.Errorf("%s already exists on %s; the first push replaces its tip with Dolt storage. %s", ref, redactRemoteURL(url), remedies(url, ref).take)
	}
	return nil
}

// guardedSyncRemoteRef is the ref for a path that wires origin without a
// prompt: the configured sync.remote-ref as it applies to url, after
// refuseGitDataRefTarget with the caller's remedies.
func guardedSyncRemoteRef(url string, remedies func(url, ref string) gitDataRefRemedy) (string, error) {
	ref := syncRemoteRefForURL(url)
	if err := refuseGitDataRefTarget(url, ref, remedies); err != nil {
		return "", err
	}
	return ref, nil
}

// remoteAddCommand is the bd dolt remote add that wires origin at url on
// ref, for a message meant to be pasted into a shell: the URL is user data
// from config.yaml, so both values are shell-quoted, the URL credential-free,
// with --allow-git-origin when the URL is this repository's git origin,
// which the command refuses without it.
func remoteAddCommand(url, ref string) string {
	cmd := "bd dolt remote add origin " + doltutil.ShellQuote(redactRemoteURL(url)) + " --ref " + doltutil.ShellQuote(ref)
	if doltRemoteMatchesGitOrigin(url) {
		cmd += " --allow-git-origin"
	}
	return cmd
}

// remoteAddRemedies serve bd config apply, which reads the key on every
// run and owns its store, so bd dolt remote add is the command that asks:
// --yes takes an existing ref, and the key names a ref that does not exist
// yet.
func remoteAddRemedies(url, ref string) gitDataRefRemedy {
	return gitDataRefRemedy{
		take: fmt.Sprintf("If that ref already holds this workspace's Dolt data, add origin on purpose with: %s --yes; otherwise set %s to a ref that does not exist yet", remoteAddCommand(url, ref), syncRemoteRefKey),
		move: fmt.Sprintf("Set %s to a branch that does not exist yet", syncRemoteRefKey),
	}
}

// sqlRemoteAddRemedies are the same on a proxied server, where bd dolt
// remote add is refused and bd sql is the route: a DOLT_REMOTE call, its SQL
// literals double-quoted with their escapes and the statement single-quoted
// for the shell, so nothing in a URL changes the command.
func sqlRemoteAddRemedies(url, ref string) gitDataRefRemedy {
	stmt := fmt.Sprintf("CALL DOLT_REMOTE(%s, %s, %s, %s, %s)",
		doltutil.SQLDoubleQuoted("add"), doltutil.SQLDoubleQuoted("--ref"),
		doltutil.SQLDoubleQuoted(ref), doltutil.SQLDoubleQuoted("origin"), doltutil.SQLDoubleQuoted(redactRemoteURL(url)))
	return gitDataRefRemedy{
		take: fmt.Sprintf("If that ref already holds this workspace's Dolt data, register origin on purpose with: bd sql %s; otherwise set %s to a ref that does not exist yet", doltutil.ShellQuote(stmt), syncRemoteRefKey),
		move: fmt.Sprintf("Set %s to a branch that does not exist yet", syncRemoteRefKey),
	}
}

// originAddRemedies serve a path whose way out is the add command itself,
// never a change to the key: with --yes to take the existing ref, or on
// another ref. verb is add or re-add, by whether origin exists.
func originAddRemedies(url, ref, verb string) gitDataRefRemedy {
	return gitDataRefRemedy{
		take: fmt.Sprintf("If that ref already holds this workspace's Dolt data, %s origin on purpose with: %s --yes; otherwise %s it with a --ref that does not exist yet", verb, remoteAddCommand(url, ref), verb),
		move: fmt.Sprintf("%s origin with a --ref naming a branch that does not exist yet: %s", strings.ToUpper(verb[:1])+verb[1:], remoteAddCommand(url, "<ref>")),
	}
}

// replaceOriginRemedies serve bd config apply moving origin to a new URL,
// where the ref comes from the remote record rather than the key.
func replaceOriginRemedies(url, ref string) gitDataRefRemedy {
	return originAddRemedies(url, ref, "re-add")
}

// initOriginRemedies serve direct bd init, which does not run twice: the
// database exists by the time origin is refused, and the next push adopts
// the git origin, the right remote only when sync.remote is that origin,
// so the remedy names the add that finishes the job.
func initOriginRemedies(url, ref string) gitDataRefRemedy {
	return originAddRemedies(url, ref, "add")
}

// guardInitDoltRemoteRef decides whether bd init wires origin at url on ref.
// Attended, in interactive mode with a terminal, it asks as bd dolt remote
// add does, and consented, a remote divergence the user has already
// authorized (the destroy-token flow), stands in for --yes. Unattended it is
// refuseGitDataRefTarget, except that consent takes an existing ref: that
// ref holds data the user chose to replace. The default branch is refused
// either way, and so is a ref the probe cannot read when nobody is watching,
// a pty under --non-interactive included. A refusal or a declined prompt
// leaves origin unconfigured, says so, and names the add that finishes the
// job (initOriginRemedies); init goes on, as it does when the add itself
// fails, since the database exists by then.
func guardInitDoltRemoteRef(url, ref string, interactive, consented bool, confirm func(question string) bool) bool {
	if interactive && remoteAddStdinIsTerminal() {
		canceled, err := guardGitDataRefTarget(url, ref, consented, confirm)
		switch {
		case err != nil:
			// Attended, the one refusal is the default branch.
			fmt.Fprintf(os.Stderr, "Warning: not configuring Dolt remote origin: %v. %s\n", err, initOriginRemedies(url, ref).move)
		case canceled:
			fmt.Fprintf(os.Stderr, "Dolt remote origin not configured. %s\n", initOriginRemedies(url, ref).take)
		}
		return err == nil && !canceled
	}
	if err := refuseGitDataRefTargetUnattended(url, ref, consented, initOriginRemedies); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: not configuring Dolt remote origin: %v\n", err)
		return false
	}
	return true
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
