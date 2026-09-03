package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// suiteTempRoot is the TestMain-owned temp directory that t.TempDir must
// land under so SweepOrphanedTestServers can reap leaked sql-servers.
var suiteTempRoot string

func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	if suiteTempRoot == "" {
		t.Fatal("TestMain did not pin suiteTempRoot; leaked dolt sql-server processes cannot be swept")
	}
	tmp := t.TempDir()
	if !pathUnderRoot(tmp, suiteTempRoot) {
		t.Fatalf("t.TempDir() %q is not under suiteTempRoot %q", tmp, suiteTempRoot)
	}
}

func pathUnderRoot(dir, root string) bool {
	dir = evalOrSelf(dir)
	root = evalOrSelf(root)
	if dir == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(dir, strings.TrimRight(root, sep)+sep)
}

func evalOrSelf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
