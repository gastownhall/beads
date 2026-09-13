# Direct-local ownership handoff contract

`internal/ownershiphandoff` is the explicit Beads-side boundary for handing a
GC-managed direct-local Dolt server to `bd`. Normal startup never invokes it.

Requests identify a canonical real-path Dolt root, database, workspace, local
endpoint, and current owner (`legacy-gc`). Symlinked, missing, non-canonical,
remote, or conflicting identities are rejected before any provider hook runs.
The operation records `prepared`, `target_configured`, `old_owner_stopped`,
`verified`, and `committed` in an atomic journal at
`<root>/.beads/ownership-handoff.json` — the path the Gas City lifecycle
projection reads, and the only value `--journal` accepts; each write is fsynced
and its containing directory is fsynced after the rename, so a checkpoint
cannot be lost while the effect it guards survives. A failure after the legacy
owner has stopped compensates instead of stranding the scope, through
`rollback_started`, `legacy_config_restored`, and `rolled_back`. Retries resume from the last phase
and committed replays are no-ops. Hooks own provider-specific snapshots,
validation, lifecycle, and artifact retirement; the package never guesses at
or kills an external process. Because a retry resumes from the last durable
phase, every hook except `Commit` must be idempotent under re-invocation, and
the legacy-owner stop is reserved in the journal before it is attempted so a
partial stop is reported as a mutation. Failures remain owned by `legacy-gc`
and are journaled with a stable error code. The commit hook has a durable
in-progress checkpoint and a post-hook completion checkpoint; a commit is
ambiguous whenever it was reserved but not recorded as complete — including
when the hook itself returned an error, which may have applied part of its
effect — and an ambiguous commit refuses to advance unless the provider
supplies an explicitly idempotent replay hook.
The per-journal advisory lock is persistent and kernel-released rather than an
`O_EXCL` marker, so a crashed process cannot leave a stale file that wedges
future retries. It follows that `<root>/.beads/ownership-handoff.json.lock`
outlives
every attempt that reached the lock, including refusals that write no journal
at all — a `provider_unavailable` run with no `GC_BIN` configured, the most
likely first contact, leaves the lock inode and nothing else. An empty lock
file beside no journal therefore means a handoff was attempted, not that one is
in flight or was mutated; only refusals raised before the lock leave the root
untouched.

## Identity and invocation

`bd migrate ownership-handoff` is the only invocation path; nothing else in
`bd` constructs a handoff request. Resuming is inherent rather than selected:
re-running the same command is the retry, so the front door offers no `--resume`
or `--retry` flag and there is no non-resuming mode to ask for. Request identity
includes the canonical Gas
City root that owns the legacy server, so the same Dolt scope under a different
city is a different handoff: it is refused as `identity_conflict` rather than
resumed. A journal written before `city_root` joined the request — pre-release
builds of this series only — decodes with an empty city root and therefore
conflicts with every request a current binary makes; discarding such a journal
is safe when it records no mutation, and the refusal says so. Every
`identity_conflict` refusal reports the journaled mutation state in `mutates`,
whichever entry point produced it, so a `--json` caller that never sees the
refusal text reads the same answer from the field.

Provider hooks are resolved while the journal lock is held, after the request
and any existing journal have been validated, so a conflicting journal writer
cannot win the race between preflight and execution. `mutation_occurred` is
persistent operator state rather than an inference from the phase: once a hook
reports that it mutated the scope, the journal carries that fact across
retries, so a later non-mutating refusal cannot report the handoff as clean.
Commit is not trusted to the phase machine alone — the commit hook must
re-prove that the legacy owner is gone (a `process_missing` refusal on a fresh
inspect) before ownership moves to `bd`.

The stop reservation is scoped to the attempt that took it. A provider that
reports its failed stop made no mutation retires the reservation this attempt
wrote, because the ambiguous window it covered is the one that provider just
closed. It does not retire a reservation inherited from an earlier attempt: a
process killed mid-stop leaves the reservation set, and the next attempt's
provider can only speak for its own call, so clearing it would report
`mutates: false` for a half stop — precisely the untruthful answer the
reservation exists to prevent.

Every refusal the front door can produce also reaches `--json` callers as text:
the typed output carries an `error` field alongside `error_code`, because the
identity-conflict refusal names the journal, both identities, and whether
discarding the journal is safe, and a machine caller has no other channel for
that guidance.

## Provider obligations

This package enforces only the Beads side of the protocol. The peer lives in
another codebase, so the following obligations are stated here and pinned by
in-tree fakes rather than by the real provider:

