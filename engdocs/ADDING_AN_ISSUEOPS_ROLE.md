# Adding an issueops role

The governing rule is short: **a new capability gets a new role interface and a
new accessor. Never append a method to an existing role.** That rule is why
`storage.Storage` reached 139 methods once and will not again.

What the rule does not say is what a new role COSTS. This page is the measured
answer, derived from `issueops.Counter` — the first role added under the rule,
and the one every later role commit follows. Nothing here is aspirational: each
item names a file that exists in that commit. The last section is for the rarer
case: a capability that does not belong in `issueops` at all, which
`memoryops` was the first of.

## Is it a role at all?

Two questions, both of which must be yes.

- **Is it a different QUESTION?** Not a different filter, a different shape of
  answer. `Counter` answers with a number; `Reader` answers with pages of
  issues. A count has no order, no page and no cursor, so folding it into
  `Reader` would have meant a request carrying paging fields a count must
  ignore — the shape that makes a caller believe `--limit 10` bounded the
  answer.
- **Is it reached THROUGH a substrate?** A role is handed back by an accessor
  on a store or a unit-of-work provider. Anything that CREATES a substrate —
  `bd init`'s filesystem and git provisioning — is constructor territory and
  cannot be a role.

A role may be born with more than one method when they are two shapes of ONE
question (`Count` and `CountByGroup` share a predicate and differ only in
whether the answer is a number or a number per bucket). The rule forbids
APPENDING later, not being born whole.

**And a third question, which the first two do not ask: does a SIBLING already
answer this, and do both survive the answer?** "Different question" is only
half a test, because two roles can each be a different question and still be
one capability split in two — and the split is only worth it if each half is
reachable, contract-pinned and named on its own terms afterwards.

`issueops.BatchCreator` and `issueops.BatchApplier` are the worked example.
Both write many beads in one transaction, and the tempting reading is that the
second subsumes the first: an apply plan of N create items is a batch create.
It is not, and the reason is what to copy:

- **The QUESTIONS differ in what the caller is allowed to say.** `batchCreate`
  takes a homogeneous list of creates and answers with the created rows in
  request order; `batchApply` takes a heterogeneous plan of creates, updates
  and closes over rows that may not exist yet, with intra-plan references and a
  per-item guard vocabulary. Folding the first into the second would make every
  caller of "create these ten beads" compose a plan.
- **The REFUSALS differ, and a caller dispatches on them.** `batchApply`'s 409
  vocabulary carries `item_index`, `item_kind`, `item_key` and
  `item_issue_id`, because an item is the unit that failed. `batchCreate` has
  no items to name, and inheriting a refusal shape it can never fill would put
  four always-absent members on its wire.
- **BOTH SURVIVE, and that is the part to check.** Each has its own contract,
  its own capability token, its own operation, and a front door that reaches
  it. Neither is a thin wrapper the next reader will ask about.

If a sibling would be left as a shim over the new role — same refusals, same
answer shape, one call through — the honest outcome is one role, not two. Ask
it before writing the leaf, because the answer decides whether you are writing
one contract file or two.

**And one command may be born with TWO roles.** `bd init` is the case:
`Bootstrapper` writes the workspace identity and `InitVerifier` reads it, and
they are separate even though `VersionReconciler` — whose read and write are
also two shapes of one question — is one. The test that separates them is not
"is it the same question" but **can one caller be entitled to the read and not
the write**. bd reads a workspace identity on paths where it is forbidden to
write one: a bts-provisioned team database whose identity the provisioning tool
owns, an authenticating gateway whose credential may be read-only. Handing those
callers a surface with the write on it is the shape that produces writes the
front door then has to suppress flag by flag — `shouldWriteProjectIDLocally`,
`shouldWriteInitStateToDB`, `shouldInitSharedGlobalDB` are three of those. A
capability a caller must not have is a capability it should not be able to
reach.

**Ask what the role's fields' LIFETIMES are before folding them in.** The same
slice teaches this: `bd init` seeds five values, and only two of them —
the prefix and the project id — are written once and adopted forever. The
repository and clone fingerprints, the synced-at marker and the recorded binary
version are refreshed on EVERY init, adopt or not, because a fresh clone of an
already-identified database needs its own. Putting those on a one-time,
refusable write means either a refusal that skips them or a write that is no
longer one-time. They stayed off the role.

## The checklist

Thirteen steps. Steps 1-9 are the role; 10-12 are what makes it the ONLY way
in; 13 is the HTTP surface, which lands with the command rather than after it.

1. **The leaf contract.** `issueops/<role>.go`. Request and result types plus
   the interface, with the doc comment written as a SPECIFICATION — every
   promise a conformance case will cite by line. The package imports
   `internal/types`, `beadserrors` and stdlib only, and exports no
   constructors. A leaf in some OTHER namespace imports `beadserrors` and
   stdlib, and reaches for `internal/types` only if its plane actually holds
   issue-shaped things — `memoryops` does not, and says so.
   Template: `issueops/readyclaimer.go`, `issueops/commenter.go`.

2. **The shared request→filter builder**, if the role takes a filter-shaped
   request. Beside its siblings in `internal/workapi` (`count.go`:
   `BuildCountFilter`, `ValidateCountGroup`). Every implementation builds
   through it, so it is the single definition of what the command means, and it
   is unit-testable with no database (`internal/workapi/count_test.go`).

