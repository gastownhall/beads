# Experimental isolation

## On / off

| Surface | Default |
| --- | --- |
| Normal local `bd` | on (unchanged) |
| This package | off until you `cd scripts/remote-cell && make all` |
| `bd doctor` remote checks | **not wired** — use `make health` |

## Rules

1. Do not change `cmd/bd` defaults here.  
2. Gateway source remains under experimental `scripts/bench-remote-server/`.  
3. Secrets stay in `data/` (gitignored) or `~/.config/beads/`.  
4. Turn off: `make stop` or `make reset`.

## Future `bd doctor` (sketch only)

```text
bd doctor --experimental-remote   # read-only; never starts processes
```

Not implemented until maintainers approve.
