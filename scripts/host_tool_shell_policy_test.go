package scripts_test

// Policy: no new ungated bare `go`/`gofmt` exec in shell scripts under
// scripts/ or .github/scripts/ that a Bazel test actually runs. This is the
// shell-script half of host_tool_exec_policy_test.go's Go-exec policy (see
// that file's package comment for the full rationale shared by both: under
// `bazel test` PATH is `hermetic_bin:` plus the strict action env, and a host
// `go`/`gofmt` toolchain is not guaranteed to be on it the way the
// hermetic-first `dolt` is -- see f4-spec.md §9). "dolt" is out of scope here
// for the same reason it is out of scope there: it is the one tool that PATH
// ordering already makes safe.
//
// Scope: unlike the Go scanner (which can cheaply scan every *_test.go file
// in the tree), finding every shell script a Bazel test actually executes
// requires reading each candidate script's BUILD.bazel wiring (sh_test
// srcs/args/data, a go_test's data, or a Go test's exec.Command of a fixed
// script path) by hand; there is no existing Starlark-aware tool in this repo
// to do that automatically, and no mechanical backstop here either -- see
// TestShellPolicyScopeCoversBazelShellData's removal note further down for
// why a regex-based one was tried and dropped. In particular this scope
// cannot see the common run_check.sh-as-launcher pattern (most
// scripts/repochecks targets pass the real target script as an `arg`, not as
// `data`/`srcs`, so run_check.sh itself is the only mechanically discoverable
// entry for that whole family) -- those still need hand curation and
// citation below.
//
// shellPolicyScope is that set, as of this writing, with how each entry was
// confirmed to run under `bazel test`:
//
//   scripts/ci/fmt-check.sh          PROGRAM of //scripts/repochecks:fmt_test
//                                    (run_check.sh execs it); data of
//                                    //scripts/prlintmake:prlintmake_test
//   scripts/ci/gofmt-bin.sh          data of prlintmake_test, which execs it
//                                    directly (gofmtbin_test.go) and fmt-check.sh
//                                    execs it as its first line
//   scripts/ci/pr-lint.sh            data of //scripts/prlintmake:prlintmake_test
//                                    AND //scripts/repochecks (doc_freshness_test's
//                                    tracked-file tree); prlintmake_test.go execs it
//                                    directly with gofmt/go shims on PATH
//   scripts/check-build-tags.sh      PROGRAM of repochecks:build_tags_test
//   scripts/check-go-install-guidance.sh  PROGRAM of repochecks:go_install_guidance_test
//   scripts/check-winget-portable-alias.sh  PROGRAM of repochecks:winget_portable_alias_test
//   scripts/check-migration-hygiene.sh  PROGRAM of repochecks:migration_hygiene_test
//   scripts/check-doc-freshness.sh   PROGRAM of repochecks:doc_freshness_test
//   scripts/check-workapi-frontend-boundary.sh  PROGRAM of repochecks:workapi_frontend_boundary_test
//   scripts/check-testing-short.sh   exec.Command("bash", ...)'d directly by
//                                    testing_short_tree_test.go, in this package
//   scripts/check-versions.sh        exports_files'd to //scripts/check-versions,
//                                    exec'd directly by pre_push_hook_test.go
//                                    (its "exit-status contract")
//   scripts/repochecks/run_check.sh  srcs of every repochecks sh_test above
//   scripts/repochecks/types_gen_drift_test.sh  srcs of repochecks:types_gen_drift_test
//   scripts/migration-test/lib/binary.sh  data of //tests/migration's
//                                    historical_upgrade_tests and legacy_bridge_test
//   scripts/migration-test/historical-dolt-upgrade-test.sh  ditto
//   scripts/migration-test/legacy-bridge-test.sh  ditto
//   scripts/migrate-legacy-to-current.sh  ditto (//scripts:migration_harness)
//   scripts/upgrade-smoke-test.sh    data of every upgrade_smoke_<release>_test
//                                    sh_test (tests/upgrade_smoke/defs.bzl); IS
//                                    Bazel-executed, unlike what an earlier
//                                    revision of this comment claimed
//   scripts/test.sh                  reached via //:repo_other_files (the
//                                    scripts/** glob) as shell_scripts_test's
//                                    data; test_script_test.go execs it 6
//                                    times with a fake go/prebuilt-bd driver
//                                    first on PATH
//   scripts/conformance.sh           ditto; conformance_script_test.go execs
//                                    it 4 times with a fake go on PATH
//   scripts/release.sh               ditto; release_script_test.go execs it
//                                    with a fake bd first on PATH
//   .github/scripts/embedded-test-shard.sh         arg of cmd/bd's
//   .github/scripts/embedded-storage-test-shard.sh  bd_embedded_test/_part2_test,
//   .github/scripts/proxied-test-shard.sh           embeddeddolt_embedded_test,
//   .github/scripts/server-storage-test-shard.sh    bd_proxied_test and
//                                    dolt_server_full_test sh_test rules, run via
//                                    tools/bazel/go_test_manifest_shard.sh
//
// Deliberately NOT in scope: scripts/ci/lib/test-env.sh. Its own TEST_SRCDIR
// gate predates this policy and is good practice, but it is not itself wired
// as Bazel test data/srcs/args anywhere -- other scripts `source` it, and
// *their* wiring is what puts it in scope transitively through their own
// scan, not a standalone one for the library file.
//
// scripts/test.sh, scripts/conformance.sh and scripts/release.sh ARE in
// scope: each runs under `bazel test` through the `scripts/**` glob behind
// //:repo_other_files (shell_scripts_test's data), not through an explicit
// per-file label -- test_script_test.go execs test.sh 6 times (with a fake
// `go`/prebuilt-bd driver on PATH), conformance_script_test.go execs
// conformance.sh 4 times (fake `go` on PATH), and release_script_test.go
// execs release.sh with a fake `bd` on PATH. An earlier revision of this
// comment claimed test.sh was only linked-to by //test/docsync and never
// executed, and that conformance.sh had no BUILD.bazel reference at all and
// so was out of policy scope entirely; both claims were wrong (gastownhall/
// beads#7485 review: "test.sh, conformance.sh and release.sh do run under
// Bazel").
//
// Detection is line-based, not a real shell parser: a line is scanned unless
// it is a `#` comment, inside a `<<'EOF'`/`<<EOF`-style heredoc body, or the
// match falls inside a quoted string that is not itself a `$(...)` command
// substitution (quotedSpans/commandSubSpans) -- message/pattern text like
// `warn "...no gofmt on PATH either"` or `git grep -E 'go install ...'`,
// rather than a command -- the same kind of pragmatic, documented-false-negative tradeoff
// check-build-tags.sh's own header comment makes for its Go-source scan. A
// scanned line is flagged if it contains a bare `go` followed by a verb this
// policy recognizes (env, build, run, ...), or a bare `gofmt` invocation,
// UNLESS the exec is inside a branch that this policy's per-branch gate
// tracker proves runs only off-Bazel (see shellBazelSetVars and
// shellLineGating below -- the shell analogue of
// host_tool_exec_policy_test.go's condPolarity/gated), or the line is in
// shellExecAllowlist.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// shellPolicyScope is the set of scripts/- and .github/scripts/-rooted shell
// scripts confirmed (see the package comment) to run inside a `bazel test`
// sandbox.
var shellPolicyScope = []string{
	"scripts/ci/fmt-check.sh",
	"scripts/ci/gofmt-bin.sh",
	"scripts/ci/pr-lint.sh",
	"scripts/check-build-tags.sh",
	"scripts/check-go-install-guidance.sh",
	"scripts/check-winget-portable-alias.sh",
	"scripts/check-migration-hygiene.sh",
	"scripts/check-doc-freshness.sh",
	"scripts/check-workapi-frontend-boundary.sh",
	"scripts/check-testing-short.sh",
	"scripts/check-versions.sh",
	"scripts/repochecks/run_check.sh",
	"scripts/repochecks/types_gen_drift_test.sh",
	"scripts/migration-test/lib/binary.sh",
	"scripts/migration-test/historical-dolt-upgrade-test.sh",
	"scripts/migration-test/legacy-bridge-test.sh",
	"scripts/migrate-legacy-to-current.sh",
	"scripts/upgrade-smoke-test.sh",
	"scripts/test.sh",
	"scripts/conformance.sh",
	"scripts/release.sh",
	".github/scripts/embedded-test-shard.sh",
	".github/scripts/embedded-storage-test-shard.sh",
	".github/scripts/proxied-test-shard.sh",
	".github/scripts/server-storage-test-shard.sh",
}

