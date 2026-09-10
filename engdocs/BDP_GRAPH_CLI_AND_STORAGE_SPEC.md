# BDP graph store — CLI and storage-interface changes, in detail

**Status:** Draft v16 (W-arch) — A1–A9 and plan rulings 13–14 ruled 2026-09-07; A10 ruled 2026-09-08; P0 current-wire completion open — feat/bead-graph
**Date:** 2026-09-10 (v15: 2026-09-09; v14: 2026-09-02)
**Companions:** `BDP_BEAD_GRAPH_PLAN.md` (rulings), `BDP_GRAPH_ARCHITECTURE.md`
(shape; its §2b lists A1–A9 and plan rulings 13–14, **ruled 2026-09-07**,
and the A10 solo-topology amendment **ruled 2026-09-08**; every
hazard-R paragraph is marked *[deferred under A9]*). This document is the
*diff*: every command, flag, config key, interface member, package,
migration, and gate the graph work adds or touches — and what it does not
touch (Part C) and what it changes that an earlier draft claimed it did not
(Part C2). Phase markers follow the plan's §7: **P0** contracts and wire,
**P1** storage, **P2** serving, **P3** writes.

Revision v15: 2026-09-09 current-source alignment and council corrections;
phase/owner gates and pinned-source clarity added, historical rulings preserved.
Revision v16: 2026-09-10 formal-review correction aligns all embedded-leg
summaries with A10 and qualifies the registry claim; plan §0a carries the
current dependency refresh and outstanding reviewer/owner gate.

<a id="alignment-addendum-2026-09-09"></a>

## Alignment addendum (2026-09-09, refreshed 2026-09-10)

