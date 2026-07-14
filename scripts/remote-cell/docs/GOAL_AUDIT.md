# SuperCharge /ult audit — remote-cell & near-data program

**Date:** 2026-07-14  
**Commit:** `78fd126c7` on `experimental/remote-cell`  
**Live verification:** `SMOKE_PASS` + `PROVE_PASS` + `health ok=11` (post-commit)

---

## Approach Decision

- Treat original user goals as a multi-thread program (architecture proof, ease-of-setup, isolation, HA/OIDC stance).
- Score **done / not done / next** against verifiable artifacts only.
- Separate **package-local proofs** (`make all`) from **external lab proofs** (GCP capacity/load).
- Stop at decisions that need human authority (PR to upstream, de-lab productization).

---

## Generated Prompt (audit contract)

```text
You are auditing the Beads near-data remote-cell program.

Original goals (reconstructed from user asks):
1. Prove near-data gateway > remote multi-hop Dolt for team multi-writer use.
2. Fix only sense-making breakage; skip multi-node HA if daemon restart suffices; explain OIDC as optional.
3. Make setup simple: hide complexity; isolated from base bd until approved.
4. Clarify admin vs user; install/setup/verify; path to maintainer-convincing PR.
5. Fix all audit issues (names, gaps, leaks, manual setup) until green with docs/tests/proofs.

Audit dimensions:
- goals done/not/next
- capabilities
- tests & proofs
- code quality & organization
- names & commands
- docs, setup, verification

Rules:
- No invented proofs; cite paths/commands/results.
- Flag residual lab constraints honestly.
- End with decisions needed vs auto-done next steps.

Output: structured scorecard + ranked next actions.
```

---

## Execution Output

### 1. Original goals → status

| # | Goal | Status | Evidence |
| --- | --- | --- | --- |
| G1 | Near-data architecture better than remote Dolt SQL | **Done** (lab) | Evidence tree: load, edge delay, multi-repo |
| G2 | Multi-repo multi-person correctness | **Done** (lab) | `TEAM_MULTI_REPO_REPORT.md` |
| G3 | Capacity curve + more CPU | **Done** (lab) | `CAPACITY_E2S8_REPORT.md` PASS_TO_16 |
| G4 | HA = daemon enough; OIDC optional | **Done** (docs) | `WHY_MISSING…`, `EXPERIMENTAL.md` |
| G5 | Isolated packaging; no base bd taint | **Done** | Only `scripts/remote-cell/`; commit `78fd126c7` |
| G6 | Simple made easy operator UX | **Mostly done** | `make all` one-shot; residual GATEWAY_SRC |
| G7 | Admin vs user roles | **Done** | ADMIN.md / USER.md / invite-join |
| G8 | Install, setup, verify | **Done** (local) | `make all` / `ci/run.sh` |
| G9 | Maintainer-convincing PR with CI | **Not done** | No PR; CI is local script only; gateway not in main |
| G10 | Fix names/gaps/leaks until green | **Done** for package | Live SMOKE/PROVE/health PASS |

### 2. Capabilities scorecard

| Capability | In package | Works | Notes |
| --- | --- | --- | --- |
| Start Dolt (host) | yes | yes | `make start-dolt` |
| Init schema + gateway | yes | yes | `bd-cell` + `cell-provision` + `bd-gateway` |
| Smoke create/idempotent | yes | yes | Requires `[beads-perf-lab]` titles |
| Claim race 2 agents | yes | yes | `make prove` |
| Token isolation | yes | yes | foreign token → 401 |
| Invite / join | yes | yes | process-per-token lab model |
| Health checks | yes | yes | not `bd doctor` |
| Docker-first cell | no (demoted) | n/a | intentional |
| Multi-token one process | no | n/a | lab gateway limit |
| Thin client in main `bd` | no | n/a | experimental only |
| Upstream CI job | no | n/a | `ci/run.sh` only |

### 3. Tests & proofs

