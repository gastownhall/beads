# `bd serve` — the v0 HTTP surface

`bd serve` answers the same work surface the CLI answers, over HTTP, for
automation clients and orchestrators that would otherwise fork a `bd`
subprocess per call. It exists because the subprocess-per-call shape has two
costs a long-running client cannot pay down: process startup on every read, and
a contract made of stdout text that clients end up parsing.

This page describes the contract and the decisions behind it. For running one,
see [SERVE_RUNBOOK.md](../SERVE_RUNBOOK.md).

Source of truth, in order:

- `internal/httpapi/spec/openapi.v0.yaml` — the wire contract. Types are
  generated from it (`make api-gen`); `make api-check` fails a change that
  edits one without the other.
- `internal/httpapi/doc.go` — what is live in this build and how the anti-drift
  properties are enforced.
- `issueops/reader.go` — the read role both front doors reach.

Nothing here may contradict those three. Where this page summarizes, it says
so.

## The surface

Six operations, all under `/v0` except liveness.

| Operation | Route | What it answers |
|---|---|---|
| `health` | `GET /healthz` | Process liveness. Never touches the database. |
| `getContext` | `GET /v0/beads/context` | Workspace and API identity, from a startup snapshot. |
| `listReadyWork` | `GET /v0/beads/ready` | Unblocked open work, in the requested sort order. |
| `listIssues` | `GET /v0/beads/issues` | A page of issues under the request's filters. |
| `getIssue` | `GET /v0/beads/issues/{id}` | One issue's detail view. |
| `claimIssue` | `POST /v0/beads/issues/{id}:claim` | Compare-and-set claim for a caller-named actor. |

`GET /v0/beads/context` reports which operations this build actually
implements, in `capabilities`. That list is derived from the registered
handlers rather than hand-maintained, so a release cut mid-slice cannot
advertise an operation that does not work. Clients probe `capabilities` for
operation-level support and a 400 `invalid_argument` /
`reason: "unknown_parameter"` for per-parameter support; there is no
finer-grained capability document, and there is deliberately no field
advertising the bind mode.

Response bodies marshal `internal/types` values directly. There is no wire
struct, so the CLI's `--json` output and these bodies are one compatibility
domain — which cuts both ways: a serialized field on `types.Issue` cannot be
renamed or removed without breaking the HTTP contract, not just the CLI's.

### What the reads share with the CLI

The reads hold no query logic of their own. Each decodes its parameters and
hands the whole request to `issueops.Reader` — the same role, reached the same
way, that `bd show --json` reaches on a store. Filter construction, the
workspace config it depends on, the default limits and the wisp fallback all
live inside that role, so a handler cannot half-perform that construction and
answer the same question a different way.

