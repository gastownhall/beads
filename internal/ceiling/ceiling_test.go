package ceiling

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// walk mimics a discovery loop: every directory from start upward that the
// bound lets it examine.
func walk(start string) []string {
	b := For(start)
	var seen []string
	for dir := start; ; dir = filepath.Dir(dir) {
		if b.Excludes(dir) {
			break
		}
		seen = append(seen, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return seen
}

func TestUnsetExaminesEveryAncestor(t *testing.T) {
	t.Setenv(EnvVar, "")
	root := t.TempDir()
	start := filepath.Join(root, "a", "b")
	if For(start) != nil {
		t.Fatal("For must return nil when the variable is unset")
	}
	got := walk(start)
	if !slices.Contains(got, root) || got[len(got)-1] != filepath.Dir(got[len(got)-1]) {
		t.Fatalf("unset ceiling must reach the filesystem root, got %v", got)
	}
}

func TestCeilingStopsBelowItself(t *testing.T) {
	root := t.TempDir()
	start := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar, strings.Join([]string{"relative/ignored", "", root}, string(os.PathListSeparator)))
	got := walk(start)
	want := []string{start, filepath.Join(root, "a")}
	if !slices.Equal(got, want) {
		t.Fatalf("walk = %v, want %v", got, want)
	}
}

func TestStartAtCeilingIsExaminedButNothingAbove(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvVar, root)
	if got := walk(root); !slices.Equal(got, []string{root}) {
		t.Fatalf("walk = %v, want only the start", got)
	}
}

func TestUnrelatedCeilingHasNoEffect(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, "other")
	start := filepath.Join(root, "a")
	t.Setenv(EnvVar, other)
	if For(start) != nil {
		t.Fatal("a ceiling that does not contain the start must not bound it")
	}
}

func TestCeilingMatchesThroughSymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "realDir")
	start := filepath.Join(realDir, "a")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv(EnvVar, link)
	got := walk(start)
	if !slices.Equal(got, []string{start}) {
		t.Fatalf("walk = %v, want only %s (ceiling named through a symlink)", got, start)
	}
}

func TestGitEnv(t *testing.T) {
	env := []string{"A=1", "GIT_CEILING_DIRECTORIES=/old"}
	t.Setenv(EnvVar, "")
	if got := GitEnv(env); !slices.Equal(got, env) {
		t.Fatalf("unset: GitEnv = %v, want unchanged", got)
	}
	t.Setenv(EnvVar, "/x")
	want := []string{"A=1", "GIT_CEILING_DIRECTORIES=/x"}
	if got := GitEnv(env); !slices.Equal(got, want) {
		t.Fatalf("GitEnv = %v, want %v", got, want)
	}
}