// shellBazelSetVars are environment variables, other than TEST_SRCDIR, that a
// Bazel test target always sets to a valid value before a specific script
// runs -- so a shell `-n "${VAR:-}"`, `${VAR:?...}` or `-x "$VAR"` check on
// one of them is exactly as reliable an under-Bazel indicator as a
// TEST_SRCDIR check is, for the script(s) it is scoped to. Each is cited by
// the BUILD.bazel/.bzl wiring that proves it, the shell analogue of
// host_tool_exec_policy_test.go's prebuiltResultVars/isPrebuiltVarIdent.
var shellBazelSetVars = map[string]bool{
	// tests/migration/defs.bzl sets this from //tests/migration:bd_source_tag
	// for every historical_upgrade_tests target.
	"SOURCE_TAG_SQLITE_BIN": true,
	// tests/migration/defs.bzl sets this from //cmd/bd:bd_for_tests for
	// historical_upgrade_tests; tests/upgrade_smoke/defs.bzl sets it from the
	// same target for every upgrade_smoke_<release>_test. Both always set it.
	"CANDIDATE_BIN": true,
	// tools/bazel/go_test_manifest_shard.sh's `export "$bin_var=$PWD/$bin"`
	// always sets exactly one of these three to an absolute path to a
	// declared `data` test binary before exec'ing the matching shard script
	// (cmd/bd/BUILD.bazel's bd_embedded_test/_part2_test/bd_proxied_test,
	// internal/storage/embeddeddolt/BUILD.bazel's embedded storage sh_test,
	// internal/storage/dolt/BUILD.bazel's server storage sh_test).
	"BEADS_TEST_CMD_BINARY":           true,
	"BEADS_TEST_EMBEDDED_TEST_BINARY": true,
	"BEADS_TEST_SERVER_TEST_BINARY":   true,
}

// shellAllowlistEntry mirrors host_tool_exec_policy_test.go's
// hostToolAllowlistEntry: keyed by the enclosing function (empty for
// top-level script code, outside any `name() { ... }`/`name() ( ... )`) and
// an exact snippet of the flagged line, not a line number, so the entry
// tracks the code through refactors instead of silently going stale or
// silently covering the wrong line (review point 5).
type shellAllowlistEntry struct {
	fn      string
	snippet string
	why     string
}

