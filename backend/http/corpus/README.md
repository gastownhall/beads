# Served command corpus

This suite is the working definition of "bd fully supports a remote HTTP
backend". It runs every distinct `bd` command shape that an orchestrator (gc)
and its packs and formulas issue against a work store. Each shape runs
against a served workspace and is compared with the same command on an
embedded workspace.

## What runs

The shapes are dealt round robin, in corpus order, across `corpusParts`
parts (`corpus_test.go`). Each part is a top-level test,
`TestServedCorpusPart1` .. `TestServedCorpusPartN`, and each does the
following for its share of the shapes:

1. Builds `bd` from this tree. `BEADS_TEST_BD_BINARY=<path>` reuses a prebuilt
   binary instead.
2. Starts an in-process `bd serve` (`httpapi.Listen` over embedded Dolt) the
   way a deployed server is bound: `AllowNonLoopback` plus a bearer token
   file. The socket is still `127.0.0.1`, but the bind mode decides every
   behaviour the server keys on, including its refusal of unlimited reads.
   Its roles come through the external-dependency policy decorator, and it
   advertises `policy.external_dependencies`, as `bd serve` composes them.
3. Puts a reference proxy in front of the server. The proxy makes it answer
   like the deployed reference server described in
   `testdata/reference_server.json`:
   - the context handshake publishes exactly that server's capability set and
     its `ContextResponse` members (so no `wire_revision`);
   - an operation the reference server does not publish gets the server's own
     404;
   - a query parameter or top-level request member it does not declare gets
     the server's own 400 `unknown_parameter`.
4. Runs `bd connect` on one workspace and `bd init` on another. Both get the
   `seed` from `testdata/corpus.yaml`: one graph of named rows, plus bulk
   graphs that put more than one server page of ready rows and wisps in the
   store. Both sides use `BEADS_ACTOR=worker` and set `CLAUDE_SESSION_ID`.
   The embedded side is initialized and seeded on its own goroutine while
   steps 2 and 3 run and the served side is seeded (`prepareSide`): the two
   sides share no directory, env or process, and each runs the seed in
   order, so the seeded data does not depend on the overlap. The served side
   then runs one `bd ready` to warm its policy cache (see "Wire
   transcripts").
5. Runs each of its shapes as a subtest, on both sides. The embedded run must
   exit with the shape's `expect_exit` and match its `expect_stderr`. If it does
   not, the shape is wrong or bd changed, and the subtest fails as a corpus
   bug that no pending mark or allowlist entry hides. The served run must:
   - exit like the embedded run;
   - carry no refusal (`ErrUnsupported`, a v0-wire refusal, the
     loopback-only 400, or a reference-profile 404/400);
   - match the embedded output under the shape's `compare` mode, after
     timestamps and revision tokens are dropped (but not `defer_until` or
     `due_at`: a shape sets those itself, as absolute dates, and they must
     match exactly);
   - for writes, give the same JSON from every `verify` read on both sides
     afterwards;
   - send exactly the requests in its wire transcript,
     `testdata/transcripts/<shape>.json`. This holds for a pending or
     allowlisted shape too: a mark excuses a shape's outcome, never its wire.

A part takes its own server, workspaces and seed, so no shape can see
another part's writes. That is safe because no shape depends on another
shape's effects:
- every check compares the served run with the embedded run after the same
  commands, so it holds whatever ran before it on both sides;
- each part keeps corpus order, so its reads still run on the bare seed,
  before any of its writes;
- each write owns its target ids (`e2e-t-*`, or ids it creates).

`TestCorpusPartsDealEveryShapeOnce` checks that every shape is in exactly one
part, in corpus order. A new shape must keep that independence: it may rely on
the seed and its own `setup`, never on another shape having run.

Every part seeds again, so running all the parts in one process takes
longer than one run over every shape did; each part logs its setup time
(`part N setup ...`). Run one part with
`-run 'TestServedCorpusPart2$'`.

There are four parts. In the embedded lane each part's setup (server start
plus both seeds, run concurrently) takes ~62-76s and the shapes ~242s in
total. Four parts ran 114-146s each and three ran 170-180s. Three would save
one setup (~75 test-seconds a run), but the longest part is the lane's
served long pole and sits on the CI gate's critical path, so four parts win.
To change the count, update `corpusParts`, `servedCorpusParts`,
`served_pinned_shards.txt` and the target's `shard_count` together.

