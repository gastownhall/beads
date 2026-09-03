# BDP graph store — CLI and storage-interface changes, in detail

**Status:** Draft v10 (W-arch, after eight council rounds; the fence cell and the counter nonce are probe-confirmed on Dolt 2.1.8) — held for the operator's rulings — feat/bead-graph
**Date:** 2026-09-02
**Companions:** `BDP_BEAD_GRAPH_PLAN.md` (rulings), `BDP_GRAPH_ARCHITECTURE.md`
(shape; its §2b lists the proposed ruling amendments A1–A9 this spec
assumes — **none is ruled yet**; A8-dependent sentences say so, and every
hazard-R paragraph is marked *[deferred under A9]*). This document is the
*diff*: every command, flag, config key, interface member, package,
migration, and gate the graph work adds or touches — and what it does not
touch (Part C) and what it changes that an earlier draft claimed it did not
(Part C2). Phase markers follow the plan's §7: **P0** contracts and wire,
**P1** storage, **P2** serving, **P3** writes.

## Part A — CLI

All graph-store commands live under one root, **`bd bdp`** (amendment A3):
`bd link`, `bd graph`, `bd restore`, and `bd promote` are existing verbs
with positional arguments, and the plan's constraint #1 forbids changing
them. The differential gate gains one row per legacy form of each (Part B7).

**Root store policy is keyed by command path for this subtree, and it is
authoritative.** The root command classifies commands by *leaf name* at
more sites than the obvious ones — `effectiveRootStorePolicy(cmd.Name(), …)`,
`runsPostCommandMaintenance(cmd.Name(), …)`, `isReadOnlyCommand`
(`readOnlyCommands`, which `context_cmd.go` mutates at init),
`shouldAutoPruneEventsJournal`, the `cmd.Name() != "import" && != "setup"`
branches, `workspace_gate.go`, `main_errors.go`. Leaf names under `bd bdp`
**may** coincide with those lists; the coincidence never governs. One
`commandPolicy(*cobra.Command)` keyed by `CommandPath()` is consulted first
at each site for any path under `bd bdp` (a new seam — the only
`CommandPath()`-keyed map today is `help_supplements.go`), with an
exhaustive Cobra-tree test **paired with a source scan** that fails on any
`cmd.Name()` consumer not routed through the policy.

| Verb | Local store | Gate | Maintenance |
| --- | --- | --- | --- |
| `bd bdp bead get\|list`, `link get\|list`, `types [get]`, `status` | read-only when `bdp.client: store`; **skipped entirely** when `bdp.client: server` | shared | no |
| `bd bdp serve` | serve's classification (A3); **staged** — the one normative sequence: bypass the generic pre-run store gate (the skip-store seam); acquire shared; open a temporary source, read the Scope row, close it; **release shared**; if there is no Scope row: acquire exclusive, reopen, re-check, mint, close, release exclusive; acquire shared; open the serving source; serve | shared → release → (exclusive → release, only for a mint) → shared; never an upgrade | no |
| `bd bdp client` | none (writes `config.local.yaml`) | none | no |
| `bd bdp promote` | writable, always local | **shared** — it relies on the lease (the workspace is the holder), so it runs beside a live `bd serve` | no (it commits — and publishes on hazard R — explicitly) |
| `bd bdp ledger snapshot` | read-only, local | shared | no |
| `bd bdp types install`, `restore`, `ledger apply` | writable, always local | **exclusive** (`internal/workspacegate`, the `bd backup restore` precedent) — the server must be stopped; a large catalog install cannot meet the fenced-transaction deadline beside a renewing server, so install is uniformly exclusive; every store-opening command holds the gate shared for its lifetime and an exclusive acquisition names the holder after a bounded poll | no |

### A1. `bd init` — graph store initialization (ruling 12)

1. Runs the graph migrations (Part B4): the replicated series, and the
   dolt-ignored `graph_authority_lease` through the tree's three-part
   mechanism — its name in `doltIgnorePatterns` (seeded by `MigrateUp`
   before either series), a main-series migration that creates it for
   existing workspaces, the ignored-series twin for fresh clones (the 0055 /
   `ignored/0012` shape; hygiene check D) — and the lease joins
   `ignoredSource.sentinelTables` so an at-latest but partially
   materialized clone repairs it.
2. **Installs no descriptors.** There is no ledger before mint; the
   built-in catalog is installed by `Mint` (A3/A4). `bd init` on any clone
   keeps working and installs nothing. A provider answering
   `*storage.ErrUnsupported` from the capability probe is skipped silently
   at the default verbosity (debug-level only), so gate output is
   byte-identical.
3. Writes **no Scope identity**, no witness, nothing to `metadata.json`.
4. Ensures `.beads/.gitignore` carries `config.local.yaml`,
   `graph-authority.local.json`, `graph-authority.lock` through
   `EnsureGitignoreForBeadsDir`; the template, `requiredPatterns`, and
   `trackedRuntimePatterns` gain them, and the witness joins
   **`sensitiveFileNames`** so a tracked copy is an **error**, not a
   warning. Init paths that bypass that call (`--init-if-missing`, an
   external `BEADS_DIR` that differs from the local dolt dir — the gate is
   `useLocalBeads`) are not relied on:
   the witness manager ensures the entries before its first write (B3).

**Registered backends:** `bd init` refuses to provision them today; their
own path owes the same obligations. In v0 they do **not serve BDP** (A3).

### A2. Client wiring — `bd init --bdp-server <url>` and `bd bdp client` (ruling 12; amendment A6)

One more `bd init` target, rerouting *above* the storage abstraction: the
`bd bdp` read verbs become a BDP client of the designated server.

**Two files, by what the key is.** `config.yaml` is git-tracked by default;
per-workspace keys go to **`config.local.yaml`** (merged by viper over
`config.yaml` for machine-specific settings; merged as the sibling of
the project-level `config.yaml` (never a user-level one); the `.beads`/basename requirement is
`yaml_config.go`'s `projectConfigPathFromLoadedState`, so the writer ensures
a project `config.yaml` exists and gets its own path plumbing).

| Key | File | Values | Notes |
| --- | --- | --- | --- |
| `bdp.scope_url` | `config.yaml` (tracked; yaml-only) | absolute URL | a **project** fact (ruling 7a). Settable while this workspace holds **no witness**; once it does, `bd config set` / `set-many` / `unset` refuse it (one guard in the shared `rejectProtectedConfigKey` path, which `set` and `set-many` call today and `unset` gains an explicit call to — a DB-free file check; a hand edit of the tracked file bypasses it, and the A3 "configured ≠ persisted" row is the real guard) and the URL changes only through `bd bdp promote --rotate-url` / `bd bdp restore`, which write it in their `config_written` phase and are refused while `BDP_SCOPE_URL` overrides it |
| `bdp.authority_heartbeat` | `config.yaml` | duration (default `30s`) | hazard R *[deferred under A9]* |
| `bdp.authority_heartbeat_grace` | `config.yaml` | count (default `3`) | hazard R *[deferred under A9]* |
| `bdp.lease_ttl` | `config.yaml` | duration (default `30s`, renewed every third) | hazard S |
| `bdp.client` | **`config.local.yaml`** | `store` (default) \| `server` | per-workspace; **not settable from env** (`blockedEnvVars`) |
| `bdp.server` | `config.local.yaml` | absolute URL | `https` required unless loopback or `bdp.insecure_http: true` |
| `bdp.insecure_http` | `config.local.yaml` | bool | the named waiver |

**Writers.** `bd init --bdp-server <url>` and **`bd bdp client server
--server <url> [--insecure-http]`** / **`bd bdp client store`** write the
per-workspace keys through one shared writer. Generic `bd config set`
accepts the `config.yaml` keys (yaml-only routing) and refuses the
per-workspace keys with "use `bd bdp client`". No token key in config
(`IsSecretKey` + the tracked-config guard; other trackers accept yaml-only
tokens — BDP chooses a file). Mechanics: `bdp.` joins the inline prefix
slice in `IsYamlOnlyKey` and `recognizedConfigPrefixes`; a `localOnlyKeys` class names the
three per-workspace keys; `validateYamlConfigValue` gains the entries.

