# ICU Regex Policy

## The Rule

**`bd` never ships with an ICU runtime dependency. All release binaries use
Go's stdlib `regexp` via the `gms_pure_go` build tag.**

This is non-negotiable. Do not remove, conditionally skip, or override
`gms_pure_go` in any build target, release workflow, or install script.

## Background

ICU (International Components for Unicode) is a C library that provides
MySQL-compatible regex via `go-icu-regex`. It enters our dependency tree
through `go-mysql-server` (the embedded Dolt SQL engine). `bd` does not
use SQL `REGEXP` functions, so ICU provides zero functional value while
creating significant portability problems:

| Platform | Problem without `gms_pure_go` |
|----------|-------------------------------|
| Linux | Binaries dynamically link a specific `libicui18n.so.NN` version; crash on distros with a different ICU version |
| macOS | ICU is keg-only in Homebrew; `go install` fails without manual `CGO_CFLAGS`/`CGO_LDFLAGS` |
| Windows | ICU C headers (`unicode/uregex.h`) not available; `go install` and CGO builds fail |
| `go install` | Users cannot pass `-tags gms_pure_go` to `go install pkg@latest` |

## How It Works

```
go-mysql-server
  ├── (default)      → go-icu-regex → links libicu (BAD)
  └── gms_pure_go    → Go stdlib regexp (GOOD)
```

The `gms_pure_go` build tag tells `go-mysql-server` to use Go's `regexp`
package instead of `go-icu-regex`. This eliminates the ICU shared-library
dependency at the binary level.

**CGO stays enabled.** CGO is required for the embedded Dolt database
(file locking, SQL engine). CGO and ICU are independent concerns:

- `CGO_ENABLED=1` + `gms_pure_go` = Dolt works, no ICU (what we ship)
- `CGO_ENABLED=1` without `gms_pure_go` = Dolt works, ICU linked (test-only)
- `CGO_ENABLED=0` = no Dolt backend at all

## Where `gms_pure_go` Must Be Set

Every build path that produces a binary for users must include `-tags gms_pure_go`:

| Location | File |
|----------|------|
| Local builds | `Makefile` (`BUILD_TAGS := gms_pure_go`) |
| Release builds | `.goreleaser.yml` (all build targets) |
| Install script | `scripts/install.sh` |
| Windows installer | `install.ps1` |
| CI test matrix | `.github/workflows/ci.yml` (Linux, macOS, Windows) |
| macOS release | `.github/workflows/release.yml` |
| Migration tests | `.github/workflows/migration-test.yml` |
| Nightly tests | `.github/workflows/nightly.yml` |
| Cross-version smoke | `.github/workflows/cross-version-smoke.yml` |

## Where `gms_pure_go` Is Intentionally Omitted

`scripts/test-cgo.sh` omits `gms_pure_go` as a local developer tool for
exercising the ICU code path in `go-mysql-server` on demand. CI no longer
does this: upstream confirmed (dolthub/go-mysql-server#3506) that
`-tags=gms_pure_go` is the sanctioned escape hatch, so we test the
configuration we ship.

## Post-Build Verification

Release builds are verified to be ICU-free:

- **Linux**: `readelf -d` and `ldd` check for `libicu` (must not appear)
- **macOS**: `otool -L` check for `libicu` (must not appear)
- **Script**: `scripts/verify-cgo.sh` runs these checks as a goreleaser post-hook

If ICU linkage is detected, the release build fails.

## The Upstream Fork (historical)

Beads used to carry a `replace github.com/dolthub/go-mysql-server => github.com/maphew/go-mysql-server ...` directive in `go.mod`, added in PR #3112 to try to make `go install` work on Windows without ICU headers. It was removed in PR #3306 (see GH#3303) after empirical testing confirmed that **`replace` directives are not honored by `go install pkg@version`** — the mechanism never worked for its stated purpose, and having the directive actively broke `go install` on every platform with a confusing error.

Upstream PR (closed, declined): https://github.com/dolthub/go-mysql-server/pull/3504
Upstream issue (closed, declined): https://github.com/dolthub/go-mysql-server/issues/3506

The dolthub maintainers have made clear the upstream default will not flip: *"We want our software to work as intended with the default settings. If users want to circumvent certain features with build tags or other build-time or run-time configuration, that's fine. Changing the default is not aligned with what we are actually trying to do."*

### How `go install` is handled now

Two supported modes, documented in [INSTALLING.md](INSTALLING.md):

1. **`CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest`** produces a **server-mode-only** binary. Works on any Go-capable box with no C compiler. Users must run an external `dolt sql-server` and use `bd init --server`.

2. **`CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install ...`** produces an embedded-capable binary. Requires a C compiler but NOT libicu.

No fork, no replace directive, no upstream patch required. The tradeoff is that `go install` users who want embedded mode have to pass an explicit `GOFLAGS`; those who don't care can use the shorter nocgo form.

## Common Mistakes to Avoid

1. **Adding ICU flags to `.buildflags` or `Makefile`** -- these were removed
   in PR #3066. The `gms_pure_go` tag makes them unnecessary.

2. **Removing `gms_pure_go` from a build target** -- this re-introduces
   ICU linkage. The post-build checks will catch it, but don't do it.

3. **Installing `libicu-dev` in release or CI test workflows** -- only
   needed for local, on-demand developer testing via `scripts/test-cgo.sh`.
   Neither release builds nor the CI test matrix link ICU; both must not
   depend on ICU being installed.

4. **Confusing CGO with ICU** -- CGO is required for embedded Dolt mode
   (NBS chunk compression via `gozstd`). ICU is independent. `CGO_ENABLED=1`
   does not imply ICU linkage as long as `-tags gms_pure_go` is present.
   beads also supports `CGO_ENABLED=0` builds via nocgo stubs: the binary
   runs in server-mode only (no embedded Dolt backend), which is the
   blessed `go install` path for users without a C toolchain.

## Trade-offs

- Go's `regexp` uses RE2 syntax, which is slightly less MySQL-compatible
  than ICU regex (no backreferences, no lookahead/lookbehind)
- `bd` does not use SQL `REGEXP` functions, so this has zero practical impact
- If a future feature needs SQL `REGEXP`, revisit this policy then

## See Also

- [INSTALLING.md](INSTALLING.md) -- user-facing build dependency docs
- [DOLT-BACKEND.md](DOLT-BACKEND.md) -- embedded Dolt architecture
- [CONTRIBUTING.md](../CONTRIBUTING.md) -- contributor guidelines
