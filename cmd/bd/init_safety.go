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
// destroy-token. A probe that could not READ the remote observed no
// history: an unverified origin falls back to local initialization, while
// an unverified configured/explicit remote keeps the conservative gates
// (gascity#7665).
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

	// RemoteProbeUnknown is true when the refs/dolt/data probe could not
	// observe the remote at all — unreachable URL, missing credentials,
	// transport the probe cannot speak. The probe helper still resolves the
	// fail-closed RemoteHasDoltData above to true (an unreadable remote must
	// never read as confirmed absence), so this field is what tells the
	// decision that a true RemoteHasDoltData here means "unverified", not
	// "confirmed history".
	RemoteProbeUnknown bool

	// RemoteIsGitOrigin is true only when the probed URL came from the git
	// origin fallback — a repository the caller cloned, whose origin nobody
	// configured as a sync remote for bd. It is false for a configured
	// sync.remote and for an explicit --remote: a URL the user (or project
	// config) named for bd is a remote bd is expected to read, so an
	// unreadable one may not silently fall back to local initialization the
	// way an unverified origin does.
	RemoteIsGitOrigin bool

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
	// No remote data → no cross-boundary concern. Proceed. An UNKNOWN probe
	// is not absence: nothing was observed, so it must not take this exit —
	// that is the conflation this tri-state fixes (gascity#7665).
	if !in.RemoteHasDoltData && !in.RemoteProbeUnknown {
		return RemoteSafetyDecision{Action: ActionNoRemoteData, Reason: "no-remote-data"}
	}

	// Any explicit override intent (force / reinit-local / from-jsonl /
	// discard-remote)
	// means the user is not accepting the default bootstrap. DiscardRemote
	// is treated as override-intent too — naming discard explicitly
	// implies the user knows the remote has data and wants to replace it.
	userOverride := in.Force || in.ReinitLocal || in.FromJSONL || in.DiscardRemote

	if in.RemoteProbeUnknown {
		// The probe never observed the remote, so nothing about its history
		// is confirmed — not its presence, and not its absence either. An
		// inferred git origin falls back to local initialization: plain init
		// already did (the bootstrap clone would run against the same
		// unreachable origin and fail the same way the probe did), and a
		// local-source override is not silently diverging history that was
		// never observed to exist. Discard intent opts out of the fallback:
		// the caller named the destructive operation, so the destroy-token
		// flow below runs unchanged.
		if in.RemoteIsGitOrigin && !in.DiscardRemote {
			return RemoteSafetyDecision{Action: ActionNoRemoteData, Reason: "origin-unverified"}
		}
		// An unverified CONFIGURED or EXPLICIT remote does not get the
		// fallback: a URL named for bd is one bd is expected to read, and
		// this decision continues below with the conservative reading
		// (unknown counts as has-data for the refusal and bootstrap gates).
	}

	// Remote has data, user did not override anything. Bootstrap is the
	// safe adoption path — this is the existing correct behavior.
	if !userOverride {
		return RemoteSafetyDecision{Action: ActionBootstrap, Reason: "bootstrap-from-remote"}
	}

	// A local-source override against a CONFIGURED or EXPLICIT remote the
	// probe could not read. Same exit code as confirmed-history divergence,
	// but the message must tell the truth: nothing was observed, the history
	// is unverified — not "already has Dolt history" (gascity#7665).
	if in.RemoteProbeUnknown && !in.RemoteIsGitOrigin && !in.DiscardRemote {
		return RemoteSafetyDecision{
			Action:      ActionRefuseDivergence,
			Reason:      "unverified-remote-history",
			ExitCode:    ExitRemoteDivergenceRefused,
			UserMessage: refusalMessageUnverifiedRemote(),
		}
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

// refusalMessageUnverifiedRemote returns the What/Why/Next refusal text for a
// local-source init against a configured or explicit remote whose Dolt history
// the probe could not verify. Unlike refusalMessageDivergence it must not
// claim the remote has history — nothing was observed — only that bd could
// not read it, and that an unreadable remote is not proof it is empty. Same
// error-text-no-echo invariant: names the safe tools, never a complete
// destructive invocation.
func refusalMessageUnverifiedRemote() string {
	return `bd init refuses: the remote's Dolt history could not be verified.

  Why: this init mode would create or reuse local history instead of
       adopting the remote, and bd could not read the remote to check
       for Dolt history (refs/dolt/data). An unreadable remote is not
       proof it is empty, so refusing is the safe default.

  Next:
    Verify the remote URL and credentials, then re-run; or adopt the
    remote with working credentials:
      bd bootstrap

    Diagnose first:
      bd doctor

    Intentionally overwrite the remote (destructive):
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
