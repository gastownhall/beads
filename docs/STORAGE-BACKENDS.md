# Storage Backends

Beads uses Dolt as its default storage backend, and Dolt remains the reference implementation. You can also point `bd` at Postgres, MySQL, or SQLite — every backend implements the full issue-tracking core, and the only thing you give up off Dolt is version control.

One seam, one shared core: the issue-tracking semantics are written once (in `issueops`) and shared by *every* backend — Dolt runs them natively, the SQL family runs them through one thin per-engine dialect. Each backend is a store adapter over that shared core. Version control lives only in the Dolt store; on the SQL backends it returns one clean, typed error.

```mermaid
flowchart TD
    CLI["bd CLI"] --> SEAM["Storage interface<br/>one seam — every backend satisfies all of it"]

    SEAM --> CORE["CORE — issueops<br/>CRUD · search · deps · ready/claim · ordering · id-mint<br/>written once, shared by EVERY backend"]
    SEAM --> CAPS["CAPABILITIES — version control<br/>history · branch · remotes · sync"]

    CORE --> DS["Dolt store<br/>runs the core natively"]
    CORE --> KIT["sqlkit.Store<br/>SQL-family adapter · Dialect / Readiness / Claim"]

    DS --> DOLT[("Dolt — embedded / server<br/>the conformance ORACLE")]
    KIT --> PGD["pgdialect"] --> PG[("Postgres")]
    KIT --> MYD["mysqldialect"] --> MY[("MySQL")]
    KIT --> LTD["sqlitedialect"] --> LT[("SQLite")]

    CAPS -->|"implemented in the Dolt store"| DS
    CAPS -->|"generated typed-unsupported shell"| SHELL["Postgres / MySQL / SQLite<br/>operation X not supported"]

    DOLT -. "conformance-tested: SQL family matches Dolt byte-for-byte" .-> KIT
```

<details>
<summary>Plain-text version of the diagram</summary>

```
                              bd CLI
                                |
                       Storage interface
                                |
      +-------------------------+--------------------------+
      |                                                    |
  CORE  (every backend)                        CAPABILITIES  (Dolt-only)
  issueops -- CRUD, search, deps,              version control:
  ready/claim, ordering, id-mint,              history, branch, remotes, sync
  cycle checks: written ONCE                     |
      |                                          +-> Dolt store: implemented
      +--------------------+                     +-> Postgres/MySQL/SQLite:
      |                    |                          generated typed-"unsupported"
  Dolt store         sqlkit.Store                     (operation "X" not supported)
  runs the core      SQL-family adapter
  natively           (Dialect/Readiness/Claim)
      |                    |
 [Dolt engine]      +------+------+---------+
 embedded/server    pgdialect mysqldialect sqlitedialect
 = conformance          |         |            |
    ORACLE          Postgres    MySQL       SQLite

  issueops is shared by EVERY backend (Dolt included); each backend is a store
  adapter over it. Dolt runs the core's SQL natively and adds version control;
  the SQL family runs it through one thin dialect per engine, and is
  conformance-tested to match the Dolt oracle byte-for-byte.
```

</details>

## Why Choose a Backend?

- **Dolt stays the default** — `bd init` with no flags behaves exactly as before, and Dolt is the only backend with history, branching, and sync
- **Postgres / MySQL** — put your issues in the database your team already runs, backs up, and monitors; many writers, one server
- **SQLite** — a single file inside `.beads/`, zero servers, zero credentials, pure-Go driver
- **Same `bd` everywhere** — create, list, ready, deps, claims, labels, comments, search all behave identically on every backend; conformance tests enforce it against the Dolt reference
- **Clean degradation** — Dolt-only commands don't half-work on SQL backends; they return one clear, typed error

## Pick Your Backend

| Backend | Best for | History | Server needed | Credentials |
|---|---|---|---|---|
| `dolt` (default) | Solo or team work where you want issue history, branching, `bd dolt push`/`pull` | Yes | No (embedded) / optional | Only in server mode |
| `sqlite` | Solo work, throwaway workspaces, CI sandboxes, air-gapped machines | No | No | None |
| `postgres` | Teams with an existing Postgres; many workspaces share one database via per-workspace schemas | No | Yes | Ladder (below) |
| `mysql` | Teams with an existing MySQL; each workspace gets its own database | No | Yes | Ladder (below) |

Rules of thumb:

- If you want `bd history`, `bd dolt push`, time-travel, or federation — use **Dolt**. It is the only backend that tracks history.
- If you want the smallest possible footprint and don't need history — use **SQLite**.
- If your organization already operates Postgres or MySQL and you want issues living next to everything else your DBAs manage — use **Postgres** or **MySQL**. Multi-writer concurrency comes from the database server itself.
- If in doubt, use the default. `bd init` without `--backend` is Dolt, unchanged.

## Getting Started

The backend is chosen once, at `bd init` time, and recorded in `.beads/metadata.json`. Every later `bd` command reads it from there — no per-command flags.

