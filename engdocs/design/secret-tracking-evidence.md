# Proposed secret-write tracking evidence contract

Status: proposed for [#6821](https://github.com/gastownhall/beads/issues/6821),
the failed-tracking-evidence part of `bd-dz16y` from the
[#6453 adoption review](https://github.com/gastownhall/beads/pull/6453#issuecomment-5854396392).
This document proposes a decision; it changes no guard or write behavior. The
design-first hold remains until maintainers choose the error contract.

## Existing behavior and ownership

At main `54dd4da6708558840f88863266b9ca702893feb1`,
`internal/config.isGitTracked` first probes the containing repository with
scrubbed routing. Exit 1 is treated as an untracked path and ends the lookup;
other failures lead to an inherited-context probe. If neither succeeds, the
Boolean result is false. `checkSecretGitTracked` therefore blocks only a write
it can prove is tracked. `cmd/bd/hooks.go:isGitTrackedFile` has a parallel
containing-first/fallback rule.

The pending [#6461](https://github.com/gastownhall/beads/pull/6461) head
`e33ff244e82d7fca92ff1c1657f785e068e3124f` uses a conservative two-probe union for
the secret guard: either tracked answer wins, but unsuccessful probes still
return false. That parent behavior must be reconciled before implementing this
proposal; the current-main and pending-parent probe sequences are not identical.

[#6222](https://github.com/gastownhall/beads/issues/6222) owns identity/tracking
routing isolation, and #6461 owns value-aware config suppression. Preserve
legitimate inherited selection, including a bare repository with an external
worktree. Tracking evidence can refuse a write; it cannot select a different
file to receive the secret.

## Decision proposed

Represent tracking as tracked, confirmed untracked, or unresolved, rather than
turning every failed probe into an untracked answer. For a secret-bearing
project-config write, refuse unresolved tracking with a distinct diagnostic by
default. Preserve an explicit override through the existing
`--force-git-tracked` command boundary, with help text that names unresolved
tracking as well as confirmed tracking; do not introduce an implicit retry or
an environment-wide bypass.

| Evidence for the actual destination | Proposed write outcome |
| --- | --- |
| An applicable containing or inherited probe reports tracked | Preserve the tracked-file refusal unless explicitly overridden. |
| Applicable repository selection and tracking checks establish untracked | Allow the write under the ordinary config-write checks. |
| A supported standalone workspace is positively established to have no applicable Git repository | Allow the write; lack of Git integration is not itself a forbidden workflow. |
| Git cannot launch, config is malformed/unreadable, permissions prevent a probe, or applicability cannot be resolved | Return an unresolved-tracking error before modifying the file, unless explicitly overridden. |

Choose probe applicability before implementation. A legitimate inherited
bare/external-worktree context can answer tracking even when the containing
probe finds no repository; that expected absence must not force a false refusal.
Conversely, one untracked answer must not erase a tracked answer from another
applicable context. Missing Git or a failed command is not proof that a workspace
has no repository. Establish the non-repository and untracked cases from actual
Git outcomes on supported hosts, not from the Boolean helper's return shape.

The concrete secret-write consumers are `bd config set` and `set-many` via
`CheckSecretKeyGitSafety`. Their existing force flag already bypasses that check.
An eventual implementation must preserve that explicit control and the existing
destination/error handling. Non-secret values and read-only discovery are out
of scope. Share tracking-result semantics with the mirrored hook guard, but do
not silently give hook installation the secret command's override policy.

## Compatibility cost and acceptance

This proposal increases evidence for secret writes at the cost of availability
when Git or configuration is broken. Today's permissive behavior is a valid
alternative if maintainers prefer uninterrupted writes; it should then be stated
as such, with a diagnostic that distinguishes uncertainty from a known untracked
path. Neither choice is implied by adopting a routing scrub.

After agreement, test the actual write boundary with a tracked containing
repository plus poisoned routing, a genuinely untracked file, legitimate
inherited bare/external selection, and launch/config/permission failures. Cover
`set-many` before its first write and verify unchanged file bytes on refusal.
The cheapest falsifier for the proposed rule is a failed tracking determination
that still writes the secret without the explicit override. Also preserve the
supported non-Git workspace and inherited-checkout cases to expose false refusals.

Diagnostics should identify the failed lookup and recovery options without
echoing the secret value. A pre-write probe is not an atomic guarantee against
concurrent index changes; this decision does not add locking or claim that later
tracking is impossible. No runtime change or settled policy is included here.
