---
title: GraphQL Queries (bd gql)
description: The read-only bd gql command, covering input and variables, the query fields, relations, paging, limits, errors and exit codes, the schema_version extension, and what it runs at startup.
---

`bd gql` runs one read-only GraphQL document and prints only the fields the
document selects. Use it when a caller needs a few fields of many issues, or an
issue together with its related issues, and the full `--json` payload of
`bd show`, `bd list` or `bd ready` is more than it wants.

| Need | Use |
|---|---|
| Filter issues and print the whole row | [`bd query`](/cli-reference/query) with `--json` |
| Choose fields, follow dependencies, or fetch several IDs in request order | `bd gql` |
| Arbitrary SQL against the tables (server mode only; can write) | [`bd sql`](/cli-reference/sql) |
| The output of one specific command | That command with `--json` |

`bd gql` reads through the same reader roles as `bd show`, `bd ready` and
`bd query`, on both the embedded and the proxied-server route. It does not
shape rows itself, and the schema has no mutations or subscriptions.

## Running a document

The document comes from exactly one source:

```bash
bd gql '{ ready(limit: 100) { items { id title status assignee priority issue_type parent } } }'
bd gql --file widget.graphql --vars '{"ids":["bd-76c","bd-missing","bd-tic"]}'
bd gql --stdin < ready.graphql
```

- A positional argument, `--file PATH` or `--stdin`. Giving two of them is an
  error, and so is an empty document. With no source, `bd gql` prints its help
  and exits non-zero.
- `--file` content is used as is. `--stdin` drops trailing newlines.
- `--vars` takes one JSON object of GraphQL variables. Any other JSON value, or
  invalid JSON, is rejected before the document runs.

## Response

Once the document runs, the response is one JSON object on stdout:

```json
{"data":{"issuesById":[{"id":"bd-76c","title":"Add retry to sync","status":"open","assignee":""},null,{"id":"bd-tic","title":"Fix login timeout","status":"open","assignee":"alice"}]},"extensions":{"schema_version":1}}
```

That is the output of this document with the `--vars` shown above:

```graphql
query($ids: [ID!]!) { issuesById(ids: $ids) { id title status assignee } }
```

- `extensions.schema_version` is on every GraphQL response, including ones with
  `errors`, whatever `BD_JSON_ENVELOPE` says. It carries the same version as the
  [JSON output contract](/reference/json-schema), so a caller can refuse a
  format it does not know.
- `errors` is present only when something failed. `bd gql` then exits 1.
  Otherwise it exits 0.

## Query fields

| Field | Returns | Notes |
|---|---|---|
| `issue(id: ID!)` | `Issue` | `null` when no issue or wisp has that exact ID. Not an error. |
| `issuesById(ids: [ID!]!)` | `[Issue]!` | One entry per ID, in request order, repeats kept, `null` in place of a missing ID. At most 200 IDs. |
| `issues(query, all, sort, reverse, limit, offset)` | `IssuePage!` | `query` uses the [`bd query`](/cli-reference/query) language, and `all`, `sort` and `reverse` mean what they mean there. Default `limit` 50. |
| `ready(limit, offset, sort, assignee, type, labels, parent)` | `IssuePage!` | Ready work as `bd ready` returns it. Default `limit` 100 and `sort` `"priority"`, as in `bd ready`. |

`IssuePage` has `items` (never null) and `has_more`, which is true when the limit
cut the result short. A list argument given as `null`, or bound to a variable that
`--vars` leaves out, takes its default.

```graphql
{ issues(all: true, limit: 100) { items { id title status assignee priority issue_type parent } } }
```

**Leaving out `query` is not `bd list`.** Without `query`, `issues` sends the
expression `id="*"` and so has `bd query` semantics with no filter: closed issues
are hidden unless `all` is true, but templates, gates and infra types are
returned. `bd list` hides all three. `template=false` hides templates only. To
get the `bd list` set with the default infra types, write
`template=false AND type!=gate AND type!=agent AND type!=role AND type!=message`;
if `types.infra` is set in the configuration, exclude those types instead.

## Fields

`Issue` fields have the names and values that `bd show --json` and
`bd query --json` print for the same issue, in `snake_case`. Introspection
(`__schema`, `__type`) lists every field and type. A few types need a note:

- `created_at` and the other times are RFC 3339 strings.
- `metadata`, `bonded_from` and `timeout` are JSON values, printed as
  `--json` prints them. `timeout` is integer nanoseconds (2 hours is
  `7200000000000`), and `0` when unset.
