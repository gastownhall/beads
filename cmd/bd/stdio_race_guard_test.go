//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// cobraParallelUnsafeMethods lists cobra.Command methods that touch process
// global state or lazily mutate inherited flag caches. Calling any of these in
// a parallel test can race with stdio capture or another shared command tree
// inspection.
var cobraParallelUnsafeMethods = []string{
	".Find(",
	".Help(",
	".InheritedFlags(",
	".Execute(",
	".Print(",
	".Printf(",
	".Println(",
	".PrintErr(",
	".PrintErrf(",
	".PrintErrln(",
	".Usage(",
	".UsageString(",
}

// TestCobraParallelPolicyGuard scans test source files and fails if any
// parallel test calls shared Cobra methods that are not safe under parallel
// execution. This prevents races where cobra's OutOrStdout()/ErrOrStderr()
// reads os.Stdout/os.Stderr concurrently with captureStdout()/captureStderr()
// redirecting them, and races where Find()/InheritedFlags() lazily merge
// persistent parent flags on shared global command objects.
//
// The rule: if a top-level test function or test method contains t.Parallel()
// (in code, not comments), it must NOT call any method name in
// cobraParallelUnsafeMethods. The matcher is intentionally receiver-agnostic;
// it favors a clear false positive over missing a shared Cobra command-state
// race. Setting cmd.SetOut()/SetErr() does NOT prevent output races because
// cobra eagerly evaluates os.Stdout as the default argument before checking
// outWriter.
//
// Fix options for flagged tests:
//  1. Remove t.Parallel() (preferred for fast tests)
//  2. Serialize the cobra call under stdioMutex
//
// This is intentionally blunt regex matching (80/20), not full AST analysis.
func TestCobraParallelPolicyGuard(t *testing.T) {
	t.Parallel()

	testFiles, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	funcPattern := regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s+)?(Test\w+)\(`)

	for _, file := range testFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		content := string(data)

		matches := funcPattern.FindAllStringIndex(content, -1)
		names := funcPattern.FindAllStringSubmatch(content, -1)

		for i, match := range matches {
			start := match[0]
			end := len(content)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			body := content[start:end]
			funcName := names[i][1]

			// Strip comment lines to avoid false positives from
			// "// Not using t.Parallel() because ..." etc.
			stripped := stripLineComments(body)

			if !strings.Contains(stripped, "t.Parallel()") {
				continue
			}

			// Also strip string literals containing method names (this
			// guard scans itself, so its string constants would match).
			strippedStrings := stripStringLiterals(stripped)

			for _, method := range cobraParallelUnsafeMethods {
				if strings.Contains(strippedStrings, method) {
					t.Errorf("%s:%s is t.Parallel() and calls method name %s — "+
						"this can race with stdio capture or shared command flag caches. "+
						"Remove t.Parallel() or serialize under stdioMutex. "+
						"See stdioMutex in test_helpers_pure_test.go.",
						file, funcName, method)
					break // one error per function is enough
				}
			}
		}
	}
}

// stripLineComments removes // comment lines, preserving code lines.
func stripLineComments(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// stripStringLiterals replaces quoted strings with empty quotes to prevent
// the guard from matching method names inside string constants.
func stripStringLiterals(s string) string {
	var b strings.Builder
	inString := false
	inRawString := false
	escaped := false
	for _, ch := range s {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString && !inRawString {
			escaped = true
			continue
		}
		if ch == '"' && !inRawString {
			inString = !inString
			b.WriteRune(ch)
			continue
		}
		if ch == '`' && !inString {
			inRawString = !inRawString
			b.WriteRune(ch)
			continue
		}
		if !inString && !inRawString {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// TestStdioMutexContract verifies that stdioMutex actually serializes
// captureStdout calls. If someone removes or bypasses the mutex, this
// test fails deterministically without needing -race.
func TestStdioMutexContract(t *testing.T) {
	var inside atomic.Int32
	var violations atomic.Int32

	const goroutines = 4
	const iterations = 50

	done := make(chan struct{}, goroutines)

	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			for range iterations {
				captureStdout(t, func() error {
					n := inside.Add(1)
					if n > 1 {
						violations.Add(1)
					}
					// Busyloop to widen the race window
					sum := 0
					for j := range 100 {
						sum += j
					}
					_ = sum
					inside.Add(-1)
					return nil
				})
			}
		}()
	}

	for range goroutines {
		<-done
	}

	if v := violations.Load(); v > 0 {
		t.Fatalf("stdioMutex failed to serialize: %d concurrent entries detected", v)
	}
}
