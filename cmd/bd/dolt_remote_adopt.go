package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// Git-origin adoption is the one write in the push path that can send private
// issue history somewhere the user never named (#5068). Before this gate,
// `bd dolt push` on a rig with no Dolt remote derived one from git origin,
// wired it, persisted sync.remote, committed that config change under the
// user's git identity, and uploaded — with no prompt, no flag and no opt-out.
// A public git origin therefore published the whole issue database on a
// command the user believed targeted an already-configured remote.
//
// The rule here is fail-closed: adoption is a consent decision, so the absence
// of consent is a refusal, never a default yes. Note that confirmPrompt in
// bootstrap.go deliberately returns true when stdin is not a TTY — that is the
// right default for "proceed with the thing you asked for" and the wrong one
// for this, which is why adoption does not reuse it.

// adoptOptIn is the caller's identity for adoption messages: the command that
// re-runs this operation with consent, and the verb the interactive question
// uses. The sync path shares the whole adoption flow with push
// (syncAdoptGitOrigin), and a refusal that steers a sync user into
// `bd dolt push --yes` would trade their pull away — the opt-in must name the
// command they actually ran.
type adoptOptIn struct {
	rerun  string // e.g. "bd dolt push --yes"
	action string // completes "Adopt this remote and <action>?"
}

var (
	pushAdoptOptIn = adoptOptIn{rerun: "bd dolt push --yes", action: "push"}
	syncAdoptOptIn = adoptOptIn{rerun: "bd sync --yes", action: "sync"}
)

// adoptPolicy carries the caller's consent inputs for git-origin adoption.
// It is a value, not global state, so the decision is testable without a TTY,
// a store, or a repo to mutate.
type adoptPolicy struct {
	// AssumeYes is --yes/-y: consent given ahead of time, for scripted use.
	AssumeYes bool
	// Disabled is --no-adopt or BD_NO_REMOTE_ADOPT=1: never adopt.
	Disabled bool
	// Interactive reports whether we can actually ask. False means fail-closed.
	Interactive bool
}

// adoptDecision is what to do once a git origin URL has been derived but
// before anything has been written.
type adoptDecision int

const (
	// adoptRefuse: do not adopt. The caller explains why and stops.
	adoptRefuse adoptDecision = iota
	// adoptAsk: prompt the user, showing the derived URL.
	adoptAsk
	// adoptProceed: consent already given (--yes).
	adoptProceed
)

// adoptRefusal distinguishes the two refusals, which need different messages
// and different exit behavior from the caller.
type adoptRefusal int

const (
	adoptRefusalNone adoptRefusal = iota
	// adoptRefusedDisabled: the user asked us not to adopt. Not an error.
	adoptRefusedDisabled
	// adoptRefusedNonInteractive: we cannot ask and were not told. An error.
	adoptRefusedNonInteractive
)

// decideRemoteAdoption is the whole policy, kept pure so the fail-closed
// property is asserted by a table test rather than inferred from the call site.
//
// Precedence: an explicit --no-adopt beats an explicit --yes (the safer of two
// contradictory instructions wins), and both beat interactivity.
func decideRemoteAdoption(p adoptPolicy) (adoptDecision, adoptRefusal) {
	switch {
	case p.Disabled:
		return adoptRefuse, adoptRefusedDisabled
	case p.AssumeYes:
		return adoptProceed, adoptRefusalNone
	case p.Interactive:
		return adoptAsk, adoptRefusalNone
	default:
		return adoptRefuse, adoptRefusedNonInteractive
	}
}

