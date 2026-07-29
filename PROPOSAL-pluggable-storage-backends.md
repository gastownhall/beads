# PROPOSAL: Pluggable Storage Backends

**Status:** Historical — partially implemented, then superseded on 2026-07-16
**Date:** 2026-07-02
**Informed by:** the bts-rs Rust spike (a from-scratch Rust reimplementation
of the gc-contract surface that validated a backend-agnostic storage seam at 299/299 byte-parity
across two radically different backends), a five-dimension recon of this repo's storage
architecture, and a two-reviewer adversarial verification pass. All file:line citations verified
against `worktree-beads-new`.

> **Superseded scope:** This proposal is retained as the design record for the
> storage interface, shared issue core, SQLite adapter, and differential
> conformance work that grew from it. Those pieces remain. The direct
> PostgreSQL and MySQL adapters were rolled back before entering a tagged
> release because supporting additional general-purpose server databases adds
> substantial dialect, credential, schema-lifecycle, migration, CI, and
> operational complexity. Our goal is to keep Beads as simple as possible and
> consume as few resources as possible. The supported paths are now embedded
> Dolt, Dolt server, and SQLite; Dolt server's use of the MySQL wire protocol is
> unchanged. See [Storage Backends](docs/architecture/storage-backends.md) for
> the current design.

Everything below this notice is retained as historical design material, not as
current backend support or migration guidance.

---

## 1. End state

- `bd init --backend=dolt|sqlite|postgres` — the user picks the engine at init. The workspace
  locator records the choice; opening consults the locator, never the environment.
- **One core command set** (the gc-contract 16 plus the portable CORE inventory, ~67 commands)
  behaves identically on every backend, proven by a differential conformance harness — not by
  code review.
- **Storage-specific commands are capability-gated addons.** `bd history`, `bd dolt push/pull`,
  `bd vc`, `bd branch`, `bd diff`, `bd federation`, `bd backup`, `bd sql` are live only when the
  opened store advertises the matching capability; otherwise they are deterministic stubs that
  explain what backend provides them (Decision D2, resolved: stubs, not hidden — see §6).
  Pick Dolt → you get history and remotes. Pick SQLite → you get a fast, zero-dep, pure-Go
  single-writer store and those commands stub out.
- Dolt remains the default and richest backend. **Back-compat guarantee:** a workspace created
  before this migration keeps opening exactly as it does today — enforced by an explicit
  legacy-locator rule (§4.3), not by hope.
- Adoption path for existing workspaces: `bd export` → re-init on the new backend → `bd import`,
  with documented fidelity limits (§5, Phase 4 gate). In-place cross-backend data migration is
  a separate initiative.

## 2. What the Rust spike proved (the transferable payload)

Evidence: `bts-rs/docs/` (`SEAM_VALIDATION_FINDINGS.md`, `CONFORMANCE_GAPS.md`,
`RED_TEAM_FINDINGS.md`, `HISTORY_ON_YUGA_DESIGN.md`, `PERF_PG_VS_YB.md`, `REMOTE_DEPLOYMENT.md`).

### 2.1 The seam shape
Six small core traits (33 data methods) + **optional capability traits** reached via accessors
defaulting to "absent". Two backends (Postgres, file-backed memory) hit byte-parity with real bd
on 299/299 scenarios **with zero command-handler changes** — handlers never name a backend.

**Go gets the spike's hardest problem for free.** Rust's non-object-safe async traits forced a
399-line delegation enum; in Go, `storage.Storage` is already an interface and `Open()` returns
the interface value. Budget the port on semantics and conformance, not plumbing.

**Seam-size caveat (from review):** the spike's 33-method core is NOT evidence that beads' much
larger core (§4.1: ~107+24 methods after the fold) is cheap to implement per-backend. The spike's
"~1–10k LOC per adapter" anchor was measured against 33 methods. Phase 0 therefore includes a
core-shrink review: which core methods are truly primitive vs derivable-above-the-seam vs
capability material. Sizing in §8 assumes the core does NOT shrink (worst case).

### 2.2 The four seam-leak rules (each was a real bug in the spike)
1. **Construction is ON the seam.** `Open(locator, identity)` / `Init(backend, ...)` are seam
   methods; otherwise every entry point names concrete backends.
2. **Identity is ON the seam** — actor identity is construction-time config in the seam package.
3. **The locator is backend-neutral and self-describing** (`{backend, workspace_id}`); backend
   is read FROM the locator at open; `--backend`/env applies **only at init**.
