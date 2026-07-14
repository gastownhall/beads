# Experimental near-data cell — simple made easy.
# Does NOT modify default `bd`. See README.md.
#
# One shot:
#   make all
#
# Clear names (preferred)          Compat aliases
#   start-dolt                     up
#   init-cell                      bootstrap
#   health                         doctor
#   stop                           down

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
DATA := $(ROOT)/data
BIN  := $(ROOT)/bin

# Tree that contains scripts/bench-remote-server/gateway (lab gateway source).
# Override: make build GATEWAY_SRC=/path/to/tree
GATEWAY_SRC ?= $(shell \
  if [ -d "$(ROOT)/../bench-remote-server/gateway" ]; then echo "$(abspath $(ROOT)/../..)"; \
  elif [ -d "/tmp/beads-pr5-fix/scripts/bench-remote-server/gateway" ]; then echo "/tmp/beads-pr5-fix"; \
  else echo ""; fi)
# Back-compat env name
BEADS_SRC ?= $(GATEWAY_SRC)

export REMOTE_CELL_ROOT := $(ROOT)
export REMOTE_CELL_DATA := $(DATA)
export GATEWAY_SRC
export BEADS_SRC := $(GATEWAY_SRC)

.PHONY: help all demo build start-dolt init-cell health smoke prove invite join stop clean reset \
	up bootstrap doctor down logs ps check-src

help:
	@echo "Remote cell (experimental)"
	@echo ""
	@echo "  make all          Build + start Dolt + init cell + smoke + prove"
	@echo "  make build        Build gateway binaries (needs GATEWAY_SRC)"
	@echo "  make start-dolt   Start Dolt only on 127.0.0.1:13360"
	@echo "  make init-cell    Schema + credentials + start gateway"
	@echo "  make smoke        healthz + ping + create + idempotent retry"
	@echo "  make prove        smoke + claim-race + two-agent invite"
	@echo "  make health       Local health checks (not bd doctor)"
	@echo "  make invite PERSON=alice"
	@echo "  make join INVITE=data/invites/alice.env"
	@echo "  make stop         Stop gateway(s) + Dolt"
	@echo "  make reset        stop + wipe data/"
	@echo ""
	@echo "Compat: up=start-dolt  bootstrap=init-cell  doctor=health  down=stop"
	@echo "GATEWAY_SRC=$(GATEWAY_SRC)"

# --- preferred names ---

all demo: build start-dolt init-cell smoke prove health
	@echo ""
	@echo "ALL PASS — cell ready. Admin invite: $(DATA)/invites/admin.env"

check-src:
	@if [ -z "$(GATEWAY_SRC)" ] || [ ! -d "$(GATEWAY_SRC)/scripts/bench-remote-server/gateway" ]; then \
	  echo "error: set GATEWAY_SRC to a beads tree with scripts/bench-remote-server/gateway"; \
	  echo "  example: make build GATEWAY_SRC=/path/to/experimental-beads"; \
	  exit 1; \
	fi

build: check-src
	@mkdir -p "$(BIN)"
	@echo "Building from $(GATEWAY_SRC) ..."
	@cd "$(GATEWAY_SRC)" && CGO_ENABLED=0 go build -o "$(BIN)/bd-gateway" ./scripts/bench-remote-server/gateway
	@cd "$(GATEWAY_SRC)" && CGO_ENABLED=0 go build -o "$(BIN)/bd-gateway-client" ./scripts/bench-remote-server/gateway-client
	@cd "$(GATEWAY_SRC)" && CGO_ENABLED=0 go build -o "$(BIN)/cell-provision" ./scripts/bench-remote-server/lab-bootstrap
	@# Schema must match gateway domain code — use same-tree bd, not random host bd.
	@cd "$(GATEWAY_SRC)" && CGO_ENABLED=0 go build -o "$(BIN)/bd-cell" ./cmd/bd
	@# Stable names used by scripts (and short aliases)
	@cp -f "$(BIN)/bd-gateway" "$(BIN)/gateway"
	@cp -f "$(BIN)/bd-gateway-client" "$(BIN)/gateway-client"
	@cp -f "$(BIN)/cell-provision" "$(BIN)/lab-bootstrap"
	@chmod +x "$(BIN)"/*
	@echo "binaries → $(BIN) (bd-gateway, bd-cell, cell-provision, …)"

start-dolt:
	@mkdir -p "$(DATA)"/{dolt-host,secrets,queues,logs,invites,public}
	@$(ROOT)/libexec/start-dolt.sh

init-cell:
	@test -x "$(BIN)/gateway" || $(MAKE) build
	@$(ROOT)/libexec/init-cell.sh

smoke:
	@$(ROOT)/libexec/smoke.sh

prove:
	@$(ROOT)/libexec/prove.sh

health:
	@$(ROOT)/libexec/health.sh

invite:
	@test -n "$(PERSON)" || (echo "usage: make invite PERSON=alice"; exit 1)
	@$(ROOT)/libexec/invite.sh "$(PERSON)"

join:
	@test -n "$(INVITE)" || (echo "usage: make join INVITE=data/invites/alice.env"; exit 1)
	@$(ROOT)/libexec/join.sh "$(INVITE)"

stop:
	@$(ROOT)/libexec/stop-cell.sh

clean: stop
	@echo "stopped; data kept (make reset to wipe)"

reset: stop
	@rm -rf "$(DATA)"
	@mkdir -p "$(DATA)"
	@echo "wiped $(DATA)"

logs:
	@tail -n 80 -f "$(DATA)/logs/gateway.log" 2>/dev/null || tail -n 80 -f "$(DATA)/logs/dolt.log" 2>/dev/null || echo "no logs yet"

ps:
	@$(ROOT)/libexec/health.sh --brief 2>/dev/null || true
	@echo "--- pids ---"
	@ls -la "$(DATA)"/*.pid 2>/dev/null || echo "(none)"

# --- compat aliases (documented) ---
up: start-dolt
bootstrap: init-cell
doctor: health
down: stop
