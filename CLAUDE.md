# Claude Code Entry Point for Beads

This file is intentionally short. Do not copy workflow, build, storage, or UI
rules here; those details drift quickly when repeated across agent entrypoints.

## Read First

- **Workflow and safety**: [AGENTS.md](AGENTS.md)
- **Detailed agent operations**: [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md)
- **Architecture orientation**: [docs/CLAUDE.md](docs/CLAUDE.md)
- **PR maintenance policy**: [PR_MAINTAINER_GUIDELINES.md](PR_MAINTAINER_GUIDELINES.md)

## Current Ground Rules

- Run `bd prime` before doing tracked work.
- Follow `go.mod` and [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for build
  and test commands; do not hard-code toolchain versions here.
- Beads uses Dolt as the issue database. Use `bd dolt push` / `bd dolt pull`
  for issue data sync; do not use export/import as a routine git workflow.
- The CLI Visual Design System lives in
  [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md#visual-design-system).
- If this file conflicts with a linked source, trust the linked source and fix
  this file by removing the duplicate.
