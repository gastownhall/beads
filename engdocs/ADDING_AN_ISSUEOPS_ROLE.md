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
    files (`internal/storage`, `internal/telemetry`) list the roles by name in
    `roleAccessorNames`, declare them on a fake store, implement them on a
    shared sentinel, and drive them in two tables each. All five places, in
    both files. Add the layering pin in `issue_roles_external_test.go` too:
    a write role expects the hook wrapper outermost, a read role expects the
    telemetry wrapper.

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

12. **Both front doors, and the lint that keeps them there.** The CLI handler
    and any HTTP handler call the role and nothing else — no filter, no config
    load, no unit of work opened by hand. Since the owner's 2026-08-05 scope
    decision the HTTP half lands WITH the command: the operation is written into
    `internal/httpapi/spec/openapi.v0.yaml` FIRST and the types generated from
    it (`make api-gen`), never the other way round. That costs four more edits
    the spec tests will name if you miss one — a row in `routeTable`, an
    `Op<Name>` constant and an `operationCodes` row in `problem.go`, and the
    capability token in `ContextResponse.capabilities`'s prose. A role served
    from the store-shaped `Config` source also becomes a required field there,
    because `checkDatabaseSource` refuses a half-set source rather than letting
    a handler dereference nil on a live server.
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
    An operation costs four more edits beside the handler: the `Op*` id and the
    `operationCodes` row in `problem.go`, the `routeTable` row and its
    `capability` token in `routes.go`, and that token in the document's
    `capabilities` vocabulary. `make api-check` is the gate.

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