// shellExecAllowlist is file (repo-relative) -> entries for bare `go`/`gofmt`
// execs that are safe despite findShellHostToolExecs flagging them and
// shellLineGating not proving it structurally -- in every case here the gate
// is a generic `command -v`/PATH-shim precondition a Go test arranges before
// exec'ing the script, not a textual TEST_SRCDIR/shellBazelSetVars check this
// scanner's line-based gate tracker can see.
var shellExecAllowlist = map[string][]shellAllowlistEntry{
	"scripts/ci/gofmt-bin.sh": {
		{
			fn:      "",
			snippet: `if [[ "$(go env GOVERSION 2>/dev/null)" == "go$pinned" ]]; then`,
			why: "only reached once `command -v go` (above) has already confirmed go is on " +
				"PATH; //scripts/prlintmake:prlintmake_test's gofmtbin_test.go exercises every " +
				"branch of this script with a real go on PATH",
		},
		{
			fn:      "",
			snippet: `    goroot="$(go env GOROOT 2>/dev/null || true)"`,
			why:     "same `command -v go` gate as the GOVERSION check above",
		},
		{
			fn:      "",
			snippet: `elif ! goroot="$(GOTOOLCHAIN="go$pinned" go env GOROOT 2>/dev/null)"; then`,
			why:     "same `command -v go` gate as the GOVERSION check above",
		},
	},
	"scripts/ci/pr-lint.sh": {
		{
			fn:      "",
			snippet: `go run -mod=readonly -tags=gms_pure_go ./scripts/pr-lint`,
			why: "scripts/prlintmake/prlintmake_test.go execs this script directly with a " +
				"shim directory (containing a fake `go`) placed first on PATH",
		},
	},
	"scripts/check-versions.sh": {
		{
			fn:      "",
			snippet: `if ! go build -tags=gms_pure_go -o "$checker" ./scripts/check-versions; then`,
			why: "scripts/check-versions/pre_push_hook_test.go execs this script directly " +
				"with fakeGoBuildDir(t) (a stand-in `go` that copies the Bazel-built " +
				"BEADS_TEST_CHECK_VERSIONS binary instead of compiling from source) first on PATH",
		},
	},
	"scripts/test.sh": {
		{
			fn:      "",
			snippet: `PREBUILT_BD_BIN="$PREBUILT_BD_DIR/bd$(go env GOEXE)"`,
			why: "test_script_test.go execs test.sh with a fake `go` (and a prebuilt-bd " +
				"driver copied into place by that fake go's `build` subcommand) first on PATH",
		},
		{
			fn:      "",
			snippet: `if go build -o "$PREBUILT_BD_BIN" "$REPO_ROOT/cmd/bd"; then`,
			why:     "same fake-go-first-on-PATH gate as the GOEXE line above",
		},
		{
			// fn is "" (top level), not "cleanup_shared_server": this line
			// is top-level script code, textually after that indented,
			// nested `cleanup_shared_server() { ... }` definition (around
			// test.sh:191-195) has already closed. An earlier revision of
			// findShellHostToolExecs's end-of-function detection recognized
			// only an UNINDENTED closing `}`/`)`, so it never saw
			// cleanup_shared_server's own indented closing brace and
			// attributed every later line in the file to that function
			// (review: "the scanner never closes an indented function
			// header... two allowlist entries are keyed to the wrong
			// function"). The scanner now matches a closing brace/paren at
			// the SAME indentation as the function's opening line, so this
			// line is correctly attributed to top level.
			fn:      "",
			snippet: `CMD=(go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -timeout "$TIMEOUT")`,
			why:     "same fake-go-first-on-PATH gate as the GOEXE line above",
		},
		{
			fn:      "", // see the fn note on the CMD= entry above
			snippet: `total=$(go tool cover -func="$COVERPROFILE" | awk '/^total:/ {print $NF}')`,
			why: "same fake-go-first-on-PATH gate; test_script_test.go's fake go's `tool` " +
				"subcommand (COVERAGE is never requested by the test, so this line only " +
				"executes under a real invocation, not under the policy's own harness, but " +
				"the fake go sits first on PATH for the whole script run regardless)",
		},
	},
	"scripts/conformance.sh": {
		{
			fn:      "",
			snippet: `BEADS_TEST_EMBEDDED_DOLT=1 CGO_ENABLED=1 go test -tags "$TAGS" -v \`,
			why:     "conformance_script_test.go execs conformance.sh with a fake `go` first on PATH",
		},
		{
			fn:      "",
			snippet: `CGO_ENABLED=1 go test -tags "$TAGS e2e" -timeout 10m ./test/conformance/`,
			why:     "same fake-go-first-on-PATH gate as Tier 1 above",
		},
		{
			fn:      "",
			snippet: `go test -tags "$TAGS" -timeout=40m ./internal/httpclient/`,
			why:     "same fake-go-first-on-PATH gate as Tier 1 above",
		},
	},
	"scripts/release.sh": {
		{
			fn:      "",
			snippet: `BD_CMD=(go run -tags gms_pure_go ./cmd/bd)`,
			why: "only reached when none of BD, a `bd` on PATH, or $REPO_ROOT/bd resolves the " +
				"repo formula; release_script_test.go's fixtures always place a fake `bd` that " +
				"does, on PATH, so this line is never reached by the test's own dataflow",
		},
	},
}

// shellHostToolVerbs are the `go` subcommands this policy treats as a real
// exec rather than incidental prose (e.g. "go.mod", "go$pinned"). "tool" is
// included for `go tool <name>` (review point 2).
var shellHostToolVerbs = regexp.MustCompile(`\bgo(?:\s+)(env|build|run|version|vet|mod|install|test|fmt|list|generate|get|work|doc|bug|tool)\b`)

// shellGofmtExec matches a bare `gofmt` invoked as its own shell word --
// bounded by whitespace/`;`/`|`/`&`/`(`/“ ` “/line-start/line-end on both
// sides -- so it does not false-positive on an identifier or filename that
// merely contains "gofmt" as a substring, such as "gofmt-bin.sh" or a
// variable named "$GOFMT_BIN" (review point 2's "misses gofmt" plus the
// matching false-positive risk a naive `\bgofmt\b` fix would introduce: that
// pattern alone DOES match inside "gofmt-bin.sh", since `-` is a non-word
// character and `\b` only requires a word/non-word transition).
var shellGofmtExec = regexp.MustCompile("(^|[\\s;|&(`])gofmt([\\s;|&)`]|$)")

// shellToolLookup matches a `command -v`/`which`/`type` lookup of go or
// gofmt, which is not itself an exec of the tool.
var shellToolLookup = regexp.MustCompile(`\b(command\s+-v|which|type)\s+(go|gofmt)\b`)

// shellCommandVGuard matches the presence-guard idiom `command -v gofmt
// >/dev/null 2>&1 && gofmt -l .` on a single line (review: "a same-line
// `command -v gofmt && gofmt` is skipped"), and ONLY that exact shape:
//
//   - `go` is deliberately not covered. A should-fix found that guarding
//     `go` this way is an evasion, not a safety idiom: `command -v go &&
//     go build` is reported safe here, but under an exit-127 host-tool
//     stub `command -v` finds the stub and the exec still runs, and on a
//     worker that genuinely lacks `go` the exec is silently skipped --
//     exactly the worker-dependent behavior this policy exists to ban
//     (review: "B is an evasion for go"; "the 'safe by construction'
//     premise is also false"). For `gofmt` specifically the guard stays
//     sound under this policy's own premise (package comment: a host
//     gofmt is not guaranteed on the hermetic-first `bazel test` PATH), so
//     `command -v gofmt && gofmt` really is safe by construction.
//   - The lookup's own clause must not be negated by a leading `!`, and
//     must not be separated from its `&&` -- or the `&&` from the exec it
//     guards -- by a `;`, `||`, or a bare `|` (review: "… && echo; go
//     build" and the rest of the `… && echo; go build` family must still
//     be flagged; the fix is to require the guarded exec to come directly
//     after the guard's own `&&`, with nothing intervening). The
//     `(^|[;&|])` lead-in only starts a match right after a clause
//     boundary, which also rules out a leading `!` on the lookup: `!
//     command -v gofmt` has no `;`/`&`/`|`/line-start immediately before
//     `command`, so it never matches.
//   - Both the lookup and the exec must name EXACTLY `gofmt`, not a
//     same-prefixed sibling like `gofmt-bin` or (for the `go` case this
//     guard no longer covers at all) `go-junit-report` (review: "require
//     exact tool-name match (not `go-*` prefixes)"). The boundary class
//     mirrors shellGofmtExec's own word-boundary set rather than `\b`,
//     since `\b` alone treats the `-` in `gofmt-bin` as a boundary too.
var shellCommandVGuard = regexp.MustCompile(
	"(^|[;&|])\\s*(?:command\\s+-v|which|type)\\s+gofmt([\\s;|&)`]|$)[^;|]*?&&\\s*(gofmt)([\\s;|&)`]|$)",
)

// commandVGuardedExecEnd returns the byte offset of the single gofmt exec
// guarded by a same-line `command -v gofmt ... && gofmt ...`-style idiom on
// line, and whether one was found. Only that EXACT occurrence -- the gofmt
// token immediately following the guard's own `&&` -- is guarded; any other
// gofmt on the line (before the guard, after a `;`/`||`/`|`, or anywhere
// else) is a separate, ungated exec and must still be reported. There is no
// `tool` parameter any more: the guard only ever recognizes gofmt (see
// shellCommandVGuard's comment for why `go` is excluded entirely).
func commandVGuardedExecEnd(line string) (int, bool) {
	m := shellCommandVGuard.FindStringSubmatchIndex(line)
	if m == nil || m[6] < 0 {
		return 0, false
	}
	return m[6], true
}

// insideAnyIntSpan reports whether pos lies in any of spans, each a
// [2]int-shaped []int as returned by regexp's FindAllStringIndex /
// FindAllStringSubmatchIndex.
func insideAnyIntSpan(spans [][]int, pos int) bool {
	for _, s := range spans {
		if pos >= s[0] && pos < s[1] {
			return true
		}
	}
	return false
}

// heredocStart matches a `<<` (optionally `<<-`) redirection naming its
// terminator, with or without quotes: `<<EOF`, `<<-EOF`, `<<'EOF'`,
// `<<"EOF"`. The delimiter must start with a letter or underscore, so this
// does not false-positive on an arithmetic left-shift (`$((1 << 2))`) or a
// here-string (`<<<`), neither of which names an identifier-shaped
// terminator starting a new line (review point 2's "stray `<<` ends the
// scan").
var heredocStart = regexp.MustCompile(`<<-?\s*(['"]?)([A-Za-z_]\w*)['"]?\s*$`)

type shellExecHit struct {
	line    int
	fn      string
	snippet string
	verb    string // "" for a gofmt hit
}

// quotedSpans returns the [start,end) byte ranges of line that are inside a
// single- or double-quoted string MINUS any `$(...)` command-substitution
// span nested inside one (command substitution executes regardless of the
// quoting around it, e.g. `warn "not found: $(go env GOROOT)"` really does
// exec `go env`). This is what tells message/pattern text ("git grep -E 'go
// install ...'", `warn "...no gofmt on PATH either"`) apart from a real exec:
// a scanned line's host-tool match is only ever text, not a command, when it
// falls inside a quoted span and outside every command-substitution span.
// Earlier revisions of this scanner instead keyed off the line's leading
// command being printf/echo/git-grep specifically, which both missed
// identically-shaped text arguments to this repo's own warn()/fallback()
// helpers (review point 2) and could not tell "foo && go build ..." (a real
// second command after a separator) from being swallowed by a same-line
// "contains echo" substring check.
func quotedSpans(line string) [][2]int {
	cmdSub := commandSubSpans(line)
	var spans [][2]int
	var quote byte
	start := -1
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote == 0 {
			if c == '\'' || c == '"' {
				quote = c
				start = i
			}
			continue
		}
		if c == '\\' && quote == '"' && i+1 < len(line) {
			i++
			continue
		}
		if c == quote {
			addNonSubSpans(&spans, start, i+1, cmdSub)
			quote = 0
		}
	}
	return spans
}

// commandSubSpans returns the [start,end) byte ranges of every `$(...)`
// command-substitution in line (simple, non-nested balanced-paren matching;
// this repo's shell scripts do not nest one command substitution directly
// inside another on the lines this policy scans).
func commandSubSpans(line string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(line)-1; i++ {
		if line[i] != '$' || line[i+1] != '(' {
			continue
		}
		depth := 1
		j := i + 2
		for ; j < len(line) && depth > 0; j++ {
			switch line[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		spans = append(spans, [2]int{i, j})
		i = j - 1
	}
	return spans
}

// addNonSubSpans appends the portion(s) of [start,end) that fall outside
// every span in cmdSub to spans, so a command substitution nested inside a
// quoted string is not itself treated as quoted text.
func addNonSubSpans(spans *[][2]int, start, end int, cmdSub [][2]int) {
	cur := start
	for _, s := range cmdSub {
		if s[1] <= cur || s[0] >= end {
			continue
		}
		if s[0] > cur {
			*spans = append(*spans, [2]int{cur, s[0]})
		}
		if s[1] > cur {
			cur = s[1]
		}
	}
	if cur < end {
		*spans = append(*spans, [2]int{cur, end})
	}
}

func insideSpan(spans [][2]int, pos int) bool {
	for _, s := range spans {
		if pos >= s[0] && pos < s[1] {
			return true
		}
	}
	return false
}

// shellDashC matches a `bash -c`/`sh -c`/`zsh -c` invocation up through the
// opening quote of its command-string argument.
var shellDashC = regexp.MustCompile(`\b(?:bash|sh|zsh)\b(?:\s+-[A-Za-z]+)*\s+-c\s+`)

// dashCCodeSpans returns the [start,end) span of each quoted command string
// immediately following a bash/sh/zsh -c on line. Unlike an ordinary quoted
// string, this one really executes -- `bash -c 'go build -o out ./cmd/bd'`
// really execs `go build` -- so quotedSpans' default treatment of quoted
// text as message/pattern prose, not a command, must not apply to it (review:
// "bash -c 'go build' regressed" -- an earlier revision of this scanner
// recognized this shape, lost when quotedSpans' quote-aware skip was added).
func dashCCodeSpans(line string) [][2]int {
	var spans [][2]int
	for _, loc := range shellDashC.FindAllStringIndex(line, -1) {
		i := loc[1]
		if i >= len(line) {
			continue
		}
		quote := line[i]
		if quote != '\'' && quote != '"' {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] != quote {
			j++
		}
		if j < len(line) {
			spans = append(spans, [2]int{i, j + 1})
		}
	}
	return spans
}

// quoted reports whether pos lies in a quoted span that is genuinely message
// text, not a bash/sh/zsh -c command string that happens to be quoted
// (dashCCodeSpans overrides quotedSpans for exactly that case).
func quoted(spans, codeSpans [][2]int, pos int) bool {
	return insideSpan(spans, pos) && !insideSpan(codeSpans, pos)
}

// findShellHostToolExecs scans src (one shell script's contents) for bare
// `go <verb>`/`gofmt` lines, skipping comments, heredoc bodies, `command -v`
// style lookups, and quoted message/pattern text. It reports every hit
// regardless of any gate or the allowlist -- callers apply those. Each hit
// also records its enclosing function name (empty at top level) and an exact
// snippet of the matched line, for shellExecAllowlist and shellLineGating.
func findShellHostToolExecs(src string) []shellExecHit {
	var hits []shellExecHit
	lines := strings.Split(src, "\n")
	scanner := bufio.NewScanner(strings.NewReader(src))
	lineno := 0
	inHeredoc := false
	heredocDelim := ""
	currentFn := ""
	currentFnIndent := ""
	for scanner.Scan() {
		lineno++
		line := scanner.Text()
		if inHeredoc {
			if strings.TrimSpace(line) == heredocDelim {
				inHeredoc = false
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if fn, ok := matchFuncStart(trimmed); ok {
			currentFn = fn
			currentFnIndent = leadingWhitespace(line)
		} else if currentFn != "" && isFuncCloseLine(trimmed) && leadingWhitespace(line) == currentFnIndent {
			// A closing brace/paren alone on a line, at the SAME indentation
			// as the function's own opening line, ends the function. Column
			// 0 alone (the previous rule) never closes an indented function
			// header like `    cleanup_shared_server() {` -- the function's
			// own closing brace is indented to match -- so every later line
			// in the file was wrongly attributed to that function (review:
			// "the scanner never closes an indented function header").
			// Matching the start line's indentation instead of hard-coding
			// column 0 handles both the common top-level case and a nested,
			// indented function definition the same way.
			currentFn = ""
			currentFnIndent = ""
		}
		if m := heredocStart.FindStringSubmatch(line); m != nil {
			inHeredoc = true
			heredocDelim = m[2]
			// The redirection line itself (e.g. `cat >&2 <<'EOF'`) is still
			// scanned below; only what follows is heredoc body.
		}
		spans := quotedSpans(line)
		codeSpans := dashCCodeSpans(line)
		lookupSpans := shellToolLookup.FindAllStringIndex(line, -1)

		for _, m := range shellHostToolVerbs.FindAllStringSubmatchIndex(line, -1) {
			if quoted(spans, codeSpans, m[0]) {
				continue
			}
			// No same-line `command -v go && go ...` guard exists any more
			// (shellCommandVGuard only recognizes gofmt): every `go` hit is
			// reported here regardless of what else shares its line.
			hits = append(hits, shellExecHit{line: lineno, fn: currentFn, snippet: lines[lineno-1], verb: line[m[2]:m[3]]})
		}
		for _, m := range shellGofmtExec.FindAllStringSubmatchIndex(line, -1) {
			start := strings.Index(line[m[0]:m[1]], "gofmt") + m[0]
			if quoted(spans, codeSpans, start) {
				continue
			}
			if insideAnyIntSpan(lookupSpans, start) {
				// This "gofmt" occurrence IS the `command -v`/`which`/`type`
				// lookup phrase itself, not a separate exec.
				continue
			}
			if guarded, ok := commandVGuardedExecEnd(line); ok && start == guarded {
				// A same-line `command -v gofmt ... && gofmt ...` (review:
				// "a same-line `command -v gofmt && gofmt` is skipped"): now
				// classified explicitly as the presence-guarded idiom it is,
				// and only that EXACT occurrence (the gofmt directly after
				// the guard's own `&&`) is excused -- not a range, so a
				// second, unrelated gofmt elsewhere on the line is still
				// reported.
				continue
			}
			hits = append(hits, shellExecHit{line: lineno, fn: currentFn, snippet: lines[lineno-1], verb: ""})
		}
	}
	return hits
}

// funcCloseLine matches a bare function-closing `}`/`)`, optionally followed
// by a trailing output redirection such as `>&2` or `2>&1` (review: "a
// function closed by `} >&2` isn't detected as ended"). No scoped script has
// one today, but a plain `trimmed == "}"` equality check would silently stop
// recognizing the function's end -- and leak whatever safety was computed
// inside it into the rest of the file -- the moment one is added.
var funcCloseLine = regexp.MustCompile(`^[})](\s+\S*>\S*)*$`)

// isFuncCloseLine reports whether trimmed is a function-closing line: a bare
// `}`/`)`, or one followed only by an output redirection.
func isFuncCloseLine(trimmed string) bool {
	return funcCloseLine.MatchString(trimmed)
}

// leadingWhitespace returns s's leading run of spaces/tabs.
func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

// shellFuncStart matches a shell function definition's opening line, any
// style this repo uses: `name() {`, `name() (`, `function name {`, or
// `function name() {`.
var shellFuncStart = regexp.MustCompile(`^(?:function\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(\))?|([A-Za-z_][A-Za-z0-9_]*)\s*\(\))\s*[{(]\s*$`)

func matchFuncStart(trimmed string) (string, bool) {
	m := shellFuncStart.FindStringSubmatch(trimmed)
	if m == nil {
		return "", false
	}
	if m[1] != "" {
		return m[1], true // `function name {` / `function name() {`
	}
	return m[2], true // `name() {` / `name() (`
}

// condVarCheck matches a condition fragment testing one of TEST_SRCDIR or a
// shellBazelSetVars entry: `-n "${VAR:-...}"`, `-z "${VAR:-...}"`,
// `-x "$VAR"`/`-x "${VAR}"`, or `${VAR:?...}`. It reports the variable name,
// the operator ("-n", "-z" or "-x"; ":?" is normalized to "-n", since a
// required-nonempty substitution has the same "true/reached implies set"
// polarity as an explicit -n check), and whether anything matched.
var condVarCheckN = regexp.MustCompile(`-n\s+"\$\{?(\w+)(:-[^}]*)?\}?"`)
var condVarCheckZ = regexp.MustCompile(`-z\s+"\$\{?(\w+)(:-[^}]*)?\}?"`)
var condVarCheckX = regexp.MustCompile(`-x\s+"\$\{?(\w+)(:-[^}]*)?\}?"`)
var condVarRequired = regexp.MustCompile(`\$\{(\w+):\?`)

// shellVarAlias matches a simple default-value assignment, `VAR="${OTHER:-...}"`
// or `VAR="${OTHER}"`, the pattern every shellPolicyScope script in this
// policy's scope uses to copy a Bazel-set variable (shellBazelSetVars) into a
// local name before testing it, e.g. .github/scripts/*-test-shard.sh's
// `CMD_BINARY="${BEADS_TEST_CMD_BINARY:-/tmp/bd-cmd-test}"`.
var shellVarAlias = regexp.MustCompile(`^(\w+)=\"\$\{(\w+)(:-[^}]*)?\}\"$`)

// shellVarAliases returns a map from a script-local variable name to the
// Bazel-set variable name (a shellBazelSetVars key) it was assigned from via
// a `VAR="${BAZEL_VAR:-default}"` default-value assignment, so
// bazelVarPolarity can recognize a later `-x "$VAR"`/`-n "$VAR"` test as
// referring, transitively, to the Bazel-set variable.
func shellVarAliases(src string) map[string]string {
	aliases := map[string]string{}
	for _, raw := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(raw)
		m := shellVarAlias.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		if shellBazelSetVars[m[2]] {
			aliases[m[1]] = m[2]
		}
	}
	return aliases
}

// bazelVarPolarity reports whether cond contains a recognized
// TEST_SRCDIR/shellBazelSetVars check and, if so, whether the condition being
// TRUE implies the script is running under Bazel (bazelWhenTrue), mirroring
// host_tool_exec_policy_test.go's condPolarity. A condition this function
// does not recognize at all (including anything containing `||`, which this
// line-based heuristic does not attempt to reason about) reports matched =
// false, so callers never treat an unrecognized condition as either kind of
// gate.
func bazelVarPolarity(cond string, aliases map[string]string) (bazelWhenTrue bool, matched bool) {
	if strings.Contains(cond, "||") {
		return false, false
	}
	isBazelVar := func(name string) bool {
		if name == "TEST_SRCDIR" || shellBazelSetVars[name] {
			return true
		}
		return shellBazelSetVars[aliases[name]]
	}
	if m := condVarRequired.FindStringSubmatch(cond); m != nil && isBazelVar(m[1]) {
		return true, true
	}
	if m := condVarCheckN.FindStringSubmatch(cond); m != nil && isBazelVar(m[1]) {
		return true, true
	}
	if m := condVarCheckX.FindStringSubmatch(cond); m != nil && isBazelVar(m[1]) {
		return true, true
	}
	if m := condVarCheckZ.FindStringSubmatch(cond); m != nil && isBazelVar(m[1]) {
		return false, true
	}
	return false, false
}

// ifFrame tracks one open if/elif/else block for shellLineGating's per-branch
// containment tracking (review point 1): the shell analogue of
// host_tool_exec_policy_test.go's within()/condPolarity-driven *ast.IfStmt
// walk, done textually over if/then/elif/else/fi keywords instead of an AST.
type ifFrame struct {
	matched           bool // condVarPolarity recognized this frame's own condition
	bodySafe          bool // lines directly in this frame's active branch never run under Bazel
	thenBazelWhenTrue bool // the (last-set) condition's polarity: true implies under-Bazel
	inElse            bool
	thenHasAbort      bool // an abort statement appeared directly in the then-branch
}

// shellLineGating replaces shellFileIsGated's whole-file, position-agnostic
// TEST_SRCDIR check (review point 1's bug: it exempted an entire file on any
// TEST_SRCDIR mention, anywhere, including in a comment or an unrelated
// branch). It walks src tracking if/elif/else/fi nesting and shell function
// boundaries, and returns the set of line numbers it can prove run only when
// NOT under Bazel -- either because they are lexically inside a branch whose
// condition's truth implies off-Bazel (containment style), or because they
// come after an if-block, in the same function, whose condition implies
// on-Bazel and which unconditionally returns/exits (abort style, the same
// two styles host_tool_exec_policy_test.go's gated() recognizes for Go).
//
// This is deliberately conservative and line-based, not a real shell parser:
// an unrecognized condition (including any `||`) leaves the surrounding code
// ungated rather than guessing, and `elif` is treated as if it reset to a
// fresh `if` rather than ANDing in the negation of earlier branches in the
// same chain (none of this policy's real call sites use elif alongside a
// TEST_SRCDIR/shellBazelSetVars condition, so that simplification does not
// currently cost any precision; a future case that needs it should extend
// this rather than work around it).
func shellLineGating(src string) map[int]bool {
	safe := map[int]bool{}
	lines := strings.Split(src, "\n")
	aliases := shellVarAliases(src)
	var stack []*ifFrame
	abortSafeFrom := -1 // line after which abort-style safety applies, -1 if none
	inFunc := false
	funcIndent := ""
	for i, raw := range lines {
		lineno := i + 1
		trimmed := strings.TrimSpace(raw)
		if _, ok := matchFuncStart(trimmed); ok {
			stack = nil
			abortSafeFrom = -1
			inFunc = true
			funcIndent = leadingWhitespace(raw)
		} else if inFunc && isFuncCloseLine(trimmed) && leadingWhitespace(raw) == funcIndent {
			// A closing brace/paren alone on a line, at the SAME indentation
			// as the function's own opening line, ends the function
			// (findShellHostToolExecs's currentFn tracking uses the same
			// rule). Column 0 alone (the previous rule) never closed an
			// indented function header like `    cleanup_shared_server() {`,
			// so this reset never fired for it and abort-style safety
			// computed inside one function (e.g. upgrade-smoke-test.sh's
			// build_candidate) leaked into whatever top-level code follows
			// the function, long after it closed (review: "the abort/skip
			// gate state isn't reset at function end"; "the scanner never
			// closes an indented function header").
			stack = nil
			abortSafeFrom = -1
			inFunc = false
			funcIndent = ""
		}
		switch {
		case strings.HasPrefix(trimmed, "if ") || trimmed == "if":
			cond := extractCond(trimmed, "if")
			bazelWhenTrue, matched := bazelVarPolarity(cond, aliases)
			frame := &ifFrame{matched: matched, thenBazelWhenTrue: bazelWhenTrue, bodySafe: matched && !bazelWhenTrue}
			stack = append(stack, frame)
		case strings.HasPrefix(trimmed, "elif "):
			if len(stack) > 0 {
				cond := extractCond(trimmed, "elif")
				bazelWhenTrue, matched := bazelVarPolarity(cond, aliases)
				top := stack[len(stack)-1]
				top.matched = matched
				top.thenBazelWhenTrue = bazelWhenTrue
				top.bodySafe = matched && !bazelWhenTrue
				top.thenHasAbort = false
			}
		case trimmed == "else":
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.inElse = true
				if top.matched {
					top.bodySafe = top.thenBazelWhenTrue
				}
			}
		case trimmed == "fi":
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				// Abort-style (review point 1/6): if this if-statement's
				// then-branch condition implied under-Bazel and the
				// then-branch unconditionally aborted, later lines in the
				// same function are reached only when that condition was
				// false, i.e. only off-Bazel.
				if top.matched && top.thenBazelWhenTrue && top.thenHasAbort {
					abortSafeFrom = lineno
				}
			}
		}
		if isAbortStmt(trimmed) && len(stack) > 0 {
			top := stack[len(stack)-1]
			if !top.inElse {
				top.thenHasAbort = true
			}
		}
		// A line's safety is the OR of every enclosing frame's current
		// branch being safe, or lying after a same-function abort.
		lineSafe := abortSafeFrom >= 0 && lineno > abortSafeFrom
		for _, f := range stack {
			if f.bodySafe {
				lineSafe = true
			}
		}
		if lineSafe {
			safe[lineno] = true
		}
	}
	return safe
}

var condExtract = regexp.MustCompile(`^(?:if|elif)\s+(.*?)\s*;?\s*then\s*$`)

// extractCond pulls the condition text out of an `if cond; then` or
// `if cond` line (the `then` may be on the next line, outside this
// function's view; extractCond returns the whole remainder in that case,
// which is fine since bazelVarPolarity only looks for substrings).
func extractCond(trimmed, kw string) string {
	if m := condExtract.FindStringSubmatch(trimmed); m != nil {
		return m[1]
	}
	return strings.TrimPrefix(trimmed, kw+" ")
}

var abortStmt = regexp.MustCompile(`^(return|exit|continue|break)\b`)

func isAbortStmt(trimmed string) bool {
	return abortStmt.MatchString(trimmed)
}

func shellMatchAllowlist(entries []shellAllowlistEntry, fn, snippet string) bool {
	for _, e := range entries {
		if e.fn == fn && strings.TrimSpace(e.snippet) == strings.TrimSpace(snippet) {
			return true
		}
	}
	return false
}

func TestShellHostToolExecPolicyScan(t *testing.T) {
	t.Run("ungated bare go build is reported", func(t *testing.T) {
		src := "#!/usr/bin/env bash\ngo build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one hit at line 2 (build)", hits)
		}
		if shellLineGating(src)[2] {
			t.Fatal("fixture has no gate; line 2 must not be reported safe")
		}
	})

	t.Run("go tool is reported (review point 2)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\ngo tool nm ./bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].verb != "tool" {
			t.Fatalf("got %+v, want one `tool` hit", hits)
		}
	})

	t.Run("bare gofmt exec is reported (review point 2)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\ngofmt -l .\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 {
			t.Fatalf("got %+v, want one gofmt hit", hits)
		}
	})

	t.Run("gofmt as a substring of a filename is not reported", func(t *testing.T) {
		src := "#!/usr/bin/env bash\nsource scripts/ci/gofmt-bin.sh\nGOFMT_BIN=/tmp/x\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (gofmt-bin.sh/GOFMT_BIN are not a `gofmt` exec)", hits)
		}
	})

	t.Run("command -v / which / type lookups are not reported", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"if ! command -v go >/dev/null 2>&1; then exit 1; fi\n" +
			"which gofmt >/dev/null\n" +
			"type go >/dev/null\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (tool-presence lookups, not execs)", hits)
		}
	})

	t.Run("comment lines are not scanned", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n# go build is mentioned here only in prose\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits", hits)
		}
	})

	t.Run("heredoc bodies are not scanned", func(t *testing.T) {
		src := "#!/usr/bin/env bash\ncat >&2 <<'EOF'\nrun `go build|test|run` yourself\nEOF\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (heredoc body)", hits)
		}
	})

	t.Run("arithmetic left-shift is not mistaken for a heredoc (review point 2)", func(t *testing.T) {
		// A bogus heredoc-start match here would swallow the rest of the
		// script (including the real `go build` below) as heredoc body.
		src := "#!/usr/bin/env bash\nx=$(( 1 << 2 ))\ngo build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 3 {
			t.Fatalf("got %+v, want one hit at line 3 (arithmetic shift must not start a heredoc)", hits)
		}
	})

	t.Run("a here-string is not mistaken for a heredoc", func(t *testing.T) {
		src := "#!/usr/bin/env bash\ncat <<< \"go build fake\"\ngo build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		found3 := false
		for _, h := range hits {
			if h.line == 3 {
				found3 = true
			}
		}
		if !found3 {
			t.Fatalf("got %+v, want a hit at line 3 (here-string must not start a heredoc)", hits)
		}
	})

	t.Run("printf and echo quoted message text are not scanned", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"printf 'error: unsupported go install module path\\n' >&2\n" +
			"echo \"run go version to check\"\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (printf/echo text)", hits)
		}
	})

	t.Run("a real exec joined onto an echo line by && is still reported (review point 2)", func(t *testing.T) {
		// A line-wide "contains echo" skip (the previous scanner's bug) would
		// hide this real second command.
		src := "#!/usr/bin/env bash\n" +
			"echo \"building\" && go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 {
			t.Fatalf("got %+v, want one hit at line 2 (go build after && is a real exec)", hits)
		}
	})

	t.Run("git grep search patterns are not scanned", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"git grep -n -E 'go install github\\.com/x/y@latest' -- . || true\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (git grep pattern)", hits)
		}
	})

	t.Run("command substitution exec is reported", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			`if [[ "$(go env GOVERSION 2>/dev/null)" == "go1.25" ]]; then` + "\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].verb != "env" {
			t.Fatalf("got %+v, want one `env` hit", hits)
		}
	})

	t.Run("dolt is never flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\ndolt sql -q 'select 1'\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (dolt is out of scope; see package comment)", hits)
		}
	})

	t.Run("TEST_SRCDIR gates only its own branch, not the whole file (review point 1)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"if [[ -z \"${TEST_SRCDIR:-}\" ]]; then\n" +
			"  go build -o out ./cmd/bd\n" +
			"fi\n" +
			"go build -o out2 ./cmd/bd\n"
		gating := shellLineGating(src)
		if !gating[3] {
			t.Fatal("line 3 is inside the off-Bazel-only branch; must be reported safe")
		}
		if gating[5] {
			t.Fatal("line 5 is NOT inside any TEST_SRCDIR branch; a whole-file gate would " +
				"wrongly report it safe (review point 1's bug)")
		}
	})

	t.Run("TEST_SRCDIR-true branch is not safe, its else is", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"if [[ -n \"${TEST_SRCDIR:-}\" ]]; then\n" +
			"  go build -o out ./cmd/bd\n" +
			"else\n" +
			"  go build -o out2 ./cmd/bd\n" +
			"fi\n"
		gating := shellLineGating(src)
		if gating[3] {
			t.Fatal("line 3 runs only under Bazel; must not be reported safe")
		}
		if !gating[5] {
			t.Fatal("line 5 runs only off-Bazel (the else of a TEST_SRCDIR-set check); must be safe")
		}
	})

	t.Run("abort-style: unset-check that returns makes the rest of the function safe", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"build_candidate() {\n" +
			"    if [ -n \"${CANDIDATE_BIN:-}\" ] && [ -x \"${CANDIDATE_BIN}\" ]; then\n" +
			"        echo \"$CANDIDATE_BIN\"\n" +
			"        return\n" +
			"    fi\n" +
			"    go build -o \"$candidate\" ./cmd/bd\n" +
			"}\n"
		gating := shellLineGating(src)
		if !gating[7] {
			t.Fatal("line 7 is reached only when CANDIDATE_BIN is unset, i.e. off-Bazel; must be safe")
		}
	})

	t.Run("abort-style safety does not leak into the next function (review point 1)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"build_candidate() {\n" +
			"    if [ -n \"${CANDIDATE_BIN:-}\" ]; then\n" +
			"        return\n" +
			"    fi\n" +
			"    true\n" +
			"}\n" +
			"other_fn() {\n" +
			"    go build -o out ./cmd/bd\n" +
			"}\n"
		if shellLineGating(src)[9] {
			t.Fatal("line 9 is in a different function; must not inherit the previous " +
				"function's abort-style safety")
		}
	})

	t.Run("abort-style safety does not leak into top-level code after the function ends (mutation check, review: upgrade-smoke-test.sh)", func(t *testing.T) {
		// The previous test only pinned that a SECOND function doesn't
		// inherit the gate (matchFuncStart resets state when a new function
		// starts). This pins the bug the review actually found: top-level
		// code AFTER a function closes, with no new function definition in
		// between, must not inherit abort-style safety computed inside that
		// function either -- the shape scripts/upgrade-smoke-test.sh uses
		// (a top-level `go build` after build_candidate()).
		src := "#!/usr/bin/env bash\n" +
			"build_candidate() {\n" +
			"    if [ -n \"${CANDIDATE_BIN:-}\" ]; then\n" +
			"        return\n" +
			"    fi\n" +
			"    true\n" +
			"}\n" +
			"go build -o out ./cmd/bd\n"
		if shellLineGating(src)[8] {
			t.Fatal("line 8 is top-level code after the function closed; must not inherit " +
				"build_candidate's abort-style safety")
		}
	})

	t.Run("bash -c 'go build' is detected (mutation check, review regression)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"bash -c 'go build -o out ./cmd/bd'\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one `build` hit at line 2: bash -c's quoted argument really "+
				"execs go build, it is not message text", hits)
		}
		if shellLineGating(src)[2] {
			t.Fatal("fixture has no gate; line 2 must not be reported safe")
		}
	})

	t.Run("an alias of a Bazel-set var still gates (review point 1, shard-script pattern)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"CMD_BINARY=\"${BEADS_TEST_CMD_BINARY:-/tmp/bd-cmd-test}\"\n" +
			"if [ -x \"$CMD_BINARY\" ]; then\n" +
			"  exec \"$CMD_BINARY\" -test.v\n" +
			"else\n" +
			"  exec go test -tags=gms_pure_go ./cmd/bd/\n" +
			"fi\n"
		if !shellLineGating(src)[6] {
			t.Fatal("line 6 runs only when the Bazel-set BEADS_TEST_CMD_BINARY-derived " +
				"binary is missing, i.e. off-Bazel; must be safe")
		}
	})

	t.Run("an unrecognized condition is not treated as a gate", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"if [ \"$FOO\" = \"1\" ]; then\n" +
			"  go build -o out ./cmd/bd\n" +
			"fi\n"
		if shellLineGating(src)[3] {
			t.Fatal("an unrelated condition must not be treated as a Bazel gate")
		}
	})

	t.Run("a condition with || is not treated as a gate (conservative)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"if [[ -z \"${TEST_SRCDIR:-}\" ]] || [[ -n \"${FORCE_LOCAL:-}\" ]]; then\n" +
			"  go build -o out ./cmd/bd\n" +
			"fi\n"
		if shellLineGating(src)[3] {
			t.Fatal("a disjunctive condition is not reasoned about; must not be reported safe")
		}
	})

	t.Run("an indented function's own indented closing brace ends it (mutation check, review: test.sh cleanup_shared_server)", func(t *testing.T) {
		// Mirrors scripts/test.sh:191-195's shape: a function defined,
		// indented, inside other indented code, whose own closing brace is
		// indented to match. A scanner that only recognizes a column-0 `}`
		// would never see this function end, and would wrongly attribute
		// the top-level `go build` below to cleanup_shared_server instead
		// of to top level.
		src := "#!/usr/bin/env bash\n" +
			"if true; then\n" +
			"    cleanup_shared_server() {\n" +
			"        rm -rf \"$DIR\"\n" +
			"    }\n" +
			"fi\n" +
			"go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		found := false
		for _, h := range hits {
			if h.line == 7 {
				found = true
				if h.fn != "" {
					t.Fatalf("line 7 hit fn = %q, want \"\" (top level, past the indented function's "+
						"own closing brace), got hits %+v", h.fn, hits)
				}
			}
		}
		if !found {
			t.Fatalf("got %+v, want a hit at line 7", hits)
		}
		// shellLineGating's own function-boundary tracking must reset the
		// same way: abort-style safety computed (if any) inside
		// cleanup_shared_server must not leak past its indented close.
		src2 := "#!/usr/bin/env bash\n" +
			"if true; then\n" +
			"    cleanup_shared_server() {\n" +
			"        if [ -n \"${CANDIDATE_BIN:-}\" ]; then\n" +
			"            return\n" +
			"        fi\n" +
			"    }\n" +
			"fi\n" +
			"go build -o out ./cmd/bd\n"
		if shellLineGating(src2)[9] {
			t.Fatal("line 9 is top-level code after the indented function's indented close; " +
				"must not inherit its abort-style safety")
		}
	})

	t.Run("a `function name {` header is recognized (POSIX/ksh style)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"function cleanup {\n" +
			"    true\n" +
			"}\n" +
			"go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 5 || hits[0].fn != "" {
			t.Fatalf("got %+v, want one hit at line 5 attributed to top level", hits)
		}
	})

	t.Run("a `function name() {` header is recognized", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"function cleanup() {\n" +
			"    go build -o out ./cmd/bd\n" +
			"}\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].fn != "cleanup" {
			t.Fatalf("got %+v, want one hit attributed to fn \"cleanup\"", hits)
		}
	})

	t.Run("a function closed by `} >&2` is recognized as ended (review follow-up)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"log() {\n" +
			"    echo \"$*\"\n" +
			"} >&2\n" +
			"go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 5 || hits[0].fn != "" {
			t.Fatalf("got %+v, want one hit at line 5 attributed to top level: `} >&2` ends log(), "+
				"a bare `}`-only check never would", hits)
		}
		src2 := "#!/usr/bin/env bash\n" +
			"log() {\n" +
			"    if [ -n \"${CANDIDATE_BIN:-}\" ]; then\n" +
			"        return\n" +
			"    fi\n" +
			"} >&2\n" +
			"go build -o out ./cmd/bd\n"
		if shellLineGating(src2)[7] {
			t.Fatal("line 7 is top-level code after log()'s `} >&2` close; must not inherit " +
				"its abort-style safety")
		}
	})

	t.Run("same-line `command -v gofmt && gofmt` is classified as the guarded idiom, not flagged (review)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"command -v gofmt >/dev/null 2>&1 && gofmt -l .\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits: command -v gofmt && gofmt is safe by construction "+
				"(gofmt is not guaranteed on the hermetic bazel-test PATH)", hits)
		}
	})

	t.Run("same-line `command -v go && go build` is NOT a guarded idiom (should-fix B)", func(t *testing.T) {
		// go is deliberately not covered by shellCommandVGuard at all (see
		// its doc comment): a command -v go guard is a worker-dependent
		// skip this policy bans, not a safe-by-construction idiom.
		src := "#!/usr/bin/env bash\n" +
			"command -v go >/dev/null 2>&1 && go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one `build` hit at line 2: a command -v go guard must not excuse it", hits)
		}
	})

	t.Run("should-fix B: command -v go-junit-report && go test is still flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"command -v go-junit-report >/dev/null 2>&1 && go test ./...\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "test" {
			t.Fatalf("got %+v, want one `test` hit at line 2: a go-junit-report lookup must not "+
				"excuse an unrelated go exec (exact tool-name match, not a go-* prefix)", hits)
		}
	})

	t.Run("should-fix B: command -v gofmt && echo; go build is still flagged", func(t *testing.T) {
		// The `… && echo; go build` family: the lookup's own && really does
		// guard `echo`, but the go build after the `;` is a separate,
		// unconditional command that must still be reported.
		src := "#!/usr/bin/env bash\n" +
			"command -v gofmt >/dev/null 2>&1 && echo; go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one `build` hit at line 2", hits)
		}
	})

	t.Run("should-fix B: command -v gofmt && true || go build is still flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"command -v gofmt >/dev/null 2>&1 && true || go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one `build` hit at line 2", hits)
		}
	})

	t.Run("should-fix B: command -v gofmt && echo || true && go build is still flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"command -v gofmt >/dev/null 2>&1 && echo || true && go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one `build` hit at line 2", hits)
		}
	})

	t.Run("should-fix B: ! command -v go && go build is still flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"! command -v go >/dev/null 2>&1 && go build -o out ./cmd/bd\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "build" {
			t.Fatalf("got %+v, want one `build` hit at line 2: a negated lookup guards nothing", hits)
		}
	})

	t.Run("should-fix B: which go; [ x ] && go test is still flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"which go; [ -x /usr/bin/go ] && go test ./...\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 || hits[0].verb != "test" {
			t.Fatalf("got %+v, want one `test` hit at line 2: the lookup is separated from the "+
				"exec's && by a ; and an unrelated [ x ] test", hits)
		}
	})

	t.Run("should-fix B generalized: ! command -v gofmt && gofmt is still flagged", func(t *testing.T) {
		// Same leading-! requirement applies to the gofmt guard that remains.
		src := "#!/usr/bin/env bash\n" +
			"! command -v gofmt >/dev/null 2>&1 && gofmt -l .\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 {
			t.Fatalf("got %+v, want one hit at line 2: a negated lookup guards nothing", hits)
		}
	})

	t.Run("should-fix B generalized: command -v gofmt-foo && gofmt is still flagged (exact tool-name match)", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"command -v gofmt-foo >/dev/null 2>&1 && gofmt -l .\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 {
			t.Fatalf("got %+v, want one hit at line 2: gofmt-foo is not gofmt", hits)
		}
	})

	t.Run("should-fix B generalized: command -v gofmt && echo; gofmt is still flagged", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"command -v gofmt >/dev/null 2>&1 && echo; gofmt -l .\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 {
			t.Fatalf("got %+v, want one hit at line 2: the gofmt exec after the ; is unguarded", hits)
		}
	})

	t.Run("an unrelated exec sharing a line with an unrelated lookup is still reported (guard is tool- and position-specific)", func(t *testing.T) {
		// The guard must not degrade back into a blanket same-line skip:
		// looking up `go` must not excuse an unrelated `gofmt` exec later
		// on the same line (different tool), and a lookup with no `&&` at
		// all must not excuse anything.
		src := "#!/usr/bin/env bash\n" +
			"command -v go >/dev/null 2>&1 && gofmt -l .\n"
		hits := findShellHostToolExecs(src)
		if len(hits) != 1 || hits[0].line != 2 {
			t.Fatalf("got %+v, want one hit at line 2: the gofmt exec is not guarded by a `go` lookup", hits)
		}
	})

	t.Run("a bare command -v gofmt lookup, with no following &&, is still just a lookup", func(t *testing.T) {
		src := "#!/usr/bin/env bash\n" +
			"if command -v gofmt >/dev/null 2>&1; then\n" +
			"  echo found\n" +
			"fi\n"
		if hits := findShellHostToolExecs(src); len(hits) != 0 {
			t.Fatalf("got %+v, want no hits (plain presence lookup, no exec)", hits)
		}
	})
}

