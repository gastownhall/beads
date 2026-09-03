# BDP graph store — architecture and design

**Status:** Draft v10 (W-arch, after eight council rounds; the fence cell and the counter nonce are probe-confirmed on Dolt 2.1.8) — held for the operator's rulings — feat/bead-graph
**Date:** 2026-09-02
**Companion:** `BDP_BEAD_GRAPH_PLAN.md` (the plan and its twelve rulings) and
`BDP_GRAPH_CLI_AND_STORAGE_SPEC.md` (the detailed CLI and storage-interface
changes). This document is the *shape*: what the pieces are, where they live,
how a request flows, and where this design corrects the plan after the
tree's own conventions were read closely. Eight three-reviewer councils
(Claude, Codex, Gemini) reviewed v1–v9; §2 records what changed and which
changes amend a ruling rather than a mechanism. Rounds 3–5 converged on one
area — the authority mechanism — and every round since v3 has replaced an
invented primitive with one the tree already has. v6 added the
operator's **simplification ruling (A9)**; v9 states its arbiter honestly:
physical database copies are an operator-managed hazard, not a fenced one.

## 1. The one-paragraph version

The graph store is a new **plane** beside issues and memories: a public leaf
contract package (`graphops`) declaring the value types, the laws, and six
role interfaces, reached through **role accessors on `storage.Storage`**
(`BeadGraph*`, to keep clear of the issue-graph `GraphCounter()`) exactly as
`issueops` and `memoryops` are — declared explicitly by every decorator
(promotion is the failure mode the censuses catch), wrapped by telemetry,
recursed unwrapped by the hook layer — with one shared transaction-level
body under `internal/storage` taking a `DBTX`-shaped runner so that both
Dolt stores *and* the unit-of-work leg call the same code, proven by
`backend/conformance` role contracts wired on all three legs and guarded by
the existing coverage gates. BDP is served by the existing
`internal/httpapi` server as a **conditional second route table** behind the
same `route()` middleware — in v0 **only from SQL-server workspaces** (the
unit-of-work leg; `bd serve` refuses embedded Dolt permanently, and a
registered backend's store arm has no fence to offer until it declares
one). `bd bdp serve` is the strict command over that server: it mints and
it requires a Scope this workspace holds; `bd serve` mounts the same rows
when it holds an already-minted Scope and never refuses on account of the
graph. Every operation is **one role call, one transaction**, asserting a
**store-owned authority witness** — loaded by the accessor from a
clone-local file bound to this installation, checked against a hash-chained
ledger head and a lease row the mutation itself must update — so a clone, a
restore to an older state (its witness head is absent), a copied file, or
a promotion elsewhere is refused by the store, and no caller can supply
the witness (a whole-installation copy is the stated operator hazard). A shared database is fenced by that
lease; a configured remote is fenced by a publication primitive (fetch →
ancestor check → scoped commit → push, with revert on refusal) — or, under
A9, is not an authority topology at all in v0.

## 2. Corrections to the plan, and proposed ruling amendments (read this first)

Two kinds of change are recorded here and must not be confused. A
**mechanism correction** replaces something the plan's §4 *proposed* with
the house idiom that already solves the problem; the ruling it serves is
unchanged. A **ruling amendment** changes text the operator ratified in §9;
it is *proposed* here and takes effect only when ruled. Nothing below is
settled until ruled.

### 2a. Mechanism corrections (no ruling changes)

| Plan §4 mechanism | Replaced by | Why |
| --- | --- | --- |
| `GraphCapable` as a *separate optional interface*, resolved by targeted decorator peels (`graphsource` package) | **Role accessors on `storage.Storage`** — the house rule — *if A8 option A is ruled*; otherwise the optional-interface shape returns with the costs stated under A8 | Every accessor lives on `Storage` (28 today); `DoltStorage` embeds it; both decorators embed the `DoltStorage` *interface*. A method added to `Storage` is therefore **promoted through the embedded interface**: every wrapper compiles unchanged — which is exactly why the reflection census in `role_accessor_decorator_test.go` (and its telemetry twin) exists: wrappers must **declare** each accessor, and the census catches the ones that silently promote. |
| A `ResolveGraphReadSource` policy that "re-applies telemetry" after peeling | Nothing — **the accessors carry the layers** | This is what accessors are *for* in this tree. |
| `backend/types.go` aliases for every `graphops` type | **No aliases.** `graphops` is a public root package, like `issueops` | The completeness guard demands aliases only for types under `internal/`. |
| `internal/graphapi` as a separate meaning-function package | **The laws live in `graphops`** | Import rule and cycle. |
| A `ReadSnapshot` handed from a resolver to a handler; a caller-supplied expectation (v2/v3) | **One role call, one transaction, asserting a store-owned witness** (A1) | A witness the *caller* supplies is forgeable from the replicated rows. |
| `SELECT … FOR UPDATE` on the Scope row to serialize the ledger (v4); a bare `next = next + 1` counter (v5–v6) | **A single-row counter whose every allocation writes a fresh random cell** (`next_seq = next_seq + 1, alloc_nonce = <random>`) — the journal's `bd_events_seq` shape, corrected | Dolt has no row locks (`FOR UPDATE` is a parse-only no-op) **and merges concurrent transactions cell by cell**: two sessions each doing `next = next + 1` from the same value both commit and the counter reads one increment (probed on 2.1.8). Only a same-cell-*different-value* write is a serialization failure (`1213`) that `withRetryTx`/`RunTxResult` replay; the random cell makes every allocation one. The events PK is the second guard, and it too converges silently on byte-identical rows — the nonce makes rows differ. The journal's own prose carries the misconception; its PK plus duplicate-key heal is what actually guards it. `next` is reserved in Dolt's parser. |
| `DOLT_RESET --hard` to undo a refused publication (v4–v5) | **`DOLT_RESET --soft <pre-op commit>`, then per graph table `DOLT_RESET('<table>')` (unstage) and `DOLT_CHECKOUT('<table>')`** when HEAD is still the operation commit; **`DOLT_REVERT <op commit>`** when HEAD has moved; the ignored lease restored on either path under the same-holder guard | A hard reset discards unrelated dirty work on a shared server; a bare checkout after a soft reset restores from the *staged* root and reverts nothing (probed), the unstage step makes it revert only the graph tables; a revert preserves later commits from other actors. |