**Environment.** `BD_<KEY>` via viper for the config keys; `BD_BDP_CLIENT`
blocked. `BDP_SCOPE_URL` is **read first, explicitly** (viper consults
`AutomaticEnv` before a `BindEnv` list — the GH#4645 `BD_ACTOR` precedent;
the `BEADS_ACTOR` shape), then `BD_BDP_SCOPE_URL`. The client token file is
**`BEADS_BDP_TOKEN_FILE`**; no `BD_BDP_TOKEN` (child-process inheritance).

**Credential lookup** (client): `BEADS_BDP_TOKEN_FILE` > credentials file
section `[bdp <origin><scope-path>]` with `token=`. No redirects followed;
`Authorization` only to the configured origin.

**Precedence, per command** (a table test pins it): `bd init --bdp-server`
— flag > env > existing `config.local.yaml`, writes `config.local.yaml`;
`bd bdp client` — writes `config.local.yaml`; `bd config set bdp.scope_url`
— no witness: writes `config.yaml`; witness held: refused; every `bd bdp`
verb — env (where permitted) > `config.local.yaml` > `config.yaml`;
`bd bdp status` — prints route, target, token source (never the token),
`insecure_http`, and the identity state row.

**In `client: server` mode** the verbs' `openBeadGraph*()` accessors return
a BDP-client realization of the same `graphops` roles (A5); `bd bdp serve`
there refuses unless `--serve-local-store`. Issue verbs are unaffected.

### A3. `bd bdp serve` — serving a Scope (rulings 7b, 9, 12; amendments A1, A2, A7, A9)

**v0 serves BDP only from SQL-server workspaces.** `bd serve` refuses
embedded Dolt permanently; every Dolt-server topology serves from the
unit-of-work provider — the serving leg, where every fence lives. A
**registered backend** is served from its store arm (`serveDatabaseSource`
routes it there; the backend may itself be embedded) and has no fence to
offer, so its BDP rows are **absent in v0** (`bd bdp serve` exit 2, typed);
the seam it would declare later is the deferred ADR's to define (an
out-of-tree module cannot import `internal/storage/graphcap`). Embedded
workspaces are **client hosts only under A9** (their local graph accessors
answer `ErrNotAuthority`); without A9 they are CLI-only.

`bd bdp serve` is a **thin command over the existing `internal/httpapi`
server**; two policies differ from `bd serve`: it **requires a Scope this
workspace holds** (exit 2 otherwise) and **it is the only command that
mints**, through a **staged startup** — the gate rules forbid a
shared→exclusive upgrade, so the sequence in Part A's table is the only
one: shared gate → temporary source reads the Scope row → close →
**release** → (no Scope row: exclusive gate → reopen and re-check → mint
(A4) → close → release) → shared gate → serving source → serve. `bd serve` mounts the rows only when
it holds an already-minted Scope, **converts every graph failure into "rows
absent + notice"**, and is byte-identical with no URL. Both inherit
`errServeReadonly` wholesale.

```text
bd bdp serve [--addr IP:PORT] [--allow-non-loopback] [--auth-token-file PATH]
             [--insecure-no-auth] [--allowed-host NAME]...          (serve's own flags/variables)
             [--scope-url URL]           first serve: mints under it and writes config.yaml
                                         (config_written phase); later: must equal the
                                         persisted Scope URL or refuse
             [--serve-local-store]       permit serving in a client: server workspace
```

No `--dev-local-test`. Behavior, in order:

1. **Classification:** `serveDatabaseSource`, verbatim; registered backend →
   rows absent (v0); embedded → refused; otherwise the UOW provider, which
   gains a `beadsDir` field (`cmd/bd/uow_factory.go`'s
   `newSQLServerUOWProvider` receives it today and forwards it to the
   journal and root resolution, but the provider struct itself keeps none;
   `timedProvider` gains a getter).
2. **Roles from the same source** (provider arm: the provider beneath
   `uow.UnwrapProvider`; `checkDatabaseSource`'s exactly-one-source rule
   extends to them).
3. **Capability probe, all-or-nothing:** `bd bdp serve` exit 2 on
   `ErrUnsupported`, abort on any other error; `bd serve` → rows absent +
   notice in both cases.