// TestNoUngatedBareGoInBazelReachedShellScripts scans shellPolicyScope and
// fails on an unallowlisted, ungated bare `go`/`gofmt` exec. See the package
// comment for scope and the detection tradeoffs.
func TestNoUngatedBareGoInBazelReachedShellScripts(t *testing.T) {
	root := sourceRepoRoot(t)
	seenAllow := map[string]map[string]bool{} // file -> "fn\x00snippet" -> true
	for _, rel := range shellPolicyScope {
		path := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (shellPolicyScope entry no longer exists; update it)", rel, err)
		}
		src := string(data)
		gating := shellLineGating(src)
		for _, hit := range findShellHostToolExecs(src) {
			if gating[hit.line] {
				continue
			}
			if shellMatchAllowlist(shellExecAllowlist[rel], hit.fn, hit.snippet) {
				if seenAllow[rel] == nil {
					seenAllow[rel] = map[string]bool{}
				}
				seenAllow[rel][hit.fn+"\x00"+strings.TrimSpace(hit.snippet)] = true
				continue
			}
			tool := "go " + hit.verb
			if hit.verb == "" {
				tool = "gofmt"
			}
			t.Errorf("%s:%d: ungated bare `%s`: under `bazel test` the hermetic-first PATH "+
				"may lack a host toolchain, so this can exit 127 on some workers (f4-spec.md "+
				"§9); gate it on TEST_SRCDIR/a shellBazelSetVars entry, resolve it through a "+
				"Bazel-wired env var, or add a justified shellExecAllowlist entry\n\t%s",
				rel, hit.line, tool, strings.TrimSpace(hit.snippet))
		}
	}
	for rel, entries := range shellExecAllowlist {
		for _, e := range entries {
			if !seenAllow[rel][e.fn+"\x00"+strings.TrimSpace(e.snippet)] {
				t.Errorf("shellExecAllowlist entry %s (fn=%q, snippet=%q) no longer matches "+
					"an ungated bare exec; remove it", rel, e.fn, e.snippet)
			}
		}
	}
}

