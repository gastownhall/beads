package scripts_test

// Helpers shared by the package's test files, kept apart so that each go_test
// in scripts/BUILD.bazel compiles only the files whose tests it runs (and so
// declares only the data those tests read). Helpers only: every target
// compiles this file, so a test here would run in all of them.

import (
	"errors"
	"go/ast"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

func sourceRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// Under Bazel the caller path is workspace-relative; CallerDir rebuilds it
	// under the runfiles root, which holds the files the go_test declares.
	return filepath.Dir(bazeltest.CallerDir(file, "scripts"))
}

func msysPath(path string) string {
	path = filepath.Clean(path)
	path = filepath.ToSlash(path)
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' {
		return "/" + strings.ToLower(path[:1]) + path[2:]
	}
	return path
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// bazelPolicyRoot returns the repository root holding the Bazel policy files.
// Under `bazel test` those files are declared as data (//:bazel_policy_files)
// and resolved from the runfiles tree; under `go test` the source checkout is
// used directly.
func bazelPolicyRoot(t *testing.T) string {
	t.Helper()
	if srcdir := os.Getenv("TEST_SRCDIR"); srcdir != "" {
		workspace := os.Getenv("TEST_WORKSPACE")
		if workspace == "" {
			workspace = "_main"
		}
		return filepath.Join(srcdir, workspace)
	}
	return sourceRepoRoot(t)
}

func readPolicyFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

const (
	forkCacheEndpoint = "grpc" + "s://rbe-cache.ops.gascity.com:8443"
	forkCacheInstance = "oss"
	// zstd cache transfers: only the anonymous fork cache can advertise a
	// compressor, so only fork-cache may ask for one, and only while
	// write-bazelrc.sh's cache-zstd-probe.sh finds it advertised (a probe,
	// not a repository variable: fork pull_request runs see no vars).
	// Trusted remote-exec and rbe-fork never: their schedulers advertise
	// none, and Bazel then refuses the remote.
	forkCacheZstdLine = "build:fork-cache --remote_cache_compression"
)

func gitRepoAvailable(root string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	return exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Run() == nil
}

// repoFiles lists the repository's tracked files under root, repo-relative and
// slash-separated, each a regular file (or, in a local runfiles tree, a
// symlink to one). Under `go test` in a git checkout that is `git ls-files`
// less what the working tree no longer holds; under Bazel it is the runfiles
// tree, whose repository part is what the go_test declares: //:repo_files
// (every tracked file outside .bazelignore, tools/bazel/go_srcs.py) or one of
// its partitions; elsewhere every file under root. A listing of fewer than
// minFiles files fails the test: an empty or partial tree would pass every
// scan.
func repoFiles(t *testing.T, root string, minFiles int) []string {
	t.Helper()
	var files []string
	if bazeltest.IsBazel() || !gitRepoAvailable(root) {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !isFileOrFileLink(path, d) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	} else {
		out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
		if err != nil {
			t.Fatalf("git ls-files: %v", err)
		}
		for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil || !info.Mode().IsRegular() {
				continue // deleted in the worktree, submodule, or symlink
			}
			files = append(files, rel)
		}
	}
	if len(files) < minFiles {
		t.Fatalf("found only %d repository files under %s; the listing is broken", len(files), root)
	}
	return files
}

// isFileOrFileLink reports whether a WalkDir entry is a regular file or a
// symlink to one. Bazel's local runfiles trees are symlink forests, so a walk
// that skipped symlinks would see no file there; a symlink to a directory
// (Bazel's bazel-* convenience links in a checkout) is never followed.
func isFileOrFileLink(path string, d os.DirEntry) bool {
	if d.Type().IsRegular() {
		return true
	}
	if d.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// bazelRuleBlock returns the text of the top-level rule named name in a
// BUILD file, or "" if there is none.
func bazelRuleBlock(build, name string) string {
	i := strings.Index(build, "\n    name = \""+name+"\",\n")
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(build[:i], "\n") + 1
	end := strings.Index(build[i:], "\n)\n")
	if end < 0 {
		return build[start:]
	}
	return build[start : i+end+3]
}

func exits(body *ast.BlockStmt) bool {
	for _, st := range body.List {
		switch st := st.(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.BranchStmt:
			if st.Tok == token.CONTINUE {
				return true
			}
		}
	}
	return false
}

const doltSQLServerImage = "dolthub/dolt-sql-server:2.2.0"
