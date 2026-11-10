package main

// Init-safety decision logic.
//
// See engdocs/adr/0002-init-safety-invariants.md for the ADR this encodes.
// The invariant: every `bd init` resolves `project_id` from exactly one
// explicitly-named source; ambiguous sources refuse. `--force` (or its
// replacement `--reinit-local`) bypasses the local data-safety guard only;
// it never authorizes silent divergence of remote history. When origin
// advertises `refs/dolt/data`, `bd init` refuses unless the caller
// authorizes the cross-boundary operation via `--discard-remote` + a
// destroy-token.
//
// Adopting a git-origin-derived remote's issue history additionally requires
// consent (#6099): `--yes` ahead of time, or an affirmative answer to the
// early interactive prompt. `--non-interactive` is not consent. `--no-adopt`
// / `BD_NO_REMOTE_ADOPT=1` skips the derivation entirely.
//
// Error-message contract: no runtime error may emit a complete destructive
// invocation. Flag identifiers and safe-tool names are permitted; token
// values and other friction-bearing arguments live in `bd help init-safety`
// and `docs/recovery/init-safety.md` only.

import "fmt"

// Exit codes for init-safety refusals. Stable values so CI scripts can
// branch on them without grep'ing stderr.
const (
	// ExitRemoteDivergenceRefused signals a local-source init was called
	// against a remote that already has `refs/dolt/data`, without
	// `--discard-remote`. Ambiguous identity source.
	ExitRemoteDivergenceRefused = 10

	// ExitLocalExistsRefused signals the interactive typed-confirmation
	// for a destructive re-init over existing local data was declined at
	// the prompt. That TTY-only path is its sole source: the same guard's
	// non-interactive branch returns ExitDestroyTokenMissing instead, and
	// a plain `bd init` over an initialized workspace is refused by the
	// local-safety guard with an ordinary error, not this code.
	ExitLocalExistsRefused = 11

	// ExitDestroyTokenMissing signals a destructive re-init was refused for
	// want of a valid `--destroy-token`. Four sources:
	//   1. non-interactive `--discard-remote` with no token;
	//   2. non-interactive `--reinit-local` over existing local issues;
	//   3. `--discard-remote` with a supplied token that does not match, in
	//      EITHER mode — a token that WAS supplied is always validated (#6480);
	//   4. the interactive `--discard-remote` typed-token confirmation
	//      declined at the prompt.
	// So a 12 does not on its own prove the caller was non-interactive.
	// Supplying the matching token is the correct remedy for sources 1-3
	// (format via `bd help init-safety`); source 4 was a deliberate decline.
	ExitDestroyTokenMissing = 12

	// ExitHomeDirRefused signals `bd init` was run directly in the user's
	// home directory, which is not itself a git repository, without the
	// caller naming a BEADS_DIR. Refused because init would `git init` $HOME
	// and install agent files into it, where most of them are user-wide
	// agent settings (GH#4635).
	//
	// 15 because 13 and 14 are taken: ExitGuardMismatch (update.go, "racer
	// won, skip gracefully") and ExitMigrationFrozen (errors.go). A wrapper
	// that branches on bd's exit codes without regard to the subcommand must
	// never read this refusal as a benign skip.
	//
	// Not part of protocol v0 §E3 (pinned in
	// cmd/bd/protocol/exit_codes_init_test.go), which freezes 10/11/12/130
	// and says a new stable code needs a spec revision. This constant gives
	// the refusal the same in-repo scriptability the other three have — a
	// stable number instead of grepping stderr — without claiming wire
	// status the spec has not granted.
	ExitHomeDirRefused = 15
)

// RemoteSafetyAction names what the init command should do once it has
// observed the remote state and read the user's flag intent.
type RemoteSafetyAction int