### 2b. Proposed ruling amendments (pending operator ruling)

| # | Ruling | Earlier drafts said | v10 proposes | Evidence |
| --- | --- | --- | --- | --- |
| A1 | **9** ("the snapshot lease" in the obligation list) | dropped silently (v1); caller-supplied expectation (v2–v3) | **The per-call transaction is the v0 lease and asserts a store-owned authority witness:** the accessor loads the witness from the clone-local file and the body checks it *inside the same transaction* — Scope row identity, the hash-chained ledger head (exact-prefix identity), the lease row (a protected read `SELECT`s it inside the read transaction and checks holder, epoch, **and `expires_at > NOW(6)`** — a holder/epoch match alone proves only that no takeover was visible in the snapshot, not that the lease is live; **reads never write the lease**: on the serving leg the watcher renews and a near-expiry read fails closed as `lost`; a CLI read whose lease names this workspace but has expired regrants **once, ephemerally, before opening a fresh read transaction** and retries; the read's context deadline is derived from the remaining lease interval and is below it; a mutation `UPDATE`s it and requires one affected row, with the holder's installation key, epoch, **and the `fence` cell value read in this transaction** in the predicate, and **sets `fence` to a fresh random value and `expires_at = NOW(6) + ttl`** — every lease write does, so any two concurrent lease writers are a same-cell-different-value conflict Dolt reports as `1213` (probed on both the versioned and the ephemeral commit paths); the loser is replayed with a fresh random fence and **re-evaluates its preconditions**: a still-authorized writer then succeeds serially, a superseded or stolen one matches zero rows and refuses. An expired lease that still names this workspace is **self-regranted** by the same write — promotion is needed only when the holder or the epoch differ), and the graph-state version (the ordered per-table hashes of the eight replicated graph tables). Public request types carry **no** authority fields. The cursor type is opaque from P1. | Rounds 1, 3, 4, 5. |
| A2 | **7b + 12** (listener; "`bd serve` creates the Scope on first serve") | a sibling server (v1); `bd serve` mints (v2–v4) | **BDP rows mount inside `internal/httpapi`** as a conditional second route table behind the same `route()`. **Only `bd bdp serve` mints**, through the **one staged startup sequence** in the spec's Part A (shared gate → temporary source reads the Scope row → close → release → only when there is no Scope row: exclusive gate → reopen, re-check, mint → close → release → shared gate → serving source → re-evaluate the identity table under it → serve; the tree forbids a shared→exclusive upgrade, and the conformance case asserts the shared gate is released before the exclusive acquisition). `bd bdp serve` inherits `errServeReadonly` and refuses without a held Scope; **`bd serve` converts every graph failure into "rows absent + notice"** and is byte-identical without a URL. **v0 serves BDP only from SQL-server workspaces** (the unit-of-work leg): embedded Dolt never serves, and a **registered backend's store arm** — which may itself be embedded — has no fence, so its rows are absent in v0; an out-of-tree backend cannot import an `internal/` capability, so the seam it would declare is the deferred ADR's to define (a public interface or an in-tree adapter), not `graphcap`'s. | Rounds 2–5. |
| A3 | **12** / §4 lifecycle | verbs "reserved now" | **Everything under `bd bdp …`**, with a `CommandPath()`-keyed policy authoritative at every leaf-name call site (paired Cobra-walk and source-scan tests). | Rounds 2–4. |
| A4 | §3 layering | values moved silently | **Record it:** values, laws, and roles in public `graphops`; accessors named `BeadGraph*`. | Package layout is public API. |
| A5 | **11** (ledger "restorable independently … its own migration") | dolt-ignored table (v2); project-id file (v3); hostname key (v4) | **(i) The clone-local half is `.beads/graph-authority.local.json`**, bound to an **installation key**: a random id created once, `O_EXCL`, mode 0600, re-read after creation, directory fsynced, in the user config directory the tree's `UserConfigYamlPath` resolver already chooses (`~/.config/bd`, native fallback) — plus the canonical `.beads` path; never the hostname. Written only by the witness manager under a bounded exclusive lock; **transitions are multi-phase** (`begun → local_committed → published → config_written → finalized`, recording a **durable operation id** — a hash-covered, indexed `op_id` column on every ledger event, and the commit message — the pre-operation HEAD and ledger head, the operation commit, the expected graph roots, the remote pre-head, the pre-operation lease row, and the config intent), recovered on the next load by **evidence, not by phase alone**: the ledger is checked for the operation id (a crash between the commit and the phase write leaves `begun` with the operation committed), then the remote is classified as {still at the pre-head, contains this operation, contains foreign work}, and recovery resumes publication, finalizes, or undoes accordingly; on a shared database with no remote a locally committed transition counts as published, **executes any outstanding `config_intent`**, records `config_written`, and only then finalizes. An **undo is itself phased** (`undo_started → versioned_undone → lease_restored`) and resumes from its recorded phase regardless of whether the operation still appears in the ledger; in-process renewal and graph mutation pause for its duration; the lease is restored **only on the reset path and only if it still names this workspace**. `Advance` is a **descendant-aware compare-and-advance** that holds the file lock while a **provider-supplied verifier** (ledger-prefix and commit-ancestry checks, run in the store) decides: a candidate already contained in the current witness is a successful no-op, newer fields are never replaced by older ones, and a fork is rejected — commits may complete in one order and advance in another; opaque hashes alone cannot decide this, so the manager never tries to. The manager ensures the ignore entries and refuses a git-tracked path; a tracked witness is an **error-class** doctor finding (`sensitiveFileNames`). **(ii) The ledger is an append-only, hash-chained event table** (`mint`, `install`, `update`, `promote`, `rotate`, `allocate`, `tombstone`, `refuse_url`) with a single-row counter; the witness records the head `{seq, hash}`. **No event exists before mint.** The ledger lane (`bd bdp ledger snapshot|apply`) restores **anti-reuse history, not graph content**: an applied `allocate` whose row is absent becomes a `reserved` allocation (never reusable; reads answer gone), the Scope lineage is replayed, and the counter is set to `last_seq + 1`; full recovery is a database restore *plus* the lane. **(iii)** providers declare `LedgerDurability`; `bd bdp restore` rotates unless continuity is shown. **Stated residuals:** a whole-directory or whole-installation copy carries the witness and is undetectable without an arbiter — so an *authority-preserving* deployment must have one (a shared database; under A9 nothing else counts); without A9, an embedded, no-remote workspace's authority is single-copy **by operator responsibility only**, and copied or snapshot-restored state there must be treated as a new Scope. | Rounds 2–5. |
| A6 | §4 lifecycle (metadata.json; `BD_BDP_TOKEN`; `BDP_SCOPE_URL`) | both files; env token | **`bdp.scope_url` is a project fact in tracked `config.yaml`** (yaml-only; `BDP_SCOPE_URL` read first, then `BD_BDP_SCOPE_URL`). A mint with `--scope-url` writes it as the transition's `config_written` phase. **Once this workspace holds a witness, `bd config set`/`set-many`/`unset` refuse the key** (one guard in the shared `rejectProtectedConfigKey` path; a DB-free file check) — the URL then changes only through `bd bdp promote --rotate-url` / `bd bdp restore`, which are refused while an overriding `BDP_SCOPE_URL` is exported. **`bdp.client`, `bdp.server`, `bdp.insecure_http` live in `config.local.yaml`** via `bd init --bdp-server` and `bd bdp client`. The three new `.beads/` files join the gitignore template, `requiredPatterns`, and `trackedRuntimePatterns`; only the witness joins `sensitiveFileNames` (error class). No env-carried token; no token key in config; `bdp.client` blocked from env. | Rounds 2–5. |
| A7 | **9** (promotion "explicit and epoch-rotating") — *new* | v5 partitioned by hazard but fenced a subset | **Fences compose by hazard; every replicated graph mutation is fenced inside its transaction and, on hazard R, published by one primitive.** *Hazard S — a shared database* (every SQL-server topology): the dolt-ignored **`graph_authority_lease`** row (the `leases`/`RunTxEphemeral` precedent) with the **fence cell** predicate in A1 — the holder is the **workspace (installation key)**, so the serving process and the workspace's own CLI verbs share it (`promote` and `rotate` take the *shared* gate and rely on the lease; `types install`, `restore`, and `ledger apply` take the exclusive gate and require the server stopped); renewal is ephemeral, rewrites the fence, and extends `expires_at`; an expired lease naming this workspace is self-regranted by the next write **regardless of expiry**; a lease naming another holder is taken **only with `--steal`** — its expiry alone never grants a takeover — and the epoch is CASed. **A lease row is not proof of "minted here"**: `dolt clone` omits the dolt-ignored table, but `DOLT_BACKUP` restore and a directory copy carry the working set with it, and Dolt's `@@server_uuid` is per machine (`~/.dolt/config_global.json`; probed identical for a same-machine copy), so no in-band identity distinguishes a copied database served by a second `sql-server`. The lease row is therefore bound to `scope_url` and `authority_id`, and in-place promotion has exactly two paths: **self-regrant** when the row names this workspace, and **`--steal`**, an operator assertion of "same database" in the class of force-push and `bd sql`; a foreign holder's *expiry alone* never grants a takeover. `Promote --rotate-url` is the bootstrap that creates a new lease row under a new URL (refusing the old one forever) — the path for a clone, a restore that cannot show continuity, and a copy. Residual, stated: a `--steal` on a copied database creates a second authority; a restored copy is caught first by the witness's ledger head (`ErrStateRewound`). Throughput model, stated: concurrent graph mutations in one workspace serialize pairwise on the fence cell, and a fenced transaction that spans a renewal loses whenever the renewal commits first, so **every fenced transaction that runs beside a possible renewer (the shared-gate context) carries a deadline below a third of the TTL** (cancelled and retried past it — the replay budget must allow more than one attempt), in-process renewal is serialized against the process's own mutations with jittered cadence, and **`types install` runs under the exclusive gate**, where the sole writer sets `expires_at` itself and runs unbounded. **The file lock is held continuously from `Begin` through `Finalize`/`Abandon`**, and recovery acquires it first, so a live transition is never mistaken for crash residue. *Hazard R — a configured remote*: **every** replicated graph mutation (`Mint`, `Promote`, `Rotate`, `Install`, `LedgerApply`, the P1 seeds, P3 writes) runs through **`PublishGraphMutation`** on the provider: `DOLT_FETCH`; require the remote-tracking HEAD to be an **ancestor** of local HEAD (`DOLT_MERGE_BASE`); record the remote's graph roots and ledger head; the fenced transaction; a **scoped** commit (`DOLT_ADD` of the graph tables, `DOLT_COMMIT -m` — never `-Am`; a new `RunTxScopedResult` on the UOW leg, whose `Commit` hardcodes `-Am` today); `DOLT_PUSH` classified by a **typed** lift of the tree's untyped `pushRacePattern` (it already matches all three race routes) — on a race, refetch and compare **both** the remote-tracking ref's **ledger head** (`SELECT seq, hash FROM graph_ledger_events AS OF '<ref>' ORDER BY seq DESC LIMIT 1` — `MAX(seq), hash` errors under `ONLY_FULL_GROUP_BY`, probed) **and the eight graph tables' diff** between the recorded remote pre-head and the new remote head (`DOLT_DIFF_STAT`/`dolt_diff` restricted to those tables; `DOLT_HASHOF_TABLE` reads only the working set) against what was recorded before the transaction: any graph delta or ledger movement → fail closed and undo — **never the `(authority_id, epoch)` tuple alone**, which a same-witness twin (a VM image or shared home) or a same-authority fork shares; neither changed (issue-plane divergence only) → keep the commit, `ErrSyncRequired`; any other failure → keep the commit, mark `unpublished`, retry later. The classifier lifts the **whole** of `isPushRaceErr` — its diverged-history and ancestor-PK-mismatch exclusions included — behind a typed lower-layer error. The remote-tracking ref is `remotes/<remote>/<branch>`, built by one provider API from the configured sync remote and the active branch (the `verifyPullLanded` spelling); a missing ref is vacuously an ancestor (the push creates it); an empty remote ledger reads as no head. Undo = `DOLT_RESET --soft <pre-op>`, then per graph table `DOLT_RESET('<table>')` (unstage) and `DOLT_CHECKOUT('<table>')` — a bare checkout after a soft reset restores from the *staged* root and reverts nothing (probed) — when HEAD is still the operation commit, `DOLT_REVERT <op commit>` otherwise; then, **on either path**, a compensating ephemeral write restores the pre-operation **lease** row — predicated on the lease still naming this workspace at the operation's epoch and on the current fence, and writing a fresh fence (a revert restores the Scope epoch while the ignored lease keeps the new one, which would lock the workspace out of its own lease — probed); if another holder now legitimately holds the lease, it is left alone; the undo runs before any other graph mutation. **Reads on hazard R** require a remote observation no older than `bdp.authority_heartbeat` (a serving process fetches on a timer; a CLI read fetches when stale), **comparing the remote ledger head against the workspace's witness — reloaded on every check — not against a process-local expectation**, and recognizing a **pending local operation** (a publication that landed before the witness advanced, or a `local_committed` record) as this workspace's own, so a publication by the workspace's CLI is a re-arm, not a loss; they fail closed after the grace. Recovery on hazard R reuses the same ledger-plus-eight-table delta classifier as the push race: issue-plane-only movement is `ErrSyncRequired`, never an undo. The fence watcher is a state machine `held → renewing → lost`: on loss the BDP rows are disabled atomically (legacy surface untouched), `bd bdp serve` exits 3, and the watcher joins before provider shutdown. *Both hazards → both fences.* A clone that received the Scope row by `dolt clone` has no lease row and bootstraps only with `bd bdp promote --rotate-url`; a copy or restore that carries the row is the operator hazard stated above. Services on an ephemeral home pin `BEADS_INSTALLATION_ID_FILE` to persistent storage, or every restart is a `--steal`. A `promote --rotate-url` beside a live server changes the served URL and the host allowlist, which are fixed at startup, so the server exits 3 and is restarted. Force-push routes bypass the fence as operator acts. | Round 5 (Codex C2–C4, H5, H7, H10, H11; Gemini). |
| A8 | plan §1 constraint #1 and ruling 12 — *new* | v5's method-set reasoning was inverted | **Two options; the docs do not pre-empt.** **A (recommended):** constraint #1 scoped to *behavior* (byte-identical gate output). Adding six required methods to `Storage` breaks **direct implementers** (the compiler catches them; six `ErrUnsupported` stubs; the joint `ReadyClaimer`/`BatchCloser` CHANGELOG entry is the precedent) and is **silently promoted through every wrapper that embeds the interface** — which is why the censuses are mandatory, not optional. **B:** an optional `BeadGraphCapable` interface on the concrete store is **not** promoted through an interface-embedding wrapper at all, so every wrapper must implement it explicitly *and* every consumer needs a resolver/unwrap — the v1-plan `graphsource` shape. | Round 5 (Codex M14, Gemini). |
| A9 | **9** — *new, optional simplification* | — | **v0 authority requires a shared database.** If ruled: hazard R's publication primitive, remote-read freshness, and multi-phase publication recovery are **deferred to the write-profile ADR**; a remote-backed workspace is a **non-authority** for any Scope it did not mint on its own shared database (its `bd serve` shows rows absent; its CLI reads refuse); cross-database promotion is **`--rotate-url` only** (a new Scope, continuity via the ledger lane). The graph tables still replicate through push/pull like any other table; the validator refuses foreign deltas. What remains of A7 is hazard S alone — one arbiter, one lease, one counter. **Cut sheet, normative if ruled — the topology matrix:** a shared database that **minted locally** is authoritative regardless of any configured remote; a shared database that received the Scope row by replication, restore, or copy without a valid local authority refuses (`ErrNotAuthority`; `--rotate-url` or `--steal` as above); the embedded and registered-store arms refuse every authority-establishing and authority-dependent local operation (`Mint`/`Promote`/`Rotate`/`Install` and local graph reads answer `ErrNotAuthority: authority requires a shared database`; the embedded leg exists only as the `bdp.client: server` host, and client-mode reads stay allowed everywhere); a configured remote neither grants nor removes authority, and **transition recovery takes the local-operation path on every shared database** (the remote classification exists only when hazard R is in force); two shared databases that both mint under one tracked `bdp.scope_url` are settled by the replication/merge ADR — until it lands, the earlier `mint` event (lineage) wins and the loser rotates; every hazard-R passage (publication primitive, remote-read freshness, publication recovery, `Remote` fixture cases) is excluded — but the default-branch rule for the ignored lease table (Part D.5) stays, because it is a property of dolt-ignored tables, not of hazard R; A5's "single-copy by operator responsibility" sentence is withdrawn, and physical database copies are the stated operator-managed hazard. **Recommended for v0**: the only serving topology already is the SQL-server workspace, and this removes the largest block of new mechanism from P1/P2. | Round 5 (Codex C1: "require an externally observable fence or weaken the guarantee"). |

