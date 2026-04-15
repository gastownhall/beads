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

`scripts/test-icu-path.sh` omits `gms_pure_go` as an explicit, opt-in local
developer tool for exercising the ICU code path in `go-mysql-server` on
demand. CI no longer does this: upstream confirmed
(dolthub/go-mysql-server#3506) that `-tags=gms_pure_go` is the sanctioned
escape hatch, so we test the configuration we ship.

The older name `scripts/test-cgo.sh` is retained only as a deprecated shim
that warns and forwards to `scripts/test-icu-path.sh`.

## Post-Build Verification

Release builds are verified to be ICU-free:

- **Linux**: `readelf -d` and `ldd` check for `libicu` (must not appear)
- **macOS**: `otool -L` check for `libicu` (must not appear)
- **Script**: `scripts/verify-cgo.sh` runs these checks as a goreleaser post-hook

If ICU linkage is detected, the release build fails.

## The Upstream Fork

`go.mod` has a `replace` directive pointing `go-mysql-server` to a fork
(`maphew/go-mysql-server`) that adds `!windows` to the CGO regex build
constraint. This ensures `go install` works on Windows without ICU headers.

Upstream PR: https://github.com/dolthub/go-mysql-server/pull/3504
Tracking issue: https://github.com/dolthub/go-mysql-server/issues/3506

Once the upstream PR merges, remove the `replace` directive from `go.mod`.

## Common Mistakes to Avoid

1. **Adding ICU flags to `.buildflags` or `Makefile`** -- these were removed
   in PR #3066. The `gms_pure_go` tag makes them unnecessary.

2. **Removing `gms_pure_go` from a build target** -- this re-introduces
   ICU linkage. The post-build checks will catch it, but don't do it.

3. **Installing `libicu-dev` in release or CI test workflows** -- only
   needed for local, on-demand developer testing via
   `scripts/test-icu-path.sh`.
   Neither release builds nor the CI test matrix link ICU; both must not
   depend on ICU being installed.

4. **Confusing CGO with ICU** -- CGO is required (for Dolt). ICU is not.
   They are independent. `CGO_ENABLED=1` does not imply ICU.

## Trade-offs

- Go's `regexp` uses RE2 syntax, which is slightly less MySQL-compatible
  than ICU regex (no backreferences, no lookahead/lookbehind)
- `bd` does not use SQL `REGEXP` functions, so this has zero practical impact
- If a future feature needs SQL `REGEXP`, revisit this policy then

## See Also

- [INSTALLING.md](INSTALLING.md) -- user-facing build dependency docs
- [DOLT-BACKEND.md](DOLT-BACKEND.md) -- embedded Dolt architecture
- [CONTRIBUTING.md](../CONTRIBUTING.md) -- contributor guidelines