```
CGO_ENABLED=1 BEADS_TEST_EMBEDDED_DOLT=1 BEADS_HTTP_TEST_REQUIRED=1 \
  go test -tags gms_pure_go -run 'TestServedCorpus|TestCorpus' ./backend/http/corpus/
```

In CI, `//backend/http/corpus:corpus_served_test` runs the suite with both
variables set, in the Embedded Dolt lane (`--config=embedded`; see
[engdocs/TESTING.md](../../../engdocs/TESTING.md)). Its
`served_pinned_shards.txt` pins part k to Bazel shard k, so the parts run
concurrently. `corpus_test` on its own sets neither variable, so it skips
the parts.

`TestServedCorpusCurrentCapabilityTranscripts` runs a handful of shapes a
second way; see "Wire transcripts".

Two environment variables change the run:
- `BEADS_CORPUS_REPORT=<file>` writes a JSON summary with per-shape status
  and request counts.
- `BEADS_CORPUS_SKIP_PENDING=1` skips pending shapes without running them.

## The data

| File | Holds |
|---|---|
| `testdata/corpus.yaml` | assets, the seed, and the shapes |
| `testdata/allowlist.yaml` | permanent refusals, each with its reason and the refusal text the served run must match |
| `testdata/reference_server.json` | the reference server's wire surface |

Each shape in `corpus.yaml` has these fields:

| Field | Meaning |
|---|---|
| `id` | `gc-*` for orchestrator subprocess shapes, `mc-*` for pack/formula/prompt shapes |
| `source` | where the shape comes from |
| `args` | the argv; `{file:NAME}` expands to an asset path and `{rev:ID}` to that side's current revision of `ID` |
| `stdin` | text piped to `bd` on both sides |
| `setup` | commands run on both sides before the shape; one that fails fails the shape |
| `compare` | `json` (default), `ids`, `count`, `text` or `exit` |
| `ordered` | under `json` and `ids`, array order is significant; by default arrays are sorted before comparing |
| `prefix_of` | for a limited `ids` listing: its unlimited form. Each side's limited rows must be a prefix, in order, of that side's own unlimited listing, both sides return as many rows, and the two unlimited listings hold the same ids. A limit that cuts through a tie set (same priority, `created_at` in the same second) lands on different rows on two workspaces seeded seconds apart, so the limited rows are not compared across sides directly |
| `verify` | reads run after the shape |
| `ignore_fields` | JSON keys dropped, at any depth, before a `json` comparison and before `verify` output is compared; for example comment ids, which each workspace mints itself |
| `expect_exit`, `expect_stderr` | expected outcome on both sides (exit 0 by default). An allowlisted shape must state `expect_exit`; only its embedded run is held to these |
| `pending` | a known failure |
| `requests` | exactly how many requests the served run of `args` makes through the reference proxy, not counting `setup`, `verify` or `{rev:ID}` reads, nor any request without bd's User-Agent. Each request is a sequential round trip, so the count is the shape's latency floor over a gateway. A change that makes the shape cheaper updates the pin. The pin must equal the length of the shape's transcript (`TestCorpusRequestPinsMatchTranscripts`), which is how a pending shape's pin is held |

`pending: {slice, reason}` marks a known failure and names the slice or
slices that fix it, for example `S6c+S15`. A pending shape **still runs**:
- while it fails, the subtest is skipped and the skip records what was
  observed;
- once it passes, the subtest fails with "now passes; remove the pending
  mark".

So removing a mark is how a slice proves itself, and any other failure is
unexpected and fails the suite.

## Wire transcripts