Two decisions the plan does not yet contain, surfaced for ruling rather
than designed around:

- **Enforcement boundary for out-of-role writes.** `bd sql`, the proxied
  `RawSQLUseCase`, force-push, and a merge can change graph tables without
  allocation, authority, revision, or owned-Link coupling checks. v6's
  position (§7): out of contract; the **state-change validator** rejects
  invalid or foreign-authority graph state; DB-privilege or trigger
  enforcement is a C-lane verification task. To be ruled before P3.
- **Replication/merge ADR as a P1 gate.** The merge entry points are not
  four Go functions: `mergesettle.go` exports seven, `fastforward.go` and
  `automerge.go` more; `CALL DOLT_PULL` merges inside Dolt on every pull
  route; the UOW leg's `doltVersionControlSQLRepository` calls
  `DOLT_MERGE`/`DOLT_PULL`/`DOLT_PUSH` directly; embedded federation sync
  fetches and merges; the remote-migrate gate does a fast-forward
  `DOLT_MERGE`. The validator runs **on every observed graph-state-version
  change**: the witness
  records the graph-state version — `DOLT_HASHOF_TABLE('<name>')` for each
  of the eight replicated graph tables in a fixed order, hashed together
  (the function exists in Dolt 2.1.8 and takes one table argument; the
  lease is ephemeral and excluded) and the HEAD commit; a body that sees a
  different version returns `ErrStateChanged` *without* validating inside
  the held transaction; the accessor validates under **one singleflight
  coordinator per provider instance** in its own transaction (ancestry
  `DOLT_MERGE_BASE(recorded HEAD, HEAD)`; row provenance for foreign
  updates), advances the witness, and retries once. Descriptor caches are
  keyed by the descriptors table's hash. Prefer refusal of
  foreign-authority deltas over invented merge rules. Lands before the
  graph migrations.