const (
	// ActionNoRemoteData — remote has no Dolt data; proceed with a normal
	// init. Caller does not need to bootstrap.
	ActionNoRemoteData RemoteSafetyAction = iota

	// ActionBootstrap — remote has Dolt data; caller should clone from it
	// instead of minting a fresh identity. This is the "adopt" path.
	ActionBootstrap

	// ActionRefuseDivergence — remote has Dolt data AND caller passed
	// `--force` or `--reinit-local` without `--discard-remote`. The new
	// identity would orphan-push over the remote on first write. Refuse
	// with `ExitRemoteDivergenceRefused`.
	ActionRefuseDivergence

	// ActionRequireDestroyToken — caller passed `--discard-remote` but
	// destroy-token validation failed: either a non-interactive caller
	// supplied no token, or a token WAS supplied — in either mode — and did
	// not match. Refuse with `ExitDestroyTokenMissing`. An interactive
	// caller that supplied NO token does not land here: it gets
	// ActionProceedWithDivergence and must prompt for confirmation instead.
	ActionRequireDestroyToken

	// ActionProceedWithDivergence — caller passed `--discard-remote` and
	// destroy-token is valid (or will be supplied via TTY prompt by the
	// caller). Skip bootstrap; wire the remote so the caller can
	// force-push local history on next operation.
	ActionProceedWithDivergence

	// ActionSkipRemote — the caller asked never to derive a remote from git
	// origin (--no-adopt / BD_NO_REMOTE_ADOPT) and the remote candidate WAS
	// derived: proceed with a purely local init. Not a refusal.
	ActionSkipRemote

	// ActionRequireAdoptionConsent — the git-origin-derived remote has Dolt
	// history and the caller did not consent to adopting it (#6099). The
	// caller must obtain consent (--yes, an affirmative interactive prompt)
	// or refuse with the generic failure code 1; adopting silently is the
	// bug this action exists to prevent.
	ActionRequireAdoptionConsent
)

// RemoteSafetyInput is the minimal input to CheckRemoteSafety. Pure
// function; no filesystem, no network, no git. Caller populates flag
// state and remote-detection state, gets back a decision.
type RemoteSafetyInput struct {
	// Flag state. Force is kept as a separate field so callers can route
	// deprecation warnings at the call site; CheckRemoteSafety treats it
	// as equivalent to ReinitLocal.
	Force       bool
	ReinitLocal bool
	// FromJSONL selects local JSONL as the init source instead of remote
	// Dolt history. When remote history exists, this is a local-source
	// override and must not silently diverge without DiscardRemote.
	FromJSONL     bool
	DiscardRemote bool

	// Destroy-token supplied on the command line, plus the token the
	// caller expects (computed from issue prefix). Both are empty when
	// the caller hasn't computed them yet — ReinitLocal without
	// DiscardRemote doesn't need a token check.
	DestroyToken  string
	ExpectedToken string

	// Observed state.
	RemoteHasDoltData bool

	// RemoteDerivedFromGitOrigin marks the remote candidate as derived from
	// git origin (no explicit --remote, no configured sync.remote). Only a
	// derived remote is subject to the adoption consent gate and the
	// --no-adopt opt-out; explicit and configured remotes name their source
	// and keep their existing semantics (#6099).
	RemoteDerivedFromGitOrigin bool

	// RemoteProbeUnknown is true when the refs/dolt/data probe could not
	// reach the remote. The fail-closed probe already reports such a remote
	// as having data (RemoteHasDoltData=true) so override flags keep their
	// refusal, but consent is NOT demanded for an unverified history: the
	// plain-init warn-and-fresh fallback for an unreachable origin keeps its
	// existing behavior, and a later successful probe re-asks.
	RemoteProbeUnknown bool

	// NoAdopt is --no-adopt / BD_NO_REMOTE_ADOPT: never derive a remote
	// from git origin. It precedes the remote-data and override evaluation
	// and beats AssumeYes — the safer of two contradictory instructions
	// wins. Callers also skip the derivation (and the probe) entirely.
	NoAdopt bool

	// AssumeYes is bd init --yes: consent given ahead of time to importing
	// a derived origin's issue history. It authorizes adoption only — never
	// the divergence/destroy-token guards below.
	AssumeYes bool

	// IsInteractive is true when the caller is attached to a TTY.
	// Interactive callers can prompt for destroy-token confirmation, so the
	// decision returned for an interactive caller that supplied NO token
	// assumes it will prompt (ActionProceedWithDivergence is returned
	// without a token match — the caller must do the prompt itself). A
	// token that WAS supplied is always validated regardless of this field:
	// the caller's prompt only fires when no token was given (#6480).
	IsInteractive bool
}

// RemoteSafetyDecision is the action + human-readable reason. UserMessage
// is populated for Refuse* actions so the caller can print exactly what
// the ADR-compliant refusal text says.
type RemoteSafetyDecision struct {
	Action      RemoteSafetyAction
	Reason      string
	ExitCode    int
	UserMessage string
}

