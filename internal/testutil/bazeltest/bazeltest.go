// Package bazeltest resolves repository paths for tests so the same test code
// works under plain `go test` (working directory inside the package, full
// checkout on disk), under `bazel test` locally, and under remote execution,
// where the only filesystem a test sees is its declared runfiles.
//
// Under plain `go test` every helper reduces to the pre-Bazel behavior, so
// adopting it never changes what `go test` does.
//
// Repository-root resolution order:
//
//  1. BEADS_TEST_REPO_ROOT, an explicit override (debug escape hatch); it is
//     honored only when it names a directory holding go.mod
//  2. under Bazel, the runfiles workspace root ($TEST_SRCDIR/$TEST_WORKSPACE);
//     it holds exactly the files the test declares in its BUILD `data`
//  3. a walk up from the working directory to go.mod (plain `go test`)
package bazeltest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RepoRootEnv names the environment variable that overrides repository-root
// resolution for every helper in this package.
const RepoRootEnv = "BEADS_TEST_REPO_ROOT"

// IsBazel reports whether the test binary runs under `bazel test`.
func IsBazel() bool {
	return os.Getenv("TEST_SRCDIR") != ""
}

// runfilesDir returns the absolute runfiles directory, or "" outside Bazel.
func runfilesDir() string {
	dir := os.Getenv("RUNFILES_DIR")
	if dir == "" {
		dir = os.Getenv("TEST_SRCDIR")
	}
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// workspaceRoot returns the runfiles directory of the main repository, or ""
// outside Bazel.
func workspaceRoot() string {
	rf := runfilesDir()
	if rf == "" {
		return ""
	}
	ws := os.Getenv("TEST_WORKSPACE")
	if ws == "" {
		ws = "_main"
	}
	root := filepath.Join(rf, ws)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return ""
	}
	return root
}

// envRoot returns BEADS_TEST_REPO_ROOT and whether it is usable: set and
// holding a go.mod, so a stale or mistyped value never redirects a scan.
func envRoot() (string, bool) {
	root := os.Getenv(RepoRootEnv)
	if root == "" {
		return "", false
	}
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return root, err == nil
}

// OverrideRoot returns the repository root when BEADS_TEST_REPO_ROOT names a
// directory holding go.mod or the test runs under Bazel, and "" otherwise. It is the drop-in condition
// for existing path helpers:
//
//	if root := bazeltest.OverrideRoot(); root != "" {
//		return filepath.Join(root, "docs")
//	}
//	// pre-Bazel resolution, unchanged under plain go test
//
// Under Bazel the returned tree holds only the test's declared data.
func OverrideRoot() string {
	if root, ok := envRoot(); ok {
		return root
	}
	return workspaceRoot()
}

// RepoRoot resolves the repository root; see the package comment for the
// resolution order. It fails the test when no root can be found.
func RepoRoot(t testing.TB) string {
	t.Helper()
	if root, ok := envRoot(); ok {
		return root
	} else if root != "" {
		t.Fatalf("%s=%s has no go.mod", RepoRootEnv, root)
	}
	if root := workspaceRoot(); root != "" {
		return root
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatalf("could not locate go.mod above %s", wd)
		}
	}
}

// CallerDir returns the directory of a source file reported by
// runtime.Caller. Under Bazel the reported path is workspace-relative, so the
// directory is rebuilt from repoPkg (for example "test/docsync") under the
// runfiles workspace root. Outside Bazel it is filepath.Dir(callerFile).
func CallerDir(callerFile, repoPkg string) string {
	if root := OverrideRoot(); root != "" {
		return filepath.Join(root, filepath.FromSlash(repoPkg))
	}
	return filepath.Dir(callerFile)
}

// Runfile resolves a runfiles-relative path, the form produced by
// $(rlocationpath <label>) (for example "_main/cmd/bd/bd_/bd"), to an
// absolute path. Outside Bazel, or when the path is absent from the
// runfiles tree, it returns an error.
func Runfile(path string) (string, error) {
	rf := runfilesDir()
	if rf == "" {
		return "", fmt.Errorf("runfile %q: not running under bazel", path)
	}
	full := filepath.Join(rf, filepath.FromSlash(path))
	if _, err := os.Stat(full); err != nil {
		return "", fmt.Errorf("runfile %q: %w", path, err)
	}
	return full, nil
}

// shardEnvVars are the Bazel sharding protocol variables. A helper process
// that re-executes a test binary must not inherit them: the child would apply
// the parent's shard filter to its own test selection and could touch the
// parent's shard status file.
var shardEnvVars = []string{
	"TEST_SHARD_INDEX",
	"TEST_TOTAL_SHARDS",
	"TEST_SHARD_STATUS_FILE",
}

// ShardFreeEnv returns env (in os.Environ form) without the Bazel sharding
// variables. Under plain `go test` those variables are absent, so the result
// equals the input.
func ShardFreeEnv(env []string) []string {
	out := make([]string, 0, len(env))
outer:
	for _, kv := range env {
		for _, name := range shardEnvVars {
			if strings.HasPrefix(kv, name+"=") {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}
