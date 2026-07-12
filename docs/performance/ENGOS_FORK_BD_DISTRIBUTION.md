# Safe fork `bd` distribution for EngOS (no dotfiles)

## Goals
- Run a **private-fork** `bd` that includes UOW fresh-retry (PR #4) while EngOS remains unchanged.
- Stay able to **upgrade to stock** `gastownhall/beads` / module `github.com/steveyegge/beads` when fixes merge upstream.
- **Never** modify EngOS or user dotfiles from this program.

## Recommended model: PATH-shadow binary (not module replace)

EngOS and agents typically invoke `bd` from `$PATH`. Prefer installing a **versioned binary** under a private prefix, then prepend that prefix to PATH **only in the shells/sessions that need the fork**.

### Build / install (operator machine)

```bash
# Pin the fork branch or tag you validated (example: PR #4 head after CI green)
FORK_REF=AI/perf-remote-server-uow-fresh-retry-main
PREFIX="$HOME/.local/beads-fork"

git clone --depth 1 --branch "$FORK_REF" https://github.com/medhatgalal/beads.git /tmp/beads-fork-src
cd /tmp/beads-fork-src

# Prefer the same mode EngOS uses today. Server-mode-only example:
CGO_ENABLED=0 GOBIN="$PREFIX/bin" go install ./cmd/bd

# Or embedded-capable (needs CGO + gms_pure_go):
# CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go GOBIN="$PREFIX/bin" go install ./cmd/bd

"$PREFIX/bin/bd" version
# Record: git rev-parse HEAD, go version, sha256 of binary
shasum -a 256 "$PREFIX/bin/bd" | tee "$PREFIX/bd.sha256"
echo "$FORK_REF $(git rev-parse HEAD)" | tee "$PREFIX/SOURCE.txt"
```

### Activate without touching EngOS or tracked dotfiles

**Session-only (safest):**
```bash
export PATH="$HOME/.local/beads-fork/bin:$PATH"
hash -r
which bd   # must show ~/.local/beads-fork/bin/bd
bd version
```

**Optional per-project env file** (not EngOS, not home global):
- e.g. `direnv` / project `.envrc` that only exports `PATH=...` for that repo.
- Do **not** commit PATH overrides into EngOS.

### Deactivate / upgrade to stock

```bash
# Remove shadow
export PATH=$(echo "$PATH" | tr ':' '\n' | grep -v 'beads-fork' | paste -sd: -)
hash -r
# Stock install per upstream docs/INSTALLING.md:
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest
# or brew / release tarball
```

When upstream merges the UOW fix: delete `$HOME/.local/beads-fork`, reinstall stock `@latest` or pinned release tag.

## Models to avoid

| Model | Why avoid |
| --- | --- |
| `replace` in EngOS `go.mod` | Couples EngOS to fork; hard upgrades; charter/boundary noise |
| Rewriting `~/.*rc` from agents | Dotfile blast radius; forbidden by program |
| `GOPROXY` / module path hacks to impersonate steveyegge/beads | Breaks verification and future stock upgrades |
| Overwriting Homebrew cellar in place | Opaque upgrades; fights package managers |

## Compatibility notes

- Module path remains `github.com/steveyegge/beads` even when cloning `gastownhall`/`medhatgalal` remotes — use `go install ./cmd/bd` from a checkout, or `go install github.com/medhatgalal/beads/cmd/bd@branch` only if module path redirects allow it; **checkout + `./cmd/bd` is reliable**.
- Verify proxy/server mode: fork fixes target **proxied-server** paths; confirm EngOS uses the same mode (`bd` against Dolt server vs embedded).
- Do not point EngOS at production Dolt from lab binaries.

## Packaging checklist before EngOS trial

- [ ] PR #4 hosted **CI Gate / Required** green
- [ ] `bd version` + binary sha256 recorded
- [ ] Smoke: create/update/close/ready/claim against **synthetic** Dolt only
- [ ] Rollback path tested (PATH remove + stock reinstall)
- [ ] EngOS tree `git status` clean (no accidental edits)

## Relation to integrated cell (PR #5)

PR #5 is a **lab harness**, not a drop-in EngOS dependency. EngOS should keep calling `bd` CLI/API. Cell packages stay under `scripts/bench-remote-server/` until a separate product decision places a gateway outside core Beads (charter).