| Layer | Command / artifact | Result |
| --- | --- | --- |
| Local full path | `make all` / `ci/run.sh` | **PASS** (session + post-commit) |
| Smoke | `make smoke` | **PASS** |
| Prove | `make prove` | **PASS** (claim exclusive + 401) |
| Health | `make health` | **ok=11 fail=0** |
| Architecture load | external evidence | PASS (not re-run this commit) |
| Capacity e2-s8 | external evidence | CAPACITY_RETEST_PASS_TO_16 |
| Unit tests for libexec | none | gap |
| GH Actions workflow | none | gap |

### 4. Code & organization

| Item | Assessment |
| --- | --- |
| Placement | `scripts/remote-cell/` — correct isolation |
| libexec split | clear: start/stop/init/smoke/prove/health/invite/join |
| http_client.py | good stdlib helper |
| Compat wrappers | bootstrap→init, doctor→health |
| GATEWAY_SRC dependency | **necessary residual** until gateway lands in-repo |
| Binaries gitignored | yes |
| data/ gitignored | yes |
| Dockerfile.gateway | optional; not on happy path |

### 5. Names & commands

| Prefer | Alias | Clarity |
| --- | --- | --- |
| `all` | `demo` | clear |
| `start-dolt` | `up` | clear |
| `init-cell` | `bootstrap` | clear |
| `health` | `doctor` | clear (docs say not bd doctor) |
| `prove` | — | clear |
| `bd-gateway` / `bd-cell` / `cell-provision` | gateway/lab-bootstrap aliases | good |

Residual odd names (lab, not packaging): `beads_perf_lab_*`, `[beads-perf-lab]`, `terminal-operations`.

### 6. Docs & setup

| Doc | Role | OK? |
| --- | --- | --- |
| README | one-command entry | yes |
| ADMIN / USER | role split | yes |
| EVIDENCE | local + external index | yes |
| EXPERIMENTAL | isolation + future doctor | yes |
| VM / systemd | optional deploy | placeholders OK |
| Setup steps | `make all` | yes if GATEWAY_SRC + dolt + go |

### 7. Verification of this commit

| Check | Result |
| --- | --- |
| Commit exists | `78fd126c7` |
| No binaries in commit | yes (29 files, scripts/docs only) |
| Post-commit smoke | PASS `rc-aa1` / prove PASS |
| Branch | `experimental/remote-cell` (not pushed) |

### 8. Bull / Bear / Decider

**Bull:** Operator surface is real, isolated, one-shot green, claim-race proven locally, architecture evidence exists externally. Ready for internal dogfood.

**Bear:** Not merge-ready to upstream main: gateway source external, lab fences remain, no GH Actions, no product multi-token gateway, no `bd --remote`.

**Decider:** Package goal **met**. Product/PR goal **not met**. Next human decision: push branch + open draft PR as experimental docs-only/operator package, vs wait until gateway source is rebased onto main.

**Confidence:** 82

### 9. What’s next (ranked)

| Pri | Action | Who | Blocks |
| --- | --- | --- | --- |
| 1 | **Decision:** push + draft PR vs keep local | human | visibility |
| 2 | Rebase/land gateway + DirectDolt onto current main under experimental tag | eng | true mainline build without GATEWAY_SRC |
| 3 | Add `.github/workflows/remote-cell.yml` calling `ci/run.sh` | eng | maintainer one-click |
| 4 | Multi-token single process (de-lab) | eng | ops scale |
| 5 | Drop lab fences (title/DB/runtime roots) | eng | real product titles |
| 6 | Optional `bd doctor --experimental-remote` | eng | after approval |
| 7 | Link evidence pack into PR body | eng | conviction |

### 10. Decisions needed (stop here)

1. **Push** `experimental/remote-cell` and open a **draft PR** now (operator package only), or wait for gateway-in-tree?  
2. Is **process-per-invite** acceptable for first external review, or must multi-token land first?  
3. Should evidence tree be **copied/submoduled** into the PR, or linked only?

---

## Why This Is Better

- Goals mapped to verifiable done/not/next, not narrative.  
- Live post-commit verification attached.  
- Explicit stop conditions for human decisions.

## Residual risks

- Session `GATEWAY_SRC=/tmp/beads-pr5-fix` is machine-local.  
- Host `BEADS_DOLT_PASSWORD` still a footgun if someone bypasses `init-cell`.  
- Lab title/prefix rules will surprise product users until de-labbed.
