# cmd/bd — contributor notes

`cmd/bd` is the `bd` CLI: one Go package (about 340 non-test files) plus the
`doctor/`, `setup/`, `protocol/`, `help_supplements/`, and `winres/`
subpackages. Commands decide what to do; storage work belongs below them in
`internal/storage/issueops` (see the layering rules in
[CONTRIBUTING_PR_GUIDELINES.md](../../CONTRIBUTING_PR_GUIDELINES.md)).

## File families

| Pattern | Meaning |
|---|---|
| `<cmd>.go` | The Cobra command, its flags, and the mode-independent body. Registers itself with `rootCmd.AddCommand` in its own `init()` — `main.go` adds no commands. |
| `<cmd>_proxied_server.go` | The proxied-server route for the same command. A command that touches the store on a proxied-server workspace needs one. |
| `capability_registry.go` | One table for every command path: what the pre-provider gate does with it on a proxied-server workspace. |
| `<cmd>_embedded_test.go` | `cgo` tests against embedded Dolt. |
| `<cmd>_integration_test.go` | Integration tests, usually `cgo && unix`. |
| `<cmd>_proxied_test.go` | Proxied-server tests. |
| `<topic>_guard_test.go` | Policy and safety guards. |

Which tier a test belongs in, and which command runs it, is decided by
[engdocs/TESTING.md](../../engdocs/TESTING.md).

## Adding a command

1. Create `cmd/bd/<cmd>.go` with the Cobra command and register it in that
   file's `init()`.
2. Add its row to `capability_registry.go`.
   `TestProxyCapabilityRegistryCoversCommandTree` fails if a command path is
   missing, and `TestProxyCapabilityRegistryHasNoStaleRows` fails on rows for
   paths that no longer exist.
3. If the command uses the store on proxied-server workspaces, add
   `<cmd>_proxied_server.go`.
4. Support `--json` for agent use. The `--json` wire surface is pinned by the
   golden corpus in `protocol/` (`TestCorpusGolden`; regenerate with
   `make corpus-regen` and review the diff — see `protocol/CATALOG.md`).
5. Add tests in the right tier.
6. Regenerate the CLI reference with `scripts/generate-cli-docs.sh`;
   `scripts/check-cli-docs-drift.sh` fails a PR whose command-surface change
   leaves the generated docs stale.

Prefer a flag on an existing command over a new command, and route
recovery/repair operations to `bd doctor --fix`; see the CLI design principles
in [engdocs/UI_PHILOSOPHY.md](../../engdocs/UI_PHILOSOPHY.md).

## Boundaries enforced by lint

From `.golangci.yml` (read the rule comments there before changing scope):

- **`cmd-bd-role-constructors` (depguard, all of `cmd/bd/**`):** "a command
  reaches the reader role through store.IssueReader(), never through the
  constructor: the accessor is where each storage decorator adds its layer".
  The same applies to `store.Counter()`, `store.WorkspaceConfig()`,
  `store.VersionReconciler()`, `store.StatsReporter()`,
  `store.ReadyCounter()`, and `store.Querier()`.
- **`cmd-bd-domain-boundary` (depguard, `label.go` and `state.go`):** "bd label
  and bd state reach storage through an issueops role accessor, never through
  the domain use cases - see openIssueLifecycle in cmd/bd/label.go".
- **forbidigo `LabelUseCase$`:** "reach the label plane through
  issueops.Lifecycle (Patch.Labels) and issueops.Reader, never the domain use
  case".
- **forbidigo `types.IssueFilter` / `types.WorkFilter`:** "do not write a
  filter here: take one back from a builder in internal/workapi, or pass the
  whole request to issueops.Reader". `cmd/bd` denies these by default; the
  files allowed to build their own filters are listed, with reasons, in
  `.golangci.yml`.
- **forbidigo `fmt.Print*` / `os.Stdout` in list/graph code:** "route format
  output through the command writer; mark an intentional non-format path
  locally".

A deliberate exception is marked on its line with
`//nolint:forbidigo // <reason>`; the reason is expected (review checks it;
no linter enforces it).

## Guard tests

- `TestEveryStoreConstructionActivatesTheEventsJournal` — every store
  construction site activates the events journal; new non-bead-writing sites
  go in its exemption table with a reason.
- `TestProxyCapabilityRegistryCoversCommandTree` and the other
  `TestProxyCapabilityRegistry*` tests — the registry stays complete and
  consistent.
- `TestCobraParallelPolicyGuard` — a test that calls `t.Parallel()` must not
  call the Cobra methods in `cobraParallelUnsafeMethods`; drop `t.Parallel()`
  or serialize the call under `stdioMutex`.
- `TestCorpusGolden` (`protocol/`) — no unreviewed change to the `--json`
  wire shapes.

Fresh-workspace command fixtures call `isolateBeadsDirForTest(t)` before
setup or dispatch and must not use `t.Parallel()`.
