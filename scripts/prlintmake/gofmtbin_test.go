package prlintmake

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestGofmtBinMatchesGoModDirective(t *testing.T) {
	goBin := testGo(t)
	want := "go" + goModToolchainVersion(t)

	resolved, stderr := runGofmtBin(t, nil, nil)

	output, err := exec.Command(goBin, "version", resolved).CombinedOutput()
	if err != nil {
		t.Fatalf("go version %s: %v\n%s\nresolver stderr:\n%s", resolved, err, output, stderr)
	}
	fields := strings.Fields(string(output))
	got := fields[len(fields)-1]
	if got != want {
		t.Fatalf("gofmt-bin.sh resolved %s (built with %s), want a %s gofmt.\n"+
			"CI formats with go.mod's version, so this host's `make fmt` would "+
			"rewrite files CI then rejects.\nresolver stderr:\n%s",
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
	// An empty GOFMT is the resolver's "not set" case, but exporting it empty
	// would make the override test indistinguishable from the default one.
	if env["GOFMT"] == "" {
		delete(env, "GOFMT")
	}

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

// goModToolchainVersion returns go.mod's go directive in GOTOOLCHAIN form, i.e.
// with the patch component a bare "go 1.26" directive omits.
func goModToolchainVersion(t *testing.T) string {
	t.Helper()
	file, err := os.Open(filepath.Join(sourceRepoRoot(), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "go" {
			if strings.Count(fields[1], ".") == 1 {
				return fields[1] + ".0"
			}
			return fields[1]
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("no go directive in go.mod")
	return ""
}

func testGo(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
	return path
}
