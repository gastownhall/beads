# Coding standards

This is the contributor contract for tests, comments, Go, and common
footguns. Command matrices and PR-readiness gates stay in their canonical
guides. Do not copy those tables here.

| Need | Canonical doc |
|---|---|
| Test commands, seams, and PR-readiness | [engdocs/TESTING.md](engdocs/TESTING.md) |
| Lint gate and exclusions | [engdocs/LINTING.md](engdocs/LINTING.md) |
| Error-exit patterns | [engdocs/ERROR_HANDLING.md](engdocs/ERROR_HANDLING.md) |
| Product and storage boundaries | [engdocs/PROJECT_CHARTER.md](engdocs/PROJECT_CHARTER.md) |
| CLI visual design | [engdocs/UI_PHILOSOPHY.md](engdocs/UI_PHILOSOPHY.md) |
| PR sizing and layering | [CONTRIBUTING_PR_GUIDELINES.md](CONTRIBUTING_PR_GUIDELINES.md) |

Adapted from
[jonbaldie/gastown `CODING_STANDARDS.md`](https://github.com/jonbaldie/gastown/blob/main/CODING_STANDARDS.md)
for beads package boundaries, test gates, and domain terms.

## Tests

- Test at the lowest seam that can fail for the user-visible reason. Add a
  higher tier only when it covers a distinct risk that the lower tier cannot
  show: integration wiring, real persistence, a process boundary, or an
  external contract.
- When behaviour crosses CLI, process, filesystem, Git, or Dolt boundaries,
  exercise that real boundary. Do not treat a green unit suite as proof that
  the product works.
- Mock only third-party services or process boundaries that we cannot
  control. Do not mock packages or business rules that we own.
- For command and workflow changes, the default proof is to run the real
  command against a temporary workspace and assert exit status, output, and
  persisted state.
- Assert results produced by production code. Do not assert values assembled
  only by test helpers, copied production logic, or mocks configured by the
  same test.
- Isolate tests from the developer's machine. Use `t.TempDir()`, the
  isolated environment from `./scripts/test.sh`, and a repository-local
  `core.hooksPath`. Do not inherit global Git config, hooks, or a production
  beads database.
- Use `testutil.RequireDoltContainer`,
  `testutil.StartIsolatedDoltContainer`, or
  `testutil/integration.RequireDolt` when a test needs those resources.
  Self-contained tests that create their own temporary workspace do not need
  an integration guard. Ordinary tests must not require Docker or a
  standalone `dolt` CLI.
- Keep concurrency tests deterministic. Synchronize on observable state or
  explicit test seams instead of sleeps when possible, and run
  race-sensitive packages with `go test -race ./path/to/package`.
- Use [engdocs/TESTING.md](engdocs/TESTING.md) to choose the smallest useful
  command. `make test` is the repository-wide Go baseline after focused and
  affected-package tests are green. Markdown-only changes use the docs, link,
  and freshness checks. Do not run the full Go suite merely because a
  Markdown file changed.

## Comments and docs

- Prefer names and structure over comments. Put the why in the commit
  message and PR description when a comment would only narrate the code.
- When a comment is required, write it in ASD-STE100 Simplified Technical
  English: short direct sentences, one instruction per sentence, and
  consistent terms.
- Use comments only for invariants, boundary conditions, compatibility
  constraints, and non-obvious reasons. Do not write comments that repeat
  what the code already makes clear.
- Use established beads domain terms consistently: bead and issue name the
  same unit of work; ready work is what `bd ready` returns; formula, proto,
  molecule, and wisp are distinct workflow stages; gate, sync, federation,
  embedded mode, and server mode have fixed meanings. "task" is one issue
  type, never the generic unit. Do not invent synonyms for existing terms.
  See [docs/core-concepts/index.md](docs/core-concepts/index.md).
- Do not put brittle references in comments or docs, such as line numbers,
  temporary paths, current versions, or "as of today" claims, when those
  details can change.
- Update user-facing docs under `docs/` when commands, configuration,
  output, or operational behaviour changes. Update contributor docs under
  `engdocs/` when process or implementation contracts change. Generated CLI
  pages are edited at their Go source, not by hand.

## Common footguns

- Tautological tests that assert a mock was called exactly as the test
  configured it.
- Mocks of packages, modules, or services we own.
- Treating a green suite as proof that a real agent or workspace can
  complete the workflow.
- Encoding agent judgment in Go: behavioural heuristics and arbitrary
  decision thresholds belong in agents and formulas. Deterministic protocol
  rules and safety boundaries can remain in Go.
- Confusing ephemeral wisps with durable Dolt-backed issues, or assuming
  every command reads the same beads database.
- Swallowing process, Git, or persistence errors and then reporting success.
- Using sleeps to hide lifecycle races or relying on goroutine completion
  order for stable results.
- Narrating comments, stale README claims, and hard-coded implementation
  details.
- Evading complexity or quality gates with denser syntax, hidden branching,
  or indirection that does not reduce real complexity.
- Reaching across layers: `cmd/bd` must not do SQL-shaped work that belongs
  in `internal/storage` or `internal/storage/issueops`. New primitives land
  at the lowest layer first. See
  [CONTRIBUTING_PR_GUIDELINES.md](CONTRIBUTING_PR_GUIDELINES.md).
- Leaking storage-engine details into beads packages: no beads-side flocks,
  Dolt introspection, storage-specific retry loops, or `github.com/dolthub/`
  imports outside the storage boundary. See
  [engdocs/PROJECT_CHARTER.md](engdocs/PROJECT_CHARTER.md#storage-boundary).
- Using emoji-style icons in CLI output. Status uses `○ ◐ ● ✓ ❄`. Priority
  uses `P0`–`P4` labels with color, not status glyphs.

## Go

- Format with `gofmt`. Keep `make ci-pr-lint` clean. That wrapper is the
  required formatting and lint contract: `make fmt-check`, golangci-lint
  with `.golangci.yml` and the `gms_pure_go` build tag, and a Windows
  non-CGO cross-lint pass. Do not treat a raw `golangci-lint run` as the
  gate.
- Scope `messgo` and `mutago` to production Go in the diff against the
  merge base, not the whole tree and not `_test.go` files. A finding in
  code the change did not touch does not block the change.
- Those production changes should report no violations on the `messgo`
  rulesets `design`, `codesize`, and `unusedcode`.
- Those production changes should report a covered-MSI of 80% or above
  from `mutago`.
- Production builds need `CGO_ENABLED=1` and `-tags=gms_pure_go`. Use
  `make build`, `make test`, or `./scripts/test.sh`. A bare
  `go build ./cmd/bd` fails the ICU linker path. See
  [engdocs/ICU-POLICY.md](engdocs/ICU-POLICY.md).
- Validate trust boundaries manually as well as with linters. Check
  user-controlled paths, subprocess arguments, SQL identifiers, and
  external input before use. Linter exclusions are not evidence that an
  operation is safe.
- Keep the module path `github.com/jonbaldie/beads` and the Go version in
  `go.mod` honest. Do not use newer language features without deliberately
  updating the module version.
- Follow Zero Framework Cognition: Go code transports data, enforces
  deterministic protocols and safety boundaries, and performs deterministic
  operations. Agents and molecule formulas perform behavioural judgment and
  reasoning.
- Use existing package boundaries and production seams. Keep Cobra command
  wiring in `cmd/bd` thin. Put reusable domain behaviour in the relevant
  `internal` package. Expose new storage operations on the storage
  interface before the CLI calls them.
- Pass `context.Context` through blocking work. Give subprocesses and
  external operations explicit cancellation or timeouts.
- Wrap errors with operation and resource context. Preserve causes with
  `%w`. Do not convert failures into apparent success. Command handlers
  return a `HandleError*` value instead of calling `os.Exit`. See
  [engdocs/ERROR_HANDLING.md](engdocs/ERROR_HANDLING.md).
- Make concurrent output deterministic. Protect shared state, avoid
  goroutine leaks, and verify concurrency changes with the race detector.
- Preserve compatibility across documented workspace, configuration, and
  storage layouts unless a migration is part of the change.
- Run focused package tests during development. Run one final `make test`
  before merging Go behaviour changes. Run a named CI wrapper only when its
  risk or surface is affected. Markdown-only changes do not require Go
  builds or tests.
- For releases, keep the release tag and `cmd/bd/version.go` version equal.
  Use `./scripts/bump-version.sh` rather than editing version files by
  hand. See [RELEASING.md](RELEASING.md).
- Use a dedicated branch for every pull request, based on the intended
  upstream branch. Never open a pull request from a fork's `main` branch.
- CLI output uses small Unicode status symbols and semantic colors from
  `internal/ui/styles.go`. Do not add emoji blobs. See
  [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md#visual-design-system) and
  [engdocs/UI_PHILOSOPHY.md](engdocs/UI_PHILOSOPHY.md).