// TestShellPolicyScopeFilesExist is a guard against shellPolicyScope rotting
// silently: repoFiles-style tests elsewhere skip missing files quietly, but
// this list is hand-maintained, so a missing entry should fail loudly instead
// of just scanning fewer files than the comment above claims.
func TestShellPolicyScopeFilesExist(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, rel := range shellPolicyScope {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("shellPolicyScope entry %s: %v", rel, err)
		}
	}
	if len(shellPolicyScope) < 10 {
		t.Fatalf("shellPolicyScope has only %d entries; did a bulk edit truncate it?", len(shellPolicyScope))
	}
}

// TestShellPolicyScopeCoversBazelShellData (review point 3's mechanical
// backstop: a regex walk over every BUILD.bazel looking for a literal `.sh`
// path under scripts/ or .github/scripts/ in a data/srcs list) was removed
// (gastownhall/beads#7485 review item 4). Its regex could not parse a
// `//pkg:file.sh` label, a `.bzl` file, or a glob() -- the three real
// reference shapes this repo's BUILD files actually use for most scripts in
// shellPolicyScope above -- so even with //:repo_other_files (every tracked
// BUILD/.bzl/script file) as data, it would not have caught
// check-versions.sh, scripts/test.sh, scripts/conformance.sh or
// scripts/release.sh going missing from shellPolicyScope; a hand reviewer
// citing each script's BUILD wiring (as the comment above does) is what
// actually found them. Paying //:repo_other_files' cost on every target that
// used it (758 extra input files, a measured 91.5%->96% rise in this
// package's cache-rerun rate across the whole repo) for a check that was
// already demonstrably incomplete was not worth it; there is no `bazel
// query`-free static check of Starlark label/glob references this policy can
// do honestly, so shellPolicyScope's accuracy depends on the hand curation
// above, same as the run_check.sh-as-launcher family already did.
