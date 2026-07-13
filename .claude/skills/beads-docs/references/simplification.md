# Running a simplification pass

Read this when you're trimming a page or a whole section — "simplify the
reference", "this page is a wall of text", "cut this down". SKILL.md §5–§7
define the *moves* (convert to a cheaper carrier, delete dead load, emphasis,
diagrams). This file is the *process*: how to run those moves across real
pages without dropping facts or drifting from the code.

## The goal

Remove **redundancy and unnecessary words**, and make the point *sharper*,
not thinner. **Word count is the output, not the target** — never trim toward
a number. The win is almost never deleting information; it's moving it to a
cheaper carrier (a table, an accordion, a diagram, or a link to the page that
already owns it) and deleting what was only restatement.

How much a page sheds depends entirely on how much *genuine redundancy* it
carries:

- A page that **restates what a sibling page owns** sheds a lot — replace the
  restatement with one sentence plus a link to the owner (usually
  `/core-concepts/index` for model material, `/cli-reference/<cmd>` for
  command detail).
- A page whose length is **earned teaching** (worked examples plus the
  behavior they illustrate) barely moves — forcing it toward a number guts
  the teaching.
- A page that is **already lean** gets left alone entirely.

Accordions and tables fold content for scannability without deleting words —
don't measure that work by the word delta.

## The per-page loop

1. **Measure.** `wc -w <page>`; note the count.
2. **Find opportunities, by carrier:** dedup-by-link, prose→table,
   prose→accordion, add/reuse a diagram, delete (throat-clearing, hedges,
   restatement).
3. **Apply** the moves in the page's own voice. Never reflow prose you are
   *not* simplifying — it buries the real change in the diff.
4. **Loss-check** (below).
5. **Fact-check** (below).
6. **Gates:** `go test ./test/docsync`, every fence parses, no body H1, no
   HTML comments (MDX), preview with `make docs-dev`.
7. **Preview, then commit on approval** — one page or section per batch, so
   the reviewer can steer before a cut compounds across the section.

## Loss-check: simplification must not lose facts

Trimming is where real details quietly disappear. After a pass, diff the page
against its pre-trim version and walk the removed lines:

> For each removed block: is it an important **fact, command, flag, config
> key, caveat, behavior, or worked example** — and is it **preserved nowhere
> else** (`grep` across `docs/` to check for relocation)?

Separate removals honestly:

- **Intentional cuts** (leave them gone): terminology migration,
  hedge/restatement deletion, content relocated to a better carrier (with a
  redirect), deliberately-retired stale claims (pre-1.0 gating, removed
  commands).
- **Genuine losses** (restore): a real detail dropped with no home elsewhere.

Restore a genuine loss as a **sharpened clause, not restored bulk** — fold
the missing *why* back into a sentence you kept rather than re-adding the
paragraph you cut.

## Fact-check: simplified prose drifts from code

Authoring and trimming both introduce drift — a stale version number, a
renamed flag surviving in prose, a rewritten sentence overstating the
implementation. Before pushing, verify every **checkable claim** on the
touched pages against the current code: CLI commands/subcommands/flags
(cheapest check: the generated `docs/cli-reference/` pages, which are correct
by construction), config keys and defaults (`internal/configfile/`,
`cmd/bd/config.go`), environment variables, file and directory paths
(embedded mode data lives at `.beads/embeddeddolt/`, server mode at
`.beads/dolt/`), issue types and dependency types, and numeric defaults.

Verify **adversarially**: try to prove each claim *false* against a
`file:line`, defaulting to "unverified" rather than "fine". In practice a
verification pass catches several real errors per rewrite. Generated
reference pages are exempt — they are correct by regeneration, and
fact-checking them against their own source is circular.
