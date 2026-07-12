# Proxied UOW fresh-retry (fork correctness slice)

See operator PR https://github.com/medhatgalal/beads/pull/4 for full maintainer notes.

## Problem
`CommitWithRetries` re-ran only `DOLT_COMMIT` after MySQL 1213/1205, which cannot recover a lost Dolt transaction snapshot.

## Fix
`uow.RunWithFreshUOWRetries` replays the full high-level operation on a new UOW for proxied update/ready-claim/close/create/dep/reopen/delete.

## Install this fork binary (PATH shadow)

```bash
PREFIX="$HOME/.local/beads-fork"
git clone --depth 1 --branch AI/perf-remote-server-uow-fresh-retry-main \
  https://github.com/medhatgalal/beads.git /tmp/beads-uow-src
cd /tmp/beads-uow-src
CGO_ENABLED=0 GOBIN="$PREFIX/bin" go install ./cmd/bd
export PATH="$PREFIX/bin:$PATH"
bd version
```

Rollback: remove the prefix from PATH and reinstall stock `github.com/steveyegge/beads/cmd/bd@latest`.

Do not modify EngOS or home dotfiles for activation.
