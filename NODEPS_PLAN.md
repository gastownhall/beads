# Zero-Dependency Migration Plan for Beads

**Goal.** Reduce `go.mod` to a single `module` line + `go 1.25.x`. No `require` block, no `go.sum`. The `bd` binary should build from stdlib-only Go source. Tests should pass without spinning up containers or external servers.

**Scope.** This is the roadmap. Each phase below lands as an independent set of PRs on `claude/remove-external-dependencies-UEIXe`. The target is a CGO-free, stdlib-only build whose on-disk storage is JSONL files.

**Branch.** All work goes on `claude/remove-external-dependencies-UEIXe`.

---

## Table of Contents

1. [Dependency inventory and verdicts](#1-dependency-inventory-and-verdicts)
2. [Storage strategy: JSONL-backed store](#2-storage-strategy-jsonl-backed-store)
3. [CLI framework replacement](#3-cli-framework-replacement)
4. [Config layer replacement](#4-config-layer-replacement)
5. [UI / TUI / markdown replacement](#5-ui--tui--markdown-replacement)
6. [Time parsing replacement](#6-time-parsing-replacement)
7. [Telemetry removal](#7-telemetry-removal)
8. [AI / LLM features removal](#8-ai--llm-features-removal)
9. [Integration adapters](#9-integration-adapters)
10. [Tests and testing utilities](#10-tests-and-testing-utilities)
11. [Feature cut list](#11-feature-cut-list)
12. [Phased execution order](#12-phased-execution-order)
13. [Risk register](#13-risk-register)
14. [Appendix A: file-level impact estimate](#appendix-a-file-level-impact-estimate)

---

## 1. Dependency inventory and verdicts

### 1.1 Direct dependencies currently in `go.mod`

Every direct dep is classified: **REPLACE** (write a local equivalent), **DELETE** (remove the feature that uses it), **ALREADY GONE** (in `go.mod` but not actually imported — just prune it), or **STDLIB** (trivial swap).

| Dep | Usage | Verdict | Notes |
|---|---|---|---|
| `github.com/dolthub/driver` | embedded Dolt driver (`internal/storage/embeddeddolt/open.go:17`) | **DELETE** | Replaced by JSONL store. See §2. |
| `github.com/dolthub/dolt/go` | embedded Dolt engine (CGO) | **DELETE** | Same. |
| `github.com/dolthub/go-mysql-server` | SQL engine used by embedded Dolt | **DELETE** | Same. |
| `github.com/dolthub/vitess` | MySQL protocol parser | **DELETE** | Same. |
| `github.com/go-sql-driver/mysql` | server-mode Dolt connection | **DELETE** | Same. |
| `github.com/testcontainers/testcontainers-go` (+dolt module) | test-only Dolt container (`internal/testutil/{container_provider,testdoltserver}.go`) | **DELETE** | Tests that need this are dropped with the Dolt layer. |
| `github.com/spf13/cobra` | CLI framework, 142 files, 664 call sites | **REPLACE** | Minimal cobra-compatible shim. See §3. |
| `github.com/spf13/viper` | config loader, **3 files** (`internal/config/config.go`, `cmd/bd/config.go`, `cmd/bd/doctor/config_values.go`) | **REPLACE** | Small YAML layered loader. See §4. |
| `github.com/spf13/pflag` | transitive through cobra | **REPLACE** | Dies with the cobra shim. |
| `github.com/subosito/gotenv` | `.env` loader, **1 call site** (`cmd/bd/main.go:20`) | **REPLACE** | ~40 LOC `KEY=VALUE` parser. |
| `github.com/BurntSushi/toml` | formula/recipe parsing (`internal/formula/parser.go`, `internal/recipes/recipes.go`) | **REPLACE** or **DELETE** | Either write a subset TOML parser or switch formula files to JSON. See §5.4. |
| `gopkg.in/yaml.v3` | config files, doctor subcommands | **REPLACE** | Subset YAML parser (scalars, maps, lists, nested). See §4.3. |
| `charm.land/lipgloss/v2` | ANSI styling (`internal/ui/styles.go`) | **REPLACE** | ~150 LOC ANSI escape helper. See §5.1. |
| `charm.land/glamour/v2` | markdown → ANSI (`internal/ui/markdown.go`) | **DELETE**/downgrade | Render as plain text. See §5.2. |
| `charm.land/huh/v2` | interactive forms (`cmd/bd/create_form.go`) | **DELETE** | Replace with non-interactive path + plain `bufio.Scanner` prompts for `bd init` etc. See §5.3. |
| `github.com/yuin/goldmark` | markdown → HTML for ADO richtext | **DELETE** (with ADO) | See §9. |
| `github.com/microcosm-cc/bluemonday` | HTML sanitizer for ADO | **DELETE** (with ADO) | Same. |
| `github.com/JohannesKaufmann/html-to-markdown/v2` | HTML → markdown for ADO | **DELETE** (with ADO) | Same. |
| `github.com/anthropics/anthropic-sdk-go` | AI duplicates (`cmd/bd/find_duplicates.go`) + compaction (`internal/compact/haiku.go`) | **DELETE** | Remove AI features. The mechanical Jaccard duplicate detector already exists as the default. See §8. |
| `github.com/olebedev/when` | NLP time parser (`internal/timeparsing/parser.go`) | **REPLACE** | Hand-rolled matcher for a finite vocabulary. See §6. |
| `go.opentelemetry.io/otel/*` (9 packages) | telemetry | **DELETE** | Replace with a tiny no-op shim that satisfies the handful of call sites. See §7. |
| `github.com/cenkalti/backoff/v4` | Dolt connect retry + tracker client retry | **REPLACE** | ~30 LOC exponential backoff helper. Dolt usage dies anyway. |
| `github.com/google/uuid` | ID generation, widely used | **STDLIB** | ~20 LOC `crypto/rand` RFC 4122 v4 generator. |
| `golang.org/x/sync` | `errgroup`, `semaphore` | **REPLACE** | `errgroup` is ~60 LOC; hand-roll or inline `sync.WaitGroup`. |
| `golang.org/x/term` | terminal size detection (`internal/ui/markdown.go`) | **REPLACE** | `$COLUMNS` env var + `syscall.TIOCGWINSZ` ioctl on unix; Windows builds fall back to 80. |
| `golang.org/x/sys` | platform syscalls (signal, ioctl, flock) | **REPLACE** | Each usage either moves to `syscall` (already stdlib) or is inlined under `//go:build linux || darwin` guards. |
| `github.com/stretchr/testify` | **1 test file only** (`internal/storage/dolt/git_remote_test.go`) | **STDLIB** | Dies with the Dolt layer. |
| `rsc.io/script` | scripttest harness (`cmd/bd/scripttest_test.go`) | **DELETE** | Already build-tagged `//go:build scripttests`. Drop the file. |
| `github.com/google/go-github/v57` | in `go.mod` but **never imported** | **ALREADY GONE** | Just prune. |
| `github.com/denisbrodbeck/machineid` | in `go.mod` but **never imported** | **ALREADY GONE** | Just prune. |

### 1.2 Indirect dependencies (the big win)

~250 indirect deps in `go.mod` come from **Dolt + testcontainers + OpenTelemetry + Anthropic SDK**. Once those four direct deps are gone, the cascade prunes AWS SDK, Azure SDK, Google Cloud SDK, Docker client, Apache Arrow/Parquet/Thrift, grpc, envoy, spiffe, zap, and the entire charmbracelet/bubbletea stack. Expected final state: **zero `require` entries**.

### 1.3 Verdict summary

- **5 direct deps are already vestigial** (`go-github`, `machineid`) or trivially swapped (`testify`, `uuid`, `rsc.io/script`).
- **8 deps die by deleting optional features** (OpenTelemetry, Anthropic SDK, `rsc.io/script`, the three markdown/HTML libs, `glamour`, `huh`).
- **6 deps die with the Dolt replacement** (the five Dolt libs + testcontainers).
- **~7 deps require an actual local reimplementation** (cobra, viper, gotenv, toml, yaml, lipgloss, when, backoff — where "implementation" is 30–400 LOC each, except cobra which is ~500 LOC for the subset we use).
