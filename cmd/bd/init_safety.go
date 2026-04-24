package main

// Init-safety decision logic.
//
// See docs/adr/0002-init-safety-invariants.md for the ADR this encodes.
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
// and `docs/RECOVERY.md` only.

import "fmt"

// Exit codes for init-safety refusals. Stable values so CI scripts can
// branch on them without grep'ing stderr.
const (
	// ExitRemoteDivergenceRefused signals `bd init --force` was called
	// against a remote that already has `refs/dolt/data`, without
	// `--discard-remote`. Ambiguous identity source.
	ExitRemoteDivergenceRefused = 10

	// ExitLocalExistsRefused signals `bd init` was called against a
	// directory that already has beads data, without `--force` or
	// `--reinit-local`. The existing local-safety refusal.
	ExitLocalExistsRefused = 11

	// ExitDestroyTokenMissing signals `--discard-remote` was passed in
	// non-interactive mode without a valid `--destroy-token`. The caller
	// must look up the token format via `bd help init-safety`.
	ExitDestroyTokenMissing = 12
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
	// destroy-token validation failed (non-interactive path with no token,
	// or wrong token). Refuse with `ExitDestroyTokenMissing`. The
	// interactive path should prompt for confirmation instead.
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
	Force         bool
	ReinitLocal   bool
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
	// Interactive callers can prompt for destroy-token confirmation; the
	// decision returned for interactive callers assumes they will prompt
	// (so ActionProceedWithDivergence can be returned without a token
	// match — the caller must do the prompt itself).
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
	// No remote data → no cross-boundary concern. Proceed.
	if !in.RemoteHasDoltData {
		return RemoteSafetyDecision{Action: ActionNoRemoteData, Reason: "no-remote-data"}
	}

	// Any explicit override intent (force / reinit-local / discard-remote)
	// means the user is not accepting the default bootstrap. DiscardRemote
	// is treated as override-intent too — naming discard explicitly
	// implies the user knows the remote has data and wants to replace it.
	userOverride := in.Force || in.ReinitLocal || in.DiscardRemote

	// Remote has data, user did not override anything. Bootstrap is the
	// safe adoption path — this is the existing correct behavior.
	if !userOverride {
		return RemoteSafetyDecision{Action: ActionBootstrap, Reason: "bootstrap-from-remote"}
	}

	// User wanted to override but did NOT authorize the cross-boundary
	// operation. This is the bd-q83 bug in pre-fix code: `--force` without
	// `--discard-remote`. Refuse with structured message.
	if !in.DiscardRemote {
		return RemoteSafetyDecision{
			Action:      ActionRefuseDivergence,
			Reason:      "force-without-discard-remote",
			ExitCode:    ExitRemoteDivergenceRefused,
			UserMessage: refusalMessageDivergence(),
		}
	}

	// User authorized. Interactive callers prompt; non-interactive must
	// supply a matching destroy-token.
	if !in.IsInteractive {
		if in.ExpectedToken == "" || in.DestroyToken != in.ExpectedToken {
			return RemoteSafetyDecision{
				Action:      ActionRequireDestroyToken,
				Reason:      "destroy-token-missing-or-wrong",
				ExitCode:    ExitDestroyTokenMissing,
				UserMessage: refusalMessageTokenMissing(),
			}
		}
	}

	return RemoteSafetyDecision{Action: ActionProceedWithDivergence, Reason: "authorized-divergence"}
}

// refusalMessageDivergence returns the What/Why/Next refusal text for the
// `--force` / `--reinit-local` without `--discard-remote` case. Deliberately
// does not echo a complete destructive invocation — the ADR invariant bars
// runtime error output from constructing a copy-pasteable override.
func refusalMessageDivergence() string {
	return `bd init refuses: remote 'origin' already has Dolt history (refs/dolt/data).

  Why: --force / --reinit-local bypasses only the LOCAL data-safety
       guard. It does not authorize silent divergence of remote history.
       Proceeding would orphan-push over the team's data on first write.

  Next:
    Adopt the remote (recommended):
      bd bootstrap

    Diagnose first:
      bd doctor

    Overwrite the remote intentionally (destructive):
      See 'bd help init-safety' for the --discard-remote workflow.`
}

// refusalMessageTokenMissing returns the What/Why/Next refusal text for
// the `--discard-remote` + missing/wrong destroy-token case. Deliberately
// does not echo the token value.
func refusalMessageTokenMissing() string {
	return `bd init refuses: --discard-remote requires an explicit destroy-token in non-interactive mode.

  Why: Destructive cross-boundary operations cannot be authorized
       silently. The token exists so automation has to make a
       deliberate, project-specific choice.

  Next:
    See 'bd help init-safety' for the destroy-token format.
    Or re-run interactively (attached to a TTY) and confirm at the prompt.`
}

// FormatDestroyToken returns the destroy-token the caller should supply
// for a given issue prefix. Callers that need to surface this to users
// should do so via `bd help init-safety` or `docs/RECOVERY.md` — NOT via
// runtime error text (see the ADR invariant).
func FormatDestroyToken(prefix string) string {
	return fmt.Sprintf("DESTROY-%s", prefix)
}