- `StopLegacy` must report an identity-matching process that is already gone
  *as that*, however it spells it. A crash between a successful stop and its
  `old_owner_stopped` checkpoint leaves the journal at `target_configured`
  while the owner is in fact stopped, and the retry re-invokes the stop hook
  against nothing. Three answers settle it, and the phase machine treats all
  three as the stop having happened: `result=stopped` with `mutates=true`,
  `result=stopped` with `mutates=false`, and a `process_missing` refusal. The
  third is what the Gas City responder actually returns — it will not call a
  process it never signaled "stopped" — so requiring the first would wedge
  every interrupted handoff against the real peer, with the legacy server down
  and no way forward. Absence is what the stop is *for*; a process that is
  present but not provably the legacy owner is a different answer
  (`process_unowned`, `identity_changed`, `port_conflict`) and stays a refusal.
  Pinned by `TestGCProviderResumeCompletesWhenStopTreatsMissingOwnerAsStopped`,
  `TestGCProviderResumeConvergesWhenStopRefusesMissingOwner`,
  `TestGCProviderResumeConvergesWhenStopReportsNonMutatingStop`, and
  `TestGCProviderResumeStillRefusesStopOfAnotherIdentity`.
- The shipped GC provider is unix-only. It requires `GC_BIN` to be an absolute,
  canonical, executable regular file, and Go synthesises fixed modes with no
  execute bit on Windows, so `GC_BIN` there is always `provider_unavailable`.
  The Windows command shim bounds the pipe drain and nothing else: there is no
  process group to signal, so a hung protocol command's children are not
  reaped. Do not read that shim as Windows support.
- `Configure` must not mutate the *legacy-owned* scope: until `Commit`,
  `legacy-gc` stays authoritative and every failure path must be able to leave
  that scope untouched. A provider whose `Configure` durably stages its own
  bd-owned state sets `ConfigureMutates`, which makes a journal-save failure
  immediately after `Configure` report the mutation truthfully; a provider that
  leaves that flag unset is reported as `mutates=false` on that path.
- A post-stop inspect must keep reporting `owner=legacy-gc`, and must keep
  answering `process_missing` for the stopped scope until commit completes. A
  response naming another owner fails the protocol decode, and an eager
  provider-side cleanup that answers `state_missing` instead wedges a resumed
  handoff at `old_owner_stopped`.

## The replacement server and the Gas City projection

A committed handoff must leave something serving the scope. `bd migrate
ownership-handoff` therefore starts a bd-owned **direct-local** server — not a
proxied one; `metadata.json`'s mode is untouched — over the same physical
`<root>/.beads/dolt` directory the legacy owner used, and records its identity
in the journal: `target_pid`, `target_birth`, `target_data_dir`,
`target_launch_id` (a 32-hex nonce), `target_launch_config`
(`<root>/.beads/dolt-handoff-<launch id>.yaml`), and an absolute canonical
`target_launch_executable`. The launch is strict: it refuses to adopt a
listener it cannot prove it started, which is why it requires a kernel-bound
process handle and refuses `unsupported_platform` elsewhere.

Those fields are not decoration. Gas City's lifecycle resolver reads this
journal — `cmd/gc/dolt_handoff_projection.go`, `validateCommittedProjection` —
to decide whether a scope is still its to manage, and a committed record that
does not carry the full strict-launch identity is rejected as an incomplete
checkpoint, which reads as "gc still owns this" and lets `gc start` raise a
second server over bd's. `internal/ownershiphandoff/projection_contract_test.go`
holds a transcription of that reader and runs it against the bytes bd writes,
so the two halves cannot drift apart silently. `validateCommittedNormalOpen`
is bd's own copy of the same admission question, and the contract test requires
the two answers to agree.

While the journal is neither `committed` nor `rolled_back`, `CheckNormalOpen`
fences ordinary bd commands out of the workspace: the explicit transfer owns
the transition, and a read that auto-started a server would race it. Only the
transfer front door carries the `bd:skip_handoff_fence` annotation.

## Compensation

Once the legacy owner is stopped, a failure that leaves the scope unserved is
worse than a refusal, so `Verify`, `Commit`, `CommitReplay`, and a `StopLegacy`
that reports a mutation and then fails all compensate through `Rollback`. The
rollback restores the captured workspace controls byte-exactly, checkpoints
`legacy_config_restored` — the only phase in which the legacy owner may be
started again — restarts it, re-inspects it, and records `rolled_back` with the
fresh inspect proof. The restart happens after the checkpoint on purpose: a
legacy process started before it could outlive the journal that knows it
exists. A rolled-back journal is terminal for its generation; a later handoff
archives it as `.rolled-back-<unixnano>` and takes a new inspect, because
reusing the old token would make the restarted process look like the stopped
one. A provider without a `Rollback` hook keeps the older behaviour: the
failure is journaled, `legacy-gc` stays the recorded owner, and an operator
reconciles by hand.
