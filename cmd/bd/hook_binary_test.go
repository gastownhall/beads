package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestHookSubprocessBinary pins how the git-hook lanes pick their `bd export`
// and `bd import` child (GH#5922): the running bd, symlinks resolved, and
// plain `bd` from PATH only when the running binary is a test binary or
// cannot be discovered.
func TestHookSubprocessBinary(t *testing.T) {
	// Under go test and Bazel (cmd/bd's test binary is bd_test, which no
	// ".test" name check sees) the lanes must find the stub bd hook tests put
	// on PATH, never launch this test binary again.
	t.Run("test executable selects bd from PATH", func(t *testing.T) {
		if got := hookSubprocessBinary(); got != "bd" {
			t.Fatalf("hookSubprocessBinary() = %q inside a test binary, want %q", got, "bd")
		}
	})

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dir, "install dir", "bd")
	if err := os.MkdirAll(filepath.Dir(installed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	notTest := func(string) bool { return false }

	t.Run("running binary", func(t *testing.T) {
		if got := pickHookSubprocessBinary(installed, nil, notTest); got != installed {
			t.Fatalf("pick = %q, want the running binary %q", got, installed)
		}
	})
	t.Run("symlink resolves to its target", func(t *testing.T) {
		link := filepath.Join(dir, "link-bd")
		if err := os.Symlink(installed, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := pickHookSubprocessBinary(link, nil, notTest); got != installed {
			t.Fatalf("pick = %q, want the symlink's target %q", got, installed)
		}
	})
	t.Run("unresolvable path is kept, not traded for PATH", func(t *testing.T) {
		gone := filepath.Join(dir, "deleted", "bd")
		if got := pickHookSubprocessBinary(gone, nil, notTest); got != gone {
			t.Fatalf("pick = %q, want the running binary's path %q", got, gone)
		}
	})
	t.Run("undiscoverable binary falls back to bd", func(t *testing.T) {
		if got := pickHookSubprocessBinary("", errors.New("no /proc"), notTest); got != "bd" {
			t.Fatalf("pick = %q, want %q", got, "bd")
		}
	})
	t.Run("test binary falls back to bd", func(t *testing.T) {
		var checked string
		isTest := func(exe string) bool { checked = exe; return true }
		if got := pickHookSubprocessBinary(installed, nil, isTest); got != "bd" {
			t.Fatalf("pick = %q, want %q", got, "bd")
		}
		if checked != installed {
			t.Fatalf("test-binary check saw %q, want the resolved path %q", checked, installed)
		}
	})
}

// TestHooksPATHBinaryWarning pins the diagnostic hook installation prints
// when the bd git hooks will find on PATH is another file, or none (GH#5922).
// It compares file identity only; no candidate is ever executed.
func TestHooksPATHBinaryWarning(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(dir, "self", "bd")
	other := filepath.Join(dir, "stale", "bd")
	for _, p := range []string{self, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	selects := func(path string, err error) func(string) (string, error) {
		return func(name string) (string, error) {
			if name != "bd" {
				t.Fatalf("lookPath(%q), want bd", name)
			}
			return path, err
		}
	}

	t.Run("another file warns with both paths", func(t *testing.T) {
		got := hookPATHBinaryWarning(self, selects(other, nil))
		// File identity cannot tell a stale copy from a wrapper that runs
		// this bd, so the advice hangs on the version check, never an
		// unconditional "remove that bd".
		for _, want := range []string{"a different file from this bd", other, self, "version " + Version, "bd version", "If it shows an older version"} {
			if !strings.Contains(got, want) {
				t.Errorf("warning lacks %q:\n%s", want, got)
			}
		}
		if n := strings.Count(got, "⚠"); n != 1 {
			t.Errorf("warning has %d ⚠ lines, want one diagnostic:\n%s", n, got)
		}
	})
	t.Run("launcher installed beside this bd is quiet", func(t *testing.T) {
		// npm's layout: <prefix>/bin/bd links to the package's bin/bd.js, a
		// Node launcher that starts the native bd next to it.
		pkgBin := filepath.Join(dir, "npm", "lib", "node_modules", "@beads", "bd", "bin")
		native := filepath.Join(pkgBin, "bd")
		link := filepath.Join(dir, "npm", "bin", "bd")
		for _, p := range []string{native, link} {
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []string{native, filepath.Join(pkgBin, "bd.js")} {
			if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "@beads", "bd", "bin", "bd.js"), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := hookPATHBinaryWarning(native, selects(link, nil)); got != "" {
			t.Errorf("warning for npm's launcher beside this bd:\n%s", got)
		}
	})
	t.Run("older bd beside this bd warns", func(t *testing.T) {
		// Same directory, but another name: a kept older install, not a
		// launcher of this one.
		old := filepath.Join(filepath.Dir(self), "bd-1.0.5")
		if err := os.WriteFile(old, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "home-bin", "bd")
		if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(old, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := hookPATHBinaryWarning(self, selects(link, nil)); !strings.Contains(got, link) {
			t.Errorf("warning for an older bd beside this one lacks %q:\n%s", link, got)
		}
	})
	t.Run("plain bd beside a differently named bd warns", func(t *testing.T) {
		tools := filepath.Join(dir, "tools")
		newer, plain := filepath.Join(tools, "bd-new"), filepath.Join(tools, "bd")
		if err := os.MkdirAll(tools, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{newer, plain} {
			if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if got := hookPATHBinaryWarning(newer, selects(plain, nil)); !strings.Contains(got, plain) {
			t.Errorf("warning for a plain bd beside %s lacks %q:\n%s", newer, plain, got)
		}
	})
	t.Run("link to a bd in another directory warns", func(t *testing.T) {
		link := filepath.Join(dir, "elsewhere", "bd")
		if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := hookPATHBinaryWarning(self, selects(link, nil)); !strings.Contains(got, link) {
			t.Errorf("warning for a link to another directory's bd lacks %q:\n%s", link, got)
		}
	})
	t.Run("no bd on PATH warns with this bd's path", func(t *testing.T) {
		got := hookPATHBinaryWarning(self, selects("", exec.ErrNotFound))
		for _, want := range []string{"no bd", self} {
			if !strings.Contains(got, want) {
				t.Errorf("warning lacks %q:\n%s", want, got)
			}
		}
	})
	t.Run("same file is quiet", func(t *testing.T) {
		if got := hookPATHBinaryWarning(self, selects(self, nil)); got != "" {
			t.Errorf("warning for the same file:\n%s", got)
		}
	})
	t.Run("symlink to this bd is quiet", func(t *testing.T) {
		link := filepath.Join(dir, "bin", "bd")
		if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(self, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := hookPATHBinaryWarning(self, selects(link, nil)); got != "" {
			t.Errorf("warning for a symlink to this bd:\n%s", got)
		}
	})
	t.Run("hard link to this bd is quiet", func(t *testing.T) {
		link := filepath.Join(dir, "hard", "bd")
		if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(self, link); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		if got := hookPATHBinaryWarning(self, selects(link, nil)); got != "" {
			t.Errorf("warning for a hard link to this bd:\n%s", got)
		}
	})
	t.Run("relative PATH entry counts as found", func(t *testing.T) {
		// exec.LookPath reports a match through a relative PATH entry as
		// ErrDot; the hook shell still runs it, so it is the selection.
		if got := hookPATHBinaryWarning(self, selects(self, exec.ErrDot)); got != "" {
			t.Errorf("warning for this bd found through a relative entry:\n%s", got)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if rel, err := filepath.Rel(cwd, self); err == nil && !filepath.IsAbs(rel) {
			if got := hookPATHBinaryWarning(self, selects(rel, exec.ErrDot)); got != "" {
				t.Errorf("warning for this bd found as relative %q:\n%s", rel, got)
			}
		} else {
			t.Logf("no relative path from %s to %s; relative self case not exercised", cwd, self)
		}
		if got := hookPATHBinaryWarning(self, selects(other, exec.ErrDot)); !strings.Contains(got, other) {
			t.Errorf("warning for another bd found through a relative entry lacks %q:\n%s", other, got)
		}
	})
	t.Run("unknown self identity is quiet", func(t *testing.T) {
		if got := hookPATHBinaryWarning(filepath.Join(dir, "gone", "bd"), selects(other, nil)); got != "" {
			t.Errorf("warning without a usable self path:\n%s", got)
		}
	})
}

// TestHooksInstallWarnsWhenPATHSelectsAnotherBD runs the real `bd hooks
// install --json`, started by absolute path, under three PATHs: a decoy bd
// first, no bd at all, and a symlink to this same bd; then once more with
// the decoy first and --quiet. Installation succeeds and stdout stays one
// JSON document each time; stderr carries one diagnostic exactly when PATH
// selects another file or none and the install is not quiet, and the decoy
// never runs.
func TestHooksInstallWarnsWhenPATHSelectsAnotherBD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script decoy bd is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	bd := buildBDForTest(t)
	if !filepath.IsAbs(bd) {
		t.Fatalf("bd must be started by absolute path, got %q", bd)
	}
	self, err := filepath.EvalSymlinks(bd)
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	decoyDir := t.TempDir()
	marker := filepath.Join(decoyDir, "decoy-ran")
	decoy := filepath.Join(decoyDir, "bd")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(decoy, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	aliasDir := t.TempDir()
	if err := os.Symlink(bd, filepath.Join(aliasDir, "bd")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// git, and the sh it needs, without any bd: the bin dirs PATH needs for
	// git, minus every directory holding a bd.
	var baseDirs []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "bd")); err == nil {
			continue
		}
		baseDirs = append(baseDirs, d)
	}
	if !slices.Contains(baseDirs, filepath.Dir(gitPath)) {
		t.Skipf("git's directory %s also holds a bd", filepath.Dir(gitPath))
	}
	basePATH := strings.Join(baseDirs, string(os.PathListSeparator))

	for _, tc := range []struct {
		name      string
		path      string
		args      []string
		wantWarn  []string
		wantQuiet bool
	}{
		{name: "decoy first", path: decoyDir + string(os.PathListSeparator) + basePATH, wantWarn: []string{decoy, self}},
		{name: "no bd", path: basePATH, wantWarn: []string{"no bd", self}},
		{name: "symlink to this bd", path: aliasDir + string(os.PathListSeparator) + basePATH, wantQuiet: true},
		// -q is "errors only": the diagnostic is advice, not an error.
		{name: "decoy first with --quiet", path: decoyDir + string(os.PathListSeparator) + basePATH, args: []string{"--quiet"}, wantQuiet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			cmd := gitCommand("init", "-q")
			cmd.Dir = repo
			cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}

			cmd = exec.Command(bd, append([]string{"hooks", "install", "--json"}, tc.args...)...)
			cmd.Dir = repo
			var env []string
			for _, e := range testutil.WithoutGitRepoLocationEnv(os.Environ()) {
				if strings.HasPrefix(e, "PATH=") || strings.HasPrefix(e, "BEADS_") || strings.HasPrefix(e, "BD_") {
					continue
				}
				env = append(env, e)
			}
			cmd.Env = append(env, "PATH="+tc.path, "HOME="+repo, "BD_DISABLE_METRICS=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("bd hooks install --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}

			var result map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
			}
			if result["success"] != true {
				t.Errorf("install result = %v, want success", result)
			}
			if _, err := os.Stat(filepath.Join(repo, ".git", "hooks", "pre-commit")); err != nil {
				t.Errorf("pre-commit hook not installed: %v", err)
			}
			if data, err := os.ReadFile(marker); err == nil {
				t.Errorf("decoy bd ran during install: %q", data)
			}

			got := stderr.String()
			if tc.wantQuiet {
				if strings.Contains(got, "⚠") {
					t.Errorf("stderr has a diagnostic for PATH's own bd:\n%s", got)
				}
				return
			}
			if n := strings.Count(got, "Git hooks run"); n != 1 {
				t.Errorf("stderr has %d PATH diagnostics, want 1:\n%s", n, got)
			}
			for _, want := range tc.wantWarn {
				if !strings.Contains(got, want) {
					t.Errorf("stderr lacks %q:\n%s", want, got)
				}
			}
		})
	}
}
