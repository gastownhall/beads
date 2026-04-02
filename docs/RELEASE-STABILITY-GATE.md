# Release Stability Gate

A release MUST NOT ship until the stability gate passes. This gate protects
users from silent upgrade regressions like mode flips, missing config, and
data-path changes.

## Upgrade Matrix

Every release candidate must pass upgrade smoke tests from these starting points:

| From Version | Mode | Scenario |
|---|---|---|
| Previous release (N-1) | Embedded (maintainer) | Init → create issues → upgrade → verify data + role |
| Previous release (N-1) | Shared-server (maintainer) | Init → create issues → upgrade → verify routing + data |
| Previous release (N-1) | Contributor | Init --contributor → upgrade → verify role preserved |
| Two releases back (N-2) | Embedded (maintainer) | Init → upgrade → verify schema migration |

### What each scenario verifies

1. **Data preservation**: issues created before upgrade are visible after upgrade
2. **Mode preservation**: `embedded` stays `embedded`, `shared-server` stays `shared-server`
3. **Role preservation**: `beads.role` git config is not cleared or changed
4. **Config continuity**: `bd doctor quick` passes after upgrade
5. **No silent errors**: upgrade path produces no unexpected warnings or errors

## Running the Gate

```bash
# Run all upgrade smoke tests
make test-upgrade

# Or directly:
./scripts/upgrade-smoke-test.sh
```

The script:
1. Downloads the previous release binary (cached in `~/.cache/beads-regression/`)
2. Creates isolated workspaces for each scenario
3. Initialises with the old binary, creates test data
4. Runs `bd init` with the candidate binary (simulating upgrade)
5. Verifies data, role, and mode are preserved
6. Reports pass/fail for each scenario

## Release Checklist Integration

Before cutting a release, the release process (see [RELEASING.md](../RELEASING.md))
requires:

- [ ] Upgrade smoke tests pass (`make test-upgrade`)
- [ ] Breaking changes documented in CHANGELOG.md with migration steps
- [ ] If config/schema migration is involved: recovery steps documented
- [ ] Regression tests pass (`make test-regression`)

## Sign-off

The person cutting the release is responsible for verifying the gate passes.
If any scenario fails, the release is blocked until the failure is resolved.

Gate failures block the release — there is no override. Fix the bug or
document the breaking change with explicit migration steps before shipping.

## Related Issues

- [#2764](https://github.com/steveyegge/beads/issues/2764) — Test gap: upgrade paths
- [#2765](https://github.com/steveyegge/beads/issues/2765) — Test gap: mode preservation
- [#2949](https://github.com/steveyegge/beads/issues/2949) — v0.63.3 silent mode switch
- [#2950](https://github.com/steveyegge/beads/issues/2950) — beads.role left unset
