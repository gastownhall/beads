# Security Dependency Exceptions

Last reviewed: 2026-04-28

This file records dependency advisories that are accepted temporarily because
they do not affect shipped runtime code. Runtime dependency advisories are not
eligible for this exception without a separate security review.

## Website build toolchain

Scope:

- `website/package.json`
- `website/package-lock.json`
- Docusaurus, webpack, Mermaid, and related static-site build tooling

Status:

- `npm audit --omit=dev` is clean.
- `GOTOOLCHAIN=go1.26.2 govulncheck ./...` is clean.
- A full `npm audit` still reports build-time advisories through the
  Docusaurus/webpack toolchain, primarily `serialize-javascript`, `uuid`,
  `sockjs`, and transitive Docusaurus packages.

Risk decision:

The website is built into static files and deployed to GitHub Pages. The
production site does not run a Node.js server or load these Node packages at
runtime. The dependencies have been moved to `devDependencies` so production
audits track the actual runtime surface.

Required controls:

- `npm audit --omit=dev` for `website/` must remain clean.
- The Docusaurus dev server must not be exposed publicly.
- Renovate must remain enabled for npm and GitHub Actions updates.
- Full audit findings must be reviewed before each website release and whenever
  Renovate proposes a Docusaurus or webpack-family update.
- Remove this exception when upstream Docusaurus resolves the remaining
  transitive build-tool advisories without a downgrade.