// CheckRemoteSafety is the chokepoint the ADR names. Every future flag on
// `bd init` that can interact with remote history must go through this
// function — never add a new `&& !someFlag` at the call site. See the
// guard matrix test in init_safety_test.go.
func CheckRemoteSafety(in RemoteSafetyInput) RemoteSafetyDecision {
	// Opt-out (--no-adopt / BD_NO_REMOTE_ADOPT): never derive a remote from
	// git origin. It precedes the remote-data and override evaluation — a
	// derived remote is skipped whether the probe found data, found none,
	// or never ran — and it beats --yes: the safer of two contradictory
	// instructions wins (#6099). It never applies to a remote the caller
	// named explicitly or configured; those keep their existing semantics.
	if in.RemoteDerivedFromGitOrigin && in.NoAdopt {
		return RemoteSafetyDecision{Action: ActionSkipRemote, Reason: "no-adopt"}
	}

	// No remote data → no cross-boundary concern. Proceed.
	if !in.RemoteHasDoltData {
		return RemoteSafetyDecision{Action: ActionNoRemoteData, Reason: "no-remote-data"}
	}

	// Any explicit override intent (force / reinit-local / from-jsonl /
	// discard-remote)
	// means the user is not accepting the default bootstrap. DiscardRemote
	// is treated as override-intent too — naming discard explicitly
	// implies the user knows the remote has data and wants to replace it.
	userOverride := in.Force || in.ReinitLocal || in.FromJSONL || in.DiscardRemote

	// Remote has data, user did not override anything. Bootstrap is the
	// adoption path. For a remote derived from git origin it is gated on
	// consent (#6099): a non-interactive init must opt in with --yes, and
	// an interactive one must answer the prompt — importing another
	// project's whole issue history silently is the reported incident.
	// An UNKNOWN probe stays out of the gate so the plain-init
	// warn-and-fresh fallback for an unreachable origin keeps its existing
	// behavior; a consent flag never reaches past this branch into the
	// divergence/destroy-token guards.
	if !userOverride {
		if in.RemoteDerivedFromGitOrigin && !in.AssumeYes && !in.RemoteProbeUnknown {
			return RemoteSafetyDecision{
				Action:   ActionRequireAdoptionConsent,
				Reason:   "adoption-consent-required",
				ExitCode: 1, // the generic failure code: no new stable exit code without a protocol spec revision (see ExitHomeDirRefused)
			}
		}
		return RemoteSafetyDecision{Action: ActionBootstrap, Reason: "bootstrap-from-remote"}
	}

	// User wanted to override but did NOT authorize the cross-boundary
	// operation. This is the bd-q83 bug class: a local-source init flag
	// (`--force`, `--reinit-local`, or `--from-jsonl`) without
	// `--discard-remote`. Refuse with structured message.
	if !in.DiscardRemote {
		return RemoteSafetyDecision{
			Action:      ActionRefuseDivergence,
			Reason:      "force-without-discard-remote",
			ExitCode:    ExitRemoteDivergenceRefused,
			UserMessage: refusalMessageDivergence(),
		}
	}

	// User authorized. Non-interactive callers must supply a matching
	// destroy-token. Interactive callers that supplied no token defer to the
	// typed-confirmation prompt, but a token that WAS supplied is always
	// validated: the caller's prompt only fires when no token was given, so
	// an unvalidated wrong token would skip every confirmation (#6480).
	if !in.IsInteractive || in.DestroyToken != "" {
		if in.ExpectedToken == "" || in.DestroyToken != in.ExpectedToken {
			// Carry token PRESENCE into the refusal. The two cases have
			// different remedies and only one of them can be reached
			// interactively, so a single conflated message necessarily
			// misdirects one of them: telling a user who supplied a wrong
			// token to "re-run interactively and confirm at the prompt"
			// loops them through the identical refusal forever, because the
			// prompt is gated on no token being supplied.
			reason, message := "destroy-token-missing", refusalMessageTokenMissing()
			if in.DestroyToken != "" {
				reason, message = "destroy-token-mismatch", refusalMessageTokenMismatch()
			}
			return RemoteSafetyDecision{
				Action:      ActionRequireDestroyToken,
				Reason:      reason,
				ExitCode:    ExitDestroyTokenMissing,
				UserMessage: message,
			}
		}
	}

	return RemoteSafetyDecision{Action: ActionProceedWithDivergence, Reason: "authorized-divergence"}
}

