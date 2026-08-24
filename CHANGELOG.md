# Changelog

All notable changes to the beads project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **The events journal records WHO performed each mutation.** `bd_events_journal`
  gains an `actor` column (migration 0066 plus its ignored-series twin 0025, so
  upgraded workspaces and fresh clones converge on the same shape), stamped
  inside the mutating transaction with the same identity the audit-events table
  resolves; on a `comment` row it is the comment's author. `bd events tail` /
  `bd events export` JSON gains an additive, omit-when-empty `actor` field —
  empty means the path had no actor (derived maintenance, deletes, rows older
  than the column), never a user. The input for same-field / different-actor
  conflict measurement over the journal.

- **`bd` warns when it stores a label containing a space**
  ([#5813](https://github.com/gastownhall/beads/pull/5813)). Such a label is a
  legitimate thing to ask for — `-l 'good first issue'` — and bd stores it as
  asked rather than re-splitting a boundary the shell already decided. But it is
  also exactly what a missed comma looks like, and that case cannot be told
  apart from the deliberate one at the point of writing. A line on stderr
  catches it at the keystroke instead of months later: `⚠ Stored "auth backend"
  as ONE label — it contains a space.` It is advice, not an error; stdout is
  untouched, so `--silent` still prints just the ID, and `--quiet` silences it.
  Not emitted for `--remove-label`, since removing such a label is how the
  damage gets repaired.

- **`bd doctor` gains a `Label Whitespace` check** for finding damage already in
  a database ([#5813](https://github.com/gastownhall/beads/pull/5813)). It
  reports labels that contain whitespace or differ from their trimmed form, and
  `bd doctor --fix` repairs them. The check is warn-only: a database with legacy
  damage still exits 0. As with the rest of bare `bd doctor`, it does not yet
  run in embedded mode (GH#3794).

- **`bd stale` and `bd blocked` gain `--label`, `--label-any` and
  `--exclude-label`** ([#5822](https://github.com/gastownhall/beads/pull/5822)),
  with the same meanings and the same flag names they already have on `bd list`
  and `bd ready`: `--label` is AND (repeatable, must have all), `--label-any` is
  OR, `--exclude-label` drops anything carrying one. They were the two listing
  commands with no label-scoped output at all, so a theme-scoped stale sweep had
  to pull the whole list as JSON and post-filter it on the labels array.
  Filtering happens in SQL ahead of `LIMIT`, not on the hydrated rows, so
  `--label x --limit 10` returns ten matching issues rather than whatever
  survives filtering the first ten. No `--theme` flag: beads has no theme
  concept, themes are labels here, and `--label theme:x` is the native way to
  say it.

### Changed

- **`bd dep add` names the implicit `type=blocks` default, but only to an
  interactive operator** (#5854). Creating an edge with no `-t/--type` silently
  produces a `blocks` edge, which drops the dependent out of `bd ready` — the
  usual surprise when someone meant structural parent/child linkage. `bd dep
  add` now says so on stderr. Because `blocks` is the documented default and
  the majority-correct case, the note is gated: it is emitted only when stderr
  is a TTY, so scripted and agent callers (and CI, and `bd dep add 2>log`)
  never see it and are not trained to ignore stderr. `--quiet` silences it, as
  it does the other non-error stderr notices, and `BD_NO_DEP_TYPE_WARNING=1`
  turns it off for operators who have internalised the default. `--json` output
  is unchanged.

- **`bd show` labels `created_by` as `Created by:`, not `Owner:`** (be-ss66).
  The text view rendered `created_by` under a label naming a different
  concept. `owner` is a separate field holding a separate value — the git
  author email kept for CV attribution — exposed as `owner` in `--json` and
  never rendered in the text view at all. Two identities, one label, and the
  label named the one that was not on screen. This is a correctness fix and
  not polish: a reader asking "whose bead is this" reasonably reaches for
  what the label promises, and two defect analyses were filed off exactly
  that mistake in one evening, both measuring provenance from the wrong
  column and both retracted after another agent disproved them. `bd show`
  now prints `Created by: <identity>`. The `--json` payload is unchanged, and
  a repo-wide audit found no in-tree parser of the old `Owner: ` text — the
  only other producer, `bd github`'s repo-config display, names an actual
  repository owner and keeps its label.

- **`beads_dir` and `repo_root` are now OPTIONAL members of `ContextResponse`
  on `GET /v0/beads/context`.** They are the only two members of the identity
  handshake that describe the SERVER's filesystem rather than the workspace's
  logical identity, and an absolute host path is not something a remote client
  can act on — it cannot open it. The document already called publishing them
  a cost "an operator accepts when binding beyond loopback"; while they were
  required, accepting it was the only conforming option. A deployment that
  does not want to disclose its filesystem layout can now withhold them and
  still serve a conforming body.

  **No body changes.** `bd serve` publishes both, always, exactly as before —
  the relaxation is a promise to clients, not a switch on the server. What
  changes is what a client may assume: it MUST tolerate their absence, and
  MUST identify a workspace by `project_id`, `database` and `backend`, which
  stay required. In Go, `apigen.ContextResponse.BeadsDir` and `.RepoRoot`
  become `*string`.

- **An explicitly configured Dolt server port now outranks the ambient
  `BEADS_DOLT_SERVER_PORT`/`BEADS_DOLT_PORT` environment variables**
  (be-wf9a.1, be-9tju, GH#4052). `ServerPort` resolution now carries a
  *source* — which step of the chain produced the port — and the env read only
  applies when that source is not an assertion by the user or by tooling acting
  for them. Two deliberate behavior changes follow. **First**, a port set by the
  caller before the store opens (a library caller filling in `Config.ServerPort`,
  or `bd init --server-port`) is no longer silently replaced by whatever the
  surrounding shell exported; previously the env var won unconditionally.
  **Second**, the *legacy* `BEADS_DOLT_PORT` spelling no longer overrides a port
  resolved from `.beads/config.yaml` (`dolt.port`), Dolt's own `config.yaml`
  (`listener.port`), or `metadata.json` (`dolt_server_port`). The current
  `BEADS_DOLT_SERVER_PORT` spelling is unaffected — it sits first in the
  resolution chain and still wins over all of them.

  **Compatibility:** a workspace that relied on exporting `BEADS_DOLT_PORT` to
  redirect away from a port pinned in config now connects to the pinned port
  instead. Switch to `BEADS_DOLT_SERVER_PORT`, or change the pinned value. Ports
  that bd resolved for *itself* — the gitignored `.beads/dolt-server.port`, and
  the shared-server default 3308 — stay non-authoritative and remain overridable
  by either spelling, which is the case that kept working throughout.

  A related failure goes away with it: because bd's own bookkeeping is no longer
  mislabeled as user configuration, a stale `.beads/dolt-server.port` left by a
  crashed server no longer makes auto-start refuse to start with *"bd will not
  silently use a different port than the one you configured"* when nothing was
  configured. Auto-start warns and retargets, as it did before. Shared-server
  mode still fails closed there — a repo-local auto-start is a different
  database, not a port refresh — but now says so in its own words.

- **`bd reclaim` summarizes the leases its replica guard declined instead of
  naming every one, every run** (wy-sp2l4). A lease granted by another replica
  is by construction never reclaimed here, so the audit was not a one-off: it
  repeated identically on every run, one stderr line per stranded remote lease.
  A supervisor on a 1-minute timer against a federated store with 47 of them
  printed 47 lines a minute, indefinitely. The default is now ONE line —
  `reclaim: skipped 47 stale leases granted by other replicas — "mini" (30),
  "mini2" (17), not this node ("laptop")…` — naming at most three replicas and
  collapsing the rest into a count, with the exact total preserved. `bd -v`
  (or `BD_DEBUG=1`) expands it to the per-lease lines, itself capped at 20 with
  a collapsed tail. Still stderr-only: `bd reclaim --json` owns stdout and its
  payload is unchanged. Under `--quiet` the audit now skips its queries
  entirely rather than running them to discard the output.

- **Actor matching decodes an exact `--` run to `/` instead of collapsing it to
  a generic separator** (be-p7dzx, bundled be-vc51). Identity comparison — used
  by `ExpectedAssignee` on release and update, and by claim/close/unclaim
  authorization — canonicalizes a run of `.`, `_` or `-` to one separator so the
  same identity spelled under different layers' conventions compares equal. An
  exact two-byte `--` run is now excluded from that collapse: it is gascity's
  session-name encoding of a rig-qualified agent's `/`, so it decodes to `/`.
  **Compatibility:** `gastown--mayor` now matches `gastown/mayor`, and stops
  matching `gastown__mayor` and `gastown-mayor`. That is the intended fix rather
  than a side effect — `gastown--mayor` is the agent `mayor` on rig `gastown`
  while `gastown__mayor` is the dotted alias `gastown.mayor`, so matching them
  was a widening — but any stored `--` spelling that is NOT a gascity slash
  encoding changes equivalence class silently, with no error to notice. Longer
  or mixed runs, `__` and `---` included, are unaffected and still collapse.

### Fixed

- **Incremental auto-export now actually takes the incremental path**
  ([#5806](https://github.com/gastownhall/beads/pull/5806)). Change detection
  compared `GetStateHash()` values — a hash of the entire database plus
  working set (`DOLT_HASHOF_DB()`), not a commit — but `ChangedIssueIDs` feeds
  both endpoints straight into `dolt_diff()`, which only accepts real commit
  hashes or the literal `WORKING`. Every incremental attempt therefore failed
  to resolve a diff and silently fell back to a full export: correct output,
  but the incremental short-circuit this feature exists for never actually
  ran. The "to" endpoint is now `WORKING` (dolt_diff's own literal for the
  live working set) paired with the previous export's real commit hash as
  "from", so the incremental path resolves and fires as designed.

  Three format/scope regressions surfaced alongside the dead code path, since
  nothing had ever exercised it end-to-end: the incremental patch could leak
  **memories** into the auto-export output (full export filters them; the
  patch path did not), could include the configured **owner**'s own issues
  where full export excludes them, and omitted the `_type` discriminator field
  full export always writes. All three are now pinned by tests that diff a
  patched run's output against a from-scratch full export of the same state.
  The patch write is also now atomic (write-temp + rename), matching the
  full-export path's existing guarantee.

  The owner and `_type` fixes match full export exactly. **Memory records
  deliberately do not**, and the difference is worth stating: where the full
  path *refuses* to overwrite an `issues.jsonl` that already contains memory
  lines (`guardAutoExportOverwrite`), the incremental path *preserves* them
  verbatim and proceeds. Preserving is the better behavior — a manually
  seeded export survives patching byte-for-byte instead of wedging — so the
  incremental path keeps it rather than adopting the refusal.

  **Scope: this is a server-mode (`DiffStore`) improvement only.**
  `EmbeddedDoltStore` implements neither `DiffStore` nor `StateHasher`, so in
  the default embedded mode the incremental path is inert (`incremental
  skipped — store does not implement DiffStore`) and every cycle is still a
  full export. The deletion-proof guard below is likewise server-mode only, so
  [#5896](https://github.com/gastownhall/beads/issues/5896) stays open for
  embedded mode, where `bd delete` still wedges auto-export.

  **Caveat: the diff anchor only advances on real commits.** In server mode
  dolt auto-commit is off, so with no commits being made the anchor stays put
  and the diff range grows with every cycle. That is correct but not free:
  once the change set crosses the 5000-id threshold, every export falls back
  to a full rewrite until something commits and the anchor moves forward.

- **`bd delete` no longer wedges auto-export in server mode**
  ([#5806](https://github.com/gastownhall/beads/pull/5806), part of
  [#5896](https://github.com/gastownhall/beads/issues/5896)). Auto-export's
  orphan guard refuses to overwrite an `issues.jsonl` holding issue records
  absent from the local store — the #4988 protection against a JSONL that has
  run ahead of the database. A normal `bd delete` produced exactly that shape,
  so a single delete left `issues.jsonl` permanently stale, with a "refusing
  to overwrite" warning on every later command. The guard now asks `dolt_diff`
  whether each JSONL-only id was genuinely removed since the last export's
  anchor and, when proven, stops treating it as corruption.

  The proof is deliberately narrow. It requires `diff_type='removed'` on the
  **issues** table itself, so a label-, dependency- or comment-only change, a
  delete-then-recreate, a wisp, or any diff error all still refuse. It also
  requires the anchor to still be reachable from `HEAD`: after a
  `DOLT_RESET --hard`, a branch checkout, or any other data-dir rewind, a row
  reads as "removed" without anyone having deleted it, and honoring that
  would drop a live record. Both conditions must hold, or the guard refuses as
  before.

- **Disabling telemetry no longer strands the queued eventsData backlog
  forever** (GH#5712). `bd send-metrics` early-returned on disabled metrics
  *before* its prune step, and the spawn gate refused to schedule the child at
  all when disabled — so the one machine whose queue can never again drain by
  upload (the one that just opted out) kept every queued batch and orphaned
  emitter temp indefinitely (2M+ files / 15.8GB observed on one control VM).
  With metrics disabled, bd now still spawns the detached child under the same
  marker-first throttle, and the child prunes by the normal TTL/cap policy and
  exits without POSTing anything. Until that pre-existing backlog ages out, a
  host that just opted out while its queue still holds batches younger than the
  7-day TTL (and under the file/size caps) keeps forking the throttled,
  detached, network-free prune child every flush interval — reclaiming nothing
  until those batches age past the TTL — so new prune-only `bd send-metrics`
  processes on a freshly disabled host are expected during that bounded window
  and never POST. Once the backlog has decayed empty, spawns stop entirely; a
  machine that never enabled telemetry has no queue directory and never forks.

- **`bd show` on a deleted or purged issue no longer prints text identical to
  an ID that never existed** (ga-m6inyb). `bd delete` and wisp `purge` both
  remove a row (and its `events`) from the live tables with no trace left
  behind, so "not found" was collapsing two states that call for opposite
  responses — archived (success) and never-existed (possibly lost data) —
  into one observable. This had a real cost: a gate-reviewer session showed
  a legitimate issue successfully, hit an unrelated mysql i/o timeout on a
  retry moments later, got the identical "not found" text for the same ID,
  and reasonably concluded data had been lost — when the issue had simply
  been archived in between. `bd show` on a missing ID now adds a second
  line — `Hint: this ID may have never existed, or may reference a
  deleted/purged record with no trace left in the live database — try 'bd
  history <id>'` — pointing at the existing, opt-in `bd history <id>` for
  anyone who wants to check further on a non-wisp issue (wisps are never
  committed to history, purged or not, so there is no signal left to
  recover for them). JSON output gains an additive `hint` field; the
  existing `error` text is unchanged. **Direct-mode text output changes for
  one path**: the single-ID not-found branch's first line changes from
  `Error fetching <id>: no issue found matching "<id>"` to `Issue <id> not
  found`, matching the wording the multi-ID and proxied-mode paths already
  used (those two are unchanged aside from gaining the new second `Hint:`
  line).

- **Explicit commits now sweep the entire working set and report no-ops
  honestly.** In server mode, `bd vc commit` and `bd dolt commit` previously
  routed through the config-excluding automatic-commit path, so out-of-band
  config changes could remain dirty even though the command exited
  successfully. Both commands now include config while continuing to exclude
  ignored wisp tables. A clean or otherwise non-committable working set reports
  `Nothing to commit` (`committed: false` in JSON) instead of attributing an
  unchanged HEAD to a new commit.

- **Every CLI label write now normalizes its input, as every label filter
  already did** ([#5813](https://github.com/gastownhall/beads/pull/5813),
  fixes [#5812](https://github.com/gastownhall/beads/issues/5812)).
  `utils.NormalizeLabels` (trim, drop empty, dedupe) was applied to `list`,
  `search`, `ready`, `orphans`, `count` and workapi — and to no write path at
  all. The filter trimmed its input and the store did not, so a label could not
  match its own filter: `bd create --labels 'theme:a, theme:b'` stored
  `theme:b` with pflag's leading space intact, and `bd list --label theme:b`
  then returned nothing. The failure is silent in the worst way — a filtered
  list quietly short of rows is indistinguishable from a complete one. One real
  database had 150 such rows across 111 issues, found months later only because
  theme-by-theme counts refused to reconcile. `create`, `quick`, `update`
  (`--add-label`, `--remove-label`, `--set-labels`) and `tag` now normalize on
  the way in. `--remove-label` matters as much as the additive flags: an
  untrimmed `' theme:a'` silently removed nothing.

  **`bd tag` is included deliberately.** Its help calls it "Shorthand for
  `bd update <id> --add-label <label>`", and it was the last CLI door still
  storing a positional verbatim. Normalization happens before the direct/proxied
  route split, so the two routes cannot disagree. A label that is only
  whitespace is now refused rather than dropped: the plural flags can discard an
  empty element and still honor the rest of the request, but `bd tag` has one
  label to add, so dropping it would report success having written nothing.

  **Import and batch ingest are deliberately excluded** and keep round-trip
  fidelity: what was exported is what is restored.

- **`bd show` no longer corrupts shell globs and other quoted wildcards in a
  description or notes**
  ([#5799](https://github.com/gastownhall/beads/pull/5799)). CommonMark lets a
  `*` or `_` run act as both an opener and a closer when the characters
  flanking it fall in the same class, and a quoted glob is exactly that shape —
  the `*` in `'*.captured'` sits between `'` and `.`. Two such runs therefore
  paired, including across lines, since goldmark joins a paragraph's lines
  before parsing. Everything between them rendered as emphasis: with color the
  asterisks were consumed and **silently vanished** from the command, leaving a
  still-plausible but different one on screen; without color, glamour's notty
  style wrote a literal `**` into the middle of the text. Storage was never
  affected — `bd show --json` always returned the body byte-for-byte — but the
  rendered text is what a reader acts on, and what an agent reads when it takes
  its instructions from a bead. Runs that can act as both opener and closer are
  now hidden from the parser and restored afterwards, so `bd show` prints what
  was stored.

  **The known trade**, in two narrow shapes, both pinned by test: an authored
  closer that is itself both-flanking no longer pairs, so `see *"quoted"*,
  next` renders literally rather than italicized (color path only — notty
  already printed it literally); and a backslash escape reaching for a
  both-flanking delimiter keeps its backslash, so `\*.captured` renders as
  `\*.captured`. An escape whose delimiter is not both-flanking, such as
  `a \*literal\* b`, is unchanged. Both are shapes CommonMark itself treats as
  ambiguous, and for a bead body — stored plain text, not authored markdown —
  showing the stored characters is the better of the two failures. Ordinary
  emphasis (`**bold**`, `*italic*`), `SELECT * FROM t`, code spans and fenced
  blocks all render exactly as before.

- **`bd blocked` silently ignored the label filters it already accepted**
  ([#5822](https://github.com/gastownhall/beads/pull/5822)).
  `types.WorkFilter` has carried `Labels`, `LabelsAny` and `ExcludeLabels` all
  along, and `bd blocked` built one, but `GetBlockedIssuesInTx` only ever read
  `ParentID`. Programmatic callers that set them got unfiltered results back and
  no error — a full list where a scoped one was asked for.

### Documentation

- **The heartbeat/re-home invariant and the two states it can strand are now
  documented where the code lives** (wy-sp2l4): a heartbeat proves the holder
  is alive, not that the lease moved, so `granted_node` is backfilled but never
  overwritten. A renamed replica (`mini` → `mini2`) and an imported foreign
  lease whose holder name also exists locally therefore keep reading foreign
  while a local heartbeat keeps them alive, recoverable only with
  `bd reclaim --any-replica`. Reviewed and KEPT rather than re-homed: re-homing
  would let a heartbeat silently reassign enforcement of a lease another
  replica granted — the robbery the guard exists to prevent. Recorded in
  `HeartbeatIssueInTx`, `docs/multi-agent/federation.md`, and
  `bd reclaim --help`.

## [1.2.2] - 2026-08-15

Recovery release, cut from the v1.1.2 tree on branch `release/v1.2.2` (not
from main). v1.2.0 and v1.2.1 were published by accident on 2026-08-11
without release testing (v1.2.0's tag burned pre-publish; only v1.2.1
reached users). This release supersedes them by re-releasing the **tested
1.1 line**: it is the v1.1.2 code under a higher version number, so every
install channel (Homebrew, npm, the install script,
`go install ...@latest`) moves forward onto tested code. The 1.2.x-only
features listed under [1.2.1] below are **not** in this release; they
remain on main for a properly tested future release.

**If v1.2.1 migrated your database** (running any command once was enough),
v1.2.2 stops with `schema version mismatch: database is at v65, binary
knows up to v53`. See the recovery guide
([site](https://beads.gascity.com/recovery/accidental-1-2-1-release),
[repo copy at the tag](https://github.com/gastownhall/beads/blob/v1.2.2/docs/RECOVERY-1.2.1.md))
for the two-minute fix (roll the schema cursor back to v53) and a verified
stopgap (`BD_IGNORE_SCHEMA_SKEW=1`).

### Changed

- **The forward-skew error recognizes the accidental-release window** (on
  the release branch): a v53 binary opening a database at schema v54–v65
  points at the recovery guide instead of advising "install the latest
  release" (which would loop back to that binary).
- **`go.mod` retracts v1.2.1, v1.2.0, and v1.1.1** so
  `go install github.com/steveyegge/beads/cmd/bd@latest` resolves to this
  release instead of the accidental one. (The retract block is also
  carried on main so future tags keep the retractions.)

## [1.2.1] - 2026-08-11

(Released as 1.2.1: the v1.2.0 tag burned pre-publish on a freebsd
cross-compile failure in the release build — fixed below — and a burned tag is
never reused, per the v1.1.1 precedent.)

### Added

- **`--brief` on `bd list` and `bd ready`, and `brief` on the two HTTP
  listings** (#5546, #5554, #5586). The listing commands return the whole body
  of every row they page. On one real 16,051-issue store `bd ready --json`
  returned 454,021 bytes with notes plus description accounting for 76% of it,
  on exactly the call an agent makes to decide what to work on next. `--brief`
  omits the free-form text (`description`, `design`, `acceptance_criteria`,
  `notes`, `payload`, `waiters`), measuring 93.4% smaller. Filtering is
  untouched: `--desc-contains` and its siblings select rows, and this selects
  fields. It is opt-in and the default payload is unchanged.

  **The response carries no marker for the omission.** An omitted field is
  indistinguishable from a genuinely empty one, so only the caller that passed
  the flag knows its rows are partial; there is no `is_lite_partial` in the
  JSON. The one visible marker is in text output, where `bd list --long
  --brief` prints `Description: (omitted by --brief)` rather than an empty
  section.

  The flag is refused wherever it could not be honored, rather than accepted
  and dropped. On `bd ready` it requires `--json` and is refused with `--claim`,
  `--gated`, `--mol` and `--explain`: the projection reaches the driver through
  the counts query alone, which those routes do not run. On `bd list` it works
  in text mode too, and is refused with `--watch`, with the `--parent` tree
  walk, and with `--format`, where a caller's template could print a dropped
  field with nothing to mark it.

- **`--brief-deps` on `bd show`, and `brief_deps` on the HTTP detail read**
  (#5546, #5547, #5549). `bd show --json` inlines every dependency at full
  body, so reading one issue costs the free-form text of everything it depends
  on. On one real 16,051-issue store a single `bd show --json` returned 214,456
  bytes, of which 193,039 was the `dependencies` array and one dependency
  carried a 180,975-byte `notes` field. The dependents side of the same detail
  view already had a shallow shape for this reason (be-4d36f2, where hub beads
  cost 5-13 GB); dependencies never got it. The new flag projects each
  dependency down to `id`, `title`, `status`, `issue_type`, `priority` and
  `dependency_type`, which measured 89.3% smaller on that call. It is opt-in
  and the default payload is unchanged.

- **`bd serve` gets optional bearer authentication, and requires it beyond
  loopback** ([#5516](https://github.com/gastownhall/beads/pull/5516)). The
  help used to say "no authentication and no TLS", and the first half of that
  is what kept the server on a developer's loopback. Two flags answer it, and
  both default to today's behavior: a `bd serve` with no arguments is byte for
  byte the server it has always been.

  `--auth-token-file` names a file of accepted tokens, one per line, all
  accepted. Every operation except `GET /healthz` then requires
  `Authorization: Bearer <token>` — `GET /v0/beads/context` included, because
  it reports the repo root, beads directory and database name. There is
  deliberately no `--auth-token`: a credential passed as an argument is
  readable out of `ps` by every local user. `BEADS_SERVE_TOKEN_FILE` is the
  environment fallback.

  **Rotation is a file rewrite, with no restart.** The accepted set is not a
  startup snapshot: the file is re-read while the server runs, at most once a
  second for the whole process, on the accepting path as well as on a
  mismatch — which is what makes REVOCATION work and not just rotation. Write
  `{new,old}`, roll the clients, drop `old`; both take effect within about a
  second. A failed or empty re-read keeps the last-good set and logs, so a
  writer that truncates before writing cannot lock every client out.

  **`--allow-non-loopback` now requires a token file.** Beyond loopback,
  reaching the address would otherwise be the whole authorization.
  `--insecure-no-auth` is the explicit, auditable way to say you meant that
  anyway. There is still NO TLS: a deployment beyond loopback supplies
  confidentiality itself. `--allowed-host` (repeatable, exact, no wildcards)
  adds Host header values to the DNS-rebinding allowlist, which is what a
  service deployment behind a DNS name trips over first.

  `401` / `unauthenticated` joins the frozen code vocabulary, with
  `WWW-Authenticate: Bearer` and a FIXED `detail` that never echoes the
  presented credential. A missing header, a wrong scheme and an unrecognized
  token are one code deliberately — telling them apart would tell an
  unauthenticated caller which guess was closer. **A token is a shared secret
  granting the whole surface, not an identity**, so `actor` is still
  caller-asserted provenance and not the authenticated principal.

- **Six more v0 operations, and the guarded-write token they compose from**
  ([#5506](https://github.com/gastownhall/beads/pull/5506),
  [#5507](https://github.com/gastownhall/beads/pull/5507),
  [#5508](https://github.com/gastownhall/beads/pull/5508),
  [#5510](https://github.com/gastownhall/beads/pull/5510),
  [#5535](https://github.com/gastownhall/beads/pull/5535),
  [#5536](https://github.com/gastownhall/beads/pull/5536),
  [#5540](https://github.com/gastownhall/beads/pull/5540)). Each is the wire
  half of a role that already existed and was already pinned on three legs.

  - `POST /v0/beads/issues/{id}:release` — give a claim back, what `bd
    unclaim` spells. **It is NOT idempotent, and that is the one thing to read
    before adopting it**: a release over a row holding no claim is `409` /
    `not_releasable`, never a 200, because the post-state is anonymous. Do not
    read the code as "already released" — it also covers "the status will not
    accept a release", which is true of a row that is still ASSIGNED. Read the
    row. `expected_assignee` is a compare-and-set on the holder and `force`
    ignores it; the two may not be sent together.
  - `POST /v0/beads/issues:claimNext` and `POST /v0/beads/issues:batchClose` —
    take the next ready bead in one round trip, and close many as one act.
  - `GET /v0/beads/issues:count` and `GET /v0/beads/dependencies:count` — the
    two counting reads, each with its role's whole filter surface.
    `dependencies:count` bounds `issue_id` at 100 anchors.
  - `GET /v0/beads/issues/{id}/related` — one anchor's neighbors, direction-
    parameterized and type-filterable. The length of `items` is a NEIGHBOR
    count and not an EDGE count: a target that is an `external:` reference or
    another repository's id has no far end here and is silently not a
    neighbor, while the same edge IS a row on `GET /v0/beads/dependencies`.
  - `expected_version` on close, reopen and delete, and `revision` on `GET
    /v0/beads/issues/{id}` — the row's optimistic-concurrency token, so a
    read-modify-write loop can start from a READ instead of seeding its first
    guard from a write it did not want to make. A miss is `409` /
    `precondition_failed` and writes nothing. **`revision` is deliberately NOT
    on the listing rows**: `types.IssueWithCounts` is also the JSONL
    interchange record, so a token there would write a per-write-random value
    into every `bd export` line and every auto-flushed `issues.jsonl`.

  `not_releasable` joins the frozen 409 vocabulary. Every one of these carries
  the claim's posture unchanged: `actor` is caller-asserted provenance, hooks
  do not fire, the per-command auto-commit machinery does not run, and the only
  durable effect is the single storage commit the role makes in its own
  transaction.

- **`bd serve` answers a `Bd-Project-Id` header, and says so in
  `capabilities`.** A client that knows which workspace it means to address
  stamps the request with that workspace's project id; a server that serves a
  different one refuses before any database work, so a misdirected read or
  write mutates nothing. An ABSENT header is served exactly as before, so this
  is additive wire surface and not a new precondition. `GET /healthz` and `GET
  /v0/beads/context` are exempt — liveness must answer whatever workspace a
  caller believed it reached, and the handshake is where a client LEARNS the id
  to stamp with. Detect it with the `project.enforce` capability.

- **A durable events journal, and `bd events` to read it** (bd-opisf). External
  tooling that wants to stay in step with a workspace had two options and
  neither was a feed: a fire-and-forget script hook that may not run, or polling
  whole snapshots and diffing them — which still misses anything that changed
  twice between two reads. The journal is the third. Every committed issue
  mutation writes one ordered record, in the same transaction as the mutation
  itself, and a consumer replays those records from a checkpoint it can resume.
  Each record carries the operation, the mutated id, and the issue's full
  post-mutation snapshot (including `is_blocked`), so a mirror stays correct
  without re-querying the graph.

  **It is off by default and opt-in per workspace**: `bd config set
  events-journal true`, or `BD_EVENTS_JOURNAL=1`. Nothing is recorded while it
  is off, so a workspace that never enables it pays nothing.

  **On means bounded.** Two retention floors define the window every consumer
  is guaranteed — `events-journal-retain-days` (default 7) and
  `events-journal-retain-rows` (default 100000), each disabled by setting it to
  0 — and beads enforces them for you: after a mutating command commits, and on
  a timer inside `bd serve`, it deletes the prefix the floors do not protect.
  The pass is throttled by a persisted watermark (about one an hour per
  workspace — or sooner after a large burst of writes), runs in its own
  transactions outside the mutation's, is capped at a few batches so a long
  backlog drains over several commands rather than stalling one, and can never
  fail a command — a failure is logged and skipped. It maintains the workspace
  whose command triggered it, so a workspace only ever written remotely (a
  routed `bd create --repo`) relies on commands run in it, or on its own `bd
  serve`. Setting both floors to 0 keeps every record forever;
  `events-journal-auto-prune false` keeps the floors but leaves deletion to you.
  All four are `config.yaml` keys with `BD_EVENTS_JOURNAL*` environment
  equivalents.

  `bd events tail --since <seq>` prints records as JSON lines and `--follow`
  keeps printing them as writes commit; `bd events export` prints the journal
  from the beginning; `bd events prune --before <seq>` takes an earlier,
  on-demand cut below the floors — it cannot cut deeper than they allow, so
  shrinking the retained window means lowering them. The floors are a
  recent-window guarantee, not a consumer watermark: size them for the longest
  outage a consumer must survive.

  **A read that cannot resume fails instead of lying.** When `--since` falls
  below the oldest retained record, the read neither skips ahead to the
  surviving suffix nor returns an empty success: both are silent record loss
  that a cursor cannot detect. It exits 1 with a typed
  `events_journal_truncated` error carrying `since`, `floor` and `head`, so a
  consumer can choose between resuming at `floor - 1` with a known gap and
  re-baselining. Under `--json` that is the error payload; mid-`--follow` it is
  one compact JSON object on a line of the JSONL stream it interrupts, because
  the consumer on the other end is a line reader. An interior gap — which
  nothing in bd can produce, since a prune only ever removes a prefix — refuses
  the same way.

  The journal is clone-local working-set state (`dolt_ignore`d): never
  versioned, never pushed or federated, per branch, and per replica — each
  clone counts its own seq space, so a checkpoint from one replica is
  meaningless against another. `bd dolt pull` and merge-settled changes, raw
  `bd sql` DML, store-open migrations, and compaction rewrites are not
  journaled. The record contract and every boundary are documented in
  [docs/reference/events-journal.md](docs/reference/events-journal.md).

- **`is_blocked` is a documented optional member of the /v0 OpenAPI issue
  schemas** (bd-opisf) — `Issue`, `IssueWithCounts`, `IssueDetails`,
  `IssueWithDependencyMetadata` and `TreeNode`, the five welded to the
  canonical Go struct. It is the persisted readiness projection: true when an
  open blocking dependency keeps an issue out of the ready set, derived from
  the dependency graph and maintained by the server, never set by a client. It
  is omitted when false, so absence means false. Journal snapshots are what set
  it; readiness itself is computed exactly as before.

- **`bd serve` grows the write half of the agent loop** (v0 wire surface,
  [#5410](https://github.com/gastownhall/beads/pull/5410),
  [#5417](https://github.com/gastownhall/beads/pull/5417),
  [#5422](https://github.com/gastownhall/beads/pull/5422),
  [#5423](https://github.com/gastownhall/beads/pull/5423),
  [#5429](https://github.com/gastownhall/beads/pull/5429)). v0 shipped with one
  mutation, the claim, which meant an HTTP client could take work and had to
  fork a `bd` subprocess to finish it. Five operations close that loop:

  - `POST /v0/beads/issues/{id}:close` and `POST /v0/beads/issues/{id}:reopen`
    — the two halves of the lifecycle move. Both are idempotent, and both say
    so in the body rather than in a status: a close of a closed issue answers
    `200` with `already_closed: true`, a reopen of an open one
    `already_open: true`, and each still carries the row.
  - `PATCH /v0/beads/issues/{id}` — partial update of one issue. The `patch`
    object publishes nineteen members; four of them (`estimated_minutes`,
    `external_ref`, `due_at`, `defer_until`) are nullable, and an explicit
    `null` CLEARS them. That set is closed and machine-checked against the
    document, so a `null` on any other member is a `400` rather than an
    unannounced clear. A patch that changes nothing answers `200` with
    `changed: false`.
  - `POST /v0/beads/dependencies:add` — up to 100 edges per request, asserted
    all or none, echoed back in request order. A refusal names the offending
    edge as `edges[i].member`, so a client learns which edge and which member
    rather than that something in the batch was wrong.
  - `POST /v0/beads/dependencies:remove` — removes exactly the edge the body
    names. Idempotent at the role: an edge that is not there is
    `removed: false` with a `200`, never a 404, so a replayed teardown does not
    have to classify an error to discover it already ran.

  **Every one of them carries the claim's posture, unchanged.** The `actor` is
  caller-asserted provenance for the audit trail and not authenticated
  identity; hooks do not fire; the per-command auto-commit machinery does not
  run. The only durable effect is the single storage commit the role makes
  inside its own transaction, so durability is per request on every write, not
  just the claim.

  **Three codes join the frozen error vocabulary**, all 409 and all typed so a
  client never has to substring-match a message. `not_closable` is close policy
  refusing an unforced close of an issue with open children or a live blocker.
  `dependency_exists` is the pair that already carries an edge of a DIFFERENT
  type, and carries `existing_type` and `requested_type`. `dependency_cycle`
  covers both never-makes-progress refusals: the plain scheduling cycle, and a
  blocking edge against the issue's own ancestor or descendant — the second
  carries `issue_id`, `blocker_id` and `blocker_is_ancestor`, and their ABSENCE
  is what tells a client which of the two it got. Every extension member is
  read off the refusing transaction's own typed error; the hierarchy members
  have no other source, since the conflicting edge may exist only inside the
  batch that was rolled back. An edge naming an endpoint this database can see
  the absence of is a `400` on `edges[i].issue_id` or `edges[i].depends_on_id`,
  not a 404: the request BODY is what is wrong, and there is no path id to have
  missed.

  **`GET /v0/beads/issues/{id}` takes two new parameters.**
  `include_comments` populates `comments` with the full comment bodies and
  `include_dependents` populates `dependents` with the issues that depend on
  this one, each carrying its edge type. Both default to false, because both
  are extra reads nobody should pay for by accident — and when a caller does
  not ask, `comment_count`, `dependent_count` and `comments_omitted` still
  report what was left out, so an absent `comments` is never ambiguous between
  "no comments" and "not included in this response". A malformed boolean is
  this operation's own `400 invalid_argument` / `reason: "invalid_value"`,
  which is deliberately not the `unknown_parameter` a client should read as
  version skew.

  `GET /v0/beads/context` advertises `issues.close`, `issues.reopen`,
  `issues.update`, `dependencies.add` and `dependencies.remove` alongside the
  rest — derived from the registered handlers as always, so probing
  `capabilities` remains the way to ask one server what it can do.

- **The two dependency-endpoint refusals now carry an identity** (bd-yby99.9).
  `DependencyEditor.AddDependencies` refuses an edge whose SOURCE names no row,
  and one whose TARGET names no row this database can see the absence of. Both
  were "an error" and nothing more — not the same error text on every backend,
  and impossible to tell apart from an infrastructure failure — which left the
  role's typed-refusal vocabulary with a hole every other refusal it can raise
  had already filled. They are now `errors.Is`-matchable as
  `ErrDependencySourceNotFound` and `ErrDependencyTargetNotFound`, carried by a
  `*DependencyEndpointNotFoundError` naming the refused edge and which of its
  endpoints was absent, all three re-exported from the root package beside the
  conflict types. What is NOT refused is unchanged: an `external:` reference
  and another repository's id are still accepted as external targets.

  The user-facing message is byte-identical on the embedded and store paths
  (`issue <id> not found`), and the proxied-server path now renders that
  message too instead of a raw foreign-key violation — the endpoint is
  classified from the refusing transaction rather than parsed out of the
  driver's prose, so the two plumbings agree on the text as well as the type.
  The shared `DependencyEditor` contract asserts the sentinel and the typed
  fields for each endpoint, alone in a request and mid-batch, with the
  mid-batch half reading the graph back at zero edges. `RemoveDependency` is
  unaffected: a removal that finds no edge is a success, not a refusal.

### Changed

- **Text-input commands now REFUSE two sources instead of silently picking
  one** (#5332). `bd comment`, `bd note` and `bd comments add` used to apply a
  precedence order when a caller gave both positional text and `--stdin` or
  `--file`: the flag won and the positional text was dropped with no message.
  Naming two sources is now an error — `cannot combine positional text "..."
  with --stdin` — because the dropped half was, in every case we could find,
  the text the caller actually meant. Single-source invocations are unchanged.
  Scripts that relied on the old precedence (passing both and expecting the
  flag to win) must drop the redundant argument.

  `bd human respond` gains the same layer, so its response text can come from
  positional args, `--response`, `--file` or `--stdin`; `--response` is no
  longer a required flag. `bd human dismiss` takes its reason positionally as
  well as via `--reason`. Both now accept trailing positional text
  (`<id> [text...]`), matching `bd comment`'s contract — a second argument is
  free text, never a second issue id.

- **`bd list --status=all` no longer hides boolean-pinned beads** (#5332).
  `all` promises every status, but the pinned exclusion was decided by
  comparing the raw selector string to `pinned`/`hooked`, so `all` fell through
  and kept forcing `Pinned=false`. The same string comparison also missed every
  multi-status set containing them, so `--status=pinned,closed` excluded the
  pinned beads it explicitly asked for. Both now lift the exclusion. **Scripts
  that used `--status=all` as a way to list everything EXCEPT pinned beads will
  see pinned beads appear**; add `--no-pinned` to keep the old result.
  `--ready` is unaffected: it forces status `open` and ignores the selector, so
  an `all` it never applies has no pinned side effect either.

- **`bd human list` matches `bd list`'s status handling, and hides no bead
  type** (#5332). It used to pass `--status` through unvalidated and show
  closed beads by default. Now done/frozen statuses and pinned beads are hidden
  by default, `--status` is validated against the built-in and custom status
  names (comma-separated sets and `all` supported, so a typo is an error rather
  than an empty list), and every bead TYPE still shows — gates, wisps, infra
  beads and templates — because a `human` label is an explicit request for a
  person's attention. `bd human stats` counts across every status, as before.

- **`bd human stats` classifies dismissals by close-reason PREFIX** (#5332).
  It matched the substring `dismiss` case-insensitively anywhere in the close
  reason; it now requires the `Dismissed` prefix that `bd human dismiss`
  writes, shared as one constant so the writer and the classifier cannot drift.
  Beads closed before this with a lowercase or mid-string "dismiss" in their
  reason move from the Dismissed count to the Responded count. Forward
  behavior is unchanged.

- **`bd label add`/`remove` over many issues is no longer one atomic
  transaction** (ga-26w10,
  [#5489](https://github.com/gastownhall/beads/pull/5489)). Both routes — the
  direct one and the proxied-server one — now end at `issueops.Lifecycle` and
  `issueops.Reader`, which deletes the four-way `(add|remove) × (wisp|durable)`
  switch the proxied route hand-rolled below the roles and the second
  implementation of the command that came with it. The plane is resolved
  inside the role's own transaction, so the wisp branch is gone rather than
  moved.

  **The deliberate delta:** a multi-issue edit is N guarded updates rather than
  one raw transaction, so `bd label add a b c mylabel` records three history
  entries instead of one, and a failure on the third leaves the first two
  written where the old shape rolled them back. Neither is obviously right —
  the new one attributes each edit to its own issue and lands what it could —
  and the atomic shape is expressible today as one `issueops.BatchApplier`
  update item per issue. The proxied route's resolution failure also lost its
  `label added: ` prefix, which named a write the command never attempted;
  both routes now print the direct route's message.

  `bd label list-all` and `bd label propagate` are unchanged and stay off the
  role: no role answers "every distinct label in this workspace", and
  propagate is a search plus a fan-out needing the same batch question.

- **The settings plane no longer serves the KV plane by any read** (bd-rfwtv,
  bd-klko9). `bd kv` keys and the `bd remember` memories nested under them are
  user data stored as config rows; they ride in the settings table without being
  settings. Listing them was already excluded, but a caller naming an exact key
  still got the value — and since `bd remember` derives its key from the content
  it stores, the keys are guessable, so `bd config get kv.memory.<slug>` and
  `GET /v0/beads/config/kv.memory.<slug>` (a surface whose only credential is
  an optional shared bearer, and whose redaction decides on the key NAME while
  a memory's secret is in the VALUE) walked around the exclusion.

  A point read of a `kv.` key now answers exactly as a key nothing ever stored
  does — the echoed key, an empty value, a nil error — so a caller cannot tell a
  refusal from an absence. `bd config show` no longer prints those rows under
  `source=database` either; it reads the table raw and prints values in full, so
  it had inherited neither filter.

  **Nothing is deleted and nothing becomes unreachable.** The rows are
  untouched, and the surfaces that own them — `bd kv get`, `bd kv list`,
  `bd recall`, `bd memories` — read the store directly and are unaffected.
  `bd config set` and `bd config unset` still take a verbatim `kv.` key, which
  keeps the escape hatch for a wedged memory. What is gone is reading memory
  contents through `bd config get`, `bd config list` and `bd config show`.

  **This is plane hygiene, not a confidentiality boundary.** `bd serve` has no
  authentication, and it serves every memory in full through
  `/v0/beads/memories` by design — anyone who can reach the server can still
  read them. What this closes is the settings plane's habit of handing user
  data to callers that asked for configuration: the routes that get republished
  into agent transcripts and pasted into issues. Do not read it as a fix that
  makes memories secret from someone who can already reach the port.

- **`bd search` now includes closed issues by default** (bd-t5yex). The
  dominant real-world search query is "was this already found/filed/fixed?" —
  exactly the query where silently excluding closed issues produced a false
  "no" (downstream repos grew shell wrappers purely to undo the old default).
  `bd list` keeps its open-only default. Narrow explicitly with `--status open`
  (or any status list) where the old behavior is wanted; `--status all` still
  works and is now redundant. Applies to both the embedded and proxied-server
  paths.

  Two consequences to know: matches beyond `--limit` (default 50) are dropped
  under a status-blind sort (priority, then created), so a broad query on a
  large DB can now fill the page with closed matches and silently drop open
  ones — use `--status open` or raise `--limit` when hunting live work. And
  `bd query` (plus the HTTP `q=` endpoint) deliberately keeps its closed-
  exclusion default with opt-in `--all`; only `bd search` changed.

### Removed

- **BREAKING (published `backend` package): orphan handling is gone** (bd-gwryr).
  `BatchCreateOptions.OrphanHandling` and its four modes never did anything
  except cost a query. Removed symbols: `backend.OrphanHandling`,
  `backend.OrphanAllow`, `backend.OrphanResurrect`, `backend.OrphanSkip`,
  `backend.OrphanStrict`, and the `storage` originals they aliased
  (`storage.OrphanHandling` and the same four constants), the
  `BatchCreateOptions.OrphanHandling` field, and `issueops.CheckOrphan`.

  **Migration for out-of-tree consumers: delete the option.** Every call site
  in bd passed `OrphanAllow` (or left the field at its zero value, which took
  the same branch), and `OrphanAllow` is exactly what the code still does —
  a hierarchical create whose parent is missing is accepted, and the child's
  auxiliary counter row is skipped because it would have no owner. Only
  `OrphanStrict` (reject the create) and `OrphanSkip` (silently drop it)
  behaved differently, and nothing but one conformance case ever passed them;
  a consumer that relied on either must now check for the parent itself before
  calling. `OrphanResurrect` was never implemented — it shared a branch with
  `OrphanAllow`, and the resurrect-a-deleted-parent behavior its doc comment
  described left with the JSONL sync layer long ago.

  Every hierarchical-ID create paid a `SELECT COUNT(*)` parent-existence probe
  to feed a switch whose only reachable arm ignored the answer. That query is
  now gone from the create path.

### Fixed

- **`--label-any` is no longer silently dropped by `bd ready` and
  `bd ready --claim`.** The ready-work WHERE builder emitted clauses for
  `--label` and `--exclude-label` but none for `--label-any`, so the OR-set
  filter was ignored on the ready/claim path (with or without `--parent`) on
  every backend — while `bd list`/`bd search` honored it. On an *atomic claim*
  this was dangerous rather than merely wrong: a worker fencing itself to its
  own lane (`bd ready --claim --label-any lane-a --parent epic-1`) would
  happily claim another lane's issue and believe it was fenced. `--label-any`
  now emits an OR-set membership clause that AND-combines with `--label`,
  `--exclude-label`, and `--parent`, exactly as the flag help promises; an
  exhausted lane now claims nothing instead of falling back to unfenced work.

- **FreeBSD builds compile again** (#5661). The dbproxy process-identity arc
  (procid, unverified-process) shipped linux/darwin/windows implementations
  with no fallback, breaking `GOOS=freebsd` compilation — caught only by the
  release build's cross-compile (GH#5662 tracks the CI gap). Unsupported
  platforms now get stubs that fail proxied-mode spawns with a clean,
  actionable error; classic (non-proxied) bd is unaffected, matching what
  v1.1.2 shipped on freebsd.
- **Telemetry no longer taxes every bd invocation.** Two startup costs paid on
  every command, measured during the post-wave startup audit, are gone. First,
  the machine-scoped distinct ID was recomputed on every invocation — a fork
  of the platform machine-id probe (`ioreg` on macOS, 20.2±1.2ms) — even with
  `BD_DISABLE_METRICS=1` and even for `bd --version`. The ID is now resolved
  only when metrics are enabled and cached at `~/.beads/machine-id` (0600), so
  the probe runs at most once per machine; a probe failure is retried next run
  rather than cached. Second, every invocation unconditionally spawned a
  detached `bd send-metrics` child — a full re-exec of the binary plus an
  HTTPS upload attempt, with no check that anything was queued. The spawn now
  requires at least one queued event batch and is throttled to one attempt per
  5 minutes (marker: `eventsData/.last-flush`); queued events still upload,
  just batched. Telemetry content, opt-out semantics, and the sanctioned
  endpoint pinning are all unchanged.

- **A long or multi-paragraph close reason renders as body text in `bd show`**
  ([#5595](https://github.com/gastownhall/beads/pull/5595)). Every other
  free-text field — description, design, notes, acceptance criteria, comments —
  reaches the terminal through the markdown renderer, which word wraps and
  indents. The close reason did not: it was formatted into the metadata block
  beside `Owner:` and `Created:`, so it never wrapped and its second and later
  lines read as separate metadata entries, a blank line and a bare `- bullet`
  sitting directly under `Created:`. `bd close --reason-file` exists so agents
  can write structured close reports, and those were exactly the reasons that
  came out corrupted. A reason that still fits one metadata line — nearly all
  of them, including one-liners that arrive from a file with a trailing
  newline — is unchanged; anything larger now gets a `CLOSE REASON` section
  rendered by the same call the other body fields make. The JSON payload is
  untouched. The compaction savings line moved into the metadata block at the
  same time, so it can no longer be stranded below that section, and all five
  `bd show` render paths now report it alike.

- **Script hooks now fire on both write plumbings** (bd-opisf). bd has two write
  plumbings and only one of them ran the workspace's hook scripts. The
  DoltStorage decorator chain fires `on_create`/`on_update`/`on_close` after
  every mutation it lands; the unit-of-work plumbing — the one **proxied-server
  mode** writes through — fired nothing from the plumbing, so an integration
  wired to `.beads/hooks/` silently missed every mutation that went through it.
  Four commands (`update`, `close`, `reopen`, `gate`) had grown hand-wired hook
  calls of their own to paper over the gap; every other write — `create`, batch
  create and close, claim, `comment`, label edits, dependency edits — ran no
  hook at all. A notifying wrapper now fires them from the plumbing, buffered
  during the transaction and drained only after the commit succeeds, so a
  rolled-back or retried write reports nothing.

  Six changes are visible in proxied mode, and all six move it toward what the
  embedded path has always done (a seventh, below, applies to both):

  - **One firing site per plumbing.** The four per-command hook calls are gone.
  - **`bd update -s closed` fires `on_update` only.** It used to fire `on_close`
    as well. `bd close` is what fires `on_close`, on both plumbings.
  - **`bd close` no longer fires an `on_update` ahead of its `on_close`.**
  - **Hooks run fire-and-forget, after the write commits**, rather than
    synchronously inside the command.
  - **`no-hooks` is honored.** The per-command calls ignored it, so hooks fired
    in proxied mode even when they were switched off.
  - **An idempotent re-close fires `on_close`**, and a double `bd gate resolve`
    fires it too. The per-command calls suppressed both; the embedded path has
    always fired for a close that succeeded, changed row or not, so a script
    reconciling on "it is closed" is told every time rather than only the first.

  And one change every workspace sees, embedded and proxied alike: **a command
  now waits for its own fire-and-forget hooks before exiting**, bounded by the
  per-hook timeout (10s). Both plumbings run hook scripts on background
  goroutines, and a short command could return from `main` before one had even
  started — a hook that silently never fired. The wait happens after the store
  closes, so a hook script's own `bd` can open the workspace, and it runs on the
  error exit paths too, so a partial batch still delivers the hook for what it
  did commit. A command that fires no hooks waits on nothing.

  `bd serve` still runs no hooks, and `bd import` still fires none on either
  plumbing (both import through the batch-upsert engine rather than the
  per-issue verbs).

- **`--dolt-auto-commit batch`/`off` now actually defer version commits in
  SQL-server mode** (bd-4wamg). The mode was silently inert there: the CLI's
  auto-commit policy was embedded-only, and the storage layer minted one Dolt
  commit inside every write transaction regardless — measured at one commit
  per write through all five configuration paths (flag, config.yaml, env,
  `bd config set`, no-daemon). The write verbs now thread the batch/off
  deferral to the storage layer's commit sites, which leave writes in the
  working set for an explicit commit point (`bd dolt commit`), in server and
  embedded mode alike. The server-mode **default** changes spelling, not
  behavior: it used to resolve to `off` (while behaving like `on`); it now
  resolves to `on`, naming what a default server-mode write has always done.
  The `--dolt-auto-commit` help also stops claiming `Default: off`. On a
  shared server, staging is table-level, so an `on`-mode writer's commit may
  sweep up another client's deferred rows — deferral bounds who *creates*
  commits, not commit contents.

  Scope: embedded and direct SQL-server modes. Proxied-server routes never
  apply auto-commit policy (they short-circuit before it resolves), so
  batch/off remain inert there — tracked as a follow-up. A deployment that
  had explicitly configured `dolt.auto-commit: off` from the old help text
  note: in server mode that was behaving like `on`; it now genuinely stops
  minting version commits until an explicit `bd dolt commit`.

- **The no-ID "last touched issue" fallback on `bd update` / `bd close` is
  now interactive-only** (bd-m00pb,
  [#4839](https://github.com/gastownhall/beads/pull/4839)).
  Previously a scripted `bd update $ID ...` with an accidentally empty `$ID`
  silently mutated whatever issue was touched last — a real agent session
  corrupted an unrelated closed bead this way. The missing-ID refusal happens
  in argument validation, before any store open, migration, or auto-import
  side effect. The fallback now requires stdin to be a terminal;
  `BD_NON_INTERACTIVE=1` and `CI=1/true` also disable it. Scripts that
  intentionally relied on it can set `BD_LAST_TOUCHED_FALLBACK=1` (or `=0` to
  disable the fallback everywhere). Explicit IDs and `bd show --current` are
  unaffected.

- **A dated defer now means what it says: the issue returns to the ready front
  when the date passes** (bd-i8qx8). `bd defer --until` and `bd update --defer`
  set `status=deferred` plus a `defer_until` timestamp, but nothing ever
  flipped the status back — so an expired defer stayed invisible to `bd ready`
  forever until a human ran `bd undefer`. One deployment measured 241 issues
  (including P1s) silently dark this way, regenerating daily from
  correct-looking automation.

  Ready-front reads and claims (`bd ready`, `bd ready --claim`, `bd list
  --ready`, and the serve/proxied Reader roles) now run a lazy wake sweep
  first: every issue (and wisp) with `status=deferred` and `defer_until <=
  now` flips to `status=open, defer_until=NULL` — byte-identical to what `bd
  undefer` writes, so a later dateless re-defer cannot inherit a stale past
  date. Each wake records a `status_changed` event (actor `bd-defer-wake`).

  What does NOT change: a **dateless** defer (`bd defer` with no `--until`,
  `defer_until` NULL) is the indefinite icebox and never auto-wakes — `bd
  undefer` remains its only exit. The sweep is a no-op when nothing has
  expired (no dolt commit is minted), advisory by contract (a ready listing
  never fails because the sweep could not run — strict `--readonly` skips it),
  and identical in embedded, server, and proxied-server modes.

- **`bd delete --cascade` no longer marks LIVE issues as deleted in other
  issues' text** (bd-x82so). When the deletion named a wisp, the set used to
  rewrite `[deleted:<id>]` citations was computed differently from the set that
  was actually deleted: dependents reachable only through a wisp root were left
  in place, and then their neighbours' `description`, `notes`, `design` and
  `acceptance_criteria` were rewritten to say those still-present rows had been
  deleted. `bd wisp gc` ran exactly this path, because it hardcodes cascade.

  **Text already corrupted by earlier builds is NOT repaired.** Those are stored
  bytes and nothing here scans for them; a `[deleted:<id>]` marker naming a row
  that still exists is a leftover from a pre-fix build. Searching for markers
  whose id is still present will find them.

  Three consequences of the same fix, all on the direct and embedded routes and
  all converging onto what the team-server route already did:

  - `bd delete <wisp> --cascade` now deletes the wisp's dependents in **both**
    planes, as `--cascade` has always claimed. It previously stopped at the
    wisp, reported only what it deleted, and gave no sign it had done less than
    asked. Anyone relying on that under-deletion will see more rows go — and
    `bd wisp gc` now removes durable rows hanging off abandoned wisps.
  - `bd delete <wisp>` with a durable dependent, unforced, is now **refused**
    with `issue <id> has dependents not in deletion set`. It previously
    succeeded and silently orphaned the dependent.
  - `bd delete <wisp> --force` now lists that dependent in the orphan report,
    where it previously reported nothing, and the removed cross-plane edge is
    counted.

  The two set computations are gone: one `DeletionSet`, resolved once, is handed
  to the neighbourhood read, the delete and the rewrite, so they cannot drift
  apart again.

- **`bd query` no longer drops matches from an `OR` or `NOT` query** (bd-pohmh).
  **Your results will change, and they change by getting bigger.** To answer an
  expression the storage filter cannot express, both routes fetched
  `max(3 × --limit, 100)` rows and applied the expression to those in memory. A
  match past that window was absent from the page — and the truncation hint said
  nothing, so the answer looked complete. `bd query "type=bug OR label=urgent"`
  over a workspace with more than a hundred issues has been returning an
  arbitrary prefix of its answer.

  The window is gone: the expression is now evaluated against every candidate
  row, the page is the first `--limit` MATCHES, and the truncation hint is
  exact. **The cost is real and worth knowing:** a broad `OR`/`NOT` expression
  over a large workspace now reads every row its plain filters admit, where it
  used to read at most a few hundred. Narrow the expression — the parts of it
  that ARE plain filters still bound the read — if that matters to you.

  Two smaller changes ride along. `bd query --offset N` **now works with
  `OR`/`NOT` expressions**, where it used to be refused outright: the offset
  skips matches, which is only meaningful now that every match is seen.
  `--offset` with `--sort` is still refused, with new wording, and so is a
  malformed expression: both messages now come from one place instead of one per
  route.

- **`bd purge --pattern` and `bd prune --pattern` no longer report success on a
  malformed glob** (bd-pn231). Both routes matched patterns with the error
  return of `filepath.Match` discarded, so `bd prune --pattern '[' --force`
  printed "No closed beads to prune" and exited 0 — indistinguishable from a
  correct pattern over an empty set. A pattern the glob syntax refuses is now an
  error naming the pattern, on both routes.

- **`bd purge --dry-run` and `bd prune --dry-run` no longer fail on sweeps the
  real run completes** (bd-pn231). The preview asked for the refuse-if-any-
  external-dependent path while the confirmed run asked for the force path, so a
  dry run could report "has dependents not in deletion set" for a sweep
  `--force` finishes by orphaning them — and the CLI then swallowed the error
  and printed zeros. The preview and the run now ask the same question.

### Added

- **`GET /v0/beads/dependencies/blocking` — the blocking decoration `bd list`
  prints, over HTTP** (bd-5k21g). `bd serve` now answers
  `?ids=bd-abc&ids=bd-def` with, per requested id, its open blockers, the beads
  it blocks and its open parent — the derived summary `bd list` renders as
  `blocked by:` and `blocks:`, from the same `issueops.BlockingAnnotator` both
  CLI routes call. `capabilities` gains `dependencies.blocking`.

  It is not a mode of `GET /v0/beads/dependencies`: that one returns stored edge
  rows and applies nothing, this one applies the status rule and separates the
  parent edge from the blocking ones. It is also not a page — the number of ids
  the caller named is what bounds it, so there is no `limit` and no cursor. An
  id that names nothing is annotated bare rather than reported missing, which is
  the same answer the CLI gives.

- **`GET /v0/beads/dependencies/tree` — the `bd dep tree` walk over HTTP**
  (bd-lx5iu). `bd serve` now answers a recursive walk from one root —
  `?root_id=bd-abc&direction=both&max_depth=3&status=open` — with the flat
  `TreeNode` list `bd dep tree --json` emits, from the same
  `issueops.TreeWalker` both CLI routes call. `capabilities` gains
  `dependencies.tree`.

  It is a sibling of `GET /v0/beads/dependencies` rather than a mode of it: that
  one answers raw stored edge rows for many anchors at one hop, this one recurses
  from a single anchor. A `root_id` that names nothing is a 404 here, where an
  unknown `issue_id` there is reported in `missing` — one anchor leaves no other
  answer to preserve. There is no `max_rows`: the defensive cap is a CLI circuit
  breaker and refusing a whole answer is not something this surface offers a
  remote client.

- **`GET /v0/beads/issues:query` — the `bd query` expression language over
  HTTP** (bd-pohmh). `bd serve` now answers boolean expressions —
  `?q=type%3Dbug+OR+label%3Durgent` — with the same page shape the ready listing
  returns, under the same completeness guarantee as the CLI, because both call
  one `issueops.Querier`. It is the only operation on the surface that takes a
  disjunction; every filter parameter of `GET /v0/beads/issues` narrows. There
  is no cursor and no `offset`: a predicate query's matching set is assembled
  outside the database, so there is no keyset position to encode. `capabilities`
  gains `issues.query`.
- **`POST /v0/beads/issues:delete` — deleting named beads over HTTP**
  (bd-x82so). `bd serve` gains the second destructive operation on its surface:
  a body of `ids` plus `cascade`, `force` and `dry_run`, answering with the
  counts and the orphan list. It calls the same `issueops.Deleter` `bd delete`
  calls, so the dependents guard, the all-or-nothing id resolution and the
  reference rewrite are the library's rather than the handler's — the endpoint
  cannot orphan a graph by omission. `cascade` and `force` both default false,
  which is the guarded mode. `capabilities` gains `issues.delete`.
- **`POST /v0/beads/issues:batchCreate` — creating many issues over HTTP**
  (bd-lu170). `bd serve` gains the second write on its surface and the first
  one that creates rows: a request carrying an `actor` and up to 100 items is
  created as ONE transaction, or none of it is. It runs on the new
  `issueops.BatchCreator` role that both `bd create --file` routes now call,
  and `capabilities` gains `issues.batchCreate`.

  **The server assigns every id.** There is no `id` member on an item, so this
  operation can never adopt or overwrite a stored row; the generated ids come
  back in `items`, in request order, which is the only place a client can learn
  them. `bd import` remains the upsert surface and is not published here.

  A dependency target may name an issue the workspace holds, an item created
  EARLIER in the same request, an `external:` reference, or an id belonging to
  another repository. Anything else is a `400` and nothing is created. Hooks do
  not fire and the per-command auto-commit machinery does not run, exactly as
  for `POST /v0/beads/issues/{id}:claim`.

- **`POST /v0/beads/issues:sweep` — bulk clearance of closed beads over HTTP**
  (bd-pn231). The operation behind `bd purge` (`tier: ephemeral`) and
  `bd prune` (`tier: durable`), on the new `issueops.Sweeper` role both CLI
  routes now call. `capabilities` gains `issues.sweep`.

  **This is the first DESTRUCTIVE operation on the surface, and nothing it
  deletes comes back.** `bd serve` binds loopback, and its bearer — optional,
  and where configured a single shared token admitting a client to the whole
  surface — grants no narrower right, so anyone who can reach the port with a
  credential it accepts can now clear closed beads: `--help` gained a
  DESTRUCTIVE OPERATIONS section and the `--allow-non-loopback` warning says so.
  A `durable` sweep with neither a cutoff nor a pattern is refused by the role
  itself rather than by the handler, and `dry_run: true` costs the same request
  without writing anything.

  **`protect_referenced` defaults to `true` here**, where the CLI's equivalent
  (`--ignore-references`) is opt-out. The local flag and the remote member
  therefore disagree on purpose: being authenticated here says nothing about
  whether this particular deletion was meant — one shared token admits a client
  to everything published — so a remote caller who omits a member should not
  get a weaker guard than the operator typing the same sweep locally. Send
  `protect_referenced: false` to match the CLI default.

- **`GET /v0/beads/config` and `GET /v0/beads/config/{key}` — the workspace's
  stored settings over HTTP** (bd-kzepq). `bd serve` now answers for the
  settings plane `bd config list` and `bd config get` read. Both operations run
  on the new `issueops.WorkspaceConfig` role the CLI reaches through the same
  accessor, and `capabilities` gains `config.list` and `config.get`.

  **A setting whose KEY marks it as credential-bearing** — the name contains
  `token`, `secret`, `password` or an API-key spelling, which
  `notion.token` does — **is published with `redacted: true` and no `value`.**
  Withheld values are OMITTED rather than masked, so a client cannot mistake a
  placeholder for configuration. The CLI is unchanged and still prints stored
  values in full: its caller already holds the database, while redaction is the
  whole control on this surface — a bearer here is optional and, where
  configured, shared and surface-wide, so it cannot withhold one value from one
  caller.

  There is no HTTP WRITE, deliberately. `bd config set` routes most keys to
  `config.yaml` or to git config before the database is ever reached, and both
  live on the client's filesystem, so a server-side write of (say)
  `export.auto` would return success and land a row nothing reads. The
  multi-source views — `bd config show`, `drift`, `apply`, `validate` — are
  off the role and off the wire for the same reason.

- **`bd serve` runs on a registered storage backend.** A downstream
  distribution that registers a backend
  (`github.com/steveyegge/beads/backend`.`Register`) could already use it from
  every ordinary `bd` command — `bd list`, `bd create` and friends dispatch
  the store open through the registry — but `bd serve` alone reimplemented a
  creation path of its own, hard-wired to Dolt's SQL wire, and refused the
  workspace before it got there. It now uses the store the root command
  already opened, through the same registry dispatch: zero creation calls of
  its own, so there is one creation path rather than two. The two issue roles
  are taken from BENEATH the workspace's hook decorator, keeping the
  documented "hooks do not fire" contract (`httpapi.Listen` refuses a
  hook-firing role rather than trusting the caller), and the store's lifecycle
  stays where it was — opened and closed by the root command, closed only
  after the server has drained. The startup line names the source
  (`db=roles`) and the backend (`mode="<name> (registered backend)"`).

  The permanent refusal is unchanged and unweakened: an embedded-Dolt
  workspace is still refused, because its commit protocol runs outside the SQL
  transaction and this server's per-request atomicity would be a lie there.
  The classification consults the registry before any Dolt-mode signal, which
  is the order the store open already resolves them in — and which also fixes
  a latent `CGO_ENABLED=0` bug where a registered workspace was handed to the
  Dolt provider and either failed with a misleading Dolt error or connected to
  a defaulted host and served the wrong database.

### Changed

- **BREAKING: `bd config list` and `GET /v0/beads/config` no longer enumerate
  the `kv.` plane, which is where `bd remember` memories live.** Memories and
  generic `bd kv` values are stored as config rows under `kv.` — user data
  riding in the settings table because there is one table, not because they are
  settings — and both doors onto the settings plane were listing them with
  their values. On the HTTP door that meant an unauthenticated
  `GET /v0/beads/config` handed every stored memory to anything that could
  reach the port, and the surface's redaction is no defence there: it decides
  on the KEY name, while a memory's content is in the VALUE, so a memory
  containing a credential under an innocuous slug was served verbatim.

  The exclusion is in the shared role both doors call, not in the HTTP handler,
  so the CLI and the API cannot drift apart on what a setting is. The whole
  `kv.` prefix goes, not just `kv.memory.`.

  **What still works:** `bd memories`, `bd recall` and `bd kv list` are
  unaffected — they read the store directly and are the purpose-built views
  over those rows. So are `bd prime`'s memory injection and export's memory
  extraction. Point access is unchanged and deliberately so: `bd config get
  kv.foo` and `GET /v0/beads/config/kv.foo` still answer a key named exactly,
  and `bd config set` / `bd config unset` still write and remove one. The
  enumeration was what made a memory discoverable; naming one you already know
  is a different question, and refusing it would take away the escape hatch of
  removing a wedged memory through `bd config unset`.

  **What breaks:** a script parsing `bd config list` or its `--json` output for
  `kv.` rows. Generic `kv.` rows have no other HTTP view today.

- **BREAKING: `bd --readonly serve` is now refused instead of binding a server
  that cannot do what it advertises.** On a Dolt SQL-server workspace this
  previously bound a fully functional, fully writable server — the flag was a
  silent no-op there — so anyone scripting `bd --readonly serve` as a "safe"
  read server loses it on upgrade and should drop the flag. The advertised capability set is a property of the
  build and includes the issue-claim operation, so under strict readonly the
  two database sources degraded differently and silently: a registered backend
  bound on its read-only store and answered every claim with an opaque 500
  while still advertising `issues.claim`, and a Dolt SQL-server workspace built
  its own writable provider and let every claim land, so the flag bought
  nothing. `bd serve` now refuses to start under `--readonly` (or `readonly` in
  config), before it resolves the workspace, so the answer is the same on both.

### Fixed

- **`GET /v0/beads/context` and `bd context` no longer describe every
  workspace as Dolt.** The shared identity projection hardcoded
  `backend: dolt` and copied `dolt_mode` and `database` unconditionally — and
  both of those default rather than fail, so a workspace on a registered
  backend was reported as `backend="dolt"`, `dolt_mode="embedded"`,
  `database="beads"`: a confident description of a topology it is not on, on
  the one endpoint automation is told to trust for a server's identity. The
  backend is now named as the store open resolves it, and the two Dolt-derived
  values are reported only for the Dolt backend; a registered backend reports
  the empty string for both, which is the only value `bd` can assert about a
  backend it does not implement. No wire field is renamed, retyped or dropped
  — both remain required strings, and the `v0` shape is unchanged.

- **Public surface for out-of-tree storage backends** (bd-h3dib.2). The
  storage backend contract and its conformance suite are now importable by
  external Go modules — the conformance-gated external-backend path promised
  on [#4415](https://github.com/gastownhall/beads/pull/4415). The new
  `github.com/steveyegge/beads/backend` package exports, by type alias, the
  engine interface (`backend.DoltStorage`) and every type its 164 method
  signatures reach, the sentinel errors the contract requires by identity,
  and thin wrappers over the backend registry (`backend.Register`); the
  suite moved from `internal/storage/conformance` to
  `github.com/steveyegge/beads/backend/conformance` unchanged, so an
  external backend proves itself with the same `conformance.RunAll` the
  embedded-Dolt reference runs in CI. Aliases mean identical types: a store
  compiled against the public names satisfies the internal interface, and
  `errors.Is` matches across the module boundary. A reflection-based drift
  guard (`backend.TestPublicSurfaceComplete`) fails in-tree if a contract
  change ever reaches an internal type with no public alias. **Stability:
  EXPERIMENTAL** — the contract still evolves; pin an exact version and
  re-run the conformance suite on every bump.

- **`bd serve` — the beads work surface over HTTP** (bd-serve v0). Automation
  clients and orchestrators that fork a `bd` subprocess per call can now hold
  one connection instead. `bd serve` binds loopback and answers six
  OpenAPI-specified operations: `GET /healthz`, `GET /v0/beads/context`,
  `GET /v0/beads/ready`, `GET /v0/beads/issues`, `GET /v0/beads/issues/{id}`,
  and the one write, `POST /v0/beads/issues/{id}:claim`. The wire contract is
  `internal/httpapi/spec/openapi.v0.yaml`; types are generated from it and
  `make api-check` fails a change that edits one without the other.
  `GET /v0/beads/context` reports which operations the running build actually
  implements, derived from the registered handlers, so a release cut mid-slice
  cannot advertise one that does not work.

  The reads run on `issueops.Reader`, the same role `bd show --json` reaches, so
  filter construction, workspace config, default limits and the wisp fallback
  happen *inside* the role rather than being re-performed per front door. The
  exact scope of that property — what is shared, what is machine-enforced and by
  which lint rule, and what is not enforced — is stated in full in
  `issueops.Reader`'s doc comment and in
  [engdocs/design/bd-serve-v0.md](engdocs/design/bd-serve-v0.md); it is narrower
  than "the CLI and the API cannot disagree", and the difference is written
  down.

  **Errors are typed.** Every non-2xx is an RFC 9457 `problem+json` document
  with a machine-readable `code` (`invalid_argument`, `invalid_cursor`,
  `not_found`, `already_claimed`, `not_claimable`, `busy`, `db_unavailable`,
  `internal`), one frozen HTTP status per code, and `Retry-After` on the
  retryable ones. A client that classified claim conflicts by substring-matching
  error text should switch to the typed 409: `assignee` and `issue_status` come
  from a read inside the losing transaction, never from parsing the sentinel's
  prose. A 5xx `detail` is a fixed string per code — driver errors embed the DSN
  — and the body's `request_id` correlates to the one log line that carries the
  real error.

  **Paging is an opaque keyset cursor** on `GET /v0/beads/issues`
  (`GET /v0/beads/ready` has none: its sort policies admit no keyset predicate).
  The token holds a position and a private encoding version and nothing else, so
  it does not expire, survives a restart, and has exactly one failure recovery —
  restart paging without it. It also carries no filters, so a cursor reused under
  changed filters is **not** refused and silently resumes from the old position;
  repeat every filter verbatim for a traversal.

  **Deliberate omissions, stated as contract rather than gaps.** No
  authentication and no TLS — the trust model is the loopback boundary the
  database already relies on, and `--allow-non-loopback` is an operator decision
  that additionally refuses unlimited (`limit=0`) reads. A DNS-rebinding `Host`
  allowlist with no off switch. Hooks do not fire on an HTTP claim (a
  user-controlled subprocess per mutation is an unbounded latency multiplier and
  an orphaned child at shutdown). The per-command auto-commit machinery does not
  run: durability is per request, and an idempotent re-claim by the current
  holder writes no commit at all, so a polling client cannot mint empty storage
  commits. An HTTP `actor` is caller-asserted provenance for the audit trail,
  not authenticated identity — the same thing `--actor` has always been.

  Embedded-Dolt workspaces are refused permanently: that backend commits outside
  the SQL transaction, so the per-request atomicity this contract states could
  not hold there.

  Operators: pass an explicit port. The default `--addr 127.0.0.1:0` takes an
  ephemeral one and carries **no** mutual exclusion — N servers run side by side
  on different ports. Probe readiness with `GET /v0/beads/ready?limit=1`;
  `/healthz` is liveness only and stays green while the database is unreachable.
  Deployment, the connection budget for a shared `dolt sql-server`, the
  request-log format and the ambiguous-shutdown re-claim recovery are in
  [engdocs/SERVE_RUNBOOK.md](engdocs/SERVE_RUNBOOK.md).

- **Replica-aware leases: `bd reclaim` no longer reverts a lease another
  replica granted** (wy-jpd3.7). A lease is only meaningful on the replica that
  granted it — the other machine's liveness view is stale by up to one sync
  interval — so each lease now records its granting node (`leases.granted_node`,
  ignored migration 0016; `types.Issue.LeaseGrantedNode`, which rides the JSONL
  interchange so an imported lease keeps its provenance). `bd reclaim` skips a
  lease that positively names a different replica, naming each skip on stderr,
  and the new `bd reclaim --any-replica` (`types.ReclaimFilter.AnyReplica`) is
  the escape hatch for a replica that is permanently gone. `bd show` annotates a
  lease granted elsewhere.

  The guard is **opt-in and fail-open**. It arms only where you set the new
  `node_id` config key (`BEADS_NODE_ID` / `BD_NODE_ID`, or
  `bd config set node_id <name>`, which writes the per-machine
  `~/.config/bd/config.yaml` — a `node_id` in the git-tracked project
  `.beads/config.yaml` would give every clone the same identity and leave the
  guard armed but inert). There is deliberately no hostname fallback: the
  hostname names the client process's machine, not the store, so on a shared or
  remote dolt sql-server — where many hosts are clients of ONE store and there is
  no sync interval to defend against — it would stop a supervisor reaping any
  worker's lease at all. Unset (the default), and a lease of unknown provenance,
  behave exactly as before, so an upgrade can never strand a lease the reaper
  could previously recover. Also documents the invariant the guard *cannot*
  enforce: grace window > sync interval, and lease TTL > sync interval.

- **`bd sync` — one verb for the federation loop** (wy-jpd3.4). Every
  multi-machine beads deployment hand-rolls the same sequence in shell; this
  ships it. `bd sync [--remote <name>] [--attempts N]` pulls, checks for merge
  conflicts, runs the full `is_blocked` recompute, and pushes — retrying a
  bounded number of times (default 3) when another replica wins the push race.
  Two properties are the point of the verb. Conflicts are detected
  **positively**, from the merge's own captured conflict rows and from
  `dolt_conflicts`, never inferred from the pull's exit status: a pull fails for
  plenty of reasons that are not conflicts, and a settled merge that *is*
  conflicted aborts and leaves `dolt_conflicts` empty, so an exit-status guess
  invents phantom conflicts and misses real ones. And a conflict sync cannot
  settle is **never** resolved by picking a side: it halts before recomputing or
  pushing and exits 2, with no `--strategy`-style override to make it do
  otherwise. (The pull underneath still auto-settles the convergent classes it
  always has — machine-local metadata, audit-only dependency rows, LWW on issue
  cells; anything past those halts.) The halt message reports which state the
  halt left behind, read from which detection source fired rather than assumed:
  the SQL pull route aborts the merge and restores the working set, while the
  CLI/git-protocol route deliberately leaves the conflict rows live. The
  recompute between pull and push is not bookkeeping either: `is_blocked` is
  denormalized, so a merge that brings in a dependency edge from another replica
  leaves `bd ready` stale until it runs. It runs unconditionally, on every
  attempt — `RecomputeAllBlocked` is precisely the repair that does not depend
  on a merge advancing HEAD, so gating it on "did anything merge" would mean a
  column left stale by a hand-resolved conflict (a state `bd sync` creates by
  exiting 2) is never repaired again while every tick reports success.
  Exit codes are the machine contract, so a sync timer can branch without
  parsing output: `0` synced, `1` error, `2` merge conflict (halted, nothing
  pushed), `3` push-race retries exhausted (transient — retry next tick).
  `--json` reports the outcome as `{"status", "attempts", "conflicts",
  "conflicts_live", "rows_corrected", "pushed"}` on every non-error exit; exit 1
  emits bd's standard `{"error": ...}` envelope. A confirmed-no-remote rig exits
  0 with the same guidance `bd dolt push` prints; `dolt.local-only` and
  `no-push` are honored.

- **`bd reclaim` scope filters** (wy-jpd3.3). `bd reclaim` gained
  `--label` / `--label-any` / `--exclude-label` / `--assignee` / `--id`,
  mirroring the claim-side label surface (`bd ready --claim`), so a supervisor
  can scope its reaper to exactly the partition it claims from. Filters
  AND-combine and never widen the stale set. This matters on a federated
  deployment: each replica's view of another machine's liveness is stale by up
  to one sync interval, so an unscoped reaper on one machine can revert a unit
  that is alive on the machine that granted its lease — point each supervisor's
  reclaim at its own claim partition and that cannot happen. A scope flag
  supplied with no usable value (e.g. `--label "$LANE"` with `LANE` unset) is a
  hard error rather than a silent degrade into a global sweep, and the builder
  fails closed (`1=0`) for programmatic callers. `bd reclaim --json` now reports
  a `scoped` boolean so a reclaim log distinguishes a scoped sweep from a global
  one.

- **Every command that had a role now reaches it through the accessor, on both
  routes** (facade waves 1-4). This is the close of the programme: twenty-three
  capability accessors on the storage substrate, each answering a role interface
  in the leaf package `issueops`, each with a backend-independent conformance
  suite run against the Dolt server store, the embedded Dolt store and the
  unit-of-work provider, and each reached by the CLI's direct route, the CLI's
  `--proxied-server` route and — where the capability is published — one HTTP
  operation, all calling the same role. `bd serve` publishes sixteen
  capability-bearing operations, twelve of them added by the programme, every one written into
  `internal/httpapi/spec/openapi.v0.yaml` before its handler existed.

  **What did NOT get a role is as deliberate as what did.** `bd show` and
  `bd close` were already behind `Reader` and `BatchCloser`/`Lifecycle` on both
  routes, so there was nothing to converge. `bd version` prints build
  information and a role is an interface onto a substrate — the one part of it
  that touches state, the recorded-version marker, got `VersionReconciler`
  instead. Most of `bd init` CREATES the substrate a role would be reached
  through; only the workspace identity is reachable as a store capability, and
  that is what `Bootstrapper` and `InitVerifier` are. `bd create --graph` is
  deferred on the record: it is an UPSERT with caller-supplied symbolic names
  and `BatchCreator` is create-only with server-assigned ids, so folding them
  would have meant one role with two incompatible id policies.

- **A shared semantic contract for every issue role** (bd-yby99). Seven of the
  eight role interfaces in `issueops` now have a backend-independent
  conformance suite in `backend/conformance`, run against the Dolt server store,
  the embedded Dolt store, and the unit-of-work provider. One hundred and
  twenty-nine cases cover `Reader`, `Relations`, `Commenter`,
  `DependencyEditor`, `ReadyClaimer`, `BatchCloser` and `Lifecycle` — the last
  spanning Create, Update, Close and Reopen. The unit-of-work leg runs 127 of
  them: the two staging cases assert which concrete Dolt tables a commit
  staged, which that provider has no way to observe. Each case is written
  against the promise
  stated in the role's doc comment, cited by file and line, rather than against
  any implementation's current behaviour, so the doc comment is the artefact
  under test as much as the code is.

  Read the three legs for what they are. The Dolt and embedded-Dolt stores
  share one validate/execute body for six of the roles and the entire
  implementation for `Reader`, so a green run on all three is **two readings of
  the contract plus a wrapper-and-engine check**, not three independent votes.
  The unit-of-work provider is the only separately written body, and every
  divergence found so far where one backend disagreed with another rather than
  all three with the doc has surfaced there.

  Where an implementation contradicts its own doc comment the case still
  asserts what the doc says, and the disagreeing backend is parked at its
  wiring site with a greppable `KNOWN DIVERGENCE <bead>` skip, so the agreeing
  backends stay pinned. Thirty-one findings have come out of that, all
  children of `bd-yby99`: seven divergences, where the implementations
  disagreed, and twenty-four spec gaps, where the doc was silent rather than
  wrong and the case carried an in-file `SPEC-GAP <bead>` marker instead of an
  invented assertion.

  **Every parked case now runs.** The three findings that had a backend parked
  are adjudicated and fixed, so the five `KNOWN DIVERGENCE` skips are gone and
  all three legs run their full set with zero skips: an invalid `ClaimNext`
  sort policy now matches `ErrValidation` (`bd-yby99.18`), a reparent on the
  unit-of-work provider now replaces *every* parent edge (`bd-yby99.26`), and a
  dependency removal on that provider now reaches the ephemeral plane
  (`bd-yby99.17`). Each has its own entry below; none was resolved by weakening
  a case.

  Twenty-two of the spec gaps are adjudicated as well. In each the leaf doc
  gained the promise it had been missing and the contract case was tightened to
  assert it, in that order, because the doc is the spec and a case may never
  assert more than the doc promises. Ten of the adjudications moved production
  code rather than only the doc — `bd-ocrn7`, `.3`, `.4`, `.7`, `.8`, `.17`,
  `.18`, `.19`, `.20` and `.26` — and each is a user-visible change with its own
  entry below, not a quiet edit made to turn a case green.

  Three findings remain open, and none is parked — no case skips anywhere.
  `bd-yby99.9` is the doc's own admitted silence, named in the leaf text so a
  caller does not read the silence as a promise: the dependency-existence
  refusals still carry no error identity. `bd-yby99.1` is with `bd-soezd` for
  measurement rather than adjudication. `bd-yby99.30` records a CLI
  retry-safety property the `bd-yby99.19` decision traded away (see below).
  The empty-string label question (`bd-yby99.28`, `bd-yby99.29`) is closed, not
  open: the entry is now dropped on every backend and `LabelPatch` says so.
  Five duplicated per-backend test files
  were deleted outright and a sixth trimmed, their scenarios folded into the
  shared cases; the Dolt-only facts they also pinned — history-entry message
  spellings, working-set cleanliness, the GH#2455 no-op removal sweep — stay
  Dolt-only in persistence tests.

### Changed

- **`bd delete --force` on a TEAM SERVER now orphans dependents instead of
  deleting them** (bd-x82so). **Read this one before you upgrade a team-server
  script.**

  Against a proxied server, `bd delete X --force` used to delete X *and its
  whole dependent subtree*, and `--cascade` was refused outright with "not
  supported in proxied-server mode (delete always cascades)". There was no way
  to delete one bead on a team server without taking everything that depended on
  it, and asking for the safer behaviour was an error.

  Both routes now mean the same three things by the same two flags, which are
  the local database's meanings, unchanged:

  | you type | you get |
  |---|---|
  | `bd delete X` | REFUSED if anything outside the request depends on X |
  | `bd delete X --force` | X is deleted; its dependents survive, ORPHANED |
  | `bd delete X --cascade --force` | X and its whole dependent subtree are deleted |

  **A team-server script that relied on the implicit cascade will start leaving
  orphans.** Add `--cascade` to it. The proxied `--json` result gains
  `orphaned_issues` so a script can see them; a cascade leaves it null.

- **Proxied `bd init` ADOPTS an existing project identity instead of
  overwriting it** (bd-zl3u8). **This one silently corrupted shared databases.**
  Against a team server whose database another rig had already identified,
  `bd init --proxied-server` wrote its own locally-derived issue prefix and
  project id straight over the stored pair — renaming every id the co-tenant was
  about to mint. It now reads the identity first and prints "Adopted project
  identity from existing database", which is what the direct route has always
  done. There is no flag that restores the overwrite.

  Two consequences ride along. `bd init` **no longer fills in a missing project
  id** on a substrate that already carries an issue prefix: half-identified is
  now adopted as it stands rather than completed, because the guard that stops
  the overwrite above cannot tell a half-provisioned workspace from a shared one
  mid-write. Re-identifying is an explicit act with no command yet; if you have
  such a workspace, it keeps working and it stays half-identified. And `bd init`
  **with `BEADS_DIR` pointing outside the repository now records a project id**,
  taken from that workspace's own `metadata.json`, where it previously recorded
  none and left exactly the state the new refusal will not complete.

- **`bd list` and `bd children` print blocker ids in ASCENDING ORDER**
  (bd-5k21g). The `blocked by:` and `blocks:` ids in compact and agent output
  came out in query order, which is map-iteration and query-plan dependent, so
  an unchanged workspace could print different bytes on two consecutive runs.
  They are now sorted ascending with repeats collapsed, on both routes. Only
  rows with two or more live blockers change; a diff-based script that pinned
  the old order was pinning an order the database never promised.

- **`bd delete <one-id>` without `--force` now REFUSES when the bead has an
  outside dependent**, instead of printing a preview and exiting 0 (bd-x82so).
  This is the direct route converging with itself: the multi-id path and
  `--dry-run` have always refused here, and only the unconfirmed one-id preview
  did not. The refusal names the bead and says to use `--cascade` or `--force`,
  which is the same sentence the other two paths already printed.

- **`bd delete` rewrites text references INSIDE the delete transaction**
  (bd-x82so). The direct route deleted the rows in one transaction and then
  rewrote `[deleted:ID]` into the neighbouring beads' descriptions afterwards,
  discarding every update error it got; a failure in the window left a workspace
  whose beads were gone and whose descriptions still cited them by id. The whole
  operation — the existence probe, the dependents guard, the cascade expansion,
  the deletion and the rewrite — is now one transaction on the new
  `issueops.Deleter` role, shared by both routes and by `bd serve`.

  **The trade, said out loud:** the transaction is now as large as the deletion
  plus its graph neighbourhood, so a delete whose neighbourhood is big enough to
  exceed the backend's write timeout fails whole instead of deleting the beads
  and leaving the text stale. Split a very large `--from-file` batch.

- **`bd delete` refuses the whole request when any id names no bead**
  (bd-x82so), on both routes and under `--dry-run`. The multi-id path already
  did; the guarantee is now the role's, so nothing partial is deleted before the
  typo is reported.
- **`bd dep tree --direction=both` now sees ONE database state** (bd-lx5iu).
  Both routes made two independent reads — the dependency walk and the dependent
  walk — with no transaction spanning them, so an edge added between them could
  leave the two halves of the picture describing different graphs. The whole
  walk, both directions, now runs inside one transaction on the new
  `issueops.TreeWalker` role.

- **`bd dep tree` on a team server resolves partial ids and honors
  `--max-rows`** (bd-lx5iu). Two things the `--proxied-server` route did not do
  and the direct route did.

  `bd dep tree a1b2` used to fail there: the argument went to storage verbatim,
  so the abbreviation that worked on a direct workspace did not work on a shared
  one. It resolves now, and an id that resolves to nothing reports the same
  lookup failure the direct route reports.

  `bd dep tree X --max-rows N` used to be refused outright — "not supported in
  proxied-server mode" — because the unit-of-work path threaded no cap. The cap
  now rides the role's request, so all three backends enforce it and the flag
  means the same thing wherever the command runs. It is still POST-HOC: a tree
  walk has no query filter to thread a cap through, so the full tree is built
  and then counted.

- **`bd dep tree --show-all-paths` is documented as the no-op it always was**
  (bd-lx5iu). Nothing has ever read the flag. It was accepted and passed down to
  a walk that ignored it, so a node reachable by two paths has always been shown
  once, under the first path that reached it. The flag is still accepted so no
  script breaks, its help text now says it is ignored, and the one-visit rule is
  a stated promise of the role rather than an accident of the walk. `TreeNode`'s
  `truncated` member is dead in the same way: it is always `false`, because a
  node beyond `--max-depth` is absent from the answer rather than present and
  flagged.

- **`bd purge` and `bd prune` select and delete in ONE transaction** (bd-pn231).
  The direct route used to search in one transaction and delete in another, so a
  bead closed between the two could be counted and not deleted, or deleted and
  not counted. Both routes now run the whole sweep as one act on the new
  `issueops.Sweeper` role, and the count a sweep reports is the set it deleted.

  **The trade is worth knowing before you run a very large purge on a team
  server.** One transaction means the Dolt server backend can no longer batch
  wisp deletions 200 at a time, so a purge big enough to exceed Dolt's write
  timeout now fails whole instead of deleting part of the workspace and
  reporting nothing. Sweep in slices (`--pattern`, `--closed-before`) if you are
  clearing a very large backlog.

  The version-control entry for a sweep is now `bd: sweep <n> <tier> bead(s)` on
  both routes, where the proxied route wrote `bd: prune <n> bead(s)` and the
  direct route wrote `bd: delete <n> issue(s)`, naming neither the command nor
  the tier. Scripts that grep commit messages for the old spellings need
  updating.

- **`bd list --max-rows` reports its own malformed value first** (bd-b34o7).
  The row cap now rides the list request rather than being stamped onto the
  filter after it was built, which moves the flag's validation ahead of the
  filter's. `bd list --max-rows -1 --status bogus` now reports the `--max-rows`
  usage error where it used to report the invalid status; the exit code is
  unchanged and it takes two bad arguments to reach.

- **`bd create --file` is all or nothing, and its plan-wide flags now mean
  something** (bd-lu170). Both routes build one `issueops.CreateBatchRequest`
  and ask for the new `BatchCreator` role, so a markdown plan is created as one
  act with one history entry on either.

  **A file the workspace refuses now creates NOTHING.** Each template goes
  through the same validation a single `bd create` goes through — the
  workspace's configured types and statuses, the field lengths, the
  create-only guard — and a template that fails takes the whole file down with
  it. **Re-running a file that used to fail can therefore give a different
  outcome than it did before**, because the run before it may have landed rows
  this one refuses to duplicate; check `bd list` before re-running a plan that
  errored on an older version.

  **A declared dependency on an id that does not exist is now an error rather
  than a silently dropped edge.** The direct route used to create the issues
  and leave the edge out, with nothing on stdout or stderr saying so, so a plan
  file with a typo produced a graph that looked complete and was not. An
  `external:` reference and an id belonging to another repository are still
  written, exactly as `bd dep add` accepts them; only a target that shares the
  workspace's own prefix and names no row is refused.

  **`--ephemeral`, `--no-history`, `--mol-type` and `--validate` are honoured
  on the direct route.** All four were accepted and silently ignored there
  while the proxied route acted on them, so `bd create --file plan.md
  --ephemeral` created durable issues on a local workspace and wisps on a team
  server. `--validate` (and `validation.on-create`) now lints every template on
  both routes, which is the policy a single-issue create has always applied.
  Issues created from a file also carry `created_by` and `owner`, as a
  single-issue create does.

  **`bd create --file --json` emits the STORED rows on both routes.** Each
  entry is the post-create snapshot the role reads back — with `owner`,
  `created_by` and dependency records carrying their own `created_at`,
  `created_by` and `metadata` — where the direct route used to marshal the
  structs it had built in memory. Additive: no member was removed, and both
  routes now emit one shape.

  Proxied `--file` loses its `config.yaml` fallback for `types.custom`, the
  same convergence onto the direct route's database-only vocabulary that
  landed for single-issue create (bd-7oyh5).

  `bd create --graph` is untouched: an upsert over a declared shape is a
  different contract, and it gets its own role when it gets one.

- **Proxied `bd ready --json --limit N` publishes `pagination.total`**
  (bd-s10oa). Under `BD_JSON_ENVELOPE=1` the direct route has always carried
  the size of the ready front beside a truncated page; the proxied route
  emitted no `total` key at all, so the same script read a number against a
  local workspace and nothing against a team server. Both routes now answer
  from the new `issueops.ReadyCounter` role. The count is only asked for when
  the page came back full, so an untruncated listing costs nothing extra.

- **Proxied `bd dep list a b c` reports ids that name nothing** (bd-nhsno). The
  route handed its raw arguments to the batch read, whose answer simply has no
  entry for an id nothing matched, so a typo printed `<id> has no dependencies`
  — a clean graph — instead of a warning. It now prints
  `warning: no issue found: <id> (skipped)` on stderr, the line the direct
  route has always printed, and omits the anchor from stdout. Emitted in
  `--json` mode too, on stderr, so the document stays a flat array.

  Two smaller shape changes ride along, on both routes: a repeated anchor is
  answered ONCE, at its first mention, rather than twice; and
  `bd dep list --type X a b` prints `<id> has no dependencies` instead of a
  section header with no rows under it for an anchor whose every edge the
  filter rejected.

- **`bd status --assigned` reports why it failed** (bd-4ze25). The direct route
  swallowed every underlying error and printed the causeless
  `failed to get assigned statistics`. Both routes now report the same
  underlying error. Relatedly, the proxied `--no-blocked` warning is derived
  from the ANSWER rather than printed unconditionally before the query, so it
  is correct for any backend and disappears by itself when a backend gains a
  fast path.

- **`bd dep cycles` output is deterministic, and no longer shortens a cycle it
  cannot fully describe** (bd-wfkbv). Two changes, both visible in bytes.

  The report is now CANONICAL: each cycle's members are rotated so the lowest
  id comes first, and the cycles are sorted against each other. Two runs
  against an unchanged database previously disagreed — the walk iterated a Go
  map, so both the order of the cycles and each one's starting point moved
  between runs, `--json` included, and on a graph with overlapping cycles even
  the SET of cycles could differ. Diffing two snapshots is now meaningful.

  A cycle whose member has no row behind it is now reported HONESTLY. Members
  that could not be looked up were silently dropped from the path, so a
  three-node cycle rendered as a two-node one and looked complete, and a cycle
  none of whose members could be looked up vanished from the report and from
  the `Found N dependency cycles` count. Every member is now carried with its
  id, its description is omitted rather than the member, and the cycle is
  marked partial. The count no longer shrinks because rows went missing.

  `--json` therefore emits a NEW SHAPE: an array of
  `{"members": [{"id", "issue"?}], "partial": bool}` where it used to emit an
  array of arrays of issues. The `issue` member is the same issue object as
  before and is absent — never null — for a member this workspace holds no
  record of. Scripts that read `bd dep cycles --json` need updating.

  The post-add cycle warning that `bd dep add`, `bd dep --blocks` and
  `bd link` print is the same sweep and gains the same two properties, on both
  the direct and the proxied route.

- **The v0 HTTP surface reaches twelve operations** (facade wave 2). This
  release adds six: `GET /v0/beads/stats` (bd-4ze25),
  `GET /v0/beads/dependencies/cycles` (bd-wfkbv), `GET /v0/beads/dependencies`
  (bd-nhsno), `GET /v0/beads/ready:count` (bd-s10oa) and the two config reads
  (bd-kzepq). Every one answers from the SAME `issueops` role its `bd` command
  reaches, so the two surfaces cannot drift: the operation is written into
  `internal/httpapi/spec/openapi.v0.yaml` first and the wire types are
  generated from it. `capabilities` now carries `ready.list`, `ready.count`,
  `issues.list`, `issues.get`, `issues.claim`, `stats.get`, `config.list`,
  `config.get`, `dependencies.cycles`, `dependencies.list`.

- **`GET /v0/beads/stats` — the workspace summary over HTTP** (bd-4ze25). The
  same envelope `bd status --json` prints, minus `recent_activity`, which no
  shipped code path populates. `skip_blocked` is a HINT: a backend with no
  cheaper path answers with the full numbers and says so through
  `blocked_count_skipped`, which is derived from the answer rather than echoed
  from the request. `assignee` scopes the summary to one actor and is refused
  when empty rather than answering with the unassigned rows.

- **`GET /v0/beads/dependencies` — stored edge rows for several issues at
  once** (bd-nhsno). The shape `bd dep list a b c --json` emits. An id that
  names nothing is reported in the response's `missing` member rather than
  turning the whole call into a 404, so a batch keeps the answers for the ids
  that WERE found; an edge whose target this database holds no row for is still
  an edge and is returned as stored.

- **`GET /v0/beads/ready:count` — the size of the ready front** (bd-s10oa).
  Takes the same filter vocabulary as `GET /v0/beads/ready`, decoded once by
  the same code so the two cannot admit different parameters, and takes no
  `limit` and no `sort`: a cardinality has no page and no order.

- **`bd serve` publishes `GET /v0/beads/dependencies/cycles`** (bd-wfkbv). The
  cycle sweep is now an HTTP operation as well as a command, answering from the
  same role, and `capabilities` gains `dependencies.cycles`. It takes no
  parameters and is not paginated: truncating would shrink the count, which is
  the number an operator acts on. `bd link` on a team server also runs its
  cycle sweep and its title lookups AFTER the write commits rather than inside
  it, so the warning describes a graph other clients can see.

- **`bd count` is one implementation again** (bd-m8hn7). The command had two:
  the direct route opened its own store and the proxied route re-parsed the
  same flags against a unit of work, and each independently loaded the
  workspace list configuration before applying `--include-infra`. The
  cardinality contract `bd count` keeps with `bd list` was therefore asserted
  once and implemented twice. Both routes now go through the new
  `issueops.Counter` role, which builds its filter with the same shared
  builder `bd list` uses. Text and `--json` output are unchanged on both
  routes — the committed protocol corpus fixture for `bd count` is
  byte-identical — so this is a change you should only notice if the two
  routes previously disagreed. Two behaviours are now written down as
  promises rather than left as accidents: an unrecognised `--status` or
  `--type` still matches nothing and answers `0` rather than refusing (where
  `bd list` validates both), and a grouped count's `Total` is computed
  independently of its buckets, so overlapping dimensions such as labels do
  not sum to it.

- **Proxied `bd create`, `bd reopen` and `bd update` emit the same `--json`
  shape as their direct counterparts** (bd-7oyh5, bd-1f5a2, bd-xt6de). Each of
  the three now answers from the hydrated post-state snapshot the
  `issueops.Lifecycle` role returns instead of a bare row read, so all three
  gained the `labels` field they were silently dropping, and — as on the
  direct route — dependency records are not included. Scripts that read
  proxied output should expect the direct route's documented shape; nothing
  was removed. `bd update --claim --json` is the one exception and still
  strips labels.

- **Proxied multi-id `bd reopen` records one history entry per id**
  (bd-1f5a2). It wrote a single combined entry covering every id in the
  batch; the direct route has always written one per id, and both routes now
  do. On a team server a `bd reopen a b c` is consequently three round trips
  and three Dolt commits rather than one, which a script reopening a large
  batch will feel.

- **Proxied `bd create` validates the way the direct route validates**
  (bd-7oyh5). Its custom issue-type check no longer falls back to
  `config.yaml` when the database has no custom types, its explicit-id prefix
  guard no longer consults the YAML prefix overlay, and its `--type`,
  `--status` and prefix refusals are worded as the role words them. The
  direct route has never had either fallback, so this is convergence onto the
  stricter of the two. The version-control entry a proxied create records
  also changes text, from `bd: create <id>` to `create issue`: the role
  publishes no field for a caller-supplied label, and no test asserted either
  string.

- **Proxied `bd update` error messages no longer name the stage that failed**
  (bd-xt6de). "opening unit of work" and "committing" collapse into the
  generic per-id update failure, and an update whose commit fails with a
  context cancellation now aborts the batch instead of recording a per-id
  verdict for it. This matches the choice `bd update --claim` already made
  when it moved to the role. Exit codes are unchanged.

- **An empty-string label is dropped rather than stored** (bd-yby99.29). Adding
  or replacing labels with an empty value wrote a `labels` row keyed on `''` on
  the Dolt-backed stores and dropped it on the unit-of-work one, so the same
  edit left different rows depending on which route served it. Dropping wins on
  both routes now: an edit carrying only an empty value changes nothing and
  reports no change, and an empty value alongside real ones drops only itself
  rather than failing the edit. A row that renders as nothing and matches
  nothing was never worth storing, and refusing the whole update would have let
  one stray value sink an otherwise-good multi-label edit. An empty row already
  stored is left alone by add and remove, and cleared by a replace that omits
  it — ordinary set behaviour, not a migration, so nothing sweeps existing data.

- **An invalid sort policy is now a typed validation refusal** (bd-yby99.18).
  Asking for a sort the ready query does not have — `--sort newest`, or a
  `ClaimNext` sort on a batch close — was refused with a bare error carrying no
  sentinel, on all three backends. A caller doing `errors.Is(err,
  issueops.ErrValidation)` to separate "you asked for something impossible"
  from "the database is unwell" classified it as a backend failure, which is
  the wrong half of the role's own error trichotomy. It now wraps
  `ErrValidation`. **The message text is byte-identical** — the wrap uses
  `%.0w` deliberately — so the HTTP surface that prefix-matches it to render a
  400 rather than a 500, and every script reading the printed line, are
  unaffected.

- **A batch close that changed nothing no longer claims work or records
  history** (bd-yby99.19). `BatchCloser` couples `ClaimNext` to "at least one
  item closed", and "closed" was being read as "the item returned no error" —
  which an already-closed item does. A batch whose items were *all* already
  closed therefore mutated nothing and still handed out a claim and wrote a
  history entry. Landing now means **changed**: an all-idempotent batch earns
  no claim and records no entry. A batch that closed even one item is
  unaffected.

  This has a cost worth stating plainly, tracked as `bd-yby99.30`. An agent
  that closed an issue, crashed before receiving its next work item, and
  retried `bd close <id> --claim-next` used to get work from the retry; it now
  gets nothing, because the retry closes nothing. That retry-safety property
  was deliberate, and the way to get it back without re-coupling a claim to a
  no-op is for `cmd/bd` to call `ReadyClaimer.ClaimNext` itself when the batch
  reports no change. That is a design call above the role seam and has not been
  made.

- **A batch's Dolt commit message no longer names wisp ids** (bd-yby99.20). A
  batch may close ephemeral and durable items together, and the durable commit
  message named every item that landed. Wisp tables are dolt-ignored, so that
  message shipped a reference to work no other machine can resolve — the same
  federation leak `Commenter` already guards against. The message is now
  composed from durable landings alone and reports ephemeral ones as a count.
  Closing a wisp still counts as work landing: a wisp-only batch still succeeds
  and still earns its claim.

- **Breaking: `bd list --ready` now refuses a filter it cannot honor instead
  of silently ignoring it** (bd-yby99.8). The `--ready` arm reaches the
  blocker-aware query through a narrower filter than the rest of `bd list`
  uses, and everything that filter could not carry was dropped without a word:
  `bd list --ready --id tst-d68` answered with every ready issue in the
  workspace, not the one named. It now fails with a validation error naming the
  fields it could not honor.

  The refused combinations are `--ready` together with any of `--id`,
  `--title`, `--spec`, `--title-contains`, `--desc-contains`,
  `--notes-contains`, `--external-contains`, `--external-ref`, any
  `--created/--updated/--closed/--defer/--due` bound, `--deferred`,
  `--overdue`, `--empty-description`, `--no-labels`, `--no-parent`,
  `--pinned`, `--priority-min`, `--priority-max`, or an HTTP list cursor. A
  script that passed one of these was already getting the wrong answer; it now
  gets an error instead of silently wrong rows. Everything `--ready` does carry
  is unchanged — `--type`, all the label flags, `--assignee`, `--no-assignee`,
  `--priority`, `--parent`, `--mol-type`, `--wisp-type`, `--metadata-field`,
  `--has-metadata-key`, `--exclude-type`, `--include-gates`, `--include-infra`,
  `--limit`, `--offset`, `--max-rows`, `--sort` and `--reverse` — as is
  `--no-pinned`, which the ready query already satisfies by construction.

  What `--ready` carries, what it refuses, and the two things the ready set
  decides for itself (it never returns pinned issues, and it applies no template
  predicate of its own) are now stated on `issueops.ListRequest.ReadyFlag` and
  pinned by the Reader conformance contract at all three backends.

- **`issueops.Reader` now refuses an `Offset` it cannot honor instead of
  silently ignoring it** (bd-yby99.7). The store-backed reader — one body,
  handed back by both the Dolt server store and the embedded store — renders
  `LIMIT` without `OFFSET`, so a caller that paged with `Offset` got the
  unpaged first page again and again with no error to notice: three matching
  rows came back 3/3/3 at `Offset` 0/1/2. `Ready` and `List` now return a typed
  `*issueops.ErrUnsupported` naming the operation and the backend. The
  unit-of-work reader, which does render `OFFSET`, keeps honoring it, and both
  behaviours are now pinned by one conformance case: an `Offset` is honored or
  refused, never ignored.

  **No CLI behaviour changes.** `bd ready --offset`, `bd list --offset` and
  `bd query --offset` already rejected a non-zero offset on the direct route
  and already reached the unit-of-work path under `--proxied-server`, where
  offset paging works and is unaffected. This closes the same hole one layer
  down, for callers built on the library rather than on the CLI.

  `ErrUnsupported` moved into the public `issueops` package and is aliased back
  from `internal/storage`, so a consumer holding only a role interface can
  classify the refusal with `errors.As` without importing an internal package.
  It is the same type throughout: `beads.ErrUnsupported` and
  `storage.ErrUnsupported` are unchanged aliases and every existing `errors.As`
  site keeps matching. The portable way to page a listing across backends is
  the keyset position (`ListRequest.AfterCreatedAt`/`AfterID`), which every
  implementation honors and which is stable under concurrent writes; ready work
  carries no keyset and therefore has no portable paging.

- **Breaking: a dependency type is now bounded at 32 characters, not 50**
  (bd-yby99.3). The `type` column on both `dependencies` and
  `wisp_dependencies` is `VARCHAR(32)` and no migration widens either, so the
  validator's old 50-character bound accepted a range of types — 33 to 50
  characters — that no row could ever carry. Depending on the caller, such a
  type either failed at the database with a raw truncation error or was
  silently cut short. `types.DependencyType.IsValid` now bounds at the new
  exported `types.MaxDependencyTypeLen`, so the refusal arrives from one place,
  ahead of every backend, with a typed validation error.

  This narrows what bd accepts on twelve paths, among them `bd create --deps`,
  `bd dep add --type`, `bd link --type`, `bd batch`, `bd graph apply` and
  markdown import. In practice nothing in the wild should notice: every
  well-known type is far shorter (the longest, `conditional-blocks`, is 18
  characters), and `bd create --deps` accepts only well-known types anyway. The
  user-visible error text changed too — messages that read `max 50 chars` or
  `at most 50 characters` now name 32 — so a script matching on that literal
  string needs updating. One rough edge is unchanged and worth knowing about:
  `bd create`'s interactive form warns and drops an invalid dependency rather
  than failing, so a 33-to-50-character type there now silently loses the edge
  instead of creating an unstorable one.

- **Breaking: `bd update --status <done-status>` now enforces close policy.**
  Moving an issue into `closed` (or any configured done-category status) via
  `bd update`, `bd batch update`, or the issueops facade now refuses when the
  issue has open children or a live direct blocker — matching `bd close`.
  Override with `bd update --force`, which now means: allow `-a/--assignee` to
  overwrite another actor's live in_progress claim, AND allow a status change
  into done despite open children or a live blocker (same as
  `bd close --force`). `bd update --force` without `-a` was previously a
  validation error on the direct path; it is now valid. In `bd batch`, spell the
  override `update <id> status=closed force=true`; an unforced refusal rolls
  back the entire batch. Facade consumers: set `UpdateRequest.ForceClosePolicy`.
  Tracker sync-pull always forces (remote state is authoritative).
  `bd batch close` remains unchecked, as before.

- **Public `beads.Storage` interface gained a required `IssueClaimer()` method**
  (bd-serve claim role). `IssueClaimer() (issueops.Claimer, error)` returns the
  guarded atomic claim: `Claim(ctx, issueops.ClaimRequest) (issueops.ClaimResult,
  error)`, one compare-and-set that sets assignee and status only while the
  issue is claimable, reports the idempotent re-claim as `Changed: false` with
  nothing written, and refuses a lost CAS with a wrapped `ErrAlreadyClaimed` or
  `ErrNotClaimable`. When the refusing state was readable in the same
  transaction the refusal is an `*issueops.ClaimConflictError` carrying the
  assignee and status — so a client classifies a conflict from typed fields
  instead of matching substrings in the message, while `errors.Is` and the
  refusal's exact prose are unchanged. `POST /v0/beads/issues/{id}:claim` now
  runs on the role; the CLI's `bd update --claim` is not on it yet. It is a role
  of its own rather than a fifth verb on `issueops.Lifecycle` — a capability
  that role does not cover gets its own accessor. Consumers that only *call* the
  interface are unaffected; any external type that *implements* it (a custom
  store, mock, or proxy) must add the method to compile. A decorator must
  implement it by recursing into the store it wraps, the way `IssueLifecycle()`
  does. `ClaimResult.Issue` is the post-state issue ROW — deliberately without
  labels, dependency records or comments, unlike `UpdateResult` — because that
  is what the already-published v0 claim response and `bd update --claim --json`
  both report today.

- **`issueops.ReopenRequest` gained a `Provenance` field, and `bd reopen` now
  names the issue in its Dolt commit message on the direct path.** `Provenance`
  is the same optional label `UpdateRequest` already carries: it names the
  surface a mutation came from, it never changes *whether* a history entry is
  recorded, and empty still selects the implementation's default. It exists on
  this request because the defaults do not agree — the store-backed backends
  write `bd: reopen issue` and the unit-of-work backend writes `reopen issue`,
  neither naming the issue. A caller that spells a label now gets one entry
  whichever backend served it. The visible change is in `bd dolt log` only: a
  direct `bd reopen bd-1` reads `bd: reopen bd-1` where it read
  `bd: reopen issue`, or `reopen issue` on the unit-of-work backend.

  `UpdateRequest` and `ReopenRequest` are the only two requests that carry the
  label, and deliberately so. `CreateRequest` gains nothing — no surface has
  ever named the created issue, so the field would have no caller. Neither do
  `CloseRequest`, `CloseBatchRequest`, `ClaimNextRequest`, `AddCommentRequest`,
  `AddDependenciesRequest` or `RemoveDependencyRequest`: each of those roles
  composes its entry from what the call actually did, so a caller-supplied
  label could only restate it or contradict it. For the batch close and the
  claim one is not even expressible — those messages name what LANDED and which
  id the claim WON, neither of which is knowable before the call. For the
  dependency verbs the entry follows the edges: one edge names both endpoints,
  several name only their count, and — see the source-routing note below — a
  request made entirely of wisp-sourced edges records no entry at all, leaving
  a label nothing to name.

- **Public `beads.Storage` interface gained required `DependencyEditor()`,
  `Commenter()` and `IssueRelations()` methods.** Three more roles alongside
  `IssueLifecycle()`, `IssueReader()`, `ReadyClaimer()` and `BatchCloser()`,
  reached the same way and layered by each decorator in its own accessor.
  Consumers that only *call* the interface are unaffected; any external type
  that *implements* it (a custom store, mock, or proxy) must add all three to
  compile, and a decorator must implement the two write roles by recursing into
  the store it wraps rather than delegating blindly.

  `DependencyEditor() (issueops.DependencyEditor, error)` edits the dependency
  graph. `AddDependencies` asserts a set of edges as ONE durable act — one
  transaction, at most one history entry — and is ALL-OR-NOTHING: any refused
  edge (self-dependency, cycle, type conflict, hierarchy conflict, unknown
  endpoint) refuses the whole request and writes nothing. Edges asserted
  together describe a graph, and half a graph is a graph nobody asked for.
  `RemoveDependency` is idempotent: a missing edge is `Removed: false` with a
  nil error and no history entry, not `ErrNotFound`.

  The dependency-type constants `issueops.DepBlocks`, `DepParentChild`,
  `DepRelated`, `DepDiscoveredFrom`, `DepRepliesTo` and `DepWaitsFor` ship with
  it. They are **documented common values of an OPEN, workspace-configurable
  set** — not a closed enum, not exhaustive, and not validated against: nothing
  refuses a type merely because it is absent from the list, and a workspace's
  own type is a first-class value everywhere a `DependencyType` is accepted.
  They exist because `internal/types` is unimportable outside the module, so
  without them an external embedder can hold a `DependencyType` but has no
  spell-checked way to name one — the same reason the error sentinels are
  re-exported.

  `Commenter() (issueops.Commenter, error)` appends one comment as one atomic
  mutation. A blank `Text` is `ErrValidation`, as is an empty `IssueID`; the
  issue-then-wisp fallback happens inside, as it does for `Reader.Get`; a
  non-empty id naming neither plane is `ErrNotFound` — an unaddressed request
  is invalid, not a thread that went missing, so the two are worth telling
  apart. `issueops.Comment` (an alias for the canonical comment model)
  is the result type. A comment on an ephemeral row records **no** durable
  history entry — the wisp tables are dolt-ignored so ephemeral work never
  ships, and an entry naming a wisp thread is exactly the artifact that
  ignoring them prevents; the comment still lands on the ephemeral thread and
  reads back from it.

  `IssueRelations() (issueops.Relations, error)` reads an issue's neighbors.
  `Related` requires an explicit `RelationOut` or `RelationIn` — **the zero
  direction is `ErrValidation`, with no implicit "both"**, because a silent
  default hands a caller the inverse graph with nothing to notice. An unusable
  `Types` entry is `ErrValidation` rather than a filter that matches nothing,
  and an anchor missing from both planes is `ErrNotFound` rather than an empty
  list. The result order is pinned: ascending by the neighbor's id, with the
  edge type breaking a tie.

  `bd dep add`, `bd dep remove`, `bd dep list`, `bd comment` and
  `bd comments add` are on these roles on BOTH routes — the direct one reaches
  them through the store's own accessor exactly as the proxied one reaches them
  through the provider's. Four behaviors changed on the
  proxied route: `bd dep list` now answers in the pinned order rather than in
  the order the two dependency tables happened to be read; a `bd dep list`
  naming an issue that does not exist now reports that instead of printing an
  empty neighbor list, matching what the direct route has always done; a
  `bd dep remove` that finds no edge now records no Dolt commit at all, where
  it used to name one; and a single-line `bd dep add --file` writes
  `bd: dep add <a> <b>` rather than `dependency: add 1 edges`.

  Four more changed on the direct route as it joined them. `bd dep list` there
  now answers in the same pinned order. Dolt commit messages for the explicit
  dep verbs are now the role's — `bd: dep add <a> <b>` and
  `bd: dep remove <a> <b>` — where the direct path used to write
  `dependency: add <type> <a> -> <b>` / `dependency: remove <a> -> <b>` in
  server mode and a generic `bd: dep add (auto-commit) by <actor> [...]` in
  embedded mode. A `bd dep add --file` edge refused by storage no longer
  carries a `line N:` prefix, because the role's request is all-or-nothing and
  names no edge index — parse and id-resolution errors still name their line.
  And a `bd dep add --file` in embedded `batch`/`off` auto-commit mode now
  defers its version commit to the next explicit commit point instead of always
  writing one, which is what every other write verb already did.

  `bd comment`'s direct route changed one thing with it, on the embedded
  backend only: a comment's Dolt commit is now the role's `bd: comment <id>`
  authored by `beads`, where the CLI auto-commit wrote
  `bd: comment (auto-commit) by <actor> [<id>]` authored by `root`. That is
  the spelling the Dolt server backend already wrote, and it is one commit per
  comment either way. Comment LISTING (`bd comments <id>`) is on neither role
  and unchanged on both routes: reading a thread is a paging question with a
  cursor of its own, which `issueops.Commenter` deliberately excludes.

  `DependencyEditor.AddDependencies` routes each edge by its SOURCE, the rule
  the stores' own dependency verb has always applied and the one `bd dep
  remove` and `bd dep list` already read: a wisp-sourced edge goes to
  `wisp_dependencies` with its event in `wisp_events`, an issue-sourced one to
  `dependencies` and `events`. Routing by source rather than pinning it to the
  issues plane is what keeps `bd dep add <wisp-id> <target>` working: a wisp
  has no row in the issues plane, so a pinned source has nothing to hang an
  edge off, and the two routes would not even have failed alike — the direct
  one on its source-existence check, the proxied one on an `fk_dep_issue`
  foreign key violation. ONE REQUEST MAY MIX THE TWO: it is still one
  transaction, the parent-child-first ordering and the whole-graph cycle gate
  both span the two tables (a cross-plane cycle is still refused), and a
  refusal rolls back both planes. Only the durable half is versioned, so a
  request made entirely of wisp-sourced edges records no history entry and
  leaves the working set clean — asserted end to end against a real embedded
  store by `TestEmbeddedDepAddWispSource`, which runs `bd dep add <wisp-id>
  <target>` at the front door. `bd link` on the proxied server routes the same
  way, matching its direct twin. Behind the role there is no plane-pinned bulk
  variant left to reach for by mistake: the internal domain layer's three bulk
  add verbs are now the one source-routed `AddDependencies`, so every bulk
  caller — role, `bd link`, unit of work — gets this rule and cannot opt out
  of it.

- **Public `beads.Storage` interface gained required `ReadyClaimer()` and
  `BatchCloser()` methods.** Two more roles alongside `IssueLifecycle()` and
  `IssueReader()`, reached the same way and layered by each decorator in its
  own accessor. Consumers that only *call* the interface are unaffected; any
  external type that *implements* it (a custom store, mock, or proxy) must add
  both methods to compile, and a decorator must implement them by recursing
  into the store it wraps rather than delegating blindly.

  `ReadyClaimer() (issueops.ReadyClaimer, error)` takes ready work atomically:
  `ClaimNext` selects, wins the compare-and-set and hydrates the row it won,
  all in one transaction. Its filter is `issueops.ReadyRequest` — the same type
  `Reader.Ready` takes — so the claim's predicate cannot drift from the set
  `bd ready` shows; `Limit` and `Offset` on it are `ErrValidation`, because the
  claim scan clears them and accepting a field only to drop it is worse than
  refusing it. An empty ready front is a nil result with a nil error, not an
  error class to classify.

  `BatchCloser() (issueops.BatchCloser, error)` closes many issues as one
  durable act: `CloseBatch` commits every item that lands in ONE transaction
  with at most one history entry. The request is the transaction boundary —
  nothing on the contract exposes begin or commit. It is deliberately not
  all-or-nothing: a refused id is skipped and the survivors commit together, so
  `CloseBatchResult.Outcomes` carries one entry per requested item in request
  order, each with a typed `errors.Is`-able refusal that never aborts the rest.
  An optional `ClaimNext` runs after the closes, in the same transaction, only
  when at least one item closed.

  `bd ready --claim` and `bd close` are on these roles on BOTH routes. Three
  behaviors changed on the proxied route: the proxied claim's Dolt commit
  message is now `bd: claim ready <id>`, the
  spelling the other two backends already wrote (it was
  `bd: ready --claim <id>`); molecule auto-close, `--suggest-next` and
  `--continue` now run after the close transaction rather than inside it, which
  is where the direct route has always run them — a plain `bd close a b c` is
  still exactly one Dolt commit, while a close that actually auto-closes a
  molecule or advances a step now produces two; and `bd close --claim-next`
  walks the ready order and takes the first row it can win, where it used to
  read the single top ready row and warn if that row had already been taken —
  a race with another agent now claims the next candidate instead of claiming
  nothing.

  `bd close`'s direct route closed N ids as N transactions with N Dolt commits
  until it joined them, so five things changed as it did. `bd close a b c` now
  writes ONE commit named `bd: close a, b, c` instead of three named
  `bd: close issue` — one request per STORE, since a transaction cannot span
  the second repo a prefix-routed id reaches. `--claim-next` runs inside the
  closes' transaction and only when something landed, walks the ready order the
  way the proxied route already did, and its `--json` `claimed` value is now the
  post-claim row (in_progress, assigned) rather than the pre-claim row the old
  two-step read. Molecule auto-close runs after ALL the closes land rather than
  between them — the root still closes exactly once; only which id's slot prints
  the `Auto-closed completed molecule` line can move. The open-children refusal
  moved from a cmd/bd pre-flight read into the close's own transaction, closing
  a read-then-write window that made `bd close <child> <parent>` depend on
  argument order; its message is unchanged. And an unsatisfied gate on an issue
  that ALSO has open children now reports the gate rather than the children,
  because the gate check stayed in the pre-flight — both were already refusals.

- **Public `beads.Storage` interface gained a required `IssueReader()` method**
  (bd-serve reader role). `IssueReader() (issueops.Reader, error)` is the read
  counterpart of `IssueLifecycle()`: one accessor returning a role that answers
  `Ready`, `List` and `Get` with filter construction, workspace-config loading,
  default limits and the wisp fallback all performed *inside* it, so a caller
  cannot half-perform that construction and answer the same question a different
  way. All three of `bd serve`'s issue reads and `bd show --json`'s detail view
  are on the role; `bd list` and `bd ready` are not yet, and share the
  `internal/workapi` builders instead — see `issueops.Reader`'s doc comment for
  exactly what that does and does not cover. It is a role of
  its own rather than three more methods on `issueops.Lifecycle` — a capability
  that role does not cover gets its own accessor. Consumers that only *call* the
  interface are unaffected; any external type that *implements* it (a custom
  store, mock, or proxy) must add the method to compile. A decorator must
  implement it by recursing into the store it wraps and layering its own
  behavior onto the result, the way `IssueLifecycle()` does, rather than
  delegating blindly — a blind delegation compiles and still satisfies the
  interface while silently dropping that decorator's layer for every read.

- **`beads.BulkIssueStore.ReclaimExpiredLeases` gained a `types.ReclaimFilter`
  parameter** (wy-jpd3.3), threaded through the domain use-case/repository
  interfaces and every backend. Callers that reclaim globally pass the zero
  `types.ReclaimFilter{}` (unchanged behavior); any external type that
  *implements* the interface must update its signature to compile.

- **Public `beads.Storage` interface gained two required methods**
  ([#4911](https://github.com/gastownhall/beads/pull/4911)). `UpdateIssueChecked`
  (an optional `ExpectedVersion` compare-and-swap on updates) and `MergeMetadata`
  (atomic single-key metadata merge) are now part of the `beads.Storage` /
  `storage.Storage` contract. Consumers that only *call* the interface are
  unaffected; any external type that *implements* it (a custom store, mock, or
  proxy) must add both methods to compile. `UpdateIssueChecked` with a nil
  `ExpectedVersion` behaves exactly like `UpdateIssue`, and `MergeMetadata` can
  be layered over a read-modify-write of issue metadata. See
  [examples/library-usage](examples/library-usage/README.md#concurrency--metadata).

- **Storage backend scope simplified** (bd-sadcd). The recently merged direct
  PostgreSQL and MySQL adapters have been rolled back before entering a tagged
  release. Supporting additional general-purpose server databases introduced
  dialect, credential, schema-lifecycle, migration, CI, and operational
  complexity at odds with our goal of keeping Beads as simple as possible and
  consuming as few resources as possible. The storage interface, shared issue
  core, SQLite implementation, and conformance harness remain; embedded Dolt,
  Dolt server, and SQLite are the supported storage paths. Dolt server mode and
  its use of the MySQL wire protocol are unchanged. Existing PostgreSQL or
  MySQL workspaces stop before their configured databases are opened or
  modified; see the [migration
  guide](docs/architecture/storage-backends.md#existing-postgresql-or-mysql-workspaces).
- **Cross-type blocking dependencies are now allowed** (bd-wg7ve,
  [#4034](https://github.com/gastownhall/beads/pull/4034)).
  `bd dep add <task> <epic>` — gating a work item on an epic (program)
  completing — previously failed with a backwards-reading error ("tasks can
  only block other tasks, not epics"). The blanket same-type rule (GH#1495)
  is replaced by a hierarchy deadlock guard that rejects only the cases that
  actually wedge the graph: gating an issue on its own ancestor (the ancestor
  can't close until its descendants finish) or on its own descendant (blocked
  status cascades down to the very issue that must close to clear the gate).
  Sibling ordering edges stay allowed. The guard now also covers
  `conditional-blocks`, which previously skipped cross-type validation
  entirely. A task gated on an epic becomes ready when the epic itself is
  closed, consistent with `bd ready`/`bd blocked`.

- **Claim/unclaim refusals now steer toward the holder instead of teaching
  eviction** (bd-at6rc, wyvern wy-zs5s2). The `--claim` refusal for an issue
  assigned to someone else suggested `bd unclaim <id>` — in a multi-agent
  fleet that copy taught an unclaim-then-claim steamroller that evicted a
  live, heartbeated claim mid-review. The refusal now says to coordinate
  with the holder, and unclaim's ownership rejection frames `--force` as an
  abandoned-claim escape hatch rather than a routine override. (The
  ownership check itself — foreign unclaim requires `--force` — landed in
  [#4675](https://github.com/gastownhall/beads/pull/4675).)

### Fixed

- **Multi-id `bd dep list` no longer changes its JSON shape on failure**
  (bd-nhsno). The direct route used the batched edge read only `if err == nil`
  and otherwise fell through to the neighbour path, which emits a different
  document — issues, not edge rows — with no error and no warning. Worse, the
  batched read was only ATTEMPTED when every resolved anchor shared one store,
  so a cross-workspace batch emitted the wrong document every time rather than
  only on failure. The read now goes through the new `issueops.EdgeReader`
  role, taken per store, with the answers merged back into argument order, and
  a failure is reported as a failure.

- **`bd ready --claim` honours the directory-label scope** (bd-s10oa). The
  branch that applies a directory's configured `directory.labels` default
  tested the filter it had already written that default into, so it could never
  fire. In a directory with `directory.labels` configured, `bd ready --claim`
  therefore claimed from the WHOLE ready front while `bd ready` beside it
  listed only the configured scope — handing an agent work the listing never
  offered it. Both routes affected.

- **Proxied `bd config set status.custom` and `types.custom` take effect
  immediately** (bd-kzepq). Reads of the custom status and type vocabularies
  are TABLE-first: they consult the normalized `custom_statuses` and
  `custom_types` tables and fall back to the config string only when the table
  is empty. The direct route has always rewritten the table in the same
  transaction as the row; the proxied route wrote only the string. So on a team
  server `bd config set types.custom "session"` reported success, `bd config
  get types.custom` read it back, `bd doctor` re-verified against the string
  and reported all-OK — and `bd create -t session` answered `invalid issue
  type: session` for as long as the workspace lived. Both routes now project
  the value into its table inside the same unit of work as the row, so the
  setting and its effect land together or neither does.

- **`bd config set-many issue_prefix=<x>` is refused, as `bd config set` has
  always been** (bd-kzepq). `bd config set` rejects the workspace id prefix in
  both spellings and points at `bd init --prefix`, `bd bootstrap` and
  `bd rename-prefix`, each of which does work a config write cannot — seeding a
  workspace that has none, or rewriting the ids that already exist. `set-many`
  partitioned its pairs by source and wrote the database ones straight through,
  so it walked past that guard and re-prefixed the workspace: beads created
  before the write and beads created after it disagreed about their own
  namespace, with nothing to reconcile them. Both verbs now refuse it, and so
  does the substrate, so a future front door cannot miss it either.


- **`bd create --id <id>` on a proxied server refuses an occupied id instead
  of overwriting it** (bd-7oyh5). The direct route has always been create-only.
  The proxied route passed no such flag, so `bd create --id bd-abc123 --title
  ...` against an id that already existed silently upserted every column of
  the resident issue — title, type, description, labels — and exited zero. The
  original was gone, with no history entry naming a create and nothing in the
  output to suggest anything had been replaced. Both routes now return
  `ErrAlreadyExists` naming the id and leave the stored row untouched, across
  the shared id space: an occupied durable id refuses a wisp create and an
  occupied wisp id refuses a durable one. If you have automation that relied
  on `--id` as an upsert, it should call `bd update`.

- **Proxied `bd update --ephemeral` and `--no-history` move the issue instead
  of flagging it in place** (bd-xt6de). The direct route sends these through an
  atomic aggregate move: insert into the target plane, copy the auxiliary rows,
  retarget inbound dependency edges, drop the lease, delete from the source.
  The proxied route instead ran a plain column update on whichever table the
  row already sat in, so a proxied `bd update --ephemeral` left the issue in
  `issues` carrying `ephemeral = 1` — still durable, still versioned, still
  replicated, but invisible to every wisp-plane read and skipped by JSONL
  export. Both routes now perform the move. The inverse spellings
  `--persistent` and `--history` appeared to work before only by accident:
  moving a row that never left `issues` is a no-op that still clears the flag.
  **This does not repair existing rows.** Any issue a proxied
  `bd update --ephemeral` produced before this release is still in `issues`
  with the flag set, and nothing here finds it; that needs its own migration.

- **Proxied `bd reopen` reopens an issue in a configured done status**
  (bd-1f5a2). The proxied handler short-circuited on `status != closed` and
  reported "nothing to do" for any workspace-configured done status, so on a
  team server an issue in a custom done state such as `verified` could not be
  reopened at all while the same command worked locally. Both routes now go
  through `issueops.Lifecycle.Reopen`, which spans the whole configured done
  category. Reopening something genuinely not closed is still a no-op and
  still says so.

- **`bd update --parent` on a proxied server no longer leaves a stale parent**
  (bd-yby99.26). A parent id is a *set* assignment: setting it replaces every
  parent edge the child has, and clearing it removes them all. The unit-of-work
  route stopped after the first edge it found. A child with two parents
  reparented to a third kept one of the originals, and clearing the parent left
  the rest in place — in both cases reported as a success. Two parents on one
  child is not the common shape, but the failure was silent and the resulting
  graph is wrong for every reader that walks parent edges. The Dolt backends
  already applied the set rule; this brings the third route into line.

- **`bd dep remove` on a wisp-sourced edge now removes it** (bd-yby99.17). On
  the proxied server the removal was pinned to the durable plane: an edge whose
  *source* is a wisp was looked for in `dependencies`, not found, and reported
  as `Removed: false` with a nil error while the row stayed in
  `wisp_dependencies`. The command exited zero having changed nothing. A
  removal cannot put an edge anywhere, so it now reads the source's plane and
  deletes from there, and the delete's own result is the found/not-found
  verdict. Removing an edge that genuinely does not exist is still a nil-error
  no-op, as before.

- **A dependency on an issue in another repository no longer fails with a raw
  foreign-key error** (bd-ocrn7). beads addresses a foreign repo's work by
  prefix — `otherrig-9001` from a rig whose own prefix is `bd-` — and the
  database holds such a target in `depends_on_external`, a column with no
  foreign key, precisely because the row cannot exist locally. The domain
  route classified the target by looking only for the literal `external:`
  prefix, decided `otherrig-9001` was local, wrote it to
  `depends_on_issue_id`, and the foreign key rejected the insert with
  `Error 1452`. No edge was written and the error named a constraint rather
  than the problem.

  Both routes now classify through one named rule,
  `issueops.IsExternalDepTarget`: a target is external if it is spelled
  `external:...` **or** its prefix differs from the source's. That is what the
  Dolt backends have always done, so this brings the third route into line
  rather than inventing a policy. The foreign key itself is untouched, and a
  genuinely local, genuinely missing target is still refused as before. One
  consequence beyond the failing case: a cross-prefix target that *does*
  happen to exist in the local issues table now also lands in the external
  column on this route, which means derived blocking no longer reads that edge
  — again matching what the Dolt backends already produced.

- **A ready claim that wins an ephemeral row no longer reports a spurious
  "could not be verified" error** (bd-yby99.4). In server mode, `bd` re-reads a
  claim it just made to confirm the write landed, because an exit code is not
  truth against a degraded server. That re-read always looked in the `issues`
  table. But a ready claim can win a wisp when the request's filter admits
  ephemeral rows (`bd ready --claim --include-ephemeral`), and the claim is
  written to the `wisps` table by design — so the verify hunted for the row in
  a table that never held it and turned a perfectly good claim into
  `reported success but could not be verified (server degraded?)`. The
  verification is now plane-aware rather than skipped: a wisp claim is checked
  against the `wisps` row, so no check was traded away for the fix.

  The `ClaimNext` contract is now explicit that the ready set is whatever the
  filter admits — an ephemeral row the filter pulls in is genuinely claimable,
  and claiming it moves that row rather than promoting it to a durable issue.
  Two properties follow that callers should plan around: such a claim records
  **no** durable history entry, because ephemeral rows are not versioned, so
  reconstructing who took what from history alone will not show it; and it
  takes no lease, so `bd heartbeat` refuses it and `bd reclaim` will not
  recover it if the agent holding it dies.

- **`bd dolt push` and `bd sync` no longer adopt a Dolt remote derived from
  git origin without consent** (#5068). On a rig with no Dolt remote
  configured, both commands silently derived one from `git remote get-url
  origin`, added it, persisted `sync.remote` into `.beads/config.yaml`,
  committed that config change under the user's git identity, and uploaded the
  full issue history — no prompt, no flag, no opt-out. A public git origin
  therefore published the whole issue database on a command the user believed
  targeted an already-configured remote.

  Adoption is now a consent decision and fails closed. Interactively, bd shows
  the derived URL and every side effect that follows a yes (including the
  config commit made under your git identity) and defaults to **no**.
  Non-interactively it refuses and exits non-zero, naming the URL it would have
  adopted and the explicit opt-in. `--yes`/`-y` consents ahead of time for
  scripted use; `--no-adopt` or `BD_NO_REMOTE_ADOPT=1` disables adoption
  entirely and wins over `--yes`. Rigs with a remote already configured are
  unaffected — that path was and remains a no-op.

  Workspace resolution also moved below the gate: it calls
  `prepareSelectedNoDBContext`, which mutates workspace state, and nothing may
  mutate before consent is established.

- **Proxied-server CI shard 1 flake: `TestProxiedServerCleanDatabases` ran a
  server-global destructive command against the shared test container**
  (p1-9lf, hazard tracked in p1-8dz). The test used the shared external Dolt
  container under `t.Parallel()`, and `bd dolt clean-databases` drops every
  database matching `staleDatabasePrefixes` on the server it is pointed at.
  The container is bootstrapped with a database named `beads_test`
  (`internal/testutil/container_provider.go`), which matches one of those
  prefixes — so every run of this test dropped the shared container's own
  bootstrap database out from under whatever sibling tests were mid-flight,
  which is timing-correlated with the `busy buffer` / "pending schema
  migrations alter pre-existing dirty tables" failures seen in
  `TestProxiedServerDelete` and `TestProxiedServerFindDuplicates`. The test now
  runs against a dedicated proxied server (`bdProxiedInit`), like the new purge
  test, so its blast radius is its own server.

- **`bd dolt clean-databases --purge-dropped` now works in proxied-server
  mode** (p1-9lf). The command read the flag and then dropped it on the floor
  before dispatching to the proxied-server implementation, so under
  `--proxied-server` stale databases were dropped but never purged: their
  directories stayed under `.dolt_dropped_databases/`, disk was never
  reclaimed, and nothing reported that the flag had done nothing. The proxied
  dual had drifted further than that one flag — it also lacked the
  batching/circuit-breaker drop loop, the dry-run "purge ignored" notice, and
  the `DOLT_UNDROP` recoverability trailer, and its early return on "nothing
  stale found" was incompatible with purge semantics (a prior run's residue is
  invisible to `SHOW DATABASES`, so `--purge-dropped` must still fire when this
  run drops nothing). Both topologies now share one implementation keyed on
  `versioncontrolops.DBConn`, which `*sql.DB` (direct server) and the pinned
  non-transactional `*sql.Conn` (proxied server, via
  `uow.MaintenanceProvider.RunNonTx`) both satisfy, so the behavior cannot
  diverge again. Per-operation timeouts now derive from the command's context
  on both paths, so Ctrl-C interrupts a long clean.

- **`bd dolt clean-databases` gains an opt-in `--purge-dropped` flag to
  reclaim disk from what it drops** (be-pq5,
  [#3663](https://github.com/gastownhall/beads/pull/3663)). `DROP DATABASE`
  only moves a database's directory under `.dolt_dropped_databases/`; Dolt
  keeps the on-disk data there — recoverable via `CALL DOLT_UNDROP(name)` —
  until an explicit `CALL DOLT_PURGE_DROPPED_DATABASES()`. The operator-facing
  cleanup command dropped stale `testdb_*`/`benchdb_*`/etc. databases from
  `SHOW DATABASES` but never issued that purge, so disk usage on the shared
  Dolt server stayed high across repeated runs. `--purge-dropped` runs the
  purge after cleanup (a failed purge is reported as a warning rather than
  aborting the command, since the databases are already dropped from the
  server's active set at that point) and fires even on a run that finds
  nothing stale to drop, since a prior run's dropped-but-unpurged residue
  isn't visible to `SHOW DATABASES` either. **`CALL
  DOLT_PURGE_DROPPED_DATABASES()` is server-global and irreversible** — Dolt
  has no way to scope it to only the databases a given run dropped, so
  `--purge-dropped` also permanently deletes every other dropped-but-not-yet-
  purged database on the server, removing `DOLT_UNDROP` recovery for all of
  them. It defaults to off for that reason; without it, dropped databases are
  left in place, recoverable via `DOLT_UNDROP`, same as before this change.

### Added

- **Conditional (compare-and-set) updates: `bd update --if-assignee` /
  `--if-status`** (bd-wsqvw, from wyvern's wheelhouse hostile-review epic
  wy-mdi5h; design in `PROPOSAL-cas-conditional-update.md`). When either guard
  is present the update applies only if the issue's current assignee/status
  still equals the expected value — one atomic transaction, nothing written on
  a mismatch, and a loud non-zero exit naming actual vs expected (typed as
  `storage.ErrAssigneeMismatch` / new `ErrStatusMismatch`). `--if-assignee ''`
  means "expected unassigned". A guard mismatch is machine-distinguishable
  from infra failure: exit code **13** when every failure in the run was a
  stale guard (a racer won — skip gracefully) vs 1 for anything else
  (retry/abort); in `--json` mode each entry in the failure report's `failed`
  array additionally carries `"guard_mismatch": true`, and the stderr text
  always contains the sentinel token `assignee mismatch` / `status mismatch`. This closes the two coordination transitions no
  existing verb could express: reassign X→Y only while X still holds it
  (`bd update <id> --if-assignee worker -a mayor`), and claim-on-behalf with a
  status guard (`bd update <id> --if-assignee '' --if-status open -a owner -s
  in_progress`). Guards require a field update to ride on, are mutually
  exclusive with `--claim` (its own CAS), compose with each other and with the
  engine's `ExpectedVersion` row CAS, and work in every dolt mode. In Dolt
  server mode a guarded update that writes assignee/status is claim-family and
  is resolved by the bd-zccb9 verify-by-re-read protocol, so its exit code
  stays truthful under a degraded server. Library consumers get the same
  guards via new `UpdateIssueOptions.ExpectedAssignee`/`ExpectedStatus` fields
  on the existing `UpdateIssueChecked` — no interface change; out-of-tree
  `Storage` implementations that ignore the new fields simply do not enforce
  them and should add support before advertising guard semantics.

- **Pool-aware claiming via the `claim.pools` config key** (bd-bguz6).
  Dispatcher fleets pre-assign issues to a pool pseudo-assignee (e.g.
  `fable-crew`); `--claim` previously refused those ("already assigned"),
  forcing a two-step `--assignee <me> -s in_progress` per issue. Aliases
  listed in `bd config set claim.pools "fable-crew,night-crew"` are now
  claimable by any actor through the same atomic compare-and-swap — the
  claim stamps the normal lease, and issues assigned to a real actor (or
  to an alias not in the config) keep their anti-steal protection.
  Pool-aware claiming works in every dolt mode — the proxied-server claim
  path applies the same predicate — and `claim.*` is a recognized config
  namespace (documented in `bd config --help`). Note: if a pool take's
  lease expires, `bd reclaim` returns the issue to the unassigned pool,
  not to the pool alias it was dispatched to.
  Off by default: with no `claim.pools` configured, behavior is unchanged.

- **Work leases: claim-TTL, heartbeat, and reclaim for dead-worker recovery**
  (schema v54, migration `0054`)
  ([#4537](https://github.com/gastownhall/beads/pull/4537)). A claim was
  previously permanent — a worker that died mid-task stranded its issue
  `in_progress` forever. Claims now carry a lease:
  - Claiming stamps `lease_expires_at = now + TTL` (default 5m) and
    `heartbeat_at`.
  - `bd heartbeat <id>` — owner-only; pushes the lease forward. Fails once
    the lease is gone.
  - `bd reclaim --older-than <dur>` — reverts `in_progress` issues whose
    lease expired more than `<dur>` ago back to ready (clears
    assignee/started_at, records a `lease_reclaimed` event). Default grace
    2×TTL.

  Because Dolt has no row locking and merges concurrent commits
  cell-by-cell, every status/ownership/lease-mutating path also rewrites a
  shared `row_lock` cell, forcing a racing heartbeat vs. reclaim to a
  serialization conflict that the retry layer replays — instead of silently
  cell-merging into a zombie claim. The same landing wraps the work-queue
  hot paths (`bd ready --claim`, claim, update, close) in serialization-
  conflict retry, so N concurrent workers draining one queue no longer
  surface MySQL 1213/1205 errors.
- `bd migrate --force` (and `bd migrate schema --force`): CLI flag twin of
  `BD_ALLOW_REMOTE_MIGRATE=1` for bypassing the remote-migrate gate (#4259)
  as the single designated migrator; process-local so it cannot leak into
  child processes (git hooks, dolt subprocesses).
- **Cursor agent hooks.** `bd setup cursor` now installs `.cursor/hooks.json`
  alongside the existing rules file, wiring three Cursor lifecycle events to a
  new hidden `bd cursor-hook` command:
  - `sessionStart` injects full `bd prime` context into every new agent session.
  - `preCompact` arms a one-shot refresh marker (and notifies the user).
  - `postToolUse` re-injects `bd prime` exactly once after a compaction, then
    no-ops.

  This brings Cursor (IDE and recent `cursor-agent` CLI builds — verified on the
  2026.06 line; early-2026 CLI builds only fired shell hooks) to parity with
  the Claude Code / Codex hook integrations, so Beads context survives context
  compaction instead of being forgotten. Existing user hooks in
  `.cursor/hooks.json` are preserved, and `bd setup cursor --remove` only
  removes the Beads-managed entries.

  The integration now matches Claude/Codex on several more fronts:
  - **`bd init` auto-installs Cursor** (rules + skill + hooks) the same way it
    auto-sets up Claude Code and Codex, and stages `.cursor/` for commit.
  - **`bd setup cursor --global`** writes hooks to `~/.cursor/hooks.json` (and the
    agent skill to `~/.agents/skills/beads`) so they apply to every project.
    (Global scope is hooks + skill only; Cursor has no reliable file-based global
    *rules* location — those live in Cursor Settings.)
  - **Canonical rules content.** `.cursor/rules/beads.mdc` now wraps the shared
    `recipes.Template` (the same content every other file-based recipe uses)
    instead of a hand-maintained copy, so it no longer drifts or omits the Issue
    Types / Priorities / git-authority sections.
  - **Agent skill.** `bd setup cursor` installs the shared
    `.agents/skills/beads/SKILL.md` (which Cursor loads natively), so Cursor-only
    users get the same progressive-disclosure skill Codex installs. The skill is
    shared with Codex and is only removed once no integration still relies on it.
  - **`bd doctor`** reports `Cursor Integration`, `Cursor Settings Health`
    (malformed `.cursor/hooks.json` → error), and `Cursor Hook Completeness`
  (all three lifecycle events present) checks — with agent-mode enrichment —
  paralleling the Claude integration checks.

### Changed

- **Remote-ahead adopts fast-forward automatically when it is provably
  loss-free AND lands exactly at this binary's latest migration.** When the
  smart remote-migrate gate
  ([#4516](https://github.com/gastownhall/beads/issues/4516)) finds the
  remote ahead with no content skew, this clone's local Dolt history a
  strict ancestor of the remote's with a clean working set, AND the
  fast-forward would land exactly at the binary's own latest migration
  (nothing pending after it, nothing beyond what this binary supports), `bd`
  now fast-forwards to the remote's migrated schema automatically instead of
  stopping with an adopt directive — nothing local is discarded. An unpushed
  local commit, a dirty working set, or a remote that is not exactly at this
  binary's latest migration all disqualify the automatic fast-forward and
  fall back to the existing manual `bd bootstrap` adopt directive (with the
  sharper loss-free guidance whenever ancestor+clean still hold), never a
  forced write. Set `BD_SMART_GATE=0` to opt out and keep the manual wall for
  every remote-ahead case, as before
  ([#4259](https://github.com/gastownhall/beads/issues/4259)).

### Fixed

- **Claim-family writes are now verified by re-read in Dolt server mode**
  (bd-zccb9, from wyvern incident wy-ejph3). Under a degraded sql-server the
  exit status of `bd update --claim` / `bd claim` / unclaim was not truth in
  either direction: a claim could report success while the server-side
  transaction died with the abandoned connection and rolled back (a phantom
  claim that later cost a duplicate implementation), and conversely a
  connection error could print with the write actually applied. After the
  claim transaction, bd now re-reads the issue's assignee and status on a
  fresh connection and resolves the outcome against the database: a reported
  success that did not land fails loudly ("treat the claim as NOT applied");
  an ambiguous commit-phase loss is settled by the re-read — verified applied
  becomes an accurate success, verified rolled back is replayed once (safe:
  nothing landed). Applies to claim, ready-claim, and unclaim in server mode;
  wisps and embedded mode are unchanged. New metrics
  `bd.claim_verify_lost_total` and `bd.claim_verify_recovered_total` count
  loud failures and converted outcomes.

- **Single-issue `bd create --id P.N` no longer leaves `child_counters` stale**
  (one root cause of [#4750](https://github.com/gastownhall/beads/issues/4750);
  the issue stays open — three other drift paths remain). `CreateIssueInTxWithResult`
  only reconciled `child_counters` when the batch path called
  `ReconcileChildCounters`; an explicit hierarchical `--id` never advanced the
  parent's `last_child` high-water mark. Once such children were later
  archived out of `issues`, `GetNextChildIDTx` no longer saw them and could
  re-mint an already-used suffix — an ID collision with an archived issue.
  The single-issue path now calls `ReconcileChildCounters` too, and its
  changed tables are merged into the result so the reconcile actually gets
  staged for commit.

## [1.1.2] - 2026-07-26

Hotfix release cut from v1.1.0. (There is no 1.1.1 release: its tag was cut
but the release pipeline's package gate failed on a stale MCP lockfile before
publishing anything, and the version number was burned rather than moving the
tag.) If `bd migrate` on 1.1.0 aborted with
`rekey aux row ids: <table>: ... invalid hash length` and the database then
refused to open, this release lets the migration complete.

### Fixed

- **The v53 aux row re-key survives dolt#11131 encoding drift.** On storage
  affected by the upstream Dolt adaptive-encoding bug, reading the drifted
  cells panics (`invalid hash length: 19`), which aborted the migration and
  left the database unopenable under 1.1.0. The re-key now skips such a
  table with a warning, records it in a clone-local drift record
  (`aux_row_rekey_drifted` in `local_metadata`), and completes the
  migration; recorded tables are retried on later migration passes and are
  exempted from the changed-signature dirty-table guard so the retry cannot
  re-brick the database
  ([#4380](https://github.com/gastownhall/beads/issues/4380)).

## [1.1.0] - 2026-07-04

First stable release of the 1.1.0 line. It consolidates everything from
[1.1.0-rc.1] and [1.1.0-rc.2] (see those sections below for the full feature
set) plus the post-rc.2 migration-recovery fixes listed here. The theme of
1.1.0 is a safe schema-migration and upgrade path: it repairs the v52/v53
drift classes that broke real-world rc.1/rc.2 upgrades, makes an interrupted
migration recoverable in-tool instead of a dead end, and turns the
remote-migrate gate from a blunt block into a state-aware one.

### Upgrade Notes

- **Back up before migrating** (`bd export --all -o backup.jsonl`) before
  running `bd migrate` on an older, especially remote-backed, database. The
  fixes below repair databases that reached a migration with drifted state;
  an export is cheap insurance while such drift is being repaired.

### Changed

- **The state-aware remote-migrate gate is now on by default.** rc.2 shipped
  the smart gate behind an opt-in env var, but nothing in the gate's block
  message or docs surfaced that the var existed, so in practice everyone kept
  hitting the blunt always-block behavior. The provably-safe first-mover case
  (remote at the same schema version as this clone) now auto-migrates without
  `BD_ALLOW_REMOTE_MIGRATE`; remote-ahead still stops with an adopt directive
  and content skew still stops for a human. Set `BD_SMART_GATE=0` to opt out
  and restore the unconditional block
  ([#4516](https://github.com/gastownhall/beads/issues/4516)).

### Fixed

- **A failed v53 migration no longer traps the database, and the v53 repair
  now covers `wisp_dependencies` split-column drift.** rc.2 repaired the
  `issues` rig columns, but a database coming from schema v49 could still fail
  v53 when its `wisp_dependencies` table was missing the split target columns
  (`depends_on_issue_id`, `depends_on_wisp_id`, `depends_on_external`) — and a
  half-applied v53 then bricked every subsequent command on open
  ([#4555](https://github.com/gastownhall/beads/issues/4555)). The migration
  runner now adds and backfills the missing split columns before v53, and a
  narrow recovery gate lets an already-failed-v53 database self-heal on the
  next open — including the `issue_snapshots` / `compaction_snapshots` tables
  that migration 0051 leaves dirty during a single-pass v49→v53 upgrade
  ([#4558](https://github.com/gastownhall/beads/pull/4558)).
- **A dirty working set no longer deadlocks migration recovery in embedded
  mode.** Every store open runs the migration, which correctly refuses to
  alter tables that have uncommitted working-set changes — but the documented
  recovery (commit the working set) is itself a command that opens the store
  and hits the same refusal, a deadlock with no in-tool escape
  ([#4566](https://github.com/gastownhall/beads/issues/4566)). `bd dolt commit`
  and `bd vc commit` now open past that guard, commit at the current schema,
  after which a normal `bd migrate` applies cleanly; the refusal message now
  names the recovery path
  ([#4567](https://github.com/gastownhall/beads/pull/4567)).

## [1.1.0-rc.2] - 2026-07-02

Second release candidate for 1.1.0. Fixes the two upgrade-breaking migration
regressions reported against rc.1 (#4502, #4534), hardens the remote-migrate
gate that rc.1 introduced, and ships the validated upgrade documentation.

### Upgrade Notes

- **Back up before migrating.** The upgrade guide's remote-backed recipes now
  start with a JSONL export (`bd export --all -o ...`) before
  `BD_ALLOW_REMOTE_MIGRATE=1 bd migrate`. The two fixes below repair real
  databases that reached a migration with drifted state; an export is the
  cheap insurance while such drift is being repaired.

### Fixed

- **v53 migration no longer fails on pre-rig `issues` tables.** The rig/agent
  columns (`hook_bead`, `role_bead`, `agent_state`, `last_activity`,
  `role_type`, `rig`) were only ever added to the squashed bootstrap
  `0001_create_issues`, so a database bootstrapped before they existed sits at
  schema v52 without them — and migration 0053, which copies those columns
  from `wisps`, failed with `Unknown column 'agent_state' in 'issues'` even
  with zero rig wisps to repair
  ([#4502](https://github.com/gastownhall/beads/issues/4502)). The migration
  runner now repairs the drift in code immediately before applying v53,
  adding whichever of the six columns are missing (databases in the wild may
  have some but not all). Shipped migration files stay frozen — the repair
  lives in the runner because a failing migration can never be fixed forward
  by a later migration file.
- **One orphaned `child_counters` row no longer bricks every `bd create`.**
  Migration 0039 dropped `fk_counter_parent` and clone-local migration 0002
  re-added it under `FOREIGN_KEY_CHECKS = 0`, so a counter row orphaned during
  the FK-less window (an interrupted create, a parent deleted without cascade)
  survived the constraint's return — and Dolt then failed constraint
  validation on every subsequent insert, including brand-new top-level issues,
  on an otherwise healthy database
  ([#4534](https://github.com/gastownhall/beads/issues/4534)). A new
  clone-local migration moves any live-wisp counters to `wisp_child_counters`
  and deletes rows dangling from `issues` — the same rows the FK's
  `ON DELETE CASCADE` would have removed had it been in force. Runs
  automatically on the next command with the new binary; already-bitten
  databases are healed in place.
- **Old binaries fail fast on writable opens of a schema-newer database**
  instead of proceeding against a schema they do not understand
  ([#4531](https://github.com/gastownhall/beads/pull/4531)) — the guard rail
  for the mixed-version window during multi-clone upgrades.
- **Prerelease GitHub releases now ship prerelease-correct install
  instructions.** The release notes header previously showed the stable
  install methods (brew, install scripts), three of which do not deliver a
  prerelease ([#4530](https://github.com/gastownhall/beads/pull/4530)).
- **`bd remember` no longer clobbers a memory whose content is its own key**,
  and a bare `bd remember <existing-key>` now recalls instead of overwriting.

### Added

- **Smarter remote-migrate gate.** The gate introduced in rc.1 is now
  state-aware and agent-safe: cases that are provably safe to migrate
  auto-resolve instead of stopping every agent at the wall, while genuinely
  risky states still require the designated migrator
  ([#4515](https://github.com/gastownhall/beads/pull/4515),
  [#4516](https://github.com/gastownhall/beads/pull/4516)).
- **Backend-agnostic storage conformance test suite**
  ([#4414](https://github.com/gastownhall/beads/pull/4414)).

### Documentation

- **Validated upgrade recipe for remote-backed / multi-clone databases** in
  the upgrade guide, exercised end-to-end on a live database
  ([#4514](https://github.com/gastownhall/beads/pull/4514)), plus a
  consolidated Homebrew tap-migration snippet across install docs, a README
  pointer to the upgrade guide, and a pre-migrate backup step in the
  recipes (this release).

## [1.1.0-rc.1] - 2026-06-23

### Upgrade Notes

- **Mixed bd versions sharing one Dolt remote.** Pre-1.0.6 binaries record
  applied migrations as `(version, NULL)` while 1.0.6+ records
  `(version, sha256)` in `schema_migrations`, so two clones applying the same
  migration with different bd vintages used to produce a row conflict on
  `bd dolt pull`. The pull auto-resolver now resolves this class by keeping
  whichever side recorded the hash. Two *different* recorded hashes for the
  same version are the real [#4259](https://github.com/gastownhall/beads/issues/4259)
  schema fork and are still surfaced to the operator (see the `bd doctor`
  Migration Content Skew check). To avoid the mixed-vintage window entirely,
  upgrade all clones of a shared remote together, letting one designated
  machine migrate and `bd dolt push` first.

- **Upgrading a remote-backed database: one machine migrates, the rest adopt.**
  This release reshapes the `dependencies` primary key (migration `0050`), and
  `bd` now refuses to silently migrate a database that has a Dolt remote
  configured. If two clones cross the `0050` boundary with un-synced dependency
  edits on both sides, their histories become permanently un-mergeable — Dolt
  refuses the merge outright (`cannot merge because table dependencies has
  different primary keys in its common ancestor`) before the pull auto-resolver
  can run. Replacing the binary alone is not enough; follow the ordered recipe:

  1. With your **current** binary, on **every** clone: `bd dolt push` +
     `bd dolt pull` until all are in sync, then stop editing. Once the new
     binary is installed, `push`/`pull` are gated too, so this must happen
     first.
  2. Designated migrator **only**: install the new binary, then
     `BD_ALLOW_REMOTE_MIGRATE=1 bd migrate` and `bd dolt push`.
  3. Every **other** clone: install the new binary, then `bd bootstrap` to adopt
     the migrated database (`bd dolt pull` is refused while the clone still has
     pending migrations; do not migrate on these clones).
  4. Confirm with `bd version`; in server mode, `bd doctor` adds a
     migration-content-skew check (not available in embedded mode).

  A single clone with a remote follows the same gate (push with the old binary,
  then migrate and push). Full recipe and the single-clone variant:
  [Upgrading bd — remote-backed databases and multiple clones](website/docs/getting-started/upgrading.md#remote-backed-databases-and-multiple-clones).
  If clones have already forked, see the recovery playbook:
  [docs/RECOVERY.md#pk-fork-refused](docs/RECOVERY.md#pk-fork-refused).

### Added

- **perf(schema):** composite `(status, updated_at)` and standalone `defer_until` indexes on `issues` (migration 0052). Speeds up `bd stale` and `bd ready` on large rigs. First run after upgrade applies the migration — expect a one-time 10–25 s pause on rigs with >10K issues. Do not interrupt.
- **Per-migration content hash.** `schema_migrations` now records the SHA-256 of each migration's file content alongside its version (`content_hash`), so two clones at the same `MAX(version)` but with divergent migration content become detectable (reporter fix No.2 for [#4259](https://github.com/gastownhall/beads/issues/4259)). The column is added to fresh databases via the bootstrap schema and idempotently to existing databases at migrate time; already-applied rows keep a NULL hash. The column definition and the hashes are deterministic, so `schema_migrations` still merges cleanly across clones.
- **`bd doctor` migration-content-skew check.** Using the recorded hashes, `bd doctor` now compares the local `schema_migrations` against the cached remote-tracking ref (no network fetch) and warns when this database and its remote applied different content for the same migration version — the silent schema fork from [#4259](https://github.com/gastownhall/beads/issues/4259), surfaced as a clear advisory instead of a cryptic merge failure. Read-only diagnostic (it does not gate push/pull); the comparison primitive (`schema.ContentHashSkew`) is reusable.
- **Remote-migrate prevention gate.** `bd` now refuses to silently auto-apply pending schema migrations to an existing database that has a remote configured (in both server and embedded mode), and tells the operator to choose: migrate (as the single designated migrator, then `bd dolt push`) or adopt the already-migrated database from the remote (`bd bootstrap`). Migrating each clone independently forks the schema and breaks `bd dolt pull` ([#4259](https://github.com/gastownhall/beads/issues/4259)). The gate is a no-op for fresh databases, databases already at the binary's version, databases with no remote, and read-only opens. The designated migrator proceeds with `BD_ALLOW_REMOTE_MIGRATE=1`. In server mode the gate also detects remotes persisted on disk in `.dolt/config`, so a freshly (auto-)started server — whose in-memory `dolt_remotes` table is not yet populated — cannot slip a remote-backed database past the gate.

- **Recovery guidance for Dolt primary-key merge refusals.** When `bd dolt pull` (or `push`) fails with Dolt's hard refusal `cannot merge because table X has different primary keys [in its common ancestor]` — the un-mergeable schema fork left behind by independently-migrated clones straddling a PK-reshaping migration ([#4259](https://github.com/gastownhall/beads/issues/4259)) — `bd` now recognizes the error class and prints the bootstrap-from-canonical-clone recovery recipe instead of just the raw error. Full playbook: [docs/RECOVERY.md#pk-fork-refused](docs/RECOVERY.md#pk-fork-refused).

### Fixed

- **Cross-clone issue-delete vs child-row-insert merges now converge.** The
  synced child tables (`dependencies`, `labels`, `comments`, `events`,
  snapshots) carry `FOREIGN KEY ... ON DELETE CASCADE` to `issues` (migrations
  `0041`/`0042`), but Dolt merges row-wise and never re-executes cascades: if
  clone A deleted an issue while clone B added a dependency, label, or comment
  referencing it, `bd dolt pull` failed outright with a foreign-key constraint
  violation ("transaction rolled back"), recorded nothing in `dolt_conflicts`,
  and could never converge on retry. The pull auto-resolver (SQL and CLI
  routes) now repairs this class by applying the foreign key's own cascade
  semantics — deleting the merged-in rows whose issue reference dangles — and
  refuses, exactly as before, any constraint violation it does not recognize
  (different constraint type, table bd does not own, FK to another parent).
  The repair converges across clones: it deletes precisely the rows the
  cascade already removed on the deleting clone. (bd-6dnrw.4)

- **`is_blocked` recompute no longer misses auto-resolved merges.** A pull
  whose merge bd auto-resolved (conflicts) or cascade-repaired (constraint
  violations) lands in the working set without advancing HEAD — the merge
  commit is created later — so the bd-6dnrw.3 post-pull recompute mistook it
  for "nothing merged" and skipped, leaving `is_blocked` stale exactly when
  the merge had needed intervention. The recompute now diffs to the working
  set and skips only when `issues`/`dependencies` are clean too. (bd-6dnrw.39)

- **`is_blocked` recomputed after `bd dolt pull`.** The denormalized
  `is_blocked` column (migration `0046`) was maintained only by local write
  paths, so a pull that merged another clone's writes silently left it stale —
  e.g. clone A closes blocker X while clone B adds an edge W→X, and the merged
  result carries `W.is_blocked=1` with a closed blocker, hiding W from
  `bd ready`. Every pull path (server SQL and CLI routes, peer pulls, embedded
  mode) now recomputes the column for exactly the rows the merge changed,
  scoped via `dolt_diff` between the pre- and post-pull HEADs and expanded
  through the same affected-set logic the local write paths use. Oversized or
  schema-reshaping merges fall back to a full recompute; conflicted pulls skip
  it until the operator resolves. (bd-6dnrw.3, PR 4107 follow-up)

- **Stale `is_blocked` is now repairable — `bd recompute-blocked`.** The
  post-pull recompute above is scoped to the merge diff and is skipped when a
  re-pull merges nothing (`HEAD` unchanged), so a recompute that failed *after*
  its merge committed — or a conflicted pull resolved by hand (which skips the
  recompute) — could leave `is_blocked` stale with no way to repair it: rerunning
  `bd dolt pull` merged nothing and recomputed nothing. The new
  `bd recompute-blocked` command runs a full, unconditional recompute over every
  issue and wisp and commits the result; it is idempotent and works in **both**
  embedded and server mode (unlike `bd doctor`, which is server-mode only).
  Server-mode `bd doctor` also gains a read-only **Blocked State** check that
  reports stale rows and a `bd doctor --fix` that runs the same repair.
  (bd-6dnrw.37)

- **Deterministic history-table primary keys (cross-clone merge-safety).**
  Migration `0037` converted the legacy BIGINT primary keys of `events`,
  `comments`, `issue_snapshots` and `compaction_snapshots` by backfilling
  `UUID()` — a different random value on every clone, the same hazard class as
  the `dependencies` fork below. Two legacy clones that upgraded independently
  held identical history rows under different primary keys, so their merges
  duplicated every pre-upgrade event and comment (or refused outright). A
  one-time-per-clone upgrade backfill now rewrites those ids to deterministic
  values derived from each row's content (`internal/storage/rowid`), so
  independently-migrated clones converge to byte-identical history tables;
  exact-duplicate rows are kept distinct via per-duplicate ordinals. The pass
  is gated on a clone-local marker (ignored migration `0009`), so steady-state
  opens never re-key — rows inserted after the pass are minted once and reach
  other clones by merge, so their ids are already consistent. (bd-6dnrw.2,
  the history-table sibling of
  [#4259](https://github.com/gastownhall/beads/issues/4259))

- **Deterministic dependency primary keys (cross-clone merge-safety).** `dependencies.id` (and `wisp_dependencies.id`) were filled by `DEFAULT (UUID())`, a per-clone-random value. Two clones that created the same edge — or that applied migration `0043` independently — diverged on the primary key, so `bd dolt pull` failed unrecoverably (`cannot merge because table dependencies has different primary keys in its common ancestor`, or a `uk_dep_*` unique-key violation). `id` is now derived deterministically from the natural edge key `(issue_id, target)` at every insert site (`internal/storage/depid`); migration `0050` drops the random default and idempotently re-asserts the natural-identity unique keys; and an upgrade backfill rewrites existing rows. Independently-migrated clones now converge to byte-identical, merge-safe `dependencies`. And because the same edge now has the same primary key on every clone, `bd dolt pull` **auto-resolves** a same-edge dependency conflict that differs only in audit columns (created_at/created_by/metadata/thread_id) the same way it already does for the metadata table — so two machines that each run `bd dep add X Y` between syncs merge cleanly. A conflict where the dependency *type* differs is still surfaced for the operator. ([#4259](https://github.com/gastownhall/beads/issues/4259))

## [1.0.5] - 2026-05-28

### Upgrade Notes

- **JSONL auto-export and auto-staging are now opt-in.** Repositories that
  relied on the previous implicit default should set it explicitly before or
  after upgrading:

  ```bash
  bd config set export.auto true
  bd config set export.git-add true
  ```

  Dolt is the primary datastore. `.beads/issues.jsonl` is now treated as an
  optional export for viewers such as `bv`, interchange, and issue-level
  migration. It is not the canonical git-tracked source of truth, not
  cross-machine sync, and not a full database backup. Use `bd dolt push` /
  `bd dolt pull` for sync and `bd backup` for restorable database backups.
  Existing repositories that already have `export.auto: true` or
  `export.git-add: true` configured keep that behavior.

### Added

- **Schema-version guard**: `bd` now hard-fails at database-open time when the
  database schema is ahead of the binary's embedded migrations. This protects
  against cryptic SQL errors (e.g., `column X could not be found in any table
  in scope`) that occur when an older binary opens a database migrated by a
  newer one. The error message names the exact version mismatch and provides a
  copy-paste rebuild command. Use `--ignore-schema-skew` (or env var
  `BD_IGNORE_SCHEMA_SKEW=1`) to bypass the guard when you know the forward
  migrations are safe for your workload. Fresh databases and normal
  forward-migration paths are unaffected.

- **Foreign keys across issue and wisp tables.** Migrations `0040`–`0042` and the new `ignored/0001`–`ignored/0004` add explicit FKs with `ON DELETE CASCADE ON UPDATE CASCADE` on `dependencies`, `labels`, `comments`, `events`, `issue_snapshots`, `compaction_snapshots`, `child_counters`, and the matching `wisp_*` tables. Deleting or renaming a parent row now cascades automatically — the manual cleanup loops in `issueops/delete.go`, `dolt/wisps.go`, `dolt/ephemeral_routing.go`, and `cmd/bd/rename_prefix.go` have been removed (net ~300 lines down). ([#3952](https://github.com/gastownhall/beads/pull/3952))
- **`issueops.DeleteWispFromDependenciesInTx` / `UpdateWispIDInDependenciesInTx`.** Because Dolt forbids foreign keys from tracked tables (`dependencies`) to `dolt_ignore`'d tables (`wisps`), wisp deletion and rename now invoke these helpers explicitly to keep `dependencies.depends_on_wisp_id` consistent. The standard store APIs (`DeleteIssue`, `UpdateIssueID`, `deleteWispBatch`, etc.) wire them up automatically; only call them directly if you bypass those entry points. ([#3952](https://github.com/gastownhall/beads/pull/3952))
- **Forward schema-skew guard.** `bd` now hard-fails when it opens a database that has been migrated to a *newer* schema version than the binary understands, instead of operating blindly on forward-migrated data. ([#4152](https://github.com/gastownhall/beads/pull/4152))
- **`dolt.mode` config key.** New `dolt.mode` (`server` | `embedded`) configuration key with validation; `bd init` now warns on ambiguous configs and hard-fails when `dolt.host`/`dolt.port` are set without server mode.
- **`bd list --skip-labels`.** New toggle to skip label hydration for faster listings; honored in both text and JSON output.
- **`bd show` count-only JSON details.** `bd show --json` can return count-only relationship details with opt-in streamed payloads, avoiding large hydrations when only counts are needed.
- **Server-side custom type validation.** Storage now honors `.beads/config.yaml` `types.custom` during server-side validation. ([#4024](https://github.com/gastownhall/beads/pull/4024), [#4026](https://github.com/gastownhall/beads/pull/4026))
- **`bd init` contributor routing.** On fork detection, `bd init` auto-configures contributor routing. ([#4028](https://github.com/gastownhall/beads/pull/4028))

### Changed

- **Large content columns widened to `LONGTEXT`.** Issue content and event/wisp-event value columns are migrated from `TEXT` to `LONGTEXT`, lifting the previous ~64 KB per-field ceiling. ([#4027](https://github.com/gastownhall/beads/pull/4027), [#3682](https://github.com/gastownhall/beads/pull/3682))

- **`dependencies.depends_on_id` is now a STORED generated column.** The polymorphic target has been split into three typed columns: `depends_on_issue_id`, `depends_on_wisp_id`, `depends_on_external`. `depends_on_id` remains as `COALESCE(...) STORED` for read paths; **writes to `depends_on_id` will fail** — code that inserts dependencies must populate exactly one typed column (enforced by a new `ck_dep_one_target` CHECK). Same split mirrored to `wisp_dependencies` with a corresponding `ck_wisp_dep_one_target`. Migrations `0041` (tracked) and `ignored/0003` (wisps) perform the column split, copy existing rows by classifying their targets against `issues` / `wisps`, and add the new typed-target indexes. ([#3952](https://github.com/gastownhall/beads/pull/3952))
- **Most existing dependency-table FKs now use `ON UPDATE CASCADE`.** Migration `0042` rebuilds `fk_dep_issue`, `fk_labels_issue`, `fk_comments_issue`, `fk_events_issue`, `fk_snapshots_issue`, and `fk_comp_snap_issue` to cascade on both delete and update. Same treatment for the wisp-side FKs in `ignored/0003`. Prefix rename and ID-update paths rely on this cascade instead of touching aux tables manually. ([#3952](https://github.com/gastownhall/beads/pull/3952))
- **Migrations `0041` and `0042` are intentionally irreversible.** The matching `.down.sql` files are documented no-ops because rebuilding the polymorphic column from typed columns would require schema-aware backfill that the storage layer no longer performs. Restore from a prior `dolt` commit if rollback is required. ([#3952](https://github.com/gastownhall/beads/pull/3952))
- **`BEADS_MAX_ROWS` / `--max-rows` defensive row cap** — ops can now set `BEADS_MAX_ROWS=N` (or pass `bd list --max-rows N`) to bound `SearchIssues` result sets. Default is disabled; on overage, `bd` exits with code 2 and writes a two-line error to stderr (stdout stays empty so `jq` pipelines don't get half-rendered JSON). The flag is wired on `bd list`, `bd ready`, `bd dep tree`, `bd find-duplicates`, and `bd graph`. The env var is also honored by `bd doctor`, `bd lint`, `bd doctor-conventions`, and `bd doctor-pollution`. `bd cleanup`, `bd gc`, `bd export`, `bd export --auto`, `bd migrate-issues`, and `bd jira` explicitly opt out so a misconfigured env can't abort a sweep partway. (be-x42v)

### Fixed

- **Symlinked `CLAUDE.md` repair guidance** - troubleshooting docs now include
  a tested `git update-index --cacheinfo` recipe for repositories that already
  contain a corrupted `mode 120000` `CLAUDE.md` entry with Markdown content.
  The setup bug was fixed in [#4192](https://github.com/gastownhall/beads/pull/4192);
  this note is for repairing affected clones.
- **Codex hook metadata isolation** - Codex hook metadata now lives under
  `.codex-plugin/hooks/`, preventing Claude from loading Codex-only hooks from
  the shared plugin root while keeping Codex setup pointed at `bd codex-hook`.
  ([#3924](https://github.com/gastownhall/beads/issues/3924))
- **Hook and prime wait bounds** - generated git hooks now fall back from
  `timeout` to `gtimeout` to Perl `alarm`, and `bd prime` memory loading is
  bounded by `BEADS_PRIME_TIMEOUT` (default 10s). Non-positive
  `BEADS_PRIME_TIMEOUT` values fall back to the default instead of silently
  disabling the safety bound. Internal beads-repo git operations also run with
  hooks suppressed via `core.hooksPath=` so automated `.beads/` commits and
  pushes cannot recurse through user hook managers. `GitCmdCWD()` remains for
  user-working-repo status/ref operations and does not perform beads commits.
  ([#4172](https://github.com/gastownhall/beads/pull/4172))
- **JSONL auto-export shrink guard** - when opt-in `export.auto` is enabled,
  auto-export now refuses to overwrite an existing `.beads/issues.jsonl` that
  contains records outside the auto-export scope (memories, infrastructure
  beads, templates, ephemeral wisps, or unknown record types). This prevents
  a viewer/interchange refresh from silently replacing a richer JSONL file
  with the filtered auto-export subset. ([#4069](https://github.com/gastownhall/beads/issues/4069))
- `bd quick` now treats label insertion as part of issue creation: label
  failures abort the create instead of being silently dropped.
- **`bd dolt status` reports externally-managed local servers truthfully** - when a rig is configured as `dolt_mode: server` pointing at a local host but `dolt.auto-start: false` (so an orchestrator or systemd owns the sql-server lifecycle), `bd dolt status` previously said `not running` because no PID file existed. It now SQL-probes the configured endpoint, matching the path already used for non-local hosts, and reports `running (external)` with host/port/database/version when the server answers. **JSON output shape change**: on affected rigs, `bd dolt status --json` now emits `{"running": true, "mode": "external", ...}` instead of `{"running": false, "pid": 0, ...}`. Automation that parsed the old `running:false` as a "needs restart" sentinel should switch to checking `running` directly. (be-0eyj, [#3550](https://github.com/gastownhall/beads/pull/3550))
- **Migrations run on a connection with no read timeout**, so large or slow schema migrations no longer fail under the default query timeout. ([#4154](https://github.com/gastownhall/beads/pull/4154))
- **Duplicate migration version numbers now hard-fail** instead of silently under-applying a release's migrations. ([#4197](https://github.com/gastownhall/beads/pull/4197))
- **`bd close` supports per-id reasons** when closing multiple issues at once ([#4194](https://github.com/gastownhall/beads/pull/4194)), and **re-closing an already-closed issue is now an idempotent success** instead of an error. ([#4025](https://github.com/gastownhall/beads/issues/4025))
- **`bd create --defer <future-date>` creates a deferred issue** (status `deferred`) rather than an open one. ([#4071](https://github.com/gastownhall/beads/issues/4071))
- **`bd create` commits labels atomically** during initial issue creation; label-insertion failures abort the create instead of being silently dropped. ([#4149](https://github.com/gastownhall/beads/pull/4149))
- **JSONL import preserves comment IDs and skips stale issue snapshots.** ([#4103](https://github.com/gastownhall/beads/pull/4103), [#4204](https://github.com/gastownhall/beads/pull/4204))
- **`bd init` Dolt wiring hardening** - honor local-only remote wiring ([#4227](https://github.com/gastownhall/beads/pull/4227)), apply config server mode before validation, platform-accurate config-path warnings, and tighter Dolt marker handling ([#4083](https://github.com/gastownhall/beads/pull/4083)).
- **MCP `validate` and `detect-pollution` route to `bd doctor`.** ([#4037](https://github.com/gastownhall/beads/issues/4037))
- **`bd dolt` detects managed handoff port conflicts.** ([#4217](https://github.com/gastownhall/beads/pull/4217))
- **`bd list` keeps `relates-to` out of dependency tree mode** and honors `--skip-labels` in JSON output. ([#3936](https://github.com/gastownhall/beads/pull/3936))
- **Audit `newID` entropy widened** to reduce ID-collision risk under high create volume. ([#4046](https://github.com/gastownhall/beads/pull/4046))
- **Performance:** `bd count` uses a SQL `COUNT(*)` aggregate instead of fetching and counting rows; `bd ready` narrows deferred-parent child filtering; `bd get` queries primary issues before falling back to wisps; and `bd search` gains skip-wisps merge and wisp-count caching.

## [1.0.4] - 2026-05-07

### Added

- **`bd init --reinit-local` / `--discard-remote`** — named-intent flags for local re-initialization and explicit remote-history override. Replaces the overloaded `--force`. See [`bd help init-safety`](engdocs/adr/0002-init-safety-invariants.md) and [`docs/recovery/init-safety.md`](docs/recovery/init-safety.md).
- **`bd init-safety`** — documents the init flag surface + destroy-token format. Referenced by every init refusal message.
- **Stable exit codes for init refusals** — `10` remote divergence, `11` local exists, `12` destroy-token missing. Grep-safe for CI.
- **[ADR 0002 — `bd init` safety invariants](engdocs/adr/0002-init-safety-invariants.md)** — encodes the single-source identity rule, scope-bound `--force`/`--reinit-local`, the `CheckRemoteSafety` chokepoint, the error-text-no-echo rule, and the race-safety invariant.
- **[`docs/RECOVERY.md`](docs/RECOVERY.md)** — playbooks for each named init refusal.
- **CODEOWNERS** — `cmd/bd/init*.go` routes review to maintainers with an ADR-linked acknowledgment requirement.
- **`bd -C <path>`** — run bd from another directory without changing the caller's shell cwd. Useful for hooks, agents, and scripts that coordinate multiple workspaces.
- **`bd close --reason-file`** — reads close reasons from a file or stdin, matching existing body-file workflows.
- **Linear sync throughput and correctness improvements** — batch create/update, idempotency markers, retry-after handling, OAuth client-credentials support, and workspace-level sync locking.
- **Shared Beads plugin package** — Claude and Codex plugin metadata now ship together from the repository.

### Changed

- **`bd init --force` semantics narrowed to local-only** — `--force` (or `--reinit-local`) bypasses only the LOCAL data-safety guard. It does NOT authorize silent divergence of remote history. When origin has `refs/dolt/data`, `bd init --force` now refuses with exit code 10 unless `--discard-remote` is also passed. Fixes the long-standing footgun where `bd init --force` in a repo with remote Dolt history silently set up an orphan branch that failed to push.
- **Init refusal messages follow What/Why/Next structure** — runtime error text no longer echoes copy-pasteable destructive invocations. Token values and exact override commands live in `bd help init-safety` and `docs/RECOVERY.md` only. Closes the failure class where an AI agent destroyed 247 issues by pattern-matching on the tool's own error output (`58f5989bf`).
- **`beads.OpenBestAvailable` signature changed (breaking)** — returns `(Storage, error)` instead of `(Storage, Unlocker, error)`. The per-open flock and `Unlocker` return value were removed; the embedded Dolt engine handles its own concurrency internally. External SDK consumers that call `OpenBestAvailable` must drop the second return value. ([PR #3614](https://github.com/gastownhall/beads/pull/3614))
- **Embedded-mode flock removed** — the process-lifetime exclusive flock on `.beads/embeddeddolt/` has been removed. Concurrent `bd` processes now open the embedded engine independently. If the GH#2571 nil-deref stack (`NewConnector` → `DoltDB.SetCrashOnFatalError` → `CollectDBs`) resurfaces under concurrent access, it should be filed and fixed in `dolthub/driver`, not reintroduced as a beads-side flock. ([PR #3614](https://github.com/gastownhall/beads/pull/3614))
- **Auto-export is now opt-in by default** — repositories without explicit
  `export.auto` / `export.git-add` settings leave JSONL refresh and staging
  disabled unless a viewer or JSONL integration explicitly enables them. This
  keeps `.beads/issues.jsonl` as an optional export surface, not the default
  mutation path. ([GH#4062](https://github.com/gastownhall/beads/issues/4062))
  Thanks [Kevin Glynn](https://github.com/kevglynn) for surfacing the JSONL
  source-of-truth mismatch through the export cleanup work that led to this
  change.

- **Release workflow uses the checked-in beads-release formula** — the old release shell path now delegates to the formula-backed release workflow with CI gates.

### Deprecated

- **`bd init --force`** — deprecated alias for `--reinit-local`. Continues to work for ≥2 releases, emits a `DeprecationWarning`, routes internally to `--reinit-local`. Use `--reinit-local` going forward. CI and scripts are unaffected for the duration of the deprecation window.

### Fixed

- **`bd close <id>` in contributor auto-routing mode** — `bd close` now resolves IDs through the same routing fallback as `bd show` and `bd update`. Previously `bd close` errored with `no issue found matching <id>` in workspaces with `routing.mode=auto`, `routing.contributor=<path>`, and `beads.role=contributor` — even for IDs that `bd create` had just returned, because the issue lived in the routed planning store while `bd close` only checked the local primary. Bulk close (`bd close <id1> <id2> ...`) is also fixed by sharing one routed-store handle across the close batch. Fixes [#3608](https://github.com/gastownhall/beads/issues/3608).
- **Dolt-in-git hook recursion** — internal pushes of `refs/dolt/data` skip git hooks, preventing sync loops. Fixes [#3724](https://github.com/gastownhall/beads/issues/3724).
- **Hook setup and migration safety** — generated hooks no longer drop sibling commands, stale Claude hooks are warned about, and post-pull/checkout auto-import keeps committed `.beads/issues.jsonl` state fresh.
- **Dependency display and routing** — dependency trees show blocked state only for genuine blockers, tolerate unresolved IDs in batch output, and include dependency type information.
- **Export filtering** — closed memories and ephemeral wisps are excluded from default exports, and NoHistory beads survive explicit filters.
- **Graph/list regressions** — graph HTML emits empty arrays instead of `null`, merges disconnected components for `--all --html`, and list output avoids duplicate issues with multiple blockers.
- **Configuration documentation and validation drift** — stale TOML/`BEADS_*` references were corrected to YAML/`BD_*`, and secret keys are refused in git-tracked config.
- **Release preflight and packaging** — release formula adoption, script selection, pure-Go test helpers, Windows install cleanup, and generated CLI reference paths were hardened for the v1.0.4 release attempt.

## [1.0.3] - 2026-04-24

### Fixed

- **`go install ...@latest` restored** — released a tag whose `go.mod` no longer contains local `replace` directives, fixing install failures from v1.0.1 and v1.0.2. Fixes [#3338](https://github.com/gastownhall/beads/issues/3338) and [#3312](https://github.com/gastownhall/beads/issues/3312).
- **npm macOS archive layout** — macOS release archives are flat, matching the path expected by the npm postinstall script. Fixes [#3388](https://github.com/gastownhall/beads/issues/3388).
- **Windows install cleanup** — `make install` on Windows removes stale extensionless `bd` files before installing `bd.exe`, avoiding PATH shadowing by older installs. Fixes [#3399](https://github.com/gastownhall/beads/issues/3399).
- **Windows test portability** — fixed tests that hardcoded POSIX path or permission behavior. Fixes [#3404](https://github.com/gastownhall/beads/issues/3404).
- **Embedded pre-commit hook cwd** — pre-commit export no longer prints "no beads database found" for embedded-mode repositories. Fixes [#3454](https://github.com/gastownhall/beads/issues/3454).

## [1.0.2] - 2026-04-15

### Fixed

- **npm publish: provenance validation** — `npm-package/package.json` `repository.url`, `bugs.url`, and `homepage` updated from `steveyegge/beads` to `gastownhall/beads` so that npm's sigstore provenance validator accepts the published artifact. The repo move in v1.0.0 left these URLs stale, which caused npm publishes for v1.0.0 and v1.0.1 to be rejected after provenance check (E422).

## [1.0.1] - 2026-04-15

### Added

- **`bd batch`** — Atomic multi-operation transactions across create/update/close/dep. All ops commit together or none do. ([PR #3165](https://github.com/gastownhall/beads/pull/3165))
- **`bd config drift` / `bd config apply`** — Detect and reconcile drift between yaml, git, and database config surfaces. ([PR #3086](https://github.com/gastownhall/beads/pull/3086))
- **`bd config show`** — Unified provenance view showing where each effective config value comes from. ([PR #3084](https://github.com/gastownhall/beads/pull/3084), [bd-934](https://github.com/gastownhall/beads/issues/934))
- **`started_at` on issues** — Timestamp recording when an issue first entered `in_progress`. ([PR #3206](https://github.com/gastownhall/beads/pull/3206), [GH#2796](https://github.com/gastownhall/beads/issues/2796))
- **`beads_global` shared-server database** — Dedicated shared-server database for cross-project state. ([`fa87dd2b`](https://github.com/gastownhall/beads/commit/fa87dd2b))
- **Pool metrics telemetry** — Shared-server connection pool metrics for diagnosing pool exhaustion. ([PR #3172](https://github.com/gastownhall/beads/pull/3172), [GH#3140](https://github.com/gastownhall/beads/issues/3140))
- **`OpenBestAvailable` public API** — Library consumers can open the best available backend without hard-coding selection. ([PRs #3143, #3149](https://github.com/gastownhall/beads/pull/3149))
- **Selective sync: `--issues` flag and `push`/`pull` subcommands** — Tracker sync accepts explicit issue ID lists; `--parent` ported across trackers. ([PR #2975](https://github.com/gastownhall/beads/pull/2975))
- **`--label` / `--label-any` for `bd orphans`** — Filter orphan detection by label. ([PR #3026](https://github.com/gastownhall/beads/pull/3026))
- **Cross-version smoke tests** — CI gate for upgrade compatibility across prior releases. ([GH#2968](https://github.com/gastownhall/beads/issues/2968))
- **Migration test harness** — Cross-era upgrade fidelity tests. ([PR #3067](https://github.com/gastownhall/beads/pull/3067))
- **Docusaurus versioning** — Site versioned per release; `llms-full.txt` aligned with snapshots. ([PR #3033](https://github.com/gastownhall/beads/pull/3033))
- **Azure Blob Storage remotes** — `az://` recognized as a valid Dolt remote URL scheme. ([PR #3101](https://github.com/gastownhall/beads/pull/3101))
- **Side-effect hints for `bd config set`/`unset`** — Shows what else changes when a config value is modified. ([PR #3089](https://github.com/gastownhall/beads/pull/3089))
- **Dynamic config-list discovery** — `bd config list` auto-discovers yaml keys and env vars. ([PR #3088](https://github.com/gastownhall/beads/pull/3088))
- **`BEADS_DOLT_READY_TIMEOUT`** — Override the 10s `waitForReady` timeout during `bd init --shared-server` for slower hardware. ([PR #3188](https://github.com/gastownhall/beads/pull/3188), [GH#3142](https://github.com/gastownhall/beads/issues/3142))

### Changed

- **Auto-export enabled by default** — `export.auto` defaults to `true` and the default `export.path` is now `issues.jsonl` (previously `export.jsonl`), with `export.git-add` on by default. `bd init` prompts interactively (default: keep enabled); `--non-interactive` keeps it enabled. All `export.*` keys are listed in `bd config --help`. ([PR #3204](https://github.com/gastownhall/beads/pull/3204), [GH#2973](https://github.com/gastownhall/beads/issues/2973))
- **`gms_pure_go` by default** — Test and install helpers default to the pure-Go build tag, dropping ICU linkage from CI. ([PRs #3240, #3259](https://github.com/gastownhall/beads/pull/3259))
- **ICU runtime dependency removed from release binaries** — Release builds no longer link ICU, fixing portability issues. ([PR #3066](https://github.com/gastownhall/beads/pull/3066))
- **Dolt connection pool lifetime configurable** — Longer default, configurable via env. ([PR #3163](https://github.com/gastownhall/beads/pull/3163))
- **Dolt connection TLS explicitly disabled** — When TLS is not configured, connections now explicitly opt out rather than depending on driver defaults. ([PR #3107](https://github.com/gastownhall/beads/pull/3107))
- **Dolt log noise reduced** — `NewConnection` spam silenced; `dolt-server.log` rotates at startup to cap size. ([PRs #3160, #3161](https://github.com/gastownhall/beads/pull/3161))

### Fixed

- **Schema migration conflict** — Removed a conflicting column on the schema migration table; migration issue patched with test coverage. ([commits `f079786b`, `65dbdbf5`, `9aa9b82f`](https://github.com/gastownhall/beads/commits/main))
- **Wisp tables missing after bootstrap clone** — `bd bootstrap` now ensures wisp tables exist after cloning. ([commit `31d51232`](https://github.com/gastownhall/beads/commit/31d51232))
- **CI test binary timeout on macOS** — `bd` test binary memoized to avoid 10-minute timeout. ([PR #3273](https://github.com/gastownhall/beads/pull/3273))
- **Local-only state moved to Dolt-ignored tables** — Prevents local state from polluting shared history. ([commit `50f715b1`](https://github.com/gastownhall/beads/commit/50f715b1))
- **Worktree awareness across commands** — Hooks path, fingerprint, doctor checks, config validate, preflight, reset, bootstrap path synthesis, `rename-prefix`, `countExistingIssues`, and formula search paths all resolve correctly when run inside a git worktree. ([PRs #3169, #3123, #3235](https://github.com/gastownhall/beads/pull/3169), plus numerous smaller fixes)
- **`bd bootstrap` hardening** — Commits `issue_prefix` to the config table; writes `metadata.json` and `config.yaml` after sync clone; uses parent workspace db name when local `.beads` missing; rejects empty `--database`; auto-start allowed for migration tests. ([PRs #3203, #3247, #3083, #3076](https://github.com/gastownhall/beads/pull/3247))
- **Remote URL validation hardened at config parse time** — Security hardening for tracker and backup remotes. ([PR #3210](https://github.com/gastownhall/beads/pull/3210))
- **`bd list` truncation hint in all output modes** — Truncation now indicated in JSON/compact/long formats. ([PR #3243](https://github.com/gastownhall/beads/pull/3243), [GH#3212](https://github.com/gastownhall/beads/issues/3212))
- **`bd list --watch` hierarchy consistency** — Parent/child ordering stable across watch refreshes. ([PR #3236](https://github.com/gastownhall/beads/pull/3236))
- **`bd update --defer` sets status=deferred** — Deferring an issue now transitions status correctly. ([PR #3241](https://github.com/gastownhall/beads/pull/3241), [GH#3233](https://github.com/gastownhall/beads/issues/3233))
- **`bd mol bond` transitive cycle detection** — Cycles through transitive dependencies now caught. ([PR #3111](https://github.com/gastownhall/beads/pull/3111), [GH#2719](https://github.com/gastownhall/beads/issues/2719))
- **`bd dep add/remove` allows cross-prefix targets** — No longer rejects dependencies that cross prefix boundaries. ([commit `7b02edda`](https://github.com/gastownhall/beads/commit/7b02edda))
- **`bd dolt pull` nil-pointer panic in embedded mode** — Embedded backend no longer panics on pull. ([PR #3148](https://github.com/gastownhall/beads/pull/3148))
- **Diverged-history guidance on auto-push failure** — Actionable recovery steps printed instead of raw error. ([PR #3138](https://github.com/gastownhall/beads/pull/3138))
- **`bd config` error message hints** — Suggest `bd config set` (not `bd config`) in error messages. ([PR #3141](https://github.com/gastownhall/beads/pull/3141))
- **JSONL export in pre-commit hook** — Atomic code+issues commits via pre-commit hook. ([PR #3121](https://github.com/gastownhall/beads/pull/3121))
- **Auto-export / auto-backup errors surfaced to stderr** — Silent failures now visible. ([PR #3122](https://github.com/gastownhall/beads/pull/3122))
- **`go install` on Windows** — Fixed ICU header dependency that broke `go install` on Windows. ([PR #3112](https://github.com/gastownhall/beads/pull/3112), [GH#3013](https://github.com/gastownhall/beads/issues/3013))
- **Stepping-stone migration paths removed** — Unsupported intermediate migration versions cleaned up. ([PR #3110](https://github.com/gastownhall/beads/pull/3110))
- **SQLite-era JSONL migration preserves dependencies and labels** — Migration from pre-Dolt exports no longer drops graph edges or labels. ([PR #3082](https://github.com/gastownhall/beads/pull/3082), [GH#3079](https://github.com/gastownhall/beads/issues/3079))
- **MCP workspace discovery detects Dolt-backed projects** — MCP server no longer misses Dolt-native repos. ([PR #3207](https://github.com/gastownhall/beads/pull/3207), [GH#2997](https://github.com/gastownhall/beads/issues/2997))
- **npm postinstall closes download streams** — Resource leak fixed. ([PR #3228](https://github.com/gastownhall/beads/pull/3228))
- **Husky hooks sanitized when copied** — Husky-managed hooks no longer corrupt beads-managed hook directory. ([PR #3208](https://github.com/gastownhall/beads/pull/3208), [GH#3132](https://github.com/gastownhall/beads/issues/3132))
- **GitLab issue link dependencies wired into sync pull** — Dependencies via GitLab links now import. ([PR #3202](https://github.com/gastownhall/beads/pull/3202), [GH#2645](https://github.com/gastownhall/beads/issues/2645))
- **`.beads/` FS_NOCOW_FL on btrfs** — Prevents btrfs kworker thrashing on Dolt files. ([PR #3162](https://github.com/gastownhall/beads/pull/3162))
- **Auto-commit config table writes** — `bd remember`, `bd forget`, and `bd config` commands now commit their writes. ([PR #3052](https://github.com/gastownhall/beads/pull/3052), [bd-g8p](https://github.com/gastownhall/beads/issues/g8p))
- **Shared-server CLI dir resolved from shared root** — Corrects path resolution for shared-server layouts. ([PR #3223](https://github.com/gastownhall/beads/pull/3223))
- **`.claude/` gitignore narrowed** — Blanket ignore replaced with specific patterns so project `.claude/` content is tracked. ([PR #3190](https://github.com/gastownhall/beads/pull/3190), [GH#3182](https://github.com/gastownhall/beads/issues/3182))
- **Release archives extracting into subdirectory** — Install handles nested archive layouts. ([PR #3167](https://github.com/gastownhall/beads/pull/3167))
- **`bd import` help text** — Removed references to nonexistent `-i` flag. ([PR #3200](https://github.com/gastownhall/beads/pull/3200))

### Docs

- **ADR-0001: multi-remote approach decision** ([PR #3209](https://github.com/gastownhall/beads/pull/3209))
- **ICU regex policy / build dependency guidance** ([GH-3126](https://github.com/gastownhall/beads/issues/3126))
- **Contributor protection policy for AI agents** ([PR #3151](https://github.com/gastownhall/beads/pull/3151))
- **Cross-era migration instructions** ([PR #3081](https://github.com/gastownhall/beads/pull/3081))
- **Unified quick-start (site + stub)** ([PR #3032](https://github.com/gastownhall/beads/pull/3032))
- **Hosted docs link** ([PR #3011](https://github.com/gastownhall/beads/pull/3011))

## [1.0.0] - 2026-04-02

### 1.0 — Stable Release

Beads 1.0 marks the transition from rapid iteration to production stability. The Dolt migration that began in v0.55.0 is complete: embedded Dolt is the default on all platforms, server lifecycle management is no longer required, and the critical reliability issues that affected the v0.55–v0.63 series have been resolved.

### Added

- **Embedded Dolt default on macOS** — Native CGO builds on macOS enable embedded Dolt by default, eliminating the external Dolt server entirely. ([PR #2971](https://github.com/gastownhall/beads/pull/2971))
- **Custom status/type normalized tables** — `custom_statuses(name, category)` and `custom_types(name)` tables replace comma-separated config strings. Views use table-backed subqueries instead of dynamic IN clauses. Migration is automatic and idempotent. ([PR #2961](https://github.com/gastownhall/beads/pull/2961))
- **`bd rules audit` and `bd rules compact`** — Scan `.claude/rules/*.md` for contradictions and near-duplicates; merge rule groups with deduplication. ([PR #2810](https://github.com/gastownhall/beads/pull/2810))
- **`bd config set-many`** — Batch config operations across yaml, git, and database keys in a single command. ([PR #2943](https://github.com/gastownhall/beads/pull/2943))
- **Credentials file support** — `~/.config/beads/credentials` for Dolt server passwords with Unix permission checks, env var override, and proper DSN escaping. ([PR #2854](https://github.com/gastownhall/beads/pull/2854))
- **Spike, story, and milestone issue types** — First-class support with constants, validation, and normalization aliases. ([PR #2923](https://github.com/gastownhall/beads/pull/2923))
- **GitLab sync: epic→milestone, task hierarchy, type filtering** — Epics sync as GitLab milestones, tasks create work items via GraphQL with parent hierarchy, internal types excluded from push by default. ([PR #2889](https://github.com/gastownhall/beads/pull/2889))
- **ADO shorthand refs** — `ado:NNNNN` recognized in IsExternalRef and ExtractIdentifier, preventing duplicates on sync. ([PR #2966](https://github.com/gastownhall/beads/pull/2966))
- **ADO push filters** — `--types`, `--states`, and `--no-create` flags now apply to push operations. ([PR #2944](https://github.com/gastownhall/beads/pull/2944))
- **Public format package** — `format.PrettyIssue`, `CompactIssue`, `LongIssue` for external tooling. ([PR #2871](https://github.com/gastownhall/beads/pull/2871))
- **SlotSet/SlotGet/SlotClear** — Per-issue metadata slots in the Storage interface for delegation tracking and hook state. ([PR #2870](https://github.com/gastownhall/beads/pull/2870))
- **WispFilter export** — `beads.WispFilter` type alias exported from root package. ([PR #2868](https://github.com/gastownhall/beads/pull/2868))
- **`--non-interactive` / `--yes` for bootstrap** — Skip confirmation prompts in CI/automation. ([PR #2942](https://github.com/gastownhall/beads/pull/2942))
- **Batch dependency list** — `GetDependenciesWithMetadata` supports multiple issue IDs. ([PR #2875](https://github.com/gastownhall/beads/pull/2875))
- **`bd ready --explain`** — Dependency-aware reasoning for why issues are or aren't ready. ([PR #2814](https://github.com/gastownhall/beads/pull/2814))
- **Graph integrity enforcement** — Extended cycle detection and `bd graph check` command. ([PR #2814](https://github.com/gastownhall/beads/pull/2814))
- **Tracker hardening** — Retry logic with jitter for all tracker HTTP clients, pagination guards for Linear/Jira, response size limits, terminal-safe content sanitization. ([PRs #2814](https://github.com/gastownhall/beads/pull/2814))
- **Release stability gate** — Upgrade safety checks and breaking-change communication. ([GH#2951](https://github.com/gastownhall/beads/issues/2951))
- **Hook firing in storage layer** — `HookFiringStore` decorator moves hook logic out of CLI layer. ([PR #2891](https://github.com/gastownhall/beads/pull/2891))

### Changed

- **`bd setup claude` defaults to project-local** — Settings written to `.claude/settings.json` (shared/team) by default; `--global` for user-level. Auto-runs during `bd init`. ([PR #2972](https://github.com/gastownhall/beads/pull/2972))
- **`strconv.ParseBool` for env vars** — `IsAutoStartDisabled` uses standard boolean parsing with whitespace trimming. ([PRs #2955, #2956](https://github.com/gastownhall/beads/pull/2956))
- **Comma-separated `--status` filter** — `bd list --status open,in_progress` now works. ([GH#2846](https://github.com/gastownhall/beads/issues/2846))
- **Custom types from YAML** — `ResolveCustomTypesInTx` honors config.yaml custom types in create path. ([GH#2793](https://github.com/gastownhall/beads/issues/2793))
- **lipgloss v2, backoff v5, goldmark 1.8.2** — Dependency updates.

### Fixed

- **Concurrent embedded Dolt panic** — Exclusive flock acquired on embedded store open, held for store lifetime. Second concurrent opener gets a clear error instead of a nil-pointer panic. ([GH#2571](https://github.com/gastownhall/beads/issues/2571))
- **Spaces in repo path** — Embedded Dolt no longer URL-encodes local filesystem paths, fixing `%20` lookup failures on macOS. ([GH#2920](https://github.com/gastownhall/beads/issues/2920))
- **Schema version bump** — `currentSchemaVersion` bumped to 11 for custom_statuses/custom_types migration. Prevents "table not found" errors on upgrade. ([GH#2974](https://github.com/gastownhall/beads/issues/2974))
- **Dolt init failure** — Embedded-by-default eliminates server port 0 / connection refused issues for new users. ([GH#2656](https://github.com/gastownhall/beads/issues/2656))
- **Bootstrap for missing database** — `bd bootstrap` probes server for actual database existence before declaring no-op. Recovery guidance updated across all error surfaces. ([PR #2940](https://github.com/gastownhall/beads/pull/2940))
- **Diverged history recovery** — Dolt push/pull detect diverged history and print actionable recovery guidance. ([PR #2941](https://github.com/gastownhall/beads/pull/2941))
- **No-db command routing** — `bd context`, `bd dolt show`, etc. honor `--db` flag, `BEADS_DB` env, and external `dolt_data_dir` layouts. ([PR #2844](https://github.com/gastownhall/beads/pull/2844))
- **Auto-backup address conflict** — Detects when another backup remote owns the URL and syncs through it. ([PR #2927](https://github.com/gastownhall/beads/pull/2927))
- **Shared server project_id** — `bd init --database` on a shared server adopts existing `_project_id` instead of generating a new one. ([PR #2925](https://github.com/gastownhall/beads/pull/2925))
- **Init leaves beads.role unset** — Fixed edge cases where role was not persisted during init. ([GH#2950](https://github.com/gastownhall/beads/issues/2950))
- **`--shared-server` mode detection** — Correctly treated as server mode for DoltMode resolution. ([PR #2947](https://github.com/gastownhall/beads/pull/2947))
- **GitLab work_items URL dedup** — Pull dedup checks both issues and wisps tables; URL pattern matches `/work_items/` paths. ([PR #2889](https://github.com/gastownhall/beads/pull/2889))
- **GitLab work item type ID** — Dynamic GraphQL query with per-session cache instead of hardcoded `gid://...`. ([Follow-up to PR #2889](https://github.com/gastownhall/beads/pull/2889))
- **Stale noms LOCK files** — Cleaned after bootstrap clone. ([PR #2913](https://github.com/gastownhall/beads/pull/2913))
- **ADO WIQL date precision** — RFC3339 timezone formatting in WIQL queries. ([PR #2911](https://github.com/gastownhall/beads/pull/2911))
- **Blocked icon in `bd list`** — Dependency-blocked issues now show blocked indicator. ([GH#2858](https://github.com/gastownhall/beads/issues/2858))
- **Installer ICU/CGO fallback** — Correct handling on systems without ICU libraries. ([PR #2965](https://github.com/gastownhall/beads/pull/2965))

### Community

Contributors: harry-miller-trimble, coffeegoddd, quad341, Bella-Giraffety, maphew, rjc123, Fredkr, sammywachtel, anupamchugh, ktoulgaridis, outdoorsea, Geargrindadmin.

## [0.63.3] - 2026-03-30

### Added

- **Embedded mode by default** — New installations now use embedded Dolt as the default storage backend, eliminating the need to run a separate Dolt server process. Existing server-mode installations are unaffected. ([PR #2842](https://github.com/gastownhall/beads/pull/2842))
- **`bd create --graph`** — Batch issue creation from a YAML/JSON dependency graph, replacing the standalone `bd graph-apply` command. ([PR #2894](https://github.com/gastownhall/beads/pull/2894))
- **Multi-project/team sync** — Linear, Jira, and ADO integrations now support syncing multiple projects and teams in a single repo. ([PR #2868](https://github.com/gastownhall/beads/pull/2868))
- **Notion sync** — Reimplemented Notion integration on REST data sources with full page property mapping. ([PR #2843](https://github.com/gastownhall/beads/pull/2843))
- **GitLab sync** — Optional group-level GitLab issue sync. ([PR #2801](https://github.com/gastownhall/beads/pull/2801))
- **Linear `--parent` flag** — `bd linear sync --parent` limits sync push to a specific ticket tree. ([PR #2853](https://github.com/gastownhall/beads/pull/2853))
- **Remote URL support** — Multi-repo hydration now supports remote URLs. ([PR #2827](https://github.com/gastownhall/beads/pull/2827))
- **Top-level command aliases** — `bd comment`, `bd assign`, `bd tag`, `bd link`, `bd priority` as convenient shortcuts. ([GH#2611](https://github.com/gastownhall/beads/issues/2611))
- **Storage interface expansion** — `ListWisps`, `MergeSlot`, `ReopenIssue`, `UpdateIssueType`, replication, and VC interfaces now exposed in the public API. ([PRs #2782-#2788](https://github.com/gastownhall/beads/pull/2782))
- **SearchIssues dependency hydration** — Bulk dependency population for search results. ([PR #2773](https://github.com/gastownhall/beads/pull/2773))
- **`--agents-file` flag** — `bd init --agents-file` customizes the agent instructions filename. ([PR #2761](https://github.com/gastownhall/beads/pull/2761))
- **Multi-process concurrency tests** — Server lifecycle and concurrent access integration tests. ([PR #2767](https://github.com/gastownhall/beads/pull/2767))

### Changed

- **Backup system simplified** — Removed git-based backup/restore (`backup export-git`, `backup fetch-git`). Backup and restore now operate exclusively through Dolt's native replication. ([PR #2842](https://github.com/gastownhall/beads/pull/2842))
- **dolthub/driver v1.84.0** — Updated from v1.83.8, picking up serialization retry support.
- **Dolt write retry** — `writeRetryTx` automatically retries on Dolt serialization errors. ([PR #2855](https://github.com/gastownhall/beads/pull/2855))
- **CI overhaul** — Test sharding, precompiled binaries, shared Go module cache for faster CI runs.
- **Gas Town terminology removed** — Remaining Gas Town naming removed from beads orchestrator detection. ([PR #2762](https://github.com/gastownhall/beads/pull/2762))
- **TypeScript v6, backoff v5, goldmark 1.7.17** — Dependency updates.

### Fixed

- **Init git cache reset** — `bd init` now resets git caches after auto-init, preventing stale `.beads/dolt/` references in embedded mode. ([PR #2907](https://github.com/gastownhall/beads/pull/2907))
- **Doctor embedded mode** — Improved embedded mode detection and push error guidance. ([PR #2910](https://github.com/gastownhall/beads/pull/2910))
- **Create description warning** — Removed spurious description warning; test-issue warning now gated on DB size. ([PR #2908](https://github.com/gastownhall/beads/pull/2908))
- **External contributor PRs** — `block-gh-watch` hook now allows external contributors to create PRs. ([PR #2907](https://github.com/gastownhall/beads/pull/2907))
- **CWD `.beads/` priority** — `bd` now checks CWD for `.beads/` before git-worktree resolution, fixing worktree-based discovery in nested projects.
- **Epic closure eligibility** — Wisp children now correctly included in epic closure checks. ([PR #2843](https://github.com/gastownhall/beads/pull/2843))
- **Prefix routing** — `bd edit`, `bd reopen`, and `bd delete` now correctly route with prefixes from town root. ([PRs #2780, #2847](https://github.com/gastownhall/beads/pull/2847))
- **ADO/Jira custom type_map** — Custom type maps no longer silently ignored. ADO dependency links now imported correctly. ([PR #2771](https://github.com/gastownhall/beads/pull/2771))
- **Init safety guard** — `bd init --force` no longer bypasses close --quiet check. ([PR #2799](https://github.com/gastownhall/beads/pull/2799))
- **Circuit breaker TTL** — Stale circuit breaker files cleaned during init; files moved to subdirectory. ([PR #2799](https://github.com/gastownhall/beads/pull/2799))
- **`bd show --watch`** — Replaced fsnotify with polling to fix cross-platform compatibility. ([PR #2755](https://github.com/gastownhall/beads/pull/2755))
- **Config unset routing** — `config unset` now routes to correct store; backup keys shown in list. ([GH#2727](https://github.com/gastownhall/beads/issues/2727))
- **Doctor false errors** — Server health checks skipped when no Dolt server expected; bare worktree fallback fixed. ([GH#2722](https://github.com/gastownhall/beads/issues/2722))
- **UTC timestamps** — ADO/Jira defer_until comparisons use UTC_TIMESTAMP(). ([PR #2819](https://github.com/gastownhall/beads/pull/2819))
- **Cloud storage routing** — Push/pull routed through CLI when cloud storage env vars are set. ([GH#2820](https://github.com/gastownhall/beads/issues/2820))
- **Embedded schema** — Removed HOP columns (crystallizes, quality_score) from embedded schema. ([PR #2837](https://github.com/gastownhall/beads/pull/2837))
- **JSON path quoting** — Dotted metadata keys now properly quoted in JSON path expressions. ([PR #2865](https://github.com/gastownhall/beads/pull/2865))
- **Hook fire on update** — `bd update --status closed` now fires on_close hook; `bd close` fires on_update. ([PR #2754](https://github.com/gastownhall/beads/pull/2754))

### Embedded Dolt

- **46 commits** from the Dolt team (coffeegoddd) completing embedded Dolt support: bootstrap, compact, migrate, sync, backup, restore, maintenance, setup, config, and advanced commands. Includes serialization retry, driver bump to v1.84.0, CI test sharding, and `RunInTransaction` refactor for shared `issueops` operations.

### Community

Contributors: coffeegoddd (Dustin Brown), matt wilkie (maphew), harry-miller-trimble, quad341, ktoulgaridis, eitsupi, renezander030, REMvisual, aphexcx, anupamchugh, MovGP0, Bella-Giraffety, julianknutsen, osamu2001, toumorokoshi, fernanja, sammywachtel, outdoorsea.

## [0.62.0] - 2026-03-21

### Added

- **Custom status categories** — `bd config set status.custom "in_review:active,qa_testing:wip,archived:done,on_hold:frozen"` assigns categories to custom statuses that control behavior in `bd ready` (active statuses included) and `bd list` (done/frozen statuses excluded by default). Backward compatible — old flat format `"foo,bar"` still works with no behavior change. ([PR #2665](https://github.com/gastownhall/beads/pull/2665))
- **`bd statuses` command** — Lists all built-in and custom statuses with their icons, categories, and descriptions. Supports `--json` for programmatic use.
- **`bd note` command** — Shorthand for appending notes to issues without the `bd update --note` ceremony.
- **Audit logging** — Status, assignee, and priority changes are logged to `.beads/interactions.jsonl` with close reasons. Survives Dolt GC flatten, so you can recover context after aggressive cleanup.
- **`--exclude-type` flag** — `bd ready --exclude-type wisp` and `bd list --exclude-type wisp` filter issues by type.
- **`--agents-profile` flag** — `bd init --agents-profile minimal` controls the default AGENTS.md profile (full/minimal).
- **Memories in export/import** — `bd export`/`bd import` now round-trip agent memories.
- **Azure DevOps integration** — New `bd ado` commands for syncing work items with Azure DevOps (`bd ado sync`, `bd ado status`, `bd ado projects`).
- **MCP `ready` tool filter** — `issue_type` filter exposed on the MCP ready tool for agent use.
- **Quality/lifecycle commands in prime** — `bd prime`, templates, and `bd doctor` now surface quality and lifecycle commands.

### Changed

- **BEADS_ACTOR replaces BD_ACTOR** — `BEADS_ACTOR` is now the primary environment variable for agent identity; `BD_ACTOR` remains as a deprecated fallback.
- **ServerMode enum** — Dolt server ownership inference consolidated into a clean `ServerMode` enum (Auto, External, Shared, Embedded), replacing ad-hoc string checks.
- **dolthub/driver v1.83.8** — Updated from v0.2.1, picking up Dolt engine improvements and bug fixes.
- **anthropic-sdk-go v1.27.1** — Updated from v1.27.0.
- **Internal cleanup** — Removed Gas Town-specific concepts from beads types and code: GUPP references, polecat/crew/overseer terminology, HOP fields, agent-as-bead subsystem, patrol molecule references, role templates, hardcoded `~/gt/` paths. Beads is now fully standalone.

### Fixed

- **Windows Dolt server lifecycle** — `ErrServerNotRunning` sentinel error prevents false "failed to stop" warnings; stale PID/port files cleaned up even when server is already dead. ([GH#2670](https://github.com/gastownhall/beads/issues/2670))
- **Hook preservation** — `bd init` now preserves ALL pre-existing hooks (not just beads-managed ones) when overriding `core.hooksPath`. ([PR #2738](https://github.com/gastownhall/beads/pull/2738))
- **Shim timeout** — Hook shim timeout increased from 30s to 300s, preventing premature termination of chained pre-commit pipelines (linters, formatters, type-checkers). Configurable via `BEADS_HOOK_TIMEOUT`. ([GH#2732](https://github.com/gastownhall/beads/issues/2732))
- **GetNextChildIDTx query** — Replaced broken `CAST(SUBSTRING_INDEX(...) AS UNSIGNED)` with Go-side `ParseHierarchicalID`, fixing child ID reconciliation for non-numeric IDs after JSONL imports. ([GH#2721](https://github.com/gastownhall/beads/issues/2721))
- **Credential key in .gitignore** — `.beads-credential-key` added to project-root `.gitignore` template as defense-in-depth for the AES-256 encryption key. ([GH#2695](https://github.com/gastownhall/beads/issues/2695))
- **SQL stored procedures for flatten/compact** — `bd flatten` and `bd compact` now use SQL stored procedures in server mode, fixing failures on shared Dolt servers.
- **`--format json` collision** — Resolved flag collision between `rootCmd` persistent `--format` flag and `list`/`dep-tree` local `--json` flags.
- **Concurrent schema init** — `initSchemaOnDB` serialized with `GET_LOCK` to prevent Dolt journal corruption in multi-agent environments. ([GH#2672](https://github.com/gastownhall/beads/issues/2672))
- **External Dolt server safety** — `KillStaleServers` respects `IsAutoStartDisabled` and server ownership, preventing accidental kills of externally-managed servers. ([GH#2641](https://github.com/gastownhall/beads/issues/2641))
- **Doctor infinite restart loop** — Doctor no longer triggers infinite Dolt server restart cycles. ([GH#2636](https://github.com/gastownhall/beads/issues/2636))
- **Schema version 8** — Dolt schema bumped to version 8 with proper commit of version update. ([PR #2635](https://github.com/gastownhall/beads/pull/2635))
- **Repo-local servers** — Auto-started Dolt servers kept alive across sequential `bd` commands within the same repo.
- **Store config preservation** — Reopening stores by path no longer drops repo-specific config. ([PR #2628](https://github.com/gastownhall/beads/pull/2628))
- **Validation on close** — `--validate` now checks `--acceptance` field; `validation.on-close` config controls enforcement. ([PR #2654](https://github.com/gastownhall/beads/pull/2654))
- **`.beads/.env` load order** — Environment file loaded before `noDbCommands` early return, fixing env-dependent non-DB commands. ([GH#2677](https://github.com/gastownhall/beads/issues/2677))
- **Update description safety** — Empty stdin/file no longer silently erases descriptions; requires explicit opt-in.
- **Bootstrap JSONL imports** — Correctly imports from git-tracked `.beads/issues.jsonl`.
- **Doctor fail-closed** — Server-mode integrity recovery now fails closed instead of silently passing.

### Embedded Dolt

- **73 commits** from the Dolt team (coffeegoddd) advancing embedded Dolt support: create, list, update, close, show, delete, search, query, label, gate, promote, move, merge-slot, quick, diff, count, find-duplicates, graph, and views/reports commands now work in embedded mode. Includes shared `issueops` package extraction for transaction-based operations used by both server and embedded backends.

### Community

Contributors: coffeegoddd (Dustin Brown), matt wilkie (maphew), harry-miller-trimble, gzur, Algorune, sfncore, angelamayxie, paf0186, Patrick Farrell, Tim Visher.

## [0.61.0] - 2026-03-15

### Added

- **`--no-history` flag** — `bd create --no-history` stores beads in the wisps table (skipping Dolt commits) without making them GC-eligible, fixing agent identity beads being incorrectly garbage collected. `bd update --no-history`/`--history` toggles the flag. ([GH#2619](https://github.com/gastownhall/beads/issues/2619))
- **`--claim-next` flag** — `bd close --claim-next` auto-claims the next highest priority available issue after closing
- **`--skills` and `--context` flags** — `bd create --skills`/`--context` add structured sections to issue descriptions
- **`--exclude-type` for wisp GC** — `bd mol wisp gc --exclude-type agent,rig` protects specific types from garbage collection
- **`--type` filter for wisp list** — `bd mol wisp list --type bug` filters by issue type
- **Auto-detect beads database on git origin** — `bd bootstrap` probes origin for `refs/dolt/data` and auto-clones the database; `bd init` warns when existing data is detected ([GH#2580](https://github.com/gastownhall/beads/issues/2580))
- **Install checksum verification** — install scripts verify SHA256 checksums before extraction ([GH#1857](https://github.com/gastownhall/beads/issues/1857))
- **Jira `pull_jql` config** — `jira.pull_jql` config key or `JIRA_PULL_JQL` env var for custom JQL filters during issue fetch
- **UUID primary keys** — events, comments, and snapshot tables migrated from AUTO_INCREMENT to UUID() primary keys for federation-safe multi-clone operation

### Changed

- **Prime SSOT** — `bd prime` is now the single source of truth for agent instructions. Agent files use versioned hash-based markers with profile support (full/minimal) for staleness detection and automatic upgrades. Symlink safety and profile precedence (full preserved over minimal) ([GH#2139](https://github.com/gastownhall/beads/issues/2139))
- **Formula extends merges by ID** — child steps with matching IDs override parent steps in-place instead of appending duplicates
- **Circuit breaker keyed on host:port** — prevents cross-host blocking when servers share a port
- **Backup scoped by prefix** — backup export and restore filter by current project prefix, preventing cross-project data leakage on shared servers

### Fixed

- **Embedded Dolt CI** — clean stale noms LOCK files before opening embedded store (fixes TestEmbeddedInitConcurrent panic), update stale from_jsonl test, gofmt import.go
- **Circuit breaker port-0 poisoning** — returns nil breaker for unresolved port 0 during standalone auto-start
- **KillStaleServers scoped to repo** — only kills orphan Dolt processes belonging to the current repo's data directory
- **Port resolution chain** — `applyConfigDefaults` consults port file > config.yaml > metadata.json instead of falling through to default 3307
- **Doctor fingerprint checks** — `bd doctor /path/to/repo` computes fingerprints from the target path, not cwd
- **Doctor auto-start on cold standalone** — doctor checks now use CLI auto-start policy instead of suppressing on explicit ports
- **Doctor server-backed runtime detection** — layered detection for server-backed runtime on older metadata.json without `dolt_mode`
- **Credential key location** — moved from `.beads/dolt/` to `.beads/` to avoid creating ghost directories in shared-server mode
- **Colons in query values** — unquoted identifiers like `label=gt:merge-request` now work without quoting
- **Init localhost warning** — warns when server host defaults to localhost for remote Dolt setups
- **Repo-local Dolt server preservation** — stale cleanup preserves servers from other repos
- **Backup restore UUID IDs** — accepts UUID-format comment and event IDs during restore

## [0.60.0] - 2026-03-12

### Added

- **`bd bootstrap` with recovery actions** — `bd bootstrap` now executes recovery actions directly instead of just printing advice, making first-run and repair workflows hands-free ([GH#2438](https://github.com/gastownhall/beads/issues/2438), [GH#2372](https://github.com/gastownhall/beads/issues/2372))
- **`bd context` command** — new command for safe-first error guidance, surfacing relevant context when things go wrong (fix-merge [PR #2496](https://github.com/gastownhall/beads/pull/2496))
- **`bd help --list` and `bd help --doc`** — CLI doc generation flags for producing machine-readable command listings and full documentation ([GH#2527](https://github.com/gastownhall/beads/issues/2527))
- **`bd done` alias** — `bd done` is now an alias for `bd close`, aligning with Gas Town's session lifecycle vocabulary
- **`bd done <id> <message>`** — last argument is treated as the close reason, removing the need for `--comment`
- **`--design-file` flag** — read design documents from files instead of stdin for `bd create` workflows ([PR #2524](https://github.com/gastownhall/beads/pull/2524))
- **`--destroy-token` for safe re-init** — enables non-interactive `bd init` re-initialization with explicit confirmation token ([GH#2497](https://github.com/gastownhall/beads/issues/2497))
- **GitHub Issues integration** — tracker plugin for syncing GitHub Issues with beads ([GH#2373](https://github.com/gastownhall/beads/issues/2373))
- **Global PRIME.md fallback** — `~/.config/beads/PRIME.md` is used when no project-level PRIME.md exists ([GH#2330](https://github.com/gastownhall/beads/issues/2330))
- **Shared Dolt server mode** — multi-repo and multi-agent setups can share a single Dolt server instance ([GH#2416](https://github.com/gastownhall/beads/issues/2416))
- **Ephemeral port allocation for Dolt** — replaces hash-derived ports with OS-assigned ephemeral ports, eliminating port collisions entirely ([GH#2415](https://github.com/gastownhall/beads/issues/2415))
- **Workspace identity preflight** — `bd bootstrap` validates workspace identity before any operations ([GH#2438](https://github.com/gastownhall/beads/issues/2438))
- **Epic close guards** — prevents accidental closure of epics with open children; includes merge re-parenting and progress display
- **`bd search` searches `external_ref`** — external references (GitHub issue URLs, Linear IDs, etc.) are now searchable ([GH#2494](https://github.com/gastownhall/beads/issues/2494))
- **JSON-aware error output** — errors include structured JSON when `--json` is active, with JSONL schema validation and contract tests ([GH#2499](https://github.com/gastownhall/beads/issues/2499))
- **`ai.api_key` config** — fallback for `ANTHROPIC_API_KEY` environment variable, stored in `config.yaml`
- **Embedded Dolt storage interfaces** — new storage abstraction layer contributed by DoltHub (coffeegoddd) for future embedded Dolt work ([PR #2427](https://github.com/gastownhall/beads/pull/2427), [PR #2428](https://github.com/gastownhall/beads/pull/2428))
- **Community tools** — added beads-web and claude-protocol to COMMUNITY_TOOLS.md; added community forks listing ([PR #2457](https://github.com/gastownhall/beads/pull/2457))

### Changed

- **Dolt port allocation** — switched from deterministic hash-derived ports to OS-assigned ephemeral ports; stored in repo-local state file rather than derived at runtime ([GH#2415](https://github.com/gastownhall/beads/issues/2415))
- **Auto-push state moved to local file** — prevents metadata merge conflicts during `bd dolt pull` by keeping auto-push state out of the shared metadata ([GH#2466](https://github.com/gastownhall/beads/issues/2466))
- **YAML config prefix precedence** — YAML config prefix now takes precedence over DB-stored prefix in shared-server mode ([GH#2469](https://github.com/gastownhall/beads/issues/2469))
- **Charm library upgrades** — migrated glamour to v2 (`charm.land/glamour/v2`) and huh to v2 (`charm.land/huh/v2`) ([GH#2471](https://github.com/gastownhall/beads/issues/2471))
- **Daemon infrastructure fully removed** — final cleanup of all remaining daemon references and idle monitor infrastructure ([GH#2431](https://github.com/gastownhall/beads/issues/2431), [GH#2452](https://github.com/gastownhall/beads/issues/2452))
- **Dead sync mode scaffolding removed** — removed remaining sync mode code that was left behind after the Dolt migration ([GH#2485](https://github.com/gastownhall/beads/issues/2485))
- **3-way merge engine remnants removed** — cleaned up leftover code from the removed 3-way merge system ([GH#2391](https://github.com/gastownhall/beads/issues/2391))
- **Nix flake** — uses `go mod edit` instead of `sed` for version patching (more reliable)

### Fixed

- **Dolt journal corruption** — `KillStaleServers` now runs inside `flock` to prevent concurrent server kills from corrupting the Dolt journal ([GH#2430](https://github.com/gastownhall/beads/issues/2430))
- **Dolt config corruption** — uses explicit `DOLT_ADD` to prevent config corruption from stale working set state ([GH#2455](https://github.com/gastownhall/beads/issues/2455))
- **Dolt pull merge errors** — auto-commits pending changes before pull to prevent merge errors ([GH#2474](https://github.com/gastownhall/beads/issues/2474))
- **Dolt pull transaction safety** — wraps `DOLT_PULL` in explicit transaction for autocommit compatibility ([GH#2501](https://github.com/gastownhall/beads/issues/2501))
- **Dolt remote directory** — `bd dolt remote add/list/remove` now operate on the correct directory ([GH#2306](https://github.com/gastownhall/beads/issues/2306), [GH#2311](https://github.com/gastownhall/beads/issues/2311))
- **Dolt endpoint drift** — warns when auto-started server endpoint doesn't match config ([GH#2399](https://github.com/gastownhall/beads/issues/2399))
- **CLI remotes synced into SQL server** — CLI-managed remotes are now synced into the SQL server on store open ([GH#2315](https://github.com/gastownhall/beads/issues/2315))
- **Server mode `.beads/` creation** — correctly creates `.beads/` directory when using external `BEADS_DOLT_*` env vars ([GH#2519](https://github.com/gastownhall/beads/issues/2519))
- **Metadata merge conflicts** — auto-resolves metadata merge conflicts during `bd dolt pull` ([GH#2466](https://github.com/gastownhall/beads/issues/2466))
- **Metadata replacement bug** — `--metadata` now merges with existing metadata instead of replacing it ([GH#2423](https://github.com/gastownhall/beads/issues/2423))
- **Backup file detection** — detects backup files on database-not-found and after `bd init` ([GH#2327](https://github.com/gastownhall/beads/issues/2327))
- **`bd export` crash** — fixed crash on zero-time timestamps and stale `bd sync` refs ([GH#2488](https://github.com/gastownhall/beads/issues/2488), [GH#2493](https://github.com/gastownhall/beads/issues/2493))
- **`bd children`** — now includes closed children by default ([GH#2481](https://github.com/gastownhall/beads/issues/2481))
- **`bd doctor --fix`** — regenerates missing `metadata.json` ([GH#2482](https://github.com/gastownhall/beads/issues/2482)); backfills `project_id` for pre-GH#2372 Dolt stores ([GH#2490](https://github.com/gastownhall/beads/issues/2490)); blocked at Gas Town town root to prevent accidental damage ([GH#2450](https://github.com/gastownhall/beads/issues/2450))
- **`bd doctor`** — detects stale `.legacy` hook sidecars ([GH#2398](https://github.com/gastownhall/beads/issues/2398)); `--clean --json` now works correctly ([GH#2438](https://github.com/gastownhall/beads/issues/2438))
- **`bd lint`** — accepts non-empty `acceptance_criteria` field without heading ([GH#2468](https://github.com/gastownhall/beads/issues/2468))
- **`bd list --json`** — no longer ignored when `--tree` defaults to true (three separate fixes consolidated)
- **`bd mol wisp`** — reports correct count when `RootOnly=true`
- **`bd init`** — prevented from creating DB on another project's Dolt server ([GH#2336](https://github.com/gastownhall/beads/issues/2336)); init guard allows fresh clones with committed `metadata.json` ([GH#2433](https://github.com/gastownhall/beads/issues/2433)); `data-dir` blocked in server mode ([GH#2438](https://github.com/gastownhall/beads/issues/2438))
- **Init guard error message** — removed dangerous `--force` suggestion
- **JSONL backups** — preserve dependency metadata ([GH#2487](https://github.com/gastownhall/beads/issues/2487)); restore denormalized JSONL without data loss ([GH#2479](https://github.com/gastownhall/beads/issues/2479))
- **Reparented children** — excluded from molecule auto-close ([GH#2480](https://github.com/gastownhall/beads/issues/2480))
- **Worktree hooks** — use absolute path for `core.hooksPath` so hooks work in worktrees ([GH#2414](https://github.com/gastownhall/beads/issues/2414))
- **Worktree `.gitignore`** — skip append when parent directory pattern already covers the entry
- **Git hook shims** — added timeout and graceful DB-missing handling
- **Stale lock files** — follow redirect target when cleaning stale lock files
- **BEADS_DIR** — stabilized paths from detached commit worktrees; prefer stable branch worktrees
- **`BEADS_DOLT_PORT` fallback** — added fallback in `GetDoltServerPort()` ([GH#2486](https://github.com/gastownhall/beads/issues/2486))
- **PersistentPreRun flag checks** — use `Root().PersistentFlags()` for all persistent flag checks ([GH#2514](https://github.com/gastownhall/beads/issues/2514))
- **Stale pinned flag** — auto-cleared on status transition
- **Install script** — detects WSL and MINGW/MSYS/CYGWIN environments ([PR #2409](https://github.com/gastownhall/beads/pull/2409))
- **Test stability** — Dolt test suite eliminates container crashes and cascading failures; timezone-aware date assertions; same-type issues for blocks dependency tests

### Performance

- **Close-time template subgraph loading** — significantly faster epic close operations ([GH#2458](https://github.com/gastownhall/beads/issues/2458))

### Documentation

- **Stale sync references purged** — comprehensive cleanup of `bd sync`, `bd import`, `--branch` refs across CLI help, setup generators, and docs ([GH#2522](https://github.com/gastownhall/beads/issues/2522), [GH#2435](https://github.com/gastownhall/beads/issues/2435), [GH#2442](https://github.com/gastownhall/beads/issues/2442))
- **Stale migration ref fixed** — `bd migrate --to-dolt` references replaced with `bd init --from-jsonl` ([GH#2333](https://github.com/gastownhall/beads/issues/2333))
- **QUICKSTART.md** — removed stale `--branch` flag and SQLite references ([GH#2522](https://github.com/gastownhall/beads/issues/2522))
- **CI doc validation** — added CI check to catch stale documentation references
- **Community tools** — added beads-web and claude-protocol

### Dependencies

- `github.com/dolthub/driver` updated (multiple digests)
- `golang.org/x/sync` v0.20.0
- `golang.org/x/sys` v0.42.0
- `golang.org/x/term` v0.41.0
- OpenTelemetry Go monorepo v1.42.0
- Testcontainers Go monorepo v0.41.0
- Charm glamour v1.0.0 → v2.0.0
- Charm huh v0.8 → v2.0.0

## [0.59.0] - 2026-03-05

### Added

- **`bd list --tree` default** — tree view is now the default display mode for `bd list` ([GH#2345](https://github.com/gastownhall/beads/issues/2345))
- **OpenCode recipe** — `bd setup` now includes an OpenCode integration recipe
- **Fresh clone detection** — `bd doctor` detects fresh clones on Dolt server and guides through init ([GH#2372](https://github.com/gastownhall/beads/issues/2372))
- **Config-aware init guard** — `bd init` distinguishes server-reachable from DB-exists, with better error messages ([GH#2372](https://github.com/gastownhall/beads/issues/2372))
- **Sync remote hint in errors** — database-not-found errors now surface `sync.git-remote` config for easier troubleshooting
- **Nix flake modernization** — updated flake for nixpkgs-25.11, fix-merge of PR #2314
- **Doctor warning suppression** — suppress specific `bd doctor` warnings via config ([GH#1095](https://github.com/gastownhall/beads/issues/1095))
- **Community Tools** — added fancypantalons/nvim-beads to COMMUNITY_TOOLS.md

### Changed

- **3-way merge config removed** — `ConflictStrategy`, `FieldStrategy`, and all associated config/validation removed; Dolt handles merge natively ([GH#2353](https://github.com/gastownhall/beads/pull/2353))
- **Daemon infrastructure removed** — the bd daemon has been fully removed; bd is now purely CLI-driven ([w-bd-001](https://github.com/gastownhall/beads/issues/w-bd-001))
- **Legacy daemon lock infrastructure removed** — cleaned up stale lock files and related code
- **Backup git-push defaults to OFF** — `bd backup` git push now requires explicit opt-in ([GH#2363](https://github.com/gastownhall/beads/issues/2363))
- **Test infrastructure modernized** — migrated all tests from `StartTestDoltServer` to container-native API; readiness expanded to 5-state enum; crash detection via standalone functions ([GH#2304](https://github.com/gastownhall/beads/pull/2304))
- **`ExtractPrefix` consolidated** — moved to `types` package for reuse across codebase

### Fixed

- **Hook marker handling** — `bd doctor --fix` repairs broken/orphaned hook markers; version added to END marker for future matching ([GH#2344](https://github.com/gastownhall/beads/issues/2344))
- **Legacy hook migration** — warn on user-modified legacy hooks; handle backup sidecar files during migration
- **Dolt push/pull directory** — use correct database subdirectory for CLI push/pull/fetch operations
- **Contributor auto-routing** — add fallback routing for show/update/close commands ([GH#2345](https://github.com/gastownhall/beads/issues/2345))
- **Config-aware prefix detection** — `ResolvePartialID` now respects config for prefix routing
- **Doctor command order** — correct `enrichFreshClone` command sequencing
- **Batch IN-clause queries** — prevent full table scans in all IN-clause queries ([GH#2294](https://github.com/gastownhall/beads/issues/2294))
- **Init port resolution** — use `DefaultConfig` for port resolution during init ([GH#2372](https://github.com/gastownhall/beads/issues/2372))
- **Idle monitor** — single-instance lock, port config isolation, project UUID verification ([GH#2367](https://github.com/gastownhall/beads/issues/2367), [GH#2372](https://github.com/gastownhall/beads/issues/2372))
- **Init data safety** — prevent data destruction from misleading error messages ([GH#2363](https://github.com/gastownhall/beads/issues/2363), [GH#2372](https://github.com/gastownhall/beads/issues/2372))
- **Circuit breaker tuning** — reduce cooldown to 5s and add active TCP health probe
- **Deterministic ordering** — add ID tiebreaker to all ORDER BY clauses
- **Backup on feature branches** — skip git commits during hook runs and on feature branches
- **Backup config routing** — route `backup.*` keys to `config.yaml` so overrides are respected
- **Embedded Dolt** — handle context canceled in cleanup, add SQL concurrency test
- **Tombstone entries** — skip tombstone entries in `bd init --from-jsonl`
- **DerivePort** — restore as standalone default in `DefaultConfig`; log fallback on collision
- **Cross-type blocking** — align validation with cross-type blocking rules
- **Cross-prefix dep routing** — extend to dep commands ([GH#2296](https://github.com/gastownhall/beads/pull/2296))
- **Dolt serialization retry** — retry on serialization error in concurrent tests
- **CLI routing** — gate on local remote availability; extend SQL push timeout ([GH#2295](https://github.com/gastownhall/beads/pull/2295))
- **Docker Hub network calls** — skip when Dolt image not cached ([GH#2277](https://github.com/gastownhall/beads/issues/2277))
- **Wisp batch deletes** — commit per batch in `deleteWispBatch` to avoid write timeout
- **Idle monitor watchdog** — kills itself via `Stop()` to prevent zombie processes

### Documentation

- Audit and clean up stale sync mode documentation ([w-bd-004](https://github.com/gastownhall/beads/issues/w-bd-004))
- Network/privacy section added to SECURITY.md
- DerivePort birthday-problem collision recovery documented
- Misleading examples corrected in QUICKSTART.md
- GIT_INTEGRATION.md updated re deprecated beads-merge and Jujutsu
- Circuit breaker and upgrade recovery troubleshooting docs
- Duplicate nvim-beads entry fixed in COMMUNITY_TOOLS.md

## [0.58.0] - 2026-03-02

### Added

- **Persistent agent memory** — `bd remember`, `bd memories`, `bd recall`, `bd forget` for knowledge that survives sessions and account rotations. Backed by the k/v store, auto-injected at `bd prime` time.
- **`bd purge`** — delete closed ephemeral beads (wisps) to reclaim storage ([GH#1692](https://github.com/gastownhall/beads/issues/1692))
- **`bd mol last-activity`** — show the most recent activity timestamp for any molecule ([GH#1456](https://github.com/gastownhall/beads/issues/1456))
- **`bd show --current`** — show the active issue without specifying an ID ([GH#2184](https://github.com/gastownhall/beads/issues/2184))
- **`bd doctor validate`** — Dolt-native conflict detection for data integrity checks ([GH#2249](https://github.com/gastownhall/beads/issues/2249))
- **`bd init --backend`** — explicit backend selection with SQLite deprecation notice
- **`--stdin` flag** — alias for `--body-file -` on `bd create` and `bd update`
- **`bd preflight --check`** — aligned with CI checks for pre-push validation ([GH#2230](https://github.com/gastownhall/beads/issues/2230))
- **gofmt + golangci-lint pre-commit hook** — automatic code quality enforcement ([GH#2179](https://github.com/gastownhall/beads/issues/2179))
- **JSONL-to-Dolt migration script** — standalone bash script for pre-0.50 users ([GH#2276](https://github.com/gastownhall/beads/issues/2276))
- **`bd doctor --fix`** — bridge pending hook migrations into automated repair
- **`bd create-form --parent`** — create sub-issues with label inheritance

### Fixed

- **Dolt CPU spikes** — batch IN-clause queries in `dependencies.go` and `computeBlockedIDs` to prevent runaway CPU usage on large databases ([GH#2294](https://github.com/gastownhall/beads/issues/2294))
- **Dolt joinIter hangs** — avoid hanging queries in `GetReadyWork` blocker computation
- **Stealth mode backup push** — `no-git-ops` config now correctly prevents backup git push ([GH#2290](https://github.com/gastownhall/beads/issues/2290))
- **Stale DB connection crash** — retry stale connection and preserve temp file in `bd edit` ([GH#2267](https://github.com/gastownhall/beads/issues/2267))
- **OSC escape leaks** — prevent escape sequence pollution when third-party hook runners (lefthook, husky) call `bd hooks run` directly ([GH#1303](https://github.com/gastownhall/beads/issues/1303))
- **`validateIssueIDPrefix`** — now checks `allowed_prefixes` config, unblocking convoy creation with multi-prefix databases ([GH#1179](https://github.com/gastownhall/beads/issues/1179))
- **Molecule steps in `bd ready`** — remove stale SQLite-era filtering that hid mol steps ([GH#1359](https://github.com/gastownhall/beads/issues/1359))
- **`bd blocked` children** — include children of blocked parents in output ([GH#1495](https://github.com/gastownhall/beads/issues/1495))
- **Same-type blocking validation** — enforce that dependencies match allowed type pairs
- **Cross-prefix dependency routing** — `bd dep add/rm/blocks/tree` now routes by prefix ([GH#2285](https://github.com/gastownhall/beads/pull/2285))
- **Cross-prefix dep existence check** — skip target existence check for cross-prefix dependencies
- **Wisps table recreation** — recreate `dolt_ignore`'d wisps tables on schema fast-path ([GH#2271](https://github.com/gastownhall/beads/issues/2271))
- **Missing wisps table** — gracefully handle in epic/blocked queries ([GH#2271](https://github.com/gastownhall/beads/issues/2271))
- **Wisp dependency scanning** — scan `wisp_dependencies` in `getChildrenOfIssues` and `getChildrenWithParents`
- **Cascade-delete wisp children** — delete blocked step children during wisp GC
- **Batch wisp deletes** — reduce Dolt commit pressure in `DeleteIssues`
- **Dolt push/pull hint** — show helpful message when remote not found ([GH#2118](https://github.com/gastownhall/beads/issues/2118))
- **Git-protocol remote fallback** — use CLI fallback for all git-protocol remotes, not just SSH ([GH#2268](https://github.com/gastownhall/beads/issues/2268))
- **`dolt_data_dir` persistence** — strip absolute paths from `metadata.json` on save ([GH#2251](https://github.com/gastownhall/beads/issues/2251))
- **Dolt config port** — consult `config.yaml` for `dolt.port` in `DefaultConfig` ([GH#2073](https://github.com/gastownhall/beads/issues/2073))
- **Missing `dolt_database`** — detect and repair in `metadata.json` ([GH#2160](https://github.com/gastownhall/beads/issues/2160))
- **Dolt remote guards** — guard remote subcommands against store init bypass ([GH#2224](https://github.com/gastownhall/beads/issues/2224))
- **`bd search` performance** — avoid `LIKE %%` full-table scans
- **Worktree DB discovery** — skip own `.beads/` in separate-DB discovery ([GH#2190](https://github.com/gastownhall/beads/issues/2190))
- **Contributor routing** — detect fork workflow, apply routing to list and ready
- **Pre-commit Go version mismatch** — detect before golangci-lint panics
- **Viper defaults** — skip in `getRoutingConfigValue` to unblock DB fallback
- **`bd mol wisp create`** — default to root-only unless formula sets `pour=true`
- **Bare repo init** — skip `AGENTS.md` generation in bare repositories
- **`last_import_time` refresh** — prevent false staleness after write commands ([GH#2255](https://github.com/gastownhall/beads/issues/2255))
- **Hook marker detection** — validate marker count and order; recognize marker-managed hooks ([GH#2244](https://github.com/gastownhall/beads/issues/2244), [GH#2242](https://github.com/gastownhall/beads/issues/2242))
- **Gitignore template** — add missing dolt runtime files ([GH#2256](https://github.com/gastownhall/beads/issues/2256))
- **Pre-0.56 Dolt database upgrade** — graceful recovery path ([GH#2137](https://github.com/gastownhall/beads/issues/2137))
- **Remotesapi port check** — skip when no federation peers configured ([GH#2273](https://github.com/gastownhall/beads/issues/2273))
- **Doctor subsystem** — fix panics, masked errors, missing `rows.Err()`, federation port, hardcoded paths; add 3 new tests
- **N+1 query patterns** — replace with batch queries in `deep.go` and `migration_validation.go`
- **`IsBlocked` reporting** — align blocker reporting with blocked-cache semantics ([GH#1524](https://github.com/gastownhall/beads/issues/1524))

### Removed

- **Beads Classic SQLite backend** — the SQLite storage layer and all migration infrastructure have been removed. Dolt is the only backend. ([GH#87493ce](https://github.com/gastownhall/beads/commit/87493ce9))
- **`go-sqlite3` dependency** — no more CGO requirement; tests use Dolt testcontainers
- **Deprecated commands** — removed dead SQLite-era code and legacy scripts

### Documentation

- Updated stale references to JSONL and SQLite architecture
- Hook migration preview/apply workflow documentation

## [0.57.0] - 2026-03-01

### Added

- **`bd doctor --agent` mode** — AI agent diagnostics for automated health checks ([bd-6ud](https://github.com/gastownhall/beads/commit/8d5e3ef7))
- **SSH push/pull fallback** — dual-surface remote management (SQL + CLI) with automatic SSH detection ([#2178](https://github.com/gastownhall/beads/pull/2178))
- **Hook migration system** — census and migration planning for git hooks with doctor signal ([#2217](https://github.com/gastownhall/beads/pull/2221))
- **Section markers for git hooks** — hooks now use section markers for safer updates ([GH#1380](https://github.com/gastownhall/beads/pull/2222))
- **Auto-close molecule root** — molecule root automatically closes when all steps complete ([#2200](https://github.com/gastownhall/beads/issues/2200))
- **CLI aliases** — `--comment` alias for `bd close`, `--yes/-y` alias for `bd mol burn`
- **`bd backup` commands** — `init`, `sync`, and `restore` for Dolt-native backups
- **JSONL backup & auto-backup** — `bd export` command for JSONL backup, auto-enable when git remote exists
- **`bd gc`, `bd compact`, `bd flatten`** — standalone beads lifecycle management
- **Circuit breaker** — Dolt server connection circuit breaker to prevent agent hangs
- **Config-driven metadata schema** — enforcement via `metadata_schema` config ([GH#1416](https://github.com/gastownhall/beads/issues/1416))
- **Metadata flags** — `--metadata` for `bd create`, `--set-metadata`/`--unset-metadata` for `bd update`, metadata filtering in `bd ready` ([GH#1406](https://github.com/gastownhall/beads/issues/1406))
- **PreToolUse hook** — blocks interactive cp/mv/rm prompts in agent workflows
- **Auto-update stale hooks** — `bd init` detects and updates outdated hooks ([GH#1466](https://github.com/gastownhall/beads/issues/1466))
- **`--root-only` flag** — for `bd mol wisp create`
- **`hk` support** — hk.jdx.dev as supported git hook manager ([#2182](https://github.com/gastownhall/beads/pull/2182))
- **`bd dolt remote`** — add, list, remove subcommands for Dolt remote management
- **Auto-push to Dolt remote** — automatic push with 5-minute debounce after Dolt commits
- **`dolt-data-dir` config** — custom dolt data directory ([#2143](https://github.com/gastownhall/beads/pull/2143))
- **`--database` flag for `bd init`** — configure existing server database ([#2102](https://github.com/gastownhall/beads/issues/2102))
- **Per-worktree redirect** — `.beads/redirect` overrides main repo `.beads` ([#1995](https://github.com/gastownhall/beads/pull/1995))
- **`has_metadata_key`** — new predicate in `bd query` DSL ([#1996](https://github.com/gastownhall/beads/pull/1996))
- **MCP claim tool** — atomic start-work for MCP integrations ([#2071](https://github.com/gastownhall/beads/pull/2071))
- **Auto-clean noms LOCK** — stale Dolt noms LOCK file cleanup on startup ([#2059](https://github.com/gastownhall/beads/issues/2059))
- **Stale database detection** — in `bd doctor` and `bd dolt clean-databases`
- **Self-managing Dolt server** — port collision fallback, idle monitor, crash watchdog for standalone users ([GH#2049](https://github.com/gastownhall/beads/issues/2049), [GH#2050](https://github.com/gastownhall/beads/issues/2050))
- **`bd dolt start/stop`** — server lifecycle management commands
- **Counter mode** — `issue_id_mode=counter` for sequential issue IDs ([#2013](https://github.com/gastownhall/beads/issues/2013))
- **Auto-migration shim** — SQLite to Dolt migration via sqlite3 CLI
- **Auto-migrate on first command** — SQLite to Dolt auto-migration on first `bd` command
- **Linear Project sync** — sync Linear projects to beads ([#2022](https://github.com/gastownhall/beads/pull/2022))
- **Label inheritance** — child issues inherit parent labels on creation
- **Infra type routing** — route agent/rig/role/message beads to wisps table
- **`IsInfraType` config-driven** — via `types.infra` config key
- **`bd init --stealth`** — sets `no-git-ops: true` in config ([GH#2159](https://github.com/gastownhall/beads/issues/2159))
- **Staleness check** — read-only commands validate against JSONL freshness
- **CLI feedback titles** — command feedback now shows issue titles ([GH#1384](https://github.com/gastownhall/beads/issues/1384))
- **OTel design docs** — OpenTelemetry architecture and data model documentation ([#2195](https://github.com/gastownhall/beads/pull/2195))
- **Jira V2 API** — support for Jira V2 API ([#2088](https://github.com/gastownhall/beads/pull/2088))

### Fixed

- **Shadow database prevention** — `bd` no longer runs `CREATE DATABASE IF NOT EXISTS` unconditionally; errors with clear diagnostic instead of creating shadow databases
- **Auto-start suppressed with explicit server port** — prevents shadow database creation when configured server is temporarily down
- **Phantom catalog entries** — respect existing `dolt_database` config in init and migrate paths ([#2051](https://github.com/gastownhall/beads/issues/2051))
- **`information_schema` resilience** — replace with `SHOW COLUMNS`/`SHOW TABLES` to avoid stale catalog crashes
- **`bd doctor --server`** — fix crash on phantom entries
- **Reparented child display** — reparented child no longer appears under old parent ([#2001](https://github.com/gastownhall/beads/pull/2001))
- **Homebrew publishing** — use `brews` instead of `homebrew_casks` for formula publishing; auto-publish tap on release
- **NULL `created_by` crash** — handle NULL in issue scan to prevent `bd duplicates` crash ([#2198](https://github.com/gastownhall/beads/pull/2198))
- **Dolt UNION crash** — avoid `SELECT * UNION` that crashes Dolt on column count mismatch
- **`SHOW DATABASES LIKE`** — replace with exact-match iteration for Dolt compatibility
- **Dolt port resolution** — doctor checks and all commands use hash-derived port instead of hardcoded 3307 ([#2148](https://github.com/gastownhall/beads/pull/2148))
- **`dolt_data_dir` respect** — doctor checks use configured data directory ([#2181](https://github.com/gastownhall/beads/pull/2181))
- **Wisps table defaults** — add default values to NOT NULL TEXT columns ([bd-5nn](https://github.com/gastownhall/beads/commit/f489463b))
- **AUTO_INCREMENT reset** — reset counters after `DOLT_PULL` ([#2135](https://github.com/gastownhall/beads/pull/2135))
- **`bd init --stealth`** — correctly sets `no-git-ops: true` in config ([GH#2159](https://github.com/gastownhall/beads/issues/2159))
- **Skip autopush on read-only** — prevent writes on read-only commands ([GH#2191](https://github.com/gastownhall/beads/pull/2215))
- **Sandbox guard** — restore sandbox guard in `maybeAutoPush` ([#2192](https://github.com/gastownhall/beads/pull/2210))
- **Mol notes field** — propagate notes field from formula steps to issues ([#2155](https://github.com/gastownhall/beads/pull/2211))
- **Graph children layer** — lift children to parent's layer in graph layout ([GH#1748](https://github.com/gastownhall/beads/pull/2208))
- **DefaultInfraTypes mutation** — unexport to prevent inconsistency ([#2193](https://github.com/gastownhall/beads/pull/2209))
- **Conditional-blocks readiness** — evaluate conditional-blocks deps in `computeBlockedIDs()` ([#2128](https://github.com/gastownhall/beads/pull/2128))
- **Dolt connection timeouts** — prevent agent hangs with connection timeouts
- **Migration safety** — verify DB target, deduplicate, spot-check data during migration ([#2156](https://github.com/gastownhall/beads/issues/2156))
- **Ephemeral read-path fallback** — fall through to issues table when wisps table missing ([#2161](https://github.com/gastownhall/beads/pull/2161))
- **Ephemeral explicit-ID lookup** — complete routing across all storage layers ([GH#2053](https://github.com/gastownhall/beads/issues/2053))
- **`waits-for` deps in `bd blocked`** — include in output ([GH#2043](https://github.com/gastownhall/beads/issues/2043))
- **Reopen/undefer no-op status** — report correct status ([GH#2037](https://github.com/gastownhall/beads/issues/2037))
- **Post-create metadata** — commit deps and labels to Dolt after create ([GH#2009](https://github.com/gastownhall/beads/issues/2009))
- **Status validation** — validate against built-in + custom statuses on update ([#2021](https://github.com/gastownhall/beads/issues/2021))
- **Empty title/label rejection** — reject on update/label add ([GH#1994](https://github.com/gastownhall/beads/issues/1994))
- **Non-zero exit on failure** — exit non-zero when all close/update attempts fail ([GH#2014](https://github.com/gastownhall/beads/issues/2014))
- **JSON metadata validation** — validate on creation path ([#2032](https://github.com/gastownhall/beads/issues/2032))
- **Wisp field validation** — parity with issues in `createWisp` ([#2032](https://github.com/gastownhall/beads/issues/2032))
- **Empty comment rejection** — reject whitespace-only comment text
- **`bd stale` validation** — reject non-positive `--days` flag
- **`bd defer` past date warning** — warn when `--until` date is in the past
- **Doctor non-git-repo** — gracefully handle in fingerprint and role checks
- **Doctor cruft cleaning** — clean cruft in redirect-expected dirs without redirect file ([#2194](https://github.com/gastownhall/beads/pull/2194))
- **Doctor gitignore** — follow redirect when fixing outdated `.beads/.gitignore` ([#2078](https://github.com/gastownhall/beads/pull/2078))
- **Doctor maintenance** — migrate maintenance checks to Dolt ([#2146](https://github.com/gastownhall/beads/pull/2146))
- **Dolt server management** — Gas Town guardrails, one server per town, kill-before-start ([#2089](https://github.com/gastownhall/beads/issues/2089))
- **Migration 007 resilience** — handle schema evolution differences ([#2168](https://github.com/gastownhall/beads/pull/2168))
- **Windows compatibility** — Makefile ldflags fix, connectex syscall handling, doltserver filesystem heuristic ([#2165](https://github.com/gastownhall/beads/pull/2165), [#2171](https://github.com/gastownhall/beads/pull/2171), [#2092](https://github.com/gastownhall/beads/pull/2092))
- **Batch SQL queries** — prevent query explosion with IN-clause batching ([#2108](https://github.com/gastownhall/beads/pull/2108))
- **Routing write divergence** — check local store before prefix routing ([#2106](https://github.com/gastownhall/beads/pull/2106))
- **Hook outdated detection** — unify between `CheckGitHooks` and `hooksNeedUpdate` ([#2105](https://github.com/gastownhall/beads/pull/2105))
- **Linear status sync** — include `stateId` in push updates ([#2144](https://github.com/gastownhall/beads/pull/2144))
- **Dep type overwrites** — prevent silent dependency type overwrites on dep add
- **Prefix sanitization** — sanitize hyphens to underscores for SQL database names ([GH#2142](https://github.com/gastownhall/beads/issues/2142))
- **Issue prefix normalization** — prevent double-hyphen bead IDs
- **SQLite `#` in URI paths** — escape to prevent truncation ([#2150](https://github.com/gastownhall/beads/pull/2150))
- **`bd dep` traversal** — traverse blocks-type edges in dep tree and graph commands ([#2151](https://github.com/gastownhall/beads/pull/2151))
- **`bd dolt push/pull/commit`** — allow to initialize store ([GH#2042](https://github.com/gastownhall/beads/issues/2042))
- **`bd init --from-jsonl`** — restore for server mode
- **Doctor stale verification** — improved verification after --fix ([#2077](https://github.com/gastownhall/beads/pull/2077))
- **Upsert on insert** — prevent duplicate primary key errors ([GH#2061](https://github.com/gastownhall/beads/issues/2061))
- **Database version auto-migrate** — auto-migrate on CLI upgrade for all commands
- **Wisp double-prefixing** — stop wisp ID double-prefixing and add wisp fallback to `ResolvePartialID`
- **Dolt `DOLT_COMMIT` transaction** — commit SQL tx before `DOLT_COMMIT` to persist wisp data
- **OSC escape leaks** — prevent in git hooks ([GH#1303](https://github.com/gastownhall/beads/issues/1303))
- **`bd ready --json`** — include labels, deps, and parent in output
- **Git upstream warning** — suppress for repos with no remotes ([#2116](https://github.com/gastownhall/beads/pull/2116))
- **`bd` init prefix sanitization** — sanitize auto-detected prefix for MySQL database names
- **Rejected flag-like args** — reject flag-like positional args in `bd create`
- **Backup streaming** — stream backup exports to disk, reduce allocations
- **Version scripts** — update for section markers, suppress unused param lint

### Performance

- **Test parallelization** — Dolt storage tests 3.5x faster, protocol tests 3x faster
- **Branch-per-test isolation** — shared DB with branch-per-test for doctor (44s → ~12s), cmd/bd, and protocol tests
- **Testcontainers migration** — test Dolt server uses testcontainers instead of binary spawning
- **Consolidated test suites** — messaging and dep test suites merged for efficiency
- Add phantom catalog detection to `bd doctor`
- Fix `bd doctor --server` crash on phantom entries (replace `INFORMATION_SCHEMA.SCHEMATA` with `SHOW DATABASES`)

## [0.56.1] - 2026-02-23

### Fixed

- **Release CI** — remove `verify-cgo.sh` post-hook from darwin and freebsd builds which intentionally use `CGO_ENABLED=0` (cross-compilation without CGO)

## [0.56.0] - 2026-02-23

### Removed

- **Embedded Dolt mode** — beads now requires a running Dolt SQL server. The embedded Dolt driver (`dolthub/driver`) and all CGO build-tag bifurcation have been removed. Binary size drops from 168MB to ~41MB. Use `bd dolt start` or `dolt sql-server` to run the server.
- **SQLite ephemeral store** — ephemeral issues (wisps) now live in Dolt-backed `wisps` table with `dolt_ignore`, replacing the separate SQLite database. Run `bd migrate wisps` to migrate existing data.
- **JSONL sync pipeline** — the entire JSONL-based sync system (`bd sync`, git-portable mode, belt-and-suspenders mode) has been removed. Dolt-native push/pull via git remotes is the only sync mechanism. `bd sync` is now a deprecated no-op.
- **JSONL bootstrap** — clone initialization uses Dolt clone only; JSONL bootstrap path removed.
- **JSONL plumbing** — cross-rig JSONL flush, JSONL recovery in doctor, and JSONL-based restore have been removed.

### Added

- **Metadata query support** — `bd list`, `bd search`, and `bd query` now support `--metadata-field key=value` and `has_metadata_key` filters (#1908)
- **Metadata visibility** — `bd show` and `bd list --long` display metadata in human-readable format (#1905)
- **Wisps table** — ephemeral issues stored in dedicated Dolt-backed table with `dolt_ignore` for compaction-friendly lifecycle
- **`bd migrate wisps`** — migration command for SQLite-to-Dolt ephemeral data
- **Batch auto-commit mode** — reduces Dolt commit bloat by batching writes with SIGTERM/SIGHUP flush
- **`--agents-template` flag** — `bd init` now supports named AGENTS.md templates via `embed.FS`
- **Mux setup recipe** — `bd setup mux` with layered AGENTS.md and managed hooks
- **Standalone formula execution** — `bd mol wisp` supports expansion formulas (#1903)
- **Sentinel errors** — `ErrNotFound`, `ErrNotInitialized`, `ErrPrefixMismatch` for cleaner error handling
- **`--skip-prefix-validation`** — `bd import` flag for legacy data migration
- **Protocol invariant test suite** — data integrity and blocking semantics regression tests (#1910)
- **OpenTelemetry instrumentation** — opt-in OTLP tracing for hooks and storage operations (#1940)
- **Transaction infrastructure** — `RunInTransaction` with commit messages, isolation, retry, and batch wrapping for Dolt concurrency

### Fixed

- **`bd ready` ordering** — respect SortPolicy and preserve result ordering (#1883)
- **`waits-for` readiness** — `bd ready` and molecule analysis now correctly handle `waits-for` dependencies (#1900)
- **Dependency tree parent ID** — populate ParentID in tree output and show [BLOCKED] for root (#1992)
- **Parent-child display** — `bd list` now separates parent-child deps from blocking deps (#1948)
- **Hook shim templates** — use `bd hooks run` instead of nonexistent `bd hook` command
- **Cross-expansion dependencies** — `bd mol cook` propagates deps across formula expansions (#1901)
- **Wisp auto-close** — wisp root automatically closes after squash (#1898)
- **Dolt server writes** — commit via `execContext` when server runs with `--no-auto-commit`
- **Metadata normalization** — normalize metadata and waiters in `UpdateIssue`
- **Batch import** — persist labels, comments, and deps during import
- **Noms LOCK detection** — use `flock` probe instead of file existence (#1960)
- **Doctor backend awareness** — deep validation checks configured backend, not directory presence
- **Doctor federation** — use configured database name in federation and health checks (#1904, #1924, #1925)
- **Doctor orphan deps** — exclude `external:` deps from orphan check (#1593)
- **Jira sync** — use correct API v3 `/search/jql` endpoint (#1953)
- **Plugin dep order** — correct inverted `bd dep add` argument order in plugin docs (#1928)
- **Wisp routing** — fix multiple ephemeral store routing gaps for create, read, promote, and gc
- **Prime output** — remove stale `--from-main` flag reference
- **`bd list` resolved blockers** — treat missing/unreachable blockers as resolved (#1884)
- **Wisp search parity** — add ~15 missing filter fields to `searchWisps`
- **Wisp label hydration** — hydrate labels in `getWispsByIDs` for search results
- **Query nil guard** — prevent panic in `GetBlockedIssues` and `GetEpicsEligibleForClosure`
- **Issue prefix clobber** — guard `SetConfig` to prevent overwrite when rigs share a Dolt database
- **Atomic bond/squash** — `bd mol bond`, `bd mol squash`, and `bd mol cook` now run in single transactions (bd-wvplu, bd-4kgbq)
- **`bd ready` parent filter** — pass `--parent` filter to `GetReadyWork`/`GetBlockedIssues` and propagate blocked status to children (#2009, #1495)
- **`bd list` sort/limit** — `--limit` now applies after `--sort`; trim whitespace in `bd edit` (#1237, #1234)
- **Doctor lock false positive** — use `flock` probe for noms LOCK, remove stale `bd sync` references (#1981, #2007)
- **Repo sync consistency** — cross-prefix hydration and close guard consistency (#1945, #1524)
- **DOLT_COMMIT in CRUD** — all write operations now produce Dolt commits for history tracking
- **Double JSON encoding** — fix daemon-mode RPC response unmarshaling across show, dep, label, reopen (bd-4ec8)
- **G304 gosec finding** — annotate `os.ReadFile` in tips.go with `#nosec` for constructed paths (bd-8g8)
- **Stale daemon references** — remove obsolete `bd daemon` references from all documentation (#1982)

### Performance

- **Binary size** — 168MB → ~41MB (dropped `dolthub/driver` and wazero WASM runtime)
- **Linux/Windows startup** — eliminated 2-second wazero JIT compilation penalty on every invocation
- **Test suite** — doctor tests 89s → 28s; shared DB pattern across cmd/bd suites
- **Test isolation** — dolt package and cmd/bd tests now isolated from production Dolt server (bd-2lf6)
- **N+1 queries** — batch dependency/label/comment queries with per-invocation caching (#1874)

## [0.55.4] - 2026-02-20

### Fixed

- **Release CI: FreeBSD build** — disable CGO for FreeBSD target; zig sysroot lacks stdlib.h

## [0.55.3] - 2026-02-20

### Fixed

- **Release CI: macOS builds** — disable CGO for darwin targets; zig sysroot lacks macOS frameworks (CoreFoundation, Security) and libresolv; pure-Go build via `gms_pure_go` + `netgo` tags

## [0.55.2] - 2026-02-20

### Fixed

- **Release CI: libresolv** — strip `-lresolv` entirely from zig wrappers instead of replacing with `-lresolv.9` (zig sysroot has neither); macOS builds use `netgo` tag so libresolv is not needed
- **Release formula** — added rate limit warnings to prevent burning GitHub API quota during CI wait

## [0.55.1] - 2026-02-20

### Fixed

- **Release workflow YAML** — heredoc in zig wrapper step broke YAML literal block parsing; replaced with echo commands
- **Version consistency** — marketplace.json was not bumped in v0.55.0
- **Go formatting** — ran gofmt on 8 files
- **Lint** — fixed unused parameter and dead code in config.go

## [0.55.0] - 2026-02-20

### Fixed

- **Release CI: zig upgrade** — upgrade zig 0.13.0 → 0.14.0 to fix `AccessDenied` bug in cross-compilation (ziglang/zig#20689)
- **Release CI: macOS libresolv** — work around zig 0.14.0 `libresolv` resolution by using versioned `-lresolv.9` (ziglang/zig#16674)
- **Release CI: Android ARM64** — disable CGO for Android target (server mode only)
- **5 pre-existing test failures** — resolve test failures and Dolt panic (bd-iqsw6v)
- **Doctor daemon references** — remove daemon references for post-daemon architecture

### Removed

- **~5K lines dead code** — classic cleanup of obsolete sync pipeline remnants

## [0.54.0] - 2026-02-18

### Fixed

- **Release CI** — fix zig cross-compilation cache race by serializing goreleaser builds (`--parallelism 1`)
- **Android ARM64 build** — disable CGO for Android target (server mode only)
- **`.zig-cache/` in `.gitignore`** — prevent goreleaser dirty-git false positive

## [0.53.0] - 2026-02-18

### Added

- **Dolt-in-Git sync** — native Dolt push/pull via git remotes replaces the entire JSONL sync pipeline; `BootstrapFromGitRemote()`, `HasRemote()`, `sync.git-remote` config key
- **`bd dolt start` / `bd dolt stop`** — explicit Dolt server management commands (#1813)
- **`bd dolt commit`** — desire-path ergonomics for committing Dolt data (#1812)
- **Server mode without CGO** — `OpenFromConfig()` exported for server-mode aware store opening (#1805)
- **Hosted Dolt support** — TLS, authentication, and explicit branch configuration
- **`bd mol wisp gc --closed`** — bulk purge of closed wisps
- **`--no-parent` flag for `bd list`** — filter out child issues
- **`bd ready` pretty format** — improved default output with priority sort, truncation footer, and parent epic context
- **Dolt compaction methods** — `bd compact` support for Dolt databases
- **Storage interface** — `Storage` interface decouples from concrete `DoltStore` (bd-l3o)
- **Lock health diagnostics** — `bd doctor` detects and reports dolt-access.lock and noms LOCK issues
- **Codecov integration** — component-based coverage tracking in CI

### Fixed

- **Pre-commit deadlock on embedded Dolt** — resolve hook deadlock (#1841, #1843)
- **`bd doctor --fix` hang** — run fixes in-process instead of spawning subprocess (#1850)
- **Dolt lock errors** — surface lock errors with actionable guidance instead of silent empty results (#1816)
- **`BEADS_DIR` config loading** — respect `BEADS_DIR` when loading config.yaml (#1854)
- **Dolt `Unknown database` retry** — retry on `Unknown database` after `CREATE DATABASE` (#1851, #1852)
- **Windows `Expand-Archive`** — qualify to avoid Pscx module conflict (#1826)
- **`bd init` git repo check** — ensures git repo exists and runs clean diagnostics
- **`SaveConfigValue`** — preserves existing file contents (#1816)
- **`molecule` core type** — treat molecule as core issue type (#1866)
- **Formula `VarDef`** — distinguish "no default" from `default=""` (#1837)
- **Worktree test isolation** — harden metadata.json isolation (bd-la2cl)
- **Goroutine leak** — replace `time.Sleep` with `waitFor` in tests (#1822)

### Removed

- **JSONL sync-branch pipeline** — deleted `internal/syncbranch/` (5,720 lines), `snapshot_manager`, `deletion_tracking`, doctor sync-branch checks/fixes, and all associated tests (~11,000 lines total)
- **Daemon infrastructure** — removed lockfile activity signal and orchestrator (bd-9u8zd)
- **3-way merge engine remnants** — removed remaining merge code (bd-aswyy)
- **Dead stubs** — `CheckBackendMigration`, `NeedsJSONL`, and other no-ops (bd-23nlq)
- **Dead SQLite-only `bd repair` command**

### Documentation

- **BeadHub** — added to COMMUNITY_TOOLS.md
- **beads-compound plugin** — added to Claude Code Orchestration section (#1814)
- **Stale reference cleanup** — removed daemon/SQLite/JSONL/tombstone references from docs and comments

## [0.52.0] - 2026-02-16

### Added

- **`bd ready --include-ephemeral`** — new flag to include ephemeral issues in ready work results

### Fixed

- **Doctor redirect target resolution** — fix path resolution for redirect targets (#1803)
- **Dolt directory creation guard** — skip dolt directory creation when not in server mode (#1800)
- **Tilde expansion in core.hooksPath on Windows** — properly expand `~` in git hook paths (#1798)
- **Worktree redirect path resolution** — resolve redirect paths from worktree root, not `.beads/` dir (#1791)
- **Block rename-prefix in worktrees** — prevent rename-prefix from running in git worktrees to avoid JSONL staleness (#1792)
- **CI lint warnings** — resolve gosec G204 and unparam lint warnings (#1790)

### Removed

- **Dead git-portable sync functions** — remove unused `gitCommitBeadsDir` and `gitPush` (#1793)

## [0.51.0] - 2026-02-16

### Changed

- **Dolt-native cleanup (dolt-1s40)** — massive 8-phase refactoring to remove all legacy SQLite/JSONL/daemon infrastructure:
  - Phase 2: Remove daemon compat stub and `--no-daemon` flag
  - Phase 3: Remove 3-way merge engine
  - Phase 4: Remove tombstone/soft-delete system
  - Phase 5: Remove JSONL sync layer
  - Phase 6: Remove SQLite backend entirely
  - Phase 7: Remove storage factory, memory backend, and provider abstraction; replace with direct Dolt calls
  - Phase 8: CLI & config cleanup — remove remaining SQLite/daemon remnants
- **Post-cleanup pruning** — removed 12 dead/duplicate DoltStore methods (650 lines), 5 legacy doctor checks (1,457 lines), internal/beads type aliases, stale no-op stubs and dead functions
- **`bd sync` is now a no-op** — sync layer removed as Dolt handles persistence directly
- **Dolt-native API renamed** — legacy storage API consolidated to direct DoltStore calls
- **Server watchdog** — added Dolt server health monitoring

### Fixed

- **Dolt config test corruption** — tests calling `setDoltConfig()` used `FindBeadsDir()` which in worktree environments returned the rig's `.beads/` instead of the test's temp dir; fixed with `t.Setenv("BEADS_DIR", beadsDir)` (#1780)
- **Dolt embedded mode database name** — read from `metadata.json` instead of hardcoding (#bd-pqn2e)
- **Batch `DeleteIssues` hang** — queries batched to prevent hang on large ID sets, with correctness hardening and comprehensive tests (#1770)
- **`bd mol current` step readiness** — use `analyzeMoleculeParallel` for accurate step readiness (#1786, #1276)
- **`bd doctor` AccessLock** — migrate Dolt health checks to shared AccessLock connection with error-path tests and `CloseWithTimeout` documentation (#1780)
- **`bd doctor --yes`** — honor `--yes` flag for repo fingerprint auto-fix (#1782)
- **`bd doctor` sqliteConnString pragmas** — restore pragmas dropped by Phase 8 cleanup
- **`GetReadyWork` type filtering** — exclude workflow/identity types from ready work results (#1774)
- **`bd create` dependency direction** — swap dependency direction for explicit `blocks:` prefix (#1742)
- **`bd dolt seed` custom types** — seed `types.custom` in default config (#1733)
- **`message` built-in type** — add `message` as recognized built-in issue type (#1742)
- **Routing redirect resolution** — lock relative redirect resolution for routed lookups (#1751)
- **Schema init skip** — skip schema initialization when already at current version (#1765)
- **Test race conditions** — serialize `dolt.New()` and stdio redirection to eliminate test races
- **`deleteBatch` signature mismatch** — fix signature and unused types import
- **gofmt** — formatting fixes across 24+ files
- **Bounded lock contention** — enforce bounded lock retry behavior in syncbranch read/write tests (#1777)
- **Malformed test names** — fix test names and remove obsolete SQLite-backend doctor tests

### Performance

- **CASCADE deletes** — leverage SQL CASCADE to cut deletion queries by 60%
- **Schema init** — skip when already at current version, reducing startup overhead

### Documentation

- **SQLite → Dolt migration** — updated 10+ documentation files replacing stale SQLite references with Dolt (ARCHITECTURE.md, FAQ, TROUBLESHOOTING, WORKTREES, INTERNALS, and more)
- **Deleted deprecated docs** — removed `EXTENDING.md` and `MULTI_REPO_HYDRATION.md` (SQLite-era, no longer applicable)
- **INSTALLING.md** — revised for mise installation methods (#1756)
- **CLAUDE.md** — fix stale file references and SQLite mentions
- **Contributor docs** — updated stale SQLite references to Dolt
- **Dolt migrate command** — fix typo (#1779)

## [0.50.3] - 2026-02-15

### Added

- **SyncEngine hook system** — `PullHooks` (GenerateID, TransformIssue, ShouldImport) and `PushHooks` (FormatDescription, ContentEqual, ShouldPush, BuildStateCache/ResolveState) allow tracker-specific behaviors without modifying the engine
- **Jira native integration** — extracted into `internal/jira/` package with REST API v3 client, ADF document conversion, field mapping, and full test coverage
- **Tracker plugin registry** — `tracker.Register()` + `init()` pattern for auto-discovery of tracker implementations (Linear, GitLab, Jira)

### Changed

- **SyncEngine refactor** — all three tracker CLIs (Linear, GitLab, Jira) now use the shared `tracker.Engine` for Pull→Detect→Resolve→Push orchestration, eliminating ~800 lines of duplicated sync code
- **Tracker adapters inlined** — moved from `internal/tracker/adapters/{linear,gitlab}` into `internal/{linear,gitlab}` as self-contained packages with `tracker.go` and `fieldmapper.go`

### Fixed

- **Jira State mapping bug** — removed stale `*StatusField` pointer assignment in `jiraToTrackerIssue` that could cause incorrect status mapping when Priority was set but Status was nil
- **CI: Windows build** — renamed `test_wait_helper.go` to `_test.go` suffix so non-test builds don't try to resolve test-only symbols
- **CI: gofmt** — fixed formatting across 14 files
- **Formula variable validation** — use `Required` field for formula variable validation
- **`bd slot` routing** — added routing and label-based agent check to slot commands

### Performance

- **Test suite** — replaced ~60 `git init` subprocess calls with cached template copy, reducing test setup overhead

## [0.50.2] - 2026-02-14

### Added

- **SQLite-to-Dolt migration nudge** — rate-limited hint (once per 24h) on any `bd` command for SQLite users, plus `bd doctor` backend migration check (#1706)
- **Dolt audit log** for `bd dolt config set` changes

### Fixed

- **`bd doctor` performance** — replaced O(n) full-table scans with SQL aggregation queries, reducing doctor runtime from 130s to 6s on large databases (#1706)
- **Dolt metadata writes** — reliable `bd_version`, `repo_id`, `clone_id` persistence with init-time verification, doctor fix, and migrate extension (#1741)
- **`FindBeadsDir` worktree boundary** — stopped directory walk from escaping sibling worktree boundaries (#1731)
- **`bd doctor` plugin install command** — corrected command shown in doctor output (#1732)
- **Backend descriptions** — updated `bd backend list` and help text to show Dolt as the default (not SQLite) since v0.50

## [0.50.1] - 2026-02-14

### Fixed

- **CI: goreleaser failure** — removed dead `internal/rpc` import from test stubs that broke `go mod tidy`
- **CI: Windows smoke test** — use `--backend sqlite` and temp dir to avoid Dolt dependency and repo `.beads/` conflicts
- **CI: lint errors** — updated golangci-lint v2 exclusions for fmt.Fprintf errcheck, gosec G104/G304
- **CI: gofmt** — fixed formatting in init_test.go, main.go, sync_git_remote_test.go

### Added

- **Plugin-based issue tracker framework** with Linear and GitLab adapters (#1150)

## [0.50.0] - 2026-02-14

### Added

- **Dolt is now the default backend** for new `bd init` projects. Existing SQLite projects are unaffected. `BEADS_DB` env var auto-detects SQLite.
- **`bd graph` visualization overhaul** — terminal-native horizontal DAG as default view, plus DOT and interactive HTML export formats. Old box view available via `--box` (bd-9de)
- **`bd sql` command** for raw SQL access to the underlying database. Supports table, JSON, and CSV output
- **`bd help --all`** to dump complete command reference (#1699)
- **`decision` built-in issue type** with aliases and help strings
- **Cross-database dependency resolution** via prefix routes in `bd show`, `bd graph`, and `bd blocked` — external deps display actual title, status, and priority (bd-k0pfm)
- **`bd show --watch`** flag for auto-refreshing display on file changes with fsnotify debounce
- **`bd vc commit --stdin`** for reading multi-line commit messages from stdin
- **`BD_BRANCH` env var** for branch-per-polecat write isolation in Dolt
- **`BD_NAME` env var** for multi-instance help text identity
- **`bd doctor` artifact cleanup** — `--check=artifacts` detects stale JSONL, WAL/SHM, backup files. Works with `--clean` and `--fix`
- **`bd doctor` Claude Code integration checks** — detect malformed `settings.json`, verify hook completeness, detect legacy MCP tool references
- **`bd doctor` output grouped by category** with per-category pass counts
- **Dolt corruption recovery** via `bd doctor --fix` — backs up corrupted `.dolt-data/`, reinitializes fresh Dolt database, auto-imports from JSONL backup
- **Frictionless `bd init`** — better test-issue detection, non-interactive stdin detection, version tracking at init time, cancellable prompts, actionable error hints via `FatalErrorWithHint`
- **Mise installation** documented in INSTALLING.md

### Changed

- **Default backend is Dolt** — `bd init` without `--backend` now creates Dolt databases. Existing projects with no explicit backend in metadata.json continue to use SQLite (backward compatible)
- **Removed daemon/RPC subsystem** — internal daemon, RPC layer, and `internal/rpc/` package deleted (~19,663 lines). All commands use direct embedded database access
- **Removed JSONL sync layer** — `internal/importer/`, `markDirtyAndScheduleFlush()`, and `daemonClient != nil` branches eliminated (~7,634 lines)
- **Stripped dirty tracking from Storage interface** — `MarkIssueDirty` subsystem no longer part of the storage contract
- **`bd graph` default view** changed from vertical box layout to horizontal DAG
- **Schema version downgrade protection** — CLI refuses to run against a database written by a newer version
- **Upgrade notification** only triggers for actual version upgrades (no longer fires on downgrades/branch switches)
- **`resolve-conflicts` defaults to canonical `issues.jsonl`**
- **Batch `GetLabelsForIssues`** replaces N+1 `GetLabels` calls in list operations
- **Consolidated redirect resolution** into single `FollowRedirect` function
- **Backend-agnostic refactoring** — compact, migrate, dep, sync, findReplies, and wisp/mol commands use Storage interface instead of SQLite type assertions

### Fixed

- **`bd close` enforces gate satisfaction** before closing machine-checkable gates (`gh:pr`, `gh:run`, `timer`, `bead`). `--force` bypasses
- **`bd show` exits non-zero** when issue not found (previously exited 0 with empty JSON output)
- **`bd list` closed-blocker filtering** — closed issues no longer shown as blockers in annotations
- **`bd list --parent` includes dotted-ID children** — matches both explicit parent links and `X.*` naming convention
- **`bd ready` deferred-parent propagation** — excludes children of deferred parents from ready work
- **Dolt `joinIter` panic** prevented by replacing `IN`/`EXISTS` subqueries with Go-level filtering
- **Embedded Dolt self-deadlock** in git hooks and `bd migrate` — added to `noDbCommands`
- **Dolt rename FK violation** — disable FK checks during rename operations
- **Dolt `GetIssuesByLabel` deadlock** — fixed connection pool exhaustion with `MaxOpenConns=1`
- **XSS vulnerabilities** in graph HTML export (bd-67uzz)
- **Redirect path resolution** — resolve from parent of `.beads` dir, not `.beads` itself
- **`bd migrate --to-dolt`** sets `sync.mode=dolt-native` and uses config-based backend
- **`bd doctor` backend-agnostic checks** — uses storage factory instead of raw SQLite; routes.jsonl false positive fix; `dolt_mode server` respected
- **`bd show` extra newline** before first issue header removed
- **`--type` flag help text** shows only base types
- **`bd prime` prompt** optimized to avoid spurious "creating issue without description" warning
- **Linuxbrew ICU paths** supported in Makefile
- **Windows build recipe** fixed (BINARY conditional moved inside targets)
- **`go install`** fixed by removing local `replace` directive
- **Nix flake build** and `gosec` lint errors resolved
- **Duplicate Cobra command registration** replaced with `Aliases`
- **Homebrew upgrade command** corrected in upgrading docs

### Documentation

- Added Dolt GitHub link to README
- Added beadsmap and Beadbox to community tools
- Corrected Homebrew upgrade command
- Added Mise installation instructions to INSTALLING.md

## [0.49.6] - 2026-02-08

### Reverted

- **Embedded Dolt mode restored** - The v0.49.5 removal was premature; embedded mode is still needed in Beads (only Gas Town should be server-only). Restores dolthub/driver, advisory flock, embedded connector lifecycle, CGO build tags, and vendored go-icu-regex

## [0.49.5] - 2026-02-08

### Added

- **`bd search` content and null-check filters** - Filter issues by description content and metadata presence (`--has`, `--no`) (bd-au0.5)
- **`bd promote` command** - Promote wisps to persistent beads (gt-7mqd.9)
- **`bd todo` command** - Lightweight task management for quick personal tracking
- **`bd find-duplicates` command** - AI-powered duplicate issue detection (bd-581b80b3)
- **`bd validate` command** - Data-integrity health checks, integrated into `bd doctor --check=validate` (bd-e108)
- **Dolt fail-fast TCP check** - Quick connectivity check before MySQL protocol initialization
- **Windows PE version info** - Embedded version metadata reduces antivirus false positives (bd-t4u1)
- **Centralized AI model config** - `ai.model` config key with `DefaultAIModel()` helper
- **Newsletter generator** - Automated narrative release summaries (#1197)

### Changed

- **Embedded Dolt mode removed** - Server-only Dolt connections now; embedded driver fully removed (bd-esqfe). Windows Dolt backend connects via MySQL protocol. **Note: reverted post-release — see [Unreleased]**
- **`bd init` defaults to chaining hooks** - No longer prompts; chains by default (bd-bxha)
- **Homebrew formula name** - `bd doctor` now correctly suggests `brew upgrade beads` instead of `brew upgrade bd`
- **Doctor output** - Summary-first layout with improved formatting (bd-4qfb)
- **JSON output standardized** - Consistent JSON format across all commands (bd-au0.7)
- **Go toolchain** - Downgraded from 1.25.7 to 1.25.5 for Nix compatibility. **Note: back to 1.25.7 after embedded Dolt restore — see [Unreleased]**

### Fixed

- **Security: SQL injection prevention** - SQL identifier validation for dynamic table and database names (dolt-test-46a)
- **Security: path traversal** - Fixed path traversal in export handler and command injection in import
- **Security: CVE-2025-68121** - Go toolchain upgrade for TLS runtime fix
- **RPC: empty mutation events** - Pass issueID via closure to prevent zero-value IDs in label/dep operations (dolt-test-rgh)
- **RPC: server shutdown** - Drain in-flight requests before closing storage; proper response-write-then-shutdown pattern
- **RPC: nil storage guards** - Added to handleCommentList, handleCommentAdd, and health handler
- **RPC: daemon socket path** - Use ShortSocketPath for status/health/metrics commands
- **Daemon: YAML config parsing** - Recognize both hyphenated and underscored daemon config keys (e.g., `auto-sync` and `auto_sync`)
- **Daemon: stderr redirect** - Removed unsafe global os.Stderr replacement during import
- **Daemon: daemon stop suggestion** - `bd doctor` fix suggestions now include workspace path argument
- **Doctor: role check** - Falls back to database config for users who set role via `bd config set`
- **Doctor: gastown detection** - Auto-detect gastown mode when routes.jsonl exists
- **Doctor: routing mode** - Accept `explicit` as valid routing mode alongside auto/maintainer/contributor
- **Doctor: Dolt conflicts query** - Use correct `table` column name (not `table_name`)
- **Sync: conflict resolution** - Apply --ours/--theirs strategy correctly
- **Sync: tombstone export** - Include tombstones in auto-flush full export
- **Merge: panic prevention** - Use strings.HasPrefix to prevent panic on short error messages
- **Import: label sync** - Remove DB labels absent from JSONL during import
- **Import: issueEqual comparison** - Include dependencies and comments in comparison
- **Export: dirty flag preservation** - Prevent stdout export from clearing dirty flags and auto-flush state
- **Export: temp file collisions** - Use unique temp filenames in writeMergedStateToJSONL
- **Migration: bounds checks** - Add bounds checks for slice/string access in migrate.go
- **Migration: daemon check** - Check for running daemon before dolt migration
- **Migration: error logging** - Log warnings for label and dependency import errors
- **Migration: legacy spec_id** - Fix doctor --fix migration for legacy spec_id
- **SQLite: WAL retry deadlock** - Make Close() idempotent to prevent deadlock (bd-4ri)
- **SQLite: BUSY retry** - Use SQLITE_BUSY retry logic for all BEGIN IMMEDIATE calls (bd-ola6)
- **SQLite: JSONL locking** - Add exclusive lock to flushToJSONLWithState to prevent race conditions
- **Dolt: connection retry** - Make "connection refused" retryable in retry logic
- **Dolt: cross-rig contamination** - Use prefix-based database names; detect contamination in post-migration check
- **Dolt: port consistency** - Use port 3307 consistently for server connections
- **Config: beads.role** - Write to git config instead of SQLite
- **Config: viper guard** - Log when viper uninitialized in getConfigList
- **Config: BD_BACKEND blocked** - Block env var to prevent data fragmentation (bd-hevyw)
- **`bd list` output** - Separate parent-child from blocks display; `--all` disables default limit
- **`bd dep list` routing** - Fix cross-rig routing from town root (bd-ciouf)
- **Cross-prefix ID resolution** - Support multi-repo scenarios (GH#1513)
- **Staleness checks** - Added to export, graph, history, gate list commands
- **Gitignore template** - Add `.jsonl.lock` to template and requiredPatterns
- **MCP plugin** - Remove output_schema workaround for FastMCP 2.14.4
- **Nix/Windows CI** - Unbroken: removed orphaned pure_go_windows.go, updated flake.lock, fixed vendorHash

### Documentation

- Messaging system documentation (messaging.md)
- Issue metadata field and reserved key prefixes
- Docker trick for updating flake.lock without nix
- Community Tools: JetBrains beads-manager plugin, Beadspace desktop app
- Fix ready docs and external-ref documentation (#1523, #1339)

## [0.49.4] - 2026-02-05

### Added

- **Label pattern and regex filtering** - New `--label-pattern` (glob) and `--label-regex` flags for `bd list` and `bd ready`. Filter issues by label patterns like `tech-*` or regex like `tech-(debt|legacy)` (#1491)
- **Simple query language** - Complex filtering via a simple query syntax for `bd list`
- **`beads` command alias** - Installer now creates a `beads` alias alongside `bd`
- **spec_id field** - New field for linking issues to specification documents (#1372)
- **Wisp type field** - `wisp_type` column for TTL-based compaction of ephemeral molecules
- **Dolt schema migration runner** - Automated schema migrations for Dolt backend
- **Dolt migration validation in bd doctor** - Checks for Dolt migration health
- **Agent-managed Dolt upgrade path** - `bd migrate` support for Dolt upgrades
- **`--metadata` flag for bd update** - Update issue metadata from CLI via JSON (#1417, bd-0vud2)
- **UpdateIssue metadata via json.RawMessage** - Programmatic metadata updates (#1417)
- **BD_SKIP_ACCESS_LOCK env var** - For Dolt lock testing scenarios
- **Orphan branch support for sync-branch migration** - Cleaner sync branch setup
- **config.local.yaml support** - Local configuration overrides via `.beads/config.local.yaml`
- **Makefile up-to-date check** - `make install` now verifies repo is current before building

### Changed

- **`bd ready` now shows only open issues** - Excludes in_progress, matching `bd list --ready` behavior. Shows work that is truly available to claim
- **release.sh is now a molecule gateway** - Creates a release wisp instead of running a batch script
- **Merge driver embeds types.Issue** - Prevents field drift between merge driver and core types (#1481)

### Fixed

- **JSONL file locking** - Prevents race conditions in concurrent writes and incremental exports
- **WAL checkpoint robustness** - Retry logic for SQLite WAL checkpointing
- **Merge driver field drift** - Driver now preserves all issue fields including spec_id, metadata, and dependencies (#1480, #1481, #1482)
- **Atomic bd claim** - Compare-and-swap semantics prevent race conditions (#1474)
- **BEADS_DIR routing** - Respects BEADS_DIR over prefix routing in show/close/update commands
- **Dolt-native sync fixes** - Disabled JSONL sync exports, auto-flush, and imports in dolt-native mode to prevent conflicts
- **Daemon auto-import** - Proper JSONL import on startup with hash update; check changes AFTER pulling
- **Windows build** - Dolt now builds on Windows via pure-Go regex; prebuilt releases in installer
- **Cross-rig dependency resolution** - Uses factory.NewFromConfig for proper backend detection
- **Dolt lock contention** - Advisory flock prevents zombie processes; scoped migration helpers
- **Dolt connection stability** - Retry logic for transient errors, timeout on embedded close, fix for nested query bad connections
- **Dolt FK constraint** - Removed FK constraint on depends_on_id for external references
- **bd list --tree deduplication** - Children no longer appear multiple times
- **bd show relates-to display** - Correctly displays and deduplicates RELATED section
- **bd cleanup --hard** - Always prunes tombstones immediately
- **Formula TOML vars** - Accept simple string vars in [vars] section
- **config.yaml fallback** - Custom types work in daemon mode
- **Content-based merge for daemon pulls** - Prevents spurious conflicts (GH#1358)
- **TOML snake_case tags** - Proper serialization of formula struct fields
- **Worktree bare repo support** - Use GetGitCommonDir for worktree creation
- **routes.jsonl corruption** - Excluded from FindJSONLInDir to prevent import errors
- **Compaction safety logic** - Restored accidentally removed safety checks
- **Structured logging** - Replaced custom daemonLogger with *slog.Logger

### Documentation

- Non-interactive shell command guidance for agents
- Makefile CGO settings and DOLT.md env vars
- Atomic claim feature in quick reference
- ICU build deps and installer hints
- Community Tools: beads-sdk and Beads Task-Issue Tracker

## [0.49.3] - 2026-01-31

### Changed

- **Embedded Dolt is now the default** - Server mode is opt-in via `dolt_mode: "server"` in metadata.json or `BEADS_DOLT_SERVER_MODE=1` env var

### Fixed

- **Dolt split-brain root cause eliminated (B1+B2)** - `DatabasePath()` now always resolves to `.beads/dolt/` for dolt backend regardless of stale `database` field values; `bootstrapEmbeddedDolt()` blocks JSONL auto-import in dolt-native sync mode to prevent silent rogue database creation
- **CGO/ICU build fix** - Makefile and test.sh detect Homebrew's keg-only `icu4c` and export CGO flags so dolt's go-icu-regex dependency links correctly on macOS
- **Dolt mergeJoinIter panic** - Eliminated three-table joins that triggered panics on type-filtered queries; added guard against nil pointer panic during auto-import in dolt-native mode
- **Template variable extraction** - Filter Handlebars keywords (`if`, `each`, `unless`, `with`) from `extractVariables` (#1411)

## [0.49.2] - 2026-01-31

### Added

- **GitLab backend for bidirectional issue sync** - Full GitLab integration for syncing beads issues with GitLab
  - REST API client wrapper with pagination, rate limiting, and context cancellation
  - Bidirectional sync: pull from GitLab, push to GitLab with conflict detection
  - `bd gitlab sync`, `bd gitlab status`, `bd gitlab projects` subcommands
  - GitLab-to-beads mapping with path-based IDs and collision handling
  - Conflict resolution strategies for concurrent edits
  - Integration and unit test suites

- **Key-value store** - New `bd kv` subcommand for persistent key-value storage
  - `bd kv get`, `bd kv set`, `bd kv delete`, `bd kv list` commands
  - Key validation and proper exit codes on missing keys

- **Per-issue JSON metadata field** - Optional metadata field on issues (SQLite + Dolt) (#1407)

- **Events JSONL export** - Opt-in event audit trail export via `events-export` config

- **Role configuration** - Explicit role configuration via git for agent and user roles
  - `bd init` interactive contributor role prompt
  - UserRole detection in RepoContext
  - Agent roles configurable via config (bd-hx8w)

- **Dolt improvements**
  - Dolt-specific diagnostics and performance profiling in `bd doctor`
  - Auto-detect Dolt server and enable server mode during `bd init`
  - Env var overrides for Dolt server connection settings
  - `bd backend` and `bd sync mode` subcommands for inspecting storage config

- **CLI improvements**
  - `comment_count` in issue JSON views (list, ready, search)
  - Comment timestamps with time display and `--local-time` flag
  - Hint to view step instructions in `bd mol current` output (#1403)
  - Resolve external refs in `bd dep list` for cross-rig dependencies
  - Inline blocking dependency display in `bd list` output

- **Jira sync** - `pull_prefix` and `push_prefix` config options for flexible prefix mapping

- **Sync** - Push permission error detection during sync

### Changed

- **Separation of concerns** - Removed Gas Town-specific code from beads core
  - Removed Gas Town role detection from hooks
  - Removed `--role-type` flag from `bd create`
  - Removed Gas Town actor validation from validation layer
  - Generalized RoleType to be project-agnostic

- **Refactoring**
  - Storage factory for backend-aware database access
  - Compact accepts interface instead of concrete SQLiteStorage
  - RPC layer no longer imports sqlite directly
  - JSONL deletion markers now processed during import

### Fixed

- **GitLab fixes** - Multiple rounds of code review improvements for merge readiness
  - Conflict detection before push with SyncContext
  - Pagination limits and context cancellation
  - Error handling and type safety improvements

- **Dolt backend fixes**
  - Graceful server-to-embedded fallback when Dolt server unreachable
  - Detect and clean stale Dolt lock files
  - Skip JSONL staleness check in dolt-native mode
  - Backend detection in all `bd doctor` fix functions
  - Use `GetDoltDatabase()` instead of raw config for server mode
  - Factory-based storage creation for backend awareness
  - Fix for stale JSONL in dolt-native mode
  - SQLite not created when Dolt fails in dolt-native mode
  - Revert Dolt lock cleanup workaround; fix embedded Dolt open via driver retries (#1389)

- **Sync fixes**
  - Set GIT_DIR and GIT_WORK_TREE in GitCmd for worktree support
  - Normalize JSONL path in CommitToSyncBranch for sync-branch mode
  - Use store.Path() for database mtime update
  - Ensure sync-branch worktree exists on fresh clone (#1349)
  - Skip sync when source and destination JSONL paths are identical (#1367)
  - Respect dolt-native mode in daemon_sync_branch.go
  - Respect dolt-native mode in JSONL export paths
  - Auto-stage JSONL files after flush in pre-push hook

- **Other fixes**
  - Ignore undeclared handlebars in formula description text (#1394)
  - Exclude ephemeral issues from Linear sync push (#1397)
  - Allow event type in CreateIssue validation (#1398)
  - Update MCP Stats model to match `bd stats --json` output (#1392)
  - Handle EINVAL from chmod on Unix sockets in containers (#1399)
  - Correct error message for `--gated` flag (#1391)
  - Check rows.Err() in SQLite GetAllEventsSince
  - Exclude tombstones from external_ref uniqueness validation
  - Handle nil pointer dereference in `bd restore` with invalid issue ID
  - Make daemon start idempotent (#1334)
  - YAML config support for daemon auto-sync settings (#1294)
  - Use IsValidWithCustom for `--type` validation (#1356)
  - Skip SQLite-specific checks in doctor when using Dolt backend
  - Tombstone wins over closed in merge conflict resolution

### Documentation

- GitHub Copilot integration guide with cross-platform MCP paths (#1348)
- KV store CLI documentation
- Updated COMMUNITY_TOOLS.md
- Added export-state/ to gitignore template

## [0.49.1] - 2026-01-25

### Added

- **Dolt backend now fully supported** - The Dolt storage backend has been extensively tested and is ready for community evaluation
  - **Note:** Dolt is not enabled by default. We encourage users to try it out and report feedback!
  - Auto-commit on write commands with explicit commit authors (#1267)
  - Server mode for multi-client access - enables multiple processes to share a Dolt database
  - `bd doctor --server` flag for Dolt server mode health checks
  - Server mode configuration in metadata.json schema
  - Comprehensive test suite for Dolt storage backend (#1299)
  - Lock retry and stale lock cleanup for operational reliability (#1260)
  - Adaptive ID length instead of hardcoded 6 chars
  - See docs/DOLT.md for setup guide

- **New command flags**
  - `bd activity --details/-d` for full issue information in activity feed (#1317)
  - `bd export --id` and `--parent` filters for targeted exports (#1292)
  - `bd update --append-notes` flag to append to existing notes (#1304)
  - `bd update --ephemeral` and `--persistent` flags (#1263)
  - `bd show --id` flag for IDs that look like flags

- **Doctor improvements**
  - `--server` flag for Dolt server mode health checks
  - Stale closed issues check is now configurable (#1291)

- **Community tools** - Added beads-kanban-ui, beads-orchestration (#1255), and abacus (#1262)

- **Newsletter generator** - Automated narrative summaries of releases

### Changed

- **Homebrew installation** - Now uses core tap for beads installation (#1261)
- **GitHub Actions** - Upgraded for Node 24 compatibility (#1307, #1308)

### Fixed

- **Dolt backend fixes**
  - Skip JSONL checks in pre-push hook for dolt-native mode
  - Add ID generation to transaction CreateIssue
  - Use capabilities check instead of blanket Dolt block in daemon
  - Recognize shim hooks as Dolt-compatible
  - Add hook compatibility check and migration warning
  - Skip JSONL sync for Dolt backend
  - Refuse daemon startup with deprecated --start flag for Dolt backend
  - Add YAML fallback for custom types/statuses
  - Prevent daemon startup and fix routing same-dir check
  - Proper server mode support for routing and storage
  - Skip daemon auto-start for all Dolt backends

- **Daemon fixes**
  - Prevent stack overflow on empty database path (#1288, #1313)
  - Add sync-branch guard to daemon code paths (#1271)

- **List command fixes**
  - Optimize `bd list --json` to fetch only needed dependencies (#1316)
  - Prevent nil pointer panic in watch mode with daemon (#1324)
  - Populate dependencies field in JSON output (#1296, #1300)

- **Import/export fixes**
  - Support custom issue types during import (#1322)
  - Populate export_hashes after successful export (#1278, #1286)
  - Use transaction for handleRename in upsertIssuesTx (#1287)

- **SQLite fixes**
  - Use BEGIN IMMEDIATE without retry loop (#1272)
  - Add retry logic to transaction entry points (#1272)
  - Use withTx helper for remaining transaction entry points (#1276)

- **Other fixes**
  - `bd gate add-waiter` now functions correctly (#1265)
  - Respect BEADS_DIR environment variable in init (#1273)
  - Find molecules attached to hooked issues (#1302)
  - Separate parent-child deps from blocking deps in `bd show` (#1293)
  - PrefixOverride now respected in transaction storage layer (#1257)
  - Prime command defaults to beads, avoids TodoWrite/TaskCreate (#1289)
  - Build: `make install` now properly builds, signs, and installs to ~/.local/bin

### Documentation

- **Dolt backend guide** - Comprehensive docs/DOLT.md guide (#1310)
  - Federation section with quick start and topologies
- **Articles collection** - Created ARTICLES.md for articles and tutorials about Beads (#1306)

## [0.49.0] - 2026-01-21

### Added

- **Dolt federation for multi-repo sync** - Peer-to-peer issue synchronization across repositories
  - `bd federation sync` command for syncing with configured peers
  - `bd federation status` shows connection and sync state
  - SQL-server mode for daemon (`--federation` flag) enables peer connections
  - SQL user authentication for secure peer-to-peer sync
  - Doctor federation health checks validate connectivity and sync state
  - See docs/FEDERATION.md for setup guide

- **SQLite to Dolt migration** - Migrate existing repos to version-controlled storage
  - `bd migrate dolt` converts SQLite database to Dolt backend
  - Preserves full issue history during migration
  - Automatic JSONL bootstrap for routes and interactions

- **New commands**
  - `bd children <id>` - Display child issues for a parent
  - `bd rename <old-id> <new-id>` - Rename issue IDs
  - `bd view` - Alias for `bd show` command (#1249)
  - `bd config validate` - Validate sync configuration
  - `bd mol seed --patrol` - Seed patrol molecules (#1149)

- **Sync improvements**
  - Per-field merge strategies for fine-grained conflict resolution
  - Interactive conflict resolution with `--manual` strategy
  - Incremental export for large repositories (performance improvement)
  - `sync.mode` configuration to control sync behavior

- **CLI conveniences**
  - `-m` flag as alias for `--description` in `bd create`
  - `--type` and `--exclude-type` flags for Linear sync filtering (#1205)
  - `--tree --parent` combination for hierarchical display (#1211)

- **Jujutsu (jj) version control support** - Beads now works with jj repositories
  - Hook support for jj operations
  - Proper detection of jj-managed repositories

- **Codex CLI setup recipe** - Automated setup for OpenAI Codex CLI integration (#1243)

- **Real-time activity feed** - Uses fsnotify for instant updates

- **Per-worktree export state tracking** - Each worktree maintains independent sync state

- **Automatic multi-repo hydration in `bd init --contributor`** - Routing and hydration now configured together
  - `bd init --contributor` automatically adds planning repo to `repos.additional`
  - Routed issues appear in `bd list` immediately after setup
  - No manual `bd repo add` required for contributor workflow

- **Doctor improvements**
  - Routing+hydration mismatch detection - warns when routing configured without hydration
  - Hydrated repo daemon check - ensures JSONL stays fresh in additional repos
  - Patrol pollution detection and fix
  - `--gastown` flag for Gas Town-specific checks (#1162)
  - Federation health checks

- **Nix shell completions** - Baked into default package (#1229)

### Changed

- **Auto-routing disabled by default** - Explicit opt-in required (#1177)
  - Prevents unexpected cross-repo routing
  - Enable with `routing.mode: auto` in config.yaml

- **Gas Town types removed from core** - Beads core no longer includes Gas Town-specific types
  - Types like `patrol`, `convoy` moved to Gas Town configuration
  - Cleaner separation between beads and Gas Town
  - Ongoing cleanup tracked in bd-741si, bd-7nd6t, bd-31ajf

### Fixed

- **Daemon zombie state after database file replacement** - Improved reconnection resilience (#1213)
  - Health checks now properly detect and handle database file replacements
  - Prevents "sql: database is closed" errors

- **Routed issues invisible in `bd list` (split-brain bug)** - Auto-flush JSONL after routing (#1251)
  - `bd create` now flushes JSONL immediately in target repo
  - Hydration now sees new issues immediately

- **WSL2 Docker Desktop compatibility** - Detect bind mounts and disable WAL mode (#1224)
  - Prevents database corruption on Docker Desktop file systems

- **Daemon stack overflow** - Prevent recursion in handleStaleLock (#1238)

- **Molecule steps excluded from `bd ready`** - Filter out non-actionable items (#1246)

- **Tree ordering stabilization** - Consistent `--tree` output (#1228)

- **Cross-repo orphan detection** - Honor `--db` flag (#1200)

- **Custom types with daemon** - Show custom types when daemon is running (#1216)

- **Dolt backend fixes**
  - Parse timestamps from TEXT columns correctly
  - Single-process mode enforcement (daemon/autostart disabled) (#1221)
  - Read-only daemon commands now work
  - Init/daemon/doctor integration fixes (#1218)

- **Redirect handling** - Follow redirect when creating database

- **Auto-import tombstone handling** - Auto-correct deleted status to tombstone (#1231)

- **Routing store cleanup** - Close original store before replacing (#1215)

- **Worktree redirect paths** - Correct path computation in `bd worktree create` (#1217)

- **Sync-branch worktree** - Use worktree for `--full --no-pull` (#1183)

- **Custom types from config** - Load types.custom during init auto-import (#1226)

### Documentation

- **Federation setup guide** - Added docs/FEDERATION.md
  - Peer configuration and authentication
  - Sync workflow documentation

- **Daemon troubleshooting guide** - Added docs/TROUBLESHOOTING.md and docs/DAEMON.md
  - Common daemon issues and solutions
  - Database replacement handling

- **Multi-repo hydration guide** - Added comprehensive section to docs/ROUTING.md
  - Explains hydration requirement when using routing
  - Troubleshooting guide for common issues

- **Contributor vs maintainer setup** - Added to README.md
  - Clarifies when to use `bd init --contributor`
  - Documents role configuration options
## [0.48.0] - 2026-01-17

### Added

- **VersionedStorage interface** - Abstract storage layer with history/diff/branch operations
  - Enables pluggable backends (SQLite, Dolt) with unified API
  - Supports time-travel queries and branching semantics

- **`bd sync` command specification** - Formalized sync workflow implementation
  - Clearer separation between export, import, and merge phases

- **`bd types` command** - List valid issue types (#1102)
  - Shows all available types with descriptions

- **"enhancement" type alias** - Alternative name for "feature" type
  - Matches GitHub issue label conventions

- **`bd close -m` flag** - Alias for `--reason` (git commit convention)
  - More intuitive for git users: `bd close <id> -m "reason"`

- **RepoContext API** - Centralized git operations context (#1102)
  - Consistent git directory handling across codebase

- **Dolt backend improvements (WIP)**
  - Automatic bootstrap from JSONL on first access
  - Git hook infrastructure for Dolt operations
  - `bd compact --dolt` flag for Dolt garbage collection

### Fixed

- **Doctor sync branch health check** - Removed destructive --fix behavior (GH#1062)
  - No longer warns about expected source file differences
  - Prevents accidental sync branch history destruction

- **Duplicate merge target selection** - Use combined weight (GH#1022)
  - Considers both dependents and dependencies when choosing merge target
  - Prevents closing issues with children/dependencies

- **Worktree exclude paths** - Use --git-common-dir for correct paths (GH#1053)
  - Fixes `bd init --stealth` in git worktrees

- **Daemon git.author config** - Apply configured author to sync commits
  - Respects `git.author` config and `BD_GIT_AUTHOR` env var

- **Hook chaining preservation** - Prevent --chain from destroying original hooks (#1120)
  - Backs up existing hooks before chaining

- **Sync routed prefixes** - Allow routed prefixes in import validation
  - Fixes multi-prefix workflow issues

- **Windows CGO-free builds** - Enable building without CGO (#1117)
  - Improved Windows compatibility

- **Shell completions without database** - Work without .beads/ (#1118)
  - Completions function in non-beads directories

- **Timestamp normalization** - Normalize to UTC for validation (#1123)
  - Prevents timezone-related validation failures

- **Symlinked .beads directories** - Correct routing for symlinks (#1112)
  - Proper resolution of symlinked beads directories

- **Nil pointer in ResolvePartialID** - Prevent panic (#1132)
  - Guards against nil pointer dereference

- **Orphaned dependencies on delete** - Mark dependents dirty (#1130)
  - Prevents orphan deps in JSONL after issue deletion

- **Git hooks in worktrees** - Fix hook execution (#1126)
  - Hooks now work correctly in linked worktrees

### Documentation

- **CLI skill reference** - Synced with v0.47.1 commands
  - Updated command documentation in claude-plugin

- **AGENTS.md fixes** - Removed references to nonexistent CLAUDE.md
  - Corrected cross-references in documentation

## [0.47.2] - 2026-01-14

### Added

- **Dolt backend (experimental)** - Version-controlled issue storage with time-travel
  - `bd init --backend=dolt` enables Dolt-based storage
  - Full version history for issues with branch/merge semantics
  - See docs/DOLT.md for migration guide

- **`bd show --children` flag** - Display child issues inline with parent
  - Shows hierarchical structure in issue details

- **Comprehensive NixOS support** - Improved Nix flake and home-manager integration
  - Better daemon handling in Nix environments
  - Updated flake.nix with proper dependencies

### Changed

- **Release workflow modernized** - bump-version.sh replaced with molecule pointer
  - Use `bd mol wisp beads-release --var version=X.Y.Z` for releases
  - New `scripts/update-versions.sh` for quick local version bumps

### Fixed

- **Redirect + sync-branch incompatibility** - bd sync works correctly in redirected repos (bd-wayc3)
  - Worktree git status failures resolved
  - Proper handling of .beads/redirect during sync operations

- **Doctor project-level settings** - Detects plugins/hooks/MCP in .claude/settings.json
  - No longer misses project-scoped configurations

- **Contributor routing** - `bd init --contributor` correctly sets up routing (#1088)
  - Fork workflows now properly configure sync.remote=upstream

### Documentation

- **EXTENDING.md deprecated** - Custom SQLite tables approach deprecated for Dolt migration
  - External tools pattern recommended for integrations

## [0.47.1] - 2026-01-12

### Added

- **`bd list --ready` flag** - Show only issues with no blockers (bd-ihu31)
  - Filters to issues that are immediately actionable
  - Equivalent to `bd ready` but integrated into list command

- **Markdown rendering in comments** - Comment text now renders Markdown (#1019)
  - Enhanced readability for formatted notes and descriptions

### Changed

- **Release formula improvements** - Updated beads-release formula with v0.47.0 learnings
  - Better gate handling and step organization

### Fixed

- **Nil pointer in wisp create** - Prevent panic when creating wisps (mol)
  - Fixed nil pointer dereference in molecule creation

- **Route prefix for rig issues** - Use correct prefix when creating issues in rigs (#1028)
  - Issues created in rigs now use the proper routing prefix

- **Duplicate merge target selection** - Prefer issues with children/deps (GH#1022)
  - Better heuristics for choosing merge target in duplicate detection

- **SQLite cache rebuild** - Rebuild blocked_issues_cache after rename-prefix (GH#1016)
  - Ensures cache consistency after prefix changes

- **Doctor JSONL check** - Exclude sync_base.jsonl from multiple files check (#1021)
  - Reduces false positives in doctor diagnostics

- **Merge struct completeness** - Add QualityScore field to merge Issue struct
  - Ensures all fields preserved during merge operations

- **MCP custom types** - Support custom issue types and statuses in MCP (#1023)
  - MCP server now handles non-built-in types correctly

- **Hyphenated prefix validation** - Support hyphens in ValidateIDFormat (#1013)
  - Prefixes like `my-project-` now validate correctly

- **Git worktree initialization** - Prevent bd init inside git worktrees (#1026)
  - Avoids configuration issues when initializing in worktree directories

### Documentation

- **bd reset clarification** - Document command behavior and workarounds (GH#922)
  - Clearer guidance on reset command usage

## [0.47.0] - 2026-01-11

### Added

- **Pull-first sync with 3-way merge** - Major sync improvement (#918)
  - Reconciles local changes with remote updates before pushing
  - Field-level conflict merging reduces manual intervention
  - Base state tracking for better change detection

- **`bd resolve-conflicts` command** - Resolve JSONL merge conflict markers (bd-7e7ddffa)
  - Mechanical mode uses updated_at timestamps for deterministic resolution
  - Closed status wins over open, higher priority wins
  - Notes concatenated, dependencies unioned
  - Dry-run mode and JSON output for agent integration

- **`bd create --dry-run`** - Preview issue creation without side effects (bd-0hi7)
  - Shows what would be created in human-readable or JSON format
  - Works with --rig/--prefix flags

- **Gate auto-discovery** - Auto-discover workflow run ID in `bd gate check` (bd-fbkd)
  - Queries GitHub directly when await_id is a workflow name hint
  - ZFC-compliant: takes most recent run deterministically

- **Linear project filter** - `linear.project_id` config for sync (#938)
  - Fetch only issues from a specific project instead of all team issues

- **`bd ready --gated`** - Gate-resume discovery for molecules (bd-lhalq)
  - Find molecules waiting on gates for automatic resumption

- **Multi-repo custom types** - Trust and discover types across repositories (bd-62g22, bd-9ji4z)
  - `bd doctor` discovers custom types from multiple repos
  - Non-built-in types trusted during hydration

- **Visual UX improvements** - Enhanced display for list tree, graph, and show commands
  - Better formatting and readability

- **Stale database handling** - AllowStale option in List API (bd-dpkdm)
  - Read-only commands auto-import on stale DB (#977, #982)
  - Cold-start bootstrap for read commands

- **Batch molecule operations** - `bd mol burn` supports multiple molecules (feat(mol))

- **Redirect health checks** - `bd doctor` validates redirect configurations

- **Schema extensions** - New fields for HOP integration
  - `crystallizes` column in sqlite storage
  - `attests` edge type for skill attestations
  - `owner` field for human attribution
  - `actor` fallback includes git user.name (#994)

### Fixed

- **Daemon mode completeness** - Several daemon mode gaps closed (GH#952)
  - `--due` and `--defer` flags now work in daemon mode (#953)
  - `bd dep add/remove --json` returns proper JSON output (#961)
  - `DeferUntil` field parsed correctly in daemon handleCreate (#950)
  - Silence deprecation warnings in `--json` mode (#1039a691)

- **Sync robustness**
  - Canonicalize dbPath to fix filepath.Rel errors (GH#959, #960)
  - Validate custom types in batch issue creation (#943)
  - Force-add .beads in worktree for contributor mode (#947)
  - Initialize store after daemon disconnect (GH#984)
  - `sync --import-only` works when daemon was connected

- **Windows fixes**
  - Infinite loop in findLocalBeadsDir/findOriginalBeadsDir (GH#996)
  - `bd init` no longer hangs when not in a git repo (#991)
  - Daemon stop/kill uses proper Windows API (GH#992)
  - SQLite uses DELETE mode on WSL2 Windows filesystem (GH#920)

- **Daemon socket handling** - Long workspace paths now work (GH#1001, #1008)
  - Socket path shortening for deep directory structures
  - Relocate daemon socket for deep paths

- **Prevent data corruption**
  - FK constraint failures on batch/concurrent issue creation (GH#956)
  - Prevent closing issues with open blockers (GH#962)
  - Nil pointer panic in dep --json mode (GH#998)

- **Doctor improvements**
  - Recognize bd shims when external manager config exists (GH#946)
  - Detect lefthook jobs syntax (GH#981)
  - Add .sync.lock and sync_base.jsonl to gitignore (#980)

- **Prime command** - Use flush-only workflow when no git remote configured (#940)

- **Install safety** - Stop existing daemons before binary replacement (#945)

- **Git hooks** - Add `--no-daemon` to sync commands to prevent inline import failures (#948)

- **Linear sync** - Use project_id when creating issues via `sync --push` (GH#973, #1012)

- **Team wizard** - Validate sync.branch in wizard and migrate commands (GH#923)

- **Worktree fixes**
  - Skip beads restore when directory is redirected (bd-lmqhe)
  - Migrate sync works in git worktree environments (#970)

- **Misc fixes**
  - `bd edit` parses EDITOR with args (GH#987)
  - Use SearchIssues for ID resolution (GH#942)
  - Respect hierarchy.max-depth config setting (GH#995, #997)
  - Add timeout to daemon request context to prevent hangs
  - Avoid null values in Claude settings hooks (GH#955)
  - Restore Gas Town types (agent, role, rig, convoy, slot) (GH#941)
  - Add TypeRig constant and IsBuiltIn method (GH#1002)

### Changed

- **Daemon CLI refactor** - Consolidated to subcommands with semantic styling (#1006)

- **Release formula refactor** - Bump script broken into individual version-update steps (bd-a854)
  - More durable: can resume from specific step if interrupted
  - Better visibility in activity feed

### Documentation

- Add lazybeads (Bubble Tea TUI by @codegangsta) to community tools (#951)
- Fix `bd quickstart` link to database extension documentation (#939)

## [0.46.0] - 2026-01-06

### Added

- **Custom type support** - Configure custom issue types in beads config.yaml (bd-649s)
  - Define project-specific types beyond the built-in set
  - Types persist across sync and export

- **Gas Town types extraction** - Core Gas Town types moved into beads package (bd-i54l)
  - Enables beads to understand rig identities and agent workflows
  - Foundation for deeper Gas Town integration

### Fixed

- **Gate workflow discovery** - Handle workflow name hints in `gh:run` gate discovery (bd-m8ew)
  - Better matching of GitHub Actions workflow runs
  - Consolidated numeric ID handling

## [0.45.0] - 2026-01-06

### Added

- **Dynamic shell completions** - Tab completion for issue IDs in bash/zsh/fish (#935)
  - Optimized prefix filtering for faster completion
  - Added completions to more commands

- **Android/Termux support** - Native ARM64 binaries for Android (#887)

- **Deep pre-commit framework integration** - `bd doctor` checks pre-commit hook configs (bd-28r5)

- **Rig identity bead type** - New `rig` bead type for Gas Town rig tracking (gt-zmznh)

- **`--filter-parent` alias** - Alternative to `--parent` in `bd list` (bd-3p4u)

- **Unified auto-sync config** - Simpler daemon configuration for agent workflows (#904)

- **BD_SOCKET env var** - Test isolation for daemon socket paths (#914)

### Fixed

- **External hook manager detection** - `bd doctor` now detects lefthook, husky, pre-commit, and other hook managers
  - Checks if external managers have `bd hooks run` integration configured
  - Reports which hooks have bd integration vs which are missing
  - `bd doctor --fix` uses `--chain` flag when external managers detected to preserve existing hooks
  - Supports YAML, TOML, and JSON config formats for lefthook
  - Detects active manager from git hooks when multiple managers present

- **Init branch persistence** - `--branch` flag now correctly persists to config.yaml (#934)

- **Worktree resolution** - Resolve worktrees by name from git registry (#921)

- **Sync with redirect** - Use inline import for `--import-only` with .beads/redirect (bd-ysal)
  - Handle .beads/redirect in git status checks (bd-arjb)
  - Persist sync branch to yaml and database (GH#909)
  - Atomic export and force-push detection (bd-3bhl, bd-4hh5)

- **Doctor improvements**
  - Recognize lowercase 's' skip-worktree flag (#931)
  - Improve messaging for detection-only hook managers (bd-par1)
  - Align duplicate detection with `bd duplicates` (bd-sali)
  - Query metadata table instead of config for last_import_time (#916)

- **Update prefix routing** - `bd update` now routes like `bd show` (bd-618f)

- **Formula phase** - beads-release formula marked as vapor phase (gt-mjsjv)

- **Lint fixes** - Address gosec, misspell, and unparam warnings

- **CI improvements** - Windows smoke tests, timeouts, golangci-lint exclusions

### Changed

- **Test refactoring** - Replace testify with stdlib in daemon tests (#936)

### Documentation

- Add bun installation method to INSTALLING.md (#930)
- Add Parade app to community tools list

## [0.44.0] - 2026-01-04

### Added

- **Recipe-based setup architecture** - Major refactor of `bd init` (bd-i3ed)
  - Modular, composable setup via recipes
  - Cleaner separation of init concerns

- **Gate evaluation system** - Phases 2-4
  - `bd gate check` for timer and GitHub gate evaluation (GH#884)
  - `bd gate discover` for auto-discovery of gh:run await_id (bd-z6kw)
  - `bd gate add-waiter` and `bd gate show` for phase handoff
  - Cross-rig bead gate support
  - Gate-aware beads-release.formula v2 (bd-r24e)
  - Merge-slot gate for serialized conflict resolution

- **Dependency command improvements**
  - `--blocks` shorthand flag for natural dependency syntax (GH#884)
  - `--blocked-by` and `--depends-on` flag aliases (bd-09kt)

- **Multi-prefix support** - `allowed_prefixes` config option (#881)
  - Configure multiple valid prefixes per rig
  - Enables flexible issue routing

- **Sync improvements**
  - Detect uncommitted JSONL changes before sync (GH#885)
  - `bd doctor` sync divergence check for JSONL/SQLite/git
  - `BD_DEBUG_SYNC` env for protection debugging

- **`PRIME.md` override** - Workflow customization (GH#876)
  - Custom prime output per project

- **Daemon config** - `daemon.auto_*` settings in config.yaml (GH#871)

- **Compound visualization** - `bd mol show` displays compound structure (bd-iw4z)

- **`/handoff` skill** - Session cycling slash command (bd-xwvo)

### Fixed

- **`bd ready` now shows in_progress issues** (#894)
  - Previously filtered out your active work

- **`bd show` displays external_ref field** in text output (#899)

- **macOS case-insensitive path handling** (GH#880)
  - Worktree validation, daemon paths, git operations
  - Canonicalize path case for consistency

- **Sync metadata timing** - Finalize after commit, not push (GH#885)
  - Defer SQLite metadata updates until after git commit
  - Prevents sync state corruption

- **Sparse checkout isolation** - Prevent config leaking to main repo (GH#886)
  - Disable sparse checkout on main repo after worktree creation

- **`close_reason` preserved** during merge/sync (GH#891)

- **Parent hub contamination** during `bd init` (GH#896)

- **NoDb mode** - Set cmdCtx.StoreActive correctly (GH#897)

- **Event storm prevention** when gitRefsPath is empty (#883)

- **Doctor improvements**
  - Detect status mismatches between DB and JSONL (GH#885)
  - Detect missing git repo, improve daemon startup message (#890)
  - Skip JSONL tracking checks in sync-branch mode (GH#858)

- **`bd rename-prefix`** - Sync JSONL before and after (#893)

- **`sync.remote` config** now respected in `bd sync` (GH#872)

- **Snapshot protection** made timestamp-aware (GH#865)

- **Hyphenated rig names** supported in agent IDs (GH#854, GH#868)

- **Cross-repo agent routing** (#864)

- **Config key normalization** - GetYamlConfig matches SetYamlConfig (#874)

- **Daemon startup failure** - Propagate reason to user (GH#863)

- **Molecule variable substitution** in root bead title/desc

- **Submodule detection** - Correct main repo root detection (#849)

- **`bd merge` output** sorted by issue id (#859)

- **Auto-create agent bead** when `bd agent state` called on non-existent agent

- **Windows zip extraction** - Add retry logic for npm install

- **Cycle detection** runs in daemon mode for --blocks flag

- **`bd slot set`** cross-beads prefix routing (bd-hmeb)

- **`git status` noise** - Hide issues.jsonl when sync.branch configured (GH#870)

### Changed

- **Skill names** updated from `/bd-*` to `/beads:*` (#862)

### Performance

- **Batch external dep checks** by project (bd-687v)

### Internal

- Extract warnIfCyclesExist helper
- Gate field parsing and creation tests
- Sync unit tests for gitHasUncommittedBeadsChanges
- SQLite cache rebuild benchmarks
- Repository guards in deployment workflows

## [0.43.0] - 2026-01-02

### Added

- **Step.Gate evaluation** - Phase 1: Human Gates
  - Gate steps that require human approval before proceeding
  - Foundation for workflow control points

- **`bd lint` command** - Template validation
  - Validate issue templates against schema
  - `--validate` flag also added to `bd create`

- **`bd ready --pretty`** - Formatted output
  - Human-friendly display of ready work

### Fixed

- **Cross-rig routing** - `bd close` and `bd update` now support cross-rig operations via prefix routing
- **Agent ID validation** - Now accepts any rig prefix (GH#827)
- **`bd sync` in bare repo worktrees** - Fixed exit 128 error (GH#827)
- **`bd --no-db dep tree`** - Now shows complete tree (GH#836)
- **`.beads/last-touched`** - Restored to gitignore template (GH#838)

## [0.42.0] - 2025-12-30

### Added

- **`llms.txt` standard support** - AI agent discoverability (#784)
  - Standard endpoint for AI agents to discover project info
  - Helps LLM-powered tools understand your project

- **`bd preflight` command** - PR readiness checks
  - Static checklist for pre-commit/pre-push verification
  - Phase 1 implementation with test, lint, and version checks planned

- **`--claim` flag for `bd update`** - Work queue semantics
  - Atomic claim-and-assign for multi-agent coordination
  - Prevents race conditions in work assignment

- **`bd state` and `bd set-state`** - Label-based state management
  - Helper commands for operational state patterns
  - Documents labels-as-state pattern

- **`bd activity --town`** - Cross-rig activity feed
  - Aggregated view across all rigs in town
  - Better visibility into multi-rig workflows

- **Convoy issue type** - Reactive completion tracking
  - Issues that complete when all tracked items complete
  - `tracks` relation type for convoy membership

- **Agent identity trailers** - prepare-commit-msg hook
  - Automatically adds agent identity to commits
  - Structured labels for agent beads

- **Daemon RPC endpoints** - Config and mol stale
  - Remote config queries via daemon
  - Stale molecule detection endpoint

- **Non-TTY auto-detection** - Cleaner output in pipes
  - Automatically adjusts output for non-interactive use

### Fixed

- **Git hook chaining** now works correctly (GH#816)
- **`.beads/redirect` not committed** - Prevents worktree conflicts (GH#814)
- **`bd sync` with sync-branch** - Fixes worktree copy direction (GH#810, #812)
- **`sync.branch` validation** - Rejects main/master as sync branch (GH#807)
- **Read operations read-only** - No more database writes on list/ready/show (GH#804)
- **`bd list` defaults** - Non-closed issues, 50 limit to protect context (GH#788)
- **External direct-commit bypass** - When sync.branch configured (bd-n663)
- **Migration 022 syntax** - SQL error on v0.30.3 upgrade path
- **MCP redirect support** - Plugin follows .beads/redirect files
- **Jira sync error** - Better message when Python script not found (GH#803)
- **Doctor false positives** - For molecule/wisp prefix variants
- **BD_ACTOR in direct mode** - Consistent actor handling
- **Label accumulation** - When updating agent role_type/rig

## [0.41.0] - 2025-12-29

### Added

- **`bd swarm` commands** - Multi-agent batch work coordination
  - `bd swarm create` - Create swarm from epic with children
  - `bd swarm status` - Show swarm progress and blocked work
  - `bd swarm validate` - Validate swarm structure and dependencies
  - N+1 query fix for blocked checks (performance optimization)

- **`bd repair` command** - Orphaned reference repair
  - Detect and repair orphaned foreign key references
  - Support for comments/events orphan detection
  - `--json` flag for machine-readable output
  - Transaction safety with backup and dirty_issues marking

- **`bd compact --purge-tombstones`** - Dependency-aware cleanup
  - Remove tombstones while respecting dependency order
  - Safe cleanup that won't break DAG structure

- **`bd init --from-jsonl`** - Manual cleanup preservation
  - Initialize database from curated JSONL file
  - Preserves manual edits made to JSONL

- **`bd human` command** - Focused help menu
  - Human-friendly command reference
  - Quick overview without CLI verbosity

- **`bd show --short`** - Compact output mode
  - Brief issue summary for scripting
  - Less verbose than default format

- **`bd delete --reason`** - Audit trail for deletions
  - Optional reason stored in activity log
  - Better traceability for issue cleanup

- **`hooked` status** - Hook-based work assignment
  - New status for issues assigned to agent hooks
  - Enables autonomous agent work pickup

- **`mol_type` schema field** - Molecule classification
  - Track molecule type (patrol, work, etc.)
  - New migration (adds schema field)

- **Agent ID canonical naming** - Validation update
  - Updated validation for orchestrator naming conventions
  - Supports rig/role/name format

### Fixed

- **`--var` flag allows commas in values** (GH#786)
  - Variables like `--var files=a.go,b.go` now work correctly
  - Parser respects quoted values

- **`bd sync` in bare repo worktrees** (GH#785)
  - Fixed sync failure when working in worktrees of bare repos
  - Correctly detects git configuration

- **`bd delete --cascade` recursive deletion** (GH#787)
  - Now properly deletes all transitive dependents
  - Previously only deleted direct children

- **`bd doctor` pre-push hook detection** (GH#799)
  - No longer falsely reports hook issues
  - Correctly identifies bd-managed hooks

- **Gitignore fork protection** (GH#796)
  - Removed negations that could override protection
  - Safer fork handling

- **Illumos/Solaris disk space check** (GH#798)
  - Added platform support for disk space detection
  - Expands OS compatibility

- **Pre-migration orphan cleanup** - Chicken-and-egg fix
  - Clean orphans before migration to avoid failures
  - Smoother upgrade path

- **`hq-` prefix routing** - Town root discovery
  - Correctly finds routes.jsonl from anywhere in town
  - Fixes cross-rig routing for HQ beads

- **Config.yaml database override warning**
  - Shows warning when config overrides db location
  - Helps debug unexpected behavior

- **`normalizeBeadsRelPath` edge case** - Similar prefix handling
  - Fixes path normalization for similar prefix names
  - e.g., `beads` vs `beads-sync`

- **`bd doctor --fix` redirect handling**
  - Properly follows .beads/redirect files
  - Limited verbose output for cleaner runs

### Changed

- **CLI command consolidation** - Reduced surface area
  - Grouped related commands under parent commands
  - Cleaner `bd --help` output

- **Code organization** - File size limits
  - Split large cmd/bd files to meet 800-line limit
  - init.go: 1928 → 705 lines
  - Improved maintainability

- **Documentation updates**
  - Replace Epic Planning with Ready Front model
  - Add components overview (CLI vs Plugin vs MCP)
  - Add installation method comparison table

### Internal

- Extract IssueDetails to shared type
- Export FollowRedirect and consolidate implementations
- Extract shared getEpicChildren helper for swarm commands
- Extract hashFieldWriter to reduce ComputeContentHash repetition
- Break up runCook (275 lines) into focused helpers
- Break up flushToJSONLWithState (280 lines) into focused helpers
- Extract shared importFromJSONLData function
- Consolidate duplicated step collection functions
- Add git helper and guard annotations for tests
- Fix golangci-lint errors (errcheck, gosec, unparam)

## [0.40.0] - 2025-12-28

### Added

- **`bd worktree` command** - Parallel development support
  - Manage git worktrees with beads integration
  - Enables working on multiple issues simultaneously

- **`bd slot` commands** - Agent bead slot management
  - Track agent assignments with dedicated slot operations
  - Supports multi-agent orchestration workflows

- **`bd agent state` command** - ZFC-compliant state reporting
  - Report agent state in standardized format
  - Enables machine-readable status checks

- **`bd doctor --deep`** - Full graph integrity validation
  - Deep validation of bead dependency graphs
  - Includes agent bead integrity checks

- **Agent bead support** - New bead types
  - `type=agent` and `type=role` for agent tracking
  - Agent-specific fields in schema (migration 030)
  - Agent ID pattern validation on create

- **Computed `.parent` field** - JSON output enhancement
  - Parent field included in JSON for convenience
  - Simplifies integration with external tools

- **Auto-bypass daemon for wisp operations** - Performance
  - Ephemeral wisp operations skip daemon overhead
  - Faster patrol cycle processing

- **Pour warning for vapor-phase formulas** - Safety
  - Warning when pouring formulas designed for wisps
  - Prevents accidental persistent molecule creation

### Fixed

- **O(2^n) → O(V+E) cycle detection** (GH#775) - Performance
  - Replaced exponential algorithm with linear DFS
  - Dramatic speedup for large dependency graphs

- **Import hash mismatch warnings** - Data integrity
  - Update jsonl_file_hash on import operations
  - Prevents spurious hash mismatch warnings

### Deprecated

The following commands are deprecated and will be removed in v1.0.0:

- **`bd relate`** → use `bd dep relate` instead
- **`bd unrelate`** → use `bd dep unrelate` instead
- **`bd daemons`** → use `bd daemon <subcommand>` instead
- **`bd cleanup`** → use `bd admin cleanup` instead
- **`bd compact`** → use `bd admin compact` instead
- **`bd reset`** → use `bd admin reset` instead
- **`bd comment`** → use `bd comments add` instead
- **`bd template`** → use `bd mol` instead
- **`bd templates`** → use `bd formula list` instead
- **`bd template show`** → use `bd mol show` instead
- **`bd template bond`** → use `bd mol bond` instead
- **`bd detect-pollution`** → use `bd doctor --check=pollution` instead
- **`bd migrate-hash-ids`** → use `bd migrate hash-ids` instead
- **`bd migrate-tombstones`** → use `bd migrate tombstones` instead
- **`bd migrate-sync`** → use `bd migrate sync` instead
- **`bd migrate-issues`** → use `bd migrate issues` instead

All deprecated commands continue to work but print a warning. Update your scripts
and muscle memory before v1.0.0 to avoid breakage.

### Changed

- **Community tools documentation** (GH#772, GH#776)
  - Consolidated into dedicated docs page
  - Added opencode-beads to community tools list

- **Activity feed improvements** - Better context
  - Shows title and assignee in activity entries
  - More informative `bd activity` output

## [0.39.1] - 2025-12-27

### Added

- **`bd where` command** - Show active beads location
  - Displays the resolved database path after following redirects
  - Helpful for debugging multi-rig setups

- **`--parent` flag for `bd update`** - Reparent issues
  - Move issues between epics with `bd update <id> --parent=<new-parent>`
  - Supports clearing parent with `--parent=none`

- **Redirect info in `bd prime`** - Multi-clone support
  - Shows when database is redirected to another location
  - Improves visibility into routing behavior

### Fixed

- **Doctor follows redirects** - Multi-clone compatibility
  - `bd doctor` now correctly follows database redirects
  - Prevents false negatives when running from rig roots

- **Remove 8-char prefix limit** (GH#770) - `bd rename-prefix`
  - Removed arbitrary length restriction on issue prefixes
  - Allows longer, more descriptive prefixes

### Changed

- **Git context consolidation** - Internal refactor
  - Unified git context into single cached struct
  - Reduces redundant git operations

### Documentation

- **Database Redirects section** - ADVANCED.md
  - Comprehensive documentation for redirect feature
  - Explains multi-clone integration patterns

- **Community Tools update** (GH#771) - README.md
  - Added opencode-beads to community tools list

## [0.39.0] - 2025-12-27

### Added

- **`bd orphans` command** (GH#767) - Detect orphaned issues
  - Finds issues mentioned in git commits that were never closed
  - DRY refactoring of orphan detection from `bd doctor`
  - Helps maintain issue hygiene in larger projects

- **`bd admin` parent command** - Consolidated admin tools
  - `bd admin cleanup` - Clean up unused data
  - `bd admin compact` - Compact the database
  - `bd admin reset` - Reset to initial state
  - Reduces top-level command clutter

- **`--prefix` flag for `bd create`** - Cross-rig issue creation
  - Create issues in other rigs from any directory
  - Accepts prefix (`bd-`), short form (`bd`), or rig name (`beads`)
  - Works with existing routes.jsonl routing

### Changed

- **`bd mol catalog` → `bd formula list`** - Command rename
  - Aligns with formula-based terminology
  - `bd formula list` shows available molecule templates

- **`bd info --thanks`** - Relocated thanks command
  - Contributors list moved under `bd info`
  - Reduces command namespace pollution

- **Removed unused commands**
  - `bd pin`, `bd unpin`, `bd hook` removed
  - Functionality covered by orchestrator molecule commands
  - Cleaner separation between beads (data) and orchestration

- **`bd doctor --check=pollution`** - Integrated test pollution check
  - Detects test artifacts left in production database
  - Previously standalone `bd detect-pollution` command

### Fixed

- **macOS codesigning** - Fixed `bump-version.sh --install`
  - Ad-hoc signing for local installations
  - Prevents macOS from quarantining the binary

- **Lint errors and Nix vendorHash** (GH#769)
  - Resolved golangci-lint issues
  - Updated Nix package hash for reproducible builds

### Documentation

- **Issue Statuses section** - CLI_REFERENCE.md
  - Comprehensive status lifecycle documentation
  - Clear explanations of open, pinned, in_progress, blocked, deferred, closed, tombstone

- **Consolidated UI_PHILOSOPHY files** (GH#745)
  - Merged duplicate philosophy docs
  - Single source of truth for UI design principles

- **README and PLUGIN.md fixes** (GH#763)
  - Corrected installation instructions
  - Updated plugin documentation

## [0.38.0] - 2025-12-27

### Added

- **Prefix-based routing** - Cross-rig command routing
  - `bd` commands auto-route to correct rig based on issue ID prefix
  - Routes stored in `~/gt/.beads/routes.jsonl`
  - Enables seamless multi-rig workflows from any directory

- **Cross-rig ID auto-resolve** - Smarter dependency handling
  - `bd dep add` auto-resolves issue IDs across different rigs
  - No need to specify full paths for cross-rig dependencies

- **`bd mol pour/wisp` subcommands** - Reorganized command hierarchy
  - `bd mol pour` for persistent molecules
  - `bd mol wisp` for ephemeral workflows
  - Cleaner organization under `bd mol` namespace

- **Comments display in `bd show`** (GH#177) - Enhanced issue details
  - Comments now visible in issue output
  - Shows full discussion thread for issues

- **`created_by` field on issues** (GH#748) - Creator tracking
  - Track who created each issue for audit trail
  - Useful for multi-agent workflows

- **Database corruption recovery** (GH#753) - Robust doctor repairs
  - `bd doctor --fix` can now auto-repair corrupted SQLite databases
  - JSONL integrity checks detect and fix malformed entries
  - Git hygiene checks for stale branches

- **Chaos testing for releases** - Thorough validation
  - `--run-chaos-tests` flag in release script
  - Exercises edge cases and failure modes

- **Pre-commit config** - Local lint enforcement
  - Consistent code quality before commits

### Changed

- **CLI consolidation** - Reduced top-level command clutter
  - `bd cleanup`, `bd compact`, `bd reset` → `bd admin cleanup|compact|reset`
  - `bd migrate-*` → `bd migrate hash-ids|issues|sync|tombstones`
  - `bd cook` → `bd formula cook`
  - `bd detect-pollution` → `bd doctor --check=pollution`
  - `bd quickstart` hidden, use docs/QUICKSTART.md instead
  - Hidden aliases maintain backwards compatibility with deprecation notices

- **Sync backoff and tips consolidation** (GH#753) - Smarter daemon
  - Daemon uses exponential backoff for sync retries
  - Tips consolidated from multiple sources

- **Wisp/Ephemeral naming finalized** - `wisp` is canonical
  - `bd mol wisp` is the correct command
  - Internal API uses "ephemeral" but CLI uses "wisp"

### Fixed

- **Comments display position** (GH#756) - Formatting fix
  - Comments now display outside dependents block
  - Proper visual hierarchy in `bd show` output

- **no-db mode storeActive** (GH#761) - JSONL-only fix
  - `storeActive` correctly set in no-database mode
  - Fixes issues with JSONL-only installations

- **`--resolution` alias** (GH#746) - Backwards compatibility
  - Restored `--resolution` as alias for `--reason` on `bd close`

- **`bd graph` with daemon** (GH#751) - Daemon compatibility
  - Graph generation works when daemon is running
  - No more conflicts between graph and daemon operations

- **`created_by` in RPC path** (GH#754) - Daemon propagation
  - Creator field correctly passed through daemon RPC

- **Migration 028 idempotency** (GH#757) - Safe re-runs
  - Migration handles partial or repeated runs gracefully
  - Checks for existing columns before adding

- **Routed ID daemon bypass** - Cross-rig show
  - `bd show` with routed IDs bypasses daemon correctly
  - Storage connections closed per iteration to prevent leaks

- **Modern git init** (GH#753) - Test compatibility
  - Tests use `--initial-branch=main` for modern git versions

- **golangci-lint clean** (GH#753) - All platforms
  - Resolved all lint errors across platforms

### Improved

- **Test coverage** - Comprehensive testing
  - Doctor, daemon, storage, and RPC client paths covered
  - Chaos testing integration for edge cases

## [0.37.0] - 2025-12-26

### Added

- **Gate commands** - Async coordination for agent workflows
  - `bd gate create` - Create gates with await conditions (gh:run, gh:pr, timer, human, mail)
  - `bd gate eval` - Evaluate timer and GitHub gates (checks run completion, PR merge)
  - `bd gate approve` - Approve human gates
  - `bd gate show/list/close/wait` - Full gate lifecycle management

- **`bd close --suggest-next`** (GH#679) - Smart workflow continuation
  - Shows newly unblocked issues after closing
  - Helps agents find next actionable work

- **`bd ready/blocked --parent`** (GH#743) - Epic-scoped filtering
  - Filter issues by parent bead or epic
  - Enables focused work within a subtree

- **TOML support for formulas** - Alternative format
  - `.formula.toml` files alongside JSON support
  - Human-friendly authoring option

- **Fork repo auto-detection** (GH#742) - Contributor workflow
  - Detects fork repositories during init
  - Offers to configure .git/info/exclude for stealth mode

- **Control flow operators** - Advanced formula composition
  - `loop` operator for iterating over collections
  - `gate` operator for conditional execution
  - Condition evaluator for gates and loops
  - Control flow integrates with aspect composition

- **Aspect composition** - Cross-cutting workflow concerns
  - `aspects` field in formulas for reusable patterns
  - Aspects match steps by type, title pattern, or custom criteria
  - Enables logging, retries, notifications as composable concerns

- **Runtime expansion types** - Dynamic step generation
  - `on_complete` - Generate steps when a step completes
  - `for-each` - Expand steps for each item in a collection
  - Enables dynamic workflows based on runtime state

- **`bd formula list/show`** - Formula discovery commands
  - `bd formula list` - List available formulas in search paths
  - `bd formula show <name>` - Display formula definition and metadata

- **`bd mol stale`** - Detect complete-but-unclosed molecules
  - Finds molecules where all children are closed but molecule is open
  - Helps identify forgotten cleanup tasks

- **Stale molecules check in doctor** - Proactive detection
  - `bd doctor` now warns about stale molecules
  - Integrated with `bd mol stale` detection

- **Distinct ID prefixes** - Clear entity type identification
  - Protos: `bd-proto-xxx` prefix
  - Molecules: `bd-mol-xxx` prefix
  - Wisps: `bd-wisp-xxx` prefix
  - Makes entity type immediately visible in IDs

- **`no-git-ops` config** (GH#593) - Manual git control
  - `bd config set no-git-ops true` to disable auto git operations
  - `bd prime` outputs stealth-mode protocol when enabled
  - Useful for custom git workflows or manual commit review

### Changed

- **Formula format: YAML → JSON** - Standardized format
  - Formulas now use `.formula.json` extension
  - JSON provides better tooling support and validation
  - Existing YAML formulas need migration

- **Removed `bd mol run`** - Orchestration delegated to orchestrator
  - Molecule execution now handled by orchestrator commands
  - `bd` focuses on issue tracking primitives
  - Use orchestrator's molecule runner for execution

- **Simplified wisp architecture** - Single database model
  - Wisps stored in main database with `Wisp=true` flag
  - Removed separate `.beads-wisp/` directory
  - Wisps filtered from JSONL export automatically

### Fixed

- **Gate await fields preserved during upsert** - Multirepo sync
  - Gate fields no longer cleared during multi-repo sync operations
  - Enables reliable gate coordination across clones

- **Tombstones retain closed_at timestamp** - Soft delete metadata
  - Tombstones preserve the original close time
  - Maintains accurate timeline for deleted issues

- **Git detection caching** - Performance improvement
  - Cache git worktree/repo detection results
  - Eliminates repeated slowness on worktree checks

- **Windows MCP graceful fallback** (GH#387) - Platform compatibility
  - Daemon mode falls back gracefully on Windows
  - Prevents crashes when daemon unavailable

- **Windows npm postinstall file locking** (GH#670) - Install reliability
  - Handles file lock errors during npm install
  - Improves installation success on Windows

- **installed_plugins.json v2 format** (GH#741) - Claude Code compatibility
  - `bd doctor` now handles both v1 and v2 plugin file formats
  - Fixes "cannot unmarshal array" error on newer Claude Code versions

- **git.IsWorktree() hang on Windows** (GH#727) - Init safety
  - Added isGitRepo() guard before calling git.IsWorktree()
  - Fixes `bd init` hanging outside git repositories on Windows

- **Skill files deleted by bd sync** (GH#738) - Sync safety
  - `bd sync` no longer deletes skill files in .claude/
  - Prevents accidental removal of Claude Code configuration

- **doctor check false positives** (GH#709) - Cleaner diagnostics
  - Skips interactions.jsonl and molecules.jsonl in sync checks
  - These files are runtime state, not sync targets

- **FatalErrorRespectJSON** - Consistent error output
  - All commands respect `--json` flag for error output
  - Errors return proper JSON structure when flag is set

- **bd sync commits non-.beads files** - Sync isolation
  - Sync operations now only commit .beads/ directory changes
  - Prevents unintended commits of working directory files

- **Content-level merge for divergence** - Better conflict resolution
  - Divergence recovery uses content-level merging
  - Reduces false conflicts during multi-clone sync

- **Child→parent dep fix opt-in** - Migration control
  - `--fix-child-parent` flag required for automatic fix
  - Prevents unexpected dependency modifications

- **Aspect self-matching recursion** - Formula safety
  - Aspects cannot match themselves during expansion
  - Prevents infinite recursion in aspect composition

- **Map expansion nested matching** - Correct child handling
  - Map expansion now correctly matches nested child steps
  - Fixes missing expansions in hierarchical formulas

- **Mol/wisp ID prefix combination** - Correct ID generation
  - Database prefix combined with type prefix correctly
  - IDs like `bd-mol-xxx` instead of `bdmol-xxx`

- **mol run hierarchical children** (bd-c8d5, bd-drcx) - Complete loading
  - `bd mol run` loads all hierarchical children
  - Supports title lookup for molecule identification

- **Wisps excluded from sync** (bd-687g, bd-9avq) - Export correctness
  - Wisps filtered from JSONL export and nodb operations
  - Ephemeral molecules don't pollute shared state

### Documentation

- **MOLECULES.md rewrite** - Workflow-first structure
  - Reorganized around practical workflows
  - Clearer distinction between protos, molecules, wisps

- **Molecular chemistry docs** - Conceptual foundation
  - Phase metaphor: solid (proto) → liquid (mol) → vapor (wisp)
  - Pour, bond, distill, squash operations explained

## [0.36.0] - 2025-12-24

### Added

- **Formula system** (bd-weu8, bd-wa2l) - Declarative workflow templates
  - `bd cook <formula>` - Execute a formula template with variable interpolation
  - Formula files (`.formula.json`) support inheritance via `extends`
  - `needs` and `waits_for` fields for dependency declarations
  - `--prefix` flag for custom issue prefix when cooking
  - Search paths: `.beads/formulas/`, `~/.beads/formulas/`, `~/gt/.beads/formulas/`

- **Gate issue type** - Async coordination primitives
  - `bd gate create <name>` - Create a gate for coordinating parallel work
  - `bd gate open <id>` - Open a gate (unblock waiters)
  - `bd gate close <id>` - Close a gate (block new work)
  - Gates integrate with `waits-for` dependencies for fanout patterns

- **`bd list` viewer enhancements** (#729) - Built-in terminal UI
  - `--pretty` - Colorized, formatted output for human viewing
  - `--watch` - Live-updating view (refreshes on changes)

- **`bd search` filters** - Enhanced search capabilities
  - `--after`, `--before` - Date range filtering
  - `--priority` - Exact priority match
  - `--content` - Full-text content search

- **`bd compact --prune`** - Standalone tombstone pruning
  - Prune old tombstones without full compaction
  - Configurable retention period

- **`bd export --priority`** - Exact priority filter for exports

- **`--resolution` alias** (GH#721) - Alternative to `--reason` on `bd close`
  - `bd close bd-42 --resolution "Fixed in commit abc123"`

- **Database size check in doctor** (#724) - Issue count threshold warnings
  - Warns when issue count exceeds configurable threshold
  - Helps identify when cleanup or compaction is needed

- **Config override notifications** (#731) - Transparency for config sources
  - Shows when environment variables override config file values
  - Helps debug unexpected behavior from env overrides

- **Windows code signing infrastructure** - Signed Windows binaries
  - Code signing certificate integration for Windows releases
  - Improved trust and installation experience on Windows

- **RPC endpoints for monitoring** (bd-0oqz, bd-l13p)
  - `GetMoleculeProgress` - Query molecule execution state
  - `GetWorkerStatus` - Query worker health and current task

- **Cross-database molecule spawning**
  - `bd mol run` can spawn in external beads databases
  - Enables cross-project workflow orchestration

- **Config-based close hooks** - Custom scripts on issue close
  - Configure hooks in `.beads/config.yaml`
  - Run validation or notification scripts when issues close

### Changed

- **Removed `bd mol spawn`** - Use pour/wisp only
  - `bd pour <proto>` for persistent molecules
  - `bd wisp create <proto>` for ephemeral molecules
  - Simplifies mental model: pour = liquid (persistent), wisp = vapor (ephemeral)

- **`bd ready` excludes workflow types** - Cleaner ready queue
  - Gates and other workflow coordination types excluded by default
  - Use `--include-workflow` to see all types

- **Natural language activation** (#718) - Improved Claude integration
  - Enhanced activation patterns for natural language commands
  - Anthropic 2025 API compliance updates

### Fixed

- **Dots in prefix handling** (GH#664) - SQLite extractParentChain fix
  - Prefixes containing dots (e.g., `my.project`) now work correctly
  - Parent chain extraction uses proper escaping

- **allowed_prefixes config respected** - Import validation
  - Import now respects `allowed_prefixes` configuration
  - Prevents importing issues with unauthorized prefixes

- **Child counter updates** (GH#728) - Explicit child ID creation
  - `child_counters` table updated when explicit child IDs created
  - Prevents ID collision when mixing auto and explicit child IDs

- **Comment timestamps preserved** (#735) - Import fidelity
  - `created_at` timestamps on comments preserved during import
  - Maintains audit trail for imported issues

- **sync.remote config respected** (#736) - Daemon sync operations
  - Daemon respects configured remote for sync operations
  - Fixes sync to wrong remote when multiple remotes configured

- **Export requires -o flag** (#733) - Explicit output control
  - `bd export` now requires `-o` flag to write to file
  - Prevents accidental stdout pollution in scripts

- **YAML config key normalization** (#732) - Consistent config parsing
  - Config keys normalized to canonical format (snake_case)
  - `sync.remote` and `sync_remote` both work

- **JSON output standardization** - Consistent API
  - Empty arrays return `[]` not `null`
  - Error responses have consistent structure
  - All commands with `--json` follow same patterns

- **MCP Claude Code compatibility** - Tool schema fix
  - Added `output_schema=None` to MCP tools
  - Fixes "Invalid schema" errors in Claude Code

- **Windows file locking** - Better Windows support
  - Proper file handle cleanup prevents locking issues
  - Fixes "file in use" errors on Windows

- **Template commands with daemon** - Daemon mode compatibility
  - `bd pour`, `bd wisp create` work correctly with daemon running

- **Startup config to config.yaml** (GH#536) - Config persistence
  - Startup wizard writes to config.yaml, not SQLite
  - Configuration survives database recreation

- **Multi-hyphen prefixes** (GH#422) - Prefix parsing
  - Prefixes like `my-project-name` parsed correctly
  - Fixes issue ID extraction with complex prefixes

- **`--pour` flag in bond operations** - Phase control
  - `bd mol bond --pour` correctly forces liquid phase

- **Stealth mode gitignore** (GH#704) - Local-only exclusion
  - Uses `.git/info/exclude` instead of global gitignore
  - Stealth mode truly local to the repository

- **Pinned field import** - Field preservation
  - `pinned` field preserved during JSONL import

- **External deps in orphan check** - Migration fix
  - External dependencies excluded from orphan validation
  - Fixes spurious migration warnings

- **Child→parent dependency detection** - Prevents LLM temporal reasoning trap
  - Epic children can no longer depend on their parent epic
  - Blocks a common AI mistake: LLMs use temporal reasoning for "phases" and invert dependencies
  - Example: "Phase 1 before Phase 2" triggers "Phase 1 blocks Phase 2" → WRONG
  - Correct model: "Phase 2 needs Phase 1" → `bd dep add phase2 phase1`
  - Clear error message explains the anti-pattern when detected

### Improved

- **Test coverage** - Significant improvements across codebase
  - daemon: 27% → 72%
  - compact: 17% → 82%
  - setup: 28% → 54%
  - storage: interface conformance tests
  - RPC: comprehensive delete handler tests

- **Structured logging** - Better observability
  - Daemon uses `slog` for structured logging
  - Consistent log format across components

- **Code organization** - Modular refactoring
  - Split `sync.go` into focused modules
  - Split `queries.go` into focused modules
  - Typed JSON response structs (no more `map[string]interface{}`)

- **JSONL size reduction** - Smaller exports
  - `omitempty` on all JSONL fields
  - Removes null/empty values from exports

## [0.35.0] - 2025-12-23

### Added

- **Dynamic molecule bonding** - Attach templates to running molecules at runtime
  - `bd mol bond <proto> <target> --ref <issue-id>` - Inject template steps dynamically
  - Enables responsive workflows: attach error recovery, expand scope mid-execution
  - Templates can reference existing issue data via `--ref` for variable interpolation

- **`bd activity` command** - Real-time state feed for workflow monitoring
  - Stream mutation events as they happen (JSON format)
  - Monitor molecule progress without polling
  - Useful for dashboards, debugging, and automation

- **`waits-for` dependency type** - Fanout coordination gates
  - `bd dep add <gate> <step> --type waits-for` - Gate waits for all steps
  - Unlike `blocks`, waits-for is used for parallel coordination (fan-in pattern)
  - Gate unblocks only when ALL waits-for dependencies complete

- **Parallel step detection** - Auto-identify parallelizable work
  - Molecules automatically detect steps that can run concurrently
  - `bd mol show` displays parallel-capable step groups
  - Helps agents optimize execution order

- **Molecule navigation commands** (bd-sal9, bd-ieyy) - Track progress through workflows
  - `bd mol current` - Show current step and molecule progress
  - `bd close --continue` - Auto-advance to next step in molecule
  - `bd close --no-auto` - Close without auto-claiming next step

- **`bd list --parent` flag** - Filter issues by parent
  - `bd list --parent=bd-xyz --status=open` shows open children of bd-xyz
  - Useful for epic progress tracking and molecule step listing

- **Conditional bond type** - Run steps only when predecessors fail
  - `bd mol bond --type conditional-blocks` for error-handling workflows
  - Step B runs only if step A fails (useful for fallback paths)

- **External dependency display** - Show cross-project deps in tree view
  - `bd dep tree` now displays external dependencies with repo prefix
  - Satisfied external deps filtered from blocked issues

- **Performance optimization indexes** (bd-bha9, bd-a9y3) - Faster queries for large databases
  - Added indexes for common query patterns
  - Improved GetReadyWork and SearchIssues performance

### Changed

- **Consolidated doctor --fix** (GH#715, bd-bqcc) - Single command for all fixes
  - `bd doctor --fix` now handles: hooks, permissions, sync branch, schema
  - Removed individual `bd migrate`, `bd hooks install` suggestions
  - Daemon health check integrated into doctor flow

- **Auto_pull config** (GH#707) - Periodic remote sync in event-driven mode
  - `bd config set daemon.auto_pull true` enables pull every 5 minutes
  - Prevents staleness when git changes happen outside daemon's watch

### Fixed

- **Rich mutation events for status changes** - Events now include full context
  - Status transitions emit complete before/after state
  - Enables better monitoring and auditing

- **External deps filtered from GetBlockedIssues** - Correct blocking calculation
  - External dependencies no longer incorrectly block local work
  - Only local blocking deps affect ready queue

- **Parallel execution migration race** (GH#720) - Multiple daemons no longer corrupt DB
  - Added file-based migration lock to prevent concurrent schema changes
  - Retry logic when migration lock is held by another process

- **bd create -f with daemon** (GH#719) - File flag now works in daemon mode
  - `-f/--file` flag properly forwarded through RPC

- **FindJSONLInDir interactions.jsonl** (GH#709) - No longer picks wrong file
  - Skip `interactions.jsonl` when discovering JSONL files

- **Daemon duplicate log messages** (PR#713) - Reduced log spam
- **Worktree health check redundancy** (PR#711) - Integrated into CreateBeadsWorktree
- **Diverged sync branch handling** (GH#697) - Auto-recovery on push failure
- **Tombstones in JSONL export** (GH#696) - Deletions now propagate correctly

### Refactored

- Remove legacy autoflush code paths
- Consolidate duplicate path-finding utilities
- Replace map[string]interface{} with typed JSON structs
- Migrate sort.Slice to slices.SortFunc
- Add testEnv helpers, reduce SQLite test file sizes

## [0.34.0] - 2025-12-22

### Added

- **Wisp commands** - Full ephemeral molecule management
  - `bd wisp create <proto>` - Instantiate proto as ephemeral wisp (solid→vapor)
  - `bd wisp list` - List all wisps with stale detection
  - `bd wisp gc` - Garbage collect orphaned wisps
  - Wisps have Wisp=true flag and are not exported to JSONL (never sync)

- **Chemistry UX commands** - Phase-aware molecule operations
  - `bd pour <proto>` - Instantiate proto as persistent mol (solid→liquid)
  - `bd mol bond --wisp` - Force spawn as vapor when attaching to mol
  - `bd mol bond --pour` - Force spawn as liquid when attaching to wisp
  - Squash clears Wisp flag, promoting to persistent (exported to JSONL)

- **Cross-project dependencies** (bd-66w1, bd-om4a) - Reference issues across repos
  - `external:<repo>:<id>` dependency syntax
  - `bd ship <id> --to <repo>` - Ship issues to other beads repos
  - `bd ready` filters by external dependency satisfaction
  - Configure additional repos in `.beads/config.yaml`

- **Orphan detection in bd doctor** - Find issues with missing parents
  - Detects parent-child relationships pointing to deleted issues
  - Suggests fix commands for orphaned issues

### Changed

- **Multi-repo config uses YAML** (GH#683) - `bd repo add/remove` now writes to `.beads/config.yaml`
  - Fixes disconnect where CLI wrote to DB but hydration read from YAML
  - `bd repo remove` now cleans up hydrated issues from removed repo
  - Breaking: `bd repo add` no longer accepts optional alias argument

### Fixed

- **Wisp flag handling** - Wisps now use Wisp=true flag in main database (not exported to JSONL)
- **Prefix validation in multi-repo mode** (GH#686) - Skip validation for external repos
- **Empty config values** (GH#680, GH#684) - Handle gracefully in getRepoConfig()
- **Doctor UX improvements** (GH#687) - Better diagnostics and daemon integration
- **Orphaned test file** - Removed repo_test.go with undefined functions

## [0.33.2] - 2025-12-21

## [0.33.1] - 2025-12-21

### Changed

- **Ephemeral → Wisp rename** - Aligns with Steam Engine metaphor (wisps = ephemeral vapor)
  - JSON field changed from `ephemeral` to `wisp` (breaking change for API consumers)
  - CLI flag changed from `--ephemeral` to `--wisp` on `bd cleanup`
  - SQLite column remains `ephemeral` (no migration needed)

### Fixed

- **Lint error in mail.go** - Added nosec directive for mail delegate exec.Command

## [0.33.0] - 2025-12-21

### Added

- **Wisp molecules** - Support for ephemeral wisps
  - `bd wisp create` creates wisp issues that live only in SQLite
  - Wisp issues never export to JSONL (prevents zombie resurrection)
  - Use `bd pour` for persistent mols, `bd wisp create` for ephemeral wisps
  - `bd mol squash` compresses wisp children into a digest issue
  - `--summary` flag allows agents to provide AI-generated summaries

### Fixed

- **Comments not deleted with issue** - `DeleteIssue` now cascades to comments table

## [0.32.1] - 2025-12-21

### Added

- **MCP output control parameters** (PR#667) - Reduce context window usage by up to 97%
  - `brief` - Return minimal responses: `BriefIssue` for reads, `OperationResult` for writes
  - `brief_deps` - Full issue with compact dependencies
  - `fields` - Custom field projection with validation
  - `max_description_length` - Truncate long descriptions
  - New models: `BriefIssue`, `BriefDep`, `OperationResult`
  - Default `brief=True` for writes (minimal confirmations)

- **MCP filtering parameters** - Align MCP tools with CLI capabilities
  - `labels` / `labels_any` - AND/OR label filtering
  - `query` - Title search (case-insensitive)
  - `unassigned` - Filter to unassigned issues
  - `sort_policy` - Sort ready work by hybrid/priority/oldest

### Fixed

- **Pin field not in allowed update fields**
  - `bd update --pinned` was failing with "invalid field" error
  - Added `pinned` to allowedUpdateFields and importer

## [0.32.0] - 2025-12-20

### Changed

- **Removed `bd mail` commands** - Mail is orchestration, not data plane
  - Removed: `bd mail send`, `bd mail inbox`, `bd mail read`, `bd mail ack`, `bd mail reply`
  - The underlying data model is unchanged: `type=message`, `Sender`, `Ephemeral`, `replies_to` fields remain
  - Orchestration tools should implement mail interfaces on top of the beads data model
  - This follows the principle that beads is a data store, not a workflow engine

### Fixed

- **Symlink preservation in atomicWriteFile** (PR#665) - Thanks @qmx
  - `bd setup claude` no longer clobbers nix/home-manager managed `~/.claude/settings.json`
  - New `ResolveForWrite()` helper writes to symlink target instead of replacing symlink

- **Broken link in examples** (GH#666) - Fixed LABELS.md link in multiple-personas example

## [0.31.0] - 2025-12-20

### Added

- **`bd defer` / `bd undefer` commands** - New "deferred" status for icebox issues
  - Issues that are deliberately postponed, not blocked by dependencies
  - Deferred issues excluded from `bd ready` and shown with ❄️ snowflake styling
  - Full support in MCP server, graph views, and statistics

- **Agent audit trail** (GH#649) - Append-only logging for AI agent interactions
  - New `.beads/interactions.jsonl` audit file
  - `bd audit record` - Log LLM calls, tool calls, or pipe JSON via stdin
  - `bd audit label <id>` - Append quality labels (good/bad) for dataset curation
  - `bd compact --audit` - Optionally log compaction prompts/responses
  - Audit entries are immutable; labels create new referencing entries

- **Directory-aware label scoping** (GH#541) - Auto-filter issues by directory in monorepos
  - Configure `directory.labels` to map paths to label filters
  - `bd ready` and `bd list` auto-apply when in matching directories
  - Example: `packages/maverick: maverick` shows only maverick-labeled issues

- **Molecules catalog** - Separate storage for template molecules
  - Templates now live in `molecules.jsonl`, distinct from work items
  - Hierarchical loading: built-in → town → user → project
  - Molecules use `mol-*` ID namespace with `is_template: true`

- **Windows winget manifest** (GH#524) - Prepare beads for Windows Package Manager
  - Added manifest files for winget submission
  - Once merged to microsoft/winget-pkgs: `winget install SteveYegge.beads`

- **Git commit configuration** (GH#600) - Control beads auto-commit behavior
  - `git.author` - Override commit author (useful for bots)
  - `git.no-gpg-sign` - Disable GPG signing (fixes Touch ID prompts)

- **Require description config** (GH#596) - Enforce descriptions on issue creation
  - Set `create.require-description: true` to error on missing descriptions
  - Also supports `BD_CREATE_REQUIRE_DESCRIPTION` env var

### Changed

- **`bd stats` merged into `bd status`** (GH#644) - Consolidated status commands
  - `stats` now an alias for `status`
  - Colorized output with emoji header
  - Shows all statistics (tombstones, pinned, epics, lead time)
  - Added `--no-activity` flag to skip git activity parsing

- **Thin hook shims** (GH#615) - Hooks now delegate to `bd hooks run`
  - Eliminates hook version drift after upgrades
  - No more manual `bd hooks install --force` needed
  - Shims use `# bd-shim v1` marker (format version, not bd version)

- **MCP context tool consolidation** - Merged 3 tools into 1
  - Combined `set_context`, `where_am_i`, `init` into single `context` tool
  - Actions: `set` (default with workspace_root), `show` (default), `init`

- **Doctor improvements** (GH#656) - Enhanced diagnostics and testing
  - Visual improvements with grouped output
  - Comprehensive test coverage added
  - Fixed gosec warnings
  - Contributed by @rsnodgrass

### Fixed

- **`relates-to` cycle detection** (GH#661) - Exclude relates-to from cycle detection
  - Relates-to links are bidirectional by design, not cycles

- **Doctor `.local_version` check** (GH#662) - Check correct version file
  - Now checks `.local_version` instead of deprecated `LastBdVersion`

- **Doctor Claude plugin link** (GH#623) - Updated broken documentation link

- **Read-only gitignore in stealth mode** (GH#663) - Print manual instructions instead of failing
  - When global gitignore is read-only (e.g., symlink to immutable location), shows what to add manually
  - Contributed by @qmx

## [0.30.7] - 2025-12-19

### Fixed

- **`bd graph` nil pointer crash** (#657) - Fixed crash when running `bd graph` on epics
  - `renderGraph()` was passed `nil` instead of the subgraph, causing panic in `computeDependencyCounts()`

- **Windows npm installer file lock** (#652) - Fixed installation failure on Windows
  - The download file handle wasn't fully closed before extraction attempted
  - Now properly waits for `file.close()` callback before proceeding

## [0.30.6] - 2025-12-18

### Added

- **`bd graph` dependency counts** - Graph command shows dependency counts using subgraph formatting
- **`types.StatusPinned`** - New status for persistent beads that survive cleanup

### Fixed

- **CRITICAL: Dependency resurrection bug** - Fixed 3-way merge to respect dependency removals
  - `mergeDependencies` was using union (additive-only) instead of proper 3-way merge
  - Now removals are authoritative: if either left or right removes a dep, it stays removed
  - This prevented orphaned parent-child relationships from being permanently removed

## [0.30.5] - 2025-12-18

### Removed

- **YAML simple template system** - Removed `--from-template` flag from `bd create`
  - Deleted embedded templates: `bug.yaml`, `epic.yaml`, `feature.yaml`
  - Templates are now purely Beads-based (epics with `template` label)
  - Use `bd template instantiate <id>` for template workflows

## [0.30.4] - 2025-12-18

### Added

- **`bd template instantiate`** - Create beads issues from YAML workflow templates
  - `bd template instantiate <file.yaml>` - Create issues from workflow definitions
  - `--assignee <identity>` flag for auto-assignment during instantiation
  - Supports multi-issue workflows with dependency chains
  - Templates define issue properties (title, type, priority, dependencies)

### Changed

- **`bd mail inbox --identity`** - Fixed to properly filter by identity parameter

### Fixed

- **Orphan detection warnings** - No longer warns about closed issues or tombstones
  - Previously `bd doctor` reported false positives for completed dependencies

### Removed

- **Legacy MCP Agent Mail integration** - Removed obsolete `mcp_agents` package
- **YAML workflow execution system** - Replaced by simpler template instantiation

### Notes

- **Experimental edges**: The new graph link fields (`relates_to`, `replies_to`, `duplicate_of`, `superseded_by`) and mail commands are **experimental and subject to change** in upcoming releases. Early adopters should expect breaking changes to these APIs.

## [0.30.3] - 2025-12-17

### Fixed

- **Data loss race condition** - Removed unsafe `ClearDirtyIssues()` method
  - Old method cleared ALL dirty issues, risking data loss if export failed partway
  - All code now uses `ClearDirtyIssuesByID()` which only clears exported issues
  - Affects: `internal/storage/sqlite/dirty.go`, `internal/storage/memory/memory.go`

### Closed (Already Implemented)

- **Stale database warning** - Commands now warn when database is out of sync with JSONL
- **Staleness check error handling** (bd-n4td, bd-o4qy) - Proper warnings and error returns

## [0.30.2] - 2025-12-16

### Added

- **`bd setup droid`** (GH#598) - Factory.ai (Droid) IDE support
  - Configure beads for use with Factory.ai's Droid
  - Contributed by @jordanhubbard

- **Messaging schema fields** - Foundation for inter-agent messaging
  - New `message` issue type for agent-to-agent communication
  - New fields: `sender`, `wisp`, `replies_to`, `relates_to`, `duplicate_of`, `superseded_by`
  - New dependency types: `replies-to`, `relates-to`, `duplicates`, `supersedes`
  - Schema migration 019 (automatic on first use)

- **`bd mail` commands** - Inter-agent messaging
  - `bd mail send <recipient> -s <subject> -m <body>` - Send messages
  - `bd mail inbox` - List open messages for your identity
  - `bd mail read <id>` - Display message content
  - `bd mail ack <id>` - Acknowledge (close) messages
  - `bd mail reply <id> -m <body>` - Reply to messages (creates threads)
  - Identity via `BEADS_IDENTITY` env var or `.beads/config.json`

- **Graph link commands** - Knowledge graph relationships
  - `bd relate <id1> <id2>` - Create bidirectional "see also" links
  - `bd unrelate <id1> <id2>` - Remove relates_to links
  - `bd duplicate <id> --of <canonical>` - Mark issue as duplicate (closes it)
  - `bd supersede <old> --with <new>` - Mark issue as superseded (closes it)
  - `bd show --thread` - View message threads via replies_to chain

- **Hooks system** - Extensible event notifications
  - `.beads/hooks/on_create` - Runs after issue creation
  - `.beads/hooks/on_update` - Runs after issue update
  - `.beads/hooks/on_close` - Runs after issue close
  - `.beads/hooks/on_message` - Runs after message send
  - Hooks receive issue ID, event type as args, full JSON on stdin

- **`bd cleanup --wisp` flag** - Clean up transient messages
  - Deletes only closed issues with `wisp=true`
  - Useful for cleaning up messages after swarms complete

### Fixed

- **Windows build errors** (GH#585) - Fixed gosec lint warnings
  - Contributed by @deblasis

- **Issue ID prefix extraction** - Word-like suffixes (e.g., `my-project-audit`) now parse correctly
  - Previously could incorrectly split on word boundaries

### Removed

- **Legacy deletions.jsonl code** - Fully migrated to inline tombstones
  - Removed `deletions.jsonl` from git tracking
  - All deletion tracking now via inline tombstones in `issues.jsonl`

### Documentation

- **Messaging documentation** - New docs for messaging system
  - `docs/messaging.md` - Full messaging reference with examples
  - `docs/graph-links.md` - Graph link types and use cases
  - Updated `AGENTS.md` with inter-agent messaging section

- Windows installation command in upgrade instructions (GH#589)
  - Contributed by @alexx-ftw

- Aligned `bd prime` guidance with skill's hybrid TodoWrite approach

## [0.30.1] - 2025-12-16

### Added

- **`bd reset` command** (GH#505) - Complete beads removal from a repository
  - Removes `.beads/` directory and all associated data
  - Cleans up git hooks installed by beads
  - Use `--force` to skip confirmation prompt

- **`bd cleanup --hard` flag** - Bypass tombstone TTL safety
  - Immediately removes tombstones regardless of age
  - Use when you need to force-clean deleted issues

- **`bd update --type` flag** (GH#522) - Change issue type after creation
  - Convert between task, bug, feature, epic types
  - Example: `bd update bd-xyz --type epic`

- **`bd q` silent quick-capture mode** (GH#540)
  - Capture issues silently without output
  - Ideal for scripting and automation

- **`bd sync --check` flag** - Pre-sync integrity checks
  - Validates database state before syncing
  - Catches potential issues early

- **`bd show` displays dependent issue status** (#583)
  - Shows status for all blocked-by and blocking issues
  - Better visibility into dependency chains

- **`bd daemon --status` shows config** (#569)
  - Displays daemon configuration in status output
  - Contributed by @crcatala

- **`bd daemon --stop-all`** - Kill all daemon processes
  - Useful for cleanup when multiple daemons are running

- **Auto-disable daemon in git worktrees** (#567)
  - Daemon automatically disabled in worktrees for safety
  - Prevents database conflicts between worktrees

- **`claude.local.md` support** - Local-only documentation
  - Add project notes that won't be committed
  - Gitignored by default

- **Auto-add session completion instructions to AGENTS.md**
  - New projects get session-close protocol guidance

- **Inline tombstones for soft-delete**
  - Deleted issues become tombstones with `status: "tombstone"` in `issues.jsonl`
  - Full audit trail: `deleted_at`, `deleted_by`, `delete_reason`, `original_type`
  - TTL-based expiration (default 30 days) with automatic pruning via `bd compact`
  - Proper 3-way merge support: fresh tombstones win, expired tombstones allow resurrection

- **`bd migrate-tombstones` command**
  - Converts legacy `deletions.jsonl` entries to inline tombstones
  - Archives old file as `deletions.jsonl.migrated`

- **Enhanced Git Worktree Support**
  - Shared `.beads` database across all worktrees
  - Worktree-aware database discovery
  - Git hooks automatically adapt to worktree context
  - Documentation in `docs/WORKTREES.md`

### Fixed

- **Multi-hyphen prefix parsing** (GH#405) - Prefixes like `my-cool-project` now work correctly
- **`bd sync` on sync branch** (GH#519) - Fixed sync when already on the sync branch
- **Pre-commit hook with removed `.beads`** (GH#483) - No longer blocks commits after beads removal
- **Priority format error messages** (GH#517) - Clearer guidance on P0-P4 format
- **Status naming consistency** (GH#444) - Uses `in_progress` everywhere
- **Orphan detection with dots in directory names** (GH#508) - No more false positives
- **Circular error in pre-push hook** (GH#532) - Fixed infinite loop scenario
- **Doctor --fix auto-migrates tombstones** - Automatic migration during repair
- **Daemon detects external DB replacement** (#564) - Contributed by @deblasis
- **Import handles hierarchical hash IDs** (#584) - Contributed by @rsnodgrass
- **Nested worktree detection** (GH#509) - Correctly finds `.beads/` in parent repo
- **Import skips cross-prefix content matches** - Prevents incorrect renames
- **3-way merge tolerates missing issues** - More robust conflict resolution
- **Pre-commit warns instead of failing on flush error** - Graceful degradation

### Security

- **Go toolchain updated to 1.24.11** - Addresses CVEs in standard library

### CI

- **PR check for `.beads/issues.jsonl` changes** - Rejects accidental database commits

### Dependencies

- Bump `github.com/ncruces/go-sqlite3` to 0.30.3
- Bump `github.com/spf13/cobra` to 1.10.2
- Bump `golang.org/x/mod` to 0.31.0
- Bump `golang.org/x/term` to 0.38.0
- Bump `pydantic` to 2.12.5 (beads-mcp)
- Bump `fastmcp` to 2.14.1 (beads-mcp)

## [0.29.0] - 2025-12-03

### Added

- **`--estimate` / `-e` flag for `bd create` and `bd update` (GH #443)**
  - Add time estimates to issues in minutes
  - Example: `bd create "My task" --estimate 120` (2 hours)
  - Example: `bd update bd-xyz --estimate 60` (1 hour)
  - Enables planning and prioritization features in vscode-beads

- **`bd doctor` improvements**
  - SQLite integrity check - Detects database corruption
  - Configuration value validation - Validates config settings
  - Stale sync branch detection - Warns about abandoned beads-sync branches
  - `--output` flag - Export diagnostics to file for sharing
  - `--dry-run` flag - Preview fixes without applying
  - Per-fix confirmation mode - Approve each fix individually

- **`--readonly` flag** - Read-only mode for worker sandboxes
  - Blocks all write operations
  - Useful for parallel worker processes that should only read

### Fixed

- **`bd sync` safety improvements**
  - Auto-push after merge with safety check
  - Handle diverged histories with content-based merge
  - Multiple safety check enhancements

- **Auto-resolve merge conflicts deterministically**
  - All field conflicts resolved without prompts
  - Uses consistent rules for field-level merging

- **3-char all-letter base36 hash support (GH #446)**
  - Fixes prefix extraction for edge case hashes like "bd-abc"

- **`bd ready` message fix**
  - Shows correct message when all issues are closed

- **Version notification spam fix**
  - Store version in gitignored .local_version file

- **Nix flake vendorHash update**
  - Fixed build after dependency bumps

### Documentation

- Added perles and vscode-beads to Third-Party Tools
- Encourage batch close and parallel creation in `bd prime` output

## [0.28.0] - 2025-12-01

### Added

- **`bd daemon --local` flag (#433)** - Run daemon without git operations
  - Ideal for multi-repo setups where git sync happens externally
  - Prevents daemon from triggering git commits/pushes
  - Use with worktrees or when beads sync is handled by another process

- **`bd daemon --foreground` flag** - Run daemon in foreground mode
  - For systemd/supervisord/launchd integration
  - Logs to stdout instead of background file
  - Process stays attached to terminal

- **`bd migrate-sync` command** - Migrate to sync.branch workflow
  - Moves beads data to a dedicated sync branch
  - Keeps main branch clean of .beads/ commits
  - Automated setup of sync.branch configuration

### Fixed

- **Database Migration: close_reason column**
  - Added missing database column for close_reason field
  - Fixes sync loops where close_reason was lost on import/export
  - Automatic migration on first run

- **Multi-repo Prefix Filtering (GH #437)**
  - Issues now filtered by prefix when flushing from non-primary repos
  - Prevents issues from other projects appearing in exports

- **Parent-Child Dependency UX (GH #440)**
  - Fixed backwards documentation in DEPENDENCIES.md
  - `bd show` now displays epic children under "Children" not "Blocks"
  - Clearer UI labels for dependency relationships

- **sync.branch Workflow Fixes**
  - Fixed .beads/ restoration from branch after sync
  - Prevents final flush after sync.branch restore
  - `bd doctor` now detects when on sync branch

- **Jira API Migration**
  - Updated from deprecated Jira API v2 to v3
  - Fixes authentication issues with newer Jira instances

- **Redundant Database Queries**
  - Removed extra GetCloseReason() calls after column migration
  - Improves query performance for issue retrieval

### Documentation

- Added go install fallback instructions for Claude Code web (GH #439)
- Added uninstall documentation (GH #445)

### Internal

- Refactored daemon sync functions to reduce ~200 lines of duplication
- Consolidated local-only sync functions into shared implementation

## [0.27.2] - 2025-11-30

### Fixed

- **CRITICAL: Prevent Mass Database Deletion on JSONL Reset**
  - git-history-backfill now includes safety guard to prevent purging entire database
  - If >50% of issues would be deleted via git history, operation is aborted with warning
  - Threshold prevents accidental deletion when JSONL is reset (git reset, branch switch, etc.)
  - If 10-50% would be deleted, operation proceeds but shows warning message
  - Fixes bug where `git reset --hard origin/main` would lose all issues

- **Fix Fresh Clone Initialization**
  - `bd init` now works on fresh clones that have JSONL but no database
  - Auto-detects issue prefix from existing JSONL (no `--prefix` flag needed)
  - Prevents "database not found" errors on first run in a cloned repository
  
- **Import Warning for Deleted Issues**
  - New warning message when issues are skipped due to deletions manifest
  - Helps users understand why expected issues aren't being imported
  - Warns user to check `bd deleted` for history of removed issues

- **Extract Issue Prefix for 3-char Hashes (#425)**
  - `ExtractIssuePrefix()` now handles base36 hashes as short as 3 characters
  - Previously required 4+ chars, breaking hyphenated prefixes (e.g., `document-intelligence-0sa`)
  - Updated hash validation to accept base36 (0-9, a-z) instead of just hex
  - Requires at least one digit to distinguish hashes from English words

## [0.27.0] - 2025-11-29

### Added

- **Custom Status States**: Define custom issue statuses via config
  - Configure project-specific statuses like `testing`, `blocked`, `review`
  - Status validation ensures only configured statuses are used
  - Backwards compatible: open/in_progress/closed always work

- **Contributor Fork Workflows**: `bd init --contributor` now auto-configures `sync.remote=upstream`
  - Syncs issues from upstream rather than origin
  - Ideal for contributors working on forks

- **Git Worktree Support**: Full support for git worktrees (#416)
  - `bd hooks install` now works correctly in worktrees
  - Helpful message when entering worktree repo without beads initialized
  - Hooks properly detect worktree vs main repository

- **Daemon Health Checks**: Health monitoring in daemon event loop
  - Periodic health checks detect stale database state
  - Auto-recovery from detected inconsistencies

- **Fresh Clone Detection**: `bd doctor` now detects fresh clones
  - Suggests `bd init` when JSONL exists but no database
  - Improved onboarding experience for new contributors

- **bd sync --squash**: Batch multiple sync commits into one
  - Reduces commit noise when syncing frequently
  - Optional flag for cleaner git history

- **Error Handling Helpers**: Extracted FatalError/WarnError utilities
  - Consistent error formatting across codebase
  - Better error messages for users

### Fixed

- **CRITICAL: Sync Corruption Prevention**: Multiple fixes prevent stale database from corrupting JSONL
  - **Hash-based staleness detection**: SHA256 hash comparison catches content mismatches
  - **Reverse ZFC check**: Detects when JSONL has more issues than DB
  - **Stale daemon connection** (eb4b81d): Prevents corruption from stale SQLite connections
  - Combined, these fixes eliminate the sync corruption bugs that affected v0.26.x

- **Multi-Hyphen Prefix Support** (#419): Hash IDs with multi-part prefixes now handled correctly
  - Example: `my-app-abc123` correctly parsed as prefix `my-app`

- **Out-of-Order Dependencies** (#414): JSONL import handles dependencies before their targets exist
  - Fixes import failures when issues reference not-yet-imported dependencies

- **--from-main Sync Mode** (#418): Now defaults to `noGitHistory=true`
  - Prevents spurious deletions when syncing from main branch

- **JSONL-Only Mode Detection**: Auto-detects when config has `no-db: true`
  - Properly handles repositories using JSONL without SQLite

- **Init Safety Guard**: Prevents overwriting existing JSONL data on init
  - Warns user when data already exists, requires confirmation

- **Snapshot Cleanup**: Properly cleans up snapshot files after sync
  - Removes `.beads/*.snapshot` files that could cause conflicts

- **Daemon Registry Locking**: Cross-process locking prevents registry corruption
  - Fixes race conditions when multiple processes access daemon registry

- **Doctor Merge Artifacts**: Excludes merge artifacts from "multiple JSONL" warning
  - Reduces false positives during merge resolution

### Changed

- **Documentation**: Fixed birthday paradox threshold explanation in README
- **Documentation**: Corrected `bd dep add` syntax and semantics

### Community

- PR #419: Multi-hyphen prefix support
- PR #418: --from-main noGitHistory default
- PR #416: Git worktree hooks support
- PR #415: CI fixes
- PR #414: Out-of-order dependency handling
- PR #404: Error on invalid JSON during init (@joelklabo)

## [0.26.2] - 2025-11-29

### Fixed

- **Hash-Based Staleness Detection**: Prevents stale DB from corrupting JSONL when counts match
  - Previous count-based check (0.26.1) missed cases where DB and JSONL had similar issue counts
  - New detection computes SHA256 hash of JSONL content and stores it after import
  - On export, compares current JSONL hash against stored hash to detect modifications
  - If JSONL was modified externally (e.g., by git pull), triggers re-import before export
  - Ensures database is always synchronized with JSONL before exporting changes

## [0.26.1] - 2025-11-29

### Fixed

- **CRITICAL: Reverse ZFC Check**: Prevents stale database from corrupting JSONL
  - Root cause: `bd sync` exports DB to JSONL before pulling from remote
  - If local DB is stale (fewer issues than JSONL), stale data would corrupt the JSONL
  - Added reverse ZFC check: detects when JSONL has >20% more issues than DB
  - When detected, imports JSONL first to sync database before any export
  - Prevents fresh/stale clones from exporting incomplete database state

## [0.26.0] - 2025-11-27

### Added

- **bd doctor --check-health**: Lightweight health checks for startup hooks (3fe94f2)
  - Quick, silent health checks (exit 0 on success, non-zero on issues)
  - Checks: version mismatch, sync.branch config, outdated hooks
  - New `hints.doctor` config option to suppress doctor hints globally
  - Git hooks now call `bd doctor --check-health` in post-merge/post-checkout

- **--no-git-history Flag**: Prevent spurious deletions during import/sync (5506486)
  - Use when git history is unreliable (shallow clones, squash merges)
  - Prevents deletion manifest from removing issues based on stale history

- **gh2jsonl Hash ID Mode**: Content-based ID generation for GitHub imports (#383 by @deangiberson)
  - `--id-mode {sequential|hash}` flag (default: sequential for backward compatibility)
  - `--hash-length {3,4,5,6,7,8}` for configurable hash length (default: 6)
  - Hash IDs are deterministic using title, description, creator, timestamp

- **CI Provenance Attestation**: npm publish now includes provenance attestation (03d62d0)

- **bdui**: Added to Third-Party Tools ecosystem (#384)

### Fixed

- **Critical: MCP Protocol Stdin Fix** (PR #400 by @cleak)
  - Subprocess stdin inheritance was breaking MCP JSON-RPC protocol
  - All subprocess calls now use `stdin=subprocess.DEVNULL`
  - Fixes hanging/blocking issues in Claude Desktop MCP integration

- **Git Worktree Staleness** (#399)
  - Staleness check was failing after writes in git worktrees
  - Now uses RFC3339Nano precision for `last_import_time` metadata

- **Multi-Part Prefix Support** (#398)
  - Issue ID extraction now correctly handles multi-part prefixes
  - Example: `my-app-123` correctly extracts prefix `my-app`

- **bd sync Commit Scope**
  - `bd sync` now only commits `.beads/` files, not other staged files
  - Prevents accidental commits of unrelated staged changes

- **Auto-Import to Wrong File**
  - Fixed auto-import exporting to wrong JSONL file
  - FindJSONLPath now correctly skips deletions.jsonl

- **Deletions.jsonl Handling**
  - Git hooks now properly stage deletions.jsonl for cross-clone propagation
  - bd doctor no longer warns about deletions.jsonl
  - Prevent rebase failures from deletions.jsonl writes

- **Defense-in-Depth**
  - Added additional check for --no-auto-import flag

- **Tilde Expansion**: Global gitignore path now expands `~` correctly

- **beads-mcp Type Checking**: Resolved all mypy type checking errors

- **CI Test Stability**: Fixed Windows test failures

### Changed

- **Stealth Mode**: Removed global gitattributes setup from `bd init --stealth` (#391)
  - Stealth mode now purely local with no global git configuration

- **Pre-Push Hook Error Message**: Improved clarity when sync fails (#390)

### Refactoring

- Extract path canonicalization and database search helpers (7d765c2)
- Consolidate check-health DB access and expand hook checks (3458956)

### Documentation

- Sync skill CLI reference with current docs (62d1dc9)

### Community

- PR #400: MCP stdin fix (@cleak)
- PR #398: Multi-part prefix support
- PR #391: Stealth mode gitattributes removal
- PR #390: Pre-push hook error message (@jonathanpberger)
- PR #384: bdui ecosystem addition
- PR #383: gh2jsonl hash ID support (@deangiberson)

## [0.25.1] - 2025-11-25

### Added

- **Zombie Resurrection Prevention**: Stale clones can no longer resurrect deleted issues
  - New JSONL sanitization step (3.6) after git pull removes deleted issues before import
  - Prevents git's 3-way merge from re-adding issues that were deleted elsewhere
  - New `bd doctor` check 18: "Deletions Manifest" detects missing/empty manifest
  - New `bd doctor --fix` hydrates deletions.jsonl from git history for pre-v0.25.0 deletions
  - ID validation prevents false positives from non-issue JSON fields

### Fixed

- **bd sync commit scope**: Now commits entire `.beads/` directory before pull
  - Previously only committed beads.jsonl, leaving metadata.json unstaged
  - Fixes "You have unstaged changes" error during `git pull --rebase`

## [0.25.0] - 2025-11-25

### Added

- **Deletion Propagation**: Deletions now sync across clones via deletions manifest
  - New `.beads/deletions.jsonl` tracks deleted issues with timestamp, actor, reason
  - Import automatically purges issues that were deleted in other clones
  - Git history fallback for pruned deletion records (self-healing)
  - New `bd deleted` command to view deletion audit trail
  - Auto-compact during sync (opt-in via `deletions.auto_compact: true`)
  - Configurable retention period (`deletions.retention_days`, default 7)
  - Local unpushed work protected from accidental deletion
  - Full documentation in docs/DELETIONS.md

- **Stealth Mode**: New `bd init --stealth` flag for invisible beads usage (#381)
  - Adds `.beads/` to project's `.gitignore` automatically
  - Useful for personal issue tracking in shared repos without affecting collaborators
  - All bd functionality works normally, just not committed to git

- **Ephemeral Branch Sync**: New `bd sync --from-main` flag
  - Syncs from main branch without pushing to remote
  - Ideal for feature branches that sync issues from main
  - Prevents pushing local branch changes to origin

## [0.24.4] - 2025-11-25

### Added

- **Transaction API**: Full transactional support for atomic multi-operation workflows
  - New `storage.Transaction` interface with CreateIssue, UpdateIssue, CloseIssue, DeleteIssue
  - Dependency operations: AddDependency, RemoveDependency within transactions
  - Label operations: AddLabel, RemoveLabel with transactional semantics
  - Config/Metadata operations for atomic config+issue workflows
  - Uses `BEGIN IMMEDIATE` mode to prevent deadlocks
  - Automatic rollback on error or panic, commit on success
  - 1,147 lines of new implementation with comprehensive tests

- **Tip System Infrastructure**: Smart contextual hints for users
  - Tips shown after successful commands (list, ready, create, show)
  - Condition-based filtering, priority ordering, probability rolls
  - Frequency limits prevent tip spam
  - Respects `--json` and `--quiet` flags
  - Deterministic testing via `BEADS_TIP_SEED` env var

- **Sorting for bd list and bd search**: New `--sort` and `--reverse` flags
  - Sort by: priority, created, updated, closed, status, id, title, type, assignee
  - Smart defaults: priority ascending (P0 first), dates descending (newest first)
  - Works with all existing filters

- **Claude Integration Verification**: New bd doctor checks
  - `CheckBdInPath`: verifies 'bd' is in PATH (needed for hooks)
  - `CheckDocumentationBdPrimeReference`: detects version mismatches in docs

- **ARM Linux Support**: GoReleaser now builds for linux/arm64 (PR #371 by @tjg184)
  - Enables bd on ARM-based Linux systems like Raspberry Pi, AWS Graviton

- **Orphan Detection Migration**: Identifies orphaned child issues
  - Detects issues with hierarchical IDs where parent no longer exists
  - Logs suggestions: delete, convert to standalone, or restore parent
  - Idempotent and safe to run multiple times

### Fixed

- **Transaction Cache Invalidation**: blocked_issues_cache now invalidates correctly
  - UpdateIssue status changes trigger cache invalidation
  - CloseIssue always invalidates (closed issues don't block)
  - AddDependency/RemoveDependency invalidate for blocking types

- **SQLITE_BUSY Retry Logic**: Exponential backoff for concurrent writes
  - `beginImmediateWithRetry()` with 5 retries (10ms, 20ms, 40ms, 80ms, 160ms)
  - Eliminates spurious failures under normal concurrent usage
  - Context cancellation respected between retry attempts

- **bd import Argument Validation**: Helpful error for common mistake
  - Running `bd import file.jsonl` (without `-i`) now shows clear error
  - Previously silently read from stdin, confusing users with "0 created"

- **ZFC Import Export Skip**: Preserve JSONL source of truth
  - After stale DB import from JSONL, skip export to avoid overwriting
  - Fixes scenario where DB with fewer issues would overwrite JSONL

- **Daemon Reopen with Reason**: `--reason` flag now works in daemon mode
  - Previously ignored in daemon RPC calls

- **Windows Test Failures**: Skip file permission tests on Windows
  - Windows doesn't support Unix-style permissions (0600, 0755)
  - Core functionality still tested, only permission checks skipped

### Changed

- **bd daemon UX**: New `--start` flag, help text when no args
  - `bd daemon` now shows help instead of immediately starting
  - Use `bd daemon --start` to explicitly start
  - Auto-start still works (uses `--start` internally)
  - More discoverable for new users

### Documentation

- **blocked_issues_cache Architecture**: Document cache behavior and invalidation
- **Antivirus False Positives**: Guide for handling security software alerts
- **import.orphan_handling**: Complete documentation
- **Error Handling Patterns**: Comprehensive audit and guidelines

### Dependencies

- Bump github.com/tetratelabs/wazero from 1.10.0 to 1.10.1 (#374)
- Bump github.com/anthropics/anthropic-sdk-go from 1.18.0 to 1.18.1 (#373)
- Bump actions/checkout from 4 to 6 (#372)

### Community

- PR #371: ARM Linux support (@tjg184)

## [0.24.3] - 2025-11-24

### Added

- **BD_GUIDE.md Generation**: Version-stamped documentation for AI agents (bd-woro, 9e16469)
  - New `--output` flag for `bd onboard` command generates BD_GUIDE.md
  - Separates bd-specific instructions from project-specific instructions
  - Auto-generated header with version stamp warns against manual editing
  - Detects outdated BD_GUIDE.md and suggests regeneration after upgrades
  - Git-trackable diffs show exactly what changed between versions

- **Configurable Export Error Policies**: Flexible error handling for export operations (bd-exug, e3e0a04)
  - Four policies: strict (fail-fast), best-effort (skip with warnings), partial (retry then skip), required-core (fail on core data only)
  - Per-project configuration via `bd config set export.error_policy`
  - Separate policy for auto-exports via `auto_export.error_policy`
  - Retry with exponential backoff for transient failures
  - Optional export manifests documenting completeness

- **Command Set Standardization**: Complete flag and feature consistency overhaul (bd-au0, 273a4d1)
  - Global verbosity flags: `--verbose/-v` and `--quiet/-q` across all commands
  - Standardized `--dry-run` flag behavior across all commands
  - Label operations added to `bd update`: `--add-labels` and `--remove-labels`
  - Enhanced `bd export` with comprehensive filters (assignee, type, labels, priority, dates)
  - Enhanced `bd search` with date and priority filters
  - Improved documentation for `bd clean` vs `bd cleanup` disambiguation

- **Auto-Migration on Version Bump**: Automatic database schema updates (bd-jgxi, 7796f5c)
  - Database version automatically synced when bd CLI is upgraded
  - Eliminates recurring "version mismatch" warnings in bd doctor
  - Best-effort and silent to avoid disrupting commands

- **Version Tracking Validation**: Comprehensive bd doctor checks (bd-u4sb, d8f3eb0)
  - Validates metadata.json exists with valid LastBdVersion field
  - Warns if LastBdVersion is very old (>10 minor versions behind)
  - Ensures version tracking system is working correctly

- **Git Hooks Executable Validation**: Hook reliability improvements (bd-fwul, 1c715bc)
  - bd doctor now validates git hooks have executable bit set
  - Prevents silent hook failures from incorrect permissions

- **Socket Cleanup Race Condition Fix**: Daemon reliability improvement (bd-4owj, 18f8105)
  - Re-checks socket existence after lock check to avoid stale state
  - Handles daemon startup race conditions gracefully

- **Monitor Web UI Enhancements**: UX/UI improvements (aa2df73)
  - Interactive stats cards as filters
  - Multi-select priority filtering with P0 support
  - Find-as-you-type search functionality
  - Modern card-based UI design with better mobile responsiveness
  - Dev mode flag for easier development

- **bump-version.sh Automation**: PyPI integration (0547004)
  - New `--upgrade-mcp` flag for automatic beads-mcp package upgrades
  - Tries pip first, falls back to uv tool
  - Warns about version mismatch before PyPI publish

### Fixed

- **JSONL Import Foreign Key Violations**: Graceful handling of deletions during merge (bd-koab, d45cff5)
  - Import now continues when dependencies reference deleted issues
  - Reports skipped dependencies with clear warnings
  - Prevents complete import failure from FK constraint violations
  - Common scenario: merges that delete issues referenced by other issues

- **Metadata JSONL Path Auto-Detection**: Fix configuration mismatches (bd-afd, d1641c7)
  - Auto-detects actual JSONL file when metadata.json is recreated
  - bd doctor --fix now includes DatabaseConfig() auto-repair
  - Prefers beads.jsonl over issues.jsonl (canonical name)
  - Prevents mismatches after git clean, merge conflicts, or rebases

- **JSONL Resurrection Bug**: Critical fix for deleted issue resurrection (bd-v0y, c9a2e7a)
  - Removed mtime fast-path in hasJSONLChanged() causing false negatives
  - Git doesn't preserve mtime on checkout, causing incorrect change detection
  - Now always uses content hash for reliable comparison
  - Prevents bd sync from overwriting pulled JSONL and resurrecting deleted issues

- **ZFC (JSONL First Consistency)**: Fix stale DB overwriting JSONL on sync (bd-l0r, 1ba068f, 2e4171a, 949ab42)
  - bd sync now detects stale DB (>50% divergence from JSONL) and imports first
  - After ZFC import, skips export to prevent overwriting JSONL source of truth
  - Fixes bug where DB with 688 issues would overwrite JSONL with 62 issues after pull
  - JSONL is source of truth after git pull - DB syncs to match, not vice versa
  - Preserves local uncommitted changes while catching stale DB scenarios

- **Merge Conflict Semantics**: Improved resolution policies (bd-pq5k, d4f9a05)
  - Merge logic now enforces: closed ALWAYS wins over open
  - Deletion ALWAYS wins over modification
  - Fixed closed_at handling: only set when status='closed'
  - Prevents issues from getting stuck in invalid states
  - Eliminates "zombie issues" that never die

- **JSONL Merge Conflict Auto-Resolution**: Streamlined rebase workflow (bd-cwmt, 3cf5e26)
  - bd sync now auto-resolves JSONL conflicts during rebase
  - Detects rebase state and JSONL-only conflicts
  - Auto-exports from DB and continues rebase automatically
  - Eliminates manual conflict resolution in common scenarios

- **Staleness Check Error Handling**: Better metadata validation (bd-2q6d, bd-o4qy, bd-n4td, b75914b)
  - CheckStaleness now returns errors for corrupted last_import_time metadata
  - Enhanced warning messages when staleness check fails
  - Handles empty string metadata (memory store behavior)
  - Prevents silent failures with corrupted metadata

- **N+1 Query Pattern in Export**: Dramatic performance improvement (bd-rcmg, 9c6b375)
  - Added batch methods: GetCommentsForIssues() and GetLabels() in bulk
  - Reduced query count from ~201 to ~3-5 for 100 issues
  - Eliminated per-issue loops in handleExport() and triggerExport()

- **Sync Branch Auto-Configuration**: Prevent bd sync failures after init (bd-flil, bd-rsua, a4c38d5, 83609d5)
  - bd init now auto-sets sync.branch to current git branch
  - bd doctor detects missing sync.branch config and provides --fix
  - All branch detection uses 'git symbolic-ref' to work in fresh repos
  - Fixes 'bd sync --status' error after fresh bd init

- **Merge Driver Auto-Repair**: Fix stale git configurations (bd-3sz0, bd-tbz3, 1c8dd49)
  - Detects old bd versions (<0.24.0) with invalid %L/%R placeholders
  - Auto-repairs during bd init and bd doctor --fix
  - Git only supports %O (base), %A (current), %B (other) placeholders
  - Supports both canonical (issues.jsonl) and legacy (beads.jsonl) filenames

- **Unvalidated Dependency Parsing**: Prevent empty ID lookups (bd-ia8r, e8a752e)
  - Validates dependsOnID is non-empty before setting discoveredFromParentID
  - Ensures parent issue exists before generating child IDs in direct mode

- **Windows CI Test Failures**: Cross-platform reliability (153f724)
  - Fixed file locking issue in TestNewSQLiteStorage (added defer store.Close())
  - Fixed nil slice return in TestFindAllDatabases (explicit empty slice initialization)
  - Both issues related to stricter Windows file locking behavior

- **Invalid closed_at States**: Data integrity fix (bd-1rh, b428254)
  - Removed invalid closed_at timestamps for open issues
  - Prevents inconsistent state from merge conflicts

- **Security**: File permission hardening (b6870de, ae5a4ac)
  - Changed file permissions from 0644 to 0600 for security (gosec G302)
  - Config files now owner read/write only (not world readable)
  - Added comprehensive security tests for WriteFile permissions
  - Handles read-only files by fixing permissions before writing

### Changed

- **bd init Defaults**: Better out-of-box experience (bd-bxha, ec4117d)
  - Git hooks and merge driver now installed by default
  - Removed interactive prompts (simpler workflow)
  - New opt-out flags: `--skip-hooks` and `--skip-merge-driver`
  - Shows warning messages on failure with suggestion to run bd doctor --fix

- **bd init Validation**: Automatic setup verification (bd-zwtq, 3a36d0b)
  - Runs bd doctor diagnostics at end of bd init
  - Immediately identifies configuration problems before user encounters them
  - Catches: missing hooks, unconfigured merge driver, missing docs, metadata issues

- **Internal Code Organization**: Improved maintainability (bd-0a43, 58f37d0)
  - Split monolithic sqlite.go (1050 lines) into focused files
  - store.go: Database initialization and utilities
  - queries.go: Issue CRUD operations
  - config.go: Configuration and metadata
  - comments.go: Comment operations
  - Zero functional changes, all tests pass

- **Documentation**: Organization improvements (a930fa3, 62b5f53)
  - Moved event-driven daemon details to docs/DAEMON.md
  - Added comprehensive error handling guidelines
  - Reduced duplication in AGENTS.md

### Testing

- **Blocked Issues Cache Validation**: Comprehensive cache tests (bd-13gm, 0e6ed91)
  - 8 tests verify cache invalidation behavior
  - Tests for dependency add/remove, status changes, transitive blocking
  - Direct cache table queries validate implementation correctness

- **Sync Test Optimization**: Reduced boilerplate (bd-ktng, dfcbb7d)
  - Added shared git repo setup helpers
  - Refactored 19 test functions
  - Reduced duplicate code by ~300 lines

### Performance

- **Daemon Log Rotation**: Production-ready configuration (bd-t7ds, f454b3d)
  - Max size increased: 10MB → 50MB per file
  - Max backups increased: 3 → 7 files
  - Max age increased: 7 → 30 days
  - Better handles long-running daemons with high log output

## [0.24.2] - 2025-11-22

### Fixed

- **Test Stability**: Complete rootCtx initialization fix for all hanging tests (issue #355, b8db5ab)
  - Fixed TestGetAssignedStatus missing rootCtx initialization (a517ec9)
  - Prevents test hangs from uninitialized context
  - Improved test reliability and isolation

- **JSONL Configuration**: Improved bd doctor JSONL checks to focus on real problems (87ee3a6)
  - Reduces false positives in JSONL validation
  - Better detection of actual configuration issues

### Changed

- **JSONL Filename Default**: Changed default JSONL filename from `beads.jsonl` to `issues.jsonl` (c4c5c80)
  - Updated TestFindJSONLPathDefault to match new default (5eefec7)
  - Removed stale `issues.jsonl` in favor of configured `beads.jsonl` (d918e47)
  - More intuitive default filename for new users

## [0.24.1] - 2025-11-22

### Added

- **bd search**: Date and priority filters (787fb4e)
  - `--created-after`, `--created-before` for date filtering
  - `--priority-min`, `--priority-max` for priority range filtering
  - Enables more precise search queries

- **bd count**: New command for counting and grouping issues (d7f4189)
  - Count issues by status, priority, type, or labels
  - Helpful for generating statistics and reports

- **Test Infrastructure**: Automatic skip list for tests (0040e80)
  - Improves test reliability and maintenance
  - Automatically manages flaky or environment-specific tests

### Fixed

- **Test Stability**: Fixed hanging tests by initializing rootCtx (822baa0, bd-n25)
  - Prevents test hangs from context cancellation issues
  - Better test isolation and cleanup

- **Git Merge Driver**: Corrected placeholders from %L/%R to %A/%B (ddd209e)
  - Fixes merge driver configuration for proper conflict resolution
  - Uses correct git merge driver variable names

- **Database Paths**: Deduplicate database paths when symlinks present (#354, f724b61)
  - Prevents duplicate database detection when symlinks are involved
  - Improves reliability in complex filesystem setups

### Changed

- **bd list**: Accept both integer and P-format for priority flags (2e2b8d7)
  - `--priority 1` and `--priority P1` now both work
  - More flexible CLI input for priority filtering

- **bd update**: Added `--body` flag as alias for `--description` (bb5a480)
  - More intuitive flag name for updating issue descriptions
  - Both flags work identically for backward compatibility

- **bd update**: Added label operations (3065db2)
  - `--add-labels` and `--remove-labels` flags
  - Simplifies label management in update operations

- **GitHub Copilot Support**: Added `.github/copilot-instructions.md` (605fff1)
  - Provides project-specific guidance for GitHub Copilot
  - Improves AI-assisted development experience

- **Documentation**: Moved design/audit docs from cmd/bd to docs/ (ce433bb)
  - Better organization of project documentation
  - Clearer separation of code and documentation

### Performance

- **Test Suite**: Deleted 7 redundant tests from main_test.go (fa727c7)
  - 3x speedup in test execution
  - Improved CI/CD performance

- **Test Coverage**: Tagged 16 slow integration tests with build tags (8290243)
  - Faster local test runs with `-short` flag
  - CI can still run full test suite

### Testing

- **Security Tests**: Added security and error handling tests for lint warnings (74f3844)
  - Improved code quality and safety
  - Better coverage of edge cases

- **Shared Database Pattern**: Refactored multiple test files to use shared DB pattern
  - compact_test.go, integrity_test.go, validate_test.go, epic_test.go, duplicates_test.go
  - Improved test consistency and maintainability
  - Faster test execution through better resource sharing

## [0.24.0] - 2025-11-20

### Added

- **bd doctor --fix**: Automatic repair functionality (bd-ykd9, 7806937)
  - Automatically fixes issues detected by `bd doctor`
  - Repairs common database inconsistencies without manual intervention

- **bd clean**: Remove temporary merge artifacts (e8355c2)
  - Cleans up `.base`, `.ours`, `.theirs` snapshot files
  - Helps maintain clean `.beads/` directory after merges

- **bd cleanup**: Enhanced bulk deletion command
  - Delete multiple closed issues efficiently
  - Improved from previous versions with better performance

- **.beads/README.md Generation**: Auto-generated during `bd init` (bd-m7ge, e1c8853)
  - Provides project-specific beads documentation
  - Helps new contributors understand the setup

- **blocked_issues_cache Table**: Performance optimization for GetReadyWork (62c1f42, ed23f8f)
  - Caches blocked issue relationships
  - Dramatically improves `bd ready` performance on large databases

- **Commit Hash in Version Output**: Enhanced version reporting (bd-hpt5, 7c96142)
  - `bd version` now shows git commit hash
  - Helps identify exact build for debugging

- **Auto-detection of Issue Prefix**: Scans git history to detect prefix (#277, 8f37904)
  - Automatically discovers project's issue prefix
  - Reduces manual configuration needed

- **external_ref Support in Daemon RPC**: Full daemon mode support (#304, 57b6ea6)
  - MCP server can now set external references in daemon mode
  - Parity with CLI functionality

- **Context Optimization Features**: AI agent improvements (#297, f7e80dd)
  - Context propagation with graceful cancellation (bd-rtp, bd-yb8, bd-2o2, 57253f9)
  - Better memory management for long-running agent sessions

### Fixed

- **Critical: Auto-import Resurrection Bug** (bd-khnb, 0020eb4, e28e3ea, 7b6370f)
  - Fixed critical bug where deleted issues were resurrected during auto-import
  - Cleaned up 497+ resurrected issues from production database
  - Prevents data corruption from improper JSONL replay

- **Critical: bd sync Auto-resolves Conflicts** (bd-ca0b, a1e5075)
  - `bd sync` now automatically resolves conflicts instead of failing
  - Dramatically improves multi-agent workflow reliability
  - Eliminates manual conflict resolution in most cases

- **Critical: Content-based Timestamp Skew Prevention** (bd-lm2q, d0e7047)
  - Fixed false-positive "JSONL is newer than database" warnings
  - Uses content-based comparison instead of timestamp-only
  - Prevents unnecessary imports that would corrupt state

- **Critical: bd sync DB Changes After Import** (81c741b)
  - Ensures database changes are properly applied after import
  - Fixes desync issues between JSONL and database

- **Critical: Context Propagation Lifecycle Bugs** (bd-rtp, bd-yb8, bd-2o2, 57253f9, a17e4af)
  - Fixed multiple context propagation issues causing crashes
  - Graceful cancellation support for long-running operations
  - Improved stability for AI agent workflows

- **Critical: Race Condition in Auto-flush** (bd-52, a9b2f9f)
  - Fixed race condition in auto-flush mechanism
  - Prevents data loss during concurrent operations

- **Critical: Resource Leaks and Error Handling** (#327, fb65163)
  - Fixed critical resource leaks in daemon mode
  - Improved error handling throughout codebase

- **Critical: In-memory Database Deadlock** (bd-yvlc, 944ed10)
  - Fixed deadlock in migrations when using in-memory database
  - Improves test reliability

- **MCP Schema Generation Recursion Bug** (GH#346, f3a678f)
  - Fixed infinite recursion in MCP schema generation
  - Prevents stack overflow crashes

- **FK Constraint Failures** (bd-5arw, 345766b)
  - Fixed foreign key constraint failures in AddComment and ApplyCompaction
  - Improved data integrity

- **--parent Flag Behavior** (b9919fe)
  - Now correctly creates parent-child dependency relationships
  - Previously was creating wrong dependency type

- **Exact ID Matching Priority** (gh-316, 934ae04)
  - Prefers exact ID matches over prefix matches
  - Prevents ambiguous ID resolution

- **Daemon Lifetime on macOS** (GH#278, 68f9bef)
  - Fixed daemon exiting after 5s on macOS due to PID 1 parent monitoring
  - Daemon now runs reliably on macOS

- **Daemon Export/JSONL Sync** (GH#301, #321, 04a1996)
  - Fixed daemon export leaving JSONL newer than database
  - Ensures proper sync between export and database state

- **bd doctor Hash ID Detection** (GH#322, 8c1f865)
  - Fixed doctor incorrectly diagnosing hash IDs as sequential
  - Improved detection logic for ID format validation

- **ResolvePartialID Handling** (GH#336, 4432af0)
  - Improved ResolvePartialID / ResolveID handling for `bd show`
  - Better partial ID matching and error messages

- **bd sync Windows Upstream Detection** (#281, 1deaad1)
  - Fixed upstream branch detection on Windows
  - Improved cross-platform compatibility

- **Compact Command Daemon Mode** (#294, d9904a8)
  - Fixed compact command failing with 'SQLite DB needed' error when daemon running
  - Removed premature store check, uses ensureDirectMode

- **DB mtime Update After Import** (#296, 9dff345)
  - Fixed DB mtime not being updated after import with 0 changes
  - Prevents false staleness warnings

- **FOREIGN KEY Constraint on Non-existent Issues** (09666b4)
  - Fixed constraint failures when operating on non-existent issues
  - Better error handling and validation

- **Monitor WebUI Daemon Detection** (e36baee)
  - Fixed monitor-webui failure to detect running daemon
  - Improved daemon health checking

- **Onboard Test Deadlock on Windows** (4e22214)
  - Fixed deadlock in onboard tests on Windows
  - Improved test stability

- **Windows Concurrent Issue Creation** (4cd26c8)
  - Fixed concurrent issue creation failures on Windows
  - Better file locking on Windows

- **Missing Git Hook Message** (#306, 92f3af5)
  - Improved messaging when git hooks are missing
  - Clearer instructions for users

- **Prefix Detection for Hyphenated Apps** (83472ac, bd-fasa)
  - Fixed prefix detection to only use first hyphen
  - Handles hyphenated application names correctly

- **External Ref Migration Failures** (8be792a)
  - Fixed external_ref migration failure on old databases
  - Backward compatibility improvements

- **Duplicate Function Declaration** (#328, 167ab67)
  - Fixed compilation failure from duplicate computeJSONLHash declaration
  - Removed old version, kept simpler implementation
  - Updated test to match new API

### Changed

- **Performance Improvements** (#319, 690c73f):
  - Optimized GetReadyWork to use blocked_issues_cache (ed23f8f)
  - Replaced N+1 label queries with bulk fetch in `bd list` (968d9e2)
  - Cache invalidation for blocked_issues_cache (614ba8a)
  - Significant speedup for large databases

- **FlushManager Improvements** (445857f)
  - Added constants for magic numbers
  - Enhanced error logging
  - Comprehensive functional tests

- **Auto-upgrade .beads/.gitignore** (#300, f4a2f87)
  - Automatically upgrades .gitignore on bd operations
  - Ensures latest patterns are always applied

- **Code Refactoring**:
  - Extract duplicated validation logic to internal/validation (d5239ee)
  - Centralize error handling patterns in storage layer (bd-bwk2, 3b2cac4)
  - Extract duplicated validation and flag logic (bd-g5p7, bbfedb0)
  - Improved code organization and maintainability

- **Documentation Improvements**:
  - Document files created by bd init and clarify .gitattributes (721274b, e7fd1dd)
  - How to resolve merge conflicts in .beads/beads.jsonl (4985a68)
  - Document MCP tools loading issue in Claude Code (GH#346, 79b8dbe)
  - Add uv prerequisite to Claude Code plugin docs (#293, a020c6c)
  - Don't auto-install Go in Windows installer (#302, 0cba73b)

- **Improved Error Messages** (#349, 27c0c33)
  - Compact error messages
  - Remove bogus merge suggestion
  - Add daemon/maintenance docs

- **AGENTS.md Refactoring** (21a0656)
  - Extracted detailed instructions to prevent context pollution
  - Better organization for AI agent consumption

- **Type Safety Improvements** (9e57cb6)
  - Improved type safety in beads-mcp
  - Fixed minor type issues

- **Test Improvements**:
  - Fix CI regressions and stabilize tests (7b63b5a)
  - Fix parallel test deadlock (1fc9bf6)
  - Annotate gosec-safe file accesses (bf9b2c8)

- **Local-only Git Repo Support** (bd-biwp, 4de9f01)
  - Support repositories without remote origin
  - Better handling of local development workflows

- **Version Marker in Post-checkout Hook** (ad2154b)
  - Add version marker to post-checkout hook
  - Include in CheckGitHooks for better version tracking

### Performance

- **GetReadyWork Optimization** (bd-5qim, 690c73f, 62c1f42, ed23f8f)
  - Introduced blocked_issues_cache table
  - Eliminated expensive recursive queries
  - Dramatically faster for large dependency graphs

- **bd list N+1 Query Elimination** (968d9e2)
  - Replaced per-issue label queries with bulk fetch
  - Significant speedup when listing many labeled issues

### Community

- **Pull Requests**:
  - #338: Prevent daemon from exiting when launcher process exits (@cpdata)
  - #337: Improve ResolvePartialID handling (@cpdata)
  - #333: Fix doctor incorrectly diagnosing hash IDs (@cpdata)
  - #327: Address critical resource leaks and error handling
  - #306: Improve missing git hook message
  - #304: Add external_ref support to daemon mode RPC
  - #302: Windows installer improvements
  - #300: Automatic .beads/.gitignore upgrade
  - #297: Context optimization features for AI agents
  - #296: Fix DB mtime update after import
  - #294: Fix compact command in daemon mode
  - #293: Add uv prerequisite documentation
  - #281: Fix bd sync Windows upstream detection
  - #277: Auto-detection of issue prefix from git history

- **Dependency Updates**:
  - Bump github.com/anthropics/anthropic-sdk-go from 1.17.0 to 1.18.0 (#330)
  - Bump golang.org/x/mod from 0.29.0 to 0.30.0 (#331)
  - Bump fastmcp from 2.13.0.2 to 2.13.1 (#332)
  - Bump pydantic from 2.12.0 to 2.12.4 (#285)
  - Bump pydantic-settings from 2.11.0 to 2.12.0 (#286)
  - Bump golangci/golangci-lint-action from 8 to 9 (#287)
  - Bump golang.org/x/sys from 0.36.0 to 0.38.0 (#288)
  - Bump github.com/ncruces/go-sqlite3 from 0.29.1 to 0.30.1 (#290)
  - Bump github.com/google/go-cmp from 0.6.0 to 0.7.0 (#291)

### Notes

This release represents a major stability and performance improvement with **179 commits** since 0.23.1. Key themes:
- **Reliability**: Fixed critical auto-import resurrection bug and multiple daemon issues
- **Performance**: Significant optimizations for `bd ready` and `bd list`
- **AI Agent Support**: Improved context propagation and error handling
- **Cross-platform**: Better Windows and macOS support
- **Developer Experience**: Auto-detection, better error messages, improved docs

## [0.23.1] - 2025-11-08

### Fixed

- **#263: Database mtime not updated after import causing false `bd doctor` warnings**
  - When `bd sync --import-only` completed, SQLite WAL mode wouldn't update the main database file's mtime
  - This caused `bd doctor` to incorrectly warn "JSONL is newer than database" even when perfectly synced
  - Now updates database mtime after imports to prevent false warnings

- **#261: SQLite URI missing 'file:' prefix causing version detection failures**
  - Without 'file:' scheme, SQLite treated `?mode=ro` as part of filename instead of connection option
  - Created bogus files like `beads.db?mode=ro`
  - Caused `bd doctor` to incorrectly report "version pre-0.17.5 (very old)" on modern databases

- **bd-17d5: Conflict marker false positives on JSON-encoded content**
  - Issues containing JSON strings with `<<<<<<<` would trigger false conflict marker detection
  - Now checks raw bytes before JSON decoding to avoid false positives

- **bd-ckvw: Schema compatibility probe prevents silent migration failures**
  - Migrations could fail silently, causing cryptic "no such column" and UNIQUE constraint errors later
  - Now probes schema after migrations, retries once if incomplete, and fails fast with clear error
  - Daemon refuses RPC if client has newer minor version to prevent schema mismatches

- **#264/#262: Remove stale `--resolve-collisions` references**
  - Docs/error messages still referenced `--resolve-collisions` flag (removed in v0.20)
  - Fixed post-merge hook error messages and git-hooks README

### Changed

- **bd-auf1: Auto-cleanup snapshot files after successful merge**
  - `.beads/` no longer accumulates orphaned `.base`, `.ours`, `.theirs` snapshot files after merges

- **bd-ky74: Optimize CLI tests with in-process testing**
  - Converted exec.Command() tests to in-process rootCmd.Execute() calls
  - **Dramatically faster: 10+ minutes → just a few seconds**
  - Improved test coverage from 20.2% to 23.3%

- **bd-6uix: Message system improvements**
  - 30s HTTP timeout prevents hangs, full message reading, --importance validation, server-side filtering

- **Remove noisy version field from metadata.json**
  - Eliminated redundant version mismatch warnings on every bd upgrade
  - Daemon version checking via RPC is sufficient

### Added

- Go agent example with Agent Mail support
- Agent Mail multi-workspace deployment guide and scripts

## [0.23.0] - 2025-11-08

### Added

- **Agent Mail Integration**: Complete Python adapter library with comprehensive documentation and multi-agent coordination tests
  - Python adapter library in `integrations/agent-mail-python/`
  - Agent Mail quickstart guide and comprehensive integration docs
  - Multi-agent race condition tests and failure scenario tests
  - Automated git traffic benchmark showing **98.5% reduction in git traffic** compared to git-only sync
  - Bash-agent integration example

- **bd info --whats-new**: Agent version awareness for quick upgrade summaries
  - Shows last 3 versions with workflow-impacting changes
  - Supports `--json` flag for machine-readable output
  - Helps agents understand what changed without re-reading full docs

- **bd hooks install**: Embedded git hooks command
  - Replaces external install script with native command
  - Git hooks now embedded in bd binary
  - Works for all bd users, not just source repo users

- **bd cleanup**: Bulk deletion command for closed issues
  - Agent-driven compaction for large databases
  - Removes closed issues older than specified threshold

### Fixed

- **3-way JSONL Merge**: Auto-invoked on conflicts
  - Automatically triggers intelligent merge on JSONL conflicts
  - No manual intervention required
  - Warning message added to zombie issues.jsonl file

- **Auto-import on Missing Database** (ab4ec90): `bd import` now auto-initializes database when missing
- **Daemon Crash Recovery**: Panic handler with socket cleanup prevents orphaned processes
- **Stale Database Exports**: ID-based staleness detection prevents exporting stale data
- **Windows MCP Subprocess Timeout**: Fix for git detection on Windows
- **Daemon Orphaning** (a6c9579): Track parent PID and exit when parent dies
- **Test Pollution Prevention** (bd-z528, bd-2c5a): Safeguards to prevent test issues in production database
- **Client Self-Heal** (a236558): Auto-recovery for stale daemon.pid files
- **Post-Merge Hook Error Messages** (abb1d1c): Show actual error messages instead of silent failures
- **Auto-import During Delete**: Disable auto-import during delete operations to prevent conflicts
- **MCP Workspace Context**: Auto-detect workspace from CWD
- **Import Sync Warning**: Warn when import syncs with working tree but not git HEAD
- **GH#254**: `bd init` now detects and chains with existing git hooks
- **GH#249**: Add nil storage checks to prevent RPC daemon crashes
- **GH#252**: Fix SQLite driver name mismatch causing "unknown driver" errors
- **Nested .beads Directories**: Prevent creating nested .beads directories
- **Windows SQLite Support**: Fix SQLite in releases for Windows

### Changed

- **Agent Affordances** (observations from agents using beads):
  - **bd new**: Added as alias for `bd create` command (agents often tried this)
  - **bd list**: Changed default to one-line-per-issue format to prevent agent miscounting; added `--long` flag for previous detailed format

- **Developer Experience**:
  - Extracted supplemental docs from AGENTS.md for better organization
  - Added warning for working tree vs git HEAD sync mismatches
  - Completion commands now work without database
  - Config included in `bd info` JSON output
  - Python cache files added to .gitignore
  - RPC diagnostics available via `BD_RPC_DEBUG` env var
  - Reduced RPC dial timeout from 2s to 200ms for fast-fail
  - Standardized daemon detection with tryDaemonLock probe
  - Improved internal/daemon test coverage to 60%

- **Code Organization**:
  - Refactored snapshot management into dedicated module
  - Documented external_ref in content hash behavior
  - Added MCP server functions for repair commands
  - Added version number to beads-mcp startup log
  - Added system requirements section for glibc compatibility in docs

- **Release Automation**:
  - Automatic Homebrew formula update in release workflow
  - Gitignore Formula/bd.rb (auto-generated, real source is homebrew-beads tap)

- **Other**:
  - Added `docs/` directory to links (#242)
  - RPC monitoring solution with web UI as implementation example (#244)
  - Remove old install.sh script, replaced by `bd hooks install`
  - Remove vc.db exclusion from FindDatabasePath filter

## [0.22.1] - 2025-11-06

### Added

- **Vendored beads-merge by @neongreen**: Native `bd merge` command for intelligent JSONL merging
  - Vendored beads-merge algorithm into `internal/merge/` with full attribution and MIT license
  - New `bd merge` command as native wrapper (no external binary needed)
  - Same field-level 3-way merge algorithm, now built into bd
  - Auto-configured during `bd init` (both interactive and `--quiet` modes)
  - Thanks to @neongreen for permission to vendor: https://github.com/neongreen/mono/issues/240
  - Original tool: https://github.com/neongreen/mono/tree/main/beads-merge

- **Git Hook Version Detection** (bd-iou5, 991c624): `bd info` now detects outdated git hooks
  - Adds version markers to all git hook templates (pre-commit, post-merge, pre-push)
  - Warns when installed hooks are outdated or missing
  - Suggests running `examples/git-hooks/install.sh` to update
  - Prevents issues like the `--resolve-collisions` flag error after updates

- **Public API for External Extensions** (8f676a4): Extensibility improvements for third-party tools
- **Multi-Repo Patterns Documentation** (e73f89e): Comprehensive guide for AI agents working across multiple repositories
- **Snapshot Versioning** (a891ebe): Add versioning and timestamp validation for snapshots
- `--clear-duplicate-external-refs` flag for `bd import` command (9de98cf)

### Fixed

- **Multi-Workspace Deletion Tracking** (708a81c, e5a6c05, 4718583): Proper deletion tracking across multiple workspaces
  - Fixes issue where deletions in one workspace weren't propagated to others
  - Added `DeleteIssue` to Storage interface for backend extensibility (e291ee0)
- **Import/Export Deadlock** (a0d24f3): Prevent import/export from hanging when daemon is running
- **Pre-Push Hook** (3ba245e): Fix pre-push hook blocking instead of exporting
- **Hash ID Recognition** (c924731, 055f1d9): Fix `isHashID` to recognize Base36 hash IDs and IDs without a-f letters
- **Git Merge Artifacts** (41b1a21): Ignore merge artifacts in `.beads/.gitignore`
- **bd status Command** (1edf3c6): Now uses git history for recent activity detection
- **Performance**: Add raw string equality short-circuit before jsonEquals (5c1f441)

### Changed

- **Code Organization**:
  - Extract SQLite migrations into separate files (b655b29)
  - Centralize BD_DEBUG logging into `internal/debug` package (95cbcf4)
  - Extract `normalizeLabels` to `internal/util/strings.go` (9520e7a)
  - Reorganize project structure: move Go files to `internal/beads`, docs to `docs/` (584c266)
  - Remove unused `internal/daemonrunner/` package (~1,500 LOC) (a7ec8a2)

- **Testing**:
  - Optimize test suite with `testing.Short()` guards for faster local testing (11fa142, 0f4b03e)
  - Add comprehensive tests for merge driver auto-config (6424ebd)
  - Add comprehensive tests for 3-way merge functionality (14b2d34)
  - Add edge case tests for `getMultiRepoJSONLPaths()` (78c9d74)

- **CI/CD**:
  - Separate Homebrew update workflow with PAT support (739786e)
  - Add manual trigger to Homebrew workflow for testing (563c12b)
  - Fix Linux checksums extraction in Homebrew workflow (c47f40b)
  - Add script to automate Nix vendorHash updates (#235)

### Performance

- Cache `getMultiRepoJSONLPaths()` to avoid redundant calls (7afb143)

## [0.22.0] - 2025-11-05

### Added

- **Intelligent Merge Driver** (bd-omx1, 52c5059): Auto-configured git merge driver for JSONL conflict resolution
  - Vendors beads-merge algorithm for field-level 3-way merging
  - Automatically configured during `bd init` (both interactive and `--quiet` modes)
  - Matches issues by identity (id + created_at + created_by)
  - Smart field merging: timestamps→max, dependencies→union, status/priority→3-way
  - Eliminates most git merge conflicts in `.beads/beads.jsonl`

- **Onboarding Wizards** (b230a22): New `bd init` workflows for different collaboration models
  - `bd init --contributor`: OSS contributor wizard (separate planning repo)
  - `bd init --team`: Team collaboration wizard (branch-based workflow)
  - Interactive setup with fork detection and remote configuration
  - Auto-configures sync settings for each workflow

- **Migration Tools** (349817a): New `bd migrate-issues` command for cross-repo issue migration
  - Migrate issues between repositories while preserving dependencies
  - Source filtering (by label, priority, status, type)
  - Automatic remote repo detection and push
  - Complete multi-repo workflow documentation

- **Multi-Phase Development Guide** (3ecc16e): Comprehensive workflow examples
  - Multi-phase development (feature → integration → deployment)
  - Multiple personas (designer, frontend dev, backend dev)
  - Best practices for complex projects

- **Dependency Status** (3acaf1d): Show blocker status in `bd show` output
  - Displays "Blocked by N open issues" when dependencies exist
  - Shows "Ready to work (no blockers)" when unblocked

- **DevContainer Support** (247e659): Automatic bd setup in GitHub Codespaces
  - Pre-configured Go environment with bd pre-installed
  - Auto-detects existing `.beads/` and imports on startup

- **Session Completion Protocol** (095e40d): Session-ending checklist for AI agents
  - Quality gates, sync procedures, git cleanup
  - Ensures clean handoff between sessions

### Fixed

- **SearchIssues N+1 Query** (bd-5ots, e90e485): Eliminated N+1 query bug in label loading
  - Batch-loads labels for all issues in one query
  - Significant performance improvement for `bd list` with many labeled issues

- **Sync Validation** (bd-9bsx, 5438485): Prevent infinite dirty loop in auto-sync
  - Added export verification to detect write failures
  - Ensures JSONL line count matches database after export

- **bd edit Direct Mode** (GH #227, d4c73c3): Force `bd edit` to always use direct mode
  - Prevents daemon interference with interactive editor sessions
  - Resolves hang issues when editing in terminals

- **SQLite Driver on arm64 macOS** (f9771cd): Fixed missing SQLite driver in arm64 builds
  - Explicitly imports CGO-enabled sqlite driver
  - Resolves "database driver not found" errors on Apple Silicon

- **external_ref Type Handling** (e1e58ef): Handle both string and *string in UpdateIssue RPC
  - Fixes type mismatch errors in MCP server
  - Ensures consistent API behavior

- **Windows Test Stability** (2ac28b0, 8c5e51e): Skip flaky concurrent tests on Windows
  - Prevents false failures in CI/CD
  - Improves overall test suite reliability

### Changed

- **Test Suite Performance** (0fc4da7): Optimized test suite for 15-18x speedup
  - Reduced redundant database operations
  - Parallelized independent test cases
  - Faster CI/CD builds

- **Priority Format** (b8785d3): Added support for P-prefix priority format (P0-P4)
  - Accepts both `--priority 1` and `--priority P1`
  - More intuitive for GitHub/Jira users

- **--label Alias** (85ca8c3): Added `--label` as alias for `--labels` in `bd create`
  - Both singular and plural forms now work
  - Improved CLI ergonomics

- **--parent Flag in Daemon Mode** (fc89f15): Added `--parent` support in daemon RPC
  - MCP server can now set parent relationships
  - Parity with CLI functionality

### Documentation

- **Multi-Repo Migration Guide** (9e60ed1): Complete documentation for multi-repo workflows
  - OSS contributors, teams, multi-phase development
  - Addresses common questions about fork vs branch workflows

- **beads-merge Setup Instructions** (527e491): Enhanced merge driver documentation
  - Installation guide for standalone binary
  - Jujutsu configuration examples

## [0.21.9] - 2025-11-05

### Added

- **Epic/Child Filtering** (bd-zkl, fbe790a): New `bd list` filters for hierarchical issue queries
  - `--ancestor <id>`: Filter by ancestor issue (shows all descendants)
  - `--parent <id>`: Filter by direct parent issue
  - `--epic <id>`: Alias for `--ancestor` (more intuitive for epic-based workflows)
  - `ancestor_id` field added to issue type for efficient epic hierarchy queries

- **Advanced List Filters**: Pattern matching, date ranges, and empty checks
  - **Pattern matching**: `--title-contains`, `--desc-contains`, `--notes-contains` (case-insensitive substring)
  - **Date ranges**: `--created-after/before`, `--updated-after/before`, `--closed-after/before`
  - **Empty checks**: `--empty-description`, `--no-assignee`, `--no-labels`
  - **Priority ranges**: `--priority-min`, `--priority-max`

- **Database Migration** (bd-bb08, 3bde4b0): Added `ON DELETE CASCADE` to `child_counters` table
  - Prevents orphaned child counter records when issues are deleted
  - Comprehensive migration tests ensure data integrity

### Fixed

- **Import Timestamp Preservation** (8b9a486): Fixed critical bug where `closed_at` timestamps were lost during sync
  - Ensures closed issues retain their original completion timestamps
  - Prevents issue resurrection timestamps from overwriting real closure times

- **Import Config Respect** (7292c85): Import now respects `import.missing_parents` config setting
  - Previously ignored config for parent resurrection behavior
  - Now correctly honors user's preference for handling missing parents

- **GoReleaser Homebrew Tap** (37ed10c): Fixed homebrew tap to point to `steveyegge/homebrew-beads`
  - Automated homebrew formula updates now work correctly
  - Resolves brew installation issues

- **npm Package Versioning** (626d51d): Added npm-package to version bump script
  - Ensures `@beads/bd` npm package stays in sync with CLI releases
  - Prevents version mismatches across distribution channels

- **Linting** (52cf2af): Fixed golangci-lint errors
  - Added proper error handling
  - Added gosec suppressions for known-safe operations

### Changed

- **RPC Filter Parity** (510ca17): Comprehensive test coverage for CLI vs RPC filter behavior
  - Ensures MCP server and CLI have identical filtering semantics
  - Validates all new filters work correctly in both modes

## [0.21.8] - 2025-11-05

### Added

- **Parent Resurrection**: Automatic resurrection of deleted parent issues from JSONL history
  - Prevents import failures when parent issues have been deleted
  - Creates tombstone placeholders for missing hierarchical parents
  - Best-effort dependency resurrection from JSONL

### Changed

- **Error Messages**: Improved error messages for missing parent issues
  - Old: `"parent issue X does not exist"`
  - New: `"parent issue X does not exist and could not be resurrected from JSONL history"`
  - **Breaking**: Scripts parsing exact error messages may need updates

### Fixed

- **JSONL Resurrection Logic**: Fixed to use LAST occurrence instead of FIRST (append-only semantics)
- **Version Bump Script**: Added `--tag` and `--push` flags to automate release tagging
  - Addresses confusion where version bump doesn't trigger GitHub release
  - New usage: `./scripts/bump-version.sh X.Y.Z --commit --tag --push`

## [0.21.7] - 2025-11-04

### Fixed

- **Memory Database Connection Pool**: Fixed `:memory:` database handling to use single shared connection
  - Prevents "no such table" errors when using in-memory databases
  - Ensures connection pool reuses the same in-memory instance
  - Critical fix for event-driven daemon mode tests

- **Test Suite Stability**: Fixed event-driven test flakiness
  - Added `waitFor` helper for event-driven testing
  - Improved timing-dependent test reliability

## [0.21.6] - 2025-11-04

### Added

- **npm Package**: Created `@beads/bd` npm package for Node.js/Claude Code for Web integration
  - Native binary downloads from GitHub releases
  - Integration tests and release documentation
  - Postinstall script for platform-specific binary installation

- **Template Support**: Issue creation from markdown templates
  - Create multiple issues from a single file
  - Structured format for bulk issue creation

- **`bd comment` Alias**: Convenient shorthand for `bd comments add`

### Changed

- **Base36 Issue IDs** (GH #213): Switched from hex to Base36 encoding for shorter, more readable IDs
  - Reduces ID length while maintaining uniqueness
  - More human-friendly format

### Fixed

- **SQLite URI Handling**: Fixed `file://` URI scheme to prevent query params in filename
  - Prevents database corruption from malformed URIs
  - Fixed `:memory:` database connection strings

- **`bd init --no-db` Behavior** (GH #210): Now correctly creates `metadata.json` and `config.yaml`
  - Previously failed to set `no-db: true` flag
  - Improved metadata-only initialization workflow

- **Symlink Path Resolution**: Fixed `findDatabaseInTree` to properly resolve symlinks
- **Epic Hierarchy Display**: Fixed `bd show` command to correctly display epic child relationships
- **CI Stability**: Fixed performance thresholds, test eligibility, and lint errors

### Dependencies

- Bumped `github.com/anthropics/anthropic-sdk-go` from 1.14.0 to 1.16.0
- Bumped `fastmcp` from 2.13.0.1 to 2.13.0.2

## [0.21.5] - 2025-11-02

### Fixed

- **Critical Double JSON Encoding Bug** (bd-1048, bd-4ec8): Fixed widespread bug in daemon RPC calls where `ResolveID` responses were incorrectly converted using `string(resp.Data)` instead of `json.Unmarshal`. This caused IDs to become double-quoted (`"\"bd-1048\""`) and database lookups to fail. Affected commands:
  - `bd show` - nil pointer dereference and 3 instances of double encoding
  - `bd dep add/remove/tree` - 5 instances
  - `bd label add/remove/list` - 3 instances  
  - `bd reopen` - 1 instance
  
  All 12 instances fixed with proper JSON unmarshaling.

## [0.21.4] - 2025-11-02

### Added

- **New Commands**:
  - `bd status` - Database overview command showing issue counts and stats
  - `bd comment` - Convenient alias for `bd comments add`
  - `bd daemons restart` - Restart specific daemon without manual kill/start
  - `--json` flag for `bd stale` command

- **Protected Branch Workflow**:
  - `BEADS_DIR` environment variable for custom database location
  - `sync.branch` configuration for protected branch workflows
  - Git worktree management with sparse checkout for sync branches
    - Only checks out `.beads/` in worktrees, minimal disk usage
    - Only used when `sync.branch` is configured, not for default users
  - Comprehensive protected branch documentation

- **Migration & Validation**:
  - Migration inspection tools for AI agents
  - Conflict marker detection in `bd import` and `bd validate`
  - Git hooks health check in `bd doctor`
  - External reference (`external_ref`) UNIQUE constraint and validation
  - `external_ref` now primary matching key for import updates

### Fixed

- **Critical Fixes**:
  - Daemon corruption from git conflicts
  - MCP `set_context` hangs with stdio transport (GH #153)
  - Double-release race condition in `importInProgress` flag
  - Critical daemon race condition causing stale exports

- **Configuration & Migration**:
  - `bd migrate` now detects and sets missing `issue_prefix` config
  - Config system refactored (renamed `config.json` → `metadata.json`)
  - Config version update in migrate command

- **Daemon & RPC**:
  - `bd doctor --json` flag not working
  - `bd import` now flushes JSONL immediately for daemon visibility
  - Panic recovery in RPC `handleConnection`
  - Daemon auto-upgrades database version instead of exiting

- **Windows Compatibility**:
  - Windows test failures (path handling, bd binary references)
  - Windows CI: forward slashes in git hook shell scripts
  - TestMetricsSnapshot/uptime flakiness on Windows

- **Code Quality**:
  - All golangci-lint errors fixed - linter now passes cleanly
  - All gosec, misspell, and unparam linter warnings resolved
  - Tightened file permissions and added security exclusions

### Changed

- Daemon automatically upgrades database schema version instead of exiting
- Git worktree management for sync branches uses sparse checkout (`.beads/` only)
- Improved test isolation and performance optimization

## [0.21.2] - 2025-11-01

### Changed
- Homebrew formula now auto-published in main repo via GoReleaser
- Deprecated separate homebrew-beads tap repository

## [0.21.1] - 2025-10-31

### Changed
- Version bump for consistency across CLI, MCP server, and plugin

## [0.20.1] - 2025-10-31

### Breaking Changes

- **Hash-Based IDs Now Default**: Sequential IDs (bd-1, bd-2) replaced with hash-based IDs (bd-a1b2, bd-f14c)
  - 4-character hashes for 0-500 issues
  - 5-character hashes for 500-1,500 issues  
  - 6-character hashes for 1,500-10,000 issues
  - Progressive length extension prevents collisions with birthday paradox math
  - **Migration required**: Run `bd migrate` to upgrade schema (removes `issue_counters` table)
  - Existing databases continue working - migration is opt-in
  - Dramatically reduces merge conflicts in multi-worker/multi-branch workflows
  - Eliminates ID collision issues when multiple agents create issues concurrently

### Removed

- **Sequential ID Generation**: Removed `SyncAllCounters()`, `AllocateNextID()`, and collision remapping logic (bd-c7af, bd-8e05, bd-4c74)
  - Hash IDs handle collisions by extending hash length, not remapping
  - `issue_counters` table removed from schema
  - `--resolve-collisions` flag removed from import (no longer needed)
  - 400+ lines of obsolete collision handling code removed

### Changed

- **Collision Handling**: Automatic hash extension on collision instead of ID remapping
  - Much simpler and more reliable than sequential remapping
  - No cross-branch coordination needed
  - Birthday paradox ensures extremely low collision rates

### Migration Notes

**For users upgrading from 0.20.0 or earlier:**

1. Run `bd migrate` to detect and upgrade old database schemas
2. Database continues to work without migration, but you'll see warnings
3. Hash IDs provide better multi-worker reliability at the cost of non-numeric IDs
4. Old sequential IDs like `bd-152` become hash IDs like `bd-f14c`

See README.md for hash ID format details and birthday paradox collision analysis.

## [0.20.0] - 2025-10-30

### Added
- **Hash-Based IDs**: New collision-resistant ID system (bd-168, bd-166, bd-167)
  - 6-character hash IDs with progressive 7/8-char fallback on collision
  - Opt-in via `.beads/config.toml` with `id_mode = "hash"`
  - Migration tool: `bd migrate --to-hash-ids` for existing databases
  - Prefix-optional ID parsing (e.g., `bd-abc123` or just `abc123`)
  - Hierarchical child ID generation for discovered-from relationships
- **Substring ID Matching**: All bd commands now support partial ID matching
  - `bd show abc` matches any ID containing "abc" (e.g., `bd-abc123`)
  - Ambiguous matches show helpful error with all candidates
- **Daemon Registry**: Multi-daemon management for multiple workspaces
  - `bd daemons list` shows all running daemons across workspaces
  - `bd daemons health` detects version mismatches and stale sockets
  - `bd daemons logs <workspace>` for per-daemon log viewing
  - `bd daemons killall` to restart all daemons after upgrades

### Fixed
- **Test Stability**: Deprecated sequence-ID collision tests
  - Kept `TestFiveCloneCollision` for hash-ID multi-clone testing
  - Fixed `TestTwoCloneCollision` to use merge instead of rebase
- **Linting**: golangci-lint v2.5.0 compatibility
  - Added `version: 2` field to `.golangci.yml`
  - Renamed `exclude` to `exclude-patterns` for v3 format

### Changed
- **Multiple bd Detection**: Warning when multiple bd binaries in PATH (PR #182)
  - Prevents confusion from version conflicts
  - Shows locations of all bd binaries found

## [0.17.7] - 2025-10-26

### Fixed
- **Test Isolation**: Export test failures due to hash caching between subtests
  - Added `ClearAllExportHashes()` method to SQLiteStorage for test isolation
  - Export tests now properly reset state between subtests
  - Fixes intermittent test failures when running full test suite

## [0.17.2] - 2025-10-25

### Added
- **Configurable Sort Policy**: `bd ready --sort` flag for work queue ordering
  - `hybrid` (default): Priority-weighted by staleness
  - `priority`: Strict priority ordering for autonomous systems
  - `oldest`: Pure FIFO for long-tail work
- **Release Automation**: New scripts for streamlined releases
  - `scripts/release.sh`: Full automated release (version bump, tests, tag, Homebrew, install)
  - `scripts/update-homebrew.sh`: Automated Homebrew formula updates

### Fixed
- **Critical**: Database reinitialization test re-landed with CI fixes
  - Windows: Fixed git path handling (forward slash normalization)
  - Nix: Skip test when git unavailable
  - JSON: Increased scanner buffer to 64MB for large issues
- **Bug**: Stale daemon socket detection
  - MCP server now health-checks cached connections before use
  - Auto-reconnect with exponential backoff on stale sockets
  - Handles daemon restarts/upgrades gracefully
- **Linting**: Fixed all errcheck warnings in production code
  - Proper error handling for database resources and transactions
  - Graceful EOF handling in interactive input
- **Linting**: Fixed revive style issues
  - Removed unused parameters, renamed builtin shadowing
- **Linting**: Fixed goconst warnings

## [0.17.0] - 2025-10-24

### Added
- **Git Hooks**: Automatic installation prompt during `bd init`
  - Eliminates race condition between auto-flush and git commits
  - Pre-commit hook: Flushes pending changes immediately before commit
  - Post-merge hook: Imports updated JSONL after pull/merge
  - Optional installation with Y/n prompt (defaults to yes)
  - See [examples/git-hooks/README.md](examples/git-hooks/README.md) for details
- **Duplicate Detection**: New `bd duplicates` command for finding and merging duplicate issues (bd-119, bd-203)
  - Automated duplicate detection with content-based matching
  - `--auto-merge` flag for batch merging duplicates
  - `--dry-run` mode to preview merges before execution
  - Helps maintain database cleanliness after imports
- **External Reference Import**: Smart import matching using `external_ref` field (bd-66-74, GH #142)
  - Issues with `external_ref` match by reference first, not content
  - Enables hybrid workflows with Jira, GitHub, Linear
  - Updates existing issues instead of creating duplicates
  - Database index on `external_ref` for fast lookups
- **Multi-Database Warning**: Detect and warn about nested beads databases
  - Prevents accidental creation of multiple databases in hierarchy
  - Helps users avoid confusion about which database is active

### Fixed
- **Critical**: Database reinitialization data loss bug (bd-130, DATABASE_REINIT_BUG.md)
  - Fixed bug where removing `.beads/` and running `bd init` would lose git-tracked issues
  - Now correctly imports from JSONL during initialization
  - Added comprehensive tests (later reverted due to CI issues on Windows/Nix)
- **Critical**: Foreign key constraint regression (bd-62, GH #144)
  - Pinned modernc.org/sqlite to v1.38.2 to avoid FK violations
  - Prevents database corruption from upstream regression
- **Critical**: Install script safety (GH #143 by @marcodelpin)
  - Prevents shell corruption from directory deletion during install
  - Restored proper error codes for safer installation
- **Bug**: Daemon auto-start reliability
  - Daemon now responsive immediately, runs initial sync in background
  - Fixes timeout issues when git pull is slow
  - Skip daemon-running check for forked child process
- **Bug**: Dependency timestamp churn during auto-import (bd-45, bd-137)
  - Auto-import no longer updates timestamps on unchanged dependencies
  - Eliminates perpetually dirty JSONL from metadata changes
- **Bug**: Import reporting accuracy (bd-49, bd-88)
  - `bd import` now correctly reports "X updated, Y unchanged" instead of "0 updated"
  - Better visibility into import operation results
- **Bug**: Memory database handling
  - Fixed :memory: database connection with shared cache mode
  - Proper URL construction for in-memory testing

### Changed
- **Removed**: Deprecated `bd repos` command
  - Global daemon architecture removed in favor of per-project daemons
  - Eliminated cross-project database confusion
- **Documentation**: Major reorganization and improvements
  - Condensed README, created specialized docs (QUICKSTART.md, ADVANCED.md, etc.)
  - Enhanced "Why not GitHub Issues?" FAQ section
  - Added Beadster to Community & Ecosystem section

### Performance
- Test coverage improvements: 46.0% → 57.7% (+11.7%)
  - Added tests for RPC, storage, cmd/bd helpers
  - New test files: coverage_test.go, helpers_test.go, epics_test.go

### Community
- Community contribution by @marcodelpin (install script safety fixes)
- Dependabot integration for automated dependency updates

## [0.16.0] - 2025-10-23

### Added
- **Automated Releases**: GoReleaser workflow for cross-platform binaries
  - Automatic GitHub releases on version tags
  - Linux, macOS, Windows binaries for amd64 and arm64
  - Checksums and changelog generation included
- **PyPI Automation**: Automated MCP server publishing to PyPI
  - GitHub Actions workflow publishes beads-mcp on version tags
  - Eliminates manual PyPI upload step
- **Sandbox Mode**: `--sandbox` flag for Claude Code integration
  - Isolated environment for AI agent experimentation
  - Prevents production database modifications during testing

### Fixed
- **Critical**: Idempotent import timestamp churn
  - Prevents timestamp updates when issue content unchanged
  - Reduces JSONL churn and git noise from repeated imports
- **Bug**: Windows CI test failures (bd-60, bd-99)
  - Fixed path separator issues and file handling on Windows
  - Skipped flaky tests to stabilize CI

### Changed
- **Configuration Migration**: Unified config management with Viper (bd-40-44, bd-78)
  - Migrated from manual env var handling to Viper
  - Bound all global flags to Viper for consistency
  - Kept `bd config` independent from Viper for modularity
  - Added comprehensive configuration tests
- **Documentation Refactor**: Improved documentation structure
  - Condensed main README
  - Created specialized guides (QUICKSTART.md, CONFIG.md, etc.)
  - Enhanced FAQ and community sections

### Testing
- Hardened `issueDataChanged` with type-safe comparisons
- Improved test isolation and reliability

## [0.15.0] - 2025-10-23

### Added
- **Configuration System**: New `bd config` command for managing configuration (GH #115)
  - Environment variable definitions with validation
  - Configuration file support (TOML/YAML/JSON)
  - Get/set/list/unset commands for user-friendly management
  - Validation and type checking for config values
  - Documentation in CONFIG.md

### Fixed
- **MCP Server**: Smart routing for lifecycle status changes in `update` tool (GH #123)
  - `update(status="closed")` now routes to `close()` tool to respect approval workflows
  - `update(status="open")` now routes to `reopen()` tool to respect approval workflows
  - Prevents bypass of Claude Code approval settings for lifecycle events
  - bd CLI remains unopinionated; routing happens only in MCP layer
  - Users can now safely auto-approve benign updates (priority, notes) without exposing closure bypass

## [0.14.0] - 2025-10-22

### Added
- **Lifecycle Safety Documentation**: Complete documentation for UnderlyingDB() usage
  - Added tracking guidelines for database lifecycle safety
  - Documented transaction management best practices
  - Prevents UAF (use-after-free) bugs in extensions

### Fixed
- **Critical**: Git worktree detection and warnings
  - Added automatic detection when running in git worktrees
  - Displays prominent warning if daemon mode is active in worktree
  - Prevents daemon from committing/pushing to wrong branch
  - Documents `--no-daemon` flag as solution for worktree users
- **Critical**: Multiple daemon race condition
  - Implemented file locking (`daemon.lock`) to prevent multiple daemons per repository
  - Uses `flock` on Unix, `LockFileEx` on Windows for process-level exclusivity
  - Lock held for daemon lifetime, automatically released on exit
  - Eliminates race conditions in concurrent daemon start attempts
  - Backward compatible: Falls back to PID check for pre-lock daemons during upgrades
- **Bug**: daemon.lock tracked in git
  - Removed daemon.lock from git tracking
  - Added to .gitignore to prevent future commits
- **Bug**: Regression in Nix Flake (#110)
  - Fixed flake build issues
  - Restored working Nix development environment

### Changed
- UnderlyingDB() deprecated for most use cases
  - New UnderlyingConn(ctx) provides safer scoped access
  - Reduced risk of UAF bugs in database extensions
  - Updated EXTENDING.md with migration guide

### Documentation
- Complete release process documentation in RELEASING.md
- Enhanced EXTENDING.md with lifecycle safety patterns
- Added UnderlyingDB() tracking guidelines

## [0.11.0] - 2025-10-22

### Added
- **Issue Merging**: New `bd merge` command for consolidating duplicate issues (bd-7, bd-11-17)
  - Merge multiple source issues into a single target issue
  - Automatically migrates all dependencies and dependents to target
  - Updates text references (bd-X mentions) across all issue fields
  - Closes source issues with "Merged into bd-Y" reason
  - Supports `--dry-run` for validation without changes
  - Example: `bd merge bd-42 bd-43 --into bd-41`
- **Multi-ID Operations**: Batch operations for increased efficiency (bd-195, #101)
  - `bd update`: Update multiple issues at once
  - `bd show`: View multiple issues in single call
  - `bd label add/remove`: Apply labels to multiple issues
  - `bd close`: Close multiple issues with one command
  - `bd reopen`: Reopen multiple issues together
  - Example: `bd close bd-1 bd-2 bd-3 --reason "Done"`
- **Daemon RPC Improvements**: Enhanced sync operations
  - `bd sync` now works correctly in daemon mode
  - Export operations properly supported via RPC
  - Prevents database access conflicts during sync
- **Acceptance Criteria Alias**: Added `--acceptance-criteria` flag (bd-228, #102)
  - Backward-compatible alias for `--acceptance` in `bd update`
  - Improves clarity and matches field name

### Fixed
- **Critical**: Test isolation and database pollution (bd-1, bd-15, bd-19, bd-52)
  - Comprehensive test isolation ensuring tests never pollute production database
  - Fixed stress test issues writing 1000+ test issues to production
  - Quarantined RPC benchmarks to prevent pollution
  - Added database isolation canary tests
- **Critical**: Daemon cache staleness
  - Daemon now detects external database modifications via mtime check
  - Prevents serving stale data after external `bd import`, `rm bd.db`, etc.
  - Cache automatically invalidates when DB file changes
- **Critical**: Counter desync after deletions
  - Issue counters now sync correctly after bulk deletions
  - Prevents ID gaps and counter drift
- **Critical**: Labels and dependencies not persisted in daemon mode (#101)
  - Fixed label operations failing silently in daemon mode
  - Fixed dependency operations not saving in daemon mode
  - Both now correctly propagate through RPC layer
- **Daemon sync support**: `bd sync` command now works in daemon mode
  - Previously crashed with nil pointer when daemon running
  - Export operations now properly routed through RPC
- **Acceptance flag normalization**: Unified `--acceptance` flag behavior (bd-228, #102)
  - Added `--acceptance-criteria` as clearer alias
  - Both flags work identically for backward compatibility
- **Auto-import Git conflicts**: Better detection of merge conflicts
  - Auto-import detects and warns about unresolved Git merge conflicts
  - Prevents importing corrupted JSONL with conflict markers
  - Clear instructions for resolving conflicts

### Changed
- **BREAKING**: Removed global daemon socket fallback
  - Each project now must use its own local daemon (.beads/bd.sock)
  - Prevents cross-project daemon connections and database pollution
  - Migration: Stop any global daemon and restart with `bd daemon` in each project
  - Warning displayed if old global socket (~/.beads/bd.sock) is found
- **Database cleanup**: Project database cleaned from 1000+ to 55 issues
  - Removed accumulated test pollution from stress testing
  - Renumbered issues for clean ID space (bd-1 through bd-55)
  - Better test isolation prevents future pollution

### Deprecated
- Global daemon socket support (see BREAKING change above)

## [0.10.0] - 2025-10-20

### Added
- **Agent Onboarding**: New `bd onboard` command for agent-first documentation
  - Outputs structured instructions for agents to integrate bd into documentation
  - Bootstrap workflow: Add 'BEFORE ANYTHING ELSE: run bd onboard' to AGENTS.md
  - Agent adapts instructions to existing project structure
  - More agentic approach vs. direct string replacement
  - Updates README with new bootstrap workflow

## [0.9.11] - 2025-10-20

### Added
- **Labels Documentation**: Comprehensive LABELS.md guide (bd-159, bd-163)
  - Complete label system documentation with workflows and best practices
  - Common label patterns (components, domains, size, quality gates, releases)
  - Advanced filtering techniques and integration examples
  - Added Labels section to README with quick reference

### Fixed
- **Critical**: MCP server crashes on None/null responses (bd-172, fixes #79)
  - Added null safety checks in `list_issues()`, `ready()`, and `stats()` methods
  - Returns empty arrays/dicts instead of crashing on None responses
  - Prevents TypeError when daemon returns empty results

## [0.9.10] - 2025-10-18

### Added
- **Label Filtering**: Enhanced `bd list` command with label-based filtering
  - `--label` (or `-l`): Filter by multiple labels with AND semantics (must have ALL)
  - `--label-any`: Filter by multiple labels with OR semantics (must have AT LEAST ONE)
  - Examples:
    - `bd list --label backend,urgent`: Issues with both 'backend' AND 'urgent'
    - `bd list --label-any frontend,backend`: Issues with either 'frontend' OR 'backend'
  - Works in both daemon and direct modes
  - Includes comprehensive test coverage
- **Log Rotation**: Automatic daemon log rotation with configurable limits
  - Prevents unbounded log file growth for long-running daemons
  - Configurable via environment variables: `BEADS_DAEMON_LOG_MAX_SIZE`, `BEADS_DAEMON_LOG_MAX_BACKUPS`, `BEADS_DAEMON_LOG_MAX_AGE`, `BEADS_DAEMON_LOG_COMPRESS`
  - Optional compression of rotated logs
  - Defaults: 50MB max size, 7 backups, 30 day retention, compression enabled
- **Batch Deletion**: Enhanced `bd delete` command with batch operations
  - Delete multiple issues at once: `bd delete bd-1 bd-2 bd-3 --force`
  - Read from file: `bd delete --from-file deletions.txt --force`
  - Dry-run mode: `--dry-run` to preview deletions before execution
  - Cascade mode: `--cascade` to recursively delete all dependents
  - Force mode: `--force` to orphan dependents instead of failing
  - Atomic transactions: all deletions succeed or none do
  - Comprehensive statistics: tracks deleted issues, dependencies, labels, and events

### Fixed
- **Critical**: `bd list --status all` showing 0 issues
  - Status filter now treats "all" as special value meaning "show all statuses"
  - Previously treated "all" as literal status value, matching no issues

## [0.9.9] - 2025-10-17

### Added
- **Daemon RPC Architecture**: Production-ready RPC protocol for client-daemon communication (bd-110, bd-111, bd-112, bd-114, bd-117)
  - Unix socket-based RPC enables faster command execution via long-lived daemon process
  - Automatic client detection with graceful fallback to direct mode
  - Serializes SQLite writes and batches git operations to prevent concurrent access issues
  - Resolves database corruption, git lock contention, and ID counter conflicts with multiple agents
  - Comprehensive integration tests and stress testing with 4+ concurrent agents
- **Issue Deletion**: `bd delete` command for removing issues with comprehensive cleanup
  - Safely removes issues from database and JSONL export
  - Cleans up dependencies and references to deleted issues
  - Works correctly with git-based workflows
- **Issue Restoration**: `bd restore` command for recovering compacted/deleted issues
  - Restores issues from git history when needed
  - Preserves references and dependency relationships
- **Prefix Renaming**: `bd rename-prefix` command for batch ID prefix changes
  - Updates all issue IDs and text references throughout the database
  - Useful for project rebranding or namespace changes
- **Comprehensive Testing**: Added scripttest-based integration tests (#59)
  - End-to-end coverage for CLI workflows
  - Tests for init command edge cases

### Fixed
- **Critical**: Metadata errors causing crashes on first import
  - Auto-import now treats missing metadata as first import instead of failing
  - Eliminates initialization errors in fresh repositories
- **Critical**: N+1 query pattern in auto-import
  - Replaced per-issue queries with batch fetching
  - Dramatically improves performance for large imports
- **Critical**: Duplicate issue imports
  - Added deduplication logic to prevent importing same issue multiple times
  - Maintains data integrity during repeated imports
- **Bug**: Auto-flush missing after renumber/rename-prefix
  - Commands now properly export to JSONL after completion
  - Ensures git sees latest changes immediately
- **Bug**: Renumber ID collision with UUID temp IDs
  - Uses proper UUID-based temporary IDs to prevent conflicts during renumbering
  - ID counter now correctly syncs after renumbering operations
- **Bug**: Collision resolution dependency handling
  - Uses unchecked dependency addition during collision remapping
  - Prevents spurious cycle detection errors
- **Bug**: macOS crashes documented (closes #3, bd-87)
  - Added CGO_ENABLED=1 workaround documentation for macOS builds

### Changed
- CLI commands now prefer RPC when daemon is running
  - Improved error reporting and diagnostics for RPC failures
  - More consistent exit codes and status messages
- Internal command architecture refactored for RPC client/server sharing
  - Reduced code duplication between direct and daemon modes
  - Improved reliability of background operations
- Ready work sort order flipped to show oldest issues first
  - Helps prioritize long-standing work items

### Performance
- Faster command execution through RPC-backed daemon (up to 10x improvement)
- N+1 query elimination in list/show operations
- Reduced write amplification from improved auto-flush behavior
- Cycle detection performance benchmarks added

### Testing
- Integration tests for daemon RPC request/response flows
- End-to-end coverage for delete/restore lifecycles  
- Regression tests for metadata handling, auto-flush, ID counter sync
- Comprehensive tests for collision detection in auto-import

### Documentation
- Release process documentation added (RELEASING.md)
- Multiple workstreams warning banner for development coordination

## [0.9.8] - 2025-10-16

### Added
- **Background Daemon Mode**: `bd daemon` command for continuous auto-sync (#bd-386)
  - Watches for changes and automatically exports to JSONL
  - Monitors git repository for incoming changes and auto-imports
  - Production-ready with graceful shutdown, PID file management, and signal handling
  - Eliminates manual export/import in active development workflows
- **Git Synchronization**: `bd sync` command for automated git workflows (#bd-378)
  - One-command sync: stage, commit, pull, push JSONL changes
  - Automatic merge conflict resolution with collision remapping
  - Status reporting shows sync progress and any issues
  - Ideal for distributed teams and CI/CD integration
- **Issue Compaction**: `bd compact` command to summarize old closed issues (bd-254-264)
  - AI-powered summarization using Claude Haiku
  - Reduces database size while preserving essential information
  - Configurable thresholds for age, dependencies, and references
  - Compaction status visible in `bd show` output
- **Label and Title Filtering**: Enhanced `bd list` command (#45, bd-269)
  - Filter by labels: `bd list --label bug,critical`
  - Filter by title: `bd list --title "auth"`
  - Combine with status/priority filters
- **List Output Formats**: `bd list --format` flag for custom output (PR #46)
  - Format options: `default`, `compact`, `detailed`, `json`
  - Better integration with scripts and automation tools
- **MCP Reopen Support**: Reopen closed issues via MCP server
  - Claude Desktop plugin can now reopen issues
  - Useful for revisiting completed work
- **Cross-Type Cycle Prevention**: Dependency cycles detected across all types
  - Prevents A→B→A cycles even when mixing `blocks`, `related`, etc.
  - Semantic validation for parent-child direction
  - Diagnostic warnings when cycles detected

### Fixed
- **Critical**: Auto-import collision skipping bug (bd-393, bd-228)
  - Import would silently skip collisions instead of remapping
  - Could cause data loss when merging branches
  - Now correctly applies collision resolution with remapping
- **Critical**: Transaction state corruption
  - Nested transactions could corrupt database state
  - Fixed with proper transaction boundary handling
- **Critical**: Concurrent temp file collisions (bd-306, bd-373)
  - Multiple `bd` processes would collide on shared `.tmp` filename
  - Now uses PID suffix for temp files: `.beads/issues.jsonl.tmp.12345`
- **Critical**: Circular dependency detection gaps
  - Some cycle patterns were missed by detection algorithm
  - Enhanced with comprehensive cycle prevention
- **Bug**: False positive merge conflict detection (bd-313, bd-270)
  - Auto-import would detect conflicts when none existed
  - Fixed with improved Git conflict marker detection
- **Bug**: Import timeout with large issue sets
  - 200+ issue imports would timeout
  - Optimized import performance
- **Bug**: Collision resolver missing ID counter sync
  - After remapping, ID counters weren't updated
  - Could cause duplicate IDs in subsequent creates
- **Bug**: NULL handling in statistics for empty databases (PR #37)
  - `bd stats` would crash on newly initialized databases
  - Fixed NULL value handling in GetStatistics

### Changed
- Compaction removes snapshot/restore (simplified to permanent decay)
- Export file writing refactored to avoid Windows Defender false positives (PR #31)
- Error handling improved in auto-import and fallback paths (PR #47)
- Reduced cyclomatic complexity in main.go (PR #48)
- MCP integration tests fixed and linting cleaned up (PR #40)

### Performance
- Cycle detection benchmarks added
- Import optimization for large issue sets
- Export uses PID-based temp files to avoid lock contention

### Community
- Merged PR #31: Windows Defender mitigation for export
- Merged PR #37: Fix NULL handling in statistics
- Merged PR #38: Nix flake for declarative builds
- Merged PR #40: MCP integration test fixes
- Merged PR #45: Label and title filtering for bd list
- Merged PR #46: Add --format flag to bd list
- Merged PR #47: Error handling consistency
- Merged PR #48: Cyclomatic complexity reduction

## [0.9.2] - 2025-10-14

### Added
- **One-Command Dependency Creation**: `--deps` flag for `bd create` (#18)
  - Create issues with dependencies in a single command
  - Format: `--deps type:id` or just `--deps id` (defaults to blocks)
  - Multiple dependencies: `--deps discovered-from:bd-20,blocks:bd-15`
  - Whitespace-tolerant parsing
  - Particularly useful for AI agents creating discovered-from issues
- **External Reference Tracking**: `external_ref` field for linking to external trackers
  - Link bd issues to GitHub, Jira, Linear, etc.
  - Example: `bd create "Issue" --external-ref gh-42`
  - `bd update` supports updating external references
  - Tracked in JSONL for git portability
- **Metadata Storage**: Internal metadata table for system state
  - Stores import hash for idempotent auto-import
  - Enables future extensibility for system preferences
  - Auto-migrates existing databases
- **Windows Support**: Complete Windows 11 build instructions (#10)
  - Tested with mingw-w64
  - Full CGo support documented
  - PATH setup instructions
- **Go Extension Example**: Complete working example of database extensions (#15)
  - Demonstrates custom table creation
  - Shows cross-layer queries joining with issues
  - Includes test suite and documentation
- **Issue Type Display**: `bd list` now shows issue type in output (#17)
  - Better visibility: `bd-1 [P1] [bug] open`
  - Helps distinguish bugs from features at a glance

### Fixed
- **Critical**: Dependency tree deduplication for diamond dependencies (bd-85, #1)
  - Fixed infinite recursion in complex dependency graphs
  - Prevents duplicate nodes at same level
  - Handles multiple blockers correctly
- **Critical**: Hash-based auto-import replaces mtime comparison
  - Git pull updates mtime but may not change content
  - Now uses SHA256 hash to detect actual changes
  - Prevents unnecessary imports after git operations
- **Critical**: Parallel issue creation race condition (PR #8, bd-66)
  - Multiple processes could generate same ID
  - Replaced in-memory counter with atomic database counter
  - Syncs counters after import to prevent collisions
  - Comprehensive test coverage

### Changed
- Auto-import now uses content hash instead of modification time
- Dependency tree visualization improved for complex graphs
- Better error messages for dependency operations

### Community
- Merged PR #8: Parallel issue creation fix
- Merged PR #10: Windows build instructions
- Merged PR #12: Fix quickstart EXTENDING.md link
- Merged PR #14: Better enable Go extensions
- Merged PR #15: Complete Go extension example
- Merged PR #17: Show issue type in list output

## [0.9.1] - 2025-10-14

### Added
- **Incremental JSONL Export**: Major performance optimization
  - Dirty issue tracking system to only export changed issues
  - Auto-flush with 5-second debounce after CRUD operations
  - Automatic import when JSONL is newer than database
  - `--no-auto-flush` and `--no-auto-import` flags for manual control
  - Comprehensive test coverage for auto-flush/import
- **ID Space Partitioning**: Explicit ID assignment for parallel workers
  - `bd create --id worker1-100` for controlling ID allocation
  - Enables multiple agents to work without conflicts
  - Documented in CLAUDE.md for agent workflows
- **Auto-Migration System**: Seamless database schema upgrades
  - Automatically adds dirty_issues table to existing databases
  - Silent migration on first access after upgrade
  - No manual intervention required

### Fixed
- **Critical**: Race condition in dirty tracking (TOCTOU bug)
  - Could cause data loss during concurrent operations
  - Fixed by tracking specific exported IDs instead of clearing all
- **Critical**: Export with filters cleared all dirty issues
  - Status/priority filters would incorrectly mark non-matching issues as clean
  - Now only clears issues that were actually exported
- **Bug**: Malformed ID detection never worked
  - SQLite CAST returns 0 for invalid strings, not NULL
  - Now correctly detects non-numeric ID suffixes like "bd-abc"
  - No false positives on legitimate zero-prefixed IDs
- **Bug**: Inconsistent dependency dirty marking
  - Duplicated 20+ lines of code in AddDependency/RemoveDependency
  - Refactored to use shared markIssuesDirtyTx() helper
- Fixed unchecked error in import.go when unmarshaling JSON
- Fixed unchecked error returns in test cleanup code
- Removed duplicate test code in dependencies_test.go
- Fixed Go version in go.mod (was incorrectly set to 1.25.2)

### Changed
- Export now tracks which specific issues were exported
- ClearDirtyIssuesByID() added (ClearDirtyIssues() deprecated with race warning)
- Dependency operations use shared dirty-marking helper (DRY)

### Performance
- Incremental export: Only writes changed issues (vs full export)
- Regex caching in ID replacement: 1.9x performance improvement
- Automatic debounced flush prevents excessive I/O

## [0.9.0] - 2025-10-12

### Added
- **Collision Resolution System**: Automatic ID remapping for import collisions
  - Reference scoring algorithm to minimize updates during remapping
  - Word-boundary regex matching to prevent false replacements
  - Automatic updating of text references and dependencies
  - `--resolve-collisions` flag for safe branch merging
  - `--dry-run` flag to preview collision detection
- **Export/Import with JSONL**: Git-friendly text format
  - Dependencies embedded in JSONL for complete portability
  - Idempotent import (exact matches detected)
  - Collision detection (same ID, different content)
- **Ready Work Algorithm**: Find issues with no open blockers
  - `bd ready` command shows unblocked work
  - `bd blocked` command shows what's waiting
- **Dependency Management**: Four dependency types
  - `blocks`: Hard blocker (affects ready work)
  - `related`: Soft relationship
  - `parent-child`: Epic/subtask hierarchy
  - `discovered-from`: Track issues discovered during work
- **Database Discovery**: Auto-find database in project hierarchy
  - Walks up directory tree like git
  - Supports `$BEADS_DB` environment variable
  - Falls back to `~/.beads/default.db`
- **Comprehensive Documentation**:
  - README.md with 900+ lines of examples and FAQs
  - CLAUDE.md for AI agent integration patterns
  - SECURITY.md with security policy and best practices
  - TEXT_FORMATS.md analyzing JSONL approach
  - EXTENDING.md for database extension patterns
  - GIT_WORKFLOW.md for git integration
- **Examples**: Real-world integration patterns
  - Python agent implementation
  - Bash agent script
  - Git hooks for automatic export/import
  - Branch merge workflow with collision resolution
  - Claude Desktop MCP integration (coming soon)

### Changed
- Switched to JSONL as source of truth (from binary SQLite)
- SQLite database now acts as ephemeral cache
- Issue IDs generated with numerical max (not alphabetical)
- Export sorts issues by ID for consistent git diffs

### Security
- SQL injection protection via allowlisted field names
- Input validation for all issue fields
- File path validation for database operations
- Warnings about not storing secrets in issues

## [0.1.0] - Initial Development

### Added
- Core issue tracking (create, update, list, show, close)
- SQLite storage backend
- Dependency tracking with cycle detection
- Label support
- Event audit trail
- Full-text search
- Statistics and reporting
- `bd init` for project initialization
- `bd quickstart` interactive tutorial

---

## Version History

- **0.9.8** (2025-10-16): Daemon mode, git sync, compaction, critical bug fixes
- **0.9.2** (2025-10-14): Community PRs, critical bug fixes, and --deps flag
- **0.9.1** (2025-10-14): Performance optimization and critical bug fixes
- **0.9.0** (2025-10-12): Pre-release polish and collision resolution
- **0.1.0**: Initial development version

## Upgrade Guide

### Upgrading to 0.9.8

No breaking changes. All changes are backward compatible:
- **bd daemon**: New optional background service for auto-sync workflows
- **bd sync**: New optional git integration command
- **bd compact**: New optional command for issue summarization (requires Anthropic API key)
- **--format flag**: Optional new feature for `bd list`
- **Label/title filters**: Optional new filters for `bd list`
- **Bug fixes**: All critical fixes are transparent to users

Simply pull the latest version and rebuild:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
# or
git pull && go build -o bd ./cmd/bd
```

**Note**: The `bd compact` command requires an Anthropic API key in `$ANTHROPIC_API_KEY` environment variable. All other features work without any additional setup.

### Upgrading to 0.9.2

No breaking changes. All changes are backward compatible:
- **--deps flag**: Optional new feature for `bd create`
- **external_ref**: Optional field, existing issues unaffected
- **Metadata table**: Auto-migrates on first use
- **Bug fixes**: All critical fixes are transparent to users

Simply pull the latest version and rebuild:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
# or
git pull && go build -o bd ./cmd/bd
```

### Upgrading to 0.9.1

No breaking changes. All changes are backward compatible:
- **Auto-migration**: The dirty_issues table is automatically added to existing databases
- **Auto-flush/import**: Enabled by default, improves workflow (can disable with flags if needed)
- **ID partitioning**: Optional feature, use `--id` flag only if needed for parallel workers

If you're upgrading from 0.9.0, simply pull the latest version. Your existing database will be automatically migrated on first use.

### Upgrading to 0.9.0

No breaking changes. The JSONL export format is backward compatible.

If you have issues in your database:
1. Run `bd export -o .beads/issues.jsonl` to create the text file
2. Commit `.beads/issues.jsonl` to git
3. Add `.beads/*.db` to `.gitignore`

New collaborators can clone the repo and run:
```bash
bd import -i .beads/issues.jsonl
```

The SQLite database will be automatically populated from the JSONL file.

## Future Releases

See open issues tagged with milestone markers for planned features in upcoming releases.

For version 1.0, see: `bd dep tree bd-8` (the 1.0 milestone epic)