3. **The store-backed body**, shared by dolt and embeddeddolt:
   `internal/workapi/store<role>/`. Its own package, not a file in
   `internal/workapi` — 22 `cmd/bd` files already import workapi, so a
   constructor there is one line away from any front door, and a front door
   that constructed the role directly would get one stripped of its decorators.

   **When the body needs a TRANSACTION, step 3 moves down instead of out.**
   `storage.DoltStorage` publishes methods, not transactions, so a role whose
   work is several reads that must see one snapshot cannot be written against
   it. `issueops.CycleDetector` is the case: it builds a graph and then hydrates
   the nodes it found. Its shared body is a tx-level function beside its
   siblings in `internal/storage/issueops`
   (`cycle_report.go`: `DetectCycleReportInTx`), and each store package holds a
   five-line accessor around its own `withReadTx`/`withConn`
   (`dolt/cycle_detector.go`, `embeddeddolt/cycle_detector.go`). The unit-of-work
   body reaches the same function through a new method on the domain repository
   and use case. Two wrappers over one function is still ONE vote, exactly as a
   shared `store<role>` package would be, and there is no importable constructor
   for step 12's depguard entry to deny — the body types are unexported and the
   accessor is the only door.

   Put the parts that decide what the answer MEANS in PURE functions either way
   (`CanonicalCyclePaths`, `BuildCycles`), so they are pinned in milliseconds
   without a database and the conformance contract is left to assert what only a
   real backend can show.
   **Unless the role needs ONE TRANSACTION.** A body that must read twice and
   have both reads see one snapshot — an existence probe plus the read it
   qualifies, which is what `Relations` and `EdgeReader` both do — cannot live
   above `storage.DoltStorage`, because that interface publishes no way to scope
   two calls to a transaction. Those bodies are `Validate…`/`Execute…InTx`
   functions in `internal/storage/issueops` (`relations.go`, `edges.go`) and
   each store's accessor wraps them in its own `withReadTx`/`withConn`. dolt and
   embeddeddolt still share ONE body, which is what step 3 is for; only its
   address differs. There is no step-12 depguard entry to add for one of these
   — the shared function takes a transaction, so no front door can call it at
   all.

   **CHECK WHETHER THE UNIT OF WORK CAN REACH THE BODY BEFORE PROMISING THAT IT
   WILL.** `MetadataCAS` and `TreeWalker` collapse all three legs onto one
   `…InTx` function, and it is tempting to read that as the rule for tx-level
   bodies. It is not. Those two take a `DBTX`, which is exactly the method set
   `domain/db.Runner` publishes, so the unit-of-work leg reaches them through
   the domain repository. `issueops.BatchApplier` cannot: its body COMPOSES
   `ExecuteCreate`, `ExecuteUpdate` and `ExecuteClose`, every one of which takes
   a `*sql.Tx`, and a unit of work's runner is a `*sql.Conn` with a transaction
   open on it. No interface between the two publishes the other, and widening
   three of the oldest write paths in the tree to take an interface is not a
   role slice's change.

   So that role has TWO bodies, and its contract says so at the top rather than
   claiming three legs and one reading. **The test is mechanical: does every
   function your body calls take an interface `Runner` satisfies?** Ask it
   before you write the contract header, because the header's vote count is
   what tells the next reader how much a three-leg run is worth.

   **THEN SHARE EVERYTHING THE FORK DOES NOT FORCE YOU TO DUPLICATE, and be
   precise about which half that is.** `BatchApplier`'s two bodies share their
   request VALIDATION and their commit-message rule outright — one function
   each, called from both. Its end gate is the interesting case, because it is
   shared at the LEAF and forked at the ORCHESTRATION: both legs reach the same
   `issueops.CheckBlockingHierarchyInTx` and the same
   `AppendSchedulingGraphInTx`/`CycleThroughEdgesInGraph` walk (the unit-of-work
   leg through two repository methods that delegate to them), so the two cannot
   disagree about what a conflict or a cycle IS — but which edges get collected,
   in what order, and how the refusal is wrapped are written twice. That is the
   shape to aim for and the shape to describe honestly: "the legs share their
   end gate" would be a claim the code does not support, and the next reader
   would trust it.

4. **The unit-of-work body and its source interface.**
   `internal/storage/uow/<role>.go`, declaring `type <Role>Source interface {
   <Role>() (publicops.<Role>, error) }` and implementing the accessor on
   `*doltSQLProvider`. This is the one genuinely independent implementation —
   dolt and embeddeddolt share step 3, so "both stores agree" is ONE vote.

5. **The accessor on the interface.** One method plus its doc on
   `storage.Storage` in `internal/storage/storage.go`. **This is the file every
   role commit touches**, one line each: cheap merges, but not free ones.

6. **The hook wrapper.** `internal/storage/hook_<role>.go`. A WRITE role wraps
   the inner surface and fires its completion hooks
   (`hook_commenter.go`); a READ role recurses and returns the inner surface
   unwrapped, because there is no completion to report (`hook_issue_reader.go`,
   `hook_counter.go`). Either way the accessor must EXIST on the decorator: it
   is declared, never inherited, and there is a reflection test that says so.

   **The read/write split is about the HOOK VOCABULARY, not about whether the
   role writes.** `internal/hooks` publishes on_create, on_update and on_close
   and nothing else, so a write the vocabulary cannot name recurses like a
   read: `issueops.Sweeper` deletes rows and `hook_sweeper.go` is three lines,
   because there is no on_delete to fire and the rows a hook script would be
   handed are gone. Say which it is and why in the file, and assert it in
   `role_accessor_decorator_test.go`'s wrapped/unwrapped table — the whole
   point of that table is that "recurses" reads identically whether it was a
   decision or an omission.

7. **The telemetry wrapper.** `internal/telemetry/<role>.go`. No read/write
   distinction here — telemetry spans reads too, so every method gets
   `storage.op` / `storage.done`.

8. **The two store accessors.** `internal/storage/dolt/<role>.go` and
   `internal/storage/embeddeddolt/<role>.go`, both delegating to step 3. The
   embedded one carries `//go:build cgo`. A nil receiver returns
   `*storage.ErrUnsupported` naming the op.

9. **Every other implementer of `storage.Storage`.** The compiler finds them;
   today that is the `configStore` stub in `internal/jira/tracker_test.go`.

