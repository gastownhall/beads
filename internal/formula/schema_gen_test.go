package formula_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/formula/schemagen"
)

// TestSchemaGenIsCurrent re-runs the schemagen walker over types.go
// in-memory and asserts the result byte-equals the committed schema_gen.go.
// Failure means someone changed types.go without running `go generate`.
func TestSchemaGenIsCurrent(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(thisFile)
	typesPath := filepath.Join(pkgDir, "types.go")
	committedPath := filepath.Join(pkgDir, "schema_gen.go")

	want, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("read %s: %v", committedPath, err)
	}

	got, err := schemagen.Generate(typesPath)
	if err != nil {
		t.Fatalf("schemagen.Generate: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("schema_gen.go is stale relative to types.go.\n"+
			"Run `go generate ./...` from %s and commit the result.\n"+
			"first diff at byte %d", pkgDir, firstDiff(got, want))
	}
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
