# Beads Documentation Site (Docusaurus)

This is the Docusaurus site behind https://gastownhall.github.io/beads/.

> **Status: parallel-run.** The docs are being migrated to a Mintlify site
> that lives in `docs/` (preview with `../mint.sh dev`). This Docusaurus site
> keeps building and deploying until the cutover, after which `website/` and
> `.github/workflows/deploy-docs.yml` retire. Author new content in `docs/`;
> only fix things here that break the still-live site.

## Local development

```bash
cd website
npm ci
npm start          # dev server with hot reload at http://localhost:3000
```

## Build and verify

```bash
npm run typecheck  # TS config sanity
npm run build      # production build into website/build/
npm run serve      # serve the production build locally
```

The CI package gate is `make ci-website` from the repo root (npm ci,
typecheck, `scripts/generate-llms-full.sh`, build). The Pages deploy runs
from `.github/workflows/deploy-docs.yml` on pushes to `main` that touch
`website/**`.

## Generated content

- `website/docs/cli-reference/` and the versioned CLI snapshots are emitted
  by `bd help --docs-root` via `scripts/generate-cli-docs.sh` — never edit
  them by hand.
- `website/static/llms-full.txt` and `llms.txt` come from
  `scripts/generate-llms-full.sh`.
- Doc versions live in `website/versioned_docs/` (managed by
  `npm run docusaurus docs:version <x.y.z>` at release time).