What none of this changes: ruling 9's level — the authority is the graph
store as reached through the normalized storage abstraction, on any
provider; Dolt is the reference realization; the CLI verbs and the BDP
handler are both clients of that abstraction.

## 3. Packages and their imports

```text
graphops/                        PUBLIC LEAF (sibling of issueops/, memoryops/)
  ├─ types.go                    Bead, Link, Ref, Properties, Revision, Attribution,
  │                              TypeDescriptor, OwnedLinkDecl, OwnedLinkGroup, ScopeIdentity,
  │                              LedgerEvent, LedgerManifest, Cursor (opaque)
  ├─ laws.go                     canonical-ID grammar, code-unit ordering, JSON canonicalization,
  │                              RFC 6902 §4.6 equality, Scope-URL validation, ledger hashing
  ├─ reader.go                   Reader: Bead, Link, Beads, Links, IncidentLinks
  ├─ types_role.go               DescriptorReader: Descriptors, Descriptor
  │                              TypeInstaller: Install (post-mint; published on hazard R)
  ├─ identity.go                 IdentityReader: Read, LedgerDurability
  │                              ScopeBootstrapper: Mint (once; installs the built-in catalog)
  │                              Admin: Promote, Rotate, LedgerSnapshot, LedgerApply,
  │                                     MarkUnverified, ClearUnverified
  (transaction-bound provider capabilities — StateVersioner, GraphPublication, LeaseClaim —
   live in internal/storage/graphcap, not here: the public leaf carries no runner type)
  ├─ writer.go                   (P3) Writer
  └─ errors.go                   ErrNotFound, ErrValidation, GoneError{Path, State},
                                 ErrNoScope, ErrScopeExists, ErrNotAuthority, ErrStateRewound,
                                 ErrStateChanged, ErrSyncRequired, ErrUnpublished, ErrURLReused,
                                 ErrRepresentationTooLarge, ErrNotServedYet
  NO authority fields on any request type — the witness is the store's.
  imports: stdlib + beadserrors ONLY

internal/storage/authority/      THE WITNESS MANAGER (clone-local half; no SQL)
  Witness{InstallationKey, ScopeURL, AuthorityID, Epoch, LedgerSeq, LedgerHash,
          StateVersion, StateCommit, Unverified, GrantedAt, Pending *Transition}
  Transition{Kind, OpID, Phase (begun|local_committed|published|config_written|
             undo_started|versioned_undone|lease_restored), PreHead, PreLedgerHead, PreLease,
             OpCommit, ExpectedRoots, RemotePreHead, ConfigIntent}
  Advance takes a provider-supplied Verifier (ledger-prefix + ancestry; under hazard S the
    ledger seq is a total order, so seq decides) and runs it under the file lock; the lock is
    held from Begin through Finalize/Abandon; LeaseClaim lives in internal/storage/graphcap
  installation id: the directory user_config_path.go's UserConfigYamlPath chooses
    (~/.config/bd preferred; native fallback), file installation-id, or BEADS_INSTALLATION_ID_FILE;
    created O_EXCL 0600, re-read, directory fsynced; per OS user (a service user and an
    operator compute different keys — cross-user shared roots are unsupported, as the
    workspace gate already declares); an ephemeral home regenerates it (promote again)
  Load: plain read; a Pending record → recovery by evidence (the ledger carries the op id?
    the remote: pre-head / contains this op / foreign?) → resume, finalize, or undo — BEFORE
    any assertion; run by admin verbs and bd bdp serve only; bd serve and CLI reads treat
    Pending as rows-absent / ErrNotAuthority with a notice
  Advance: bounded exclusive flock on .beads/graph-authority.lock (internal/lockfile;
    poll with timeout, the workspacegate precedent; both ErrLocked and ErrLockBusy honored)
    → descendant-aware compare-and-advance (older contained candidate = no-op; fork = reject)
    → atomicfile → fsync dir
  Begin / SetPhase / Finalize / Abandon; preflight ENSURES the .beads/.gitignore entries and
    refuses a git-tracked path

internal/storage/graphops/       TX-LEVEL SHARED BODY — all three legs call it
  type DBTX interface { ExecContext; QueryContext; QueryRowContext }
  assertAuthorityInTx(ctx, tx, w, claim, mutating): Scope row == w; ledger head; lease row —
    read: SELECT holder/epoch/expires_at/fence and require expires_at > NOW(6) (reads never
    write the lease; a CLI read regrants once, ephemerally, before a fresh read tx when its own
    lease has expired); mutation: UPDATE … SET heartbeat_at = ?,
    expires_at = NOW(6) + ttl, fence = <fresh random, regenerated per retry> WHERE id = 1
    AND holder_installation_key = ? AND epoch = ? AND fence = <value just read> with exactly
    one affected row (same-cell-different-value → 1213 → replay re-evaluates: still
    authorized → succeeds serially; superseded → 0 rows → refuse); the 1213 surfaces from
    CALL DOLT_COMMIT on the scoped path, so that call stays inside the retried closure;
    graph-state version == w.StateVersion else ErrStateChanged
  nextLedgerSeqInTx: UPDATE graph_ledger_seq SET next_seq = next_seq + 1, alloc_nonce = <random>
    WHERE id = 0 (the random cell is what makes two allocators conflict)
  ReadBeadInTx / … / MintScopeInTx / PromoteInTx / RotateInTx / LedgerSnapshotInTx /
  LedgerApplyInTx (anti-reuse history; reserved allocations; counter set) /
  ValidateStateInTx / graphStateVersionInTx: eight calls DOLT_HASHOF_TABLE('<validated name>')
    in the fixed order; StateVersion = sha256 of the concatenated hashes
  SeedBeadInTx / SeedLinkInTx — fixture writer; _test.go call sites only

internal/storage/dolt/beadgraph_*.go, internal/storage/embeddeddolt/beadgraph_*.go
  CLI legs (embedded never serves): witness + claim loaded per call; withReadTx /
  withRetryTx; scoped commit (DOLT_ADD graph tables + DOLT_COMMIT -m — the
  doltAddAndCommit precedent); PublishGraphMutation for hazard R

internal/storage/domain/db/beadgraph.go + doltVersionControlSQLRepository additions
  BeadGraphUseCase over db.Runner; the repository gains Fetch/Push (exist), MergeBase,
  ResetSoft, CheckoutTables, Revert, HashOfTables
internal/storage/uow/beadgraph_*.go        THE SERVING LEG
  uow.UnitOfWork gains BeadGraphUseCase(); doltSQLProvider gains beadsDir (received by
  cmd/bd/uow_factory.go's newSQLServerUOWProvider today, dropped) and timedProvider gains a
  getter for it (it builds roles over the wrapper today); NEW
  RunTxScopedResult(tables, msg) — doltServerTx.Commit hardcodes DOLT_COMMIT('-Am') today;
  RunTxEphemeral for lease renewal; PublishGraphMutation on the provider

internal/storage/storage.go        + six BeadGraph* accessors
internal/storage/hook_beadgraph_*.go, internal/telemetry/beadgraph_*.go, backend/conformance/…
internal/httpapi/bdp_routes.go     bdpRouteTable — conditional rows behind route() (P2);
                                   FenceSource state machine (held → renewing → lost)
internal/httpapi/bdpwire/          GENERATED DTOs from the vendored, pinned schema — P0
internal/bdpclient/                graphops.Reader/DescriptorReader over the wire
cmd/bd/bdp*.go                     `bd bdp` root; bdpRootPolicy keyed by CommandPath();
                                   bdp_serve.go staged startup (exclusive mint, then shared serve)
cmd/bd/backup_restore.go           runBackupRestore → Admin.MarkUnverified (no-op without a witness)
```

