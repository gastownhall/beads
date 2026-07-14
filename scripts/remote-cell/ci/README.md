# CI for remote-cell

```bash
GATEWAY_SRC=/path/to/beads-with-gateway ./scripts/remote-cell/ci/run.sh
```

Requires: Go, `dolt`, `bd`, `python3`.  
Does not modify base Beads CI until maintainers add a workflow that calls this script on an experimental job.
