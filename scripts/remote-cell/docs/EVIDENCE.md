# Evidence index

## Package-local (run anytime)

| Proof | Command | Pass means |
| --- | --- | --- |
| Install + lifecycle | `make all` | build, Dolt, gateway, smoke, prove, health |
| Smoke | `make smoke` | healthz, ping, create **succeeded**, idempotent same id |
| Prove | `make prove` | smoke + exclusive claim race (exactly 1 winner) + foreign token 401 |
| Health | `make health` | binaries (incl. `bd-cell`), ports, live `/healthz` |

**Verified 2026-07-14:** `make all` → `ALL PASS` (claim race winner admin; alice rejected; cross-token 401).

CI entrypoint: [../ci/run.sh](../ci/run.sh)

### Known lab constraints (not package bugs)

| Constraint | Why |
| --- | --- |
| Title prefix `[beads-perf-lab]` | Lab gateway synthetic-title fence |
| DB name `beads_perf_lab_*` | Lab gateway database prefix check |
| Runtime root fixed paths | Lab path allow-list |
| One process per invite token | Lab gateway single bearer per process |
| `GATEWAY_SRC` external tree | Gateway not merged into mainline yet |

## Architecture / scale (external evidence tree)

These were produced on the GCP lab and live outside this package:

| Report | Decision / takeaway |
| --- | --- |
| `…/cell-vertical-proof/ARCHITECTURE_LOAD_REPORT.md` | Single-project load PASS |
| `…/cell-vertical-proof/TEAM_MULTI_REPO_REPORT.md` | 10×4 multi-person PASS |
| `…/cell-vertical-proof/CAPACITY_E2S8_REPORT.md` | SLO ≤16 workers on e2-standard-8 |
| `…/cell-vertical-proof/USE_CASE_PROOF.md` | Use-case gate matrix |
| `…/cell-vertical-proof/WHY_MISSING_AND_WHAT_WE_SKIP.md` | HA-lite + OIDC skip |
| `…/cell-vertical-proof/EDGE_DELAY_PROOF.json` | Edge RTT vs bad multipath |

Default evidence root (this machine):

```text
/Users/medhat.galal/Desktop/AI_Repos/beads-fleet-scale-evidence-20260712/cell-vertical-proof/
```

Override: `REMOTE_CELL_EVIDENCE_ROOT=/path make health` (optional future check).

## Intentionally not re-proven here

- Multi-day soak  
- Real multi-region client  
- Multi-node HA / OIDC  
- Full CLI surface over gateway  

See WHY_MISSING for rationale.
