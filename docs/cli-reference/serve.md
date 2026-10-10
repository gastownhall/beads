---
title: "bd serve"
description: "Serve the beads HTTP API over loopback"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc serve`.

Serve the beads HTTP API — the same work surface the CLI answers, for
automation clients that would otherwise fork a bd subprocess per call.

The wire contract is described by an OpenAPI document (/v0); GET
/v0/beads/context reports which operations this build actually implements.

DEPLOYMENT

  Pass an explicit port. The default 127.0.0.1:0 takes an ephemeral one, which
  is right for ad-hoc and test use — where the bound address printed on stdout
  is read immediately — but carries no mutual exclusion: two serves against one
  workspace then run side by side on different ports with no way to enumerate
  them. On a fixed port the second one fails to bind, which is the intended
  behavior. Concurrent serves are data-safe either way; claims are arbitrated
  in the SQL server.

  Run it under a supervisor. bd shuts down gracefully on SIGHUP as well as
  SIGINT and SIGTERM, so closing the terminal of a foreground bd serve stops it.

PROBES

  GET /healthz is LIVENESS only: it answers from the process and never touches
  the database, so it stays green while the database is unreachable. For
  readiness use GET /v0/beads/ready?limit=1 — a real query, where 200 means
  ready and 503 means live but not ready.

AUTHENTICATION

  Optional, and off by default on loopback — where the trust model is the
  loopback boundary itself, the same one the database behind it already relies
  on. --auth-token-file turns it on: every operation except GET /healthz then
  requires an "Authorization: Bearer &lt;token&gt;" header, GET /v0/beads/context
  included, because it reports the repo root, beads directory and database name.

  The file holds ONE TOKEN PER LINE and every line is accepted. That is the
  rotation mechanism: write the new token alongside the old, roll the clients
  over, then delete the old line. The file is re-read while the server runs, so
  both the addition and the removal take effect within about a second and
  neither needs a restart. Write it atomically (a temp file plus rename; a
  Kubernetes secret mount already does this).

  There is deliberately no --auth-token flag. A credential passed as an
  argument is readable by every local user in the process listing.

  --allow-non-loopback REQUIRES a token file. Beyond loopback, reaching the
  address would otherwise be the whole authorization: any peer could read every
  issue and claim work as any actor. --insecure-no-auth is the explicit,
  auditable way to say you meant that anyway.

  The Host allowlist is what a service deployment usually trips over first. The
  DNS-rebinding check answers only to loopback spellings and the bind address,
  so a client dialing a service name gets 400 on every request; enumerate the
  names it dials with --allowed-host (repeatable). Matching is exact — no
  wildcards — and the startup log line prints the effective allowlist.

WHAT THIS DOES NOT DO

  No TLS. Even with a token, the credential and every issue body travel in
  plaintext, so a deployment beyond loopback has to supply confidentiality
  itself — a service mesh, or a network boundary you already trust.

  Hooks do not fire. A hook is a user-controlled subprocess per mutation: in a
  concurrent server that is an unbounded latency multiplier and an orphaned
  child at shutdown, and its working-directory-derived hook lookup is
  meaningless in a server process. A CLI claim runs on_update; an HTTP claim
  does not.

  The per-command auto-commit machinery does not run. Durability is per request:
  a successful claim commits inside its own transaction, exactly as a proxied
  CLI claim does today.

  An actor on an HTTP request is caller-asserted provenance for the audit trail,
  not authenticated identity — the same thing it has always been on the CLI,
  where any local process can pass any --actor.

  It does not run under --readonly, and refuses to start rather than binding.
  Every server it binds publishes the issue-claim operation, and the capability
  set it advertises is a property of the build rather than of the flags on the
  process that started it — so a read-only server would advertise a write it
  could never land.

DESTRUCTIVE OPERATIONS

  POST /v0/beads/issues:sweep deletes closed beads in bulk — the operation
  behind bd purge and bd prune — and nothing it deletes comes back. It shares
  the library surface those commands call, so it inherits their guards: pinned
  beads are never swept, and a durable sweep with neither a cutoff nor an id
  pattern is refused rather than clearing every closed bead in the workspace.
  Combined with the trust model above, that means anyone who can reach this
  address can erase closed work; bind it accordingly.

```
bd serve [flags]
```

**Flags:**

```
      --addr string                Address to bind as IP:PORT; the host must be a numeric IP literal, and port 0 takes an ephemeral port (default "127.0.0.1:0")
      --allow-non-loopback         Permit a bind beyond loopback. Requires --auth-token-file, since reaching the address would otherwise be the whole authorization
      --allowed-host stringArray   Additional Host header value to answer to, e.g. a service DNS name. Repeatable; matched exactly, with no wildcards
      --auth-token-file string     Require an Authorization: Bearer token from this file, one token per line, all accepted. Re-read while running, so rewriting it rotates tokens without a restart (env BEADS_SERVE_TOKEN_FILE)
      --insecure-no-auth           Serve a non-loopback bind with NO authentication. Every peer that can reach the address gets full read and claim access
```
