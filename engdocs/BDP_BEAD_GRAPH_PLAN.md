# BDP in beads: the bead-graph plan

**Status:** Draft v27 — feat/bead-graph — W-arch amendments **A1–A9 and rulings 13–14 RULED 2026-09-07; A10 RULED 2026-09-08**; every P-1 decision is ruled; **P0 is open**. (Thirteen adversarial review rounds:
1–7 on the whole plan, SOUND at round 7; 8–13 on the storage-interfaces
section, SOUND-ADDITION at round 13; v6 withdrew the Issue projection from
v0 on review-round-5 counterexamples; v9–v11 record the P-1 ruling tranches — all
twelve decisions are ruled; v14–v21 reopened P-1 for the nine amendments; v22–v23 record all nine ruled; v24–v25 record rulings 13–14; v26 records the 2026-09-09 source alignment and council gate/traceability corrections; v27 carries the 2026-09-10 formal-review corrections and dependency refresh; P0 current-wire completion remains open)
**Date:** 2026-09-10 (v26: 2026-09-09; v1: 2026-08-31; v25: 2026-09-02)
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

The original P0 wire input targeted the BDP spec **as of the owned-Links rulings**:
**BDP commit `0b7d86e7`** (the gastownhall/bdp PR #18 merge, 2026-09-07, carried attribution for #10; supersedes the `aee075f5` pin of PR #17),
schema bundle `schemas/bdp-v0.schema.json` at that commit, Read conformance
matrix `packages/conformance/matrices/read-v1.json` at that commit
(40 scenarios: 35 normative + 5 diagnostic, all profile `read` — counted at the pin
during P0 vendoring; earlier drafts said 38).
**No implementation phase begins until the pin is written here.** BDP remains
a draft; "matrix green" exits below mean green against the *pinned* matrix,
re-pinned deliberately, never against a moving `main`.

Model laws this plan builds to (the baseline pin plus the dated #19/#20
Mutation results correction, pinned in §0a):

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
  identity **including its final live revision**, in the shared
  `deletedIdentity` schema, carried as the result's `deleted` member
  (`{ resourceKind, resource: { id, type, revision } }`; #19 Mutation results
  in §0a, shared with #20); an owned-Link deletion result
  additionally reports the owning source's fresh revision.
- References: the URI is identity; a pin is provenance, echoed byte-identical,
  equality-only, never validated in v0. In-Scope and external references have
  different canonicalization duties — the Go model captures them as a sum,
  not a naked pair.
- Authorization views are closed projections, closed over owned Links.
- The Read problem table is closed vocabulary (including `resource-pruned`
  and `resource-erased` — merged in gastownhall/bdp#16).

## 0a. Current BDP and versioned-beads alignment (2026-09-09)

This dated alignment updates the historical survey and P3 dependencies without
changing the ruled v0 scope: **graph Beads/Links only; no Issue projection**.
The §0 Read pin remains the historical P0 wire input, not a claim that it
contains subsequent rulings. Donna's 2026-09-09 continuation authorizes
materialization on the condition that this design and P0 track both evolving
BDP and Jim's versioned-beads work, as recorded in the [operator continuation](https://github.com/donnabox/agent-coordination/blob/4fd57836ae052dec41b66493b88954bdabbb4d0f/context/janet/beads-workstream-state.md). The following are read/verified source
pins; the continuation and History ACK below are operator records, not
implementation pins. These dependency records do not authorize implementation
against moving drafts or treat draft PRs as merged dependencies. Before P3
begins, its owner records a reviewed pin for the selected write profile in §0:
spec, bundle, problem rows, catalog, illustrative fixtures and executable matrix
(or its explicit pending status). The claim-specific evidence exit remains
required; a source pin alone cannot discharge it.

The following table preserves the **2026-09-09 source snapshot**; the current
2026-09-10 refresh immediately below supersedes its dependency statuses.

| Source | Exact pin | State / graph consequence |
| --- | --- | --- |
| BDP #19 Read+Update | `06ebabdb391d8ea730295f4e01ed00bc1206fe38` | Draft; singleton/sequence envelopes, durable admission/outcomes, aliases and shared deleted identity now exist; P3 must adopt their reviewed selected-profile successor under the explicit write pin. |
| BDP #20 Transactional | `5c3f3b10a2edbb77d914b7260cdf035008fc34c7` | Draft; T1–T65 ruled, including unresolved admission, bounded direct comparison and same-epoch retraction retry without deadline reset; T57 implementation/evidence work remains. |
| BDP #22 wildcard / #23 numeric | `c201cc28f74c6f71212aaf7f25966aabf55fb97e` / `2c537a6f8a4f42e4fef0fa5d47439bcb25d2efe7` | Draft sources for the domain rules already incorporated ahead of P0's old wire pin; reviewed wire integration remains required. |
| BDP #24 named Read projection / #27 Read erasure correction | `87de37f673f83ec54989fdff4891bacc05730ea6` / `d68cc698f63cc114a41d7d965a8b6bebbfda8b3c` | Draft; preserve named-projection/coverage law and genuine successor evidence; port the narrow erased-pointer rejection to the Go wire boundary. |
| Jim #6147 Phase 0 | `9c4e7a8f1959582f07db3b87641cb33863fda860` | Open; contract hooks remain nil/skipped, not a working CAS/History implementation. |
| Jim #6304 Phase 1 | merge `2bb1e20de0f0072d7600656ea3cb7f606929dcc6` | Merged 2026-09-08; migration 0067 includes issue/wisp shape parity, ignored-series twin and raw-SQL replay / CLI override. |
| Jim #6358 Phase 2 | `5fdfb92fe544c9a83feb098e83f2ccdd87b896c8` | Open; current writer/inventory, superseding older incomplete call-site reports; 0068 reserves attribution and byte-preserving storage, not the deferred durable-address migration. |

### Current dependency refresh (2026-09-10)

This docs-only successor incorporates Beads main
`a690b0a8c4d1ddc4f0bd9bf767499625dd71bc96`. It preserves both dependencies:
**BDP #19/#20 and Jim's versioned-beads work**. Current source/check observations
below are dated, not ongoing readiness claims.

| Source | Exact current source | State and phase consequence |
| --- | --- | --- |
| BDP Read foundation #29 | merge `19923f5bb6cc3f4ee4c508e36df3bd4c5c52344b`, source head `599361130bfaa07c2b5d157f3ecbe44f0691cb9e` | Merged 2026-09-10. Candidate input for the P0 wire owner’s §0 re-pin record (bundle/projection/matrix pins still owed): wildcard, numeric, named-projection and erased-pointer corrections from #22/#23/#24/#27 are incorporated ancestries. P0 adoption and current-base checks are separate work, not performed by this design correction. |
| BDP #19 Read+Update | merge `6d88f857cb643fe4e5d77e1dc45038a7d2e5ebb5`, source head `a2531e43baa5c6b27f22149b214326d8736e6198` | Merged 2026-09-10 wire/spec, without RU runtime. A P3 RU realization adopts a reviewed selected-profile pin and its own evidence; it need not wait for TX-only runtime. |
| BDP #20 Transactional | published PR head `5c3f3b10a2edbb77d914b7260cdf035008fc34c7`; reviewed local correction `0166ff8ef57c481f9ee8fb1223f728f12f9c75f4` (unpushed; no fetchable source pin) | Open draft at the published head. The local correction contains the reviewed G1–G5 HTTP clarification; it is not the remote PR head or a merged dependency. Shared Read HTTP implementation and fresh observations remain a gate before #20 lands. |
| Jim #6147 Phase 0 | `9c4e7a8f1959582f07db3b87641cb33863fda860` | Open; snapshot shows 117 successful, two skipped and two failed checks (PR Core and CI Gate / Required). Nil-hook contract scaffolding is not graph CAS or History realization; these failures are not automatically P0 blockers. |
| Jim #6304 Phase 1 | source `162a47703bb702d43e3192c79b6b802cf650d31d`, merge `2bb1e20de0f0072d7600656ea3cb7f606929dcc6` | Merged 2026-09-08, including 0067. Its historical head had 119 successful and one skipped checks. |
| Jim #6358 Phase 2 | `5fdfb92fe544c9a83feb098e83f2ccdd87b896c8` | Open; snapshot shows 121 successful and two skipped checks, with both CI Gate / Required checks successful. This is its own check evidence, not graph conformance. Migration 0068 remains reserved; graph P1 rechecks its actual slot. |

The candidate P0 Read input is the explicit #29 merge, not the later RU bundle
or a moving BDP main; the wire owner records the complete §0 adoption pins. Preserve §0's original pin and historical results until
the separate P0 successor records actual adoption, source/fixture provenance
and current Go boundary checks. P0 proves contracts; P2 must prove live Go Read
serving, current-view/owned closure, HTTP behavior and stable cross-request
cursors. The preserved #29 cohort does not prove the later G1–G5 prose.
P1 retains its storage/replication ADR gate; P3 retains its selected write-profile
ADR and evidence gate. Neither full TX runtime nor Jim's remaining phases are
blanket prerequisites for P0.

Current main already corrects the
old `docs/recovery/init-safety.md` freshness date; the earlier failed check was
a historical branch-base result, not a standing current-main failure.

**BDP ownership and phase boundaries** (citations below preserve the historical
draft pins; the P3 adoption re-verifies owning text at its selected write pin). [#19 Mutation results](https://github.com/gastownhall/bdp/blob/06ebabdb391d8ea730295f4e01ed00bc1206fe38/docs/specs/bdp.md#L2814-L2843)
requires the deleted Resource's final live revision in the `deletedIdentity`
schema carried as `result.deleted`; no version is minted by deletion. The
owned-Link result's `source` is the source Bead's absolute canonical URL and
`sourceRevision` is that Bead's resulting revision on create, update and delete. [#20 admission/comparison](https://github.com/gastownhall/bdp/blob/5c3f3b10a2edbb77d914b7260cdf035008fc34c7/docs/specs/bdp.md#L1225-L1308)
requires a durable execution owner and immutable creator-attempt binding separate from
the graph authority lease. Direct unresolved comparisons wait outside the DB
transaction under one finite budget; sequence precedence remains nonwaiting.
The graph lease and allocation ledger do not implement receipts, exactly-once
admission or the TX projected erasure ledger. P3 owns that realization, and
Read+Update does not wait for TX-only implementation. Before P3 implementation,
its ADR audits the selected profile's storage and interface needs against P1:
migration additions, state-version coverage, fence triggers, replication
inspection and fence/decorator censuses, plus any declared source break and
CHANGELOG migration under A8. The initial eight-table P1 scope is not a promise
that every future profile fits it. This audit mandates neither new tables nor
another interface widening; shipped migrations stay frozen and any necessary
addition follows migration discipline. P1 need not wait for the full P3 design. [T65](https://github.com/gastownhall/bdp/blob/5c3f3b10a2edbb77d914b7260cdf035008fc34c7/docs/specs/bdp.md#L5532-L5542)
scopes persistent Event-consumer erasure claims to the existing changefeed/
snapshot-ledger integration; Event delivery alone is not that assurance.
The same continuation also ACKed manifest-handle retrieval, same-epoch receipt-
page URL restart survival, body-less 406 negotiation refusal and the initial
receipt/finite-feed validator policy. At the historical #20 pin these five HTTP
choices were pending materialization. The local reviewed correction in the refresh above contains
their wire prose, while shared Read runtime and observations remain pending.
The graph wire/serving phases must adopt their reviewed result, including
shared Read consequences.

**Jim's current contribution and limits.** The [current writer](https://github.com/gastownhall/beads/blob/5fdfb92fe544c9a83feb098e83f2ccdd87b896c8/internal/storage/issueops/version_history.go#L24-L65)
records dependency changes and final outgoing dependency state. The [current
inventory](https://github.com/gastownhall/beads/blob/5fdfb92fe544c9a83feb098e83f2ccdd87b896c8/VERSIONED-BEADS-WRITE-PATHS.md#L9-L49)
distinguishes 27 seam call sites, 46 must-mint entry points and 21 versioned
paths out of 34 listed live paths; these counts are not interchangeable.
Remaining limits include unchanged re-import minting, UOW's per-repository-write
`1+N+M` versions versus the direct-leg composite operation, and stranded
delete/rename/demote history. `current_revision` is a store-local ordinal;
`version_id` and participation migration steps are not implemented at this pin.
The allocator is single-writer **at a time**, even within one store. None of
these facts licenses substituting the Issue writer for graph operation-local
revision, identity, ownership or transaction guarantees. Future C-lane work
should build on this contributor work while proving its translation, rather
than citing the old uninstrumented-writer survey as current evidence.

**Translation constraints.** Jim's JCS writer rounds numbers before preserving
the resulting bytes in LONGBLOB. The [BDP numeric rule](https://github.com/gastownhall/bdp/blob/2c537a6f8a4f42e4fef0fa5d47439bcb25d2efe7/docs/specs/bdp.md#L644-L661)
refuses inadmissible values before BDP mutation acceptance. An Issue-to-BDP
mapping must be established before allocating its BDP revision; it cannot
round or rewrite already-addressed BDP state. Random graph revisions, local
Issue ordinals, future durable UUID addresses, content tokens and TX erasure
digests remain distinct. Jim's empty actor plus `unknown` is not a valid
present BDP attribution with an empty principal: preserve truthful absence
or explicitly mapped nonempty attribution. Its current NULL agent/message
columns and writer timestamp do not implement BDP's new context contract.
Its [Phase 0 vocabulary](https://github.com/gastownhall/beads/blob/9c4e7a8f1959582f07db3b87641cb33863fda860/backend/conformance/expected_revision_contract.go#L26-L83)
has nil hooks and treats Unretained/disclosure as a later axis; BDP separates
incomplete reconstruction from authorization. Local Hold contracts are not
BDP wire holds, and product test shapes are not BDP conformance observations.

**History direction now selected; upstream materialization pending.** Donna
ACKed the earlier sixteen core choices and the final twenty-two initial-History
choices on 2026-09-09; the [durable ACK and consolidated choices](https://github.com/donnabox/agent-coordination/blob/4fd57836ae052dec41b66493b88954bdabbb4d0f/context/janet/history-tx-complete-ballot-20260909.md)
record the selected alternatives. This is a dependency record for the approved 38-unit
direction, not a competing normative definition or a claim that these pins
already contain the resulting schemas. The upstream History author owns the
closed prose/schema/fixture materialization before this realization adopts it:

- Complete optional History on all three profiles, canonical Bead/Link
  revision resolution and stable `view=versions` enumeration; retained
  addresses survive restore, while other token fences remain. Enumerate
  all retained versions, newest authority-order first, distinguishing
  replacement lineage and incomplete bodies; omit erased and unauthorized
  metadata rows. Authorized deleted-subject enumeration remains available.
- Subject-history-gated diagnoses, bounded missing-state diagnostics with
  explicit completeness, no refusal windows, aggregate participation claims,
  advance retention guarantees, sync hints or wire holds initially.
  Participation requires positive knowledge; absence does not prove pruning.
  Whole historical success requires current disclosure permission for its
  owned state, with no partial record. Unauthorized subject-history callers
  receive uniform `resource-not-found` / 404. Preserve known truthful direct
  predecessor/successor relations on retained replaced records; an authorized
  `latest-version` may name the current authority version. Do not invent
  adjacency between replaced and current lineages.
- Immutable change context now: authority-observed **commit time**, one
  instant per atomic transaction and separate instants for committed sequence
  members; optional per-operation assisting agent/message copied to every
  version that operation mints. Context accompanies version records, retained
  rows and matching version-bearing Events under their authorization and
  erasure rules. No-op comparison excludes context; legacy absence stays
  truthful. This applies to versions minted under History capability, not a
  retroactive requirement on unrelated non-History implementations.
- Initial local-store erasure assurance does not advertise generic persistent
  consumer replication. Imported retained copies require positively
  established erasure status; unestablishable copies are rejected/discarded.
  Required Gone evidence and applicable permanent erasure ledgers survive
  recovery. Generic pre-removal administration and extra import metadata are
  deferred, as are exact-byte witnesses, extra scheme mapping and the
  alternative surviving-citation deletion lifecycle.
- `revision-unrepresentable` is selected for positively established inability
  to serve an existing bound BDP value faithfully, distinct from missing
  pieces, unknown provenance and temporary I/O. `revision-allocation-unsafe`
  is the selected write-only persistent repair-required conflict; transient
  safety-inspection failure retains its existing temporary-failure behavior.
  Their exact profile/schema fan-out remains upstream materialization.
  Historical resolution never makes an old token a current write guard: the
  existing `expectedRevision` equality/current-state law still applies.

**P0 completion gate.** Preserve the historical pin/provenance and review
records. Before claiming current wire behavior, deliberately adopt a reviewed
Read successor, update vendored bundle/examples/fixtures/matrix/DTO parity
together, retire the wildcard run-ahead tripwire, and port #27's narrow
`resource-erased` pointer prohibition while preserving harmless RFC 9457
extensions. Respect #24's named Read projection and actual successor
observations; do not relabel old evidence or blindly vendor the whole TX
bundle. A contracts-only P0 can precede full P3 and Jim Phase 3, but its
remaining wire and runtime work is assigned by the phase/owner gates in §7. No graph
capability, readiness or merge grant follows from this alignment text or
from Jim's versioning flag.

## 1. Goal and constraints

Implement BDP in this repo: a first-class graph store the
protocol (and eventually the CLI) is implemented in terms of, beside — not
instead of — the existing Issue/Dependency machinery.

Hard constraints, in priority order:

1. **Zero compatibility degradation, defined precisely (amended 2026-09-07,
   A8 option A):** *same-version* legacy behavior is byte-identical — every
   existing CLI verb, JSONL interchange shape, journal record, and sync path
   behaves exactly as before on the same binary, with gate (non-TTY) output
   byte-identical. Out-of-tree `backend/` implementations take the **source
   break the storage interface already declares** (six one-line
   `ErrUnsupported` stubs for the `BeadGraph*` accessors, called out in
   CHANGELOG with the stub migration, the joint ReadyClaimer/BatchCloser
   entry being the precedent); once stubbed they behave exactly as before. Schema
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
  Two consequences, stated so "no new investment" is not misread: Memory-typed
  Beads' change feed lands on the **graph plane** (ruling 8's changefeed, P3),
  not on the journal; and the legacy `bd remember` / `bd recall` /
  `bd memories` / `bd forget` surfaces survive as **projections over
  `graphops` after P3**, which is the compatibility projection #5877 R24/R25
  describe.

## 2b. Where BDP and the Issue/Dependency stack disagree

The conflict inventory, consolidated. Each row is a law of the pinned BDP
spec set against behavior verified in the original survey; dated §0a
qualifications identify later contributor progress, not a retraction of the
historical observations; the last column says what the
conflict costs. This section is why the v0 projection was withdrawn (§5)
and is the requirements list for any C-lane path.

| Area | BDP law (pinned) | Current Issue/Dependency stack | Consequence |
| --- | --- | --- | --- |
| Revision coverage | Every record read serves an opaque, equality-only revision; every state-changing operation on a surviving Resource mints a fresh one; a semantic no-op mints none | `RowVersion` has documented partial coverage (direct-UPDATE text paths bypass it); label writes touch only the labels table; revision is served on the detail read and mutation responses only — list/JSONL forbid it | No existing token can stand in for a BDP revision |
| Reverse transitions | A→B→A is three distinct revisions | `updated_at` is second-precision `DATETIME` with documented same-second ties; `bd import --allow-stale` restores old rows including timestamps | State-derived revisions are impossible (r5 blocker) |
| Out-of-band writes | (Implementation constraint, not spec text: BDP's revision/identity laws presuppose the authority observes every mutation) | `bd sql` permits arbitrary direct SQL; compaction rewrites text in place; backup/restore resurrects historical state | No complete mutation feed exists; only funnel (C1/C3) or storage-level observation (C2) can close it; for the graph tables, ruling 13's row-level fence plus the state-change validator |
| Identity non-reuse | Committed Resource URLs are never reassigned — surviving deletion and epoch changes | Same-ID delete/recreate is permitted; import UPSERTs over existing IDs and accepts caller-supplied historical `created_at`; rename (delete+create) can A→B→A-reactivate an ID | Legacy IDs cannot be BDP URLs without a durable allocation/tombstone mechanism the stack lacks |
| Attribution (bdp#18, merged 2026-09-07) | A carried per-version `attribution {principal, status ∈ claimed\|unknown}` — data, not evidence; supplied by every version-minting operation; outside `properties`; excluded from the no-op comparison | Issues carry `created_by` and per-field audit rows; no per-version carrier; bd's later versions cannot name their author | The bd realization maps `created_by` to a claimed principal with status `unknown` for later versions; the graph store carries the member natively (spec B4 attribution columns) |
| ID grammar | Creation-time canonical IDs, multi-segment supported, reject-don't-trim | Configurable prefix grammar + adaptive-length collision-probability IDs; validation checks prefix shape, not BDP path grammar | Eligibility/surrogate policy required before any legacy ID is served (C lane) |
| Type system | One immutable nominal declared Type per Resource; descriptors with `conformsTo`; a Type describes beads or links, never both | `issue_type` is an ordinarily mutable column; open string vocabulary via `types.custom`; no descriptors, no hierarchy | Type immutability is violated by ordinary updates (r5); descriptor catalog must be built |
| Edge multiplicity | Links are first-class; no uniqueness constraint on (type, source, target) | `depid.New(issueID, target)` — at most ONE edge per (source, target) pair, type excluded from the key | Dependencies structurally cannot represent BDP Links (S2 killer #1) |
| Edge versioning | Every Link carries its own revision; owned-Link mutations version the source | Historical survey: Dependencies carry no revision; dependency edits never touch the source Issue's `row_lock`; `Metadata` is a `string`, surrogate `ID` populated only on some read paths. Current-source qualification (2026-09-09, §0a): Jim versions the source through `current_revision` / `issue_versions`, a different witness from `row_lock`, including its outgoing dependency state | Dependencies still lack independent BDP revisions and endpoint-key multiplicity; source witnessing has progressed, but does not establish the BDP owned-Link contract |
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
   changes to the migration framework itself; the ruling-13 fence triggers
   are DDL in the same files, shipped through the unchanged runner (probed:
   one multi-statement `Exec` creates `BEGIN … END` trigger bodies over the
   tree's `multiStatements=true` DSN).

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
   ruling 12 as amended by A2, `bd --graph-mode link serve` mints the Scope
   on first serve under a configured URL and serves it honestly empty; plain
   `bd serve` never mints and mounts only an already-minted held Scope; without a configured URL there
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

> **Spelling as ruled 2026-09-07 (A2/A3/A6):** `bd bdp-serve` (now `bd --graph-mode link serve`) →
> `bd --graph-mode link serve` (the strict, minting command over the same
> `httpapi` server; `bd serve` mounts the BDP rows only when it holds an
> already-minted Scope); graph behavior is selected by the root flag
> `--graph-mode link|dependency`, never by a verb prefix; `bdp.scope_url`
> lives in tracked `config.yaml`, the per-workspace `link-graph.route` and
> `bdp.server` in untracked `config.local.yaml` (nothing in `metadata.json`). Detail:
> `BDP_GRAPH_CLI_AND_STORAGE_SPEC.md` Part A.

Three commands, three responsibilities — the store, the Scope, and the
client:

1. **`bd init` initializes the graph store** alongside everything it
   initializes today: graph tables, the allocation/tombstone ledger, and the
   Type Descriptor bootstrap, all against the normalized storage interfaces
   (any provider). No separate `bd graph init`. A workspace therefore always
   has a graph store; it does not yet have a *Scope*.
2. **`bd --graph-mode link serve` creates the BDP Scope on top of the store; `bd serve`
   serves an already-minted Scope it holds** (as ruled 2026-09-07,
   A2/A7/A9): on its first serve under a configured `bdp.scope_url` (ruling
   7a) `bd --graph-mode link serve` mints the Scope row, the `mint` ledger event, and the
   built-in Type catalog in one **multi-phase, fenced** transaction (a shared
   database: the dolt-ignored authority lease with its fence cell; a
   configured remote — *deferred under A9*: fetch → ancestor check → scoped
   commit → push), finalizes this workspace's
   authority witness, and serves the Scope — honestly empty at birth, with
   `beads/`, `links/`, and `types/` all present. Because the Scope URL is a
   tracked project fact, **among serving commands, only `bd --graph-mode link serve` mints**: a plain `bd serve` on
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
   never a startup refusal on account of the graph. `bd --graph-mode link serve` refuses
   (exit 2) in those cases. No development-mode URL derivation exists in
   bd. (W2 decides whether `bd --graph-mode link serve` survives as the alias — default
   yes; it is the serving minting path.)
3. **Client wiring — `bd init --bdp-server <url>` and `bd --graph-mode link client`**
   (as ruled 2026-09-07, A6): one more `bd init` target, beside
   `--server`, `--shared-server`, `--proxied-server`, `--team-server`, and
   `--backend`, distinguished by rerouting ABOVE the normalized storage
   abstraction (at the CLI): the link-mode read verbs become a BDP client of
   the designated server. The per-workspace keys live in the untracked
   `config.local.yaml`; the project fact lives in tracked `config.yaml`:

   ```yaml
   # config.yaml (tracked)
   bdp:
     scope_url: https://beads.example/acme/   # what the authority mints/serves (7a)
   # config.local.yaml (untracked, machine-specific; merged over config.yaml)
   bdp:
     # (link-graph.route: local | server, default local — the client route)
     server: https://beads.example/acme/      # graph-verb target when client: server
   ```

   `bd init --bdp-server <url>` and `bd --graph-mode link client server --server <url>`
   write `config.local.yaml`; generic `bd config set` refuses the
   per-workspace keys with that guidance. Env: `BDP_SCOPE_URL` (7a) and
   `BD_BDP_SCOPE_URL`; `BD_BDP_SERVER`; **`link-graph.route` is blocked from env**
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

| Surface | Legacy behavior (verified) | Graph-store policy (ruled; §9) |
| --- | --- | --- |
| Dolt push/pull (server + embedded) | rows travel; embedded pushes directly | graph rows travel identically |
| Merge settlement | `versioncontrolops/mergesettle.go` already settles metadata, dependencies, migrations, config, issues, labels, comments, and events, with seven-table FK-cascade repair — conflict dispatch is a hard-coded switch, separate from an always-considered FK-repair pass; NOTE `MergeWithStrategy` returns early on clean merges and plain `Merge` bypasses settlement entirely | graph settlement must be **centralized so every merge entry point runs it** (clean-merge early-returns and plain `Merge` included — enumerate or funnel the routes): identity/endpoint integrity, dangling-Link detection, owned-Link invariant validation. A pass can reject or quarantine invalid imported state; it CANNOT serialize two independently accepted `max`-violating writes after the fact — hence decision 9: BDP writes flow through one serving authority per Scope, and foreign-clone merges of graph tables are out-of-contract (quarantined on detection); **ruling 14 (2026-09-07) settles this row:** every SQL pull route fetches, inspects the eight graph tables against the tracking ref, and refuses a foreign or unexplained delta before merging; routes that cannot inspect first fall to the validator's revert; no graph-table conflict is auto-resolved and `--strategy` never touches one |
| Federation type-filtering | **server-topology-specific**; deletes `issues` rows by type | v0: graph tables ride federation **unfiltered, by decision** (rulings 9/14); a per-topology filter hook is post-v0, and filtering one endpoint must also drop/deny the Link (never emit a dangling edge) |
| Journal (frozen v0 vocabulary) | Issue/Dependency/Comment payloads only | **graph events are excluded**; a separate graph changefeed carries them; the frozen vocabulary is not extended |
| Export/JSONL (contract class) | contractual shapes | graph gets its own export lane; legacy shapes untouched |
| Backup / restore | whole-database state (a different contract class from export); a Dolt backup restore carries the working set, dolt-ignored tables included (probed) | ruling 11 as amended by A5 (ruled 2026-09-07): the installation-keyed authority witness (`.beads/graph-authority.local.json`) records the hash-chained ledger head; a restore keeps the file but the store no longer contains that head → refused until `bd --graph-mode link restore`, which shows continuity from a `bd --graph-mode link ledger snapshot` (recovery predicate) or rotates the Scope URL and epoch; providers DECLARE `LedgerDurability`; `bd backup restore` also marks the witness unverified |
| Wisps | private/transient; excluded from export/federation by default | **ruling 6:** not served in v0 (C-lane note) |

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

**Current-source qualification (2026-09-09):** the dependency-writer statement
above describes that historical substrate. Jim's Phase 2 now versions the
referencing Issue when an outgoing dependency changes and captures its complete
outgoing dependency set. Its current limits and the BDP translation boundary
are recorded in §0a above; this progress does not turn Dependencies into
first-class BDP Links or reverse the ruled v0 projection withdrawal.

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

The historical round-5 assessment was: mutation-time witnessing — the only
remaining mechanism — would require instrumenting every legacy write path
including `bd sql`, which is
arbitrary SQL and cannot be completely instrumented even in principle.
The conclusion is structural, not incremental: **a store that permits
timestamp ties, stale restores, arbitrary SQL, and identity resurrection
cannot be projected into BDP's revision and identity laws by any read-side
mechanism.** Current-source qualification (2026-09-09, §0a): Jim's writer
now versions 21 of 34 listed live paths, including outgoing dependency edits;
this is mutation witnessing, distinct from the old `row_lock` survey. Unchanged
re-import currently mints, UOW can mint per repository write, and raw SQL,
restore and merge-settle remain outside that witness contract. This progress
does not prove complete BDP translation or change the historical conclusion
about read-side reconstruction. So v0 withholds the projection:

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
  tasks all pass (the original C2 survey did not establish them): (a) Dolt
  trigger availability and transactional semantics; (b) whether direct `bd sql` DML actually
  fires them (noting `bd sql` is unavailable in embedded mode and runs via
  direct SQL-server or proxied-server paths — and UOW is an access path,
  not a third storage engine); (c) trigger-row behavior under replication,
  merge, and restore; (d) Scope URL/epoch handling after restore
  (decision 11). Current-source qualification (2026-09-09, §0a): the graph
  fence probes and Jim's application-level version writer are later evidence
  with their own scopes; neither proves this complete legacy observer. Build
  on the current writer/inventory rather than repeating a claim of no mutation
  witnessing; raw SQL, restore and merge-settle coverage still needs proof.
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
> `GraphReadSource`, `graphsource`, `bd bdp-serve` (now `bd --graph-mode link serve`), `bd bead`/`bd link`
> verbs): read them through `BDP_GRAPH_ARCHITECTURE.md` §2. Phase
> *boundaries* stand: P0 contracts + pinned wire, P1 storage (roles,
> bodies, migrations, conformance; the replication/merge ADR is a P1
> gate), P2 serving (BDP rows inside `httpapi`; collection routes after the
> cursor ADR), P3 writes. **P0 is open (A1–A9 and rulings 13–14 ruled 2026-09-07).**

- **P-1 — Decisions and pins (no code):** charter ADR; ratify the
  projection withdrawal (v0 Scope = graph store only); Scope URL/identity;
  graph-store allocation/tombstone design; serving authority; replication
  matrix rows; auth-view mapping for bearer-token reality;
  listener/authority-semantics choice. (The BDP pin is already written in
  §0.) *Exit: every row ruled by Donna, recorded in this doc.*
- **P0 — Contracts:** wire DTOs held to the pinned schema by tests (hand-written, Part D.3); immutable
  domain values (`Properties`, `Ref` sum, records); pure validators; typed
  error vocabulary; the three ruling-13 verification rows — **answered
  2026-09-07: PASS / PASS-WITH-RULE / PASS-WITH-RULE**
  (`engdocs/BDP_P0_VERIFICATION_ROWS.md` on the P0 branch; the fence
  ships, with the rules spec B3/B4 now record). *Exit: model laws 100%
  table-tested; DTO round-trip against pinned schema fixtures; the three
  rows answered.* **Met against the historical §0 pin on 2026-09-07** on `janet-beadgraph-p0` (council 11:
  three reviewers, all findings folded; graphops 100% statement coverage;
  bdpwire 94%). This is historical completion only; §0a and the owner gates
  below govern current-wire completion and later serving claims. Contracts-only
  P0 promotion must name that scope and its remaining gates; it is not a graph
  capability/readiness or upstream merge grant.
- **P1 — Graph read storage (S1):** the replication/merge ADR first
  (ruling 14: `engdocs/BDP_GRAPH_REPLICATION_ADR.md`, council-reviewed; no
  graph migration merges before it); record the A10 solo mint trigger and
  workspace-gate ↔ lease-predicate mapping (architecture §2b) before wiring
  the embedded leg; then tables + migrations (descriptor
  store and the ruling-13 fence triggers included); typed snapshot-source resolution (`GraphReadSource`)
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
  (auth/project/deadline semantics preserved); run the external BDP Read
  matrix **deliberately re-pinned under §0a** as a target. *Exit: that
  successor matrix green, the applicable owner gates below complete, with its
  own provenance split honored — packaged rows proven at the packaged
  public boundary, self-certified in-process rows via the in-process lane
  and labeled as such (the pinned artifact is explicit that they are not
  black-box conformance) — plus a beads-owned public-boundary
  cursor-stability test across requests.*
- **P3 — Writes and CLI, gated on its own ADR AND on upstream spec
  artifacts:** the pinned write-profile envelope (an owned-Link mutation's
  result must also report the source Bead's resulting revision), the
  owned-Link Event delta, AND the sequence/idempotency envelope schemas,
  problem rows at the reviewed write-profile pin recorded in §0 before P3.
  At the §0a dependency pins, #19 already has
  [`packages/conformance/catalog/read-update-v1.json`](https://github.com/gastownhall/bdp/blob/06ebabdb391d8ea730295f4e01ed00bc1206fe38/packages/conformance/catalog/read-update-v1.json)
  and illustrative [`fixtures/read-update/`](https://github.com/gastownhall/bdp/tree/06ebabdb391d8ea730295f4e01ed00bc1206fe38/fixtures/read-update);
  #20 has [`packages/conformance/catalog/transactional-v1.json`](https://github.com/gastownhall/bdp/blob/5c3f3b10a2edbb77d914b7260cdf035008fc34c7/packages/conformance/catalog/transactional-v1.json)
  and illustrative [`fixtures/transactional/`](https://github.com/gastownhall/bdp/tree/5c3f3b10a2edbb77d914b7260cdf035008fc34c7/fixtures/transactional).
  These are catalog metadata and illustrations, not executable write matrices
  or observed runtime evidence; those remain pending at these pins. The
  shared HTTP ACKs also require their reviewed upstream materialization.
  Profiles are **Scope-wide**
  (uniformity law), and the Event-delta gate binds exactly the profile
  that has Events: a Scope containing owning Types cannot advertise the
  **Transactional** profile until the owned-Link Event delta exists —
  Read+Update has no Events and is not blocked by it — and this is a
  Scope-level gate, not a per-Type advertisement. Write tests require: create/property-update
  mint fresh Link AND source revisions; **deletion mints nothing for the
  deleted Link** — its result reports the deleted identity plus the
  source's fresh revision; the deleted identity carries the Link's final
  live revision, agreeing with `DeletedData` and changefeed tombstones
  ([#20 Mutation results](https://github.com/gastownhall/bdp/blob/5c3f3b10a2edbb77d914b7260cdf035008fc34c7/docs/specs/bdp.md#L3328-L3347)); target revision
  unchanged throughout; both surviving revisions preserved on semantic
  no-op. Then tombstones,
  endpoint constraints, replication of writes; only then
  `bd bead`/`bd link` verbs. *Exit: the pinned write-profile conformance
  artifacts green when they exist upstream, plus beads-owned transaction,
  identity/non-reuse, deletion-result, installed-Type-contract
  validation, and replication tests at the public boundary.*

### Current-wire and profile adoption gates (2026-09-09)

Owners below are phase responsibilities within this plan (janet's graph work);
BDP owns the upstream source/evidence products. A row is an exit condition,
not evidence that work has run. Preserve the historical P0 result above.

| Work / owner | Phase and prerequisite | Required exit before the corresponding claim |
| --- | --- | --- |
| Read successor pin — P0 wire owner | P0 current-wire completion; reviewed upstream Read cohort | Record the selected spec/bundle/projection and matrix pins in §0, honoring #24's named projection and actual successor provenance. Never relabel old observations or import the whole TX bundle as Read. |
| Wire parity — P0 wire owner | Same P0 successor adoption | Update vendored provenance, bundle, examples, fixtures, matrix references and DTO parity together; replace the obsolete wildcard-rejection tripwire with the successor's applicable wildcard contract checks. |
| Erased-problem boundary — P0 wire owner | Same P0 successor adoption | Port #27's narrow `resource-erased` pointer prohibition with focused Go boundary checks, preserving harmless RFC 9457 extensions. |
| Response negotiation — P0 wire / P2 serving owners | Reviewed materialization of the shared HTTP ACK; P0 captures the contract, P2 serves it | Body-less 406 for unsupported response media and its normal failure/auth non-disclosure precedence are represented and tested at the Go HTTP boundary before current serving claims. |
| Applicable conditionals — P0 wire / P2 serving owners | Same reviewed HTTP materialization; apply only to the endpoints/profiles that use it | Preserve the selected native body-less 304/412 behavior, validation precedence and normal response metadata; do not substitute a BDP revision conflict. Prove the Read endpoint behavior at the public boundary; receipt/feed validator policy belongs to its P3 profile. |
| Read execution — P2 serving owner | Above applicable P0 contracts and the re-pinned Read matrix | Fresh applicable Go public-boundary observations, provenance-labelled in-process rows and the beads-owned cross-request cursor check; old P0 probes are not successor observations. |
| Selected write profile — P3 owner | Reviewed §0 write pin and P3 delta ADR | Adopt applicable admission, results and recovery contracts and approved HTTP successors; prove the selected profile's executable conformance and beads-owned exits. RU does not wait for TX-only receipts, Events or erasure-feed implementation. |

A contracts-only P0 may land with an explicitly historical wire pin and these
later completion owners. Current-wire claims wait for the P0 rows; current
Read serving claims also wait for P2. Neither requires all TX runtime or Jim's
remaining phases. A future Issue/History integration still needs its §0a
translation proofs; no new interface, table, capability or upstream merge
permission is selected by assigning these gates.

## 8. Related workstreams (operator direction 2026-09-02)

This plan covers the graph store and its Read serving. Sibling workstreams,
each owning its own writeup:

- **W-arch** — `BDP_GRAPH_ARCHITECTURE.md` and
  `BDP_GRAPH_CLI_AND_STORAGE_SPEC.md` (v16, 2026-09-10; v15: 2026-09-09;
  v14: 2026-09-02), eight council rounds with live Dolt probes; A1–A9 and
  decisions 13–14 ruled 2026-09-07, with A10 ruled 2026-09-08 and recorded
  in §9.
  Preceded P0 code; P0 is open.
- **W1** — flesh out the **Update and Transactional profiles** of BDP and
  the reference implementations (the protocol is Read-heavy today); this is
  the upstream gate for P3 writes.
- **W2** — `bd --graph-mode link serve` / `bd serve` integration (as ruled 2026-09-07,
  A2: one server, a conditional BDP route table): decide whether the
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
7b. **Listener — RULED (amended 2026-09-07, A2):** same listener, same
   table-driven middleware path; BDP routes are a conditional second route
   table inside `internal/httpapi` behind the same `route()` wrapper, so
   bearer-auth and project-identity semantics are preserved by construction;
   among serving commands, only `bd --graph-mode link serve` mints, and `bd serve` never refuses on
   account of the graph. v0 authorization-view mapping: one view per bearer
   token = the whole Scope (no hidden Resources) — honest and conformant
   until real views exist; federation/multi-view later changes the
   mapping, not the listener.
8. **Journal/changefeed — RULED:** frozen v0 journal untouched; the graph
   store gets its own changefeed as a contract capability arriving with
   P3 writes (providers with a native event log realize it over that).
9. **Serving authority — RULED (corrected model):** the authority is the
   graph store *as reached through the normalized storage abstraction*,
   whichever provider realizes it — not `bd serve`, and not Dolt. The
   authority marker (Scope URL + authority id, minted on first serve by
   `bd --graph-mode link serve` under a configured URL — ruling 12 as
   amended by A2; A10 separately permits local solo minting),
   single-serialized history, non-authority refusal (of graph writes AND
   BDP serving for that URL), and **single-transaction operations under a
   store-asserted authority witness** (A1, ruled 2026-09-07; the snapshot
   lease is withdrawn as a mechanism — cross-request continuation is P2's
   cursor ADR) are graph-CONTRACT obligations proven by the graph
   conformance suite; the CLI graph verbs
   and the BDP handler are both clients of that abstraction, so they are
   one authority on any provider. Dolt is the in-tree reference
   realization. Promotion is explicit and epoch-rotating; **in v0 a Scope's
   authority is a shared database that minted it**, except A10's embedded
   solo topology (A9 ruled 2026-09-07, amended by A10 on 2026-09-08):
   replication, restore, and copy confer nothing; promotion in place is a
   self-regrant or an operator's explicit steal; a new database takes a new
   Scope URL; the shared-database fence is the lease of A7. Consequence: the
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
   `bd --graph-mode link restore` (spelling per A3) rotates the Scope URL and
   epoch and refuses the old URL. An epoch change alone is never
   sufficient.
12. **Store, Scope, client — RULED (replaces "empty-at-birth"):** three
   commands, three responsibilities (§4 "Lifecycle commands"). `bd init`
   initializes the graph store with everything else, against the
   interfaces — no separate graph init. `bd --graph-mode link serve`
   creates the Scope on first serve under a configured URL (A2), and
   serves it honestly empty; `bd serve` only mounts an already-minted
   held Scope and never mints. No URL → no BDP routes. A10 separately
   permits local solo minting and verbs, with no embedded HTTP serving. A
   client-wiring is `bd init --bdp-server <url>` — one more `bd init`
   target, distinguished by rerouting ABOVE the storage abstraction (at
   the CLI) rather than below it (§4 "Lifecycle commands"); after it, the
   CLI's graph verbs speak BDP to the designated server. Tests prove Issues never leak into graph inventories;
   a provider implementing the six accessor stubs without the capability
   keeps existing `bd serve` behavior — routes absent, never a startup
   failure (A8, ruled 2026-09-07).

13. **Out-of-role DML enforcement boundary — RULED (2026-09-07): "A+B".**
   Out-of-role DML — `bd sql` in both modes, raw SQL clients, force-push,
   and merges — is out of contract. The state-change validator refuses
   invalid or foreign-authority graph state on every observed
   state-version change; for the ledger-covered tables (descriptors,
   allocations, the ledger itself) the delta must be explained by ledger
   events since the recorded head, and bead and link bodies are checked
   by row provenance (`last_authority_id`/`last_epoch`). The eight
   replicated graph tables carry `BEFORE INSERT/UPDATE/DELETE` triggers,
   installed by the graph migrations, that refuse a row unless the session
   set the role variable `@bd_graph_role`; every graphops mutation sets it
   inside its transaction and clears it before `COMMIT` or `ROLLBACK`.
   The fence stops accidents, not holders of database credentials; a
   DB-privilege boundary (two SQL users) is a C-lane task. Merges belong
   to ruling 14. Probed on Dolt 2.1.8: the trigger shape parses (in
   `BEGIN … END` form) and fires (errno 1644); a session variable gates
   it; a transaction that commits without clearing leaves the pooled
   connection unfenced, one that clears does not; the triggers replicate
   through `dolt_schemas` by push, clone, and pull; they are silent on
   `DOLT_MERGE` and `DOLT_PULL`; one multi-statement `Exec` of a migration
   file creates them over the tree's DSN; privileges live in
   `.doltcfg/privileges.db` per installation and never replicate. Three
   P0 verification rows gate the fence (§7); if one fails, v0 ships the
   validator alone and this ruling records it. **Answered 2026-09-07:**
   (i) PASS — the embedded leg creates and fires the triggers; (ii)
   PASS-WITH-RULE — a transaction cancelled between statements is pooled
   with the variable set on both server legs, so the deferred clear runs
   on `context.WithoutCancel` and poisons the connection when it cannot be
   confirmed; (iii) PASS-WITH-RULE — the CLI migration bundle needs a
   `DELIMITER` rendition, check D and `doltIgnorePatterns` must learn the
   lease table, and a dirty `dolt_schemas` is refused late. The fence
   ships. **Ruled 2026-09-08 (Part D.7 = A):** the validator and ruling 14's
   inspection set carry a fence census over `dolt_schemas` — each of the
   eight replicated tables must carry its three triggers; a missing one is
   repaired with the migration's idempotent `DROP TRIGGER IF EXISTS` +
   `CREATE TRIGGER` pair before a delta is judged, and the census never
   changes the graph-state version. No new `bd sql` flag: the
   deliberate override is a proxied-mode batch that sets the variable
   first, or a raw client session. Option A alone (validator only), C
   (two SQL users in v0), and D (a `bd sql` statement guard) were the
   alternatives; C stays a C-lane task, D is redundant under B.

14. **Replication/merge ADR — RULED (2026-09-07): option B, the gate plus
   a four-law charter.** The ADR gates the graph migrations (P1) and is
   council-reviewed before they merge; it writes mechanism and conformance
   rows under these laws. (1) **Fetch, inspect, merge.** Every SQL pull
   route fetches, inspects the eight replicated tables between HEAD and
   the remote-tracking ref (`dolt_diff('HEAD', 'remotes/<remote>/<branch>',
   '<table>')`), and refuses before merging when the delta is
   foreign-authority or unexplained by ledger events; routes that cannot
   inspect first — the CLI-subprocess pull, a merge that already landed,
   restore, force-push — fall to the state-change validator, which reverts
   the eight tables to their state at the recorded HEAD in a new commit
   when that HEAD is an ancestor (the hazard-R undo shape: table-scoped,
   later commits preserved) and marks the witness `unverified` otherwise.
   (2) **No graph-table conflict is ever auto-resolved**, `--strategy
   ours|theirs` never touches a graph table, and the pull is refused
   naming the table; the remedies are the link-mode verbs (rotate, steal,
   restore), not conflict resolution. (3) **A foreign graph delta is
   refused whole**; graph tables are never merged cell by cell; the later
   of two mints under one URL adopts the earlier by an explicit verb the
   ADR names (the A9 interim "earlier mint wins" becomes that verb).
   (4) **A workspace without a witness takes the remote's graph state
   wholesale** — its graph tables are a replica — and refuses to mint
   until rotated or stolen (A9). Federation carries the graph tables
   unfiltered in v0. The entry points the ADR must cover are every
   `DOLT_PULL`/`DOLT_MERGE` route: the SQL pull route
   (`pullWithAutoResolveUnchecked`), the CLI-subprocess pull, federation
   peer pulls on both routes, the UOW leg's `Pull`, `bd vc merge`
   (`MergeWithStrategy`), the remote-migrate gate's `--ff-only` adopt, and
   `ForcePush`. Probed on Dolt 2.1.8: `DOLT_PULL` inside a transaction
   commits the merge immediately — fast-forward and no-conflict three-way
   alike — and `ROLLBACK` does not undo it (only a conflicted merge stays
   in the working set, which is what the tree's rollback-on-conflict
   relies on); after `DOLT_FETCH`, `dolt_diff` against the tracking ref
   enumerates the incoming rows before any merge. Tree facts the ADR
   inherits: `TryAutoResolveMergeConflicts` resolves only `metadata`,
   audit-only `dependencies`, `issues` (field-level three-way), `labels`,
   `comments`, and `events` — a conflict on any other table already fails
   the pull; the SQL pull route merges into the default branch (be-5ybd),
   where the graph tables and the lease live. Options A (gate only,
   content open), C (post-hoc validator only), and D (gate at P2/P3)
   rejected. The ADR is P1's first deliverable:
   `engdocs/BDP_GRAPH_REPLICATION_ADR.md`.

### Amendments ruled (A1–A9: 2026-09-07; A10: 2026-09-08) — the interview record

Raised by eight three-reviewer councils on the W-arch docs and ruled one
decision at a time on 2026-09-07. The normative text above (rulings 7b, 9,
11, 12; §3; §4 lifecycle) now reads as amended; this block is the record.

- **A1 (ruling 9).** "Single-transaction operations under a store-asserted
  authority witness" replaces "the snapshot lease": the accessor loads this
  workspace's witness (a clone-local file) and the body asserts it inside
  its transaction — Scope row identity, hash-chained ledger head, the lease
  (a read requires an unexpired lease and never writes it; a mutation must
  UPDATE it and see one affected row), and the graph-state version. No
  request type carries authority fields. The cursor is opaque from P1;
  cross-request continuation is P2's cursor ADR; no collection routes ship
  before it.
- **A2 (rulings 7b, 12).** BDP routes are a conditional second table inside
  `internal/httpapi` behind the same middleware, served from the
  unit-of-work leg. Only `bd --graph-mode link serve` mints, through the
  staged startup; it inherits serve's whole-surface `--readonly` refusal
  and refuses without a held Scope. `bd serve` mounts the rows when it
  holds an already-minted Scope, converts every graph failure into "rows
  absent + notice", and never refuses on account of the graph. Intra-Scope
  references are stored Scope-relative and rendered against the live Scope
  row; serving under a different base URL is a rotation, never a remount.
- **A3 (ruling 12 / §4 lifecycle).** Graph behavior is selected by the root
  flag `--graph-mode link` (default `dependency`; `BD_GRAPH_MODE`; config
  `graph-mode`), never by a verb prefix. The mode names the graph by its
  edge kind; every verb creates, reads, or serves the selected graph or
  refuses; `bd link` creates the selected graph's edge; without the flag
  every verb is byte-identical to today. The root store policy keys on mode
  and command path and is authoritative at every leaf-name call site.
- **A4 (§3 layering).** Values, laws, and roles live in public `graphops`;
  accessors are named `BeadGraph*`; no `backend/` aliases.
- **A5 (ruling 11).** The clone-local half is `.beads/graph-authority.local.json`,
  bound to an installation key, written under a bounded exclusive lock with
  multi-phase transitions recovered by evidence; the ledger is an
  append-only, hash-chained event table with a nonced sequence counter; the
  witness records the head; no event exists before mint; the ledger lane
  restores anti-reuse history only; providers declare `LedgerDurability`;
  restore rotates unless continuity is shown. Residuals stated. The
  retained-versions shape Memory needs is the History lane's (bdp#1). The
  History lane's first laws are now ruled there (2026-09-08): a
  version-addressed read is complete or a typed refusal — Unretained is a
  refusal naming the missing fields, never a member inside a record — and
  ownership over an open vocabulary is the `"*"` entry in `ownsOutgoing`; the
  graph plane's own history reads, when they come, follow both.
- **A6 (§4 lifecycle, 7a env).** `bdp.scope_url` is a project fact in
  tracked `config.yaml` (`BDP_SCOPE_URL` first), refused by `config set`
  once a witness is held; `link-graph.route`, `bdp.server`, and
  `bdp.insecure_http` live in untracked `config.local.yaml`, written by
  `bd init --bdp-server` and the link-mode `client` verb; tokens from files
  only; nothing in `metadata.json`; precedence env, then local, then
  tracked, with the route blocked from env.
- **A7 (ruling 9, promotion) — the shared-database half.** A dolt-ignored
  lease row bound to Scope and authority whose every write rewrites a
  random fence cell and predicates on the value it read (probe-confirmed
  on Dolt 2.1.8, which merges transactions cell by cell); reads never write
  it and require an unexpired lease; an expired lease naming this workspace
  self-regrants; a foreign holder is replaced only by an explicit
  `--steal`; fenced transactions beside a live server carry a deadline
  below a third of the TTL; `promote` and `rotate` run beside the server,
  `types install`, `restore`, and `ledger apply` take the exclusive gate.
  The remote half (publication primitive, remote-read freshness,
  publication recovery) is deferred to the write-profile ADR under A9.
- **A9 (ruling 9).** v0 authority requires a shared database. The topology
  matrix: a shared database that minted locally is authoritative regardless
  of any configured remote; one that received the Scope row by replication,
  restore, or copy refuses until rotated or explicitly stolen; embedded and
  registered-backend workspaces refuse local authority operations and exist
  as client hosts; a remote neither grants nor removes authority; two
  shared databases minting under one tracked URL are settled by the
  replication ADR (interim: the earlier mint wins). **Amended 2026-09-08
  (solo topology, ruled B — A10 below):** an embedded workspace with no
  configured remote and no server is its own authority.

- **A8 (§1 constraint #1; ruling 12) — option A.** Constraint #1 is
  scoped to *behavior*: every in-tree topology and existing workspace is
  byte-identical in gate output; out-of-tree `backend/` implementers take
  the source break the storage interface already declares (six one-line
  `ErrUnsupported` stubs, CHANGELOG call-out with the stub migration). The
  compiler catches direct implementers; a required method promotes silently
  through every wrapper that embeds the interface, so the three reflection
  censuses stay mandatory. Option B (an optional capability interface with
  explicit wrapper implementations, a capability census, and resolvers)
  rejected.

- **A10 (ruling 9; amends A9) — solo topology, RULED 2026-09-08: B.** An
  embedded workspace with no configured remote and no server mints a local
  Scope, holds a witness and a lease satisfied by the workspace gate (the
  fence has nothing to fence: one process holds an embedded store), serves
  nothing (`bd serve` still refuses embedded Dolt), and answers every
  link-mode verb locally. It becomes a client host the moment a shared
  database or remote appears, under the rotation and steal rules; a solo
  store later pushed to a remote is "minted locally" for the replication
  ADR — authoritative until a foreign delta appears. The embedded leg wires
  the full read contract for the solo topology and the refusal contract for
  client hosts. Raised by Steph's adoption harness (sjarmak/mem), which runs
  bd embedded with one isolated store per trial; without this row the
  post-P3 memory projection would have no local graph on any laptop.
  Options A (defer to the P3 packet) and C (require a local server)
  rejected. Epoch spelling confirmed the same day (`authority_epoch`,
  `last_authority_epoch`, `birth_authority_epoch` at P1).
