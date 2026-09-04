package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// cmd/bd's package tests run ~602-604s under -race (721 test files, ~2697
// tests) — within seconds of go test's 10m default per-package budget, which
// intermittently trips CI on unrelated PRs (see docs-only PR #6001, which
// failed identically). pr-core.sh must set an explicit -timeout comfortably
// above that observed worst case.
func TestPRCoreGoTestHasExplicitTimeoutAboveDefaultBudget(t *testing.T) {
	path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", "pr-core.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pr-core.sh: %v", err)
	}

	invocation := goTestInvocationLine(t, string(data))

	timeoutRE := regexp.MustCompile(`-timeout(?:=|\s+)(\S+)`)
	match := timeoutRE.FindStringSubmatch(invocation)
	if match == nil {
		t.Fatalf("pr-core.sh go test invocation has no -timeout flag:\n%s", invocation)
	}

	timeout, err := time.ParseDuration(match[1])
	if err != nil {
		t.Fatalf("pr-core.sh -timeout value %q does not parse as a duration: %v", match[1], err)
	}

	const goDefaultTestTimeout = 10 * time.Minute
	if timeout <= goDefaultTestTimeout {
		t.Fatalf("pr-core.sh -timeout is %s, want strictly greater than go test's default %s so cmd/bd's ~602-604s -race runtime has real headroom", timeout, goDefaultTestTimeout)
	}
}

// goTestInvocationLine locates the single logical "go test ..." command in
// pr-core.sh — as opposed to the ci_time label string, which also contains
// the substring "go test" — and flattens any backslash line continuations
// into one line so flag parsing does not depend on how the command wraps.
func goTestInvocationLine(t *testing.T, script string) string {
	t.Helper()
	lines := strings.Split(script, "\n")

	var starts []int
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "go test ") {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("pr-core.sh has %d lines starting a `go test` invocation, want exactly 1: %v", len(starts), starts)
	}

	var parts []string
	for i := starts[0]; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		continued := strings.HasSuffix(line, `\`)
		line = strings.TrimSuffix(line, `\`)
		parts = append(parts, strings.TrimSpace(line))
		if !continued {
			break
		}
	}
	return strings.Join(parts, " ")
}
