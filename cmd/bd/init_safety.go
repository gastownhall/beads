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
// Error-message contract: no runtime error may emit a complete destructive
// invocation. Flag identifiers and safe-tool names are permitted; token
// values and other friction-bearing arguments live in `bd help init-safety`
// and `docs/recovery/init-safety.md` only.

import (
	"fmt"

	"github.com/steveyegge/beads/internal/remedy"
)

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

	// ActionAskAdoption — the remote was derived from git origin, has Dolt
	// history, no override was passed, and no consent was given ahead of
	// time, but the caller can ask. The caller prompts before any workspace
	// write; only an affirmative answer bootstraps (GH#6099).
	ActionAskAdoption

	// ActionRefuseAdoption — as ActionAskAdoption, but the caller cannot ask
	// (non-interactive, JSON, or past the point where a prompt is allowed).
	// Adoption is a consent decision, so the absence of consent refuses with
	// exit 1 (GH#6099, the inbound twin of #5068).
	ActionRefuseAdoption

	// ActionSkipDerivedRemote — the remote would have been derived from git
	// origin, but the caller opted out (--no-adopt / BD_NO_REMOTE_ADOPT).
	// Callers apply initSkipsDerivedRemote before resolving or probing
	// origin, so they never reach this; it is the matrix's statement that
	// the opt-out outranks every remote-data and override column.
	ActionSkipDerivedRemote
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

	// IsInteractive is true when the caller is attached to a TTY.
	// Interactive callers can prompt for destroy-token confirmation, so the
	// decision returned for an interactive caller that supplied NO token
	// assumes it will prompt (ActionProceedWithDivergence is returned
	// without a token match — the caller must do the prompt itself). A
	// token that WAS supplied is always validated regardless of this field:
	// the caller's prompt only fires when no token was given (#6480).
	IsInteractive bool

	// Inbound adoption of a git-origin-derived remote (GH#6099). These
	// fields matter only when DerivedFromGitOrigin is true: an explicit
	// --remote or a configured sync.remote names its source, so its
	// bootstrap needs no further consent.
	//
	// DerivedFromGitOrigin reports that the URL came from git origin, not
	// from --remote or sync.remote.
	DerivedFromGitOrigin bool
	// RemoteProbeUnknown reports that the refs/dolt/data probe failed.
	// RemoteHasDoltData is then true (fail closed for the override guards),
	// but a plain init keeps its warn-and-fresh fallback instead of asking
	// to adopt history nobody could see.
	RemoteProbeUnknown bool
	// AdoptionDisabled is --no-adopt or BD_NO_REMOTE_ADOPT. It beats every
	// other input, including AdoptionConsented.
	AdoptionDisabled bool
	// AdoptionConsented is --yes, or an affirmative answer to the early
	// prompt for this exact URL.
	AdoptionConsented bool
	// AdoptionInteractive reports that the caller may prompt for adoption
	// consent now: interactive, not JSON, and before any workspace write.
	AdoptionInteractive bool
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
	// The opt-out precedes every other column: a skipped derived remote is
	// not probed, not cloned and not wired, whatever it holds.
	if in.DerivedFromGitOrigin && in.AdoptionDisabled {
		return RemoteSafetyDecision{Action: ActionSkipDerivedRemote, Reason: "derived-remote-opted-out"}
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
	// adoption path. A remote the user named (--remote, sync.remote) is
	// consent enough; one derived from git origin is not, because on a
	// fork-less clone origin is upstream and adopting it imports another
	// project's whole issue history (GH#6099). An UNKNOWN probe keeps the
	// caller's warn-and-fresh fallback: nothing will be cloned.
	if !userOverride {
		if in.DerivedFromGitOrigin && !in.RemoteProbeUnknown {
			// The same consent policy as the outbound gate (#5068):
			// consent proceeds, an interactive caller asks, anyone else
			// refuses. The opt-out was handled above.
			switch decision, _ := decideRemoteAdoption(adoptPolicy{
				AssumeYes:   in.AdoptionConsented,
				Interactive: in.AdoptionInteractive,
			}); decision {
			case adoptAsk:
				return RemoteSafetyDecision{Action: ActionAskAdoption, Reason: "derived-adoption-needs-consent"}
			case adoptRefuse:
				return RemoteSafetyDecision{
					Action:   ActionRefuseAdoption,
					Reason:   "derived-adoption-without-consent",
					ExitCode: 1,
				}
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

// initSkipsDerivedRemote is the one predicate both init checkpoints and the
// proxied-server route consult before resolving git origin: the opt-out means
// no derived URL, no probe, no clone and no wiring (GH#6099). Explicit
// --remote and configured sync.remote are not derived and are unaffected.
func initSkipsDerivedRemote(noAdopt bool) bool {
	return noAdopt || envNoRemoteAdopt()
}

// refusalMessageAdoption returns the What/Why/Next refusal text for adopting
// a git-origin-derived remote's Dolt history without consent. It names the
// URL (credentials stripped) because the reporter's complaint was not knowing
// what was about to be imported. lateCheck marks the rare refusal that comes
// after workspace scaffolding, where "nothing was written" would be false.
func refusalMessageAdoption(remoteURL string, lateCheck bool) string {
	written := "Nothing was written."
	if lateCheck {
		written = "The workspace was scaffolded, but no history was imported."
	}
	return adoptionRefusalText("bd init refuses: git origin already has Dolt history, and bd will not adopt it without consent.", remoteURL, written)
}

// refusalMessageAdoptionDeclined is the same text for an interactive caller
// who answered anything but yes at the adoption prompt. A decline never falls
// through to a fresh database: that would leave origin to be wired later.
func refusalMessageAdoptionDeclined(remoteURL string) string {
	return adoptionRefusalText("bd init stopped: adoption of git origin's Dolt history was declined.", remoteURL, "Nothing was written.")
}

func adoptionRefusalText(headline, remoteURL, written string) string {
	return fmt.Sprintf(`%s

  Derived from git origin: %s

  Why: adopting imports that remote's entire issue history and project
       identity into this workspace, wires it as Dolt remote "origin" and
       persists it as sync.remote. On a fork-less clone, origin is the
       upstream project. %s

  Next:
    Adopt the remote's history:       bd init --yes
    Start a fresh local database:     bd init --no-adopt   (or BD_NO_REMOTE_ADOPT=1)
    Bootstrap explicitly:             bd bootstrap`, headline, remedy.PrintedValue(redactRemoteURL(remoteURL)), written)
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
