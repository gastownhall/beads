# Cell admin

## Once

```bash
cd scripts/remote-cell
export GATEWAY_SRC=/path/to/experimental-beads   # if not auto-detected
make all
```

Artifacts:

- Gateway: `http://127.0.0.1:7707`
- Admin invite: `data/invites/admin.env`
- Public config: `data/public/remote.json` → copy to repo `.beads/remote.json` (no secrets)

## Per person

```bash
make invite PERSON=alice
# send data/invites/alice.env securely
```

Each person gets their own **gateway process** (lab binary: one bearer token per process) on the next port, same project database. Claims still deconflict in Dolt.

## Ops

| Need | Command |
| --- | --- |
| Health | `make health` |
| Logs | `make logs` |
| Stop | `make stop` |
| Wipe | `make reset` then `make all` |

## HA-lite

Restart Dolt + gateway (`make start-dolt && make init-cell`). systemd templates under `systemd/` are optional for VMs. Multi-node HA is not required for MVP.

## Security

- Loopback / private VPC only  
- Never commit `data/` or invite files  
- Rotate = new `make invite PERSON=…` and revoke old file  
