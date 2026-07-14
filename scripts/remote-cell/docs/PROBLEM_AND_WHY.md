# Why remote-cell exists

## The problem

Beads is an issue tracker optimized for **agents** (create / update / claim / close, dependencies, exclusivity). Solo use is fine with **embedded Dolt** on the laptop.

Team and multi-agent use breaks when the database is **not** next to the worker:

1. **Chatty remote Dolt SQL over the WAN**  
   Each command becomes many round trips (connect, begin, queries, commit). At ~150 ms RTT, measured **command p95 lands in seconds** (e.g. ping p95 ~3.4 s for direct remote SQL in the lab) — past agent-friendly SLOs (ping p95 &lt; 1.5 s, mutations p95 &lt; 3 s).

2. **“Everyone embeds Dolt and syncs”**  
   Multi-writer conflict, claim races, and sync policy become a second product. That is not “Beads remote”; it is distributed-DB design.

3. **Scale without a clear cell model**  
   More agents and more repos without a co-located mutation path either melts latency or invents HA/OIDC before the topology is proven.

**Failure mode we are solving:** remote multi-agent correctness and latency without putting Dolt on the public internet or rewriting Beads into a new tracker.

## The experiment

Hypothesis:

> If **issue semantics run next to Dolt** (near-data gateway) and agents only send a **few HTTP requests**, then multi-agent workflows stay correct under concurrency and meet latency SLOs even when the **client** is ~150 ms away.

What we built in the lab (not all of it is in this PR):

| Piece | Role |
| --- | --- |
| **Dolt sql-server** | Source of truth (external process) |
| **Gateway** | Terminal ops HTTP API, auth, idempotency queue, UOW against **loopback** Dolt |
| **Thin client** | Agents/CI: URL + project id + bearer token |
| **Operator package (this PR)** | `scripts/remote-cell/` — hide bootstrap flags, smoke/prove, docs |

Negative control: delaying **gateway↔Dolt** (e.g. netem on loopback) destroys SLOs. Delaying only **client↔gateway** (edge) still passes — so co-location is the load-bearing design choice.

## Architecture

```text
┌─────────────────────┐         few HTTP RTTs          ┌──────────────────────────────────┐
│  Agent / CI / laptop│  ───────────────────────────►  │  Near-data cell (one host/VPC)    │
│  thin client        │   create / update / claim /    │                                  │
│  (no Dolt)          │   close + idempotency key      │  ┌────────────┐   loopback SQL   │
└─────────────────────┘                                │  │  gateway   │ ───────────────►│ Dolt │
                                                       │  └────────────┘                  │
                                                       │     bearer token, project id       │
                                                       │     durable local op queue         │
                                                       └──────────────────────────────────┘
```

**Not this (rejected as team default):**

```text
Agent ── many MySQL RTTs over WAN ──► remote Dolt sql-server
```

**Also not this (different product):**

```text
Agent embeds Dolt ── magically syncs ──► other agents' Dolt
```

### Roles

| Role | Runs | Holds |
| --- | --- | --- |
| Cell admin | Dolt + gateway | DB passwords, tokens, capacity |
| Repo user / agent | Thin client only | URL + project id + personal token |

HA for MVP: **systemd/compose restart** (data on disk + idempotent client retry). Multi-node HA and OIDC are optional later, not prerequisites for the architecture.

## Performance data (lab)

Lab host: GCP `beads-perf-lab-20260711`. Numbers are from retained evidence reports (not re-run inside this PR’s GitHub CI).

### A. Direct remote SQL vs near-data gateway (~150 ms RTT class)

| Path | Result (order of magnitude) |
| --- | --- |
| Direct remote Dolt SQL | Ping/list/ready/show **p95 multi-second** (e.g. ping p95 ~3.4 s) — **misses** agent SLOs |
| Near-data gateway | Point commands **~160–300 ms** class at simulated 150 ms RTT in early gateway matrices — **meets** original command SLOs |

Conclusion recorded in lab README: **`GATEWAY_ARCHITECTURE_REQUIRED`**.

### B. Concurrent load on co-located cell (single-project)

**Decision: `LOAD_ARCHITECTURE_PASS`**

| Metric | Value |
| --- | ---: |
| Total ops | **1504** |
| Failures | **0** |
| Success | **100%** |
| Concurrency | 4–16 workers |
| Edge RTT | 0 and **150 ms** (client-side only) |
| SLO | ping p95 &lt; 1.5 s; mutations p95 &lt; 3 s — **all scenarios PASS** |

Example: create burst 16 workers, 320 creates — p95 ~1.0 s, 100% success.

### C. Multi-repo × multi-person (10 DBs × 4 agents)

**Decision: `TEAM_MULTI_REPO_PASS` (correctness)**

| Check | Result |
| --- | --- |
| Lifecycle ops under load | 100% success in capacity runs |
| Exclusive claim races | **exactly one winner** (exclusive_rate 1.0) |
| Cross-repo isolation | foreign project claim **rejected** |
| Latency SLO | Holds at lower concurrency on small VMs; overload stays **correct** but slow |

### D. Capacity on e2-standard-8 (10×4 fleet)

**Decision: `CAPACITY_RETEST_PASS_TO_16`**

| Workers | Edge RTT | Fail | p95 ms | Latency SLO | Races / isolation |
| ---: | ---: | ---: | ---: | --- | --- |
| 8 | 0 | 0 | ~1048 | PASS | OK |
| 16 | 0 | 0 | ~1508 | PASS | OK |
| 40 | 0 | 0 | ~3514 | FAIL (latency only) | OK |
| 8–16 | 150 | 0 | ~1.0–1.5 s | PASS | OK |

Reading: **correctness holds through overload**; plan ~**16 concurrent workers** per e2-s8-class cell for latency SLOs; scale out with **more cells**, not multi-node HA of one Dolt first.

### E. Edge delay control

**Decision: `EDGE_DELAY_PROOF_PASS`**

- Delay only on **client↔gateway** TCP → still under SLO.  
- Delay that also hits **Dolt path** → SLO collapse (wrong model for “remote agents”).

### F. Local package prove (this PR’s scripts)

On a developer machine after `make all`:

- healthz + ping + create + **idempotent same id**  
- **two agents, exclusive claim (1 winner)**  
- foreign token → **401**

## Why this PR exists (narrowly)

The lab proved the **architecture**. This PR ships the **operator-facing packaging and docs** so maintainers can:

1. See the problem and data without archaeology.  
2. Run a **local** smoke/prove path (`scripts/remote-cell`).  
3. Comment on direction and landing bar **before** a large gateway-on-main merge.

It deliberately does **not** change default `bd`. Gateway source may still live in an experimental tree (`GATEWAY_SRC`) until a follow-up lands it on main.

## What we are not claiming

- Production multi-region HA  
- OIDC/SSO as required for private cells  
- That `make all` re-runs the full GCP capacity matrix in GitHub Actions  
- That process-per-token is the final multi-user design (lab limit; multi-token is a follow-up)