10. **The decorator enumerations.** Both `role_accessor_decorator_test.go`
    files (`internal/storage`, `internal/telemetry`) declare the role on a fake
    store, implement its methods on a shared sentinel, and drive it in two
    tables each — the wrapped/unwrapped table and the error-propagation table.
    In the storage file the fake store also needs its field and that field's
    line in `newRoleAccessorStore`; in the telemetry file the accessor returns
    the single shared surface, so there is no field. Measured against
    `issueops.GraphCounter`'s commit: six edits in the storage file, four in
    the telemetry one.

    **There is NO name list to add to, and there has not been since #5460.**
    `roleAccessorNames` is DERIVED by reflection over `DoltStorage`; the
    telemetry file duplicates the derivation and runs its own declaration
    check, because the storage file's is unexported and in a test package it
    cannot import. That is the whole point of the
    derivation: the decorators embed `DoltStorage`, so a new accessor is
    PROMOTED onto every one of them and everything still compiles, and a
    hand-kept list would simply never hear about it. What still costs you an
    edit is a fake store and a sentinel, because those are types the compiler
    has to see satisfy the interface.

    Add the layering pin in `issue_roles_external_test.go` too: a write role
    expects the hook wrapper outermost, a read role expects the telemetry
    wrapper. And when you touch the storage file's pass-through paragraph — the
    one naming which roles return the inner surface unwrapped — add the role
    there rather than beside it; the paragraph exists so "recurses" reads as a
    decision instead of an omission.

11. **The conformance contract and its three wirings.**
    `backend/conformance/<role>_contract.go` holds the cases and the
    `<Role>Fixture`; the wirings are
    `internal/storage/{dolt,embeddeddolt,uow}/<role>_contract_test.go`. Each
    wiring is a `roleFixtureKit` plus the accessor plus a prefix, with no
    adapter in between — the kit is FROZEN and a role slice does not edit it.
    Every case asserts what the leaf doc PROMISES, cited by line. A backend
    that genuinely disagrees is parked at its WIRING site with
    `skipKnownDivergence`, never by weakening the case.

    A case may need to establish a state the ROLE CANNOT PRODUCE — a recorded
    version below its own high-water mark is one, and reconciling is the one
    thing that can never create it. The kit is frozen and reaches the issues
    and config planes only, so that hook is a short closure built at each
    wiring site over a seam the backend already publishes, the way
    `CycleDetectorFixture.Exec` is. It belongs on the fixture, documented as
    out-of-band, not in the kit.

    **SAY HOW MANY VOTES THE THREE LEGS ACTUALLY ARE, at the top of the
    contract file.** dolt and embeddeddolt share step 3, so most roles are two
    votes; `issueops.TreeWalker` is the first where ALL THREE share one body —
    the unit of work reaches the same `…InTx` function through the domain
    repository — so its three-leg run is ONE reading plus an engine check. That
    is still worth running, because every measured drift in the graph family has
    lived in a WRAPPER, but the cases have to be written for it: assert
    SENTINELS rather than message text and assert a typed error's FIELDS, since
    a wrapper losing a transaction, dropping a request field or breaking
    `errors.Is` is what a per-leg failure would actually be. And pull as much of
    the answer's MEANING as will go into pure functions beside the body, so the
    parts one vote cannot cover are pinned without a database.

    Do not add role cases to `backend/conformance`'s older `RunAll` suite: its
    `Factory` hands back a bare `storage.DoltStorage`, which a unit-of-work
    provider can never be, so a case placed there silently never runs on the
    backend most likely to diverge.

    **TWO GATES NOW FAIL THE BUILD IF YOU SKIP THIS STEP, and both take a
    reasoned waiver rather than silence.** They exist because coverage used to
    be aspirational: `issueops.Importer.ImportBatch` had no contract case from
    the day it was written and nothing noticed.

    - **`TestEveryRoleMethodHasAContractCase`**
      (`backend/conformance/role_coverage_gate_test.go`). Every method of every
      interface the public facade declares must be CALLED by a contract case in
      that package. Both halves of the comparison are derived — the facade
      census from source, the call sites by scanning the package — so a new
      role method reaches this gate on its own. To waive, add an entry to
      `uncoveredRoleMethods` keyed `"<Role>.<Method>"` with a sentence saying
      why the contract tier cannot reach it. The gate is shrink-only in the
      way that matters: it fails on an entry naming no real method AND on an
      entry the package has since covered, so the change that writes the first
      case deletes the waiver in the same diff.
    - **`unwiredContractEntrypoints`**
      (`internal/storage/contract_leg_registry_test.go`). The mirror question,
      per LEG: a registered leg is held to every entrypoint the conformance
      package exports. To waive, call `registerContractLegWaivers("<leg>",
      map[string]string{"<Entrypoint>": "why"})` from an `init()`. Same rules —
      a real leg, a real entrypoint, a reason, and removal the moment it is
      wired — and a second registration of one entrypoint is REFUSED rather
      than merged, so a waiver cannot outlive the reason that justified it.
      A leg part-way through adopting the tier uses `adoptionCeiling` instead
      of hundreds of identical reasons; it is asserted EXACTLY, so it is a
      ratchet and not a budget.

    Legs register from an `init()`, never by editing a literal. That is what
    lets a distribution built on this repository bring its own leg and its own
    waivers in its own file and merge without conflict (#5499).

12. **Both front doors, and the lint that keeps them there.** The CLI handler
    and any HTTP handler call the role and nothing else — no filter, no config
    load, no unit of work opened by hand. Since the owner's 2026-08-05 scope
    decision the HTTP half lands WITH the command: the operation is written into
    `internal/httpapi/spec/openapi.v0.yaml` FIRST and the types generated from
    it (`make api-gen`), never the other way round. Step 13 has the measured
    file list for that half; it is not repeated here, because two copies of one
    count is how the last one went stale.
    Then close the holes behind them in
    `.golangci.yml`: add the step-3 package to the `cmd-bd-role-constructors`
    depguard deny list, and if the command no longer names `types.IssueFilter`,
    REMOVE it from the forbidigo exception list (and decrement the count in the
    comment above it, and in `issueops/reader.go`'s claim, which both state the
    number). Removing an entry there is how a role commit proves its front door
    actually reached the role.

13. **The HTTP half, when the role has one.** The spec is written FIRST —
    `internal/httpapi/spec/openapi.v0.yaml`, then `make api-gen` — and the
    handler is written against the generated types, never the other way round.
    `make api-check` is the gate: it regenerates and fails if regeneration
    changed anything, then runs the spec tests.

    **What an operation costs beside the handler, measured against the two
    freshest wire slices** — `issueops.GraphCounter`'s (#5536) and
    `issueops.Relations`' (#5540), whose full file sets differ (each has its own
    handler, its own handler test and its own proxied integration test) around
    an identical nine-file core:

    | File | Edit |
    |---|---|
    | `spec/openapi.v0.yaml` | The operation, its schemas, and the capability token in `ContextResponse.capabilities`'s prose — that token is hand-edited; `Capabilities()` itself is derived from the route table |
    | `problem.go` | The `Op<Name>` constant and its `operationCodes` row |
    | `routes.go` | The `routeTable` row, carrying the `capability` token |
    | `server.go` | A required `Config` field, its entry in `sourceRoles`, its name in `roleSourceNames`, the `Server` field, the `Listen` assignment, and the per-request accessor |
    | `claim.go` | The `timedProvider` accessor, plus the `uow.<Role>Source` compile-time assertion beside its siblings |
    | `cmd/bd/serve.go` | The `serveRoleSource` method, the `serveIssueRoles` row, the `serveRoles` field, and the `httpapi.Config` literal |
    | `cmd/bd/serve_source_test.go`, `cmd/bd/serve_store_identity_test.go` | The two serve stubs — a COMPILE ERROR since #5539 if you forget, see "the step with no number" |

    Nine files, and the spec tests name most of them if you miss one. Budget
    `server.go` in particular: six lines in one file, and the `Config` field is
    REQUIRED, so every caller and test that built a Config grows a line too.

    **And a tenth both slices paid, which is not in the core because it is a
    test:** `internal/httpapi/roles_test.go` needs the role's fake and its entry
    in `rolesConfig` — about thirty lines each time, and the compiler does not
    ask for them, so it is the one on this list you can finish the operation
    without noticing.

    **A role may honestly have none, and `issueops.VersionReconciler` is the
    one that does.** It answers a startup hook rather than a command: its two
    markers are clone-local and dolt-ignored, so they describe the machine
    holding the database and mean nothing to a remote caller, and the one
    version fact a client can act on is already `ContextResponse.bd_version`.
    Writing that decision down — in the leaf doc, beside the promises, and in
    AMBIGUITIES.md for the owner — is the work. Adding an operation because the
    checklist has a row for one is not.

    `issueops.Bootstrapper` and `issueops.InitVerifier` are the second and third,
    and their reason is different enough to be worth its own sentence: a server
    can only serve a database it is already bound to, and binding it required
    the identity the bootstrapper writes, so the only caller that could reach
    such an endpoint would be asking a running server to identify a workspace
    that is by construction already identified. The read half has no endpoint
    because it already has one — `ContextResponse.project_id` is its answer, on
    the wire, today.

    **The ROLES database source is the part that surprises.** `httpapi.Config`
    carries either a unit-of-work provider or the roles, and the roles are
    required TOGETHER: a new operation served from a new role adds a required
    field, so `checkDatabaseSource` grows a term and every caller and test that
    built a Config grows a line. That is deliberate — a Config missing the new
    role would otherwise bind and nil-dereference on the first request that
    reached the new handler — but budget it, because it is the one edit in this
    list that touches code the role commit did not write.