The exact scope of that property, and its limits, are stated in
[The claim](#the-claim) below. It is a narrower property than "the CLI and the
API cannot disagree", and the difference matters.

## The error-code vocabulary

Every non-2xx byte is an RFC 9457 `application/problem+json` document. There is
one error shape. The mapping from sentinel error to status and code lives
entirely in `internal/httpapi/problem.go` and is matched exclusively with
`errors.Is`/`errors.As` — never `err != nil -> status`.

`code` is the machine-readable member and the only member a client may dispatch
on.

| Code | Status | Meaning | Recovery |
|---|---|---|---|
| `invalid_argument` | 400 | Request validation refused it. Carries `param` and `reason`. | Send something different. Never retry. |
| `invalid_cursor` | 400 | A cursor this server did not issue, cannot decode, or issued under a different encoding version. | Restart paging with no `cursor`. |
| `not_found` | 404 | No issue or wisp with that id. | — |
| `already_claimed` | 409 | Another actor holds the claim. Carries `assignee`. | — |
| `not_claimable` | 409 | The issue is not in a claimable state. Carries `issue_status`. | — |
| `busy` | 503 | Retryable contention: the transaction retry budget was spent, or the in-flight limit was saturated. Carries `Retry-After`. | Retry after the header's delay. |
| `db_unavailable` | 503 | Retryable connectivity failure reaching the database. Carries `Retry-After`. | Retry after the header's delay. |
| `internal` | 500 | Anything else. | — |

`reason` splits the two client postures behind a 400 `invalid_argument`, so
telling them apart never requires parsing `detail`:

- `unknown_parameter` — this server does not know that parameter. Version skew;
  the client degrades or falls back.
- `invalid_value` — the server will not act on that value: malformed, outside
  the vocabulary, or legal-but-refused in this server's configuration.

The vocabulary is a one-way door. Renaming or removing a documented
status+code pair breaks the wire; adding one does not, which is why clients are
told to default-branch on unknown codes within a status class.
`TestSpecStatusCodesMatchHandlerTable` asserts set-equality in both directions
between the handler table and the spec, so an undocumented emission and an
unemittable documented status both fail CI.

Two rules govern what a problem body may say. A 5xx `detail` is a fixed string
per code, whatever the underlying error was: driver and dial errors routinely
embed the DSN, and query errors can carry SQL fragments, so a verbose 5xx
detail becomes an information-disclosure channel the moment the server is bound
with `--allow-non-loopback`. A 4xx `detail` stays specific, because it reflects
the caller's own input back rather than server state. The real error goes to
the server log, correlated by the `request_id` the 5xx body carries — that id
is the client's only handle on the one log line that has it.

### Why typed conflicts matter for adopting clients

A client that classified claim conflicts by substring-matching error text
("already assigned to", "claimed by") should switch to the typed 409 code. The
`assignee` and `issue_status` extension members come from a read inside the
losing transaction, never from parsing fragments out of the sentinel's message.
That substring classification is exactly what an adopting client gets to
delete, and it can only delete it because the server never does it either.

## The cursor contract

`GET /v0/beads/issues` pages with an opaque keyset cursor.
`GET /v0/beads/ready` does not: the ready sort policies admit no keyset
predicate, and the intended usage there is snapshot-and-requery.

Four properties, all normative:

**Opaque.** Clients must not construct, parse or mutate a cursor. The encoding
is server-private and version-prefixed. It is base64 of a small JSON object,
which is legible enough that someone will read it — so the contract is enforced
by the version prefix rather than by obscurity. A client that mints its own
token gets `invalid_cursor` the moment the encoding moves.

**No lifetime.** The token carries a position and a private encoding version,
and nothing else. The server keeps no state for it, so it does not expire, does
not become invalid across a restart, and is not tied to the connection that
issued it. The only thing that invalidates one is an encoding change.

**One recovery.** Every failure mode — wrong version, undecodable base64,
malformed JSON, an empty position — is the same answer, because it is the same
client situation: restart paging with no `cursor`. Re-sending the value cannot
succeed.

**Misuse is not detectable.** Because the token carries no filters, a page
fetched with a cursor minted under different filters is *not* refused. The
server applies the current request's filters from the old request's position,
silently skipping every row the new filter set would have placed before it.
Repeat every filter verbatim for the whole traversal, and start a new traversal
when they change.

That last property is a deliberate trade. Embedding the filters would make the
token a second, opaque copy of the request that can disagree with the request
itself; keeping it a bare position makes the failure mode a documented client
obligation instead of a hidden server-side reconciliation.

## The loopback posture

v0 has no authentication and no TLS. The trust model is the loopback boundary
— the same boundary the database behind it already relies on.

`--addr` defaults to `127.0.0.1:0`. The host must be a numeric IP literal.
`--allow-non-loopback` is the operator decision to bind beyond loopback; it is
never taken by default, it prints a warning naming what it exposes, and nothing
else about the server changes. Every peer that can reach the address gets full
read and claim access.

Three things bound the posture regardless of bind mode:

**The Host allowlist**, which has no off switch. An unauthenticated service on
loopback is reachable from any browser on the host, and a page that re-resolves
its own name to `127.0.0.1` issues requests the browser treats as same-origin,
so no CORS rule stops them. What the browser does preserve is the attacker's
hostname in `Host`, which is what this rejects. Every bind answers to the
loopback spellings and to the bound address itself. A *wildcard* bind (`0.0.0.0`,
`::`) has no single configured address, so it answers to any numeric IP literal
and still refuses foreign DNS names — a rebound page cannot produce an
IP-literal `Host`, because the browser sends the hostname from the attacker's
URL. Matching is on parsed addresses, so every spelling of an allowed address
is allowed.

**The JSON-only content type on the claim**, which is a CSRF control rather
than pedantry: a JSON content type is not CORS-"simple", so a cross-origin
claim always triggers a preflight this server never approves. Accepting
`text/plain` or a form encoding would let a page skip the preflight and drive
the one write on this surface from any browser on the host.

**The mode-dependent refusal of an unlimited read.** `limit=0` means unlimited
on both list operations, exactly as `bd list --limit 0` does — except under
`--allow-non-loopback`, where it is refused with 400 `invalid_argument`,
`param: "limit"`, `reason: "invalid_value"`. An unlimited read buffers the
whole active set and its JSON encoding inside one shared process, which must
not be reachable by arbitrary network peers. The bind mode is deliberately not
advertised in `ContextResponse`: a client that wants an unlimited read asks for
one and, on that 400, re-issues with an explicit limit and pages with `cursor`.
It is a client-side fix, never a retry.

An `actor` on an HTTP request is caller-asserted provenance for the audit
trail, not authenticated identity — the same thing it has always been on the
CLI, where any local process can pass any `--actor`. The claim's
compare-and-set is therefore a correctness fence against *concurrent* claims,
not an authorization boundary: it guarantees that two racing claimants cannot
both win, and guarantees nothing about who either of them really is.

## No hooks

Hooks do not fire on an HTTP claim. A CLI claim runs `on_update`; this does
not.

A hook is a user-controlled subprocess per mutation. In a concurrent server
that is an unbounded latency multiplier and an orphaned child at shutdown, and
the working-directory-derived hook lookup that finds them is meaningless in a
server process that does not share the client's working directory.

This is a contract statement, not a gap to be closed later. A client that needs
hook side effects on a claim runs the claim through the CLI.

## No auto-commit

The per-command auto-commit, export and push maintenance that wraps a CLI
invocation does not run here. Durability is per request: a successful claim
commits inside its own transaction, exactly as a proxied CLI claim does today.

Two consequences worth stating for an adopting client:

- There is no end-of-process flush. Anything the server did is already durable
  when the response is written, or it is not going to be.
- An idempotent re-claim by the current holder writes no commit. The
  compare-and-set matched no row because there was nothing to change, and an
  empty commit message tells the transaction runner to skip the commit — so a
  polling client cannot mint an empty storage commit per call.

## Claim throughput

The claim is the only mutation in v0, and its cost is a storage commit. Sizing
follows from that, not from HTTP:

- **A claim that changes something commits.** Its throughput ceiling is the
  store's write path, which serializes commits, and not the request pipeline in
  front of it. HTTP concurrency does not raise that ceiling.
- **Contention surfaces as a retryable 503, not as a stall.** The transaction
  runner spends a serialization-retry budget internally; exhausting it produces
  `busy` with `Retry-After: 5`. That delay is deliberately not one second: the
  budget already spans many seconds of observed write contention, and a
  one-second comeback invites a convoy of retries that each hold a database
  slot while they wait, starving reads exactly when the server is busiest.
- **Saturation surfaces as the same code with a shorter delay.** A request that
  cannot get a database slot within the bounded wait is also `busy`, with
  `Retry-After: 1`, because slot pressure clears quickly. Shedding load
  introduces no new status vocabulary — one code, two delays, and the header is
  the thing to obey.
- **An idempotent re-claim costs no commit** (above), so a client polling to
  confirm it still holds a claim does not consume write throughput.
- **Reads are bounded by slots, not by commits.** Every database-touching
  handler holds one of a fixed number of in-flight slots, and each slot pins one
  SQL connection. The arithmetic is in
  [SERVE_RUNBOOK.md](../SERVE_RUNBOOK.md#connection-budget).

Wisps are not claimable over v0. The claim dispatches to the issues table only,
so a wisp id answers 404.

## The claim

Carried across from `internal/httpapi/doc.go` and `issueops/reader.go`, which
are the source of truth for it. Stated once and in full so it can be checked
sentence by sentence, and deliberately not strengthened here.

**SHARED.** All three issue reads on this surface go through `issueops.Reader`,
and so does `bd show --json`'s detail view on both its routes. `bd list` and
`bd ready` are *not* on the role and share instead the request types, the two
builders in `internal/workapi` that their golden files pin, and
`workapi.FinishPage` — `bd list` on both routes in every mode but the
hierarchical `--parent` tree, `bd ready` on its proxied route only.

**ENFORCED, and by what.** depguard (`httpapi-transport-boundary`) denies
`internal/workapi` from every non-test file of `internal/httpapi`, so no builder
is callable there; a forbidigo rule denies naming `types.IssueFilter` or
`types.WorkFilter` there at all, so no filter is writable there either. Both
are directory-scoped with no per-file exception, so a file added to that package
tomorrow is covered the moment it exists. That same forbidigo rule covers
`cmd/bd` deny-by-default with 64 named exceptions, so the files implementing
`bd list` and `bd show` cannot write a filter, and neither can a file they are
split or renamed into unless the new name lands on that list.

**NOT ENFORCED.** The rule forbids *naming* those types, not holding a value,
so the property is "no filter is written there", not "every filter there came
from a builder". Test files are exempt from both rules, because the oracles hold
filters in order to inspect them. `bd ready`'s files are among the 64, since its
listing and `--claim` are handed the filter itself and the blocked-issue views in
those files name one directly, so it is guarded by the builder and the golden
files and not by the linter. `GET /healthz` and
`GET /v0/beads/context` are not issue queries and are on no role. `bd ready`'s
direct route and `bd list`'s hierarchical tree run epilogues of their own. And
none of this is a merge gate: the rules run in `make ci-pr-lint` on every pull
request and aggregate into the `ci-gate` job, but main carries no branch
protection beyond deletion and non-fast-forward, so no check is GitHub-required
and a red gate binds by convention.

Closing the rest needs more roles — a claim role, an explain role — not more
methods on the read one.

## Workspace modes

`bd serve` refuses exactly one workspace mode, permanently: embedded Dolt.
There is no unit-of-work provider for that backend and there will not be one.
Its commit protocol runs outside the SQL transaction on a separate connection,
so the per-request atomicity this contract states would be a lie there even if
a provider were written. The refusal names the workspace and what serve needs,
and promises nothing further.

Every mode with a SQL server behind it is served: proxied (managed or
external), and server, external-server and shared-server. In the latter three
the root command has already opened a `DoltStore` that serve never uses; serve
builds its own unit-of-work provider from the same connection settings. That
idle store matters only for the connection budget — see the runbook.
