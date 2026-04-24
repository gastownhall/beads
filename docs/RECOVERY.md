# Recovery Playbooks

This document lives next to the ADRs and matches the structure of `bd`'s
error messages: each named refusal in `bd init` points here to a labeled
anchor with step-by-step recovery instructions.

See also: `bd help init-safety`, and
[ADR 0002 — `bd init` safety invariants](adr/0002-init-safety-invariants.md).

## Table of contents

- [init-force-refused — `bd init --force`/`--reinit-local` refused because origin has Dolt history](#init-force-refused)
- [init-token-missing — `--discard-remote` refused because `--destroy-token` is missing or wrong](#init-token-missing)
- [init-local-exists — `bd init` refused because local data already exists](#init-local-exists)

---

## init-force-refused

**Exit code:** `10` (`ExitRemoteDivergenceRefused`)

**Symptom**

```
bd init refuses: remote 'origin' already has Dolt history (refs/dolt/data).
  Why: --force / --reinit-local bypasses only the LOCAL data-safety
       guard. ...
```

**Why this happens**

`bd init --force` (or `--reinit-local`) tells `bd` to bypass the local
data-safety guard. But the remote already has project history. Proceeding
would create an orphan local Dolt branch with no common ancestor on
origin. The next `bd dolt push` would either fail (no common ancestor)
or — worse, if force-pushed — destroy the team's data.

**Recovery paths**

Pick the one that matches your intent.

### 1. You want to adopt the remote's history (most common)

```
bd bootstrap
```

This clones the remote's Dolt database into a fresh local `.beads/`.
Your local state is ignored; the team's history becomes yours.

### 2. You want to diagnose what went wrong before deciding

```
bd doctor
bd dolt status
```

`bd doctor` walks the local + remote state and names concrete problems.
`bd dolt status` shows the Dolt-level view. Neither modifies anything.

### 3. You intentionally want to overwrite the remote's history (destructive)

This is a cross-boundary operation that affects every collaborator. You
need to pair `--reinit-local` with `--discard-remote`. In interactive
mode `bd` will prompt for confirmation; in non-interactive mode you must
supply a `--destroy-token`. See `bd help init-safety` for the token
format.

After `bd init --reinit-local --discard-remote`, your next
`bd dolt push` must be a history-replacing push. Coordinate with your
team before doing this.

---

## init-token-missing

**Exit code:** `12` (`ExitDestroyTokenMissing`)

**Symptom**

```
bd init refuses: --discard-remote requires an explicit destroy-token in non-interactive mode.
```

**Why this happens**

You're running non-interactively (CI, agent, piped input) and passed
`--discard-remote`. Destructive cross-boundary operations cannot be
authorized silently.

**Recovery paths**

### 1. Run interactively

Re-run in a TTY. `bd init --reinit-local --discard-remote` will prompt
you to type the destroy-token at confirmation time.

### 2. Supply the token explicitly (CI/automation)

The token format is `DESTROY-<issue-prefix>`. For a project whose issue
prefix is `bd`:

```
bd init --reinit-local --discard-remote --destroy-token=DESTROY-bd
```

Automation should template the token from project state, not from error
output. See [ADR 0002 — Invariant 4](adr/0002-init-safety-invariants.md)
for why the token is never echoed in `bd`'s error messages.

---

## init-local-exists

**Exit code:** `11` (`ExitLocalExistsRefused`)

**Symptom**

```
Refusing to destroy N issues in non-interactive mode.
  See 'bd help init-safety' for the required --destroy-token format.
```

Or, in interactive mode, you declined the typed `destroy N issues`
confirmation.

**Why this happens**

Local `.beads/` has existing issues. `bd init --reinit-local` would
permanently destroy them.

**Recovery paths**

### 1. Export first, then proceed

```
bd export > backup.jsonl
bd init --reinit-local
```

`backup.jsonl` lets you re-import individual issues if needed.

### 2. Investigate why you hit this

If you did NOT expect `bd init` to be the right command here, run
`bd doctor` first — you may be looking at a server config issue that a
re-init won't fix.