Dependency direction, enforced: `cmd/bd → graphops, storage accessors,
bdpclient`; `internal/httpapi → graphops, bdpwire`; `internal/storage/* →
graphops, authority`; `graphops → beadserrors, stdlib`. A new depguard rule
denies `internal/storage/graphops` to everything but the three legs and
`domain/db`.

## 4. Roles — how many, and why

The house test: a role is a **different question**, born whole with the
methods that are shapes of one question; and *can one caller be entitled
to the read and not the write?* — if yes, two roles. Six, each behind its
own `BeadGraph*` accessor. **Authority is never a parameter.** Transitions
produce the witness and carry their own preconditions instead of the head
check — named per method.

- **`graphops.Reader`** — one record by path (grouped, bounded
  `ownedLinks`), keyset pages under an opaque cursor, incident Links.
  Protected.
- **`graphops.DescriptorReader`** — the ordered catalog and a keyed lookup.
  Protected. Bounded.
- **`graphops.TypeInstaller`** — post-mint install/converge with `install`
  events; a fenced mutation, **published through the same primitive as
  every other replicated mutation** on hazard R. P1. Before mint there is
  no install: the built-in catalog is installed by `Mint`.
- **`graphops.IdentityReader`** — the Scope row, the witness's claim, and
  `LedgerDurability`. **Exempt** (it reports state).
