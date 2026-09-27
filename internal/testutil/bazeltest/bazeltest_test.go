package bazeltest

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

const probeRel = "internal/testutil/bazeltest/testdata/probe.txt"

func TestIsBazelMatchesEnvironment(t *testing.T) {
	if got, want := IsBazel(), os.Getenv("TEST_SRCDIR") != ""; got != want {
		t.Fatalf("IsBazel() = %v, want %v", got, want)
	}
}

func TestRepoRootHoldsDeclaredData(t *testing.T) {
	root := RepoRoot(t)
	if !filepath.IsAbs(root) {
		t.Fatalf("RepoRoot() = %q, want an absolute path", root)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(probeRel))); err != nil {
		t.Fatalf("probe not found under RepoRoot %s: %v", root, err)
	}
	if !IsBazel() {
		// Plain go test: the root is the module root.
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
			t.Fatalf("RepoRoot %s has no go.mod under plain go test: %v", root, err)
		}
	}
}

func TestRepoRootHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RepoRootEnv, dir)
	if got := RepoRoot(t); got != dir {
		t.Fatalf("RepoRoot() = %q, want override %q", got, dir)
	}
	if got := OverrideRoot(); got != dir {
		t.Fatalf("OverrideRoot() = %q, want override %q", got, dir)
	}
}

func TestOverrideRootIgnoresRootWithoutGoMod(t *testing.T) {
	t.Setenv(RepoRootEnv, "")
	want := OverrideRoot()
	t.Setenv(RepoRootEnv, t.TempDir())
	if got := OverrideRoot(); got != want {
		t.Fatalf("OverrideRoot() = %q with a go.mod-less override, want %q (override ignored)", got, want)
	}
}

func TestOverrideRootIsEmptyOnlyOutsideBazel(t *testing.T) {
	t.Setenv(RepoRootEnv, "")
	got := OverrideRoot()
	if IsBazel() {
		if got != RepoRoot(t) {
			t.Fatalf("OverrideRoot() = %q under bazel, want RepoRoot %q", got, RepoRoot(t))
		}
		return
	}
	if got != "" {
		t.Fatalf("OverrideRoot() = %q under plain go test, want empty", got)
	}
}

func TestCallerDirLocatesThisPackage(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := CallerDir(file, "internal/testutil/bazeltest")
	if _, err := os.Stat(filepath.Join(dir, "testdata", "probe.txt")); err != nil {
		t.Fatalf("CallerDir() = %q does not hold testdata/probe.txt: %v", dir, err)
	}
}

func TestRunfileResolvesOnlyUnderBazel(t *testing.T) {
	ws := os.Getenv("TEST_WORKSPACE")
	if ws == "" {
		ws = "_main"
	}
	path, err := Runfile(ws + "/" + probeRel)
	if !IsBazel() {
		if err == nil {
			t.Fatalf("Runfile() = %q outside bazel, want an error", path)
		}
		return
	}
	if err != nil {
		t.Fatalf("Runfile(): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Runfile() = %q does not exist: %v", path, err)
	}
	if _, err := Runfile(ws + "/does/not/exist"); err == nil {
		t.Fatal("Runfile() of a missing path succeeded")
	}
}

func TestShardFreeEnvStripsShardingVariables(t *testing.T) {
	in := []string{
		"PATH=/bin",
		"TEST_SHARD_INDEX=1",
		"TEST_TOTAL_SHARDS=4",
		"TEST_SHARD_STATUS_FILE=/tmp/x",
		"TEST_SHARD_INDEX_NOT=kept",
		"HOME=/home/x",
	}
	want := []string{"PATH=/bin", "TEST_SHARD_INDEX_NOT=kept", "HOME=/home/x"}
	if got := ShardFreeEnv(in); !slices.Equal(got, want) {
		t.Fatalf("ShardFreeEnv() = %q, want %q", got, want)
	}
}