[Plan §0a](BDP_BEAD_GRAPH_PLAN.md#0a-current-bdp-and-versioned-beads-alignment-2026-09-09)
records the historical pins and the 2026-09-10 refresh for both BDP #19/#20
and Jim #6147/#6304/#6358, approved History
direction and completion gates. The historical P0 wire pin remains explicit;
it is not a current-write or History capability claim. Three storage/adapter
boundaries follow from that reconciliation:

1. P3 delete results carry the shared `deletedIdentity` schema in the
   `deleted` result member, including the removed Resource's final live
   revision. Owned-Link results also report `source` (the source Bead's
   absolute canonical URL) and `sourceRevision` (that Bead's resulting
   revision), on creation, update and deletion alike. Deletion never mints a
   deleted Resource version.
2. The allocation/authority ledger below is neither Jim's retained
   `issue_versions` store nor the TX projected erasure ledger. Preserve the
   separate epochs and recovery duties. A future History store needs complete
   retained records and applicable copy cleanup; adding History names to this
   allocation ledger does not implement either requirement. The P3 ADR
   inventories selected-profile storage/interface deltas and affected migration,
   state-version, fence, inspection and census gates under plan §0a. The eight
   replicated tables below are the initial P1 scope; no new table or interface
   widening is prescribed, and P1 need not wait for the full P3 design. Any
   necessary later addition preserves frozen migrations and A8 source-break
   disclosure.
3. The future approved context envelope is distinct from carried attribution
   and properties. Its commit-time, operation fan-out, legacy absence,
   postimage/Event/row placement and erasure constraints need the upstream
   closed materialization and an explicit provider mapping. Jim's existing
   columns, empty-actor status and local ordinal do not supply that mapping.

P0 must deliberately re-pin reviewed wire inputs and port the narrow erased-
pointer rejection before current-wire claims, preserving other RFC 9457
extensions. P1 rechecks merged main and the migration advisory before claiming
actual slots; 0067 is now merged and 0068 is reserved by Jim's still-open
Phase 2. [Plan §7's owner gates](BDP_BEAD_GRAPH_PLAN.md#current-wire-and-profile-adoption-gates-2026-09-09)
assign these exits, shared negotiation/conditional contracts and their P2
public-boundary proof. Before P3, record the reviewed selected write-profile
pin under plan §0. Field spellings above quote the §0a upstream pins and are
re-verified at that write pin; this addendum introduces no new local wire
field, migration or implementation.

## Part A — CLI

Graph behavior is selected by the **root flag `--graph-mode link`**
(default `dependency`; `BD_GRAPH_MODE`; config key `graph-mode`), never
by a verb prefix (amendment A3, ruled 2026-09-07). The mode names the
graph by the kind of edge it carries: the dependency graph is issues joined
by dependencies, the link graph is graph beads joined by typed Links.
**Every verb creates, reads, or serves the selected graph or refuses**;
`bd link a b` creates a dependency, `bd --graph-mode link link a b --type
<url>` creates a Link; verbs with no link-graph meaning (`dep`, `ready`,
`prime`, …) refuse under the flag rather than silently running on the
dependency graph. Without the flag every verb is byte-identical to today;
the differential gate gains one row per legacy verb form (Part B7). A
workspace may default to link mode through the config key, and the key's
help says what that does to `bd link`.

**Root store policy is keyed by mode and command path, and it is
authoritative.** The root command classifies commands by *leaf name* at
several sites — `effectiveRootStorePolicy(cmd.Name(), …)`,
`runsPostCommandMaintenance(cmd.Name(), …)`, `isReadOnlyCommand`
(`readOnlyCommands`, which `context_cmd.go` mutates at init),
`shouldAutoPruneEventsJournal`, the `cmd.Name() != "import" && != "setup"`
branches, `workspace_gate.go`, `main_errors.go`. One
`commandPolicy(mode, *cobra.Command)` keyed by the mode and `CommandPath()`
is consulted first at each site (a new seam — the only `CommandPath()`-keyed
map today is `help_supplements.go`), with an exhaustive Cobra-tree test
**paired with a source scan** that fails on any `cmd.Name()` consumer not
routed through the policy. The flag is a root persistent flag consulted
before any store opens, like `--readonly`.

| Verb under `--graph-mode link` | Local store | Gate | Maintenance |
| --- | --- | --- | --- |
| `show <path>`, `list`, `types [get]`, `status` | read-only when `link-graph.route: local`; **skipped entirely** when `link-graph.route: server` | shared | no |
| `serve` | serve's classification (A3); **staged** — the one normative sequence: bypass the generic pre-run store gate (the skip-store seam); acquire shared; open a temporary source, read the Scope row, close it; **release shared**; if there is no Scope row: acquire exclusive, reopen, re-check, mint, close, release exclusive; acquire shared; open the serving source; re-evaluate the identity table under it; serve | shared → release → (exclusive → release, only for a mint) → shared; never an upgrade | no |
| `client` | none (writes `config.local.yaml`) | none | no |
| `ledger snapshot` | read-only, local | shared | no |
| `promote` | writable, always local | **shared** — it relies on the lease (the workspace is the holder), so it runs beside a live `bd serve` | no |
| `types install`, `restore`, `ledger apply` | writable, always local | **exclusive** (`internal/workspacegate`, the `bd backup restore` precedent) — the server must be stopped; `commandNeedsExclusiveGate` learns them | no |
| (P3) `create`, `update`, `delete`, `link` | writable | shared | no |

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

### A2. Client wiring — `bd init --bdp-server <url>` and `bd --graph-mode link client` (ruling 12; amendment A6, ruled 2026-09-07)

One more `bd init` target, rerouting *above* the storage abstraction: the
link-mode read verbs become a BDP client of the designated server.

**Two files, by what the key is.** `config.yaml` is git-tracked by default;
per-workspace keys go to **`config.local.yaml`** (merged by viper over
`config.yaml` for machine-specific settings; merged as the sibling of
the project-level `config.yaml` (never a user-level one); the `.beads`/basename requirement is
`yaml_config.go`'s `projectConfigPathFromLoadedState`, so the writer ensures
a project `config.yaml` exists and gets its own path plumbing).

| Key | File | Values | Notes |
| --- | --- | --- | --- |
| `bdp.scope_url` | `config.yaml` (tracked; yaml-only) | absolute URL | a **project** fact (ruling 7a). Settable while this workspace holds **no witness**; once it does, `bd config set` / `set-many` / `unset` refuse it (one guard in the shared `rejectProtectedConfigKey` path, which `set` and `set-many` call today and `unset` gains an explicit call to — a DB-free file check; a hand edit of the tracked file bypasses it, and the A3 "configured ≠ persisted" row is the real guard) and the URL changes only through `bd --graph-mode link promote --rotate-url` / `bd --graph-mode link restore`, which write it in their `config_written` phase and are refused while `BDP_SCOPE_URL` overrides it |
| `bdp.authority_heartbeat` | `config.yaml` | duration (default `30s`) | hazard R *[deferred under A9]* |
| `bdp.authority_heartbeat_grace` | `config.yaml` | count (default `3`) | hazard R *[deferred under A9]* |
| `bdp.lease_ttl` | `config.yaml` | duration (default `30s`, renewed every third) | hazard S |
| `link-graph.route` | **`config.local.yaml`** | `local` (default) \| `server` | per-workspace; **not settable from env** (`blockedEnvVars`) |
| `bdp.server` | `config.local.yaml` | absolute URL | `https` required unless loopback or `bdp.insecure_http: true` |
| `bdp.insecure_http` | `config.local.yaml` | bool | the named waiver |

**Writers.** `bd init --bdp-server <url>` and **`bd --graph-mode link client server
--server <url> [--insecure-http]`** / **`bd --graph-mode link client local`** write the
per-workspace keys through one shared writer. Generic `bd config set`
accepts the `config.yaml` keys (yaml-only routing) and refuses the
per-workspace keys with "use `bd --graph-mode link client`". No token key in config
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
`bd --graph-mode link client` — writes `config.local.yaml`; `bd config set bdp.scope_url`
— no witness: writes `config.yaml`; witness held: refused; every link-mode
verb — env (where permitted) > `config.local.yaml` > `config.yaml`;
`bd --graph-mode link status` — prints route, target, token source (never the token),
`insecure_http`, and the identity state row.

**In `link-graph.route: server` mode** the verbs' `openBeadGraph*()` accessors return
a BDP-client realization of the same `graphops` roles (A5); `bd --graph-mode link serve`
there refuses unless `--serve-local-store`. Issue verbs are unaffected.

### A3. `bd --graph-mode link serve` — serving a Scope (rulings 7b, 9, 12; amendments A1, A2, A7, A9 — ruled 2026-09-07; A10 — ruled 2026-09-08)

**v0 serves BDP only from SQL-server workspaces.** `bd serve` refuses
embedded Dolt permanently; every Dolt-server topology serves from the
unit-of-work provider — the serving leg, where every fence lives. A
**registered backend** is served from its store arm (`serveDatabaseSource`
routes it there; the backend may itself be embedded) and has no fence to
offer, so its BDP rows are **absent in v0** (`bd --graph-mode link serve` exit 2, typed);
the seam it would declare later is the deferred ADR's to define (an
out-of-tree module cannot import `internal/storage/graphcap`). Embedded
workspaces under A9 as amended by A10 are solo authorities when they have
no remote and no server, with local graph reads and verbs. Embedded client
hosts retain the local graph `ErrNotAuthority` refusal contract. Neither
embedded topology serves BDP over HTTP in v0.

`bd --graph-mode link serve` is a **thin command over the existing `internal/httpapi`
server**; two policies differ from `bd serve`: it **requires a Scope this
workspace holds** (exit 2 otherwise) and **it is the only serving command that
mints**, through a **staged startup** — the gate rules forbid a
shared→exclusive upgrade, so the sequence in Part A's table is the only
one: shared gate → temporary source reads the Scope row → close →
**release** → (no Scope row: exclusive gate → reopen and re-check → mint
(A4) → close → release) → shared gate → serving source → serve. `bd serve` mounts the rows only when
it holds an already-minted Scope, **converts every graph failure into "rows
absent + notice"**, and is byte-identical with no URL. Both inherit
`errServeReadonly` wholesale.

```text
bd --graph-mode link serve [--addr IP:PORT] [--allow-non-loopback] [--auth-token-file PATH]
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
3. **Capability probe, all-or-nothing:** `bd --graph-mode link serve` exit 2 on
   `ErrUnsupported`, abort on any other error; `bd serve` → rows absent +
   notice in both cases.
4. **Identity** — split by command; `bd serve` **never refuses and never
   mints**:

   | Persisted Scope | Configured URL | Witness | `bd --graph-mode link serve` | `bd serve` |
   | --- | --- | --- | --- | --- |
   | none | none | — | exit 2 | no BDP rows, silent |
   | none | set | — | **staged mint** (A4) then serve honestly empty | no BDP rows; notice "unminted; run `bd --graph-mode link serve`" |
   | present | none / **different** | any | exit 2 / refuse | no BDP rows; notice |
   | present, matches | matches | **absent** (clone; pull into a fresh dir; copy elsewhere — installation key mismatch) | refuse `ErrNotAuthority`; guidance: `bd --graph-mode link promote --steal` if this is the same database (a moved or re-keyed workspace — the lease names the old key), otherwise `bd --graph-mode link promote --rotate-url` | no BDP rows; notice |
   | present, matches | matches | **pending transition** | recovery first (B3, by evidence), then re-evaluate | no BDP rows; notice (plain `bd serve` never runs recovery — it never mints, fetches, or publishes; CLI reads answer `ErrNotAuthority` with the same notice) |
   | present, matches | matches | `(authority_id, epoch)` stale, or the lease held by **another** holder | refuse; guidance `bd --graph-mode link promote --steal` (an operator assertion of "same database"; a foreign holder's expiry alone never grants a takeover) or `--rotate-url` | no BDP rows; notice |
   | present, matches | matches | consistent but the lease **expired while still naming this workspace** (the server was down longer than the TTL) | **self-regrant** on the next lease write (same fence-cell predicate; no epoch change); serve | serve BDP rows |
   | present, matches | matches | ledger head not in the store (restore or different history) | refuse `ErrStateRewound`; guidance `bd --graph-mode link restore` | no BDP rows; notice |
   | present, matches | matches | `unverified` set | refuse until `bd --graph-mode link restore` | no BDP rows; notice |
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
   and its capability list untouched), `bd --graph-mode link serve` exits 3 after
   draining, `bd serve` logs and continues; the watcher **joins before the
   provider shuts down**. Both hazards → both watchers.
7. **Mounting:** `serveListen(opts, httpapi.Config{…, Graph: …})`.
8. **Lifecycle:** excluded from the post-command maintenance net by
   `commandPolicy`, not by the leaf name.

### A4. `bd --graph-mode link promote` / `restore` / `ledger` / `types install` (rulings 9, 11; amendments A5, A7, A9 — ruled 2026-09-07)

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
restore that cannot show continuity. On the shared-database leg under A9 this is the whole arbiter;
without A9 hazard R adds the remote fence. Stated residual: a `--steal` on
a copied database creates a second authority.

- **`Mint`** (serving path: `bd --graph-mode link serve`'s staged startup;
  A10 local solo trigger and gate mapping are P1 mechanisms, architecture §2b): precondition *no Scope
  row*; INSERT the singleton row, seed `graph_ledger_seq`, `mint` event,
  install the built-in catalog with `install` events; hazard S: take the
  lease; publish; `config_written` when `--scope-url` supplied the URL;
  finalize.
- **`bd --graph-mode link promote`**: precondition a consistent Scope row; three cases,
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
- **`bd --graph-mode link restore`**: runs after a database restore (`bd backup restore`
  — also reached by `bd bootstrap`'s restore action — calls
  `Admin.MarkUnverified` after `RestoreDatabase`, before its commit; a
  no-op without a witness). Exempt from the head check; precondition
  lineage match under the exclusive gate; branches on `LedgerDurability`:
  `in-state` (Dolt, v0) → requires a ledger snapshot reaching the witness's
  `{seq, hash}` (`--ledger <file>`, applied through `LedgerApply`) to show
  continuity, otherwise **rotates** (`refuse_url` + `rotate`, `config.yaml`,
  regrant); `independent` → re-validates (ancestry) and regrants; `none` →
  always rotates. Never silent.
- **`bd --graph-mode link ledger snapshot|apply`**: the hash-chained events as JSONL
  under a manifest `{scope_url, authority lineage, first_seq, last_seq,
  prev_hash of first, head_hash}`. `LedgerApply`'s recovery predicate:
  exclusive gate; lineage matches; the store's head equals the manifest's
  predecessor; every `hash` verifies. It restores **anti-reuse history,
  not graph content**: an applied `allocate` whose row is absent becomes a
  **`reserved`** allocation (never reusable; reads answer the gone family),
  the Scope lineage is replayed, `graph_scope_history` re-derived, and the
  counter set to `last_seq + 1`; then regrant. Full recovery is a database
  restore *plus* the lane.
- **`bd --graph-mode link types install <file>`**: post-mint catalog change (P1; W3
  emits the file): a fenced, published mutation with `install` events.

### A5. Graph verbs (v0 reads; P3 writes)

```text
bd --graph-mode link show <path>                       # a Bead or a Link, by path
bd --graph-mode link list [--kind bead|link] [--type URL] [--source PATH] [--target REF] [--after CURSOR] [--limit N]
bd --graph-mode link types [get <url>] | types install <file>
bd --graph-mode link status
bd --graph-mode link client local | server --server URL [--insecure-http]
bd --graph-mode link serve | promote [--rotate-url URL] [--steal] | restore [--ledger FILE] | ledger snapshot|apply
(P3)  bd --graph-mode link create|update|delete … ; bd --graph-mode link link <src> <dst> --type URL
```

Each verb reaches its role through an accessor (the `cmd/bd/label.go`
pattern plus the client route), with a route-fork test in the shape of the
`*_proxied_integration_test.go` / `*_embedded_test.go` pairs. CLI reads on
the authority workspace read its own state; on any other workspace they
refuse (`ErrNotAuthority`) — there is no replica read in v0. Under A9 as
amended by A10, an embedded workspace with no remote and no server is a solo
authority with local reads and verbs; embedded client hosts and registered-
backend client hosts retain the refusal contract. Solo authority serves no HTTP.
`internal/bdpclient` maps Problems back to the typed errors (round-trip
test). No collection routes before the cursor ADR (`ErrNotServedYet` on
the client route; `list` works on the local route from P1).

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
type LinkSelectRequest  struct{ TypeURL string; Source *Ref; Target *Ref; After Cursor; Limit int }  // endpoints symmetric (P0 council 2026-09-07)
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

**The row-level fence (ruling 13).** Every mutating body issues `SET
@bd_graph_role = 1` as its first statement inside the transaction and
`SET @bd_graph_role = NULL` as its last statement before `COMMIT` or
`ROLLBACK`, on every exit path (deferred); reads never set it. The clear
is load-bearing: probed over the MySQL protocol with a one-connection
pool, a transaction that commits without clearing leaves the next plain
statement on that connection unfenced, and one that clears does not. Verification row (ii) corrected the cancellation claim (2026-09-07): a
transaction cancelled *between* statements is rolled back and its
connection **pooled with the variable still set** on both server legs
(the UOW leg's `closeAttempt` rolls back on `context.WithoutCancel` and
releases the connection; the `*sql.Tx` shape keeps it because the driver
implements `SessionResetter` and `Validator`); only a mid-statement
cancellation closes the socket. The rule: the deferred clear runs on
`context.WithoutCancel(ctx)` with its own short timeout — proved to fence
the pooled session — and a clear that cannot be confirmed poisons the
connection (closed, never pooled); the CLI leg's wrapper begins its
transaction on `context.WithoutCancel` or pins `db.Conn`. The embedded leg
has no pool hazard: its driver's `ResetSession` returns `ErrBadConn`, so
every checkout is a fresh session. Mutating budgets carry two
statements more than their SQL alone. A body that omits the variable
cannot write — the triggers refuse its own statements — so the
conformance suite catches the omission by construction.

**Errors:** `ErrNoScope`, `ErrScopeExists`, `ErrNotAuthority`,
`ErrStateRewound`, `ErrStateChanged`, `ErrSyncRequired`, `ErrUnpublished`,
`ErrURLReused`, `ErrRepresentationTooLarge`, `ErrNotServedYet`,
`GoneError{Path, State}`. Home (P0, recorded 2026-09-07): the plane-neutral
sentinels are declared in `beadserrors` and aliased here; `ErrNoScope`,
`ErrScopeExists`, `ErrURLReused`, and `GoneError` are `graphops`'s own,
because `beadserrors`' charter keeps domain-naming refusals in the leaf.
`GoneError` matches `ErrNotFound` under `errors.Is` (the spec's same-404
default); a handler opts into the 410 by `errors.As`.

**P0 implementation notes (recorded 2026-09-07 after council 11's fold; the
code on `janet-beadgraph-p0` is the reference).** Scope-URL laws split in two:
`ValidateScopeURL` is the client-side law (a client may reference a bdptest
dev server's `…/local-test/` Scope) and `ValidatePersistedScopeURL` the
persisted-identity law used by `Mint`, `Rotate`, ledger events carrying
`scope_url`, and manifests (`local-test` refused there). `NormalizeScopeURL`
normalizes the **whole origin** under one WHATWG model — scheme and host case,
percent-encoded host characters, the ends-in-a-number rule (all-digit or `0x`
last label ⇒ IPv4, rendered dotted-decimal), IPv6 compression (`[::ffff:102:304]`),
ports with leading zeros and default ports — and the same model classifies a
reference as in-Scope or external; a noncanonical in-Scope spelling is refused,
an external reference is preserved byte-for-byte; a trailing-dot registered name
is canonical and a distinct host; no IDNA (configure IDN hosts as A-labels).
`LinkSelectRequest` takes `Source *Ref` and `Target *Ref` (endpoints symmetric;
the one-in-Scope-endpoint law). Ledger: `MaxLedgerSeq = MaxUint64-1`, seq ∈
[1, MaxLedgerSeq]; the counter is exhausted at `MaxUint64` and the chain
refuses wraparound. Descriptor canonical form: `description:""` ≡ absent,
`propertiesSchema:""` refused, a zero endpoint constraint is the empty set,
`conformsTo` sorted by code unit. Every string entering a JSON-backed value must
be valid UTF-8. **Ruled on bdp#1 (2026-09-08):** `ownsOutgoing` admits the
wildcard entry `"*": { max }` — every outgoing Link Type not listed explicitly
is owned, `max` bounds the whole owned set, explicit entries take precedence
for the types they name; `OwnedLinkDecl` gains that variant and `Owns` resolves
explicit-then-wildcard; the record's owned groups exist for types actually
present plus empty groups only for explicitly declared types; no group is
ever keyed `"*"`, and `Owns("*")` is false (it is a key, not a Type). Landed
on the P0 branch (1cbb5e6e3): `OwnedLinkDecl` is a sum —
`NewWildcardOwnedLinkDecl(max)`, `Wildcard()`, `WildcardOwnedLinkKey` — and
`ValidateTypeURL("*")` is refused by name everywhere a Type URL is expected.
The wire stays at the pin: the pinned bundle's `propertyNames:
absoluteHttpUrl` on `ownsOutgoing` and `ownedLinks` refuses `"*"` (bdpwire's
decoder holds member shape, not key grammar; the test-only tripwire
`TestWildcardOwnedLinkKeyIsNotInThePinnedBundle` retires with the pin bump).

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
  `bd --graph-mode link promote` compute different keys — cross-user shared roots are
  unsupported, as the workspace gate already declares); an **ephemeral home**
  regenerates it on every start (`ErrNotAuthority` until `bd --graph-mode link promote`);
  a moved workspace needs a `bd --graph-mode link promote` (guidance: "moved or copied").
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
| embedded Dolt (CLI only, A9 as amended by A10) | `internal/storage/embeddeddolt/beadgraph_*.go` | same body, `withConn`; solo topology wires the full local read contract with its witness and workspace-gate lease; client hosts wire `ErrNotAuthority` refusal. No embedded HTTP serving. |
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
HEAD)`; for the ledger-covered tables — descriptors, allocations, the
ledger — the delta must be explained by ledger events since the recorded
head, and bead and link bodies are checked by row provenance (ruling 13) —
advances the witness, and retries once. A refused delta is **undone**
when the recorded HEAD is an ancestor of HEAD: the eight tables are
reverted to their state at `w.StateCommit` in a new commit (the hazard-R
undo shape — table-scoped, later commits preserved) and the caller sees one
`ErrStateChanged`; otherwise the witness is marked `unverified` (ruling 14). The validation
also runs the **fence census** (Part D.7 = A): each replicated table carries
its three ruling-13 triggers; a missing one is repaired with the migration's
idempotent pair before the delta is judged. Descriptor caches are keyed by the descriptors table's hash.
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
guarded and resumable. **Initial P1 scope: eight replicated tables in five files**, proposed as **0069 or later**: slot
0067 is occupied by the merged versioned-beads Phase 1 migration (`issue_versions`,
the `store_epoch` singleton, `issues.current_revision`); slot 0068 is reserved
by its still-open Phase 2 (`0068_add_attribution_status`,
`issue_versions.attribution_status` and byte-preserving `durable_state` LONGBLOB
storage; the `version_id` and participation
steps remain deferred at the §0a pin),
and our claim is registered in the CLAIMED.md registry on the still-open
#6149 branch (row added 2026-09-07 at c53ef8810 as "0069 and later"),
not on main; the actual P1 migration slot must be rechecked —
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

**The row-level fence (ruling 13).** Each of the five table files also
carries, for every replicated table it creates, three triggers —
`<table>_bi`, `<table>_bu`, `<table>_bd` (`BEFORE INSERT/UPDATE/DELETE …
FOR EACH ROW`), body `BEGIN IF @bd_graph_role IS NULL THEN SIGNAL SQLSTATE
'45000' SET MESSAGE_TEXT = '<table>: out-of-role write refused'; END IF;
END` — twenty-four in all, each a `DROP TRIGGER IF EXISTS` + `CREATE
TRIGGER` pair (Dolt 2.1.8 has no `CREATE TRIGGER IF NOT EXISTS`; the pair
is resumable), no `NOW()`/`RAND()`. The lease table carries none
(dolt-ignored, fenced by its own law). Probed on Dolt 2.1.8: the body
parses only in `BEGIN … END` form; it fires with errno 1644; a session
variable gates it; the triggers replicate through the versioned
`dolt_schemas` table by push, clone, and pull; they are silent on
`DOLT_MERGE` and `DOLT_PULL` (ruling 14's territory); and one
multi-statement `Exec` of such a file over the tree's
`multiStatements=true` DSN (`doltutil/dsn.go`) creates them — the files
ship through `execMigrationBody` unchanged (the `dolt sql` CLI splits on the body's
inner `;`, so the `AllMigrationsSQL()` bundle the CLI-parity test loads with
`dolt sql -f` needs a `DELIMITER`-wrapped `cliCompatibleMigrationSQL`
rendition — which the protocol path in turn refuses — and the parity
oracle, which excludes `dolt_` tables, gains a trigger count). Three **P0
verification rows** gate the fence — a failure ships v0 with the
validator alone and ruling 13 records it: **answered 2026-09-07**
(`engdocs/BDP_P0_VERIFICATION_ROWS.md`, spikes on the P0 branch): (i)
**PASS** — `embeddeddolt.OpenSQL` with one pinned connection and one
`ExecContext` of the migration text creates the triggers, they fire, the
variable gates them, and `dolt_schemas` stages and commits; the
skip-by-rule branch is not needed; (ii) **PASS-WITH-RULE** — B3's clear on
`WithoutCancel` and poison on failure; (iii) **PASS-WITH-RULE** — hygiene
checks A–E pass on a trigger-carrying file and the real runner commits
`dolt_schemas` with the table and cursor; three P1 rules follow: the
CLI-bundle rendition above; check D's clone-local list and
`doltIgnorePatterns` must learn `graph_authority_lease` before the B4 twin
is enforced; `migrationSQLTouchesTable` cannot see trigger DDL, so a
pre-existing dirty `dolt_schemas` is refused by the post-pass signature
check rather than up front. **Ruled (Part D.7 = A, 2026-09-08):**
`dolt_schemas` is outside the eight hashed and inspected tables, so an
out-of-band `DROP TRIGGER` replicates silently and `content_skew.go`
cannot see it (equal hashes) — proposed: the validator and ruling 14's
inspection set gain a **fence census** over `dolt_schemas` (trigger
presence per graph table).
Clones receive the triggers with the schema and refuse raw DML too —
harmless: clones are not authorities, and their in-role paths set the
variable. `bd sql` gains no flag; the deliberate override is a
proxied-mode batch whose first statement sets the variable, or a raw
client session.

**Collation.** Dolt's default collation is already binary
(`utf8mb4_0900_bin` — probed), and no migration in the tree declares one.
Every identifier column below still carries **`CHARACTER SET utf8mb4
COLLATE utf8mb4_bin`** (written `BIN`) as the defense for providers whose
default is case-insensitive, with a contract case.

**Two epochs, two spellings (recorded 2026-09-07).** Every `epoch` in the
graph tables — `graph_scope.epoch`, the lease's `epoch`, `last_epoch`,
`birth_epoch` — is the **authority epoch** of ruling 9/A5, rotated by
promote, rotate, and restore-without-continuity. It is not the History
lane's `store_epoch` (versioned beads, migration 0067: bumped only by
restore, destructive reinit, or a token-scheme change, and voiding only
addresses of versions no longer served). To keep SQL unambiguous, P1 spells
the graph columns `authority_epoch`, `last_authority_epoch`, and
`birth_authority_epoch`; the docs keep the short names as the concept's name.

**Identity is Scope-relative.** Rows store the canonical Scope-relative
`path`; the absolute URL is `scope_url + path`, computed at the boundary,
so a URL rotation rewrites no rows.

**JSON is bytes.** `properties` and `descriptor` are canonical JSON bytes in
`LONGBLOB`, never the engine `JSON` type (the tree measured `1.0`→`1`,
integers past 2^53 rounded, `1e300` expanded in `internal/storage/issueops/metadata_cas.go` and the public
`issueops/metadatacas.go`; `-0.0`→`0` per the role guide). The canonical
form (P0 decision 3, recorded 2026-09-07) is RFC 8785 serialization applied
to each number literal's **exact decimal value**, never its nearest binary64:
`1.0`→`1`, `-0.0`→`0`, `1e300`→`1e+300`, and integers past 2^53 survive
intact; the exponent bound applies to the normalized value so that
canonicalization is a fixed point. Every string entering a canonicalized or
hashed value must be **valid UTF-8** (P0 council: invalid bytes collapse to
U+FFFD in JSON, so distinct values would hash identically). The frozen
ledger hash layout is `graphops`' golden: JCS of the event's members with
absent members omitted, `at` as RFC 3339 UTC with six fractional digits,
sha256 hex, genesis = 64 zeros. The value limit is a P1 number (Part D.1).
Admission law (bdp#21, ruled 2026-09-08): a number literal whose exact
decimal value does not round-trip through IEEE-754 binary64 (nearest double,
serialized shortest, equal to the literal's value) is refused at admission
with the offending JSON pointer, so on every stored `properties`/`descriptor`
value the exact-decimal form and a JCS peer's binary64 serialization agree
byte-for-byte; the ledger framing is exempt (its `seq`/`epoch` are Go integers
hashed exactly over the full uint64 range) and its frozen layout is unchanged
(P0 commit 944b1fc9f). Descriptor canonical form sorts `conformsTo` (and endpoint
`conformsTo`) by code unit — they are sets — so reordered parents fingerprint
identically. Canonicalization is the only thing the store does to the
content of `properties`: a URI or pinned reference inside it is neither
validated, resolved, canonicalized as a URL, nor traversed, and it is not an
edge — `graph_links` holds every edge there is, and a reference that needs
graph semantics is a Link, owned under an explicit entry or the wildcard
(bdp#1 item 5 as amended, ruled 2026-09-08).

**Provenance on every mutable row.** `last_authority_id` / `last_epoch` are
stamped by every mutation on descriptors, beads, links, and allocations.

**The graph-state version** is `DOLT_HASHOF_TABLE('<name>')` for each of
the eight replicated tables below, in this order, the eight hashes hashed
together with sha256; the lease is ephemeral and excluded. The descriptors
table's hash within it keys the descriptor cache.

| Table | Columns (type; nullability) | Keys / constraints |
| --- | --- | --- |
| `graph_scope` | `id TINYINT NOT NULL` (always 1), `scope_url VARCHAR(2048) BIN NOT NULL`, `authority_id CHAR(32) NOT NULL`, `epoch BIGINT UNSIGNED NOT NULL`, `minted_at DATETIME(6) NOT NULL` | `PRIMARY KEY (id)`, `CHECK (id = 1)` — singleton. **Distinct from History's `store_epoch`** (migration 0067), which fences restore and replacement on the issue plane; this epoch fences promotion on the graph plane; a restore reasons about both |
| `graph_scope_history` | `scope_url VARCHAR(2048) BIN NOT NULL`, `refused_seq BIGINT UNSIGNED NOT NULL`, `refused_at DATETIME(6) NOT NULL`, `reason VARCHAR(64) NOT NULL` | `PRIMARY KEY (scope_url)`; derived from `refuse_url` events |
| `graph_type_descriptors` | `url VARCHAR(2048) BIN NOT NULL`, `descriptor LONGBLOB NOT NULL`, `fingerprint CHAR(64) NOT NULL`, `installed_seq BIGINT UNSIGNED NOT NULL`, `installed_at DATETIME(6) NOT NULL`, `last_authority_id CHAR(32) NOT NULL`, `last_epoch BIGINT UNSIGNED NOT NULL` | `PRIMARY KEY (url)`; `UNIQUE (fingerprint)` |
| `graph_beads` | `path VARCHAR(1024) BIN NOT NULL`, `type_url VARCHAR(2048) BIN NOT NULL`, `revision CHAR(32) NOT NULL`, `attribution_principal VARCHAR(512) NULL`, `attribution_status ENUM('claimed','unknown') NULL`, `properties LONGBLOB NOT NULL`, `last_authority_id CHAR(32) NOT NULL`, `last_epoch BIGINT UNSIGNED NOT NULL`, `created_at DATETIME(6) NOT NULL`, `updated_at DATETIME(6) NOT NULL` | `PRIMARY KEY (path)`; `INDEX (type_url, path)`; `FOREIGN KEY (type_url) REFERENCES graph_type_descriptors(url)`; attribution columns both NULL or both set |
| `graph_links` | `path VARCHAR(1024) BIN NOT NULL`, `type_url … BIN NOT NULL`, `revision CHAR(32) NOT NULL`, `source_kind ENUM('in','ext') NOT NULL`, `source_path VARCHAR(1024) BIN NULL`, `source_url VARCHAR(2048) BIN NULL`, `source_pin VARCHAR(512) BIN NULL`, `target_kind ENUM('in','ext') NOT NULL`, `target_path VARCHAR(1024) BIN NULL`, `target_url VARCHAR(2048) BIN NULL`, `target_pin VARCHAR(512) BIN NULL` (endpoints symmetric and pins variable-width — **changed 2026-09-07, P0 council:** the pinned fixtures carry external-source Links such as `urn:external:pin-witness → beads/demo-f`, and a pin is an opaque string echoed byte-identically, so `CHAR(32)` cannot hold it), `attribution_*`, `properties LONGBLOB NOT NULL`, `last_authority_id`, `last_epoch`, timestamps | `PRIMARY KEY (path)`; `INDEX (source_path, type_url, path)`, `INDEX (target_path, type_url, path)` (a typed incoming read is a keyed scan — accepted from sjarmak's review); `FOREIGN KEY (source_path) REFERENCES graph_beads(path)` (a NULL `source_path` — external source — skips the check); `CHECK` exactly one of `source_path`/`source_url` per `source_kind` and exactly one of `target_path`/`target_url` per `target_kind`, and at least one endpoint `in` (the pinned spec's endpoint law); **no** uniqueness on (type, source, target) |
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

**P1 must ship what P0 settled (recorded 2026-09-07).** `source_path` is
nullable with the `CHECK` that at least one endpoint is `in` (the symmetric
columns above); the ledger counter's exhaustion rule; the descriptor canonical
form and UTF-8 rule stated under "JSON is bytes"; `migrationSQLTouchesTable`
learns `CREATE TRIGGER … ON <table>` and `DROP TRIGGER [IF EXISTS] <name>`
separately (a DROP names no table; metadata lookup); the Dolt lane runs the
ruling-13 spikes (they gate on `testutil.RequireDoltBinary` alone). The
installer's `Max` law covers the wildcard declaration (bdp#1, 2026-09-08):
an owning declaration — explicit or `"*"` — without `max` is refused; the
wildcard's `max` bounds the Bead's **whole owned set** — every owned Link
across every owned type, explicit entries included — and an explicit entry's
`max` MUST NOT exceed the wildcard's (refused at descriptor validation);
ruled OW1 = A on 2026-09-08, the literal reading (P0's narrower first reading
is being flipped). The P1 batched owned-Links read for a wildcard owner
selects all owned Links grouped by type under `LIMIT wildcard.max + 1`,
inside the ≤ 7-statement budget row; that single statement covers only the
whole-set bound, so an explicit group over its own `max` is caught after
grouping in the body or refused at acceptance — `graphops.CheckBeadRecord`
enforces both bounds at acceptance (P0 commit ec692e146).

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
- Wirings on all three legs (under A9 as amended by A10 the embedded leg wires the full
  read contract for the solo topology and the refusal contract for client hosts); the leg registry
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
  `set-many`/`unset`; `bd --graph-mode link serve`'s staged startup releases the
  exclusive gates before serving; a registered backend serves no rows; the
  watcher disables rows atomically and joins before shutdown; a heartbeat
  does **not** change the graph-state version; case-differing paths
  distinct and code-unit ordered; ownedLinks completeness incl. **empty
  groups** — empty groups only for explicit declarations, wildcard-owned
  groups only when a Link is present, none keyed `"*"` — and the bound under
  `LIMIT remaining+1`; keyset continuation
  inside one transaction; gone-family incl. `reserved`; a promote in
  another process is honored by the next read; descriptor read on a
  non-authority clone refuses; `bd init` re-run on any clone succeeds and
  installs nothing; `Mint` installs the built-in catalog with `install`
  events; ledger snapshot/apply round trip, gap refusal, foreign-lineage
  refusal, recovery-predicate regrant, counter set; the installer refuses
  an owning declaration without `Max`; statement budgets per method;
  `ErrStateChanged` triggers one validation under concurrent reads and the
  reads retry once.
- **Ruling 13 rows:** a raw `INSERT`, `UPDATE`, and `DELETE` on each of
  the eight replicated tables without the variable is refused (errno
  1644, the table named); the same statement with `@bd_graph_role = 1`
  in the session succeeds; a three-way `DOLT_MERGE` and a `DOLT_PULL`
  carrying graph rows land without the variable (the validator, not the
  trigger, decides them); a mutation followed by a raw statement on the
  same one-connection pool is refused (no leak); a body that omits the
  variable cannot write; a clone carries the triggers; the validator
  refuses a descriptors/allocations/ledger delta unexplained by ledger
  events and marks the witness `unverified`.
- **Ruling 14 rows (per route):** an SQL pull whose fetched delta touches
  a graph table with foreign provenance or ledger events of a foreign
  lineage is refused before merging, naming the table, and HEAD does not
  move; the same delta arriving by the CLI-subprocess route lands, and the
  next in-role transaction reverts the eight tables to the recorded HEAD
  in a new commit with later issue-plane commits preserved; `--strategy
  theirs` on a pull with a graph-table conflict leaves the graph table
  untouched and the pull failed; a clone without a witness takes the
  remote's graph state wholesale; a federation peer pull carries graph
  tables unfiltered and is refused on a foreign delta like any pull; the
  remote-migrate gate's fast-forward onto a remote that carries graph rows
  succeeds and the workspace refuses to mint (A9); a mirror round-trip
  produces no graph delta and no refusal; `bd dolt pull` with no graph
  delta is byte-identical.
- **Fence census (Part D.7):** a clone that received a foreign `DROP TRIGGER`
  through replication is repaired at its next validation, and the census never
  changes the graph-state version.
- **Solo topology (A10):** an embedded workspace with no remote and no server
  mints, holds a gate-satisfied lease, answers every link-mode read locally,
  serves nothing, and becomes a client host when a remote or shared database
  appears.
- **Properties are opaque (bdp#1 item 5 as amended, 2026-09-08):** a URI or
  pinned reference inside `properties` is data, never an edge; the store
  validates nothing inside `properties`, and `graph_links` holds every edge
  there is.
- **Rotation is never a remount:** a server started under a base URL that
  differs from the persisted Scope URL refuses, and no stored intra-Scope
  reference changes when the Scope URL rotates (paths are Scope-relative).
- Differential-gate rows: every legacy verb without `--graph-mode` is
  byte-identical, including `bd link`, `bd graph`, `bd graph check`,
  `bd restore`, `bd promote`;
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
- **Wire — P0 (vendored 2026-09-07 on the P0 branch):**
  `internal/httpapi/bdpwire/schema/bdp-v0.schema.json` is vendored verbatim
  with a `PROVENANCE` file (upstream repo, the plan's §0 commit, and per
  file the sha256 and git blob sha1) covering the bundle, the
  reference-domain and Read fixtures, the Read catalog and matrix, and the
  spec's JSON example fences; a test recomputes every digest and fails on
  drift. **Generator decision (Part D.3):** hand-written DTOs held to the
  bundle by parity, round-trip, and matrix tests — the recorded fallback;
  both generator routes were run on the pinned bundle and rejected
  (`oapi-codegen` cannot consume the bundle by external `$ref` and
  `openapi.v0.yaml` is frozen; `go-jsonschema` collapses the reference sum,
  the consts, and nullable `next` to `interface{}`), see
  `internal/httpapi/bdpwire/GENERATOR.md`. There is no `make bdp-gen`;
  `bdp-check` is the package's tests, which already run inside
  `make api-check`'s `go test ./internal/httpapi/...` step, so
  `scripts/ci/pr-policy.sh` gains no target. Decoding posture (council 11 fold):
  `bdpwire.Unmarshal`/`Decode` read member by member — exact case-sensitive
  names, required ⇔ no `omitempty` (welded to the bundle by the parity test),
  null refused except a collection's nullable `next`, each member held to its
  JSON type, integers decoded exactly from any RFC 8259 spelling within the
  documented Go `int` (64-bit) range; `ReadDiscovery.Validate()` enforces the
  two consts.

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
  state outside the roles — the ruling-13 triggers are silent on merge and
  pull (probed) — so, under ruling 14, every SQL pull route **fetches,
  inspects the eight tables against the tracking ref, and refuses a
  foreign or unexplained delta before merging** (`DOLT_PULL` commits a
  clean merge immediately and `ROLLBACK` does not undo it — probed — so
  inspection is the only pre-merge refusal point); routes that cannot
  inspect first fall to the **state-change validator** (B3), which reverts
  the eight tables to the recorded HEAD in a new commit or marks the
  witness `unverified`; no graph-table conflict is auto-resolved and
  `--strategy` never touches one; a workspace without a witness takes the
  remote's graph state wholesale. The replication/merge ADR
  (`engdocs/BDP_GRAPH_REPLICATION_ADR.md`) writes the mechanism under that
  charter before the migrations land.
- **Federation.** Graph tables ride filtered pushes **unfiltered, by
  decision, in v0**; the lease table never replicates.
- **`bd sql`, raw SQL, and force-push.** Out of contract for graph tables
  (ruling 13): raw DML on the eight replicated tables is refused by the
  row-level fence unless the session set `@bd_graph_role`; force-push and
  merges are the validator's and ruling 14's. `bd sql` itself is unchanged —
  no flag; a legacy statement never names a graph table.
- **`bd dolt pull`, `bd federation sync`, `bd vc merge`, `bd conflicts
  resolve`** gain the fetch-inspect step (one `DOLT_FETCH`, eight `dolt_diff`
  reads against the tracking ref) before merging and the graph-table
  exclusion for `--strategy` (ruling 14); gate output is byte-identical
  unless a graph delta is refused.
- **`bd backup restore`** calls `Admin.MarkUnverified` after
  `RestoreDatabase`, before its commit (no-op without a witness).
- **Root store policy** gains `commandPolicy` (Part A), and
  `commandNeedsExclusiveGate` in `cmd/bd/workspace_gate.go` — today true only
  for `backup restore` — learns `bd --graph-mode link types install`, `restore`, and
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
3. Generator choice for `bdpwire` — recorded at P0 (2026-09-07): hand-written
   DTOs held to the vendored bundle by tests (B8; `GENERATOR.md`).
4. Whether `bd --graph-mode link serve` remains after W2 (default: yes).
5. Whether hazard-R publication should use an isolated branch instead of
   soft-reset/checkout/revert. The constraint that decides it survives A9:
   the ephemeral lease table lives in the default branch's working set and
   branch-qualified sessions do not see it, so every fenced transaction runs
   on the default branch.
6. The adoption verb for the later of two mints under one URL (ruling 14,
   law 3) — name and shape fixed in the replication/merge ADR.
7. **Ruled 2026-09-08 (A):** the state-change validator and ruling 14's
   fetch-inspect set carry a fence census over `dolt_schemas` (three triggers
   per replicated table; repaired with the migration's idempotent pair; never
   part of the graph-state version).

## Part E — Ruled amendments (A1–A9 and 13–14: 2026-09-07; A10: 2026-09-08)

Ruled: A1 store-owned witness asserted in every transaction is the v0 lease
(ruling 9); A2 BDP rows inside `httpapi`, `bd --graph-mode link serve` the
only minting path among serving commands (A10 separately permits local solo
minting), `bd serve` never refusing on account of the graph,
intra-Scope references Scope-relative and a new base URL a rotation
(rulings 7b/12); A3 the `--graph-mode link|dependency` root flag with a
mode-and-path-keyed policy; A4 public `graphops`, `BeadGraph*` accessors, no
`backend/` aliases; A5 installation-keyed witness with multi-phase
transitions, hash-chained ledger, ledger lane restoring anti-reuse history,
provider `LedgerDurability`; A6 tracked `bdp.scope_url`, per-workspace keys in
`config.local.yaml`, tokens from files only; A7's shared-database half (the
fence cell); A9 v0 authority requires a shared database outside A10's
embedded solo exception; the remote half of A7 is deferred to the write-profile
ADR; A8 option A — constraint #1 scoped to
behavior, out-of-tree implementers take the declared source break with six
stubs and a CHANGELOG call-out. Ruling 13 (out-of-role DML, "A+B"): out of
contract, the state-change validator with ledger accounting, and
session-gated triggers on the eight replicated tables (B3, B4, B7, C2).
Ruling 14 (replication/merge ADR, option B): the gate plus the four-law
charter — fetch-inspect-merge on the SQL pull routes, validator revert
elsewhere, no auto-resolve or `--strategy` on graph tables, foreign deltas
refused whole, clones take remote state wholesale (B3, B7, C2).
A10 (solo topology, B), Part D.7 (fence census, A), the
`authority_epoch` spelling, and the properties-are-opaque amendment (bdp#1
item 5) ruled 2026-09-08. **These rulings are settled.** Full A10 text is in
architecture §2b; current BDP #19/#20 and Jim dependencies are in the plan
§0a refresh of 2026-09-10. Local solo verbs do not authorize embedded HTTP
serving.
