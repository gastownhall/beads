# Remote cell (experimental)

> **Status:** experimental · **Default:** off · **Touches base `bd`:** no

Near-data Beads: agents talk HTTP to a **gateway next to Dolt**.  
This package hides lab flags, secrets paths, and bootstrap order.

```text
  agents / CI  ──HTTP──►  gateway  ──loopback──►  Dolt
```

## One command

```bash
cd scripts/remote-cell
export GATEWAY_SRC=/path/to/tree-with-scripts/bench-remote-server/gateway
# auto-detects /tmp/beads-pr5-fix when present

make all
```

That runs: **build → start Dolt → init cell → smoke → prove → health**.

## Commands (clear names)

| Command | What it does |
| --- | --- |
| `make all` | Full local proof path |
| `make build` | Build `bd-gateway`, `cell-provision`, … into `./bin/` |
| `make start-dolt` | Host Dolt on `127.0.0.1:13360` only |
| `make init-cell` | Schema + SQL user + gateway `:7707` + admin invite |
| `make smoke` | healthz, ping, create, idempotent retry |
| `make prove` | smoke + claim race + token isolation |
| `make health` | Standalone checks (**not** `bd doctor`) |
| `make invite PERSON=alice` | Second agent (own gateway process; same DB) |
| `make join INVITE=…` | Install invite into `~/.config/beads/` |
| `make stop` / `make reset` | Stop / wipe `data/` |

Compat aliases: `up`→`start-dolt`, `bootstrap`→`init-cell`, `doctor`→`health`, `down`→`stop`.

## Requirements

| Tool | Why |
| --- | --- |
| `GATEWAY_SRC` tree | Lab gateway **and** matching `cmd/bd` source |
| `dolt` on PATH | Data plane (host; passwordless root on loopback) |
| Go | `make build` → `bd-gateway`, `bd-cell`, `cell-provision` |
| `python3` | Smoke / prove / health |

`make build` produces **`bd-cell`** from the same tree as the gateway so schema and domain code match. Do **not** rely on a random Homebrew `bd` for cell init (that caused post-create read failures).

**Docker is not required** (and is not the default).

## Admin vs user

| Role | Does |
| --- | --- |
| **Admin** | `make all` once; `make invite PERSON=…` per agent |
| **User** | Receive `*.env` invite → `make join` → export vars |

Users never install Dolt or the gateway.

## Secrets

- `data/` is gitignored (passwords, tokens, queues, logs).  
- Share only `data/invites/<person>.env` over a secure channel.  
- Repo may hold **non-secret** `.beads/remote.json` (URL + project id).

## Isolation

- No changes to default `bd` CLI behavior.  
- Gateway source stays lab-side until maintainers approve a merge.  
- See [docs/EXPERIMENTAL.md](docs/EXPERIMENTAL.md).

## Evidence & proofs

Local package proofs: `make smoke` + `make prove`.  
Architecture / load / capacity proofs: [docs/EVIDENCE.md](docs/EVIDENCE.md).

## Docs

- [docs/PROBLEM_AND_WHY.md](docs/PROBLEM_AND_WHY.md) — **problem, experiment, architecture, performance data**
- [docs/MAINTAINER_REVIEW.md](docs/MAINTAINER_REVIEW.md) — **start here for review / decisions**  
- [docs/ADMIN.md](docs/ADMIN.md) · [docs/USER.md](docs/USER.md)  
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) · [docs/VM.md](docs/VM.md)  
- [docs/EXPERIMENTAL.md](docs/EXPERIMENTAL.md) · [docs/EVIDENCE.md](docs/EVIDENCE.md)  
- [docs/GOAL_AUDIT.md](docs/GOAL_AUDIT.md) — goal scorecard
