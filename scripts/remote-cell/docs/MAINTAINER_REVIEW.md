# Maintainer review guide (experimental remote-cell)

This package is offered for **comment, modification, and direction**—not as a finished product merge.

## What this PR is

| Is | Is not |
| --- | --- |
| Isolated operator surface under `scripts/remote-cell/` | A change to default `bd` behavior |
| Docs + scripts to run a near-data cell locally | Full gateway productization on `main` |
| A concrete place to debate topology & ops | Multi-node HA or OIDC |
| Proof hooks: `make all` / `make prove` | A claim that every environment is supported without `GATEWAY_SRC` |

## How to try (minimum)

```bash
# Needs: Go, dolt on PATH, and GATEWAY_SRC = a beads tree that contains
# scripts/bench-remote-server/gateway (experimental lab tree; not yet on main).
cd scripts/remote-cell
export GATEWAY_SRC=/path/to/experimental-beads-with-gateway
make all    # build + Dolt + init + smoke + claim-race prove + health
```

If `GATEWAY_SRC` is missing, **that is expected on stock main today**—call that out in review rather than treating it as a packaging footgun alone.

## Decisions we need from maintainers

Please answer (even briefly) so follow-up work has a finish line:

1. **Direction:** Is near-data (gateway co-located with Dolt; thin HTTP clients) something Beads wants to support experimentally?
2. **Landing bar:** (A) docs/scripts only, (B) merge experimental package + CI later, (C) only with gateway + multi-user model on main?
3. **Multi-agent v0:** Is **one gateway process per bearer token** acceptable for experimental, or must multi-token single process land first?
4. **Ownership:** Cell admin (ops) vs repo user—does the admin/user split match project charter?

## Suggested improvement vectors (welcome)

- Rename paths/commands to match project conventions  
- Replace `GATEWAY_SRC` with an in-tree experimental build tag / module  
- GH Actions job wrapping `ci/run.sh`  
- Multi-token gateway process  
- Relax lab fences (`beads_perf_lab_*` DB prefix, `[beads-perf-lab]` titles, fixed runtime roots)  
- Wire read-only checks into `bd doctor` behind a flag  
- Thin client surface (`bd --remote` or config)  

## What we will *not* expand without your signal

- Multi-node HA / cluster failover as a requirement  
- OIDC as a requirement for private cells  
- EngOS policy or org IAM in beads core schema  

## Long-term plan (if direction = yes)

| Phase | Deliverable | Merge-ish bar |
| --- | --- | --- |
| **0 — this PR** | Operator package + docs + local prove | Draft / experimental feedback |
| **1** | Gateway (+ schema-matched init) buildable from this repo on current main | CI smoke optional |
| **2** | `ci/run.sh` in Actions; pinned experimental job | Non-blocking CI green |
| **3** | Multi-token single process; de-lab names/paths | Usable for internal teams |
| **4** | Optional `bd` remote mode + doctor checks | Documented product path |

## Normal stopping points (see also PR body)

- **Stop A — Architecture conviction:** evidence + docs only; no code merge.  
- **Stop B — Experimental tooling merged:** this package (or successor) in-tree; gateway may still be experimental.  
- **Stop C — Product remote path:** team cell without lab hacks; CI on main.  

Please say which stop you want so work doesn’t expand forever.
