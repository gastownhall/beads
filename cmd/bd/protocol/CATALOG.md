# bd contract corpus — producer catalog

This package is the **producer** half of the Beads ↔ consumer cross-version
contract-test system. It pins the `bd` `--json` wire surface that a downstream
consumer's decoder consumes, by generating a canonicalized golden-JSON **corpus**
under `testdata/corpus/` and failing CI on any unreviewed change to it.

A downstream consumer vendors this corpus and replays it against its own decoder,
so a `bd` release can never silently change a wire shape the consumer parses.

## How it works

- `corpus.go` — the deterministic command **plan**, the **canonicalizer**
  (timestamps → `<TS>`, object arrays stable-sorted, keys sorted, 2-space
  indent), and the provenance **manifest**. A capture's `Projection` replaces
  run-specific values the generator knows about: the workspace's random id
  prefix and directory (`<PREFIX>`, `<WORKSPACE>`), an id bd minted
  (`<MINTED:name>`), and, in `bd history --events` blobs, each event row's
  random `id` (dropped) and the timestamps embedded in its `old_value` /
  `new_value` issue snapshots (`<TS>`). Event rows are therefore ordered by
  content, not time.
- `corpus_test.go` — `TestCorpusGolden` runs the plan against a live `bd`
  (built from source, in an embedded-Dolt workspace) in both flat and
  `BD_JSON_ENVELOPE=1` variants, canonicalizes, and **byte-compares** against
  the committed corpus. It also writes and compares
  `testdata/corpus_commits.golden`, the **commit ledger**: how many Dolt
  commits each write capture adds, read before and after it from
  `bd flatten --dry-run --json` (`commit_count`, the `dolt_log` row count;
  embedded mode refuses `bd sql`). The ledger is test-only: it is outside
  `testdata/corpus/` and the release job does not ship it. A diff is a hard
  failure (`make corpus-regen` to update); a Dolt-boot failure is an infra
  skip, never a silent pass.
- `canonicalize_test.go` — unit tests for the canonicalizer plus
  `TestCorpusDoubleRunByteIdentical`, the determinism backstop that generates
  the corpus twice and asserts byte-identity.

Regenerate after any deliberate wire change: `make corpus-regen`, review the
diff, **and bump `JSONSchemaVersion` (`cmd/bd/output.go`)** — the `schema_version`
field in every blob is the coordination canary a downstream consumer keys its
pinned-decoder migration off. Adding a capture is not a wire change: it adds
blobs and leaves every existing one byte-identical, so it does not bump
`JSONSchemaVersion`. New captures go after the existing ones
(`TestCorpusPlanIsWellFormed` holds the order).

## Coverage

| Blob (flat + envelope) | bd command | Contract domain |
| ---------------------- | ---------- | --------------- |
| `create_root`, `create_dep`, `create_closed`, `create_deleted` | `bd create … --json` | json-output-shapes (object) |
| `show` | `bd show <id> --json` | json-output-shapes (array-of-one), dependency shape |
| `update` | `bd update … --json` | json-output-shapes (mutation array), label add + metadata coercion (`phase` → string) |
| `close` | `bd close --reason … --json` | close semantics (`close_reason`, `closed_at`) |
| `reopen` | `bd reopen … --json` | reopen semantics (status back to open) |
| `list` | `bd list --all --json` | json-output-shapes (array / list envelope) |
| `ready` | `bd ready --json` | ready-projection-semantics |
| `dep_add`, `dep_list` | `bd dep add/list --json` | json-output-shapes (dependency) |
| `dep_remove` | `bd dep remove … --json` | dependency-edge removal confirmation |
| `delete` | `bd delete --force … --json` | delete confirmation shape |
| `count` | `bd count --json` | json-output-shapes (scalar) |
| `version` | `bd version --json` | version-compat, the `schema_version` canary |
| `error` | `bd show <missing> --json` | exit-codes-and-errors (`{error, schema_version}`) |

### Native-fix captures

The commands the fork's native fixes move onto roles (rows B1–B10, D4–D6 of
its decision records), pinned as today's direct-mode behavior so each fix PR
shows a reviewed diff. Each write has a subject of its own, created by a setup
step that leaves no blob and carrying a description, notes, a label, metadata
and a non-blocking edge (a plain subject hides a role result that adds
`dependencies`). Each write is followed by a `<name>_events` blob
(`bd history <subject> --events --json`) and has a line in the commit ledger.

| Blob (flat + envelope) | bd command | Pins |
| ---------------------- | ---------- | ---- |
| `priority`, `priority_events` | `bd priority corpus-priority 1 --json` | B1 |
| `tag`, `tag_events` | `bd tag corpus-tag … --json` | B2 |
| `note`, `note_events` | `bd note corpus-note … --json` | B3 |
| `q_parent`, `q_parent_show`, `q_parent_events` | `bd q … --parent corpus-parent --json` | B4: `bd q` ignores `--json` and prints the bare new id (the blob is that stdout as a JSON string); `q_parent_show` is the child it made. The parent is a pinned id outside the workspace prefix, which `bd q --parent` accepts and `bd create --parent` refuses. |
| `todo_add`, `todo_add_events` | `bd todo add … --json` | B5: the minted id reads `<MINTED:todo_add>`; no envelope even under `BD_JSON_ENVELOPE=1` |
| `duplicate`, `duplicate_events`, `supersede`, `supersede_events` | `bd duplicate … --of …`, `bd supersede … --with …` | B6 |
| `dep_relate`, `dep_relate_events`, `dep_unrelate`, `dep_unrelate_events` | `bd dep relate/unrelate … --json` | B7: no envelope; no Dolt commit of their own (ledger `0`) |
| `defer`, `defer_events`, `undefer`, `undefer_events` | `bd defer … --reason … --until 2099-01-01 --json`, `bd undefer … --json` | B8 |
| `close_claim_next`, `close_claim_next_events` | `bd close corpus-closer --claim-next --json` | B10: claims `corpus-root`, the only P0 |
| `unclaim`, `unclaim_events` | `bd unclaim corpus-root --json` | B9 |
| `ready_explain` | `bd ready --explain --json` | D4 (one blocked and one resolved blocker) |
| `info` | `bd info --json` | D5 (direct route) |
| `show_refs`, `show_children` | `bd show … --refs/--children --json` | D6 |

## Known gaps (tracked, not silent)

- `bd sql` is **not supported in embedded mode** (this harness's mode), so it is
  not in the corpus. A downstream consumer's ready-projection enrichment depends on `bd sql`
  against a managed Dolt server; covering it needs a server-mode generation path.
- The `show` blob pins the **count-only** default payload (`comment_count`,
  `dependent_count`); the opt-in `--include-comments` / `--include-dependents`
  shapes are not in the corpus. They are covered at the CLI level by the
  preservation and round-trip tests, not byte-pinned for a consumer.
- The native-fix captures run in direct embedded mode, like the rest of the
  corpus: not in direct server mode, and not over `bd connect`. Each write has
  one events blob, its own subject's; the other end of relate, duplicate and
  supersede is not captured separately. The ledger pins commit counts, not
  commit messages. Template issues (B8's template guard) are out of the corpus.
- The `error` blob pins the not-found envelope; other error classifiers
  (claim-conflict, silent-fallback auto-import, `bd sql` unsupported) are
  plain-text on stderr and belong in a separate error-string fixture set.
