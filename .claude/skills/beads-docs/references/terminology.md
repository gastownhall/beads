# Renaming a concept across the docs

When the project's word for something changes, the rule is: **rename the
concept in prose; preserve every literal that reflects the actual program.**
The code and its output are the source of truth — if the docs rename a string
the binary still prints, the docs now lie.

## The discipline

1. **Survey first.** Count occurrences in `docs/`, `engdocs/`, `README.md`,
   `AGENTS.md`, `AGENT_INSTRUCTIONS.md`, and `*.go`. Read enough to tell
   *prose* (the concept) from *literals*.
2. **Rename prose only**, with judgment per occurrence — fix articles and
   grammar so it reads naturally.
3. **Preserve literals:**
   - Real program output inside a code fence. Verify against `cmd/bd/` and
     `internal/` source; if the binary prints it, the doc must match.
   - Command and subcommand names (`bd mol`, `bd dep`), flags, JSON field
     names, config keys (`.beads/config.yaml`), label names (a proto's
     `template` label), issue types, file paths, and any backticked
     identifier.
   - A *different* thing with the same word (a "task" issue type vs a task in
     prose; Dolt's own "branch" vs a git branch).
   - **Generated files** (`docs/cli-reference/*`, `docs/CLI_REFERENCE.md`,
     the CLI pages array in `docs/docs.json`, `website/docs/cli-reference/`):
     never hand-edit. If the term must change there, change the Cobra strings
     in `cmd/bd/*.go` and run `./scripts/generate-cli-docs.sh`.
4. **Don't touch the code or `engdocs/` implementation names** as part of a
   docs rename unless explicitly asked. Renaming the binary's output strings
   is a separate Go PR (with regenerated CLI docs), worth flagging as a
   follow-up so output and docs align.
5. **Watch for collateral words.** A word-boundary match for `gate` must not
   touch "delegate" or "aggregate"; a `mol` rename must not mangle
   "molecule".
6. **Verify after:** every remaining occurrence in `docs/` (outside generated
   files) should be a justified literal — audit them explicitly, classify
   each as "literal (correct)" or "missed prose (fix)".

## Settled literal notes (as of the Mintlify port)

- `mol` — command literal only (`bd mol pour`, `bd ready --mol`); the concept
  in prose is always **molecule**.
- `template` — the label literal protos carry; the concept is **proto**.
- `issue` vs `bead` — both are sanctioned; CLI output and flags say issue, so
  prose mirroring the CLI keeps issue. Don't "fix" one into the other
  mechanically.
- `.beads/issues.jsonl` — always described as a passive export. Never call it
  the database, the sync protocol, or a backup.
- Paths `bd` prints (`docs/RECOVERY.md`, `docs/SETUP.md`, …) — never
  recreate pointer stubs at old paths; moved pages are covered by the
  `redirects` array in `docs/docs.json` (decision 6 of
  `engdocs/decisions/2026-07-10-mintlify-docs-overhaul.md`). If bd prints an
  old path, fix the Go source so it prints the new one and regenerate.
