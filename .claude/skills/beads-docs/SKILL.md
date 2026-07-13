---
name: beads-docs
description: >-
  Project conventions for writing, editing, restructuring, or reviewing the
  beads user documentation — the Mintlify site under docs/. Use this whenever
  you touch anything in docs/ (pages, concept docs, reference, integration
  guides, recovery runbooks, diagrams, docs.json navigation) or write/edit
  prose about beads, even when the request is just "fix the docs", "write a
  docs page", "the docs are wrong/confusing", "rename X across the docs", or
  an edit to a file under docs/. It defines the canonical concept model
  (bead → dependencies → ready work; formula → proto → molecule/wisp; gates;
  Dolt sync and federation), required terminology, the prose/emphasis/diagram
  conventions, the rule that generated docs are edited at their source, and
  the gates to run before docs work is done.
---

# Writing beads docs

This is the house style for `docs/` — the beads user documentation, published
as a Mintlify site. The goal is docs that motivate before they jargon, say the
same thing the same way everywhere, show concepts as pictures and snippets
instead of walls of prose, and never drift from the code. Apply it to any page
you create, edit, or review.

The audience of `docs/` is a **user** of beads — a human or agent installing
`bd`, tracking work, and syncing it. Contributor-facing material lives in
`engdocs/` and `AGENTS.md` and follows different rules (it may describe the
implementation literally). When this skill says "the docs," it means `docs/`.

## 1. The canonical model

Beads is a dependency-aware issue graph with a workflow layer on top. Teach it
consistently and link to the canonical concept page —
**`docs/core-concepts/index.md`** — rather than re-explaining it.

| Concept | Role | Key idea |
|---|---|---|
| **bead** (issue) | the unit of work | one tracked item with a hash ID (`bd-a1b2`), type, status, priority; "bead" and "issue" name the same thing |
| **dependency** | ordering | `blocks` edges hide a bead from agents until its blockers close; `parent-child`, `related`, `discovered-from` organize without blocking |
| **ready work** | what `bd ready` computes | open beads with no open blockers, excluding in_progress, blocked, deferred, and hooked — the claimable frontier |
| **formula** | workflow source | a TOML/JSON file defining a DAG of steps; `bd cook` compiles it into a proto |
| **proto** | workflow template | a template epic (label `template`) with `{{variables}}`; not live work |
| **molecule** | instantiated workflow | real beads poured from a proto (`bd mol pour`); persistent |
| **wisp** | ephemeral molecule | same instantiation, transient lifecycle (`bd mol wisp`); purged by `bd purge` |
| **gate** | async wait | blocks a workflow step until closed — by a human, a timer, a GitHub run/PR, or a cross-rig bead |
| **sync** | cross-machine movement | Dolt push/pull over `refs/dolt/data` on the git remote; `.beads/issues.jsonl` is a passive export, never the database |
| **federation** | cross-repo sync | peer-to-peer sharing of beads across repos/organizations |

The pipeline worth internalizing: **formula → (cook) → proto → (pour) →
molecule** or **→ (wisp) → wisp**. Gates pause molecule steps; `bd ready`
surfaces the claimable steps; sync moves the whole graph between machines.

Storage facts that pages keep getting wrong: embedded mode (the default
`bd init`) stores data at `.beads/embeddeddolt/`; server mode
(`bd init --server`) uses `.beads/dolt/`. Never present `.beads/dolt/` as the
general data path.

**Cross-project vocabulary (beads ↔ Gas City).** Gas City (the sibling
project) shares the words molecule, formula, wisp, and gate, but the two doc
corpora use them differently: Gas City's docs treat molecule/wisp as v1
implementation detail (never a user concept), and a Gas City formula is an
orchestration method the orchestrator runs across agents. In beads,
molecule/wisp/proto ARE user concepts, and a formula is the TOML source you
cook into a proto. Never import Gas City's definitions into beads pages (or
vice versa); when a page must bridge the two projects, say explicitly which
project's sense is meant.

## 2. Required terminology

