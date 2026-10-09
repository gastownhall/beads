# Served command corpus

This suite is the working definition of "bd fully supports a remote HTTP
backend". It runs every distinct `bd` command shape that an orchestrator (gc)
and its packs and formulas issue against a work store. Each shape runs
against a served workspace and is compared with the same command on an
embedded workspace.

## What runs

`TestServedCorpus` does the following:

1. Builds `bd` from this tree. `BEADS_TEST_BD_BINARY=<path>` reuses a prebuilt
   binary instead.
2. Starts an in-process `bd serve` (`httpapi.Listen` over embedded Dolt) the
   way a deployed server is bound: `AllowNonLoopback` plus a bearer token
   file. The socket is still `127.0.0.1`, but the bind mode decides every
   behaviour the server keys on, including its refusal of unlimited reads.
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
5. Runs each shape as a subtest, on both sides. The served run must:
   - exit like the embedded run;
   - carry no refusal (`ErrUnsupported`, a v0-wire refusal, the
     loopback-only 400, or a reference-profile 404/400);
   - match the embedded output under the shape's `compare` mode, after
     timestamps and revision tokens are dropped;
   - for writes, give the same JSON from every `verify` read on both sides
     afterwards.

The run takes about five minutes.

```
CGO_ENABLED=1 BEADS_TEST_EMBEDDED_DOLT=1 BEADS_HTTP_TEST_REQUIRED=1 \
  go test -tags gms_pure_go -run 'TestServedCorpus|TestCorpusFile' ./backend/http/corpus/
```

Two environment variables change the run:
- `BEADS_CORPUS_REPORT=<file>` writes a JSON summary with per-shape status
  and request counts.
- `BEADS_CORPUS_SKIP_PENDING=1` skips pending shapes without running them.

## The data

| File | Holds |
|---|---|
| `testdata/corpus.yaml` | assets, the seed, and the shapes |
| `testdata/allowlist.yaml` | permanent refusals, each with its reason and the refusal text it must match |
| `testdata/reference_server.json` | the reference server's wire surface |

Each shape in `corpus.yaml` has these fields:

| Field | Meaning |
|---|---|
| `id` | `gc-*` for orchestrator subprocess shapes, `mc-*` for pack/formula/prompt shapes |
| `source` | where the shape comes from |
| `args` | the argv; `{file:NAME}` expands to an asset path and `{rev:ID}` to that side's current revision of `ID` |
| `compare` | `json` (default), `ids`, `count`, `text` or `exit` |
| `prefix_of` | for a limited `ids` listing: its unlimited form. Each side's limited rows must be a prefix, in order, of that side's own unlimited listing, both sides return as many rows, and the two unlimited listings hold the same ids. A limit that cuts through a tie set (same priority, `created_at` in the same second) lands on different rows on two workspaces seeded seconds apart, so the limited rows are not compared across sides directly |
| `verify` | reads run after the shape |
| `expect_exit`, `expect_stderr` | expected outcome on both sides |
| `pending` | a known failure |

`pending: {slice, reason}` marks a known failure and names the slice or
slices that fix it, for example `S6c+S15`. A pending shape **still runs**:
- while it fails, the subtest is skipped and the skip records what was
  observed;
- once it passes, the subtest fails with "now passes; remove the pending
  mark".

So removing a mark is how a slice proves itself, and any other failure is
unexpected and fails the suite.

## Updating the reference profile

`reference_server.json` was derived from two things:
- the deployed server's live `GET /v0/beads/context` capabilities;
- its published OpenAPI document (bd 1.3.0, no `wire_revision`). For each
  operation the profile keeps the method, the path template, the query
  parameter names, and the top-level request members.

When the deployed server is upgraded (SRV-1), regenerate the file the same
way. Then remove the `SRV-1` pending marks that now pass.

## Known fidelity limits

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
