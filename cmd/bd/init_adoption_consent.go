package main

// Inbound adoption consent for `bd init` (#6099).
//
// `bd dolt push` adopting a git-origin-derived remote already fails closed
// without consent (#5068, dolt_remote_adopt.go); this file is the same rule
// for the other direction — importing a derived origin's entire issue
// history into the local database. The consent decision itself lives in
// CheckRemoteSafety (ActionRequireAdoptionConsent); the pieces here are the
// caller-side state that survives across init's two remote checkpoints, the
// interactive prompt, and the refusal text.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// adoptionConsentState carries bd init's adoption-consent inputs across both
// remote checkpoints. It is a value the RunE body owns, not global state, so
// the fail-closed behavior is assertable without a TTY or a store.
type adoptionConsentState struct {
	// assumeYes is bd init --yes: consent given ahead of time, for scripted
	// use. It authorizes adoption only — never the divergence/destroy-token
	// guards, which CheckRemoteSafety evaluates first.
	assumeYes bool
	// interactive reports whether an early-checkpoint consent prompt may be
	// shown: derived from init's isNonInteractiveInit result, excluding JSON
	// mode. --non-interactive, CI, a non-TTY stdin or --json all make it
	// false, and false means fail closed, never default-yes.
	interactive bool
	// consentedURL is the normalized remote URL whose adoption was granted
	// at the early prompt. Consent binds to that URL only: a later
	// checkpoint that derives a different URL, or newly finds populated
	// history, cannot silently reuse it.
	consentedURL string
}

// promptInitRemoteAdoption asks for consent to importing the issue history a
// git-origin-derived remote already carries. It prompts on stderr, defaults
// to NO, and grants consent only on an explicit "y"/"yes": empty input, EOF,
// "n" or any other answer is a decline. Cancellation propagates so the
// caller retains the standard exit-130 behavior (readLineWithContext). The
// reader is injected so the fail-closed answer table is testable.
func promptInitRemoteAdoption(ctx context.Context, remoteURL string, reader *bufio.Reader) (bool, error) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "No Dolt remote is configured for this repository, and its git origin already")
	fmt.Fprintf(os.Stderr, "  has Dolt history: %s\n", remoteURL)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Adopting it will:")
	fmt.Fprintln(os.Stderr, "  • import its full issue history and project identity into this workspace")
	fmt.Fprintln(os.Stderr, "  • wire it as Dolt remote \"origin\" and persist sync.remote in .beads/config.yaml")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "Adopt this origin's issue history? [y/N] ")

	response, err := readLineWithContext(ctx, reader, os.Stdin)
	if err != nil {
		return false, err
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

// promptInitRemoteAdoptionFn is the seam handleRemoteSafetyDecision calls;
// a package var so tests can intercept the interactive arm of the
// checkpoint (and prove it does not fire when consent already exists).
var promptInitRemoteAdoptionFn = promptInitRemoteAdoption

// adoptionConsentRefusalMessage returns the What/Why/Next refusal text for
// an unconsented adoption of a git-origin-derived remote (#6099). It names
// the derived URL — "a remote was derived" is useless without knowing which
// one, which was the reporter's whole complaint — and the three reachable
// remedies: consent ahead of time with --yes, skip the derivation with
// --no-adopt / BD_NO_REMOTE_ADOPT=1, or run bd bootstrap explicitly.
//
// afterWorkspaceWrites is true only for the late-checkpoint refusal, which
// may follow .beads scaffolding and must not claim that nothing was written.
func adoptionConsentRefusalMessage(remoteURL string, afterWorkspaceWrites bool) string {
	msg := fmt.Sprintf(`bd init refuses: adopting this git origin's issue history requires consent.

  Derived from git origin: %s

  Why: that origin already has Dolt history (refs/dolt/data). Adopting it
       imports its entire issue history and project identity into this
       workspace, wires it as the Dolt remote "origin", and persists
       sync.remote in .beads/config.yaml. bd init no longer does that
       silently (#6099); --non-interactive is not consent.

  Next:
    bd init --yes          # adopt this origin's issue history
    bd init --no-adopt     # or set BD_NO_REMOTE_ADOPT=1: initialize a fresh
                           # local database without deriving a remote
    bd bootstrap           # or run the explicit adoption command`, remoteURL)
	if afterWorkspaceWrites {
		msg += `

  Note: this refusal came after workspace scaffolding was written; no issue
  history was imported and no remote was wired.`
	}
	return msg
}