### Dolt (default)

```bash
bd init --prefix myproj
```

Nothing changes. See [DOLT.md](DOLT.md) for embedded vs. server mode.

### SQLite

```bash
bd init --backend=sqlite --prefix myproj

# Optional: choose the database file (relative to .beads/; default beads.db)
bd init --backend=sqlite --sqlite-path=issues.db --prefix myproj
```

That's it — no server, no credentials. Foreign keys and immediate-transaction locking are configured automatically.

### Postgres

Each Beads workspace lives in its own Postgres **schema**, so many workspaces can share one database.

```bash
bd init --backend=postgres \
  --pg-url='postgres://bd:onlyforinit@db.example.com:5432/beads?sslmode=require' \
  --pg-schema=myproj \
  --prefix myproj
```

- `--pg-url` — a standard `postgres://` URL. If omitted, `bd init` falls back to the `BEADS_POSTGRES_URL` environment variable.
- `--pg-schema` — the schema this workspace owns; created and pinned via `search_path`.

> **Note:** `bd init` connects with the URL exactly as given, so embed the password
> in `--pg-url` for init. It is stripped before anything is written to disk —
> `.beads/metadata.json` stores only the password-free DSN — and every later
> command resolves the password through the credential ladder below.

```bash
# For all subsequent commands, supply the password via any ladder rung, e.g.:
export BEADS_PG_PASSWORD_COMMAND='vault kv get -field=password secret/beads/pg'

bd create "first issue" -p 1
bd ready
```

### MySQL

Each Beads workspace gets its own MySQL **database**.

```bash
bd init --backend=mysql \
  --mysql-url='bd:onlyforinit@tcp(db.example.com:3306)/' \
  --mysql-database=myproj_beads \
  --prefix myproj
```

- `--mysql-url` — go-sql-driver DSN grammar: `user:password@tcp(host:port)/` (note the trailing slash, no scheme). Falls back to `BEADS_MYSQL_URL` if omitted.
- `--mysql-database` — the database this workspace owns.

Same rule as Postgres: embed the password in `--mysql-url` for init only; it is redacted before persisting, and later commands use the ladder:

```bash
export BEADS_MYSQL_PASSWORD_COMMAND='vault kv get -field=password secret/beads/mysql'
# or a static password:
export BEADS_MYSQL_PASSWORD=...
```

> **Note:** the MySQL DSN grammar cannot carry a password without a username.
> `bd` refuses such DSNs loudly rather than silently connecting passwordless.

## Credentials & Override Layers

SQLite needs no credentials. For Postgres and MySQL, the password is resolved fresh at open time by a **ladder** of sources, highest priority first. The first *configured* rung wins, and a configured rung that fails aborts the connection — it never silently falls through to a lower rung.

Precedence, per backend:

1. **Password embedded in the DSN** — a password inside `BEADS_POSTGRES_URL` / `BEADS_MYSQL_URL` wins outright; no ladder runs.
2. **Credential command** — `BEADS_PG_PASSWORD_COMMAND` / `BEADS_MYSQL_PASSWORD_COMMAND`
3. **Static env var** — `BEADS_PG_PASSWORD` / `BEADS_MYSQL_PASSWORD`
4. **Credentials file** — an entry matching the DSN's `host:port`
5. **Driver-native fallback** — Postgres only: `PGPASSWORD`, `~/.pgpass`, `PGPASSFILE` still work when nothing above is configured. MySQL has no driver-native fallback; with nothing configured it attempts an empty password.

> **Note the naming asymmetry:** the URL variables use full names
> (`BEADS_POSTGRES_URL`, `BEADS_MYSQL_URL`) while the password variables
> abbreviate Postgres (`BEADS_PG_PASSWORD`, `BEADS_PG_PASSWORD_COMMAND`).
> There is no `BEADS_PG_URL`.

### The credential command (rotating secrets)

This is the same idiom as kubectl's ExecCredential, the AWS CLI's `credential_process`, and git credential helpers: `bd` runs your command with `sh -c` (30-second timeout) and reads the secret from stdout. Output can be a bare token, a kubectl-style JSON envelope (`{"token": "...", "expirationTimestamp": "..."}`), or an OAuth-style one (`{"access_token": "...", "expires_in": 900}`). Results are cached in-process and refreshed shortly before expiry, so repeated opens don't re-spawn your helper.

```bash
# Vault
export BEADS_PG_PASSWORD_COMMAND="vault kv get -field=password secret/beads/pg"

# AWS RDS IAM auth (15-minute tokens; caching and expiry are handled for you)
export BEADS_PG_PASSWORD_COMMAND="aws rds generate-db-auth-token \
  --hostname db.example.com --port 5432 --username bd"
```

The command variables are read from the environment only — never from workspace metadata — because a persisted command would be arbitrary code executed on every `bd` invocation.

### The credentials file (static, per-host)