- **`graphops.ScopeBootstrapper`** — `Mint`, once, under the exclusive gate
  through a temporary source (the spec's one staged-startup sequence: shared
  read → release → exclusive only when there is no Scope row → release →
  shared serve), fenced per hazard, multi-phase: precondition *no Scope row*; INSERT the singleton Scope row, seed
  the counter, append `mint`, install the built-in catalog with its
  `install` events — one transaction, one scoped commit, published on
  hazard R, `config.yaml` written when `--scope-url` supplied it; then the
  witness is finalized.
- **`graphops.Admin`** — `Promote` (precondition: Scope row CAS + the
  fence; produces the witness), `Rotate` (precondition: Scope-row lineage
  and the lease; `refuse_url(old)` + `rotate(new)` in one
  transaction; `config.yaml` in the `config_written` phase; refused while
  `BDP_SCOPE_URL` overrides), `LedgerSnapshot`, `LedgerApply` (recovery
  predicate; anti-reuse history only; regrants), `MarkUnverified`/
  `ClearUnverified`. Authorized by being the local administrative
  composition root — `Promote` and `Rotate` under the **shared** gate (they
  rely on the lease and run beside a live server), `Install`, `LedgerApply`,
  and `bd bdp restore` under the **exclusive** gate; `httpapi.GraphConfig`
  has no field for it.
