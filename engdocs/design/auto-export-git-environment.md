# Proposed auto-export Git environment contract

Status: proposed for [#6785](https://github.com/gastownhall/beads/issues/6785).
This document makes the policy choice reviewable; it changes no staging behavior.

## Decision proposed

Keep auto-export's hook-staging policy distinct from the broader policy for
commands that intentionally select a repository by directory. Share environment
key identity and parsing through the work in
[#6321](https://github.com/gastownhall/beads/pull/6321), preserving its Windows
and POSIX semantics. Do not replace `scrubGitHookEnv` mechanically with the
broader `gitenv.ScrubRouting` now on main. #6321 merged at
`13150ba36066a0e7d6fa45f57933c85fea13acb2` from adopted head
`1409ed6e8edf346c2ff4d2efdc91ef168602de5c`; its key-handling behavior is retained.

Before broadening the filter, resolve the temporary-index contract owned by
[#4080](https://github.com/gastownhall/beads/issues/4080). A hook invoked during
a pathspec commit can legitimately stage into Git's temporary index. Keeping
`GIT_INDEX_FILE` alone is insufficient: a relative value must be interpreted
from the original hook working directory, and the lock check must address that
selected index rather than the ordinary index that the parent commit can lock.
The existing cross-worktree guard must still decline staging into another
worktree. Discovery used by that guard is evidence for refusal, not permission
to redirect the eventual write.

## Inventory and compatibility boundary

The following is the original source inventory at main
`511849496b33b607d60489962382d0bc781a50b4`, compared with the shared helper in
[#6454](https://github.com/gastownhall/beads/pull/6454). It is not evidence that
every additional key currently causes an auto-export failure.

| Membership | Variables |
| --- | --- |
| Both filters | `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_COMMON_DIR`, `GIT_PREFIX`, `GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_CEILING_DIRECTORIES`, `GIT_DISCOVERY_ACROSS_FILESYSTEM`, and the `GIT_CONFIG*` family |
| Shared routing helper only | `GIT_EXEC_PATH`, `GIT_GRAFT_FILE`, `GIT_IMPLICIT_WORK_TREE`, `GIT_INTERNAL_SUPER_PREFIX`, `GIT_NAMESPACE`, `GIT_QUARANTINE_PATH`, `GIT_REPLACE_REF_BASE`, `GIT_SHALLOW_FILE`, `GIT_SUPER_PREFIX`, `GIT_TEMPLATE_DIR` |

At the #6321 merge, the nine-versus-nineteen exact-key split remains, but
`ScrubRouting` preserves assigned `GIT_CONFIG_NOSYSTEM` values verbatim
(including `false`, `0`, and invalid Booleans), and `GIT_CONFIG_GLOBAL` or
`GIT_CONFIG_SYSTEM` set to `/dev/null` (also case-insensitive `NUL` on Windows).
The hook filter removes all `GIT_CONFIG*` entries instead.
`ScrubRoutingAndSuppression` removes those exemptions but still differs in
exact-key membership and bare-entry handling. These are compatibility
differences, not an agreed instruction to broaden hook staging.

#6321 also preserves a valueless exact-key entry while removing matching
`GIT_CONFIG*` entries; the broader helper removes recognized exact keys without
requiring `=`. Consolidation must retain the chosen malformed-entry contract,
source-slice ownership, entry order, duplicate entries, values, and unrelated
controls. Reusing a helper name must not silently choose different semantics.

## Implementation and acceptance sequence

1. Retain merged #6321's host-key matching and behavioral fixtures.
2. Agree with #4080's owner on same-hook temporary-index selection, relative
   paths, and index-lock ownership. Keep that implementation independently
   reviewable; this proposal neither implements nor closes #4080.
3. For each proposed membership change, identify the Git operation it affects
   and the compatibility case that must survive. Broaden only the agreed policy.
4. Validate `gitAddFile` with real target and decoy indexes, including a
   same-worktree temporary index while the ordinary `index.lock` exists,
   relative index resolution, and cross-worktree refusal with neither index
   changed. Keep the existing locked-index diagnostic and non-hook behavior.
5. Exercise actual supported Windows and POSIX Git boundaries for any claimed
   platform behavior, alongside the existing environment ownership tests.

The outcome sought is one documented auto-stage policy with shared key handling.
The decision and its remaining code work stay open until maintainer agreement;
this proposal is not a report that broader filtering or temporary-index staging
has been fixed.
