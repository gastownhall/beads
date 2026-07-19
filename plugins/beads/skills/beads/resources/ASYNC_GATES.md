# Async Gates for Workflow Coordination

> Adapted from ACF beads skill

`bd gate` provides async coordination primitives for cross-session and external-condition workflows. Gates are **wisps** (ephemeral issues) that block until a condition is met.

---

## Gate Types

| Type | `--type` value | `--await-id` | Use Case |
|------|-----------------|--------------|----------|
| Human | `human` (default) | — | Cross-session human approval |
| CI | `gh:run` | GitHub Actions run ID | Wait for GitHub Actions completion |
| PR | `gh:pr` | PR number | Wait for PR merge/close |
| Timer | `timer` | — | Deployment propagation delay |

---

## Creating Gates

```bash
# Human approval gate
bd gate create --type human --blocks bd-abc \
  --reason "Approve production deploy" \
  --timeout 4h

# CI gate (GitHub Actions)
bd gate create --type gh:run --blocks bd-abc \
  --await-id 123456789 \
  --timeout 30m

# PR merge gate
bd gate create --type gh:pr --blocks bd-abc \
  --await-id 42 \
  --timeout 24h

# Timer gate (deployment propagation)
bd gate create --type timer --blocks bd-abc \
  --timeout 15m
```

**Required options**:
- `--blocks <issue-id>` — Issue that stays blocked until the gate resolves

**Optional**:
- `--type <type>` — Gate type: `human`, `timer`, `gh:run`, `gh:pr` (default `human`)
- `--await-id <id>` — Condition identifier (run ID, PR number, etc.)
- `--reason <text>` — Human-readable description of what's being gated
- `--timeout <duration>` — Recommended: prevents forever-open gates

---

## Monitoring Gates

```bash
bd gate list              # All open gates
bd gate list --all        # Include closed
bd gate show <gate-id>    # Details for specific gate
bd gate check             # Evaluate open gates, close resolved ones
bd gate check --dry-run   # Preview what would close
```

**Auto-close behavior** (`bd gate check`):
- `timer` — Closes when duration elapsed
- `gh:run` — Checks GitHub API (`gh run view`), closes on success/failure
- `gh:pr` — Checks GitHub API (`gh pr view`), closes on merge/close
- `human` — Never auto-closes; requires explicit `bd gate resolve`

---

## Closing Gates

```bash
# Human gates require explicit resolution
bd gate resolve <gate-id>
bd gate resolve <gate-id> --reason "Reviewed and approved by Steve"

# Manual close (any gate) — there's no separate "close" subcommand,
# resolve works on gates of any type
bd gate resolve <gate-id> --reason "No longer needed"

# Auto-close via evaluation
bd gate check
```

---

## Best Practices

1. **Always set timeouts**: Prevents forever-open gates
   ```bash
   bd gate create --type human --blocks bd-abc --timeout 24h
   ```

2. **Clear reasons**: The `--reason` text should indicate what's being gated
   ```bash
   --reason "Approve Phase 2: Core Implementation"
   ```

3. **Check periodically**: Run at session start to close elapsed gates
   ```bash
   bd gate check
   ```

4. **Clean up obsolete gates**: Close gates that are no longer needed
   ```bash
   bd gate resolve <id> --reason "superseded by new approach"
   ```

5. **Check before creating**: Avoid duplicate gates
   ```bash
   bd gate list | grep "spec-myfeature"
   ```

---

## Gates vs Issues

| Aspect | Gates (Wisp) | Issues |
|--------|--------------|--------|
| Persistence | Ephemeral (not synced) | Permanent (synced to git) |
| Purpose | Block on external condition | Track work items |
| Lifecycle | Auto-close when condition met | Manual close |
| Visibility | `bd gate list` | `bd list` |
| Use case | CI, approval, timers | Tasks, bugs, features |

Gates are designed to be temporary coordination primitives—they exist only until their condition is satisfied.

---

## Troubleshooting

### Gate won't close

```bash
# Check gate details
bd gate show <gate-id>

# For gh:run gates, verify the run exists
gh run view <run-id>

# Force close if stuck
bd gate resolve <gate-id> --reason "manual override"
```

### Can't find gate ID

```bash
# List all gates (including closed)
bd gate list --all

# Search by title pattern
bd gate list | grep "Phase 2"
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
