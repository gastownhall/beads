# Dolt Server Hang Incident Report

**Date:** 2026-02-23
**Dolt Version:** 1.82.2 (now upgraded to 1.82.4)
**Platform:** macOS Darwin 25.3.0, arm64
**Reporter:** Steve Yegge (Gas Town / beads project)

## Summary

A shared Dolt SQL server (PID 13360, port 3307) became completely unresponsive
under concurrent load from ~20 AI coding agents. All queries timed out, causing a
cascade failure that bricked the entire multi-agent workspace. Required
force-killing the Dolt server and all ~15 stuck bd/gt processes to recover.

## Environment

Gas Town is a multi-agent workspace where ~20 Claude Code agents run
concurrently, each issuing `bd` (beads CLI) commands that connect to a shared
Dolt SQL server.

### Server Configuration (`config.yaml`)

```yaml
behavior:
  autocommit: false

listener:
  host: 127.0.0.1
  port: 3307
  # max_connections, back_log, max_connections_timeout_millis all at defaults
```

### Databases

The shared server hosts ~15 databases (beads, gastown, hq, wyvern, sky, plus
test databases from automated test runs).

### Client Connection Pattern (pre-fix)

Each `bd` command is a separate Go process using `go-sql-driver/mysql`. The
transaction pattern was:

```go
sqlTx, err := db.BeginTx(ctx, nil)
// ... INSERT/UPDATE operations ...
sqlTx.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", msg, author)
sqlTx.Commit()  // ← REDUNDANT: DOLT_COMMIT already ends the transaction
```

Per Tim Sehn's guidance (2026-02-22), `DOLT_COMMIT()` implicitly commits the SQL
transaction, making the explicit `tx.Commit()` redundant and adding "raciness."

### Client Pool Settings

```go
db.SetMaxOpenConns(10)
db.SetMaxIdleConns(5)
db.SetConnMaxLifetime(5 * time.Minute)
```

No query-level timeouts — root context has no deadline.

## Timeline

1. ~20 agents simultaneously issue `bd` commands (create, update, list, close)
2. Each command opens a connection to port 3307, does work, calls DOLT_COMMIT
3. Dolt server becomes completely unresponsive — all queries hang
4. ~15 bd/gt processes pile up waiting for responses
5. Manual intervention required: force-kill Dolt server (PID 13360) + all stuck processes
6. GT daemon auto-restarts fresh Dolt server → town recovers

## What We've Changed

1. **Removed redundant `tx.Commit()` after `DOLT_COMMIT`** — per Tim's guidance
2. **Upgraded Dolt** from 1.82.2 → 1.82.4
3. **Built a repro script** (`scripts/repro-dolt-hang/main.go`) — could not reproduce
   the hang with 50 concurrent workers doing 1000 ops against a single database

## Reproduction Attempts

The repro fires N goroutines each doing BEGIN → INSERT → DOLT_COMMIT in a loop
with a watchdog monitoring server responsiveness. Tested up to 50 workers / 1000
ops with both old (with tx.Commit) and new (without) patterns on Dolt 1.82.4:

```
[old] 1000/1000 success (100.0%), max latency 312ms, 0 unresponsive events
[new] 1000/1000 success (100.0%), max latency 321ms, 0 unresponsive events
```

The simple repro doesn't trigger the hang. Suspected additional factors in
production:
- Multiple databases (~15) on one server
- Idle-monitor process checking/restarting server concurrently
- Separate OS processes (not goroutines) — each with its own connection pool
- `autocommit: false` in server config
- The redundant `tx.Commit()` after `DOLT_COMMIT` adding raciness

## Questions for Dolt Team

1. **Is this the bug Tim mentioned fixing?** ("We fixed the bug you ran into" —
   email 2026-02-21). The 1.82.3 and 1.82.4 changelogs don't show a concurrency
   fix. Was the fix in an earlier release, or is the "fix" the guidance to drop
   the explicit `tx.Commit()` after `DOLT_COMMIT`?

2. **Can the redundant `tx.Commit()` after `DOLT_COMMIT` cause a server hang?**
   Tim said it "adds raciness" — could that raciness escalate to full server
   unresponsiveness under high concurrent load?

3. **Should we configure `max_connections` / `back_log` / `max_connections_timeout_millis`
   explicitly?** Currently all at defaults. With 20 agents creating separate
   connection pools (MaxOpenConns=10 each), we could hit 200 connections.

4. **Multiple databases on one server** — is there any known issue with lock
   contention across databases on the same Dolt server? We have ~15 databases
   including leftover test databases.

## Run the Repro

```bash
cd beads/
go run ./scripts/repro-dolt-hang 50 20 both
```