Every shape's served run of `args` is recorded, request by request, and held
to `testdata/transcripts/<shape>.json`. Each request is one line: the method,
the `operation_id` it matched in the reference profile (`""` for a request
the proxy answered with its unrouted 404), the path, the sorted query
parameter names, the sorted top-level request member names, and the status.
Values are never recorded, so no timestamp, revision token or cursor can make
two runs differ. The path is the one value kept: an id the corpus does not
name anywhere (one the run minted, such as a gate's hash id) becomes
`<minted-N>`, numbered in the order the run first sent it.

What counts as a request:
- Only requests with bd's User-Agent (`bd/<version> bd-http-backend/v0`).
  Other processes on the host do probe the proxy's loopback port (a
  `GET /` with Go's default User-Agent reached every listener in local runs);
  those are passed through, counted apart, and logged per part.
- Only the served run of `args`, the same window as `requests`: not `setup`,
  `verify` or `{rev:ID}` reads.
- A shape whose `setup` fails on the reference profile has no served run, so
  it has no transcript (only a pending shape may lack one).

Two things a served run's requests would otherwise depend on are pinned:
- **The policy cache.** `bd ready` asks the server whether it enforces the
  external-dependency policy, and remembers the answer for
  `BEADS_HTTP_POLICY_CACHE_TTL` (5m by default) in the user cache directory,
  which is the served workspace here. The served side sets the TTL to 24h and
  runs one `bd ready` after the seed, so every shape runs with the cache warm,
  as gc's polling finds it, and no transcript depends on which shape of its
  part ran first or on how long the part took.
- **The read memo.** One `bd` process answers an identical `getIssue`,
  `getSetting` or `listSettings` read from memory for 5s (`wire.ReadMemoTTL`)
  unless it wrote in between. On a loaded host a long command (a `mol current`
  walk) can take longer than that between two identical reads and send one
  more. The recorder therefore leaves out a memoable read of a URI that was
  answered 2xx at least `wire.ReadMemoTTL` earlier in the same run with no
  write since, and logs how many it left out
  (`TestExpiredMemoReadRuleFires`). A memo regression re-reads within
  milliseconds, so it is still recorded and still drifts.

Regenerate the transcripts after an intended change, and review the diff:

```
BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 BEADS_TEST_EMBEDDED_DOLT=1 \
  go test -tags gms_pure_go -count=1 -timeout 40m -run 'TestServedCorpus|TestCorpusTranscripts' ./backend/http/corpus/
```

Do not set `BEADS_CORPUS_SKIP_PENDING` while regenerating: a skipped shape's
transcript is left as it was. A `requests` pin is not rewritten; update it in
`corpus.yaml` with the transcript, in the same review. `-count=1` matters:
`bd` is built in a subprocess, so go test's result cache cannot see a change
to `cmd/bd` alone and would replay a cached pass.

The engine-free checks run in every cgo lane:
- `TestCorpusTranscriptsAreWellFormed`: every transcript names a shape and
  only operations of its profile, is in canonical form, and every shape that
  is not pending has one.
- `TestCorpusRequestPinsMatchTranscripts`: every `requests` pin equals its
  transcript's length.
- `TestCorpusTranscriptsCoverTheReferenceOperations`: the operations some
  transcript got a 2xx from cover `reference_server.json`'s operations, less
  `uncoveredReferenceOperations`, which says why each of the rest is out of
  reach. That list only shrinks: a shape that starts covering one fails the
  test until its entry is removed.

### Current-capability transcripts

The reference profile is bd 1.3.0, which lacks `issues.update.claim`, so
there every `update --claim` already takes the `claimIssue` fallback, and a
client change on the current path (always `claimIssue` plus a read-back, say)
leaves every reference transcript as it was.
`TestServedCorpusCurrentCapabilityTranscripts` therefore also runs the shapes
in `currentCapabilityShapes` (`current_capability_test.go`), in corpus order,
on one served workspace with the same seed, through an unmasked proxy: the
server's own handshake, 404s and 400s pass through, and operation ids come
from the OpenAPI document this tree embeds. It holds each run's requests and
exit code to `testdata/transcripts/current/<shape>.json`. There is no
embedded run and no output comparison; the parts hold the outcomes. It runs
in `corpus_served_test`'s round-robin shard.

## Updating the reference profile

`reference_server.json` was derived from two things:
- the deployed server's live `GET /v0/beads/context` capabilities;
- its published OpenAPI document (bd 1.3.0, no `wire_revision`). For each
  operation the profile keeps the method, the path template, the query
  parameter names, and the top-level request members.

When the deployed server is upgraded (SRV-1), regenerate the file the same
way. Then remove the `SRV-1` pending marks that now pass, and regenerate the
wire transcripts: a new profile changes the request paths bd takes.

## Known fidelity limits

- Transcripts record names, not values: a request that sends a different
  value under the same names changes no transcript, and is caught only if
  the output or a `verify` read differs.
- Responses pass through unfiltered. A response member the reference server
  would not send still reaches the client.
- Only top-level request members are checked; nested members (for example
  `batchApply` items) are not.
- The served backend underneath is embedded Dolt, not the reference
  deployment's storage backend.
- Out of scope, because they are local by design or never touch a remote
  store: `init`, `bootstrap`, `migrate`, `doctor`, `flatten`, `gc`, and the
  `dolt` subcommands. `context`, `version`, `backup status` and `prime`
  are in the corpus, but only checked for exit codes.
