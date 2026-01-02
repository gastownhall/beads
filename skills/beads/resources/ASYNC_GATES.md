# Async Gates for Workflow Coordination

> Adapted from ACF beads skill

`bd gate` provides async coordination primitives for cross-session and external-condition workflows. Gates are **wisps** (ephemeral issues) that block until a condition is met.

---

## Gate Types

| Type | Await Syntax | Use Case |
|------|--------------|----------|
| Human | `human:<prompt>` | Cross-session human approval |
| CI | `gh:run:<id>` | Wait for GitHub Actions completion |
| PR | `gh:pr:<id>` | Wait for PR merge/close |
| Timer | `timer:<duration>` | Deployment propagation delay |
| Mail | `mail:<pattern>` | Wait for matching email |

---

## Creating Gates

```bash
# Human approval gate
bd gate create --await human:deploy-approval \
  --title "Approve production deploy" \
  --timeout 4h

# CI gate (GitHub Actions)
bd gate create --await gh:run:123456789 \
  --title "Wait for CI" \
  --timeout 30m

# PR merge gate
bd gate create --await gh:pr:42 \
  --title "Wait for PR approval" \
  --timeout 24h

# Timer gate (deployment propagation)
bd gate create --await timer:15m \
  --title "Wait for deployment propagation"
```

**Required options**:
- `--await <spec>` — Gate condition (see types above)
- `--timeout <duration>` — Recommended: prevents forever-open gates

**Optional**:
- `--title <text>` — Human-readable description
- `--notify <recipients>` — Email/beads addresses to notify

---

## Monitoring Gates

```bash
bd gate list              # All open gates
bd gate list --all        # Include closed
bd gate show <gate-id>    # Details for specific gate
bd gate eval              # Auto-close elapsed/completed gates
bd gate eval --dry-run    # Preview what would close
```

**Auto-close behavior** (`bd gate eval`):
- `timer:*` — Closes when duration elapsed
- `gh:run:*` — Checks GitHub API, closes on success/failure
- `gh:pr:*` — Checks GitHub API, closes on merge/close
- `human:*` — Requires explicit `bd gate approve`

---

## Closing Gates

```bash
# Human gates require explicit approval
bd gate approve <gate-id>
bd gate approve <gate-id> --comment "Reviewed and approved by Steve"

# Manual close (any gate)
bd gate close <gate-id>
bd gate close <gate-id> --reason "No longer needed"

# Auto-close via evaluation
bd gate eval
```

---

## Best Practices

1. **Always set timeouts**: Prevents forever-open gates
   ```bash
   bd gate create --await human:... --timeout 24h
   ```

2. **Clear titles**: Title should indicate what's being gated
   ```bash
   --title "Approve Phase 2: Core Implementation"
   ```

3. **Eval periodically**: Run at session start to close elapsed gates
   ```bash
   bd gate eval
   ```

4. **Clean up obsolete gates**: Close gates that are no longer needed
   ```bash
   bd gate close <id> --reason "superseded by new approach"
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
bd gate close <gate-id> --reason "manual override"
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