// refusalMessageDivergence returns the What/Why/Next refusal text for local
// source selection without `--discard-remote`. Deliberately does not echo a
// complete destructive invocation — the ADR invariant bars runtime error
// output from constructing a copy-pasteable override.
func refusalMessageDivergence() string {
	return `bd init refuses: remote 'origin' already has Dolt history (refs/dolt/data).

  Why: this init mode would create or reuse local history instead of
       adopting the remote. --force / --reinit-local bypasses only the
       LOCAL data-safety guard; --from-jsonl selects JSONL as the local
       source. Neither authorizes silent divergence of remote history.

  Next:
    Adopt the remote (recommended):
      bd bootstrap

    Diagnose first:
      bd doctor

    Overwrite the remote intentionally (destructive):
      See 'bd help init-safety' for the --discard-remote workflow.`
}

// refusalMessageTokenMissing returns the What/Why/Next refusal text for
// `--discard-remote` with NO destroy-token supplied. That combination is
// reachable only from a non-interactive caller — an interactive one with no
// token defers to the typed-confirmation prompt — so both the
// "non-interactive mode" framing and the "re-run interactively" remedy are
// accurate here. A supplied-but-wrong token gets
// refusalMessageTokenMismatch() instead, where that remedy would be a dead
// end. The text is reproduced in docs/recovery/init-safety.md under
// `init-token-missing`; keep them in sync. Deliberately does not echo the
// token value.
func refusalMessageTokenMissing() string {
	return `bd init refuses: --discard-remote requires an explicit destroy-token in non-interactive mode.

  Why: Destructive cross-boundary operations cannot be authorized
       silently. The token exists so automation has to make a
       deliberate, project-specific choice.

  Next:
    See 'bd help init-safety' for the destroy-token format.
    Or re-run interactively (attached to a TTY) and confirm at the prompt.`
}

// refusalMessageTokenMismatch returns the What/Why/Next refusal text for
// `--discard-remote` with a destroy-token that was supplied but does not
// match. Unlike the missing-token case this is reachable in BOTH modes, so
// its Next must hold on a TTY too: re-running interactively with the same
// token repeats this refusal, because the typed-confirmation prompt fires
// only when no token was given. Dropping the flag is the remedy that
// actually reaches the prompt. Deliberately does not echo the token value.
func refusalMessageTokenMismatch() string {
	return `bd init refuses: the supplied --destroy-token does not match this project's token.

  Why: Destructive cross-boundary operations cannot be authorized
       silently, and a token that does not match is not authorization.
       The expected token is derived from the issue prefix; this refusal
       deliberately does not echo it.

  Next:
    See 'bd help init-safety' for the destroy-token format, then re-run
    with the matching value.

    Or drop --destroy-token entirely and re-run interactively (attached
    to a TTY): with no token supplied, bd asks for typed confirmation at
    the prompt instead.`
}

// FormatDestroyToken returns the destroy-token the caller should supply
// for a given issue prefix. Callers that need to surface this to users
// should do so via `bd help init-safety` or `docs/recovery/init-safety.md` — NOT via
// runtime error text (see the ADR invariant).
func FormatDestroyToken(prefix string) string {
	return fmt.Sprintf("DESTROY-%s", prefix)
}

// skipDerivedRemoteAdoption reports whether bd init must not derive a Dolt
// remote from git origin at all: --no-adopt or a truthy BD_NO_REMOTE_ADOPT
// (#6099). Both init checkpoints and the proxied init route apply it before
// any origin resolution or probing, so an opted-out run never touches the
// origin. It never affects an explicit --remote or a configured sync.remote.
func skipDerivedRemoteAdoption(noAdoptFlag bool) bool {
	return noAdoptFlag || envNoRemoteAdopt()
}

// initAdoptionConsentSatisfied reports whether the caller's consent inputs
// authorize importing the derived remote at syncURL (#6099): --yes consents
// to any derived origin; a consent granted at the interactive early prompt
// binds to the normalized URL it named, so a later checkpoint that newly
// finds populated derived history — or derives a different URL — cannot
// silently reuse it.
func initAdoptionConsentSatisfied(assumeYes bool, consentedURL, syncURL string) bool {
	return assumeYes || (consentedURL != "" && consentedURL == syncURL)
}
