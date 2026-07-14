# Repo user / agent

You do **not** install Dolt or the gateway.

```bash
make join INVITE=/path/to/alice.env
set -a; source ~/.config/beads/remote-cell.env; set +a
```

| Variable | Meaning |
| --- | --- |
| `BEADS_GATEWAY_URL` | Your agent endpoint |
| `BEADS_PROJECT_ID` | Project UUID |
| `BEADS_TOKEN` | Personal bearer token |

Teammates share one project DB. **Claim** is exclusive: one winner; losers take other ready work. Retries use the same idempotency key.

Leave: delete `~/.config/beads/remote-cell.env` and stop exporting vars.
