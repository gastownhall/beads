package prlintmake

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// The regression tests for be-gx8.
//
// gofmt's output is not stable across Go releases, and the repo's formatting
// gate is judged by whichever binary runs. CI installs exactly go.mod's version
// (actions/setup-go with go-version-file), so any resolution that can land on a
// different Go silently disagrees with the gate that blocks merges -- and the
// gate's own advice, "run make fmt", then rewrites files into a form CI
// rejects.
//
// These pin the resolution itself rather than fmt-check.sh's reporting, because
// the shipped bug was in the resolution: a bare `gofmt` reached PATH, and every
// output assertion around it stayed green.

func TestGofmtBinMatchesGoModToolchain(t *testing.T) {
	goBin := testGo(t)
	want := "go" + goModToolchainVersion(t)

	resolved, stderr := runGofmtBin(t, nil, nil)

	output, err := exec.Command(goBin, "version", resolved).CombinedOutput()
	if err != nil {
		t.Fatalf("go version %s: %v\n%s\nresolver stderr:\n%s", resolved, err, output, stderr)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		t.Fatalf("go version %s printed no fields\nresolver stderr:\n%s", resolved, stderr)
	}
	got := fields[len(fields)-1]
	if got != want {
		t.Fatalf("gofmt-bin.sh resolved %s (built with %s), want a %s gofmt.\n"+
			"CI and every make target format with the toolchain go.mod selects, "+
			"so this host's `make fmt` would rewrite files CI then rejects.\n"+
			"resolver stderr:\n%s",
			resolved, got, want, stderr)
	}
}

func TestGofmtBinIgnoresPathGofmt(t *testing.T) {
	testGo(t)
	bash := testBash(t)
	shimDir := filepath.Join(t.TempDir(), "path shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(shimDir, "gofmt")
	writeShellExecutable(t, bash, shim, "#!/usr/bin/env bash\nexit 0\n")

	path := shimDir + string(os.PathListSeparator) + os.Getenv("PATH")
	resolved, stderr := runGofmtBin(t, map[string]string{"PATH": path}, nil)

	if resolved == shellVisiblePath(shim) {
		t.Fatalf("gofmt-bin.sh returned the PATH shim %s; it must resolve from "+
			"go.mod's toolchain instead.\nresolver stderr:\n%s", resolved, stderr)
	}
}

func TestGofmtBinHonorsGOFMTOverride(t *testing.T) {
	want := shellVisiblePath(filepath.Join(t.TempDir(), "explicit gofmt"))

	resolved, stderr := runGofmtBin(t, map[string]string{"GOFMT": want}, nil)

	if resolved != want {
		t.Fatalf("gofmt-bin.sh = %q, want the GOFMT override %q\nresolver stderr:\n%s",
			resolved, want, stderr)
	}
}

// runGofmtBin runs scripts/ci/gofmt-bin.sh and returns its stdout (the resolved
// path) and stderr separately. Fallback warnings go to stderr, so keeping them
// apart is what lets a failure report why the resolution went the way it did.
func runGofmtBin(t *testing.T, overrides map[string]string, args []string) (string, string) {
	t.Helper()
	bash := testBash(t)
	env := map[string]string{
		"BASH_ENV":  "",
		"BASHOPTS":  "",
		"ENV":       "",
		"GOFMT":     "",
		"LANG":      "C",
		"LC_ALL":    "C",
		"SHELLOPTS": "",
	}
	for key, value := range overrides {
		env[key] = value
	}
	// GOFMT is exported empty rather than deleted from the map. environment()
	// starts from the ambient environment, so deleting the key here would leave
	// an ambient GOFMT in place -- and that short-circuits the very resolution
	// these tests exist to pin, failing one of them with a message blaming the
	// script and passing the other vacuously. gofmt-bin.sh reads GOFMT with
	// [[ -n "${GOFMT:-}" ]], so empty already is its "not set" case, and the
	// override test stays distinguishable by setting a non-empty value.

	script := shellVisiblePath(filepath.Join(sourceRepoRoot(), "scripts", "ci", "gofmt-bin.sh"))
	cmd := exec.Command(bash, append([]string{"--noprofile", "--norc", "--", script}, args...)...)
	cmd.Dir = sourceRepoRoot()
	cmd.Env = environment(env)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("gofmt-bin.sh failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr.String())
	}
	return strings.TrimSpace(normalizeNewlines(string(stdout))), stderr.String()
}

// goModToolchainVersion returns the Go version go.mod selects, in GOTOOLCHAIN
// form: the toolchain directive when present, otherwise the go directive, with
// the patch component a bare "go 1.26" directive omits.
//
// Toolchain-first is the repo's precedence, not this test's: Makefile:85-90
// exports GOTOOLCHAIN from the same pair for every make target, and
// scripts/bazel_policy_test.go's goModToolchainVersion is the same rule in Go.
// `go` is the floor promised to importers; `toolchain` is what this repo is
// built and formatted with, so it is what the formatting gate must agree with.
func goModToolchainVersion(t *testing.T) string {
	t.Helper()
	file, err := os.Open(filepath.Join(sourceRepoRoot(), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	goDirective := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "toolchain":
			// `toolchain default` is legal and names no version, so it must fall
			// through to the `go` directive exactly as gofmt-bin.sh does -- the
			// script's awk requires the same literal `go` prefix. Returning the
			// bare value here would make this helper agree with a buggy resolver
			// instead of detecting it.
			if version, ok := strings.CutPrefix(fields[1], "go"); ok {
				return withPatchComponent(version)
			}
		case "go":
			if goDirective == "" {
				goDirective = fields[1]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if goDirective != "" {
		return withPatchComponent(goDirective)
	}
	t.Fatal("no toolchain or go directive in go.mod")
	return ""
}

// withPatchComponent returns a GOTOOLCHAIN-shaped version: names there carry
// the patch component that a bare "go 1.26" directive is allowed to omit.
func withPatchComponent(version string) string {
	if strings.Count(version, ".") == 1 {
		return version + ".0"
	}
	return version
}

// testGo returns the go the test runs and puts it first on PATH for the
// scripts it starts. Under Bazel that is the registered Go SDK (BUILD data,
// BEADS_TEST_GO), which is go.mod's toolchain: the go of the executor's PATH
// may be another release, and resolving go.mod's toolchain through it
// downloads one, which fails where actions have no network.
func testGo(t *testing.T) string {
	t.Helper()
	if bazeltest.IsBazel() {
		path, err := bazeltest.RunfileEnv("BEADS_TEST_GO")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
		return path
	}
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
	return path
}