// currentAdoptPolicy builds the policy from flags plus BD_NO_REMOTE_ADOPT.
// stdinIsTerminal is injected so tests do not depend on how they were invoked.
// jsonMode forces non-interactive even on a TTY: --json is a machine contract,
// and a consent prompt inside it hangs whatever is parsing the stream (same
// rule as validateHookMigrationApplyConsent — JSON callers opt in with --yes).
func currentAdoptPolicy(assumeYes, noAdopt, stdinIsTerminal, jsonMode bool) adoptPolicy {
	return adoptPolicy{
		AssumeYes:   assumeYes,
		Disabled:    noAdopt || envNoRemoteAdopt(),
		Interactive: stdinIsTerminal && !jsonMode,
	}
}

func envNoRemoteAdopt() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("BD_NO_REMOTE_ADOPT")))
	return v == "1" || v == "true" || v == "yes"
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// adoptionRefusedError is the message for the non-interactive refusal. It
// names the URL that would have been adopted, because "a remote was derived"
// is useless without knowing which one — the reporter's whole complaint was
// not knowing where the upload was going.
func adoptionRefusedError(remoteURL string, optIn adoptOptIn) error {
	return fmt.Errorf(`no Dolt remote is configured, and bd will not adopt one without consent.

  Derived from git origin: %s

This would publish your entire issue history to that remote. If it is what you
want, opt in explicitly:

  %-38s # adopt the derived remote and %s
  bd dolt remote add origin <url>        # or name the remote yourself first

To never adopt implicitly, pass --no-adopt or set BD_NO_REMOTE_ADOPT=1.`, remoteURL, optIn.rerun, optIn.action)
}

// applyAdoptionConsent is the gate as the push path uses it: given a derived
// URL and a policy, either return proceed=true (everything after this may
// write) or stop. It is separated from adoptGitOriginRemoteForPush so the
// fail-closed behavior can be asserted without resolving a workspace — that
// resolution mutates whatever repo the test binary runs in, which is exactly
// what the syncAdoptGitOrigin seam comment in sync.go warns about.
//
// proceed=false with err=nil is the deliberate --no-adopt case: the caller
// falls through to its ordinary no-remote handling rather than failing.
func applyAdoptionConsent(remoteURL string, policy adoptPolicy, optIn adoptOptIn) (bool, error) {
	switch decision, refusal := decideRemoteAdoption(policy); decision {
	case adoptRefuse:
		if refusal == adoptRefusedNonInteractive {
			return false, adoptionRefusedError(remoteURL, optIn)
		}
		return false, nil
	case adoptAsk:
		if !confirmRemoteAdoption(remoteURL, optIn) {
			return false, fmt.Errorf("remote adoption declined; nothing was written and nothing was pushed")
		}
		return true, nil
	default: // adoptProceed
		// --yes: consent given ahead of time. Still announce the target, so a
		// scripted run leaves a record of where it uploaded.
		fmt.Fprintf(os.Stderr, "Adopting Dolt remote origin from git origin: %s\n", remoteURL)
		return true, nil
	}
}

// confirmRemoteAdoption asks, showing the URL and every side effect that
// follows a yes. The config.yaml commit is named here because it is made under
// the user's git identity in their repo, which is a surprise worth disclosing
// before the fact rather than in a line printed after it happened (#5068
// acceptance item 4).
func confirmRemoteAdoption(remoteURL string, optIn adoptOptIn) bool {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "No Dolt remote is configured for this rig.")
	fmt.Fprintf(os.Stderr, "  Derived from git origin: %s\n", remoteURL)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Adopting it will:")
	fmt.Fprintln(os.Stderr, "  • add it as Dolt remote \"origin\"")
	fmt.Fprintln(os.Stderr, "  • write sync.remote into .beads/config.yaml")
	fmt.Fprintln(os.Stderr, "  • commit that config change under your git identity")
	fmt.Fprintln(os.Stderr, "  • upload your full issue history there")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "Adopt this remote and %s? [y/N] ", optIn.action)

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	// Default is NO: an empty line, EOF, or a read error must not consent.
	return strings.TrimSpace(strings.ToLower(line)) == "y" ||
		strings.TrimSpace(strings.ToLower(line)) == "yes"
}
