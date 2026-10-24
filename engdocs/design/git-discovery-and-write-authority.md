# Proposed Git discovery and write authority contract

Status: proposed for [#6786](https://github.com/gastownhall/beads/issues/6786).
This document selects a contract for review and does not change runtime behavior.

## Discovery scopes

Preserve inherited repository selection for legacy implicit discovery. Its
process cache represents the first selected context; callers must not treat a
cached result as fresh evidence after changing the working directory or routing
environment. Changing that cache contract requires an explicit caller migration.

An operation with an explicitly selected project directory instead resolves a
fresh context under its declared environment policy. It must not obtain private
Git directory, common Git directory, and worktree root from different discovery
generations. Setting a subprocess working directory alone does not override an
inherited `GIT_DIR`.

| Caller scope | Proposed selection policy | Mutation boundary |
| --- | --- | --- |
| Legacy implicit reads | Honor the initial inherited Git selection and retain the documented cache lifetime | A cached path alone does not authorize a later write |
| Explicit project init | Use the chosen project directory with routing overrides removed for its discovery subprocess | Bind subsequent artifact and config operations to that selected context |
| Standalone hook command | Honor legitimate initial inherited selection, including bare repositories with external worktrees | Capture the selected context, then pin later repository writes to it |
| Explicit read-only resolver | Use its supplied directory and environment, without changing process state or caches | Resolving paths does not itself grant mutation authority |
| Secret or hook-write guard | Preserve conservative evidence from scrubbed or inherited tracking probes | Either tracking proof can refuse a write; neither redirects its destination |

Process-lifetime environment clearing remains an explicit command boundary.
It must not become a side effect of calling a read-only discovery helper.

## Config and workspace discovery must agree

The `bd-5p25o` follow-up in the
[#6453 adoption review](https://github.com/gastownhall/beads/pull/6453#issuecomment-5854396392)
names two remaining workspace probes. At reviewed head
`a8464cff97eee5b2806aaa212b8ea6825c5b258c`, pending adoption, the relevant sites
are:

| Probe | Selection at that head | Consumer |
| --- | --- | --- |
| `internal/config/config.go:gitDirsForRepo` | `git -C repoPath rev-parse --git-dir --git-common-dir`, with routing scrubbed | Shared-worktree config fallback during initialization |
| `internal/beads/beads.go:worktreeFallbackBeadsDirForRepo` | The same two-result query, with inherited environment | Shared `.beads` fallback, including `ResolveBeadsDirForRepo` |
| `internal/beads/context.go:getGitCommonDirForPath` | `git -C path rev-parse --git-common-dir`, with inherited environment | External-workspace classification in `isExternalBeadsDir` |

The two workspace queries are separate sites, not identical argument lists.
Inherited routing can select a different repository despite `-C`, so the config
probe can answer for repository A while workspace fallback or classification
answers for B. Main `54dd4da6708558840f88863266b9ca702893feb1` also retains these
two inherited workspace probes. This inventory is not a claim the code gap has
been closed.

For an explicitly selected path, the proposed migration is to make these config
and workspace consumers use the same selected context and declared environment
policy. Inventory the implicit callers before sharing that behavior: preserve
their legitimate inherited bare/external-worktree selection and the legacy
process-cache lifetime described above. Do not globally scrub cached discovery
or settle [#5700](https://github.com/gastownhall/beads/issues/5700) by implication.

Acceptance must select a target linked worktree and a decoy through inherited
`GIT_DIR`/`GIT_WORK_TREE`, then compare the config file, shared `.beads` location,
and external-workspace classification. They must agree under the chosen explicit
selection contract. Also exercise a legitimate inherited bare/external worktree
and stale-cache/cwd transitions for implicit discovery. Read-only probes must
leave both repositories unchanged; any later write must affect only the selected
context. Compose with [#6384](https://github.com/gastownhall/beads/issues/6384)
and [#6461](https://github.com/gastownhall/beads/pull/6461) for role/accessor and
value-aware config-control behavior instead of duplicating those fixes.

## Hook configuration writes

Once an operation selects its hook context, write `core.hooksPath` only in that
context's common repository-local configuration. A later inherited routing
change must not redirect the write into a decoy repository, global config, or
private worktree config. Preserve the intended effective hook-path read before
the write, including legitimate existing worktree configuration.

A filesystem-only operation on an absolute hook path does not automatically
have authority to edit a repository's common config. Keep that distinction in
the operation's inputs rather than inferring it from an absolute path alone.
Caller cancellation belongs to the operation and must reach config subprocesses;
[#6789](https://github.com/gastownhall/beads/issues/6789) owns that code follow-up.

## Existing work and migration order

At main `511849496b33b607d60489962382d0bc781a50b4`, the legacy
`internal/git.initGitContext` uses inherited state and caches its result. The
following PRs own the related implementations; reconcile their adopted versions
before migrating another caller:

- [#6436](https://github.com/gastownhall/beads/pull/6436) owns the explicit,
  read-only hook-context resolver.
- [#6440](https://github.com/gastownhall/beads/pull/6440) and
  [#6464](https://github.com/gastownhall/beads/pull/6464) own selected init
  hook consumers and their hook writes.
- [#6463](https://github.com/gastownhall/beads/pull/6463) already implements the
  selected standalone hook-write path on its branch. Retain its implementation
  and tests instead of opening a competing writer fix.
- [#5700](https://github.com/gastownhall/beads/issues/5700) retains ownership of
  caller-cwd versus a non-Git `bd -C` target. This proposal does not settle it by
  globally scrubbing legacy discovery.

After those owners compose, inventory remaining callers of the legacy hook
configuration wrappers. Migrate only callers that require fresh selected write
authority; leave compatible implicit reads explicit and preserve contributor
attribution and the conservative tracking union.

Acceptance for each migration must distinguish target and decoy configuration,
exercise legitimate inherited bare/external worktrees, and attack stale cache
after cwd or environment changes. Verify common-local versus private-worktree
and global config, and refusal without mutation for unresolved context. Retain
the existing selected-init, standalone-hook, and explicit-resolver fixtures.
The contract decision remains proposed until maintainer agreement; code
completion requires those caller-specific validations on the adopted stack.
