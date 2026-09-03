# BDP in beads: the bead-graph plan

**Status:** Draft v21 — feat/bead-graph — **P-1 REOPENED** for the W-arch amendments A1–A9 (§9); P0 code is BLOCKED until they are ruled. (Thirteen adversarial review rounds:
1–7 on the whole plan, SOUND at round 7; 8–13 on the storage-interfaces
section, SOUND-ADDITION at round 13; v6 withdrew the Issue projection from
v0 on review-round-5 counterexamples; v9–v11 record the P-1 ruling tranches — all
twelve decisions are ruled; v14–v21 reopen P-1 for the nine amendments)
**Date:** 2026-09-02 (v1: 2026-08-31)
**Owners:** Donna Box (ruling), janet (drafting/implementation)
**References:** the BDP spec (gastownhall/bdp `docs/specs/bdp.md`), beads#6051,
this repo's `backend/` conformance surface, `engdocs/PROJECT_CHARTER.md`.

## Terminology

- **Graph beads / graph links** — Resources in the **graph store**: the
  BDP-shaped tables (beads, links, Type Descriptors, allocation/tombstone
  ledger, authority marker) reached through the normalized storage
  abstraction and served over BDP. ("Native" was retired: inside this repo
  it reads as the existing implementation, the opposite of what was meant.)
- **The graph store** — the graph contract realized by any storage provider
  (Dolt in tree is the reference realization; other providers plug in
  through the same `backend/` contract).
- **Issues / Dependencies / wisps** — the existing issue stack. Unchanged,
  not served over BDP in v0.
- **The bead graph** — the union concept from ruling 1: the graph beads will
  eventually hold work AND non-work information (wisps being the existing
  example of non-work information) once the C lane lands.

## 0. The BDP pin, and the spec-first dependency