4. **Capabilities OPTIMIZE core behavior; they never GATE it.** For every capability use inside
   a core command there must be a core-interface fallback, OR the whole command is
   capability-gated. (The spike's only runtime divergence violated this.)

### 2.3 Transactional-integrity rules that apply to beads' CORE (not just capabilities)
The spike's red team hit this bug class three times; in beads it lives on the core surface:
- **Same-tx side effects:** beads writes the events table inside the mutation transaction
  (`issueops/create.go:601`, `labels.go:151`, `helpers.go:147`; read via core
  `GetAllEventsSince`, `storage.go:84`, and `bd audit`). Any new backend must do the same —
  events emitted outside the mutation tx strand or duplicate audit rows on crash.
- **Denormalized `is_blocked` is the most conformance-dangerous core semantic.** It is
  maintained across 10+ issueops write paths (dependencies/close/reopen/delete/promote/…) and
  read by ready queries (`sqlbuild/ready.go:96`). Delete/purge must recompute surviving
  neighbours. A fresh backend reimplements this propagation on every mutation; the harness
  must carry required transition fixtures (close-blocker→ready, purge-orphan re-seed).

### 2.4 The conformance oracle
- **Differential, not static:** scenarios as real processes in throwaway workspaces; volatile
  normalization (`<TS>`/`<UUID>`/actor); per-step exit + stderr + JSON-aware stdout diff. A
  static canonicalized corpus "pins a formatter, not a system".
- **Multiset array diffing by default**, with a per-scenario `ordered` flag valid only where
  ordering is implementation-independent.
- **Property tests** for non-byte-diffable paths (minted IDs, `--graph`).
- **Concurrency proofs as capability gates:** the 24-process `ready --claim` drain applies only
  to backends advertising multi-writer.
- **Epistemic honesty — what green does NOT prove (§7 risk table):** hook-firing semantics
  (post-commit, rollback-suppressed — invisible to stdout/stderr diffing), equal-priority and
  wall-clock-hybrid ordering (`ready`'s 48h recency rule), and anything outside the in_scope
  predicate. Each gate below pins its in_scope coverage explicitly so "proven identical" is
  never claimed beyond what ran.
- Operational: unique candidate-binary path per run (`cp` over an exec-mapped binary fails
  silently and scores a stale binary).

### 2.5 History without Dolt (later, not now)
`bd history`'s minimal substrate is a per-issue, ordered, never-pruned field-delta changelog
written in the same tx as each mutation + `HistoryViewer{History, AsOf, Diff}` as a backward
fold. Dolt provides all three natively (`dolt_history_*`, `AS OF`, `dolt_diff`). So "pick Dolt →
get history" can later relax to "any backend with the Changelog capability" without
re-architecting.

### 2.6 Backend guidance from measured perf
Postgres bounded ops flat to 100k (list ~2ms). Remote cost = round-trips × RTT **plus ~6 RTT of
connection setup per command in the shell-out model** (~300ms pure overhead at 50ms RTT) —
mitigations: co-location, local PgBouncer ≥1.21 transaction mode, daemon endgame. Collapse write
paths to 1–2 statements; `FOR UPDATE SKIP LOCKED` IS the claim guarantee. YugabyteDB ≈ 2–3×
latency / 10× write cost — multi-region only. An embedded pure-Go store is the cheapest second
backend and the one that validates the seam.

## 3. Where beads is today (recon summary, review-corrected)

### 3.1 The good news — the seam is already 80% drawn
- `storage.DoltStorage` (`internal/storage/storage.go:200-213`) composes core `Storage` (62
  methods) + 11 sub-interfaces (144 total; + `Transaction` 24). The Dolt-shaped surface is
  already isolated in **5 named sub-interfaces** — VersionControl (16), HistoryViewer (3),
  RemoteStore (12), SyncStore (2), FederationStore (4) = 37 methods, ~87 genuine call sites in
  non-test cmd/bd. The other **6 sub-interfaces are backend-neutral** (BulkIssueStore,
  DependencyQueryStore, AnnotationStore, ConfigMetadataStore, CompactionStore,
  AdvancedQueryStore — ~45 methods, ~155 cmd/bd call sites serving CORE commands like
  `ready --claim`, `dep`, `comment`) and fold INTO the core seam (§4.1).
- **The capability-gating pattern already ships:** 10 optional interfaces
  (`storage.go:217-287`), `UnwrapStore` + type-assert at 22 cmd/bd sites, graceful errors
  (`sql.go:54-57`).
- **The public API is not frozen to the god interface:** root `beads.go:20` aliases
  `Storage = beads.Storage` (= `storage.Storage`); RemoteStore/SyncStore are exported as
  separately assertable interfaces. The retype does not break the public type surface — but see
  §4.4 for the public `Open*` constructors and the extension-compat note.
- **Real prior art for the oracle:** `tests/regression` (built for the SQLite→Dolt migration;
  found 70+ bugs) contributes workspace isolation, binary management, container TestMain, and a
  normalizer that already encodes cross-backend representational divergences
  (`regression_test.go:587` local-vs-UTC midnight; `:647` metadata `{}` vs absent). Its
  execution core does NOT transfer (§5 Phase 1).
- Integrations are portable for free (all 6 trackers bind to narrow `storage.Storage`); the
  tracker engine already demonstrates capability-with-fallback (`tracker/engine.go:695-717`).
- Dialect exposure is concentrated: ~90% of SQL flows through `issueops`/`sqlbuild`/`domain/db`.
  Dolt-specific SQL inside issueops sits in **7 files**: `as_of.go`, `commit_pending.go`,
  `diff.go`, `federation.go`, `blocked_merge.go`, `history.go` (`dolt_history_issues`), and
  `blocked_consistency.go` (`dolt_status` guard — backend-internal, moves with the backend).
  The first six map 1:1 onto gateable capabilities.

### 3.2 The hazards — what actually blocks a second backend
- **H1. Two competing seams, mid-flight — and BOTH are alive.** The `DoltStorage` path covers
  100% of commands. The **uow/domain layer** covers 13 commands via `*_proxied_server.go`
  duals and is an ACTIVE workstream (one command per PR since 2026-05-10, latest merges
  2026-06-24; the init gate at `init.go:135-140` is deliberate staging per PR #4488, and the
  CI lane is dark only until the gate lifts). It is Dolt-flavored at its Tx layer
  (`uow/doltserver_tx.go:28` = `CALL DOLT_COMMIT`), duplicates issueops (kept honest by
  Seam-A parity tests), and is already feature-diverging from the embedded path (`--offset`
  is proxied-only). **Proxied-server mode exists ONLY on the uow path** — `newDoltStore`
  refuses it (`store_factory.go:48-53`). D1 must resolve the seam relationship AND proxied's
  fate, with the uow workstream owner participating (§6).
- **H2. Dolt lifecycle is fused into every command's write path, with real protocol structure.**
  Not just "auto-commit in PostRun": `transactHonoringAutoCommit` (`dolt_autocommit.go:28-47`)
  **blanks the commit message** to suppress mid-tx commits depending on mode; a non-blank
  message + success sets `commandDidExplicitDoltCommit` which suppresses PostRun auto-commit
  (`main.go:1223-1227`); the tips system defers metadata writes and PostRun issues a SECOND
  commit for them (`tips.go:154-183`, `main.go:1233-1257`); auto-export/backup freshness reads
  `GetCurrentCommit` AFTER the commit; auto-push gates and error semantics differ from
  auto-commit; the autocommit-mode default itself is chosen by the CLI from `usesSQLServer()`
  (`main.go:1099-1106`). `commitPendingIfEmbedded`/`transactHonoringAutoCommit` have **21 call
  sites across 18 files**. This is a protocol, not a hook — it gets a specified contract (§4.2)
  and its own phase (2b).
- **H3. Global store + FIVE construction paths.** `store storage.DoltStorage`
  (`main.go:45`; **159 non-test refs / 61 files**, 15 more test files). Paths: PreRun choke
  point; lazy `direct_mode.go`; read-only; ~13 files opening routed/extra stores; **and the
  public API** — root `beads.go:63-73` `Open/OpenFromConfig` hard-code `dolt.New*`
  (+ `beads_nocgo.go` `OpenBestAvailable`), used by external Go extensions. The registry must
  own all five. The `HookFiringStore` decorator EMBEDS DoltStorage
  (`hook_decorator.go:28-29`) and is applied only on the main path today — the registry must
  own decorator application or lazy/read-only/routed opens keep silently skipping hooks.
- **H4. The migration stream is not portable.** 53 tracked + 10 dolt-local `*.up.sql`, MySQL
  dialect; migrations 0019/0028/0040/0041 execute `dolt_ignore`/`CALL DOLT_COMMIT` inline. New
  backends need per-backend migration sources (the `schema` runner's two-stream design is the
  extension point).
- **H5. Core-signature Dolt leaks.** `RunInTransaction(ctx, commitMsg, fn)` — where the message
  is load-bearing suppression semantics per H2, NOT advisory; `GetCurrentCommit` as freshness
  token (`export_auto.go:85`, `backup_auto.go:127`); `ApplyCompaction(commitHash)`; `bd edit`'s
  raw `*sql.DB` keepalive (`edit.go:150`); "dolt-ignored" LocalMetadata contract.
- **H6. Command metadata in name-keyed string lists.** `noDbCommands` (27), `readOnlyCommands`
  (14), dolt-subcommand patch lists (`main.go:770-808,122-137`).
- **H7. bd doctor** — 37/45 files Dolt-tinged, raw SQL on `dolt_remotes`/`dolt_status`.
- **H8. Mode-divergent semantics are unformalized.** The draft claimed this "includes a LIVE
  BUG": ~~with server-mode autocommit OFF, `GetCurrentCommit` never advances, so
  auto-export/backup freshness checks silently stop firing in server mode.~~
  **CORRECTED AFTER VERIFICATION: REFUTED.** The claimed "live bug" does not exist. The
  server-mode store (`internal/storage/dolt.DoltStore`) commits to HEAD **per write method,
  unconditionally** — independent of `--dolt-auto-commit` and of `maybeAutoCommit`
  (`dolt/issues.go:49` CreateIssue → `CALL DOLT_COMMIT`; UpdateIssue/CloseIssue/DeleteIssue
  likewise; `RunInTransaction` → `versioncontrolops.StageAndCommit`). So in non-proxied server
  mode HEAD advances on every persistent write, `DOLT_HASHOF('HEAD')` changes, and the
  auto-backup freshness check DOES fire. Separately, auto-export never even reaches the
  freshness check in server mode — it early-returns (`export_auto.go:38-41`, `if serverMode {
  return nil }`). The `maybeAutoCommit` machinery governs the EMBEDDED working-set-persistence
  flow, not the server store's per-method commits; the H8 draft was written against an
  inaccurate model of the server store. The rest of H8 stands: auto-commit ON embedded / OFF
  server; single-writer flock embedded-only (`store_factory.go:66-85`); server DoltStore has
  real streaming iterators (`dolt/iter_issues.go:48`) while embedded ships `NewSliceIter`
  stubs; `bd sql`/`bd admin` work in server mode but error in embedded (`sql.go:45`,
  `admin.go:17`) — and that embedded error string is gc contract class 3.
- **H9. Auto-import is a silent data-mutation path on the core surface** (missed in the first
  draft; review-added). `maybeAutoImportJSONL` runs in PreRun for every command
  (`main.go:1163-1164`), takes `storage.DoltStorage`, auto-imports JSONL into empty databases,
  and has a documented data-clobber history (`auto_import_upgrade_unit_test.go:74-87`). Its
  "auto-importing … into empty database" strings are gc error class 4 (the write-loss guard).
  A second backend that is empty-on-fresh-open is exactly the trigger condition; if its strings
  differ, gc's guard goes blind.
- **H10. Env precedence can override the locator at OPEN.** `BEADS_DOLT_SERVER_MODE=1` is
  precedence #1 over metadata.json (`configfile.go:232-243`); shared-server env/config
  overrides pinned `dolt_mode` with a Notice (`main.go:247`); `--global` hard-requires
  shared-server (`main.go:1071-1076`). Parsing is scattered (configfile, doltserver, init,
  bootstrap). The spike's "locator wins at open" rule must extend to topology, with an explicit
  ownership and inertness rule (§4.3).

### 3.3 The command surface (census, corrected)
~271 cobra commands (~111 top-level), all registered unconditionally **in package `init()` —
before any workspace resolution** (this kills naive hide-at-registration; see D2). Classified:
CORE ≈ 67 (incl. the gc-contract 16), STORAGE-SPECIFIC = 11 (+3 mixed: `bd gc`, `bd migrate`,
`bd restore`'s history fallback), INTEGRATION = 6 families, INFRA ≈ 27. **12 of the gc-16 have
dual (store+uow) implementations; `purge`, `count`, `version`, `stats` do not** — and `purge`
is precisely where the spike's red team found re-seeding bugs, so it is a required Phase 1
scenario, not a "covered elsewhere" case.

## 4. Target architecture — the clean-room object model, and the bridge to it

Two things are described here and it matters to keep them distinct: **§4.0 the destination**
(the clean-room object model we would build greenfield, which the migration accretes toward)
and **§4.5 the bridge** (the flat-interface waypoint the mechanical phases pass through).
The destination is what new code is written AGAINST from Phase 4 onward; the bridge is
scaffolding that de-risks the retype and is thinned afterward (Phase 7).

### 4.0 The clean-room object model (the destination)

```
┌────────────────────────────────────────────────────────────────────────┐
│ cmd/bd — commands, formatting, streams, contract strings               │
│ command registry: declared metadata {needsStore, readOnly,             │
│ requires: Capability} — replaces the name-keyed string lists (H6)      │
└──────────────────────────────┬─────────────────────────────────────────┘
                               │ consumes ONLY:
┌──────────────────────────────▼─────────────────────────────────────────┐
│ package store — THE SEAM (small, frozen, documented)                   │
│   Driver / Registry / Locator / Identity     (construction, rules 1–3) │
│   Store / Tx + ~7 domain interfaces          (core; Tx-first)          │
│   Capability interfaces + CapabilitySet      (optional surface, rule 4)│
│   Error taxonomy + retry classes             (typed, rendering-free)   │
│   storetest.RunContract(t, driver)           (the compliance suite)    │
└──────┬───────────────┬───────────────┬───────────────┬─────────────────┘
       │               │               │               │
  backend/dolt    backend/postgres  backend/sqlite  backend/fake
  (topologies:    (multi-writer;    (pure-Go;       (in-memory/file;
   embedded |      no history v1)    single-writer;   unit-test keystone)
   server |                          fills nocgo)
   proxied
   INTERNAL)
```

The core, sketched (signatures indicative, not final):

```go
// construction — the spike's leak rules 1–3, verbatim
type Driver interface {
    Open(ctx context.Context, loc Locator, id Identity) (Store, error)
    Init(ctx context.Context, opts InitOptions) (Locator, error)
}
type Locator struct{ Backend string; Raw json.RawMessage } // self-describing; env only at Init
type Identity struct{ Actor, Email string }                // on the seam; injectable for tests

type Store interface {
    // Run is the PRIMARY seam op: one retryable unit of work. The seam owns
    // phase-aware retry (Serialization -> bounded auto-replay of fn;
    // CommitIndeterminate -> surfaced, never auto-retried; fn must be replayable,
    // no external I/O while the Tx is open). Semantics lifted from uow/run_in_tx.go.
    Run(ctx context.Context, op OpInfo, fn func(Tx) error) error
    Begin(ctx context.Context, op OpInfo) (Tx, error) // low-level escape hatch
    Capabilities() CapabilitySet                      // per-INSTANCE (topology-aware)
    Limits() Limits    // {MaxAtomicTxBytes, MaxValueBytes, MaxTxDuration,
                       //  ConsistentScanBudget} — CLI pre-flights + chunks instead
                       //  of discovering backend ceilings as commit failures
    ChangeToken(ctx context.Context) (string, error)  // equality-only freshness cursor
    Close() error
}

// OpInfo + CommandScope are where the H2 commit protocol collapses to. NOTE
// (red-team blocker): commit messages are OUTCOME-derived in bd (composed after
// close/claim results — close_proxied_server.go:242-255, uow.RunInTxMsg), so the
// description travels on Commit, NOT on Begin. A CommandScope above Tx owns the
// one-version-commit-per-command rule (N txs -> ONE dolt commit), the tips
// deferred-metadata second commit, and PostWritePush ordering.
type OpInfo struct{ Command, Actor string; ReadOnly bool }

// the transaction is the first-class object — one per command/logical op
type Tx interface {
    Issues() Issues        // CRUD, search, ready, claims; wisps as a TIER param, not a
    Deps() Dependencies    //   parallel ×2 method family
    Labels() Labels
    Comments() Comments
    Events() EventReader   // reads; event WRITES happen implicitly inside mutations, same tx
    Config() Config
    Commit(ctx context.Context, desc string) error // desc outcome-derived;
                                       // Dolt: DOLT_COMMIT(desc); PG: COMMIT; reads: Rollback, never Commit
    Rollback(ctx context.Context) error
}

// capabilities: extend core behavior, never gate it (rule 4)
type CapabilitySet struct {
    History, VersionControl, RemoteSync, Backup, RawSQL bool
    // MultiWriter bool was too coarse (red-team, 2 lenses independently):
    Concurrency struct {
        ConcurrentReaders, ConcurrentWritersSafe bool
        WriterExclusion string // enforced-lock | lease | none
        ContentionModel string // row | workspace-CAS
    }
}
func History(s Store) (HistoryViewer, bool)
type HistoryViewer interface{ /* History, AsOf, Diff */ }
// + VersionControl, RemoteSync, Backup, RawSQL, Maintenance(GC/Flatten/Compact)

// errors: typed below the seam, rendered above it
var ErrNotFound, ErrClaimConflict error
type ErrUnsupported struct{ Op, Backend string }
type RetryClass int // Transient | Serialization | CommitIndeterminate | Permanent
```

**Every commitment traces to a scar** in the current system or the spike:

| Commitment | Scar it heals |
|---|---|
| Tx-first seam (`Begin` → domain ifaces → `Commit`) | `DoltStore`'s per-method commits = the stale-snapshot data-loss class (#3822) that forced the May pivot; command-scoped short tx is the proven concurrency unit. This is `uow.UnitOfWork` promoted to the seam. |
| ~7 small domain interfaces, no god interface | the 805-line panic wall (PR #3792); "can't easily mock DoltStorage"; 168 methods before one command works |
| `OpInfo` on `Begin` | H2: commit protocol currently lives in CLI globals (message-blanking, `commandDidExplicitDoltCommit`, tips double-commit) |
| `ChangeToken()` on core | backend-neutral freshness: `GetCurrentCommit`/`DOLT_HASHOF` is Dolt-specific, so export/backup freshness must key on a neutral opaque token any backend can supply (SQLite/Postgres have no Dolt commit hash). (NB: the earlier "H8 live bug" rationale is withdrawn — see H8; ChangeToken stands on backend-neutrality alone.) |
| per-instance `CapabilitySet`, extend-never-gate | `bd sql` works on Dolt-server, stubs on Dolt-embedded — capability varies by topology; the spike's only runtime divergence was a capability gating core behavior |
| topologies INSIDE `backend/dolt` | embedded/server/proxied are three connections to ONE engine; CLI knowledge of them produced 38 scattered mode checks |
| events written inside mutations, same tx | the spike's red team hit emit-outside-tx three times; the use-cases already do it right — make it structural + contract-tested |
| `backend/fake` + `storetest.RunContract` | nothing tests against the interface today; every suite needs a Dolt container |
| wisps as a tier param | today every use-case method is duplicated ×2 (41 where ~25 would do); the `--offset` union-paging divergence is a symptom |
| slices, not streaming iterators, in v1 | all 10 `Iter*` methods are slice stubs in the embedded backend anyway; streaming returns later as a measured need |

One-sentence summary: **the clean-room seam is Dustin's `UnitOfWork` widened to the full
domain surface, wearing bts-rs's construction/capability/error discipline.**

### 4.1 Bridge-seam arithmetic (the flat waypoint; review-corrected)
Core = today's `Storage` (62) **+ the six backend-neutral sub-interfaces (~45)** = ~107 methods,
+ `Transaction` (24), all mandatory for every backend. The ~155 cmd/bd call sites of the six
folded interfaces need **zero conversion**. The ~87 call sites of the 5 Dolt-shaped interfaces
convert to capability asserts. `DoltStorage` is deleted; the Dolt backends keep a `var _`
compile assertion for core + all capabilities. Phase 0's core-shrink review may pull some of
the ~107 above the seam (derivable) — treat any shrink as upside, not plan.

### 4.2 The WriteLifecycle contract (replaces the naive "PostWrite hook")
A specified seam protocol, designed in Phase 2b BEFORE code moves:
- `WriteSummary{didWrite, explicitCommitOccurred, readOnlyCommand, messageParts, deferredMeta}`
  carried per command (absorbs today's `commandDidWrite`/`commandDidExplicitDoltCommit`
  globals and the tips deferred-write protocol).
- Two seam calls bracketing the CLI's own export/backup steps: `PostWriteCommit(summary)`
  (Dolt: auto-commit incl. the second tips commit; others: no-op) then — after the CLI's
  auto-export/auto-backup, whose freshness check consumes a **neutral ChangeToken()** (core,
  with the capability-shortcut-plus-fallback shape; motivated by backend-neutrality, since
  `DOLT_HASHOF` is Dolt-specific — NOT by the withdrawn H8 "live bug", see H8) —
  `PostWritePush(summary)` (Dolt: auto-push with its swallowed-error semantics; others: no-op).
- In-transaction suppression (`transactHonoringAutoCommit`'s blank-message trick) becomes an
  explicit seam behavior: the backend decides mid-tx checkpointing from the summary + its own
  mode; the message parameter on `RunInTransaction` stays but is re-specified as "operation
  description; versioned backends MAY use it as a commit message" (D3).
- Autocommit-mode POLICY (today chosen by the CLI from `usesSQLServer()`) moves into the Dolt
  backend's topology config — the CLI no longer knows modes exist.

### 4.3 Locator, legacy rule, and env precedence
- Locator in `.beads/metadata.json`: `{backend: "dolt"|"sqlite"|"postgres", ...}`. `dolt_mode`
  becomes Dolt-internal topology config.
- **Legacy-value rule (review blocker):** the existing `backend` field is UNTRUSTED legacy —
  workspaces in the wild may carry `backend:"sqlite"` from the pre-removal era
  (removed in 87493ce91), and `GetBackend()`'s hard-return of "dolt" (`configfile.go:207-210`)
  is a deliberate shim over exactly that. Rule: introduce a NEW locator key (e.g.
  `storage_backend`, plus a metadata schema version); absent/legacy/unknown values + presence
  of a Dolt data dir resolve to **dolt**, never to a new backend. A Phase 1 harness fixture
  with a legacy `backend:"sqlite"` metadata.json pins this. This closes the trap where a legacy
  value resolves to the new SQLite backend, opens an empty store, and H9 auto-import silently
  re-seeds/forks the workspace.
- **Env precedence:** `BEADS_DOLT_*` (19 vars) are owned by the Dolt backend and are **inert
  for non-Dolt locators**; env parsing consolidates behind the registry; the Phase 1 harness
  pins the precedence table (H10). `--global`/shared-server: the CLI keeps a seam-level
  "workspace redirection" concept whose only current provider is Dolt shared-server topology —
  on SQLite/Postgres, `--global` errors with a capability stub (D2 pattern).

### 4.4 Errors, formatting, and the public API
- Typed errors below the seam (NotFound/ClaimConflict/Unsupported{op,backend}/retry-class);
  ALL envelope/stream routing and contract strings above it — including gc error class 3
  (`bd sql` embedded string), class 4 (H9 auto-import strings — now backend-neutral pinned
  contract), and the claim-conflict text.
- Public API: `beads.Open*` route through the registry (H3 fifth path). **Compat note for
  extension authors:** on non-Dolt workspaces, type-asserting `RemoteStore` etc. on the
  returned `Storage` starts returning false — document in release notes; the assertion pattern
  itself (`beads.go:27-31`) is unchanged.
- Single binary, no dynamic plugins. "Addons/packs" = capability-gated stubs inside one binary
  + build tags where deps demand (embedded Dolt is cgo-only; modernc SQLite is pure-Go → nocgo
  builds gain a local backend for the first time, `beads_nocgo.go:27`).

### 4.5 Bridge vs destination — how the two relate

The mechanical phases (2a/2b/3) retype the CLI onto the FLAT core interface (§4.1) because
that keeps a 159-reference migration mechanical. That flat interface is **scaffolding, not
the destination**: from Phase 4 onward, the `uowStore` adapter's inner surface is built as
the PUBLIC clean-room seam (§4.0 `Tx` + domain interfaces) rather than as a private adapter
detail — so the destination accretes underneath the migration instead of requiring a second
one. Phase 7 then thins the flat layer opportunistically (or keeps it as sugar). Existing
components map as: `uow.UnitOfWork`/use-cases/`domain/db` → the seam's embryo;
`EmbeddedDoltStore`/`DoltStore`/`issueops` → `backend/dolt` interior; the 13 duals,
`DoltStorage`, the string lists, and the Seam-A parity tests → deleted.

### 4.6 Future-backend expandability (FDB, redb-class embedded KV, S3)

The seam must not assume SQL — two of the three named future backends are key-value and one
is object storage. Design constraints this imposes, to be validated by adversarial review:
- **FoundationDB:** interactive transactions exist but carry hard limits (~5s / ~10MB);
  `bd import`-scale work cannot be one `Tx`. The seam therefore must NOT promise unbounded
  transaction size — bulk operations need an explicit chunked/bulk path on the seam, and the
  contract suite must pin what atomicity bulk ops actually guarantee. Ordered scans and
  secondary indexes are backend-built (the bts-rs readiness-projection pattern applies).
- **redb-class embedded KV:** single-writer ACID — same shape as the SQLite slot. (redb
  itself is a Rust library; in-process use from Go implies cgo/FFI or it arrives via the
  bts-rs lineage — the seam question is only "can an embedded KV implement the contract",
  and the spike's file-backed mem store already answered yes.)
- **S3:** no multi-key transactions. A conforming backend is manifest-CAS shaped (commit =
  conditional-PUT of a new manifest; `Commit` returns a Serialization retry class on CAS
  loss) — which the `Tx`+`RetryClass` model can express, but claim latency and contention
  behavior must be honestly capability-flagged (likely `MultiWriter: false` or
  claims-unsupported). S3 may be BETTER fitted as the target of RemoteSync/Backup/cold
  history (the bts-rs BlockStore/RefStore substrate is S3-native with CAS on refs) than as
  a primary Store; the seam should permit both without contortion. `ChangeToken` maps
  naturally (manifest ETag).

### 4.7 Red-team revisions (Fable panel, 5 lenses, all verdicts holds-with-fixes) — NORMATIVE

These contract additions override/extend §4.0 where they conflict; fold into the sketch on
the next full edit. Convergent = demanded independently by 2+ lenses.

**Convergent (adopted):**
1. **`Store.Run(closure)` is the primary op; commands migrate ONTO it in Phase 7** (not off
   it). Retry ownership lives on the seam; typed errors gain backend-supplied `RetryAdvice`
   (backoff hint + budget); storetest includes an error-injecting fake and requires that a
   conforming driver's advice lets 24 contending claimers converge (no livelock).
2. **CommandScope above Tx** (see §4.0 note): one version-commit per command, tips second
   commit, commit→backup/export→push ordering; dolt-log divergence scenarios join the Phase
   2b gate (stdout diffing alone cannot see N-commits-instead-of-one).
3. **Read-consistency contract:** statements within one Tx observe ONE snapshot (PG:
   REPEATABLE READ or single-statement collapse; the two-phase ready-union read is the
   motivating bug); within-Tx read-your-writes is contract-tested; read families are
   classified single-snapshot-required (ready, claim-candidate, mutation-feeding reads) vs
   chunk-consistent-permitted (export, audit, stats) — FDB's ~5s limit applies to READS too.
4. **Bulk/chunked port with pinned atomicity:** per-chunk atomic, idempotent upsert,
   resumable journal; crash-mid-bulk + auto-import-resume (H9) fixtures in Phase 1;
   `create --graph` atomicity resolved against `Limits()`.
5. **Wisp/tier respec:** tier is a DERIVED attribute (ephemeral/no_history/infra-config
   routing — never caller-chosen); query methods take visibility flags, not a tier enum;
   explicit cross-tier ops (PromoteFromEphemeral, AffectedByDeletion); **tiered-atomicity
   contract** (Commit atomic per tier, pinned ordering, `PartialCommit` error class or
   `AtomicTiers` flag); a tier×capability locality matrix (History/RemoteSync/Backup/export
   MUST exclude local-tier data) with fixtures: purge-spares-no_history,
   backup-excludes-wisps. All of this feeds D5.
6. **Ordering/predicate semantics become a written contract:** binary-codepoint collation
   (PG DDL pins COLLATE "C"), second-precision timestamps, full tiebreak chains incl.
   NULL-first, LIKE-wildcard/escape behavior, metadata `{"k": null}` counts as present;
   clock ownership moves client-side (a `Clock` provider joins Identity on the construction
   seam) so relative-time predicates and the 48h-hybrid ordering are deterministic +
   contract-testable.

**Single-lens (adopted):**
7. **Mutation-observation contract** (repo lens, blocker): `Tx.Commit` yields a
   `MutationSummary` (feedable from the same-tx event rows) consumed by a registry-owned
   hook dispatcher; the decorator wraps Tx, not just Store; hook-artifact fixtures join the
   Phase 1 harness; Phase 7 conversions gate on hook parity.
8. **Interleaving rule:** capability methods may not run between Begin and Commit/Rollback
   on the same Store (typed error, storetest-pinned); per-topology connection budgets are
   documented backend-author contract (embedded's eager second dolt_ignore connection binds
   lazily or every command pays issue-#4303's dominant cost).
9. **Sync/archive TARGETS are a first-class seam concept distinct from Store** (S3 lens):
   `BlockStore{Put,Get}`/`RefStore{Get,CAS}` target interfaces + per-scheme drivers
   (s3/gs/file); `CapabilitySet.RemoteSync` rescoped to backend-NATIVE remotes (Dolt);
   Phase 6 cold history and portable backup-to-URL route through targets — S3's best fit,
   without pretending it is (or isn't) a primary Store. Backends whose commit protocol can
   strand garbage (CAS-race orphans) MUST implement + declare Maintenance.GC; doctor
   surfaces it.
10. **Error taxonomy additions:** `ErrBusy` (Transient; the embedded flock string is its
    pinned rendering); claim errors carry structured holder/status (the gc class-2 string
    is rendered ABOVE the seam from data, incl. idempotent self-reclaim success);
    Open/Begin blocking-vs-fail-fast behavior specified per concurrency class; backends MAY
    resolve CommitIndeterminate internally (verify-after-timeout) and return a definite
    class.
11. **ChangeToken contract:** opaque, equality-only, data-changed ⇒ token-changed
    (false positives permitted); Dolt = hash over non-ignored core tables (NOT HEAD);
    per-tier advance semantics pinned; S3 = seq embedded in the manifest, not raw ETag;
    FDB = versionstamped last-mutation key.
12. **Testing-story honesty** (repo lens): backend/fake + storetest get their own phase,
    gate, and sizing row; evaluate a go-mysql-server-backed fake running the REAL SQL
    (reuse, not a fifth semantics copy); Seam-A parity-suite deletion re-gated on
    single-SQL-stack convergence inside backend/dolt (embedded TxProvider over the embedded
    engine → delete issueops), not on dual-file collapse; §3.1's "integrations are free" is
    WRONG — the tracker keys its Dolt fast path on `DB()` presence and must key on the
    History capability (same audit for every RawDBAccessor consumer: SQL-speaking ≠
    has-Dolt-system-tables).
13. **Streaming:** unbounded consumers (audit, sync, export) keep a cursor/`since+limit`
    path (or a StreamingReads capability) before Phase 7 converts them — "slices only" is
    bounded-by-workspace materialization, stated as such.

**§4.6 corrections from the future-backend lenses:** FDB limits apply to reads too (~5s
read-version expiry; 100kB value cap vs bd's unbounded LONGTEXT — chunked content or
declared `Limits`); the readiness-projection pattern transplants only with rework (chunked
catch-up outside the op Tx, lease-based projector); redb: what the spike validated is "an
embedded single-file backend behind the seam for the gc-16 surface" — the 9-sort-order
index design, ~107-method surface, and cross-process behavior remain unvalidated, and redb
has no C API (a Go binding does not exist; that backend arrives via FFI work or the bts-rs
lineage); S3 claims stay CORE (never "claims-unsupported") — the drain gate keys on
`Concurrency.ConcurrentWritersSafe` with a disclosed contention model; 412/409/503/timeout
→ RetryClass mapping table to be added.

## 5. Migration phases

Each phase lands green with the default backend byte-identical; each has a verification gate
that names its in_scope coverage.

### Phase 0 — Decisions + contract pinning (docs only)
- Resolve D1–D6 (§6) with maintainers.
- **Leak-4 audit** over capability-sourced behavior in core commands (restore's history
  fallback; export/backup freshness → ChangeToken; `bd gc`'s portable decay phase; tracker
  fast path). Classification defines the core command set.
- **Core-shrink review** (§2.1 caveat): triage the ~107 core methods into primitive /
  derivable-above-seam / capability.
- Census additions from review: H9 auto-import; the 21 `commitPendingIfEmbedded` sites; the
  seven issueops Dolt files; the WriteLifecycle protocol inventory (H2).
- Pin H8 behavior contracts (incl. the iterator and autocommit divergences) into
  `BackendCapabilities` (`configfile.go:183-204`).
- **Gate:** signed-off decision doc; core-vs-capability census table; WriteLifecycle contract
  spec reviewed.

### Phase 1 — The oracle before the surgery
**A new differential runner reusing `tests/regression` scaffolding** (workspace isolation,
binary management, container TestMain, the battle-tested normalizer) — NOT an extension of its
execution core: today's runner is 177 Go closures compared by end-state snapshot only, `t.Fatal`s
on any non-zero exit, and merges stdout+stderr (`regression_test.go:363-374,811-831`). New:
per-step recorder (exit code, split streams, JSON-aware diff), replayable scenario format,
multiset-default + `ordered` flag, in_scope predicate, backend/mode parameter on workspace
setup, property tests for minted IDs, the claim drain (multi-writer only), unique
candidate-binary paths.
- Scope: gc-contract 16 × their flags first (incl. **purge** — no uow dual, red-team history),
  plus fixtures: legacy-locator (§4.3), auto-import class-4 trigger (H9), close-blocker→ready
  and purge-orphan re-seed transitions (§2.3), the env-precedence table (H10).
- **Shake-out: Dolt-embedded vs Dolt-server.** Premise (review-corrected): these are KNOWN
  DIVERGENT (H8) — the run formalizes the divergences, it does not validate on identical
  engines. Allowlist discipline: entries are backend-PAIR-scoped, field-scoped, tagged
  intentional-vs-known-bug, and expire with the Phase 2 scrub. **Mode waivers do not carry
  into SQLite runs.** Expect the autocommit asymmetry to be the dominant waiver class. (The
  earlier "expect to confirm the H8 live bug" line is withdrawn — H8's server-mode
  export-freshness bug was refuted on verification; the server store commits per write and
  auto-export is server-mode-disabled.)
- **Gate:** harness green on Dolt-vs-Dolt with a fully attributed allowlist; coverage report
  artifact (scenarios × commands × flags) checked in.

### Phase 2a — Neutralize the core seam (the mechanical part)
- Fold the six neutral sub-interfaces into core (§4.1); retype the global `store` + decorator +
  `molecules.NewLoader` + `remotecache.StoreOpener` + helpers from `DoltStorage` to the core
  interface (**159 refs / 61 files + 15 test files**); convert the ~87 Dolt-shaped call sites
  to UnwrapStore asserts; delete `DoltStorage`.
- Consolidate the FIVE construction paths (incl. public `beads.Open*`) into the registry;
  registry owns decorator application (closing the existing lazy/read-only/routed hook gap);
  new locator key + legacy rule (§4.3).
- Convert H6 string lists to declared per-command properties.
- **Gate:** full suite + Phase-1 harness Dolt-vs-Dolt: zero NEW divergences vs the Phase-1
  allowlist.

### Phase 2b — The WriteLifecycle migration (the semantic part; own phase, own gate)
- Implement the §4.2 contract; migrate the 21 `commitPendingIfEmbedded`/`transact*` call
  sites, PersistentPostRunE, the tips deferred-write protocol, ChangeToken for export/backup
  freshness (on backend-neutrality grounds — the "fixes the silent server-mode
  export-freshness bug" rationale is withdrawn; that bug was refuted on verification, so
  ChangeToken is a portability requirement, not a bug fix, and needs no behavior-change release
  note), retype H9 auto-import and pin its strings.
- **Gate:** Phase-1 harness re-run; the ONLY acceptable diffs are the enumerated bug fixes;
  autocommit/push/export ordering pinned by new dedicated scenarios.

### Phase 3 — Capability-gate the addon surface
- `Capabilities()` is **instance-level** (computed after open, backend × topology — review
  fix: `bd sql` works on Dolt-server but not Dolt-embedded, same backend). Gating happens at
  **RunE-time via stubs** (D2 resolved): registration stays in `init()` (no bootstrap
  restructuring; `bd --help` and the CI docs-regen stay deterministic); absent capability →
  deterministic stub error naming the backend that provides it (federation_nocgo precedent;
  `bd sql`'s embedded string preserved verbatim as contract).
- Surface: `bd dolt` (16 subcmds), `bd vc`, `bd branch`, `bd history`, `bd diff`,
  `bd show --as-of`, `bd federation`, `bd backup`, `bd sql`, `bd flatten`, Dolt legs of
  `bd compact`/`bd gc`, `db-proxy-child`, `--global` (§4.3).
- `bd doctor`: per-backend check registry (D6); Dolt checks (incl. `blocked_consistency`'s
  `dolt_status` guard) move into the Dolt backend's set.
- Audit + flip the `--backend=sqlite` deprecation contract (`init.go:215-225` hard-errors
  today; docs/scripts written against that string need a coordinated, release-noted flip).
- **Gate:** on Dolt, behavior + `--help` byte-identical; a test-only capability-less backend
  exercises every stub; docs regen green.

### Phase 4 — Second backend: Postgres (THE PROOF backend; reordered per owner direction)
Owner-stated success criterion: **beads runs on a Postgres backend and the bd CLI simply
doesn't have the history functionality.** Postgres — not SQLite — is therefore the seam
validator and the critical-path deliverable. (The first draft's SQLite-first ordering
followed the spike's cheapest-second-backend guidance; the owner's proof criterion overrides
it.)

**Two candidate build routes — choosing is part of D1, because they hinge on the uow
relationship:**
- **Route A (recommended if D1 lands the adapter): ride the uow stack.** A Postgres
  `TxProvider` (plain BEGIN/COMMIT; the commit-message param is ignored) + a dialect port of
  the `domain/db` repositories (~75 methods of EXISTING, Seam-A-parity-tested SQL; the
  MySQL-isms are enumerable — `ON DUPLICATE KEY`→`ON CONFLICT`, backticks, no `CALL DOLT_*`)
  + the `uowStore` adapter + the five gap units +
  per-backend DDL. This REUSES the already-implemented dangerous semantics (same-tx events,
  `is_blocked` propagation) instead of re-deriving them, and leans on the workstream's own
  stated invariant (issue #4489): "the local-vs-proxied difference is confined to the uow
  transaction layer (the mode-polymorphic db.Runner)."
- **Route B (fallback, if D1 keeps the layers separate): direct core implementation**
  (~107+24 methods over a fresh schema), cribbing the bts-rs PG adapter wholesale (schema,
  order/ready indexes + batched hydration, single-statement claim via `FOR UPDATE SKIP
  LOCKED`, typed retry classes, optional readiness projection). Independent of the uow
  roadmap; more code; a THIRD implementation of the business semantics to keep honest —
  **the hard part is semantics, not SQL** (§2.3: same-tx events, `is_blocked` propagation,
  delete/purge neighbour recompute, wisp/tier semantics, D5 local-tables).

**Proof-wedge milestone (demoable before Phases 2–3 fully land):** minimal locator `backend`
field + one factory arm + the PG stack + a TEMPORARY full-`DoltStorage` compat shell whose 37
Dolt-shaped methods return typed Unsupported errors (graceful errors, NOT panics — the D1
design-history lesson) + PersistentPostRun neutralized for non-Dolt. This demonstrates "bd on
Postgres, `bd history` absent" without waiting for the 159-ref retype; the shell is deleted
when Phase 2a lands. Accepted sharp edges for the demo: auto-export/backup freshness and
parts of `bd doctor` degrade on the wedge; the gc-16 harness subset is the demo's acceptance
test.
- **Gate:** Phase-1 harness PG-vs-Dolt green over the gc-16 in_scope set, THEN widened to the
  CORE-67 surface before the backend is announced (coverage artifact enforces the widening —
  no shipping green-on-16-only). Transition fixtures (§2.3) green. 24-process claim drain (PG
  advertises multi-writer). **Remote deployment guidance shipped** — per-command
  connection-setup cost measured, PgBouncer/co-location documented (§2.6).

### Phase 5 — Third backend: SQLite (the nocgo/embedded story; now optional)
Pure-Go (`modernc.org/sqlite`), documented single-writer (claim-drain gate N/A); fills the
nocgo hole (`beads_nocgo.go:27` — non-CGO builds currently have NO local backend). Under
Route A it rides the same production line (a SQLite `TxProvider` + dialect port); under
Route B, a direct implementation with the WAL lessons (`synchronous=NORMAL`,
`wal_autocheckpoint=0` + explicit checkpoint cadence, pragmas on the read pool, no unclosed
rows). Slice-iterator stubs are legal (embedded Dolt ships them; server streams — both
satisfy the interface).
- **Gate:** as Phase 4 minus the claim drain; single-writer lock UX tested. Export→import
  adoption-path fidelity documented (events history, wisps, LocalMetadata do NOT obviously
  round-trip — measure and state).

### Phase 6 — History as a portable capability (optional)
Changelog capability (same-tx field-delta log) + HistoryViewer-as-fold (§2.5). Dolt keeps its
native implementation. Mirror the spike-documented wire quirks (bare JSON array, PascalCase
keys, full snapshots, client-side `--limit`, volatile CommitHash normalized).

### Phase 7 — The end-game: promote the clean-room seam, thin the bridge (opportunistic)
Migrate commands off the flat conveniences onto `Begin/Tx/Commit` directly — file by file,
no flag day, each conversion deleting a `RunInTransaction` wart — until the flat layer is
thin enough to delete or small enough to keep as sugar. No deadline: the system is already
multi-backend and capability-gated before this phase starts. Exit criterion: the `store`
package's godoc IS the backend-author contract, and `storetest.RunContract` is the only
thing a new backend must pass before the differential harness.

### Explicitly deferred / out of scope
- In-place cross-backend DATA migration (`bd migrate --to-backend=X`) — the interim adoption
  path is export→re-init→import with Phase-4-documented fidelity limits.
- Retiring the uow layer's 13 dual command paths (fate fixed by D1; execution follows Phase 3).
- YugabyteDB/FDB backends; dynamic plugin loading; backend-neutral federation.

## 6. Decisions required

- **D1 — Canonical seam AND proxied-server's fate** (one decision, not two). **Corrected after
  git-history review: the uow/domain layer is NOT dormant** — it is an active workstream
  (Dustin Brown / coffeegoddd, DoltHub), converting one command per PR since 2026-05-10 with
  merges through 2026-06-24 (#4039–#4488: provider, list, update, delete, show, close, reopen,
  ready, dep, config, context, query). The init gate is deliberate staging ("lifting the gate
  is a separate, deliberate change", PR #4488), not abandonment. Its motivation is
  **multi-process concurrency**: embedded Dolt is single-writer; a shared server + short
  per-command transactions serves concurrent agents. "Freeze uow" is therefore not a viable
  recommendation.
  **Design-history fact that reframes this decision:**
  a `storage.DoltStorage` implementation over the proxy WAS built first — `DoltServerStore`,
  PR #3792, May 6–7 2026 — and died in four days as an 805-line wall of
  `panic("unimplemented")` (128/132 methods), pivoted to the UOW design with only a two-line
  TODO as rationale. It died because the god interface cannot be implemented incrementally
  from scratch — a cause the uow workstream's own six weeks of use-case building has since
  removed (the adapter delegates to them), and which Phase 2's interface shrink + capability
  split removes for every future backend. "We tried a store over the proxy already" is
  therefore true but not dispositive.
  **Recommended reconciliation — compose, don't pick:** `storage.Storage` remains the backend
  seam (what the CLI consumes; what SQLite/Postgres implement; where the capability/addon
  model lives — use-cases have no answer for command gating). Build the missing **`uowStore`
  adapter** (core `Storage` implemented over `UnitOfWork` use-cases; today no such adapter
  exists and `store_factory.go:51/96/169` TODOs point the other way — this is the crux to
  align with the uow owner). Consequences: proxied-server becomes a topology of the Dolt
  backend behind the registry; the 13 `*_proxied_server.go` duals collapse; the domain
  repositories over `db.Runner` (3 methods) become an ACCELERATOR for future SQL backends
  (supply Runner+TxProvider+dialect, reuse the repos) rather than a rival seam; the
  `Tx.Commit` = `CALL DOLT_COMMIT` (`uow/doltserver_tx.go:28`) adopts the §4.2 WriteLifecycle
  contract one layer down. **Full sketch with the method-by-method mapping and gap analysis**
  (headline: ~60% of core maps 1:1 to existing use-case
  methods; the true remaining cost of full proxied coverage is five small gap units, not 54
  more command conversions).
  **The alternative (full use-case trajectory):** commands migrate to use-cases everywhere and
  backends supply repositories. Coherent, but: ~54 more command conversions, dual
  implementations + parity tests for the duration, non-SQL backends become impossible
  (`Runner` assumes SQL), and the capability model is still needed regardless.
  **Divergence governance either way:** the two paths already diverge in features (`--offset`
  is proxied-only because the embedded issues+wisps union cannot page — PR #4488) — assign an
  owner for keeping the paths semantically converged until they reunify, and add the
  divergences to the Phase 1 allowlist as tagged, expiring entries.
  **This decision requires the uow workstream owner in the room.**
- **D2 — Absent-capability UX. RESOLVED by review: stubs, not hidden.** Cobra registration
  happens in package `init()` before workspace resolution; hiding requires pre-cobra locator
  resolution and breaks help/docs determinism. Stubs preserve the gc error contract and the
  federation_nocgo precedent. (If hiding is ever wanted, it is its own sized work item.)
- **D3 — `RunInTransaction` message param:** keep, re-specified per §4.2 (the blank-message
  suppression semantics become explicit seam behavior; "advisory" was wrong).
- **D4 — MergeSlot\*/Slot\***: keep in core (bead-backed, portable); revisit when Postgres
  lands (Phase 4) — real row locking may obviate them.
- **D5 — Wisps' dolt-local tables** on backends without an ignore concept: table-prefix
  convention (recommended) vs separate file/schema. Feeds the Phase 4 schema design.
- **D6 — Doctor scoping:** per-backend check registry (recommended) vs neutral check
  interfaces.

## 7. Risks

| Risk | Mitigation |
|---|---|
| Two-seam divergence widens while D1 is undecided (uow conversions merge weekly; `--offset`-class proxied-only features accumulate) | D1 decided WITH the uow owner in Phase 0; until then, new proxied features carry a convergence note + Phase 1 allowlist entry; the `uowStore` adapter collapses the duals once agreed |
| Legacy `backend:"sqlite"` metadata resolves to the new backend and auto-import forks data | §4.3 legacy rule (new locator key, dolt-default fallback) + Phase 1 fixture — was a review blocker, now designed-out |
| Silent drift during the Phase 2a retype | Harness EXISTS FIRST; zero-new-divergence gate; commit per file-cluster |
| WriteLifecycle migration changes semantics invisibly | Own phase (2b) + dedicated ordering scenarios; only enumerated bug-fix diffs accepted |
| Allowlist waivers rot and mask real regressions | Pair-scoped, field-scoped, tagged, expiring entries; no carryover to new-backend runs |
| Green harness over-trusted | §2.4 epistemics: hooks/timing/coverage limits stated per gate; coverage artifact required; CORE-67 widening enforced before Phase-4 announce |
| A from-scratch backend (Route B) re-opens the spike's transactional bug class | §2.3 rules are Phase 4/5 implementation requirements with mandatory transition fixtures; Route A sidesteps by reusing the parity-tested use-case semantics |
| `bd doctor`/backup partial coverage confuses users | Capability-aware stubs + doctor prints per-backend coverage |
| Public-API extensions break on non-Dolt workspaces | §4.4 compat note; assertions return false (defined behavior), release-noted |
| Schema drift between per-backend migration streams | Shared logical-schema contract suite each backend's fresh DB must pass |

## 8. Sizing (rough, sequential; review-adjusted)

| Phase | Size | Notes |
|---|---|---|
| 0 | ~1 week | decisions + leak-4 audit + core-shrink review + WriteLifecycle spec |
| 1 | 2–3 weeks | new runner on old scaffolding; gc-16 scope + fixtures (was 1–2; review: per-step core is new code) |
| 2a | 2–3 weeks | fold + retype (159 refs/61 files) + registry (5 paths) + metadata properties |
| 2b | 1–2 weeks | WriteLifecycle contract migration (semantic; own gate) |
| 3 | 1–2 weeks | instance-level capabilities + stubs + doctor registry + deprecation flip |
| 4 | Route A: 4–6 weeks / Route B: 5–8 weeks | Postgres proof: A = PG TxProvider + repo dialect port (~75 methods of existing SQL) + adapter + 5 gap units + DDL; B = ~107-method direct core + is_blocked/events semantics from scratch + schema; both + conformance to CORE-67 |
| 5 | 2–4 weeks (Route A) / 4–7 (Route B) | SQLite riding the same production line (A) or direct (B); nocgo hole |
| 7 | opportunistic | thin the flat bridge onto the §4.0 seam; no deadline |
| 6 | 2–4 weeks | optional; independent — and under Route A, the Changelog capability could later give PG history too (§2.5), making "no history on Postgres" a v1 statement, not a permanent one |

Phases 4/5 are independent of each other; 6 independent of both.
**Critical path to the owner's PROOF** ("bd on Postgres, no history"): 0 (D1 + route choice) →
1 (gc-16 harness subset) → Phase-4 proof wedge (~6–9 weeks). **Critical path to the full
end state** ("users choose a backend", shipped properly): 0 → 1 → 2a → 2b → 3 → 4
(~12–19 weeks sequential); the wedge work is on that path, not thrown away — only the compat
shell is deleted when 2a lands.

---

## Appendix: review provenance

Recon: 5 parallel read-only explorers (storage interface census; Dolt leak census; command
surface census; existing-abstraction assessment; bts-rs learnings extraction) — ~578k tokens,
311 tool calls. Adversarial pass: 2 reviewers (feasibility-vs-repo, completeness-vs-spike),
verdicts sound-with-fixes; 2 blockers, 8 majors, 5 minors, 13 missing items — all folded into
this revision. Notable review discoveries retained above: the six-neutral-interfaces blocker
(§4.1), the legacy-locator trap (§4.3), the WriteLifecycle protocol structure (§4.2/H2), the
instance-level capability requirement (Phase 3), ~~the H8 live bug (server-mode export
freshness)~~ (corrected after verification: refuted — the server store commits per write and
auto-export is server-mode-disabled; ChangeToken is retained on backend-neutrality grounds),
and the H9 auto-import hazard. Clean-room shred: 5-lens Fable panel
(object-model, repo-reality, FDB, embedded-KV, S3) — all verdicts holds-with-fixes; 2
blockers (outcome-derived commit messages vs OpInfo-at-Begin; hook-observation contract), 17
majors; all folded as §4.7.
