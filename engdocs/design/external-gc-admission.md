# External GC admission: executable, directory, and server authority

Status: draft proposal. This note selects no new admission or runtime policy.
Refs [#5703](https://github.com/gastownhall/beads/issues/5703) and
[#6123](https://github.com/gastownhall/beads/issues/6123).

## Three separate questions

| Question | Relevant evidence | What it does not establish |
|---|---|---|
| Which collector will run, and what GC surface does it support? | Resolved executable identity, version, launch identity, and CLI capability. | Authority over the selected database or safety with a particular live server. |
| Can this store expose a local database directory? | Captured locality and directory capability for the active database. | Permission to perform destructive maintenance or control its server lifecycle. |
| May external GC operate on that database now? | The chosen policy's ownership, liveness, coordination, and compatibility evidence. | A general permission to manage other databases or terminate a server. |

At source snapshot `25a54a73`, `resolveLocalActiveDatabaseDir` in
`internal/storage/dolt/store.go` supplies a captured path for advisory sizing.
It excludes several external-routing cases, but its shared-mode path does not
prove ownership or liveness. `sharedServerDatabase` uses a stronger live-ownership
proof for schema migration; that does not automatically make it the GC contract.

The [#6327](https://github.com/gastownhall/beads/pull/6327) change landed in
`8269a88c`, introducing `ExternalGCLocator` for the active database's GC path.
Correctly identifying that directory fixes target selection. It does not settle
the broader destructive-maintenance policy raised in the [adoption review](https://github.com/gastownhall/beads/pull/6327#issuecomment-5877102997).
The recorded live-server GC succeeded for the tested PATH binary; that result is
not a compatibility matrix or a new server-ownership guarantee.

## Executable and capability choices

Current external compact GC checks `exec.LookPath("dolt")`, then constructs the
primary and fallback commands by name. A follow-up should coordinate with #5703's
resolved-identity work building on merged [#5092](https://github.com/gastownhall/beads/pull/5092), rather than add a competing
resolver. Decide how selection, probing, primary launch, and fallback retain the
same identity, and how a change between selection and launch is detected/reported.

GC evidence must cover its actual CLI arguments. Support for a SQL GC argument or
a server YAML key does not establish CLI flag support. Preserve the existing
unknown-archive-flag fallback while its contract is unchanged. Candidate, launched,
and live-server versions may differ; record those differences without inventing
a hard version floor or requiring every supported topology to use one binary.

## Destructive admission alternatives

These are choices for review, not accepted restrictions:

| Alternative | Open contract question |
|---|---|
| Permit only a proven offline, managed database | How is absence of a live writer established for the operation's lifetime, and what supported live workflows would be lost? |
| Permit coordinated GC with a live server | Which engine-supported coordination and version/mode combinations establish safe concurrent reads and writes? |
| Require an operator-managed procedure for external/shared servers | What evidence and diagnostics distinguish unsupported automation from inability to read the local directory? |

Define the lifetime of any admission evidence through launch and collection,
including server replacement and failure. A readable path, loopback endpoint,
configuration label, or stale pidfile alone does not answer that question.
Do not silently tighten the read-only size capability to obtain a destructive
permission check; #6123 explicitly separates those questions.

## Evidence and ownership

Exercise supported owned, shared, external, and routed topologies with real selected
executables. Include stale/replaced servers, changed executable selection, archive
fallback, and a readable local directory belonging to a different live endpoint.
Check that the selected database is the one collected, unrelated sentinels remain
unchanged, reads and later writes succeed, and recovery follows the engine contract.
A counterexample where path resolution succeeds but GC touches another database,
or where ownership evidence no longer names the live server, defeats that proposal.

[PR #5934](https://github.com/gastownhall/beads/pull/5934) already owns overlapping
environment-port/lifecycle work; ambient routing can coexist with an owned server.
Coordinate predicate changes through #6123 and executable changes through #5703.
Interruption remains a separate choice in [#6918](https://github.com/gastownhall/beads/issues/6918).
PR #6327 has landed while [#5668](https://github.com/gastownhall/beads/pull/5668)
remains open. Preserve contributor ownership and review partial reverts together.
This note changes neither directory authority,
runtime commands, timeouts, nor the storage/driver boundary; implementation awaits
agreement on a concrete policy and its compatibility evidence.