4. **Identity** — split by command; `bd serve` **never refuses and never
   mints**:

   | Persisted Scope | Configured URL | Witness | `bd bdp serve` | `bd serve` |
   | --- | --- | --- | --- | --- |
   | none | none | — | exit 2 | no BDP rows, silent |
   | none | set | — | **staged mint** (A4) then serve honestly empty | no BDP rows; notice "unminted; run `bd bdp serve`" |
   | present | none / **different** | any | exit 2 / refuse | no BDP rows; notice |
   | present, matches | matches | **absent** (clone; pull into a fresh dir; copy elsewhere — installation key mismatch) | refuse `ErrNotAuthority`; guidance: `bd bdp promote --steal` if this is the same database (a moved or re-keyed workspace — the lease names the old key), otherwise `bd bdp promote --rotate-url` | no BDP rows; notice |
   | present, matches | matches | **pending transition** | recovery first (B3, by evidence), then re-evaluate | no BDP rows; notice (plain `bd serve` never runs recovery — it never mints, fetches, or publishes; CLI reads answer `ErrNotAuthority` with the same notice) |
   | present, matches | matches | `(authority_id, epoch)` stale, or the lease held by **another** holder | refuse; guidance `bd bdp promote --steal` (an operator assertion of "same database"; a foreign holder's expiry alone never grants a takeover) or `--rotate-url` | no BDP rows; notice |
   | present, matches | matches | consistent but the lease **expired while still naming this workspace** (the server was down longer than the TTL) | **self-regrant** on the next lease write (same fence-cell predicate; no epoch change); serve | serve BDP rows |
   | present, matches | matches | ledger head not in the store (restore or different history) | refuse `ErrStateRewound`; guidance `bd bdp restore` | no BDP rows; notice |
   | present, matches | matches | `unverified` set | refuse until `bd bdp restore` | no BDP rows; notice |
   | present, matches | matches | consistent | take/renew the lease; serve | serve BDP rows |

5. **Host policy:** the Scope URL's host joins the allowlist; plaintext
   behind TLS termination.
6. **Fencing while serving — the watcher state machine** (`FenceSource`):
   `held → renewing → lost`. Hazard S: the lease is renewed every third of
   `bdp.lease_ttl` through `RunTxEphemeral` — the renewal rewrites `fence`
   **and extends `expires_at = NOW(6) + ttl`** — and asserted inside every
   transaction with the full predicate (B3); the watcher **rebuilds its claim
   from the witness on every renewal** (an in-workspace `promote --rotate-url`
   moves the epoch; a consistent witness makes that a re-arm, not a loss) with
   jittered cadence, serialized in-process against the process's own
   mutations; every fenced transaction in the shared-gate context carries a
   deadline below a third of the TTL and is cancelled and retried past it; a
   failed renewal past `expires_at` is `lost`, and so is a protected read
   whose remaining lease interval is shorter than its deadline — the serving
   leg never regrants on the read path. Hazard R *[deferred under A9]*: the tracking ref
   `remotes/<remote>/<branch>` is fetched every `bdp.authority_heartbeat` and
   its **ledger head** is read (`SELECT seq, hash FROM graph_ledger_events AS
   OF '<ref>' ORDER BY seq DESC LIMIT 1`); a head not contained in the
   **workspace's witness (reloaded on every check)**, or
   `bdp.authority_heartbeat_grace` missed fetches, is `lost` — the tuple
   alone cannot distinguish a same-witness twin, and a process-local
   expectation would mistake the workspace's own CLI publication for a loss.
   On `lost`: the BDP rows are **disabled atomically** (the legacy surface
   and its capability list untouched), `bd bdp serve` exits 3 after
   draining, `bd serve` logs and continues; the watcher **joins before the
   provider shuts down**. Both hazards → both watchers.
7. **Mounting:** `serveListen(opts, httpapi.Config{…, Graph: …})`.
8. **Lifecycle:** excluded from the post-command maintenance net by
   `commandPolicy`, not by the leaf name.

### A4. `bd bdp promote` / `restore` / `ledger` / `types install` (rulings 9, 11; amendments A5, A7, A9)

The only reachers of `BeadGraphAdmin()` and `BeadGraphTypeInstaller()`.
`promote` and `rotate` run under the **shared** gate and rely on the lease
(the workspace is the holder, so they run beside a live server);
`types install`, `restore`, and `ledger apply` run under the **exclusive**
gate with the server stopped. Every replicated graph mutation
(mint, promote, rotate, install, ledger apply, the P1 seeds, P3 writes)
runs through **one primitive on the provider, `PublishGraphMutation`**, and
transitions (mint, promote, rotate, apply) are **multi-phase** (B3).

**The primitive, per hazard.** *Hazard S:* the fenced transaction (lease
`UPDATE` rewriting the fence cell and extending `expires_at` with one
affected row — self-regranting an expired lease that still names this
workspace — the counter with its allocation nonce, rows, events, each
transition event carrying the transition's **operation id**), scoped commit
(`DOLT_ADD` graph tables + `DOLT_COMMIT -m` via the new `RunTxScopedResult`;
`doltServerTx.Commit` hardcodes `-Am` today), done. *Hazard R [deferred
under A9]:* record local HEAD; `DOLT_FETCH`; require the remote-tracking
HEAD to be an **ancestor** of local HEAD (`DOLT_MERGE_BASE`; unpushed
issue-plane commits do not block); record the remote's graph roots and
ledger head; the fenced transaction; scoped commit (phase
`local_committed`, op commit recorded); `DOLT_PUSH`, classified by a
**typed lift of the whole of `isPushRaceErr`** (`cmd/bd/sync.go`: its
`pushRacePattern` matches all three race routes — the SQL "behind its
remote counterpart", the CLI `! [rejected] … (non-fast-forward)`, and the
`git+*` `(stale info)`/`(fetch first)` forms — **and** its diverged-history
and ancestor-PK-mismatch exclusions, which must travel with it): **race** →
refetch and compare **both** the remote-tracking ref's **ledger head**
(`SELECT seq, hash FROM graph_ledger_events AS OF '<ref>' ORDER BY seq DESC
LIMIT 1` — `MAX(seq), hash` errors under Dolt's default
`ONLY_FULL_GROUP_BY`, probed) **and the eight graph tables' diff** between
the recorded remote pre-head and the new remote head (`DOLT_DIFF_STAT`
restricted to those tables) against what was recorded before the
transaction — any graph delta or ledger movement → **fail closed and
undo** (never the `(authority_id, epoch)` tuple alone: a VM-image twin or a
same-authority fork shares it); neither changed (issue-plane divergence
only) → keep the commit, `ErrSyncRequired`, retryable after `bd dolt pull`; **any other failure** →
keep the commit, phase stays `local_committed` with `unpublished` set,
retry on the next attempt. **Undo:** if HEAD is still the op commit,
`DOLT_RESET --soft <pre-op HEAD>`, then per graph table `DOLT_RESET('<table>')`
(unstage — a bare checkout after a soft reset restores from the *staged*
root and reverts nothing, probed) and `DOLT_CHECKOUT('<table>')` (or
`DOLT_CHECKOUT('HEAD', '--', '<table>')`), leaving unrelated dirty tables
untouched; if HEAD has moved, `DOLT_REVERT <op commit>` (later commits from
other actors are preserved). The versioned undo does not touch the
dolt-ignored lease, so **on either path, and only if the lease still
names this workspace at the operation's epoch**, a compensating
`RunTxEphemeral` write restores the pre-operation lease row — predicated
on the current fence and writing a fresh one (a `DOLT_REVERT` restores the
Scope epoch while the ignored lease keeps the operation's, which would lock
the workspace out of its own lease — probed); when another holder now
legitimately holds the lease, it is left alone. The undo is phased (`undo_started →
versioned_undone → lease_restored`), resumes from its recorded phase on the
next load whether or not the operation still appears in the ledger, and
pauses in-process renewal and graph mutation while it runs. Then `Abandon`.
`doltVersionControlSQLRepository` gains `MergeBase`, `ResetSoft`,
`UnstageTables`, `CheckoutTables`, `Revert`, `HashOfTables`; the remote
pre-head hash is read with `SELECT commit_hash FROM dolt_log AS OF
'<ref>' LIMIT 1` (`DOLT_HASHOF` rejects the `remotes/` spelling). Dolt's
staging area is shared, so a scoped `DOLT_COMMIT -m` still sweeps whatever
another session staged in the same tables — a stated limitation, in the
`bd sql` class. A missing remote-tracking ref is
vacuously an ancestor (the push creates it; an empty remote ledger reads as
no head); the ref is `remotes/<remote>/<branch>`, built by one provider API
from the configured sync remote and the active branch — the tree's
`verifyPullLanded` spelling, and the one `DOLT_MERGE_BASE` already receives. Table-scoped staging still sweeps rows
other sessions left uncommitted in the *same* graph tables — consistent
with `bd sql` being out of contract, and stated. *Both hazards → both.*
**A lease row is not proof of "minted here"** — `dolt clone` omits the
dolt-ignored table, but `DOLT_BACKUP` restore and a directory copy carry
the working set with it, and Dolt's `@@server_uuid` is per machine
(`~/.dolt/config_global.json`, identical for a same-machine copy — probed),
so nothing in-band distinguishes a copied database served by a second
`sql-server`. In-place promotion therefore has exactly two paths:
**self-regrant** when the lease row (bound to `scope_url` and
`authority_id`) names this workspace, and **`--steal`**, an operator
assertion of "same database" in the class of force-push and `bd sql`; a
foreign holder's expiry alone never grants a takeover. **`Promote
--rotate-url` is the bootstrap** that creates a new lease row under a new
URL and refuses the old one forever — the path for a clone, a copy, and a
restore that cannot show continuity. Under A9 this is the whole arbiter;
without A9 hazard R adds the remote fence. Stated residual: a `--steal` on
a copied database creates a second authority.

- **`Mint`** (`bd bdp serve`'s staged startup): precondition *no Scope
  row*; INSERT the singleton row, seed `graph_ledger_seq`, `mint` event,
  install the built-in catalog with `install` events; hazard S: take the
  lease; publish; `config_written` when `--scope-url` supplied the URL;
  finalize.
- **`bd bdp promote`**: precondition a consistent Scope row; three cases,
  no fourth. (1) The lease names this workspace → self-regrant regardless
  of expiry (no epoch change; the verb reports "already the authority").
  (2) It names another holder → only `--steal` (operator-confirmed) takes
  it: CAS the epoch (a lost race is a serialization loser → typed refusal)
  with a `promote` event carrying the `op_id`; publish; finalize. (3) No
  lease row → **refuse** unless `--rotate-url`, which creates the lease row
  under the new URL with `refuse_url(old)` + `rotate(new)`; publish;
  finalize. `--rotate-url` beside a live server makes the server exit 3
  (its served URL and host allowlist are fixed at startup). `--rotate-url <new>` rotates in the same
  transition (refused while `BDP_SCOPE_URL` is exported).
- **`bd bdp restore`**: runs after a database restore (`bd backup restore`
  — also reached by `bd bootstrap`'s restore action — calls
  `Admin.MarkUnverified` after `RestoreDatabase`, before its commit; a
  no-op without a witness). Exempt from the head check; precondition
  lineage match under the exclusive gate; branches on `LedgerDurability`:
  `in-state` (Dolt, v0) → requires a ledger snapshot reaching the witness's
  `{seq, hash}` (`--ledger <file>`, applied through `LedgerApply`) to show
  continuity, otherwise **rotates** (`refuse_url` + `rotate`, `config.yaml`,
  regrant); `independent` → re-validates (ancestry) and regrants; `none` →
  always rotates. Never silent.
- **`bd bdp ledger snapshot|apply`**: the hash-chained events as JSONL
  under a manifest `{scope_url, authority lineage, first_seq, last_seq,
  prev_hash of first, head_hash}`. `LedgerApply`'s recovery predicate:
  exclusive gate; lineage matches; the store's head equals the manifest's
  predecessor; every `hash` verifies. It restores **anti-reuse history,
  not graph content**: an applied `allocate` whose row is absent becomes a
  **`reserved`** allocation (never reusable; reads answer the gone family),
  the Scope lineage is replayed, `graph_scope_history` re-derived, and the
  counter set to `last_seq + 1`; then regrant. Full recovery is a database
  restore *plus* the lane.
- **`bd bdp types install <file>`**: post-mint catalog change (P1; W3
  emits the file): a fenced, published mutation with `install` events.

### A5. Graph verbs

```text
bd bdp bead get <path> | list [--type URL] [--after CURSOR] [--limit N]
bd bdp link get <path> | list [--type URL] [--source PATH] [--target REF] [--after CURSOR] [--limit N]
bd bdp types [get <url>] | types install <file>
bd bdp status
bd bdp client store | server --server URL [--insecure-http]
bd bdp serve | promote [--rotate-url URL] [--steal] | restore [--ledger FILE] | ledger snapshot|apply
(P3)  bd bdp bead create|update|delete ; bd bdp link create|update|delete
```

Each verb reaches its role through an accessor (the `cmd/bd/label.go`
pattern plus the client route), with a route-fork test in the shape of the
`*_proxied_integration_test.go` / `*_embedded_test.go` pairs. CLI reads on
the authority workspace read its own state — on hazard R they first
require a remote observation no older than the heartbeat interval *[under
A9: a configured remote is irrelevant — a shared database that minted
locally reads as the authority; one that did not refuses]*; on
any other workspace they refuse (`ErrNotAuthority`) — there is no replica
read in v0. `internal/bdpclient` maps Problems back to the typed errors
(round-trip test). No collection routes before the cursor ADR
(`ErrNotServedYet` on the client route).

## Part B — Storage interfaces

### B1. Accessors on `storage.Storage` (amendments A4, A8 — option A wording)

Named **`BeadGraph*`** (`GraphCounter()` counts the *issue* graph). Six
accessors — `BeadGraphReader`, `BeadGraphTypes`, `BeadGraphTypeInstaller`,
`BeadGraphIdentityReader`, `BeadGraphBootstrapper`, `BeadGraphAdmin` —
with the doc comments of v5 (manager-backed roles; every call reloads the
witness; no request carries authority; the admin role is never held by a
server). Added to **`storage.Storage`**. A required method is **promoted
through every wrapper that embeds the `DoltStorage` interface** — the
wrappers compile unchanged — so every decorator and provider wrapper
**declares** each accessor explicitly and the three censuses (B5) catch the
ones that do not; **direct implementers** (a custom store, mock, proxy) fail
to compile until they add the six stubs — the source break `storage.go`
declares, called out in CHANGELOG as the joint `ReadyClaimer`/`BatchCloser`
entry was. Under option B, an optional `BeadGraphCapable` interface on the
concrete store is **not** promoted through an interface-embedding wrapper,
so every wrapper implements it explicitly and every consumer resolves it.

### B2. The `graphops` leaf (public, repo root; amendment A4)

```go
package graphops   // imports: stdlib, beadserrors — nothing else

// ---- requests and results: NO authority fields anywhere (the witness is the store's)
type Cursor string            // OPAQUE: store-produced; binds Scope URL, epoch, selection hash, last path;
                              // P2 adds snapshot identity inside it — no public interface change
type BeadRequest        struct{ Path string }
type LinkRequest        struct{ Path string }
type BeadSelectRequest  struct{ TypeURL string; After Cursor; Limit int }
type LinkSelectRequest  struct{ TypeURL string; SourcePath string; Target *Ref; After Cursor; Limit int }
type IncidentRequest    struct{ Path string; Direction Direction /* In | Out | Both */; After Cursor; Limit int }
type DescriptorRequest  struct{ URL string }
type InstallRequest     struct{ Descriptors []TypeDescriptor }

type OwnedLinkGroup struct{ TypeURL string; Links []Link }   // the pinned schema keys ownedLinks by Link Type URL
type BeadRecord struct{ Bead Bead; OwnedLinks []OwnedLinkGroup }  // groups in code-unit order of TypeURL; Links in code-unit
                                                                   // order of path; an owned Type with no Links is an EMPTY group
type BeadPage struct{ Items []BeadRecord; Next Cursor }
type LinkPage struct{ Items []Link; Next Cursor }

type Reader interface {
    Bead(ctx, BeadRequest) (BeadRecord, error)
    Link(ctx, LinkRequest) (Link, error)
    Beads(ctx, BeadSelectRequest) (BeadPage, error)       // WHERE path > last ORDER BY path LIMIT n, binary-collated column
    Links(ctx, LinkSelectRequest) (LinkPage, error)
    IncidentLinks(ctx, IncidentRequest) (LinkPage, error) // one UNION over (source, target) indexes, ordered, limited
}
type DescriptorReader interface {
    Descriptors(ctx) ([]TypeDescriptor, error)            // ordered by URL; bounded by MaxCatalog
    Descriptor(ctx, DescriptorRequest) (TypeDescriptor, error)
}
type TypeInstaller interface {
    Install(ctx, InstallRequest) (InstallResult, error)   // post-mint; fenced; published on hazard R; idempotent by fingerprint
}
type IdentityReader interface {
    Read(ctx) (ScopeIdentity, error)                      // Scope row + witness claim (Held, Epoch, LedgerSeq, Unverified, Pending)
    LedgerDurability(ctx) (LedgerDurability, error)       // in-state | independent | none (ruling 11)
}
type ScopeBootstrapper interface {
    Mint(ctx, MintRequest) (ScopeIdentity, error)         // multi-phase; fenced per hazard; catalog installed inside
}
type Admin interface {
    Promote(ctx, PromoteRequest) (ScopeIdentity, error)   // multi-phase; fenced per hazard; RotateURL optional
    Rotate(ctx, RotateRequest) (ScopeIdentity, error)     // refuse_url(old) + rotate(new), one transaction; config.yaml in config_written
    LedgerSnapshot(ctx, LedgerRange) (LedgerManifest, []LedgerEvent, error)
    LedgerApply(ctx, LedgerManifest, []LedgerEvent) (LedgerApplyResult, error)   // recovery predicate; anti-reuse history; regrants
    MarkUnverified(ctx) error                             // no-op without a witness
    ClearUnverified(ctx) error
}

type AllocationState = string   // live | reserved | pruned | erased  ("reserved": ledger-applied, row absent)

// NOT here: transaction-bound provider capabilities live in internal/storage/graphcap —
//   StateVersioner { GraphStateVersion(ctx, tx DBTX) (StateVersion, error) }  // DOLT_HASHOF_TABLE('<name>') × 8, sha256
//   GraphPublication (a registered backend's own hazard-R fence; deferred)
//   LeaseClaim{InstallationKey, Epoch}
// — the public leaf carries no runner type, so the capability that takes one cannot be public.
```

**Values** (`Bead`, `Link`, `Ref`, `Properties`, `Revision`, `Attribution`,
`TypeDescriptor`, `OwnedLinkDecl`, `ScopeIdentity`, `LedgerEvent`,
`LedgerManifest`) have unexported fields and constructors that enforce the
laws in `laws.go`. `Properties` is the immutable raw-JSON object value from
the plan; its canonical bytes are what B4 stores. `Ref` is a sum: in-Scope
(`Path`) or external (`URL`). `Revision` is 128 bits from `crypto/rand`,
lower-hex. A `LedgerEvent` carries `{seq, kind, op_id, path|scope_url,
revision?, fingerprint?, authority_id, epoch, at, prev_hash, hash}` with
`hash = sha256(canonical(event without hash))`.

**Bounds:** `MaxExpandedRows` with `LIMIT (MaxExpandedRows − materialized) + 1`;
`Max` required on owning Types. **Statement budgets, per role method**
(pinned by the contract; a validation run has its own budget outside the
read's transaction; the lease `UPDATE` of a mutation counts as one; a CLI
read's one-time ephemeral regrant on its own expired lease is a separate
transaction outside the budget):

| Method | Statements | Composition |
| --- | --- | --- |
| `Bead` / `Beads` page | ≤ 7 (6 on a descriptor-cache hit) | Scope row; ledger head; lease; state version; row/page; descriptors; batched owned links |
| `Link`, `IncidentLinks`, `Links` page | ≤ 5 | Scope row; ledger head; lease; state version; the query (one `UNION` for incident) |
| `Descriptors`, `Descriptor` | ≤ 5 | Scope row; ledger head; lease; state version; catalog |
| `IdentityReader.Read` | 3 | Scope row; ledger head; lease |

**Errors:** `ErrNoScope`, `ErrScopeExists`, `ErrNotAuthority`,
`ErrStateRewound`, `ErrStateChanged`, `ErrSyncRequired`, `ErrUnpublished`,
`ErrURLReused`, `ErrRepresentationTooLarge`, `ErrNotServedYet`,
`GoneError{Path, State}` — declared in `beadserrors`, aliased here.

### B3. Bodies, the witness manager, and legs

Bodies take **`DBTX`**, the witness, and the process-local claim:

```go
func ReadBeadInTx(ctx, tx DBTX, w authority.Witness, claim graphcap.LeaseClaim, req graphops.BeadRequest) (graphops.BeadRecord, error)
```

**`internal/storage/authority` — the witness manager** (no SQL):

- **Installation key.** A random id created once in the directory the
  tree's `UserConfigYamlPath` resolver (`internal/config/user_config_path.go`)
  chooses — the documented `~/.config/bd` when possible, the native
  `os.UserConfigDir` location otherwise — file `installation-id`, or the
  path in `BEADS_INSTALLATION_ID_FILE`: `O_EXCL`, mode 0600, re-read after
  creation (the winner of a race is what every process uses), directory
  fsynced; an unresolvable directory fails closed. `InstallationKey =
  sha256(id ":" realpath(.beads))`. Never the hostname. Stated residuals: the
  id is **per OS user** (a service user's `bd serve` and an operator's
  `bd bdp promote` compute different keys — cross-user shared roots are
  unsupported, as the workspace gate already declares); an **ephemeral home**
  regenerates it on every start (`ErrNotAuthority` until `bd bdp promote`);
  a moved workspace needs a `bd bdp promote` (guidance: "moved or copied").
- **`Load`** is a plain read; a pending transition triggers **recovery**
  before any assertion (below).
- **`Advance`** takes the exclusive lock with a bounded poll (`internal/lockfile`
  has no timeout API; the `workspacegate` poll is the precedent; both
  `ErrLocked` and `ErrLockBusy` honored) and, **while holding it, asks the
  evidence provider** to decide a **descendant-aware compare-and-advance**:
  a candidate whose ledger head is an exact prefix of, and whose commit is
  an ancestor of, the current witness's is a successful no-op; newer fields
  are never replaced by older ones; a forked candidate is rejected — two
  writers can commit in one order and advance in the other, and opaque
  hashes alone cannot decide this; then `internal/atomicfile` (file fsync)
  and a directory fsync.
- **Transitions are multi-phase, recovered by evidence.** `Begin`
  (preflight: ensure the three ignore entries, refuse a git-tracked witness,
  take the lock — **held continuously through `Finalize`/`Abandon`**, and
  recovery acquires it first, so a live transition is never mistaken for
  crash residue) writes `{kind, op_id, phase: begun, pre_head,
  pre_ledger_head, pre_lease, remote_pre_head, expected_roots,
  config_intent}`; every ledger event carries an indexed, hash-covered
  `op_id` column and the commit message repeats it; the SQL-free manager
  asks the store through an **evidence provider** ("is `op_id` in the
  ledger?", "does the current commit descend from this one?", "is this
  head an exact prefix?"); after the scoped commit `SetPhase(local_committed, op_commit)`;
  after the push `published`; after `config.yaml` `config_written`; then
  `Finalize` writes the new witness and clears the record. **Recovery** on
  `Load` never trusts the phase alone: it first asks the ledger whether
  `op_id` is present (a crash between the commit and the phase write leaves
  `begun` with the operation committed — then it is `local_committed`); with
  no local operation → `Abandon`; with a local operation on a shared
  database when hazard R is not in force (no remote, **or any remote under
  A9**) → publication is satisfied: **execute any outstanding
  `config_intent`, record `config_written`, then `Finalize`**; with hazard
  R in force → classify the remote with the same ledger-plus-eight-table
  delta classifier as the push race, in this precedence: {contains this
  operation → `published`; still at `remote_pre_head` → resume the push;
  issue-plane-only movement → `ErrSyncRequired`, keep the commit; graph
  delta from foreign work → undo and `Abandon`}; `published` → write config
  if intended; `config_written` → `Finalize`. `LedgerApply`'s evidence is
  kind-specific — the manifest's head, range, and expected roots — because
  imported events keep their original `op_id`s (rewriting them would break
  their hashes). Retrying a transition never mints a second epoch. Ordinary
  published mutations (`Install`, P3 writes) carry an `op_id` in their event
  and an `unpublished` marker until pushed, recovered the same way.
- **Order of an ordinary mutation:** DB commit (and publish) before the
  witness advances; a crash between leaves the witness behind, which the
  next assertion tolerates. Residual (P3): an acknowledged write never
  witnessed before a restore.

| Leg | Files | Body |
| --- | --- | --- |
| server Dolt (CLI) | `internal/storage/dolt/beadgraph_*.go` | witness + claim per call; `withReadTx` / `withRetryTx`; scoped commit; `PublishGraphMutation` |
| embedded Dolt (CLI only without A9) | `internal/storage/embeddeddolt/beadgraph_*.go` | same body, `withConn` — *[under A9: the accessors answer `ErrNotAuthority`; the leg wires the refusal contract, not the role contracts]* |
| unit of work (**the serving leg**) | `internal/storage/domain/beadgraph.go`, `internal/storage/domain/db/beadgraph.go` (+ the version-control repository's new `MergeBase`/`ResetSoft`/`CheckoutTables`/`Revert`/`HashOfTables`), `internal/storage/uow/beadgraph_*.go` (`BeadGraphUseCase()`; `RunTxRead`; **`RunTxScopedResult(tables, msg)`** — new, since `doltServerTx.Commit` hardcodes `DOLT_COMMIT('-Am')`; `RunTxEphemeral` for renewal; `PublishGraphMutation` on the provider) | **same body** |

Every protected body begins with `assertAuthorityInTx(ctx, tx, w, claim,
mutating)`: Scope row identity; ledger head present (exact prefix) and
`MAX(seq) >= w.LedgerSeq`; on hazard S the lease row — a protected read `SELECT`s it inside the
read transaction and checks holder key, epoch, **and `expires_at >
NOW(6)`** (a holder/epoch match alone proves only that no takeover was
visible in the snapshot); **reads never write the lease**: on the serving
leg the watcher renews and a read whose remaining interval is shorter than
its deadline fails closed as `lost`; a CLI read whose own lease has expired
regrants **once, ephemerally, before opening a fresh read transaction**
(a read transaction opened before the regrant keeps its expired snapshot —
probed) and retries; the read's context deadline is derived from the
remaining lease interval (the shared `route()` deadline of sixty seconds
exceeds the default TTL, so BDP rows carry their own per-row deadline below
a third of the TTL); a mutation reads the `fence` cell and a
mutation `UPDATE graph_authority_lease SET heartbeat_at = ?, expires_at =
NOW(6) + ttl, fence = <fresh random, regenerated on every retry> WHERE id = 1
AND holder_installation_key = ? AND epoch = ? AND fence = <the value just
read>` and requires **exactly one affected row** — which also **self-regrants
an expired lease that still names this workspace** (a server restart longer
than the TTL needs no promotion); a lease held by *another* holder is taken
**only with `--steal`** — its expiry alone never grants a takeover. **Dolt merges concurrent
transactions cell by cell** (probed on 2.1.8): a takeover that rewrites
`holder`/`epoch` while a mutation rewrites `heartbeat_at` lets *both*
commit; only a same-cell-different-value write is a `1213` serialization
failure. Every lease write — grant, steal, expiry reclaim, renewal, and the
per-mutation fence — therefore rewrites `fence` with a fresh random value,
so any two of them conflict (probed on Dolt 2.1.8 for the versioned path,
the ephemeral plain-`COMMIT` path, read-then-mutate, and disjoint-column
takeovers; the tree's `TestRowLockForcesConflictOnDisjointCellWrites` is the
same trick for `row_lock`). `withRetryTx`/`RunTxResult`/`RunTxEphemeral`
replay the loser, which **re-evaluates its preconditions** with a new random
fence: a still-authorized writer (two mutations, or a renewal and a
mutation) succeeds serially; a superseded, stolen, or expired-and-retaken
claim matches zero rows → refusal. On the scoped-commit path the `1213`
surfaces from `CALL DOLT_COMMIT` (the trailing `COMMIT` then succeeds with
nothing persisted), so `RunTxScopedResult` keeps that call inside the
retried closure. Bound, stated: a fenced transaction that spans a renewal loses
whenever the renewal commits first, so every fenced transaction **in the
shared-gate context** carries a deadline below a third of the TTL
(cancelled and retried past it; the replay budget allows more than one
attempt), in-process renewal is serialized against the process's own
mutations with jittered cadence, and `types install` is exclusive (the
server stopped; the sole writer sets `expires_at` itself and runs
unbounded). The `leases` precedent's
`INSERT … ON DUPLICATE KEY UPDATE … IF(...)` is a statement-time guard and
does not fence at commit; this design does not rely on it. The ledger
counter is the same shape: `UPDATE graph_ledger_seq SET next_seq = next_seq
+ 1, alloc_nonce = <random> WHERE id = 0` — a bare `+ 1` from the same
value **converges** (both allocators commit, one increment lands; probed),
the random cell makes it a conflict, and the events PK is the second guard
(it too converges on byte-identical rows, which the nonce prevents); the **graph-state version** from the provider's `StateVersioner`
(Dolt: `DOLT_HASHOF_TABLE('<validated name>')` for each of the eight
replicated graph tables in the fixed B4 order, the eight hashes hashed
together with sha256 — the function exists in 2.1.8 and takes exactly one
argument; `DOLT_HASHOF_DB()` is not used because every ephemeral write,
including this lease's own renewal, moves it) equal to
`w.StateVersion`, else `ErrStateChanged` **without validating in the held
transaction**; the accessor then validates under **one singleflight
coordinator per provider instance** (per-request `timedProvider` roles
share it) in its own transaction — ancestry `DOLT_MERGE_BASE(w.StateCommit,
HEAD)`, provenance for foreign updates — advances the witness, and retries
once. Descriptor caches are keyed by the descriptors table's hash.
Providers without `StateVersioner` fail closed. Exempt from the head check,
with their own preconditions: `Mint`, `Promote`, `Rotate`, `LedgerApply`,
`IdentityReader`, the witness-file operations.

### B4. Schema (migrations; frozen once merged)

Rules the tree enforces: migrations are **frozen once merged** — hygiene
check C forbids editing a shipped file (a git-diff check), and the runtime
`content_skew.go` compares `schema_migrations.content_hash` across clones;
**no `NOW()`/`UUID()`/`RAND()`** in migration SQL (check B) — timestamps and
ids come from Go; real-Dolt tests for anything a `sqlmock` echo cannot
exercise; DDL is not transactional across statements, so each `CREATE` is
guarded and resumable. **Eight replicated tables in five files** —
`NNNN_beadgraph_scope.up.sql` (scope, history), `NNNN_beadgraph_types.up.sql`,
`NNNN_beadgraph_beads.up.sql`, `NNNN_beadgraph_links.up.sql`,
`NNNN_beadgraph_ledger.up.sql` (events, counter, allocations) — plus the
lease's three parts: its name in `doltIgnorePatterns`, a main-series
`NNNN_beadgraph_authority_lease.up.sql` that creates it for existing
workspaces (the 0055 `__temp__` + conditional `RENAME` shape), and
`ignored/NNNN_beadgraph_authority_lease.up.sql` for fresh clones (check D);
the lease joins `ignoredSource.sentinelTables`. It is reached exactly as
`issueops/lease.go` reaches `leases`: on the default branch's working set
(branch-qualified sessions do not see it — which is why publication stays
on the default branch, Part D.5).

**Collation.** Dolt's default collation is already binary
(`utf8mb4_0900_bin` — probed), and no migration in the tree declares one.
Every identifier column below still carries **`CHARACTER SET utf8mb4
COLLATE utf8mb4_bin`** (written `BIN`) as the defense for providers whose
default is case-insensitive, with a contract case.

**Identity is Scope-relative.** Rows store the canonical Scope-relative
`path`; the absolute URL is `scope_url + path`, computed at the boundary,
so a URL rotation rewrites no rows.

**JSON is bytes.** `properties` and `descriptor` are canonical JSON bytes in
`LONGBLOB`, never the engine `JSON` type (the tree measured `1.0`→`1`,
integers past 2^53 rounded, `1e300` expanded in `internal/storage/issueops/metadata_cas.go` and the public
`issueops/metadatacas.go`; `-0.0`→`0` per the role guide). Size limit 1 MiB per value.

**Provenance on every mutable row.** `last_authority_id` / `last_epoch` are
stamped by every mutation on descriptors, beads, links, and allocations.

**The graph-state version** is `DOLT_HASHOF_TABLE('<name>')` for each of
the eight replicated tables below, in this order, the eight hashes hashed
together with sha256; the lease is ephemeral and excluded. The descriptors
table's hash within it keys the descriptor cache.

| Table | Columns (type; nullability) | Keys / constraints |
| --- | --- | --- |
| `graph_scope` | `id TINYINT NOT NULL` (always 1), `scope_url VARCHAR(2048) BIN NOT NULL`, `authority_id CHAR(32) NOT NULL`, `epoch BIGINT UNSIGNED NOT NULL`, `minted_at DATETIME(6) NOT NULL` | `PRIMARY KEY (id)`, `CHECK (id = 1)` — singleton |
| `graph_scope_history` | `scope_url VARCHAR(2048) BIN NOT NULL`, `refused_seq BIGINT UNSIGNED NOT NULL`, `refused_at DATETIME(6) NOT NULL`, `reason VARCHAR(64) NOT NULL` | `PRIMARY KEY (scope_url)`; derived from `refuse_url` events |
| `graph_type_descriptors` | `url VARCHAR(2048) BIN NOT NULL`, `descriptor LONGBLOB NOT NULL`, `fingerprint CHAR(64) NOT NULL`, `installed_seq BIGINT UNSIGNED NOT NULL`, `installed_at DATETIME(6) NOT NULL`, `last_authority_id CHAR(32) NOT NULL`, `last_epoch BIGINT UNSIGNED NOT NULL` | `PRIMARY KEY (url)`; `UNIQUE (fingerprint)` |
| `graph_beads` | `path VARCHAR(1024) BIN NOT NULL`, `type_url VARCHAR(2048) BIN NOT NULL`, `revision CHAR(32) NOT NULL`, `attribution_principal VARCHAR(512) NULL`, `attribution_status ENUM('claimed','unknown') NULL`, `properties LONGBLOB NOT NULL`, `last_authority_id CHAR(32) NOT NULL`, `last_epoch BIGINT UNSIGNED NOT NULL`, `created_at DATETIME(6) NOT NULL`, `updated_at DATETIME(6) NOT NULL` | `PRIMARY KEY (path)`; `INDEX (type_url, path)`; `FOREIGN KEY (type_url) REFERENCES graph_type_descriptors(url)`; attribution columns both NULL or both set |
| `graph_links` | `path VARCHAR(1024) BIN NOT NULL`, `type_url … BIN NOT NULL`, `revision CHAR(32) NOT NULL`, `source_path VARCHAR(1024) BIN NOT NULL`, `source_pin CHAR(32) NULL`, `target_kind ENUM('in','ext') NOT NULL`, `target_path VARCHAR(1024) BIN NULL`, `target_url VARCHAR(2048) BIN NULL`, `target_pin CHAR(32) NULL`, `attribution_*`, `properties LONGBLOB NOT NULL`, `last_authority_id`, `last_epoch`, timestamps | `PRIMARY KEY (path)`; `INDEX (source_path, type_url, path)`, `INDEX (target_path, path)`; `FOREIGN KEY (source_path) REFERENCES graph_beads(path)`; `CHECK` exactly one of `target_path`/`target_url` per `target_kind`; **no** uniqueness on (type, source, target) |
| `graph_ledger_seq` | `id TINYINT NOT NULL` (always 0), `next_seq BIGINT UNSIGNED NOT NULL` (`next` is reserved in Dolt's parser), `alloc_nonce CHAR(32) NOT NULL` | `PRIMARY KEY (id)` — **the single-row sequence counter**: `UPDATE … SET next_seq = next_seq + 1, alloc_nonce = <random> WHERE id = 0` inside the mutation's transaction; the random cell is what makes two allocators a `1213` conflict (a bare increment converges under Dolt's cell-wise merge — probed); a rolled-back transaction burns no seq; seeded by `Mint`; set by `LedgerApply` |
| `graph_ledger_events` | `seq BIGINT UNSIGNED NOT NULL`, `op_id CHAR(32) NOT NULL` (indexed; covered by the hash), `kind ENUM('mint','install','update','promote','rotate','allocate','tombstone','refuse_url') NOT NULL`, `path VARCHAR(1024) BIN NULL`, `scope_url VARCHAR(2048) BIN NULL`, `resource_kind ENUM('bead','link') NULL`, `revision CHAR(32) NULL`, `state ENUM('pruned','erased') NULL`, `fingerprint CHAR(64) NULL`, `authority_id CHAR(32) NOT NULL`, `epoch BIGINT UNSIGNED NOT NULL`, `at DATETIME(6) NOT NULL`, `prev_hash CHAR(64) NOT NULL`, `hash CHAR(64) NOT NULL` | `PRIMARY KEY (seq)` — **append-only, hash-chained**; `UNIQUE (hash)`; `INDEX (path, seq)`; `INDEX (op_id)` (non-unique: one operation may append several events); `CHECK` per kind. Every mutation is an event |
| `graph_allocations` | `path VARCHAR(1024) BIN NOT NULL`, `resource_kind ENUM('bead','link') NOT NULL`, `birth_seq BIGINT UNSIGNED NOT NULL`, `birth_authority_id CHAR(32) NOT NULL`, `birth_epoch BIGINT UNSIGNED NOT NULL`, `state ENUM('live','reserved','pruned','erased') NOT NULL`, `tombstone_seq BIGINT UNSIGNED NULL`, `last_authority_id CHAR(32) NOT NULL`, `last_epoch BIGINT UNSIGNED NOT NULL` | `PRIMARY KEY (path)` — the O(1)/O(log n) ID test (ruling 3); **derived state**; `reserved` = ledger-applied with no row (A4) |
| `graph_authority_lease` (**dolt-ignored**, never replicates) | `id TINYINT NOT NULL`, `scope_url VARCHAR(2048) BIN NOT NULL`, `authority_id CHAR(32) NOT NULL` (bound to the Scope row), `holder_installation_key CHAR(64) NOT NULL` (the workspace), `renewer CHAR(32) NOT NULL` (informational: the process that last renewed), `epoch BIGINT UNSIGNED NOT NULL`, `granted_at DATETIME(6) NOT NULL`, `expires_at DATETIME(6) NOT NULL`, `heartbeat_at DATETIME(6) NOT NULL`, **`fence CHAR(32) NOT NULL`** | `PRIMARY KEY (id)`, `CHECK (id = 1)`; the hazard-S fence (A7): **every write rewrites `fence`** with a fresh random value and predicates on the value it read — the one cell all lease writers collide on; renewals through the ephemeral commit form |

`updated_at` is protocol-irrelevant bookkeeping. Bead `type_url` and Link
`source_path`/`target_*` are immutable after insert.

**The witness file: `.beads/graph-authority.local.json`.**
`{installation_key, scope_url, authority_id, epoch, ledger_seq,
ledger_hash, state_version, state_commit, unverified, granted_at,
pending?}`. Written only by the manager (B3). What each operation does to
it: `git clone` / `dolt clone` — absent; pull — untouched; `bd backup
restore` / `DOLT_BACKUP` restore to an **older** state — present, and the
ledger head it names is no longer in the store → `ErrStateRewound` (a
restore to the matching head is not a rewind; `bd backup restore`'s
`MarkUnverified` is the belt for that case and is tested separately); directory copy to another path
— installation key mismatch → `ErrNotAuthority`; a copy to the same path on
another machine — a different installation id → `ErrNotAuthority`; a
**whole-installation copy** (id and path both preserved) — the A5
residual, undetectable without an arbiter.

**Cross-repo coupling (bts).** `DoltTeamServer` workspaces refuse to open
when `current < latest` with **no `BD_IGNORE_SCHEMA_SKEW` hatch** — a
**numeric-version** comparison only. The coupling is a cross-repository
**release-parity gate**: bts must ship byte-identical copies of the six
main-series files and the ignored-series twin (the
`schema_migrations.content_hash` values `migration_content_hashes.go`
reads are what a bts-side parity test compares). The migration PR is
sequenced with bts; the remote-migrate gate (#4259) forces
migrate-vs-adopt on every remote-backed workspace at upgrade.

### B5. Decorators, censuses, and every embedding surface

- `internal/storage/hook_beadgraph_*.go` (six files): declared, recurse
  **unwrapped**; `storage.RoleFiresHooks` is a type switch over hook
  wrappers, so an unwrapped role needs no entry; a test asserts each graph
  role answers `false`. Added to `role_accessor_decorator_test.go`'s table.
- `internal/telemetry/beadgraph_*.go`: every method spanned; the
  **telemetry census** gains the classification.
- **Three censuses**, each must learn `graphops`: the storage reflection
  census, the telemetry census, and the conformance package's
  **source-parsed** `facadePackages` map (`role_coverage_scan_test.go`) —
  without the third, `TestRoleFacadeCensusAgreesWithReflection`
  (`role_coverage_gate_test.go`) fails.
- `internal/storage/uow/notifying.go` (explicit accessors, parity test);
  `internal/httpapi/claim.go`'s `timedProvider` (which builds roles over the wrapper today and gains a `beadsDir` getter);
  `cmd/bd/serve.go`'s `serveRoleSource` and its stubs;
  every surface that embeds the store or a provider, enumerated by
  `grep -l 'func (.*) Memories()'` at implementation. Because a required
  method is promoted silently through every interface-embedding wrapper,
  the censuses are what catch an undeclared one; the compiler catches direct
  implementers — `internal/jira/tracker_test.go`'s `configStore` is one (a
  flat implementer, not an embedder) and fails to compile until it gains the
  stubs.

### B6. `backend/` public surface and depguard

- **No aliases** (amendment A4): `graphops` is public and imported
  directly, like `issueops`. `TestPublicSurfaceComplete` stays green
  *because* no `internal/` type is reachable from the new accessors — a
  test asserts that.
- `backend/backend.go`'s doc-comment sketch of a minimal external backend
  gains the six accessors as `ErrUnsupported` stubs (option A; there is no
  example package under `backend/` — the stub contract is
  `conformance.RunUnsupportedContract`). The **CHANGELOG entry** follows the
  joint `ReadyClaimer`/`BatchCloser` entry's wording ("must add both methods
  to compile").
- `.golangci.yml` gains a **new, stricter** rule (cmd/bd imports the
  `issueops` tx-body package directly today, so this is not the existing
  convention): `internal/storage/graphops` is importable only by
  `internal/storage/{dolt,embeddeddolt,domain/db,uow,graphcap}` and its own
  tests. A
  mutation test **deletes the deny entry and asserts that a fixture
  violating it then passes lint** — which proves the entry is what fails
  the violation.

### B7. Conformance

- Families: `beadgraph_reader_contract.go`, `beadgraph_types_contract.go`
  (reader + installer), `beadgraph_identity_contract.go` (reader,
  bootstrapper, admin), each citing the leaf doc by line.
- `RoleContractBundle` gains six factory fields **and** their rows in
  `role_bundle_cases.go`; `BeadGraphFixture` carries the seed hook, a
  `Witness` hook (a temp workspace directory and installation id standing
  in for `.beads/` and the user config dir), and a `Remote` hook (a temp
  Dolt remote for hazard-R cases *[deferred under A9]*).
- Wirings on all three legs (under A9 the embedded leg wires the refusal
  contract only); the leg registry
  (`internal/storage/contract_leg_registry_test.go`) and
  `TestEveryLegWiresEveryRoleContract` see them; both coverage gates apply.
- Non-capable stores answer `*storage.ErrUnsupported{Op: "<accessor
  name>"}` — the six strings pinned — proven per accessor.
- Cases the councils asked for by name: a clone produced by push/pull
  refuses; a **`DOLT_BACKUP` restore of an authority** refuses
  (`ErrStateRewound`); a **copied witness** in another directory refuses;
  an **expired claim held by another holder** cannot mutate and is not taken over by expiry alone; an expired claim naming this workspace self-regrants; a **stale fence value** cannot mutate; a protected read on an expired, un-regranted lease refuses;
  a **lease takeover between a mutation's SELECT and its commit** refuses
  **even when the takeover rewrites disjoint columns** (the fence cell);
  two allocators with **byte-identical event payloads** conflict on the
  counter's nonce;
  two clones minting before either pulls (hazard R: the second push is a
  race and is undone); concurrent mint on one database (one wins); a
  promotion race (one CAS wins; the loser's push races, is undone by soft
  reset + checkout, unrelated dirty tables untouched); a **graph delta
  under an unchanged `(authority_id, epoch)`** fails closed on a race;
  **issue-plane-only divergence** keeps the commit and answers
  `ErrSyncRequired`; a **network failure on push** keeps the commit as
  unpublished and retries; **undo when HEAD moved** reverts and preserves
  later commits; **each transition phase** recovers on the next load
  (resume and undo, both outcomes); an `Install` is published on hazard R;
  a hazard-R CLI read with a stale observation fetches and fails closed
  past the grace; heartbeat detects a changed `(authority_id, epoch)`;
  rotation refuses the old URL and updates `config.yaml`; `bd config set
  bdp.scope_url` is refused with a witness present, and via
  `set-many`/`unset`; `bd bdp serve`'s staged startup releases the
  exclusive gates before serving; a registered backend serves no rows; the
  watcher disables rows atomically and joins before shutdown; a heartbeat
  does **not** change the graph-state version; case-differing paths
  distinct and code-unit ordered; ownedLinks completeness incl. **empty
  groups** and the bound under `LIMIT remaining+1`; keyset continuation
  inside one transaction; gone-family incl. `reserved`; a promote in
  another process is honored by the next read; descriptor read on a
  non-authority clone refuses; `bd init` re-run on any clone succeeds and
  installs nothing; `Mint` installs the built-in catalog with `install`
  events; ledger snapshot/apply round trip, gap refusal, foreign-lineage
  refusal, recovery-predicate regrant, counter set; the installer refuses
  an owning declaration without `Max`; statement budgets per method;
  `ErrStateChanged` triggers one validation under concurrent reads and the
  reads retry once.
- Differential-gate rows: every legacy form of `bd link`, `bd graph`,
  `bd graph check`, `bd restore`, `bd promote` parses and behaves as before;
  `bd init` gate output on a non-capable backend is byte-identical;
  `bd serve` without a Scope URL is byte-identical.

### B8. `httpapi` integration and the pinned wire (amendments A2, A7)

- **`httpapi.Config.Graph *GraphConfig`** — `{Reader graphops.Reader; Types
  graphops.DescriptorReader; ScopeURL string; Fence FenceSource}` on the
  store arm; on the provider arm `Reader`/`Types` are nil and the provider's
  own accessors answer per request through `timedProvider`. `Fence` carries
  the `held → renewing → lost` state machine of A3.6 and its shutdown join.
  No admin or installer field (a test asserts it); `checkDatabaseSource`'s
  exactly-one-source rule extends to the graph fields. Registered backends
  and embedded workspaces never reach `Graph != nil` in v0.
- **`bdpRouteTable`** (`internal/httpapi/bdp_routes.go`) — **P2**: rows in
  the same `route` shape, registered by `Server.handler()` **only when
  `cfg.Graph != nil`** — `handler()` reads no `Config` today, so this is a
  first, and `TestSpecRouteParity`'s `(&Server{}).handler()` keeps excluding
  the rows — each wrapped by the same `s.route(rt)`. First rows: discovery,
  `types/`, one Bead, one Link; **collection rows wait for the cursor ADR**.
  No capability token in v0; a sibling parity test compares `bdpRouteTable`
  against the pinned schema's path grammar.
- **Posture parity test:** one refusal matrix drives a legacy row and a BDP
  row and asserts identical status and log shape.
- **Handler = serializer**; typed graph errors → BDP Problem records
  (`bdp_problem.go`), here and only here.
- **Wire — P0, not yet in the tree:** `internal/httpapi/bdpwire/schema/bdp-v0.schema.json`
  **will be vendored** with a `PROVENANCE` file (upstream repo, commit — the
  plan's §0 pin — and sha256); `make bdp-gen` runs a **pinned** JSON-Schema→Go
  generator (fallback recorded at P0: hand-written DTOs validated against
  the schema); `make bdp-check` regenerates and diffs, and
  `scripts/ci/pr-policy.sh` runs it beside `make api-check`.

## Part C — What does not change

`storage.Storage`'s existing 28 accessors and every `issueops` role; the
journal's frozen vocabulary; `openapi.v0.yaml` and `TestSpecRouteParity`;
`bd serve` on a workspace with **no** `bdp.scope_url` (byte-identical) and
on any workspace that does not hold the authority (legacy surface up, rows
absent); every legacy CLI verb (differential gate rows); JSONL export
shapes; `metadata.json`'s schema; `bd init` gate output on non-capable
backends; a registered backend's serving behavior (rows absent).

## Part C2 — What changes that an earlier draft claimed did not

- **Merge, pull, and sync.** Every `DOLT_PULL`/`DOLT_MERGE` route (the
  `versioncontrolops` exports — seven in `mergesettle.go`, plus
  `fastforward.go` and `automerge.go`; `doltCLIPull`, `Pull`, `PullRemote`,
  `pullTransport`/`pullWithAutoResolve` in `internal/storage/dolt/store.go`;
  the UOW leg's `doltVersionControlSQLRepository`; embedded federation sync;
  the remote-migrate gate's fast-forward `DOLT_MERGE`) can change graph
  state outside the roles, so the **state-change validator** runs on every
  observed graph-state-version change (B3) and refuses a foreign-authority
  or invalid delta; a superseded clone that pulls resets to the remote. The
  replication/merge ADR specifies its rules before the migrations land.
- **Federation.** Graph tables ride filtered pushes **unfiltered, by
  decision, in v0**; the lease table never replicates.
- **`bd sql`, raw SQL, and force-push.** Out of contract for graph tables;
  the enforcement-boundary ruling decides the rest before P3.
- **`bd backup restore`** calls `Admin.MarkUnverified` after
  `RestoreDatabase`, before its commit (no-op without a witness).
- **Root store policy** gains `commandPolicy` (Part A), and
  `commandNeedsExclusiveGate` in `cmd/bd/workspace_gate.go` — today true only
  for `backup restore` — learns `bd bdp types install`, `restore`, and
  `ledger apply`.
- **`bd init`** gains the three `.beads/.gitignore` entries and installs no
  descriptors (the catalog moves to `Mint`).
- **`bd config set`/`set-many`/`unset`** refuse `bdp.scope_url` once a
  witness is held.
- **The UOW leg** gains `RunTxScopedResult` and `PublishGraphMutation`; the
  version-control repository gains `MergeBase`, `ResetSoft`,
  `CheckoutTables`, `Revert`, `HashOfTables`.
- **The doctor** gains an error-class finding for a tracked witness; the
  `domain/fs` `WriteBeadsGitignore` writer shares the template and gains the
  entries too (hygiene check D already warns and skips without a base ref —
  no change there).

## Part D — Open implementation questions (not rulings)

1. Proposed numbers (`MaxExpandedRows`, `MaxCatalog`, page bounds, value
   limit, lease TTL, heartbeat, grace) fixed at P1 with rationale.
2. Whether the P1 fixture writer becomes the internal half of `Writer`.
3. Generator choice for `bdpwire` — recorded at P0.
4. Whether `bd bdp serve` remains after W2 (default: yes).
5. Whether hazard-R publication should use an isolated branch instead of
   soft-reset/checkout/revert. The constraint that decides it survives A9:
   the ephemeral lease table lives in the default branch's working set and
   branch-qualified sessions do not see it, so every fenced transaction runs
   on the default branch.

## Part E — Proposed ruling amendments this spec assumes (pending)

A1 store-owned witness with the full lease predicate; A2 BDP rows inside
`httpapi`, `bd bdp serve` the only minting path (staged startup), `bd serve`
never refusing, v0 serving from SQL-server workspaces only; A3 `bd bdp …`
with an authoritative CommandPath policy; A4 public `graphops`,
`BeadGraph*`; A5 installation-keyed witness with multi-phase transitions,
hash-chained ledger, ledger lane = anti-reuse history, stated residuals;
A6 tracked `bdp.scope_url` (refused once held), per-workspace keys in
`config.local.yaml`, `BDP_SCOPE_URL` first; A7 fences by hazard with one
publication primitive for every replicated mutation; A8 two options with
the corrected method-set reasoning; **A9 (recommended): v0 authority
requires a shared database — hazard R deferred to the write-profile ADR;
in-place promotion is self-regrant or an operator `--steal`, never a
foreign holder's expiry; `--rotate-url` is the bootstrap for clones, copies,
and restores; physical database copies are the stated operator-managed
hazard; the embedded and registered-store arms refuse every local authority
operation, and the registered-backend publication seam is the deferred
ADR's to define.**
Plus the enforcement boundary and the replication/merge ADR. Full text:
architecture §2b.
