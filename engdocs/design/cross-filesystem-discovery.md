# Proposed cross-filesystem Git discovery contract

Status: proposed for [#6822](https://github.com/gastownhall/beads/issues/6822),
the cross-filesystem trade in `bd-dz16y` from the
[#6453 adoption review](https://github.com/gastownhall/beads/pull/6453#issuecomment-5854396392).
This is a policy proposal with no environment-filter or worktree changes. The
design-first hold remains until maintainers confirm the intended boundary.

## Existing trade

At pending #6453 head `a8464cff97eee5b2806aaa212b8ea6825c5b258c`, the shared
routing scrub retains `GIT_DISCOVERY_ACROSS_FILESYSTEM`. An operator's enabled
value permits discovery across a filesystem boundary; an unset value leaves
Git's default stop in place. This is different from `GIT_CEILING_DIRECTORIES`,
which restricts upward discovery and can widen selection when removed.

The removal adapter delegates to that shared policy. Compared with its older
hardcoded filter, which removed the cross-filesystem variable, this lets
`bd worktree remove` discover across a mount when the operator permits it.
The maintainer accepted that compatibility trade and recorded the changed
removal behavior for later evaluation. Main
`54dd4da6708558840f88863266b9ca702893feb1` still lists the key in its shared scrub;
this document describes the pending adoption, not a claim that main implements
the proposed policy.

The adoption review did not execute a cross-mount scenario on its runner. A
filter test retaining `=1` verifies environment membership, not which repository
Git discovers across a real boundary or which metadata an operation changes.

## Decision proposed

Preserve an explicitly enabled cross-filesystem permission consistently during
initial discovery for worktree verbs, including removal. Keep Git's default
stop when unset and preserve an operator's ceiling. Do not silently supply a
permission the caller did not set. After selecting a context, an operation's
mutation authority remains that selected repository/worktree; a discovery
permission does not independently authorize a different mutation target.

| Operation phase | Proposed boundary |
| --- | --- |
| Initial discovery for list/info or a modifying verb | Honor the operator's cross-filesystem permission and ceiling under the selected environment contract. |
| Creation or removal after selection | Bind Git metadata and filesystem changes to the selected repository/worktree; do not reinterpret discovery permission as permission to mutate an unrelated parent or decoy. |
| A caller intentionally restricted to one selected root | State and implement that operation's boundary explicitly; do not obtain it by removing ceilings or by changing the shared policy for every verb. |

This favors legitimate cross-mount checkouts and one understandable discovery
policy. Its cost is preserving wider initial discovery for removal than the old
adapter allowed. A narrower removal-only policy is an alternative if maintainers
choose that cost: it must document the unsupported cross-mount case and define
how an explicit, legitimate repository selection remains usable. A blanket
scrub is not an accepted fix by implication.

## Evidence needed before implementation

Use a real filesystem boundary, with a discoverable repository above it and
distinct target/decoy metadata. Compare unset, disabled, and enabled permission,
then repeat with an operator ceiling. Record the Git version, filesystem layout,
and actual selected repository. If a host cannot provide that boundary, report
the missing evidence rather than treating an environment-array test as coverage.

Exercise removal and at least one other worktree verb on a legitimate
cross-mount layout. Inspect actual metadata and filesystem changes: they must
belong only to the selected context. Include refusal or unresolved selection and
prove no target/decoy mutation in that outcome. The cheapest falsifiers are an
enabled legitimate checkout that becomes undiscoverable, or a modification to a
different repository merely because discovery could cross the mount.

[#6790](https://github.com/gastownhall/beads/issues/6790) and
[#6794](https://github.com/gastownhall/beads/pull/6794) own accurate documentation
of existing behavior. [#6786](https://github.com/gastownhall/beads/issues/6786)
and [#6796](https://github.com/gastownhall/beads/pull/6796) own the related cached
discovery/write-authority proposal. Preserve those owners and #6453's reviewed
implementation while deciding this specific trade. No cross-mount runtime
validation or policy acceptance is claimed by this document.