Use the left column; never the right (except as noted). The full
prose-vs-literal rename discipline is in
[references/terminology.md](references/terminology.md).

| Use this | Not this | Notes |
|---|---|---|
| **bead** / **issue** | "task", "ticket", "TODO item" as the unit's name | Both terms are correct and interchangeable; lead with *bead* when teaching identity, use *issue* when mirroring CLI output or flags (`bd create`, "issue types"). "task" is one issue *type*, never the generic unit. |
| **ready work** | "unblocked queue", "available tasks", "the ready set" as a formal term | Say what `bd ready` returns: open beads with no open blockers. |
| **proto** | "template" as a noun for the concept | `template` stays only as the literal label name protos carry. |
| **molecule** | "mol" in prose | `mol` is the command literal (`bd mol pour`); the concept is a molecule. |
| **formula** | conflating formula with molecule/proto | The formula is the *file*; cooking makes a proto; pouring makes live work. |
| **gate** | "barrier", "checkpoint", "lock" | Gates are async wait conditions with types (human, timer, gh:run, gh:pr, bead). |
| **sync** = Dolt push/pull | export/import as a sync workflow | `bd dolt push` / `bd dolt pull` over `refs/dolt/data`. `.beads/issues.jsonl` is a passive export for viewers and interchange. |
| **embedded mode** / **server mode** | "local mode", "daemon mode" for storage | Embedded is the default; data at `.beads/embeddeddolt/`. Server mode connects to `dolt sql-server`; data at `.beads/dolt/`. |
| **federation** | "multi-repo sync" as a distinct feature name | Federation is the peer-to-peer cross-repo sharing feature. |
| **hash ID** | "random ID", "UUID" | IDs like `bd-a1b2` are content-derived hashes sized adaptively to prevent collisions. |

## 3. Content stance

- **Motivate before you mechanize.** Lead a page with the problem it solves,
  then the solution, then the mechanics. Never open on vocabulary.
- **Docs are not the project's history.** No `internal/*` package paths, no
  "this was removed in vX", no "(v0.20.1+)" gating on user pages — beads is a
  1.x product and pre-1.0 archaeology belongs in `engdocs/` or the CHANGELOG.
- **Lead with the value.** Beads' value is persistent, dependency-aware memory
  for coding agents — agents that survive context loss because the work graph
  outlives the session. The comparison framing (vs GitHub Issues, Jira,
  markdown TODO lists) is the best newcomer conversion tool; keep it early
  where it exists.
- **One concrete example beats three abstract sentences.** Where you assert a
  capability, show a `bd` invocation and what it prints.

## 4. Information architecture

Navigation lives in `docs/docs.json`: Getting Started, Core Concepts,
Architecture, Workflows, Recovery, Multi-Agent, Integrations, Community, and
Reference — with the generated CLI Reference nested inside Reference as a
collapsed sub-group.

- **Every section has an index/Overview page** that introduces the section in
  a sentence or two, then lists every child page with a one-line accurate
  summary and link.
- One page, one purpose. A page that tries to teach *and* specify does
  neither — split it and cross-link.
- Concept material is unified on `core-concepts/index`; don't re-derive the
  model on other pages — link to it.
- The repository map belongs in the README, not in `docs/`.

## 5. Prose doctrine — cut words, sharpen points

Most bloat is information stored in the wrong medium. Move it to a cheaper
carrier, then delete what isn't pulling weight. Every page must **stand
alone** — a reader landing cold needs a one-line setup, not a previous page.

**Convert** (move load off prose):
- A relationship or sequence → a **diagram** (mermaid renders natively;
  richer diagrams follow the Excalidraw pipeline — see §7).
- Parallel options/fields/comparisons → a **table**.
- "you run X, which does Y" narration → an **annotated CLI snippet** (show
  the command and its output, comment the interesting lines).
- Edge cases and deep mechanics → an `<Accordion>`, or a reference page.
  Keep the 80% case on the page.

