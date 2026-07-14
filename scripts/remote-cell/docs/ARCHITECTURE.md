# Architecture (one page)

**Problem:** Chatty remote Dolt SQL over the WAN is slow and fragile for multi-agent teams.

**Solution:** Gateway co-located with Dolt; thin HTTP clients everywhere else.

```text
 agents  --few HTTP RTTs-->  gateway  --loopback-->  Dolt
```

| Piece | Count |
| --- | --- |
| Dolt | 1 per cell |
| Gateway process | 1 per token (lab); ideal product: 1 per project |
| Clients | many |

Deconfliction: Beads claim + idempotency on the shared DB — not per-person databases.

Skip for MVP: multi-node HA, OIDC, public Dolt.
