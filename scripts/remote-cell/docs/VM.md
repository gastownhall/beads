# Bare VM (optional)

Prefer laptop `make all` first. On a dedicated VM:

1. Install `dolt` and `bd` (and Go to build).  
2. `export GATEWAY_SRC=…` and `make build`.  
3. On Linux, `REMOTE_CELL_RUNTIME_ROOT=/var/lib/beads-perf-lab` (gateway lab constraint).  
4. `make start-dolt && make init-cell && make prove`.  
5. Optional: install `systemd/*.service` templates (edit project id / paths).  

Bind only to loopback or private interfaces.