This plan targets the BDP spec **as of the owned-Links rulings**:
**BDP commit `aee075f5`** (the gastownhall/bdp PR #17 merge, 2026-09-01),
schema bundle `schemas/bdp-v0.schema.json` at that commit, Read conformance
matrix `packages/conformance/matrices/read-v1.json` at that commit
(38 scenarios).
**No implementation phase begins until the pin is written here.** BDP remains
a draft; "matrix green" exits below mean green against the *pinned* matrix,
re-pinned deliberately, never against a moving `main`.

Model laws this plan builds to (all normative at the pin):

- A Link is first-class; its `id`, `type`, `source`, `target`, and pin are
  immutable; repoint/re-pin is delete-and-create.
- **Owned Links:** a Bead Type may own outgoing Link Types; each owned Link —
  target, pin, and properties — is part of the source Bead's versioned state;
  every mutation of an owned Link (create, delete, property update) versions
  the source. The record read serves an `ownedLinks` member: complete Link
  records, keyed by owned Link Type URL, ascending code-unit id order.
  Unowned incident Links are a view and version nothing.
- Revisions are opaque and equality-only. **Each surviving Resource whose
  state actually changes receives a fresh opaque revision; a semantic no-op
  (RFC 6902 §4.6 value comparison) changes none; and A→B→A is three
  distinct revisions — a reverse transition never reuses one.** Deletion
  mints nothing for the deleted Resource — its result reports the deleted
  identity (the final live revision is Transactional `DeletedData` Event
  material, not a deletion-result member); an owned-Link deletion result
  additionally reports the owning source's fresh revision.
- References: the URI is identity; a pin is provenance, echoed byte-identical,
  equality-only, never validated in v0. In-Scope and external references have
  different canonicalization duties — the Go model captures them as a sum,
  not a naked pair.
- Authorization views are closed projections, closed over owned Links.
- The Read problem table is closed vocabulary (including `resource-pruned`
  and `resource-erased` — merged in gastownhall/bdp#16).

## 1. Goal and constraints

Implement BDP in this repo: a first-class graph store the
protocol (and eventually the CLI) is implemented in terms of, beside — not
instead of — the existing Issue/Dependency machinery.

Hard constraints, in priority order:

1. **Zero compatibility degradation, defined precisely:** *same-version*
   legacy behavior is byte-identical — every existing CLI verb, JSONL
   interchange shape, journal record, sync path, and out-of-tree `backend/`
   implementation behaves exactly as before on the same binary. Schema
   migrations keep their existing version discipline (an upgraded database
   is "ahead" of an older binary, which refuses to open it — that is the
   *current* contract, and this plan does not promise more than the repo
   already does). Mixed-version rollout beyond that is out of scope unless
   ruled otherwise.
2. **BDP fidelity** at the pin, provable by the pinned matrix under its
   packaged/public-boundary and self-certified/in-process provenance
   split.
3. **One seam per axis.** Two different seams exist and must not be
   conflated: an outer **authority seam** (which workspace/store, which
   authorization view, which read snapshot — `ScopeResolver`) selects
   exactly one scope; the **representation seam** (graph store vs projected —
   `unionscope`) is C-lane future work now that the v0 projection is
   withdrawn (§5) — in v0 the resolver fronts the graph store directly.
   Call sites see `graphops` interfaces; composites are the only
   switches.

## 2. What exists (survey — corrected after adversarial verification)

- **Storage contract:** `internal/storage/storage.go` defines `Storage` as
  **28 role accessors** (IssueLifecycle, IssueReader, IssueClaimer,
  ReadyClaimer, BatchCloser, BatchCreator, DependencyEditor, Commenter,
  Counter, Memories, …), each returning a role interface. Implementations:
  `*dolt.DoltStore` under decorator chains (`hook_*.go`, telemetry) plus a
  distinct **UOW/provider** chain. Documented policy: adding a required
  `Storage` method breaks out-of-tree implementers.
- **Optional-capability idiom — with teeth:** bare type assertions on a
  decorated store are a KNOWN BUG CLASS here: decorators embed an interface,
  so methods outside it are not promoted; the repo requires `UnwrapStore`
  peeling before optional assertions (`hook_decorator.go:160`) and carries a
  regression test for exactly the silent-skip failure
  (`cmd/bd/vc_recompute_test.go`). `bd serve` assembles a deliberately
  narrow role source (drops hooks, keeps telemetry) — full unwrapping there
  would change semantics.
- **Backends actually in tree:** server Dolt, embedded Dolt, and the
  UOW/proxied path. **SQLite is gone** (`cmd/bd/backend_support.go`);
  `backend/conformance` exists but `RunAll` openly does not exercise version
  control, sync, or federation families. `backend/` has a completeness guard
  requiring public aliases for every internal type reachable through the
  contract.
- **Read paths are not snapshots, and not even read-only:** role reads open
  a transaction per call (`dolt/store.go withReadTx`); issue paging is
  offset-oriented and finalized above storage; and "ready" reads run an
  advisory WRITE (waking expired defers) on server, embedded, and UOW paths;
  even `OpenForReadOnlyCommand` returns a writable store.
- **Data model:** `Issue` (`internal/types/types.go:17–190`) is very wide
  (well over 40 exported fields), `Metadata json.RawMessage` as extension
  point, `RowVersion int64` equality-only with documented partial coverage.
  `Dependency`: surrogate `ID` populated only by some read paths, endpoints,
  `Type`, `Metadata` as **string**, `ThreadID`, **no revision**;
  `depid.New(issueID, target)` keys on endpoints only — no type — so at most
  one edge per (source, target) pair, and delete/recreate would reuse the
  projected URL.
- **v0 REST precedent is narrow:** detail is the only **read**
  representation carrying `revision` (`GET /v0/beads/issues/{id}`);
  list/JSONL explicitly forbid it, while guarded **mutation** responses
  (update, close) also return their resulting revision. The
  HTTP layer maps ordinary typed errors to generated Problem DTOs centrally
  (`internal/httpapi/problem.go`) — the house pattern this plan follows.
- **Auth today:** bearer tokens grant the whole surface and carry no user
  identity or scopes (`internal/httpapi/auth.go`); routes run one
  table-driven middleware path (auth, project identity, deadlines,
  concurrency). Any BDP serving must preserve those semantics regardless of
  listener choice.
- **Wisps:** same `Issue` struct routed to a second table by storage-class
  flags; detail assembly carries `isWisp` explicitly and resolves
  issue-then-wisp; the two tables share one logical ID space enforced by
  transactional sibling checks (`cross_table_id_collision_test.go`). Wisps
  are **private/transient: excluded from export and federation by default** —
  so they are a policy decision for BDP serving, not a free rider.
- **The type shoehorn precedent:** `IssueType` is an open vocabulary
  (decision, message, molecule, gate, event, plus `types.custom`);
  `issue_type NOT IN` filters exist. Non-task semantics on the Issue chassis
  is proven practice.
- **Memories:** `memoryops` is a separate key/value plane with its own role.
  **Operator ruling 2026-09-01 (external to this tree): the memory system is
  legacy** — successor is Memory-typed Beads on this graph; no new
  investment. Note `Memories()` remains wired in `bd serve` today; retiring
  it is future work outside this plan.
- **Charter tension, named honestly:** `engdocs/PROJECT_CHARTER.md` frames
  beads as a focused issue tracker and prefers metadata over new schema for
  extension concepts. A general graph store is a product-scope expansion.
  **P-1 decision #1 is an explicit charter ADR** — this plan does not
  proceed on implication.
- (Correction from v1: `internal/storage/domain` is UOW-specific machinery
  over `types.Issue` and `.beads`-directory concerns, not a generic domain
  landing zone. The graph package lands as its own leaf package.)

## 2b. Where BDP and the Issue/Dependency stack disagree

The conflict inventory, consolidated. Each row is a law of the pinned BDP
spec set against verified current behavior; the last column says what the
conflict costs. This section is why the v0 projection was withdrawn (§5)
and is the requirements list for any C-lane path.

| Area | BDP law (pinned) | Current Issue/Dependency stack | Consequence |
| --- | --- | --- | --- |
| Revision coverage | Every record read serves an opaque, equality-only revision; every state-changing operation on a surviving Resource mints a fresh one; a semantic no-op mints none | `RowVersion` has documented partial coverage (direct-UPDATE text paths bypass it); label writes touch only the labels table; revision is served on the detail read and mutation responses only — list/JSONL forbid it | No existing token can stand in for a BDP revision |
| Reverse transitions | A→B→A is three distinct revisions | `updated_at` is second-precision `DATETIME` with documented same-second ties; `bd import --allow-stale` restores old rows including timestamps | State-derived revisions are impossible (r5 blocker) |
| Out-of-band writes | (Implementation constraint, not spec text: BDP's revision/identity laws presuppose the authority observes every mutation) | `bd sql` permits arbitrary direct SQL; compaction rewrites text in place; backup/restore resurrects historical state | No complete mutation feed exists; only funnel (C1/C3) or storage-level observation (C2) can close it |
| Identity non-reuse | Committed Resource URLs are never reassigned — surviving deletion and epoch changes | Same-ID delete/recreate is permitted; import UPSERTs over existing IDs and accepts caller-supplied historical `created_at`; rename (delete+create) can A→B→A-reactivate an ID | Legacy IDs cannot be BDP URLs without a durable allocation/tombstone mechanism the stack lacks |
| ID grammar | Creation-time canonical IDs, multi-segment supported, reject-don't-trim | Configurable prefix grammar + adaptive-length collision-probability IDs; validation checks prefix shape, not BDP path grammar | Eligibility/surrogate policy required before any legacy ID is served (C lane) |
| Type system | One immutable nominal declared Type per Resource; descriptors with `conformsTo`; a Type describes beads or links, never both | `issue_type` is an ordinarily mutable column; open string vocabulary via `types.custom`; no descriptors, no hierarchy | Type immutability is violated by ordinary updates (r5); descriptor catalog must be built |
| Edge multiplicity | Links are first-class; no uniqueness constraint on (type, source, target) | `depid.New(issueID, target)` — at most ONE edge per (source, target) pair, type excluded from the key | Dependencies structurally cannot represent BDP Links (S2 killer #1) |
| Edge versioning | Every Link carries its own revision; owned-Link mutations version the source | Dependencies carry no revision; dependency edits never touch the source Issue's `row_lock`; `Metadata` is a `string`, surrogate `ID` populated only on some read paths | S2 killers #2 and #3; no owned-links concept exists |
| Snapshot reads | Collection cursors continue ONE logical projected snapshot across requests, bound to an authorization view | Per-call read transactions (`withReadTx`); offset pagination finalized above storage; "read-only" paths write (defer-wake); `OpenForReadOnlyCommand` returns a writable store | BDP Read semantics need a new snapshot port; existing role readers cannot serve it |
| Authorization | Per-request Authorization View — a closed projection, closed over owned Links; uniform 404 nondisclosure | Bearer token grants the whole surface; no identity, no scopes, no view concept | View mapping is a P-1 design, not a translation |
| Deletion lifecycle (Read profile) | Logical identity non-reuse survives deletion; `resource-pruned`/`resource-erased` disclosure vocabulary on reads | Deletion frees the ID for reuse; no disclosure vocabulary | The gone-family Read contract must be built in the graph store |
| Deletion lifecycle (Transactional) | Deletion results report deleted identity; tombstones and erasure records propagate on the changefeed | No tombstones; no erasure machinery | Transactional-profile obligations; arrive with P3 writes |
| Changefeed (Transactional profile) | Change Groups at Scope positions, projection advances, erasure records, no-Event erasures | The journal has a frozen vocabulary limited to Issue/Dependency/Comment payloads, emitted structurally inside issue mutations | Frozen journal stays untouched; the graph gets its own changefeed (§4 matrix) |
| Content model | One authored JSON properties OBJECT per Resource, schema-validated per Type | Typed columns (status enums, priority int, timestamps) plus a `Metadata` blob | The column→properties mapping is a design artifact (the C lane inherits §5's table) |
| Multi-writer history (Transactional/history contract) | One serialized serving authority per Scope; independently writable replicas and multi-authority merge of one Scope history are excluded | Independently writable Dolt clones merging later is a normal workflow | Decision 9: authority rule; foreign-clone graph merges out-of-contract |

## 3. Thrust 1 — the abstract data model (Go)

**What Go gives us:** structurally-satisfied interfaces — Java-interface
shape, Rust-trait spirit (implicit satisfaction, declared where consumed),
capability discovery by type assertion, composition by embedding. The repo's
role-accessor style is already exactly this idiom; `graphops` speaks it.

Two layers, strictly separated (review Blocker 1/High 6):

- **Wire DTOs are generated from the pinned BDP schema bundle** — the
  protocol layer serializes those, and only the BDP handler maps domain
  errors to generated Problem records.
- **Domain values are immutable and JSON-faithful:**

```go
package graph

// Properties is an immutable authored JSON OBJECT value (BDP properties
// are objects, not arbitrary documents): backed by copied raw bytes;
// rejects duplicate keys; preserves numbers (no float64 laundering);
// deterministic encoding; and provides the RFC 6902 §4.6 semantic-equality
// check that gates revision minting (a no-op write MUST NOT mint).
type Properties struct{ /* unexported: raw []byte + parsed index */ }

// Ref is a sum, not a naked pair: an in-Scope reference (canonicalized at
// admission, resolvable locally) or an external one (opaque, preserved
// byte-identically). Both may carry an equality-only pin.
type Ref struct{ /* unexported discriminant; constructors enforce */ }

type Revision string // opaque, equality-only

type Bead struct { /* unexported fields; accessors */ }
// ID, Type (immutable), Revision, Properties.
// NOTE: ownedLinks is not physically duplicated inside the immutable
// authored Bead value — but it IS semantically covered Bead state. The
// port assembles it from the Links themselves, in the same snapshot, and
// EVERY record projection — singleton, collection item, selection item —
// returns a BeadRecord carrying the complete ownedLinks expansion, one
// entry per declared owned type, empty entries included. (Coverage —
// owned-Link mutations versioning the source — is a storage-transaction
// law, not a struct field.)

type Link struct { /* unexported; ID, Type, Revision, Source, Target Ref, Properties */ }

type TypeDescriptor struct { /* ID, Name, Describes, ConformsTo, PropertiesSchema, OwnsOutgoing{Label,Max}, endpoint constraints */ }
```

Laws in the package, tested once: canonical-ID grammar (reject-don't-trim),
canonical-URI ordering, owned-Link trigger law (as invariant checks the
storage transactions call), semantic no-op equality. BDP problem-code
constants live in the generated protocol layer, NOT here — the domain stays
transport-neutral and speaks typed Go errors.

```go
package graphops

// Scope answers Reads within ONE resolved (workspace, authorization view,
// read snapshot). Errors are ordinary typed Go errors — transport-neutral;
// the BDP handler maps them to Problems.
type Scope interface {
    // BeadRecord = Bead + its complete ownedLinks expansion, assembled in
    // the SAME snapshot. Collections return records too — for a Bead whose
    // Type owns, the member is never elided from any projection (absent
    // exactly when the Type owns nothing). Acceptance also asserts each
    // inlined Link's type equals its entry key and source equals the
    // containing Bead — mirroring the BDP parser's contextual laws.
    // Plan-owned collection tests enforce all of it (the external matrix
    // validates the schema but does not force the optional member's
    // presence on every item).
    Bead(ctx context.Context, id string) (graph.BeadRecord, error)
    Link(ctx context.Context, id string) (graph.Link, error)
    Beads(ctx context.Context, q CollectionQuery) (Page[graph.BeadRecord], error)
    Links(ctx context.Context, q CollectionQuery) (Page[graph.Link], error)
    IncidentLinks(ctx context.Context, bead string, d Direction) (Page[graph.Link], error)
    Types(ctx context.Context) ([]graph.TypeDescriptor, error)
}
```

### The layering, in one picture

```text
bd serve (HTTP/BDP)              generated DTOs; error→Problem mapping
      │
ScopeResolver                    ← OUTER authority seam: picks workspace/store,
      │                            authorization view, and ONE ReadSnapshot
graphops.Scope (per snapshot)    ← the "trait"
      ├─ graphstore              ← v0: S1 tables, the only realization
      ├─ (unionscope + issueproj)← C-lane future, when Issues move into the graph store
      └─ (tests, CLI later)
```

**ReadSnapshot is a first-class port**: one SQL transaction (or UOW unit)
backs all reads a request makes against the graph store. BDP cursors must
continue one logical projected snapshot; per-call `withReadTx` role reads
cannot provide that, so graph reads run their own snapshot-scoped queries.
Cursors bind (snapshot, view). Graph reads never call readiness roles and
never use writable "read-only command" opens (the defer-wake write is
exactly what a Read surface must not trigger). (Union cursors with per-leg
continuation are C-lane machinery, recorded in §5's historical record.)

## 4. Thrust 2 — storage: additive, capability-resolved, no breaking change

> **Superseded mechanisms (2026-09-02, W-arch v2).** The mechanisms this
> section proposes — `GraphCapable` as a separate optional interface, the
> `graphsource` resolvers, the `ReadSnapshot` lease, `backend/` aliases,
> and the `bd serve` optional graph field — are replaced by the house
> idiom in `BDP_GRAPH_ARCHITECTURE.md` §2a (role accessors on
> `storage.Storage`, `DBTX`-shaped shared bodies, per-call transactions
> asserting a store-owned authority witness, BDP rows mounted inside `httpapi`).
> The *rulings* this section serves are unchanged except where §9's
> "Amendments proposed by W-arch" block says otherwise. The text below is
> kept as the record of what was proposed and why.

Not a bare assertion (review Blocker 3). One resolution policy with a
**typed** source — no `any` — and, per round 3, NO `UnwrapStore`:
`UnwrapStore` peels every `Unwrapper` including telemetry, which is exactly
what `bd serve` refuses ("never storage.UnwrapStore" — it performs one
concrete hook peel and keeps telemetry, `cmd/bd/serve.go:671`). Resolution
follows that model — targeted single-layer peels, each named in the result:

```go
package graphsource // internal/storage/graphsource

// graphops.GraphReadSource is what a plumbing stack must yield to serve
// graph reads: the snapshot opener plus the telemetry it must retain.
// (Full contract in "The storage interfaces, concretely" below:
// ErrGraphUnsupported = absence; any other error = operational failure.)
func ResolveGraphReadSource(s storage.Storage) (graphops.GraphReadSource, error)
func ResolveGraphReadSourceFromUOW(p uow.UnitOfWorkProvider) (graphops.GraphReadSource, error)
```

With regression tests mirroring `vc_recompute_test.go` for: hook+telemetry
chains (asserting telemetry RETAINED while exactly the hook layer peels),
the notifying UOW provider, and `bd serve`'s narrow role source.
(The resolver PAIR — `ResolveGraphReadSource` for the store arm,
`ResolveGraphReadSourceFromUOW` for the provider arm — is the name; P1
uses both.)
Realization legs in tree — with the transport distinction the tree
enforces: **`bd serve` refuses embedded Dolt permanently** (its commit
protocol cannot satisfy the server's per-request atomicity contract,
`cmd/bd/serve.go:546`); it serves from server-Dolt/UOW **and registered
backends' store sources** (`serve.go:563`). **Embedded Dolt is a
storage-contract conformance leg, not a `bd serve` transport leg**; serving
BDP from an embedded workspace would need a separately ruled read-only
listener. (SQLite removed from the plan.) **And any registered provider
implementing the graph contract is a first-class leg** — proven by the same
conformance suite the Dolt realization must pass. Accordingly the graph
capability is part of the PUBLIC `backend/` contract from P1: `graphops`
types get public aliases, `GraphCapable` becomes a completeness-guard root,
and the graph suite is a conformance family from the start (ruling 9 flips
the earlier "in-tree-only until opened" hedge).

Graph-store persistence (substrate S1): new `beads`/`links` tables in the normal
migration series, **plus the Type Descriptor store**: descriptors are
persisted rows (not compiled-in Go values), because every Read Scope must
advertise `types/` and mutation authorities must retain the pinned
descriptor contract closure. That means: descriptor bootstrap at graph
initialization, an operator installation mechanism for new Types, closure
validation with fingerprint retention on install, and acceptance coverage
in each phase (P1 persistence + serving, P2 the pinned Type scenarios,
P3 write-time contract validation). Revision minting gated on semantic
change (no-op preserves revision); owned-Link version coupling enforced in
the write transaction.
Where this plan says "ledger" it now means exactly one thing: the GRAPH-STORE
allocation/tombstone table written inside graph write transactions (the
journal-counter pattern the tree already demonstrates). Ruling 3's
condition is a contract obligation: testing whether a canonical ID was
ever allocated is a keyed point lookup — O(1) or O(log n) — never a scan;
the ledger is keyed by canonical URL. The deleted
read-time revision ledger does not return; no projection ledger exists
because no projection exists.

### The storage interfaces, concretely

**The level at which this is defined (ruling 9):** the graph contract lives
at the **normalized storage abstraction** — the `backend/`-level contract —
and any storage provider realizing it is the graph store for the Scopes it
holds. Everything below that names Dolt, `withReadTx`, `NewUOW`, or
`RunTxRead` is the **in-tree reference realization**, not the definition:
other providers (bts-rs's stores, out-of-tree backends) implement the same
contract and pass the same graph conformance suite.

How the graph attaches to the existing storage architecture, member by
member — and what changes where:

**What exists (verified):**

```text
storage.Storage (interface, 28 role accessors)      ← contract; adding a
  IssueLifecycle() / IssueReader() / ... / Memories()   required method BREAKS
      ▲ implemented by                                   out-of-tree stores
*dolt.DoltStore (concrete)
      ▲ wrapped by (each embeds + forwards the interface)
HookFiringStore → telemetry.Storage → ...           ← methods OUTSIDE the
      ▲ or, separately                                   embedded interface
uow.UnitOfWorkProvider (RunTxRead/RunTx)                 are NOT promoted
      ▲ consumed by
cmd/bd/serve role-source table                      ← one concrete hook peel,
                                                      telemetry KEPT, never
                                                      storage.UnwrapStore
```

**What is added (and precisely what is not):**

1. **`storage.Storage` does not change.** No new required method — the
   documented breaking-change policy holds. The graph capability is a
   separate, optional interface:

   ```go
   package graphops
   type Store = GraphReadSource // v0 alias: the read surface IS the
                                // store; write roles widen it in P3

   package storage
   type GraphCapable interface {
       BeadGraph() (graphops.Store, error) // error = operational failure,
   }                                       // never "unsupported"
   ```

   (`graphops` owns `Store`, `GraphReadSource`, `ReadSnapshot`, and
   `Scope`; the UOW adapter satisfies them by constructing a
   `ReadSnapshot` over the transaction a direct `NewUOW` owns, answering
   `graphops.Scope` queries from that one transaction.)

   `*dolt.DoltStore` implements it concretely. Exposure policy, settled:
   **in-tree-only for v0** — out-of-tree implementation arrives only when
   the contract is deliberately opened (claim 6), not by accident of an
   exported interface. "Unsupported" and "broken" stay distinct all the
   way up: resolution returns `(GraphReadSource, error)` with a sentinel
   `ErrGraphUnsupported`; any other error is an operational failure and
   must not be collapsed into absence.

2. **Decorators do not forward it.** Forwarding through every wrapper is
   the failure mode the tree already documents. Instead, resolution does
   what `bd serve` already does — targeted peels, telemetry retained:

   ```go
   package graphsource // internal/storage/graphsource — see placement below

   func ResolveGraphReadSource(s storage.Storage) (graphops.GraphReadSource, error)
   // Peels the known hook layer, then — because telemetry's wrapper
   // embeds the statically typed DoltStorage, so an inner BeadGraph
   // method is NOT promoted through it — inspects THROUGH telemetry,
   // asserts GraphCapable on the inner store, and explicitly re-wraps
   // the returned graph source in the telemetry layer it peeled. Never
   // storage.UnwrapStore. The result names every peeled/rewrapped
   // layer; ErrGraphUnsupported means absence, anything else is failure.
   func ResolveGraphReadSourceFromUOW(p uow.UnitOfWorkProvider) (graphops.GraphReadSource, error)
   // The UOW access path CANNOT ride RunTxRead — it closes its unit of
   // work before returning. OpenSnapshot instead takes ownership of a
   // direct NewUOW; ReadSnapshot.Close performs the rollback/close.
   ```

   Graph reads carry the same telemetry issue reads carry — by explicit
   re-wrap, not by promotion.

3. **The one genuinely new storage primitive: the snapshot lease.**
   Existing read helpers are per-call — `withReadTx` opens and closes a
   transaction inside each role call, which is exactly why they cannot
   serve BDP snapshot semantics. `GraphReadSource` therefore exposes:

   ```go
   package graphops // owns Store, GraphReadSource, ReadSnapshot, Scope

   type GraphReadSource interface {
       OpenSnapshot(ctx context.Context) (ReadSnapshot, error)
   }
   type ReadSnapshot interface {
       Scope                    // unqualified: same package; all reads
       Close(ctx context.Context) error // answer from ONE transaction
   }
   ```

   The RESOLVERS live in a neutral assembly package,
   `internal/storage/graphsource` — a SEPARATE package (not package
   `storage`): it imports `storage`, `uow`, and `graphops`, and nothing
   imports it back except composition roots (`bd serve`, tests). This is
   forced, not stylistic: `uow` already imports `storage`, so placing the
   provider-arm resolver in package `storage` itself would create a
   `storage → uow → storage` cycle. `GraphCapable` stays in `storage`
   (returning `graphops.Store`; `storage → graphops` is a leafward
   import); the resolvers qualify every foreign type.

   A snapshot is request-scoped, and **`ScopeResolver` is its one owner**:
   it selects workspace, authorization view, and opens the snapshot; the
   handler receives and uses it; the resolver closes it when the response
   is written. This is a new *lifetime* discipline, not a new engine
   feature — the same Dolt/SQL transaction machinery `withReadTx` uses,
   held open for the request instead of per call. Two P1 design items
   with tests: pool interaction (a leaked snapshot must not pin a
   connection indefinitely), and **detached, bounded close** — rollback
   must run on a fresh bounded context, never the request context, which
   may already be cancelled (the tree documents that rolling back on a
   cancelled context burns the pinned connection).

4. **Schema machinery: normal series, no special cases.** Graph tables
   (beads, links, type descriptors, allocation/tombstone ledger) are
   ordinary migrations in the existing series, subject to the existing
   version gate (older binary refuses newer DB — §1's contract). No
   changes to the migration framework itself.

5. **`bd serve` gets a separate OPTIONAL graph field, not a role-table
   entry.** The existing role binding table is deliberately mandatory —
   it aborts on any binding error, and the HTTP layer rejects partial
   role sets — so BDP cannot join it as one more ordinary binding.
   Instead: an optional graph source on the server config, populated at
   assembly by the source-appropriate resolver (store arm or UOW provider
   arm), with a conditional route-registration seam. `ErrGraphUnsupported` leaves BDP routes
   unregistered (existing serve behavior exactly as before); an
   operational error still aborts; and capability-present-but-no-Scope-
   yet is a THIRD state with its own explicit representation — per
   ruling 12, `bd serve` mints the Scope on first serve under a configured
   URL and then serves it honestly empty; without a configured URL there
   are no BDP routes — never conflated with capability absence. The optional field is populated via the source-appropriate
   resolver (`ResolveGraphReadSource` for the store arm,
   `ResolveGraphReadSourceFromUOW` for the provider arm — `serve.go`
   assembles from both).

6. **`backend/` (all providers): public from P1, precise about the
   existing machinery.** Today `RunAll` never exercises the optional
   capability families — `RunUnsupportedContract` proves their typed
   refusals instead. The graph contract arrives as its own suite beside
   them (the P1 graph-storage conformance suite) with a refusal contract
   for non-capable stores; `GraphCapable` is a completeness-guard ROOT and
   `graphops` types carry public aliases from P1, because providers other
   than Dolt are first-class targets (ruling 9), not a later opening.

7. **`issueops`, the journal, sync, and every legacy role: untouched.**
   The graph store is a sibling under the same DoltStore, not a layer
   over the issue roles — with the projection withdrawn, nothing in the
   graph path calls them at all.

### Lifecycle commands (ruling 12)

> **Spelling amended by W-arch (pending ruling A2/A3/A6):** `bd
> bdp-serve` → `bd bdp serve` (the strict, minting command over the same
> `httpapi` server; `bd serve` mounts the BDP rows only when it holds an
> already-minted Scope); graph verbs under `bd bdp …`; `bdp.scope_url` lives in
> tracked `config.yaml`, the per-workspace `bdp.client`/`bdp.server` in
> untracked `config.local.yaml` (nothing in `metadata.json`). Detail:
> `BDP_GRAPH_CLI_AND_STORAGE_SPEC.md` Part A.

Three commands, three responsibilities — the store, the Scope, and the
client:

1. **`bd init` initializes the graph store** alongside everything it
   initializes today: graph tables, the allocation/tombstone ledger, and the
   Type Descriptor bootstrap, all against the normalized storage interfaces
   (any provider). No separate `bd graph init`. A workspace therefore always
   has a graph store; it does not yet have a *Scope*.
2. **`bd bdp serve` creates the BDP Scope on top of the store; `bd serve`
   serves an already-minted Scope it holds** (as amended by A2/A7,
   pending): on its first serve under a configured `bdp.scope_url` (ruling
   7a) `bd bdp serve` mints the Scope row, the `mint` ledger event, and the
   built-in Type catalog in one **multi-phase, fenced** transaction (a shared
   database: the dolt-ignored authority lease with its fence cell; a
   configured remote — *deferred under A9*: fetch → ancestor check → scoped
   commit → push), finalizes this workspace's
   authority witness, and serves the Scope — honestly empty at birth, with
   `beads/`, `links/`, and `types/` all present. Because the Scope URL is a
   tracked project fact, **only `bd bdp serve` mints**: a plain `bd serve` on
   an unminted store keeps the legacy surface up with a notice. BDP routes
   are a conditional second table inside `internal/httpapi` behind the same
   middleware, in v0 served only from SQL-server workspaces — the
   unit-of-work leg (`bd serve` refuses embedded Dolt permanently; a
   registered backend's store arm has no fence to offer, so its rows are
   absent until it declares one). The mint runs as the spec's one staged startup sequence (shared read
   → release → exclusive only when there is no Scope row → release →
   shared serve). `bd serve` with no configured URL is
   byte-identical to today; on a workspace that does not hold the authority
   it keeps the legacy surface up with the BDP rows absent and a notice —
   never a startup refusal on account of the graph. `bd bdp serve` refuses
   (exit 2) in those cases. No development-mode URL derivation exists in
   bd. (W2 decides whether `bd bdp serve` survives as the alias — default
   yes; it is the minting path.)
3. **Client wiring — `bd init --bdp-server <url>` and `bd bdp client`**
   (as amended by A6, pending): one more `bd init` target, beside
   `--server`, `--shared-server`, `--proxied-server`, `--team-server`, and
   `--backend`, distinguished by rerouting ABOVE the normalized storage
   abstraction (at the CLI): the `bd bdp` read verbs become a BDP client of
   the designated server. The per-workspace keys live in the untracked
   `config.local.yaml`; the project fact lives in tracked `config.yaml`:

   ```yaml
   # config.yaml (tracked)
   bdp:
     scope_url: https://beads.example/acme/   # what the authority mints/serves (7a)
   # config.local.yaml (untracked, machine-specific; merged over config.yaml)
   bdp:
     client: server                           # store | server (default store)
     server: https://beads.example/acme/      # graph-verb target when client: server
   ```

   `bd init --bdp-server <url>` and `bd bdp client server --server <url>`
   write `config.local.yaml`; generic `bd config set` refuses the
   per-workspace keys with that guidance. Env: `BDP_SCOPE_URL` (7a) and
   `BD_BDP_SCOPE_URL`; `BD_BDP_SERVER`; **`bdp.client` is blocked from env**
   like `backend`. The bearer token comes from a file only —
   `BEADS_BDP_TOKEN_FILE` or a credentials-file section keyed by origin and
   Scope path — never from an environment variable and never from a config
   key. Precedence: env (where permitted) > `config.local.yaml` >
   `config.yaml`; `metadata.json` carries nothing for the graph. `client`
   is an explicit mode, never inferred from the presence of a URL. Issue
   verbs are untouched. v0 routes graph verbs only.

### Replication participation matrix (review High 4, corrected round 2)

Each row is policy decided in P-1, not discovered in CI. "Byte-identical
legacy behavior" scopes to **legacy-only data and operations**; rows are
split by topology where the tree differs:

| Surface | Legacy behavior (verified) | Graph-store policy (proposed) |
| --- | --- | --- |
| Dolt push/pull (server + embedded) | rows travel; embedded pushes directly | graph rows travel identically |
| Merge settlement | `versioncontrolops/mergesettle.go` already settles metadata, dependencies, migrations, config, issues, labels, comments, and events, with seven-table FK-cascade repair — conflict dispatch is a hard-coded switch, separate from an always-considered FK-repair pass; NOTE `MergeWithStrategy` returns early on clean merges and plain `Merge` bypasses settlement entirely | graph settlement must be **centralized so every merge entry point runs it** (clean-merge early-returns and plain `Merge` included — enumerate or funnel the routes): identity/endpoint integrity, dangling-Link detection, owned-Link invariant validation. A pass can reject or quarantine invalid imported state; it CANNOT serialize two independently accepted `max`-violating writes after the fact — hence decision 9: BDP writes flow through one serving authority per Scope, and foreign-clone merges of graph tables are out-of-contract (quarantined on detection) |
| Federation type-filtering | **server-topology-specific**; deletes `issues` rows by type | graph tables get their own filter hook per topology; filtering one endpoint must also drop/deny the Link (never emit a dangling edge) |
| Journal (frozen v0 vocabulary) | Issue/Dependency/Comment payloads only | **graph events are excluded**; a separate graph changefeed carries them; the frozen vocabulary is not extended |
| Export/JSONL (contract class) | contractual shapes | graph gets its own export lane; legacy shapes untouched |
| Backup / restore | whole-database state (a different contract class from export); a Dolt backup restore carries the working set, dolt-ignored tables included (probed) | ruling 11 as amended by A5 (pending): the installation-keyed authority witness (`.beads/graph-authority.local.json`) records the hash-chained ledger head; a restore keeps the file but the store no longer contains that head → refused until `bd bdp restore`, which shows continuity from a `bd bdp ledger snapshot` (recovery predicate) or rotates the Scope URL and epoch; providers DECLARE `LedgerDurability`; `bd backup restore` also marks the witness unverified |
| Wisps | private/transient; excluded from export/federation by default | **P-1 policy decision** — excluded from BDP serving in v0 (proposed) |

## 5. Thrust 3 — Issues/Dependencies beside the graph

### The seam decision (HISTORICAL — C-lane record; superseded for v0 by
the withdrawal below)

What follows is preserved as design input for the C lane, not v0 scope.
Option A (projection port + union composite) over B (peer interfaces on the
structs), C (storage unification now), D (call-site switches) — with the
review's correction adopted: the union is the *representation* seam only,
subordinate to the outer authority seam, snapshot-scoped, and:

- **Duplicate full Resource ID across legs is an integrity error, never
  precedence.** The v1 "native first, then legacy" shadowing is withdrawn.
- **Namespace AND ledger — they answer different laws (round-2
  correction):** a reserved graph-store namespace prevents *collisions*; it does
  nothing for *lifetime identity non-reuse* (a deleted projected Issue ID
  must never be reassigned — BDP's no-URL-reassignment law survives
  deletion and epoch changes). So: namespace disjointness for allocation,
  PLUS a durable allocation/tombstone guarantee behind every exposed URL,
  graph-store and projected — covering legacy import's same-ID UPSERT and
  Dependency delete/recreate reuse. And an **eligibility policy for legacy
  IDs** (P-1): an issue ID that is not a canonical BDP path segment is
  omitted, mapped to a stable surrogate, or fails Scope projection — ruled,
  not improvised (current validation checks prefix shape, not BDP grammar).
- **Cross-realization Links** (C-lane decision when projection returns):
  if a graph Link may target a projected Issue, every legacy deletion
  needs a graph coordinator hook (else dangling edges); if forbidden, the
  Type constraints must say so. Moot in v0 — nothing legacy is served.
- Multi-repo routing stays where it is — `ScopeResolver` wraps the existing
  owning-store resolution; the union never spans stores.

### The substrate decision (SETTLED for v0: S1)

S1 (new tables) is the v0 substrate — with the projection withdrawn there
is nothing for a chassis substrate to buy: S2's free-rider argument was
chassis sync for *legacy interop*, and v0 has none. The historical
scorecard stands as C-lane input: S2 fails conformance on three laws
(`depid` admits one edge per endpoint pair — no type, no multiplicity;
Dependencies carry no revision; dependency edits don't version the
source); S2-lite was the fallback only while a projection existed.

### The Issue projection is withdrawn from v0 (round-5 conclusion)

The conflict inventory in §2b is the full map; rounds 3–5 tested every
read-side fidelity mechanism against it, and each fell to tree
counterexamples:

- **State tuples** (r3): label add→remove recreates the tuple; direct text
  restoration moves only second-granularity `updated_at`.
- **Read-time witness ledgers** (r4): witness reads, not transitions —
  legacy A→B→A between reads serves the old revision.
- **Complete-representation state hashes** (r5): `updated_at` is
  second-precision `DATETIME` and the tree documents same-second ties, so
  A→B→A within one second reuses the hash; `bd import --allow-stale`
  deliberately restores old rows including `updated_at`; `bd sql` permits
  arbitrary direct writes no witness can enumerate; merge resolution and
  backup restore reinstate historical rows.
- **Birth-identity URLs** (r5): same-second recreation collides; import
  accepts caller-supplied historical `created_at`; import-over-existing
  does not converge `created_at` across replicas; rename A→B→A reactivates
  the original URL; database restore resurrects old tuples. BDP's
  never-reassign law cannot be met.
- **Type immutability** (r5): legacy `issue_type` is ordinarily mutable;
  BDP declared Types are immutable.

Mutation-time witnessing — the only remaining mechanism — would require
instrumenting every legacy write path including `bd sql`, which is
arbitrary SQL and cannot be completely instrumented even in principle.
The conclusion is structural, not incremental: **a store that permits
timestamp ties, stale restores, arbitrary SQL, and identity resurrection
cannot be projected into BDP's revision and identity laws by any read-side
mechanism.** So v0 withholds the projection:

- The v0 BDP Scope serves **graph beads and links only**.
- Issues keep their existing surfaces (CLI, REST v0, JSONL) untouched.
- Issues join the graph when storage unification (Option C) moves them
  into the graph store — where operation-local revisions and durable
  identity are properties of the write path, not reconstructions. The
  union composite and this section's counterexample record are the design
  input for that future lane.
- The `unionscope`/`issueproj` machinery drops out of v0 scope; the
  authority seam (`ScopeResolver`: workspace, view, one ReadSnapshot) and
  `GraphReadSource` remain — they serve the graph store.

### The C lane: paths to Issues/Dependencies on the graph (informative)

Operator direction (2026-09-02): the withdrawal stands for v0, and the C
lane should be sketched now. The round-3–5 counterexamples fix the shape of
the solution space: **uniform versioning requires that every mutation
either funnels through one write path or is completely observed at the
storage layer.** Read-side reconstruction is proven impossible. That yields
three paths:

- **C1 — Funnel with a legacy compat shim** (the operator's sketch): Issues
  and Dependencies become graph beads/links; the legacy surfaces (CLI
  verbs, REST v0, JSONL, journal, sync) are reimplemented as a compat
  adapter OVER the graph store, reproducing legacy behavior byte-for-byte.
  Versioning is uniform because every mutation goes through the graph store's
  write path. The crux is what "keep the current code path" means at the
  storage layer: if legacy code keeps writing legacy TABLES, the bypasses
  persist and uniformity fails; so C1 means legacy *behavior* preserved
  over graph *storage* — and the hard cases are exactly the round-5
  killers re-specified deliberately: `bd sql` (verification task: whether
  Dolt supports legacy-compatible updatable views that can route DML
  through graph revision/tombstone semantics — else direct SQL is
  re-scoped), import `--allow-stale` (becomes a versioned operation),
  backup/restore (restores history, not just state; decision 11). The
  wisp precedent (plane routing at storage, surfaces unchanged) is the
  house style for this move.
- **C2 — Observe: a complete mutation feed at the storage layer**: legacy
  tables remain the record for legacy surfaces; a storage-level observer —
  DB triggers on the legacy tables feeding a transition log in the same
  transaction — would give the graph per-operation revisions and
  tombstones without touching legacy code paths, and would be the only
  observation variant that survives round 5 IF the following verification
  tasks all pass (none is established fact): (a) Dolt trigger availability
  and transactional semantics; (b) whether direct `bd sql` DML actually
  fires them (noting `bd sql` is unavailable in embedded mode and runs via
  direct SQL-server or proxied-server paths — and UOW is an access path,
  not a third storage engine); (c) trigger-row behavior under replication,
  merge, and restore; (d) Scope URL/epoch handling after restore
  (decision 11).
- **C3 — Cutover**: one-time migration, graph store becomes the only
  store, legacy tables dropped or frozen read-only. Maximum uniformity,
  no dual bookkeeping, maximum one-shot risk; `bd sql` compat ends or
  becomes views. Realistic only after C1's compat adapter exists and has
  soaked — C3 is C1 minus the legacy tables, not an alternative to it.

Sequencing implication: C1 and C3 share the compat-adapter investment; C2
is the only path that leaves legacy storage untouched. A future C-lane
ruling chooses funnel (C1→C3) vs observe (C2) — and none of it blocks or
changes v0.

## 6. Addressing

One workspace = one Scope. Scope URL scheme is a P-1 decision (config key
vs derived). Graph-bead IDs mint under BDP creation-time rules (supplied
multi-segment or generated flat, reject-don't-trim) against the graph-store
allocation/tombstone ledger. No legacy IDs are served in v0.

## 7. Phasing (re-sequenced per review; each phase exits green)

> **Names in this section are superseded** (`GraphCapable`,
> `GraphReadSource`, `graphsource`, `bd bdp-serve`, `bd bead`/`bd link`
> verbs): read them through `BDP_GRAPH_ARCHITECTURE.md` §2. Phase
> *boundaries* stand: P0 contracts + pinned wire, P1 storage (roles,
> bodies, migrations, conformance; the replication/merge ADR is a P1
> gate), P2 serving (BDP rows inside `httpapi`; collection routes after the
> cursor ADR), P3 writes. **P0 is blocked until A1–A9 and the two decisions are ruled.**

- **P-1 — Decisions and pins (no code):** charter ADR; ratify the
  projection withdrawal (v0 Scope = graph store only); Scope URL/identity;
  graph-store allocation/tombstone design; serving authority; replication
  matrix rows; auth-view mapping for bearer-token reality;
  listener/authority-semantics choice. (The BDP pin is already written in
  §0.) *Exit: every row ruled by Donna, recorded in this doc.*
- **P0 — Contracts:** generated wire DTOs from the pinned schema; immutable
  domain values (`Properties`, `Ref` sum, records); pure validators; typed
  error vocabulary. *Exit: model laws 100% table-tested; DTO round-trip
  against pinned schema fixtures.*
- **P1 — Graph read storage (S1):** tables + migrations (descriptor
  store included); typed snapshot-source resolution (`GraphReadSource`)
  with single-request snapshot consistency and the zero-legacy-writes
  regression (defer-wake); the resolver pair across the storage legs —
  `ResolveGraphReadSource` for server/embedded Dolt (embedded as
  storage-contract leg), `ResolveGraphReadSourceFromUOW` for the UOW
  access path — with decorator regression tests; an internal, non-BDP
  bootstrap/fixture write API (the only writer until P3) enforcing the
  allocation/tombstone ledger; replication-matrix gates. *Exit: a NEW
  graph-storage conformance suite — defined in this phase under
  `backend/conformance`, enumerating the storage contract (snapshot
  isolation, ordering, ledger enforcement, descriptor persistence) —
  green on all legs (descriptor persistence AND inventory serving
  included); cross-request cursor stability is explicitly NOT a P1
  claim.*
- **P2 — Protocol Read over the graph store:** snapshot-bound cursors —
  including the **cross-request continuation mechanism** BDP requires
  (later requests continue the same selected set, projection, and
  revisions): a durable snapshot registry, materialized result sets, or
  Dolt `AS OF` identity surviving through cursor expiry — chosen by ADR in
  this phase; BDP handler through the existing middleware path
  (auth/project/deadline semantics preserved); run the **pinned** external
  BDP Read matrix as a target. *Exit: the pinned matrix green with its
  own provenance split honored — packaged rows proven at the packaged
  public boundary, self-certified in-process rows via the in-process lane
  and labeled as such (the pinned artifact is explicit that they are not
  black-box conformance) — plus a beads-owned public-boundary
  cursor-stability test across requests.*
- **P3 — Writes and CLI, gated on its own ADR AND on upstream spec
  artifacts:** the pinned write-profile envelope (an owned-Link mutation's
  result must also report the source Bead's resulting revision), the
  owned-Link Event delta, AND the later-profile artifacts BDP itself marks
  pending — the sequence/idempotency envelope schemas, problem rows, and
  conformance artifacts for the write profiles. Profiles are **Scope-wide**
  (uniformity law), and the Event-delta gate binds exactly the profile
  that has Events: a Scope containing owning Types cannot advertise the
  **Transactional** profile until the owned-Link Event delta exists —
  Read+Update has no Events and is not blocked by it — and this is a
  Scope-level gate, not a per-Type advertisement. Write tests require: create/property-update
  mint fresh Link AND source revisions; **deletion mints nothing for the
  deleted Link** — its result reports the deleted identity plus the
  source's fresh revision (the final live revision is Transactional
  `DeletedData` Event material, not a result member); target revision
  unchanged throughout; both surviving revisions preserved on semantic
  no-op. Then tombstones,
  endpoint constraints, replication of writes; only then
  `bd bead`/`bd link` verbs. *Exit: the pinned write-profile conformance
  artifacts green when they exist upstream, plus beads-owned transaction,
  identity/non-reuse, deletion-result, installed-Type-contract
  validation, and replication tests at the public boundary.*

## 8. Related workstreams (operator direction 2026-09-02)

This plan covers the graph store and its Read serving. Sibling workstreams,
each owning its own writeup:

- **W-arch** — held at v10 (2026-09-03): `BDP_GRAPH_ARCHITECTURE.md` and
  `BDP_GRAPH_CLI_AND_STORAGE_SPEC.md`, eight council rounds with live Dolt
  probes; nine ruling amendments (A1–A9) and two new decisions pending in
  §9. Precedes P0 code.
- **W1** — flesh out the **Update and Transactional profiles** of BDP and
  the reference implementations (the protocol is Read-heavy today); this is
  the upstream gate for P3 writes.
- **W2** — `bd bdp serve` / `bd serve` integration (as amended by A2,
  pending: one server, a conditional BDP route table): decide whether the
  strict alias survives, whether BDP rows contribute a capability token,
  and whether the current HTTP surface moves; nothing to fold in.
- **W3** — **inventory of bead types and generation of Bead/Link Types** —
  in the beads repo, not bdp; feeds the descriptor store's bootstrap
  catalog (§4).

## 8a. Process

All work on `feat/bead-graph`; slices land by PR with adversarial
convergence; the feature branch merges to main only at phase exits behind a
differential gate proving same-version legacy behavior unchanged. Spec
changes go to gastownhall/bdp first — this plan's §0 pin is the enforcement
of that.

## 9. Decisions requested from Donna (the P-1 list)

1. **Charter — RULED (2026-09-02): core.** The charter amendment FOLLOWS
   working bits (the maintainers' own precedent: the charter file changes
   when implementation lands). Its scope: *the bead graph expands to
   include non-work-tracking information* (wisps being the existing
   example) — a generalization of what beads already is, not a second
   product surface.
2. **Substrate — RULED: S1** (graph tables: beads, links, Type
   Descriptors, allocation/tombstone ledger, authority marker).
3. **Allocation/tombstone ledger — RULED:** append-only allocation record
   per committed canonical ID (URL, birth authority id, epoch,
   tombstoned-at), consulted before every create and by restore;
   condition accepted as a contract obligation — the ID test is a keyed
   point lookup, O(1)/O(log n), never a scan. (Namespace-vs-issue-grammar
   and legacy-ID eligibility move to the C lane with the projection.)
4. *(Moved to the C lane — cross-realization Links cannot arise in v0;
   see §5's historical record.)*
5. **Projection withdrawal — RULED: ratified.** The v0 Scope serves graph
   beads and links only; Issues, Dependencies, and wisps keep their
   existing surfaces and join via the C lane (wisps inherit every Issue
   counterexample — they are plane-routed Issues).
6. **Wisps — RULED:** moot for v0 serving; recorded as a C-lane
   *visibility* decision (wisps are private/transient by default today, so
   their eventual graph entry needs a visibility ruling, not just plane
   routing).
7a. **Scope URL scheme — RULED:** mirror BDP's pinned startup contract.
   An explicit `bdp.scope_url` (`BDP_SCOPE_URL`) is required to serve a
   real Scope; a `local-test` URL derived from the listener is permitted
   only under an explicit development mode and is never persisted as
   identity; the URL is persisted in the graph store beside the authority
   marker; never derived from a git remote or workspace path; one Scope
   per workspace, path-distinguished under one host.
7b. **Listener — RULED:** same listener, same table-driven middleware
   path; BDP routes register conditionally (ruling 12) inside the existing
   table, so bearer-auth and project-identity semantics are preserved by
   construction. v0 authorization-view mapping: one view per bearer
   token = the whole Scope (no hidden Resources) — honest and conformant
   until real views exist; federation/multi-view later changes the
   mapping, not the listener.
8. **Journal/changefeed — RULED:** frozen v0 journal untouched; the graph
   store gets its own changefeed as a contract capability arriving with
   P3 writes (providers with a native event log realize it over that).
9. **Serving authority — RULED (corrected model):** the authority is the
   graph store *as reached through the normalized storage abstraction*,
   whichever provider realizes it — not `bd serve`, and not Dolt. The
   authority marker (Scope URL + authority id, minted by `bd serve` on
   first serve under a configured URL — ruling 12),
   single-serialized history, non-authority refusal (of graph writes AND
   BDP serving for that URL), and the snapshot lease are graph-CONTRACT
   obligations proven by the graph conformance suite; the CLI graph verbs
   and the BDP handler are both clients of that abstraction, so they are
   one authority on any provider. Dolt is the in-tree reference
   realization. Promotion is explicit and epoch-rotating. Consequence: the
   graph is single-authority while Issues stay multi-clone-mergeable —
   graph writes on a non-authority instance refuse with a typed error.
   Replica *reads* from a non-authority instance are deferred until BDP
   defines replica labeling (candidate note for gastownhall/beads#6051).
10. *(Absorbed into decision 5 — the round-5 review closed this fork:
   withholding is the only conformant option.)*
11. **Restore vs identity — RULED: both, layered.** The ledger is
   append-only and restorable independently of state (older state +
   current ledger preserves non-reuse); providers declare whether their
   ledger survives restore; when preservation cannot be guaranteed,
   `bd bdp restore` (spelling per A3, pending) rotates the Scope URL and
   epoch and refuses the old URL. An epoch change alone is never
   sufficient.
12. **Store, Scope, client — RULED (replaces "empty-at-birth"):** three
   commands, three responsibilities (§4 "Lifecycle commands"). `bd init`
   initializes the graph store with everything else, against the
   interfaces — no separate graph init. `bd serve` creates the Scope on
   top of the store on first serve under a configured URL (minting the
   marker) and serves it honestly empty; no URL → no BDP routes. A
   client-wiring is `bd init --bdp-server <url>` — one more `bd init`
   target, distinguished by rerouting ABOVE the storage abstraction (at
   the CLI) rather than below it (§4 "Lifecycle commands"); after it, the
   CLI's graph verbs speak BDP to the designated server. Tests prove Issues never leak into graph inventories;
   a provider without the capability keeps existing `bd serve` behavior —
   routes absent, never a startup failure.

### Amendments proposed by W-arch v10 (2026-09-03) — PENDING RULING

Raised by eight three-reviewer councils on the W-arch docs (the lease fence and
the ledger counter are probe-confirmed on Dolt 2.1.8); each changes
ratified text above, so none takes effect until ruled. Full rationale and
evidence: `BDP_GRAPH_ARCHITECTURE.md` §2b.

- **A1 (ruling 9).** Replace "the snapshot lease" with "single-transaction
  operations under a store-asserted authority witness": the accessor loads
  this workspace's witness (a clone-local file) and the body asserts it
  *inside its transaction* — Scope row identity, hash-chained ledger head
  (exact prefix), the fencing cell (a mutation must UPDATE it and see one
  affected row; a protected read first self-regrants ephemerally and then
  requires an unexpired lease inside its transaction), and the graph-state
  version (per-table hashes). **No
  request type carries authority fields.** The cursor type is opaque from
  P1.
- **A2 (rulings 7b, 12).** BDP routes are a conditional second table inside
  `internal/httpapi` behind the same middleware, always served from the
  unit-of-work leg. **Only `bd bdp serve` mints** (the Scope URL is a
  tracked project fact); it inherits serve's whole-surface `--readonly`
  refusal and refuses without a held Scope. `bd serve` mounts the rows when
  it holds an already-minted Scope, converts every graph failure into "rows
  absent + notice", and never refuses on account of the graph.
- **A3 (ruling 12 / §4 lifecycle).** All graph verbs under `bd bdp …`; the
  root command's policy for that subtree is keyed by `CommandPath()` and
  authoritative at every leaf-name call site (paired Cobra-walk and
  source-scan tests).
- **A4 (§3 layering).** Values, laws, and roles in public `graphops`;
  accessors named `BeadGraph*`; no `backend/` aliases.
- **A5 (ruling 11).** The clone-local half is `.beads/graph-authority.local.json`,
  bound to an installation key (a per-installation id under the user config
  dir plus the canonical path — never the hostname or the shared project
  id), written under a bounded exclusive lock with monotone
  read-modify-write and directory fsync, with **multi-phase transitions**
  (mint, promote, rotate, ledger apply) carrying a durable operation id and
  recovered on the next load by evidence (the ledger, then the remote), with
  a descendant-aware witness advance; the
  manager ensures the ignore entries and refuses a git-tracked path. The
  ledger is an append-only, hash-chained event table with a single-row
  sequence counter (Dolt's `FOR UPDATE` is a no-op); the witness records
  the head `{seq, hash}`; **no event exists before mint** (the built-in
  catalog is installed inside the Mint transaction). `bd bdp ledger
  snapshot|apply` carries manifest-bound ranges under a recovery predicate
  exempt from the head check; providers declare `LedgerDurability`;
  `bd bdp restore` rotates unless continuity is shown. Residuals (stated):
  whole-directory filesystem snapshots; acknowledged-but-unwitnessed writes
  before a crash (P3 obligation).
- **A6 (§4 lifecycle, 7a env).** `bdp.scope_url` is a project fact in
  tracked `config.yaml` (yaml-only; `BDP_SCOPE_URL` read first, then
  `BD_BDP_SCOPE_URL`; `bd config set` refuses it once minted — the URL then
  changes only through `bd bdp promote --rotate-url` / `bd bdp restore`,
  which update the Scope row and the file as one transition); the
  per-workspace `bdp.client`, `bdp.server`, `bdp.insecure_http` live in
  untracked `config.local.yaml`, written by `bd init --bdp-server` and
  `bd bdp client` (generic `bd config set` refuses them); the three new
  `.beads/` files join all three doctor ignore lists. No env-carried token
  and no token key in config; `bdp.client` blocked from env; nothing in
  `metadata.json`.
- **A7 (ruling 9, promotion) — NEW.** Fences compose by hazard and every
  replicated graph mutation is fenced inside its transaction: **a shared
  database** (every SQL-server topology — the only serving topology in v0)
  → a dolt-ignored `graph_authority_lease` row (the `leases`/bd-lrgn1
  precedent) that a mutation must UPDATE — holder installation key, epoch,
  and the `fence` cell it read in the predicate (no expiry term for the
  holder's own writes, which is what lets an expired lease naming this
  workspace self-regrant; a foreign holder is replaced only by `--steal`)
  — rewriting `fence` with a fresh random value and extending `expires_at`, because Dolt
  merges transactions cell by cell (probed): only a same-cell-different-
  value write is a serialization loser, so every lease write collides on
  that one cell — probe-confirmed; a protected read requires an unexpired
  lease inside its transaction and never writes it (a CLI read on its own
  expired lease regrants once, ephemerally, first); the ledger counter writes a random allocation nonce for
  the same reason (a bare increment converges); **a configured remote** → every replicated mutation (mint,
  promote, rotate, install, ledger apply, P3 writes) runs through one
  provider primitive: `DOLT_FETCH` → remote-tracking HEAD must be an
  ancestor of local HEAD → the fenced transaction → a scoped commit
  (`DOLT_ADD` graph tables; a new `RunTxScopedResult`, since the UOW commit
  hardcodes `-Am`) → `DOLT_PUSH` with a typed lift of the tree's
  the whole of `isPushRaceErr`; on a race the remote-tracking ref's ledger
  head and the graph tables' diff are compared (never the `(authority_id,
  epoch)` tuple alone, which a same-witness twin shares) — any graph delta
  fails closed and is undone (soft reset, per-table unstage + checkout, and
  a compensating lease restore when HEAD is still the operation commit;
  `DOLT_REVERT` otherwise; never a hard reset), issue-plane-only divergence
  keeps the commit as "sync required"; other failures keep the commit as
  unpublished and retry. Hazard-R reads require a fresh remote observation
  compared against the workspace's witness; the serving watcher is a
  `held → renewing → lost` state machine that disables the BDP rows
  atomically. Both hazards → both fences. **A lease row is not proof of "minted
  here"** (`DOLT_BACKUP` restore and a directory copy carry the working set;
  `@@server_uuid` is per machine — probed), so in-place promotion is either
  a **self-regrant** by the workspace the lease names or an operator
  **`--steal`** (an assertion of "same database", in the class of
  force-push); a foreign holder's expiry alone never grants a takeover;
  `--rotate-url` is the bootstrap for clones, copies, and restores; a
  physical database copy is the stated operator-managed hazard. `promote`
  and `rotate` take the shared workspace gate and rely on the lease, so they
  run beside a live server; `types install`, `restore`, and `ledger apply`
  take the exclusive gate. Force-push routes bypass the
  fence as operator acts.
- **A8 (§1 constraint #1; ruling 12) — NEW, two options.** **A
  (recommended):** constraint #1 scoped to *behavior* (byte-identical gate
  output). Six required methods on `Storage` break **direct implementers**
  (the compiler; six `ErrUnsupported` stubs; the joint
  `ReadyClaimer`/`BatchCloser` CHANGELOG entry is the precedent) and are
  **silently promoted through every wrapper that embeds the interface** —
  which is why the censuses are mandatory. **B:** an optional
  `BeadGraphCapable` interface is **not** promoted through an
  interface-embedding wrapper, so every wrapper implements it explicitly
  and every consumer needs a resolver (the v1 `graphsource` shape).
- **A9 (ruling 9) — NEW, optional simplification, RECOMMENDED for v0.**
  **v0 authority requires a shared database.** Hazard R (the remote
  publication primitive, remote-read freshness, multi-phase publication
  recovery) is deferred to the write-profile ADR; a remote-backed workspace
  is a non-authority for any Scope it did not mint on its own shared
  database (its `bd serve` shows rows absent; its CLI reads refuse);
  cross-database promotion is `--rotate-url` only (a new Scope; continuity
  via the ledger lane). The graph tables still replicate through push/pull;
  the validator refuses foreign deltas. What remains of A7 is the lease
  alone — one arbiter, one lease, one counter — and the only serving
  topology already is the SQL-server workspace. Cut sheet if ruled — the topology matrix: a shared database that minted
  locally is authoritative regardless of any configured remote; one that
  received the Scope row by replication, restore, or copy without a valid
  local authority refuses (`--rotate-url` or `--steal`); the embedded and
  registered-store arms refuse every local authority operation (the embedded
  leg exists only as the `bdp.client: server` host; client-mode reads stay
  allowed); a remote neither grants nor removes authority; every hazard-R
  passage is excluded, while the default-branch rule for the ignored lease
  table stays.

Two decisions the plan does not yet contain, surfaced for ruling:

- **Out-of-role DML enforcement boundary** (`bd sql`, raw SQL, merges
  bypass allocation/authority/revision/owned-Link laws). Proposed v0
  posture: out of contract + a state-change validator that runs whenever
  the store's state version changes and refuses invalid or
  foreign-authority graph state (rows carry `last_authority_id`/`last_epoch`
  provenance); DB-privilege/trigger enforcement is a C-lane verification
  task. To be ruled before P3.
- **Replication/merge ADR** as a P1 gate: the merge entry points include
  every `DOLT_PULL`/`DOLT_MERGE` route (pull, UOW remote use case, embedded
  federation sync, the remote-migrate gate), not four Go functions; prefer
  refusing foreign-authority deltas; federation unfiltered in v0 by
  decision. Lands before the graph migrations.
