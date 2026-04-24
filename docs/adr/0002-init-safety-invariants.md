# ADR 0002 — `bd init` safety invariants

## Status

Accepted — 2026-04-24.

## Context

`bd init --force` in a repo whose origin already has `refs/dolt/data`
silently skipped the bootstrap-from-remote path at `cmd/bd/init.go:511`
and then still wired origin as a Dolt remote (`init.go:643-650`). The
next write auto-pushed an orphan Dolt history, failing with
"no common ancestor." Recovery options were all destructive (force-push
over the team's remote, or `rm -rf .beads/dolt`). The tool's own warning
text acknowledged the failure class as a well-known footgun.

Historical pattern analysis (git log for `cmd/bd/init.go`): at least
eight prior commits each patched one surface of this class without
encoding the underlying invariant:

- `58f5989bf` — misleading error messages cost a user 247 issues.
- `af12d6f72` — server→embedded migration export ordering.
- `9db6b56f2` — v0.63→v1.0 schema backfill multi-statement ALTER.
- `63c7a2601` — `project_id` adoption in shared-server mode.
- `401da9df7` — `project_id` backfill via `bd doctor --fix`.
- `3a4840169` — introduced `--force` as a safety-bypass override.
- `12dc4e075` — `--destroy-token` because `--force+--quiet` bypassed confirmation.
- `83b0f099b` — remove `--quiet` bypass of the safety guard.

Each commit added a guard for one data source (local JSONL, local DB
file, shared-server `_project_id`, remote `refs/dolt/data`) but the
`--force` flag lived in global scope. Every future guard silently
inherited `--force` as an implicit bypass unless its author remembered
to special-case it. `3a4840169` introduced this shape: the flag said
"bypass the safety guard" (singular) but the code wrote it in a form
that applied to every future guard.

## Decision

### Invariant 1 — single-source identity resolution

Every `bd init` invocation resolves `project_id` from exactly **one**
explicitly-named source: (a) mint fresh, (b) adopt from remote (via
`bd bootstrap` or automatic bootstrap when origin has `refs/dolt/data`),
or (c) reuse remote identity with local reinit. When two disjoint
candidate sources exist (local data + remote Dolt history) and no flag
names the winner, `bd init` refuses.

### Invariant 2 — scope-bound `--force` / `--reinit-local`

`--force` (and its replacement `--reinit-local`) bypasses the **local**
data-safety guard only. It never authorizes silent divergence of remote
history. When origin advertises `refs/dolt/data`, `bd init --force`
refuses unless `--discard-remote` is also passed.

### Invariant 3 — central chokepoint (executable, not advisory)

Every flag on `bd init` that can interact with remote history routes
through `CheckRemoteSafety` in `cmd/bd/init_safety.go`. Adding a new
flag is a signal to extend the guard matrix test in
`cmd/bd/init_safety_test.go`; if the table doesn't exhaustively cover
`(dataSource × flagSet) → outcome`, this ADR has a gap.

### Invariant 4 — error-text-no-echo

No `bd` runtime error output may contain a complete invocation of a
destructive command. Flag identifiers (`--discard-remote`,
`--destroy-token`) and safe-tool names (`bd bootstrap`, `bd doctor`,
`bd help init-safety`) are permitted; token values
(`DESTROY-<prefix>`), hashes, and other friction-bearing arguments
live in `bd help init-safety`, this ADR, and `docs/RECOVERY.md` only.

This invariant closes the `58f5989bf` failure class where an AI agent
copy-pasted the suggested `bd init --force --destroy-token=<hash>`
one-liner from the tool's own error text and destroyed 247 issues. The
agent's behavior was rational for the error it read; the text was the
bug.

### Invariant 5 — race-safety

When `--discard-remote` is authorized (interactive confirm or
destroy-token match), `bd init` re-verifies `refs/dolt/data` on origin
between prompt/confirm and execute. If the remote state changed during
the confirmation window (another agent pushed), `bd init` aborts with
`ExitRemoteDivergenceRefused`. Race-safety is an internal invariant,
not a user-facing ceremony.

## Consequences

### Flag surface after this ADR

```
bd init                         mint, or auto-bootstrap if origin has refs/dolt/data
bd init --reinit-local          local reinit; refuses remote divergence
bd init --reinit-local \        local reinit, overwrite remote on next push
    --discard-remote            (interactive confirm or --destroy-token required)
bd init --force                 deprecated alias for --reinit-local (≥2 releases)
bd bootstrap                    adopt remote — signposted by init refusal
```

### Exit codes (stable, grep-safe)

```
10   ExitRemoteDivergenceRefused   --force/--reinit-local without --discard-remote
11   ExitLocalExistsRefused        existing local data, declined destroy confirm
12   ExitDestroyTokenMissing       --discard-remote without valid --destroy-token
```

### Deprecation cycle

`--force` continues to work for ≥2 releases post-0.x, emitting a
`DeprecationWarning` and routing internally to `--reinit-local`. Scripts
and CI muscle memory keep working; the CHANGELOG under "Deprecations"
names the substitute.

### Test contract

1. `TestCheckRemoteSafety_GuardMatrix` — table-driven, covers every
   (flag combination × remote state) permutation. New flags must extend
   this table.
2. `TestCheckRemoteSafety_RefusalTextNoEcho` — asserts Invariant 4:
   refusal text must name `bd bootstrap` and `bd help init-safety`; must
   NOT contain a complete destructive invocation.
3. `TestInitForceRefusesWhenRemoteHasDoltData` — subprocess regression
   for the bd-q83 bug, using a synthetic `refs/dolt/data` git ref
   fixture (no real Dolt push needed).

Mutation-testing ritual (optional but recommended): flip `!force &&` to
`!force ||` in the legacy gate, or flip any decision tree branch in
`CheckRemoteSafety`, and confirm both the matrix test and the
subprocess test fail. If either stays green, coverage is theater.

### Review discipline

`.github/CODEOWNERS` points `cmd/bd/init*.go` at maintainers and
references this ADR in the review-requirement comment. Future reviewers
are reminded to walk the matrix when a new flag or data source lands.

## Alternatives considered

- **Symmetric `--take-local` / `--take-remote` flag pair.** Rejected:
  names symmetric-sounding flags for structurally asymmetric operations
  (one-rig scope vs team-wide blast radius). The asymmetric
  `--reinit-local` + `--discard-remote` split exposes that asymmetry
  explicitly.
- **Typed remote-URL confirmation** instead of `--destroy-token`.
  Rejected: `--destroy-token` already existed at `init.go:214-222` for
  the local-data-destruction path. Introducing a second confirmation
  convention for the same op class is surface bloat. With Invariant 4,
  the token pattern is not friction-theater.
- **Fold `bd bootstrap` into `bd init --adopt-remote`.** Rejected:
  `bd bootstrap` is a first-class command users rely on; the verb split
  between "init" (mint) and "bootstrap" (adopt) is the UX that works.
  Flattening it would break existing muscle memory for zero structural
  benefit.

## References

- Issue source: bd-q83 (local beads tracker, merged from qa-engineer F1
  and historian F3 findings in `council-2026-04-22-beads-resilience-audit`).
- Decision log: `~/.claude/councils/2026-04-24-bd-q83-init-force-safety/log.md`.
- Implementation chokepoint: `cmd/bd/init_safety.go`.
- Guard matrix: `cmd/bd/init_safety_test.go`.
- Recovery playbooks: `docs/RECOVERY.md`.