**Delete** (the load was fake): throat-clearing openers ("In this section
we'll…"), hedge chains ("generally / typically / in most cases"),
restatement, narrating an artifact a snippet already shows, and adjectives
standing in for evidence.

When you run a deliberate simplification pass, follow
[references/simplification.md](references/simplification.md) — the per-page
loop and the two guardrails that keep a trim honest: a **loss-check** (never
drop a fact that lives nowhere else) and a **fact-check** (trimmed prose must
not drift from the code).

## 6. Emphasis and formatting

- **Bold** *names a term*, on first mention only. *Italic* marks a property
  or contrast. ~1–2 marks per paragraph; never re-emphasize a term already
  introduced; never give one phrase both treatments.
- No body `# H1` — the frontmatter `title` is the H1. Use `##`/`###`.
- Links are root-relative and extensionless (`/getting-started/quickstart`).
  Links out of the site (engdocs/, repo files) use full GitHub URLs.
- Mintlify parses `.md` as MDX: no HTML comments (`{/* … */}` instead), and
  angle-bracket placeholders like `<id>` must stay inside backticks or code
  fences.
- Mintlify components (`<Note>`, `<Tip>`, `<Warning>`, `<Accordion>`)
  sparingly — they lose force with repetition.

## 7. Diagrams

Mermaid fences render natively — prefer them for graphs and flows. For richer
diagrams use the Excalidraw pipeline: author the `.excalidraw` source under
`docs/diagrams/excalidraw/`, render with `make diagrams-excalidraw` (source
and rendered `.svg` are both committed), embed as
`/diagrams/excalidraw-rendered/<name>.svg` with descriptive alt text, and
keep labels short (a two-line "Name / role" beats a sentence crammed in a
box). Two non-negotiables:

- **Rasterize and look at every rendered diagram** — text overflow and layout
  problems are invisible in a text diff.
- **Diagrams and images cannot be reviewed in a diff.** Render them and get
  the maintainer's approval before committing.

## 8. Generated content is edited at its source

Never hand-edit a generated file. The generated surfaces are:

- `docs/cli-reference/*.md` and the CLI Reference pages array inside
  `docs/docs.json` — bd emits vendor-neutral pages (`bd help --docs-root`,
  from the Cobra command strings in `cmd/bd/*.go`) into an uncommitted
  staging tree, and `tools/docsmint` post-processes them into the committed
  Mintlify form. bd itself never emits Mintlify (or any site-generator)
  specifics; that lives in docsmint.
- `docs/CLI_REFERENCE.md` — the single-file reference, emitted directly by
  `bd help --docs-root`.

To change wording in any of them, edit the Go source (`Short:`, `Long:`,
`Example:` strings) and run `./scripts/generate-cli-docs.sh` (which runs both
stages). To change the Mintlify page form itself (comment markers, link
style, nav), edit `tools/docsmint` and its tests. The drift gates
(`generate-cli-docs.sh --check`, `scripts/check-cli-docs-drift.sh` in PR CI,
and the docs-autofix bot) fail or auto-fix any hand edit.

## 9. Verify before you call it done

Run the gates in [references/verification.md](references/verification.md).
The short list: `go test ./test/docsync` (nav↔file sync + link conventions),
`./scripts/generate-cli-docs.sh --check` (generated docs fresh),
`./scripts/check-doc-freshness.sh` (Last reviewed markers), and a live
preview with `make docs-dev` (or `./mint.sh dev`) at `localhost:3000`.

When you **move or remove a page**: add a redirect to the `redirects` array
in `docs/docs.json`, rewrite inbound links repo-wide (README, engdocs/,
examples/, npm-package/, plugin resources — grep, don't guess), and check
whether `bd` prints the old path (if so, fix the Go source and regenerate —
never a pointer stub; decision 6 covers old routes with redirects).

## 10. Review and commit discipline

- **Author → the maintainer reviews → commit on explicit approval.** This
  matters most for diagrams and images, which can't be reviewed in a text
  diff.
- Group commits by audience: user docs (`docs/`) separate from contributor
  docs (`engdocs/`, `AGENTS.md`) separate from generator/Go changes.
- Track docs work as bd issues per `AGENTS.md`.
