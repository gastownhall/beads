# Datacosmos fork workflow

This fork publishes qualified beads releases from `dc-use`. Keep only changes
with a current downstream consumer.

| Branch | Purpose |
| --- | --- |
| `main` | Exact mirror of the official `main`. The sync job advances it by fast-forward only. A divergent mirror fails the job without publishing. |
| `dc-use` | Reviewed integration and release lane. Merge the mirrored upstream SHA before qualifying a release. |
| `contrib/<subject>` | One generally useful change based on official `main`. Open its PR directly against the upstream repository. |
| `adopt/<subject>` | An upstream PR or other third-party change awaiting integration. Record its source SHA and preserve authorship. |
| `super/<integration>` | A dedicated working branch for a cross-component integration or large change. Integrate only qualified changes into `dc-use`; never release from this branch. |

Before retaining a local change, reproduce the consumer's need on upstream
`main` and check existing upstream PRs. Use the upstream implementation when
it covers that need. Remove the downstream copy once the official history
carries it. For a remaining generic gap, submit the smallest fix upstream
and link its PR in the fork integration PR.

The sync workflow belongs on the default branch `dc-use` so scheduled runs
can update the mirror without putting workflow files on `main`. A sync that
advances the mirror does not release anything: integrate it into `dc-use`
through one reviewed PR with the native checks.

For a legacy divergent fork `main`, preserve it under a durable branch and
reconcile the migration with the repository owner before enabling the mirror.
The job never rewrites branch history.

- **Migrations are upstream's.** The fork ships none of its own. Stores that
  recorded upstream main's earlier `ignored/0026_add_wisps_current_revision`
  converge through upstream's own renumbering to `ignored/0027`, whose guard
  makes the re-run a no-op.

## Release

After the PR is merged:

```bash
scripts/fork/fork.sh release
```

This tags the merged head as the next `<upstream-version>-dc.N`, records the synced
upstream commit in the tag annotation, and pushes the tag. The fork release
workflow then builds and publishes the release.

## bd ↔ gc pairing

Gas City checks that the `bd` CLI version equals the version of the beads
library it links, as an exact string. In the gascity fork,
`scripts/fork/fork.sh pair [<bd-fork-tag>]` does the pairing:
- it points `go.mod`'s `replace` for `github.com/steveyegge/beads` at the bd
  fork release, by default the newest one for the linked version;
- it refreshes `go.sum`;
- it records `BD_FORK_MODULE` and `BD_FORK_VERSION` in `deps.env`.

`scripts/check-gomod-replace.sh` accepts exactly that replace and nothing
else. Release the bd fork first, then pair and release gc.
