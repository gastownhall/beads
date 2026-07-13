# Verification gates

Run these before considering docs work complete. Ordered cheapest-first.

## Durable repo gates (committed — always available)

```bash
# 1. Docs sync: docs.json nav <-> file consistency, link conventions
#    (root-relative extensionless in docs/, exact paths in engdocs/ and
#    curated root files), orphan detection (only CLI_REFERENCE.md is exempt).
go test ./test/docsync

# 2. Generated CLI docs freshness: regenerates from the live command tree
#    and diffs docs/CLI_REFERENCE.md, docs/cli-reference/, and the CLI
#    pages array in docs/docs.json (bd emits generic pages to staging;
#    tools/docsmint produces the committed Mintlify form).
./scripts/generate-cli-docs.sh --check
# CI's blame-scoped variant (only fails PRs for drift they introduced):
./scripts/check-cli-docs-drift.sh

# 3. Doc flags + freshness markers: stale flag/command references and the
#    `Last reviewed:` / `Freshness source:` markers on reference docs.
#    (make check-docs runs 1 + 3 together.)
./scripts/check-doc-flags.sh ./bd
./scripts/check-doc-freshness.sh

# 4. Live preview while editing.
make docs-dev              # or: ./mint.sh dev   -> http://localhost:3000

# 5. Broken links the way CI checks them (baseline-aware on PRs via
#    .github/workflows/docs-mintlify.yml):
./mint.sh broken-links
```

## Principles the gates don't fully cover

- **Every code fence must be real.** A `bash` fence should show a command the
  current `bd` accepts (check the generated CLI reference); TOML fences for
  formulas must parse.
- **MDX validity**: no HTML comments (`{/* … */}` instead), no bare
  angle-bracket placeholders outside backticks, balanced Mintlify components.
- **No body `# H1`** — frontmatter `title` is the H1. (Beware false
  positives: `#` comments inside code fences are not H1s.)
- **Freshness markers**: pages carrying `Last reviewed:` / `Freshness
  source:` lines (configuration, ide-setup, azure-devops, json-schema,
  init-safety runbooks) must keep them intact and current when edited —
  `scripts/check-doc-freshness.sh` enforces format, age, and that the named
  source paths exist.

## Moving or removing a page

Removing or merging a page breaks inbound links and external bookmarks. Do
all of:

1. **Redirect** — add an entry to the `redirects` array in `docs/docs.json`
   from the old route to the new one.
2. **Rewrite inbound links** — grep the whole repo, not just `docs/`:
   README.md, AGENTS.md, AGENT_INSTRUCTIONS.md, engdocs/, examples/,
   npm-package/, plugins/, integrations/, scripts, and Go comments.
3. **Check bd's printed output** — if `bd` prints the old path (grep `cmd/`
   and `internal/templates/` for it), fix the Go source to print the new
   path and regenerate the CLI docs. Do **not** create a pointer stub at the
   old path — decision 6 (no pointer stubs; Mintlify redirects) accepts that
   old GitHub links printed by already-released binaries 404.
4. **Fix anchor text** — if a link's label named the old page, update the
   label too, and dedupe links that now collapse to the same target.

## Review rule for images

Diagrams and images cannot be reviewed in a text diff. Render them (rasterize
SVGs), look at them, and get the maintainer's approval before committing.
