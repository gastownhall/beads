# Plan: Schema-Skew Guard Implementation

**Filed:** 2026-05-22  
**PM:** beads/pm  
**Root cause:** be-85qfg — stale bd binary accumulated ~25,000 errno-1105 errors over 16h  
**Architecture:** be-blpg3  
**Status:** In-flight

---

## Goal

Hard-fail at db-open time when the database schema is ahead of the binary's
embedded migrations, preventing cryptic SQL errors caused by stale binaries.

## Work Breakdown

### be-wwbsv — Implement schema-skew guard (P1, builder)

**What:** Core implementation of the schema-skew guard.

**Files:**
- `internal/storage/schema/schema.go` — SchemaSkewError type, IsSchemaSkewError helper, checkSchemaSkew (unexported), CheckForwardDrift (exported), call in MigrateUp
- `internal/storage/dolt/store.go` — CheckForwardDrift in ReadOnly branch of newServerMode
- `cmd/bd/main.go` — --ignore-schema-skew PersistentFlag, env-var set in PersistentPreRun, dedicated SchemaSkewError handler before FatalError

**Key UX decisions (from designer, be-wwbsv):**
- Dedicated `errors.As(err, &skewErr)` check before FatalError (avoids triple-prefix)
- SchemaSkewError.UserMessage() for human output, Error() for terse one-liner
- Singular/plural: "1 migration ahead" / "N migrations ahead"
- Warning line: `Warning: schema skew ignored — database (v{DBVersion}) is ahead of binary (v{BinaryVersion}); some queries may fail`
- JSON: extend buildJSONError with "schema_skew" subobject (current_version, required_version, delta)

**Acceptance criteria:**
- `bd list` / `bd ready` exits 1 with actionable SchemaSkewError when DB > binary
- BD_IGNORE_SCHEMA_SKEW=1 bypasses guard, prints Warning line to stderr
- --ignore-schema-skew flag sets env var in PersistentPreRun
- --json flag produces schema_skew subobject on stderr
- Fresh DB (currentVersion=0) and normal upgrade path unaffected
- bd migrate and bd init excluded from guard

### be-x0rl6 — Document schema-skew guard (P2, builder)

**What:** Documentation additions (no new files).

**Files:**
- `cmd/bd/main.go` — flag description for --ignore-schema-skew (exact copy in be-x0rl6 §2a)
- `README.md` — ### Schema Version Guard section after ### Backup & Migration (exact copy in be-x0rl6 §3b)
- `CHANGELOG.md` — entry under [Unreleased] → Added (exact copy in be-x0rl6 §4b)

**Acceptance criteria:**
- `bd --help` shows --ignore-schema-skew with exact description text
- README renders ### Schema Version Guard section with correct code block (using 4-backtick or ~~~ fence)
- CHANGELOG entry present under [Unreleased] → Added

### be-cdhvj — Tests: schema-skew guard (P1, validator)

**Blocked on:** be-wwbsv  
**What:** Unit, integration, and escape-hatch test coverage.

**Acceptance criteria:**
- Unit: checkSchemaSkew mock for version=0/equal/+1/+3, BD_IGNORE_SCHEMA_SKEW path
- Error copy exact-match assertions: Error(), UserMessage(), warning line, EscapeHint()
- JSON --json flag: schema_skew subobject present with correct field types
- Integration: dolt.New at N+1 returns *SchemaSkewError; + BD_IGNORE_SCHEMA_SKEW succeeds
- ReadOnly CheckForwardDrift: same integration scenarios
- bd init guard no-op (fresh DB)
- IsSchemaSkewError distinguishes skew from other errors

---

## Dependency Graph

```
be-wwbsv (impl) ──blocks──> be-cdhvj (tests)
be-x0rl6 (docs)  ── independent
```

Builder ships be-wwbsv and be-x0rl6 in one PR. Validator writes be-cdhvj after.

---

## Routing

| Bead | Agent | Label |
|------|-------|-------|
| be-wwbsv | beads/builder | ready-to-build |
| be-x0rl6 | beads/builder | ready-to-build |
| be-cdhvj | beads/validator | needs-tests |

---

## Guardrails (carry forward from be-blpg3)

- BD_IGNORE_SCHEMA_SKEW is the ONLY bypass — no ReadOnly=true bypass
- checkSchemaSkew returns nil on currentVersion=0 — do not break bd init
- CheckForwardDrift called only in ReadOnly branch (write path gets it via MigrateUp)
- Do NOT add guard to bd migrate command
- Error message in README must match SchemaSkewError.UserMessage() exactly — update both in same PR
