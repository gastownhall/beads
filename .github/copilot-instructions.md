# GitHub Copilot instructions for beads

The canonical contributor instructions for this repository are in
[AGENTS.md](../AGENTS.md); area-specific rules are in the nested `AGENTS.md`
files listed in its routing table. Read those, not this file, for workflow,
build, test, and scope rules.

The rules most often missed in review:

- A PR must close a documented GitHub issue (filed before or alongside it).
- Build with `make install`, never `go build -o bd ./cmd/bd` or `go install ./cmd/bd`.
- `make ci-pr-lint` must pass with zero issues; choose tests with
  [engdocs/TESTING.md](../engdocs/TESTING.md).
- Never create test issues in a production database; use `t.TempDir()`.
- CLI output uses small Unicode status symbols, never emoji icons.