The lowest configured rung is an INI-style file keyed by `host:port`, at `~/.config/beads/credentials` (Linux/macOS) or `%APPDATA%\beads\credentials` (Windows); override the path with `BEADS_CREDENTIALS_FILE`.

```ini
# ~/.config/beads/credentials  (chmod 600)
[127.0.0.1:5432]
password=localDevPassword

[db.example.com:3306]
password=teamServerPassword
```

If the file is readable by group or others, `bd` prints an ssh-style warning. The file rung is deliberately forgiving: a missing file or unmatched section just means "not configured" and the ladder continues — fail-closed behavior is reserved for rungs you explicitly configured, like a command.

### Dolt server credentials

The Dolt backend in server mode has its own rungs: `BEADS_DOLT_PASSWORD`, then the credentials file, with `BEADS_DOLT_SERVER_USER` overriding the username. There is also `BEADS_DOLT_CREDENTIAL_COMMAND`, an *identity* credential: the minted token is presented as the connection username to an authenticating gateway server, which verifies it and routes to the database. This is for gateway deployments only — the direct Postgres/MySQL backends reject `BEADS_PG_CREDENTIAL_COMMAND` / `BEADS_MYSQL_CREDENTIAL_COMMAND` with an error telling you to use the `_PASSWORD_COMMAND` form instead.

### Secrets never reach disk

`bd init` redacts the password from your URL before writing `.beads/metadata.json`, using the database driver's own parser, and then re-parses to verify nothing survived — refusing to init rather than persisting a secret it can't prove is gone. Check for yourself:

```bash
cat .beads/metadata.json
# {"backend":"postgres","postgres_dsn":"postgres://bd@db.example.com:5432/beads?sslmode=require", ...}
```

## What You Keep and What You Give Up

Every backend implements the complete issue-tracking core — the same shared implementation, not a re-derivation per backend:

- Issue CRUD, bulk creates, close/reopen/delete
- Search, counts, filtering, sorting
- Dependencies, cycle detection, dependency trees
- Ready/blocked work queries, claims and leases
- Labels, comments, the full event/audit trail
- Config, metadata slots, statistics, transactions, streaming iterators

What is Dolt-only — by design, since these *are* version control:

- **History & time travel** — `bd history`, as-of queries, diffs
- **Branching & merging** — branch, checkout, merge, conflict resolution
- **Remotes & sync** — `bd dolt push` / `bd dolt pull`, fetch, sync
- **Federation** — peer management
- **Compaction** — snapshot-based memory compaction

On Postgres, MySQL, and SQLite these operations don't limp along or approximate — each returns one clean, typed error:

```
$ bd history myproj-9q9
Error: failed to get history: operation "History" not supported by the sqlite backend
```

`bd init` sets the expectation up front:

```
✓ bd initialized with the SQLite backend
  History: not tracked (SQLite backend has no version control; use Dolt for history)
```

A few practical consequences:

- Writes on SQL backends are durable the moment the command returns — each write commits its own SQL transaction. (`bd`'s internal commit hooks become harmless no-ops, not errors.)
- The Dolt-only maintenance tail (auto-commit, auto-export, auto-backup, auto-push) is skipped automatically on non-Dolt backends.
- "Sync" on Postgres/MySQL is your database server: everyone connects to the same database, so there is nothing to push or pull. Back up issues with your normal database tooling, or `bd export` for JSONL interoperability.

## How It Stays Correct

The SQL backends are held to the embedded-Dolt reference by a conformance harness, run locally and in CI (with real Postgres and MySQL servers) on every pull request:

- **Tier 1 (in-process):** the same behavior corpus the Dolt reference passes — CRUD, search, dependencies, ready/blocked, claims and leases, plus a ~94-case audit corpus of edge behaviors — runs against every backend. Two extra gates make capability gaps impossible to hide: a completeness test proves the set of unsupported operations exactly matches an audited allowlist, and a contract test calls every one of them to prove it returns the typed error rather than panicking or lying.
- **Tier 2 (end-to-end):** a real `bd` binary runs `bd init --backend=...` and a CLI scenario corpus in an isolated workspace per backend, and the normalized output must be byte-identical to the Dolt reference. Known-divergence entries are visible in the registry and can only shrink — currently there are none.

```bash
./scripts/conformance.sh
# Postgres/MySQL profiles self-skip unless you point them at a server:
BEADS_PG_TEST_URL="postgres://user:pass@127.0.0.1:5432/beads_test" \
BEADS_MYSQL_TEST_URL="user:pass@tcp(127.0.0.1:3306)/" \
./scripts/conformance.sh
```

Adding a new backend is one declarative profile entry in `test/conformance/profiles.go` plus a store-factory arm; both tiers pick it up automatically.

See [DOLT.md](DOLT.md) for the default backend, [CONFIG.md](CONFIG.md) for configuration, and [TROUBLESHOOTING.md](TROUBLESHOOTING.md) if a connection misbehaves.