- Counts are `dependency_count`, `dependent_count` and `comment_count`.
- `revision` and the epic progress fields `epic_total_children`,
  `epic_closed_children` and `epic_closeable` come from the detail view.

A field the row does not carry (relations, `comments`, `revision`, the epic
fields) costs one extra issue read per issue. Within one request, a repeated
read of the same issue for the same selection is served from a cache.

## Relations

`dependencies` and `dependents` return `Relation` objects: `id`,
`dependency_type`, and `issue`, the related issue. `comments` returns `id`,
`issue_id`, `author`, `text` and `created_at`.

```graphql
query($ids: [ID!]!) { issuesById(ids: $ids) { id dependency_count dependencies { id dependency_type } } }
```

```json
{"data":{"issuesById":[{"id":"bd-76c","dependency_count":1,"dependencies":[{"id":"bd-tic","dependency_type":"blocks"}]},{"id":"bd-tic","dependency_count":0,"dependencies":[]}]},"extensions":{"schema_version":1}}
```

- When a `Relation.issue` selection asks only for `id`, `title`, `status`,
  `issue_type` or `priority`, those come from the relation row with no extra
  read. Any other field reads the related issue.
- The `dependencies` list is best effort, as in `bd show`: it can be shorter
  than `dependency_count`. `bd gql` returns both so a caller can compare them.
  A requested `dependents` or `comments` list is not best effort: a failure to
  load it is an error.
- The lists are not paged. Selecting `dependents` or `comments` reads the
  issue's whole list, as `bd show --include-dependents` and
  `--include-comments` do, before the limits below are checked. For an issue
  with many dependents or comments, check `dependent_count` or `comment_count`
  first.

## Paging

`limit` is 1 to 200 on both lists. To read more, walk with `offset` until
`has_more` is false.

- `issues` refuses an `offset` together with `sort`, because the order is applied
  to the rows each page bounded. Page without `sort`, or sort in the caller.
- `ready` accepts `offset` with its `sort`. An offset past the end returns an
  empty page.
- A negative offset is an error.
- Offset paging can skip or repeat an issue when issues change during the walk.

## Limits and errors

| Limit | Value |
|---|---|
| Document size | 16384 bytes, from any source |
| Nesting depth | 8 |
| List `limit` | 1 to 200 |
| `issuesById` IDs | 200 |
| Store reads per request | 200: each `issues`, each `ready` and each issue read not already made in this request |
| Objects per response | 10,000 issues, relations and comments, counting every repeat |
| Long text per response | 64 MiB of `description`, `design`, `acceptance_criteria`, `notes`, `payload`, `metadata` and comment `text`, counting every repeat |

The last two limits count what the response holds, so an issue selected twice,
or a field repeated under two aliases, counts twice. They do not limit what one
read loads: a selected `dependents` or `comments` list is read in full first
(see [Relations](#relations)). If a document breaks one, lower `limit`, select
fewer relations, comments or text fields, or split the document. A 200-row page
that also selects relations or comments on every row needs 201 reads, so use
`limit: 199` there.

Short fields such as `title`, `status`, numbers and times are not counted
against a byte limit. A document that repeats them under many aliases can
still build a large response. `bd gql` builds the response in its own process,
so this uses the memory of that `bd` process, not of a shared server.

A document that fails to parse or validate, names an unknown field, is a
mutation or subscription, breaks a limit above, or has an invalid `bd query`
expression gets an `errors` entry naming the fault, and `bd gql` exits 1. Here
the failed field is non-null, so `data` is `null`:

```json
{"errors":[{"message":"limit must be between 1 and 200","path":["issues"]}],"data":null,"extensions":{"schema_version":1}}
```

Input problems found before the document runs (two sources, an empty
document, bad `--vars`, an unreadable file) are reported like other bd errors:
`Error: ...` on stderr, or a JSON `error` object on stdout with the global
`--json` flag. They also exit 1.

## Startup behavior

`bd gql` is a read-only command in the same class as `bd query` and
`bd ready`. It skips the post-run auto-push and auto-export, and it runs the
startup maintenance every read runs: version tracking, auto-migration after an
upgrade, and auto-backup when that is enabled. `ready` also wakes deferred
issues whose date has passed, as `bd ready` does.

Outside proxied-server mode, the global `--readonly` flag opens the store
read-only and turns that maintenance off. On a proxied-server workspace bd
refuses `--readonly` for every command.