- **`graphops.Writer`** (P3, not now).

## 5. A read, end to end

```text
client ──GET /acme/beads/x──▶ internal/httpapi (bd serve | bd bdp serve; UOW leg)
   │ route(): deadline → bearer → Bd-Project-Id stamp (absent = pass) → database slot
   ▼
bdp handler: path grammar (graphops laws) → ONE role call
   ▼
graphops.Reader.Bead(ctx, BeadRequest{Path})          ← no authority fields
   ▼
BeadGraphReader (manager-backed role): w := authority.Load(beadsDir); claim := process claim
   │ hazard R: require a remote observation ≤ bdp.authority_heartbeat old (fetch if stale;
   │           fail closed past the grace)   [absent under A9]
   ▼ RunTxRead ─▶ storage/graphops.ReadBeadInTx(ctx, runner, w, claim, req)
   │ stmt 1  Scope row: (scope_url, authority_id, epoch) == w
   │ stmt 2  ledger head: event at w.LedgerSeq has hash w.LedgerHash; MAX(seq) >= w.LedgerSeq
   │ stmt 3  lease row: holder key/epoch == claim AND expires_at > NOW(6)   (SELECT — a read never
   │         writes the lease; a mutation UPDATEs it, rewriting the fence cell and extending
   │         expires_at, self-regranting if expired; a CLI read on its own expired lease regrants
   │         once ephemerally before a fresh read tx)
   │ stmt 4  graph-state version: DOLT_HASHOF_TABLE('<name>') × 8 in fixed order, sha256 of
   │         the concatenation == w.StateVersion → else ErrStateChanged (returned WITHOUT validating here;
   │         the accessor validates under the provider's singleflight coordinator, advances the
   │         witness, retries once)
   │ stmt 5  bead row
   │ stmt 6  descriptors for the Bead's Type (cache keyed by the descriptors table's hash,
   │         which stmt 4 already read)
   │ stmt 7  ONE batched owned-links query, LIMIT (bound − rows so far) + 1
   ▼
graphops.BeadRecord {Bead, OwnedLinks []OwnedLinkGroup}
   ▼
bdp handler: bdpwire DTO ← record; typed error → BDP Problem; JSON out
```

