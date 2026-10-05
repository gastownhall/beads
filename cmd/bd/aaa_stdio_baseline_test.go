package main

import (
	"os"
	"testing"
)

// baselineStdout/baselineStderr are the os.Stdout/os.Stderr objects as the
// FIRST test in this package saw them, captured here rather than at package
// var-init: under `go test -json` (what gotestsum runs, so every CI coverage
// leg) the testing framework itself reassigns os.Stderr = os.Stdout inside
// M.Run so both streams interleave cleanly into one test2json event stream
// (go1.26 testing/testing.go:2391, go.dev/issue/33419). That swap is the
// framework's, not a test's; a leak guard that baselines at var-init convicts
// the framework on every -json run (#5881) while measuring nothing about the
// tests. This file is named aaa_* so its test is declared first: go test runs
// tests in declaration order across files sorted by name (no -shuffle here).
var baselineStdout, baselineStderr *os.File

func TestAAAStdioBaseline(t *testing.T) {
	baselineStdout, baselineStderr = os.Stdout, os.Stderr
}
