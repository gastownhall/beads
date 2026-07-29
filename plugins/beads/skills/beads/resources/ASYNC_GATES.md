# Async Gates for Workflow Coordination

> Adapted from ACF beads skill

`bd gate` provides async coordination primitives for cross-session and external-condition workflows. Gates are **wisps** (ephemeral issues) that block until a condition is met.

---

## Gate Types

| Type | `--type` value | `--await-id` | Use Case |
|------|-----------------|--------------|----------|
| Human | `human` (default) | — | Cross-session human approval |
| CI | `gh:run` | GitHub Actions run ID | Wait for GitHub Actions completion |
| PR | `gh:pr` | PR number | Wait for PR merge |
| Timer | `timer` | — | Deployment propagation delay |
| Bead | `bead` | `<bead-id>` (local) | Wait for another local bead to close |

---

## Creating Gates

```bash
# Human approval gate (timeout is stored but not enforced by check)
bd gate create --type human --blocks bd-abc \
  --reason "Approve production deploy"

# CI gate (GitHub Actions)
bd gate create --type gh:run --blocks bd-abc \
  --await-id 123456789

# PR merge gate
bd gate create --type gh:pr --blocks bd-abc \
  --await-id 42

# Timer gate (deployment propagation) — --timeout is enforced by check
bd gate create --type timer --blocks bd-abc \
  --timeout 15m

# Bead gate (waits for a local bead to close)
bd gate create --type bead --blocks bd-abc \
  --await-id bd-xyz
```

> Note: cross-rig await IDs (`<rig>:<bead-id>`) are accepted at create time
> but are not currently evaluable — the gate stays pending forever. Use a
> local bead ID.

**Required options**:
- `--blocks <issue-id>` — Issue that stays blocked until the gate resolves

**Optional**:
- `--type <type>` — Gate type: `human`, `timer`, `gh:run`, `gh:pr`, `bead` (default `human`)
- `--await-id <id>` — Condition identifier (run ID, PR number, `rig:bead-id`, etc.)
- `--reason <text>` — Stored on the gate as its description (what is being gated)
- `--timeout <duration>` — **Only timer gates enforce this** via `bd gate check`. You can set it on other types for documentation, but `check` does not auto-resolve/escalate human or GitHub gates based on timeout.

---

## Monitoring Gates

```bash
bd gate list              # All open gates
bd gate list --all        # Include closed
bd gate show <gate-id>    # Details for specific gate
bd gate check             # Evaluate open gates (resolve / escalate / pending)
bd gate check --dry-run   # Preview without closing or escalating
bd gate check --escalate  # Also fire escalations for failed/closed-unmerged gates
```

**What `bd gate check` does** (by type):

| Type | Resolves (closes gate) when | Escalates (gate stays open) when |
|------|-----------------------------|----------------------------------|
| `timer` | `now > created_at + timeout` | never (timers only resolve or stay pending) |
| `gh:run` | run `status=completed` **and** `conclusion=success` (also `skipped`) | run completed with `failure` / `canceled` |
| `gh:pr` | PR `state=MERGED` | PR `state=CLOSED` without merge |
| `bead` | target bead `status=closed` | — |
| `human` | **never** via `check` — use `bd gate resolve` | — |

Only **resolved** gates are closed. Escalated gates remain open; use `--escalate` to notify, then decide whether to `bd gate resolve` manually.

---

## Closing Gates

```bash
# Human gates require explicit resolution
bd gate resolve <gate-id>
bd gate resolve <gate-id> --reason "Reviewed and approved by Steve"

# Manual close (any gate) — there is no separate "close" subcommand;
# resolve works on gates of any type
bd gate resolve <gate-id> --reason "No longer needed"

# Auto-resolve via evaluation (success/merge/timer only)
bd gate check
```

---

## Best Practices

1. **Timeouts only protect timer gates**: `bd gate check` evaluates `Timeout` for `timer` only. For human/GitHub forever-open protection, schedule a manual review or resolve yourself — do not rely on `--timeout` alone.
   ```bash
   bd gate create --type timer --blocks bd-abc --timeout 15m
   ```

2. **Clear reasons**: `--reason` is stored as the gate description (what is being gated).
   ```bash
   --reason "Approve Phase 2: Core Implementation"
   ```

3. **Check periodically**: Run at session start to resolve elapsed timers / successful CI / merged PRs.
   ```bash
   bd gate check
   ```

4. **Clean up obsolete gates**: Resolve gates that are no longer needed.
   ```bash
   bd gate resolve <id> --reason "superseded by new approach"
   ```

5. **Find a gate by reason/description**: plain `bd gate list` prints id + await type/id only (not title/description). Filter JSON instead:
   ```bash
   bd gate list --json | jq -r '.[] | select((.description // .title // "") | test("Phase 2"; "i")) | .id'
   ```

---

## Gates vs Issues

| Aspect | Gates (Wisp) | Issues |
|--------|--------------|--------|
| Persistence | Ephemeral (not synced) | Permanent (synced to git) |
| Purpose | Block on external condition | Track work items |
| Lifecycle | Resolve on success/merge/timer; escalate on failure | Manual close |
| Visibility | `bd gate list` | `bd list` |
| Use case | CI, approval, timers, cross-rig | Tasks, bugs, features |

Gates are designed to be temporary coordination primitives—they exist only until their condition is satisfied (or you resolve them by hand).

---

## Troubleshooting

### Gate won't close

```bash
# Check gate details
bd gate show <gate-id>

# For gh:run gates, verify the run exists
gh run view <run-id>

# Failed CI / closed-unmerged PR escalate — they do NOT auto-resolve.
# Resolve manually when you have handled the failure:
bd gate resolve <gate-id> --reason "handled failure / no longer blocking"
```

### Can't find gate ID

```bash
# List all gates (including closed)
bd gate list --all

# Search by description/reason (list text output has neither)
bd gate list --json | jq -r '.[] | select((.description // .title // "") | test("Phase 2"; "i")) | "\(.id) \(.await_type // .awaitType) \(.description // .title)"'
```

### CI run ID detection fails

```bash
# Check GitHub CLI auth
gh auth status

# List runs manually
gh run list --branch <branch>

# Use specific workflow
gh run list --workflow ci.yml --branch <branch>
```