Statement budgets are per role method (spec B2). A write (P3): the body
asserts **mutatingly** (the lease `UPDATE` with one affected row), runs the
no-op gate, records attribution, versions the source on owned-Link
mutation, takes the next `seq`, appends the event, stamps provenance; the
accessor commits **scoped**, **publishes on hazard R**, and only then
advances the witness — DB first, file second.

## 6. Where the twelve rulings land

| Ruling | Lands in |
| --- | --- |
| 1 charter | `engdocs/PROJECT_CHARTER.md` edit rides the first merged slice |
| 2 substrate S1 | replicated migrations (scope, history, descriptors, beads, links, ledger events + counter, allocations) + the lease's three parts (`doltIgnorePatterns`, main-series creation, ignored-series twin) + `ignoredSource.sentinelTables` gains the lease |
| 3 allocation ledger | `graph_allocations` (derived; `reserved` state after a ledger-lane apply); `graph_ledger_events`; `graph_ledger_seq` |
| 5 withdrawal | nothing projects Issues; structural |
| 7a Scope URL | `bdp.scope_url` in tracked `config.yaml` (`BDP_SCOPE_URL` first); singleton Scope row; no dev-mode derivation |
| 7b listener | BDP rows behind `httpapi.route()` (A2) |
| 8 changefeed | P3 |
| 9 authority | store-owned witness in every transaction (A1); fences by hazard (A7) or hazard S only (A9); contract cases incl. clone, restore, copied witness, expired-lease mutation, nonce mismatch, graph delta under an unchanged tuple, revert when HEAD moved, each phase's recovery |
| 11 restore vs identity | hash-chained head in the witness; multi-phase transitions; ledger lane = anti-reuse history; `LedgerDurability`; rotation (A5) |
| 12 store/Scope/client | `bd init` migrations only; `bd bdp serve` mints (staged); `bd bdp client` (A6); A8 options; registered backends absent in v0 |
| 6 wisps | not served |

## 7. What is deliberately not designed here

- Cross-request cursor continuation (P2 ADR); no collection routes before it.
- The write profiles' wire (W1), `graphops.Writer`, per-token authorization
  classes, the push-on-commit latency budget, and the
  acknowledged-but-unwitnessed-write window (P3).
- The replication/merge ADR (§2b). Precedes the migrations.
- The enforcement boundary for out-of-role DML beyond the validator.
- The `GraphPublication` capability a registered backend would declare to
  serve BDP (deferred; v0 serves from SQL-server workspaces only).
- Whether `bd bdp serve` remains after W2 (default: yes — the minting path).
- Type generation from the bead-type inventory (W3) — it feeds the built-in
  catalog `Mint` installs.