## What the whole thing costs

`bd count` behind `issueops.Counter`, end to end, in one commit:

| | files |
|---|---|
| new production files | 6 (leaf, builder, store body, uow body, hook wrapper, telemetry wrapper) + 2 store accessors |
| edited production files | 3 (`storage.go`, `cmd/bd/count.go`, `cmd/bd/count_proxied_server.go`) |
| new test files | 4 (contract + three wirings) + 1 builder unit test |
| edited test files | 5 (two decorator enumerations, the root layering pins, the command's own, the `internal/jira` stub from step 9) |
| config | 1 (`.golangci.yml`: one deny entry added, one exception entry removed) |

Counter's commit also touched three files the next role will not: renaming the
depguard rule from `cmd-bd-reader-constructor` to `cmd-bd-role-constructors`
moved a word in `internal/workapi/storereader/reader.go`,
`cmd/bd/show_proxied_server.go` and `issueops/reader.go`. That was the one-off
generalization that made step 12 a one-line edit from here on.

Nine of those are mechanical once the leaf contract is written. The two that
are not, and where the time actually goes, are the leaf doc comment and the
conformance cases — because those are the parts that decide what the role
MEANS, and the parts every later reader trusts.

## Two traps

- **Reach a role through its ACCESSOR, never its constructor.** The accessor is
  where each decorator adds its layer, so a caller that constructed the body
  directly gets it unspanned and unhooked, and the code looks perfectly
  ordinary. This is what the depguard rule in step 12 is for, and it is why the
  uow conformance wirings assert through `provider.(<Role>Source)` rather than
  calling `New<Role>`.
- **Three wirings are not three votes.** dolt and embeddeddolt share step 3.
  Say "two independent bodies plus an engine check" in the commit message, not
  "three backends agree".

## The second namespace

`memoryops.Memories` is the first role in a leaf that is NOT `issueops`: the
`kv.memory.*` plane behind `bd remember`, `bd recall`, `bd forget`,
`bd memories` and `bd prime`'s injection. Steps 1-11 transferred without
argument — same accessor on `storage.Storage`, same two decorators declared
rather than inherited, same three wirings over two independent bodies. What
follows is only where the second namespace had to DECIDE instead of copy,
because that is the list a third one inherits, and each of these reads like a
convention until you notice it was situational.

**Alias the sentinels; do not mint a second vocabulary.** `memoryops/errors.go`
is one line of declaration — `var ErrValidation = beadserrors.ErrValidation`
— and the IDENTITY is the entire point. `errors.Is` against that one
value is what the HTTP problem classifier, `cmd/bd`'s error handling and every
conformance contract already do. A `memoryops`-flavored twin would be a
different value meaning the same thing, so all of those sites would have to
match both forever; and the cost is not the double-match, it is the site that
adds only the first arm and classifies a validation refusal as an internal
error. Re-exporting rather than telling callers to reach through the declaring
package is the other half: code holding only `Memories` can classify a refusal
without discovering a second import.

**Alias DOWNWARD, to `beadserrors`, not sideways into `issueops`.** This is the
part the second namespace got wrong first and then fixed. `memoryops` originally
aliased `issueops.ErrValidation`, which kept identity but put the issue package
in a memory leaf's dependency graph with `internal/types` behind it — a claim
that the memory plane sits downstream of the issue plane, when the two are
siblings over one config table. `beadserrors` now declares the namespace-neutral
vocabulary (`ErrValidation`, `ErrNotFound`, `ErrNotInitialized`, the
`ErrUnsupported` type) and `issueops` re-exports it by alias like everyone else.
`memoryops` depends on `beadserrors` and `context`, nothing more.

The test for what belongs down there: **would a leaf for a plane nobody has
written yet need it?** A request can be invalid, a row can be missing, a
substrate can be uninitialized and a backend can not implement a capability, on
any plane. `ErrCloseBlocked` and `ErrDependencyCycle` name issue concepts and
stay put. `beadserrors` imports stdlib and nothing else — enforced by the
`beadserrors-leaf` depguard rule, not just asked for, because the next tempting
import is always one shared constant that "obviously belongs with the errors".

**One earlier claim here was wrong and is worth keeping visible**: this section
used to say moving the sentinels later would be expensive, "because by then
every `errors.Is` site in the tree points at the `issueops` value and either the
new leaf aliases backwards or every site moves at once." That is a false
dichotomy. A Go alias preserves identity in both directions, so the new leaf
declares canonically and the old home aliases FORWARD to it — 141 `ErrValidation`
references across the tree, and not one of them moved. What actually grows with
each namespace is not migration cost but the number of leaves that have baked in
the wrong dependency direction. Judge that timing on the direction, not on churn
that never materializes.

**No `.golangci.yml` entry, and that is not an omission.** `memoryops` adds
nothing to the `cmd-bd-role-constructors` deny list because there is nothing to
deny: the role needs one transaction for probe-and-act, so step 3 moved DOWN
into `internal/storage/memoryops`, where every function takes a `*sql.Tx` that
no front door holds (`internal/storage/memoryops/memories.go:15-18`). Step 3's
address is what decides the entry, so a namespace of tx-level bodies has no
entries at all, and the absence has to be readable as a decision rather than as
a step someone skipped.

`issueops.BatchApplier` is the second role with no entry, for the same reason
and one more: its step-3 body is an `…InTx` function AND it has no `cmd/bd`
front door at all in the slice that introduced it — it landed with the HTTP
half only. An absent CLI is a decision too, and the place to write it down is
the leaf doc beside the promises, exactly as `VersionReconciler` writes down
its absent HTTP half.

The test: **does step 3 have an exported constructor returning the role
interface, in a package a `cmd/bd` file can import?**
`internal/workapi/store<role>` does, so it gets an entry. A `…InTx` function
does not. What the test is NOT is "does the namespace have a package below the
role" — `internal/memoryapi` is exactly that, holds `DeriveKey`, the two
refusals and the search filter, and is deliberately importable from `cmd/bd`
(`internal/memoryapi/memoryapi.go:7-13`),
because the `bd remember` front door has to derive a key to recognize the
bare-slug case before it knows which method to call. Denying the meaning layer
would be denying step 2, which every role depends on being reachable.
Constructors are the boundary; meaning functions are not.

**A miss is a RESULT FIELD, not `ErrNotFound`, because of a fact about the
substrate.** `RecallResult.Found` and `ForgetResult.Found` carry the miss and
the front doors translate — the CLI to its SilentExit contract, HTTP to its 404
(`memoryops/errors.go:18-29`). The reason is checkable and it is not taste: the
config table this plane rides in CANNOT DISTINGUISH AN ABSENT ROW FROM A ROW
STORED AS THE EMPTY STRING, which `issueops/workspaceconfig.go:42-46` already
states for settings over the same seam. A role answering `ErrNotFound` would be
minting an error out of a distinction it cannot see, and the first out-of-band
empty write turns that error into a lie. The honest, weaker promise is what
shipped: `Found` is `Value != ""`, an empty row and no row are the same answer,
and `List` still enumerates the empty row because its KEY exists — the one way
a caller can tell them apart, stated at `memoryops/memories.go:63-72` and
pinned by a case.

The next namespace should re-run the check rather than copy the conclusion: ask
whether YOUR seam can tell absent from zero-valued. If it can — a NOT NULL
column, a distinct id, a row a delete actually removes — then `ErrNotFound` is
honest and a result-carried `Found` is just a second spelling of it. The shape
follows the substrate, and the substrate is the thing to cite.

**One promise the contract declares UNPINNABLE rather than faking.** Every
implementation promises that the existence probe and the act it qualifies
happen in ONE transaction; that is why step 3 moved down, and it is what makes
`Replaced` and the value `bd forget` prints true statements instead of hopeful
ones. The contract does not test it, and SAYS SO, in the file header beside the
vote count (`backend/conformance/memories_contract.go:51-57`).

The promise is structural, not black-box observable. A single-threaded case
cannot falsify it — one transaction and two produce identical answers when
nothing else is writing — and a concurrent case would be flaky at three
engines, which buys a red suite people learn to re-run rather than a guarantee.
What pins it instead is the SHAPE of the bodies: there is no two-call
composition to regress into without deleting the `…InTx` functions. A green
single-threaded case named after the promise would have been strictly worse
than nothing, because a reviewer greps for the promise, finds a test named for
it, and stops looking. So state the promise in the contract's coverage
paragraph, name the mechanism that actually holds it, and name the probe that
would upgrade it (here: a transaction-counting seam on the fixture kit). Do not
fake it with sleeps.

**And one trap the second namespace hit that a third will.**
`memoryops.Memories.List` and `issueops.Reader.List` are ONE METHOD NAME WITH
TWO SIGNATURES, so no single Go type satisfies both, and the shared sentinel
that stood in for every role in both `role_accessor_decorator_test.go` files
could not implement the new one. Each file needed a second sentinel type
(`internal/storage/role_accessor_decorator_test.go:302`,
`internal/telemetry/role_accessor_decorator_test.go:252`). That part is
mechanical and the compiler drives it.

What is not mechanical is what the split does to the telemetry table, whose
rows fail on `surface == test.inner` — the accessor that recurses
(`return s.Unwrap().Memories()`) instead of wrapping, dropping every span and
timing on the role. Every row compared against one implied sentinel, so a
memories row written the same way would have compared the memory surface
against the ISSUE sentinel: a value it can never equal, wrapped or not. Green
forever, including against the exact regression the table exists for. The rows
now carry a per-row `inner` comparand naming the surface each one must not be
(`internal/telemetry/role_accessor_decorator_test.go:279-282`). **A shared test
fixture a new namespace cannot satisfy is a signal: the row you add beside it
may be a row that cannot fail.**

## The third namespace

`journalops.Journal` is the durable mutation journal's read side: the
seq-ordered replay feed behind `bd events tail`, `bd events export` and
`GET /v0/beads/events`. It is a third leaf rather than a role in either
existing one because its rows are neither beads nor settings — they are
clone-local engine state on a `dolt_ignore`d table, written in the same
transaction as the mutation they describe, versioned by nothing and replicated
nowhere. A plane whose rows deliberately survive no merge has nothing in common
with the plane that holds the merged data, however similar an id column makes
them look.

Most of what the second namespace decided transferred without argument. Four
things did not, and the first two are the ones a fourth namespace inherits.

**A role can have NO ACCESSOR AT ALL, and that is what makes the census a
source parse rather than a convenience.** Every other role in this tree is
handed out by a method on a store or a provider; this one is reached by TYPE
ASSERTION, because the journal is not on `storage.DoltStorage`'s published
surface and a backend is free not to implement it (`cmd/bd/serve.go`,
`serveJournalCursor`). `issueops.Importer` is the precedent and the warning: it
had no contract case from the day it was written and nothing noticed, precisely
because a reflection-only census can only ask about types something already
names. So the demand side is where an accessorless role is added —
`facadePackages` in `backend/conformance/role_coverage_scan_test.go`, one line
— and from there `TestEveryRoleMethodHasAContractCase` treats it exactly like a
role with three accessors. The supply side needs nothing: `#5499`'s per-leg
lock is ENTRYPOINT-scoped, so the six new `Run…` functions were demanded of all
three legs the moment the contract file existed. Check which of the two gates
your role is invisible to before assuming both.

Nothing else in the accessor apparatus applies, and the absences should read as
decisions: no row in either `role_accessor_decorator_test.go`, because there is
no accessor to decorate; no `RoleFiresHooks` entry, because a read fires none;
no `.golangci.yml` deny entry, for `memoryops`' reason — the body is an `…InTx`
function (`issueops.ReadEventsPageInTx`) that no front door can hold, so there
is no constructor to deny.

**Conditional requiredness is a RESOLVED BOOLEAN the caller hands in, never
something the server works out.** `httpapi.Config.EventsJournal` is the one
role field required conditionally — on `Config.EventsJournalEnabled` — and the
flag is separate from the role because it CANNOT BE INFERRED FROM THE DATA: a
disabled journal presents as zero rows and a head of zero, byte-identical to an
enabled journal nothing has written to yet, so a server without the flag would
answer "you are caught up" to a consumer polling a workspace that will never
emit a record. Activation lives in the target workspace's own config and
environment, which `internal/httpapi` resolves none of. If your role is
optional, ask what tells the difference between "off" and "empty"; if the
answer is nothing, the flag is a field and not an inference.

**Alias FORWARD from the old home when a role is carved out of shipped code.**
The second namespace's section above argued that moving a vocabulary later
costs nothing because a Go alias preserves identity in both directions, and
called the opposite claim a false dichotomy. This is that claim measured. Four
names and a constant moved from `internal/storage` into the leaf and the old
spellings became aliases —
`type EventsJournalCursor = journalops.Journal` and its three siblings — and
the whole tree compiled with **no non-test change anywhere else**: not in
`internal/httpapi`, not in `cmd/bd`, not in any of the four implementations,
not in the enterprise sync. `errors.As` against `*journalops.TruncatedError`
matches an error every leg constructs as `*storage.EventsJournalTruncatedError`,
because they are one type. The direction is what has to be right — the leaf
imports `context` and `fmt` and cannot name `internal/storage`, so the canon
goes down and the alias goes up — and the contract asserts the identity at
runtime on three legs rather than leaving it to be argued from the spec.

**A role may arrive AFTER its front doors, and the leaf is where that gets
written down.** The checklist's usual worry is a role whose command or
operation lands later (`GraphCounter`, `BatchApplier`, `VersionReconciler`).
This is the inverse: the CLI, the HTTP operation and four implementations all
shipped first, against a seam in `internal/storage`, and the role was carved
out of them afterwards. What that buys is exactly what a facade-only slice
buys, one step later: ONE place where the promises are stated and three legs
held to them. What it costs is that the promises have to be reconstructed from
working code rather than written before it — so read the bodies for what they
actually do at every boundary (a checkpoint at or above the head; a limit of 0;
a head after a full prune) and put each answer in the leaf doc, because those
are the cases the shipped tests were least likely to have covered.

**And keep the operator's half OFF the role, deliberately and in writing.**
`storage.EventsJournalAccessor` (read plus prune) and
`storage.EventsJournalConfigurer` (per-instance activation) stayed in
`internal/storage` when the read moved out. The entitlement test from `bd init`
applies and comes back loudly yes: `bd serve` documents itself as publishing
the journal and never retaining it, so handing it a delete would make that
documentation the only thing between a consumer's checkpoint and a prune. The
conformance fixture still needs both — the cases have to create records and
manufacture a truncation — so they arrive as fixture hooks with a comment
saying they are the operator surface being borrowed, not part of what is under
test (`backend/conformance/journal_contract.go`, `JournalFixture.Prune`).

## When all three legs share one body

`issueops.TreeWalker` was the first, and `issueops.MetadataCAS` is the second:
the two stores wrap the `…InTx` function and the unit of work reaches the SAME
function through the domain repository. Two things about that arrangement are
not obvious the first time, and both cost a debugging session here.

**The domain seam pulls the meaning layer DOWN, past step 2's address.** The
unit-of-work leg reaches the body through `internal/storage/domain`, so
`domain` has to name whatever request type the body takes — and `internal/workapi`
already imports `domain`. So step 2's package cannot be workapi for this shape:
the plan type and the equality rule live in `internal/storage` instead
(`metadata_cas.go`, beside `ValidateMetadataKey`). Check the direction before
picking step 2's address; the compiler tells you late and the fix is a move.

**"Nothing to VERSION" and "nothing was WRITTEN" are different facts, and the
unit-of-work leg is where conflating them bites.** A tx body naturally returns
the durable tables it changed, for the store legs to stage. An EPHEMERAL write
changes none of them — `ChangedTables.Add` drops the wisp tables on purpose —
so an empty set arrives for a swap that really did write a row. The store legs
survive that: their SQL transaction commits either way and only the Dolt commit
is skipped. The unit of work does not, because its COMMIT MESSAGE is what
commits the SQL transaction as well as what versions it, so an empty message
rolls the write back. Here the wisp case went green on both stores and red on
the unit of work with the row simply absent. Return the two facts separately
(`issueops.MetadataCASWrite`), and make sure a wisp case exists on all three
legs — it is the only case that can tell them apart.

**And one measurement worth copying rather than the conclusion.** Two of this
role's promises turned out to be held one layer down rather than by the code
that reads as if it holds them: the metadata column is a Dolt JSON column that
normalizes on write, so canonicalizing the STORED side is unfalsifiable on every
in-tree backend, and `DiscardNoopIssueUpdates` already suppresses a metadata
write that matches the row, so the body's own no-op short-circuit is
unfalsifiable too. Neither was guessed — each was mutated and watched to stay
green. One of them cost a case that had already been written, wired into three
legs and the bundle, and then deleted, because a green case named for a promise
is worse than no case. Say which mechanism actually holds a promise in the
contract's coverage paragraph, and name the substrate that would make it
observable.

**And validation moves WITH the body when there is only one.** `ExecuteEdgeRead`
leaves `ValidateEdgeReadRequest` to each accessor, because that role has two
bodies and the check belongs to each of them. `issueops.GraphCounter` has one
body on all three legs, so its validation runs INSIDE `ExecuteEdgeCount`: there
is no second implementation for a per-leg check to belong to, and a leg that
forgot to call the validator would be a leg answering a different contract with
nothing to notice it. Ask which shape you have before copying the accessor's
first three lines from a sibling.

**A role whose front doors land later still lands whole, and says so.** The
checklist's steps 1-11 are the role; steps 12 and 13 are the front doors.
`issueops.GraphCounter` shipped with NEITHER — no `bd` command and no HTTP
operation — because the numbers it answers are already printed through
`internal/workapi`'s detail seam, which is shared with an HTTP handler and
therefore moves in a change with its own parity argument, and because the wire
operation was separately gated. That is `BatchApplier`'s absent CLI and
`VersionReconciler`'s absent HTTP at the same time, and it is only legible as a
decision if the leaf doc says it: what a facade-only slice buys is ONE place
where the role's rules are stated and held to on three legs, before either
surface has to agree with the other.

**Then go back and update the leaf when a front door lands**, because the same
sentence that made the absence legible makes it a lie the moment it stops being
true. `GET /v0/beads/dependencies:count` landed in #5536 and
`GET /v0/beads/issues/{id}/related` in #5540; the first left `graphcounter.go`
saying it had "no HTTP operation either" and promising a bound "the graph-counts
wire slice sets" in the future tense, over a slice that had already set it. A
facade-only leaf carries a claim with an expiry date on it. The wire slice owns
the expiry.

## When the role's answer carries a JSON value

Two traps, both found in review of `issueops.MetadataCAS` and both invisible to
every test that asserts on wire BYTES.

**A `*json.RawMessage` wire member cannot READ a present `null`.** `encoding/json`
answers a JSON null against a pointer by setting the pointer to nil, before any
`UnmarshalJSON` runs — so a generated client decoding `{"current":null}` gets
exactly what it gets for a response with no `current` member at all. If those
two mean different things on your operation, the generated client cannot tell
them apart and no round-trip test on the server notices, because the server's
own handler reads raw members. The fix is
`x-go-type-skip-optional-pointer: true` beside the `x-go-type`, which makes the
member a bare `json.RawMessage` — an `Unmarshaler` in its own right, so it
receives the literal, while an omitted member still leaves it nil and
`omitempty` still omits it on the way out. **The wire does not change in either
direction**, which is why nothing else catches the regression: add a test that
decodes a present null INTO the generated struct, or the next `make api-gen`
takes the fix away. Add `nullable: true` too — the document is OpenAPI 3.0.3 and
a validating gateway is entitled to reject a legitimate null without it.

**Do not justify a rule with a precision the SUBSTRATE does not keep.** This
role shipped with a comparison rule defended, in three places, as protecting
int64s past 2^53 from a float64 round-trip. The metadata column is a
go-mysql-server JSON column that decodes numbers through float64 itself:
measured, `9007199254740993` stores as `...992`, `1.0` as `1`, `-0.0` as `0`,
and `1e300` as three hundred and one digits. The defense was against a loss that
had already happened one layer down. Two consequences worth generalizing:

- **Measure the column before writing the promise.** One throwaway probe that
  seeds a row and reads the raw bytes back settles it in a minute, and the same
  probe is what tells you whether a canonicalization is observable at all.
- **A result value the caller feeds back must be READ, not echoed.** Answering
  with the request's own bytes made the "what you get is what a later read sees"
  promise false and left the documented retry loop unable to converge on any
  value the store renormalizes. Re-read it inside the deciding transaction — one
  extra SELECT — and say in the leaf that the caller composes its next
  expectation from that value and not from its own spelling.

**And the step with no number: every surface that EMBEDS the store or a use
case.** A new accessor arrives on each of them PROMOTED rather than declared, so
the build stays green and the first symptom is a nil dereference in somebody
else's stub. `issueops.MetadataCAS` was caught by CI in four such places after
passing every package test its own slice ran: `storage.RoleFiresHooks` (a role
whose hook decorator WRAPS must gain a case, or `checkDatabaseSource` cannot
refuse a hook-firing one — missing it is a `bd serve` that runs a user
subprocess per call); `uow`'s notifying wrapper, in BOTH halves — the recording
use case, which silently records nothing for an inherited method, and the
notifying provider, whose missing accessor makes a caller's type assertion stop
matching; and two `cmd/bd` stub stores that embedded `storage.DoltStorage`. Grep
for the embed, not for the interface.

**A READ ROLE IS THE ONE THAT BITES, and the reason is the decorator, not the
role.** A role whose hook decorator WRAPS produces a surface the caller holds
instead of the stub's, so a missing stub accessor is only reached to build the
wrapper. A read role's decorator RECURSES — `hook_counter.go` and
`hook_graph_counter.go` are three lines each, because nothing completed and
there is no hook to fire — so the call lands on the STUB itself, and a stub
that promoted the accessor off a nil embed dereferences nil right there. Both
roles this actually happened to, `issueops.Counter` and
`issueops.GraphCounter`, are reads.

Neither was found by reasoning about it. `Counter` surfaced only once `bd serve`
began binding the role, and `GraphCounter` (#5508) surfaced as a panic in
`TestServeIssueRolesComeFromBeneathTheHookDecorator` on a full-package CI
shard — a test about hook peeling, which no `-run` pattern anyone reaches for
names, so local runs never touched it. **The sharpened rule: a role bound in
`serveIssueRoles` must be DECLARED on the serve stub store in the same commit.**

Those two `cmd/bd` stubs are the one place this step is now taken FOR you.
`serveIssueRoles` asks for `serveRoleSource` — the accessor subset it actually
reaches — and both stubs declare that subset, assert it with
`var _ serveRoleSource = (*serveRolesStore)(nil)`, and embed NOTHING, so there
is no promotion to hide behind. A role added to the loop is a compile error
naming the missing method instead of a segfault inside role extraction.
`issueops.Relations` (#5540) is the first role added since, and it measured the
return: `*serveRolesStore does not implement serveRoleSource (missing method
IssueRelations)`, at build time, in the file that has to change. Every other
surface above is still yours to grep for.
And note that `internal/storage/uow`'s own
package run is nine minutes — the parity guards live there, so a role slice that
runs only its contract on the three legs has not run them.

## Retiring a test against a contract

Once a role has a contract, the ad-hoc tests that predate it start to look
redundant. Some are. A four-slice pass over ~78k LOC of backend tests retired
about 700 lines net. Along the way the slices and their reviews found **about a
dozen coverage losses and six tests that could not fail**, every one of them
green under `go build`, `go vet`, `go test` and `golangci-lint`. Two of the six
could not fail by construction — unconditional `t.Skip` bodies with no
assertion. The other four are the harder class, and most of what follows is
about them. Treat the counts as an order of magnitude: the per-slice tallies
live in the pull requests, and review revised them more than once.

**A mutation verdict is only true of the body you mutated.** This is the most
productive rule here — it produced confirmed losses in more than one slice
independently, and reviews kept finding it after it had been written down. A
test is broken,
the contract goes red on the same break, the verdict reads REDUNDANT — and the
deleted test was watching a different body. `uow` runs its own bodies under
`internal/storage/domain/` where the two store backends share others; a store
wrapper composes shared functions *itself* while the role path reaches the same
functions through `runIssueOperationTx` and never calls the wrapper at all.
Before
accepting red/red: **name the body the deleted assertion observes, and confirm
your mutation was in it.** Nothing automates this.

**A wrapper is a composition, and the contract cannot see it.** `DoltStore` and
`EmbeddedDoltStore` expose `UpdateIssueChecked`, `CloseIssueChecked` and
`ClaimIssue` — published on `storage.DoltStorage`, with option types in
`beads.go` — which assemble the shared bodies on their own. Deleting their only
observers left `ExpectedStatus` routed on no branch at all, and disabling a
wrapper's compare-and-set passed every contract case on that backend. When a
wrapper has
no other caller, keep a narrow routing residue: see
`internal/storage/dolt/checked_wrapper_smoke_test.go`.

**Write a residue from the decision surface, not from the mutants you ran.**
The slice that kept that residue still lost six things, because it wrote the
file from the two breaks it had already measured. A review broke eight more.
Half were POSITIVE assertions — a wrapper that refuses correctly and *never
writes* passed the entire suite. Refusal-only coverage of a guarded write is
half a test. Enumerate each branch and each option, and ask of each whether
anything would notice it going away.

**Ask what the fixture makes unobservable.** The four fixture-defect cases had
nothing wrong with their assertions — every one was correct. One seeded `is_blocked=1` with no
blocker edge, so the guard short-circuited and the case could never fail on the
term it was named for. One used ready ids `1` and `10`, whose natural, lexical
and query orders are identical, so it could not see a dropped sort. One
collided a key in two patch stages that a *later* stage removed — and a key any
later stage removes cannot witness the order of the stages before it. Every
case earns its place by going red against a mutation of its own subject.

**Never let a role-answer assertion replace a raw-row one.** Reading a value
back through the role is exactly the check that passes on a corrupted table.
`is_blocked` is the standing example: derived *and* persisted, so a case that
only asks the role whether an issue is blocked passes on a backend that never
denormalizes. The lifecycle and batch-closer contracts read the raw column for
that reason, and `lifecycle_close_reopen_contract.go` says so at the read.
Several of those readers were added by the pass described here, precisely
because the role answer could not see the defect.

**Cite promises by symbol, not by line.** Two sweeps had to re-resolve 23 stale
`file.go:line` citations across two contract files, drifted by growth above the
cited region. Wrong ones look exactly like right ones, so a slice copied one
into new prose and minted a second beside it. `memoryops` cites by symbol name
and its citations still resolve.

`scripts/mutation-equivalence.sh` runs the red/red comparison in a disposable
worktree and refuses the four ways this measurement lies: a `-run` matching
nothing (`go test -run NoSuchTest` exits 0), a mutation changing no bytes, a
baseline that is not green, and a result grep that over-anchors leading
whitespace — `go test -v` indents subtests by four and nested subtests by
eight.
