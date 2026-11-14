//go:build !windows

package workspacegate

import (
	"os"
	"path/filepath"
	"testing"
)

// Outside Windows gate-parent canonicalization is exactly
// filepath.EvalSymlinks: same result, same error, for plain, symlinked,
// and missing parents.
func TestCanonicalParentIsEvalSymlinksOffWindows(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		dir,
		real,
		link,
		filepath.Join(link, "missing"),
		filepath.Join(dir, "missing", "deeper"),
	} {
		want, wantErr := filepath.EvalSymlinks(p)
		got, err := canonicalParent(p)
		if got != want || (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
			t.Errorf("canonicalParent(%s) = %q, %v; want EvalSymlinks' %q, %v", p, got, err, want, wantErr)
		}
	}
}
